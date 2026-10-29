package frontend_test

// 渲染桥的垂直切片测试：真实官方前端插件（embedded）→ Bridge → Slot/Page。
//
// 本文件是测试，允许出现官方插件 id（设计 1.5.5 的边界只约束非测试源码）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// fakeSup / fakeCore 是最小 Supervisor/CoreClient 替身（与 web 包的测试同形）。
type fakeCore struct {
	mu     sync.Mutex
	resps  map[string]json.RawMessage
	failed map[string]error
	calls  []struct {
		Method string
		Params map[string]any
	}
}

func newFakeCore() *fakeCore {
	return &fakeCore{resps: map[string]json.RawMessage{}, failed: map[string]error{}}
}

func (f *fakeCore) on(method, raw string) *fakeCore {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resps[method] = json.RawMessage(raw)
	return f
}

func (f *fakeCore) failOn(method string, err error) *fakeCore {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[method] = err
	return f
}

func (f *fakeCore) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, struct {
		Method string
		Params map[string]any
	}{Method: method, Params: m})
	if err, ok := f.failed[method]; ok {
		return nil, err
	}
	raw, ok := f.resps[method]
	if !ok {
		return nil, errors.New("fake: 未配置 " + method)
	}
	return raw, nil
}

func (f *fakeCore) Subscribe(int) (<-chan contracts.Event, func()) { return nil, func() {} }
func (f *fakeCore) Close() error                                   { return nil }

func (f *fakeCore) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

func (f *fakeCore) lastParams(method string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i].Params, true
		}
	}
	return nil, false
}

type fakeSup struct {
	core contracts.CoreClient
}

func (s *fakeSup) Acquire(ctx context.Context, workspace string) (contracts.CoreClient, error) {
	return s.core, nil
}
func (s *fakeSup) Release(string)                 {}
func (s *fakeSup) Shutdown(context.Context) error { return nil }
func (s *fakeSup) List() []contracts.WorkspaceStatus {
	return nil
}

func testBridge(t *testing.T, core contracts.CoreClient) *frontend.Bridge {
	t.Helper()
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	if len(scripts) < 2 {
		t.Fatalf("官方前端插件应有 2 个，got %d", len(scripts))
	}
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     scripts,
		Supervisor:  &fakeSup{core: core},
		ProductName: "ada-go",
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	return b
}

func request(t *testing.T, target string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	return r
}

// 动作预取失败（Core 尚未实现 action.invoke 的注册）时，插槽渲染成功：
// 模板回退到内置示例数据（M4→tools-plan 接线点，见 slot.tsx 注释）。
func TestBridgeTaskPlanSlotWithFallbackSample(t *testing.T) {
	core := newFakeCore().failOn("action.invoke", errors.New("core: action.invoke 尚未实现"))
	b := testBridge(t, core)

	html, count, err := b.Slot("thread.tray", request(t, "/thread/th_1"))
	if err != nil {
		t.Fatalf("Slot(thread.tray): %v", err)
	}
	if count != 1 || html == "" {
		t.Fatalf("插槽贡献数 = %d，内容为空: %q", count, html)
	}
	for _, want := range []string{
		`<details class="tp-card" data-state="collapsed" id="task-plan-card">`,
		`class="tp-card__summary" title="点击展开任务规划步骤"`,
		`<span class="tp-card__count">2/3</span>`,
		`<div class="ada-todo__list-wrap"><p class="ada-todo__notes"><span>备注：</span>`,
		`<li class="ada-todo__step is-done">`,
		`<li class="ada-todo__step is-pending">`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("任务卡缺少 %s\n--- html ---\n%s", want, html)
		}
	}
	// 动作预取确实发生了（协议合规：既有 action.invoke）。
	if got := core.count("action.invoke"); got != 1 {
		t.Fatalf("action.invoke 次数 = %d, want 1", got)
	}
	if params, ok := core.lastParams("action.invoke"); ok {
		if params["pluginId"] != "tools-plan" || params["action"] != "plan" {
			t.Fatalf("预取 envelope 不对: %+v", params)
		}
		if params["threadId"] != "th_1" {
			t.Fatalf("预取 envelope 缺 threadId: %+v", params)
		}
	}
}

