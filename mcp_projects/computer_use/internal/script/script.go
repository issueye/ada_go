//go:build windows

// Package script runs JavaScript in moejs with the program's actions bound
// into it: a script clicks, types and draws the mask through `cu`, records
// what it does into a macro, and replays macros.
package script

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moejs"

	"computeruse/internal/engine"
	"computeruse/internal/macro"
	"computeruse/internal/mask"
	"computeruse/internal/screen"
	"computeruse/internal/win32"
)

// Runner runs scripts against one engine. It runs one script at a time.
type Runner struct {
	eng     *engine.Engine
	version string
	mu      sync.Mutex
}

// New returns a runner for the engine.
func New(eng *engine.Engine, version string) *Runner {
	return &Runner{eng: eng, version: version}
}

// Request is a script to run.
type Request struct {
	Source string         `json:"source"`
	Name   string         `json:"name,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	// TimeoutMS stops a script that runs too long; 60000 when zero.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// Record records the actions of the script into a macro of this name.
	Record string `json:"record,omitempty"`
}

// Result is what a run produced.
type Result struct {
	OK         bool     `json:"ok"`
	Result     any      `json:"result,omitempty"`
	Output     []string `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	// Macro is the recording the run made or replayed last.
	Macro string `json:"macro,omitempty"`
}

// output collects what a script logs.
type output struct {
	lines []string
}

func (o *output) add(format string, args ...any) {
	o.lines = append(o.lines, fmt.Sprintf(format, args...))
	if len(o.lines) > 500 {
		o.lines = o.lines[len(o.lines)-500:]
	}
}

