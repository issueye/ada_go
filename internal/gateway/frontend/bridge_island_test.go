package frontend

// P1-4 岛屿标记：hydrate 贡献的根元素要带 data-ada-*，首帧 props 不含 CSRF；
// 非 hydrate 贡献保持原 SSR，不出现标记。

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

func islandBridge(t *testing.T, hydrate bool, src string) *Bridge {
	t.Helper()
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Slots: []plugins.SlotContribution{{
				Slot: "sidebar.top", Template: "slot.tsx", Hydrate: hydrate,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest:     m,
			Bundle:       map[string]string{"slot.tsx": src},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
		CSRF: func(*http.Request) string { return "csrf-secret" },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	return b
}

func TestHydrateSlotInjectsIslandMarkers(t *testing.T) {
	b := islandBridge(t, true, `import { h, defineComponent } from "ada:render";
export default defineComponent({
  render(h) { return <div class="island">hello</div>; }
});
`)
	html, n, err := b.Slot("sidebar.top", httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Slot: %v", err)
	}
	if n != 1 {
		t.Fatalf("贡献数 = %d，期望 1", n)
	}
	text := string(html)
	for _, want := range []string{
		`data-ada-plugin-island="demo:slot:sidebar.top"`,
		`data-ada-plugin="demo"`,
		`data-ada-entry="slot:sidebar.top"`,
		`data-ada-module="/ext/demo/static/client.js?v=`,
		`data-ada-props="`,
		`class="island"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("岛屿 HTML 缺少 %s：%s", want, text)
		}
	}
	if strings.Contains(text, "csrf-secret") {
		t.Fatalf("CSRF 不应进入 data-ada-props：%s", text)
	}
}

func TestLegacySlotHasNoIslandMarkers(t *testing.T) {
	b := islandBridge(t, false, `export default function () {
  return <div class="legacy">hello</div>;
}
`)
	html, _, err := b.Slot("sidebar.top", httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Slot: %v", err)
	}
	if strings.Contains(string(html), "data-ada-plugin-island") {
		t.Fatalf("非 hydrate 贡献不应有岛屿标记：%s", html)
	}
}

func TestHydrateSlotCarriesStoreSnapshots(t *testing.T) {
	b := islandBridge(t, true, `import { h, defineComponent, defineStore } from "ada:render";
const useStore = defineStore("bridge-store", { state() { return { n: 4 }; } });
export default defineComponent({
  setup() { return { store: useStore() }; },
  render(h) { return <div class="island">{this.store.n}</div>; }
});
`)
	htmlText, _, err := b.Slot("sidebar.top", httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Slot: %v", err)
	}
	raw := html.UnescapeString(string(htmlText))
	if !strings.Contains(raw, `"stores":{"bridge-store":{"n":4}}`) {
		t.Fatalf("data-ada-props 未携带 store 快照：%s", raw)
	}
}

func TestDisableIslandsKeepsSSRWithoutMarkers(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Slots: []plugins.SlotContribution{{
				Slot: "sidebar.top", Template: "slot.tsx", Hydrate: true,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{"slot.tsx": `import { h, defineComponent } from "ada:render";
export default defineComponent({ render(h) { return <div class="island">hello</div>; } });
`},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
		DisableIslands: true,
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	if b.HasIslands("chat", httptest.NewRequest(http.MethodGet, "/", nil)) {
		t.Fatal("DisableIslands 时 HasIslands 必须为 false")
	}
	htmlText, _, err := b.Slot("sidebar.top", httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Slot: %v", err)
	}
	text := string(htmlText)
	if !strings.Contains(text, `class="island"`) {
		t.Fatalf("关闭岛屿后仍应输出 SSR 内容：%s", text)
	}
	if strings.Contains(text, "data-ada-plugin-island") || strings.Contains(text, "data-ada-module") {
		t.Fatalf("关闭岛屿后不应有客户端标记：%s", text)
	}
}

func TestHydratePageInjectsIslandMarkers(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Pages: []plugins.PageContribution{{
				Slug: "providers", Template: "page.tsx", Title: "供应商", Hydrate: true,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{"page.tsx": `import { h, defineComponent } from "ada:render";
export default defineComponent({
  render(h) { return <section class="page">providers</section>; }
});
`},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	html, err := b.Page("demo", "providers", httptest.NewRequest(http.MethodGet, "/ext/demo/providers", nil))
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if !strings.Contains(string(html), `data-ada-plugin-island="demo:page:providers"`) {
		t.Fatalf("页面缺少岛屿标记：%s", html)
	}
	if !b.HasIslands("ext:demo", httptest.NewRequest(http.MethodGet, "/ext/demo/providers", nil)) {
		t.Fatal("hydrate 页面应报告 HasIslands")
	}
	if b.HasIslands("ext:demo", httptest.NewRequest(http.MethodGet, "/ext/demo/other", nil)) {
		t.Fatal("非 hydrate 页面不应报告 HasIslands")
	}
}

func TestHasIslandsForChatSlot(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Slots: []plugins.SlotContribution{{
				Slot: "chat.corner", Template: "slot.tsx", Hydrate: true,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{"slot.tsx": `import { h, defineComponent } from "ada:render";
export default defineComponent({ render(h) { return <div class="corner">x</div>; } });
`},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/threads/th_1", nil)
	if !b.HasIslands("chat", req) {
		t.Fatal("chat 页面有 hydrate 的 chat.corner，应报告 HasIslands")
	}
	if b.HasIslands("overview", req) {
		t.Fatal("overview 不渲染 chat.corner，不应报告 HasIslands")
	}
}

// P2-3：多个 hydrate 贡献共享同一插槽时，标记必须落在各自根元素上，
// 不能额外包一层容器（否则插槽的 flex/`> *` 布局会被破坏）。
func TestHydrateSlotKeepsRootsUnwrapped(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Slots: []plugins.SlotContribution{
				{Slot: "sidebar.top", Template: "one.tsx", Hydrate: true, Order: 1},
				{Slot: "sidebar.top", Template: "two.tsx", Hydrate: true, Order: 2},
			},
		},
	}
	src := `import { h, defineComponent } from "ada:render";
export default defineComponent({ render(h) { return <div class="%s">%s</div>; } });
`
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{
				"one.tsx": fmt.Sprintf(src, "one", "1"),
				"two.tsx": fmt.Sprintf(src, "two", "2"),
			},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	html, n, err := b.Slot("sidebar.top", httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Slot: %v", err)
	}
	if n != 2 {
		t.Fatalf("贡献数 = %d，期望 2", n)
	}
	text := string(html)
	// 根元素自己带标记；没有额外的 wrapper 元素。
	if strings.Count(text, "data-ada-plugin-island=") != 2 {
		t.Fatalf("两个根都应带标记：%s", text)
	}
	if strings.Contains(text, `<div data-ada-plugin-island`) {
		t.Fatalf("不应生成包裹层：%s", text)
	}
	if !strings.Contains(text, `class="one"`) || !strings.Contains(text, `class="two"`) {
		t.Fatalf("两个根的 class 应保留：%s", text)
	}
}

// clientOnly 贡献：服务端**不执行模板**，只输出带标记的空壳。这里故意让模板
// 在渲染时抛错——如果桥还在跑模板，测试就会失败；输出里也不能有模板内容。
func TestClientOnlySlotRendersEmptyShell(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Slots: []plugins.SlotContribution{{
				Slot: "composer.footer", Template: "slot.tsx", Hydrate: true, ClientOnly: true,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{"slot.tsx": `import { h, defineComponent } from "ada:render";
export default defineComponent({
  render(h) { throw new Error("clientOnly 贡献不应在服务端渲染"); }
});
`},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
		CSRF: func(*http.Request) string { return "csrf-secret" },
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/threads/th_1", nil)
	htmlText, n, err := b.Slot("composer.footer", req)
	if err != nil {
		t.Fatalf("Slot: %v", err)
	}
	if n != 1 {
		t.Fatalf("贡献数 = %d，期望 1", n)
	}
	text := string(htmlText)
	for _, want := range []string{
		`class="ada-island ada-island--client"`,
		`data-ada-plugin-island="demo:slot:composer.footer"`,
		`data-ada-plugin="demo"`,
		`data-ada-entry="slot:composer.footer"`,
		`data-ada-module="/ext/demo/static/client.js?v=`,
		`data-ada-props="`,
		`data-ada-client-only="1"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("clientOnly 空壳缺少 %s：%s", want, text)
		}
	}
	// 空壳：没有子节点，也没有模板输出（模板 render 抛错也不应影响外壳）。
	if strings.Count(strings.TrimSpace(text), "<div") != 1 {
		t.Fatalf("空壳应只有一个根元素：%s", text)
	}
	if strings.Contains(text, "clientOnly 贡献不应在服务端渲染") {
		t.Fatalf("clientOnly 不应在服务端执行模板：%s", text)
	}
	// 与 hydrate 贡献同一套约束：首帧 props 里不能有 CSRF。
	if strings.Contains(text, "csrf-secret") {
		t.Fatalf("CSRF 不应进入 data-ada-props：%s", text)
	}
}

// clientOnly 贡献同样让页面加载客户端引擎（否则空壳永远没人接管）。
func TestClientOnlySlotReportsHasIslands(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Capabilities: []plugins.Capability{plugins.CapFrontendScript},
			Client:       "client.js",
			Slots: []plugins.SlotContribution{{
				Slot: "composer.footer", Template: "slot.tsx", Hydrate: true, ClientOnly: true,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{"slot.tsx": `export default function () { return null; }
`},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		}},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/threads/th_1", nil)
	if !b.HasIslands("chat", req) {
		t.Fatal("clientOnly 贡献也应让 chat 页面加载客户端引擎")
	}
	if b.HasIslands("overview", req) {
		t.Fatal("overview 不渲染 composer.footer，不应报告 HasIslands")
	}
}

// clientOnly 没有 client bundle 是装配/渲染错误（没有 SSR 可回退，空壳没意义）。
func TestClientOnlySlotWithoutClientIsError(t *testing.T) {
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{
			Slots: []plugins.SlotContribution{{
				Slot: "composer.footer", Template: "slot.tsx", ClientOnly: true,
			}},
		},
	}
	b, err := NewBridge(BridgeOptions{
		Scripts: []host.Script{{
			Manifest: m,
			Bundle: map[string]string{"slot.tsx": `export default function () { return null; }
`},
			StaticAssets: map[string][]byte{},
		}},
	})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}
	htmlText, n, err := b.Slot("composer.footer", httptest.NewRequest(http.MethodGet, "/threads/th_1", nil))
	if err == nil {
		t.Fatalf("没有 client 的 clientOnly 贡献应报错，实际产出：%s", htmlText)
	}
	if n != 0 {
		t.Fatalf("失败贡献不应计数，n=%d", n)
	}
}
