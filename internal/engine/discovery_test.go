package engine_test

// 发现式工具（discovery: true）的引擎语义：
//   - 不注入模型工具清单；模型直呼其名 → tool_not_found；
//   - 目录里有发现式工具时，基座入口 find_tool / exec_tool 注入模型；
//   - exec_tool 的内层调用走与注入式完全相同的审批链（beforeCall 钩子 →
//     失败关闭 → Runner），内层回执状态原样上浮。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

// discoveryDescriptor 构造一个发现式工具描述符（testkit.Descriptor 不带该字段）。
func discoveryDescriptor(name, pluginID string, access domain.Access) domain.Descriptor {
	d := testkit.Descriptor(name, pluginID, access)
	d.Discovery = true
	return d
}

// toolEntryPayload 取线程 entries 里 role=tool、callId 匹配的回执 payload 原文。
func toolEntryPayload(t *testing.T, h *harness, threadID, callID string) string {
	t.Helper()
	for _, e := range h.store.Entries(threadID) {
		if e.Role != domain.RoleTool {
			continue
		}
		if strings.Contains(string(e.Payload), `"callId":"`+callID+`"`) {
			return string(e.Payload)
		}
	}
	t.Fatalf("找不到 callId=%s 的工具回执 entry", callID)
	return ""
}

// requestToolNames 取第 n 次模型请求里的工具名清单。
func requestToolNames(t *testing.T, h *harness, n int) []string {
	t.Helper()
	reqs := h.provider.Requests()
	if n >= len(reqs) {
		t.Fatalf("模型请求只有 %d 次，取不到第 %d 次", len(reqs), n+1)
	}
	names := make([]string, 0, len(reqs[n].Tools))
	for _, s := range reqs[n].Tools {
		names = append(names, s.Name)
	}
	return names
}

func assertContains(t *testing.T, label string, list []string, want string) {
	t.Helper()
	for _, name := range list {
		if name == want {
			return
		}
	}
	t.Fatalf("%s 缺少 %q（实际：%v）", label, want, list)
}

func assertNotContains(t *testing.T, label string, list []string, absent string) {
	t.Helper()
	for _, name := range list {
		if name == absent {
			t.Fatalf("%s 不应出现 %q（实际：%v）", label, absent, list)
		}
	}
}

// 发现式工具不注入模型清单；存在发现式工具时基座入口注入。
func TestDiscoveryToolsHiddenFromModelButFindable(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_find", "find_tool", `{}`),
		testkit.TextTurn("找到了"),
	)
	h := newHarness(t, model,
		testkit.Descriptor("read_file", "tools-fs", domain.AccessRead),
		discoveryDescriptor("lazy_tool", "tools-lazy", domain.AccessRead),
	)
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "发现工具"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q，期望 completed", got)
	}

	// 模型请求：注入式 + 基座入口在，发现式不在。
	names := requestToolNames(t, h, 0)
	assertContains(t, "模型请求", names, "read_file")
	assertContains(t, "模型请求", names, "find_tool")
	assertContains(t, "模型请求", names, "exec_tool")
	assertNotContains(t, "模型请求", names, "lazy_tool")

	// find_tool 的结果列出发现式工具（名称/说明/参数 Schema）。
	// payload 是 JSON 套 JSON：content 文本里的引号被再转义一层。
	payload := toolEntryPayload(t, h, th.ID, "call_find")
	for _, want := range []string{`lazy_tool`, `契约测试用的 lazy_tool`, `parameters`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("find_tool 结果缺少 %s：%s", want, payload)
		}
	}
	// find 不执行任何工具。
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("find_tool 不应执行工具，实际 %v", calls)
	}
}

// 目录里没有发现式工具时，基座入口不注入（不为用不上的入口花 token）。
func TestBaseToolsNotInjectedWithoutDiscovery(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(testkit.TextTurn("纯注入式"))
	h := newHarness(t, model, testkit.Descriptor("read_file", "tools-fs", domain.AccessRead))
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "hi"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	names := requestToolNames(t, h, 0)
	assertContains(t, "模型请求", names, "read_file")
	assertNotContains(t, "模型请求", names, "find_tool")
	assertNotContains(t, "模型请求", names, "exec_tool")
}

