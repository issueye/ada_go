//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"time"

	"github.com/egoist/mygo"

	"computeruse/internal/mask"
	"computeruse/internal/win32"
)

// runDemo shows the mask with sample annotations for a few seconds: what
// the user sees while an agent works, without an agent.
func runDemo(args []string) error {
	seconds := 20
	if v, ok := flagValue(args, "seconds"); ok && v != "" {
		seconds = atoi(v)
	}
	grid := 100
	if v, ok := flagValue(args, "grid"); ok && v != "" {
		grid = atoi(v)
	}
	preview := ""
	if v, ok := flagValue(args, "preview"); ok {
		preview = v
	}
	overlay := mask.New()

	mygo.App.WhenReady(func() {
		if err := overlay.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "computer-use:", err)
			mygo.App.Quit()
			return
		}
		_, displays := win32.Virtual()
		if len(displays) == 0 {
			mygo.App.Quit()
			return
		}
		d := displays[0]
		for _, dd := range displays {
			if dd.Primary {
				d = dd
			}
		}
		work := d.Work
		// A "dialog" in the middle, and the field inside it an agent would
		// click, both with their coordinates written beside them.
		box := win32.Rect{
			X: work.X + work.W/5, Y: work.Y + work.H/4,
			W: work.W * 3 / 5, H: work.H / 2,
		}
		field := win32.Rect{
			X: box.X + box.W/8, Y: box.Y + box.H*3/5,
			W: box.W * 3 / 4, H: box.H / 5,
		}
		overlay.Update(func(st *mask.State) {
			st.Visible = true
			st.Dim = 0.5
			st.Dark = true
			st.Grid = grid
			st.Coords = true
			st.HUD = mask.HUD{
				Title:  "Computer Use",
				Detail: "演示蒙板 · mask demo",
				Busy:   false,
			}
			st.Regions = []mask.Region{
				{Rect: box, Label: "示例元素 · sample element", Index: 1, Tone: "accent"},
				{Rect: field, Label: "点击这里 · click here", Index: 2, Tone: "green"},
			}
			st.Markers = []mask.Marker{{
				At:    win32.Point{X: field.X + field.W/2, Y: field.Y + field.H/2},
				Label: "目标 · target",
				Tone:  "accent",
			}}
			st.Chips = []mask.Chip{{
				At:   win32.Point{X: box.X + 24, Y: box.Y + box.H + 40},
				Text: "坐标均为物理像素 · coordinates are physical pixels",
			}}
		})
		if preview != "" {
			// The demo's preview is a real capture of the screen with the
			// overlay left in it: exactly what the user sees, which the
			// window capture cannot promise while the app warms up.
			overlay.SetProtected(false)
			go func() {
				defer overlay.SetProtected(true)
				// A click, so that the preview shows the rings and the pill
				// of an action, not only the outline.
				time.Sleep(1200 * time.Millisecond)
				overlay.SetHUD(fmt.Sprintf("click %d,%d", field.X+field.W/2, field.Y+field.H/2), true)
				overlay.Ripple(win32.Point{X: field.X + field.W/2, Y: field.Y + field.H/2}, "accent")
				time.Sleep(200 * time.Millisecond)
				img, err := win32.CaptureScreen(d.Bounds)
				if err != nil {
					fmt.Fprintln(os.Stderr, "computer-use:", err)
					return
				}
				var buf bytes.Buffer
				if err := png.Encode(&buf, img); err != nil {
					fmt.Fprintln(os.Stderr, "computer-use:", err)
					return
				}
				if err := os.WriteFile(preview, buf.Bytes(), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "computer-use:", err)
					return
				}
				fmt.Printf("saved the mask preview to %s\n", preview)
				overlay.SetHUD("待命 · idle", false)
			}()
		}
		fmt.Printf("demo: the mask shows for %d s (Ctrl+Alt+Shift+X clears it, Ctrl+Alt+Shift+Q quits)\n", seconds)
		go func() {
			time.Sleep(time.Duration(seconds) * time.Second)
			mygo.App.Quit()
		}()
	})
	return mygo.App.Run()
}
