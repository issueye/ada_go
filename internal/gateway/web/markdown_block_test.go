package web_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/plugins/official"
)

type blockPluginUI struct{}

func (blockPluginUI) RenderBlock(r *http.Request, rendererID, blockID string, version int, data json.RawMessage) (json.RawMessage, error) {
	switch rendererID {
	case "demo/card":
		return json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block"},"c":[{"t":"#text","v":"block hi"}]}`), nil
	case "demo/empty":
		// 渲染器显式返回空：合法结果，AST 应跳过该块。
		return json.RawMessage(`null`), nil
	case "demo/version":
		// 只接受 version 2，用于钉住封套 version 透传到渲染器。
		if version != 2 {
			return nil, errors.New("version mismatch")
		}
		return json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block"},"c":[{"t":"#text","v":"version 2"}]}`), nil
	default:
		return nil, errors.New("unknown renderer")
	}
}

func newBlockServer(t *testing.T) *web.Server {
	t.Helper()
	sup := &fakeSupervisor{core: newFakeCore()}
	srv, err := web.New(web.Config{
		Supervisor:  sup,
		Workspace:   "/tmp/ws",
		ProductName: "测试产品",
		PluginUI:    blockPluginUI{},
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func postJSON(t *testing.T, srv *web.Server, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestMarkdownASTDynamicBlock(t *testing.T) {
	srv := newBlockServer(t)
	text := "before\n\n```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{\"text\":\"hi\"}}\n```\n\nafter"
	rec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "adag-ui-block") || !strings.Contains(rec.Body.String(), `"v":"block hi"`) {
		t.Fatalf("AST 缺少动态块: %s", rec.Body.String())
	}
}

func TestMarkdownDynamicBlockFallbacks(t *testing.T) {
	srv := newBlockServer(t)
	// 未知 renderer：安全降级提示，且不吞掉前后的 Markdown。
	bad := "before\n\n```adag-ui\n{\"renderer\":\"demo/missing\",\"data\":{}}\n```\n\nafter"
	rec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(bad)+`}`)
	if !strings.Contains(rec.Body.String(), "adag-ui-block--error") {
		t.Fatalf("未知 renderer 应降级: %s", rec.Body.String())
	}
	for _, want := range []string{`"v":"before"`, `"v":"after"`} {
		wantContains(t, rec.Body.String(), want)
	}
	// 非法 JSON：保留代码块。
	invalid := "```adag-ui\n{not json}\n```"
	rec = postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(invalid)+`}`)
	if !strings.Contains(rec.Body.String(), "language-adag-ui") {
		t.Fatalf("非法 JSON 应保留代码块: %s", rec.Body.String())
	}
}

// TestMarkdownBlockLimits 钉单文档块数与单块字节上限（与 spec x-limits 一致）。
func TestMarkdownBlockLimits(t *testing.T) {
	srv := newBlockServer(t)

	var many strings.Builder
	for i := 0; i < web.MaxMarkdownBlocks+1; i++ {
		many.WriteString("```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{}}\n```\n\n")
	}
	rec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(many.String())+`}`)
	if got := strings.Count(rec.Body.String(), `"v":"block hi"`); got != web.MaxMarkdownBlocks {
		t.Fatalf("渲染块数 = %d，期望 %d", got, web.MaxMarkdownBlocks)
	}
	wantContains(t, rec.Body.String(), "动态块数量超过上限")

	huge := strings.Repeat("x", web.MaxMarkdownBlockBytes+1)
	text := "```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{\"t\":\"" + huge + "\"}}\n```"
	rec = postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	wantContains(t, rec.Body.String(), "动态块数据超过上限")
	if strings.Contains(rec.Body.String(), `"v":"block hi"`) {
		t.Fatal("超限块不应渲染")
	}
}

