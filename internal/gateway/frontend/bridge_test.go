package frontend_test

// 渲染桥的垂直切片测试：真实官方前端插件（embedded）→ Bridge → Slot/Page。
//
// 本文件是测试，允许出现官方插件 id（设计 1.5.5 的边界只约束非测试源码）。

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
		Scripts:        scripts,
		Supervisor:     &fakeSup{core: core},
		ProductName:    "ada-go",
		MarkdownBlocks: true,
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

// renderBlock 是测试里的默认版本（1）封装；版本相关的用例直接调 RenderBlock。
func renderBlock(b *frontend.Bridge, r *http.Request, rendererID, blockID string, data json.RawMessage) (json.RawMessage, template.HTML, error) {
	return b.RenderBlock(r, rendererID, blockID, 1, data)
}

// TestBridgeRenderBlock 钉 Markdown 动态块的端到端：数据 Schema → TSX → VNode → HTML。
func TestBridgeRenderBlock(t *testing.T) {
	b := testBridge(t, newFakeCore())
	data := json.RawMessage(`{"scope":"thread","title":"本轮用量","usage":{"inputTokens":1234,"outputTokens":567,"cacheTokens":89,"costMicros":1200}}`)
	raw, html, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "u1", data)
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if !strings.Contains(string(raw), `"adag-ui-block"`) || !strings.Contains(string(raw), `"data-adag-renderer"`) {
		t.Fatalf("block VNode 缺少包装: %s", raw)
	}
	if !strings.Contains(string(html), "tu-card") || !strings.Contains(string(html), "1,234") {
		t.Fatalf("block HTML = %s", html)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "block", "usage-card.html"))
	if err != nil {
		t.Fatalf("读取 golden: %v", err)
	}
	if strings.TrimSpace(string(html)) != strings.TrimSpace(string(golden)) {
		t.Fatalf("block HTML 与 golden 不一致:\n--- got ---\n%s\n--- want ---\n%s", html, golden)
	}

	// 数据 Schema 失败：调用前拒绝，不执行 TSX。
	if _, _, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "", json.RawMessage(`{"scope":"bad"}`)); err == nil {
		t.Fatal("非法 data 应被 schema 拒绝")
	}
	// 未知 renderer。
	if _, _, err := renderBlock(b, request(t, "/thread/th_1"), "no-such/block", "", json.RawMessage(`{}`)); err == nil {
		t.Fatal("未知 renderer 应报错")
	}
}

// 产品 capabilities.markdownBlocks=false 时 Block 渲染入口关闭。
func TestBridgeMarkdownBlocksDisabled(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	b, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: scripts, MarkdownBlocks: false})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if _, _, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "", json.RawMessage(`{"scope":"thread"}`)); err == nil {
		t.Fatal("markdownBlocks=false 时应拒绝 block")
	}
}

// TestBridgeRenderBlockComponent 验证 block TSX 产出的 $C 组件节点能通过校验与 HTML 渲染。
func TestBridgeRenderBlockComponent(t *testing.T) {
	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "demo-block", Name: "Demo Block", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{{
				Name: "icon", Schema: "schemas/icon.json", Template: "blocks/icon.tsx", Profile: "display",
			}}},
		},
		Bundle: map[string]string{
			"blocks/icon.tsx": `import { Icon } from "ssr:components";
export default function Block() { return <Icon name="i-list" />; }`,
		},
		Schemas: map[string][]byte{"schemas/icon.json": []byte(`{"type":"object"}`)},
	}}
	b, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: scripts, MarkdownBlocks: true})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	raw, html, err := renderBlock(b, request(t, "/thread/th_1"), "demo-block/icon", "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if !strings.Contains(string(raw), `"t":"$C"`) || !strings.Contains(string(raw), `"n":"Icon"`) {
		t.Fatalf("block 应保留 $C 组件节点: %s", raw)
	}
	if !strings.Contains(string(html), "ada-icon") {
		t.Fatalf("$C Icon 应渲染为 SVG: %s", html)
	}
}

