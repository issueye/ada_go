//go:build windows

package corerpc

import "golang.org/x/sys/windows"

// STILL_ACTIVE 是 GetExitCodeProcess 对仍在运行进程返回的码。
const stillActive = 259

// parentAlive 用 OpenProcess + GetExitCodeProcess 探测父进程。
//
// 只有「参数无效」（进程不存在）才判定为死；访问被拒等错误保守地当作还活着，
// 避免在权限受限的父进程上误杀 Core。
func parentAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return false
		}
		return true
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
