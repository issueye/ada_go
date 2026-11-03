//go:build windows

package mask

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"computeruse/internal/win32"
)

// The overlay's palette: one accent the annotations share, and tones that
// tell a click from a warning.
var (
	accent   = ui.RGB(59, 130, 246)
	green    = ui.RGB(34, 197, 94)
	amber    = ui.RGB(245, 158, 11)
	red      = ui.RGB(239, 68, 68)
	white    = ui.RGB(255, 255, 255)
	muted    = ui.RGBA(203, 213, 225, 0.92)
	chipBG   = ui.RGBA(9, 13, 22, 0.92)
	chipEdge = ui.RGBA(255, 255, 255, 0.16)
	hudBG    = ui.RGBA(9, 13, 22, 0.90)
	dimColor = ui.RGBA(3, 6, 12, 1)
)

// coordinateFont is a monospaced face where the system has one, so that the
// numbers of coordinates line up.
const coordinateFont = "Consolas, Cascadia Mono, Menlo, monospace"

func toneColor(name string) ui.Color {
	switch tone(name) {
	case "green":
		return green
	case "amber":
		return amber
	case "red":
		return red
	case "white":
		return white
	default:
		return accent
	}
}

// draw is the view of one display's overlay window.
func (w *window) draw(c *ui.Context) {
	c.Root().Background(ui.Transparent)
	st := w.m.Snapshot()
	w.drawn.Store(int64(st.Revision))
	if !st.Visible || w.m.Disabled() {
		return
	}
	ui.Box(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) {
		w.paint(p, r, st)
	})
}

// dipPoint converts a desktop point into the DIPs of this display's window.
func (w *window) dipPoint(pt win32.Point) (float32, float32) {
	s := w.display.Scale
	return float32(float64(pt.X-w.display.Bounds.X) / s), float32(float64(pt.Y-w.display.Bounds.Y) / s)
}

// dipRect converts a desktop rectangle into the DIPs of this window.
func (w *window) dipRect(r win32.Rect) ui.Rect {
	x, y := w.dipPoint(win32.Point{X: r.X, Y: r.Y})
	return ui.Rect{X: x, Y: y, W: float32(float64(r.W) / w.display.Scale), H: float32(float64(r.H) / w.display.Scale)}
}

// paint draws one frame of the overlay: the mask, then what it points at.
func (w *window) paint(p *ui.Painter, r ui.Rect, st State) {
	w.paintDim(p, r, st)
	if st.Grid > 0 {
		w.paintGrid(p, r, st.Grid)
	}
	for _, reg := range st.Regions {
		w.paintRegion(p, r, reg, st)
	}
	for _, chip := range st.Chips {
		w.paintChip(p, r, chip)
	}
	w.paintTrail(p, st)
	for _, mk := range st.Markers {
		w.paintMarker(p, r, mk)
	}
	w.paintRipples(p, st)
	if st.Coords && st.CursorOK && st.CursorDisplay == w.display.Name {
		w.paintCursor(p, r, st.Cursor)
	}
	if w.display.Primary {
		w.paintHUD(p, r, st)
	}
}

// holes are the areas the mask leaves at full brightness: the regions it
// outlines, and the points it marks, so that what the agent acts on shows.
func (w *window) holes(st State, r ui.Rect) []ui.Rect {
	var out []ui.Rect
	for _, reg := range st.Regions {
		if d := w.dipRect(reg.Rect); intersects(d, r) {
			out = append(out, d)
		}
	}
	for _, mk := range st.Markers {
		x, y := w.dipPoint(mk.At)
		pad := float32(22)
		d := ui.Rect{X: x - pad, Y: y - pad, W: 2 * pad, H: 2 * pad}
		if intersects(d, r) {
			out = append(out, d)
		}
	}
	return out
}

// paintDim darkens the desktop around the holes: the mask of the overlay.
func (w *window) paintDim(p *ui.Painter, r ui.Rect, st State) {
	dim := st.Dim
	if dim <= 0 {
		return
	}
	color := dimColor.Alpha(dim)
	pieces := subtract(r, w.holes(st, r))
	for _, piece := range pieces {
		p.Fill(piece, color, 0)
	}
}

