package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
)

// Owner tells who opened a tab: the agent or the human.
type Owner string

const (
	OwnerAgent Owner = "agent"
	OwnerUser  Owner = "user"
)

// TabOptions configure Manager.NewTab.
type TabOptions struct {
	Owner    Owner
	Activate bool
}

// Tab is one browser tab and its embedded web view.
type Tab struct {
	ID    string
	View  *mygo.WebView
	Page  *mygo.Page
	Owner Owner

	// actionMu serializes actions of this tab.
	actionMu sync.Mutex

	mu         sync.RWMutex
	title      string
	url        string
	loading    bool
	loadErr    string
	lastAction string

	version      atomic.Uint64
	navSeq       atomic.Uint64
	runtimeReady atomic.Bool
	closing      atomic.Bool
	actionBusy   atomic.Bool

	leaseUntil  time.Time
	leaseClient string
}

// Info is a tab as tools see it.
type TabInfo struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Loading bool   `json:"loading"`
	Active  bool   `json:"active"`
	Locked  bool   `json:"locked"`
	Owner   string `json:"owner"`
}

// Info returns the tab as tools see it.
func (t *Tab) Info(active bool) TabInfo { return t.info(active) }

func (t *Tab) info(active bool) TabInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TabInfo{
		ID: t.ID, URL: t.url, Title: t.title, Loading: t.loading,
		Active: active, Locked: t.hasLeaseLocked(), Owner: string(t.Owner),
	}
}

func (t *Tab) hasLeaseLocked() bool {
	return !t.leaseUntil.IsZero() && time.Now().Before(t.leaseUntil)
}

// HasLease reports whether an agent currently holds the tab.
func (t *Tab) HasLease() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.hasLeaseLocked()
}

// acquire gives the agent the tab for d.
func (t *Tab) acquire(client string, d time.Duration) {
	t.mu.Lock()
	t.leaseUntil = time.Now().Add(d)
	t.leaseClient = client
	t.mu.Unlock()
}

// releaseLease clears the lease when client owns it, or for any client when
// client is empty.
func (t *Tab) releaseLease(client string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if client != "" && t.leaseClient != "" && t.leaseClient != client {
		return false
	}
	if t.leaseUntil.IsZero() {
		return false
	}
	t.leaseUntil = time.Time{}
	t.leaseClient = ""
	return true
}

// renewLease extends the lease if client still owns it. A lease that was
// cleared (takeover, pause, release) or taken by another client is not revived.
func (t *Tab) renewLease(client string, d time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.leaseUntil.IsZero() || (t.leaseClient != "" && t.leaseClient != client) {
		return false
	}
	t.leaseUntil = time.Now().Add(d)
	return true
}

// leaseHeartbeat renews the lease while an action holds the action slot, so a
// long action (a 120s wait, a slow script step) is not stripped of its lease
// and its input lock mid-flight. It stops when stop closes (the action's done)
// or the lease no longer belongs to client.
func (t *Tab) leaseHeartbeat(client string, every, d time.Duration, stop <-chan struct{}) {
	if every <= 0 {
		return
	}
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			if !t.renewLease(client, d) {
				return
			}
		}
	}
}

// expire clears an expired lease and reports whether it did. A tab with an
// action in flight keeps its lease: the action's heartbeat renews it, and
// expiring here would unlock the page under a running action.
func (t *Tab) expire(now time.Time) bool {
	if t.actionBusy.Load() {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.leaseUntil.IsZero() && !now.Before(t.leaseUntil) {
		t.leaseUntil = time.Time{}
		t.leaseClient = ""
		return true
	}
	return false
}

func (t *Tab) setLoading(v bool) {
	t.mu.Lock()
	t.loading = v
	t.mu.Unlock()
}

func (t *Tab) setTitle(s string) {
	t.mu.Lock()
	t.title = s
	t.mu.Unlock()
}

func (t *Tab) setURL(s string) {
	t.mu.Lock()
	t.url = s
	t.mu.Unlock()
}

func (t *Tab) setLoadErr(s string) {
	t.mu.Lock()
	t.loadErr = s
	t.mu.Unlock()
}

func (t *Tab) setAction(s string) {
	t.mu.Lock()
	t.lastAction = s
	t.mu.Unlock()
}

// LastAction is the most recent action shown on the mask.
func (t *Tab) LastAction() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.lastAction
}

