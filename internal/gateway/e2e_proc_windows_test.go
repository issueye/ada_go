//go:build windows

package gateway_test

// Windows 进程树清理：gateway 由 Supervisor 拉起 Core 子进程，
// 只对父进程 Kill 可能留下持锁的 Core，因此用 taskkill /T /F 整棵树回收。

import (
	"os/exec"
	"strconv"
)

func configureProcessGroup(*exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// 失败通常表示进程已经退出；后续 Wait 仍会拿到最终状态。
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
