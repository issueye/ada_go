package web_test

// 插件前端接线测试（SSR 移除后的收缩版）：
//   - /ext 静态资源 allowlist 与 512KB 限额（冻结契约）；
//   - 动作桥（ActionEnvelope）；
//   - PluginLister → /partials/extensions.json 的前端插件合并。

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/adag/ada-go/internal/gateway/web"
)

// stubUI 是 PluginUI 的可控替身：静态资源、动作绑定与插件列表。
type stubUI struct {
	assets map[string][]byte
	// bindings："<plugin>/<binding>" → 解析结果。
	bindings map[string]web.ActionBinding
	// list：PluginLister 的前端插件清单（extensions.json 用）。
	list []web.FrontendPluginInfo
}

func (s *stubUI) List(_ *http.Request) []web.FrontendPluginInfo {
	out := append([]web.FrontendPluginInfo(nil), s.list...)
	for i := range out {
		if out[i].Status == "" {
			out[i].Status = "ready"
		}
		out[i].Enabled = out[i].Status != "disabled" && out[i].Status != "unused"
	}
	return out
}

func (s *stubUI) StaticAsset(pluginID, name string) ([]byte, bool) {
	data, ok := s.assets[pluginID+"/"+name]
	return data, ok
}

func (s *stubUI) ActionBinding(pluginID, binding string) (web.ActionBinding, bool) {
	ab, ok := s.bindings[pluginID+"/"+binding]
	return ab, ok
}

func newServerWithUI(t *testing.T, core *fakeCore, ui web.PluginUI) (*web.Server, *fakeSupervisor) {
	t.Helper()
	sup := &fakeSupervisor{core: core}
	srv, err := web.New(web.Config{
		Supervisor:  sup,
		Workspace:   "/tmp/ws",
		Workspaces:  []string{"/tmp/ws"},
		ProductName: "测试产品",
		Locale:      "zh-CN",
		CSRF:        func(*http.Request) string { return "csrf-token" },
		PluginUI:    ui,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, sup
}

// ---- 动作桥（ActionEnvelope）----------------------------------------------

func TestExtActionBridge(t *testing.T) {
	ui := &stubUI{bindings: map[string]web.ActionBinding{
		"p1/read_plan": {BackendID: "backend-a", Action: "plan", Mode: "read"},
		"p1/apply":     {BackendID: "backend-a", Action: "apply", Mode: "write"},
	}}
	core := newFakeCore().on("action.invoke", `{"ok":true,"result":{"plan":{"total":3}}}`)
	srv, _ := newServerWithUI(t, core, ui)

	// 读动作：GET 返回 JSON，envelope 带上 query 里的 threadId。
	rec := serve(t, srv, "GET", "/ext/p1/action/read_plan?threadId=th_9", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 读动作 = %d\n%s", rec.Code, rec.Body.String())
	}
	wantContains(t, rec.Body.String(), `"ok":true`)
	wantContains(t, rec.Body.String(), `"plan":{"total":3}`)
	if call, ok := core.lastCall("action.invoke"); ok {
		if call.Params["pluginId"] != "backend-a" || call.Params["action"] != "plan" {
			t.Fatalf("envelope 身份不对: %+v", call.Params)
		}
		if call.Params["threadId"] != "th_9" {
			t.Fatalf("envelope 缺 threadId: %+v", call.Params)
		}
	} else {
		t.Fatal("读动作没有发出 action.invoke")
	}

	// 写动作：POST 303 回来源页，input 来自表单（不含 csrf_token）。
	form := url.Values{}
	form.Set("decision", "apply")
	form.Set("csrf_token", "x")
	rec = serve(t, srv, "POST", "/ext/p1/action/apply", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST 写动作 = %d\n%s", rec.Code, rec.Body.String())
	}
	if call, ok := core.lastCall("action.invoke"); ok {
		input, _ := call.Params["input"].(map[string]any)
		if input == nil || input["decision"] != "apply" {
			t.Fatalf("写动作 input 不对: %+v", call.Params)
		}
		if _, leak := input["csrf_token"]; leak {
			t.Fatalf("csrf_token 不应进入 input: %+v", input)
		}
	} else {
		t.Fatal("写动作没有发出 action.invoke")
	}

	// 方法与模式不匹配 → 405；未知绑定 → 404。
	rec = serve(t, srv, "GET", "/ext/p1/action/apply", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 写动作 = %d, want 405", rec.Code)
	}
	rec = serve(t, srv, "POST", "/ext/p1/action/read_plan", url.Values{})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 读动作 = %d, want 405", rec.Code)
	}
	rec = serve(t, srv, "GET", "/ext/p1/action/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知绑定 = %d, want 404", rec.Code)
	}
}

