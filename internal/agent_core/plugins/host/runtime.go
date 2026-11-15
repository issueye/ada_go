package host

// 本文件实现「一个插件一个 Runtime、一条专用 goroutine」的生命周期（06 §6.4）：
//
//   - Instance 持有 moejs Runtime 与 eventloop.Loop，两者只在 owner goroutine 上被触碰；
//     外部调用经带缓冲 channel 串行进入，脚本值（moejs.Value）绝不跨 goroutine 传递，
//     跨边界只走 JSON 字节（05 §6.3、06 §6.4）。
//   - 预算按调用类型分档（Budgets）；等待宿主长调用（ui/state/http/secret）期间暂停脚本计时，
//     由各自的期限负责（06 §6.4「否则 5s/200ms 的脚本上限会误杀正常的长任务」）。
//   - 超时/取消用 loop.Interrupt 投递，脚本不可 catch（INV-8）；
//     四类失败分别映射 plugin_timeout / plugin_crashed（08 §8.5、06 §6.4 回收策略）。
//   - 脏 Runtime：一次失败之后标记 dirty，下一次调用前重建（重跑入口工厂、按登记表复用槽位）。
//   - 连续 ConsecutiveFailureLimit 次失败自动禁用（06 §6.4）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/MoeForge/moejs/eventloop"

	"github.com/adag/ada-go/internal/agent_core/plugins"
	"github.com/adag/ada-go/internal/common/domain"
	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/common/ports"
)

// 宿主投递给 Runtime 的中断哨兵：脚本看不到、也无法 catch（Interrupt 不是异常）。
var (
	errInterruptTimeout = errors.New("host: 脚本预算耗尽")
	errInterruptCancel  = errors.New("host: 调用已取消")
	errInterruptClosed  = errors.New("host: 插件运行时已关闭")
)

// 对外错误：调用方据此区分 plugin_timeout / plugin_crashed / 取消。
var (
	// ErrTimeout 表示本次调用超出脚本预算（06 §6.4：超时记 plugin_timeout 并标脏）。
	ErrTimeout = errors.New("host: 插件调用超时")
	// ErrCrashed 表示 Runtime 被弄脏（超内存、引擎内部错误、脚本 panic）：需重建。
	ErrCrashed = errors.New("host: 插件运行时崩溃，需重建")
	// ErrDisabled 表示插件因连续失败被自动禁用（06 §6.4）。
	ErrDisabled = errors.New("host: 插件已被禁用")
	// ErrClosed 表示插件运行时已关闭。
	ErrClosed = errors.New("host: 插件运行时已关闭")
	// ErrUnsettled 表示脚本返回的 Promise 永不结算（宿主没有挂死：循环已空转结束）。
	ErrUnsettled = errors.New("host: 脚本 Promise 未结算")
)

// 宿主桥的默认期限（06 §6.4：交互 300s、命令 120s、provider 由三档期限负责）。
const defaultHostCallTimeout = domain.DefaultInteractionTimeout

// Options 是装配期注入的实例配置。
type Options struct {
	// Budgets 为零值时取 06 §6.4 的默认档位。
	Budgets Budgets
	// MemoryLimit 为 0 时取 MemoryLimit 常量（64MB）。
	MemoryLimit int64
	// Targets 是贡献结算的目标；某一类有贡献但缺注册表 → 装配失败（不静默丢弃）。
	Targets Targets
	// OnWarning 接收「运行期二次注册」等非致命警告（6.5）；nil 时只记在实例里。
	OnWarning func(string)
}

