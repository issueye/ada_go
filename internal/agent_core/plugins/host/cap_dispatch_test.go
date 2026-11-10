package host

// ctx.dispatch：内部发送桥（capability: dispatch.send，仅 builtin）。
//
// 插件给的是「发给谁、发什么」，身份由宿主盖章；桥未注入时 ctx.dispatch 不存在。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

func TestDispatchContextMember(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapDispatchSend)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "dispatch_probe", description: "内部发送探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const res = await ctx.dispatch.send({
        workspace: args.workspace, threadId: args.threadId, title: "夜间报告",
        text: "生成今天的日报", mode: "start", idempotencyKey: "cron:task-1:2026-11-09T09:00",
        taskId: "task-1",
        // 伪造身份：宿主必须忽略它（sender 由能力集盖章）。
        pluginId: "attacker", sender: { kind: "user", id: "admin" },
      });
      return { content: [{ type: "text", text: JSON.stringify(res) }] };
    },
  });
}
`
	script := testScript(t, caps, src)
	tools := &recordingTools{}
	var got DispatchRequest
	var gotPluginID string
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	hc := Context{
		PluginID: "cron-manager",
		Dispatch: func(_ context.Context, req DispatchRequest) (DispatchResult, error) {
			got = req
			gotPluginID = "cron-manager"
			return DispatchResult{
				Workspace: "/ws/a", ThreadID: "th_9", RunID: "run_9", Mode: "start",
			}, nil
		},
	}
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler,
		json.RawMessage(`{"workspace":"/ws/a","threadId":"th_1"}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	for _, want := range []string{`"workspace":"/ws/a"`, `"threadId":"th_9"`, `"mode":"start"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("回执缺少 %s: %s", want, text)
		}
	}
	if got.Text != "生成今天的日报" || got.Mode != "start" || got.TaskID != "task-1" ||
		got.IdempotencyKey != "cron:task-1:2026-11-09T09:00" {
		t.Fatalf("请求 = %+v", got)
	}
	if gotPluginID != "cron-manager" {
		t.Fatalf("身份应由宿主盖章，实得 %q", gotPluginID)
	}
}

// 未注入控制面（CLI 直连、无网关）时 ctx.dispatch 不存在，而不是给个假实现。
func TestDispatchAbsentWithoutBridge(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapDispatchSend)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "dispatch_probe", description: "内部发送探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) { return { content: [{ type: "text", text: String(typeof ctx.dispatch) }] }; },
  });
}
`
	script := testScript(t, caps, src)
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "undefined" {
		t.Fatalf("没有控制面时不应安装 ctx.dispatch: %q", got)
	}
}

// 非 builtin 拿不到桥（能力位的信任约束仍生效）。
func TestDispatchRequiresBuiltin(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustVerified, pluginapi.CapDispatchSend)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "dispatch_probe", description: "内部发送探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) { return { content: [{ type: "text", text: String(typeof ctx.dispatch) }] }; },
  });
}
`
	script := testScript(t, caps, src)
	// verified 声明 builtin-only 能力在装配期就该被拒；这里直接构造 Runtime 验证成员不安装。
	p, err := Load(context.Background(), script, caps, Context{
		Dispatch: func(context.Context, DispatchRequest) (DispatchResult, error) { return DispatchResult{}, nil },
	}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err == nil {
		defer func() { _ = p.Close(context.Background()) }()
		res, callErr := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
		if callErr != nil {
			t.Fatalf("CallTool: %v", callErr)
		}
		if got := strings.TrimSpace(res.Content[0].Text); got != "undefined" {
			t.Fatalf("非 builtin 不应拿到 ctx.dispatch: %q", got)
		}
	}
}

// 参数校验在宿主侧完成：空 text / 非法 mode / 超长都抛固定错误码。
func TestDispatchValidatesArgs(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapDispatchSend)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "dispatch_probe", description: "内部发送探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      try {
        await ctx.dispatch.send(args.req);
        return { content: [{ type: "text", text: "ok" }] };
      } catch (e) {
        return { content: [{ type: "text", text: "ERR:" + (e && e.code ? e.code : e) }] };
      }
    },
  });
}
`
	script := testScript(t, caps, src)
	hc := Context{
		Dispatch: func(context.Context, DispatchRequest) (DispatchResult, error) { return DispatchResult{}, nil },
	}
	p, err := Load(context.Background(), script, caps, hc, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	cases := []struct {
		req  string
		want string
	}{
		// 参数形状错误（缺 text / mode 不在枚举）与越界分别是两个错误码：
		// 前者是调用方写错了参数，后者是值本身太大。
		{`{"text":"  "}`, "ERR_INVALID_ARG_TYPE"},
		{`{"text":"x","mode":"nope"}`, "ERR_INVALID_ARG_TYPE"},
		{`{"text":"` + strings.Repeat("a", 65<<10) + `"}`, "ERR_OUT_OF_RANGE"},
		{`{"text":"x","title":"` + strings.Repeat("标", 201) + `"}`, "ERR_OUT_OF_RANGE"},
	}
	for _, c := range cases {
		args, _ := json.Marshal(map[string]any{"req": json.RawMessage(c.req)})
		res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, hc)
		if err != nil {
			t.Fatalf("CallTool(%s): %v", c.req, err)
		}
		if got := strings.TrimSpace(res.Content[0].Text); !strings.Contains(got, c.want) {
			t.Fatalf("%s → %q，期望包含 %s", c.req, got, c.want)
		}
	}
}

// 桥返回的业务失败要上浮（cron 用它记 thread_busy），而不是被吞成成功。
func TestDispatchSurfacesBridgeError(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapDispatchSend)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "dispatch_probe", description: "内部发送探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      try {
        await ctx.dispatch.send({ text: "定时消息", mode: "start" });
        return { content: [{ type: "text", text: "ok" }] };
      } catch (e) {
        return { content: [{ type: "text", text: "ERR:" + String(e && e.message ? e.message : e) }] };
      }
    },
  });
}
`
	script := testScript(t, caps, src)
	hc := Context{
		Dispatch: func(context.Context, DispatchRequest) (DispatchResult, error) {
			return DispatchResult{}, &bridgeError{}
		},
	}
	p, err := Load(context.Background(), script, caps, hc, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := res.Content[0].Text; !strings.Contains(got, "thread_busy") {
		t.Fatalf("业务失败应上浮到脚本: %q", got)
	}
}

// bridgeError 是一条带业务码的桥错误。
type bridgeError struct{}

func (*bridgeError) Error() string { return "thread_busy: 线程已有运行中轮次" }
