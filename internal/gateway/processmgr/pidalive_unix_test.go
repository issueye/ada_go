//go:build !windows

package processmgr

// pidAlive 用 0 号信号探测进程是否存活（Unix：进程不存在返回 ESRCH）。

import (
	"os"
	"syscall"
	"testing"
)

func pidAlive(t *testing.T, pid int) bool {
	t.Helper()
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