// 动作预取成功：真实动作结果取代内置示例。
func TestBridgeTaskPlanSlotWithActionResult(t *testing.T) {
	core := newFakeCore().on("action.invoke",
		`{"ok":true,"result":{"plan":{"done":1,"total":2,"steps":[{"step":"真数据","status":"done"},{"step":"待办","status":"pending"}]}}}`)
	b := testBridge(t, core)

	html, count, err := b.Slot("thread.tray", request(t, "/thread/th_1?x=1"))
	if err != nil || count != 1 {
		t.Fatalf("Slot: %v (%d)", err, count)
	}
	wantContains := func(h, want string) {
		t.Helper()
		if !strings.Contains(h, want) {
			t.Fatalf("任务卡缺少 %q\n%s", want, h)
		}
	}
	wantContains(string(html), `<span class="tp-card__count">1/2</span>`)
	wantContains(string(html), "<span>真数据</span>")
	wantNotContains := func(h, unwanted string) {
		t.Helper()
		if strings.Contains(h, unwanted) {
			t.Fatalf("任务卡不应包含 %q（示例数据必须被真实动作结果取代）\n%s", unwanted, h)
		}
	}
	wantNotContains(string(html), "示例数据")
}

// 页面路由：hello 页面 200；未知插件/页面是 ErrExtNotFound（web 层给 404）。
func TestBridgeHelloPage(t *testing.T) {
	b := testBridge(t, newFakeCore())

	html, err := b.Page("ui-hello-tsx", "hello", request(t, "/ext/ui-hello-tsx/hello"))
	if err != nil {
		t.Fatalf("Page(hello): %v", err)
	}
	for _, want := range []string{
		`<section class="ext-hello">`,
		"<h2>Hello from TSX</h2>",
		"<li>产品：ada-go</li>",
		"<li>插件：ui-hello-tsx（Hello TSX）版本 0.1.0</li>",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("hello 页缺少 %q\n%s", want, html)
		}
	}
	if _, err := b.Page("ui-hello-tsx", "nope", request(t, "/ext/ui-hello-tsx/nope")); !errors.Is(err, web.ErrExtNotFound) {
		t.Fatalf("未知页面应返回 ErrExtNotFound, got %v", err)
	}
	if _, err := b.Page("other-plugin", "hello", request(t, "/ext/other-plugin/hello")); !errors.Is(err, web.ErrExtNotFound) {
		t.Fatalf("未知插件应返回 ErrExtNotFound, got %v", err)
	}
	if got := b.PageTitle("ui-hello-tsx", "hello"); got != "Hello TSX" {
		t.Fatalf("PageTitle = %q", got)
	}
}

// 失败隔离：一个插件的模板抛错，同插槽其它贡献与其它插槽不受影响。
func TestBridgeSlotFailureIsolation(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	broken := host.Script{Manifest: mustManifest(t, `{
		"id": "test-broken",
		"name": "坏插件",
		"version": "0.1.0",
		"apiVersion": 1,
		"trust": "builtin",
		"frontend": {
			"slots": [{ "slot": "thread.tray", "template": "broken.tsx", "order": 1 }]
		}
	}`), Bundle: map[string]string{"broken.tsx": "export default function () { throw new Error('插槽炸了'); }"}}
	b, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: append(scripts, broken), ProductName: "ada-go"})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	html, count, err := b.Slot("thread.tray", request(t, "/thread/th_1"))
	if err != nil {
		t.Fatalf("单贡献失败不应拖垮插槽: %v", err)
	}
	if count != 1 || !strings.Contains(string(html), "task-plan-card") {
		t.Fatalf("好插件的贡献应照常渲染 (count=%d): %s", count, html)
	}
	// 全部贡献都失败时才报错给 web（回退基座占位）。
	html2, count2, err2 := b.Slot("sidebar.top", request(t, "/"))
	if err2 != nil || count2 != 0 || html2 != "" {
		t.Fatalf("无贡献插槽应为空: (%q, %d, %v)", html2, count2, err2)
	}
}

func mustManifest(t *testing.T, raw string) *plugins.Manifest {
	t.Helper()
	m, err := plugins.LoadManifest([]byte(raw))
	if err != nil {
		t.Fatalf("清单非法: %v", err)
	}
	return m
}

// 资产与绑定解析。
func TestBridgeAssetsAndBindings(t *testing.T) {
	b := testBridge(t, newFakeCore())

	styles := b.Styles()
	found := false
	for _, s := range styles {
		if strings.HasPrefix(s, "/ext/ui-task-plan/static/task-plan.css?v=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Styles 缺少任务卡样式（带内容指纹）: %v", styles)
	}
	scripts := b.Scripts()
	if len(scripts) != 1 || !strings.HasPrefix(scripts[0], "/ext/provider-admin/static/provider-admin.js?v=") {
		t.Fatalf("Scripts 应只含 provider-admin 的增强脚本（带内容指纹），got %v", scripts)
	}
	if data, ok := b.StaticAsset("ui-task-plan", "task-plan.css"); !ok || len(data) == 0 {
		t.Fatal("StaticAsset 应能取到 task-plan.css")
	}
	if _, ok := b.StaticAsset("ui-task-plan", "../plugin.json"); ok {
		t.Fatal("路径穿越必须拒绝")
	}
	if _, ok := b.StaticAsset("nope", "x"); ok {
		t.Fatal("未知插件必须拒绝")
	}
	ab, ok := b.ActionBinding("ui-task-plan", "slot_thread_tray")
	if !ok || ab.BackendID != "tools-plan" || ab.Action != "plan" || ab.Mode != "read" {
		t.Fatalf("ActionBinding 解析不对: %+v ok=%v", ab, ok)
	}
	if _, ok := b.ActionBinding("ui-task-plan", "missing"); ok {
		t.Fatal("未知绑定必须拒绝")
	}
	// 绑定名约定：非 [a-z0-9] 字符编码为下划线。
	if got := frontend.SlotBindingName("thread.tray"); got != "slot_thread_tray" {
		t.Fatalf("SlotBindingName = %q", got)
	}
	if got := frontend.PageBindingName("hello-world"); got != "page_hello_world" {
		t.Fatalf("PageBindingName = %q", got)
	}
}

