package frontend

// P0-3 一致性测试骨架（docs/20）：同一份 VNode fixture 的 Go SSR golden。
// 浏览器侧由 internal/gateway/web/testdata/iso-parity.test.js 读取同一份
// parity.vnode.json / parity.html，比较 mount 后的 DOM 结构。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsoParityGolden(t *testing.T) {
	dir := filepath.Join("testdata", "iso")
	raw, err := os.ReadFile(filepath.Join(dir, "parity.vnode.json"))
	if err != nil {
		t.Fatalf("读取 VNode fixture 失败: %v", err)
	}
	got, err := RenderHTML(raw)
	if err != nil {
		t.Fatalf("RenderHTML: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "parity.html"))
	if err != nil {
		t.Fatalf("读取 golden 失败: %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Fatalf("SSR golden 不符：\n got %s\nwant %s", got, want)
	}
}
