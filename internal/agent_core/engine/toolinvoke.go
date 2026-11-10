package engine

// InvokeTool 是 tool.invoke 宿主桥的引擎实现：在轮次外按内层工具自己的描述符
// 复用同一份 Runner（超时、截断、回执），让发现式工具插件与模型直呼走同一条执行路径。
//
// 只接受发现式工具（Descriptor.Discovery）：exec_tool 的契约是「执行 find_tool
// 列出的工具」，注入式工具模型本来就直呼其名，再开一条旁路只会让循环检测多一套入口。
//
// 审批策略已整体移除：这里不再有 tool.beforeCall / 策略失败关闭这一层。
//
// 它不写会话 entry：外层 exec_tool 的结果由引擎负责落库；这里只把内层结果
// 交回宿主桥，由插件决定内容/界面细节。

import (
	"context"

	"github.com/adag/ada-go/internal/common/domain"
)

// InvokeTool 执行一次内层工具调用。返回的 Result 携带内层回执状态
// （error/timeout 等），error 只用于宿主级失败（找不到工具、端口缺失）。
func InvokeTool(ctx context.Context, deps Deps, spec TurnSpec, call domain.ToolCall) (domain.Result, error) {
	if err := deps.Validate(); err != nil {
		return domain.Result{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	desc, ok := deps.Tools.Lookup(ctx, call.Tool)
	if !ok {
		return domain.Result{
			Content: []domain.Part{{Type: domain.PartText, Text: "tool_not_found: 该工具当前不可用"}},
			Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: "tool_not_found"},
		}, nil
	}
	if !desc.Discoverable() {
		// 注入式工具不能经 exec_tool 执行：它本来就该被模型直接调用，
		// 走旁路会绕过「模型只调用本轮声明过的工具」这条边界。
		return domain.Result{
			Content: []domain.Part{{Type: domain.PartText, Text: "tool_not_discoverable: " + call.Tool +
				" 不是发现式工具（它已直接注入模型工具清单，请直接调用）"}},
			Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: "tool_not_discoverable"},
		}, nil
	}
	if call.CallID == "" {
		call.CallID = spec.RunID
	}
	// 审批策略已移除：内层工具与模型直呼共用同一份 Runner，没有执行前钩子。
	res := deps.Runner.Execute(ctx, domain.ToolCall{CallID: call.CallID, Tool: call.Tool, Args: call.Args})
	return res, nil
}
