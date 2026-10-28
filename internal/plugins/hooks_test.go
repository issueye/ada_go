package plugins_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

func evt(point ports.HookPoint) ports.HookEvent {
	return ports.HookEvent{Point: point, ThreadID: "th_1", RunID: "r1",
		Snapshot: json.RawMessage(`{"tool":"write_file","access":"write"}`)}
}

func handler(id string, trust domain.Trust, point ports.HookPoint, max plugins.HookAction,
	fn func(context.Context, ports.HookEvent) (ports.HookOutcome, error)) *plugins.HookHandler {
	return &plugins.HookHandler{PluginID: id, Trust: trust, Point: point, MaxAction: max, Fn: fn}
}

// priorityHandler 额外指定 priority，用于验证执行顺序。
func priorityHandler(id string, prio int, point ports.HookPoint,
	fn func(context.Context, ports.HookEvent) (ports.HookOutcome, error)) *plugins.HookHandler {
	h := handler(id, domain.TrustBuiltin, point, plugins.ActionNote, fn)
	h.Priority = prio
	return h
}

// TestHookOrderIsDeterministic 验证 priority 排序 + 同级按插件 id 稳定排序（06 §6.5）。
func TestHookOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()
	var order []string
	mk := func(id string, prio int) *plugins.HookHandler {
		return priorityHandler(id, prio, ports.HookToolAfterCall,
			func(_ context.Context, _ ports.HookEvent) (ports.HookOutcome, error) {
				order = append(order, id)
				return ports.HookOutcome{PluginID: id, Action: ports.HookNote, Note: id}, nil
			})
	}
	for _, h := range []*plugins.HookHandler{mk("zeta", 5), mk("alpha", 5), mk("beta", 1)} {
		if err := chain.Register(h); err != nil {
			t.Fatal(err)
		}
	}
	v, err := chain.Run(context.Background(), evt(ports.HookToolAfterCall))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "beta,alpha,zeta" {
		t.Fatalf("执行顺序 = %s，期望 beta,alpha,zeta", got)
	}
	if len(v.Notes) != 3 {
		t.Fatalf("note 未汇总：%v", v.Notes)
	}
}

// TestBeforeCallFailsClosed 验证 06 §6.6：tool.beforeCall 是唯一失败方向为关的点位。
func TestBeforeCallFailsClosed(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()
	if err := chain.Register(handler("policy", domain.TrustBuiltin, ports.HookToolBeforeCall, plugins.ActionDeny,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) {
			return ports.HookOutcome{}, errors.New("策略插件炸了")
		})); err != nil {
		t.Fatal(err)
	}
	v, err := chain.Run(context.Background(), evt(ports.HookToolBeforeCall))
	if err == nil {
		t.Fatal("安全点失败必须回传错误")
	}
	if v.Allowed {
		t.Fatal("安全点失败方向必须是拒绝")
	}
	if chain.FailureDirection(ports.HookToolBeforeCall) != domain.FailClosed {
		t.Fatal("tool.beforeCall 必须是 FailClosed")
	}
	if chain.FailureDirection(ports.HookTurnAfter) != domain.FailOpen {
		t.Fatal("非安全点应为 FailOpen")
	}
}

// TestObservationPointFailureIsIgnored 验证其它点位失败方向为开。
func TestObservationPointFailureIsIgnored(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()
	if err := chain.Register(handler("observer", domain.TrustThirdParty, ports.HookSessionStart, plugins.ActionObserve,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) {
			return ports.HookOutcome{}, errors.New("观察器超时")
		})); err != nil {
		t.Fatal(err)
	}
	v, err := chain.Run(context.Background(), evt(ports.HookSessionStart))
	if err != nil {
		t.Fatalf("观察点失败不应中断主流程: %v", err)
	}
	if !v.Allowed {
		t.Fatal("观察点失败不应改变裁决")
	}
	if len(chain.Warnings()) == 0 {
		t.Fatal("失败必须留下警告，不能静默")
	}
}

