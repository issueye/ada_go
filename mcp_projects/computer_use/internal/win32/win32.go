//go:build windows

// Package win32 wraps the Win32 calls this program needs: the displays, the
// screen, the pointer and the keyboard, the windows of other apps, and the
// clipboard. Everything is in physical pixels of the virtual desktop, whose
// origin is the top-left corner of the primary display and which may extend
// to negative coordinates.
package win32

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shcore   = syscall.NewLazyDLL("shcore.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	procGetDC                      = user32.NewProc("GetDC")
	procReleaseDC                  = user32.NewProc("ReleaseDC")
	procGetCursorPos               = user32.NewProc("GetCursorPos")
	procSetCursorPos               = user32.NewProc("SetCursorPos")
	procSendInput                  = user32.NewProc("SendInput")
	procEnumDisplayMonitors        = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW            = user32.NewProc("GetMonitorInfoW")
	procMonitorFromPoint           = user32.NewProc("MonitorFromPoint")
	procMonitorFromWindow          = user32.NewProc("MonitorFromWindow")
	procEnumWindows                = user32.NewProc("EnumWindows")
	procGetWindowTextW             = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW       = user32.NewProc("GetWindowTextLengthW")
	procGetClassNameW              = user32.NewProc("GetClassNameW")
	procGetWindowRect              = user32.NewProc("GetWindowRect")
	procGetClientRect              = user32.NewProc("GetClientRect")
	procIsWindowVisible            = user32.NewProc("IsWindowVisible")
	procIsWindow                   = user32.NewProc("IsWindow")
	procIsIconic                   = user32.NewProc("IsIconic")
	procIsZoomed                   = user32.NewProc("IsZoomed")
	procGetWindowThreadProcID      = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow        = user32.NewProc("SetForegroundWindow")
	procShowWindow                 = user32.NewProc("ShowWindow")
	procBringWindowToTop           = user32.NewProc("BringWindowToTop")
	procAttachThreadInput          = user32.NewProc("AttachThreadInput")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procPrintWindow                = user32.NewProc("PrintWindow")
	procSystemParametersInfoW      = user32.NewProc("SystemParametersInfoW")
	procSetProcessDPIContext       = user32.NewProc("SetProcessDpiAwarenessContext")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGetWindowLongPtrW          = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW          = user32.NewProc("SetWindowLongPtrW")
	procGetAncestor                = user32.NewProc("GetAncestor")
	procGetAsyncKeyState           = user32.NewProc("GetAsyncKeyState")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procStretchBlt             = gdi32.NewProc("StretchBlt")
	procSetStretchBltMode      = gdi32.NewProc("SetStretchBltMode")
	procSetBrushOrgEx          = gdi32.NewProc("SetBrushOrgEx")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procGetDpiForMonitor       = shcore.NewProc("GetDpiForMonitor")

	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procGetCurrentThreadID         = kernel32.NewProc("GetCurrentThreadId")
	procGetCurrentProcessID        = kernel32.NewProc("GetCurrentProcessId")

	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
)

// Pixel formats and flags.
const (
	SRCCOPY      = 0x00CC0020
	CAPTUREBLT   = 0x40000000
	DIBRGBColors = 0
	BIRGB        = 0
	// halftone makes StretchBlt average the pixels it shrinks, which keeps
	// text legible; with the default it drops nine of every ten.
	halftone             = 4
	PW_RENDERFULLCONTENT = 0x00000002

	INPUTMOUSE    = 0
	INPUTKEYBOARD = 1

	MOUSEEVENTF_MOVE        = 0x0001
	MOUSEEVENTF_LEFTDOWN    = 0x0002
	MOUSEEVENTF_LEFTUP      = 0x0004
	MOUSEEVENTF_RIGHTDOWN   = 0x0008
	MOUSEEVENTF_RIGHTUP     = 0x0010
	MOUSEEVENTF_MIDDLEDOWN  = 0x0020
	MOUSEEVENTF_MIDDLEUP    = 0x0040
	MOUSEEVENTF_XDOWN       = 0x0080
	MOUSEEVENTF_XUP         = 0x0100
	MOUSEEVENTF_WHEEL       = 0x0800
	MOUSEEVENTF_HWHEEL      = 0x01000
	MOUSEEVENTF_ABSOLUTE    = 0x8000
	MOUSEEVENTF_VIRTUALDESK = 0x4000

	XBUTTON1 = 0x0001
	XBUTTON2 = 0x0002

	WHEELDELTA = 120

	KEYEVENTF_EXTENDEDKEY = 0x0001
	KEYEVENTF_KEYUP       = 0x0002
	KEYEVENTF_UNICODE     = 0x0004

	SW_SHOW    = 5
	SW_RESTORE = 9

	PROCESS_QUERY_LIMITED_INFORMATION          = 0x1000
	GA_ROOT                                    = 2
	GMEM_MOVEABLE                              = 0x0002
	CFUNICODETEXT                              = 13
	DWMWA_EXTENDED_FRAME_BOUNDS                = 9
	DWMWA_CLOAKED                              = 14
	MDT_EFFECTIVE_DPI                          = 0
	DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = ^uintptr(0) - 3
)

