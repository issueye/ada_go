// Package cli 是 adag 的会话侧命令实现：把「产品装配 → 引擎 → SQLite → 事件」接成
// 可脚本化的 CLI（开发计划 M1-G）。
//
// 它不认识官方插件包：插件脚本由 cmd/adag 注入（边界检查禁止 internal/ import official/）。
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/coreproto"
	"github.com/adag/ada-go/internal/agent_core/engine"
	"github.com/adag/ada-go/internal/agent_core/plugins/discovery"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/agent_core/plugins/provider"
	"github.com/adag/ada-go/internal/common/adapters"
	"github.com/adag/ada-go/internal/common/adapters/sqlite"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
	"github.com/adag/ada-go/internal/common/toolkit"
)

// Config 是打开一次 CLI 会话的全部输入。
type Config struct {
	Workspace string
	DataDir   string
	Product   string
	Provider  string
	Model     string
	BaseURL   string
	// AllowHosts 已废弃：出网 host 黑白名单暂未启用（后续以插件形式实现）。
	// 字段保留是为了兼容旧的组合根赋值，值会被忽略。
	AllowHosts []string
	// Fake 使用确定性回显模型，不需要 API key。
	Fake bool
	// ThreadID 非空时打开后直接切到该线程。
	ThreadID string
	// Interactive 为 true 时把交互/提问转到终端（y/n）；false 时按无 UI 处理。
	Interactive bool
	// HasUI 表示「这个会话背后有界面可以回答交互」：CLI 交互模式是终端，
	// Gateway 是网页里的交互托盘。它决定 ctx.ui.available 与 broker 的 HasUI，
	// 与 Interactive（是否在终端读 stdin）分开——Core 进程没有终端，但有网页。
	HasUI bool
	// Scripts 是官方插件包（由 cmd 注入）。
	Scripts []host.Script
	// Assembler 是组合根注入的装配入口（corecontracts.Assembler）；nil 时 Open 失败。
	Assembler corecontracts.Assembler
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
	// ProvidersPath 是供应商配置位置；空时按数据目录决定：
	//   - 显式传 DataDir：<DataDir>/providers.json（测试/隔离数据目录）；
	//   - 默认数据目录：~/.adago/providers.json（切换工作区共享同一套供应商）。
	ProvidersPath string
	// PluginStatePath 是全局插件启停状态（plugins.json）的位置；空时取
	// ~/.adago/plugins.json（ADAGO_HOME 优先）。测试用显式路径隔离真实主目录。
	PluginStatePath string
	// Log 接收插件日志；nil 时丢弃。
	Log func(level, msg string, fields map[string]any)
	// Debug 开启出网调试：把每次 provider/ctx.fetch 请求与响应摘要（密钥已脱敏）
	// 写到 Err，用于定位端点、请求体与状态码问题。
	Debug bool
}

