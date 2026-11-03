//go:build windows

// Package api serves the program's JSON API over HTTP, on the loopback
// interface by default: an agent calls it with curl, a script or an MCP
// server, and a human opens it in a browser.
package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"computeruse/internal/engine"
	"computeruse/internal/mask"
	"computeruse/internal/screen"
	"computeruse/internal/script"
	"computeruse/internal/win32"
)

// Server is the HTTP server.
type Server struct {
	eng    *engine.Engine
	runner *script.Runner
	token  string
	// version is reported by /v1/health.
	version string
	srv     *http.Server
}

// New returns a server in front of an engine.
func New(eng *engine.Engine, version, token string) *Server {
	return &Server{eng: eng, runner: script.New(eng, version), version: version, token: token}
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/v1/health", s.health)
	mux.HandleFunc("/v1/state", s.state)
	mux.HandleFunc("/v1/screenshot", s.screenshot)
	mux.HandleFunc("/v1/screenshot.png", s.screenshotPNG)
	mux.HandleFunc("/v1/windows", s.windows)
	mux.HandleFunc("/v1/actions", s.actions)
	mux.HandleFunc("/v1/mask", s.setMask)
	mux.HandleFunc("/v1/mask/clear", s.clearMask)
	mux.HandleFunc("/v1/mask.png", s.maskPNG)
	mux.HandleFunc("/v1/pause", s.pause)
	mux.HandleFunc("/v1/resume", s.resume)
	mux.HandleFunc("/v1/clipboard", s.clipboard)
	s.scriptRoutes(mux)
	return s.withAuth(mux)
}

// Listen serves on addr until the server closes. It returns at once.
func (s *Server) Listen(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("api: %v", err)
		}
	}()
	return "http://" + ln.Addr().String(), nil
}

// Close stops serving.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			got := r.Header.Get("Authorization")
			got = strings.TrimPrefix(got, "Bearer ")
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// writeJSONCompact writes JSON without the indentation, for payloads with
// a screenshot in them, where pretty printing costs time and bytes.
func writeJSONCompact(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"ok":      true,
		"version": s.version,
		"uptime":  s.eng.Uptime().Round(time.Second).String(),
		"time":    time.Now().Format(time.RFC3339),
	})
}

// state is what an agent reads first: where the screens are, what has the
// keyboard, and what the overlay shows.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	virtual, displays := screen.DisplayInfo()
	cursor, _ := win32.Cursor()
	var fg map[string]any
	if win, ok := win32.WindowByHandle(win32.FocusedWindow()); ok {
		fg = map[string]any{
			"handle": win.Handle, "title": win.Title, "process": win.Process,
			"rect": win.Rect, "minimized": win.Minimized,
		}
	}
	writeJSON(w, map[string]any{
		"ok":         true,
		"version":    s.version,
		"virtual":    virtual,
		"displays":   displays,
		"cursor":     cursor,
		"foreground": fg,
		"paused":     s.eng.Paused(),
		"mask":       s.eng.Mask().Snapshot(),
		"hint":       "x,y are physical pixels: screen = region + image/scale",
	})
}

func (s *Server) shotOptions(r *http.Request) screen.Options {
	q := r.URL.Query()
	o := screen.Options{
		MaxWidth: atoi(q.Get("max_width")),
		Grid:     atoi(q.Get("grid")),
		Cursor:   q.Get("cursor") == "1" || q.Get("cursor") == "true",
		Format:   q.Get("format"),
		Quality:  atoi(q.Get("quality")),
		Fast:     q.Get("fast") == "1" || q.Get("fast") == "true",
	}
	if v, err := strconv.ParseFloat(q.Get("scale"), 64); err == nil {
		o.Scale = v
	}
	if q.Get("display") != "" {
		_, displays := win32.Virtual()
		i := atoi(q.Get("display"))
		if i >= 0 && i < len(displays) {
			o.Region = displays[i].Bounds
		}
	}
	// A region, in physical pixels, overrides the display.
	if q.Get("x") != "" || q.Get("y") != "" || q.Get("w") != "" || q.Get("h") != "" {
		o.Region = win32.Rect{X: atoi(q.Get("x")), Y: atoi(q.Get("y")), W: atoi(q.Get("w")), H: atoi(q.Get("h"))}
	}
	return o
}

