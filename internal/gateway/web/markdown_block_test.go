package web_test

import (
	"bytes"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/plugins/official"
)

type blockPluginUI struct{}

func (blockPluginUI) Slot(string, *http.Request) (template.HTML, int, error) { return "", 0, nil }
func (blockPluginUI) Page(string, string, *http.Request) (template.HTML, error) {
	return "", web.ErrExtNotFound
}
func (blockPluginUI) Styles() []string  { return nil }
func (blockPluginUI) Scripts() []string { return nil }
func (blockPluginUI) RenderBlock(r *http.Request, rendererID, blockID string, data json.RawMessage) (json.RawMessage, template.HTML, error) {
	if rendererID != "demo/card" {
		return nil, "", web.ErrExtNotFound
	}
	return json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block"},"c":[{"t":"#text","v":"block hi"}]}`),
		template.HTML(`<div class="adag-ui-block">block hi</div>`), nil
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

func TestMarkdownASTDynamicBlock(t *testing.T) {
	srv := newBlockServer(t)
	text := "before\n\n```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{\"text\":\"hi\"}}\n```\n\nafter"
	rec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "adag-ui-block") || !strings.Contains(rec.Body.String(), "block hi") {
		t.Fatalf("AST 缺少动态块: %s", rec.Body.String())
	}
}

func TestMarkdownHTMLDynamicBlock(t *testing.T) {
	srv := newBlockServer(t)
	text := "```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{}}\n```"
	rec := postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(text)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "adag-ui-block") || !strings.Contains(rec.Body.String(), "block hi") {
		t.Fatalf("HTML 缺少动态块: %s", rec.Body.String())
	}
}

