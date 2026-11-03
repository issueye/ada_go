package app

import (
	_ "embed"
	"runtime"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// appIconPNG is the browser-use icon: a browser window with an agent badge.
//
//go:embed icon.png
var appIconPNG []byte

// trayIconPNG is the 32x32 notification-area / menu-bar icon, scaled from
// icon.png by tools/icopack. It is small so Windows and macOS do not have
// to shrink the 256px app icon for the tray.
//
//go:embed tray.png
var trayIconPNG []byte

var (
	iconOnce sync.Once
	iconSrc  ui.ImageSource
)

// appIcon returns the icon as a UI image for the title bar.
func appIcon() ui.ImageSource {
	iconOnce.Do(func() {
		iconSrc, _ = ui.DecodeImage(appIconPNG)
	})
	return iconSrc
}

// applyIcon sets the window icon (title bar and taskbar) and, on macOS,
// the Dock icon.
func applyIcon(w *mygo.Window) {
	if w == nil {
		return
	}
	_ = w.SetIcon(appIconPNG)
	if runtime.GOOS == "darwin" {
		_ = mygo.App.Dock.SetIcon(appIconPNG)
	}
}
