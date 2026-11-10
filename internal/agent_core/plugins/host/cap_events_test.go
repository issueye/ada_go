package host

// ctx.events 的边界：只读、绑定当前线程、按类型/条数过滤；未声明能力时不存在。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

func TestEventsContextMember(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty, pluginapi.CapEventsRead)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "events_probe", description: "事件探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const rows = await ctx.events.query({ types: ["evt.usage"], limit: 1 });
      return { content: [{ type: "text", text: JSON.stringify(rows) }] };
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
	now := time.Now().UTC()
	hc := Context{
		ThreadID: "th_test",
		Events: func(_ context.Context, q EventQuery) ([]EventView, error) {
			if q.ThreadID != "th_test" {
				t.Fatalf("查询线程 = %q", q.ThreadID)
			}
			return []EventView{
				{Seq: 1, Type: "evt.usage", ThreadID: "th_test", At: now, Data: json.RawMessage(`{"inputTokens":3}`)},
				{Seq: 2, Type: "evt.error", ThreadID: "th_test", At: now, Data: json.RawMessage(`{}`)},
			}, nil
		},
	}
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	if strings.Contains(text, "evt.error") || !strings.Contains(text, "inputTokens") {
		t.Fatalf("事件过滤不对: %s", text)
	}
}

func TestEventsContextMemberRejectsOtherThread(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty, pluginapi.CapEventsRead)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "events_bad", description: "越权探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      try {
        await ctx.events.query({ threadId: "th_other" });
        return { content: [{ type: "text", text: "allowed" }] };
      } catch (e) {
        return { content: [{ type: "text", text: String(e && e.code) }] };
      }
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
	hc := Context{ThreadID: "th_test", Events: func(context.Context, EventQuery) ([]EventView, error) {
		t.Fatal("跨线程查询不应到达宿主回调")
		return nil, nil
	}}
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got == "allowed" {
		t.Fatal("跨线程查询没有被拒绝")
	}
}

func TestEventsUsageContextMember(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty, pluginapi.CapEventsRead)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "usage_probe", description: "用量探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const all = await ctx.events.usage({ limit: 10 });
      const mine = await ctx.events.usage({ threadId: "th_test", limit: 10 });
      let cross = "allowed";
      try {
        await ctx.events.usage({ threadId: "th_other" });
      } catch (e) {
        cross = String(e && e.code);
      }
      return { content: [{ type: "text", text: JSON.stringify({ all, mine, cross }) }] };
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
	now := time.Now().UTC()
	hc := Context{
		ThreadID: "th_test",
		Usage: func(_ context.Context, q UsageQuery) ([]UsageRow, error) {
			if q.ThreadID != "" && q.ThreadID != "th_test" {
				t.Fatalf("跨线程查询不应到达宿主回调: %q", q.ThreadID)
			}
			return []UsageRow{{
				ThreadID: "th_test", Provider: "p", Model: "m",
				InputTokens: 7, OutputTokens: 3, CacheTokens: 2, CostMicros: 5, At: now,
			}}, nil
		},
	}
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, `"inputTokens":7`) || !strings.Contains(text, `"costMicros":5`) {
		t.Fatalf("原始用量行没有透出: %s", text)
	}
	if strings.Contains(text, `"cross":"allowed"`) {
		t.Fatalf("跨线程 usage 查询没有被拒绝: %s", text)
	}
}

func TestEventsNotInstalledWithoutCapability(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "events_probe", description: "事件探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) { return { content: [{ type: "text", text: String(typeof ctx.events) }] }; },
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
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), Context{ThreadID: "th_test"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "undefined" {
		t.Fatalf("未授予时 ctx.events 应为 undefined，得到 %q", got)
	}
}
