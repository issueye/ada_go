//go:build windows

package win32

// Extended window styles this program adds to its own overlay windows.
const (
	WS_EX_TOOLWINDOW = 0x00000080
	WS_EX_NOACTIVATE = 0x08000000
	// GWL_EXSTYLE is -20, as the system takes it unsigned.
	GWL_EXSTYLE = ^uintptr(19)
)

// AddWindowExStyle adds style bits to a window, keeping the others. The
// overlay uses it to stay out of Alt+Tab (WS_EX_TOOLWINDOW) and to never
// take the keyboard from the app the agent drives (WS_EX_NOACTIVATE).
func AddWindowExStyle(hwnd uintptr, bits uint32) {
	if hwnd == 0 {
		return
	}
	current := WindowExStyle(hwnd)
	want := current | bits
	if want == current {
		return
	}
	if procSetWindowLongPtrW.Find() == nil {
		procSetWindowLongPtrW.Call(hwnd, GWL_EXSTYLE, uintptr(want))
		return
	}
	// 32-bit Windows has no SetWindowLongPtrW.
	set := user32.NewProc("SetWindowLongW")
	if set.Find() == nil {
		set.Call(hwnd, GWL_EXSTYLE, uintptr(want))
	}
}

// WindowExStyle returns the extended style bits of a window.
func WindowExStyle(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	if procGetWindowLongPtrW.Find() == nil {
		v, _, _ := procGetWindowLongPtrW.Call(hwnd, GWL_EXSTYLE)
		return uint32(v)
	}
	get := user32.NewProc("GetWindowLongW")
	if get.Find() != nil {
		return 0
	}
	v, _, _ := get.Call(hwnd, GWL_EXSTYLE)
	return uint32(v)
}