// Run compiles and runs a script, with the actions of the program bound into
// it as `cu`.
func (r *Runner) Run(req Request) (*Result, error) {
	if strings.TrimSpace(req.Source) == "" {
		return nil, fmt.Errorf("the script is empty")
	}
	name := req.Name
	if name == "" {
		name = "script.js"
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Minute
	}
	start := time.Now()
	out := &output{}

	r.mu.Lock()
	defer r.mu.Unlock()

	// A recording asked for by the request wraps the whole run.
	recording := false
	if req.Record != "" {
		if _, err := r.eng.StartRecording(req.Record); err != nil {
			return nil, err
		}
		recording = true
		defer func() {
			if recording {
				// A run that failed early still keeps what it recorded.
				_, _ = r.eng.StopRecording()
			}
		}()
	}

	rt := moejs.NewRuntime(moejs.Options{TimeZone: time.Local, Locale: "zh-CN"})
	globals := map[string]any{
		"cu":      r.bindings(rt, out, req.Args),
		"console": r.console(out),
		"args":    req.Args,
		"version": r.version,
	}
	for k, v := range globals {
		if err := rt.SetGlobal(k, v); err != nil {
			return nil, err
		}
	}
	compiled, err := moejs.CompileScript(name, req.Source)
	if err != nil {
		return &Result{OK: false, Error: err.Error(), Output: out.lines, DurationMS: time.Since(start).Milliseconds()}, nil
	}

	var (
		value  any
		runErr error
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := rt.RunScript(compiled)
		if err != nil {
			runErr = err
			return
		}
		v, err := rt.ToGo(res)
		if err == nil {
			value = v
		}
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		rt.Interrupt(fmt.Sprintf("the script ran longer than %s", timeout))
		<-done
		runErr = fmt.Errorf("the script ran longer than %s", timeout)
	}

	res := &Result{
		OK:         runErr == nil,
		Result:     value,
		Output:     out.lines,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if runErr != nil {
		res.Error = describe(runErr, rt)
	}
	if recording {
		if m, err := r.eng.StopRecording(); err == nil {
			res.Macro = m.Name
		}
		recording = false
	}
	return res, nil
}

// describe turns a script error into a line a person can read.
func describe(err error, rt *moejs.Runtime) string {
	var exc *moejs.Exception
	if e, ok := err.(*moejs.Exception); ok {
		exc = e
	}
	if exc != nil {
		if stack := rt.StackTrace(exc); stack != "" {
			return err.Error() + "\n" + stack
		}
	}
	return err.Error()
}

// bindings builds the `cu` object.
func (r *Runner) bindings(rt *moejs.Runtime, out *output, args map[string]any) map[string]any {
	eng := r.eng
	// call runs an action, and turns a Go error into a JavaScript throw.
	call := func(fn func() error) (moejs.Value, error) {
		if err := fn(); err != nil {
			return moejs.Undefined(), err
		}
		return moejs.Undefined(), nil
	}
	value := func(v any) (moejs.Value, error) { return rt.FromGo(v) }

	b := map[string]any{
		"version": r.version,
		"args":    args,

		"click": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			opts := options(rt, a, 2)
			button, err := win32.Button(optStr(opts, "button", "left"))
			if err != nil {
				return moejs.Undefined(), err
			}
			return call(func() error {
				return eng.Click(win32.Point{X: argInt(realm, a, 0), Y: argInt(realm, a, 1)}, button, max(optInt(opts, "clicks", 1), 1))
			})
		}),
		"dblclick": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			opts := options(rt, a, 2)
			button, err := win32.Button(optStr(opts, "button", "left"))
			if err != nil {
				return moejs.Undefined(), err
			}
			return call(func() error {
				return eng.Click(win32.Point{X: argInt(realm, a, 0), Y: argInt(realm, a, 1)}, button, 2)
			})
		}),
		"move": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			opts := options(rt, a, 2)
			p := win32.Point{X: argInt(realm, a, 0), Y: argInt(realm, a, 1)}
			return call(func() error { return eng.Move(p, optInt(opts, "steps", 1)) })
		}),
		"drag": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			opts := options(rt, a, 4)
			button, err := win32.Button(optStr(opts, "button", "left"))
			if err != nil {
				return moejs.Undefined(), err
			}
			from := win32.Point{X: argInt(realm, a, 0), Y: argInt(realm, a, 1)}
			to := win32.Point{X: argInt(realm, a, 2), Y: argInt(realm, a, 3)}
			return call(func() error { return eng.Drag(from, to, button) })
		}),
		"scroll": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			p := win32.Point{X: argInt(realm, a, 0), Y: argInt(realm, a, 1)}
			return call(func() error { return eng.Scroll(p, argInt(realm, a, 2), argInt(realm, a, 3)) })
		}),
		"type": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			opts := options(rt, a, 1)
			text := argStr(realm, a, 0)
			delay := time.Duration(-1)
			if ms := optInt(opts, "delay_ms", -1); ms >= 0 {
				delay = time.Duration(ms) * time.Millisecond
			}
			return call(func() error { return eng.Type(text, delay) })
		}),
		"key": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			opts := options(rt, a, 1)
			return call(func() error { return eng.Key(argStr(realm, a, 0), optInt(opts, "repeat", 1)) })
		}),
		"wait": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			return call(func() error { return eng.Wait(argInt(realm, a, 0)) })
		}),
		"focus": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			handle, title := target(realm, rt, a)
			w, err := eng.FocusWindow(handle, title)
			if err != nil {
				return moejs.Undefined(), err
			}
			return value(describeWindow(w))
		}),
		"outline": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			handle, title := target(realm, rt, a)
			w, err := eng.OutlineWindow(handle, title, optBool(options(rt, a, 1), "append", false))
			if err != nil {
				return moejs.Undefined(), err
			}
			return value(describeWindow(w))
		}),
		"screenshot": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			o := screen.Options{MaxWidth: optInt(options(rt, a, 0), "max_width", 1600), Format: optStr(options(rt, a, 0), "format", "png")}
			opts := options(rt, a, 0)
			o.Grid = optInt(opts, "grid", 0)
			o.Fast = optBool(opts, "fast", false)
			if x, ok := opts["region"]; ok {
				if m, ok := x.(map[string]any); ok {
					o.Region = win32.Rect{X: toInt(m["x"]), Y: toInt(m["y"]), W: toInt(m["w"]), H: toInt(m["h"])}
				}
			}
			if d := optInt(opts, "display", -1); d >= 0 && o.Region.Empty() {
				_, displays := win32.Virtual()
				if d < len(displays) {
					o.Region = displays[d].Bounds
				}
			}
			shot, err := eng.Shot(o, true)
			if err != nil {
				return moejs.Undefined(), err
			}
			meta := map[string]any{"width": shot.Width, "height": shot.Height, "region": shot.Region, "scale": shot.Scale, "format": shot.Format}
			if optBool(opts, "image", false) {
				meta["image_base64"] = base64.StdEncoding.EncodeToString(shot.Data)
			}
			return value(meta)
		}),
		"state": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			return value(r.state())
		}),
		"cursor": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			p, err := win32.Cursor()
			if err != nil {
				return moejs.Undefined(), err
			}
			return value(map[string]any{"x": p.X, "y": p.Y})
		}),
		"windows": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			wins := eng.Windows()
			list := make([]map[string]any, 0, len(wins))
			for _, w := range wins {
				list = append(list, describeWindow(w))
			}
			return value(list)
		}),
		"mask": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			m := eng.Mask()
			if m == nil {
				return moejs.Undefined(), fmt.Errorf("the overlay is off")
			}
			opts := options(rt, a, 0)
			raw, err := json.Marshal(opts)
			if err != nil {
				return moejs.Undefined(), err
			}
			var req mask.Request
			if err := json.Unmarshal(raw, &req); err != nil {
				return moejs.Undefined(), err
			}
			if err := m.Apply(req); err != nil {
				return moejs.Undefined(), err
			}
			return value(m.Snapshot())
		}),
		"clearMask": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			if m := eng.Mask(); m != nil {
				m.Clear()
			}
			return moejs.Undefined(), nil
		}),
		"pause": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			eng.SetPaused(true)
			return moejs.Undefined(), nil
		}),
		"resume": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			eng.SetPaused(false)
			return moejs.Undefined(), nil
		}),
		"log": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			parts := make([]string, 0, len(a))
			for _, v := range a {
				g, err := rt.ToGo(v)
				if err != nil {
					g = v.String()
				}
				parts = append(parts, fmt.Sprint(g))
			}
			out.add("%s", strings.Join(parts, " "))
			return moejs.Undefined(), nil
		}),

		"recordStart": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			name, err := eng.StartRecording(argStr(realm, a, 0))
			if err != nil {
				return moejs.Undefined(), err
			}
			return moejs.String(name), nil
		}),
		"recordStop": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			m, err := eng.StopRecording()
			if err != nil {
				return moejs.Undefined(), err
			}
			return value(map[string]any{"name": m.Name, "steps": len(m.Steps)})
		}),
		"macroList": moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			store := eng.Macros()
			if store == nil {
				return value([]any{})
			}
			return value(store.List())
		}),
		"macroRun": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			store := eng.Macros()
			if store == nil {
				return moejs.Undefined(), fmt.Errorf("no macro store")
			}
			name := argStr(realm, a, 0)
			m, ok := store.Get(name)
			if !ok {
				return moejs.Undefined(), fmt.Errorf("no macro named %q", name)
			}
			speed := 1.0
			if opts := options(rt, a, 1); opts != nil {
				if v, ok := opts["speed"].(float64); ok && v > 0 {
					speed = v
				}
			}
			n, err := eng.RunMacro(m, speed)
			if err != nil {
				return moejs.Undefined(), err
			}
			return value(map[string]any{"name": name, "steps": n})
		}),
		"macroScript": moejs.NativeFunc(func(realm *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
			store := eng.Macros()
			if store == nil {
				return moejs.Undefined(), fmt.Errorf("no macro store")
			}
			m, ok := store.Get(argStr(realm, a, 0))
			if !ok {
				return moejs.Undefined(), fmt.Errorf("no macro named %q", argStr(realm, a, 0))
			}
			return moejs.String(macro.Script(m)), nil
		}),
	}
	// sleep is wait by another name, as scripts are written both ways.
	b["sleep"] = b["wait"]
	return b
}

