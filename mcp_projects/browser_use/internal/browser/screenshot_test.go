package browser

import (
	"image"
	"image/color"
	"testing"
)

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// TestStitchTiles 钉住整页截图的拼接:各分段按 device-pixel 偏移就位,
// 末段超出画布时画布随之加高;重叠分段按顺序覆盖(滚动钳制在页面底部时,
// 相邻分段内容一致,覆盖无害)。
func TestStitchTiles(t *testing.T) {
	red := color.RGBA{R: 255, A: 255}
	green := color.RGBA{G: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}

	stitched := stitchTiles([]fullPageTile{
		{img: solidRGBA(10, 8, red), yPix: 0},
		{img: solidRGBA(10, 8, green), yPix: 8},
		{img: solidRGBA(10, 6, blue), yPix: 16},
	})
	if stitched == nil {
		t.Fatal("stitchTiles 返回 nil")
	}
	if got := stitched.Bounds().Dx(); got != 10 {
		t.Fatalf("宽 = %d, 期望 10", got)
	}
	if got := stitched.Bounds().Dy(); got != 22 {
		// 画布高度 = max(yPix + 段高):16+6=22(第三段比前两段矮)。
		t.Fatalf("高 = %d, 期望 22", got)
	}
	// 每段顶部第一个像素的颜色要与该段一致(就位正确)。
	if got := stitched.RGBAAt(0, 0); got != red {
		t.Fatalf("tile0 @0 = %v, 期望红", got)
	}
	if got := stitched.RGBAAt(0, 8); got != green {
		t.Fatalf("tile1 @8 = %v, 期望绿", got)
	}
	if got := stitched.RGBAAt(0, 16); got != blue {
		t.Fatalf("tile2 @16 = %v, 期望蓝", got)
	}

	// 重叠:第二段 yPix=4(与第一段 4..8 重叠),画布高度取两段的并集。
	overlapped := stitchTiles([]fullPageTile{
		{img: solidRGBA(10, 8, red), yPix: 0},
		{img: solidRGBA(10, 8, blue), yPix: 4},
	})
	if overlapped.Bounds().Dy() != 12 {
		t.Fatalf("重叠画布高 = %d, 期望 4+8=12", overlapped.Bounds().Dy())
	}
	// 重叠区(4..8)应被后一段覆盖为蓝。
	if got := overlapped.RGBAAt(0, 6); got != blue {
		t.Fatalf("重叠区应被后段覆盖: %v", got)
	}

	if stitchTiles(nil) != nil {
		t.Fatal("空分段应返回 nil")
	}
}
