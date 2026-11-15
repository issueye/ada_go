package host

import (
	"context"
	"fmt"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
)

// guardUsable 检查禁用状态（06 §6.4）：防御性兜底，正常路径由 prepare 的
// maybeRecover 先行拦截并给出自动恢复提示。
func (p *Instance) guardUsable() error {
	p.mu.Lock()
	disabled := p.disabled
	remaining := time.Duration(0)
	if disabled {
		remaining = p.disabledAt.Add(p.cooldown).Sub(time.Now())
	}
	p.mu.Unlock()
	if disabled {
		return p.disabledError(remaining)
	}
	return nil
}

// disabledError 构造熔断禁用期间的拒绝错误：带剩余冷却秒数与自动恢复提示，
// 让模型/调用方知道稍后重试即可，而不是等人工重载（06 §6.4）。
func (p *Instance) disabledError(remaining time.Duration) error {
	p.mu.Lock()
	reason := p.lastError
	p.mu.Unlock()
	secs := int((remaining + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return domain.WrapError(domain.NewError(domain.CodePluginDenied,
		fmt.Sprintf("插件 %s 已连续失败被暂时禁用（%s），约 %d 秒后自动恢复，可稍后重试", p.id, reason, secs)), ErrDisabled)
}

// maybeRecover 处理熔断禁用的自动恢复（06 §6.4）：未禁用直接放行；冷却未到
// 拒绝并给出剩余秒数；冷却到点做半开探测——串行化后重建运行时并放行本次调用
// 作为试探。试探调用成功即完全恢复（recordResult 清冷却）；失败则立即重新熔断，
// 冷却翻倍（探测重建本身失败同样翻倍重新计时）。
func (p *Instance) maybeRecover(ctx context.Context) error {
	p.mu.Lock()
	disabled := p.disabled
	remaining := time.Duration(0)
	if disabled {
		remaining = p.disabledAt.Add(p.cooldown).Sub(time.Now())
	}
	p.mu.Unlock()
	if !disabled {
		return nil
	}
	if remaining > 0 {
		return p.disabledError(remaining)
	}

	// 冷却到点：抢锁探测，并发的其余调用按探测后的状态走。
	p.recoverMu.Lock()
	defer p.recoverMu.Unlock()
	p.mu.Lock()
	if !p.disabled { // 另一个并发调用已完成探测
		p.mu.Unlock()
		return nil
	}
	if wait := p.disabledAt.Add(p.cooldown).Sub(time.Now()); wait > 0 { // 等锁期间被重新熔断
		p.mu.Unlock()
		return p.disabledError(wait)
	}
	p.mu.Unlock()

	c := newCall(callReload, 0, "reload", nil, p.baseContext(ctx), ctx)
	if err := p.roundTrip(ctx, c).err; err != nil {
		// 重建失败：rebuildFailed 已置回 disabled 并把冷却翻倍重新计时，
		// 这里只读当前状态构造提示。
		p.mu.Lock()
		cd, reason := p.cooldown, p.lastError
		p.mu.Unlock()
		return domain.WrapError(domain.NewError(domain.CodePluginDenied,
			fmt.Sprintf("插件 %s 自动恢复失败（%s），将在 %d 秒后再次尝试", p.id, reason, int(cd.Seconds())+1)), ErrDisabled)
	}
	// 重建成功：标记探测中，试探调用的结果决定完全恢复或立即重新熔断。
	p.mu.Lock()
	p.probing = true
	p.mu.Unlock()
	return nil
}

// markDirty 标记运行时需重建（06 §6.4 回收策略）。
func (p *Instance) markDirty() {
	p.mu.Lock()
	p.dirty = true
	p.mu.Unlock()
}

// recordResult 维护连续失败计数与自动禁用（06 §6.4：连续 3 次 → disabled + 原因）。
// 熔断带自动恢复：禁用后进入冷却（首次 DisabledCooldownInit，反复熔断指数退避），
// 冷却结束后下一次调用在 prepare 里半开探测；真实成功调用把冷却归零。
func (p *Instance) recordResult(c *call, err error) {
	if err == nil {
		p.mu.Lock()
		p.failures = 0
		// 成功调用清掉上一次调用的错误：否则插件会一直显示“降级”，
		// 即使导致失败的调用早已修好（例如缺 threadId 的一次插槽预取）。
		p.lastError = ""
		p.cooldown = 0
		p.probing = false
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
	// 半开探测后的首次失败：立即重新熔断（经典熔断器只放行一次试探调用）。
	if p.probing {
		p.probing = false
		p.disabled = true
		p.disabledAt = time.Now()
		p.lastError = fmt.Sprintf("恢复探测失败：%s", err)
		p.cooldown = min(2*p.cooldown, DisabledCooldownMax)
		cd := p.cooldown
		p.mu.Unlock()
		p.warn(fmt.Sprintf("插件 %s 半开探测失败，冷却加倍至 %s: %v", p.id, cd, err))
		return
	}
	if n >= ConsecutiveFailureLimit && !p.disabled {
		p.disabled = true
		p.lastError = fmt.Sprintf("连续 %d 次失败：%s", n, err)
		p.disabledAt = time.Now()
		if p.cooldown <= 0 {
			p.cooldown = DisabledCooldownInit
		} else {
			p.cooldown = min(2*p.cooldown, DisabledCooldownMax)
		}
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
