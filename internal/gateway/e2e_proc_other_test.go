//go:build !windows && !unix

package gateway_test

// 其它平台只保证可编译：没有进程组语义，退化为杀父进程；
// Core 仍会通过 --parent-pid 的 WatchParent 自行退出。

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
