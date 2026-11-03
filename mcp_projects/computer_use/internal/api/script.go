//go:build windows

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"computeruse/internal/macro"
	"computeruse/internal/script"
	"computeruse/internal/win32"
)

// scriptRoutes serves the JavaScript and macro endpoints.
func (s *Server) scriptRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/script", s.runScript)
	mux.HandleFunc("GET /v1/macros", s.listMacros)
	mux.HandleFunc("GET /v1/macros/{name}", s.getMacro)
	mux.HandleFunc("PUT /v1/macros/{name}", s.putMacro)
	mux.HandleFunc("DELETE /v1/macros/{name}", s.deleteMacro)
	mux.HandleFunc("POST /v1/macros/{name}/run", s.runMacro)
	mux.HandleFunc("GET /v1/macros/{name}/script", s.macroScript)
	mux.HandleFunc("GET /v1/record", s.recordStatus)
	mux.HandleFunc("POST /v1/record/start", s.recordStart)
	mux.HandleFunc("POST /v1/record/stop", s.recordStop)
}

// runScript runs JavaScript with the actions bound into it.
func (s *Server) runScript(w http.ResponseWriter, r *http.Request) {
	var req script.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.runner.Run(req)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) listMacros(w http.ResponseWriter, r *http.Request) {
	store := s.eng.Macros()
	if store == nil {
		writeJSON(w, map[string]any{"ok": true, "macros": []any{}})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "macros": store.List()})
}

func (s *Server) getMacro(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store := s.eng.Macros()
	if store == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	m, ok := store.Get(name)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "macro": m})
}

// putMacro saves a macro an outside tool wrote, such as a recorded script.
func (s *Server) putMacro(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := macro.ValidName(name); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	var body struct {
		Steps []macro.Step `json:"steps"`
		Notes string       `json:"notes,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	store := s.eng.Macros()
	if store == nil {
		fail(w, http.StatusConflict, fmt.Errorf("macros are off"))
		return
	}
	m := &macro.Macro{Name: name, Steps: body.Steps, Source: "script"}
	if err := store.Save(m); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "macro": m})
}

func (s *Server) deleteMacro(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store := s.eng.Macros()
	if store == nil || !store.Delete(name) {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "deleted": name})
}

// runMacro replays a macro, with a speed for its waits.
func (s *Server) runMacro(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store := s.eng.Macros()
	if store == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	m, ok := store.Get(name)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	speed := 1.0
	var body struct {
		Speed float64 `json:"speed"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, err)
			return
		}
	}
	if body.Speed > 0 {
		speed = body.Speed
	}
	steps, err := s.eng.RunMacro(m, speed)
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	p, _ := win32.Cursor()
	writeJSON(w, map[string]any{"ok": true, "name": name, "steps": steps, "speed": speed, "cursor": p})
}

// macroScript writes a macro as JavaScript, for a person to read or edit.
func (s *Server) macroScript(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	store := s.eng.Macros()
	if store == nil {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	m, ok := store.Get(name)
	if !ok {
		fail(w, http.StatusNotFound, fmt.Errorf("no macro named %q", name))
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Write([]byte(macro.Script(m)))
}

func (s *Server) recordStatus(w http.ResponseWriter, r *http.Request) {
	name, steps, started, ok := s.eng.RecordingStatus()
	writeJSON(w, map[string]any{"ok": true, "recording": ok, "name": name, "steps": steps, "started": started})
}

func (s *Server) recordStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if r.ContentLength != 0 {
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	}
	name, err := s.eng.StartRecording(strings.TrimSpace(body.Name))
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "recording": true, "name": name})
}

func (s *Server) recordStop(w http.ResponseWriter, r *http.Request) {
	m, err := s.eng.StopRecording()
	if err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "macro": m})
}
