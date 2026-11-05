package frontend

// P1-4 岛屿标记：hydrate 贡献的根元素要带 data-ada-*，首帧 props 不含 CSRF；
// 非 hydrate 贡献保持原 SSR，不出现标记。

import (
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
}
