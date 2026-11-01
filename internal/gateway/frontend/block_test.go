package frontend_test

import (
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