// exec_tool 按名执行发现式工具：内层调用进 Runner，结果原样上浮。
func TestExecToolRunsDiscoveredTool(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_exec", "exec_tool", `{"tool":"lazy_tool","args":{"q":"x"}}`),
		testkit.TextTurn("执行完了"),
	)
	h := newHarness(t, model, discoveryDescriptor("lazy_tool", "tools-lazy", domain.AccessRead))
	h.runner.res["lazy_tool"] = domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: "ran lazy"}},
		Details: map[string]any{"q": "x"},
	}
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "执行"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}

	calls := h.runner.called()
	if len(calls) != 1 || calls[0].Tool != "lazy_tool" {
		t.Fatalf("Runner 应恰好收到一次 lazy_tool 调用，实际 %+v", calls)
	}
	var args map[string]any
	if err := json.Unmarshal(calls[0].Args, &args); err != nil || args["q"] != "x" {
		t.Fatalf("内层参数不对: %s (%v)", calls[0].Args, err)
	}
	// exec_tool 的回执 = 内层回执（ok），内容是内层产出，Details 带内层工具名。
	payload := toolEntryPayload(t, h, th.ID, "call_exec")
	for _, want := range []string{`"tool":"exec_tool"`, `ran lazy`, `"tool":"lazy_tool"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("exec_tool 回执缺少 %s：%s", want, payload)
		}
	}
}

// 模型直呼发现式工具 → tool_not_found（契约是发现后经 exec_tool 执行）。
func TestDiscoveryToolDirectCallIsNotFound(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_direct", "lazy_tool", `{"q":"x"}`),
		testkit.TextTurn("换条路"),
	)
	h := newHarness(t, model, discoveryDescriptor("lazy_tool", "tools-lazy", domain.AccessRead))
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "直接调"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	payload := toolEntryPayload(t, h, th.ID, "call_direct")
	if !strings.Contains(payload, "tool_not_found") {
		t.Fatalf("直呼发现式工具应 tool_not_found：%s", payload)
	}
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("不应执行任何工具，实际 %v", calls)
	}
}

// exec_tool 只接受发现式工具：注入式与未知工具都给可读错误。
func TestExecToolRejectsInjectedAndUnknown(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_injected", "exec_tool", `{"tool":"read_file","args":{}}`),
		testkit.ToolTurn("call_unknown", "exec_tool", `{"tool":"nope","args":{}}`),
		testkit.TextTurn("知道了"),
	)
	h := newHarness(t, model,
		testkit.Descriptor("read_file", "tools-fs", domain.AccessRead),
		discoveryDescriptor("lazy_tool", "tools-lazy", domain.AccessRead),
	)
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "校验目标"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	if p := toolEntryPayload(t, h, th.ID, "call_injected"); !strings.Contains(p, "invalid_target") {
		t.Fatalf("注入式目标应 invalid_target：%s", p)
	}
	if p := toolEntryPayload(t, h, th.ID, "call_unknown"); !strings.Contains(p, "tool_not_found") {
		t.Fatalf("未知目标应 tool_not_found：%s", p)
	}
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("两种拒绝都不应进 Runner，实际 %v", calls)
	}
}

// 审批链保留：deny 钩子按内层描述符裁决，exec_tool 上浮 denied，内层不执行。
func TestExecToolHonorsDenyHook(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_exec", "exec_tool", `{"tool":"lazy_tool","args":{}}`),
		testkit.TextTurn("被拒后我换个说法"),
	)
	h := newHarness(t, model, discoveryDescriptor("lazy_tool", "tools-lazy", domain.AccessWrite))
	if err := h.hooks.Register(&plugins.HookHandler{
		PluginID: "policy-test", Trust: domain.TrustBuiltin,
		Point: ports.HookToolBeforeCall, MaxAction: plugins.ActionDeny,
		Fn: func(_ context.Context, evt ports.HookEvent) (ports.HookOutcome, error) {
			// 快照必须是内层工具：策略插件看到真实工具名与参数
			// （引擎 run 路径的钩子事件只带 Point/ThreadID/RunID/Snapshot，
			// 工具身份在 snapshot JSON 里）。
			if !strings.Contains(string(evt.Snapshot), `"tool":"lazy_tool"`) {
				t.Errorf("钩子快照应指向内层工具 lazy_tool：%s", evt.Snapshot)
			}
			return ports.HookOutcome{PluginID: "policy-test", Action: ports.HookDeny, Reason: "不允许写"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "执行被拒"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q，期望 completed（拒绝不终止轮次）", got)
	}
	payload := toolEntryPayload(t, h, th.ID, "call_exec")
	if !strings.Contains(payload, `"denied"`) || !strings.Contains(payload, "不允许写") {
		t.Fatalf("exec_tool 应上浮 denied 回执：%s", payload)
	}
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("被拒的内层不应进 Runner，实际 %v", calls)
	}
}

// 失败关闭：没有策略插件时，经 exec_tool 调 write 工具同样被拒。
func TestExecToolFailsClosedWithoutPolicy(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_exec", "exec_tool", `{"tool":"lazy_tool","args":{}}`),
		testkit.TextTurn("知道了"),
	)
	h := newHarness(t, model, discoveryDescriptor("lazy_tool", "tools-lazy", domain.AccessWrite))
	h.deps.HasPolicy = func() bool { return false }
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "无策略"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	payload := toolEntryPayload(t, h, th.ID, "call_exec")
	if !strings.Contains(payload, `"denied"`) {
		t.Fatalf("无策略 + write 内层应 denied：%s", payload)
	}
	if calls := h.runner.called(); len(calls) != 0 {
		t.Fatalf("失败关闭不应执行内层，实际 %v", calls)
	}
}

// find_tool 的 query 过滤：按名称/说明包含匹配，大小写不敏感。
func TestFindToolQueryFilter(t *testing.T) {
	t.Parallel()
	model := testkit.NewScriptedModel(
		testkit.ToolTurn("call_find", "find_tool", `{"query":"DATABASE"}`),
		testkit.TextTurn("ok"),
	)
	// StaticCatalog.List 返回副本，改不了已注册的描述符：直接构造带说明的描述符。
	dbTool := discoveryDescriptor("db_query", "tools-db", domain.AccessRead)
	dbTool.Description = "database 查询工具"
	h := newHarness(t, model,
		dbTool,
		discoveryDescriptor("fs_stat", "tools-fs", domain.AccessRead),
	)
	th, _ := h.store.CreateThread(context.Background(), h.workspace, "t")
	if got := h.run(t, context.Background(), th.ID, "查数据库工具"); got != domain.TerminalCompleted {
		t.Fatalf("终态 = %q", got)
	}
	payload := toolEntryPayload(t, h, th.ID, "call_find")
	if !strings.Contains(payload, "db_query") {
		t.Fatalf("query 命中的工具应出现：%s", payload)
	}
	if strings.Contains(payload, "fs_stat") {
		t.Fatalf("未命中的工具不应出现：%s", payload)
	}
}