// Instance 是一个已加载插件的宿主实例，实现 Plugin（06 §6.4）。
type Instance struct {
	// 不可变字段。
	id      string
	script  Script
	caps    *CapabilitySet
	hc      Context
	opts    Options
	budgets Budgets

	requests   chan *call
	quit       chan struct{}
	ownerDone  chan struct{}
	closed     atomic.Bool
	generation atomic.Int64

	// mu 保护跨 goroutine 可读的状态。owner 在运行 JS 期间绝不持有它：
	// AddLiveRoots 的回调可能在脚本执行中间被调用，那里不允许等待 JS 持有过的锁。
	mu         sync.Mutex
	dirty      bool
	failures   int
	disabled   bool
	disabledAt time.Time     // 最近一次熔断时刻（冷却计时起点）
	cooldown   time.Duration // 当前冷却时长：0 表示尚未熔断过；成功调用归零，再熔断翻倍
	probing    bool          // 半开探测中：探测后的首次失败立即重新熔断（06 §6.4）
	lastError  string
	warnings   []string
	contrib    *Contribution
	loopPtr    *mojsLoop
	timerStop  context.CancelFunc

	// recoverMu 串行化半开探测：冷却到点后多个并发调用只做一次重建。
	recoverMu sync.Mutex

	// 以下字段只属于 owner goroutine（含 AddLiveRoots 回调，它也在该 goroutine 上跑）。
	rt          *mojsRuntime
	loop        *mojsLoop
	handlers    []moejs.Value
	slots       map[string]int
	expectSlots map[string]int // 非空表示重建：登记表必须与已提交贡献一致（06 §6.5）
	roots       []moejs.Value
	removeRoots func()
	loader      *moduleLoader
	cur         *call
	staging     *Staging
	frozen      bool
}

var _ Plugin = (*Instance)(nil)

// Load 创建 Runtime、加载入口、执行工厂并结算贡献（06 §6.5 两阶段装配的前半段）。
//
// 任何一步失败都不留半个实例：内部先 Close，错误文案点名缺失项（12 §6.14）。
func Load(ctx context.Context, script Script, caps *CapabilitySet, hc Context, opts Options) (*Instance, error) {
	if InstallCapabilities == nil {
		// 安装挂载点没接线时拒绝加载，而不是装配出一个缺 fs/child_process 的半成品。
		return nil, fmt.Errorf("host: InstallCapabilities 未赋值（caps.go 未落地），拒绝加载插件 %s", script.PluginID())
	}
	if err := script.Validate(); err != nil {
		return nil, err
	}
	if caps == nil {
		return nil, fmt.Errorf("host: 插件 %s 缺少能力授予集合", script.PluginID())
	}
	if hc.PluginID != "" && hc.PluginID != script.PluginID() {
		return nil, fmt.Errorf("host: 宿主上下文的 pluginId %q 与清单 id %q 不一致", hc.PluginID, script.PluginID())
	}
	budgets := opts.Budgets.withDefaults()
	if script.Manifest != nil && script.Manifest.Backend != nil && script.Manifest.Backend.InitTimeoutMS > 0 {
		budgets.Factory = time.Duration(script.Manifest.Backend.InitTimeoutMS) * time.Millisecond
	}
	p := &Instance{
		id: script.PluginID(), script: script, caps: caps, hc: hc, opts: opts, budgets: budgets,
		requests:  make(chan *call, budgets.Queue),
		quit:      make(chan struct{}),
		ownerDone: make(chan struct{}),
		handlers:  make([]moejs.Value, 1, 32), // 槽位从 1 开始：0 表示「没有脚本句柄」（contrib.Validate）
		slots:     map[string]int{},
	}
	go p.serve()

	initCall := newCall(callInit, 0, "entry", nil, p.baseContext(ctx), ctx)
	if o := p.roundTrip(ctx, initCall); o.err != nil {
		_ = p.Close(context.Background())
		return nil, o.err
	}
	p.startTimers()
	return p, nil
}

// startTimers 为 agent.every 登记的每条周期任务起一个 goroutine。
//
// 每次调用都用新的 Context（不复用插件在路由里捕获的 ctx），并且有独立
// 超时；插件 Close 时取消。它只是通用定时器，不解释 cron 表达式或任务。
func (p *Instance) startTimers() {
	c := p.Contribution()
	if c == nil || len(c.Timers) == 0 {
		return
	}
	base, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.timerStop = cancel
	p.mu.Unlock()
	for _, t := range c.Timers {
		interval := time.Duration(t.IntervalMS) * time.Millisecond
		handler := t.Handler
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-base.Done():
					return
				case <-p.quit:
					return
				case <-ticker.C:
				}
				ctx, stop := context.WithTimeout(base, 10*time.Second)
				_, err := p.CallRoute(ctx, handler, json.RawMessage(`{"timer":true}`), p.baseContext(ctx))
				stop()
				if err != nil && !p.closed.Load() && p.hc.Log != nil {
					p.hc.Log("warn", "host.timer.failed", map[string]any{
						"pluginId": p.id, "error": err.Error(),
					})
				}
			}
		}()
	}
}

