package web_test

// M4 web 接线测试：PluginUI 填充 12 插槽（失败隔离 / 贡献者计数）、
// /ext 页面与静态资源路由、动作桥的 ActionEnvelope。

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/web"
)

// stubUI 是 PluginUI 的可控替身：按插槽名给内容或报错。
type stubUI struct {
	// slots：插槽名 → (html, count)；err 按插槽名报错。
	slots map[string]struct {
		body  template.HTML
		count int
	}
	errs map[string]error
	// page：slug → html；pageErr：slug → error。
	page    map[string]template.HTML
	pageErr map[string]error
	styles  []string
	scripts []string
	assets  map[string][]byte
	// bindings："<plugin>/<binding>" → 解析结果。
	bindings map[string]web.ActionBinding
}

func (s *stubUI) Slot(name string, r *http.Request) (template.HTML, int, error) {
	if err, ok := s.errs[name]; ok {
		return "", 0, err
	}
	v, ok := s.slots[name]
	if !ok {
		return "", 0, nil
	}
	return v.body, v.count, nil
}

func (s *stubUI) Page(pluginID, slug string, r *http.Request) (template.HTML, error) {
	if err, ok := s.pageErr[slug]; ok {
		return "", err
	}
	if body, ok := s.page[slug]; ok {
		return body, nil
	}
	return "", web.ErrExtNotFound
}

func (s *stubUI) Styles() []string  { return s.styles }
func (s *stubUI) Scripts() []string { return s.scripts }

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

// ---- 插槽填充与失败隔离 ----------------------------------------------------

func TestPluginSlotsFillAndCount(t *testing.T) {
	ui := &stubUI{slots: map[string]struct {
		body  template.HTML
		count int
	}{
		"chat.corner": {"<b>角落卡</b>", 1},
		"thread.tray": {"<i>托盘卡</i>", 2},
	}}
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesDescending)
	srv, _ := newServerWithUI(t, core, ui)

	rec := serve(t, srv, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d, want 200", rec.Code)
	}
	wantContains(t, rec.Body.String(), `data-slot="chat.corner" data-contributors="1"`)
	wantContains(t, rec.Body.String(), `data-slot="thread.tray" data-contributors="2"`)
	wantContains(t, rec.Body.String(), "角落卡")
	// 未贡献的插槽保持 0。
	wantContains(t, rec.Body.String(), `data-slot="composer.left" data-contributors="0"`)
}

func TestPluginSlotFailureIsolated(t *testing.T) {
	ui := &stubUI{
		slots: map[string]struct {
			body  template.HTML
			count int
		}{
			"chat.corner": {"<b>正常卡</b>", 1},
		},
		errs: map[string]error{"thread.tray": errors.New("boom")},
	}
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesDescending)
	srv, _ := newServerWithUI(t, core, ui)

	rec := serve(t, srv, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("单槽抛错时整页仍应为 200，got %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// 失败槽回退基座占位（空容器），其余槽与页面主体照常。
	wantContains(t, body, `data-slot="thread.tray" data-contributors="0"`)
	wantContains(t, body, `data-slot="chat.corner" data-contributors="1"`)
	wantContains(t, body, "正常卡")
	wantContains(t, body, "这是问题") // transcript 照常
	wantNotContains(t, body, "boom")
}

// ---- /ext 页面路由 ---------------------------------------------------------

func TestExtPageRoutes(t *testing.T) {
	ui := &stubUI{
		page:    map[string]template.HTML{"hello": "<p>插件页面</p>"},
		pageErr: map[string]error{"boom": errors.New("渲染失败")},
	}
	srv, _ := newServerWithUI(t, newFakeCore(), ui)

	rec := serve(t, srv, "GET", "/ext/p1/hello", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/p1/hello = %d, want 200", rec.Code)
	}
	wantContains(t, rec.Body.String(), "插件页面")

	rec = serve(t, srv, "GET", "/ext/p1/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知 slug = %d, want 404", rec.Code)
	}
	rec = serve(t, srv, "GET", "/ext/p1/boom", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("渲染失败 = %d, want 502", rec.Code)
	}
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

// ---- 插件样式与脚本注入 head ------------------------------------------------

func TestPluginStylesAndScriptsInHead(t *testing.T) {
	ui := &stubUI{styles: []string{"/ext/p1/static/style.css"}, scripts: []string{"/ext/p1/static/app.js"}}
	srv, _ := newServerWithUI(t, newFakeCore(), ui)

	rec := serve(t, srv, "GET", "/", nil)
	wantContains(t, rec.Body.String(), `<link rel="stylesheet" href="/ext/p1/static/style.css">`)
	wantContains(t, rec.Body.String(), `<script src="/ext/p1/static/app.js" defer></script>`)
}

// PluginUI 为 nil：插槽保持 0 贡献，/ext 404（现状不回归）。
func TestPluginUINilKeepsBaseBehavior(t *testing.T) {
	core := newFakeCore().on("thread.list", `{"threads":[],"nextCursor":"","hasMore":false}`)
	srv, _ := newServer(t, core)
	rec := serve(t, srv, "GET", "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `data-slot="sidebar.top" data-contributors="0"`) {
		t.Fatalf("无 PluginUI 时首页插槽应保持空占位: %d", rec.Code)
	}
	rec = serve(t, srv, "GET", "/ext/p1/hello", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无 PluginUI 时 /ext 应 404, got %d", rec.Code)
	}
	rec = serve(t, srv, "GET", "/ext/p1/static/style.css", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("无 PluginUI 时静态资源应 404, got %d", rec.Code)
	}
}
