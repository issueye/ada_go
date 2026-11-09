package host

// tool.discovery 宿主桥：目录只读投影 + 内层工具执行回执；仅 builtin。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
)

func TestToolDiscoveryContextMembers(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapToolDiscovery)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "disc_probe", description: "发现桥探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const list = await ctx.catalog.list();
      const res = await ctx.tool.invoke({ tool: "inner_tool", args: { x: 1 } });
      return { content: [{ type: "text", text: JSON.stringify({ list, res }) }] };
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
	var gotCall domain.ToolCall
	hc := Context{
		ThreadID: "th_1",
		Catalog: func(context.Context) ([]domain.Descriptor, error) {
			return []domain.Descriptor{
				{Name: "lazy", Description: "懒加载", Access: domain.AccessRead, Discovery: true, Parameters: json.RawMessage(`{"type":"object"}`)},
				{Name: "eager", Description: "注入", Access: domain.AccessRead},
			}, nil
		},
		InvokeTool: func(_ context.Context, _ CallMeta, call domain.ToolCall) (domain.Result, error) {
			gotCall = call
			return domain.Result{
				Content: []domain.Part{{Type: domain.PartText, Text: "inner-ok"}},
				Receipt: domain.Receipt{Status: domain.ReceiptOK},
			}, nil
		},
	}
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "lazy") || strings.Contains(text, "eager") {
		t.Fatalf("目录投影混入注入式工具: %s", text)
	}
	if gotCall.Tool != "inner_tool" || string(gotCall.Args) != `{"x":1}` {
		t.Fatalf("内层调用 = %+v", gotCall)
	}
	if !strings.Contains(text, "inner-ok") || !strings.Contains(text, `"status":"ok"`) {
		t.Fatalf("内层回执没有上浮: %s", text)
	}
}

func TestToolDiscoveryBridgeRequiresBuiltin(t *testing.T) {
	caps := testCaps(t, plugins.TrustVerified, plugins.CapToolDiscovery)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "disc_probe", description: "发现桥探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) { return { content: [{ type: "text", text: String(typeof ctx.catalog) + "/" + String(typeof ctx.tool) }] }; },
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
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), Context{ThreadID: "th_1", Catalog: func(context.Context) ([]domain.Descriptor, error) { return nil, nil }, InvokeTool: func(context.Context, CallMeta, domain.ToolCall) (domain.Result, error) { return domain.Result{}, nil }})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "undefined/undefined" {
		t.Fatalf("非 builtin 不应拿到发现桥: %q", got)
	}
}
