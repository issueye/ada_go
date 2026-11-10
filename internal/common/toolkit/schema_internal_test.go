package toolkit

import (
	"encoding/json"
	"strings"
	"testing"
)

// 模型常把数组/对象序列化成字符串再传；错误文案要指出正确形态，
// 否则模型只会换一种引号继续猜。
func TestSchemaTypeErrorHintsSerializedString(t *testing.T) {
	s, err := compileSchema(json.RawMessage(`{"type":"object","properties":{"args":{"type":"array","items":{"type":"string"}}}}`))
	if err != nil {
		t.Fatalf("compileSchema: %v", err)
	}
	err = s.Validate(json.RawMessage(`{"args":"[\"-NoProfile\"]"}`))
	if err == nil {
		t.Fatal("字符串 args 应被拒绝")
	}
	if !strings.Contains(err.Error(), "实际是 string") || !strings.Contains(err.Error(), "不要传序列化后的字符串") {
		t.Fatalf("错误文案缺少提示: %v", err)
	}
	// 正确的数组形态不受影响。
	if err := s.Validate(json.RawMessage(`{"args":["-NoProfile"]}`)); err != nil {
		t.Fatalf("合法数组被拒: %v", err)
	}
}
