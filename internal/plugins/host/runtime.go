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

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
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

// Budgets 是各档预算（06 §6.4、09 §9.5）。零值字段由 withDefaults 补默认；
// 测试用小值覆盖「死循环超时」这类断言（不能拿 120s 真实预算跑单测）。
type Budgets struct {
	// Tool 是工具调用的兜底预算；描述符里的 timeoutMs 优先（02 §5.4）。
	Tool time.Duration
	// Hook 是钩子的软预算（默认 200ms）。
	Hook time.Duration
	// HookCap 是钩子预算上限（2s）：要得更长也会被夹到这里（06 §6.6）。
	HookCap time.Duration
	// Action 与 Route 是界面动作与动态路由的预算（默认 5s）。
	Action time.Duration
	Route  time.Duration
	// Factory 是入口工厂（含 Promise 结算）的预算。
	Factory time.Duration
	// ProviderConnect / ProviderIdle / ProviderTotal 是 provider 流的三档期限（03 §5.5）。
	ProviderConnect time.Duration
	ProviderIdle    time.Duration
	ProviderTotal   time.Duration
	// Queue 是调用队列的缓冲长度。
	Queue int
	// ResultBytes 是单次结果上限（1MB）；RouteBytes 是路由响应上限（64KB）。
	ResultBytes int
	RouteBytes  int
	// DeadlineGrace 是「比调用方 ctx 期限提前多少开火」，让 plugin_timeout 与
	// 上层的 timeout/interrupted 判定不要撞在同一毫秒上（02 §5.4 回执状态）。
	DeadlineGrace time.Duration
	// InterruptGrace 是投递中断之后等待任务回执的宽限期。
	InterruptGrace time.Duration
}

// withDefaults 补齐零值字段并夹住钩子上限。
func (b Budgets) withDefaults() Budgets {
	if b.Tool <= 0 {
		b.Tool = DefaultToolTimeout
	}
	if b.Hook <= 0 {
		b.Hook = DefaultHookTimeout
	}
	if b.HookCap <= 0 {
		b.HookCap = MaxHookTimeout
	}
	if b.Hook > b.HookCap {
		b.Hook = b.HookCap
	}
	if b.Action <= 0 {
		b.Action = DefaultActionTimeout
	}
	if b.Route <= 0 {
		b.Route = DefaultCallTimeout
	}
	if b.Factory <= 0 {
		b.Factory = DefaultCallTimeout
	}
	if b.ProviderConnect <= 0 {
		b.ProviderConnect = domain.ProviderConnectTimeout
	}
	if b.ProviderIdle <= 0 {
		b.ProviderIdle = domain.ProviderIdleTimeout
	}
	if b.ProviderTotal <= 0 {
		b.ProviderTotal = domain.ProviderTotalTimeout
	}
	if b.Queue <= 0 {
		b.Queue = 64
	}
	if b.ResultBytes <= 0 {
		b.ResultBytes = MaxResultBytes
	}
	if b.RouteBytes <= 0 {
		b.RouteBytes = domain.RouteResponseMaxBytes
	}
	if b.DeadlineGrace <= 0 {
		b.DeadlineGrace = 50 * time.Millisecond
	}
	if b.InterruptGrace <= 0 {
		b.InterruptGrace = 2 * time.Second
	}
	return b
}

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

// callKind 决定预算档位与 JS 调用形状。
type callKind uint8

const (
	callInit   callKind = iota + 1 // 入口工厂执行 + 贡献结算
	callReload                     // 手动重载 / 脏运行时重建（M3 双代不在本切片）
	callTool
	callHook
	callAction
	callRoute
	callProvider
)

func (k callKind) String() string {
	switch k {
	case callInit:
		return "init"
	case callReload:
		return "reload"
	case callTool:
		return "tool"
	case callHook:
		return "hook"
	case callAction:
		return "action"
	case callRoute:
		return "route"
	case callProvider:
		return "provider"
	}
	return "unknown"
}