// URL returns the tab's current URL.
func (t *Tab) URL() string { return t.urlValue() }

// Title returns the tab's current title.
func (t *Tab) Title() string { return t.titleValue() }

func (t *Tab) titleValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.title
}

func (t *Tab) urlValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.url
}

func (t *Tab) loadErrValue() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loadErr
}

// waitLoad waits until the page is no longer loading. It polls mygo's
// IsLoading, which is safe from any goroutine, and the tab's own events.
func (t *Tab) waitLoad(ctx context.Context, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if !t.Page.IsLoading() {
			if err := t.loadErrValue(); err != "" {
				return &Error{Code: CodeNavFailed, Message: err}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return &Error{Code: CodeTimeout, Message: "navigation canceled: " + ctx.Err().Error()}
		case <-tick.C:
			if time.Now().After(deadline) {
				return &Error{Code: CodeTimeout, Message: "page did not finish loading"}
			}
		}
	}
}

// waitCommit waits for a navigation to start after seq, then for it to
// finish loading. A same-document navigation may not start one, so it also
// returns after a short grace period.
func (t *Tab) waitCommit(ctx context.Context, seq uint64, grace time.Duration) bool {
	if grace <= 0 {
		grace = 2 * time.Second
	}
	deadline := time.Now().Add(grace)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if t.navSeq.Load() > seq || t.Page.IsLoading() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

func (t *Tab) destroy() {
	if t.closing.Swap(true) {
		return
	}
	if t.View != nil {
		t.View.Destroy()
	}
}

func safeDownloadName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" {
		name = "download"
	}
	return name
}

func (t *Tab) attachHandlers(m *Manager) {
	p := t.Page
	p.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if e == nil {
			return
		}
		if !allowedNavigation(e.URL) {
			e.PreventDefault()
			t.setLoadErr("blocked navigation to " + e.URL)
		}
	})
	p.OnDidNavigate(func(url string) {
		t.setURL(url)
		t.setLoading(true)
		t.version.Add(1)
		t.navSeq.Add(1)
		t.runtimeReady.Store(false)
		m.notify()
		m.emit("nav.committed", t.ID, url)
	})
	p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		t.setTitle(e.Title)
		m.notify()
	})
	p.OnDidFinishLoad(func() {
		t.setLoading(false)
		t.setLoadErr("")
		m.notify()
		m.emit("nav.finished", t.ID, t.URL())
	})
	p.OnDidFailLoad(func(e *mygo.LoadError) {
		t.setLoading(false)
		if e != nil {
			t.setLoadErr(e.Error())
		}
		m.notify()
		m.emit("nav.failed", t.ID, t.loadErrValue())
	})
	p.OnWillDownload(func(e *mygo.DownloadEvent) {
		name := safeDownloadName(e.SuggestedName)
		dir := m.downloadsDir
		if dir == "" {
			e.PreventDefault()
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			e.PreventDefault()
			return
		}
		e.Path = filepath.Join(dir, time.Now().Format("20060102-150405")+"-"+name)
		m.downloadStarted(t.ID, e.URL, e.Path)
	})
	p.OnDownloadDone(func(d *mygo.Download) {
		m.recordDownload(t.ID, d)
		if d != nil && d.Err == nil {
			m.emit("download.finished", t.ID, d.Path)
		}
	})
	p.SetPermissionHandler(func(req mygo.PermissionRequest) bool { return false })
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if req.URL != "" {
			go func() {
				_, _ = m.NewTab(context.Background(), req.URL, TabOptions{Owner: OwnerAgent, Activate: true})
			}()
		}
		return nil
	})
}