// TestHookTimeoutUsesContractBudget 验证 06 §6.6 的预算与失败方向：
// 默认 200ms、上限 2s；观察点超时不阻断主流程但必须留警告，安全点超时按拒绝收敛。
func TestHookTimeoutUsesContractBudget(t *testing.T) {
	t.Parallel()
	slow := func(ctx context.Context, _ ports.HookEvent) (ports.HookOutcome, error) {
		select {
		case <-time.After(2 * time.Second):
			return ports.HookOutcome{}, nil
		case <-ctx.Done():
			return ports.HookOutcome{}, ctx.Err()
		}
	}

	chain := plugins.NewHookChain()
	if err := chain.Register(&plugins.HookHandler{
		PluginID: "slow", Trust: domain.TrustThirdParty, Point: ports.HookUserMessage,
		MaxAction: plugins.ActionObserve, Timeout: 20 * time.Millisecond, Fn: slow,
	}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	v, err := chain.Run(context.Background(), evt(ports.HookUserMessage))
	if err != nil {
		t.Fatalf("观察点超时不应中断主流程: %v", err)
	}
	if !v.Allowed {
		t.Fatal("观察点超时改变了裁决")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("超时没生效，耗时 %s", elapsed)
	}
	if len(chain.Warnings()) == 0 || !strings.Contains(chain.Warnings()[0], "超时") {
		t.Fatalf("超时必须留下警告：%v", chain.Warnings())
	}

	// 安全点：同一个慢处理器挂在 tool.beforeCall 上 → 拒绝 + plugin_timeout。
	safety := plugins.NewHookChain()
	if err := safety.Register(&plugins.HookHandler{
		PluginID: "slow", Trust: domain.TrustBuiltin, Point: ports.HookToolBeforeCall,
		MaxAction: plugins.ActionDeny, Timeout: 20 * time.Millisecond, Fn: slow,
	}); err != nil {
		t.Fatal(err)
	}
	sv, err := safety.Run(context.Background(), evt(ports.HookToolBeforeCall))
	if err == nil {
		t.Fatal("安全点超时必须回传错误")
	}
	if sv.Allowed {
		t.Fatal("安全点超时必须按拒绝收敛")
	}
	if !strings.Contains(err.Error(), "plugin_timeout") {
		t.Fatalf("错误应映射到 plugin_timeout，实得 %v", err)
	}

	// 超过 2s 的钩子预算不允许登记（06 §6.6 最大 2s）。
	if err := chain.Register(&plugins.HookHandler{
		PluginID: "too-long", Trust: domain.TrustBuiltin, Point: ports.HookUserMessage,
		MaxAction: plugins.ActionObserve, Timeout: 3 * time.Second,
		Fn: func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { return ports.HookOutcome{}, nil },
	}); err == nil || !strings.Contains(err.Error(), "超过上限") {
		t.Fatalf("钩子超时超过 2s 必须拒绝登记，实得 %v", err)
	}
}

// TestTrustGating 验证 replace/abort 仅 builtin，且第三方无法弹窗（06 §6.6、03 §5.6）。
func TestTrustGating(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()

	if err := chain.Register(handler("3p", domain.TrustThirdParty, ports.HookToolBeforeCall, plugins.ActionAbort,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { return ports.HookOutcome{}, nil })); err == nil {
		t.Fatal("第三方不得登记 abort 等级的处理器")
	}
	if err := chain.Register(handler("3p", domain.TrustThirdParty, ports.HookModelBeforeRequest, plugins.ActionReplace,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { return ports.HookOutcome{}, nil })); err == nil {
		t.Fatal("第三方不得登记 replace")
	}
	if err := chain.Register(handler("3p", domain.TrustThirdParty, ports.HookTurnBefore, plugins.ActionDeny,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { return ports.HookOutcome{}, nil })); err == nil {
		t.Fatal("turn.before 不允许 deny，登记必须失败")
	}

	// 运行时越权：登记为 observe 的第三方返回 abort → 只能被忽略并留警告。
	if err := chain.Register(handler("3p", domain.TrustThirdParty, ports.HookToolAfterCall, plugins.ActionObserve,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) {
			return ports.HookOutcome{PluginID: "3p", Action: ports.HookAbort, Reason: "试图中止"}, nil
		})); err != nil {
		t.Fatal(err)
	}
	v, err := chain.Run(context.Background(), evt(ports.HookToolAfterCall))
	if err != nil {
		t.Fatal(err)
	}
	if v.Aborted || !v.Allowed {
		t.Fatalf("第三方 abort 必须被忽略：%+v", v)
	}
	if len(chain.Warnings()) == 0 {
		t.Fatal("越权动作必须留下警告")
	}
}

