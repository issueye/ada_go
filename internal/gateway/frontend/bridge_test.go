package frontend_test

// 渲染桥的垂直切片测试：Markdown 动态块（数据 Schema → TSX → VNode JSON）。
// 插槽/页面渲染已随 SSR 移除；块返回校验后的 VNode JSON，不再有 HTML。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/plugins/official"
)

func testBridge(t *testing.T) *frontend.Bridge {
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
func renderBlock(b *frontend.Bridge, r *http.Request, rendererID, blockID string, data json.RawMessage) (json.RawMessage, error) {
	return b.RenderBlock(r, rendererID, blockID, 1, data)
}

// TestBridgeRenderBlock 钉 Markdown 动态块的端到端：数据 Schema → TSX → VNode JSON。
func TestBridgeRenderBlock(t *testing.T) {
	b := testBridge(t)
	data := json.RawMessage(`{"scope":"thread","title":"本轮用量","usage":{"inputTokens":1234,"outputTokens":567,"cacheTokens":89,"costMicros":1200}}`)
	raw, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "u1", data)
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if !strings.Contains(string(raw), `"adag-ui-block"`) || !strings.Contains(string(raw), `"data-adag-renderer"`) {
		t.Fatalf("block VNode 缺少包装: %s", raw)
	}
	// 卡片内容以 VNode 数据表达：class 与文本都要能找到。
	for _, want := range []string{"tu-card", `"本轮用量"`, "1,234"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("block VNode 缺少 %q: %s", want, raw)
		}
	}

	// 数据 Schema 失败：调用前拒绝，不执行 TSX。
	if _, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "", json.RawMessage(`{"scope":"bad"}`)); err == nil {
		t.Fatal("非法 data 应被 schema 拒绝")
	}
	// 未知 renderer。
	if _, err := renderBlock(b, request(t, "/thread/th_1"), "no-such/block", "", json.RawMessage(`{}`)); err == nil {
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
	if _, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "", json.RawMessage(`{"scope":"thread"}`)); err == nil {
		t.Fatal("markdownBlocks=false 时应拒绝 block")
	}
}