// outcome 是一次调用的跨边界结果：只有 JSON 字节与 Go 错误，没有脚本值。
type outcome struct {
	data json.RawMessage
	err  error
}

// call 是一次排队的调用请求。
type call struct {
	kind callKind
	// slot 是 HandlerRef.Slot（脚本侧函数在宿主登记表里的位置）。
	slot int
	// name 只用于诊断与预算（工具名 / 点位 / 动作名）。
	name string
	args json.RawMessage
	hc   Context
	// signal 是取消贯穿的来源 context（调用方 ctx）。
	signal context.Context
	// onDelta 只在 provider 流上使用；它只在 owner goroutine 上被调用。
	onDelta func(json.RawMessage) error

	done chan outcome

	budget *callBudget
	// firstDelta 记录 provider 是否已经收到首个增量（建连窗口 → 空闲窗口）。
	firstDelta atomic.Bool

	mu       sync.Mutex
	hostWork []context.CancelFunc
	// replied 与 out 一起记录「本次调用已经结算」：roundTrip（调用方）与
	// collectOutcome（owner）都要读到同一个结果，不能靠抢同一个 channel ——
	// 抢输的一方会把「已成功的调用」误判成 Promise 未结算。
	replied   bool
	out       *outcome
	cancelled atomic.Bool
	started   time.Time
}

func newCall(kind callKind, slot int, name string, args json.RawMessage, hc Context, signal context.Context) *call {
	if signal == nil {
		signal = context.Background()
	}
	if hc.Signal == nil {
		hc.Signal = signal
	}
	return &call{
		kind: kind, slot: slot, name: name, args: args, hc: hc, signal: signal,
		done: make(chan outcome, 1), started: time.Now(),
	}
}

// reply 记录本次调用的最终结果（容量 1，绝不阻塞 loop）。
func (c *call) reply(o outcome) {
	c.mu.Lock()
	if c.replied {
		c.mu.Unlock()
		return
	}
	c.replied = true
	oc := o
	c.out = &oc
	c.mu.Unlock()
	c.done <- o
}

// settled 返回已经结算的结果（无论 channel 里的那份有没有被调用方取走）。
func (c *call) settled() (outcome, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.out == nil {
		return outcome{}, false
	}
	return *c.out, true
}

// trackCancel 登记一个宿主长调用的释放函数。
func (c *call) trackCancel(release func()) {
	c.mu.Lock()
	c.hostWork = append(c.hostWork, release)
	c.mu.Unlock()
}

// releaseHostWork 释放本调用启动的全部宿主长调用（取消 + 关闭观察协程）。
func (c *call) releaseHostWork() {
	c.mu.Lock()
	list := c.hostWork
	c.hostWork = nil
	c.mu.Unlock()
	for _, release := range list {
		release()
	}
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
	mu        sync.Mutex
	dirty     bool
	failures  int
	disabled  bool
	lastError string
	warnings  []string
	contrib   *Contribution
	loopPtr   *mojsLoop

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
		// 能力安装没接线时拒绝加载，而不是「跳过能力安装」装配出一个缺 fs/child_process 的半成品。
		return nil, fmt.Errorf("host: InstallCapabilities 未赋值（caps.go 未落地），拒绝加载插件 %s", script.pluginID())
	}
	if err := script.validate(); err != nil {
		return nil, err
	}
	if caps == nil {
		return nil, fmt.Errorf("host: 插件 %s 缺少能力授予集合", script.pluginID())
	}
	if hc.PluginID != "" && hc.PluginID != script.pluginID() {
		return nil, fmt.Errorf("host: 宿主上下文的 pluginId %q 与清单 id %q 不一致", hc.PluginID, script.pluginID())
	}
	budgets := opts.Budgets.withDefaults()
	p := &Instance{
		id: script.pluginID(), script: script, caps: caps, hc: hc, opts: opts, budgets: budgets,
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
	return p, nil
}

