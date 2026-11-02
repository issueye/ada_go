//go:build windows

package host

// Windows 进程树语义：taskkill /T 连子进程一起杀。
// 这不等同于 Job Object（Job Object 能覆盖进程组之外的托管关系），
// 但对 execFile 直接派生的进程树足够；09 §9.2 的 Job Object 表述以本实现为准。

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// configureProcessTree 在 Windows 上不需要额外进程组标记。
func configureProcessTree(*exec.Cmd) {}

// killProcessTree 用 taskkill /F /T 杀进程及其子树；taskkill 失败时回退到杀单个进程。
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run(); err == nil {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
