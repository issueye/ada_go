// Package prockill 是网关名下子进程的统一进程树终止原语：
// Configure 在 spawn 时把命令放进独立进程组（Unix），Kill 按平台语义
// 杀掉整棵进程树并在平台命令失败时回退单体 Kill。
//
// 它同时服务两类父进程：supervisor（Core 子进程）与 processmgr（插件长驻进程）。
// 两边都必须杀树——Core/插件都会派生自己的子进程，只杀主进程会留孤儿
// （09 §9.2「停止后无残留」；e2e_proc_*_test 的回收语义以此为准）。
package prockill

import "os/exec"

// Configure 在启动前对 cmd 做平台所需的进程组标记（Unix 下 Setpgid，
// Windows 无需处理）。必须在 cmd.Start 之前调用。
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	configureProcessTree(cmd)
}

// Kill 杀掉 pid 及其整棵子树；平台命令失败时回退单体 Kill。
// pid <= 0 直接返回。fire-and-forget：错误只兜底不外抛。
func Kill(pid int) {
	killProcessTree(pid)
}