// serve 是 owner goroutine：Runtime/Loop 只在它上面被使用。
func (p *Instance) serve() {
	defer close(p.ownerDone)
	for {
		select {
		case c := <-p.requests:
			p.handle(c)
		case <-p.quit:
			p.shutdown()
			return
		}
	}
}

// handle 分派一次请求。
func (p *Instance) handle(c *call) {
	switch c.kind {
	case callInit:
		p.runInit(c)
	case callReload:
		p.runReload(c)
	default:
		p.runCall(c)
	}
}

// roundTrip 把调用交给 owner goroutine 并等待结果（取消贯穿、不挂死）。
func (p *Instance) roundTrip(ctx context.Context, c *call) outcome {
	if p.closed.Load() {
		return outcome{err: p.closedError()}
	}
	select {
	case p.requests <- c:
	case <-p.quit:
		return outcome{err: p.closedError()}
	case <-p.ownerDone:
		return outcome{err: p.closedError()}
	case <-ctx.Done():
		return outcome{err: cancelError(ctx)}
	}
	select {
	case o := <-c.done:
		return o
	case <-ctx.Done():
		p.abort(c, ctx)
		return p.awaitAborted(c, ctx)
	case <-p.ownerDone:
		return outcome{err: p.closedError()}
	}
}

// awaitAborted 在投递中断之后等任务回执；超出宽限期同样返回取消（不挂死）。
func (p *Instance) awaitAborted(c *call, ctx context.Context) outcome {
	grace := time.NewTimer(p.budgets.InterruptGrace)
	defer grace.Stop()
	select {
	case o := <-c.done:
		if o.err != nil {
			return o
		}
		return outcome{err: cancelError(ctx)}
	case <-grace.C:
		return outcome{err: cancelError(ctx)}
	}
}

// abort 从调用方 goroutine 取消一次进行中的调用：
// 先释放宿主长调用，再 loop.Interrupt 中止脚本（脚本不可 catch，INV-8）。
func (p *Instance) abort(c *call, ctx context.Context) {
	c.cancelled.Store(true)
	c.releaseHostWork()
	p.mu.Lock()
	loop := p.loopPtr
	p.mu.Unlock()
	if loop != nil {
		loop.Interrupt(errInterruptCancel)
	}
	_ = ctx
}

// newBudgetFor 按档位建预算；provider 用「建连 → 空闲」两段。
//
// 调用方 ctx 带 deadline 时夹到「deadline - DeadlineGrace」，保证 plugin_timeout
// 而不是上层的 interrupted 先发生（02 §5.4 的回执状态由错误类型决定）。
func (p *Instance) newBudgetFor(c *call) *callBudget {
	b := p.budgets
	var soft, wall time.Duration
	switch c.kind {
	case callTool:
		soft = b.Tool
		if d, ok := p.descriptorOf(c.name); ok && d.Timeout > 0 {
			soft = d.Timeout
		}
	case callHook:
		soft = b.Hook
		if d, ok := p.hookTimeoutOf(c.name); ok && d > 0 {
			soft = d
		}
		if soft > b.HookCap {
			soft = b.HookCap
		}
	case callAction:
		soft = b.Action
		if d, ok := p.actionTimeoutOf(c.name); ok && d > 0 {
			soft = d
		}
	case callRoute:
		soft = b.Route
	case callProvider:
		soft, wall = b.ProviderConnect, b.ProviderTotal
	default:
		soft = b.Factory
	}
	if left, ok := deadlineOf(c.signal); ok {
		clamped := left - b.DeadlineGrace
		if clamped <= 0 {
			clamped = time.Millisecond
		}
		if soft > clamped {
			soft = clamped
		}
	}
	return newCallBudget(soft, wall)
}