func TestMarkdownDynamicBlockFallbacks(t *testing.T) {
	srv := newBlockServer(t)
	// 未知 renderer：安全降级提示，且不吞掉前后的 Markdown。
	bad := "before\n\n```adag-ui\n{\"renderer\":\"demo/missing\",\"data\":{}}\n```\n\nafter"
	rec := postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(bad)+`}`)
	if !strings.Contains(rec.Body.String(), "adag-ui-block--error") {
		t.Fatalf("未知 renderer 应降级: %s", rec.Body.String())
	}
	for _, want := range []string{"before", "after"} {
		wantContains(t, rec.Body.String(), want)
	}
	// 非法 JSON：保留代码块。
	invalid := "```adag-ui\n{not json}\n```"
	rec = postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(invalid)+`}`)
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
	rec := postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(many.String())+`}`)
	if got := strings.Count(rec.Body.String(), "block hi"); got != web.MaxMarkdownBlocks {
		t.Fatalf("渲染块数 = %d，期望 %d", got, web.MaxMarkdownBlocks)
	}
	wantContains(t, rec.Body.String(), "动态块数量超过上限")

	astRec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(many.String())+`}`)
	wantContains(t, astRec.Body.String(), "动态块数量超过上限")

	huge := strings.Repeat("x", web.MaxMarkdownBlockBytes+1)
	text := "```adag-ui\n{\"renderer\":\"demo/card\",\"data\":{\"t\":\"" + huge + "\"}}\n```"
	rec = postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(text)+`}`)
	wantContains(t, rec.Body.String(), "动态块数据超过上限")
	if strings.Contains(rec.Body.String(), "block hi") {
		t.Fatal("超限块不应渲染")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestMarkdownTokenUsageBlockIntegration 走真实 Bridge × 官方插件 × token-usage block。
func TestMarkdownTokenUsageBlockIntegration(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:        scripts,
		Supervisor:     &fakeSupervisor{core: newFakeCore()},
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
	rec := postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(text)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tu-card") || !strings.Contains(rec.Body.String(), "1,234") {
		t.Fatalf("token-usage block 未渲染: %s", rec.Body.String())
	}
	// /markdown 与浏览器消费的 AST 都源自同一份 VNode；HTML 也必须一致。
	browserGolden, err := os.ReadFile(filepath.Join("..", "frontend", "testdata", "block", "usage-card.html"))
	if err != nil {
		t.Fatalf("读取 block HTML golden: %v", err)
	}
	if strings.TrimSpace(rec.Body.String()) != strings.TrimSpace(string(browserGolden)) {
		t.Fatalf("/markdown 与 golden HTML 不一致:\n--- got ---\n%s\n--- want ---\n%s", rec.Body.String(), browserGolden)
	}
	astRec := postJSON(t, srv, "/markdown.ast", `{"text":`+strconvQuote(text)+`}`)
	if astRec.Code != http.StatusOK {
		t.Fatalf("ast status = %d: %s", astRec.Code, astRec.Body.String())
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "markdown-block", "usage-card.ast.json"))
	if err != nil {
		t.Fatalf("读取 AST golden: %v", err)
	}
	if !jsonEqual(t, astRec.Body.Bytes(), golden) {
		t.Fatalf("AST 与 golden 不一致:\n--- got ---\n%s\n--- want ---\n%s", astRec.Body.String(), golden)
	}

	// 浏览器（VNode AST）与无 JS（/markdown 服务端 HTML）必须渲染出同一份块 DOM。
	var astDoc struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(astRec.Body.Bytes(), &astDoc); err != nil {
		t.Fatalf("解析 AST: %v", err)
	}
	if len(astDoc.Nodes) != 1 {
		t.Fatalf("AST 顶层节点数 = %d，期望 1", len(astDoc.Nodes))
	}
	browserHTML, err := frontend.RenderHTML(astDoc.Nodes[0])
	if err != nil {
		t.Fatalf("AST VNode → HTML: %v", err)
	}
	if strings.TrimSpace(string(browserHTML)) != strings.TrimSpace(rec.Body.String()) {
		t.Fatalf("AST 与 /markdown HTML 不一致:\n--- ast ---\n%s\n--- html ---\n%s", browserHTML, rec.Body.String())
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
		Supervisor:     &fakeSupervisor{core: newFakeCore()},
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

// TestAssistantMessageDynamicBlockE2E：块出现在助手消息的无 JS 页面与对话流 JSON 里；
// 禁用插件后立即降级（不吃旧缓存）并记审计，重新启用后恢复。
func TestAssistantMessageDynamicBlockE2E(t *testing.T) {
	enabled := true
	var events []frontend.BlockAuditEvent
	srv := newBlockE2EServer(t, &enabled, &events)

	// 1) 无 JS 页面：助手消息里渲染出 token-usage 卡片。
	body := serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	for _, want := range []string{
		"本轮用量如下",
		`data-adag-renderer="token-usage/usage-card"`,
		"tu-card",
		"1,234",
		"以上。",
	} {
		wantContains(t, body, want)
	}

	// 2) 对话流 JSON：助手 text 段的 VNode 里带块节点，且不再重复预渲染 HTML。
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
	nodes := string(payload.Rows[1].Parts[0].Nodes)
	for _, want := range []string{"adag-ui-block", "tu-card", "1,234"} {
		wantContains(t, nodes, want)
	}
	if payload.Rows[1].HTML != "" {
		t.Fatalf("assistant 行不应再带整行 HTML: %q", payload.Rows[1].HTML)
	}
	if len(events) != 0 {
		t.Fatalf("正常渲染不应产生审计事件: %+v", events)
	}

	// 3) 禁用 token-usage：旧缓存不得绕过启停检查，块降级为 fallback 文本。
	enabled = false
	body = serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	wantContains(t, body, "adag-ui-block--error")
	wantContains(t, body, "用量卡片暂不可用")
	if strings.Contains(body, "tu-card") {
		t.Fatalf("禁用插件后仍渲染出块: %s", body)
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

	// 4) 重新启用：块恢复渲染。
	enabled = true
	body = serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	wantContains(t, body, "tu-card")
	wantContains(t, body, "1,234")
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