// bridgeWithOnly 只装载一个前端插件（隔离同槽的其它贡献，断言不受干扰）。
func bridgeWithOnly(t *testing.T, core *fakeCore, id, csrf string) *frontend.Bridge {
	t.Helper()
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	var picked []host.Script
	for _, s := range scripts {
		if s.Manifest != nil && s.Manifest.ID == id {
			picked = append(picked, s)
		}
	}
	if len(picked) == 0 {
		t.Fatalf("没有前端插件 %q", id)
	}
	var csrfFn func(*http.Request) string
	if csrf != "" {
		csrfFn = func(*http.Request) string { return csrf }
	}
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     picked,
		Supervisor:  &fakeSup{core: core},
		ProductName: "ada-go",
		CSRF:        csrfFn,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	return b
}

// ui-ask-user：待答交互来自 thread.get 的只读投影，表单提交回基座交互路由。
func TestBridgeAskUserSlotUsesInteractionProjection(t *testing.T) {
	core := newFakeCore().
		on("thread.get", `{"thread":{"id":"th_1"},"interactions":[{"id":"ir_1","threadId":"th_1","kind":"select","title":"选择环境","body":"请选择部署环境","options":["staging","prod"],"status":"pending"}]}`).
		on("provider.list", `{"providers":[]}`).
		on("usage.get", `{"thread":{},"workspace":{}}`)
	b := bridgeWithOnly(t, core, "ui-ask-user", "csrf-x")

	html, count, err := b.Slot("thread.tray", request(t, "/threads/th_1"))
	if err != nil || count != 1 {
		t.Fatalf("Slot(thread.tray) = %d, %v", count, err)
	}
	for _, want := range []string{
		`class="aq-card"`,
		"选择环境",
		"请选择部署环境",
		`name="csrf_token"`,
		`value="csrf-x"`,
		`action="/threads/th_1/interaction/ir_1"`,
		`value="staging"`,
		`value="prod"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("提问卡缺少 %q\n%s", want, html)
		}
	}
	if params, ok := core.lastParams("thread.get"); !ok || params["threadId"] != "th_1" {
		t.Fatalf("thread.get 参数不对: %+v", params)
	}
}

// provider-admin：页面是 Master-Detail 骨架，数据由插件 route（provider-admin.js）加载。
func TestBridgeProviderAdminPageRendersSkeleton(t *testing.T) {
	core := newFakeCore()
	b := bridgeWithOnly(t, core, "provider-admin", "csrf-y")

	html, err := b.Page("provider-admin", "providers", request(t, "/ext/provider-admin/providers"))
	if err != nil {
		t.Fatalf("Page(provider-admin): %v", err)
	}
	for _, want := range []string{
		`data-provider-admin`, "模型设置", "管理自定义模型供应商", "添加供应商",
		`data-pm-refresh`, `data-pm-list`, `data-pm-detail`, `data-pm-status`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("供应商页缺少 %q\n%s", want, html)
		}
	}
}

// token-usage：dashboard 贡献走同一条插槽渲染路径，数据来自 usage.get。
func TestBridgeTokenUsageDashboardProjection(t *testing.T) {
	core := newFakeCore().
		on("provider.list", `{"providers":[]}`).
		on("usage.get", `{"thread":{"provider":"openai","model":"gpt-4o","inputTokens":1200,"outputTokens":300,"cacheTokens":400,"costMicros":1500},"workspace":{"inputTokens":5000,"outputTokens":800,"cacheTokens":2000,"costMicros":9000}}`)
	b := bridgeWithOnly(t, core, "token-usage", "")

	html, count, err := b.Slot("dashboard.widgets", request(t, "/overview"))
	if err != nil || count != 1 {
		t.Fatalf("Slot(dashboard.widgets) = %d, %v", count, err)
	}
	for _, want := range []string{
		"Token 用量", "5,800", "1,200", "$0.0090",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("Token 用量卡片缺少 %q\n%s", want, html)
		}
	}
}
