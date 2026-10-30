// Package cli 是 adag 的会话侧命令实现：把「产品装配 → 引擎 → SQLite → 事件」接成
// 可脚本化的 CLI（开发计划 M1-G）。
//
// 它不认识官方插件包：插件脚本由 cmd/adag 注入（边界检查禁止 internal/ import official/）。
package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/adapters"
	"github.com/adag/ada-go/internal/adapters/sqlite"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/coreproto"
	"github.com/adag/ada-go/internal/devfake"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/engine"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/discovery"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/plugins/provider"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/internal/toolkit"
)

// Config 是打开一次 CLI 会话的全部输入。
type Config struct {
	Workspace string
	DataDir   string
	Product   string
	Provider  string
	Model     string
	BaseURL   string
	// AllowHosts 追加出网 allowlist；为空时只允许 BaseURL 的 host（或 api.openai.com）。
	AllowHosts []string
	// Fake 使用确定性回显模型，不需要 API key。
	Fake bool
	// ThreadID 非空时打开后直接切到该线程。
	ThreadID string
	// Interactive 为 true 时把审批/提问转到终端（y/n）；false 时按无 UI 失败关闭。
	Interactive bool
	// HasUI 表示「这个会话背后有界面可以回答交互」：CLI 交互模式是终端，
	// Gateway 是网页里的交互托盘。它决定 ctx.ui.available 与 broker 的 HasUI，
	// 与 Interactive（是否在终端读 stdin）分开——Core 进程没有终端，但有网页。
	HasUI bool
	// Scripts 是官方插件包（由 cmd 注入）。
	Scripts []host.Script
	// FrontendScripts 是前端插件（M4）：产品声明允许它们缺席本侧装配，
	// 真实渲染在 Gateway 侧；Session 只用它们的 id 集合。
	FrontendScripts []host.Script
	// Settings 是按插件 id 的设置。
	Settings map[string]map[string]any
	// ModelClient 覆盖模型（测试注入）；nil 时按 Fake/provider 选择。
	ModelClient ports.ModelClient

	In  io.Reader
	Out io.Writer
	Err io.Writer
	// EventsPath 非空时把全部领域事件写成 JSONL。
	EventsPath string
	// SecretsPath 是 secrets.toml 位置；空时取 ~/.adago/secrets.toml。
	SecretsPath string
	// Log 接收插件日志；nil 时丢弃。
	Log func(level, msg string, fields map[string]any)
	// Debug 开启出网调试：把每次 provider/ctx.fetch 请求与响应摘要（密钥已脱敏）
	// 写到 Err，用于定位端点、请求体与状态码问题。
	Debug bool
}

// Session 是已装配的 Core 侧 CLI 会话。
type Session struct {
	cfg      Config
	store    *sqlite.Store
	asm      *app.Assembly
	emitter  *coreproto.Emitter
	broker   *coreproto.Broker
	executor *toolkit.Executor
	deps     engine.Deps
	ids      *adapters.ULIDGen
	clock    adapters.SystemClock
	model    ports.ModelClient
	modelErr error
	secrets  map[string]string

	// M5 检查点账目：changes 是工具执行的内存账本（回执 chk_ id、写串行、
	// 热重载跨代复用）；changeStore 是数据目录 changes/ 下的持久化账目
	// （文件备份 + manifest，change.list / change.revert 的真源）。
	changes     *toolkit.ChangeQueue
	changeStore *changeStore

	// M3 插件平台：随包插件 base（Open 时冻结，热重载的合并基准）、产品声明、
	// 装配期宿主依赖（app.Deps 原样复用）、第三方发现快照与失败记录。
	// baseScripts/cfg 在 Open 之后只读；discovered/failedPlugins 变更持 s.mu。
	baseScripts []host.Script
	product     *coreapp.Product
	appDeps     app.Deps
	// httpBridge 是插件出网桥；供应商配置变更时动态追加 allowlist。
	httpBridge httpBridge
	// debug 与 httpBridge 共用同一序号，--debug 时输出每次出网请求。
	debug *host.DebugLog
	// hostBase 是装配期宿主上下文模板（CallAction 用）；frontendScripts/IDs 是
	// M4 前端插件的脚本与「产品声明允许缺席」集合。
	hostBase        host.Context
	frontendScripts []host.Script
	frontendIDs     map[string]bool
	discovered      []discovery.Plugin
	failedPlugins   map[string]string
	reloadMu        sync.Mutex // 串行化热重载（plugin.reload / plugin.setEnabled）
	// 供应商配置（管理页）：文件在数据目录，改动后直接更新当前装配的 Registry。
	providerStore   *provider.Store
	providersMu     sync.Mutex
	providerConfigs []provider.Config

	mu           sync.Mutex
	threadID     string
	events       io.WriteCloser
	eventsBuf    *bufio.Writer
	unsub        []func()
	policy       *coreapp.Policies
	workspaceMod *ports.ModelRef
	threadModels map[string]ports.ModelRef

	// steering 队列按线程隔离：mutex 保护 map 与每个线程的活跃轮次计数。
	steerMu     sync.Mutex
	steerQueues map[string][]domain.Message
	turnActive  map[string]int

	in *bufio.Reader
}

// PolicyUpdate 是 workspace.updatePolicy 的合并输入。
// 字段为 nil 表示保持当前值；非 nil 时覆盖并校验。
type PolicyUpdate struct {
	ToolTimeoutMS   *int
	TurnTimeoutMS   *int
	MaxTurnTokens   *int
	LoopRepeatLimit *int
	LoopAbortLimit  *int
	ParallelTools   *int
}

// threadCtxKey 把「本次工具调用属于哪个线程」传给 stateOp。
// engine 的工具执行使用轮次 ctx，宿主 State 闭包能拿到它；这比 Session 上的
// 可变 currentThread 更适合 Core 的多线程 RPC。
type threadCtxKey struct{}

// runCtxKey 把轮次 id 带进工具执行 ctx：检查点账目（M5）用它记录归属。
type runCtxKey struct{}

func withThread(ctx context.Context, threadID string) context.Context {
	return context.WithValue(ctx, threadCtxKey{}, threadID)
}

func threadFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(threadCtxKey{}).(string)
	return id
}

func withRun(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, runCtxKey{}, runID)
}

func runFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(runCtxKey{}).(string)
	return id
}

// Open 打开工作区、装配产品并接好引擎端口。
func Open(ctx context.Context, cfg Config) (*Session, error) {
	if strings.TrimSpace(cfg.Workspace) == "" {
		return nil, errors.New("cli: 需要工作区目录")
	}
	abs, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	cfg.Workspace = abs
	if cfg.In == nil {
		cfg.In = os.Stdin
	}
	if cfg.Out == nil {
		cfg.Out = os.Stdout
	}
	if cfg.Err == nil {
		cfg.Err = os.Stderr
	}
	var debugLog *host.DebugLog
	if cfg.Debug {
		debugLog = host.NewDebugLog(cfg.Err)
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir, err = sqlite.DefaultDataDir(abs)
		if err != nil {
			return nil, err
		}
	}
	cfg.DataDir = dataDir

	clock := adapters.SystemClock{}
	ids := adapters.NewULIDGen()
	secrets, err := loadSecrets(cfg.SecretsPath)
	if err != nil {
		return nil, err
	}
	store, err := sqlite.OpenCore(ctx, sqlite.Config{
		Workspace: abs,
		DataDir:   dataDir,
		Now:       clock.Now,
		NewID:     ids.NewULID,
	})
	if err != nil {
		return nil, err
	}
	s := &Session{
		cfg: cfg, store: store, ids: ids, clock: clock, secrets: secrets,
		providerStore: provider.NewStore(filepath.Join(dataDir, "providers.json")),
		steerQueues:   map[string][]domain.Message{},
		turnActive:    map[string]int{},
		in:            bufio.NewReader(cfg.In),
		debug:         debugLog,
	}
	cleanup := func(e error) (*Session, error) {
		_ = s.Close(context.Background())
		return nil, e
	}
	if cfg.ThreadID != "" {
		if _, err := store.GetThread(ctx, cfg.ThreadID); err != nil {
			return cleanup(err)
		}
		s.threadID = cfg.ThreadID
	}

	httpBridge, err := buildHTTPBridge(cfg, debugLog)
	if err != nil {
		return cleanup(err)
	}
	s.httpBridge = httpBridge
	hc := host.Context{
		Workspace: abs,
		Secret:    s.secret,
		Log:       cfg.Log,
		HTTP:      httpBridge,
	}
	// ctx.ui：把插件的 confirm/select/input 接到交互桥上（05 §6.3 UIContext）。
	// 只有确实有界面时才装上——nil 时 ctx.ui.available=false，ask 类工具按失败关闭。
	if cfg.HasUI {
		hc.UI = s.uiAsk
	}
	hc.State = s.stateOp
	// 给插件的只读技能通道（6.7）：官方的技能搜索工具用它做匹配；
	// 快照随每次装配更新，调用时读当前值。
	hc.Skills = s.skillsMetaSnapshot
	// 供应商管理（model.admin）：目录只读、不含密钥；切换复用 model.set 的解析。
	hc.Providers = s.providerCatalog
	hc.SelectModel = s.selectModelFromPlugin
	hc.SaveProvider = s.saveProviderFromPlugin
	hc.RemoveProvider = s.removeProviderFromPlugin
	hc.FetchProviderModels = s.fetchModelsFromPlugin
	// M5 检查点：宿主 fs 写入前把被覆盖文件备份进持久化账目（changes/）。
	s.changes = toolkit.NewChangeQueue()
	s.changeStore = newChangeStore(dataDir)
	hc.Checkpoint = s.checkpointWrite
	s.hostBase = hc
	// M4 前端插件：产品声明里允许出现、但由 Gateway 渲染桥装配的 id 集合。
	frontend, ferr := sessionFrontendScripts(cfg)
	if ferr != nil {
		httpBridge.Close()
		return cleanup(ferr)
	}
	s.frontendScripts = frontend
	s.frontendIDs = map[string]bool{}
	for _, sc := range frontend {
		if sc.Manifest != nil {
			s.frontendIDs[sc.Manifest.ID] = true
		}
	}
	s.appDeps = app.Deps{
		Context:     hc,
		Secrets:     func(name string) bool { _, ok := s.secret(name); return ok },
		Settings:    cfg.Settings,
		BaseURL:     cfg.BaseURL,
		FrontendIDs: s.frontendIDs,
	}
	// 随包插件 base：测试注入优先，否则嵌入官方包（经组合根转手，internal 不碰 official/）。
	base, err := sessionBaseScripts(cfg)
	if err != nil {
		httpBridge.Close()
		return cleanup(err)
	}
	s.baseScripts = base
	product, err := coreapp.LoadProduct(cfg.Product)
	if err != nil {
		httpBridge.Close()
		return cleanup(err)
	}
	s.product = product
	asm, err := s.assembleInitial(ctx)
	if err != nil {
		httpBridge.Close()
		return cleanup(err)
	}

	s.emitter = coreproto.NewEmitter(clock)
	broker, err := coreproto.NewBroker(coreproto.BrokerConfig{
		Sink:  s.emitter,
		Clock: clock,
		IDs:   ids,
		Store: interactionStore{st: store},
		HasUI: func(context.Context, string) bool { return cfg.HasUI },
	})
	if err != nil {
		return cleanup(err)
	}
	s.broker = broker

	// 引擎依赖里与装配无关的部分先固定；目录/模型相关子集由 installAssembly 填充
	//（热重载只重建后者，broker/emitter/store 全部复用）。
	s.deps = engine.Deps{
		Clock: clock, IDs: ids, Events: s.emitter, Session: store,
		Interactions: broker,
		RunStore:     runStore{st: store, model: s.ModelRefFor},
		Steer:        s,
	}
	if err := s.installAssembly(asm); err != nil {
		return cleanup(err)
	}

	if cfg.EventsPath != "" {
		f, err := os.Create(cfg.EventsPath)
		if err != nil {
			return cleanup(err)
		}
		s.events = f
		s.eventsBuf = bufio.NewWriter(f)
	}
	s.unsub = append(s.unsub, s.emitter.Subscribe(s.onEvent))
	if cfg.Interactive {
		s.unsub = append(s.unsub, s.emitter.Subscribe(s.onInteraction))
	}
	return s, nil
}

