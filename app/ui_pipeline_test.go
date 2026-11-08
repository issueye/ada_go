package app_test

// M4 前端插件管线的组合根验收（垂直切片）：
//
//	官方前端插件（磁盘 plugins/official）→ frontend.Bridge（TSX→VNode→HTML）→ web.Server（12 插槽 / /ext）
//
// 本文件是测试，允许出现官方插件 id（1.5.5 的边界只约束非测试源码）。

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/plugins/official"
)

// uiFakeCore 只实现 SSR 取数需要的面：thread.get/entries 走固定 fixture，
// action.invoke 可配置成败。
type uiFakeCore struct {
	mu       sync.Mutex
	invokeOK bool
	result   json.RawMessage
	calls    int
}

func (f *uiFakeCore) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "action.invoke":
		f.calls++
		if !f.invokeOK {
			return nil, errors.New("core: action.invoke 尚未实现（M4 桩）")
		}
		return json.RawMessage(`{"ok":true,"result":` + string(f.result) + `}`), nil
	case "thread.get":
		return json.RawMessage(`{"thread":{"id":"th_1","workspace":"/tmp/ws","title":"UI 管线","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},"items":[],"seq":1,"run":{"runId":"run_1","status":"terminal","terminal":"completed"},"interactions":[],"hasMore":false}`), nil
	case "thread.entries":
		return json.RawMessage(`{"entries":[],"hasMore":false}`), nil
	default:
		return nil, errors.New("uiFakeCore: 未配置 " + method)
	}
}

func (f *uiFakeCore) Subscribe(int) (<-chan contracts.Event, func()) { return nil, func() {} }
func (f *uiFakeCore) Close() error                                   { return nil }

func (f *uiFakeCore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type uiFakeSup struct{ core contracts.CoreClient }

func (s *uiFakeSup) Acquire(context.Context, string) (contracts.CoreClient, error) {
	return s.core, nil
}
func (s *uiFakeSup) Release(string)                    {}
func (s *uiFakeSup) Shutdown(context.Context) error    { return nil }
func (s *uiFakeSup) List() []contracts.WorkspaceStatus { return nil }

func newUIServer(t *testing.T, core *uiFakeCore) *web.Server {
	t.Helper()
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     scripts,
		Supervisor:  &uiFakeSup{core: core},
		ProductName: "ada-go",
	})
	if err != nil {
		t.Fatalf("frontend.NewBridge: %v", err)
	}
	srv, err := web.New(web.Config{
		Supervisor:  &uiFakeSup{core: core},
		Workspace:   "/tmp/ws",
		Workspaces:  []string{"/tmp/ws"},
		ProductName: "ada-go",
		Locale:      "zh-CN",
		CSRF:        func(*http.Request) string { return "csrf-token" },
		PluginUI:    bridge,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func uiGet(t *testing.T, srv *web.Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// 计划卡片（tools-plan 的前端段）挂在 chat.corner：动作成功时用真实计划渲染。
func TestTaskPlanCardInChatCorner(t *testing.T) {
	core := &uiFakeCore{invokeOK: true, result: json.RawMessage(
		`{"plan":{"done":4,"total":6,"steps":[{"step":" golden ","status":"done"},{"step":"待办","status":"pending"}]}}`)}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/thread/th_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-slot="chat.corner" data-contributors="1"`,
		`<details class="todo-card"`,
		`<span class="todo-card__count">4/6</span>`,
		`class="ada-todo__step is-done"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("会话页缺少 %q", want)
		}
	}
	// 文本由 Go 侧转义：动作结果里的空格保留、HTML 特殊字符不可能逃逸。
	if !strings.Contains(body, "<span> golden </span>") {
		t.Fatalf("步骤标题渲染不对:\n%s", body)
	}
	// 动作预取走了既有 action.invoke（协议合规，未新增 RPC 方法）。
	if core.count() == 0 {
		t.Fatal("计划卡渲染应尝试 action.invoke 预取")
	}
}

// 动作预取失败时插槽贡献为空：没有示例数据兜底，整页仍 200。
func TestTaskPlanCardHiddenWithoutAction(t *testing.T) {
	core := &uiFakeCore{}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/thread/th_1?stub=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "todo-card") {
		t.Fatal("取不到计划时不应画卡（不允许示例数据兜底）")
	}
	if core.count() == 0 {
		t.Fatal("计划卡渲染应尝试 action.invoke 预取")
	}
}