// handler 取槽位里的脚本函数句柄。
func (p *Instance) handler(slot int) (moejs.Value, bool) {
	if slot <= 0 || slot >= len(p.handlers) {
		return moejs.Value{}, false
	}
	v := p.handlers[slot]
	return v, engine.IsCallable(v)
}

// bindHandler 把脚本函数登记进槽位表；只有 owner goroutine 调用。
func (p *Instance) bindHandler(slot int, fn moejs.Value) {
	for len(p.handlers) <= slot {
		p.handlers = append(p.handlers, moejs.Value{})
	}
	p.handlers[slot] = fn
	p.keepRoot(fn)
}

// keepRoot 把只有 Go 持有的脚本值登记为活跃根，让内存上限算得上它（12 §6.11 E-7）。
func (p *Instance) keepRoot(v moejs.Value) {
	p.roots = append(p.roots, v)
}

// shutdown 结束 owner goroutine：终止循环（丢弃全部定时器与任务）并释放引用。
func (p *Instance) shutdown() {
	p.teardown(errInterruptClosed)
	// 让还在排队的调用立刻拿到「已关闭」，不等它们自己的 ctx。
	// 这些调用不会再走 handle，所以要在这里同时结算与投递。
	for {
		select {
		case c := <-p.requests:
			err := p.closedError()
			c.reply(outcome{err: err})
			c.publish(err)
		default:
			return
		}
	}
}

// teardown 拆掉当前 Runtime/Loop 组合（重建与关闭共用）。
func (p *Instance) teardown(reason error) {
	p.mu.Lock()
	loop := p.loopPtr
	p.loopPtr = nil
	p.mu.Unlock()
	if loop != nil {
		if reason != nil {
			loop.Interrupt(reason)
		}
		// Terminate 丢掉全部待定定时器、任务与 hold：关闭不留异步工作（06 §6.4）。
		loop.Terminate()
	}
	if p.removeRoots != nil {
		p.removeRoots()
		p.removeRoots = nil
	}
	// 安装状态持有上游 node.API（工作区目录句柄与 worker 池）：不关就是每个插件一条泄漏。
	if p.rt != nil {
		_ = CloseCapabilities(p.rt)
	}
	p.rt = nil
	p.loop = nil
	p.cur = nil
	p.staging = nil
	p.frozen = false
	p.handlers = make([]moejs.Value, 1, 32)
	p.roots = nil
	p.loader = nil
}

