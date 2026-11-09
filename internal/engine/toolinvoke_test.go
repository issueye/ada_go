package engine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/engine"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

// InvokeTool 必须重跑内层描述符的审批链，而不是直接 Runner.Execute。
func TestInvokeToolRunsBeforeCallPolicy(t *testing.T) {
	t.Parallel()
	danger := domain.Descriptor{
		Name: "danger", Description: "写", Parameters: json.RawMessage(`{"type":"object"}`),
		Access: domain.AccessWrite, PluginID: "tools-test",
	}
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

	// 策略 deny：Runner 不得执行，返回 denied 回执。
	if err := h.hooks.Register(&plugins.HookHandler{
		PluginID: "policy-test", Trust: domain.TrustBuiltin,
		Point: ports.HookToolBeforeCall, MaxAction: plugins.ActionDeny,
		Fn: func(context.Context, ports.HookEvent) (ports.HookOutcome, error) {
			return ports.HookOutcome{PluginID: "policy-test", Action: ports.HookDeny, Reason: "不允许写"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err = engine.InvokeTool(context.Background(), h.deps, engine.TurnSpec{ThreadID: "th_1", RunID: "run_1"},
		domain.ToolCall{Tool: "danger", CallID: "call_2", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("InvokeTool deny: %v", err)
	}
	if res.Receipt.Status != domain.ReceiptDenied || len(h.runner.called()) != 1 {
		t.Fatalf("deny 后不应再执行 Runner: %+v / calls=%d", res, len(h.runner.called()))
	}
}
