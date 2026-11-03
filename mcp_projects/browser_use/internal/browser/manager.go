package browser

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"browseruse/internal/browser/runtime"
	"browseruse/internal/config"

	"github.com/egoist/mygo"
)

// Download is one finished download.
type Download struct {
	Tab  string    `json:"tab"`
	URL  string    `json:"url"`
	Path string    `json:"path"`
	Err  string    `json:"err,omitempty"`
	At   time.Time `json:"at"`
}

// ControlState is the agent/human control state of the browser.
type ControlState struct {
	State      string     `json:"state"` // idle | agent | user
	TabID      string     `json:"tab,omitempty"`
	Masked     bool       `json:"masked"`
	LeaseUntil *time.Time `json:"lease_until,omitempty"`
	Client     string     `json:"client,omitempty"`
}

// Manager owns the tabs of the window and the agent's control lease.
type Manager struct {
	win     *mygo.Window
	cfg     config.Config
	session *mygo.Session

	downloadsDir string

	mu        sync.RWMutex
	tabs      map[string]*Tab
	order     []string
	active    string
	nextID    int
	downloads []Download
	user      bool // human has taken over

	onChange func()
	stop     chan struct{}
	stopped  sync.Once
}

// New creates a manager for a window that shows native UI.
func New(win *mygo.Window, cfg config.Config, session *mygo.Session) *Manager {
	dir := ""
	if d, err := config.Dir(); err == nil {
		sub := cfg.Agent.DownloadsSubdir
		if sub == "" {
			sub = "downloads"
		}
		dir = filepath.Join(d, sub)
		_ = os.MkdirAll(dir, 0o755)
	}
	m := &Manager{
		win: win, cfg: cfg, session: session, downloadsDir: dir,
		tabs: map[string]*Tab{}, stop: make(chan struct{}),
	}
	go m.expireLoop()
	return m
}

// SetOnChange registers a callback that runs when tab or control state
// changes; the app uses it to redraw.
func (m *Manager) SetOnChange(fn func()) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *Manager) notify() {
	m.mu.RLock()
	fn := m.onChange
	m.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (m *Manager) expireLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-t.C:
			changed := false
			for _, tab := range m.snapshotTabs() {
				if tab.expire(now) {
					changed = true
					m.unlockTab(tab)
				}
			}
			if changed {
				m.notify()
			}
		}
	}
}

func (m *Manager) snapshotTabs() []*Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Tab, 0, len(m.order))
	for _, id := range m.order {
		if t := m.tabs[id]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

// Close stops background work.
func (m *Manager) Close() {
	m.stopped.Do(func() { close(m.stop) })
}

// NewTab creates a tab and its web view.
func (m *Manager) NewTab(ctx context.Context, rawURL string, opts TabOptions) (*Tab, error) {
	rawURL, err := NormalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if len(m.tabs) >= m.cfg.Agent.MaxTabs {
		m.mu.Unlock()
		return nil, errorf(CodeTooManyTabs, "at most %d tabs", m.cfg.Agent.MaxTabs)
	}
	m.mu.Unlock()

	v, err := m.win.NewWebView(mygo.WebViewOptions{
		URL: rawURL,
		Page: mygo.PageOptions{
			PreloadScript:    runtime.Source(),
			PreloadAllFrames: true,
			Session:          m.session,
		},
		BackgroundColor: "#ffffff",
	})
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}

	m.mu.Lock()
	m.nextID++
	t := &Tab{
		ID:    fmt.Sprintf("t%d", m.nextID),
		View:  v,
		Page:  v.Page(),
		Owner: opts.Owner,
	}
	if t.Owner == "" {
		t.Owner = OwnerAgent
	}
	m.tabs[t.ID] = t
	m.order = append(m.order, t.ID)
	activate := opts.Activate || m.active == ""
	if activate {
		m.active = t.ID
	}
	m.mu.Unlock()

	t.setURL(rawURL)
	t.setTitle("New Tab")
	t.attachHandlers(m)

	m.notify()
	if activate {
		_ = m.Activate(t.ID)
	}
	return t, nil
}

// CloseTab destroys a tab and chooses the next active tab.
func (m *Manager) CloseTab(id string, force bool) error {
	m.mu.Lock()
	t := m.tabs[id]
	if t == nil {
		m.mu.Unlock()
		return errorf(CodeInvalidTab, "no tab %q", id)
	}
	if t.Owner == OwnerUser && !force {
		m.mu.Unlock()
		return &Error{Code: CodeProtectedTab, Message: "tab belongs to the user", Hint: "pass force or take over"}
	}
	delete(m.tabs, id)
	idx := -1
	for i, x := range m.order {
		if x == id {
			idx = i
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	if m.active == id {
		m.active = ""
		if len(m.order) > 0 {
			if idx < 0 {
				idx = 0
			}
			if idx >= len(m.order) {
				idx = len(m.order) - 1
			}
			m.active = m.order[idx]
		}
	}
	m.mu.Unlock()

	t.destroy()
	m.notify()
	return nil
}

// Activate makes a tab the active one.
func (m *Manager) Activate(id string) error {
	m.mu.Lock()
	if m.tabs[id] == nil {
		m.mu.Unlock()
		return errorf(CodeInvalidTab, "no tab %q", id)
	}
	m.active = id
	t := m.tabs[id]
	m.mu.Unlock()
	if t != nil && t.View != nil {
		t.View.Focus()
	}
	m.notify()
	return nil
}

// Tabs returns the tabs in order.
func (m *Manager) Tabs() []TabInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]TabInfo, 0, len(m.order))
	for _, id := range m.order {
		if t := m.tabs[id]; t != nil {
			out = append(out, t.info(m.active == id))
		}
	}
	return out
}

