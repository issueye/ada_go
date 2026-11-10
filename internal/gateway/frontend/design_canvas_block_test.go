package frontend_test

// 仓库内示例插件 plugins/design-canvas 的动态块端到端回归：
// Schema 校验 → TSX 编译 → VNode → HTML。
//
// 放在这里是因为 block 渲染桥只装配到 Gateway，单靠 manifest 静态检查
// 无法发现「模板用了 display profile 禁用元素」「TSX 语法错误」这类问题 ——
// 那些在运行期只会静默降级成 fallback，用户在对话里看到的是「动态内容暂不可用」。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/gateway/frontend"
)

func TestDesignCanvasLedgerBlock(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "plugins", "design-canvas")
	tplPath := filepath.Join(dir, "canvas-ledger.tsx")
	schemaPath := filepath.Join(dir, "schemas", "canvas-ledger.json")
	if _, err := os.Stat(tplPath); err != nil {
		t.Skipf("示例插件不存在，跳过: %v", err)
	}
	tpl, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("读模板: %v", err)
	}
	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("读 schema: %v", err)
	}

	scripts := []host.Script{{
		Manifest: &plugins.Manifest{
			ID: "design-canvas", Name: "设计画布台账", Version: "0.1.0", APIVersion: 1,
			Trust: plugins.TrustThirdParty,
			Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{{
				Name:     "canvas-ledger",
				Schema:   "schemas/canvas-ledger.json",
				Template: "canvas-ledger.tsx",
				Profile:  "display",
			}}},
		},
		Bundle:  map[string]string{"canvas-ledger.tsx": string(tpl)},
		Schemas: map[string][]byte{"schemas/canvas-ledger.json": schema},
	}}
	b, err := frontend.NewBridge(frontend.BridgeOptions{Scripts: scripts, MarkdownBlocks: true})
	if err != nil {
		t.Fatalf("NewBridge: %v", err)
	}

	data := json.RawMessage(`{
		"title": "设计画布台账",
		"subtitle": "web-1440 ×2 · iphone-15 ×1",
		"total": 3,
		"canvasUrl": "http://127.0.0.1:8788",
		"groups": [
			{"label": "element", "value": 2},
			{"label": "apple", "value": 1}
		],
		"nodes": [
			{"title": "Element 组件", "system": "element", "artboard": "web-1440", "size": "1440×1009"}
		]
	}`)
	raw, html, err := b.RenderBlock(request(t, "/thread/th_1"), "design-canvas/canvas-ledger", "b1", 1, data)
	if err != nil {
		t.Fatalf("RenderBlock: %v", err)
	}
	out := string(html)
	for _, want := range []string{
		"adag-chart", // 复用宿主令牌类，而不是插件自己的 CSS
		"设计画布台账",
		"adag-chart__bar--l10", // 2/2 最大值 → 第 10 档
		"adag-chart__bar--l5",  // 1/2 → 第 5 档
		"Element 组件",
		"http://127.0.0.1:8788",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("渲染结果缺少 %q:\n%s", want, out)
		}
	}
	// display profile 不允许交互元素
	for _, banned := range []string{"<button", "<form", "<input", "style="} {
		if strings.Contains(out, banned) {
			t.Fatalf("渲染结果出现禁用项 %q: %s", banned, out)
		}
	}
	if !strings.Contains(string(raw), `"adag-ui-block"`) {
		t.Fatalf("block VNode 缺少包装: %s", raw)
	}

	// 多一个键就该被 Schema 拒绝（additionalProperties: false）
	if _, _, err := b.RenderBlock(request(t, "/thread/th_1"), "design-canvas/canvas-ledger", "b2", 1,
		json.RawMessage(`{"title":"x","total":1,"unexpected":true}`)); err == nil {
		t.Fatal("多余字段应被 Schema 拒绝")
	}
	// 缺必填字段同样拒绝
	if _, _, err := b.RenderBlock(request(t, "/thread/th_1"), "design-canvas/canvas-ledger", "b3", 1,
		json.RawMessage(`{"title":"缺 total"}`)); err == nil {
		t.Fatal("缺少 required 字段应被拒绝")
	}
}