// subtract returns the parts of area that none of holes covers.
func subtract(area ui.Rect, holes []ui.Rect) []ui.Rect {
	pieces := []ui.Rect{area}
	for _, h := range holes {
		var next []ui.Rect
		for _, piece := range pieces {
			if !intersects(piece, h) {
				next = append(next, piece)
				continue
			}
			top := max(h.Y, piece.Y)
			bottom := min(h.Y+h.H, piece.Y+piece.H)
			left := max(h.X, piece.X)
			right := min(h.X+h.W, piece.X+piece.W)
			if top > piece.Y {
				next = append(next, ui.Rect{X: piece.X, Y: piece.Y, W: piece.W, H: top - piece.Y})
			}
			if bottom < piece.Y+piece.H {
				next = append(next, ui.Rect{X: piece.X, Y: bottom, W: piece.W, H: piece.Y + piece.H - bottom})
			}
			if left > piece.X {
				next = append(next, ui.Rect{X: piece.X, Y: top, W: left - piece.X, H: bottom - top})
			}
			if right < piece.X+piece.W {
				next = append(next, ui.Rect{X: right, Y: top, W: piece.X + piece.W - right, H: bottom - top})
			}
		}
		pieces = next
	}
	return pieces
}

func intersects(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// paintRegion outlines a region, brackets its corners and writes its
// coordinates beside it.
func (w *window) paintRegion(p *ui.Painter, r ui.Rect, reg Region, st State) {
	d := w.dipRect(reg.Rect)
	if !intersects(d, r) {
		return
	}
	c := toneColor(reg.Tone)
	// A soft glow lifts the outline off a busy desktop.
	p.Shadow(d, 8, 0, 0, 18, 3, c.Alpha(0.35))
	if reg.Dashed {
		p.StrokeDashed(d, c, 8, 2)
	} else {
		p.Stroke(d, c.Alpha(0.95), 8, 2)
	}
	bracket := float32(16)
	thick := float32(3)
	tone := c
	// Corner brackets, as a target in a camera's viewfinder.
	corners := []struct {
		x, y   float32
		sx, sy float32
	}{
		{d.X, d.Y, 1, 1},
		{d.X + d.W, d.Y, -1, 1},
		{d.X, d.Y + d.H, 1, -1},
		{d.X + d.W, d.Y + d.H, -1, -1},
	}
	for _, cn := range corners {
		p.Line(cn.x, cn.y, cn.x+cn.sx*bracket, cn.y, thick, tone)
		p.Line(cn.x, cn.y, cn.x, cn.y+cn.sy*bracket, thick, tone)
	}
	// The label: what the element is, the coordinates of its top-left
	// corner, and its size.
	spans := []ui.Span{}
	if reg.Index > 0 {
		spans = append(spans, ui.Span{Text: fmt.Sprintf("%d ", reg.Index), Weight: 700, Color: tone, Size: 11})
	}
	if reg.Label != "" {
		spans = append(spans, ui.Span{Text: reg.Label + "  ", Weight: 600, Color: white, Size: 12})
	}
	spans = append(spans, ui.Span{Text: coordText(reg.Rect), Font: coordinateFont, Size: 11, Color: muted})
	w.pillAt(p, r, d.X, d.Y-6, spans, tone)
}

// paintMarker points at a place: a crosshair, a ring that breathes, and
// the coordinates under it.
func (w *window) paintMarker(p *ui.Painter, r ui.Rect, mk Marker) {
	x, y := w.dipPoint(mk.At)
	if !r.Contains(x, y) {
		return
	}
	// A marker flies in from its lower right, which draws the eye.
	if mk.Arrive {
		t := float32(0)
		if !mk.started.IsZero() {
			t = clamp01(float32(time.Since(mk.started)) / float32(markerArrive))
		}
		e := easeOut(t)
		x += (1 - e) * 26
		y += (1 - e) * 20
		p.AnimationFrame()
	}
	c := toneColor(mk.Tone)
	// A breathing ring.
	phase := float32(p.Now().UnixMilli()%1400) / 1400
	ring := float32(11) + 4*float32(math.Sin(float64(phase)*2*math.Pi))
	p.Stroke(ui.Rect{X: x - ring, Y: y - ring, W: 2 * ring, H: 2 * ring}, c.Alpha(0.75), ring, 2)
	p.Fill(ui.Rect{X: x - 3, Y: y - 3, W: 6, H: 6}, c, 3)
	// A crosshair, dark under light so it reads on any background.
	arm := float32(15)
	for i, col := range []ui.Color{ui.RGBA(0, 0, 0, 0.55), ui.RGBA(255, 255, 255, 0.9)} {
		off := float32(i)
		p.Line(x-arm-off, y-off, x+arm+off, y-off, 1.5, col)
		p.Line(x+off, y-arm-off, x+off, y+arm+off, 1.5, col)
	}
	spans := []ui.Span{}
	if mk.Label != "" {
		spans = append(spans, ui.Span{Text: mk.Label + "  ", Weight: 600, Color: white, Size: 12})
	}
	spans = append(spans, ui.Span{Text: pointText(mk.At), Font: coordinateFont, Size: 11, Color: c})
	w.pillAt(p, r, x+18, y+14, spans, c)
	p.AnimationFrame()
}

// paintChip writes a line at a point, with that point's coordinates.
func (w *window) paintChip(p *ui.Painter, r ui.Rect, chip Chip) {
	x, y := w.dipPoint(chip.At)
	if !r.Contains(x, y) {
		return
	}
	c := toneColor(chip.Tone)
	spans := []ui.Span{{Text: chip.Text + "  ", Weight: 600, Color: white, Size: 12}}
	if len(chip.Text) == 0 || chip.Text != pointText(chip.At) {
		spans = append(spans, ui.Span{Text: pointText(chip.At), Font: coordinateFont, Size: 11, Color: muted})
	}
	w.pillAt(p, r, x, y, spans, c)
}

// paintTrail draws the path of a drag, fading towards its start.
func (w *window) paintTrail(p *ui.Painter, st State) {
	if len(st.Trail) < 2 {
		return
	}
	var path ui.Path
	for i, pt := range st.Trail {
		x, y := w.dipPoint(pt)
		if i == 0 {
			path.MoveTo(x, y)
		} else {
			path.LineTo(x, y)
		}
	}
	if x, y := w.dipPoint(st.Trail[len(st.Trail)-1]); true {
		p.Fill(ui.Rect{X: x - 4, Y: y - 4, W: 8, H: 8}, accent, 4)
	}
	p.StrokePath(&path, 2.5, accent.Alpha(0.55))
}

// paintRipples shows clicks: rings that spread and fade.
func (w *window) paintRipples(p *ui.Painter, st State) {
	for _, rp := range st.Ripples {
		age := time.Since(rp.Start)
		if age < 0 || age >= rippleLife {
			continue
		}
		t := float32(age) / float32(rippleLife)
		x, y := w.dipPoint(rp.At)
		for i := 0; i < 2; i++ {
			tt := clamp01(t - float32(i)*0.18)
			if tt <= 0 {
				continue
			}
			radius := 8 + 40*easeOut(tt)
			var path ui.Path
			path.Circle(x, y, radius)
			p.StrokePath(&path, 2.5-float32(i), toneColor(rp.Tone).Alpha(0.85*(1-tt)))
		}
		p.AnimationFrame()
	}
}

// paintCursor writes the pointer's coordinates beside it, and a crosshair
// on the point: the coordinates of the element under the pointer are what
// the agent needs most.
func (w *window) paintCursor(p *ui.Painter, r ui.Rect, mk Marker) {
	x, y := w.dipPoint(mk.At)
	if !r.Contains(x, y) {
		return
	}
	arm := float32(7)
	for i, col := range []ui.Color{ui.RGBA(0, 0, 0, 0.6), ui.RGBA(255, 255, 255, 0.95)} {
		off := float32(i)
		p.Line(x-arm-off, y-off, x+arm+off, y-off, 1, col)
		p.Line(x+off, y-arm-off, x+off, y+arm+off, 1, col)
	}
	spans := []ui.Span{{Text: pointText(mk.At), Font: coordinateFont, Size: 11, Color: white}}
	// Keep the label on the display.
	cx, cy := x+14, y+12
	w2, h2 := p.MeasureText(0, spans...)
	if cx+w2+18 > r.X+r.W {
		cx = x - w2 - 26
	}
	if cy+h2+14 > r.Y+r.H {
		cy = y - h2 - 22
	}
	w.pillAt(p, r, cx, cy, spans, accent)
}

// paintGrid draws lines every step desktop pixels, with their coordinates
// along the top and left edges: a ruler over the screen, drawn over the dim
// so that it stays crisp.
func (w *window) paintGrid(p *ui.Painter, r ui.Rect, step int) {
	if step <= 0 {
		return
	}
	s := w.display.Scale
	b := w.display.Bounds
	line := ui.RGBA(255, 255, 255, 0.14)
	first := func(lo int) int { return int(math.Ceil(float64(lo)/float64(step))) * step }
	for sx := first(b.X); sx < b.X+b.W; sx += step {
		x := float32(float64(sx-b.X) / s)
		p.Fill(ui.Rect{X: x, Y: 0, W: 1, H: r.H}, line, 0)
		w.gridLabel(p, x+3, 3, fmt.Sprint(sx))
	}
	for sy := first(b.Y); sy < b.Y+b.H; sy += step {
		y := float32(float64(sy-b.Y) / s)
		p.Fill(ui.Rect{X: 0, Y: y, W: r.W, H: 1}, line, 0)
		w.gridLabel(p, 3, y+3, fmt.Sprint(sy))
	}
}

// gridLabel writes a coordinate with a shadow, so that it reads over any
// window behind it.
func (w *window) gridLabel(p *ui.Painter, x, y float32, text string) {
	shadow := []ui.Span{{Text: text, Font: coordinateFont, Size: 9, Color: ui.RGBA(0, 0, 0, 0.55)}}
	p.RichText(x+1, y+1, 0, shadow...)
	spans := []ui.Span{{Text: text, Font: coordinateFont, Size: 9, Color: ui.RGBA(255, 255, 255, 0.85)}}
	p.RichText(x, y, 0, spans...)
}

// paintHUD draws the pill that says what the agent does, and whether it is
// paused, on the primary display.
func (w *window) paintHUD(p *ui.Painter, r ui.Rect, st State) {
	tone := accent
	if st.Paused {
		tone = red
	} else if st.HUD.Tone != "" {
		tone = toneColor(st.HUD.Tone)
	}
	title := st.HUD.Title
	if title == "" {
		title = "Computer Use"
	}
	detail := st.HUD.Detail
	spans := []ui.Span{{Text: title, Weight: 700, Size: 12, Color: white}}
	if detail != "" {
		spans = append(spans,
			ui.Span{Text: "   ", Size: 12, Color: muted},
			ui.Span{Text: detail, Size: 12, Color: ui.RGBA(226, 232, 240, 0.92)},
		)
	}
	tw, th := p.MeasureText(0, spans...)
	padX, padY := float32(13), float32(7)
	dot := float32(8)
	width := padX*2 + dot + 8 + tw
	height := max(th+padY*2, 28)
	x := r.X + (r.W-width)/2
	y := r.Y + r.H - height - 26
	p.Shadow(ui.Rect{X: x, Y: y, W: width, H: height}, height/2, 0, 6, 22, 0, ui.RGBA(0, 0, 0, 0.45))
	p.Fill(ui.Rect{X: x, Y: y, W: width, H: height}, hudBG, height/2)
	p.Stroke(ui.Rect{X: x, Y: y, W: width, H: height}, tone.Alpha(0.45), height/2, 1)
	// The dot: steady while idle, pulsing while a command runs, red when
	// the agent is paused.
	cx, cy := x+padX+dot/2, y+height/2
	if st.Paused {
		p.Fill(ui.Rect{X: cx - dot/2, Y: cy - dot/2, W: dot, H: dot}, red, dot/2)
	} else if st.HUD.Busy {
		phase := float32(p.Now().UnixMilli()%900) / 900
		alpha := 0.35 + 0.65*float32(math.Abs(math.Sin(float64(phase)*math.Pi)))
		p.Fill(ui.Rect{X: cx - dot/2 - 2, Y: cy - dot/2 - 2, W: dot + 4, H: dot + 4}, tone.Alpha(0.22*alpha), dot/2+2)
		p.Fill(ui.Rect{X: cx - dot/2, Y: cy - dot/2, W: dot, H: dot}, tone.Alpha(alpha), dot/2)
		p.AnimationFrame()
	} else {
		p.Fill(ui.Rect{X: cx - dot/2, Y: cy - dot/2, W: dot, H: dot}, tone, dot/2)
	}
	p.RichText(x+padX+dot+8, y+(height-th)/2, 0, spans...)
}

// pillAt draws a label chip whose bottom-left corner sits above (x, y), and
// keeps it inside r. tone tints its border.
func (w *window) pillAt(p *ui.Painter, r ui.Rect, x, y float32, spans []ui.Span, tone ui.Color) {
	tw, th := p.MeasureText(0, spans...)
	padX, padY := float32(8), float32(5)
	width, height := tw+padX*2, th+padY*2
	bx := x
	by := y - height
	if by < r.Y+2 {
		by = y + 10 // above the display: put it inside
	}
	bx = clamp(bx, r.X+2, r.X+r.W-width-2)
	by = clamp(by, r.Y+2, r.Y+r.H-height-2)
	p.Shadow(ui.Rect{X: bx, Y: by, W: width, H: height}, 7, 0, 2, 10, 0, ui.RGBA(0, 0, 0, 0.35))
	p.Fill(ui.Rect{X: bx, Y: by, W: width, H: height}, chipBG, 7)
	p.Stroke(ui.Rect{X: bx, Y: by, W: width, H: height}, chipEdge, 7, 1)
	p.RichText(bx+padX, by+padY, 0, spans...)
}

func clamp(v, lo, hi float32) float32 {
	if hi < lo {
		return lo
	}
	return max(lo, min(v, hi))
}

func clamp01(v float32) float32 { return clamp(v, 0, 1) }

func easeOut(t float32) float32 {
	t = clamp01(t)
	return 1 - (1-t)*(1-t)
}
