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
	// 未知 renderer：安全降级提示。
	bad := "```adag-ui\n{\"renderer\":\"demo/missing\",\"data\":{}}\n```"
	rec := postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(bad)+`}`)
	if !strings.Contains(rec.Body.String(), "adag-ui-block--error") {
		t.Fatalf("未知 renderer 应降级: %s", rec.Body.String())
	}
	// 非法 JSON：保留代码块。
	invalid := "```adag-ui\n{not json}\n```"
	rec = postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(invalid)+`}`)
	if !strings.Contains(rec.Body.String(), "language-adag-ui") {
		t.Fatalf("非法 JSON 应保留代码块: %s", rec.Body.String())
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

	text := "```adag-ui\n{\"renderer\":\"token-usage/usage-card\",\"data\":{\"scope\":\"thread\",\"title\":\"本轮用量\",\"usage\":{\"inputTokens\":1234,\"outputTokens\":567}}}\n```"
	rec := postJSON(t, srv, "/markdown", `{"text":`+strconvQuote(text)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tu-card") || !strings.Contains(rec.Body.String(), "1,234") {
		t.Fatalf("token-usage block 未渲染: %s", rec.Body.String())
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
