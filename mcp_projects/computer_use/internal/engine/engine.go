//go:build windows

// Package engine is the program's core: it turns what an agent asks for, a
// click or a screenshot, into injected input and a screenshot of the
// desktop, and it tells the mask what to show while it happens.
package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"computeruse/internal/macro"
	"computeruse/internal/mask"
	"computeruse/internal/screen"
	"computeruse/internal/win32"
)

// ErrPaused is returned while the user holds the agent.
var ErrPaused = errors.New("the agent is paused; resume it before acting")

// Config chooses what the engine may do.
type Config struct {
	// Mask is the overlay the engine draws on. It may be nil, as in tests.
	Mask *mask.Mask
	// AllowInput lets actions inject input. Without it, the engine only
	// reads the screen: useful to watch a human.
	AllowInput bool
	// LogPath is the file actions are appended to, as JSON lines. Empty
	// keeps no log.
	LogPath string
	// TypingDelay is how long a key waits after the one before it. Zero
	// types in batches, as fast as the window reads them.
	TypingDelay time.Duration
	// Lead is how long the mask shows a target before the input lands, so
	// that a human sees where the agent is about to act. Zero acts at once.
	Lead time.Duration
	// Settle is how long the pointer waits after it moved before a button
	// goes down: a window that reacts to the move needs a moment.
	Settle time.Duration
}

// Engine executes actions.
type Engine struct {
	cfg     Config
	mu      sync.Mutex
	paused  bool
	stopCh  chan struct{}
	started time.Time
	// last is what the HUD shows.
	last string
	// log is the audit file, kept open for appends.
	log *os.File
	// logMu guards log.
	logMu sync.Mutex
	// gesture serializes actions, so two calls never fight over the
	// pointer.
	gesture sync.Mutex
	// batch counts the actions of a batch: the HUD moves once per batch,
	// not once per action, since a frame of the overlay costs more than the
	// action it reports.
	batch int
	// recMu guards the recording, and macros is the store recordings go to.
	recMu     sync.Mutex
	rec       *recording
	macros    *macro.Store
	replaying bool
	// winMu guards the window list an agent polls, which enumerating the
	// desktop costs tens of milliseconds to make.
	winMu   sync.Mutex
	winAt   time.Time
	winList []win32.Window
}

// New returns an engine.
func New(cfg Config) (*Engine, error) {
	if cfg.TypingDelay < 0 {
		cfg.TypingDelay = 0
	}
	if cfg.Lead < 0 {
		cfg.Lead = 0
	}
	if cfg.Settle <= 0 {
		cfg.Settle = 10 * time.Millisecond
	}
	e := &Engine{cfg: cfg, stopCh: make(chan struct{}), started: time.Now()}
	if cfg.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0o755); err == nil {
			f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				e.log = f
			}
		}
	}
	return e, nil
}

// Close releases the audit log and the overlay.
func (e *Engine) Close() {
	close(e.stopCh)
	e.logMu.Lock()
	if e.log != nil {
		e.log.Close()
		e.log = nil
	}
	e.logMu.Unlock()
}

// Paused reports whether the user holds the agent.
func (e *Engine) Paused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused
}

// SetPaused holds or releases the agent.
func (e *Engine) SetPaused(v bool) {
	e.mu.Lock()
	e.paused = v
	e.mu.Unlock()
	if e.cfg.Mask != nil {
		e.cfg.Mask.SetPaused(v)
	}
	if v {
		e.hud("已暂停 · paused", false)
	} else {
		e.hud("待命 · idle", false)
	}
}

// TogglePaused flips the pause.
func (e *Engine) TogglePaused() { e.SetPaused(!e.Paused()) }

// Uptime is how long the app runs.
func (e *Engine) Uptime() time.Duration { return time.Since(e.started) }

// guard refuses input while paused and while input is not allowed.
func (e *Engine) guard() error {
	if e.cfg.Mask != nil && e.cfg.Mask.Disabled() {
		// A disabled mask must not stop the agent from working.
	}
	if e.Paused() {
		return ErrPaused
	}
	if !e.cfg.AllowInput {
		return errors.New("this instance was started read-only (--read-only): it does not inject input")
	}
	return nil
}