// runCall 执行一次脚本调用（owner goroutine）。
func (p *Instance) runCall(c *call) {
	if p.rt == nil {
		c.reply(outcome{err: fmt.Errorf("host: 插件 %s 尚未装配运行时", p.id)})
		return
	}
	if err := p.guardUsable(); err != nil {
		c.reply(outcome{err: err})
		return
	}
	fn, ok := p.handler(c.slot)
	if !ok {
		c.reply(outcome{err: fmt.Errorf("host: 插件 %s 没有槽位 %d 的可调用句柄（%s）", p.id, c.slot, c.kind)})
		return
	}

	b := p.newBudgetFor(c)
	c.budget = b
	p.cur = c
	b.begin(func(reason error) { p.interrupt(reason) })

	var runErr error
	if p.loop.RunOnLoop(func(rt *mojsRuntime) { p.invoke(rt, c, fn) }) {
		runErr = p.loop.Run(nil)
	} else {
		runErr = eventloop.ErrTerminated
	}
	b.stop()
	p.cur = nil
	c.releaseHostWork()

	out := p.collectOutcome(c, runErr)
	p.recordResult(c, out.err)
	c.reply(out)
}

// collectOutcome 取任务写回的结果；loop 在回执之前结束（中断、异常、空转）时给出分类结果。
func (p *Instance) collectOutcome(c *call, runErr error) outcome {
	// 先看「已经结算」的副本：调用方可能已经把 channel 里那份取走了。
	if o, ok := c.settled(); ok {
		return o
	}
	select {
	case o := <-c.done:
		return o
	default:
	}
	return outcome{err: p.classify(runErr, c)}
}

// classify 把 loop.Run 的错误分成四类（06 §6.4 回收策略）。
func (p *Instance) classify(runErr error, c *call) error {
	if c.cancelled.Load() {
		p.markDirty()
		return domain.WrapError(domain.NewError(domain.CodeTurnAborted,
			fmt.Sprintf("插件 %s 的 %s 调用被取消: %v", p.id, c.kind, context.Canceled)), context.Canceled)
	}
	if runErr == nil {
		// 循环空转结束却没有回执：脚本返回的 Promise 永远没有结算。
		return domain.WrapError(domain.NewError(domain.CodePluginCrashed,
			fmt.Sprintf("host: 插件 %s 的 %s 返回的 Promise 没有结算（%v）", p.id, c.kind, ErrUnsettled)), ErrUnsettled)
	}
	if errors.Is(runErr, eventloop.ErrTerminated) {
		return domain.WrapError(domain.NewError(domain.CodePluginCrashed,
			"host: 插件运行时已终止"), ErrClosed)
	}
	var ie *moejs.InterruptedError
	if errors.As(runErr, &ie) {
		switch ie.Value {
		case errInterruptTimeout:
			p.markDirty()
			return p.timeoutError(c)
		case errInterruptCancel:
			p.markDirty()
			return domain.WrapError(domain.NewError(domain.CodeTurnAborted,
				fmt.Sprintf("插件 %s 的 %s 调用被取消: %v", p.id, c.kind, context.Canceled)), context.Canceled)
		case errInterruptClosed:
			p.markDirty()
			return p.closedError()
		}
	}
	if errors.Is(runErr, moejs.ErrMemoryLimit) {
		p.markDirty()
		return domain.WrapError(domain.NewError(domain.CodePluginCrashed,
			fmt.Sprintf("host: 插件 %s 超出内存上限 %d 字节（06 §6.4）", p.id, p.memoryLimit())), runErr)
	}
	var internal *moejs.InternalError
	if errors.As(runErr, &internal) {
		p.markDirty()
		return domain.WrapError(domain.NewError(domain.CodePluginCrashed,
			fmt.Sprintf("host: 插件 %s 的运行时内部错误/panic：%s", p.id, internal.Error())), runErr)
	}
	// 宿主长调用（ctx.http / ctx.fetch）拒绝时带来的业务错误保持原码，
	// 不要退化成「脚本抛了 TypeError」——provider 桥要靠 provider_error 决定重试。
	var de *domain.Error
	if errors.As(runErr, &de) {
		return de
	}
	// HTTP 状态错误保留 Status/Retry-After，重试包装据此区分 401 与 502。
	var he *HTTPError
	if errors.As(runErr, &he) {
		return he
	}
	// 其它：脚本抛出的异常。不标脏（06 §6.4 只有超时/超内存/内部错误/panic 四类弄脏运行时）。
	if exc, ok := runErr.(*moejs.Exception); ok {
		return scriptError(exc, p.id, c.kind)
	}
	return runErr
}