// Close 逆序释放：订阅 → 插件 → 存储 → 事件文件。
func (s *Session) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	asm := s.asm
	s.asm = nil
	s.mu.Unlock()
	for _, fn := range s.unsub {
		fn()
	}
	s.unsub = nil
	var errs []error
	if s.eventsBuf != nil {
		if err := s.eventsBuf.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.events != nil {
		if err := s.events.Close(); err != nil {
			errs = append(errs, err)
		}
		s.events = nil
	}
	if asm != nil {
		if err := asm.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if s.store != nil {
		if err := s.store.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		s.store = nil
	}
	return errors.Join(errs...)
}

// NewThread 新建线程并设为当前线程。
func (s *Session) NewThread(ctx context.Context, title string) (ports.Thread, error) {
	if strings.TrimSpace(title) == "" {
		title = "CLI 会话"
	}
	th, err := s.store.CreateThread(ctx, s.cfg.Workspace, title)
	if err != nil {
		return ports.Thread{}, err
	}
	s.setThread(th.ID)
	return th, nil
}

// UseThread 切到已有线程（校验存在）。
func (s *Session) UseThread(ctx context.Context, threadID string) error {
	if _, err := s.store.GetThread(ctx, threadID); err != nil {
		return err
	}
	s.setThread(threadID)
	return nil
}

// CurrentThread 返回当前线程 id（未建线程时为空）。
func (s *Session) CurrentThread() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID
}

// ReadLine 读一行输入（与审批提示共用同一个缓冲 reader，避免吞掉用户的下一行）。
func (s *Session) ReadLine() (string, error) {
	line, err := s.in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// Run 跑一轮对话并返回终态。
func (s *Session) Run(ctx context.Context, prompt string) (domain.TerminalReason, error) {
	s.mu.Lock()
	modelErr := s.modelErr
	s.mu.Unlock()
	if modelErr != nil {
		return domain.TerminalError, modelErr
	}
	if strings.TrimSpace(prompt) == "" {
		return domain.TerminalError, errors.New("cli: 输入为空")
	}
	if s.CurrentThread() == "" {
		title := prompt
		if len([]rune(title)) > 60 {
			title = string([]rune(title)[:60]) + "…"
		}
		if _, err := s.NewThread(ctx, title); err != nil {
			return domain.TerminalError, err
		}
	}
	runID, err := s.ids.NewRunID(ctx)
	if err != nil {
		return domain.TerminalError, err
	}
	return s.RunInThread(ctx, s.CurrentThread(), runID, prompt)
}

// RunInThread 在指定线程上跑一轮（不改变 CLI 的当前线程）。
//
// Core RPC 用它支持多线程；线程身份经 ctx 传给插件 State，避免并发轮次互相串台。
func (s *Session) RunInThread(ctx context.Context, threadID, runID, prompt string) (domain.TerminalReason, error) {
	return s.runInThread(ctx, threadID, runID, prompt, nil)
}

// RunInThreadReady 与 RunInThread 相同，但在登记活跃轮次之后关闭 ready，
// 让 corerpc 能等轮次真正可被 steer 之后再返回 turn.start（避免紧接的
// turn.steer 被误判成没有运行中轮次）。ready 为 nil 时与 RunInThread 等价。
func (s *Session) RunInThreadReady(ctx context.Context, threadID, runID, prompt string, ready chan<- struct{}) (domain.TerminalReason, error) {
	return s.runInThread(ctx, threadID, runID, prompt, ready)
}

func (s *Session) runInThread(ctx context.Context, threadID, runID, prompt string, ready chan<- struct{}) (domain.TerminalReason, error) {
	// 入参不合法时也关闭 ready，调用方不会永久等待。
	fail := func(err error) (domain.TerminalReason, error) {
		if ready != nil {
			close(ready)
		}
		return domain.TerminalError, err
	}
	if strings.TrimSpace(prompt) == "" {
		return fail(errors.New("cli: 输入为空"))
	}
	if threadID == "" || runID == "" {
		return fail(errors.New("cli: 轮次需要 threadId 与 runId"))
	}
	// 登记线程活跃轮次：steering 只在此期间可入队；结束时丢弃未排空消息并记日志。
	s.BeginTurn(threadID)
	defer s.EndTurn(threadID)
	if ready != nil {
		close(ready)
	}
	// deps/modelErr 会被热重载在锁内替换：轮次开始时取一次快照，
	// 整轮固定用同一份目录与模型（双代语义的在途侧）。
	s.mu.Lock()
	deps, modelErr := s.deps, s.modelErr
	s.mu.Unlock()
	if modelErr != nil {
		return domain.TerminalError, modelErr
	}
	runCtx := withRun(withThread(ctx, threadID), runID)
	return engine.Run(runCtx, deps, s.newTurnSpec(threadID, runID, prompt)), nil
}

// newTurnSpec 把产品策略与当前模型合成引擎输入。
//
// 策略与模型覆盖都在 Session 内以锁保护；这里只读一次快照，避免并发调用
// workspace.updatePolicy/model.set 时把半更新的值送进轮次。
func (s *Session) newTurnSpec(threadID, runID, prompt string) engine.TurnSpec {
	policies := s.Policies()
	spec := engine.TurnSpec{
		ThreadID: threadID,
		RunID:    runID,
		UserInput: domain.Message{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Type: domain.PartText, Text: prompt}},
		},
		Model:           s.ModelRefFor(threadID),
		LoopRepeatLimit: policies.LoopRepeatLimit,
		LoopAbortLimit:  policies.LoopAbortLimit,
		ParallelTools:   policies.ParallelTools,
		TurnTimeoutMS:   policies.TurnTimeoutMS,
		MaxTurnTokens:   policies.MaxTurnTokens,
	}
	if spec.LoopRepeatLimit <= 0 {
		spec.LoopRepeatLimit = 3
	}
	if spec.LoopAbortLimit <= spec.LoopRepeatLimit {
		spec.LoopAbortLimit = 5
	}
	if spec.ParallelTools <= 0 {
		spec.ParallelTools = 1
	}
	return spec
}

// NewRunID 生成轮次 id（Core RPC turn.start 用）。
func (s *Session) NewRunID(ctx context.Context) (string, error) { return s.ids.NewRunID(ctx) }

// BeginTurn 登记线程有一个运行中轮次（引用计数，可安全重复调用）。
//
// 通常由 runInThread 调用；直接驱动轮次的调用方也可以用它显式开启 steering
// 窗口，再成对调用 EndTurn。steering 只在线程有活跃轮次时可入队。
func (s *Session) BeginTurn(threadID string) {
	if threadID == "" {
		return
	}
	s.steerMu.Lock()
	if s.turnActive == nil {
		s.turnActive = map[string]int{}
	}
	s.turnActive[threadID]++
	s.steerMu.Unlock()
}

// EndTurn 注销一次活跃轮次登记；计数归零时丢弃仍未排空的中途消息并记录日志。
//
// 丢弃而不是留给下一轮，是为了让 turn.steer 的语义可预期：入队只对本次运行中
// 的轮次有效，轮次结束后的排队不会偷偷注入下一轮。丢弃必须有可见记录（不静默）。
func (s *Session) EndTurn(threadID string) {
	if threadID == "" {
		return
	}
	s.steerMu.Lock()
	if n := s.turnActive[threadID]; n > 1 {
		s.turnActive[threadID] = n - 1
		s.steerMu.Unlock()
		return
	}
	delete(s.turnActive, threadID)
	pending := s.steerQueues[threadID]
	delete(s.steerQueues, threadID)
	s.steerMu.Unlock()

	if len(pending) == 0 {
		return
	}
	msg := fmt.Sprintf("cli: 线程 %s 的轮次已结束，丢弃 %d 条未排空的中途消息", threadID, len(pending))
	if s.cfg.Log != nil {
		s.cfg.Log("warn", msg, map[string]any{"threadId": threadID, "dropped": len(pending)})
		return
	}
	out := s.cfg.Err
	if out == nil {
		out = os.Stderr
	}
	fmt.Fprintln(out, msg)
}

// EnqueueSteering 把一条中途消息排入线程队列，返回其位置（从 1 开始）。
//
// 只有线程存在运行中轮次时才接受；否则返回 core_unavailable，而不是静默收下
// 再在轮次结束时丢掉。
func (s *Session) EnqueueSteering(threadID, text string) (int, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("cli: 中途消息需要 threadId")
	}
	if strings.TrimSpace(text) == "" {
		return 0, errors.New("cli: 中途消息不能为空")
	}

	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	if s.turnActive[threadID] <= 0 {
		return 0, domain.NewError(domain.CodeCoreUnavailable, "线程没有运行中轮次，无法排队中途消息")
	}
	if s.steerQueues == nil {
		s.steerQueues = map[string][]domain.Message{}
	}
	s.steerQueues[threadID] = append(s.steerQueues[threadID], domain.Message{
		Role:  domain.RoleUser,
		Parts: []domain.Part{{Type: domain.PartText, Text: text}},
	})
	return len(s.steerQueues[threadID]), nil
}

// DrainSteering 排空线程队列并返回消息；实现 engine.SteeringSource。
func (s *Session) DrainSteering(_ context.Context, threadID string) []domain.Message {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	out := s.steerQueues[threadID]
	if len(out) == 0 {
		return nil
	}
	delete(s.steerQueues, threadID)
	return out
}

// SteeringQueueLength 返回线程待排空的中途消息数（turn.get 的 queueLength）。
func (s *Session) SteeringQueueLength(threadID string) int {
	s.steerMu.Lock()
	defer s.steerMu.Unlock()
	return len(s.steerQueues[threadID])
}

// ModelRef 返回当前生效模型。
func (s *Session) ModelRef() ports.ModelRef { return s.modelRef() }

// ModelError 返回模型配置问题（nil 表示可用）。
func (s *Session) ModelError() error { return s.modelErr }

// Product 返回已加载的产品声明。
func (s *Session) Product() *coreapp.Product {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.product
}

// Assembly 返回装配结果（插件、工具、provider 目录）。
// 热重载会在锁内整体替换这份结果；拿到的指针对应的目录已冻结，可安全只读。
func (s *Session) Assembly() *app.Assembly {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asm
}