// TestRecursionGuard 验证钩子内不再触发同一点位的深度上限（06 §6.6 上限 2）。
func TestRecursionGuard(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()
	depth := 0
	var runErr error
	if err := chain.Register(&plugins.HookHandler{
		PluginID: "recur", Trust: domain.TrustThirdParty, Point: ports.HookUserMessage,
		MaxAction: plugins.ActionObserve,
		Fn: func(ctx context.Context, e ports.HookEvent) (ports.HookOutcome, error) {
			depth++
			if depth > 5 {
				return ports.HookOutcome{}, errors.New("递归失控")
			}
			_, err := chain.Run(ctx, e)
			if err != nil {
				runErr = err
				return ports.HookOutcome{}, nil
			}
			return ports.HookOutcome{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.Run(context.Background(), evt(ports.HookUserMessage)); err != nil {
		t.Fatalf("外层调用不应失败: %v", err)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "递归深度") {
		t.Fatalf("期望递归深度上限被触发，实得 %v", runErr)
	}
	if depth > domain.HookRecursionDepthLimit+1 {
		t.Fatalf("递归没被拦：depth=%d", depth)
	}
}

// TestPanicIsContained 验证处理器 panic 不带崩调用方（06 §6.4）。
func TestPanicIsContained(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()
	if err := chain.Register(handler("boom", domain.TrustThirdParty, ports.HookSessionEnd, plugins.ActionObserve,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { panic("炸了") })); err != nil {
		t.Fatal(err)
	}
	if _, err := chain.Run(context.Background(), evt(ports.HookSessionEnd)); err != nil {
		t.Fatalf("观察点 panic 应被忽略: %v", err)
	}
	if err := chain.Register(handler("boom2", domain.TrustBuiltin, ports.HookToolBeforeCall, plugins.ActionDeny,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { panic("炸了") })); err != nil {
		t.Fatal(err)
	}
	v, err := chain.Run(context.Background(), evt(ports.HookToolBeforeCall))
	if err == nil || v.Allowed {
		t.Fatalf("安全点 panic 必须按拒绝收敛: v=%+v err=%v", v, err)
	}
}

// TestUnknownPointAndDuplicatesRejected 验证点位集合封闭（15 个，全部有调用方）。
func TestUnknownPointAndDuplicatesRejected(t *testing.T) {
	t.Parallel()
	if len(plugins.HookPoints()) != 15 {
		t.Fatalf("钩子点位数量 = %d，期望 15", len(plugins.HookPoints()))
	}
	chain := plugins.NewHookChain()
	if err := chain.Register(handler("x", domain.TrustBuiltin, "tool.madeUp", plugins.ActionObserve,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { return ports.HookOutcome{}, nil })); err == nil {
		t.Fatal("未知点位必须拒绝登记")
	}
	fn := func(context.Context, ports.HookEvent) (ports.HookOutcome, error) { return ports.HookOutcome{}, nil }
	if err := chain.Register(handler("dup", domain.TrustBuiltin, ports.HookSessionStart, plugins.ActionObserve, fn)); err != nil {
		t.Fatal(err)
	}
	if err := chain.Register(handler("dup", domain.TrustBuiltin, ports.HookSessionStart, plugins.ActionObserve, fn)); err == nil {
		t.Fatal("同插件同点位重复登记必须失败")
	}
	if n := chain.RemovePlugin("dup"); n != 1 {
		t.Fatalf("RemovePlugin = %d", n)
	}
	if _, err := chain.Run(context.Background(), evt(ports.HookSessionStart)); err != nil {
		t.Fatal(err)
	}
}

// TestReplacePayloadMustBeJSON 验证替换载荷非法时按不允许收敛（引擎会重新校验）。
func TestReplacePayloadMustBeJSON(t *testing.T) {
	t.Parallel()
	chain := plugins.NewHookChain()
	if err := chain.Register(handler("builtin", domain.TrustBuiltin, ports.HookToolBeforeCall, plugins.ActionReplace,
		func(context.Context, ports.HookEvent) (ports.HookOutcome, error) {
			return ports.HookOutcome{PluginID: "builtin", Action: ports.HookReplace, Replacement: json.RawMessage(`{oops`)}, nil
		})); err != nil {
		t.Fatal(err)
	}
	v, err := chain.Run(context.Background(), evt(ports.HookToolBeforeCall))
	if err != nil {
		t.Fatal(err)
	}
	if v.Allowed {
		t.Fatal("非法替换载荷必须被拒绝")
	}
}