// Close 实现 Plugin：取消全部异步工作并等待 owner goroutine 退出（不泄漏 goroutine）。
func (p *Instance) Close(ctx context.Context) error {
	if !p.closed.CompareAndSwap(false, true) {
		select {
		case <-p.ownerDone:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case <-p.quit:
	default:
		close(p.quit)
	}
	p.mu.Lock()
	stopTimers := p.timerStop
	p.timerStop = nil
	loop := p.loopPtr
	p.mu.Unlock()
	if stopTimers != nil {
		stopTimers()
	}
	if loop != nil {
		loop.Interrupt(errInterruptClosed)
	}
	select {
	case <-p.ownerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reload 是手动重载路径（06 §6.4「用户可以手动重载」）。
//
// M3 的双代热重载（新代构建完成后原子替换注册表、在途调用走旧代）不在本切片：
// 这里是同代原地重建，重建失败即降级并保留错误原因。
func (p *Instance) Reload(ctx context.Context) error {
	if p.closed.Load() {
		return p.closedError()
	}
	c := newCall(callReload, 0, "reload", nil, p.baseContext(ctx), ctx)
	return p.roundTrip(ctx, c).err
}

// runReload 重建 Runtime 并重跑入口工厂（同代）。
func (p *Instance) runReload(c *call) {
	if p.rt == nil {
		c.reply(outcome{err: fmt.Errorf("host: 插件 %s 尚未装配运行时", p.id)})
		return
	}
	err := p.rebuild()
	c.reply(outcome{err: err})
}

// rebuild 关闭当前 Runtime 并重建一个（06 §6.4：下一次调用前重新初始化）。
func (p *Instance) rebuild() error {
	p.teardown(errInterruptClosed)
	expect := p.committedSlots()
	if len(expect) == 0 {
		return fmt.Errorf("host: 插件 %s 没有已提交的贡献，无法重建运行时", p.id)
	}
	p.expectSlots = expect
	p.slots = map[string]int{}
	if err := p.openRuntime(); err != nil {
		p.rebuildFailed(err)
		return err
	}
	if err := p.installCapabilities(); err != nil {
		p.rebuildFailed(err)
		return err
	}
	if err := p.loadEntry(); err != nil {
		p.rebuildFailed(err)
		return err
	}
	st := newStaging(p)
	if err := p.runFactory(st); err != nil {
		p.rebuildFailed(err)
		return err
	}
	if err := st.matchSlots(expect); err != nil {
		p.rebuildFailed(err)
		return err
	}
	p.slots = st.keys
	p.frozen = true
	p.generation.Add(1)
	p.mu.Lock()
	p.dirty = false
	p.failures = 0
	p.disabled = false
	p.lastError = ""
	p.probing = false
	p.mu.Unlock()
	return nil
}

func (p *Instance) rebuildFailed(err error) {
	p.teardown(errInterruptClosed)
	p.mu.Lock()
	p.dirty = true
	p.lastError = err.Error()
	p.disabled = true
	// 重建失败同样按熔断簿记：重新计时并翻倍冷却（06 §6.4 自动恢复）。
	p.disabledAt = time.Now()
	if p.cooldown <= 0 {
		p.cooldown = DisabledCooldownInit
	} else {
		p.cooldown = min(2*p.cooldown, DisabledCooldownMax)
	}
	p.mu.Unlock()
	p.warn(fmt.Sprintf("插件 %s 运行时重建失败: %v", p.id, err))
}

// openRuntime 造 Runtime 与 Loop：全部边界在这里固定（06 §6.4 初始化）。
func (p *Instance) openRuntime() error {
	p.loader = newModuleLoader(p.script, p.caps)
	rt := moejs.NewRuntime(moejs.Options{
		Importer: p.loader.importer(),
		// 内存上限只到 MemoryLimit（64MB）；栈与调用深度的上限由 moejs 固定
		// （alpha.11 没有 StackSize/MaxCallDepth 选项，见 docs/11-structure-roadmap.md D-7）。
		MemoryLimit:        p.memoryLimit(),
		DisableDynamicCode: false,
		Locale:             "en-US",
	})
	p.rt = rt
	p.removeRoots = rt.AddLiveRoots(func(add func(moejs.Value)) {
		for _, v := range p.roots {
			add(v)
		}
	})
	loop, err := eventloop.New(rt, eventloop.Options{
		OnError: func(err error) {
			// 回调异常不结束循环（有 OnError），但必须可见（12 §6.11 可观测）。
			p.warn(fmt.Sprintf("插件 %s 的脚本回调异常: %s", p.id, p.exportError(err).Error()))
		},
	})
	if err != nil {
		return fmt.Errorf("host: 插件 %s 建 eventloop 失败: %w", p.id, err)
	}
	p.loop = loop
	p.mu.Lock()
	p.loopPtr = loop
	p.mu.Unlock()
	return nil
}

func (p *Instance) memoryLimit() int64 {
	if p.opts.MemoryLimit > 0 {
		return p.opts.MemoryLimit
	}
	return MemoryLimit
}

// Plugin 接口的热路径方法 -----------------------------------------------------

// ID 返回插件 id。
func (p *Instance) ID() string { return p.id }

// BaseContext 返回装配期注入的宿主上下文（含该插件的 Settings/Workspace/HTTP/Secret）。
// 适配器桥用它补齐调用方没有携带的身份字段。
func (p *Instance) BaseContext() Context { return p.hc }

// Manifest 返回已校验清单。
func (p *Instance) Manifest() *pluginapi.Manifest { return p.script.Manifest }

// Contribution 返回装配期结算的贡献集合（只读，勿改）。
func (p *Instance) Contribution() *Contribution {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contrib
}

// CallTool 执行一次工具调用（预算取描述符 timeoutMs）。
func (p *Instance) CallTool(ctx context.Context, h HandlerRef, args json.RawMessage, callCtx Context) (domain.Result, error) {
	if err := p.prepare(ctx); err != nil {
		return domain.Result{}, err
	}
	c := newCall(callTool, h.Slot, p.toolNameOf(h.Slot), args, callCtx, ctx)
	o := p.roundTrip(ctx, c)
	if o.err != nil {
		return domain.Result{}, o.err
	}
	return decodeToolResult(p.id, o.data, c.started)
}

// CallHook 执行一次钩子（默认 200ms、上限 2s；等待 UI 的时间不计入预算）。
func (p *Instance) CallHook(ctx context.Context, h HandlerRef, event ports.HookEvent, hookCtx Context) (ports.HookOutcome, error) {
	if err := p.prepare(ctx); err != nil {
		return ports.HookOutcome{}, err
	}
	evt, err := json.Marshal(event)
	if err != nil {
		return ports.HookOutcome{}, fmt.Errorf("host: 插件 %s 的钩子事件序列化失败: %w", p.id, err)
	}
	c := newCall(callHook, h.Slot, string(event.Point), evt, hookCtx, ctx)
	o := p.roundTrip(ctx, c)
	if o.err != nil {
		return ports.HookOutcome{}, o.err
	}
	return decodeHookOutcome(p.id, o.data)
}

// CallAction 执行一次前端动作（默认 5s，结果上限 1MB）。
func (p *Instance) CallAction(ctx context.Context, h HandlerRef, input json.RawMessage, actCtx Context) (json.RawMessage, error) {
	if err := p.prepare(ctx); err != nil {
		return nil, err
	}
	c := newCall(callAction, h.Slot, p.actionNameOf(h.Slot), input, actCtx, ctx)
	o := p.roundTrip(ctx, c)
	return o.data, o.err
}

// CallRoute 执行一次动态路由处理（默认 5s，响应上限 64KB）。
func (p *Instance) CallRoute(ctx context.Context, h HandlerRef, req json.RawMessage, routeCtx Context) (json.RawMessage, error) {
	if err := p.prepare(ctx); err != nil {
		return nil, err
	}
	c := newCall(callRoute, h.Slot, routeKeyOf(req), req, routeCtx, ctx)
	o := p.roundTrip(ctx, c)
	return o.data, o.err
}

// CallRouteStream 执行一次 SSE 路由：处理器用 ctx.sse.send 推帧，
// 每帧同步交给 onEvent（背压作用在脚本 goroutine 上）。
func (p *Instance) CallRouteStream(ctx context.Context, h HandlerRef, req json.RawMessage, routeCtx Context, onEvent func(RouteEvent) error) error {
	if err := p.prepare(ctx); err != nil {
		return err
	}
	if onEvent == nil {
		return errors.New("host: SSE 路由需要事件回调")
	}
	c := newCall(callRouteStream, h.Slot, routeKeyOf(req), req, routeCtx, ctx)
	c.onRouteEvent = onEvent
	return p.roundTrip(ctx, c).err
}

// CallProviderStream 执行 provider 的 stream（建连 15s / 空闲 90s / 总 30 分钟）。
//
// 增量不在这条返回值里：ctx.emit 每来一个就同步回调 onDelta（owner goroutine 上）。
func (p *Instance) CallProviderStream(ctx context.Context, h HandlerRef, req json.RawMessage, streamCtx Context, onDelta func(json.RawMessage) error) error {
	if err := p.prepare(ctx); err != nil {
		return err
	}
	c := newCall(callProvider, h.Slot, p.providerIDOf(h.Slot), req, streamCtx, ctx)
	c.onDelta = onDelta
	return p.roundTrip(ctx, c).err
}

// Dirty 报告运行时是否已被一次失败弄脏。
func (p *Instance) Dirty() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dirty
}

// prepare 在调用前结算禁用与脏状态（06 §6.4）：熔断冷却未到拒绝并提示自动恢复；
// 冷却到点半开探测（重建运行时，放行本次调用作为试探）；脏运行时先重建。
func (p *Instance) prepare(ctx context.Context) error {
	if p.closed.Load() {
		return p.closedError()
	}
	if err := p.maybeRecover(ctx); err != nil {
		return err
	}
	if !p.Dirty() {
		return p.guardUsable()
	}
	c := newCall(callReload, 0, "reload", nil, p.baseContext(ctx), ctx)
	return p.roundTrip(ctx, c).err
}

// Status 给出 ports.ScriptHost.PluginHandle 需要的失败/禁用视图。
func (p *Instance) Status() (failures int, disabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures, p.disabled
}

// Summary 返回插件状态摘要（ready/degraded/disabled）。
func (p *Instance) Summary() plugins.Summary {
	p.mu.Lock()
	enabled, lastErr, disabled := p.failures == 0 && !p.disabled, p.lastError, p.disabled
	contrib := p.contrib
	p.mu.Unlock()
	if contrib == nil {
		return plugins.Summary{ID: p.id, Status: "failed", LastError: lastErr, Enabled: false}
	}
	s := contrib.Summary(enabled, "", lastErr)
	if disabled {
		s.Status = "disabled"
	} else if lastErr != "" {
		s.Status = "degraded"
	}
	return s
}

// Warnings 返回运行期警告（二次注册、被忽略的回调异常）。
func (p *Instance) Warnings() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.warnings...)
}