// TestMarkdownTokenUsageBlockIntegration 走真实 Bridge × 官方插件 × token-usage block。
// HTML 渲染已随 SSR 移除：这里钉住 /markdown.ast 输出块 VNode（客户端渲染同一份数据）。
func TestMarkdownTokenUsageBlockIntegration(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:        scripts,
		ProductName:    "测试产品",
		MarkdownBlocks: true,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	srv, err := web.New(web.Config{
		Supervisor:  &fakeSupervisor{core: newFakeCore()},
		Workspace:   "/tmp/ws",
		ProductName: "测试产品",
		PluginUI:    bridge,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	text := "```adag-ui\n{\"renderer\":\"token-usage/usage-card\",\"data\":{\"scope\":\"thread\",\"title\":\"本轮用量\",\"usage\":{\"inputTokens\":1234,\"outputTokens\":567,\"cacheTokens\":89,\"costMicros\":1200}}}\n```"
	astRec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if astRec.Code != http.StatusOK {
		t.Fatalf("ast status = %d: %s", astRec.Code, astRec.Body.String())
	}
	var astDoc struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(astRec.Body.Bytes(), &astDoc); err != nil {
		t.Fatalf("解析 AST: %v", err)
	}
	if len(astDoc.Nodes) != 1 {
		t.Fatalf("AST 顶层节点数 = %d，期望 1（整条消息只有一个块）", len(astDoc.Nodes))
	}
	out := string(astDoc.Nodes[0])
	for _, want := range []string{`"adag-ui-block"`, "tu-card", "1,234", "本轮用量"} {
		if !strings.Contains(out, want) {
			t.Fatalf("AST VNode 缺少 %q:\n%s", want, out)
		}
	}
}

// ---- 助手消息内动态块 e2e（MB-8） ----

const usageCardFence = "```adag-ui\n" +
	`{"renderer":"token-usage/usage-card","id":"b1","fallback":"用量卡片暂不可用",` +
	`"data":{"scope":"thread","title":"本轮用量","usage":{"inputTokens":1234,"outputTokens":567}}}` +
	"\n```"

// newBlockE2EServer 用真实 Bridge × token-usage 起一个助手消息里带动态块的服务；
// enabled 控制插件启停，events 收集块渲染审计。
func newBlockE2EServer(t *testing.T, enabled *bool, events *[]frontend.BlockAuditEvent) *web.Server {
	t.Helper()
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:        scripts,
		ProductName:    "测试产品",
		MarkdownBlocks: true,
		Enabled:        func(*http.Request, string) bool { return *enabled },
		BlockAudit:     func(e frontend.BlockAuditEvent) { *events = append(*events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	text := "本轮用量如下：\n\n" + usageCardFence + "\n\n以上。"
	core := newFakeCore().
		on("thread.get", blockThreadGet).
		on("thread.entries", blockEntries(text))
	srv, err := web.New(web.Config{
		Supervisor:  &fakeSupervisor{core: core},
		Workspace:   "/tmp/ws",
		ProductName: "测试产品",
		PluginUI:    bridge,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

const blockThreadGet = `{
  "thread": {"id":"th_1","workspace":"/tmp/ws","title":"用量会话","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},
  "items": [], "seq": 2,
  "run": {"runId":"run_1","status":"done","startedAt":"2026-10-28T10:00:00Z"},
  "interactions": [], "hasMore": false
}`

func blockEntries(text string) string {
	return `{"entries":[
	  {"id":"e1","threadId":"th_1","seq":1,"kind":"message","role":"user",
	   "payload":{"role":"user","parts":[{"type":"text","text":"本轮用量?"}]},
	   "createdAt":"2026-10-28T10:00:01Z"},
	  {"id":"e2","threadId":"th_1","seq":2,"kind":"message","role":"assistant",
	   "payload":{"role":"assistant","parts":[{"type":"text","text":` + strconvQuote(text) + `}]},
	   "createdAt":"2026-10-28T10:00:02Z"}
	],"hasMore":false}`
}

// transcriptAssistantNodes 拉取对话流 JSON 并返回助手行 text 段的 VNode 数据。
func transcriptAssistantNodes(t *testing.T, srv *web.Server) (string, json.RawMessage) {
	t.Helper()
	rec := serve(t, srv, "GET", "/partials/transcript/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/transcript/th_1 = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Rows []struct {
			Kind  string `json:"kind"`
			HTML  string `json:"html"`
			Parts []struct {
				Type  string          `json:"type"`
				Nodes json.RawMessage `json:"nodes"`
			} `json:"parts"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("transcript 不是合法 JSON: %v", err)
	}
	if len(payload.Rows) != 2 || payload.Rows[1].Kind != "assistant" || len(payload.Rows[1].Parts) != 1 {
		t.Fatalf("transcript rows = %+v", payload.Rows)
	}
	if payload.Rows[1].HTML != "" {
		t.Fatalf("assistant 行不应再带整行 HTML: %q", payload.Rows[1].HTML)
	}
	return payload.Rows[1].Kind, payload.Rows[1].Parts[0].Nodes
}

// TestAssistantMessageDynamicBlockE2E：块以 VNode 数据出现在对话流 JSON 里；
// 禁用插件后立即降级（不吃旧缓存）并记审计，重新启用后恢复。
func TestAssistantMessageDynamicBlockE2E(t *testing.T) {
	enabled := true
	var events []frontend.BlockAuditEvent
	srv := newBlockE2EServer(t, &enabled, &events)

	// 1) 对话流 JSON：助手 text 段的 VNode 里带块节点，且没有整行 HTML。
	_, nodes := transcriptAssistantNodes(t, srv)
	for _, want := range []string{"adag-ui-block", "tu-card", "1,234"} {
		wantContains(t, string(nodes), want)
	}
	if len(events) != 0 {
		t.Fatalf("正常渲染不应产生审计事件: %+v", events)
	}

	// 2) 禁用 token-usage：旧缓存不得绕过启停检查，块降级为 fallback 文本。
	enabled = false
	_, nodes = transcriptAssistantNodes(t, srv)
	wantContains(t, string(nodes), "adag-ui-block--error")
	wantContains(t, string(nodes), "用量卡片暂不可用")
	if strings.Contains(string(nodes), "tu-card") {
		t.Fatalf("禁用插件后仍渲染出块: %s", nodes)
	}
	found := false
	for _, e := range events {
		if e.Kind == "ui.block.render_failed" && e.PluginID == "token-usage" && e.Error == "plugin_disabled" {
			found = true
			if e.BlockID != "b1" {
				t.Fatalf("审计 block id = %q，期望 b1", e.BlockID)
			}
		}
		if strings.Contains(e.Error, "1234") || strings.Contains(e.Error, "{") {
			t.Fatalf("审计泄露 data: %+v", e)
		}
	}
	if !found {
		t.Fatalf("缺少 plugin_disabled 审计: %+v", events)
	}

	// 3) 重新启用：块恢复渲染。
	enabled = true
	_, nodes = transcriptAssistantNodes(t, srv)
	wantContains(t, string(nodes), "tu-card")
	wantContains(t, string(nodes), "1,234")
}

// TestMarkdownBlockFenceContext 钉住 fence 上下文：只有列 0、且不在其他代码块内的
// ```adag-ui 才执行；代码块里演示语法（外层 fence / 4 空格缩进 / 块引用）保持原样。
func TestMarkdownBlockFenceContext(t *testing.T) {
	srv := newBlockServer(t)
	block := "{\"renderer\":\"demo/card\",\"data\":{}}"
	cases := map[string]string{
		"外层 markdown fence": "说明：\n\n```markdown\n```adag-ui\n" + block + "\n```\n```\n\n结束",
		"4 空格缩进":            "示例：\n\n    ```adag-ui\n    " + block + "\n    ```\n",
		"块引用":               "示例：\n\n> ```adag-ui\n> " + block + "\n> ```\n",
	}
	for name, text := range cases {
		astRec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
		if strings.Contains(astRec.Body.String(), "adag-ui-block") {
			t.Fatalf("%s：AST 路径不应产出块节点\n%s", name, astRec.Body.String())
		}
	}

	// 正控：普通代码块之后、列 0 的 adag-ui 仍然执行。
	text := "```js\nconst a = 1;\n```\n\n```adag-ui\n" + block + "\n```\n"
	rec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if !strings.Contains(rec.Body.String(), `"v":"block hi"`) {
		t.Fatalf("列 0 的块应正常执行\n%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "language-js") {
		t.Fatalf("普通代码块应保留\n%s", rec.Body.String())
	}
}

// TestMarkdownEmptyBlock 钉住「渲染器返回 null ＝ 空块」：AST 跳过，
// 不画错误卡，也不影响前后 Markdown。
func TestMarkdownEmptyBlock(t *testing.T) {
	srv := newBlockServer(t)
	text := "before\n\n```adag-ui\n{\"renderer\":\"demo/empty\",\"data\":{}}\n```\n\nafter"

	astRec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if strings.Contains(astRec.Body.String(), "adag-ui-block") {
		t.Fatalf("空块不应进入 AST: %s", astRec.Body.String())
	}
	for _, want := range []string{`"v":"before"`, `"v":"after"`} {
		wantContains(t, astRec.Body.String(), want)
	}
}

// TestMarkdownBlockEnvelope 钉住封套 Schema：未知字段 / 尾随 JSON / 非法 id /
// 超长 fallback 都按「封套失败」退回普通代码块；version 缺省 1、显式值透传。
func TestMarkdownBlockEnvelope(t *testing.T) {
	srv := newBlockServer(t)
	render := func(payload string) string {
		t.Helper()
		text := "```adag-ui\n" + payload + "\n```"
		rec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	for _, tc := range []struct{ name, payload string }{
		{"未知字段", `{"renderer":"demo/card","data":{},"bogus":1}`},
		{"尾随 JSON", `{"renderer":"demo/card","data":{}}{"x":1}`},
		{"非法 id", `{"renderer":"demo/card","id":"1bad","data":{}}`},
		{"fallback 超长", `{"renderer":"demo/card","fallback":"` + strings.Repeat("x", 513) + `","data":{}}`},
		{"version 非正", `{"renderer":"demo/card","version":-1,"data":{}}`},
	} {
		out := render(tc.payload)
		if strings.Contains(out, `"v":"block hi"`) || !strings.Contains(out, "language-adag-ui") {
			t.Fatalf("%s：应退回普通代码块\n%s", tc.name, out)
		}
	}

	// version 缺省 = 1：正常渲染。
	if !strings.Contains(render(`{"renderer":"demo/card","data":{}}`), `"v":"block hi"`) {
		t.Fatal("缺省 version 应渲染")
	}
	// 显式 version 透传给渲染器（fake 只接受 2）。
	if !strings.Contains(render(`{"renderer":"demo/version","version":2,"data":{}}`), `"v":"version 2"`) {
		t.Fatal("version 2 应透传给渲染器")
	}
	if strings.Contains(render(`{"renderer":"demo/version","version":1,"data":{}}`), `"v":"version 2"`) {
		t.Fatal("version 1 不应渲染成 version 2")
	}
}

// TestMarkdownBlockDuplicateID 钉住「同一 Markdown 内 id 唯一」：重复的块降级，
// 不静默复用锚点，也不影响其它块。
func TestMarkdownBlockDuplicateID(t *testing.T) {
	srv := newBlockServer(t)
	text := "```adag-ui\n{\"renderer\":\"demo/card\",\"id\":\"a\",\"data\":{}}\n```\n\n" +
		"```adag-ui\n{\"renderer\":\"demo/card\",\"id\":\"a\",\"data\":{}}\n```"
	astRec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if got := strings.Count(astRec.Body.String(), `"v":"block hi"`); got != 1 {
		t.Fatalf("AST 只应有一个渲染成功的块，got %d: %s", got, astRec.Body.String())
	}
	wantContains(t, astRec.Body.String(), "adag-ui-block--error")
	wantContains(t, astRec.Body.String(), "动态块 id 重复")
}

// jsonEqual 按语义（键序无关）比较两段 JSON。
func jsonEqual(t *testing.T, got, want []byte) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("解析 got JSON: %v", err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("解析 want JSON: %v", err)
	}
	gb, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("序列化 got JSON: %v", err)
	}
	wb, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("序列化 want JSON: %v", err)
	}
	return bytes.Equal(gb, wb)
}