// console gives scripts console.log and its siblings.
func (r *Runner) console(out *output) map[string]any {
	log := moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, a []moejs.Value) (moejs.Value, error) {
		parts := make([]string, 0, len(a))
		for _, v := range a {
			if v.IsString() {
				parts = append(parts, v.String())
				continue
			}
			parts = append(parts, v.String())
		}
		out.add("%s", strings.Join(parts, " "))
		return moejs.Undefined(), nil
	})
	return map[string]any{"log": log, "info": log, "warn": log, "error": log, "debug": log}
}

// state is the picture cu.state() returns.
func (r *Runner) state() map[string]any {
	virtual, displays := win32.Virtual()
	p, _ := win32.Cursor()
	out := map[string]any{"virtual": virtual, "displays": displays, "cursor": p, "paused": r.eng.Paused()}
	if w, ok := win32.WindowByHandle(win32.FocusedWindow()); ok {
		out["foreground"] = describeWindow(w)
	}
	if m := r.eng.Mask(); m != nil {
		out["mask"] = m.Snapshot()
	}
	return out
}

// describeWindow is how a window comes back to a script.
func describeWindow(w win32.Window) map[string]any {
	return map[string]any{
		"handle": w.Handle, "title": w.Title, "process": w.Process,
		"rect": w.Rect, "minimized": w.Minimized, "foreground": w.Foreground,
	}
}

// Number and string helpers for host functions.

func argInt(r *moejs.Realm, args []moejs.Value, i int) int {
	if i >= len(args) {
		return 0
	}
	n, err := r.ToNumber(args[i])
	if err != nil {
		return 0
	}
	return int(n)
}

func argStr(r *moejs.Realm, args []moejs.Value, i int) string {
	if i >= len(args) {
		return ""
	}
	s, err := r.ToString(args[i])
	if err != nil || s == nil {
		return ""
	}
	return s.GoString()
}

// options reads the object argument at i as a Go map, nil when it is not an
// object.
func options(rt *moejs.Runtime, args []moejs.Value, i int) map[string]any {
	if i >= len(args) || !args[i].IsObject() {
		return nil
	}
	v, err := rt.ToGo(args[i])
	if err != nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// target reads {handle, title} or a bare title from the arguments.
func target(r *moejs.Realm, rt *moejs.Runtime, args []moejs.Value) (uint64, string) {
	if len(args) == 0 {
		return 0, ""
	}
	if args[0].IsString() {
		return 0, argStr(r, args, 0)
	}
	m := options(rt, args, 0)
	return uint64(max(optInt(m, "handle", 0), 0)), optStr(m, "title", "")
}

func optInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	return asInt(m[key], def)
}

// asInt reads a number a script passed: moejs hands whole numbers back as
// int64, and JSON objects as float64.
func asInt(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

func optStr(m map[string]any, key, def string) string {
	if m == nil {
		return def
	}
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func optBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func toInt(v any) int { return asInt(v, 0) }
