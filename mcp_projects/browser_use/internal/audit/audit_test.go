package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactArgsEmpty 钉住边界:空参数与 null 都是 nil,解析失败只留字节数,
// 不落原文(原文可能含未脱敏数据)。
func TestRedactArgsEmpty(t *testing.T) {
	if got := RedactArgs("click", nil); got != nil {
		t.Fatalf("nil args 应返回 nil,得到 %v", got)
	}
	if got := RedactArgs("click", json.RawMessage("null")); got != nil {
		t.Fatalf("null args 应返回 nil,得到 %v", got)
	}
	out, ok := RedactArgs("click", json.RawMessage("{broken")).(map[string]any)
	if !ok {
		t.Fatalf("畸形 args 应返回 map,得到 %T", out)
	}
	if _, ok := out["bytes"]; !ok {
		t.Fatalf("畸形 args 应只留字节数: %v", out)
	}
	if len(out) != 1 {
		t.Fatalf("畸形 args 不得携带原文: %v", out)
	}
}

// TestRedactArgsBulkAndText 钉住 bulk 数据与文本的脱敏:data/image 整体删除,
// text 只留长度,expression 截断 200 字符,source 只留字节数。
func TestRedactArgsBulkAndText(t *testing.T) {
	out := RedactArgs("screenshot", json.RawMessage(
		`{"data":"PNG-DATA","image":"BASE64","ref":"e1"}`)).(map[string]any)
	if _, ok := out["data"]; ok {
		t.Fatal("data 应删除")
	}
	if _, ok := out["image"]; ok {
		t.Fatal("image 应删除")
	}
	if out["ref"] != "e1" {
		t.Fatalf("ref 应保留: %v", out["ref"])
	}

	const text = "typed-secret"
	out = RedactArgs("type", json.RawMessage(`{"text":"`+text+`","ref":"e2"}`)).(map[string]any)
	if _, ok := out["text"]; ok {
		t.Fatal("text 应删除,只留长度")
	}
	if out["text_len"] != len(text) { // RedactArgs 存的是原生 int,不是 JSON 回转的 float64
		t.Fatalf("text_len = %v(%T), 期望 %d", out["text_len"], out["text_len"], len(text))
	}

	long := strings.Repeat("x", 300)
	out = RedactArgs("eval", json.RawMessage(`{"expression":"`+long+`"}`)).(map[string]any)
	expr := out["expression"].(string)
	if len(expr) != 200+3 || !strings.HasSuffix(expr, "…") {
		t.Fatalf("expression 应截断为 200 字符+…: len=%d suffix=%q", len(expr), expr[max(0, len(expr)-3):])
	}

	out = RedactArgs("read", json.RawMessage(`{"source":"abc"}`)).(map[string]any)
	if _, ok := out["source"]; ok {
		t.Fatal("source 应删除,只留字节数")
	}
	if out["source_bytes"] != 3 {
		t.Fatalf("source_bytes = %v(%T), 期望 3", out["source_bytes"], out["source_bytes"])
	}
}

// TestRedactArgsCookies 钉住 cookie 值打码:顶层 value 与 cookies[] 数组里的
// value 都替换为 *** 并保留长度;其它工具的 value 不受影响。
func TestRedactArgsCookies(t *testing.T) {
	out := RedactArgs("cookies", json.RawMessage(
		`{"name":"session","value":"TOPSECRET","cookies":[{"name":"a","value":"B2"},{"name":"b"}]}`)).(map[string]any)
	if out["value"] != "***" {
		t.Fatalf("顶层 cookie value 应打码: %v", out["value"])
	}
	if out["value_len"] != len("TOPSECRET") {
		t.Fatalf("value_len = %v(%T), 期望 %d", out["value_len"], out["value_len"], len("TOPSECRET"))
	}
	arr := out["cookies"].([]any)
	first := arr[0].(map[string]any)
	if first["value"] != "***" || first["value_len"] != 2 {
		t.Fatalf("数组内 cookie value 应打码: %v", first)
	}
	second := arr[1].(map[string]any)
	if _, ok := second["value"]; ok {
		t.Fatal("缺失 value 不应凭空生成")
	}

	out = RedactArgs("click", json.RawMessage(`{"value":"plain"}`)).(map[string]any)
	if out["value"] != "plain" {
		t.Fatalf("非 cookies 工具的 value 不应动: %v", out["value"])
	}
}

// TestRedactArgsScript 钉住 script 参数脱敏:args 整体替换为键名列表。
func TestRedactArgsScript(t *testing.T) {
	out := RedactArgs("script", json.RawMessage(
		`{"source":"...","args":{"q":"secret-query","page":2}}`)).(map[string]any)
	if _, ok := out["args"]; ok {
		t.Fatal("script.args 应删除")
	}
	keys := out["args_keys"].([]string)
	if len(keys) != 2 {
		t.Fatalf("args_keys 应有 2 个键: %v", keys)
	}
	if _, ok := out["source"]; ok {
		t.Fatal("source 应删除(只留 source_bytes)")
	}
	if _, ok := out["source_bytes"]; !ok {
		t.Fatal("source_bytes 应保留")
	}
}
