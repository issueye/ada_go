package control

import (
	"encoding/json"
	"testing"
)

// TestToolSpecs 钉工具表的不变量：名字唯一、schema 是合法 JSON、工具名集合与表同步。
// 异步任务（/v1/calls）与 MCP tools/list 都按这份表校验，漏一条就是协议缺口。
func TestToolSpecs(t *testing.T) {
	seen := make(map[string]bool, len(Tools))
	for _, spec := range Tools {
		if spec.Name == "" {
			t.Fatal("工具名不能为空")
		}
		if seen[spec.Name] {
			t.Fatalf("工具名重复：%s", spec.Name)
		}
		seen[spec.Name] = true
		if !json.Valid(spec.Schema) {
			t.Fatalf("%s 的 inputSchema 不是合法 JSON", spec.Name)
		}
		if spec.Description == "" {
			t.Fatalf("%s 缺少描述", spec.Name)
		}
		if !toolNameSet[spec.Name] {
			t.Fatalf("toolNameSet 缺少 %s", spec.Name)
		}
	}
}

// TestConsoleToolSpec 钉 console 工具的形状：四个动作、无必填参数、过滤器齐全。
func TestConsoleToolSpec(t *testing.T) {
	var spec *ToolSpec
	for i := range Tools {
		if Tools[i].Name == "console" {
			spec = &Tools[i]
			break
		}
	}
	if spec == nil {
		t.Fatal("工具表缺少 console")
	}
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		t.Fatalf("console schema: %v", err)
	}
	want := []string{"list", "get", "clear", "wait"}
	got := schema.Properties["action"].Enum
	if len(got) != len(want) {
		t.Fatalf("console action enum = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("console action enum = %v，期望 %v", got, want)
		}
	}
	for _, key := range []string{"level", "text_contains", "source", "since", "limit", "id", "timeout_ms", "tab"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("console schema 缺少 %s", key)
		}
	}
	if len(schema.Required) != 0 {
		t.Fatalf("console 不应有必填参数，得到 %v", schema.Required)
	}
}
