package web_test

// 真实 Bridge × 官方 frontend 模板：钉住 mcp-client 管理页可渲染、可导航。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/plugins/official"
)

func TestMCPClientAdminPageRenders(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     scripts,
		ProductName: "测试产品",
		CSRF:        func(*http.Request) string { return "csrf-token" },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	if got := bridge.PageTitle("mcp-client", "servers"); got != "MCP 服务端" {
		t.Fatalf("PageTitle = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/ext/mcp-client/servers", nil)
	html, err := bridge.Page("mcp-client", "servers", req)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	for _, want := range []string{
		"data-mcp-page",
		"MCP 服务端",
		"data-mcp-servers",
		"data-mcp-editor",
		"data-mcp-highlight-code",
		"data-mcp-format",
		"data-mcp-config-status",
		"data-mcp-save",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("管理页缺少 %q:\n%s", want, html)
		}
	}

	// 侧栏导航与「打开插件页面」的入口都来自同一份清单。
	found := false
	for _, p := range bridge.List(req) {
		if p.ID != "mcp-client" {
			continue
		}
		found = true
		if !p.Enabled || p.FirstPage != "servers" || p.PageCount != 1 {
			t.Fatalf("前端摘要不符: %+v", p)
		}
		if len(p.Nav) != 1 || p.Nav[0].Label != "MCP 服务端" || p.Nav[0].Slug != "servers" {
			t.Fatalf("导航不符: %+v", p.Nav)
		}
	}
	if !found {
		t.Fatal("前端插件列表里没有 mcp-client")
	}

	// 静态资源要真实可加载：页面脚本与样式都已随包声明。
	if _, ok := bridge.StaticAsset("mcp-client", "mcp-client.js"); !ok {
		t.Fatal("缺少 static/mcp-client.js")
	}
	if _, ok := bridge.StaticAsset("mcp-client", "mcp-client.css"); !ok {
		t.Fatal("缺少 static/mcp-client.css")
	}
}