// timeoutError 生成 plugin_timeout。detail 里带上 context.DeadlineExceeded 的文本：
// toolkit.Executor 目前按字符串识别超时（它没有 errors.Is 路径），这样回执才是 timeout
// 而不是 error（契约缺口已在汇报里列出）。
func (p *Instance) timeoutError(c *call) error {
	limit := p.budgets.Tool
	if c.budget != nil {
		limit = c.budget.softLimit()
	}
	detail := fmt.Sprintf("插件 %s 的 %s 调用超出预算 %s: %v（06 §6.4 plugin_timeout）",
		p.id, c.kind, limit, context.DeadlineExceeded)
	return domain.WrapError(domain.NewError(domain.CodePluginTimeout, detail), ErrTimeout)
}

// invoke 在 loop 上做一次脚本调用：入参 JSON 进、结果 JSON 出（05 §6.3 的 JSON 边界）。
func (p *Instance) invoke(rt *mojsRuntime, c *call, fn moejs.Value) {
	defer func() {
		if rec := recover(); rec != nil {
			p.replyErr(c, fmt.Errorf("host: 插件 %s 调用 %s 时宿主侧 panic: %v", p.id, c.kind, rec))
		}
	}()
	args, err := p.callArgs(rt, c)
	if err != nil {
		p.replyErr(c, err)
		return
	}
	res, err := rt.CallFunction(fn, moejs.Undefined(), args...)
	if err != nil {
		p.replyErr(c, p.exportError(err))
		return
	}
	p.adopt(rt, c, res)
}

// adopt 结算脚本返回值：thenable 走 then 回调，其余直接按 JSON 回（06 §6.4「Await 语义」）。
func (p *Instance) adopt(rt *mojsRuntime, c *call, v moejs.Value) {
	r := rt.Realm()
	then, err := r.GetMethod(v, r.KeyFromGoString("then"))
	if err != nil {
		p.replyErr(c, p.exportError(err))
		return
	}
	if !engine.IsCallable(then) {
		p.replyValue(rt, c, v)
		return
	}
	onOk := rt.Function("__ada_settle_ok", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		p.replyValue(p.rt, c, moejs.Arg(args, 0))
		return moejs.Undefined(), nil
	})
	onErr := rt.Function("__ada_settle_err", 1, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		p.replyErr(c, p.valueError(moejs.Arg(args, 0), c))
		return moejs.Undefined(), nil
	})
	if _, err := rt.CallFunction(then, v, onOk, onErr); err != nil {
		p.replyErr(c, p.exportError(err))
	}
}

// replyValue 把脚本值序列化成 JSON 并回执（含 1MB / 64KB 上限）。
func (p *Instance) replyValue(rt *mojsRuntime, c *call, v moejs.Value) {
	raw, err := rt.AppendJSON(nil, v)
	if err != nil {
		p.replyErr(c, fmt.Errorf("host: 插件 %s 的 %s 结果不可 JSON 序列化: %w", p.id, c.kind, err))
		return
	}
	limit := p.budgets.ResultBytes
	if c.kind == callRoute {
		limit = p.budgets.RouteBytes
	}
	if len(raw) > limit {
		p.replyErr(c, fmt.Errorf("host: 插件 %s 的 %s 结果 %d 字节超过上限 %d（05 §6.3）", p.id, c.kind, len(raw), limit))
		return
	}
	c.reply(outcome{data: json.RawMessage(raw)})
	if p.loop != nil {
		p.loop.StopNoWait()
	}
}

// replyErr 回执一个 Go 侧错误（已分类）。
func (p *Instance) replyErr(c *call, err error) {
	c.reply(outcome{err: err})
	if p.loop != nil {
		p.loop.StopNoWait()
	}
}

