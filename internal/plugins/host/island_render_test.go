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