// Generation 返回运行时代数（重建一次 +1）；诊断与测试用。
func (p *Instance) Generation() int64 { return p.generation.Load() }

// baseContext 是装配期/重建时给能力桥与入口工厂用的宿主上下文。
func (p *Instance) baseContext(ctx context.Context) Context {
	hc := p.hc
	hc.PluginID = p.id
	if hc.Workspace == "" {
		hc.Workspace = p.caps.WorkspaceRoot
	}
	if hc.Now.IsZero() {
		hc.Now = time.Now().UTC()
	}
	hc.Signal = ctx
	return hc
}

// closedError 返回「插件已关闭」的业务错误。
func (p *Instance) closedError() error {
	return domain.WrapError(domain.NewError(domain.CodePluginCrashed,
		fmt.Sprintf("插件 %s 的运行时已关闭", p.id)), ErrClosed)
}

// 槽位 → 名字回查（诊断与预算档位依据已提交的贡献）。

func (p *Instance) toolNameOf(slot int) string {
	for _, t := range p.toolsOf() {
		if t.Handler.Slot == slot {
			return t.Name
		}
	}
	return ""
}

func (p *Instance) actionNameOf(slot int) string {
	if c := p.Contribution(); c != nil {
		for _, a := range c.Actions {
			if a.Handler.Slot == slot {
				return a.Name
			}
		}
	}
	return ""
}