// exportError 把脚本抛出的异常转成 Go 错误：只保留 name/message，不带宿主绝对路径。
func (p *Instance) exportError(err error) error {
	if err == nil {
		return nil
	}
	var exc *moejs.Exception
	if errors.As(err, &exc) {
		return scriptError(exc, p.id, p.curKind())
	}
	return err
}

// valueError 处理 Promise 的 reject 值：它不一定是 Error。
// 宿主长调用（ctx.http / ctx.fetch）的 reject 值携带原始 Go 错误时，
// 通过 Exception.Unwrap 取回它，保住错误码与可重试标记。
func (p *Instance) valueError(v moejs.Value, c *call) error {
	if v.IsObject() {
		if inner := (&moejs.Exception{Value: v}).Unwrap(); inner != nil {
			return inner
		}
	}
	kind := "unknown"
	if c != nil {
		kind = c.kind.String()
	}
	return fmt.Errorf("host: 插件 %s 的 %s 被 reject：%s", p.id, kind, p.valueText(v))
}

// valueText 读出脚本值的可诊断文本（只取脚本自己的字段，不泄露宿主路径）。
func (p *Instance) valueText(v moejs.Value) string {
	rt := p.rt
	if rt == nil {
		return v.String()
	}
	if v.IsObject() {
		var parts []string
		for _, key := range []string{"name", "message", "code"} {
			pv, err := rt.Get(v, key)
			if err != nil || pv.IsUndefined() || pv.IsNull() {
				continue
			}
			if s := pv.String(); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}
	return v.String()
}

// curKind 返回当前调用的类型名（异常文本里要能看见是哪一类调用）。
func (p *Instance) curKind() callKind {
	if p.cur != nil {
		return p.cur.kind
	}
	return callInit
}

// interrupt 从别的 goroutine 中止脚本（loop.Interrupt 同时唤醒空闲循环）。
func (p *Instance) interrupt(reason error) {
	p.mu.Lock()
	loop := p.loopPtr
	p.mu.Unlock()
	if loop != nil {
		loop.Interrupt(reason)
	}
}

// callBudget 是一次调用的脚本时间预算：宿主长调用在飞时暂停（06 §6.4）。
type callBudget struct {
	mu        sync.Mutex
	soft      time.Duration
	used      time.Duration
	segStart  time.Time
	running   bool
	inflight  int
	timer     *time.Timer
	wall      time.Duration
	wallTimer *time.Timer
	stopped   bool
	onFire    func(reason error)
}

func newCallBudget(soft, wall time.Duration) *callBudget {
	return &callBudget{soft: soft, wall: wall}
}

func (b *callBudget) begin(onFire func(reason error)) {
	b.mu.Lock()
	b.onFire = onFire
	b.used = 0
	b.segStart = time.Now()
	b.running = true
	b.armLocked()
	if b.wall > 0 {
		b.wallTimer = time.AfterFunc(b.wall, func() { b.fire(errInterruptTimeout) })
	}
	b.mu.Unlock()
}

func (b *callBudget) softLimit() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.soft
}

// armLocked 按「剩余脚本时间」重新武装计时器；宿主长调用在飞时不武装。
func (b *callBudget) armLocked() {
	if b.stopped || !b.running || b.inflight > 0 || b.onFire == nil {
		return
	}
	left := b.soft - b.used
	if left <= 0 {
		left = time.Millisecond
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(left, func() { b.fire(errInterruptTimeout) })
}

func (b *callBudget) fire(reason error) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	fn := b.onFire
	b.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
}

// pause 进入宿主长调用：暂停脚本计时（期限由各自的桥负责）。
func (b *callBudget) pause() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.inflight++
	if b.running {
		b.used += time.Since(b.segStart)
		b.running = false
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
}

// resume 结束宿主长调用：恢复计时。
func (b *callBudget) resume() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.inflight > 0 {
		b.inflight--
	}
	if b.inflight == 0 && !b.stopped {
		b.running = true
		b.segStart = time.Now()
		b.armLocked()
	}
	b.mu.Unlock()
}

