package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
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
	FullPage bool
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
	FullPage  bool           `json:"full_page,omitempty"`
	Tiles     int            `json:"tiles,omitempty"`
	FixedHid  int            `json:"fixed_hidden,omitempty"`
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

	if o.FullPage && (o.Ref != "" || o.Selector != "") {
		return nil, &Error{Code: CodeBadRequest, Message: "full_page cannot be combined with ref/selector", Hint: "use full_page for the whole page, or ref/selector for an element"}
	}
	var origScroll map[string]any
	if o.FullPage {
		// 回到顶部再截图:快照 rect 才是页面绝对坐标,分段也从 0 开始。
		if raw, err := t.invoke(ctx, "info", map[string]any{}); err == nil {
			var info struct {
				Scroll map[string]any `json:"scroll"`
			}
			if json.Unmarshal(raw, &info) == nil {
				origScroll = info.Scroll
			}
		}
		if _, err := t.invoke(ctx, "scroll", map[string]any{"xy": map[string]any{"x": 0, "y": 0}}); err != nil {
			return nil, err
		}
		time.Sleep(150 * time.Millisecond)
	}

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

	// ---- 捕获:整页 = 分段滚动 + 拼接;单屏 = 一次 CapturePage ----
	var pngBytes []byte
	var preRGBA *image.RGBA // 整页时已有像素,跳过解码
	var pageW, pageH float64
	var tiles, fixedHid int
	if o.FullPage {
		preRGBA, pngBytes, pageW, pageH, tiles, fixedHid, err = m.captureFullPage(ctx, t, origScroll)
		if err != nil {
			return nil, err
		}
	} else {
		pngBytes, err = t.View.CapturePage()
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "capture: " + err.Error(), Hint: "is the window visible?"}
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

	maxW := o.MaxWidth
	if maxW <= 0 {
		maxW = 1600
	}

	dir, derr := config.Dir()
	if derr != nil {
		return nil, &Error{Code: CodeInternal, Message: derr.Error()}
	}
	dir = filepath.Join(dir, "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}

	// 快路径:输出 PNG、无标注、无裁剪且无需缩放 —— 捕获字节(整页时是拼接
	// 结果)直接落盘,零解码/重编码(旧实现全帧 decode→toRGBA→encode 三个步骤)。
	// 插件默认开标注不走这里;程序化截图(annotate:false)是最大受益方。
	if format == "png" && !o.Annotate && clipCSS == nil {
		cfgImg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "decode screenshot: " + err.Error()}
		}
		if cfgImg.Width <= maxW {
			name := time.Now().Format("20060102-150405.000") + "-" + t.ID + ext
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
				return nil, &Error{Code: CodeInternal, Message: err.Error()}
			}
			res := &ScreenshotResult{
				Path: path, MIMEType: mimeType,
				Width: cfgImg.Width, Height: cfgImg.Height,
				CSSWidth: snap.Viewport.W, CSSHeight: snap.Viewport.H,
				DPR: snap.Viewport.DPR, Scroll: snap.Scroll, Activated: activated,
			}
			if o.FullPage {
				// 拼接结果的 CSS 尺寸是整页,不是视口。
				res.CSSWidth, res.CSSHeight = pageW, pageH
				res.FullPage = true
				res.Tiles = tiles
				res.FixedHid = fixedHid
			}
			if activated {
				res.Warning = "activated the tab for capture"
			}
			return withInlineData(res, o, pngBytes), nil
		}
	}

	img, err := func() (image.Image, error) {
		if preRGBA != nil {
			return preRGBA, nil // 整页拼接结果,不必再解码
		}
		return png.Decode(bytes.NewReader(pngBytes))
	}()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "decode screenshot: " + err.Error()}
	}
	// 只有改像素(标注/裁剪)或缩放才需要可变 RGBA;纯转码直接编码原图,
	// 省一次全帧拷贝。
	var rgba *image.RGBA
	if img.Bounds().Dx() > maxW {
		rgba = downscale(img, maxW)
	} else if o.Annotate || clipCSS != nil {
		rgba = toRGBA(img)
	}
	if rgba != nil && o.Annotate {
		// 整页时快照 rect 是页面绝对坐标(scroll=0),按整页 CSS 尺寸缩放。
		aw, ah := snap.Viewport.W, snap.Viewport.H
		if o.FullPage {
			aw, ah = pageW, pageH
		}
		annotate(rgba, snap.Nodes, aw, ah)
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

	// 单次编码双写:文件与(可选的)inline 缓冲一次生成,不再编码后回读文件。
	var target image.Image = img
	if rgba != nil {
		target = rgba
	}
	name := time.Now().Format("20060102-150405.000") + "-" + t.ID + ext
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	var buf bytes.Buffer
	sink := io.Writer(f)
	if o.Inline {
		sink = io.MultiWriter(f, &buf)
	}
	if format == "jpeg" {
		quality := o.Quality
		if quality <= 0 {
			quality = 82
		}
		if quality > 100 {
			quality = 100
		}
		err = jpeg.Encode(sink, target, &jpeg.Options{Quality: quality})
	} else {
		err = png.Encode(sink, target)
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
	if o.FullPage {
		cssW, cssH = pageW, pageH
	}
	res := &ScreenshotResult{
		Path: path, MIMEType: mimeType,
		Width: target.Bounds().Dx(), Height: target.Bounds().Dy(),
		CSSWidth: cssW, CSSHeight: cssH, DPR: snap.Viewport.DPR,
		Scroll: snap.Scroll, Clip: clip, Annotated: o.Annotate, Activated: activated,
		FullPage: o.FullPage, Tiles: tiles, FixedHid: fixedHid,
	}
	if o.Annotate {
		res.Nodes = snap.Nodes
	}
	if activated {
		if warning != "" {
			warning += "; "
		}
		warning += "activated the tab for capture"
	}
	if o.FullPage {
		extra := fmt.Sprintf("full-page capture in %d tiles", tiles)
		if tiles >= maxFullPageTiles {
			extra += " (page truncated at the tile limit)"
		}
		if fixedHid > 0 {
			extra += fmt.Sprintf("; %d fixed/sticky elements hidden after the first tile", fixedHid)
		}
		if warning != "" {
			warning += "; "
		}
		warning += extra
	}
	res.Warning = warning
	return withInlineData(res, o, buf.Bytes()), nil
}

// captureFullPage scrolls the page viewport by viewport and stitches the tiles
// into one image. fixed/sticky elements stay in the first tile only (they would
// otherwise repeat on every tile); the page returns to its original scroll
// position afterwards.
func (m *Manager) captureFullPage(ctx context.Context, t *Tab, origScroll map[string]any) (*image.RGBA, []byte, float64, float64, int, int, error) {
	raw, err := t.invoke(ctx, "fullPage", map[string]any{"action": "prepare"})
	if err != nil {
		return nil, nil, 0, 0, 0, 0, err
	}
	var meta struct {
		PageW     int     `json:"page_w"`
		PageH     int     `json:"page_h"`
		ViewportH int     `json:"viewport_h"`
		DPR       float64 `json:"dpr"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ViewportH <= 0 || meta.PageH <= 0 {
		return nil, nil, 0, 0, 0, 0, &Error{Code: CodeInternal, Message: "bad fullPage metrics"}
	}

	scrollTo := func(cssY int) error {
		if _, err := t.invoke(ctx, "scroll", map[string]any{"xy": map[string]any{"x": 0, "y": cssY}}); err != nil {
			return err
		}
		time.Sleep(120 * time.Millisecond)
		return nil
	}
	scrollY := func() float64 {
		raw, err := t.invoke(ctx, "info", map[string]any{})
		if err != nil {
			return 0
		}
		var info struct {
			Scroll struct {
				Y float64 `json:"y"`
			} `json:"scroll"`
		}
		if json.Unmarshal(raw, &info) != nil {
			return 0
		}
		return info.Scroll.Y
	}

	need := (meta.PageH + meta.ViewportH - 1) / meta.ViewportH
	if need > maxFullPageTiles {
		need = maxFullPageTiles
	}
	var tiles []fullPageTile
	fixedHid := 0
	for i := 0; i < need; i++ {
		if i > 0 {
			if err := scrollTo(i * meta.ViewportH); err != nil {
				return nil, nil, 0, 0, 0, 0, err
			}
		}
		if i == 1 {
			// 第一段已包含 fixed/sticky 的自然位置;后续分段把它们藏起来,
			// 避免同一条头部在每一段里重复出现。
			raw, err := t.invoke(ctx, "fullPage", map[string]any{"action": "hide"})
			if err == nil {
				var out struct {
					Hidden int `json:"hidden"`
				}
				if json.Unmarshal(raw, &out) == nil {
					fixedHid = out.Hidden
				}
			}
		}
		tilePNG, err := t.View.CapturePage()
		if err != nil {
			return nil, nil, 0, 0, 0, 0, &Error{Code: CodeInternal, Message: "capture: " + err.Error(), Hint: "is the window visible?"}
		}
		img, err := png.Decode(bytes.NewReader(tilePNG))
		if err != nil {
			return nil, nil, 0, 0, 0, 0, &Error{Code: CodeInternal, Message: "decode tile: " + err.Error()}
		}
		tiles = append(tiles, fullPageTile{img: img, yPix: int(math.Round(scrollY() * meta.DPR))})
	}
	// 恢复 fixed/sticky 与原滚动位置。
	_, _ = t.invoke(ctx, "fullPage", map[string]any{"action": "restore"})
	if origScroll != nil {
		if _, err := t.invoke(ctx, "scroll", map[string]any{"xy": origScroll}); err != nil {
			_ = err
		}
	}

	stitched := stitchTiles(tiles)
	if stitched == nil {
		return nil, nil, 0, 0, 0, 0, &Error{Code: CodeInternal, Message: "no full-page tiles captured"}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, stitched); err != nil {
		return nil, nil, 0, 0, 0, 0, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return stitched, buf.Bytes(), float64(meta.PageW), float64(meta.PageH), len(tiles), fixedHid, nil
}

// withInlineData fills res.Data for inline results. 调用方传的是编码缓冲或
// 捕获字节,不再回读刚写完的文件。
func withInlineData(res *ScreenshotResult, o ScreenshotOptions, data []byte) *ScreenshotResult {
	if o.Inline {
		res.Data = base64.StdEncoding.EncodeToString(data)
	}
	return res
}

func cropRGBA(img *image.RGBA, rect image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), img, rect.Min, draw.Src)
	return dst
}

// maxFullPageTiles bounds the stitch: an endless-scroller page would otherwise
// grow the capture without limit.
const maxFullPageTiles = 20

// fullPageTile is one viewport capture with its CSS scroll offset.
type fullPageTile struct {
	img  image.Image
	yPix int // device-pixel offset of the tile top in the stitched page
}

// stitchTiles composes viewport tiles into one page image, each placed at its
// device-pixel offset. Overlapping tiles (scroll clamped at the page bottom)
// overdraw identical content, so pasting in order is safe.
func stitchTiles(tiles []fullPageTile) *image.RGBA {
	if len(tiles) == 0 {
		return nil
	}
	w, h := 0, 0
	for _, t := range tiles {
		b := t.img.Bounds()
		if b.Dx() > w {
			w = b.Dx()
		}
		if t.yPix+b.Dy() > h {
			h = t.yPix + b.Dy()
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for _, t := range tiles {
		draw.Draw(out, image.Rect(0, t.yPix, t.img.Bounds().Dx(), t.yPix+t.img.Bounds().Dy()), t.img, t.img.Bounds().Min, draw.Src)
	}
	return out
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
