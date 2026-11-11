package frontend_test

import (
	"encoding/json"
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

// TestBlockTodoListVNode 钉住 $C TodoList 的 VNode 形态能通过整树校验
// （HTML 渲染已随 SSR 移除；块以校验后的 VNode JSON 交给客户端）。
func TestBlockTodoListVNode(t *testing.T) {
	raw := json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block","data-adag-renderer":"demo/todo"},"c":[{"t":"$C","n":"TodoList","p":{"notes":"发布前检查","steps":[{"step":"写文档","status":"done"},{"text":"跑测试","status":"in_progress"},{"title":"发布","status":"pending"}]}}]}`)
	var root frontend.Node
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("解码 VNode: %v", err)
	}
	if err := frontend.ValidateNode(root); err != nil {
		t.Fatalf("TodoList block VNode 应通过校验: %v", err)
	}

	// 空清单（即使带 notes）也必须是合法节点：组件自己决定不画面板。
	emptyRaw := json.RawMessage(`{"t":"div","p":{"class":"adag-ui-block","data-adag-renderer":"demo/todo"},"c":[{"t":"$C","n":"TodoList","p":{"notes":"不应出现","steps":[]}}]}`)
	var empty frontend.Node
	if err := json.Unmarshal(emptyRaw, &empty); err != nil {
		t.Fatalf("解码空 TodoList: %v", err)
	}
	if err := frontend.ValidateNode(empty); err != nil {
		t.Fatalf("空 TodoList block VNode 应通过校验: %v", err)
	}
}
