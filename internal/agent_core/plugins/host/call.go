package host

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
)

// callKind 决定预算档位与 JS 调用形状。
type callKind uint8

const (
	callInit   callKind = iota + 1 // 入口工厂执行 + 贡献结算
	callReload                     // 手动重载 / 脏运行时重建（M3 双代不在本切片）
	callTool
	callHook
	callAction
	callRoute
	callRouteStream
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
	case callRouteStream:
		return "route-stream"
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
	// onRouteEvent 只在 SSE 路由上使用；同样只在 owner goroutine 上被调用。
	onRouteEvent func(RouteEvent) error

	done chan outcome

	budget *callBudget
	// firstDelta 记录 provider 是否已经收到首个增量（建连窗口 → 空闲窗口）。
	firstDelta atomic.Bool

	mu       sync.Mutex
	hostWork []context.CancelFunc
	// replied / out 记录「本次调用的最终结果」。结算（reply）与发布（publish）分两步：
	// 脚本结果在调用过程中就会结算，但送进 done 要等 owner 跑完 recordResult
	// （熔断计数、禁用与冷却都落在那之后）。顺序颠倒会让调用方拿到返回值后立刻读
	// Status() 时看到「上一次」的计数——这是 TestBreaker* 在 -race 下偶发
	// 「三连败只记了 2 次」的根因。published 保证 done 只被投递一次。
	replied   bool
	published bool
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

// reply 记录本次调用的最终结果（只记不投递；投递由 publish 在熔断簿记之后做）。
//
// 重复 reply 保持第一次的结果：脚本侧 then 回调与 collectOutcome 可能都走到这里。
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
}

// publish 把已结算的结果交给调用方（owner goroutine 在 recordResult 之后调用）。
//
// 只有这里往 done 里投递，所以调用方从 roundTrip 返回时，宿主已经完成本次调用的
// 熔断簿记（失败计数、禁用与冷却）——调用方紧接着读 Status() 看到的是本次结果。
// 没有结算就走到这里说明 owner 漏了 reply：给一个拒绝而不是让调用方挂死。
func (c *call) publish(fallback error) {
	c.mu.Lock()
	if c.published {
		c.mu.Unlock()
		return
	}
	c.published = true
	o, ok := outcome{}, false
	if c.out != nil {
		o, ok = *c.out, true
	}
	replied := c.replied
	c.mu.Unlock()
	if !replied || !ok {
		o = outcome{err: fallback}
	}
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
//
// 结算与投递分两步：run* 里完成熔断簿记并记下结果（c.reply），这里才把结果
// 交给调用方（c.publish）。顺序颠倒会让「脚本结果已结算、recordResult 还没跑」
// 的窗口暴露给调用方——调用方紧接着读 Status() 就会看到上一次的计数。
func (p *Instance) handle(c *call) {
	switch c.kind {
	case callInit:
		p.runInit(c)
	case callReload:
		p.runReload(c)
	default:
		p.runCall(c)
	}
	c.publish(p.unsettledError(c))
}

// unsettledError 是「owner 走完了却没有结算结果」的兜底错误：宁可拒绝，不让调用方挂死。
func (p *Instance) unsettledError(c *call) error {
	return domain.WrapError(domain.NewError(domain.CodePluginCrashed,
		fmt.Sprintf("host: 插件 %s 的 %s 调用没有结算结果（%v）", p.id, c.kind, ErrUnsettled)), ErrUnsettled)
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