// TestBridgeBlockAudit 钉三种失败审计，且审计不携带 data/渲染器错误原文。
func TestBridgeBlockAudit(t *testing.T) {
	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "audit-block", Name: "Audit", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{
				{Name: "bad-data", Schema: "schemas/required.json", Template: "blocks/ok.tsx"},
				{Name: "bad-render", Schema: "schemas/any.json", Template: "blocks/throw.tsx"},
				{Name: "bad-vnode", Schema: "schemas/any.json", Template: "blocks/form.tsx"},
				{Name: "interactive", Schema: "schemas/any.json", Template: "blocks/ok.tsx", Profile: "interactive"},
			}},
		},
		Bundle: map[string]string{
			"blocks/ok.tsx":    `export default function () { return <div>ok</div>; }`,
			"blocks/throw.tsx": `export default function () { throw new Error("secret-data-123"); }`,
			"blocks/form.tsx":  `export default function () { return <form></form>; }`,
		},
		Schemas: map[string][]byte{
			"schemas/required.json": []byte(`{"type":"object","required":["x"],"additionalProperties":false}`),
			"schemas/any.json":      []byte(`{"type":"object"}`),
		},
	}}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	run := func(renderer string, data string) {
		t.Helper()
		if _, _, err := renderBlock(b, request(t, "/thread/th_1"), renderer, "b1", json.RawMessage(data)); err == nil {
			t.Fatalf("%s 应失败", renderer)
		}
	}
	run("audit-block/bad-data", `{}`)
	run("audit-block/bad-render", `{}`)
	run("audit-block/bad-vnode", `{}`)
	run("missing/block", `{}`)
	run("audit-block/interactive", `{}`)

	want := []struct{ kind, plugin, code string }{
		{"ui.block.invalid_data", "audit-block", "schema_validation"},
		{"ui.block.render_failed", "audit-block", "render_error"},
		{"ui.block.invalid_vnode", "audit-block", "profile_violation"},
		{"ui.block.render_failed", "missing", "renderer_missing"},
		{"ui.block.render_failed", "audit-block", "unsupported_profile"},
	}
	if len(events) != len(want) {
		t.Fatalf("审计事件数 = %d，期望 %d: %+v", len(events), len(want), events)
	}
	for i, w := range want {
		e := events[i]
		if e.Kind != w.kind || e.PluginID != w.plugin || e.Error != w.code {
			t.Fatalf("事件[%d] = %+v，期望 %+v", i, e, w)
		}
		if e.BlockID != "b1" {
			t.Fatalf("事件[%d] block id = %q，期望 b1", i, e.BlockID)
		}
		if strings.Contains(e.Error, "secret-data-123") || strings.Contains(e.Error, "{") {
			t.Fatalf("审计泄露 data/错误原文: %+v", e)
		}
	}
}

// TestBridgeBlockPropsNoCSRF 钉住块 props 不带 CSRF token，且 nil request 不 panic。
func TestBridgeBlockPropsNoCSRF(t *testing.T) {
	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "csrf-block", Name: "CSRF", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{
				{Name: "echo", Schema: "schemas/any.json", Template: "blocks/echo.tsx"},
			}},
		},
		Bundle: map[string]string{
			"blocks/echo.tsx": `export default function (props) { return <div class="csrf">{String(props.app.csrf || "none")}</div>; }`,
		},
		Schemas: map[string][]byte{"schemas/any.json": []byte(`{"type":"object"}`)},
	}}
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		CSRF: func(*http.Request) string { return "secret-csrf" },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	_, html, err := renderBlock(b, request(t, "/thread/th_1"), "csrf-block/echo", "b1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if strings.Contains(string(html), "secret-csrf") {
		t.Fatalf("块 props 泄露 CSRF token: %s", html)
	}
	if !strings.Contains(string(html), "none") {
		t.Fatalf("块应看到空 csrf: %s", html)
	}
	if _, _, err := renderBlock(b, nil, "csrf-block/echo", "b2", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("nil request 不应失败: %v", err)
	}
}