// screenshot returns the image and what it maps to, both in one JSON
// answer: image_base64 with format png or jpeg.
func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	shot, err := s.eng.Shot(s.shotOptions(r), r.URL.Query().Get("audit") != "0")
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	virtual, displays := screen.DisplayInfo()
	writeJSONCompact(w, map[string]any{
		"ok":           true,
		"media_type":   mediaType(shot.Format),
		"image_base64": base64.StdEncoding.EncodeToString(shot.Data),
		"meta": map[string]any{
			"width":    shot.Width,
			"height":   shot.Height,
			"region":   shot.Region,
			"scale":    shot.Scale,
			"format":   shot.Format,
			"virtual":  virtual,
			"displays": displays,
			"mapping":  "screen_x = region.x + image_x/scale; screen_y = region.y + image_y/scale",
			"ms": map[string]any{
				"capture": shot.CaptureMS,
				"scale":   shot.ScaleMS,
				"encode":  shot.EncodeMS,
			},
		},
	})
}

// screenshotPNG returns the image itself, with the mapping in headers, for
// tools that show an image rather than decode JSON.
func (s *Server) screenshotPNG(w http.ResponseWriter, r *http.Request) {
	shot, err := s.eng.Shot(s.shotOptions(r), r.URL.Query().Get("audit") != "0")
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", mediaType(shot.Format))
	w.Header().Set("X-Screen-Region", fmt.Sprintf("%d,%d,%d,%d", shot.Region.X, shot.Region.Y, shot.Region.W, shot.Region.H))
	w.Header().Set("X-Screen-Scale", strconv.FormatFloat(shot.Scale, 'f', -1, 64))
	w.Header().Set("X-Screen-Width", strconv.Itoa(shot.Width))
	w.Header().Set("X-Screen-Height", strconv.Itoa(shot.Height))
	w.Write(shot.Data)
}

func mediaType(format string) string {
	if format == "jpeg" {
		return "image/jpeg"
	}
	return "image/png"
}

func (s *Server) windows(w http.ResponseWriter, r *http.Request) {
	wins := s.eng.Windows()
	out := make([]map[string]any, 0, len(wins))
	for _, win := range wins {
		out = append(out, map[string]any{
			"handle": win.Handle, "title": win.Title, "process": win.Process,
			"class": win.Class, "rect": win.Rect, "minimized": win.Minimized,
			"maximized": win.Maximized, "foreground": win.Foreground,
		})
	}
	writeJSON(w, map[string]any{"ok": true, "windows": out})
}

