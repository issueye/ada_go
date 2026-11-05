package frontend

// 12 插槽全部挂 hydrate 插件的 SSR 基线（docs/20 P3-3）。
// 只测 Bridge.Slot 的渲染成本，不含真实 HTTP/浏览器 hydration。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

var benchSlots = []string{
	"sidebar.top", "sidebar.bottom", "topbar.right", "chat.corner",
	"composer.left", "composer.right", "composer.footer", "composer.above",
	"thread.tray", "transcript.after", "extensions.list", "dashboard.widgets",
}

func BenchmarkRenderAllSlots(b *testing.B) {
	const src = `import { h, defineComponent } from "ada:render";
export default defineComponent({
  render(h) { return <div class="slot-bench">ok</div>; },
});
`
	scripts := make([]host.Script, 0, len(benchSlots))
	for i, slot := range benchSlots {
		id := "bench-" + string(rune('a'+i))
		m := &plugins.Manifest{
			ID: id, Name: id, Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
			Trust: plugins.TrustBuiltin,
			Frontend: &plugins.Frontend{
				Capabilities: []plugins.Capability{plugins.CapFrontendScript},
				Client:       "client.js",
				Slots: []plugins.SlotContribution{{
					Slot: slot, Template: "slot.tsx", Hydrate: true,
				}},
			},
		}
		scripts = append(scripts, host.Script{
			Manifest:     m,
			Bundle:       map[string]string{"slot.tsx": src},
			StaticAssets: map[string][]byte{"client.js": []byte("export default {}")},
		})
	}
	bridge, err := NewBridge(BridgeOptions{Scripts: scripts})
	if err != nil {
		b.Fatalf("NewBridge: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, slot := range benchSlots {
			if _, _, err := bridge.Slot(slot, req); err != nil {
				b.Fatal(err)
			}
		}
	}
}
