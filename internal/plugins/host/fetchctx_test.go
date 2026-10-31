package host

// ctx.fetch（net.fetch）的返回契约：text 必须是字符串属性（不是函数），
// status/ok 数值与布尔——这是 FromGo 桥接的硬边界，回归用真实 Load + 工具调用钉住。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
)

func TestCtxFetchReturnsStringText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"city":"成都","temp":"22"}`)
	}))
	defer srv.Close()

	caps := testCaps(t, plugins.TrustThirdParty, plugins.CapNetFetch)
	src := `
export default function register(agent) {
  agent.registerTool({
    name: "fetch_probe",
    description: "探测 ctx.fetch 返回契约",
    parameters: { type: "object", properties: {}, additionalProperties: false },
    access: "read",
    async execute(args, ctx) {
      const res = await ctx.fetch("` + srv.URL + `/weather", { method: "GET" });
      const kinds = [
        "status=" + typeof res.status,
        "ok=" + typeof res.ok,
        "text=" + typeof res.text,
      ];
      const data = JSON.parse(res.text);
      return { content: [{ type: "text", text: kinds.join("|") + " body=" + data.city }] };
    },
  });
}
`
	script := testScript(t, caps, src)
	bridge, bridgeErr := NewHTTPBridge(HTTPBridgeConfig{})
	if bridgeErr != nil {
		t.Fatalf("NewHTTPBridge: %v", bridgeErr)
	}
	defer bridge.Close()
	tools := &recordingTools{}
	hc := Context{Workspace: tempWorkspace(t), HTTP: bridge}
	p, err := Load(context.Background(), script, caps, hc, Options{
		Targets: Targets{Tools: tools},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	contrib := p.Contribution()
	if contrib == nil || len(contrib.Tools) != 1 {
		t.Fatalf("贡献工具数 = %+v，期望 1 个", contrib)
	}
	def := contrib.Tools[0]
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.Receipt.Status != domain.ReceiptOK {
		t.Fatalf("回执 = %#v", res.Receipt)
	}
	text := res.Content[0].Text
	for _, want := range []string{
		"status=number", "ok=boolean", "text=string",
		// FromGo 不支持 func：字符串属性保证 res.text 可直接 JSON.parse
		"body=成都",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("ctx.fetch 返回缺少 %q: %s", want, text)
		}
	}
	if strings.Contains(text, "function") {
		t.Fatalf("text 不应是函数: %s", text)
	}
}