func (p *Instance) providerIDOf(slot int) string {
	if c := p.Contribution(); c != nil {
		for _, pr := range c.Providers {
			if pr.Stream.Slot == slot {
				return pr.ID
			}
		}
	}
	return ""
}

// helpers ------------------------------------------------------------------

// scriptError 把 *moejs.Exception 转成带插件与调用类型的错误文本。
func scriptError(exc *moejs.Exception, pluginID string, kind callKind) error {
	name, msg := exc.Name(), exc.Message()
	if name == "" {
		name = "Error"
	}
	if msg == "" {
		msg = exc.Error()
	}
	return fmt.Errorf("host: 插件 %s 的 %s 抛出 %s: %s", pluginID, kind, name, msg)
}

// isCancelledError 判定错误是否属于「取消」（不计入连续失败）。
func isCancelledError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrClosed) {
		return true
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code == domain.CodeTurnAborted
	}
	return false
}

// cancelError 把 ctx 的取消转成 interrupted/cancelled 语义（不挂死）。
func cancelError(ctx context.Context) error {
	cause := context.Cause(ctx)
	if cause == nil {
		cause = ctx.Err()
	}
	if cause == nil {
		cause = context.Canceled
	}
	return domain.WrapError(domain.NewError(domain.CodeTurnAborted,
		fmt.Sprintf("插件调用已取消: %v", cause)), cause)
}

// deadlineOf 读出 context 的剩余期限（0 表示没有 deadline）。
func deadlineOf(ctx context.Context) (time.Duration, bool) {
	if ctx == nil {
		return 0, false
	}
	dl, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	left := time.Until(dl)
	if left < 0 {
		left = 0
	}
	return left, true
}

// routeKeyOf 从路由请求里取 method+path（诊断名）。
func routeKeyOf(req json.RawMessage) string {
	var meta struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(req, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Method + " " + meta.Path)
}

// 拆分说明（纯搬移）：调用通道与请求对象见 call.go，预算与宿主长调用见 budget.go，
// 脚本执行与结果分类见 execute.go，熔断/禁用/冷却见 breaker.go。