// TestBridgeBlockRenderTimeout 钉单块渲染超时：死循环 block 必须被中断，
// 按 render_failed/render_error 记审计，而不是拖住请求。
func TestBridgeBlockRenderTimeout(t *testing.T) {
	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "slow-block", Name: "Slow", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{
				{Name: "loop", Schema: "schemas/any.json", Template: "blocks/loop.tsx"},
			}},
		},
		Bundle: map[string]string{
			"blocks/loop.tsx": `export default function () { while (true) {} }`,
		},
		Schemas: map[string][]byte{"schemas/any.json": []byte(`{"type":"object"}`)},
	}}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	start := time.Now()
	if _, _, err := renderBlock(b, request(t, "/thread/th_1"), "slow-block/loop", "b1", json.RawMessage(`{}`)); err == nil {
		t.Fatal("死循环 block 应超时失败")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}
	if len(events) != 1 || events[0].Kind != "ui.block.render_failed" || events[0].Error != "render_error" {
		t.Fatalf("超时审计 = %+v", events)
	}
}

// 动作预取失败（Core 尚未实现 action.invoke 的注册）时插槽贡献为空：
// 计划卡片和工具在同一个插件里，取不到计划就不画卡（没有示例数据兜底）。
func TestBridgeTaskPlanSlotWithoutAction(t *testing.T) {
	core := newFakeCore().failOn("action.invoke", errors.New("core: action.invoke 尚未实现"))
	b := testBridge(t, core)

	html, _, err := b.Slot("chat.corner", request(t, "/thread/th_1"))
	if err != nil {
		t.Fatalf("Slot(chat.corner): %v", err)
	}
	if html != "" {
		t.Fatalf("预取失败时不应画卡: %q", html)
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

// 停用的前端插件不渲染、也不触发它的动作预取（Enabled 回调按工作区过滤）。
func TestBridgeSkipsDisabledPlugin(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	core := newFakeCore()
	var asked []string
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     scripts,
		Supervisor:  &fakeSup{core: core},
		ProductName: "ada-go",
		Enabled: func(_ *http.Request, id string) bool {
			asked = append(asked, id)
			return id != "tools-plan"
		},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	html, count, err := b.Slot("chat.corner", request(t, "/thread/th_1"))
	if err != nil {
		t.Fatalf("Slot(chat.corner): %v", err)
	}
	if html != "" || count != 0 {
		t.Fatalf("停用插件的贡献仍被渲染: %q (%d)", html, count)
	}
	if len(asked) == 0 {
		t.Fatal("Enabled 回调没有被调用")
	}
	if got := core.count("action.invoke"); got != 0 {
		t.Fatalf("停用插件仍触发了动作预取: %d 次", got)
	}
}

// 会话级插槽在首页（没有 threadId）不渲染、也不预取动作；
// 否则 Todo 卡片的 plan 动作会带着空 threadId 失败，把插件标成降级。
func TestBridgeThreadSlotSkipsHomePage(t *testing.T) {
	core := newFakeCore().on("action.invoke", `{"ok":true,"result":{"plan":{"done":0,"total":0,"steps":[]}}}`)
	b := testBridge(t, core)

	html, count, err := b.Slot("chat.corner", request(t, "/"))
	if err != nil {
		t.Fatalf("Slot(chat.corner) on /: %v", err)
	}
	if html != "" || count != 0 {
		t.Fatalf("首页不应渲染会话级插槽: %q (%d)", html, count)
	}
	if got := core.count("action.invoke"); got != 0 {
		t.Fatalf("首页不应预取插槽动作: %d 次", got)
	}

	// 有 threadId 的会话页仍然预取（ActionResult 为空计划时模板返回 null）。
	if _, _, err := b.Slot("chat.corner", request(t, "/threads/th_1")); err != nil {
		t.Fatalf("Slot(chat.corner) on thread: %v", err)
	}
	if got := core.count("action.invoke"); got != 1 {
		t.Fatalf("会话页应预取一次动作，got %d", got)
	}
}

// 动作预取成功：计划卡用真实动作结果渲染。
func TestBridgeTaskPlanSlotWithActionResult(t *testing.T) {
	core := newFakeCore().on("action.invoke",
		`{"ok":true,"result":{"plan":{"done":1,"total":2,"steps":[{"step":"真数据","status":"done"},{"step":"待办","status":"pending"}]}}}`)
	b := testBridge(t, core)

	html, count, err := b.Slot("chat.corner", request(t, "/thread/th_1?x=1"))
	if err != nil || count != 1 {
		t.Fatalf("Slot: %v (%d)", err, count)
	}
	wantContains := func(h, want string) {
		t.Helper()
		if !strings.Contains(h, want) {
			t.Fatalf("计划卡缺少 %q\n%s", want, h)
		}
	}
	wantContains(string(html), `<details class="todo-card"`)
	wantContains(string(html), `<span class="todo-card__count">1/2</span>`)
	wantContains(string(html), "<span>真数据</span>")
	wantContains(string(html), `<li class="ada-todo__step is-done">`)
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
			"slots": [{ "slot": "chat.corner", "template": "broken.tsx", "order": 1 }]
		}
	}`), Bundle: map[string]string{"broken.tsx": "export default function () { throw new Error('插槽炸了'); }"}}
	core := newFakeCore().on("action.invoke",
		`{"ok":true,"result":{"plan":{"done":1,"total":2,"steps":[{"step":"步骤","status":"pending"}]}}}`)
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: append(scripts, broken), Supervisor: &fakeSup{core: core}, ProductName: "ada-go",
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	html, count, err := b.Slot("chat.corner", request(t, "/thread/th_1"))
	if err != nil {
		t.Fatalf("单贡献失败不应拖垮插槽: %v", err)
	}
	if count != 1 || !strings.Contains(string(html), `id="todo-card"`) {
		t.Fatalf("好插件的贡献应照常渲染 (count=%d): %s", count, html)
	}
	// 全部贡献都失败时才报错给 web（回退基座占位）。
	// 用 topbar.right 做空插槽：sidebar.top 现在由 ui-hello-tsx 的 P2-3 试点占用。
	html2, count2, err2 := b.Slot("topbar.right", request(t, "/"))
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
		if strings.HasPrefix(s, "/ext/tools-plan/static/todo.css?v=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Styles 缺少计划卡样式（带内容指纹）: %v", styles)
	}
	scripts := b.Scripts()
	// provider-admin / env-manager / mcp-client 都已改成岛屿（client.js），
	// 不再注册 legacy 脚本，因此这里只剩一个仍在用 legacy 运行时的插件。
	wantScripts := []string{
		"/ext/cron-manager/static/cron-manager.js?v=",
	}
	if len(scripts) != len(wantScripts) {
		t.Fatalf("Scripts 应含 %v，got %v", wantScripts, scripts)
	}
	for i, prefix := range wantScripts {
		if !strings.HasPrefix(scripts[i], prefix) {
			t.Fatalf("Scripts[%d] 应以 %q 开头（带内容指纹），got %v", i, prefix, scripts)
		}
	}
	if data, ok := b.StaticAsset("tools-plan", "todo.css"); !ok || len(data) == 0 {
		t.Fatal("StaticAsset 应能取到 todo.css")
	}
	if _, ok := b.StaticAsset("tools-plan", "../plugin.json"); ok {
		t.Fatal("路径穿越必须拒绝")
	}
	if _, ok := b.StaticAsset("nope", "x"); ok {
		t.Fatal("未知插件必须拒绝")
	}
	ab, ok := b.ActionBinding("tools-plan", "slot_chat_corner")
	if !ok || ab.BackendID != "tools-plan" || ab.Action != "plan" || ab.Mode != "read" {
		t.Fatalf("ActionBinding 解析不对: %+v ok=%v", ab, ok)
	}
	if _, ok := b.ActionBinding("tools-plan", "missing"); ok {
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

// 提问（tools-ask 的前端段）：待答交互来自 thread.get 的只读投影，表单提交回基座交互路由。
func TestBridgeAskUserSlotUsesInteractionProjection(t *testing.T) {
	core := newFakeCore().
		on("thread.get", `{"thread":{"id":"th_1"},"interactions":[{"id":"ir_1","threadId":"th_1","kind":"select","title":"选择环境","body":"请选择部署环境","options":["staging","prod"],"status":"pending"}]}`).
		on("provider.list", `{"providers":[]}`).
		on("usage.get", `{"thread":{},"workspace":{}}`)
	b := bridgeWithOnly(t, core, "tools-ask", "csrf-x")

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

// provider-admin：页面是 Master-Detail 骨架，数据由插件 route（/state）加载。
func TestBridgeProviderAdminPageRendersSkeleton(t *testing.T) {
	core := newFakeCore()
	b := bridgeWithOnly(t, core, "provider-admin", "csrf-y")

	html, err := b.Page("provider-admin", "providers", request(t, "/ext/provider-admin/providers"))
	if err != nil {
		t.Fatalf("Page(provider-admin): %v", err)
	}
	// SSR 只保证骨架与首屏文案：详情内容依赖 /state 预取，不在断言范围。
	for _, want := range []string{
		`data-provider-admin-next`, "模型供应商", "添加供应商",
		`pm-layout`, `pm-list`, `pm-detail`,
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("供应商页缺少 %q\n%s", want, html)
		}
	}
}

// provider-admin 详情分支：有供应商数据时，详情头、字段表、危险区都要能 SSR 出来
// （SSR 的 VNode 白名单不含 svg / 未登记组件，这里顺带守住那条边界）。
func TestBridgeProviderAdminDetailRenders(t *testing.T) {
	catalog := `{
		"providers": [{
			"id": "unisound",
			"displayName": "unisound",
			"protocol": "openai-chat",
			"baseUrl": "https://maas-api.unisound.com/v1",
			"proxy": "",
			"headers": {"X-Api-Version": "2024-01-01"},
			"disabled": false,
			"hasApiKey": true,
			"apiKeyHint": "594b",
			"models": [
				{"id": "unisound-chat", "contextWindow": 128000, "supportsTools": true,
				 "thinkingLevels": ["low", "medium", "high"], "input": ["text"]},
				{"id": "unisound-embed", "supportsTools": false}
			]
		}],
		"current": {"providerId": "unisound", "modelId": "unisound-chat"}
	}`
	core := newFakeCore().on("action.invoke", `{"ok":true,"result":`+catalog+`}`)
	b := bridgeWithOnly(t, core, "provider-admin", "csrf-y")

	html, err := b.Page("provider-admin", "providers", request(t, "/ext/provider-admin/providers"))
	if err != nil {
		t.Fatalf("Page(provider-admin): %v", err)
	}
	for _, want := range []string{
		"unisound", "已启用", "密钥已保存",
		"供应商 ID", "端点", "危险操作", "供应商信息", "模型列表",
		"2 个模型", "查看全部",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("供应商详情缺少 %q\n%s", want, html)
		}
	}
}

// mcp-client 页面 SSR 预取：动作结果按绑定名 page_servers 落到 props.data.backend，
// 首屏就要能看到两张 server 卡、状态徽章与工具表，而不是空占位。
func TestBridgeMCPClientPageRenders(t *testing.T) {
	state := `{
		"configPath": ".adago/mcp.json",
		"userConfigPath": "~/.adago/mcp.json",
		"servers": [
			{"id":"computer_use","transport":"stdio","url":"E:/mcp/computer_use.exe mcp",
			 "source":".adago/mcp.json","enabled":false,"status":"disabled","tools":0,"items":[],
			 "toolPrefix":"","error":""},
			{"id":"browser_use","transport":"stdio","url":"E:/mcp/browser-use.exe mcp",
			 "source":".adago/mcp.json","enabled":true,"status":"ready","tools":2,
			 "toolPrefix":"browser_",
			 "items":[
				{"name":"browser_open","mcpName":"browser_open","access":"read","schemaSimplified":false},
				{"name":"browser_act","mcpName":"browser_act","access":"write","schemaSimplified":true}
			 ]}
		]
	}`
	core := newFakeCore().on("action.invoke", `{"ok":true,"result":`+state+`}`)
	b := bridgeWithOnly(t, core, "mcp-client", "csrf-y")

	html, err := b.Page("mcp-client", "servers", request(t, "/ext/mcp-client/servers"))
	if err != nil {
		t.Fatalf("Page(mcp-client): %v", err)
	}
	for _, want := range []string{
		"computer_use", "browser_use", "已停用", "已连接",
		"共 2 个 server", "已连接 1", "已停用 1",
		"工具", "browser_open", "只读", "写入", "Schema 已简化",
		"data-ada-entry=\"page:servers\"",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("MCP 页面缺少 %q\n%s", want, html)
		}
	}
}

// token-usage：dashboard 贡献走同一条插槽渲染路径，数据来自 usage.get。
func TestBridgeTokenUsageDashboardProjection(t *testing.T) {
	core := newFakeCore().
		on("provider.list", `{"providers":[]}`).
		on("action.invoke", `{"ok":true,"result":{"workspace":{"inputTokens":5000,"outputTokens":800,"cacheTokens":2000,"costMicros":9000},"thread":null}}`)
	b := bridgeWithOnly(t, core, "token-usage", "")

	html, count, err := b.Slot("dashboard.widgets", request(t, "/overview"))
	if err != nil || count != 1 {
		t.Fatalf("Slot(dashboard.widgets) = %d, %v", count, err)
	}
	for _, want := range []string{
		"Token 用量", "5,800", "$0.0090",
	} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("Token 用量卡片缺少 %q\n%s", want, html)
		}
	}
}

// TestBridgeRenderBlockEmpty 钉住「模板返回 null ＝ 合法空块」：
// 不报错、不记审计、HTML 为空；二次调用命中缓存后语义不变。
func TestBridgeRenderBlockEmpty(t *testing.T) {
	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "empty-block", Name: "Empty", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{
				{Name: "none", Schema: "schemas/any.json", Template: "blocks/none.tsx"},
				{Name: "blank-object", Schema: "schemas/any.json", Template: "blocks/blank.tsx"},
			}},
		},
		Bundle: map[string]string{
			"blocks/none.tsx":  `export default function () { return null; }`,
			"blocks/blank.tsx": `export default function () { return {}; }`,
		},
		Schemas: map[string][]byte{"schemas/any.json": []byte(`{"type":"object"}`)},
	}}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	for i := 0; i < 2; i++ { // 第二次走缓存
		raw, html, err := renderBlock(b, request(t, "/thread/th_1"), "empty-block/none", "b1", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("空块不应失败: %v", err)
		}
		if strings.TrimSpace(string(raw)) != "null" || html != "" {
			t.Fatalf("空块结果 = %q / %q，期望 null / 空", raw, html)
		}
	}
	if len(events) != 0 {
		t.Fatalf("空块不应产生审计: %+v", events)
	}

	// 非 null 但没有根节点：仍是失败，且用独立错误码与 profile 违规区分。
	if _, _, err := renderBlock(b, request(t, "/thread/th_1"), "empty-block/blank-object", "b2", json.RawMessage(`{}`)); err == nil {
		t.Fatal("空对象 VNode 应失败")
	}
	if len(events) != 1 || events[0].Kind != "ui.block.invalid_vnode" || events[0].Error != "empty_root" {
		t.Fatalf("审计 = %+v", events)
	}
}

// TestBridgeRenderBlockVersion 钉住封套 version 契约：插件声明 version=2 时，
// 封套 version 2 才渲染（props.block.version 同源），1 一律降级并记审计。
func TestBridgeRenderBlockVersion(t *testing.T) {
	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "versioned-block", Name: "Versioned", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{
				{Name: "card", Schema: "schemas/any.json", Template: "blocks/echo.tsx", Version: 2},
			}},
		},
		Bundle: map[string]string{
			"blocks/echo.tsx": `export default function (props) { return <div class="v">{String(props.block.version)}</div>; }`,
		},
		Schemas: map[string][]byte{"schemas/any.json": []byte(`{"type":"object"}`)},
	}}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts: scripts, MarkdownBlocks: true,
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	_, html, err := b.RenderBlock(request(t, "/thread/th_1"), "versioned-block/card", "b1", 2, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("version 2 应渲染: %v", err)
	}
	if !strings.Contains(string(html), ">2<") {
		t.Fatalf("props.block.version 应为 2: %s", html)
	}
	if _, _, err := b.RenderBlock(request(t, "/thread/th_1"), "versioned-block/card", "b2", 1, json.RawMessage(`{}`)); err == nil {
		t.Fatal("version 不一致应降级")
	}
	if len(events) != 1 || events[0].Kind != "ui.block.render_failed" || events[0].Error != "unsupported_version" {
		t.Fatalf("审计 = %+v", events)
	}
}
