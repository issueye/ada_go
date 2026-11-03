package app

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"browseruse/internal/browser"
	"browseruse/internal/config"

	"github.com/egoist/mygo"
)

type sessionState struct {
	Version   int       `json:"version"`
	Tabs      []string  `json:"tabs"`
	Active    int       `json:"active"`
	X         int       `json:"x,omitempty"`
	Y         int       `json:"y,omitempty"`
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
	Maximized bool      `json:"maximized,omitempty"`
	SavedAt   time.Time `json:"saved_at"`
}

func sessionPath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "session.json")
}

func loadSession() *sessionState {
	p := sessionPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var st sessionState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil
	}
	clean := make([]string, 0, len(st.Tabs))
	for _, u := range st.Tabs {
		if allowedSessionURL(u) {
			clean = append(clean, u)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	st.Tabs = clean
	if st.Active < 0 || st.Active >= len(clean) {
		st.Active = 0
	}
	return &st
}

func allowedSessionURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "about":
		return raw == "about:blank"
	default:
		return false
	}
}

func (a *App) restoreTabs(st *sessionState) bool {
	if st == nil || a.mgr == nil {
		return false
	}
	max := a.cfg.Session.MaxTabs
	if max <= 0 {
		max = 20
	}
	if a.cfg.Agent.MaxTabs > 0 && max > a.cfg.Agent.MaxTabs {
		max = a.cfg.Agent.MaxTabs
	}
	type opened struct {
		id string
	}
	var tabs []opened
	want := st.Active
	for i, u := range st.Tabs {
		if len(tabs) >= max {
			break
		}
		tab, err := a.mgr.NewTab(context.Background(), u, browser.TabOptions{Owner: browser.OwnerUser, Activate: false})
		if err != nil {
			log.Printf("browser-use: restore %s: %v", u, err)
			continue
		}
		tabs = append(tabs, opened{id: tab.ID})
		if i == want {
			want = len(tabs) - 1
		}
	}
	if len(tabs) == 0 {
		return false
	}
	if want < 0 || want >= len(tabs) {
		want = 0
	}
	_ = a.mgr.Activate(tabs[want].id)
	return true
}

func (a *App) scheduleSessionSave() {
	if !a.sessionReady || a.saveTimer != nil {
		return
	}
	a.saveTimer = time.AfterFunc(700*time.Millisecond, func() {
		mygo.RunOnMain(func() {
			a.saveTimer = nil
			a.saveSessionNow()
		})
	})
}

func (a *App) saveSessionNow() {
	if !a.sessionReady || !a.sessionSave || a.win == nil || a.mgr == nil {
		return
	}
	max := a.cfg.Session.MaxTabs
	if max <= 0 {
		max = 20
	}
	infos := a.mgr.Tabs()
	st := sessionState{Version: 1, SavedAt: time.Now()}
	for _, info := range infos {
		if !allowedSessionURL(info.URL) {
			continue
		}
		if info.Active {
			st.Active = len(st.Tabs)
		}
		st.Tabs = append(st.Tabs, info.URL)
		if len(st.Tabs) >= max {
			break
		}
	}
	if st.Active >= len(st.Tabs) {
		st.Active = 0
	}
	r := a.win.Bounds()
	st.X, st.Y, st.Width, st.Height = r.X, r.Y, r.Width, r.Height
	st.Maximized = a.win.IsMaximized()
	p := sessionPath()
	if p == "" {
		return
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}
