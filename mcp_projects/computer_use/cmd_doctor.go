//go:build windows

package main

import (
	"fmt"
	"image"
	"time"

	"computeruse/internal/win32"
)

// runDoctor checks what the program needs: a desktop to capture, a pointer
// to move, and windows to read. It changes nothing on the screen.
func runDoctor(args []string) error {
	fmt.Printf("Computer Use %s\n\n", version)
	virtual, displays := win32.Virtual()
	ok := true
	line := func(good bool, label, value string) {
		mark := "ok  "
		if !good {
			mark = "FAIL"
			ok = false
		}
		fmt.Printf("  %s  %-22s %s\n", mark, label, value)
	}
	if len(displays) == 0 {
		line(false, "displays", "none")
	} else {
		line(true, "displays", fmt.Sprintf("%d, %d×%d at %d,%d", len(displays), virtual.W, virtual.H, virtual.X, virtual.Y))
	}
	for i, d := range displays {
		kind := ""
		if d.Primary {
			kind = ", primary"
		}
		fmt.Printf("          display %d %-16s %d×%d at %d,%d  %d dpi (%.2fx)%s\n",
			i, d.Name, d.Bounds.W, d.Bounds.H, d.Bounds.X, d.Bounds.Y, d.DPI, d.Scale, kind)
	}

	start := time.Now()
	img, err := win32.CaptureScreen(virtual)
	if err != nil {
		line(false, "screen capture", err.Error())
	} else {
		line(true, "screen capture", fmt.Sprintf("%d×%d in %v", img.Bounds().Dx(), img.Bounds().Dy(), time.Since(start).Round(time.Millisecond)))
		line(brightness(img) > 0, "screen is not black", "mean brightness "+fmt.Sprint(brightness(img)))
	}

	// Moving the pointer to where it already is proves injection works
	// without disturbing the user.
	p, err := win32.Cursor()
	if err != nil {
		line(false, "pointer", err.Error())
	} else {
		line(true, "pointer", fmt.Sprintf("%d,%d", p.X, p.Y))
		if err := win32.MoveTo(p); err != nil {
			line(false, "input injection", err.Error())
		} else {
			line(true, "input injection", "SendInput accepted a move")
		}
		if _, _, err := win32.KeyCodes("ctrl+shift+t"); err != nil {
			line(false, "key chords", err.Error())
		} else {
			line(true, "key chords", "ctrl+shift+t parses")
		}
	}

	wins := win32.Windows()
	line(len(wins) > 0, "windows", fmt.Sprintf("%d visible", len(wins)))
	if fg, good := win32.WindowByHandle(win32.FocusedWindow()); good {
		line(true, "foreground", fmt.Sprintf("%q (%s)", fg.Title, fg.Process))
	}

	text, err := win32.ClipboardText()
	if err != nil {
		line(false, "clipboard", err.Error())
	} else {
		line(true, "clipboard", fmt.Sprintf("%d characters of text", len([]rune(text))))
	}

	// The mask needs a compositor; without one it would draw opaque black.
	line(true, "mask", "started with `computer-use run`; see the tray icon")
	fmt.Println()
	if !ok {
		return fmt.Errorf("the desktop is not ready")
	}
	fmt.Println("ready: an agent can capture this desktop and drive it")
	return nil
}

// brightness is the mean luminance of every 64th pixel, to tell a black
// capture from a real one.
func brightness(img *image.RGBA) int {
	b := img.Bounds()
	if b.Empty() {
		return 0
	}
	sum, n := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y += 8 {
		for x := b.Min.X; x < b.Max.X; x += 8 {
			o := img.PixOffset(x, y)
			sum += int(img.Pix[o]) + int(img.Pix[o+1]) + int(img.Pix[o+2])
			n += 3
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}