// ---- 静态资源 allowlist ----------------------------------------------------

func TestExtStaticAllowlist(t *testing.T) {
	ui := &stubUI{assets: map[string][]byte{
		"p1/style.css": []byte(".a{color:red}"),
		"p1/app.js":    []byte("void 0;"),
		"p1/app.mjs":   []byte("export {};"),
		"p1/icon.svg":  []byte("<svg/>"),
		"p1/data.json": []byte("{}"),
		"p1/big.css":   make([]byte, 600<<10), // 超过 512KB 上限
		"p1/run.exe":   []byte("MZ"),
		"p1/page.html": []byte("<html>"),
	}}
	srv, _ := newServerWithUI(t, newFakeCore(), ui)

	ok := map[string]string{
		"/ext/p1/static/style.css": "text/css; charset=utf-8",
		"/ext/p1/static/app.js":    "application/javascript; charset=utf-8",
		"/ext/p1/static/app.mjs":   "application/javascript; charset=utf-8",
		"/ext/p1/static/icon.svg":  "image/svg+xml",
		"/ext/p1/static/data.json": "application/json; charset=utf-8",
	}
	for target, ct := range ok {
		rec := serve(t, srv, "GET", target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", target, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != ct {
			t.Fatalf("GET %s Content-Type = %q, want %q", target, got, ct)
		}
	}
	for _, target := range []string{
		"/ext/p1/static/run.exe",
		"/ext/p1/static/page.html",
		"/ext/p1/static/big.css",
		"/ext/p1/static/missing.css", // 静态资源表里没有 → 404
		"/ext/p2/static/style.css",   // 未知插件
		"/ext/p1/static/",            // 空名
	} {
		rec := serve(t, srv, "GET", target, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", target, rec.Code)
		}
	}
	// 非规范路径（..）由 ServeMux 在路由前重定向，不允许 200 直出内容。
	rec := serve(t, srv, "GET", "/ext/p1/static/../secret", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("路径穿越不应返回内容: %d", rec.Code)
	}
}

// ---- 插件列表 → extensions.json -------------------------------------------

// PluginLister 提供的前端插件与 Core plugin.list 的后端插件合并成一份列表，
// CSRF 一并带给客户端（启停表单用）。
func TestExtensionsJSONIncludesFrontendLister(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[{"id":"tools-fs","name":"文件工具","version":"1.2.0","enabled":true,"status":"ready","contributions":{"tools":["read"],"frontend":false,"backend":true}}]}`)
	ui := &stubUI{list: []web.FrontendPluginInfo{
		{ID: "ui-demo", Name: "演示前端", Version: "0.1.0", Status: "ready", HasAssets: true},
	}}
	srv, _ := newServerWithUI(t, core, ui)

	rec := serve(t, srv, "GET", "/partials/extensions.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/extensions.json = %d, want 200", rec.Code)
	}
	var state struct {
		Plugins []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Enabled  bool   `json:"enabled"`
			Backend  bool   `json:"backend"`
			Frontend bool   `json:"frontend"`
		} `json:"plugins"`
		Count int    `json:"count"`
		CSRF  string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("extensions.json 不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if state.Count != 2 || len(state.Plugins) != 2 {
		t.Fatalf("plugins/count = %+v/%d", state.Plugins, state.Count)
	}
	byID := map[string]struct {
		Backend, Frontend bool
	}{}
	for _, p := range state.Plugins {
		byID[p.ID] = struct{ Backend, Frontend bool }{p.Backend, p.Frontend}
	}
	if row, ok := byID["tools-fs"]; !ok || !row.Backend {
		t.Fatalf("后端插件缺失或标记错误: %+v", state.Plugins)
	}
	if row, ok := byID["ui-demo"]; !ok || !row.Frontend {
		t.Fatalf("前端插件缺失或标记错误: %+v", state.Plugins)
	}
	if state.CSRF != "csrf-token" {
		t.Fatalf("csrf = %q, want csrf-token", state.CSRF)
	}
}

// PluginUI 为 nil：/ext 404（动作桥与静态资源都不可用）。
func TestPluginUINilKeepsBaseBehavior(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	rec := serve(t, srv, "GET", "/ext/p1/static/style.css", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无 PluginUI 时静态资源应 404, got %d", rec.Code)
	}
	rec = serve(t, srv, "GET", "/ext/p1/action/read", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无 PluginUI 时动作桥应 404, got %d", rec.Code)
	}
}
