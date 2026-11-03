//go:build windows

package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"

	"computeruse/internal/win32"
)

// Composite blends an overlay rendered by a window, a PNG whose size is in
// the DIPs of the display it covers, over a fresh capture of that display.
// The result is what the user sees with the mask on, which a plain screen
// capture cannot show: the overlay stays out of screenshots.
func Composite(region win32.Rect, overlayPNG []byte) ([]byte, error) {
	desktop, err := win32.CaptureScreen(region)
	if err != nil {
		return nil, err
	}
	overlay, err := png.Decode(bytes.NewReader(overlayPNG))
	if err != nil {
		return nil, fmt.Errorf("screen: cannot read the overlay: %w", err)
	}
	ow, oh := overlay.Bounds().Dx(), overlay.Bounds().Dy()
	if ow == 0 || oh == 0 {
		return nil, fmt.Errorf("screen: the overlay is empty")
	}
	// The overlay's pixels are DIPs of the display: scale it to the
	// capture's pixels, and scale that capture down when the overlay is
	// smaller than the screen.
	if dw, dh := desktop.Bounds().Dx(), desktop.Bounds().Dy(); dw != ow || dh != oh {
		if dw > ow && dh > oh {
			desktop = Downscale(desktop, float64(ow)/float64(dw))
		}
		scaled := resize(overlay, desktop.Bounds().Dx(), desktop.Bounds().Dy())
		draw.Draw(desktop, desktop.Bounds(), scaled, scaled.Bounds().Min, draw.Over)
	} else {
		draw.Draw(desktop, desktop.Bounds(), overlay, overlay.Bounds().Min, draw.Over)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, desktop); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resize scales an image to w×h by nearest neighbour, which keeps the
// overlay's crisp lines and text readable.
func resize(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	for y := 0; y < h; y++ {
		sy := b.Min.Y + y*sh/h
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*sw/w
			r, g, bb, a := src.At(sx, sy).RGBA()
			o := dst.PixOffset(x, y)
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = byte(r>>8), byte(g>>8), byte(bb>>8), byte(a>>8)
		}
	}
	return dst
}
