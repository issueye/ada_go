package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// HookHandler 是一个钩子处理器。M1-C 的 moejs 宿主与测试替身都通过这里接入，
// 引擎只认识 ports.HookRunner（设计 3.3：engine 不知道 moejs）。
type HookHandler struct {
	PluginID string
	Trust    domain.Trust
	Point    ports.HookPoint
	// Priority 越小越先执行；同级按 PluginID 稳定排序，保证回放确定性（06 §6.5）。
	Priority int
	// MaxAction 是该插件清单声明的最高动作等级（未声明点位调用时拒绝，6.2）。
	MaxAction HookAction
	// Timeout 为 0 时取 DefaultHookTimeout（200ms，最大 2s）。
	Timeout time.Duration
	Fn      func(ctx context.Context, evt ports.HookEvent) (ports.HookOutcome, error)
}

// HookChain 是 ports.HookRunner 的生产实现。
//
// 它负责的是「协调」：顺序、超时、动作分级、失败方向（设计 1.3 协调性 + 1.5.1 插件宿主）。
// 具体规则（该不该问、问什么）在策略插件里，基座一律不解释（03 §5.6）。
type HookChain struct {
	mu       sync.RWMutex
	handlers map[ports.HookPoint][]*HookHandler
	// clock 用于超时判定；nil 时用真实时钟。
	clock ports.Clock
	// warnings 记录被忽略的观察型钩子失败，避免静默吞错。
	warnings []string
}

var _ ports.HookRunner = (*HookChain)(nil)

// NewHookChain 创建空的钩子链。
func NewHookChain() *HookChain {
	return &HookChain{handlers: map[ports.HookPoint][]*HookHandler{}}
}

// Register 登记处理器。点位必须是契约声明过的 15 个点位之一：
// 未声明的点位运行期不允许出现（06 §6.6 全部点位都有真实调用方）。
func (c *HookChain) Register(h *HookHandler) error {
	if h == nil || h.Fn == nil {
		return fmt.Errorf("plugins: 钩子处理器为空（INV-2 不允许 nil 处理器）")
	}
	point, ok := LookupHookPoint(string(h.Point))
	if !ok {
		return fmt.Errorf("plugins: 未知钩子点位 %q", h.Point)
	}
	if h.MaxAction == "" {
		h.MaxAction = ActionObserve
	}
	if HookAction(h.MaxAction).Rank() < 0 {
		return fmt.Errorf("plugins: 点位 %q 的未知动作等级 %q", h.Point, h.MaxAction)
	}
	if !point.AllowsAction(h.MaxAction, h.Trust == domain.TrustBuiltin) {
		return fmt.Errorf("plugins: 插件 %q（trust=%s）在点位 %q 上不能使用动作 %q",
			h.PluginID, h.Trust, h.Point, h.MaxAction)
	}
	if h.Timeout > domain.MaxHookTimeout {
		return fmt.Errorf("plugins: 点位 %q 的钩子超时 %s 超过上限 %s（06 §6.6）", h.Point, h.Timeout, domain.MaxHookTimeout)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.handlers[h.Point]
	for _, existing := range list {
		if existing.PluginID == h.PluginID {
			return fmt.Errorf("plugins: 插件 %q 已在点位 %q 上登记过处理器", h.PluginID, h.Point)
		}
	}
	list = append(list, h)
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority < list[j].Priority
		}
		return list[i].PluginID < list[j].PluginID
	})
	c.handlers[h.Point] = list
	return nil
}

// RemovePlugin 摘掉某插件的全部处理器（禁用/卸载后回到空态，1.5.6）。
func (c *HookChain) RemovePlugin(pluginID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for point, list := range c.handlers {
		kept := list[:0]
		for _, h := range list {
			if h.PluginID == pluginID {
				n++
				continue
			}
			kept = append(kept, h)
		}
		c.handlers[point] = kept
	}
	return n
}

// FailureDirection 实现端口：安全点默认关闭，其余默认开放（06 §6.6）。
func (c *HookChain) FailureDirection(point ports.HookPoint) domain.FailureDirection {
	p, ok := LookupHookPoint(string(point))
	if !ok || p.FailClosed {
		return domain.FailClosed
	}
	return domain.FailOpen
}

// AllowedActions 实现端口：点位允许集合 ∩ 该信任级别可用集合。
func (c *HookChain) AllowedActions(point ports.HookPoint, trust domain.Trust) []ports.HookAction {
	p, ok := LookupHookPoint(string(point))
	if !ok {
		return nil
	}
	builtin := trust == domain.TrustBuiltin
	out := make([]ports.HookAction, 0, len(p.Allowed))
	for _, a := range p.Allowed {
		if p.AllowsAction(a, builtin) {
			out = append(out, ports.HookAction(a))
		}
	}
	return out
}

