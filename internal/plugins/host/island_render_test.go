package host

// P1-3 同构渲染模式：TSX 通过 ada:render 使用 ada-render.js，在 moejs 里
// renderToJSON 成 VNode；事件函数不进入线格式。与 P0 探针的区别是走正式
// Renderer API（编译缓存、预算、模块链接）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func islandEngineSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "gateway", "web", "static", "ada-render.js")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	return string(raw)
}

const islandSource = `import { h, defineComponent } from "ada:render";

export default defineComponent({
  props: { label: { default: "" } },
  data() { return { n: 1 }; },
  render(h) {
    return (
      <div class="iso">
        <span>{this.label + ":" + this.n}</span>
        <button type="button" onClick={() => { this.n += 1; }}>add</button>
      </div>
    );
  },
});
`

func TestRenderIsland(t *testing.T) {
	r := NewRenderer(WithRenderEngineSource(islandEngineSource(t)))
	raw, err := r.RenderIsland(islandSource, json.RawMessage(`{"label":"a"}`))
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"class":"iso"`, `"v":"a:1"`, `"type":"button"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("VNode 缺少 %s：%s", want, text)
		}
	}
	if strings.Contains(text, "onClick") || strings.Contains(text, "function") {
		t.Fatalf("事件函数进入线格式：%s", text)
	}
}

func TestRenderIslandRequiresEngineSource(t *testing.T) {
	r := NewRenderer()
	if _, err := r.RenderIsland(islandSource, nil); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("未配置引擎源时应 fail-closed，err = %v", err)
	}
}

const isoComponentsSource = `import { h, defineComponent } from "ada:render";
import { Badge, Card, Button } from "ada:ui";

export default defineComponent({
  render(h) {
    return (
      <Card title="同构卡片" subtitle="P2-1">
        <Badge tone="brand" label="3/5" />
        <Button variant="primary">保存</Button>
      </Card>
    );
  },
});
`

func TestRenderIslandWithIsoComponents(t *testing.T) {
	iso, err := os.ReadFile(filepath.Join("..", "..", "gateway", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := NewRenderer(
		WithRenderEngineSource(islandEngineSource(t)),
		WithIsoComponentsSource(string(iso)),
	)
	raw, err := r.RenderIsland(isoComponentsSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"t":"section"`, `"class":"card"`, `"badge`, `"t":"button"`, `ada-btn--primary`} {
		if !strings.Contains(text, want) {
			t.Fatalf("同构组件 VNode 缺少 %s：%s", want, text)
		}
	}
	if strings.Contains(text, `"$C"`) {
		t.Fatalf("同构组件应展开为原生标签，不应残留 $C：%s", text)
	}
}

const islandStateSource = `import { h, defineComponent, defineStore, provide, inject } from "ada:render";

const useStore = defineStore("demo", {
  state() { return { n: 2 }; },
  getters: { double(state) { return state.n * 2; } },
  actions: { inc() { this.n += 1; } },
});

const Child = defineComponent({
  setup() {
    return { theme: inject("theme", "default") };
  },
  render(h) {
    return <span class="theme">{this.theme}</span>;
  },
});

export default defineComponent({
  setup() {
    provide("theme", "dark");
    return { store: useStore() };
  },
  render(h) {
    return (
      <div class="state">
        <span class="count">{this.store.n}</span>
        <span class="double">{this.store.double}</span>
        <Child />
      </div>
    );
  },
});
`

func TestRenderIslandStateAndProvide(t *testing.T) {
	r := NewRenderer(WithRenderEngineSource(islandEngineSource(t)))
	raw, err := r.RenderIsland(islandStateSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"v":"2"`, `"v":"4"`, `"v":"dark"`, `"class":"state"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR 状态/注入缺少 %s：%s", want, text)
		}
	}
}

const islandStoreSnapshotSource = `import { h, defineComponent, defineStore } from "ada:render";

const useStore = defineStore("island-store", {
  state() { return { n: 3 }; },
});

export default defineComponent({
  setup() {
    return { store: useStore() };
  },
  render(h) {
    return <div class="store">{this.store.n}</div>;
  },
});
`

func TestRenderIslandWithStoreSnapshots(t *testing.T) {
	r := NewRenderer(WithRenderEngineSource(islandEngineSource(t)))
	vnode, stores, err := r.RenderIslandWithStores(islandStoreSnapshotSource, nil)
	if err != nil {
		t.Fatalf("RenderIslandWithStores: %v", err)
	}
	if !strings.Contains(string(vnode), `"v":"3"`) {
		t.Fatalf("VNode 缺少 store 状态: %s", vnode)
	}
	if !strings.Contains(string(stores), `"island-store"`) || !strings.Contains(string(stores), `"n":3`) {
		t.Fatalf("store 快照缺少状态: %s", stores)
	}
}
