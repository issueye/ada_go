package web_test

// 真实 Bridge × 官方 frontend 模板：钉住 env-manager 管理页可渲染、可导航。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/plugins/official"
)

func TestEnvManagerPageRenders(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     scripts,
		ProductName: "测试产品",
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	if got := bridge.PageTitle("env-manager", "settings"); got != "环境变量" {
		t.Fatalf("PageTitle = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/ext/env-manager/settings", nil)
	html, err := bridge.Page("env-manager", "settings", req)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	for _, want := range []string{
		"data-em-page",
		"环境变量",
		"data-em-add",
		"data-em-rows",
		"data-em-scope",
		"data-em-value",
		"data-em-bins",
		"data-em-bins-list",
		"服务程序",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("管理页缺少 %q:\n%s", want, html)
		}
	}

	found := false
	for _, p := range bridge.List(req) {
		if p.ID != "env-manager" {
			continue
		}
		found = true
		if !p.Enabled || p.FirstPage != "settings" || p.PageCount != 1 {
			t.Fatalf("前端摘要不符: %+v", p)
		}
		if len(p.Nav) != 1 || p.Nav[0].Label != "环境变量" || p.Nav[0].Slug != "settings" {
			t.Fatalf("导航不符: %+v", p.Nav)
		}
	}
	if !found {
		t.Fatal("前端插件列表里没有 env-manager")
	}

	if _, ok := bridge.StaticAsset("env-manager", "env-manager.js"); !ok {
		t.Fatal("缺少 static/env-manager.js")
	}
	if _, ok := bridge.StaticAsset("env-manager", "env-manager.css"); !ok {
		t.Fatal("缺少 static/env-manager.css")
	}
}