// native turns an address a Win32 call returned into a pointer. The
// conversion goes through unsafe.Pointer so that vet's checkptr rule
// accepts it, as MyGo does.
func native(addr uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&addr)) }

// Point is a position in physical pixels.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Rect is a rectangle in physical pixels; Right and Bottom are exclusive.
type Rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func (r Rect) Right() int  { return r.X + r.W }
func (r Rect) Bottom() int { return r.Y + r.H }
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.Y >= r.Y && p.X < r.Right() && p.Y < r.Bottom()
}
func (r Rect) Intersect(o Rect) Rect {
	x, y := max(r.X, o.X), max(r.Y, o.Y)
	right, bottom := min(r.Right(), o.Right()), min(r.Bottom(), o.Bottom())
	if right <= x || bottom <= y {
		return Rect{}
	}
	return Rect{X: x, Y: y, W: right - x, H: bottom - y}
}
func (r Rect) Offset(dx, dy int) Rect { return Rect{X: r.X + dx, Y: r.Y + dy, W: r.W, H: r.H} }
func (r Rect) Union(o Rect) Rect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	x, y := min(r.X, o.X), min(r.Y, o.Y)
	right, bottom := max(r.Right(), o.Right()), max(r.Bottom(), o.Bottom())
	return Rect{X: x, Y: y, W: right - x, H: bottom - y}
}

// Display is a monitor.
type Display struct {
	// Name is the device name, as `\\.\DISPLAY1`.
	Name string
	// Bounds is the whole area of the display.
	Bounds Rect
	// Work is the area left for windows, without the taskbar.
	Work Rect
	// Scale is the display's DPI over 96.
	Scale   float64
	DPI     int
	Primary bool
	// DIP is Bounds in device-independent pixels, as MyGo reports it.
	DIP Rect
}

type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) toRect() Rect {
	return Rect{X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)}
}

type point struct{ X, Y int32 }

type monitorInfoEx struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
	Device  [32]uint16
}

const monitorInfoPrimary = 0x00000001

// InitProcess makes the process per-monitor DPI aware, so that calls and
// injected input use physical pixels on displays of any scale. MyGo does
// this too; calling it twice is harmless.
func InitProcess() {
	if procSetProcessDPIContext.Find() == nil {
		if r, _, _ := procSetProcessDPIContext.Call(DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2); r != 0 {
			return
		}
	}
	// Before Windows 10 1703.
	if set := shcore.NewProc("SetProcessDpiAwareness"); set.Find() == nil {
		set.Call(2) // PROCESS_PER_MONITOR_DPI_AWARE
	}
}

var (
	monitorCallback uintptr
	monitorSink     func(m uintptr)
)

func enumMonitors(fn func(m uintptr)) {
	if monitorCallback == 0 {
		monitorCallback = syscall.NewCallback(func(mon, _, _, _ uintptr) uintptr {
			if monitorSink != nil {
				monitorSink(mon)
			}
			return 1
		})
	}
	monitorSink = fn
	procEnumDisplayMonitors.Call(0, 0, monitorCallback, 0)
	monitorSink = nil
}

