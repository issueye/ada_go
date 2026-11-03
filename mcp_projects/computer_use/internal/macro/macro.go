//go:build windows

// Package macro keeps the operations an agent performed as a macro: a
// list of steps that can be saved, read back as JavaScript, and replayed.
package macro

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Step is one operation of a macro, with the same shape as an action.
type Step struct {
	Type    string `json:"type"`
	X       int    `json:"x,omitempty"`
	Y       int    `json:"y,omitempty"`
	X2      int    `json:"x2,omitempty"`
	Y2      int    `json:"y2,omitempty"`
	Button  string `json:"button,omitempty"`
	Clicks  int    `json:"clicks,omitempty"`
	DX      int    `json:"dx,omitempty"`
	DY      int    `json:"dy,omitempty"`
	Text    string `json:"text,omitempty"`
	Keys    string `json:"keys,omitempty"`
	Repeat  int    `json:"repeat,omitempty"`
	Handle  uint64 `json:"handle,omitempty"`
	Title   string `json:"title,omitempty"`
	Steps   int    `json:"steps,omitempty"`
	DelayMS int    `json:"delay_ms,omitempty"`
	Ms      int    `json:"ms,omitempty"`
	// Note is what the recorder wrote about the step, such as why it was
	// made.
	Note string `json:"note,omitempty"`
	// At is when the step was recorded, which replaying a long macro does
	// not wait for.
	At time.Time `json:"at,omitempty"`
}

// Macro is a named list of steps.
type Macro struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Steps   []Step    `json:"steps"`
	// Source says where the macro came from: "recording" or "script".
	Source string `json:"source,omitempty"`
}

// Summary is a macro without its steps, for a list.
type Summary struct {
	Name    string    `json:"name"`
	Steps   int       `json:"steps"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Source  string    `json:"source,omitempty"`
}

// Store keeps macros in memory and in a JSON file.
type Store struct {
	mu     sync.Mutex
	path   string
	byName map[string]*Macro
}

// NewStore reads the macros saved in path, if there are any.
func NewStore(path string) *Store {
	s := &Store{path: path, byName: map[string]*Macro{}}
	s.load()
	return s
}

// ValidName checks that name can name a macro in a URL and a file.
func ValidName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("a macro needs a name")
	}
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.'
		if !ok {
			return fmt.Errorf("a macro name holds letters, digits, -, _ and . only: %q", name)
		}
	}
	return nil
}

// Save stores a macro under its name, replacing one with that name.
func (s *Store) Save(m *Macro) error {
	if err := ValidName(m.Name); err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byName[m.Name]; ok && m.Created.IsZero() {
		m.Created = old.Created
	}
	if m.Created.IsZero() {
		m.Created = now
	}
	m.Updated = now
	copied := *m
	copied.Steps = append([]Step(nil), m.Steps...)
	s.byName[m.Name] = &copied
	return s.persistLocked()
}

// Get returns the macro with that name.
func (s *Store) Get(name string) (*Macro, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byName[name]
	if !ok {
		return nil, false
	}
	copied := *m
	copied.Steps = append([]Step(nil), m.Steps...)
	return &copied, true
}

// List returns the macros, by name.
func (s *Store) List() []Summary {
	s.mu.Lock()
	out := make([]Summary, 0, len(s.byName))
	for _, m := range s.byName {
		out = append(out, Summary{Name: m.Name, Steps: len(m.Steps), Created: m.Created, Updated: m.Updated, Source: m.Source})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes a macro, and reports whether there was one.
func (s *Store) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byName[name]; !ok {
		return false
	}
	delete(s.byName, name)
	s.persistLocked()
	return true
}

// Count reports how many macros are kept.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byName)
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file struct {
		Macros []*Macro `json:"macros"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return
	}
	for _, m := range file.Macros {
		if m == nil || ValidName(m.Name) != nil {
			continue
		}
		s.byName[m.Name] = m
	}
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	list := make([]*Macro, 0, len(s.byName))
	for _, m := range s.byName {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	raw, err := json.MarshalIndent(map[string]any{"macros": list}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Script renders a macro as JavaScript that calls the action API, so that a
// person can read, edit and run it as a script.
func Script(m *Macro) string {
	var b strings.Builder
	b.WriteString("// Macro: " + m.Name)
	if !m.Created.IsZero() {
		b.WriteString("  (recorded " + m.Created.Format(time.RFC3339) + ")")
	}
	b.WriteString("\n// Run it with cu.runScript(fs.readFileSync(...)) or POST /v1/script.\n\n")
	for i, s := range m.Steps {
		if s.Note != "" {
			b.WriteString("// " + s.Note + "\n")
		}
		b.WriteString(jsStep(s))
		if i < len(m.Steps)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// jsStep renders one step as a call.
func jsStep(s Step) string {
	q := func(v string) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	num := func(v int) string { return fmt.Sprint(v) }
	switch s.Type {
	case "click", "double_click":
		opts := ""
		if s.Button != "" && s.Button != "left" {
			opts += fmt.Sprintf("button: %s, ", q(s.Button))
		}
		if s.Clicks > 1 {
			opts += fmt.Sprintf("clicks: %d, ", s.Clicks)
		}
		return fmt.Sprintf("cu.click(%s, %s%s);", num(s.X), num(s.Y), withOpts(opts))
	case "move":
		return fmt.Sprintf("cu.move(%s, %s);", num(s.X), num(s.Y))
	case "drag":
		opts := ""
		if s.Button != "" && s.Button != "left" {
			opts += fmt.Sprintf("button: %s, ", q(s.Button))
		}
		return fmt.Sprintf("cu.drag(%s, %s, %s, %s%s);", num(s.X), num(s.Y), num(s.X2), num(s.Y2), withOpts(opts))
	case "scroll":
		return fmt.Sprintf("cu.scroll(%s, %s, %s, %s);", num(s.X), num(s.Y), num(s.DX), num(s.DY))
	case "type":
		opts := ""
		if s.DelayMS > 0 {
			opts += fmt.Sprintf("delay_ms: %d, ", s.DelayMS)
		}
		return fmt.Sprintf("cu.type(%s%s);", q(s.Text), withOpts(opts))
	case "key":
		opts := ""
		if s.Repeat > 1 {
			opts += fmt.Sprintf("repeat: %d, ", s.Repeat)
		}
		return fmt.Sprintf("cu.key(%s%s);", q(s.Keys), withOpts(opts))
	case "focus":
		if s.Title != "" {
			return fmt.Sprintf("cu.focus({title: %s});", q(s.Title))
		}
		return fmt.Sprintf("cu.focus({handle: %d});", s.Handle)
	case "outline":
		if s.Title != "" {
			return fmt.Sprintf("cu.outline({title: %s});", q(s.Title))
		}
		return fmt.Sprintf("cu.outline({handle: %d});", s.Handle)
	case "wait":
		return fmt.Sprintf("cu.wait(%s);", num(s.Ms))
	}
	return fmt.Sprintf("// unsupported step %q\n", s.Type)
}

func withOpts(opts string) string {
	if opts == "" {
		return ""
	}
	return ", {" + strings.TrimSuffix(opts, ", ") + "}"
}