// Store 返回工作区存储句柄（Core RPC 的线程/entry 查询复用它）。
func (s *Session) Store() *sqlite.Store { return s.store }

// Workspace 返回规范化后的工作区路径。
func (s *Session) Workspace() string { return s.cfg.Workspace }

// DataDir 返回数据目录。
func (s *Session) DataDir() string { return s.cfg.DataDir }

// BaseURL 返回 provider 兼容端点覆盖。
func (s *Session) BaseURL() string { return s.cfg.BaseURL }

// Subscribe 订阅领域事件；返回取消函数。
func (s *Session) Subscribe(fn func(domain.Event)) func() { return s.emitter.Subscribe(fn) }

// CreateThread 创建线程但不切换 CLI 当前线程。
func (s *Session) CreateThread(ctx context.Context, title string) (ports.Thread, error) {
	if strings.TrimSpace(title) == "" {
		title = "新会话"
	}
	return s.store.CreateThread(ctx, s.cfg.Workspace, title)
}

// GetThread 读取线程。
func (s *Session) GetThread(ctx context.Context, id string) (ports.Thread, error) {
	return s.store.GetThread(ctx, id)
}

// ListThreads 列线程。
func (s *Session) ListThreads(ctx context.Context, req ports.ListThreadsRequest) (ports.ThreadPage, error) {
	return s.store.ListThreads(ctx, req)
}

// EntriesBefore 历史分页。
func (s *Session) EntriesBefore(ctx context.Context, threadID string, fromSeq, limit int64) (ports.EntriesPage, error) {
	return s.store.EntriesBefore(ctx, threadID, fromSeq, limit)
}

// PendingInteractions 列出线程待处理交互。
func (s *Session) PendingInteractions(ctx context.Context, threadID string) ([]ports.InteractionRequest, error) {
	return s.broker.ListPending(ctx, threadID)
}

// RespondInteraction 提交交互响应（幂等由 Broker 保证）。
func (s *Session) RespondInteraction(ctx context.Context, threadID, requestID string, status domain.InteractionStatus, value string) (ports.InteractionResolution, error) {
	return s.broker.Respond(ctx, threadID, requestID, status, value)
}

// HasUI 报告 CLI/Core 当前是否有交互界面。
func (s *Session) HasUI(ctx context.Context, threadID string) bool {
	return s.broker.HasUI(ctx, threadID)
}

// ActionDef 返回某插件的界面动作定义（M4 action.invoke 数据面）。
func (s *Session) ActionDef(pluginID, action string) (host.ActionDef, bool) {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	return asm.ActionDef(pluginID, action)
}

