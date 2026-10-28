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

	mu        sync.Mutex
	threadID  string
	events    io.WriteCloser
	eventsBuf *bufio.Writer
	unsub     []func()

	in *bufio.Reader
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
		in: bufio.NewReader(cfg.In),
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
		RunStore:  runStore{st: store},
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
	if s.modelErr != nil {
		return domain.TerminalError, s.modelErr
	}
	if strings.TrimSpace(prompt) == "" {
		return domain.TerminalError, errors.New("cli: 输入为空")
	}
	if threadID == "" || runID == "" {
		return domain.TerminalError, errors.New("cli: 轮次需要 threadId 与 runId")
	}
	runCtx := withThread(ctx, threadID)
	return engine.Run(runCtx, s.deps, s.newTurnSpec(threadID, runID, prompt)), nil
}

// newTurnSpec 把产品策略与当前模型合成引擎输入。
func (s *Session) newTurnSpec(threadID, runID, prompt string) engine.TurnSpec {
	spec := engine.TurnSpec{
		ThreadID: threadID,
		RunID:    runID,
		UserInput: domain.Message{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Type: domain.PartText, Text: prompt}},
		},
		Model:           s.modelRef(),
		LoopRepeatLimit: s.asm.Product.Policies.LoopRepeatLimit,
		LoopAbortLimit:  s.asm.Product.Policies.LoopAbortLimit,
		ParallelTools:   s.asm.Product.Policies.ParallelTools,
		TurnTimeoutMS:   s.asm.Product.Policies.TurnTimeoutMS,
		MaxTurnTokens:   s.asm.Product.Policies.MaxTurnTokens,
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

// modelRef 由产品/CLI 配置与 provider 声明的能力合成。
func (s *Session) modelRef() ports.ModelRef {
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
	ref := ports.ModelRef{
		ProviderID: providerID, ModelID: modelID,
		ContextWindow: 128000, SupportsTools: len(s.asm.Tools.List(context.Background())) > 0,
	}
	if s.asm.Providers != nil {
		ids := s.asm.Providers.Providers()
		found := false
		for _, id := range ids {
			if id != providerID {
				continue
			}
			found = true
			for _, m := range s.asm.Providers.Models(id) {
				if m.ID == modelID {
					ref.ContextWindow = int64(m.ContextWindow)
					ref.MaxOutput = int64(m.MaxOutput)
					ref.Input = append([]string(nil), m.Input...)
					ref.SupportsTools = m.SupportsTools
					ref.SupportsThinking = m.SupportsThinking
				}
			}
		}
		if !found && len(ids) == 1 {
			// 产品可能直接写插件 id；桥支持这种写法，但能力仍来自唯一 provider。
			providerID = ids[0]
			ref.ProviderID = providerID
		}
	}
	return ref
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
type runStore struct{ st *sqlite.Store }

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
	return r.st.FinishRun(ctx, rec.ID, rec.Status, rec.ErrorCode, usage)
}
