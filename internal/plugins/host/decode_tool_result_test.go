package host

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// 系统插件包装内层工具时可以用 status/reason 保留 denied/timeout 等回执语义。
func TestDecodeToolResultStatus(t *testing.T) {
	res, err := decodeToolResult("tool-discovery", json.RawMessage(`{
		"content":[{"type":"text","text":"denied by policy: no"}],
		"status":"denied","reason":"no policy"
	}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Receipt.Status != domain.ReceiptDenied || res.Receipt.Reason != "no policy" {
		t.Fatalf("receipt = %+v", res.Receipt)
	}
	if _, err := decodeToolResult("x", json.RawMessage(`{"status":"bogus"}`), time.Now()); err == nil {
		t.Fatal("未知状态应被拒绝")
	}
}
