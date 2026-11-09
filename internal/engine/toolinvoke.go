package engine

// InvokeTool 是 tool.invoke 宿主桥的引擎实现：在轮次外按内层工具自己的描述符
// 重跑完整的审批链（tool.beforeCall → policy 失败关闭 → Runner.Execute），
// 让发现式工具插件不能绕过策略插件（05 §6.3、06 §6.6、11 §16）。
//
// 它不写会话 entry：外层 exec_tool 的结果由引擎负责落库；这里只把内层结果
// 交回宿主桥，由插件决定内容/界面细节。

import (
	"context"

	"github.com/adag/ada-go/internal/domain"
)

// InvokeTool 执行一次内层工具调用。返回的 Result 携带内层回执状态
// （denied/timeout 等），error 只用于宿主级失败（找不到工具、钩子错误）。
func InvokeTool(ctx context.Context, deps Deps, spec TurnSpec, call domain.ToolCall) (domain.Result, error) {
	if err := deps.Validate(); err != nil {
		return domain.Result{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r := newRun(ctx, deps, spec)
	desc, ok := deps.Tools.Lookup(ctx, call.Tool)
	if !ok {
		return domain.Result{
			Content: []domain.Part{{Type: domain.PartText, Text: "tool_not_found: 该工具当前不可用"}},
			Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: "tool_not_found"},
		}, nil
	}
	if call.CallID == "" {
		call.CallID = spec.RunID
	}
	verdict, err := r.beforeCall(call, desc)
	if err != nil {
		return invokeDenied("hook failed", err.Error()), nil
	}
	if verdict.Aborted {
		return invokeError("aborted", "控制面钩子中止了内层工具调用: "+verdict.Reason), nil
	}
	if !verdict.Allowed {
		return invokeDenied(verdict.Reason, verdict.Reason), nil
	}
	args := call.Args
	if len(verdict.Replacements) > 0 {
		if replaced, ok := coerceArgs(verdict.Replacements); ok {
			args = replaced
		}
	}
	if reason, denied := r.policyDeny(desc); denied {
		return invokeDenied(reason, reason+"（没有可用的策略插件）"), nil
	}
	res := deps.Runner.Execute(ctx, domain.ToolCall{CallID: call.CallID, Tool: call.Tool, Args: args})
	r.hook(HookToolAfterCall, mustJSON(map[string]any{
		"callId": call.CallID, "tool": call.Tool, "status": string(res.Receipt.Status),
	}))
	return res, nil
}

func invokeDenied(reason, detail string) domain.Result {
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: "denied by policy: " + detail}},
		Receipt: domain.Receipt{Status: domain.ReceiptDenied, Reason: reason},
	}
}

func invokeError(reason, text string) domain.Result {
	return domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: text}},
		Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: reason},
	}
}
