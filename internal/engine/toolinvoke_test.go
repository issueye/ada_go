package engine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/engine"
)

// InvokeTool 在轮次外直接复用同一份 Runner（审批策略已移除，没有执行前钩子）。
//
// 内层工具用发现式描述符：exec_tool 的契约就是「执行 find_tool 列出的工具」，
// 注入式工具经这条旁路执行会被拒（见 TestInvokeToolRejectsInjectedTool）。
func TestInvokeToolRunsThroughRunner(t *testing.T) {
	t.Parallel()
	danger := discoveryDescriptor("danger", domain.AccessWrite)
	h := newHarness(t, nil, danger)
	h.runner.res["danger"] = domain.Result{Content: []domain.Part{{Type: domain.PartText, Text: "ran"}}}

	res, err := engine.InvokeTool(context.Background(), h.deps, engine.TurnSpec{ThreadID: "th_1", RunID: "run_1"},
		domain.ToolCall{Tool: "danger", CallID: "call_1", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if res.Content[0].Text != "ran" || len(h.runner.called()) != 1 {
		t.Fatalf("未走 Runner: %+v / %+v", res, h.runner.called())
	}
	if res.Receipt.Status == domain.ReceiptDenied {
		t.Fatalf("审批策略已移除，不应出现 denied 回执: %+v", res.Receipt)
	}
}

// exec_tool 只能执行发现式工具：注入式工具模型本来就直呼其名，
// 再开一条 exec_tool 旁路会绕过「模型只调用本轮声明过的工具」这条边界。
func TestInvokeToolRejectsInjectedTool(t *testing.T) {
	t.Parallel()
	injected := domain.Descriptor{
		Name: "read_file", Description: "读文件", Parameters: json.RawMessage(`{"type":"object"}`),
		Access: domain.AccessRead, PluginID: "tools-fs",
	}
	if injected.Discovery {
		t.Fatal("夹具应当是注入式工具")
	}
	h := newHarness(t, nil, injected)

	res, err := engine.InvokeTool(context.Background(), h.deps, engine.TurnSpec{ThreadID: "th_1", RunID: "run_1"},
		domain.ToolCall{Tool: "read_file", CallID: "call_1", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if res.Receipt.Status != domain.ReceiptError || res.Receipt.Reason != "tool_not_discoverable" {
		t.Fatalf("注入式工具应被拒绝，实得 %+v", res.Receipt)
	}
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("被拒绝的调用不得进 Runner: %+v", calls)
	}
}

// 目录里没有的工具仍是 tool_not_found，不被新约束改写。
func TestInvokeToolUnknownTool(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil, discoveryDescriptor("lazy", domain.AccessRead))

	res, err := engine.InvokeTool(context.Background(), h.deps, engine.TurnSpec{ThreadID: "th_1", RunID: "run_1"},
		domain.ToolCall{Tool: "ghost", CallID: "call_1", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}
	if res.Receipt.Reason != "tool_not_found" {
		t.Fatalf("未知工具应为 tool_not_found，实得 %+v", res.Receipt)
	}
}

// discoveryDescriptor 造一个发现式工具描述符（fixture 共用）。
func discoveryDescriptor(name string, access domain.Access) domain.Descriptor {
	return domain.Descriptor{
		Name: name, Description: "发现式探针", Parameters: json.RawMessage(`{"type":"object"}`),
		Access: access, PluginID: "lazy-tools", Discovery: true,
	}
}
