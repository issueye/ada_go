package host

// ctx.turn.invoke 的边界：仅 builtin、prompt 必填/限额、只返回受理回执。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

func TestTurnInvokeBridge(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapTurnInvoke)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "turn_probe", description: "轮次探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const ok = await ctx.turn.invoke({ title: "定时任务", prompt: " 跑一下  " });
      let empty = "allowed";
      try {
        await ctx.turn.invoke({ prompt: "   " });
      } catch (e) {
        empty = String(e && e.code);
      }
      return { content: [{ type: "text", text: JSON.stringify({ ok, empty }) }] };
    },
  });
}
`
	script := testScript(t, caps, src)
	tools := &recordingTools{}
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	def := p.Contribution().Tools[0]
	var got TurnRequest
	hc := Context{TurnInvoke: func(_ context.Context, req TurnRequest) (TurnResult, error) {
		got = req
		return TurnResult{ThreadID: "th_plugin", RunID: "run_plugin"}, nil
	}}
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got.Prompt != "跑一下" || got.Title != "定时任务" {
		t.Fatalf("TurnRequest = %+v", got)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, `"runId":"run_plugin"`) || strings.Contains(text, `"empty":"allowed"`) {
		t.Fatalf("回执/空 prompt 校验不对: %s", text)
	}
}

func TestTurnInvokeNotInstalledWithoutCapability(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "turn_probe", description: "轮次探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) { return { content: [{ type: "text", text: String(typeof ctx.turn) }] }; },
  });
}
`
	script := testScript(t, caps, src)
	tools := &recordingTools{}
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "undefined" {
		t.Fatalf("未授予时 ctx.turn 应为 undefined，得到 %q", got)
	}
}
