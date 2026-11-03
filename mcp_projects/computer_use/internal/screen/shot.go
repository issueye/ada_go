//go:build windows

// Package screen takes screenshots of the desktop or of one display and
// encodes them, with an optional grid of coordinates burned into the image
// so that whoever looks at it can name a point to click.
package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"time"
	"unsafe"

	"computeruse/internal/win32"
)

// Options choose what a screenshot shows and how big it is.
type Options struct {
	// Region is the part of the desktop to capture, in physical pixels.
	// The zero value means every display.
	Region win32.Rect
	// MaxWidth shrinks the image to this width, keeping its proportions.
	MaxWidth int
	// Scale shrinks the image by this factor instead of MaxWidth.
	Scale float64
	// Grid burns a grid of coordinates into the image every Grid physical
	// pixels, with their values at the edges.
	Grid int
	// Cursor burns a marker and the coordinates of the pointer in.
	Cursor bool
	// Format is "png" (the default) or "jpeg".
	Format string
	// Quality is the JPEG quality, 1 to 100; 85 when zero.
	Quality int
	// Fast captures without CAPTUREBLT, which is faster but can leave
	// layered windows of other apps out of the picture.
	Fast bool
}

// Shot is an encoded screenshot and what it maps to.
type Shot struct {
	Data []byte
	// Width and Height are the image's pixels.
	Width, Height int
	// Region is the part of the desktop the image shows; Scale tells how
	// many image pixels one desktop pixel takes. A desktop point p is the
	// image point (p - Region.origin) * Scale.
	Region win32.Rect
	Scale  float64
	Format string
	// CaptureMS, ScaleMS and EncodeMS are where the time went, to show an
	// agent, or a person, what a slow screenshot waits on.
	CaptureMS, ScaleMS, EncodeMS int64
}

// Take captures the desktop and encodes it.
func Take(o Options) (*Shot, error) {
	virtual, _ := win32.Virtual()
	region := o.Region
	if region.Empty() {
		region = virtual
	}
	region = region.Intersect(virtual)
	if region.Empty() {
		return nil, fmt.Errorf("screen: the region is outside the desktop (%v)", o.Region)
	}
	// A downscale can happen while the screen is copied: GDI then reads the
	// screen once, at the size asked for, instead of reading every pixel
	// and averaging them on the CPU.
	scale := 1.0
	switch {
	case o.Scale > 0 && o.Scale < 1:
		scale = o.Scale
	case o.MaxWidth > 0 && region.W > o.MaxWidth:
		scale = float64(o.MaxWidth) / float64(region.W)
	}
	t0 := time.Now()
	var img *image.RGBA
	var err error
	switch {
	case scale < 1 && o.Fast:
		// GDI shrinks while it copies: one pass, and no CPU resample.
		w := max(1, int(math.Round(float64(region.W)*scale)))
		h := max(1, int(math.Round(float64(region.H)*scale)))
		img, err = win32.CaptureScaled(region, w, h)
	case o.Fast:
		img, err = win32.CaptureScreenFast(region)
	default:
		img, err = win32.CaptureScreen(region)
	}
	if err != nil {
		return nil, err
	}
	captureMS := time.Since(t0).Milliseconds()
	t1 := time.Now()
	if scale < 1 {
		if !o.Fast {
			img = Downscale(img, scale)
		}
		scale = float64(img.Bounds().Dx()) / float64(region.W)
	}
	if o.Grid > 0 {
		DrawGrid(img, region, o.Grid, scale)
	}
	if o.Cursor {
		if p, err := win32.Cursor(); err == nil && region.Contains(p) {
			drawCursor(img, region, scale, p)
		}
	}
	scaleMS := time.Since(t1).Milliseconds()
	t2 := time.Now()
	format := o.Format
	if format == "" {
		format = "png"
	}
	var buf bytes.Buffer
	switch format {
	case "png":
		// BestSpeed: a screenshot is a temporary picture for an agent, and
		// the default compression takes three times as long to save bytes
		// nobody keeps.
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, err
		}
	case "jpeg", "jpg":
		format = "jpeg"
		q := o.Quality
		if q <= 0 || q > 100 {
			q = 85
		}
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("screen: unknown image format %q", o.Format)
	}
	return &Shot{
		Data:      buf.Bytes(),
		Width:     img.Bounds().Dx(),
		Height:    img.Bounds().Dy(),
		Region:    region,
		Scale:     scale,
		Format:    format,
		CaptureMS: captureMS,
		ScaleMS:   scaleMS,
		EncodeMS:  time.Since(t2).Milliseconds(),
	}, nil
}

// ScreenPoint converts a point of the image into a point of the desktop,
// as an agent reads a screenshot to click on it: screen = region +
// image/scale.
func (s *Shot) ScreenPoint(x, y int) win32.Point {
	return win32.Point{
		X: s.Region.X + int(math.Round(float64(x)/s.Scale)),
		Y: s.Region.Y + int(math.Round(float64(y)/s.Scale)),
	}
}

// DisplayInfo returns the virtual desktop and the displays in it.
func DisplayInfo() (win32.Rect, []win32.Display) { return win32.Virtual() }

