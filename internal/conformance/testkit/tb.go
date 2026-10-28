package testkit

// TB 是 *testing.T 的最小子集（10 §11.1：替身必须能在测试包之外复用，
// 因此不 import "testing"，避免把 flag 解析拖进非测试二进制）。
//
// *testing.T、*testing.B 与 *testing.F 都天然满足它。
type TB interface {
	// Helper 把本文件从报告的调用栈里剔除。
	Helper()
	// Cleanup 注册测试结束时的清理函数（临时工作区目录靠它回收）。
	Cleanup(func())
	// TempDir 返回该测试独占的临时目录。
	TempDir() string
	// Fatalf 立即失败。
	Fatalf(format string, args ...any)
}

// MustNil 断言 err 为空；用于替身内部「不该失败」的调用点。
func MustNil(t TB, where string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: 期望无错误，实得 %v", where, err)
	}
}

// MustErr 断言 err 非空且信息包含 want 片段（12 §6.14 的「报错直接指向缺失能力」
// 也用它断言：片段就是缺失的那个能力/点位/插槽名）。
func MustErr(t TB, where string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: 期望错误包含 %q，实得 nil", where, want)
	}
	if !contains(err.Error(), want) {
		t.Fatalf("%s: 期望错误包含 %q，实得 %q", where, want, err.Error())
	}
}

// MustTrue 断言条件成立。
func MustTrue(t TB, where string, ok bool) {
	t.Helper()
	if !ok {
		t.Fatalf("%s: 期望为真", where)
	}
}

// contains 是 strings.Contains 的局部包装（避免多文件重复 import）。
func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	n, m := len(haystack), len(needle)
	if m > n {
		return -1
	}
	for i := 0; i+m <= n; i++ {
		if haystack[i:i+m] == needle {
			return i
		}
	}
	return -1
}
