//go:build unix

package gateway_test

// Unix 进程树清理：让 gateway 成为新进程组的组长，
// 回收时直接杀整个进程组，连带 Supervisor 拉起的 Core 子进程。

import (
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	// 负 pid 表示向进程组发信号；失败时再对单体进程兜底。
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