// CallAction 执行一次前端动作（读/写都走这里；写动作的确认文案在清单里，
// CSRF/Origin 由 Gateway 中间件负责）。input 是 JSON 对象原文。
func (s *Session) CallAction(ctx context.Context, pluginID, action string, input json.RawMessage, threadID string) (json.RawMessage, error) {
	s.mu.Lock()
	asm := s.asm
	base := s.hostBase
	s.mu.Unlock()
	if asm == nil {
		return nil, domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	def, ok := asm.ActionDef(pluginID, action)
	if !ok {
		return nil, domain.NewError(domain.CodePluginDenied,
			"插件 "+pluginID+" 没有界面动作 "+action)
	}
	inst, ok := asm.Plugin(pluginID)
	if !ok {
		return nil, domain.NewError(domain.CodePluginDenied,
			"插件 "+pluginID+" 未加载")
	}
	hc := base
	hc.PluginID = pluginID
	hc.ThreadID = threadID
	hc.Signal = ctx
	hc.Now = time.Now().UTC()
	// 只读动作也按线程命名空间读 state（t:<threadID>/<key>）：计划/任务类插件
	// 的状态是线程级，不能因为前端预取而落到空线程。
	if threadID != "" {
		ctx = withThread(ctx, threadID)
		hc.Signal = ctx
	}
	return inst.CallAction(ctx, def.Handler, input, hc)
}

// HTTPAPIError 是插件 HTTP 路由的业务状态错误（404/405），供 Core RPC 映射。
type HTTPAPIError struct {
	Status  int
	Message string
}

func (e *HTTPAPIError) Error() string { return e.Message }

// CallHTTPAPI 调用插件注册的动态 HTTP 路由（route）。
// 路由表与插件实例都在当前装配里；404/405 转换成 HTTPAPIError。
func (s *Session) CallHTTPAPI(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string) (json.RawMessage, error) {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if asm == nil {
		return nil, domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	out, err := asm.CallHTTPAPI(ctx, pluginID, method, path, req, threadID)
	if err != nil {
		return nil, httpAPIError(err)
	}
	return out, nil
}

// CallHTTPAPIStream 执行一次 SSE 路由；每帧通过 onEvent 交给调用方。
// 与 CallHTTPAPI 共用同一套路由表、线程上下文与 404/405 映射；类型不符返回 406。
func (s *Session) CallHTTPAPIStream(ctx context.Context, pluginID, method, path string, req json.RawMessage, threadID string, onEvent func(host.RouteEvent) error) error {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if asm == nil {
		return domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	if err := asm.StreamHTTPAPI(ctx, pluginID, method, path, req, threadID, onEvent); err != nil {
		return httpAPIError(err)
	}
	return nil
}

// CheckHTTPAPIStream 校验路由存在且是 SSE（不执行处理器）。
func (s *Session) CheckHTTPAPIStream(pluginID, method, path string) error {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if asm == nil {
		return domain.NewError(domain.CodeCoreUnavailable, "插件装配尚未就绪")
	}
	if err := asm.CheckHTTPAPIStream(pluginID, method, path); err != nil {
		return httpAPIError(err)
	}
	return nil
}

// httpAPIError 把路由层的判定映射成可读的 HTTP 状态（由 Core RPC 透给 Gateway）。
func httpAPIError(err error) error {
	switch {
	case errors.Is(err, app.ErrRouteNotFound):
		return &HTTPAPIError{Status: 404, Message: "没有匹配的接口"}
	case errors.Is(err, app.ErrRouteMethodNotAllowed):
		return &HTTPAPIError{Status: 405, Message: "该路径不支持此方法"}
	case errors.Is(err, app.ErrRouteStreaming):
		return &HTTPAPIError{Status: 406, Message: "该接口是 SSE，请用 EventSource 订阅"}
	case errors.Is(err, app.ErrRouteNotStreaming):
		return &HTTPAPIError{Status: 406, Message: "该接口不是 SSE"}
	}
	return err
}

// Policies 返回当前生效的运行参数（workspace.updatePolicy 覆盖优先，否则用产品声明）。
func (s *Session) Policies() coreapp.Policies {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.policiesLocked()
}

func (s *Session) policiesLocked() coreapp.Policies {
	if s.policy != nil {
		return *s.policy
	}
	if s.product != nil {
		return s.product.Policies
	}
	return coreapp.Policies{}
}

// UpdatePolicies 合并一次运行参数更新；nil 字段保持当前值。
//
// 校验与协议 ProductPolicy 的 minimum 一致，并额外要求 loopAbortLimit 严格大于
// loopRepeatLimit —— 否则「先警告后中止」的循环检测语义不成立。
func (s *Session) UpdatePolicies(up PolicyUpdate) (coreapp.Policies, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.policiesLocked()
	if up.ToolTimeoutMS != nil {
		if *up.ToolTimeoutMS < 0 {
			return cur, errors.New("toolTimeoutMs 不能小于 0")
		}
		cur.ToolTimeoutMS = *up.ToolTimeoutMS
	}
	if up.TurnTimeoutMS != nil {
		if *up.TurnTimeoutMS < 0 {
			return cur, errors.New("turnTimeoutMs 不能小于 0")
		}
		cur.TurnTimeoutMS = *up.TurnTimeoutMS
	}
	if up.MaxTurnTokens != nil {
		if *up.MaxTurnTokens < 0 {
			return cur, errors.New("maxTurnTokens 不能小于 0")
		}
		cur.MaxTurnTokens = *up.MaxTurnTokens
	}
	if up.LoopRepeatLimit != nil {
		if *up.LoopRepeatLimit < 1 {
			return cur, errors.New("loopRepeatLimit 不能小于 1")
		}
		cur.LoopRepeatLimit = *up.LoopRepeatLimit
	}
	if up.LoopAbortLimit != nil {
		if *up.LoopAbortLimit < 1 {
			return cur, errors.New("loopAbortLimit 不能小于 1")
		}
		cur.LoopAbortLimit = *up.LoopAbortLimit
	}
	if up.ParallelTools != nil {
		if *up.ParallelTools < 1 {
			return cur, errors.New("parallelTools 不能小于 1")
		}
		cur.ParallelTools = *up.ParallelTools
	}
	if cur.LoopAbortLimit <= cur.LoopRepeatLimit {
		return cur, errors.New("loopAbortLimit 必须大于 loopRepeatLimit")
	}
	saved := cur
	s.policy = &saved
	return cur, nil
}

// ResolveModel 在装配好的 provider 目录里解析 provider/model，并补齐能力位。
// 返回 false 表示 provider 或 model 未在 asm.Providers 中声明。
func (s *Session) ResolveModel(providerID, modelID string) (ports.ModelRef, bool) {
	if providerID == "" || modelID == "" {
		return ports.ModelRef{}, false
	}
	asm := s.Assembly()
	if asm == nil || asm.Providers == nil {
		return ports.ModelRef{}, false
	}
	for _, id := range s.asm.Providers.Providers() {
		if id != providerID {
			continue
		}
		for _, m := range s.asm.Providers.Models(id) {
			if m.ID != modelID {
				continue
			}
			return ports.ModelRef{
				ProviderID:       id,
				ModelID:          m.ID,
				ContextWindow:    int64(m.ContextWindow),
				MaxOutput:        int64(m.MaxOutput),
				Input:            append([]string(nil), m.Input...),
				SupportsTools:    m.SupportsTools,
				SupportsThinking: m.SupportsThinking,
			}, true
		}
	}
	return ports.ModelRef{}, false
}

// SetWorkspaceModel 设置工作区级模型覆盖（调用方负责先校验 provider/model）。
func (s *Session) SetWorkspaceModel(ref ports.ModelRef) {
	s.mu.Lock()
	saved := ref
	s.workspaceMod = &saved
	s.mu.Unlock()
}

// SetThreadModel 设置线程级模型覆盖（调用方负责先校验 provider/model）。
func (s *Session) SetThreadModel(threadID string, ref ports.ModelRef) {
	if threadID == "" {
		return
	}
	s.mu.Lock()
	if s.threadModels == nil {
		s.threadModels = map[string]ports.ModelRef{}
	}
	s.threadModels[threadID] = ref
	s.mu.Unlock()
}

// ForgetThreadModel 清理线程级模型覆盖（线程删除时调用，避免 map 单调增长）。
func (s *Session) ForgetThreadModel(threadID string) {
	s.mu.Lock()
	delete(s.threadModels, threadID)
	s.mu.Unlock()
}

// ModelRefFor 返回线程当前生效模型；threadID 为空时返回工作区级模型。
func (s *Session) ModelRefFor(threadID string) ports.ModelRef {
	s.mu.Lock()
	override := s.threadOverrideLocked(threadID)
	s.mu.Unlock()
	if override != nil {
		if ref, ok := s.ResolveModel(override.ProviderID, override.ModelID); ok {
			return ref
		}
		return *override
	}
	return s.defaultModelRef()
}

// providerCatalog 实现 host.Context.Providers（capability: model.admin）。
// 返回所有配置（含停用项，页面需要展示开关）；API Key 只给「是否配置 + 尾号」。
func (s *Session) providerCatalog() host.ProviderCatalog {
	s.providersMu.Lock()
	cfgs := cloneProviderConfigs(s.providerConfigs)
	s.providersMu.Unlock()
	cat := host.ProviderCatalog{Providers: make([]host.ProviderInfo, 0, len(cfgs))}
	for _, c := range cfgs {
		cat.Providers = append(cat.Providers, s.providerInfo(c))
	}
	cur := s.ModelRef()
	cat.Current = host.ModelRef{ProviderID: cur.ProviderID, ModelID: cur.ModelID}
	return cat
}

// selectModelFromPlugin 实现 host.Context.SelectModel（capability: model.admin）。
// 与 model.set 同一套校验：provider/model 必须在当前装配里存在；线程级切换先校验线程。
func (s *Session) selectModelFromPlugin(ctx context.Context, providerID, modelID, threadID string) (host.ModelRef, error) {
	ref, ok := s.ResolveModel(providerID, modelID)
	if !ok {
		return host.ModelRef{}, fmt.Errorf("模型 %s/%s 未在装配中声明", providerID, modelID)
	}
	if threadID != "" {
		if _, err := s.store.GetThread(ctx, threadID); err != nil {
			return host.ModelRef{}, err
		}
		s.SetThreadModel(threadID, ref)
	} else {
		s.SetWorkspaceModel(ref)
	}
	return host.ModelRef{ProviderID: ref.ProviderID, ModelID: ref.ModelID}, nil
}

// applyProviderConfigs 把 providers.json 注入当前装配的 Registry。
// 文件不存在时从适配器插件播种（保留旧产品开箱即用的行为）。
// 配置读取/写入失败只记警告：供应商配置坏掉不应该让整个 Core 起不来。
func (s *Session) applyProviderConfigs(asm *app.Assembly) {
	if asm == nil || asm.Providers == nil {
		return
	}
	cfgs, exists, err := s.providerStore.Load()
	if err != nil {
		s.logWarn("供应商配置读取失败，沿用插件默认: " + err.Error())
		return
	}
	if !exists {
		cfgs = seedProviderConfigs(asm, s.BaseURL())
		if err := s.providerStore.Save(cfgs); err != nil {
			s.logWarn("供应商配置首次写入失败: " + err.Error())
		}
	}
	for _, c := range cfgs {
		s.allowProviderHost(c)
	}
	asm.Providers.SetConfigs(cfgs)
	s.providersMu.Lock()
	s.providerConfigs = cloneProviderConfigs(cfgs)
	s.providersMu.Unlock()
	s.ensureCurrentModel(asm)
}

// seedProviderConfigs 用已装配的适配器声明播种默认供应商。
func seedProviderConfigs(asm *app.Assembly, baseURL string) []provider.Config {
	out := make([]provider.Config, 0, len(asm.Providers.AdapterIDs()))
	for _, id := range asm.Providers.AdapterIDs() {
		models := asm.Providers.AdapterModels(id)
		if len(models) == 0 {
			continue
		}
		protocol := provider.ProtocolForAdapter(id)
		cfgBase := baseURL
		if protocol == provider.ProtocolAnthropic {
			if cfgBase == "" {
				cfgBase = "https://api.anthropic.com/v1"
			}
		} else if cfgBase == "" {
			cfgBase = "https://api.openai.com/v1"
		}
		cfg := provider.Config{
			ID: id, Name: providerDisplayName(id), Protocol: protocol,
			BaseURL: cfgBase, Models: make([]provider.Model, 0, len(models)),
		}
		for _, m := range models {
			cfg.Models = append(cfg.Models, provider.Model{
				ID: m.ID, DisplayName: m.DisplayName,
				ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
				Input: m.Input, SupportsTools: m.SupportsTools,
				SupportsThinking: m.SupportsThinking,
			})
		}
		out = append(out, cfg)
	}
	return out
}

func providerDisplayName(adapterID string) string {
	switch adapterID {
	case "openai":
		return "OpenAI Chat（兼容端点）"
	case "openai-responses":
		return "OpenAI Responses"
	case "anthropic":
		return "Anthropic Messages"
	default:
		return adapterID
	}
}

// ensureCurrentModel 在供应商/模型被删改后保证当前选择仍然有效；无效时选第一个可用模型。
func (s *Session) ensureCurrentModel(asm *app.Assembly) {
	if asm == nil || asm.Providers == nil || s.cfg.Fake || s.cfg.ModelClient != nil {
		return
	}
	cur := s.ModelRef()
	if modelUsable(asm, cur.ProviderID, cur.ModelID) {
		return
	}
	for _, pid := range asm.Providers.Providers() {
		models := asm.Providers.Models(pid)
		if len(models) == 0 {
			continue
		}
		m := models[0]
		s.SetWorkspaceModel(ports.ModelRef{
			ProviderID: pid, ModelID: m.ID,
			ContextWindow: int64(m.ContextWindow), MaxOutput: int64(m.MaxOutput),
			SupportsTools: m.SupportsTools,
		})
		return
	}
}

func modelUsable(asm *app.Assembly, providerID, modelID string) bool {
	for _, pid := range asm.Providers.Providers() {
		if pid != providerID {
			continue
		}
		for _, m := range asm.Providers.Models(pid) {
			if m.ID == modelID {
				return true
			}
		}
	}
	return false
}

// saveProviderFromPlugin 实现 host.Context.SaveProvider。
// APIKey 留空表示保留旧值；ClearAPIKey 才清空。
func (s *Session) saveProviderFromPlugin(ctx context.Context, in host.ProviderInput) (host.ProviderInfo, error) {
	s.providersMu.Lock()
	defer s.providersMu.Unlock()
	cfgs := cloneProviderConfigs(s.providerConfigs)

	id := strings.TrimSpace(in.ID)
	if id == "" {
		generated, err := s.ids.NewULID(ctx)
		if err != nil {
			return host.ProviderInfo{}, fmt.Errorf("生成供应商 id 失败: %w", err)
		}
		id = "p_" + strings.ToLower(generated)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = id
	}
	protocol := provider.Protocol(strings.TrimSpace(in.Protocol))
	if !protocol.Valid() {
		return host.ProviderInfo{}, fmt.Errorf("未知 API 格式: %s", in.Protocol)
	}
	baseURL := strings.TrimSpace(in.BaseURL)
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return host.ProviderInfo{}, errors.New("Base URL 必须是 http(s) 地址")
		}
	}
	models, err := normalizeProviderModels(in.Models)
	if err != nil {
		return host.ProviderInfo{}, err
	}

	idx := -1
	for i := range cfgs {
		if cfgs[i].ID == id {
			idx = i
			break
		}
	}
	key := ""
	if idx >= 0 {
		key = cfgs[idx].APIKey
	}
	if in.ClearAPIKey {
		key = ""
	} else if v := strings.TrimSpace(in.APIKey); v != "" {
		key = v
	}
	cfg := provider.Config{
		ID: id, Name: name, Protocol: protocol, BaseURL: baseURL,
		APIKey: key, Disabled: in.Disabled, Models: models,
	}
	if idx >= 0 {
		cfgs[idx] = cfg
	} else {
		cfgs = append(cfgs, cfg)
	}
	if err := s.providerStore.Save(cfgs); err != nil {
		return host.ProviderInfo{}, fmt.Errorf("保存供应商失败: %w", err)
	}
	s.allowProviderHost(cfg)
	s.providerConfigs = cloneProviderConfigs(cfgs)
	if asm := s.Assembly(); asm != nil && asm.Providers != nil {
		asm.Providers.SetConfigs(cfgs)
		s.ensureCurrentModel(asm)
	}
	return s.providerInfo(cfg), nil
}

// providerInfo 在配置信息上补充「环境变量/secrets 已提供密钥」的状态。
func (s *Session) providerInfo(c provider.Config) host.ProviderInfo {
	info := providerInfoOf(c)
	info.Available = true
	if asm := s.Assembly(); asm != nil && asm.Providers != nil {
		info.Available = asm.Providers.ConfigAvailable(c.ID)
	}
	if info.HasAPIKey {
		return info
	}
	name := "openai_api_key"
	if c.Protocol == provider.ProtocolAnthropic {
		name = "anthropic_api_key"
	}
	if _, ok := s.secret(name); ok {
		info.HasAPIKey = true
		info.APIKeyHint = "环境变量/secrets"
	}
	return info
}

// removeProviderFromPlugin 实现 host.Context.RemoveProvider。
func (s *Session) removeProviderFromPlugin(_ context.Context, providerID string) error {
	s.providersMu.Lock()
	defer s.providersMu.Unlock()
	providerID = strings.TrimSpace(providerID)
	if providerID == "" {
		return errors.New("缺少供应商 id")
	}
	cfgs := cloneProviderConfigs(s.providerConfigs)
	out := cfgs[:0]
	for _, c := range cfgs {
		if c.ID != providerID {
			out = append(out, c)
		}
	}
	if len(out) == len(cfgs) {
		return nil // 幂等
	}
	if err := s.providerStore.Save(out); err != nil {
		return fmt.Errorf("删除供应商失败: %w", err)
	}
	s.providerConfigs = cloneProviderConfigs(out)
	if asm := s.Assembly(); asm != nil && asm.Providers != nil {
		asm.Providers.SetConfigs(out)
		s.ensureCurrentModel(asm)
	}
	return nil
}

// fetchModelsFromPlugin 实现 host.Context.FetchProviderModels：服务端用已保存的
// 端点与密钥调用 /models，密钥不经过前端。
func (s *Session) fetchModelsFromPlugin(ctx context.Context, providerID string) ([]host.ProviderModel, error) {
	s.providersMu.Lock()
	var cfg provider.Config
	found := false
	for _, c := range s.providerConfigs {
		if c.ID == providerID {
			cfg, found = c.Clone(), true
			break
		}
	}
	s.providersMu.Unlock()
	if !found {
		return nil, fmt.Errorf("没有供应商 %s", providerID)
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		switch cfg.Protocol {
		case provider.ProtocolAnthropic:
			baseURL = "https://api.anthropic.com/v1"
		default:
			baseURL = "https://api.openai.com/v1"
		}
	}
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		name := "openai_api_key"
		if cfg.Protocol == provider.ProtocolAnthropic {
			name = "anthropic_api_key"
		}
		if v, ok := s.secret(name); ok {
			key = strings.TrimSpace(v)
		}
	}
	if key == "" {
		return nil, errors.New("请先填写 API Key（或配置对应的 secrets.toml/环境变量）")
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("模型列表地址非法: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if cfg.Protocol == provider.ProtocolAnthropic {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	debugID := s.debug.NextID()
	startedAt := time.Now()
	s.debug.Request(debugID, http.MethodGet, endpoint, req.Header, nil)
	res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		s.debug.Error(debugID, err, time.Since(startedAt))
		return nil, fmt.Errorf("拉取模型列表失败: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		s.debug.Error(debugID, err, time.Since(startedAt))
		return nil, fmt.Errorf("读取模型列表失败: %w", err)
	}
	s.debug.Response(debugID, res.Status, res.Header, time.Since(startedAt), body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("拉取模型列表失败：HTTP %d", res.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"display_name"`
			ContextWindow int    `json:"context_window"`
			MaxOutput     int    `json:"max_output"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("模型列表不是预期的 JSON 形状")
	}
	out := make([]host.ProviderModel, 0, len(payload.Data))
	seen := map[string]bool{}
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, host.ProviderModel{
			ID: id, DisplayName: m.DisplayName,
			ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
			Input: []string{"text"}, SupportsTools: true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func normalizeProviderModels(in []host.ProviderModel) ([]provider.Model, error) {
	out := make([]provider.Model, 0, len(in))
	seen := map[string]bool{}
	for _, m := range in {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			return nil, errors.New("模型 id 不能为空")
		}
		if len(id) > 200 {
			return nil, errors.New("模型 id 太长")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		input := m.Input
		if len(input) == 0 {
			input = []string{"text"}
		}
		allowed := input[:0]
		for _, v := range input {
			if v == "text" || v == "image" {
				allowed = append(allowed, v)
			}
		}
		if m.ContextWindow < 0 || m.MaxOutput < 0 {
			return nil, fmt.Errorf("模型 %s 的窗口/输出上限不能为负数", id)
		}
		out = append(out, provider.Model{
			ID: id, DisplayName: strings.TrimSpace(m.DisplayName),
			ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
			Input: allowed, SupportsTools: m.SupportsTools,
			SupportsThinking: m.SupportsThinking, Disabled: m.Disabled,
		})
	}
	return out, nil
}

func providerInfoOf(c provider.Config) host.ProviderInfo {
	info := host.ProviderInfo{
		ID: c.ID, DisplayName: c.Name, Protocol: string(c.Protocol),
		BaseURL: c.BaseURL, Disabled: c.Disabled,
		HasAPIKey:  strings.TrimSpace(c.APIKey) != "",
		APIKeyHint: maskAPIKey(c.APIKey),
		Models:     make([]host.ProviderModel, 0, len(c.Models)),
	}
	for _, m := range c.Models {
		info.Models = append(info.Models, host.ProviderModel{
			ID: m.ID, DisplayName: m.DisplayName,
			ContextWindow: m.ContextWindow, MaxOutput: m.MaxOutput,
			Input: m.Input, SupportsTools: m.SupportsTools,
			SupportsThinking: m.SupportsThinking, Disabled: m.Disabled,
		})
	}
	return info
}

func maskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "已配置"
	}
	return "••••" + key[len(key)-4:]
}

func cloneProviderConfigs(in []provider.Config) []provider.Config {
	out := make([]provider.Config, 0, len(in))
	for _, c := range in {
		out = append(out, c.Clone())
	}
	return out
}

func (s *Session) threadOverrideLocked(threadID string) *ports.ModelRef {
	if threadID != "" {
		if ref, ok := s.threadModels[threadID]; ok {
			return &ref
		}
	}
	if s.workspaceMod != nil {
		ref := *s.workspaceMod
		return &ref
	}
	return nil
}

// modelRef 由产品/CLI 配置与 provider 声明的能力合成（工作区级）。
func (s *Session) modelRef() ports.ModelRef { return s.ModelRefFor("") }

// defaultModelRef 是没有任何覆盖时的模型选择（--fake > provider 默认）。
func (s *Session) defaultModelRef() ports.ModelRef {
	s.mu.Lock()
	asm := s.asm
	s.mu.Unlock()
	if s.cfg.Fake || s.cfg.ModelClient != nil {
		return ports.ModelRef{ProviderID: "fake", ModelID: "fake-1", ContextWindow: 128000, SupportsTools: true}
	}
	providerID := s.cfg.Provider
	if providerID == "" {
		providerID = "openai"
	}
	modelID := s.cfg.Model
	if modelID == "" {
		modelID = "gpt-4o"
	}
	if ref, ok := s.ResolveModel(providerID, modelID); ok {
		return ref
	}
	toolCount := 0
	if asm != nil {
		toolCount = len(asm.Tools.List(context.Background()))
	}
	ref := ports.ModelRef{
		ProviderID: providerID, ModelID: modelID,
		ContextWindow: 128000, SupportsTools: toolCount > 0,
	}
	if asm != nil && asm.Providers != nil {
		ids := asm.Providers.Providers()
		if len(ids) == 1 {
			// 产品可能直接写插件 id；桥支持这种写法，但能力仍来自唯一 provider。
			ref.ProviderID = ids[0]
		}
	}
	return ref
}

// LastSeq 返回线程已分配到的最大事件序号（thread.get 的快照基线）。
func (s *Session) LastSeq(threadID string) int64 {
	if s.emitter == nil {
		return 0
	}
	return s.emitter.LastSeq(threadID)
}

func (s *Session) setThread(id string) {
	s.mu.Lock()
	s.threadID = id
	s.mu.Unlock()
}

// onEvent 把增量写到 stdout，并把所有事件写入可选的事件文件。
func (s *Session) onEvent(evt domain.Event) {
	if s.eventsBuf != nil {
		raw, err := json.Marshal(evt)
		if err == nil {
			_, _ = s.eventsBuf.Write(raw)
			_ = s.eventsBuf.WriteByte('\n')
			_ = s.eventsBuf.Flush()
		}
	}
	if evt.Kind != domain.EvtMessageDelta {
		return
	}
	var body struct {
		PartKind string `json:"partKind"`
		Text     string `json:"text"`
	}
	if json.Unmarshal(evt.Payload, &body) != nil {
		return
	}
	if body.PartKind == "text" && body.Text != "" {
		fmt.Fprint(s.cfg.Out, body.Text)
	}
}

// uiAsk 是 host.Context.UI 的实现：把插件的 ctx.ui 请求转成交互桥上的一次交互，
// 由界面（终端或网页托盘）回答。没有界面时 broker 会立即解析成 unavailable，
// 调用方按失败关闭处理（INV-4）。
func (s *Session) uiAsk(ctx context.Context, req host.UIRequest) (host.UIResolution, error) {
	if s.broker == nil {
		return host.UIResolution{Status: domain.InteractionUnavailable}, nil
	}
	threadID := threadFrom(ctx)
	if threadID == "" {
		threadID = s.CurrentThread()
	}
	// form 的题目要能跟着 interactions 行一起持久化：表里没有 payload 列，
	// 题目就放在 body（view 侧按 kind=form 解析 body 的 JSON）。
	body := req.Message
	if req.Kind == domain.InteractionForm {
		body = string(req.Payload)
	}
	res, err := s.broker.Request(ctx, ports.InteractionRequest{
		ThreadID: threadID,
		RunID:    runFrom(ctx),
		PluginID: req.PluginID,
		Kind:     req.Kind,
		Title:    req.Title,
		Body:     body,
		Options:  req.Options,
		Payload:  req.Payload,
	})
	if err != nil {
		return host.UIResolution{}, err
	}
	return host.UIResolution{Status: res.Status, Value: res.Value}, nil
}

// onInteraction 是 CLI 的终端替身界面：确认 y/n、选择序号、输入文本。
func (s *Session) onInteraction(evt domain.Event) {
	if evt.Kind != domain.EvtInteractionRequested {
		return
	}
	var body struct {
		ThreadID      string   `json:"threadId"`
		InteractionID string   `json:"interactionId"`
		Kind          string   `json:"kind"`
		Title         string   `json:"title"`
		Body          string   `json:"body"`
		Options       []string `json:"options"`
	}
	if json.Unmarshal(evt.Payload, &body) != nil || body.InteractionID == "" {
		return
	}
	status, value := s.ask(body.Kind, body.Title, body.Body, body.Options)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.broker.Respond(ctx, body.ThreadID, body.InteractionID, status, value); err != nil {
		fmt.Fprintf(s.cfg.Err, "交互响应失败: %v\n", err)
	}
}

func (s *Session) ask(kind, title, body string, options []string) (domain.InteractionStatus, string) {
	fmt.Fprintf(s.cfg.Err, "\n[审批] %s\n%s\n", title, body)
	switch kind {
	case string(domain.InteractionSelect):
		for i, opt := range options {
			fmt.Fprintf(s.cfg.Err, "  %d) %s\n", i+1, opt)
		}
		fmt.Fprint(s.cfg.Err, "选择序号（空=拒绝）: ")
		line, _ := s.in.ReadString('\n')
		line = strings.TrimSpace(line)
		var idx int
		if _, err := fmt.Sscanf(line, "%d", &idx); err == nil && idx >= 1 && idx <= len(options) {
			return domain.InteractionAccepted, options[idx-1]
		}
		return domain.InteractionDenied, ""
	case string(domain.InteractionInput):
		fmt.Fprint(s.cfg.Err, "输入（空=取消）: ")
		line, _ := s.in.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			return domain.InteractionDenied, ""
		}
		return domain.InteractionAccepted, line
	default:
		fmt.Fprint(s.cfg.Err, "允许？[y/N] ")
		line, _ := s.in.ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "y" || line == "yes" {
			return domain.InteractionAccepted, "true"
		}
		return domain.InteractionDenied, "false"
	}
}

// stateOp 把插件 KV 落到 SQLite，键按线程命名空间（t:<threadID>/<key>）。
func (s *Session) stateOp(ctx context.Context, op host.StateOp) (json.RawMessage, error) {
	thread := strings.TrimSpace(op.ThreadID)
	if thread == "" {
		thread = threadFrom(ctx)
	}
	if thread == "" {
		thread = s.CurrentThread()
	}
	key := ports.StateKey{PluginID: op.PluginID, Key: "t:" + thread + "/" + op.Key}
	switch op.Kind {
	case "get":
		v, found, err := s.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if !found {
			return json.RawMessage("null"), nil
		}
		return v, nil
	case "set":
		return nil, s.store.Set(ctx, key, op.Value)
	case "delete":
		return nil, s.store.Delete(ctx, key)
	case "list":
		keys, err := s.store.ListKeys(ctx, op.PluginID, "t:"+thread+"/"+op.Prefix)
		if err != nil {
			return nil, err
		}
		return json.Marshal(keys)
	default:
		return nil, fmt.Errorf("cli: 未知 state 操作 %q", op.Kind)
	}
}

func buildHTTPBridge(cfg Config, debugLog *host.DebugLog) (httpBridge, error) {
	allow := append([]string(nil), cfg.AllowHosts...)
	if cfg.BaseURL != "" {
		if u, err := urlHost(cfg.BaseURL); err == nil && u != "" {
			allow = append(allow, u)
		}
	}
	if len(allow) == 0 {
		allow = []string{"api.openai.com"}
	}
	bcfg := host.HTTPBridgeConfig{AllowHosts: allow, DebugLog: debugLog}
	b, err := host.NewHTTPBridge(bcfg)
	return b, err
}

// httpBridge 让调用方能在关闭时释放连接池；领域侧只依赖 host.HTTPBridge。
type httpBridge interface {
	host.HTTPBridge
	AllowHost(host string)
	Close()
}

// allowProviderHost 把供应商端点加入出网 allowlist；空 BaseURL 时按协议默认域名。
func (s *Session) allowProviderHost(c provider.Config) {
	if s.httpBridge == nil {
		return
	}
	raw := strings.TrimSpace(c.BaseURL)
	hostName := ""
	if raw != "" {
		if h, err := urlHost(raw); err == nil {
			hostName = h
		}
	} else if c.Protocol == provider.ProtocolAnthropic {
		hostName = "api.anthropic.com"
	} else {
		hostName = "api.openai.com"
	}
	if hostName != "" {
		s.httpBridge.AllowHost(hostName)
	}
}

func urlHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", nil
	}
	return u.Host, nil
}