// Session 是已装配的 Core 侧 CLI 会话。
type Session struct {
	cfg       Config
	store     *sqlite.Store
	asm       corecontracts.Assembly
	assembler corecontracts.Assembler
	emitter   *coreproto.Emitter
	broker    *coreproto.Broker
	executor  *toolkit.Executor
	deps      engine.Deps
	ids       *adapters.ULIDGen
	clock     adapters.SystemClock
	model     ports.ModelClient
	modelErr  error
	secrets   map[string]string

	// M5 检查点账目：changes 是工具执行的内存账本（回执 chk_ id、写串行、
	// 热重载跨代复用）；changeStore 是数据目录 changes/ 下的持久化账目
	// （文件备份 + manifest，change.list / change.revert 的真源）。
	changes     *toolkit.ChangeQueue
	changeStore *changeStore

	// M3 插件平台：随包插件 base（Open 时冻结，热重载的合并基准）、产品声明、
	// 装配期宿主依赖（corecontracts.Deps 原样复用）、第三方发现快照与失败记录。
	// baseScripts/cfg 在 Open 之后只读；discovered/failedPlugins 变更持 s.mu。
	baseScripts []host.Script
	product     *coreapp.Product
	appDeps     corecontracts.Deps
	// httpBridge 是插件出网桥；供应商配置变更时无需重建（当前不做 host 黑白名单）。
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
	// dispatchClient 是 ctx.dispatch 的后端（网关控制面）；nil 表示本次部署
	// 没有接入网关调度层（CLI 直连），此时插件拿不到 ctx.dispatch。
	dispatchClient *gatewayDispatch
	// processesClient 是 ctx.processes 的后端（网关进程管理控制面）；nil 表示
	// 本次部署没有接入网关（CLI 直连），此时插件拿不到 ctx.processes。
	processesClient *gatewayProcesses
	// 供应商配置（管理页）：文件在数据目录，改动后直接更新当前装配的 Registry。
	providerStore   providerConfigStore
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
type PolicyUpdate = corecontracts.PolicyUpdate

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
	if cfg.Assembler == nil {
		return nil, errors.New("cli: 需要组合根注入 Assembler")
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
	// 工作区本地数据目录：TODO 等插件状态放在 .adago/ 下。
	// 只读工作区不阻塞启动；真正需要写入时会给出明确错误。
	if mkErr := os.MkdirAll(filepath.Join(abs, ".adago"), 0o755); mkErr != nil {
		fmt.Fprintf(cfg.Err, "adag: 创建 %s 失败（TODO 等插件状态可能需要它）: %v\n", filepath.Join(abs, ".adago"), mkErr)
	}
	var debugLog *host.DebugLog
	if cfg.Debug {
		debugLog = host.NewDebugLog(cfg.Err)
	}
	dataDir := cfg.DataDir
	explicitDataDir := dataDir != ""
	if dataDir == "" {
		dataDir, err = sqlite.DefaultDataDir(abs)
		if err != nil {
			return nil, err
		}
	}
	cfg.DataDir = dataDir
	providersPath := strings.TrimSpace(cfg.ProvidersPath)
	if providersPath == "" {
		if explicitDataDir {
			providersPath = filepath.Join(dataDir, "providers.json")
		} else {
			providersPath = defaultProvidersPath(dataDir)
		}
	}
	cfg.ProvidersPath = providersPath

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
		assembler:     cfg.Assembler,
		providerStore: newProviderConfigStore(providersPath),
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
	hc.ConfigDir = filepath.Join(filepath.Dir(s.cfg.ProvidersPath), "config")
	hc.OnConfigChange = func(ns string) {
		if ns == "provider-config" {
			s.RefreshProviderConfigs()
		}
	}
	// 只读事件投影（events.read）：绑定当前线程，见 plugin_events.go。
	hc.Events = s.queryPluginEvents
	// 原始用量行（events.read）：基座只给每轮计数，聚合由插件自己做。
	hc.Usage = s.queryPluginUsage
	// 通用轮次触发桥（turn.invoke，仅 builtin）：到期任务等由插件提交提示词。
	hc.TurnInvoke = s.invokeTurnFromPlugin
	// 发现式工具宿主桥（tool.discovery）：目录只读投影 + 内层执行链。
	hc.Catalog = func(ctx context.Context) ([]domain.Descriptor, error) {
		if s.asm == nil {
			return nil, nil
		}
		return s.asm.ToolCatalog().List(ctx), nil
	}
	hc.InvokeTool = func(ctx context.Context, meta host.CallMeta, call domain.ToolCall) (domain.Result, error) {
		return engine.InvokeTool(ctx, s.deps, engine.TurnSpec{ThreadID: meta.ThreadID, RunID: meta.RunID}, call)
	}
	// 技能通道（6.7）由 Assembler 按产品 capabilities.skills 注入：
	// ctx.skills 只给 name/description/path；正文由模型用 read_file 按路径读取。
	// 供应商管理（model.admin）：目录只读、不含密钥；切换复用 model.set 的解析。
	hc.Providers = s.providerCatalog
	hc.SelectModel = s.selectModelFromPlugin
	// 插件安装（plugin.admin）：骨架统一写入全局插件根 ~/.adago/plugins/<id>/。
	hc.InstallPlugin = s.installPluginGlobally
	// 插件健康检查（plugin.admin）：诊断报告（发现/启停/装配/入口预加载）。
	hc.CheckPlugin = s.checkPlugin
	hc.RemovePlugin = s.removePlugin
	// M5 检查点：宿主 fs 写入前把被覆盖文件备份进持久化账目（changes/）。
	s.changes = toolkit.NewChangeQueue()
	s.changeStore = newChangeStore(dataDir)
	hc.Checkpoint = s.checkpointWrite
	// 内部发送桥（dispatch.send，仅 builtin）：网关注入控制面地址/令牌后才有。
	s.wireDispatch(&hc)
	// 进程监督桥（process.supervise，仅 builtin）：同一组控制面地址/令牌。
	s.wireProcesses(&hc)
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
	s.appDeps = corecontracts.Deps{
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
	// 装配完成后再恢复持久化模型：ResolveModel 需要 provider 目录。
	s.restoreModelSelections(ctx)

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

// ReadLine 读一行输入（与交互提示共用同一个缓冲 reader，避免吞掉用户的下一行）。
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
	return s.RunInThread(ctx, s.CurrentThread(), runID, prompt, nil)
}

// RunInThread 在指定线程上跑一轮（不改变 CLI 的当前线程）。
//
// Core RPC 用它支持多线程；线程身份经 ctx 传给插件 State，避免并发轮次互相串台。
func (s *Session) RunInThread(ctx context.Context, threadID, runID, prompt string, images []domain.Part) (domain.TerminalReason, error) {
	return s.runInThread(ctx, threadID, runID, prompt, images, nil)
}

// RunInThreadReady 与 RunInThread 相同，但在登记活跃轮次之后关闭 ready，
// 让 corerpc 能等轮次真正可被 steer 之后再返回 turn.start（避免紧接的
// turn.steer 被误判成没有运行中轮次）。ready 为 nil 时与 RunInThread 等价。
func (s *Session) RunInThreadReady(ctx context.Context, threadID, runID, prompt string, images []domain.Part, ready chan<- struct{}) (domain.TerminalReason, error) {
	return s.runInThread(ctx, threadID, runID, prompt, images, ready)
}

func (s *Session) runInThread(ctx context.Context, threadID, runID, prompt string, images []domain.Part, ready chan<- struct{}) (domain.TerminalReason, error) {
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
	return engine.Run(runCtx, deps, s.newTurnSpec(threadID, runID, prompt, images)), nil
}

// newTurnSpec 把产品策略与当前模型合成引擎输入。
//
// 策略与模型覆盖都在 Session 内以锁保护；这里只读一次快照，避免并发调用
// workspace.updatePolicy/model.set 时把半更新的值送进轮次。
func (s *Session) newTurnSpec(threadID, runID, prompt string, images []domain.Part) engine.TurnSpec {
	policies := s.Policies()
	spec := engine.TurnSpec{
		ThreadID: threadID,
		RunID:    runID,
		// 图片部件跟在正文后面：双门校验（产品能力 + 模型 input）在 turn.start 做。
		UserInput: domain.Message{
			Role: domain.RoleUser,
			Parts: append([]domain.Part{{Type: domain.PartText, Text: prompt}},
				images...),
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

// Product 返回已加载的产品声明。
func (s *Session) Product() *coreapp.Product {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.product
}

// Assembly 返回装配结果（插件、工具、provider 目录）。
// 热重载会在锁内整体替换这份结果；拿到的指针对应的目录已冻结，可安全只读。
func (s *Session) Assembly() corecontracts.Assembly {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asm
}

// Plugins 返回当前装配的插件实例（Core RPC 的 hello/debug 统计用）。
func (s *Session) Plugins() []*host.Instance {
	asm := s.Assembly()
	if asm == nil {
		return nil
	}
	return append([]*host.Instance(nil), asm.PluginInstances()...)
}

// Tools 返回当前装配的工具描述符（tool.list 数据源）。
func (s *Session) Tools(ctx context.Context) []domain.Descriptor {
	asm := s.Assembly()
	if asm == nil || asm.ToolCatalog() == nil {
		return nil
	}
	return asm.ToolCatalog().List(ctx)
}

// Store 返回工作区存储句柄（Core RPC 的线程/entry 查询复用它）。
func (s *Session) Store() *sqlite.Store { return s.store }

// ContextBreakdown 返回提示词侧的两块上下文开销（系统提示、工具声明）的估算
// token；消息窗口由调用方从 head 路径累加。三段合计才是发给模型的那次请求。
//
// 与引擎同一套估算与装配（engine.SystemPromptTokens / ToolSchemaTokens），
// 用户补上真实 contextWindow 后，环上的占比和压缩水位是同一个数。
func (s *Session) ContextBreakdown(ctx context.Context, threadID string) (systemTokens, toolTokens int) {
	s.mu.Lock()
	deps := s.deps
	s.mu.Unlock()
	spec := engine.TurnSpec{ThreadID: threadID, Model: s.ModelRefFor(threadID)}
	return engine.SystemPromptTokens(ctx, spec, deps.Prompts, deps.Skills), engine.ToolSchemaTokens(ctx, deps.Tools)
}

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
	// host 黑白名单暂未启用（后续以插件形式实现，见 AGENTS.md）：
	// 出网不限目标主机；协议/上限/超时由桥兜底。
	bcfg := host.HTTPBridgeConfig{DebugLog: debugLog}
	b, err := host.NewHTTPBridge(bcfg)
	return b, err
}

// httpBridge 让调用方能在关闭时释放连接池；领域侧只依赖 host.HTTPBridge。
type httpBridge interface {
	host.HTTPBridge
	Close()
}

// loadSecrets 读取 ~/.adago/secrets.toml（形如 some_key = "sk-..."）。
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
	if v := strings.TrimSpace(s.secrets[name]); v != "" {
		return v, true
	}
	return "", false
}

// 拆分说明（纯搬移）：供应商配置与模型选择见 session_provider.go，steering 队列与轮次
// 活跃计数见 session_steering.go，终端交互替身见 session_interact.go，装配与热重载见
// session_assembly.go，插件视图/启停/重载见 session_pluginview.go，SQLite 适配见 session_stores.go。
