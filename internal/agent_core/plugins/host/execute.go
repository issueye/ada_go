package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
	"github.com/MoeForge/moejs/eventloop"
	"github.com/adag/ada-go/internal/common/domain"
)

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

	// 换入本次调用的宿主上下文（caps.go 的约定）：node:fs 写路径的检查点回调
	// （M5）读 st.currentContext()，没有这一步它只能看到装配期那份上下文。
	// owner goroutine 串行处理调用，st.hc 不存在并发写。
	if err := UpdateCallContext(p.rt, c.hc); err != nil {
		c.reply(outcome{err: err})
		b.stop()
		p.cur = nil
		return
	}

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
//
// 只读 c.settled()，不碰 c.done：done 由 handle 结尾的 publish 独占投递，
// owner 在这里取走的话调用方就永远等不到结果。
func (p *Instance) collectOutcome(c *call, runErr error) outcome {
	if o, ok := c.settled(); ok {
		return o
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
	// SSE 路由允许处理器不返回值（帧已经通过 ctx.sse.send 推完）。
	if c.kind == callRouteStream && v.IsNullish() {
		c.reply(outcome{data: json.RawMessage("null")})
		if p.loop != nil {
			p.loop.StopNoWait()
		}
		return
	}
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
