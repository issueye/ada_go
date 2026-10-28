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
	"net/url"
	"os"
	"path/filepath"
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
	// Scripts 是官方插件包（由 cmd 注入）。
	Scripts []host.Script
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
		steerQueues: map[string][]domain.Message{},
		turnActive:  map[string]int{},
		in:          bufio.NewReader(cfg.In),
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

	httpBridge, err := buildHTTPBridge(cfg)
	if err != nil {
		return cleanup(err)
	}
	hc := host.Context{
		Workspace: abs,
		Secret:    s.secret,
		Log:       cfg.Log,
		HTTP:      httpBridge,
	}
	hc.State = s.stateOp
	deps := app.Deps{
		Context:  hc,
		Secrets:  func(name string) bool { _, ok := s.secret(name); return ok },
		Settings: cfg.Settings,
		BaseURL:  cfg.BaseURL,
	}
	var asm *app.Assembly
	if len(cfg.Scripts) == 0 {
		// 默认走嵌入的官方插件包；组合根是唯一知道 official/ 的包。
		asm, err = app.LoadOfficial(ctx, cfg.Product, deps)
	} else {
		p, perr := coreapp.LoadProduct(cfg.Product)
		if perr != nil {
			httpBridge.Close()
			return cleanup(perr)
		}
		asm, err = app.Assemble(ctx, p, cfg.Scripts, deps)
	}
	if err != nil {
		httpBridge.Close()
		return cleanup(err)
	}
	s.asm = asm

	s.emitter = coreproto.NewEmitter(clock)
	broker, err := coreproto.NewBroker(coreproto.BrokerConfig{
		Sink:  s.emitter,
		Clock: clock,
		IDs:   ids,
		Store: interactionStore{st: store},
		HasUI: func(context.Context, string) bool { return cfg.Interactive },
	})
	if err != nil {
		return cleanup(err)
	}
	s.broker = broker
	s.executor, err = toolkit.NewExecutor(toolkit.ExecutorConfig{
		Catalog:   asm.Tools,
		Invoker:   asm.ToolInvoker(),
		Clock:     clock,
		HasPolicy: asm.Runtime.PolicyPluginActive,
	})
	if err != nil {
		return cleanup(err)
	}

	// 模型选择：测试覆盖 > --fake > provider 插件。
	switch {
	case cfg.ModelClient != nil:
		s.model = cfg.ModelClient
	case cfg.Fake:
		s.model = devfake.New()
	case asm.ProviderCount() > 0:
		s.model = provider.NewRetry(asm.Providers)
	default:
		s.modelErr = errors.New("没有可用的 provider：启用 provider 插件或使用 --fake")
	}

	s.deps = engine.Deps{
		Clock: clock, IDs: ids, Events: s.emitter, Session: store, Model: s.model,
		Tools: asm.Tools, Interactions: broker, Hooks: asm.Hooks,
		Runner:    s.executor,
		Compactor: &engine.ModelCompactor{Model: s.model, Clock: clock, IDs: ids},
		RunStore:  runStore{st: store, model: s.ModelRefFor},
		Steer:     s,
		HasPolicy: func() bool { return asm.Runtime.PolicyPluginActive },
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
	if s.asm != nil {
		if err := s.asm.Close(ctx); err != nil {
			errs = append(errs, err)
		}
		s.asm = nil
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
	if s.modelErr != nil {
		return domain.TerminalError, s.modelErr
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
	if s.modelErr != nil {
		return domain.TerminalError, s.modelErr
	}
	runCtx := withThread(ctx, threadID)
	return engine.Run(runCtx, s.deps, s.newTurnSpec(threadID, runID, prompt)), nil
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
func (s *Session) Product() *coreapp.Product { return s.asm.Product }

// Assembly 返回装配结果（插件、工具、provider 目录）。
func (s *Session) Assembly() *app.Assembly { return s.asm }

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
	if s.asm != nil && s.asm.Product != nil {
		return s.asm.Product.Policies
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
	if providerID == "" || modelID == "" || s.asm == nil || s.asm.Providers == nil {
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
	ref := ports.ModelRef{
		ProviderID: providerID, ModelID: modelID,
		ContextWindow: 128000, SupportsTools: len(s.asm.Tools.List(context.Background())) > 0,
	}
	if s.asm != nil && s.asm.Providers != nil {
		ids := s.asm.Providers.Providers()
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
	thread := threadFrom(ctx)
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

func buildHTTPBridge(cfg Config) (httpBridge, error) {
	allow := append([]string(nil), cfg.AllowHosts...)
	if cfg.BaseURL != "" {
		if u, err := urlHost(cfg.BaseURL); err == nil && u != "" {
			allow = append(allow, u)
		}
	}
	if len(allow) == 0 {
		allow = []string{"api.openai.com"}
	}
	b, err := host.NewHTTPBridge(host.HTTPBridgeConfig{AllowHosts: allow})
	return b, err
}

// httpBridge 让调用方能在关闭时释放连接池；领域侧只依赖 host.HTTPBridge。
type httpBridge interface {
	host.HTTPBridge
	Close()
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
