package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"browseruse/internal/config"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// ScreenshotOptions configure a screenshot.
type ScreenshotOptions struct {
	Annotate bool
	Inline   bool
	MaxWidth int
	Ref      string
	Selector string
	Format   string // png (default) or jpeg
	Quality  int    // jpeg quality, 1..100
}

// ScreenshotResult is a PNG/JPEG screenshot and its metadata.
type ScreenshotResult struct {
	Path      string         `json:"path,omitempty"`
	Data      string         `json:"data,omitempty"`
	MIMEType  string         `json:"mime_type"`
	Width     int            `json:"width"`
	Height    int            `json:"height"`
	CSSWidth  float64        `json:"css_width"`
	CSSHeight float64        `json:"css_height"`
	DPR       float64        `json:"dpr"`
	Scroll    map[string]any `json:"scroll,omitempty"`
	Clip      *Rect          `json:"clip,omitempty"`
	Annotated bool           `json:"annotated"`
	Nodes     []SnapshotNode `json:"nodes,omitempty"`
	Activated bool           `json:"activated,omitempty"`
	Warning   string         `json:"warning,omitempty"`
}

type screenshotSnapshot struct {
	Nodes    []SnapshotNode `json:"nodes"`
	Viewport struct {
		W   float64 `json:"w"`
		H   float64 `json:"h"`
		DPR float64 `json:"dpr"`
	} `json:"viewport"`
	Scroll map[string]any `json:"scroll"`
}

// Screenshot captures the active page, optionally annotating refs.
func (m *Manager) Screenshot(ctx context.Context, tabID, client string, o ScreenshotOptions) (*ScreenshotResult, error) {
	t := m.Tab(tabID)
	if t == nil {
		if tabID == "" {
			t = m.Active()
		}
		if t == nil {
			return nil, &Error{Code: CodeNoTab, Message: "no active tab"}
		}
	}
	activated := false
	if m.ActiveID() != t.ID {
		if err := m.Activate(t.ID); err != nil {
			return nil, err
		}
		activated = true
		time.Sleep(150 * time.Millisecond)
	}
	t2, done, err := m.begin(ctx, t.ID, client, "screenshot", false)
	if err != nil {
		return nil, err
	}
	defer done()
	t = t2

	var clipCSS *Rect
	if o.Ref != "" || o.Selector != "" {
		target := Target{Ref: o.Ref, Selector: o.Selector}
		if err := target.Validate(); err != nil {
			return nil, err
		}
		if _, err := t.invoke(ctx, "scroll", map[string]any{"to": target.toJS()}); err != nil {
			return nil, err
		}
		raw, err := t.invoke(ctx, "query", map[string]any{"target": target.toJS(), "state": "visible"})
		if err != nil {
			return nil, err
		}
		var q struct {
			Info *SnapshotNode `json:"info"`
		}
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, &Error{Code: CodeInternal, Message: "bad query result: " + err.Error()}
		}
		if q.Info == nil || q.Info.Rect.W <= 0 || q.Info.Rect.H <= 0 {
			return nil, &Error{Code: CodeNotFound, Message: "screenshot target is not visible", Hint: "scroll it into view or wait for it to render"}
		}
		r := q.Info.Rect
		clipCSS = &r
	}

	raw, err := t.invoke(ctx, "snapshot", map[string]any{"max_chars": 50000})
	if err != nil {
		return nil, err
	}
	var snap screenshotSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	pngBytes, err := t.View.CapturePage()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "capture: " + err.Error(), Hint: "is the window visible?"}
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decode screenshot: " + err.Error()}
	}

	maxW := o.MaxWidth
	if maxW <= 0 {
		maxW = 1600
	}
	rgba := downscale(img, maxW)
	if o.Annotate {
		annotate(rgba, snap.Nodes, snap.Viewport.W, snap.Viewport.H)
	}

	var clip *Rect
	warning := ""
	if clipCSS != nil {
		if snap.Viewport.W <= 0 || snap.Viewport.H <= 0 {
			return nil, &Error{Code: CodeInternal, Message: "page has no viewport"}
		}
		sx := float64(rgba.Bounds().Dx()) / snap.Viewport.W
		sy := float64(rgba.Bounds().Dy()) / snap.Viewport.H
		x0 := int(math.Floor(clipCSS.X * sx))
		y0 := int(math.Floor(clipCSS.Y * sy))
		x1 := int(math.Ceil((clipCSS.X + clipCSS.W) * sx))
		y1 := int(math.Ceil((clipCSS.Y + clipCSS.H) * sy))
		rect := image.Rect(x0, y0, x1, y1).Intersect(rgba.Bounds())
		if rect.Empty() {
			return nil, &Error{Code: CodeNotVisible, Message: "screenshot target is outside the viewport", Hint: "scroll it into view first"}
		}
		cropped := cropRGBA(rgba, rect)
		rgba = cropped
		clip = &Rect{
			X: float64(rect.Min.X) / sx,
			Y: float64(rect.Min.Y) / sy,
			W: float64(rect.Dx()) / sx,
			H: float64(rect.Dy()) / sy,
		}
		if rect.Dx() < x1-x0 || rect.Dy() < y1-y0 {
			warning = "element screenshot clipped to the viewport"
		}
	}

	format := strings.ToLower(strings.TrimSpace(o.Format))
	if format == "jpg" {
		format = "jpeg"
	}
	if format == "" {
		format = "png"
	}
	ext, mimeType := ".png", "image/png"
	if format == "jpeg" {
		ext, mimeType = ".jpg", "image/jpeg"
	} else if format != "png" {
		return nil, &Error{Code: CodeBadRequest, Message: "format must be png or jpeg"}
	}

	dir, derr := config.Dir()
	if derr != nil {
		return nil, &Error{Code: CodeInternal, Message: derr.Error()}
	}
	dir = filepath.Join(dir, "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	name := time.Now().Format("20060102-150405.000") + "-" + t.ID + ext
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	if format == "jpeg" {
		quality := o.Quality
		if quality <= 0 {
			quality = 82
		}
		if quality > 100 {
			quality = 100
		}
		err = jpeg.Encode(f, rgba, &jpeg.Options{Quality: quality})
	} else {
		err = png.Encode(f, rgba)
	}
	if err != nil {
		_ = f.Close()
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	_ = f.Close()

	cssW, cssH := snap.Viewport.W, snap.Viewport.H
	if clip != nil {
		cssW, cssH = clip.W, clip.H
	}
	res := &ScreenshotResult{
		Path: path, MIMEType: mimeType,
		Width: rgba.Bounds().Dx(), Height: rgba.Bounds().Dy(),
		CSSWidth: cssW, CSSHeight: cssH, DPR: snap.Viewport.DPR,
		Scroll: snap.Scroll, Clip: clip, Annotated: o.Annotate, Activated: activated,
	}
	if o.Annotate {
		res.Nodes = snap.Nodes
	}
	if o.Inline {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: err.Error()}
		}
		res.Data = base64.StdEncoding.EncodeToString(b)
	}
	if activated {
		if warning != "" {
			warning += "; "
		}
		warning += "activated the tab for capture"
	}
	res.Warning = warning
	return res, nil
}