// 供应商管理入口由 provider-admin 的 composer.left 插槽提供：
// 基座模板不再写死插件 id 与页面路由；禁用插件时入口随插槽一起消失。
func TestProviderAdminSlotInComposer(t *testing.T) {
	core := &uiFakeCore{}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/thread/th_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-slot="composer.left" data-contributors="1"`,
		`href="/ext/provider-admin/providers"`,
		`管理供应商与模型`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("composer.left 插槽缺少 %q", want)
		}
	}
}

// 输入框辅助区整块由 composer-telemetry 的纯客户端岛接管：服务端只出空壳，
// 页面里不该出现任何指标文案（没有 SSR 回退）。
func TestComposerTelemetryIsClientOnlyShell(t *testing.T) {
	core := &uiFakeCore{}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/thread/th_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-slot="composer.footer" data-contributors="1"`,
		`data-ada-plugin-island="composer-telemetry:slot:composer.footer"`,
		`data-ada-entry="slot:composer.footer"`,
		`data-ada-module="/ext/composer-telemetry/static/client.js?v=`,
		`data-ada-client-only="1"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("composer.footer 岛屿缺少 %q", want)
		}
	}
	// 空壳：没有 SSR 内容；指标由岛自己轮询 /threads/{id}/state 后渲染。
	for _, absent := range []string{"缓存命中", "ada-token-ring", "ada-context", "工作区："} {
		if strings.Contains(body, absent) {
			t.Fatalf("基座页面不应输出辅助区内容 %q（clientOnly 没有 SSR 回退）", absent)
		}
	}
	// 首帧 props 不含 CSRF，且带上会话引用。属性值会被 html/template 转义，
	// extractIslandProps 已经还原。
	props := extractIslandProps(body)
	if strings.Contains(props, "csrf") {
		t.Fatalf("首帧 props 不应包含 CSRF：%s", props)
	}
	if !strings.Contains(props, `"thread":{"id":"th_1"}`) {
		t.Fatalf("首帧 props 应带会话引用：%s", props)
	}
	// 工作区显示名由桥算好（web.workspaceName 同一规则）；这个夹具没接
	// WorkspaceFor，所以两者都为空。
	if strings.Contains(props, `"workspaceName"`) {
		t.Fatalf("夹具没有工作区，props 不该出现 workspaceName：%s", props)
	}
	// bundle 与样式都能取到（岛要能在浏览器挂载）。
	if rec := uiGet(t, srv, "/ext/composer-telemetry/static/client.js"); rec.Code != http.StatusOK {
		t.Fatalf("client.js = %d", rec.Code)
	}
	if rec := uiGet(t, srv, "/ext/composer-telemetry/static/telemetry.css"); rec.Code != http.StatusOK {
		t.Fatalf("telemetry.css = %d", rec.Code)
	}
	// 插件的 styles 必须被页面引用（`.ada-island--client { display: contents }`
	// 就靠它，否则空壳会多占一层布局）。
	if !strings.Contains(body, "/ext/composer-telemetry/static/telemetry.css") {
		t.Fatal("会话页应引用插件的 telemetry.css")
	}
	// 页面必须加载客户端岛屿引擎，否则空壳永远没人接管。
	if !strings.Contains(body, "plugin-islands.js") {
		t.Fatal("会话页应加载 plugin-islands.js")
	}
}

// extractIslandProps 取页面上 clientOnly 空壳的 data-ada-props 原文（转义前），
// 失败信息里给出可读的片段而不是整页 HTML。属性按 map 键排序输出，
// data-ada-client-only 可能排在 data-ada-props 之前——从标记之后向前找。
func extractIslandProps(body string) string {
	const marker = `data-ada-client-only="1"`
	index := strings.Index(body, marker)
	if index < 0 {
		return "（找不到 clientOnly 空壳）"
	}
	start := strings.Index(body[index:], `data-ada-props="`)
	if start < 0 {
		return "（空壳上没有 data-ada-props）"
	}
	start += index + len(`data-ada-props="`)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return "（data-ada-props 未闭合）"
	}
	return html.UnescapeString(body[start : start+end])
}

