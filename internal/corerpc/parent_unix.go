//go:build !windows

package corerpc

import (
	"errors"
	"syscall"
)

// parentAlive 用 signal 0 探测进程是否存在（EPERM 表示存在但无权限，仍算活）。
func parentAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