func cropRGBA(img *image.RGBA, rect image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), img, rect.Min, draw.Src)
	return dst
}

func downscale(img image.Image, maxW int) *image.RGBA {
	b := img.Bounds()
	if b.Dx() <= maxW {
		return toRGBA(img)
	}
	h := int(float64(b.Dy()) * float64(maxW) / float64(b.Dx()))
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	return dst
}

func toRGBA(img image.Image) *image.RGBA {
	b := img.Bounds()
	if r, ok := img.(*image.RGBA); ok && b.Min == (image.Point{}) {
		return r
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

var accent = color.RGBA{R: 37, G: 99, B: 235, A: 255}

func annotate(img *image.RGBA, nodes []SnapshotNode, cssW, cssH float64) {
	if cssW <= 0 || cssH <= 0 {
		return
	}
	sx := float64(img.Bounds().Dx()) / cssW
	sy := float64(img.Bounds().Dy()) / cssH
	face := basicfont.Face7x13
	n := 0
	for _, node := range nodes {
		if n >= 80 {
			break
		}
		r := node.Rect
		if r.W <= 0 || r.H <= 0 {
			continue
		}
		if r.X+r.W < 0 || r.Y+r.H < 0 || r.X > cssW || r.Y > cssH {
			continue
		}
		x0 := int(r.X * sx)
		y0 := int(r.Y * sy)
		x1 := int((r.X + r.W) * sx)
		y1 := int((r.Y + r.H) * sy)
		borderRect(img, x0, y0, x1, y1, 2, accent)
		label := node.Ref
		w := font.MeasureString(face, label).Ceil()
		bw, bh := w+8, 16
		bx, by := x0, y0-bh
		if by < 0 {
			by = y0
		}
		if bx+bw > img.Bounds().Dx() {
			bx = img.Bounds().Dx() - bw
		}
		if by+bh > img.Bounds().Dy() {
			by = img.Bounds().Dy() - bh
		}
		if bx < 0 || by < 0 {
			continue
		}
		fillRect(img, bx, by, bw, bh, color.RGBA{R: 15, G: 23, B: 42, A: 230})
		d := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.White),
			Face: face,
			Dot:  fixed.P(bx+4, by+12),
		}
		d.DrawString(label)
		n++
	}
}

func borderRect(img *image.RGBA, x0, y0, x1, y1, width int, c color.Color) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	fillRect(img, x0, y0, x1-x0, width, c)
	fillRect(img, x0, y1-width, x1-x0, width, c)
	fillRect(img, x0, y0, width, y1-y0, c)
	fillRect(img, x1-width, y0, width, y1-y0, c)
}

func fillRect(img *image.RGBA, x, y, w, h int, c color.Color) {
	if w <= 0 || h <= 0 {
		return
	}
	rect := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	if rect.Empty() {
		return
	}
	draw.Draw(img, rect, image.NewUniform(c), image.Point{}, draw.Src)
}
