//go:build windows

package mask

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"computeruse/internal/win32"
)

// Mask is the overlay: one click-through window per display, drawing the
// state as the agent changes it. Create one with New and Start it once the
// application is ready.
type Mask struct {
	mu      sync.Mutex
	st      State
	windows []*window
	started bool
	stop    chan struct{}
	// protect keeps the overlay out of screenshots, so that an agent
	// captures the desktop rather than its own annotations.
	protect bool
	// disabled turns the whole overlay off (--no-mask): the state still
	// answers, but nothing is drawn.
	disabled bool
}

type window struct {
	m       *Mask
	win     *mygo.Window
	display win32.Display
	dip     mygo.Rectangle
	// drawn is the revision of the state the window has drawn, which a
	// preview waits for: a capture taken earlier returns a stale frame.
	drawn atomic.Int64
}

// New returns an overlay that draws nothing yet.
func New() *Mask {
	return &Mask{stop: make(chan struct{}), protect: true}
}

// SetProtected keeps the overlay out of screenshots (the default) or lets
// a screenshot show it, for debugging.
func (m *Mask) SetProtected(v bool) {
	m.mu.Lock()
	m.protect = v
	wins := append([]*window(nil), m.windows...)
	m.mu.Unlock()
	for _, w := range wins {
		w.win.SetContentProtection(v)
	}
}

// SetDisabled turns the overlay off entirely, as the --no-mask flag does.
func (m *Mask) SetDisabled(v bool) {
	m.mu.Lock()
	m.disabled = v
	m.mu.Unlock()
	m.rebuild()
}

// Disabled reports whether the overlay draws nothing.
func (m *Mask) Disabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disabled
}

// Start creates the overlay windows. Call it on the main thread, typically
// from mygo.App.WhenReady.
func (m *Mask) Start() error {
	if !mygo.App.IsReady() {
		return errors.New("mask: the application is not ready")
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.st = State{Visible: true, Dim: 0, Coords: true, Revision: 1, HUD: HUD{Title: "Computer Use", Detail: "待命 · idle"}}
	m.mu.Unlock()
	m.rebuild()
	go m.loop()
	mygo.Screen.OnDisplaysChanged(func() { m.rebuild() })
	return nil
}

// Stop closes the overlay windows.
func (m *Mask) Stop() {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	m.started = false
	close(m.stop)
	wins := m.windows
	m.windows = nil
	m.mu.Unlock()
	for _, w := range wins {
		w.win.Close()
	}
}

// rebuild creates the windows that match the displays. Main thread only.
func (m *Mask) rebuild() {
	m.mu.Lock()
	if !m.started || m.disabled {
		m.mu.Unlock()
		return
	}
	old := m.windows
	m.windows = nil
	m.mu.Unlock()

	displays := win32.Displays()
	_, dips := dipRectangles(displays)

	var created []*window
	for i, d := range displays {
		dip := dips[i]
		w := &window{m: m, display: d, dip: dip}
		opts := mygo.WindowOptions{
			Title:             "Computer Use mask",
			Width:             dip.Width,
			Height:            dip.Height,
			X:                 dip.X,
			Y:                 dip.Y,
			Frameless:         true,
			Transparent:       true,
			AlwaysOnTop:       true,
			SkipTaskbar:       true,
			DisableResize:     true,
			DisableMinimize:   true,
			DisableMaximize:   true,
			DisableClose:      true,
			DisableFullScreen: true,
			DisableShadow:     true,
			Hidden:            true,
			Content:           ui.View(w.draw),
		}
		win := mygo.NewWindow(opts)
		win.SetBounds(dip)
		win.SetIgnoreMouseEvents(true)
		win.SetContentProtection(m.protected())
		// A tool window stays out of Alt+Tab, and no-activate keeps it from
		// taking the keyboard from the app the agent drives.
		win32.AddWindowExStyle(win.NativeHandle(), win32.WS_EX_TOOLWINDOW|win32.WS_EX_NOACTIVATE)
		win.ShowInactive()
		w.win = win
		created = append(created, w)
	}
	m.mu.Lock()
	m.windows = created
	m.mu.Unlock()
	for _, w := range old {
		w.win.Close()
	}
	m.invalidate()
}

// dipRectangles matches each physical display with the DIP rectangle MyGo
// places a window at. MyGo reports DIPs per display (physical divided by
// the display's scale), which is where a window must go to cover it.
func dipRectangles(displays []win32.Display) ([]string, []mygo.Rectangle) {
	byName := map[string]mygo.Rectangle{}
	for _, d := range mygo.Screen.Displays() {
		byName[d.Label] = d.Bounds
	}
	var names []string
	var out []mygo.Rectangle
	for _, d := range displays {
		if r, ok := byName[d.Name]; ok {
			out = append(out, r)
		} else {
			out = append(out, mygo.Rectangle{X: d.DIP.X, Y: d.DIP.Y, Width: d.DIP.W, Height: d.DIP.H})
		}
		names = append(names, d.Name)
	}
	return names, out
}

func (m *Mask) protected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.protect
}