// Displays lists the monitors, the primary one first.
func Displays() []Display {
	var out []Display
	enumMonitors(func(m uintptr) {
		mi := monitorInfoEx{Size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		if r, _, _ := procGetMonitorInfoW.Call(m, uintptr(unsafe.Pointer(&mi))); r == 0 {
			return
		}
		dpi := 96
		if procGetDpiForMonitor.Find() == nil {
			var dx, dy uint32
			if hr, _, _ := procGetDpiForMonitor.Call(m, MDT_EFFECTIVE_DPI, uintptr(unsafe.Pointer(&dx)), uintptr(unsafe.Pointer(&dy))); hr == 0 && dx > 0 {
				dpi = int(dx)
			}
		}
		b := mi.Monitor.toRect()
		d := Display{
			Name:    syscall.UTF16ToString(mi.Device[:]),
			Bounds:  b,
			Work:    mi.Work.toRect(),
			DPI:     dpi,
			Scale:   float64(dpi) / 96,
			Primary: mi.Flags&monitorInfoPrimary != 0,
		}
		// MyGo reports DIPs per display: physical / scale.
		d.DIP = Rect{
			X: int(float64(b.X) / d.Scale), Y: int(float64(b.Y) / d.Scale),
			W: int(float64(b.W) / d.Scale), H: int(float64(b.H) / d.Scale),
		}
		out = append(out, d)
	})
	// Primary first, then left to right, top to bottom.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if !b.Primary && (a.Primary || lessDisplay(a, b)) {
				break
			}
			out[j-1], out[j] = b, a
		}
	}
	return out
}

func lessDisplay(a, b Display) bool {
	if a.Bounds.X != b.Bounds.X {
		return a.Bounds.X < b.Bounds.X
	}
	return a.Bounds.Y < b.Bounds.Y
}

// Virtual returns the area of all displays together, and their count.
func Virtual() (Rect, []Display) {
	ds := Displays()
	var v Rect
	for _, d := range ds {
		v = v.Union(d.Bounds)
	}
	return v, ds
}

// DisplayAt returns the display holding p, or the nearest one.
func DisplayAt(p Point) (Display, bool) {
	ds := Displays()
	var best Display
	bestDist := -1
	for _, d := range ds {
		if d.Bounds.Contains(p) {
			return d, true
		}
		dx := max(d.Bounds.X-p.X, p.X-d.Bounds.Right(), 0)
		dy := max(d.Bounds.Y-p.Y, p.Y-d.Bounds.Bottom(), 0)
		if dist := dx*dx + dy*dy; bestDist < 0 || dist < bestDist {
			best, bestDist = d, dist
		}
	}
	return best, bestDist == 0
}

// Cursor returns the pointer position in physical pixels.
func Cursor() (Point, error) {
	var p point
	if r, _, err := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		return Point{}, fmt.Errorf("win32: GetCursorPos failed: %v", err)
	}
	return Point{int(p.X), int(p.Y)}, nil
}

// SetCursor moves the pointer, without the mouse messages an app would see.
func SetCursor(p Point) error {
	if r, _, err := procSetCursorPos.Call(uintptr(p.X), uintptr(p.Y)); r == 0 {
		return fmt.Errorf("win32: SetCursorPos failed: %v", err)
	}
	return nil
}

// FocusedWindow returns the window with the keyboard.
func FocusedWindow() uintptr {
	h, _, _ := procGetForegroundWindow.Call()
	return h
}

// CurrentProcessID returns this program's process id, to leave its own
// windows out of the windows an agent lists.
func CurrentProcessID() uint32 { return pid }

var pid, _ = func() (uint32, error) {
	r, _, err := procGetCurrentProcessID.Call()
	return uint32(r), err
}()

// RootWindow returns the top-level ancestor of a window.
func RootWindow(hwnd uintptr) uintptr {
	r, _, _ := procGetAncestor.Call(hwnd, GA_ROOT)
	return r
}

var (
	enumCallback uintptr
	enumSink     func(hwnd uintptr)
)

func enumWindows(fn func(hwnd uintptr)) {
	if enumCallback == 0 {
		enumCallback = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
			if enumSink != nil {
				enumSink(hwnd)
			}
			return 1
		})
	}
	enumSink = fn
	procEnumWindows.Call(enumCallback, 0)
	enumSink = nil
}

// Window is a top-level window of any app.
type Window struct {
	Handle     uintptr
	Title      string
	Class      string
	ProcessID  uint32
	Process    string
	Rect       Rect
	Visible    bool
	Minimized  bool
	Maximized  bool
	Foreground bool
}

func windowTitle(hwnd uintptr) string {
	n, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func windowClass(hwnd uintptr) string {
	buf := make([]uint16, 256)
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func windowRect(hwnd uintptr) Rect {
	// The extended frame bounds leave out the invisible resize borders that
	// Windows 10 adds around windows, which screenshots do not show.
	var r rect
	if res, _, _ := procDwmGetWindowAttribute.Call(hwnd, DWMWA_EXTENDED_FRAME_BOUNDS, uintptr(unsafe.Pointer(&r)), unsafe.Sizeof(r)); res == 0 {
		if out := r.toRect(); !out.Empty() {
			return out
		}
	}
	var g rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&g)))
	return g.toRect()
}