// /ext 页面：hello 页 200，未知页面 404，静态资源 allowlist。
func TestUIExtRoutes(t *testing.T) {
	core := &uiFakeCore{invokeOK: true, result: json.RawMessage(`{
		"providers": [{
			"id": "p_ui",
			"displayName": "UI 供应商",
			"protocol": "openai-chat",
			"baseUrl": "https://ui.example/v1",
			"hasApiKey": true,
			"apiKeyHint": "1234",
			"models": [{ "id": "m_ui", "displayName": "UI 模型", "supportsTools": true }]
		}],
		"current": { "providerId": "p_ui", "modelId": "m_ui" }
	}`)}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/ext/ui-hello-tsx/hello")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/ui-hello-tsx/hello = %d\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"<h2>Hello from TSX</h2>",
		"<li>产品：ada-go</li>",
		`data-ext-plugin="ui-hello-tsx"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("hello 页缺少 %q", want)
		}
	}

	if rec := uiGet(t, srv, "/ext/ui-hello-tsx/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("未知 slug = %d, want 404", rec.Code)
	}
	// P1-7 最小试点：同一份 TSX SSR + hydrate；页面带岛屿标记与引导脚本。
	rec = uiGet(t, srv, "/ext/ui-hello-tsx/counter")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/ui-hello-tsx/counter = %d\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		`data-ada-plugin-island="ui-hello-tsx:page:counter"`,
		`data-ada-entry="page:counter"`,
		`data-ada-module="/ext/ui-hello-tsx/static/client.js?v=`,
		`data-ada-props="`,
		`<strong>0</strong>`,
		`plugin-islands.js`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("counter 岛缺少 %q\n%s", want, rec.Body.String())
		}
	}
	rec = uiGet(t, srv, "/ext/ui-hello-tsx/static/client.js")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/javascript; charset=utf-8" {
		t.Fatalf("client bundle = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	// sidebar.top 是插槽级岛屿：hello 页虽然没有页面 hydrate，也应出现插槽标记。
	rec = uiGet(t, srv, "/ext/ui-hello-tsx/hello")
	for _, want := range []string{
		`data-ada-entry="slot:sidebar.top"`,
		`class="ext-hello-slot"`,
		`badge-brand`,
		`plugin-islands.js`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("sidebar 插槽岛缺少 %q\n%s", want, rec.Body.String())
		}
	}

	// P2-4 首个真实业务页面：Token 用量（只读投影 + 视图切换 / 用量构成）。
	rec = uiGet(t, srv, "/ext/token-usage/usage")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/token-usage/usage = %d\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		`data-ada-entry="page:usage"`,
		`data-ada-module="/ext/token-usage/static/client.js?v=`,
		`data-token-page`,
		`tu-title`,
		`tu-kpi`,
		`用量构成`,
		`plugin-islands.js`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("token-usage 岛缺少 %q\n%s", want, rec.Body.String())
		}
	}
	rec = uiGet(t, srv, "/ext/token-usage/static/client.js")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/javascript; charset=utf-8" {
		t.Fatalf("token-usage client bundle = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// P2-4b-2b：providers 页已替换为组件化实现，legacy provider-admin.js 不再加载。
	rec = uiGet(t, srv, "/ext/provider-admin/providers")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/provider-admin/providers = %d\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		`data-ada-entry="page:providers"`,
		`data-ada-module="/ext/provider-admin/static/client.js?v=`,
		`data-provider-admin-next="true"`,
		`模型供应商`,
		`UI 供应商`,
		`plugin-islands.js`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("provider-admin 岛缺少 %q\n%s", want, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "/ext/provider-admin/static/provider-admin.js") {
		t.Fatal("provider-admin.js 不应再被加载")
	}
	if strings.Contains(rec.Body.String(), `data-provider-admin="true"`) {
		t.Fatal("legacy provider-admin 骨架不应再渲染")
	}
	if rec := uiGet(t, srv, "/ext/provider-admin/providers-next"); rec.Code != http.StatusNotFound {
		t.Fatalf("providers-next 过渡页应已移除，got %d", rec.Code)
	}
	if rec := uiGet(t, srv, "/ext/no-such-plugin/hello"); rec.Code != http.StatusNotFound {
		t.Fatalf("未知插件 = %d, want 404", rec.Code)
	}
	rec = uiGet(t, srv, "/ext/tools-plan/static/todo.css")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("插件样式 = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := uiGet(t, srv, "/ext/tools-plan/static/evil.exe"); rec.Code != http.StatusNotFound {
		t.Fatalf("扩展名 allowlist 之外的资源 = %d, want 404", rec.Code)
	}
}