// Snapshot returns a copy of what the overlay shows.
func (m *Mask) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.clone()
}

// Update changes the state and redraws the overlay. fn runs with the
// overlay's lock held, so it must not call back into the mask.
func (m *Mask) Update(fn func(*State)) {
	m.mu.Lock()
	fn(&m.st)
	m.st.Revision++
	m.st.UpdatedAt = time.Now()
	m.mu.Unlock()
	m.invalidate()
}

// SetVisible shows or hides the whole overlay.
func (m *Mask) SetVisible(v bool) {
	m.Update(func(s *State) { s.Visible = v })
}

// SetPaused marks the overlay as paused, which the HUD shows in red.
func (m *Mask) SetPaused(v bool) {
	m.Update(func(s *State) {
		s.Paused = v
		if v {
			s.Dim = 0
			s.HUD.Detail = "已暂停 · paused"
			s.HUD.Tone = "red"
		} else {
			s.HUD.Detail = "待命 · idle"
			s.HUD.Tone = ""
		}
	})
}

// SetHUD puts a line in the pill, such as what the agent does now.
func (m *Mask) SetHUD(detail string, busy bool) {
	m.Update(func(s *State) {
		s.HUD.Detail = detail
		s.HUD.Busy = busy
		if !s.Paused {
			s.HUD.Tone = ""
		}
	})
}

// Options are the overlay's settings, which outlive each annotation.
type Options struct {
	Dim    float32
	Dark   bool
	Grid   int
	Coords bool
}

// SetOptions changes how the overlay dims and measures the desktop.
func (m *Mask) SetOptions(o Options) {
	m.Update(func(s *State) {
		s.Dim = clampDim(o.Dim)
		s.Dark = o.Dark
		s.Grid = max(o.Grid, 0)
		s.Coords = o.Coords
	})
}

// ShowRegions replaces the outlined regions.
func (m *Mask) ShowRegions(regions []Region) {
	m.Update(func(s *State) {
		s.Regions = regions
		if len(regions) > 0 {
			s.Dark = true
			if s.Dim == 0 {
				s.Dim = defaultDim
			}
		}
	})
}

// AddRegions adds outlined regions to the ones shown.
func (m *Mask) AddRegions(regions ...Region) {
	m.Update(func(s *State) {
		s.Regions = append(s.Regions, regions...)
		if len(regions) > 0 {
			s.Dark = true
			if s.Dim == 0 {
				s.Dim = defaultDim
			}
		}
	})
}

// Mark points at a place with a crosshair and its coordinates.
func (m *Mask) Mark(mk Marker) {
	mk.started = time.Now()
	mk.Arrive = true
	m.Update(func(s *State) {
		s.Markers = append(s.Markers, mk)
		if s.Dim == 0 {
			s.Dim = defaultDim
			s.Dark = true
		}
	})
}

// ClearMarkers removes the crosshairs.
func (m *Mask) ClearMarkers() {
	m.Update(func(s *State) { s.Markers = nil })
}

// ChipAt writes a line of text at a point, with that point's coordinates.
func (m *Mask) ChipAt(c Chip) {
	m.Update(func(s *State) { s.Chips = append(s.Chips, c) })
}

// Ripple shows a click at p.
func (m *Mask) Ripple(p win32.Point, tone string) {
	m.Update(func(s *State) {
		s.Ripples = append(s.Ripples, Ripple{At: p, Start: time.Now(), Tone: tone})
	})
}

// SetTrail keeps a path, as the path of a drag.
func (m *Mask) SetTrail(points []win32.Point) {
	m.Update(func(s *State) { s.Trail = points })
}

// Clear removes every annotation and stops dimming.
func (m *Mask) Clear() {
	m.Update(func(s *State) {
		s.Regions = nil
		s.Markers = nil
		s.Chips = nil
		s.Ripples = nil
		s.Trail = nil
		s.Dim = 0
		s.Dark = false
	})
}

// PrimaryWindow returns the overlay's main window, for previews and tests. It is
// nil when the overlay is disabled or has not started.
func (m *Mask) PrimaryWindow() *mygo.Window {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.windows) == 0 {
		return nil
	}
	return m.windows[0].win
}

// Preview renders what the overlay draws on its first display into a PNG.
// The result is what a screenshot would show with the content protection
// off, without changing the screen.
func (m *Mask) Preview() ([]byte, error) {
	png, _, ok := m.PreviewCapture()
	if !ok {
		return nil, errors.New("mask: no overlay window")
	}
	return png, nil
}

// PreviewCapture renders the first display's overlay and says which
// display it covers, so that the caller can blend it over a capture of the
// desktop.
func (m *Mask) PreviewCapture() ([]byte, win32.Display, bool) {
	return m.PreviewCaptureOf(0)
}