// Shot is what a screenshot of the desktop shows. audit false keeps a
// screenshot out of the action log, for the status page's live view.
func (e *Engine) Shot(o screen.Options, audit bool) (*screen.Shot, error) {
	shot, err := screen.Take(o)
	if err != nil {
		if audit {
			e.audit(map[string]any{"type": "screenshot", "ok": false, "error": err.Error()})
		}
		return nil, err
	}
	if audit {
		e.audit(map[string]any{
			"type": "screenshot", "ok": true, "region": shot.Region, "scale": shot.Scale,
			"width": shot.Width, "height": shot.Height, "format": shot.Format,
		})
	}
	return shot, nil
}

// Point checks that p is on the desktop.
func Point(p win32.Point) error {
	v, _ := win32.Virtual()
	if !v.Contains(p) {
		return fmt.Errorf("(%d, %d) is outside the desktop %v", p.X, p.Y, v)
	}
	return nil
}

// Click presses a button at p, with the pointer moved there first.
func (e *Engine) Click(p win32.Point, button win32.MouseButton, clicks int) error {
	if err := e.guard(); err != nil {
		return err
	}
	if err := Point(p); err != nil {
		return err
	}
	e.gesture.Lock()
	defer e.gesture.Unlock()
	name := "click"
	if clicks > 1 {
		name = fmt.Sprintf("double-click")
	}
	e.hud(fmt.Sprintf("%s %d,%d", name, p.X, p.Y), true)
	e.markAt(p, "", 1500*time.Millisecond)
	if d := e.cfg.Lead; d > 0 {
		time.Sleep(d)
	}
	if err := win32.MoveTo(p); err != nil {
		return err
	}
	if d := e.cfg.Settle; d > 0 {
		time.Sleep(d)
	}
	if err := win32.Click(button, clicks); err != nil {
		return err
	}
	e.ripple(p, toneFor(button))
	e.markAt(p, "", 900*time.Millisecond)
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": name, "at": p, "button": button, "ok": true})
	e.record(macro.Step{Type: "click", X: p.X, Y: p.Y, Button: buttonName(button), Clicks: clicks})
	return nil
}

