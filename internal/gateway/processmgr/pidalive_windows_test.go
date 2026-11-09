//go:build windows

package processmgr

// pidAlive 在 Windows 上无法用 0 号信号探测：已退出的 pid 的 Signal 会返回
// ErrProcessDone。这里用 tasklist 判定，避免依赖 Unix 信号语义。

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func pidAlive(t *testing.T, pid int) bool {
	t.Helper()
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").CombinedOutput()
	if err != nil {
		// tasklist 不可用时不阻断测试；宁可漏报残留，也不要误报失败。
		t.Skipf("tasklist 不可用，跳过残留检查: %v", err)
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
