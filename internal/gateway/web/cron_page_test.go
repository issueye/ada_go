package web_test

// 真实 Bridge × 官方 frontend 模板：钉住 cron-manager 管理页可渲染、可导航。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/plugins/official"
)

func TestCronManagerPageRenders(t *testing.T) {
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

	if got := bridge.PageTitle("cron-manager", "cron"); got != "定时任务" {
		t.Fatalf("PageTitle = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "/ext/cron-manager/cron", nil)
	html, err := bridge.Page("cron-manager", "cron", req)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	// 页面仍是 SSR 骨架 + legacy 脚本：钉住与其他插件页同一套结构，
	// 任务行、运行历史与 toast 由 static/cron-manager.js 在浏览器填充。
	for _, want := range []string{
		"data-cm-page",
		"cm-title",
		"cm-summary",
		"data-cm-filter=\"all\"",
		"data-cm-count-all",
		"data-cm-form",
		"data-cm-preset",
		"data-cm-workspace",
		"cm-row--head",
		"data-cm-rows",
		"data-cm-runs",
		"cm-foot",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("管理页缺少 %q:\n%s", want, html)
		}
	}
	// 调度预设与星期沿用宿主 Select（编译成 <ada-select>），不允许原生下拉。
	if strings.Contains(string(html), "<select") {
		t.Fatalf("插件页不应使用原生 <select>:\n%s", html)
	}

	found := false
	for _, p := range bridge.List(req) {
		if p.ID != "cron-manager" {
			continue
		}
		found = true
		if !p.Enabled || p.FirstPage != "cron" || p.PageCount != 1 {
			t.Fatalf("前端摘要不符: %+v", p)
		}
	}
	if !found {
		t.Fatal("前端插件列表里没有 cron-manager")
	}

	if _, ok := bridge.StaticAsset("cron-manager", "cron-manager.js"); !ok {
		t.Fatal("缺少 static/cron-manager.js")
	}
	if _, ok := bridge.StaticAsset("cron-manager", "cron-manager.css"); !ok {
		t.Fatal("缺少 static/cron-manager.css")
	}
}