// window 换档：provider 的首个增量之后把软预算换成空闲窗口。
func (b *callBudget) window(d time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.soft = d
	b.used = 0
	b.segStart = time.Now()
	b.running = !b.stopped
	b.armLocked()
	b.mu.Unlock()
}

func (b *callBudget) stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.stopped = true
	b.running = false
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if b.wallTimer != nil {
		b.wallTimer.Stop()
		b.wallTimer = nil
	}
	b.mu.Unlock()
}

// hostAsync 把一次宿主长调用包装成脚本里的 Promise（06 §6.4 宿主异步桥）。
//
// fn 在别的 goroutine 上执行，只碰 Go 值；结果回到 loop 上结算。
// 期间暂停脚本预算计时（06 §6.4），并解除调用方 deadline（否则 200ms 的钩子预算
// 会误杀一次 300s 的交互）。
func (p *Instance) hostAsync(c *call, name string, fn func(ctx context.Context) (any, error)) moejs.Value {
	return p.hostAsyncTimeout(c, name, defaultHostCallTimeout, fn)
}

// hostAsyncTimeout 与 hostAsync 相同，但允许调用方给出更长的兜底期限
// （provider 的 SSE 流是 30 分钟级长任务，不能被 300s 的交互兜底误杀，03 §5.5）。
func (p *Instance) hostAsyncTimeout(c *call, name string, timeout time.Duration, fn func(ctx context.Context) (any, error)) moejs.Value {
	promise, settle := p.loop.NewPromise()
	ctx, release := p.hostContextFor(c, timeout)
	var once sync.Once
	releaseOnce := func() { once.Do(release) }
	c.trackCancel(releaseOnce)
	c.budget.pause()
	go func() {
		defer releaseOnce()
		v, err := fn(ctx)
		settle(func(rrt *moejs.Runtime) (moejs.Value, error) {
			if !c.cancelled.Load() {
				c.budget.resume()
			}
			if err != nil {
				// 业务错误（HTTP 状态、allowlist、取消）保持原样穿过 Promise，
				// 让 provider 桥能按错误码决定是否重试（03 §5.5）。
				var de *domain.Error
				if errors.As(err, &de) {
					return moejs.Undefined(), err
				}
				// HTTP 状态错误同样原样穿过；重试包装需要 Status/Retry-After。
				var he *HTTPError
				if errors.As(err, &he) {
					return moejs.Undefined(), err
				}
				return moejs.Undefined(), rrt.Realm().TypeError("host: %s 失败: %v", name, err)
			}
			return rrt.FromGo(v)
		})
	}()
	return promise
}

