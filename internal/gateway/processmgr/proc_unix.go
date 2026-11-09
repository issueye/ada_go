//go:build !windows

package processmgr

// Unix 进程树语义：被管进程以自己为进程组组长启动，停止时向整个组发 SIGKILL。
// 这样 node 脚本派生的孙进程也会一起退出（09 §9.2、计划 P3「停止后无残留」）。

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcessTree 让命令成为新进程组的组长。
func configureProcessTree(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree 杀整个进程组；组已不存在时回退到杀单个进程。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
