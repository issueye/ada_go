//go:build windows

package screen

import (
	"image"
	"image/color"
	"testing"

	"computeruse/internal/win32"
)

func TestDownscale(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			src.Set(x, y, color.RGBA{uint8(x * 2), uint8(y * 2), 128, 255})
		}
	}
	dst := Downscale(src, 0.5)
	if got := dst.Bounds().Dx(); got != 50 {
		t.Fatalf("width = %d, want 50", got)
	}
	if got := dst.Bounds().Dy(); got != 50 {
		t.Fatalf("height = %d, want 50", got)
	}
	// The box filter averages the 2×2 block at 0,0: x 0 and 1, y 0 and 1.
	r, g, b, _ := dst.At(0, 0).RGBA()
	if r>>8 != 1 || g>>8 != 1 || b>>8 != 128 {
		t.Fatalf("pixel 0,0 = %d,%d,%d", r>>8, g>>8, b>>8)
	}
	if same := Downscale(src, 1); same != src {
		t.Fatal("a factor of 1 must keep the image")
	}
}

func TestDrawGrid(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	DrawGrid(img, win32.Rect{X: 0, Y: 0, W: 400, H: 300}, 100, 1)
	// A line at x=100 must darken the pixel there, and leave x=150 alone.
	if img.Pix[img.PixOffset(100, 50)+0] >= 255 {
		t.Fatal("no vertical line at x=100")
	}
	if img.Pix[img.PixOffset(150, 50)+0] != 255 {
		t.Fatal("a line appeared between the grid lines")
	}
	// Labels are drawn at the top left of each line.
	differing := 0
	for x := 0; x < 40; x++ {
		for y := 0; y < 40; y++ {
			if img.Pix[img.PixOffset(x, y)+0] < 250 {
				differing++
			}
		}
	}
	if differing == 0 {
		t.Fatal("no coordinate labels at the origin")
	}
	// A grid finer than the image must not hang or panic.
	DrawGrid(img, win32.Rect{X: 0, Y: 0, W: 400, H: 300}, 1, 1)
}

func TestShotMapping(t *testing.T) {
	shot := &Shot{Region: win32.Rect{X: 1920, Y: 0, W: 1920, H: 1080}, Scale: 0.5, Width: 960, Height: 540}
	// The middle of the image is the middle of the second display.
	if p := shot.ScreenPoint(480, 270); p != (win32.Point{X: 1920 + 960, Y: 540}) {
		t.Fatalf("ScreenPoint = %+v", p)
	}
}