// Run 按顺序调用点位上的处理器并汇总裁决。
//
// 语义（06 §6.6、02 §5.4）：
//
//	deny   → 只接受 tool.beforeCall 的 deny，裁决为不允许；
//	abort  → 仅 builtin，且只在这三个点位有效；
//	replace→ 仅 builtin，载荷由引擎自行重新校验；
//	超时/异常 → 按点位失败方向收敛：tool.beforeCall 拒绝，其余忽略并记警告。
func (c *HookChain) Run(ctx context.Context, evt ports.HookEvent) (ports.HookVerdict, error) {
	verdict := ports.HookVerdict{Allowed: true}
	if evt.Point == "" {
		return verdict, fmt.Errorf("plugins: 钩子事件缺少点位")
	}
	point, ok := LookupHookPoint(string(evt.Point))
	if !ok {
		return verdict, fmt.Errorf("plugins: 未知钩子点位 %q", evt.Point)
	}
	if depth := ctxDepth(ctx); depth >= domain.HookRecursionDepthLimit {
		return verdict, fmt.Errorf("plugins: 点位 %q 递归深度达到上限 %d（06 §6.6 防递归）",
			evt.Point, domain.HookRecursionDepthLimit)
	}

	c.mu.RLock()
	handlers := append([]*HookHandler(nil), c.handlers[evt.Point]...)
	c.mu.RUnlock()

	for _, h := range handlers {
		if ctx.Err() != nil {
			// 取消贯穿：安全点按拒绝收敛，其它点位保持原裁决。
			if point.FailClosed {
				verdict.Allowed = false
				verdict.Reason = "轮次已取消"
			}
			return verdict, ctx.Err()
		}
		deadline := h.Timeout
		if deadline <= 0 {
			deadline = domain.DefaultHookTimeout
		}
		hctx, cancel := context.WithTimeout(withDepth(ctx, depthOf(ctx)+1), deadline)
		outcome, err := c.invoke(hctx, h, evt)
		cancel()

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				c.noteWarning(fmt.Sprintf("点位 %q 插件 %q 超时 %s", evt.Point, h.PluginID, deadline))
				err = domain.NewError(domain.CodePluginTimeout, "钩子超时: "+h.PluginID)
			}
			if point.FailClosed {
				verdict.Allowed = false
				verdict.Reason = "策略插件未能给出允许判定（失败关闭）: " + err.Error()
				return verdict, err
			}
			c.noteWarning(fmt.Sprintf("点位 %q 插件 %q 失败（失败方向为开，已忽略）: %v", evt.Point, h.PluginID, err))
			continue
		}
		// note 只在对应分支里并入，避免同一条说明被计两次。
		switch outcome.Action {
		case "", ports.HookObserve:
		case ports.HookNote:
			verdict.Notes = append(verdict.Notes, mergeNotes(outcome.Note)...)
		case ports.HookDeny:
			if !point.AllowsAction(HookAction(ports.HookDeny), h.Trust == domain.TrustBuiltin) {
				c.noteWarning(fmt.Sprintf("插件 %q 在点位 %q 返回 deny 越权，按不允许处理", h.PluginID, evt.Point))
			}
			verdict.Allowed = false
			verdict.Reason = denyReason(outcome)
			if point.FailClosed {
				return verdict, nil
			}
		case ports.HookReplace:
			if !point.AllowsAction(HookAction(ports.HookReplace), h.Trust == domain.TrustBuiltin) {
				c.noteWarning(fmt.Sprintf("插件 %q（trust=%s）无权在 %q 替换，忽略该动作（06 §6.6）",
					h.PluginID, h.Trust, evt.Point))
				continue
			}
			if len(outcome.Replacement) == 0 {
				continue
			}
			if !json.Valid(outcome.Replacement) {
				verdict.Allowed = false
				verdict.Reason = "替换载荷不是合法 JSON"
				return verdict, nil
			}
			verdict.Replacements = outcome.Replacement
		case ports.HookAbort:
			if !point.AllowsAction(HookAction(ports.HookAbort), h.Trust == domain.TrustBuiltin) {
				c.noteWarning(fmt.Sprintf("插件 %q（trust=%s）无权 abort %q，忽略（06 §6.6）", h.PluginID, h.Trust, evt.Point))
				continue
			}
			verdict.Aborted = true
			verdict.Allowed = false
			verdict.Reason = firstNonEmpty(outcome.Reason, "控制面中止")
			return verdict, nil
		default:
			c.noteWarning(fmt.Sprintf("未知动作等级 %q（插件 %q）", outcome.Action, h.PluginID))
		}
	}
	return verdict, nil
}

// Warnings 返回被忽略的失败与越权记录（页面与日志要能看见，不静默）。
func (c *HookChain) Warnings() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.warnings...)
}

func (c *HookChain) noteWarning(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.warnings) > 256 {
		c.warnings = c.warnings[len(c.warnings)-256:]
	}
	c.warnings = append(c.warnings, msg)
}

// invoke 把 panic 收敛成错误：插件处理器崩溃不能带崩轮次（06 §6.4 回收策略）。
func (c *HookChain) invoke(ctx context.Context, h *HookHandler, evt ports.HookEvent) (out ports.HookOutcome, err error) {
	defer func() {
		if p := recover(); p != nil {
			out = ports.HookOutcome{}
			err = fmt.Errorf("plugins: 钩子 %s/%s panic: %v", h.Point, h.PluginID, p)
		}
	}()
	return h.Fn(ctx, evt)
}

func denyReason(o ports.HookOutcome) string {
	if o.Reason != "" {
		return o.Reason
	}
	return "策略插件拒绝"
}

func mergeNotes(note string) []string {
	if note == "" {
		return nil
	}
	return []string{note}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// 钩子递归深度通过 context 传递，避免在事件对象上带可变状态（06 §6.6）。
type depthKey struct{}

func withDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, depthKey{}, d)
}

func depthOf(ctx context.Context) int {
	if v, ok := ctx.Value(depthKey{}).(int); ok {
		return v
	}
	return 0
}

func ctxDepth(ctx context.Context) int { return depthOf(ctx) }
