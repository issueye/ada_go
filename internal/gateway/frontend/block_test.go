package frontend_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
)

func TestValidateBlockNodeProfile(t *testing.T) {
	allowed := []frontend.Node{
		{T: "div", P: map[string]any{"class": "x"}, C: []frontend.Node{{T: "#text", V: "ok"}}},
		{T: "$C", N: "Icon", P: map[string]any{"name": "i-list"}},
		{T: "$C", N: "TodoList", P: map[string]any{"steps": []any{}}},
	}
	for _, n := range allowed {
		if err := frontend.ValidateBlockNode(n); err != nil {
			t.Fatalf("合法 block 节点被拒绝: %+v / %v", n, err)
		}
	}
	denied := []frontend.Node{
		{T: "form"},
		{T: "button", C: []frontend.Node{{T: "#text", V: "x"}}},
		{T: "input"},
		{T: "$C", N: "Composer"},
		{T: "$C", N: "InteractionTray"},
		{T: "$C", N: "Slot"},
	}
	for _, n := range denied {
		if err := frontend.ValidateBlockNode(n); err == nil {
			t.Fatalf("block profile 应拒绝 %+v", n)
		}
	}
}

// TestBlockTodoListGolden 钉住 $C TodoList 的 Go 渲染结果。
// 浏览器一致性用例（web/testdata/ada-block-components.test.js）读同一份 golden，
// 两边任何一侧改动 DOM 都会让另一侧失败。
func TestBlockTodoListGolden(t *testing.T) {
	raw := json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block","data-adag-renderer":"demo/todo"},"c":[{"t":"$C","n":"TodoList","p":{"notes":"发布前检查","steps":[{"step":"写文档","status":"done"},{"text":"跑测试","status":"in_progress"},{"title":"发布","status":"pending"}]}}]}`)
	html, err := frontend.RenderHTML(raw)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "block", "todo-list.html"))
	if err != nil {
		t.Fatalf("读取 golden: %v", err)
	}
	if strings.TrimSpace(string(html)) != strings.TrimSpace(string(golden)) {
		t.Fatalf("TodoList block HTML 与 golden 不一致:\n--- got ---\n%s\n--- want ---\n%s", html, golden)
	}

	// 空清单（即使带 notes）Go 侧完全不输出内容（不画面板）。
	// 浏览器端组件必须返回单根节点，会留一个无样式的空容器，这是唯一有意的 DOM 差异。
	emptyRaw := json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block","data-adag-renderer":"demo/todo"},"c":[{"t":"$C","n":"TodoList","p":{"notes":"不应出现","steps":[]}}]}`)
	emptyHTML, err := frontend.RenderHTML(emptyRaw)
	if err != nil {
		t.Fatalf("RenderHTML(empty): %v", err)
	}
	want := `<div class="adag-ui-block" data-adag-renderer="demo/todo"></div>`
	if string(emptyHTML) != want {
		t.Fatalf("空 TodoList = %q，期望 %q", emptyHTML, want)
	}
}
