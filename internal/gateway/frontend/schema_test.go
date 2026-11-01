package frontend_test

// block 数据 Schema（受控 2020-12 子集）的直接单测：
// 校验器是手写的，关键字语义、边界与「未知关键字必须报错」都要单独钉住，
// 不能只靠图表/usage 块的间接覆盖。

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/frontend"
)

func decodeData(t *testing.T, raw string) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("解码测试数据 %s: %v", raw, err)
	}
	return out
}

func compileSchema(t *testing.T, raw string) *frontend.DataSchema {
	t.Helper()
	s, err := frontend.CompileDataSchema([]byte(raw))
	if err != nil {
		t.Fatalf("编译 Schema %s: %v", raw, err)
	}
	return s
}

func TestDataSchemaCompileErrors(t *testing.T) {
	cases := []struct{ name, schema, want string }{
		{"未知关键字", `{"type":"object","multipleOf":2}`, "multipleOf"},
		{"type 非法", `{"type":"tuple"}`, "未知类型"},
		{"type 数组为空", `{"type":[]}`, "不能为空"},
		{"required 非字符串", `{"required":[1]}`, "required"},
		{"properties 非对象", `{"properties":[]}`, "properties"},
		{"additionalProperties 非法", `{"additionalProperties":1}`, "additionalProperties"},
		{"pattern 非法", `{"pattern":"["}`, "pattern"},
		{"minLength 负数", `{"minLength":-1}`, "非负整数"},
		{"items 非对象", `{"items":true}`, "items"},
	}
	for _, tc := range cases {
		if _, err := frontend.CompileDataSchema([]byte(tc.schema)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

func TestDataSchemaValidate(t *testing.T) {
	cases := []struct {
		name, schema, data, want string
	}{
		{"对象合法", `{"type":"object","properties":{"a":{"type":"number"}},"required":["a"],"additionalProperties":false}`, `{"a":1}`, ""},
		{"缺少必填", `{"type":"object","properties":{"a":{"type":"number"}},"required":["a"],"additionalProperties":false}`, `{}`, "缺少必填"},
		{"未知字段", `{"type":"object","properties":{"a":{"type":"number"}},"additionalProperties":false}`, `{"x":1}`, "未知字段"},
		{"允许额外字段", `{"type":"object"}`, `{"x":1}`, ""},
		{"额外字段 Schema", `{"type":"object","additionalProperties":{"type":"string"}}`, `{"x":1}`, "类型"},
		{"enum", `{"enum":["a","b"]}`, `"c"`, "enum"},
		{"enum 数字相等", `{"enum":[1]}`, `1.0`, ""},
		{"const", `{"const":"x"}`, `"y"`, "const"},
		{"pattern", `{"type":"string","pattern":"^[a-z]+$"}`, `"ABC"`, "pattern"},
		{"minLength", `{"type":"string","minLength":2}`, `"a"`, "短于"},
		{"maxLength", `{"type":"string","maxLength":3}`, `"abcd"`, "超过 maxLength"},
		{"minimum", `{"type":"number","minimum":1}`, `0`, "minimum"},
		{"maximum", `{"type":"number","maximum":1}`, `2`, "maximum"},
		{"integer 拒绝小数", `{"type":"integer"}`, `1.5`, "类型"},
		{"items", `{"type":"array","items":{"type":"string"}}`, `["a",1]`, "类型"},
		{"minItems", `{"type":"array","minItems":2}`, `[1]`, "minItems"},
		{"maxItems", `{"type":"array","maxItems":1}`, `[1,2]`, "maxItems"},
		{"allOf", `{"allOf":[{"type":"object","required":["a"]},{"type":"object","required":["b"]}]}`, `{"a":1}`, "缺少必填"},
		{"anyOf 不匹配", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `true`, "anyOf"},
		{"oneOf 多个匹配", `{"oneOf":[{"type":"number"},{"type":"integer"}]}`, `1`, "oneOf"},
		{"oneOf 单个匹配", `{"oneOf":[{"type":"string"},{"type":"number"}]}`, `1`, ""},
		{"null 联合类型", `{"type":["string","null"]}`, `null`, ""},
		{"嵌套属性", `{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}},"required":["b"]}}}`, `{"a":{}}`, "缺少必填"},
	}
	for _, tc := range cases {
		s := compileSchema(t, tc.schema)
		err := s.ValidateData(decodeData(t, tc.data))
		if tc.want == "" {
			if err != nil {
				t.Fatalf("%s: 不应失败: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

// TestDataSchemaDepthLimit 钉住 data 深度上限（32 层）：
// 校验器按 Schema 递归下降，超过上限必须报错而不是无界递归。
func TestDataSchemaDepthLimit(t *testing.T) {
	schema := `{"type":"string"}`
	data := `"x"`
	for i := 0; i < 33; i++ {
		schema = `{"type":"object","properties":{"a":` + schema + `},"required":["a"]}`
		data = `{"a":` + data + `}`
	}
	s := compileSchema(t, schema)
	err := s.ValidateData(decodeData(t, data))
	if err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("深层数据应触发 32 层上限: %v", err)
	}
}