// Downscale shrinks an image with a box filter, averaging the pixels each
// one of the result covers: text stays readable where a nearest-neighbour
// scale would drop strokes.
func Downscale(src *image.RGBA, factor float64) *image.RGBA {
	if factor >= 1 {
		return src
	}
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dw := max(1, int(math.Round(float64(sw)*factor)))
	dh := max(1, int(math.Round(float64(sh)*factor)))
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	src32 := unsafe.Slice((*uint32)(unsafe.Pointer(&src.Pix[0])), sw*sh)
	for y := 0; y < dh; y++ {
		y0 := y * sh / dh
		y1 := max((y+1)*sh/dh, y0+1)
		for x := 0; x < dw; x++ {
			x0 := x * sw / dw
			x1 := max((x+1)*sw/dw, x0+1)
			var r, g, b uint32
			var n uint32
			for sy := y0; sy < y1; sy++ {
				row := src32[sy*sw:]
				for sx := x0; sx < x1; sx++ {
					v := row[sx]
					r += v & 0xFF
					g += (v >> 8) & 0xFF
					b += (v >> 16) & 0xFF
					n++
				}
			}
			if n == 0 {
				continue
			}
			d := dst.Pix[y*dst.Stride+x*4:]
			d[0] = byte(r / n)
			d[1] = byte(g / n)
			d[2] = byte(b / n)
			d[3] = 0xFF
		}
	}
	return dst
}

// DrawGrid draws lines every step desktop pixels, with their desktop
// coordinates at the edges: the agent reads the numbers rather than
// counting pixels.
func DrawGrid(img *image.RGBA, region win32.Rect, step int, scale float64) {
	if step <= 0 {
		return
	}
	imgScale := max(1, int(math.Round(4*scale)))
	line := func(x, y, w, h int) {
		drawRect(img, x, y, w, h, 0x30, 0x30, 0x30, 0x66)
	}
	first := func(lo int) int {
		return int(math.Ceil(float64(lo)/float64(step))) * step
	}
	for sx := first(region.X); sx < region.Right(); sx += step {
		ix := int(math.Round(float64(sx-region.X) * scale))
		line(ix, 0, imgScale, img.Bounds().Dy())
		drawLabel(img, ix+2, 2, fmt.Sprint(sx), scale)
	}
	for sy := first(region.Y); sy < region.Bottom(); sy += step {
		iy := int(math.Round(float64(sy-region.Y) * scale))
		line(0, iy, img.Bounds().Dx(), imgScale)
		drawLabel(img, 2, iy+2, fmt.Sprint(sy), scale)
	}
}

func drawCursor(img *image.RGBA, region win32.Rect, scale float64, p win32.Point) {
	ix := int(math.Round(float64(p.X-region.X) * scale))
	iy := int(math.Round(float64(p.Y-region.Y) * scale))
	arm := 12
	drawRect(img, ix-arm, iy, 2*arm, 1, 0x00, 0x00, 0x00, 0xAA)
	drawRect(img, ix, iy-arm, 1, 2*arm, 0x00, 0x00, 0x00, 0xAA)
	drawRect(img, ix-arm, iy+1, 2*arm, 1, 0xFF, 0xFF, 0xFF, 0xCC)
	drawRect(img, ix+1, iy-arm, 1, 2*arm, 0xFF, 0xFF, 0xFF, 0xCC)
	label := fmt.Sprintf("%d,%d", p.X, p.Y)
	drawLabel(img, ix+8, iy+10, label, scale)
}

func drawRect(img *image.RGBA, x, y, w, h int, r, g, b, a byte) {
	bounds := img.Bounds()
	for yy := max(0, y); yy < min(bounds.Dy(), y+h); yy++ {
		row := img.Pix[yy*img.Stride:]
		for xx := max(0, x); xx < min(bounds.Dx(), x+w); xx++ {
			p := row[xx*4:]
			alpha := float64(a) / 255
			p[0] = byte(float64(p[0])*(1-alpha) + float64(r)*alpha)
			p[1] = byte(float64(p[1])*(1-alpha) + float64(g)*alpha)
			p[2] = byte(float64(p[2])*(1-alpha) + float64(b)*alpha)
			p[3] = 0xFF
		}
	}
}

// drawLabel draws digits with a dark halo, legible on any background.
func drawLabel(img *image.RGBA, x, y int, text string, scale float64) {
	px := max(2, int(math.Round(3*scale)))
	drawText(img, x, y, text, px, 0, 0, 0, 0x99, 1)
	drawText(img, x, y, text, px, 0xFF, 0xFF, 0xFF, 0xFF, 0)
}

// A 3x5 font, enough for the digits and the signs of coordinates.
var glyphs = map[rune][5]uint8{
	'0': {0b111, 0b101, 0b101, 0b101, 0b111},
	'1': {0b010, 0b110, 0b010, 0b010, 0b111},
	'2': {0b111, 0b001, 0b111, 0b100, 0b111},
	'3': {0b111, 0b001, 0b111, 0b001, 0b111},
	'4': {0b101, 0b101, 0b111, 0b001, 0b001},
	'5': {0b111, 0b100, 0b111, 0b001, 0b111},
	'6': {0b111, 0b100, 0b111, 0b101, 0b111},
	'7': {0b111, 0b001, 0b001, 0b001, 0b001},
	'8': {0b111, 0b101, 0b111, 0b101, 0b111},
	'9': {0b111, 0b101, 0b111, 0b001, 0b111},
	'-': {0b000, 0b000, 0b111, 0b000, 0b000},
	',': {0b000, 0b000, 0b000, 0b010, 0b100},
	'x': {0b000, 0b101, 0b010, 0b101, 0b000},
	' ': {0, 0, 0, 0, 0},
}

func drawText(img *image.RGBA, x, y int, text string, px int, r, g, b, a byte, dx int) {
	cx := x
	for _, ch := range text {
		glyph, ok := glyphs[ch]
		if !ok {
			cx += 4 * px
			continue
		}
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if glyph[row]&(1<<uint(2-col)) == 0 {
					continue
				}
				drawRect(img, cx+col*px+dx, y+row*px+dx, px, px, r, g, b, a)
			}
		}
		cx += 4 * px
	}
}