// Action is one command of /v1/actions.
type Action struct {
	Type    string `json:"type"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	X2      int    `json:"x2"`
	Y2      int    `json:"y2"`
	Button  string `json:"button"`
	Clicks  int    `json:"clicks"`
	DX      int    `json:"dx"`
	DY      int    `json:"dy"`
	Text    string `json:"text"`
	Keys    string `json:"keys"`
	Repeat  int    `json:"repeat"`
	Handle  uint64 `json:"handle"`
	Title   string `json:"title"`
	Steps   int    `json:"steps"`
	DelayMS int    `json:"delay_ms"`
	Ms      int    `json:"ms"`
	Append  bool   `json:"append"`
}

func (s *Server) actions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, fmt.Errorf("POST only"))
		return
	}
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	// Accept one action or a batch of them, run in order.
	var batch struct {
		Actions []Action `json:"actions"`
	}
	var single Action
	var raw json.RawMessage
	if err := body.Decode(&raw); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := json.Unmarshal(raw, &batch); err == nil && len(batch.Actions) > 0 {
		results := make([]map[string]any, 0, len(batch.Actions))
		start := time.Now()
		// One HUD for the whole batch: a repaint per action would cost more
		// than the actions.
		s.eng.BeginBatch()
		for _, a := range batch.Actions {
			one := time.Now()
			res, err := s.run(a)
			if err != nil {
				s.eng.EndBatch()
				fail(w, http.StatusBadRequest, err)
				return
			}
			res["elapsed_ms"] = time.Since(one).Milliseconds()
			results = append(results, res)
		}
		s.eng.EndBatch()
		writeJSON(w, map[string]any{"ok": true, "elapsed_ms": time.Since(start).Milliseconds(), "results": results})
		return
	}
	if err := json.Unmarshal(raw, &single); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	start := time.Now()
	res, err := s.run(single)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	res["elapsed_ms"] = time.Since(start).Milliseconds()
	writeJSON(w, res)
}

func (s *Server) run(a Action) (map[string]any, error) {
	after := func() map[string]any {
		out := map[string]any{"ok": true, "type": a.Type}
		if p, err := win32.Cursor(); err == nil {
			out["cursor"] = p
		}
		if win, ok := win32.WindowByHandle(win32.FocusedWindow()); ok {
			out["foreground"] = map[string]any{"handle": win.Handle, "title": win.Title}
		}
		return out
	}
	switch strings.ToLower(a.Type) {
	case "click", "dblclick", "double_click", "doubleclick":
		button, err := win32.Button(a.Button)
		if err != nil {
			return nil, err
		}
		clicks := max(a.Clicks, 1)
		if strings.HasPrefix(strings.ToLower(a.Type), "dbl") {
			clicks = 2
		}
		if err := s.eng.Click(win32.Point{X: a.X, Y: a.Y}, button, clicks); err != nil {
			return nil, err
		}
	case "move":
		if err := s.eng.Move(win32.Point{X: a.X, Y: a.Y}, a.Steps); err != nil {
			return nil, err
		}
	case "drag":
		button, err := win32.Button(a.Button)
		if err != nil {
			return nil, err
		}
		if err := s.eng.Drag(win32.Point{X: a.X, Y: a.Y}, win32.Point{X: a.X2, Y: a.Y2}, button); err != nil {
			return nil, err
		}
	case "scroll":
		if err := s.eng.Scroll(win32.Point{X: a.X, Y: a.Y}, a.DX, a.DY); err != nil {
			return nil, err
		}
	case "type", "write":
		// No delay_ms types in batches, as fast as the window reads;
		// delay_ms asks for a pause after each character.
		delay := time.Duration(-1)
		if a.DelayMS > 0 {
			delay = time.Duration(a.DelayMS) * time.Millisecond
		}
		if err := s.eng.Type(a.Text, delay); err != nil {
			return nil, err
		}
	case "key", "press":
		if err := s.eng.Key(a.Keys, a.Repeat); err != nil {
			return nil, err
		}
	case "wait", "sleep":
		ms := a.Ms
		if ms <= 0 {
			ms = 250
		}
		// Through the engine, so that a recording keeps the pause.
		s.eng.Wait(ms)
	case "outline", "highlight":
		win, err := s.eng.OutlineWindow(a.Handle, a.Title, a.Append)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "type": "outline", "window": map[string]any{
			"handle": win.Handle, "title": win.Title, "rect": win.Rect,
		}}, nil
	case "focus":
		win, err := s.eng.FocusWindow(a.Handle, a.Title)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "type": "focus", "window": map[string]any{
			"handle": win.Handle, "title": win.Title, "rect": win.Rect,
		}}, nil
	default:
		return nil, fmt.Errorf("unknown action %q", a.Type)
	}
	return after(), nil
}

func (s *Server) setMask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, http.StatusMethodNotAllowed, fmt.Errorf("POST only"))
		return
	}
	m := s.eng.Mask()
	if m == nil || m.Disabled() {
		fail(w, http.StatusConflict, fmt.Errorf("the overlay is off (started with --no-mask)"))
		return
	}
	var req mask.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := m.Apply(req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "mask": m.Snapshot()})
}

func (s *Server) clearMask(w http.ResponseWriter, r *http.Request) {
	m := s.eng.Mask()
	if m == nil || m.Disabled() {
		fail(w, http.StatusConflict, fmt.Errorf("the overlay is off (started with --no-mask)"))
		return
	}
	m.Clear()
	writeJSON(w, map[string]any{"ok": true})
}

// maskPNG renders the overlay itself over the desktop, so that the agent
// can check what the human sees. The screen capture leaves the overlay
// out; this blends the two.
func (s *Server) maskPNG(w http.ResponseWriter, r *http.Request) {
	m := s.eng.Mask()
	if m == nil || m.Disabled() {
		fail(w, http.StatusConflict, fmt.Errorf("the overlay is off (started with --no-mask)"))
		return
	}
	png, display, ok := m.PreviewCaptureOf(atoi(r.URL.Query().Get("display")))
	if !ok {
		fail(w, http.StatusInternalServerError, fmt.Errorf("the overlay has no window yet"))
		return
	}
	out, err := screen.Composite(display.Bounds, png)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(out)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	s.eng.SetPaused(true)
	writeJSON(w, map[string]any{"ok": true, "paused": true})
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.eng.SetPaused(false)
	writeJSON(w, map[string]any{"ok": true, "paused": false})
}

func (s *Server) clipboard(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		text, err := s.eng.ClipboardText()
		if err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "text": text})
	case http.MethodPost:
		var req struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
		if err := s.eng.SetClipboardText(req.Text); err != nil {
			fail(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		fail(w, http.StatusMethodNotAllowed, fmt.Errorf("GET or POST"))
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
