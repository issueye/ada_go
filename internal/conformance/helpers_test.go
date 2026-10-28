package conformance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot 返回仓库根目录（相对测试工作目录 internal/conformance）。
//
// 用 runtime.Caller 而不是 ../../..，是为了让用例在 go test 指定 -C 或 IDE
// 以仓库根为工作目录时仍能定位夹具（两种 GOWORK 模式下路径一致）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试源文件路径")
	}
	dir := filepath.Dir(thisFile) // internal/conformance
	root := filepath.Clean(filepath.Join(dir, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("推断的仓库根 %s 里没有 go.mod: %v", root, err)
	}
	return root
}

// repoPath 拼出仓库内的绝对路径并断言存在。
func repoPath(t *testing.T, elems ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{repoRoot(t)}, elems...)...)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("夹具缺失 %s: %v", p, err)
	}
	return p
}

// fixtureDir 返回 internal/conformance/testdata。
func fixtureDir(t *testing.T, elems ...string) string {
	t.Helper()
	return repoPath(t, append([]string{"internal", "conformance", "testdata"}, elems...)...)
}

// readFixture 读取夹具原文。
func readFixture(t *testing.T, elems ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixtureDir(t, elems...))
	if err != nil {
		t.Fatalf("读取夹具失败: %v", err)
	}
	return raw
}

// mustNoErr 是本地断言辅助（不引入 testify，10 §11.2）。
func mustNoErr(t *testing.T, where string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
}

// mustErrContains 断言错误信息指向缺失能力/点位/插槽名（12 §6.14 的要求：
// 「报错直接指向缺失能力」，不是笼统的装配失败）。
func mustErrContains(t *testing.T, where string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: 期望错误且信息包含 %q，实得 nil", where, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: 期望错误信息包含 %q，实得 %q", where, want, err.Error())
	}
}

// mustTrue 断言条件成立。
func mustTrue(t *testing.T, where string, ok bool) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: 期望为真", where)
	}
}

// mustEqual 断言字符串相等（差异一并打印）。
func mustEqual(t *testing.T, where, want, got string) {
	t.Helper()
	if want != got {
		t.Fatalf("%s:\n want=%q\n  got=%q", where, want, got)
	}
}

// mustCount 断言数量一致。
func mustCount(t *testing.T, where string, want, got int) {
	t.Helper()
	if want != got {
		t.Fatalf("%s: 期望 %d，实得 %d", where, want, got)
	}
}

// sortedStrings 返回排序副本（集合比较用）。
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