// hostContextFor 派生宿主长调用的 context：
//   - 保留「显式取消」（轮次取消必须贯穿到桥，INV-8）；
//   - 不传递调用方 deadline（脚本预算/钩子 200ms 不是宿主长调用的期限，06 §6.4）；
//   - 自带 timeout 兜底，避免桥实现忘记期限时永久挂住。
func (p *Instance) hostContextFor(c *call, timeout time.Duration) (context.Context, func()) {
	if timeout <= 0 {
		timeout = defaultHostCallTimeout
	}
	parent := context.Background()
	if c.signal != nil {
		parent = c.signal
	}
	base, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	stop := make(chan struct{})
	go func() {
		select {
		case <-parent.Done():
			if errors.Is(context.Cause(parent), context.DeadlineExceeded) {
				// 只是脚本预算/上层 deadline 到期：长调用由各自的期限负责，不因此取消。
				return
			}
			cancel()
		case <-stop:
		}
	}()
	return base, func() {
		close(stop)
		cancel()
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

// guardUsable 检查禁用状态（06 §6.4）。
func (p *Instance) guardUsable() error {
	p.mu.Lock()
	disabled, reason := p.disabled, p.lastError
	p.mu.Unlock()
	if disabled {
		return domain.WrapError(domain.NewError(domain.CodePluginDenied,
			fmt.Sprintf("插件 %s 已连续失败并被禁用：%s（06 §6.4，需手动重载）", p.id, reason)), ErrDisabled)
	}
	return nil
}

// markDirty 标记运行时需重建（06 §6.4 回收策略）。
func (p *Instance) markDirty() {
	p.mu.Lock()
	p.dirty = true
	p.mu.Unlock()
}

// recordResult 维护连续失败计数与自动禁用（06 §6.4：连续 3 次 → disabled + 原因）。
func (p *Instance) recordResult(c *call, err error) {
	if err == nil {
		p.mu.Lock()
		p.failures = 0
		p.mu.Unlock()
		return
	}
	if isCancelledError(err) {
		return // 取消不是插件的错
	}
	p.mu.Lock()
	p.failures++
	n := p.failures
	p.lastError = err.Error()
	if n >= ConsecutiveFailureLimit && !p.disabled {
		p.disabled = true
		p.lastError = fmt.Sprintf("连续 %d 次失败：%s", n, err)
	}
	p.mu.Unlock()
	p.warn(fmt.Sprintf("插件 %s 的 %s 调用失败（连续第 %d 次）: %v", p.id, c.kind, n, err))
}

// warn 记录一条非致命警告（运行期二次注册、被忽略的回调异常）。
func (p *Instance) warn(msg string) {
	p.mu.Lock()
	p.warnings = append(p.warnings, msg)
	if len(p.warnings) > 256 {
		p.warnings = p.warnings[len(p.warnings)-256:]
	}
	cb := p.opts.OnWarning
	p.mu.Unlock()
	if cb != nil {
		cb(msg)
	}
	if p.hc.Log != nil {
		p.hc.Log("warn", msg, map[string]any{"plugin": p.id})
	}
}

// shutdown 结束 owner goroutine：终止循环（丢弃全部定时器与任务）并释放引用。
func (p *Instance) shutdown() {
	p.teardown(errInterruptClosed)
	// 让还在排队的调用立刻拿到「已关闭」，不等它们自己的 ctx。
	for {
		select {
		case c := <-p.requests:
			c.reply(outcome{err: p.closedError()})
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
	// 能力安装持有上游 node.API（工作区目录句柄与 worker 池）：不关就是每个插件一条泄漏。
	if p.rt != nil {
		_ = CloseCapabilities(p.rt)
	}
	p.rt = nil
	p.loop = nil
	p.cur = nil
	p.staging = nil
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
	loop := p.loopPtr
	p.mu.Unlock()
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
	p.mu.Unlock()
	return nil
}

func (p *Instance) rebuildFailed(err error) {
	p.teardown(errInterruptClosed)
	p.mu.Lock()
	p.dirty = true
	p.lastError = err.Error()
	p.disabled = true
	p.mu.Unlock()
	p.warn(fmt.Sprintf("插件 %s 运行时重建失败: %v", p.id, err))
}

// openRuntime 造 Runtime 与 Loop：全部边界在这里固定（06 §6.4 初始化）。
func (p *Instance) openRuntime() error {
	p.loader = newModuleLoader(p.script, p.caps)
	rt := moejs.NewRuntime(moejs.Options{
		Importer: p.loader.importer(),
		// 内存上限只到 MemoryLimit（64MB）；栈与调用深度的上限由 moejs 固定
		// （alpha.11 没有 StackSize/MaxCallDepth 选项，见 docs/decisions.md D-7）。
		MemoryLimit:        p.memoryLimit(),
		DisableDynamicCode: p.caps.Trust != plugins.TrustBuiltin,
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
func (p *Instance) Manifest() *plugins.Manifest { return p.script.Manifest }

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

// prepare 在脏时先重建（06 §6.4：下一次调用前重新初始化），再检查禁用。
func (p *Instance) prepare(ctx context.Context) error {
	if p.closed.Load() {
		return p.closedError()
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