// PreviewCaptureOf renders the overlay of the display at index i, counting
// from the primary one. It waits up to three seconds for the window to draw:
// a capture taken as the app starts returns an empty frame, before the
// surface has a size and the view has run.
func (m *Mask) PreviewCaptureOf(i int) ([]byte, win32.Display, bool) {
	m.mu.Lock()
	var w *window
	if len(m.windows) > 0 {
		if i < 0 || i >= len(m.windows) {
			i = 0
		}
		w = m.windows[i]
	}
	m.mu.Unlock()
	if w == nil {
		return nil, win32.Display{}, false
	}
	rev := int64(m.Snapshot().Revision)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var png []byte
		if w.drawn.Load() >= rev {
			if cw, ch := w.win.ContentSize(); cw > 0 && ch > 0 {
				if out, err := w.win.CapturePage(); err == nil {
					png = out
					if hasInk(out) {
						return out, w.display, true
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return png, w.display, png != nil
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// hasInk reports whether a PNG holds a pixel that is not fully transparent,
// which tells an empty capture from a drawn frame.
func hasInk(png []byte) bool {
	img, err := pngDecode(bytes.NewReader(png))
	if err != nil {
		return false
	}
	b := img.Bounds()
	step := max(1, b.Dx()/64)
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				return true
			}
		}
	}
	return false
}

func (m *Mask) invalidate() {
	m.mu.Lock()
	wins := append([]*window(nil), m.windows...)
	m.mu.Unlock()
	for _, w := range wins {
		w.win.Invalidate()
	}
}

// invalidateDisplay repaints one display, by its device name.
func (m *Mask) invalidateDisplay(name string) {
	m.mu.Lock()
	var target *window
	for _, w := range m.windows {
		if w.display.Name == name {
			target = w
			break
		}
	}
	m.mu.Unlock()
	if target != nil {
		target.win.Invalidate()
	}
}

// loop runs the overlay's clock: it drops what expired, follows the pointer
// while coordinates are shown, and keeps animations moving.
func (m *Mask) loop() {
	// 80 ms: the pointer's chip still follows the hand, and the overlay
	// rebuilds a whole display far less often while the mouse moves.
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tick()
		}
	}
}

func (m *Mask) tick() {
	now := time.Now()
	var (
		needAll      bool
		cursorMoved  bool
		displayMoved bool
		oldDisplay   string
		newDisplay   string
	)
	m.mu.Lock()
	s := &m.st
	// Expire annotations that asked for it.
	if len(s.Regions) > 0 {
		kept := s.Regions[:0]
		for _, r := range s.Regions {
			if r.Until.IsZero() || now.Before(r.Until) {
				kept = append(kept, r)
			} else {
				needAll = true
			}
		}
		s.Regions = kept
	}
	if len(s.Markers) > 0 {
		kept := s.Markers[:0]
		for _, mk := range s.Markers {
			if !mk.Until.IsZero() && !now.Before(mk.Until) {
				needAll = true
				continue
			}
			if mk.Arrive && now.Sub(mk.started) >= markerArrive {
				mk.Arrive = false
				needAll = true
			}
			kept = append(kept, mk)
		}
		s.Markers = kept
	}
	if len(s.Chips) > 0 {
		kept := s.Chips[:0]
		for _, c := range s.Chips {
			if c.Until.IsZero() || now.Before(c.Until) {
				kept = append(kept, c)
			} else {
				needAll = true
			}
		}
		s.Chips = kept
	}
	if len(s.Ripples) > 0 {
		kept := s.Ripples[:0]
		for _, r := range s.Ripples {
			if now.Sub(r.Start) < rippleLife {
				kept = append(kept, r)
			} else {
				needAll = true
			}
		}
		s.Ripples = kept
	}
	if s.Visible && s.Coords && !s.Paused {
		if p, err := win32.Cursor(); err == nil {
			if !s.CursorOK || p != s.Cursor.At {
				oldDisplay = s.CursorDisplay
				s.Cursor = Marker{At: p}
				s.CursorOK = true
				newDisplay = ""
				if d, ok := win32.DisplayAt(p); ok {
					newDisplay = d.Name
				}
				s.CursorDisplay = newDisplay
				cursorMoved = true
				displayMoved = oldDisplay != newDisplay
			}
		}
	} else if s.CursorOK {
		s.CursorOK = false
		s.CursorDisplay = ""
		needAll = true
	}
	rev := s.Revision
	m.mu.Unlock()

	switch {
	case needAll:
		m.invalidate()
	case displayMoved:
		m.invalidateDisplay(oldDisplay)
		m.invalidateDisplay(newDisplay)
	case cursorMoved:
		m.invalidateDisplay(newDisplay)
	}
	_ = rev
}

func clampDim(v float32) float32 {
	if v <= 0 {
		return 0
	}
	if v > 0.85 {
		return 0.85
	}
	return v
}

// pngDecode reads a PNG the overlay rendered.
func pngDecode(r io.Reader) (image.Image, error) { return png.Decode(r) }