// TestBridgeRenderBlockComponent 验证 block TSX 产出的 $C 组件节点能通过 block profile 校验。
func TestBridgeRenderBlockComponent(t *testing.T) {
	scripts := []pluginapi.Script{{
		Manifest: &pluginapi.Manifest{
			ID: "demo-block", Name: "Demo Block", Version: "0.1.0", APIVersion: 1,
			Trust: pluginapi.TrustBuiltin,
			Frontend: &pluginapi.Frontend{Blocks: []pluginapi.BlockContribution{{
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
	raw, err := renderBlock(b, request(t, "/thread/th_1"), "demo-block/icon", "", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if !strings.Contains(string(raw), `"t":"$C"`) || !strings.Contains(string(raw), `"n":"Icon"`) {
		t.Fatalf("block 应保留 $C 组件节点: %s", raw)
	}
}

// TestBridgeBlockAudit 钉三种失败审计，且审计不携带 data/渲染器错误原文。
func TestBridgeBlockAudit(t *testing.T) {
	scripts := []pluginapi.Script{{
		Manifest: &pluginapi.Manifest{
			ID: "audit-block", Name: "Audit", Version: "0.1.0", APIVersion: 1,
			Trust: pluginapi.TrustBuiltin,
			Frontend: &pluginapi.Frontend{Blocks: []pluginapi.BlockContribution{
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
		if _, err := renderBlock(b, request(t, "/thread/th_1"), renderer, "b1", json.RawMessage(data)); err == nil {
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

// TestBridgeBlockPropsNoCSRF 钉住块 props 不携带 CSRF token，且 nil request 不 panic。
// props.app 里根本没有 csrf 字段（渲染桥不再注入任何令牌）。
func TestBridgeBlockPropsNoCSRF(t *testing.T) {
	scripts := []pluginapi.Script{{
		Manifest: &pluginapi.Manifest{
			ID: "csrf-block", Name: "CSRF", Version: "0.1.0", APIVersion: 1,
			Trust: pluginapi.TrustBuiltin,
			Frontend: &pluginapi.Frontend{Blocks: []pluginapi.BlockContribution{
				{Name: "echo", Schema: "schemas/any.json", Template: "blocks/echo.tsx"},
			}},
		},
		Bundle: map[string]string{
			"blocks/echo.tsx": `export default function (props) { return <div class="probe">{String(props.app.csrf || "none")}</div>; }`,
		},
		Schemas: map[string][]byte{"schemas/any.json": []byte(`{"type":"object"}`)},
	}}
	b, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: scripts, MarkdownBlocks: true})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	raw, err := renderBlock(b, request(t, "/thread/th_1"), "csrf-block/echo", "b1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	if strings.Contains(string(raw), "secret-csrf") || strings.Contains(string(raw), `"csrf":"`) {
		t.Fatalf("块 props 携带了 CSRF 字段: %s", raw)
	}
	if !strings.Contains(string(raw), `"none"`) {
		t.Fatalf("块应看到空 csrf（渲染为 \"none\" 文本）: %s", raw)
	}
	if _, err := renderBlock(b, nil, "csrf-block/echo", "b2", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("nil request 不应失败: %v", err)
	}
}

// TestBridgeBlockRenderTimeout 钉单块渲染超时：死循环 block 必须被中断，
// 按 render_failed/render_error 记审计，而不是拖住请求。
func TestBridgeBlockRenderTimeout(t *testing.T) {
	scripts := []pluginapi.Script{{
		Manifest: &pluginapi.Manifest{
			ID: "slow-block", Name: "Slow", Version: "0.1.0", APIVersion: 1,
			Trust: pluginapi.TrustBuiltin,
			Frontend: &pluginapi.Frontend{Blocks: []pluginapi.BlockContribution{
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
	if _, err := renderBlock(b, request(t, "/thread/th_1"), "slow-block/loop", "b1", json.RawMessage(`{}`)); err == nil {
		t.Fatal("死循环 block 应超时失败")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("超时未生效，耗时 %v", elapsed)
	}
	if len(events) != 1 || events[0].Kind != "ui.block.render_failed" || events[0].Error != "render_error" {
		t.Fatalf("审计 = %+v", events)
	}
}

// TestBridgeAssetsAndBindings 钉静态资源与动作桥绑定（/ext 契约）。
func TestBridgeAssetsAndBindings(t *testing.T) {
	b := testBridge(t)

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
}

// TestBridgeDisabledPluginBlocksRejected 钉住启停过滤对 block 生效：
// 停用的插件的 block 渲染拒绝并记审计。
func TestBridgeDisabledPluginBlocksRejected(t *testing.T) {
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	var events []frontend.BlockAuditEvent
	b, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:        scripts,
		MarkdownBlocks: true,
		Enabled: func(_ *http.Request, pluginID string) bool {
			return pluginID != "token-usage"
		},
		BlockAudit: func(e frontend.BlockAuditEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if _, err := renderBlock(b, request(t, "/thread/th_1"), "token-usage/usage-card", "b1", json.RawMessage(`{"scope":"thread"}`)); err == nil {
		t.Fatal("停用插件的 block 应拒绝")
	}
	if len(events) != 1 || events[0].Kind != "ui.block.render_failed" || events[0].Error != "plugin_disabled" {
		t.Fatalf("审计 = %+v", events)
	}
}

// TestBridgeRenderBlockEmpty 钉住「模板返回 null ＝ 合法空块」：
// 不报错、不记审计；二次调用命中缓存后语义不变。
func TestBridgeRenderBlockEmpty(t *testing.T) {
	scripts := []pluginapi.Script{{
		Manifest: &pluginapi.Manifest{
			ID: "empty-block", Name: "Empty", Version: "0.1.0", APIVersion: 1,
			Trust: pluginapi.TrustBuiltin,
			Frontend: &pluginapi.Frontend{Blocks: []pluginapi.BlockContribution{
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
		raw, err := renderBlock(b, request(t, "/thread/th_1"), "empty-block/none", "b1", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("空块不应失败: %v", err)
		}
		if strings.TrimSpace(string(raw)) != "null" {
			t.Fatalf("空块结果 = %q，期望 null", raw)
		}
	}
	if len(events) != 0 {
		t.Fatalf("空块不应产生审计: %+v", events)
	}

	// 非 null 但没有根节点：仍是失败，且用独立错误码与 profile 违规区分。
	if _, err := renderBlock(b, request(t, "/thread/th_1"), "empty-block/blank-object", "b2", json.RawMessage(`{}`)); err == nil {
		t.Fatal("空对象 VNode 应失败")
	}
	if len(events) != 1 || events[0].Kind != "ui.block.invalid_vnode" || events[0].Error != "empty_root" {
		t.Fatalf("审计 = %+v", events)
	}
}

// TestBridgeRenderBlockVersion 钉住封套 version 契约：插件声明 version=2 时，
// 封套 version 2 才渲染（props.block.version 同源），1 一律降级并记审计。
func TestBridgeRenderBlockVersion(t *testing.T) {
	scripts := []pluginapi.Script{{
		Manifest: &pluginapi.Manifest{
			ID: "versioned-block", Name: "Versioned", Version: "0.1.0", APIVersion: 1,
			Trust: pluginapi.TrustBuiltin,
			Frontend: &pluginapi.Frontend{Blocks: []pluginapi.BlockContribution{
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
	raw, err := b.RenderBlock(request(t, "/thread/th_1"), "versioned-block/card", "b1", 2, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("version 2 应渲染: %v", err)
	}
	if !strings.Contains(string(raw), `"v":"2"`) {
		t.Fatalf("props.block.version 应为 2: %s", raw)
	}
	if _, err := b.RenderBlock(request(t, "/thread/th_1"), "versioned-block/card", "b2", 1, json.RawMessage(`{}`)); err == nil {
		t.Fatal("version 不一致应降级")
	}
	if len(events) != 1 || events[0].Kind != "ui.block.render_failed" || events[0].Error != "unsupported_version" {
		t.Fatalf("审计 = %+v", events)
	}
}
