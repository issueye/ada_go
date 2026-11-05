package host

// 同构渲染的性能基线（docs/20 P3-4）。不设 CI 阈值，先记录每次
// 引擎加载 + 编译缓存命中 + renderToJSON 的墙钟与分配，供后续优化对比。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const benchIslandSource = `import { h, defineComponent } from "ada:render";
export default defineComponent({
  props: { label: { default: "" } },
  data() { return { n: 1 }; },
  render(h) {
    return (
      <div class="iso">
        <span>{this.label + ":" + this.n}</span>
      </div>
    );
  },
});
`

func benchRenderer(b *testing.B, iso bool) *Renderer {
	b.Helper()
	engine, err := os.ReadFile(filepath.Join("..", "..", "gateway", "web", "static", "ada-render.js"))
	if err != nil {
		b.Fatalf("读取 ada-render.js: %v", err)
	}
	opts := []RendererOption{WithRenderEngineSource(string(engine))}
	if iso {
		ui, rerr := os.ReadFile(filepath.Join("..", "..", "gateway", "web", "static", "ada-ui-iso.js"))
		if rerr != nil {
			b.Fatalf("读取 ada-ui-iso.js: %v", rerr)
		}
		opts = append(opts, WithIsoComponentsSource(string(ui)))
	}
	return NewRenderer(opts...)
}

func BenchmarkRenderIsland(b *testing.B) {
	r := benchRenderer(b, false)
	props := json.RawMessage(`{"label":"a"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.RenderIsland(benchIslandSource, props); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderIslandIsoComponents(b *testing.B) {
	r := benchRenderer(b, true)
	src := `import { h, defineComponent } from "ada:render";
import { Badge, Card, Button } from "ada:ui";
export default defineComponent({
  render(h) {
    return (
      <Card title="卡片">
        <Badge tone="brand" label="3" />
        <Button variant="primary">保存</Button>
      </Card>
    );
  },
});
`
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.RenderIsland(src, nil); err != nil {
			b.Fatal(err)
		}
	}
}