// Tab returns a tab by id.
func (m *Manager) Tab(id string) *Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tabs[id]
}

// Active returns the active tab, or nil.
func (m *Manager) Active() *Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tabs[m.active]
}

// ActiveID returns the active tab's id.
func (m *Manager) ActiveID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// UserControlled reports whether a human has taken over.
func (m *Manager) UserControlled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.user
}

// TakeOver gives the browser to the human and releases every lease.
func (m *Manager) TakeOver() ControlState {
	m.mu.Lock()
	m.user = true
	tabs := make([]*Tab, 0, len(m.order))
	for _, id := range m.order {
		if t := m.tabs[id]; t != nil {
			tabs = append(tabs, t)
		}
	}
	m.mu.Unlock()
	for _, t := range tabs {
		if t.releaseLease("") {
			m.unlockTab(t)
		}
	}
	m.notify()
	return m.State()
}

// Resume gives control back to the agent.
func (m *Manager) Resume() ControlState {
	m.mu.Lock()
	m.user = false
	m.mu.Unlock()
	m.notify()
	return m.State()
}

// Acquire gives the agent a lease on a tab.
func (m *Manager) Acquire(tabID, client string) (*Tab, error) {
	if m.UserControlled() {
		return nil, &Error{Code: CodeControlLocked, Message: "the human has taken over", Hint: "ask the human to resume"}
	}
	t := m.Tab(tabID)
	if t == nil {
		return nil, errorf(CodeInvalidTab, "no tab %q", tabID)
	}
	d := time.Duration(m.cfg.Control.IdleReleaseMS) * time.Millisecond
	if d <= 0 {
		d = 120 * time.Second
	}
	t.acquire(client, d)
	m.notify()
	return t, nil
}

// Release releases one tab, or every tab when tabID is empty.
func (m *Manager) Release(tabID, client string) ControlState {
	var tabs []*Tab
	if tabID != "" {
		if t := m.Tab(tabID); t != nil {
			tabs = []*Tab{t}
		}
	} else {
		tabs = m.snapshotTabs()
	}
	for _, t := range tabs {
		if t.releaseLease(client) {
			m.unlockTab(t)
		}
	}
	m.notify()
	return m.State()
}

// State reports the control state.
func (m *Manager) State() ControlState {
	m.mu.RLock()
	user := m.user
	active := m.active
	var t *Tab
	if active != "" {
		t = m.tabs[active]
	}
	if !user && t == nil {
		for _, id := range m.order {
			if x := m.tabs[id]; x != nil && x.HasLease() {
				t = x
				break
			}
		}
	}
	m.mu.RUnlock()

	cs := ControlState{State: "idle"}
	if user {
		cs.State = "user"
		return cs
	}
	if t != nil && t.HasLease() {
		t.mu.RLock()
		cs.State = "agent"
		cs.TabID = t.ID
		until := t.leaseUntil
		cs.LeaseUntil = &until
		cs.Client = t.leaseClient
		t.mu.RUnlock()
	}
	cs.Masked = cs.State == "agent" && m.cfg.Control.Mask && t != nil && t.ID == m.ActiveID()
	return cs
}

// Masked reports whether the active tab shows the agent mask.
func (m *Manager) Masked(tabID string) bool {
	if !m.cfg.Control.Mask || m.UserControlled() {
		return false
	}
	t := m.Tab(tabID)
	return t != nil && t.HasLease()
}

// unlockTab releases the page input lock of a tab after its lease ends.
func (m *Manager) unlockTab(t *Tab) {
	if t == nil || t.Page == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = t.invoke(ctx, "setInputLock", map[string]any{"on": false})
	}()
}

func (m *Manager) recordDownload(tabID string, d *mygo.Download) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d == nil {
		return
	}
	item := Download{Tab: tabID, URL: d.URL, Path: d.Path, At: time.Now()}
	if d.Err != nil {
		item.Err = d.Err.Error()
	}
	m.downloads = append(m.downloads, item)
	if len(m.downloads) > 50 {
		m.downloads = m.downloads[len(m.downloads)-50:]
	}
}

// Downloads returns the finished downloads.
func (m *Manager) Downloads() []Download {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Download(nil), m.downloads...)
}

// DownloadsDir is where downloads go.
func (m *Manager) DownloadsDir() string { return m.downloadsDir }

// NormalizeURL validates a URL the agent or human asked for.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "about:blank", nil
	}
	if raw == "about:blank" {
		return raw, nil
	}
	lower := strings.ToLower(raw)
	for _, scheme := range []string{"javascript:", "data:", "file:", "vbscript:", "blob:"} {
		if strings.HasPrefix(lower, scheme) {
			return "", &Error{Code: CodeBlockedURL, Message: "scheme not allowed: " + strings.TrimSuffix(scheme, ":"), Hint: "use http or https"}
		}
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", &Error{Code: CodeBlockedURL, Message: "invalid URL: " + raw}
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String(), nil
	case "about":
		if u.String() == "about:blank" {
			return u.String(), nil
		}
	}
	return "", &Error{Code: CodeBlockedURL, Message: "scheme not allowed: " + u.Scheme, Hint: "use http or https"}
}

// allowedNavigation reports whether a page may navigate there.
func allowedNavigation(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "about", "blob":
		return true
	}
	return false
}