func windowCloaked(hwnd uintptr) bool {
	var cloaked uint32
	if res, _, _ := procDwmGetWindowAttribute.Call(hwnd, DWMWA_CLOAKED, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); res == 0 {
		return cloaked != 0
	}
	return false
}

// procNames caches process names for a few seconds: a window list asks
// for the same processes on every screenshot and every poll.
var procNames = struct {
	mu sync.Mutex
	at map[uint32]procName
}{at: map[uint32]procName{}}

type procName struct {
	name string
	at   time.Time
}

func processName(pid uint32) string {
	if pid == 0 {
		return ""
	}
	procNames.mu.Lock()
	if c, ok := procNames.at[pid]; ok && time.Since(c.at) < 5*time.Second {
		procNames.mu.Unlock()
		return c.name
	}
	procNames.mu.Unlock()
	name := queryProcessName(pid)
	procNames.mu.Lock()
	if len(procNames.at) > 512 {
		clear(procNames.at)
	}
	procNames.at[pid] = procName{name: name, at: time.Now()}
	procNames.mu.Unlock()
	return name
}

func queryProcessName(pid uint32) string {
	h, _, _ := procOpenProcess.Call(PROCESS_QUERY_LIMITED_INFORMATION, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := procQueryFullProcessImageNameW.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func describeWindow(hwnd, foreground uintptr) (Window, bool) {
	if r, _, _ := procIsWindow.Call(hwnd); r == 0 {
		return Window{}, false
	}
	title := windowTitle(hwnd)
	if title == "" {
		return Window{}, false
	}
	if windowCloaked(hwnd) {
		return Window{}, false
	}
	var pid uint32
	procGetWindowThreadProcID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	iconic, _, _ := procIsIconic.Call(hwnd)
	zoomed, _, _ := procIsZoomed.Call(hwnd)
	w := Window{
		Handle:     hwnd,
		Title:      title,
		Class:      windowClass(hwnd),
		ProcessID:  pid,
		Process:    processName(pid),
		Rect:       windowRect(hwnd),
		Visible:    visible != 0,
		Minimized:  iconic != 0,
		Maximized:  zoomed != 0,
		Foreground: hwnd == foreground,
	}
	return w, true
}

// Windows lists the visible top-level windows that have a title, in the
// order the system keeps them, the front one first.
func Windows() []Window {
	foreground := FocusedWindow()
	var out []Window
	enumWindows(func(hwnd uintptr) {
		visible, _, _ := procIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return
		}
		if w, ok := describeWindow(hwnd, foreground); ok {
			out = append(out, w)
		}
	})
	return out
}

// WindowByHandle describes one window.
func WindowByHandle(hwnd uintptr) (Window, bool) {
	return describeWindow(hwnd, FocusedWindow())
}

// FocusWindow brings a window to the front and gives it the keyboard, as a
// click on its title bar would. It restores a minimized window.
func FocusWindow(hwnd uintptr) error {
	if r, _, _ := procIsWindow.Call(hwnd); r == 0 {
		return errors.New("win32: no such window")
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		procShowWindow.Call(hwnd, SW_RESTORE)
	}
	// A foreground window of another process may refuse the keyboard; the
	// system then allows the change for a thread that shares its input
	// state, as the click on a taskbar button does.
	if current := FocusedWindow(); current == hwnd {
		return nil
	}
	var fgProc, targetProc uint32
	fgThread, _, _ := procGetWindowThreadProcID.Call(FocusedWindow(), uintptr(unsafe.Pointer(&fgProc)))
	curThread, _, _ := procGetCurrentThreadID.Call()
	procAttachThreadInput.Call(curThread, fgThread, 1)
	procBringWindowToTop.Call(hwnd)
	procSetForegroundWindow.Call(hwnd)
	procAttachThreadInput.Call(curThread, fgThread, 0)
	_ = targetProc
	if FocusedWindow() != hwnd {
		// Some windows accept neither path; try once more plainly.
		procSetForegroundWindow.Call(hwnd)
	}
	if FocusedWindow() != hwnd {
		return errors.New("win32: the window did not take the keyboard")
	}
	return nil
}