// loadSecrets 读取 ~/.adago/secrets.toml（形如 openai_api_key = "sk-..."）。
// 文件不存在不是错误：只用环境变量的部署是合法的。
func loadSecrets(path string) (map[string]string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return map[string]string{}, nil
		}
		path = filepath.Join(home, ".adago", "secrets.toml")
	}
	out := map[string]string{}
	if _, err := toml.DecodeFile(path, &out); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("cli: 读取 %s 失败: %w", path, err)
	}
	return out, nil
}

// secret 实现密钥查找：环境变量优先，其次 secrets.toml（09 §9.4）。
func (s *Session) secret(name string) (string, bool) {
	env := "ADAG_SECRET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v, true
	}
	if name == "openai_api_key" {
		if v := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); v != "" {
			return v, true
		}
	}
	if v := strings.TrimSpace(s.secrets[name]); v != "" {
		return v, true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// M3 插件平台：发现、启用持久化与双代热重载（06 §6.5、06 §6.9）
// ---------------------------------------------------------------------------

// reloadDrainTimeout 是热重载等待在途轮次结束的上限；超过后仍会切换
// （在途调用可能拿到已关闭的旧代并失败，不再无限等待）。
const reloadDrainTimeout = 30 * time.Second

// sessionBaseScripts 决定随包插件的来源：测试注入优先，否则用嵌入官方包。
// internal 不得 import official/——经组合根转手（app.OfficialScripts）。
func sessionBaseScripts(cfg Config) ([]host.Script, error) {
	if len(cfg.Scripts) > 0 {
		return cfg.Scripts, nil
	}
	return app.OfficialScripts()
}

// sessionFrontendScripts 返回前端插件脚本：测试注入优先，否则经组合根取嵌入包。
// Session 只用 id 集合（产品声明允许缺席的集合）；真实渲染在 Gateway 侧。
func sessionFrontendScripts(cfg Config) ([]host.Script, error) {
	if cfg.FrontendScripts != nil {
		return cfg.FrontendScripts, nil
	}
	return app.FrontendScripts()
}

// installAssembly 把装配结果接进会话：工具执行器、模型选择与引擎依赖里
// 可重建的子集（目录/钩子/模型/Runner/Compactor/HasPolicy/Prompts/Skills）。
// Open 与热重载共用。
// 旧装配在新目录接入后关闭（双代：替换瞬间之后的新调用走新代，旧代在途调用
// 已由 reloadAssembly 的排空等待覆盖）。
func (s *Session) installAssembly(asm *app.Assembly) error {
	exec, err := toolkit.NewExecutor(toolkit.ExecutorConfig{
		Catalog: asm.Tools,
		Invoker: asm.ToolInvoker(),
		Clock:   s.clock,
		// Changes 跨热重载复用：检查点账本不随装配换代清零（M5）。
		Changes:   s.changes,
		HasPolicy: asm.Runtime.PolicyPluginActive,
		// 检查点归属：threadId/runId 由轮次 ctx 携带（withThread/withRun）。
		CtxMeta: func(ctx context.Context) (string, string) {
			return threadFrom(ctx), runFrom(ctx)
		},
	})
	if err != nil {
		return fmt.Errorf("cli: 建工具执行器失败: %w", err)
	}
	s.applyProviderConfigs(asm)
	model, modelErr := s.pickModel(asm)
	prompts := assemblyPromptSource{frags: promptFragmentsOf(asm)}
	var skillSrc engine.SkillSource
	if s.Product().Capabilities.Skills {
		skillSrc = sessionSkillSource{s: s}
	}
	for _, conflict := range asm.SkillConflicts() {
		s.logWarn("技能同名冲突，工作区优先: " + conflict)
	}
	s.mu.Lock()
	old := s.asm
	s.asm = asm
	s.executor = exec
	s.model, s.modelErr = model, modelErr
	s.deps.Tools = asm.Tools
	s.deps.Hooks = asm.Hooks
	s.deps.Model = model
	s.deps.Runner = exec
	s.deps.Compactor = &engine.ModelCompactor{Model: model, Clock: s.clock, IDs: s.ids}
	s.deps.HasPolicy = func() bool { return asm.Runtime.PolicyPluginActive }
	s.deps.Prompts = prompts
	s.deps.Skills = skillSrc
	s.mu.Unlock()
	if old != nil && old != asm {
		_ = old.Close(context.Background())
	}
	return nil
}

// pickModel 是装配后的模型选择：测试覆盖 > --fake > provider 插件（与 M1 相同优先级）。
// 第二个返回值非 nil 表示当前装配没有可用模型（没有 provider 且未用 --fake）。
func (s *Session) pickModel(asm *app.Assembly) (ports.ModelClient, error) {
	switch {
	case s.cfg.ModelClient != nil:
		return s.cfg.ModelClient, nil
	case s.cfg.Fake:
		return devfake.New(), nil
	case asm.ProviderCount() > 0:
		return provider.NewRetry(asm.Providers), nil
	default:
		return nil, errors.New("没有可用的 provider：启用 provider 插件或使用 --fake")
	}
}

// assembleInitial 是 Open 的装配路径：随包 base + 启用的第三方。
//
// 这里没有旧代可回退：单个第三方插件装配失败时把它隔离（记 plugin_meta.last_error、
// 状态 failed），其余插件照常装配——Core 不因一个坏插件起不来（6.5「其他插件不受影响」），
// 也不会降级成空基座（随包 base 仍在）。归因不到第三方时让 Open 失败（诚实报错）。
func (s *Session) assembleInitial(ctx context.Context) (*app.Assembly, error) {
	discovered, warnings := s.discoverThirdParty()
	s.logWarnings(warnings)
	s.mu.Lock()
	s.discovered = discovered
	s.failedPlugins = map[string]string{}
	for _, w := range warnings {
		if w.PluginID != "" {
			s.failedPlugins[w.PluginID] = w.String()
		}
	}
	s.mu.Unlock()
	meta := s.listPluginMeta(ctx)
	productIDs := productIDSet(s.product)
	extras, disabled, optIn := pluginSelection(discovered, meta, s.baseScripts, s.frontendScripts, productIDs)
	asm, err := app.AssembleWithExtras(ctx, s.product, s.baseScripts, extras, disabled, optIn, s.appDeps)
	if err == nil {
		if serr := s.syncPluginMeta(ctx, discovered, meta, nil); serr != nil {
			return nil, serr
		}
		return asm, nil
	}
	// 隔离重试：把装配错误归因到一个第三方插件并摘掉，直到成功或归因不出。
	remaining := append([]discovery.Plugin(nil), discovered...)
	failures := map[string]string{}
	for err != nil && len(remaining) > 0 {
		id := blameThirdParty(err, remaining)
		if id == "" {
			return nil, err
		}
		failures[id] = err.Error()
		if s.cfg.Log != nil {
			s.cfg.Log("warn", "第三方插件装配失败，已隔离: "+failures[id], map[string]any{"pluginId": id})
		}
		kept := make([]discovery.Plugin, 0, len(remaining)-1)
		for _, p := range remaining {
			if p.ID() != id {
				kept = append(kept, p)
			}
		}
		remaining = kept
		extras, disabled, optIn = pluginSelection(remaining, meta, s.baseScripts, s.frontendScripts, productIDs)
		asm, err = app.AssembleWithExtras(ctx, s.product, s.baseScripts, extras, disabled, optIn, s.appDeps)
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.discovered = remaining
	s.failedPlugins = failures
	s.mu.Unlock()
	if serr := s.syncPluginMeta(ctx, remaining, meta, failures); serr != nil {
		return nil, serr
	}
	return asm, nil
}

// discoverThirdParty 扫描工作区与全局插件目录，合并成一份第三方快照：
// 工作区优先、与随包插件同 id 的忽略并记警告（6.1、6.9）。
func (s *Session) discoverThirdParty() ([]discovery.Plugin, []discovery.Warning) {
	var warns []discovery.Warning
	ws, w, err := discovery.LoadDirDetailed(discovery.WorkspaceRoot(s.cfg.Workspace), discovery.SourceWorkspace)
	if err != nil {
		warns = append(warns, discovery.Warning{Message: err.Error()})
	}
	warns = append(warns, w...)
	if root, gerr := discovery.GlobalRoot(); gerr == nil {
		gl, gw, rerr := discovery.LoadDirDetailed(root, discovery.SourceGlobal)
		if rerr != nil {
			warns = append(warns, discovery.Warning{Message: rerr.Error()})
		}
		warns = append(warns, gw...)
		merged, mw := discovery.MergeByPriority(ws, gl)
		ws = merged
		warns = append(warns, mw...)
	} else {
		warns = append(warns, discovery.Warning{Message: gerr.Error()})
	}
	baseIDs := map[string]bool{}
	for _, sc := range s.baseScripts {
		if sc.Manifest != nil {
			baseIDs[sc.Manifest.ID] = true
		}
	}
	out := make([]discovery.Plugin, 0, len(ws))
	for _, p := range ws {
		if baseIDs[p.ID()] {
			warns = append(warns, discovery.Warning{
				PluginID: p.ID(), Dir: p.Dir,
				Message: "与随包插件同 id，忽略磁盘版本（随包插件不可被同名包覆盖，6.9）",
			})
			continue
		}
		out = append(out, p)
	}
	return out, warns
}

// logWarnings 把发现警告送进日志通道（未配置日志时丢弃）。
func (s *Session) logWarnings(warnings []discovery.Warning) {
	if s.cfg.Log == nil {
		return
	}
	for _, w := range warnings {
		s.cfg.Log("warn", "插件发现警告: "+w.String(), map[string]any{"pluginId": w.PluginID})
	}
}

// listPluginMeta 读取 plugin_meta；读失败按空表处理（启用状态缺省 enabled）。
func (s *Session) listPluginMeta(ctx context.Context) map[string]sqlite.PluginMetaRecord {
	out := map[string]sqlite.PluginMetaRecord{}
	rows, err := s.store.ListPluginMeta(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

// pluginSelection 决定本次装配的第三方、被禁用的随包插件，以及用户显式启用的
// 未声明随包插件（optIn）：
//   - 第三方按 plugin_meta.enabled 过滤（缺省 enabled）；
//   - 随包插件里 enabled=false 且被产品声明的进 disabled；
//   - 产品未声明、但用户显式启用过的随包插件进 optIn，装配时追加。
func pluginSelection(discovered []discovery.Plugin, meta map[string]sqlite.PluginMetaRecord, base, frontend []host.Script, productIDs map[string]bool) ([]host.Script, map[string]bool, map[string]bool) {
	extras := make([]host.Script, 0, len(discovered))
	for _, p := range discovered {
		if rec, ok := meta[p.ID()]; ok && !rec.Enabled {
			continue
		}
		extras = append(extras, p.Script)
	}
	var disabled map[string]bool
	var optIn map[string]bool
	// 统一插件包会同时出现在 base 与 frontend（后端/前端两种加载形态）；
	// 按 id 去重，但前端-only 插件只能从 frontend 看到。
	seen := map[string]bool{}
	bundled := make([]host.Script, 0, len(base)+len(frontend))
	bundled = append(bundled, base...)
	bundled = append(bundled, frontend...)
	for _, sc := range bundled {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		if seen[id] {
			continue
		}
		seen[id] = true
		rec, ok := meta[id]
		if !ok {
			continue
		}
		if rec.UserSet && rec.Enabled {
			if optIn == nil {
				optIn = map[string]bool{}
			}
			optIn[id] = true
		}
		if !productIDs[id] {
			continue
		}
		if !rec.Enabled {
			if disabled == nil {
				disabled = map[string]bool{}
			}
			disabled[id] = true
		}
	}
	return extras, disabled, optIn
}

// productIDSet 把产品声明的插件列表转成集合。
func productIDSet(p *coreapp.Product) map[string]bool {
	out := map[string]bool{}
	if p == nil {
		return out
	}
	for _, id := range p.Plugins {
		out[id] = true
	}
	return out
}

// syncPluginMeta 把当前发现的插件写进 plugin_meta（6.9：id+版本+指纹）：
// 新插件缺省 enabled；已有记录保留用户启用状态；本次装配失败被隔离的插件
// 记录 last_error，健康的插件清掉旧错误。随包插件同样落一行（否则 setEnabled
// 会因缺行失败），指纹取嵌入 bundle 的哈希。
//
// 产品默认与用户显式选择分开：user_set=0 时随包插件的 enabled 由产品声明决定
// （声明=启用，未声明=未启用）；user_set=1 时以用户的选择为准。这样旧版本给
// 所有随包插件写下的 enabled=1 不会把未声明插件误当成已启用。
func (s *Session) syncPluginMeta(ctx context.Context, discovered []discovery.Plugin, meta map[string]sqlite.PluginMetaRecord, failures map[string]string) error {
	productIDs := productIDSet(s.product)
	for _, sc := range s.baseScripts {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		inProduct := productIDs[id]
		enabled, userSet, lastErr := inProduct, false, ""
		if rec, ok := meta[id]; ok {
			userSet = rec.UserSet
			lastErr = rec.LastError
			if userSet {
				enabled = rec.Enabled
			}
		}
		rec := sqlite.PluginMetaRecord{
			ID: id, Version: sc.Manifest.Version, Enabled: enabled, UserSet: userSet,
			Fingerprint: fingerprintScript(sc), LastError: lastErr,
		}
		if err := s.store.UpsertPluginMeta(ctx, rec); err != nil {
			return fmt.Errorf("cli: 写随包插件 %s 的元信息失败: %w", id, err)
		}
	}
	for _, p := range discovered {
		id := p.ID()
		if id == "" || p.Script.Manifest == nil {
			continue
		}
		enabled, userSet := true, false
		if rec, ok := meta[id]; ok {
			enabled = rec.Enabled
			userSet = rec.UserSet
		}
		lastErr := ""
		if msg, bad := failures[id]; bad {
			lastErr = msg
		}
		rec := sqlite.PluginMetaRecord{
			ID: id, Version: p.Script.Manifest.Version, Enabled: enabled, UserSet: userSet,
			Fingerprint: p.Fingerprint, LastError: lastErr,
		}
		if err := s.store.UpsertPluginMeta(ctx, rec); err != nil {
			return fmt.Errorf("cli: 写插件 %s 的元信息失败: %w", id, err)
		}
	}
	return nil
}

// blameThirdParty 从装配错误文本里归因到一个第三方插件 id。
// app/host 的装配错误都带「插件 <id>」片段；按 id 长度降序取最长命中，避免前缀误伤。
func blameThirdParty(err error, candidates []discovery.Plugin) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	best := ""
	for _, p := range candidates {
		id := p.ID()
		if id == "" || !strings.Contains(text, "插件 "+id) {
			continue
		}
		if best == "" || len(id) > len(best) {
			best = id
		}
	}
	return best
}

// fingerprintScript 是随包插件的指纹：清单身份 + 入口源码的 sha256 前 16 位
// （对应「官方包指纹取嵌入 bundle 的哈希」，6.9）。
func fingerprintScript(sc host.Script) string {
	h := sha256.New()
	if sc.Manifest != nil {
		fmt.Fprintf(h, "%s\x00%s\x00", sc.Manifest.ID, sc.Manifest.Version)
	}
	h.Write([]byte(sc.Source))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// reloadAssembly 重建装配并双代切换（6.9）：
//
//	发现第三方（警告即失败） → 按 plugin_meta 过滤 → app.AssembleWithExtras 构建新代
//	→ 构建失败：保留旧代并返回错误（Core 不降级为空）
//	→ 构建成功：等待在途轮次结束（上限 30s）→ 锁内替换目录与依赖 → 关闭旧代
//
// 随包插件的启停也在这里生效（pluginSelection 把 enabled=false 的随包 id 传给装配）。
func (s *Session) reloadAssembly(ctx context.Context) error {
	discovered, warnings := s.discoverThirdParty()
	if len(warnings) > 0 {
		msgs := make([]string, 0, len(warnings))
		for _, w := range warnings {
			msgs = append(msgs, w.String())
		}
		return fmt.Errorf("cli: 第三方插件发现失败，保留旧装配: %s", strings.Join(msgs, "; "))
	}
	meta := s.listPluginMeta(ctx)
	extras, disabled, optIn := pluginSelection(discovered, meta, s.baseScripts, s.frontendScripts, productIDSet(s.product))
	newAsm, err := app.AssembleWithExtras(ctx, s.product, s.baseScripts, extras, disabled, optIn, s.appDeps)
	if err != nil {
		return fmt.Errorf("cli: 重建装配失败，保留旧装配: %w", err)
	}
	// 等在途轮次结束再切换，避免杀掉在途的工具调用（双代语义）。
	s.waitTurnsDrained(reloadDrainTimeout)
	if err := s.installAssembly(newAsm); err != nil {
		// 新目录没接上：旧装配保持原状；新代运行时关掉，不泄漏。
		_ = newAsm.Close(context.Background())
		return err
	}
	s.mu.Lock()
	s.discovered = discovered
	s.failedPlugins = map[string]string{}
	s.mu.Unlock()
	return s.syncPluginMeta(ctx, discovered, meta, nil)
}

// waitTurnsDrained 等待全部在途轮次结束（turnActive 计数归零）；超时返回 false。
func (s *Session) waitTurnsDrained(max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		s.steerMu.Lock()
		active := 0
		for _, n := range s.turnActive {
			active += n
		}
		s.steerMu.Unlock()
		if active == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// discoveredSnapshot 返回第三方发现快照的副本。
func (s *Session) discoveredSnapshot() []discovery.Plugin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]discovery.Plugin(nil), s.discovered...)
}

// PluginView 是 plugin.list 的一行：运行时摘要 + 发现层信息。
type PluginView struct {
	ID          string
	Name        string
	Version     string
	APIVersion  int
	Trust       string
	Enabled     bool
	Status      string // ready | degraded | disabled | failed | unused
	Fingerprint string
	LastError   string
	// Source 是发现来源：""＝随包（官方）；workspace | global。
	Source string
	Tools  []string
	Hooks  []string
	// Skills 是随包技能名（6.7 的 contributions.skills 计数来源）。
	Skills []string
	// Frontend / Backend 标明清单段；统一插件包可以两者都为真。
	Frontend bool
	Backend  bool
}

// PluginViews 列出随包 + 第三方插件的当前状态（plugin.list 的数据源）。
//
// 列表是「全部随包/已安装插件」而不是「当前产品装配的插件」：
// 产品未声明的随包插件用中性状态 unused 表示，不伪装成 failed。
func (s *Session) PluginViews(ctx context.Context) []PluginView {
	s.mu.Lock()
	asm := s.asm
	base := s.baseScripts
	frontend := append([]host.Script(nil), s.frontendScripts...)
	discovered := append([]discovery.Plugin(nil), s.discovered...)
	failed := map[string]string{}
	for id, msg := range s.failedPlugins {
		failed[id] = msg
	}
	product := s.product
	s.mu.Unlock()

	productIDs := map[string]bool{}
	if product != nil {
		for _, id := range product.Plugins {
			productIDs[id] = true
		}
	}

	meta := s.listPluginMeta(ctx)
	out := make([]PluginView, 0, len(base)+len(frontend)+len(discovered)+len(failed))
	seen := map[string]bool{}
	for _, sc := range base {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		seen[id] = true
		rec, hasRec := meta[id]
		v := pluginViewOf(sc, "", nil, rec, hasRec, asm)
		v.Backend = true
		v.Frontend = sc.Manifest.Frontend != nil
		if !productIDs[id] {
			// 随包但当前产品未声明：只有用户显式启用过才进入装配；
			// 否则用中性状态 unused 表示，可查看、可启用，但不是故障。
			optedIn := hasRec && rec.UserSet && rec.Enabled
			if !optedIn {
				v.Status = "unused"
				v.Enabled = false
				v.LastError = ""
			}
		}
		out = append(out, v)
	}
	// frontend-only 插件也进列表：产品声明或用户显式启用决定它是否已挂载。
	for _, sc := range frontend {
		if sc.Manifest == nil {
			continue
		}
		id := sc.Manifest.ID
		if seen[id] {
			continue
		}
		seen[id] = true
		rec, hasRec := meta[id]
		enabled := productIDs[id]
		if hasRec && rec.UserSet {
			enabled = rec.Enabled
		}
		status := "ready"
		if !enabled {
			if productIDs[id] {
				status = "disabled"
			} else {
				status = "unused"
			}
		}
		v := PluginView{
			ID: id, Name: sc.Manifest.Name, Version: sc.Manifest.Version,
			APIVersion: sc.Manifest.APIVersion, Trust: string(sc.Manifest.Trust),
			Enabled: enabled, Frontend: true, Status: status,
		}
		out = append(out, v)
	}
	for _, p := range discovered {
		id := p.ID()
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		rec, hasRec := meta[id]
		v := pluginViewOf(p.Script, p.Source, &p, rec, hasRec, asm)
		v.Backend = true
		v.Frontend = p.Script.Manifest != nil && p.Script.Manifest.Frontend != nil
		out = append(out, v)
	}
	for id, msg := range failed {
		if seen[id] {
			continue
		}
		seen[id] = true
		v := PluginView{ID: id, Status: "failed", LastError: msg, Enabled: true, Backend: true,
			Trust: string(plugins.TrustThirdParty)}
		if rec, ok := meta[id]; ok {
			v.Enabled = rec.Enabled
			v.Version = rec.Version
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// pluginViewOf 把一份脚本 + 元信息 + 运行时状态合成一行视图。
// recOK 为 false 表示 plugin_meta 还没有这一行（缺省 enabled）。
func pluginViewOf(sc host.Script, source string, p *discovery.Plugin, rec sqlite.PluginMetaRecord, recOK bool, asm *app.Assembly) PluginView {
	v := PluginView{Source: source}
	m := sc.Manifest
	if m == nil {
		return v
	}
	v.ID, v.Name, v.Version = m.ID, m.Name, m.Version
	v.APIVersion = m.APIVersion
	v.Trust = string(m.Trust)
	v.Enabled = !recOK || rec.Enabled
	if p != nil {
		v.Fingerprint = p.Fingerprint
	} else {
		v.Fingerprint = fingerprintScript(sc)
	}
	if recOK {
		v.LastError = rec.LastError
	}
	if !v.Enabled {
		v.Status = "disabled"
		return v
	}
	if asm != nil {
		if inst, ok := asm.Plugin(m.ID); ok {
			sum := inst.Summary()
			v.Status = sum.Status
			if sum.LastError != "" {
				v.LastError = sum.LastError
			}
			v.Tools = sum.Tools
			v.Hooks = sum.Hooks
			v.Skills = sum.Skills
			return v
		}
	}
	// 已启用但没有运行时结果：装配失败被隔离（last_error 已记录）或尚未装配。
	v.Status = "failed"
	if v.LastError == "" {
		v.LastError = "已启用但未出现在当前装配中"
	}
	return v
}

// viewFor 返回单个插件视图（setEnabled 的返回值）。
func (s *Session) viewFor(ctx context.Context, id string) PluginView {
	for _, v := range s.PluginViews(ctx) {
		if v.ID == id {
			return v
		}
	}
	return PluginView{ID: id, Status: "failed", LastError: "插件不在当前视图里"}
}

// lookupPlugin 找一个插件的脚本、来源与指纹（随包 base 优先，其次前端段，最后第三方）。
func (s *Session) lookupPlugin(id string) (host.Script, string, string, bool) {
	for _, sc := range s.baseScripts {
		if sc.Manifest != nil && sc.Manifest.ID == id {
			return sc, "", fingerprintScript(sc), true
		}
	}
	// 纯前端官方插件只在 frontendScripts 里（baseScripts 只收 backend 段）。
	for _, sc := range s.frontendScripts {
		if sc.Manifest != nil && sc.Manifest.ID == id {
			return sc, "", fingerprintScript(sc), true
		}
	}
	for _, p := range s.discoveredSnapshot() {
		if p.ID() == id {
			return p.Script, p.Source, p.Fingerprint, true
		}
	}
	return host.Script{}, "", "", false
}

// hasToolPolicyRole 报告清单是否声明了审批策略角色（与 app.isPolicyPlugin 同口径；
// 这里是 Session 侧的门禁预检，基座仍不解释策略规则，5.4）。
func hasToolPolicyRole(m *plugins.Manifest) bool {
	if m == nil || m.Backend == nil {
		return false
	}
	for _, r := range m.Backend.Roles {
		if r == plugins.RoleToolPolicy {
			return true
		}
	}
	return false
}

// hasNonReadToolsExcluding 报告当前目录里是否仍有其它插件贡献的非只读工具。
func (s *Session) hasNonReadToolsExcluding(ctx context.Context, pluginID string) bool {
	asm := s.Assembly()
	if asm == nil {
		return false
	}
	for _, d := range asm.Tools.List(ctx) {
		if d.PluginID != pluginID && d.Access != domain.AccessRead {
			return true
		}
	}
	return false
}

// ReloadPlugins 重载插件（plugin.reload）：pluginId 为空重载全部第三方
// （整装配重建、双代切换）；指定第三方 id 时走同一条重建路径；指定随包插件时
// 原地重建其 Runtime（同代，登记表必须与已提交贡献一致）。返回重载后的视图。
func (s *Session) ReloadPlugins(ctx context.Context, pluginID string) ([]PluginView, error) {
	pluginID = strings.TrimSpace(pluginID)
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if pluginID != "" {
		for _, sc := range s.baseScripts {
			if sc.Manifest == nil || sc.Manifest.ID != pluginID {
				continue
			}
			asm := s.Assembly()
			if asm == nil {
				return nil, errors.New("cli: 会话没有装配结果")
			}
			inst, ok := asm.Plugin(pluginID)
			if !ok {
				return nil, fmt.Errorf("cli: 插件 %s 当前未装配（可能已被禁用）", pluginID)
			}
			if err := inst.Reload(ctx); err != nil {
				return nil, err
			}
			// 审计（09 §10：插件重载）：随包插件原地重建同一代 Runtime。
			s.audit(ctx, "plugin.reload", "", pluginID, map[string]any{"pluginId": pluginID, "scope": "plugin"})
			return s.PluginViews(ctx), nil
		}
		found := false
		for _, p := range s.discoveredSnapshot() {
			if p.ID() == pluginID {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("cli: 没有发现插件 %q（先安装到 <workspace>/.adago/plugins/<id> 再重载）", pluginID)
		}
	}
	if err := s.reloadAssembly(ctx); err != nil {
		return nil, err
	}
	// 审计（09 §10：插件重载）：pluginId 为空表示整装配重建。
	s.audit(ctx, "plugin.reload", "", pluginID, map[string]any{"pluginId": pluginID, "scope": scopeOf(pluginID)})
	return s.PluginViews(ctx), nil
}

// scopeOf 描述重载范围（审计 detail 用）。
func scopeOf(pluginID string) string {
	if pluginID == "" {
		return "all"
	}
	return "plugin"
}

// SetPluginEnabled 启用/禁用插件（plugin.setEnabled）：先落 plugin_meta，再双代重载。
//
//   - 第三方禁用后从目录摘除（INV-11：禁用即能力消失），启用后恢复；
//   - 随包插件禁用后本次装配容忍缺席，但禁用唯一策略插件且目录里仍有写/执行工具时
//     直接拒绝（失败关闭，5.4）；
//   - 重载失败时回滚 meta 并返回错误（运行中的旧代保持原样）。
func (s *Session) SetPluginEnabled(ctx context.Context, pluginID string, enabled bool) (PluginView, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return PluginView{}, errors.New("cli: 需要 pluginId")
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	script, _, fp, ok := s.lookupPlugin(pluginID)
	if !ok {
		return PluginView{}, fmt.Errorf("cli: 没有发现插件 %q", pluginID)
	}
	if !enabled && hasToolPolicyRole(script.Manifest) && s.hasNonReadToolsExcluding(ctx, pluginID) {
		return PluginView{}, domain.NewError(domain.CodePluginDenied,
			fmt.Sprintf("插件 %s 是审批策略插件，目录里仍有写/执行工具，禁用会被拒绝（失败关闭，5.4）", pluginID))
	}
	meta := s.listPluginMeta(ctx)
	prev, hadPrev := meta[pluginID]
	prevEnabled := true
	prevUserSet := false
	if hadPrev {
		prevEnabled = prev.Enabled
		prevUserSet = prev.UserSet
	}
	rec := sqlite.PluginMetaRecord{
		ID: pluginID, Version: manifestVersionOf(script), Enabled: enabled, UserSet: true, Fingerprint: fp,
	}
	if err := s.store.UpsertPluginMeta(ctx, rec); err != nil {
		return PluginView{}, err
	}
	if err := s.reloadAssembly(ctx); err != nil {
		// 回滚 meta，与仍在运行的旧代保持一致。
		rollback := rec
		rollback.Enabled = prevEnabled
		rollback.UserSet = prevUserSet
		rollback.LastError = prev.LastError
		_ = s.store.UpsertPluginMeta(ctx, rollback)
		return PluginView{}, err
	}
	// 审计（09 §10：插件禁用/启用）：成功才记，失败路径上面已回滚 meta。
	s.audit(ctx, "plugin.changed", "", pluginID, map[string]any{"enabled": enabled})
	return s.viewFor(ctx, pluginID), nil
}

// manifestVersionOf 取清单版本（缺清单时用占位版本，plugin_meta 需要非空行）。
func manifestVersionOf(sc host.Script) string {
	if sc.Manifest == nil || sc.Manifest.Version == "" {
		return "0.0.0"
	}
	return sc.Manifest.Version
}

// ListPluginMetaOffline 用只读连接读取插件启用状态（adag plugin list 用）。
// 数据库还不存在时返回空表：插件目录可以先安装、后启动。
func ListPluginMetaOffline(ctx context.Context, workspace, dataDir string) (map[string]sqlite.PluginMetaRecord, error) {
	abs, dir, err := paths(workspace, dataDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(sqlite.StateDBPath(dir)); err != nil {
		return map[string]sqlite.PluginMetaRecord{}, nil
	}
	r, err := sqlite.OpenReadOnly(ctx, sqlite.Config{Workspace: abs, DataDir: dir})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	rows, err := r.ListPluginMeta(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]sqlite.PluginMetaRecord{}
	for _, rec := range rows {
		out[rec.ID] = rec
	}
	return out, nil
}

// interactionStore 把 coreproto 的持久化回调接到 SQLite（04 interactions 表）。
type interactionStore struct{ st *sqlite.Store }

func (i interactionStore) SavePending(ctx context.Context, req ports.InteractionRequest) error {
	return i.st.CreateInteraction(ctx, req)
}

func (i interactionStore) Resolve(ctx context.Context, _ string, rec coreproto.InteractionRecord) error {
	_, err := i.st.ResolveInteraction(ctx, rec.ThreadID, rec.RequestID, rec.Status, rec.Value)
	return err
}

func (i interactionStore) ListPending(ctx context.Context, threadID string) ([]ports.InteractionRequest, error) {
	rows, err := i.st.ListPendingInteractions(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]ports.InteractionRequest, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.InteractionRequest)
	}
	return out, nil
}

// runStore 把 engine.RunStore 接到 SQLite（04 runs 表）。
type runStore struct {
	st    *sqlite.Store
	model func(threadID string) ports.ModelRef
}

func (r runStore) StartRun(ctx context.Context, rec engine.RunRecord) error {
	_, err := r.st.StartRun(ctx, sqlite.Run{
		ID: rec.ID, ThreadID: rec.ThreadID, Status: sqlite.RunRunning, StartedAt: rec.StartedAt,
	})
	return err
}

func (r runStore) EndRun(ctx context.Context, rec engine.RunRecord) error {
	usage, err := json.Marshal(rec.Usage)
	if err != nil {
		return err
	}
	if err := r.st.FinishRun(ctx, rec.ID, rec.Status, rec.ErrorCode, usage); err != nil {
		return err
	}
	if rec.Usage == (domain.Usage{}) {
		return nil
	}
	// 把本轮用量另行记入 04 §5.7 的 usage 表，usage.get 的线程/工作区聚合
	// 依赖它（runs.usage_json 只是轮次快照）。provider/model 取当前生效模型。
	provider, model := "unknown", "unknown"
	if r.model != nil {
		ref := r.model(rec.ThreadID)
		if ref.ProviderID != "" {
			provider = ref.ProviderID
		}
		if ref.ModelID != "" {
			model = ref.ModelID
		}
	}
	return r.st.RecordUsage(ctx, sqlite.UsageRecord{
		ThreadID: rec.ThreadID, Provider: provider, Model: model,
		InputTokens:  rec.Usage.InputTokens,
		OutputTokens: rec.Usage.OutputTokens,
		CacheTokens:  rec.Usage.CacheTokens,
		CostMicros:   rec.Usage.CostMicros,
		CreatedAt:    rec.EndedAt,
	})
}