// Move moves the pointer to p, in steps that a window sees as a drag.
func (e *Engine) Move(p win32.Point, steps int) error {
	if err := e.guard(); err != nil {
		return err
	}
	if err := Point(p); err != nil {
		return err
	}
	e.gesture.Lock()
	defer e.gesture.Unlock()
	e.hud(fmt.Sprintf("move %d,%d", p.X, p.Y), true)
	if steps < 1 {
		steps = 1
	}
	if steps == 1 {
		if err := win32.MoveTo(p); err != nil {
			return err
		}
	} else {
		from, err := win32.Cursor()
		if err != nil {
			return err
		}
		for i := 1; i <= steps; i++ {
			t := float64(i) / float64(steps)
			at := win32.Point{
				X: from.X + int(float64(p.X-from.X)*t),
				Y: from.Y + int(float64(p.Y-from.Y)*t),
			}
			if err := win32.MoveTo(at); err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	e.markAt(p, "", 1200*time.Millisecond)
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": "move", "at": p, "ok": true})
	e.record(macro.Step{Type: "move", X: p.X, Y: p.Y, Steps: steps})
	return nil
}

// Drag drags from one point to another.
func (e *Engine) Drag(from, to win32.Point, button win32.MouseButton) error {
	if err := e.guard(); err != nil {
		return err
	}
	if err := Point(from); err != nil {
		return err
	}
	if err := Point(to); err != nil {
		return err
	}
	e.gesture.Lock()
	defer e.gesture.Unlock()
	e.hud(fmt.Sprintf("drag %d,%d → %d,%d", from.X, from.Y, to.X, to.Y), true)
	e.markAt(from, "", 1200*time.Millisecond)
	trail := []win32.Point{}
	for i := 0; i <= 16; i++ {
		t := float64(i) / 16
		trail = append(trail, win32.Point{
			X: from.X + int(float64(to.X-from.X)*t),
			Y: from.Y + int(float64(to.Y-from.Y)*t),
		})
	}
	if e.cfg.Mask != nil {
		e.cfg.Mask.SetTrail(trail)
	}
	if err := win32.Drag(from, to, button, 12); err != nil {
		return err
	}
	e.markAt(to, "", 900*time.Millisecond)
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": "drag", "from": from, "to": to, "button": button, "ok": true})
	e.record(macro.Step{Type: "drag", X: from.X, Y: from.Y, X2: to.X, Y2: to.Y, Button: buttonName(button)})
	go func() {
		time.Sleep(1200 * time.Millisecond)
		if e.cfg.Mask != nil {
			e.cfg.Mask.SetTrail(nil)
		}
	}()
	return nil
}

// Scroll turns the wheel at p: dx notches right, dy notches down.
func (e *Engine) Scroll(p win32.Point, dx, dy int) error {
	if err := e.guard(); err != nil {
		return err
	}
	if err := Point(p); err != nil {
		return err
	}
	e.gesture.Lock()
	defer e.gesture.Unlock()
	e.hud(fmt.Sprintf("scroll %d,%d", dx, dy), true)
	e.markAt(p, "", 900*time.Millisecond)
	if err := win32.Scroll(p, dx, dy); err != nil {
		return err
	}
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": "scroll", "at": p, "dx": dx, "dy": dy, "ok": true})
	e.record(macro.Step{Type: "scroll", X: p.X, Y: p.Y, DX: dx, DY: dy})
	return nil
}

// Type types text at the keyboard focus.
func (e *Engine) Type(text string, delay time.Duration) error {
	if err := e.guard(); err != nil {
		return err
	}
	if text == "" {
		return nil
	}
	e.gesture.Lock()
	defer e.gesture.Unlock()
	preview := text
	if len([]rune(preview)) > 24 {
		preview = string([]rune(preview)[:24]) + "…"
	}
	e.hud(fmt.Sprintf("type %q", strings.ReplaceAll(preview, "\n", "⏎")), true)
	if delay < 0 {
		delay = e.cfg.TypingDelay
	}
	err := win32.Type(text, delay)
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": "type", "text": text, "ok": err == nil})
	if err == nil {
		ms := 0
		if delay > 0 {
			ms = int(delay / time.Millisecond)
		}
		e.record(macro.Step{Type: "type", Text: text, DelayMS: ms})
	}
	return err
}

// Key presses a key chord, as "ctrl+s" or "alt+tab".
func (e *Engine) Key(keys string, repeat int) error {
	if err := e.guard(); err != nil {
		return err
	}
	if repeat < 1 {
		repeat = 1
	}
	// Parse first, so that a bad chord fails before the HUD moves.
	if _, _, err := win32.KeyCodes(keys); err != nil {
		return err
	}
	e.gesture.Lock()
	defer e.gesture.Unlock()
	e.hud("key "+keys, true)
	for i := 0; i < repeat; i++ {
		if err := win32.KeyCombo(keys); err != nil {
			e.hud("待命 · idle", false)
			return err
		}
		if i < repeat-1 {
			time.Sleep(30 * time.Millisecond)
		}
	}
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": "key", "keys": keys, "repeat": repeat, "ok": true})
	e.record(macro.Step{Type: "key", Keys: keys, Repeat: repeat})
	return nil
}

// OutlineWindow draws a window with its coordinates in the mask: the
// element an agent is about to use, shown to whoever watches.
func (e *Engine) OutlineWindow(handle uint64, title string, add bool) (win32.Window, error) {
	m := e.cfg.Mask
	if m == nil || m.Disabled() {
		return win32.Window{}, errors.New("the overlay is off")
	}
	var target win32.Window
	switch {
	case handle != 0:
		w, ok := win32.WindowByHandle(uintptr(handle))
		if !ok {
			return win32.Window{}, fmt.Errorf("no window with handle %d", handle)
		}
		target = w
	case title != "":
		matches := e.matchWindows(title)
		if len(matches) == 0 {
			return win32.Window{}, fmt.Errorf("no window with %q in its title", title)
		}
		target = matches[0]
	default:
		w, ok := win32.WindowByHandle(win32.FocusedWindow())
		if !ok {
			return win32.Window{}, errors.New("no window has the keyboard")
		}
		target = w
	}
	region := mask.Region{
		Rect:  target.Rect,
		Label: truncate(target.Title, 40),
		Tone:  "accent",
	}
	if add {
		m.AddRegions(region)
	} else {
		m.ShowRegions([]mask.Region{region})
	}
	e.hud("outline "+truncate(target.Title, 32), false)
	e.audit(map[string]any{"type": "outline", "handle": target.Handle, "title": target.Title, "ok": true})
	e.record(macro.Step{Type: "outline", Handle: uint64(target.Handle), Title: target.Title})
	return target, nil
}

// Windows lists the windows an agent can focus, without this program's own
// overlay. The list is kept for a moment: agents poll it between steps, and
// enumerating the desktop costs more than the answer changes.
func (e *Engine) Windows() []win32.Window {
	e.winMu.Lock()
	defer e.winMu.Unlock()
	if e.winList != nil && time.Since(e.winAt) < 400*time.Millisecond {
		return append([]win32.Window(nil), e.winList...)
	}
	own := win32.CurrentProcessID()
	all := win32.Windows()
	out := make([]win32.Window, 0, len(all))
	for _, w := range all {
		if w.ProcessID == own {
			continue
		}
		out = append(out, w)
	}
	e.winList, e.winAt = out, time.Now()
	return append([]win32.Window(nil), out...)
}

// forgetWindows drops the kept window list, after something changed the
// order or the focus of the desktop.
func (e *Engine) forgetWindows() {
	e.winMu.Lock()
	e.winAt = time.Time{}
	e.winMu.Unlock()
}

// FocusWindow brings a window to the front, by handle or by title.
func (e *Engine) FocusWindow(handle uint64, title string) (win32.Window, error) {
	if err := e.guard(); err != nil {
		return win32.Window{}, err
	}
	var target win32.Window
	switch {
	case handle != 0:
		w, ok := win32.WindowByHandle(uintptr(handle))
		if !ok {
			return win32.Window{}, fmt.Errorf("no window with handle %d", handle)
		}
		target = w
	case title != "":
		matches := e.matchWindows(title)
		if len(matches) == 0 {
			return win32.Window{}, fmt.Errorf("no window with %q in its title", title)
		}
		target = matches[0]
	default:
		return win32.Window{}, errors.New("give a handle or a title")
	}
	e.hud("focus "+truncate(target.Title, 32), true)
	if err := win32.FocusWindow(target.Handle); err != nil {
		e.hud("待命 · idle", false)
		return target, err
	}
	e.forgetWindows()
	time.Sleep(120 * time.Millisecond)
	e.hud("待命 · idle", false)
	e.audit(map[string]any{"type": "focus", "handle": target.Handle, "title": target.Title, "ok": true})
	e.record(macro.Step{Type: "focus", Handle: uint64(target.Handle), Title: target.Title})
	return target, nil
}

func (e *Engine) matchWindows(title string) []win32.Window {
	needle := strings.ToLower(title)
	var out []win32.Window
	for _, w := range e.Windows() {
		if strings.Contains(strings.ToLower(w.Title), needle) {
			out = append(out, w)
		}
	}
	return out
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ClipboardText returns the clipboard's text.
func (e *Engine) ClipboardText() (string, error) {
	e.audit(map[string]any{"type": "clipboard", "op": "get", "ok": true})
	return win32.ClipboardText()
}

// SetClipboardText replaces the clipboard's text.
func (e *Engine) SetClipboardText(text string) error {
	if err := e.guard(); err != nil {
		return err
	}
	err := win32.SetClipboardText(text)
	e.audit(map[string]any{"type": "clipboard", "op": "set", "bytes": len(text), "ok": err == nil})
	return err
}

// Mask returns the overlay, which may be nil.
func (e *Engine) Mask() *mask.Mask { return e.cfg.Mask }

// SetMacroStore gives the engine the store that recordings go to.
func (e *Engine) SetMacroStore(s *macro.Store) { e.macros = s }

// Macros returns the store, which may be nil.
func (e *Engine) Macros() *macro.Store { return e.macros }

// recording is a macro being recorded.
type recording struct {
	name    string
	started time.Time
	steps   []macro.Step
}

// StartRecording begins recording every action into a macro named name. A
// name given twice is replaced when the recording stops.
func (e *Engine) StartRecording(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "macro-" + time.Now().Format("20060102-150405")
	}
	if err := macro.ValidName(name); err != nil {
		return "", err
	}
	e.recMu.Lock()
	if e.rec != nil {
		old := e.rec.name
		e.recMu.Unlock()
		return "", fmt.Errorf("a recording named %q is already running", old)
	}
	e.rec = &recording{name: name, started: time.Now()}
	e.recMu.Unlock()
	e.hud("录制中 · recording "+name, true)
	return name, nil
}

// StopRecording ends the recording and saves the macro.
func (e *Engine) StopRecording() (*macro.Macro, error) {
	e.recMu.Lock()
	r := e.rec
	e.rec = nil
	e.recMu.Unlock()
	if r == nil {
		return nil, errors.New("no recording is running")
	}
	m := &macro.Macro{Name: r.name, Created: r.started, Steps: r.steps, Source: "recording"}
	if e.macros != nil {
		if err := e.macros.Save(m); err != nil {
			return m, err
		}
	}
	e.hud(fmt.Sprintf("已录制 %d 步 · record saved", len(m.Steps)), false)
	return m, nil
}

// RecordingStatus reports the recording in progress, if any.
func (e *Engine) RecordingStatus() (name string, steps int, started time.Time, ok bool) {
	e.recMu.Lock()
	defer e.recMu.Unlock()
	if e.rec == nil {
		return "", 0, time.Time{}, false
	}
	return e.rec.name, len(e.rec.steps), e.rec.started, true
}

// record adds a step to the recording in progress.
func (e *Engine) record(step macro.Step) {
	e.recMu.Lock()
	defer e.recMu.Unlock()
	if e.rec == nil || e.replaying {
		return
	}
	step.At = time.Now()
	e.rec.steps = append(e.rec.steps, step)
}

// Wait sleeps for a while, and records the pause when recording.
func (e *Engine) Wait(ms int) error {
	if ms <= 0 {
		return nil
	}
	ms = min(ms, 30000)
	e.record(macro.Step{Type: "wait", Ms: ms})
	time.Sleep(time.Duration(ms) * time.Millisecond)
	return nil
}

// RunMacro replays a macro: each step goes through the same engine methods as
// an action, so the mask, the pause switch and the audit log apply. speed
// scales the waits (2 plays twice as fast, 0.5 twice as slow).
func (e *Engine) RunMacro(m *macro.Macro, speed float64) (int, error) {
	if m == nil || len(m.Steps) == 0 {
		return 0, errors.New("the macro has no steps")
	}
	if speed <= 0 {
		speed = 1
	}
	e.recMu.Lock()
	e.replaying = true
	e.recMu.Unlock()
	defer func() {
		e.recMu.Lock()
		e.replaying = false
		e.recMu.Unlock()
	}()

	e.BeginBatch()
	defer e.EndBatch()
	done := 0
	for i, s := range m.Steps {
		if e.Paused() {
			return done, ErrPaused
		}
		if e.cfg.Mask != nil {
			e.cfg.Mask.SetHUD(fmt.Sprintf("宏回放 %s %d/%d · replay", m.Name, i+1, len(m.Steps)), true)
		}
		if err := e.runStep(s, speed); err != nil {
			e.audit(map[string]any{"type": "macro", "name": m.Name, "step": i + 1, "ok": false, "error": err.Error()})
			return done, fmt.Errorf("step %d (%s): %w", i+1, s.Type, err)
		}
		done++
	}
	e.audit(map[string]any{"type": "macro", "name": m.Name, "steps": done, "speed": speed, "ok": true})
	return done, nil
}

// runStep runs one step of a macro.
func (e *Engine) runStep(s macro.Step, speed float64) error {
	button, err := win32.Button(s.Button)
	if err != nil {
		return err
	}
	switch s.Type {
	case "click", "double_click":
		clicks := max(s.Clicks, 1)
		if s.Type == "double_click" {
			clicks = 2
		}
		return e.Click(win32.Point{X: s.X, Y: s.Y}, button, clicks)
	case "move":
		return e.Move(win32.Point{X: s.X, Y: s.Y}, s.Steps)
	case "drag":
		return e.Drag(win32.Point{X: s.X, Y: s.Y}, win32.Point{X: s.X2, Y: s.Y2}, button)
	case "scroll":
		return e.Scroll(win32.Point{X: s.X, Y: s.Y}, s.DX, s.DY)
	case "type":
		return e.Type(s.Text, time.Duration(s.DelayMS)*time.Millisecond)
	case "key":
		return e.Key(s.Keys, s.Repeat)
	case "focus":
		_, err := e.FocusWindow(s.Handle, s.Title)
		return err
	case "outline":
		_, err := e.OutlineWindow(s.Handle, s.Title, false)
		return err
	case "wait":
		ms := int(float64(s.Ms) / speed)
		if ms > 0 {
			time.Sleep(time.Duration(min(ms, 30000)) * time.Millisecond)
		}
		return nil
	}
	return fmt.Errorf("unknown step %q", s.Type)
}

// BeginBatch makes the actions that follow share one HUD update.
func (e *Engine) BeginBatch() {
	e.mu.Lock()
	e.batch++
	e.mu.Unlock()
	if e.cfg.Mask != nil {
		e.cfg.Mask.SetHUD("批量执行 · batch", true)
	}
}

// EndBatch returns to one HUD update per action.
func (e *Engine) EndBatch() {
	e.mu.Lock()
	if e.batch > 0 {
		e.batch--
	}
	left := e.batch
	e.mu.Unlock()
	if left == 0 && e.cfg.Mask != nil {
		e.cfg.Mask.SetHUD("待命 · idle", false)
	}
}

// HUD shows a line in the overlay's pill.
func (e *Engine) hud(detail string, busy bool) {
	if e.cfg.Mask == nil {
		return
	}
	e.mu.Lock()
	e.last = detail
	inBatch := e.batch > 0
	e.mu.Unlock()
	if inBatch {
		// The batch sets the pill itself.
		return
	}
	e.cfg.Mask.SetHUD(detail, busy)
}

func (e *Engine) markAt(p win32.Point, label string, ttl time.Duration) {
	if e.cfg.Mask == nil {
		return
	}
	e.cfg.Mask.Mark(mask.Marker{At: p, Label: label, Until: time.Now().Add(ttl)})
}

func (e *Engine) ripple(p win32.Point, tone string) {
	if e.cfg.Mask == nil {
		return
	}
	e.cfg.Mask.Ripple(p, tone)
}

// buttonName writes a button the way a macro and the API name it.
func buttonName(b win32.MouseButton) string {
	switch b {
	case win32.Right:
		return "right"
	case win32.Middle:
		return "middle"
	}
	return "left"
}

func toneFor(b win32.MouseButton) string {
	switch b {
	case win32.Right:
		return "amber"
	case win32.Middle:
		return "green"
	}
	return "accent"
}

// audit appends a line to the action log.
func (e *Engine) audit(record map[string]any) {
	if e.log == nil {
		return
	}
	record["time"] = time.Now().Format(time.RFC3339Nano)
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	e.logMu.Lock()
	e.log.Write(append(line, '\n'))
	e.logMu.Unlock()
}
