package engine_test

// 工具可见性的端到端口径：模型声明、直呼执行与 exec_tool 内层执行必须问同一份规则
// （domain.ToolVisibleInjected / ToolVisibleDiscovery），不能各自过滤。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

func schemaNames(req ports.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		out = append(out, t.Name)
	}
	return out
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// 发现式工具不注入模型声明；系统入口 find_tool/exec_tool 是注入式，照常出现。
func TestToolSchemasExcludeDiscoveryTools(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(testkit.TextTurn("好了"))
	injected := testkit.Descriptor("read_file", "tools-fs", domain.AccessRead)
	lazy := discoveryDescriptor("cron_list", domain.AccessRead)
	find := domain.Descriptor{
		Name: domain.ToolFindName, Description: "发现工具", Parameters: json.RawMessage(`{"type":"object"}`),
		Access: domain.AccessRead, PluginID: "tool-discovery", System: true,
	}
	exec := find
	exec.Name = domain.ToolExecName
	h := newHarness(t, model, injected, lazy, find, exec)

	th, err := h.store.CreateThread(context.Background(), h.workspace, "t")
	if err != nil {
		t.Fatalf("建线程失败: %v", err)
	}
	if got := h.run(t, context.Background(), th.ID, "看看工具"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	reqs := h.provider.Requests()
	if len(reqs) == 0 {
		t.Fatal("没有发出模型请求")
	}
	names := schemaNames(reqs[0])
	if contains(names, "cron_list") {
		t.Fatalf("发现式工具不应注入模型声明: %v", names)
	}
	for _, want := range []string{"read_file", domain.ToolFindName, domain.ToolExecName} {
		if !contains(names, want) {
			t.Fatalf("模型声明缺少 %s: %v", want, names)
		}
	}
}

// 模型直呼发现式工具名必须 tool_not_found：它的契约是「经 find_tool 发现」。
func TestDirectCallToDiscoveryToolIsNotFound(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_1", "cron_list", `{}`),
		testkit.TextTurn("工具不可用"),
	)
	h := newHarness(t, model, discoveryDescriptor("cron_list", domain.AccessRead))
	th, err := h.store.CreateThread(context.Background(), h.workspace, "t")
	if err != nil {
		t.Fatalf("建线程失败: %v", err)
	}
	if got := h.run(t, context.Background(), th.ID, "列出任务"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("发现式工具不应被直接执行: %+v", calls)
	}
	entries := h.store.Entries(th.ID)
	var sawNotFound bool
	for _, e := range entries {
		if e.Role == domain.RoleTool && containsText(e.Payload, "tool_not_found") {
			sawNotFound = true
		}
	}
	if !sawNotFound {
		t.Fatalf("应给模型 tool_not_found 回执: %+v", entries)
	}
}

func containsText(payload json.RawMessage, want string) bool {
	return len(payload) > 0 && json.Valid(payload) && stringContains(string(payload), want)
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
