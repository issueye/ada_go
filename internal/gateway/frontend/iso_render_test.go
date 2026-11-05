package frontend

// P2-1 服务端同构组件：ada-ui-iso.js 的 render(h) 定义必须展开成原生标签，
// 并通过 Go 的 VNode 白名单/转义出口，而不是残留 $C 组件名。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins/host"
)

const isoCardSource = `import { h, defineComponent } from "ada:render";
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

func TestIsoComponentsRenderThroughGo(t *testing.T) {
	engine, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-render.js"))
	if err != nil {
		t.Fatalf("读取 ada-render.js: %v", err)
	}
	iso, err := os.ReadFile(filepath.Join("..", "web", "static", "ada-ui-iso.js"))
	if err != nil {
		t.Fatalf("读取 ada-ui-iso.js: %v", err)
	}
	r := host.NewRenderer(
		host.WithRenderEngineSource(string(engine)),
		host.WithIsoComponentsSource(string(iso)),
	)
	vnode, err := r.RenderIsland(isoCardSource, nil)
	if err != nil {
		t.Fatalf("RenderIsland: %v", err)
	}
	html, err := RenderHTML(vnode)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	text := string(html)
	for _, want := range []string{
		`<section class="card">`,
		`<span class="badge badge-brand">3/5</span>`,
		`<button class="ada-btn ada-btn--primary ada-btn--md" type="button">`,
		`<span class="ada-btn__label">保存</span>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("SSR HTML 缺少 %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "$C") {
		t.Fatalf("SSR HTML 不应包含 $C：%s", text)
	}
}
