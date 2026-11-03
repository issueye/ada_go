//go:build windows

package cli

import "syscall"

// detach starts the GUI process without a console window and independent of
// the CLI.
func detach() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | createNewProcessGroup}
}
