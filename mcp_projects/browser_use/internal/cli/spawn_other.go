//go:build !windows

package cli

import "syscall"

// detach starts the GUI process in its own session.
func detach() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
