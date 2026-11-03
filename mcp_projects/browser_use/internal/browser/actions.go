package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"browseruse/internal/browser/runtime"
)

// Point is a CSS-pixel point in the viewport.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Target locates an element by ref, selector or visible text.
type Target struct {
	Ref      string `json:"ref,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Point    *Point `json:"point,omitempty"`
}

// Validate checks that exactly one locator is set.
func (t Target) Validate() error {
	n := 0
	if strings.TrimSpace(t.Ref) != "" {
		n++
	}
	if strings.TrimSpace(t.Selector) != "" {
		n++
	}
	if strings.TrimSpace(t.Text) != "" {
		n++
	}
	if t.Point != nil {
		n++
	}
	if n == 0 {
		return &Error{Code: CodeBadRequest, Message: "one of ref, selector, text or point is required"}
	}
	if n > 1 {
		return &Error{Code: CodeBadRequest, Message: "ref, selector, text and point are mutually exclusive"}
	}
	return nil
}

func (t Target) toJS() map[string]any {
	m := map[string]any{}
	if t.Ref != "" {
		m["ref"] = t.Ref
	}
	if t.Selector != "" {
		m["selector"] = t.Selector
	}
	if t.Text != "" {
		m["text"] = t.Text
	}
	if t.Point != nil {
		m["point"] = map[string]float64{"x": t.Point.X, "y": t.Point.Y}
	}
	return m
}

// SnapshotNode is one element of a snapshot.
type SnapshotNode struct {
	Ref  string `json:"ref"`
	Role string `json:"role"`
	Name string `json:"name"`
	Rect Rect   `json:"rect"`
}

// Rect is a CSS-pixel rectangle.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Snapshot is the result of a snapshot action.
type Snapshot struct {
	Snapshot   string         `json:"snapshot"`
	Nodes      []SnapshotNode `json:"nodes,omitempty"`
	Version    int            `json:"version"`
	Truncated  bool           `json:"truncated"`
	URL        string         `json:"url"`
	Title      string         `json:"title"`
	Viewport   map[string]any `json:"viewport,omitempty"`
	Scroll     map[string]any `json:"scroll,omitempty"`
	NodesCount int            `json:"nodes_count"`
}

// SnapshotOptions configure a snapshot.
type SnapshotOptions struct {
	Scope    string `json:"scope,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
	Filter   string `json:"filter,omitempty"`
}

type snapshotWire struct {
	Text       string         `json:"text"`
	Nodes      []SnapshotNode `json:"nodes"`
	Version    int            `json:"version"`
	Truncated  bool           `json:"truncated"`
	URL        string         `json:"url"`
	Title      string         `json:"title"`
	Viewport   map[string]any `json:"viewport"`
	Scroll     map[string]any `json:"scroll"`
	NodesCount int            `json:"nodes_count"`
}

// Receipt is returned by click/type/press/select/hover.
type Receipt struct {
	Clicked   *SnapshotNode  `json:"clicked,omitempty"`
	Typed     *SnapshotNode  `json:"typed,omitempty"`
	Pressed   map[string]any `json:"pressed,omitempty"`
	Selected  []string       `json:"selected,omitempty"`
	Hovered   *SnapshotNode  `json:"hovered,omitempty"`
	Navigated bool           `json:"navigated"`
	Revealed  bool           `json:"revealed,omitempty"`
	URL       string         `json:"url,omitempty"`
	Title     string         `json:"title,omitempty"`
	Submitted bool           `json:"submitted,omitempty"`
}

type clickWire struct {
	Clicked  *SnapshotNode `json:"clicked"`
	Revealed bool          `json:"revealed"`
	URL      string        `json:"url"`
	Title    string        `json:"title"`
}

type typeWire struct {
	Typed     *SnapshotNode `json:"typed"`
	Submitted bool          `json:"submitted"`
	URL       string        `json:"url"`
}

type pressWire struct {
	Pressed map[string]any `json:"pressed"`
	URL     string         `json:"url"`
}

type selectWire struct {
	Selected []string `json:"selected"`
	Value    string   `json:"value"`
	URL      string   `json:"url"`
}

type hoverWire struct {
	Hovered *SnapshotNode `json:"hovered"`
	URL     string        `json:"url"`
}

// EvalResult is the value a page expression returned.
type EvalResult struct {
	Result any    `json:"result"`
	URL    string `json:"url"`
	Title  string `json:"title"`
}

// Eval runs JavaScript in the page and returns its JSON result.
func (m *Manager) Eval(ctx context.Context, tabID, client, expression string, timeout time.Duration) (*EvalResult, error) {
	if !m.cfg.Agent.AllowEval {
		return nil, &Error{Code: CodeBlockedAction, Message: "eval is disabled", Hint: "set agent.allow_eval to true"}
	}
	if len(expression) > 1<<20 {
		return nil, &Error{Code: CodeBadRequest, Message: "expression is too large"}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	t, done, err := m.begin(ctx, tabID, client, "eval", true)
	if err != nil {
		return nil, err
	}
	defer done()
	v, err := t.Page.EvalContext(ctx, expression)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Code: CodeTimeout, Message: "eval timed out"}
		}
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return &EvalResult{Result: v, URL: t.Page.URL(), Title: t.titleValue()}, nil
}

// Dialog is one script dialog the runtime intercepted.
type Dialog struct {
	Type         string  `json:"type"`
	Message      string  `json:"message"`
	DefaultValue *string `json:"defaultValue,omitempty"`
	TS           int64   `json:"ts"`
}

// Dialogs lists the alert/confirm/prompt calls the page made.
func (m *Manager) Dialogs(ctx context.Context, tabID, client string) ([]Dialog, error) {
	t, done, err := m.begin(ctx, tabID, client, "dialogs", false)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "dialogs", map[string]any{})
	if err != nil {
		return nil, err
	}
	var w struct {
		Dialogs []Dialog `json:"dialogs"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return w.Dialogs, nil
}

// ReadResult is the readable text of a page or element.
type ReadResult struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Chars     int    `json:"chars"`
}

// Read returns the page's readable text.
func (m *Manager) Read(ctx context.Context, tabID, client, selector string, maxChars int) (*ReadResult, error) {
	t, done, err := m.begin(ctx, tabID, client, "read", false)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "read", map[string]any{"selector": selector, "max_chars": maxChars})
	if err != nil {
		return nil, err
	}
	var r ReadResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return &r, nil
}

// ClickOptions configure a click.
type ClickOptions struct {
	Button    string
	Count     int
	Modifiers []string
	Force     bool
	Reveal    *bool
	WaitAfter string
	Timeout   time.Duration
}

// TypeOptions configure typing.
type TypeOptions struct {
	Clear     bool
	Submit    bool
	Slowly    bool
	WaitAfter string
	Timeout   time.Duration
}

// begin resolves a tab, acquires its lease and serializes the action.
func (m *Manager) begin(ctx context.Context, tabID, client, action string, mutating bool) (*Tab, func(), error) {
	if strings.TrimSpace(tabID) == "" {
		tabID = m.ActiveID()
	}
	if tabID == "" {
		return nil, nil, &Error{Code: CodeNoTab, Message: "no active tab", Hint: "navigate or create a tab first"}
	}
	if mutating && m.UserControlled() {
		return nil, nil, &Error{Code: CodeControlLocked, Message: "the human has taken over", Hint: "ask the human to resume"}
	}
	t := m.Tab(tabID)
	if t == nil {
		return nil, nil, errorf(CodeInvalidTab, "no tab %q", tabID)
	}
	if !m.UserControlled() {
		var err error
		t, err = m.Acquire(tabID, client)
		if err != nil {
			return nil, nil, err
		}
	}
	t.actionMu.Lock()
	t.setAction(action)
	if err := t.ensureRuntime(ctx); err != nil {
		t.actionMu.Unlock()
		return nil, nil, err
	}
	if m.cfg.Control.Mask && !m.UserControlled() {
		t.setInputLock(true)
	}
	m.notify()
	done := func() {
		t.actionMu.Unlock()
		m.notify()
	}
	return t, done, nil
}

func (t *Tab) ensureRuntime(ctx context.Context) error {
	if t.runtimeReady.Load() {
		return nil
	}
	for i := 0; i < 3; i++ {
		if v, err := t.Page.EvalContext(ctx, "typeof window.__browserUse"); err == nil && v == "object" {
			t.runtimeReady.Store(true)
			return nil
		}
		if _, err := t.Page.EvalContext(ctx, runtime.Source()); err == nil {
			if v, err := t.Page.EvalContext(ctx, "typeof window.__browserUse"); err == nil && v == "object" {
				t.runtimeReady.Store(true)
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return &Error{Code: CodeTimeout, Message: "page is not ready: " + ctx.Err().Error()}
		case <-time.After(100 * time.Millisecond):
		}
	}
	return &Error{Code: CodeUnsupported, Message: "page runtime is not installed", Hint: "reload the page"}
}

func (t *Tab) invoke(ctx context.Context, op string, args any) (json.RawMessage, error) {
	if t.Page == nil {
		return nil, &Error{Code: CodeTabClosed, Message: "tab has no page"}
	}
	payload, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	code := "window.__browserUse.invoke(" + string(payload) + ")"
	raw, err := t.evalString(ctx, code)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &Error{Code: CodeTimeout, Message: "runtime call timed out"}
		}
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	var head struct {
		OK    bool   `json:"ok"`
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &head); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad runtime result: " + err.Error()}
	}
	if !head.OK {
		if head.Error != nil {
			return nil, head.Error
		}
		return nil, &Error{Code: CodeInternal, Message: "runtime call failed"}
	}
	return json.RawMessage(raw), nil
}

func (t *Tab) evalString(ctx context.Context, code string) (string, error) {
	v, err := t.Page.EvalContext(ctx, code)
	if err != nil {
		return "", err
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("unexpected eval result %T", v)
	}
	return s, nil
}

func (t *Tab) setInputLock(on bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = t.invoke(ctx, "setInputLock", map[string]any{"on": on})
}

// Snapshot returns a semantic summary of a tab.
func (m *Manager) Snapshot(ctx context.Context, tabID, client string, opts SnapshotOptions) (*Snapshot, error) {
	t, done, err := m.begin(ctx, tabID, client, "snapshot", false)
	if err != nil {
		return nil, err
	}
	defer done()
	args := map[string]any{"scope": opts.Scope, "max_chars": opts.MaxChars, "filter": opts.Filter}
	raw, err := t.invoke(ctx, "snapshot", args)
	if err != nil {
		return nil, err
	}
	var w snapshotWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return &Snapshot{
		Snapshot: w.Text, Nodes: w.Nodes, Version: w.Version, Truncated: w.Truncated,
		URL: w.URL, Title: w.Title, Viewport: w.Viewport, Scroll: w.Scroll, NodesCount: w.NodesCount,
	}, nil
}

// Click clicks an element.
func (m *Manager) Click(ctx context.Context, tabID, client string, target Target, o ClickOptions) (*Receipt, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	t, done, err := m.begin(ctx, tabID, client, "click", true)
	if err != nil {
		return nil, err
	}
	defer done()
	if o.WaitAfter == "" {
		o.WaitAfter = "auto"
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	before := t.navSeq.Load()
	clickArgs := map[string]any{
		"target": target.toJS(), "button": o.Button, "count": o.Count,
		"modifiers": o.Modifiers, "force": o.Force,
	}
	if o.Reveal != nil {
		clickArgs["reveal"] = *o.Reveal
	}
	raw, err := t.invoke(ctx, "click", clickArgs)
	if err != nil {
		return nil, err
	}
	var w clickWire
	_ = json.Unmarshal(raw, &w)
	r := &Receipt{Clicked: w.Clicked, Revealed: w.Revealed}
	if o.WaitAfter != "none" {
		grace := 800 * time.Millisecond
		if o.WaitAfter == "load" {
			grace = 2 * time.Second
		}
		if t.waitCommit(ctx, before, grace) {
			r.Navigated = true
			if err := t.waitLoad(ctx, o.Timeout); err != nil {
				return r, err
			}
		}
	}
	r.URL = t.Page.URL()
	r.Title = t.titleValue()
	return r, nil
}

// Type enters text into an element.
func (m *Manager) Type(ctx context.Context, tabID, client string, target Target, text string, o TypeOptions) (*Receipt, error) {
	if strings.TrimSpace(target.Ref) == "" && strings.TrimSpace(target.Selector) == "" {
		return nil, &Error{Code: CodeBadRequest, Message: "ref or selector is required"}
	}
	if target.Ref != "" && target.Selector != "" {
		return nil, &Error{Code: CodeBadRequest, Message: "ref and selector are mutually exclusive"}
	}
	t, done, err := m.begin(ctx, tabID, client, "type", true)
	if err != nil {
		return nil, err
	}
	defer done()
	if o.WaitAfter == "" {
		o.WaitAfter = "auto"
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	before := t.navSeq.Load()
	raw, err := t.invoke(ctx, "type", map[string]any{
		"target": target.toJS(), "text": text, "clear": o.Clear,
		"submit": o.Submit, "slowly": o.Slowly,
	})
	if err != nil {
		return nil, err
	}
	var w typeWire
	_ = json.Unmarshal(raw, &w)
	r := &Receipt{Typed: w.Typed, Submitted: w.Submitted}
	// Typing alone rarely navigates: only wait when it may submit or the
	// caller asked for a load.
	if o.WaitAfter != "none" && (o.Submit || o.WaitAfter == "load") {
		grace := 800 * time.Millisecond
		if o.WaitAfter == "load" {
			grace = 2 * time.Second
		}
		if t.waitCommit(ctx, before, grace) {
			r.Navigated = true
			if err := t.waitLoad(ctx, o.Timeout); err != nil {
				return r, err
			}
		}
	}
	r.URL = t.Page.URL()
	r.Title = t.titleValue()
	return r, nil
}

// Press sends a key or chord to the page.
func (m *Manager) Press(ctx context.Context, tabID, client, key string, target *Target, timeout time.Duration) (*Receipt, error) {
	t, done, err := m.begin(ctx, tabID, client, "press", true)
	if err != nil {
		return nil, err
	}
	defer done()
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	before := t.navSeq.Load()
	args := map[string]any{"key": key}
	if target != nil {
		if err := target.Validate(); err != nil {
			return nil, err
		}
		args["target"] = target.toJS()
	}
	raw, err := t.invoke(ctx, "press", args)
	if err != nil {
		return nil, err
	}
	var w pressWire
	_ = json.Unmarshal(raw, &w)
	r := &Receipt{Pressed: w.Pressed}
	if t.waitCommit(ctx, before, 800*time.Millisecond) {
		r.Navigated = true
		if err := t.waitLoad(ctx, timeout); err != nil {
			return r, err
		}
	}
	r.URL = t.Page.URL()
	return r, nil
}

// Select chooses options of a <select>.
func (m *Manager) Select(ctx context.Context, tabID, client string, target Target, values []string, timeout time.Duration) (*Receipt, error) {
	if strings.TrimSpace(target.Ref) == "" && strings.TrimSpace(target.Selector) == "" {
		return nil, &Error{Code: CodeBadRequest, Message: "ref or selector is required"}
	}
	t, done, err := m.begin(ctx, tabID, client, "select", true)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "select", map[string]any{"target": target.toJS(), "values": values})
	if err != nil {
		return nil, err
	}
	var w selectWire
	_ = json.Unmarshal(raw, &w)
	return &Receipt{Selected: w.Selected, URL: t.Page.URL()}, nil
}

// Scroll scrolls the page or to an element.
func (m *Manager) Scroll(ctx context.Context, tabID, client, direction string, amount float64, target *Target) (map[string]any, error) {
	t, done, err := m.begin(ctx, tabID, client, "scroll", true)
	if err != nil {
		return nil, err
	}
	defer done()
	args := map[string]any{"direction": direction, "amount": amount}
	if target != nil {
		args["to"] = target.toJS()
	}
	raw, err := t.invoke(ctx, "scroll", args)
	if err != nil {
		return nil, err
	}
	var w struct {
		Scroll map[string]any `json:"scroll"`
	}
	_ = json.Unmarshal(raw, &w)
	return w.Scroll, nil
}

// Hover moves the pointer over an element.
func (m *Manager) Hover(ctx context.Context, tabID, client string, target Target) (*Receipt, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	t, done, err := m.begin(ctx, tabID, client, "hover", true)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "hover", map[string]any{"target": target.toJS()})
	if err != nil {
		return nil, err
	}
	var w hoverWire
	_ = json.Unmarshal(raw, &w)
	return &Receipt{Hovered: w.Hovered, URL: t.Page.URL()}, nil
}

// NavigateOptions configure navigation.
type NavigateOptions struct {
	WaitUntil string
	Timeout   time.Duration
}

// Navigate goes to a URL, or back/forward/reload/stop.
func (m *Manager) Navigate(ctx context.Context, tabID, client, action, rawURL string, o NavigateOptions) (*TabInfo, error) {
	if action == "" {
		action = "goto"
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if tabID == "" {
		tabID = m.ActiveID()
	}
	if action == "goto" {
		// Validate before taking a lease: a blocked URL must not mask the page.
		u, err := NormalizeURL(rawURL)
		if err != nil {
			return nil, err
		}
		rawURL = u
	}
	if tabID == "" && action == "goto" {
		t, err := m.NewTab(ctx, rawURL, TabOptions{Owner: OwnerAgent, Activate: true})
		if err != nil {
			return nil, err
		}
		if o.WaitUntil != "none" {
			_ = t.waitLoad(ctx, o.Timeout)
		}
		info := t.info(true)
		return &info, nil
	}
	t, done, err := m.begin(ctx, tabID, client, "navigate:"+action, true)
	if err != nil {
		return nil, err
	}
	defer done()
	before := t.navSeq.Load()
	switch action {
	case "goto":
		if err := t.Page.LoadURL(rawURL); err != nil {
			return nil, &Error{Code: CodeNavFailed, Message: err.Error()}
		}
	case "back":
		t.Page.GoBack()
	case "forward":
		t.Page.GoForward()
	case "reload":
		t.Page.Reload()
	case "stop":
		t.Page.Stop()
	default:
		return nil, &Error{Code: CodeBadRequest, Message: "unknown navigate action: " + action}
	}
	if action != "stop" && o.WaitUntil != "none" {
		if t.waitCommit(ctx, before, 3*time.Second) {
			if err := t.waitLoad(ctx, o.Timeout); err != nil {
				return nil, err
			}
		} else if action == "goto" && t.Page.IsLoading() {
			if err := t.waitLoad(ctx, o.Timeout); err != nil {
				return nil, err
			}
		}
	}
	info := t.info(m.ActiveID() == t.ID)
	return &info, nil
}

// WaitOptions configure a wait.
type WaitOptions struct {
	For     string
	Value   string
	State   string
	Timeout time.Duration
}

// WaitResult is the result of a wait.
// FrameInfo is a directly embedded iframe that answered the runtime relay.
type FrameInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

// FramesResult lists iframes or the active frame.
type FramesResult struct {
	Frames []FrameInfo `json:"frames,omitempty"`
	Total  int         `json:"total,omitempty"`
	Active string      `json:"active,omitempty"`
}

// Frames lists direct iframes or selects the frame used by page actions.
func (m *Manager) Frames(ctx context.Context, tabID, client, action, frame string) (*FramesResult, error) {
	t, done, err := m.begin(ctx, tabID, client, "frames", false)
	if err != nil {
		return nil, err
	}
	defer done()
	if action == "" {
		action = "list"
	}
	raw, err := t.invoke(ctx, "frames", map[string]any{"action": action, "frame": frame})
	if err != nil {
		return nil, err
	}
	var out FramesResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad frames result: " + err.Error()}
	}
	return &out, nil
}

// UploadFile is one file sent to a page file input.
type UploadFile struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// UploadResult describes a completed upload.
type UploadResult struct {
	Uploaded struct {
		Ref      string   `json:"ref"`
		Names    []string `json:"names"`
		Count    int      `json:"count"`
		Bytes    int      `json:"bytes"`
		Multiple bool     `json:"multiple"`
	} `json:"uploaded"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// Upload reads local files and places them in a page file input.
func (m *Manager) Upload(ctx context.Context, tabID, client string, target Target, paths []string) (*UploadResult, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, &Error{Code: CodeBadRequest, Message: "paths is required"}
	}
	if len(paths) > 10 {
		return nil, &Error{Code: CodeBadRequest, Message: "at most 10 files at once"}
	}
	files := make([]UploadFile, 0, len(paths))
	total := 0
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, &Error{Code: CodeBadRequest, Message: "file: " + err.Error()}
		}
		if !info.Mode().IsRegular() {
			return nil, &Error{Code: CodeBadRequest, Message: "not a regular file: " + p}
		}
		if info.Size() > 20<<20 {
			return nil, &Error{Code: CodeBadRequest, Message: "file is larger than 20 MiB: " + p}
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, &Error{Code: CodeBadRequest, Message: "read file: " + err.Error()}
		}
		total += len(b)
		if total > 20<<20 {
			return nil, &Error{Code: CodeBadRequest, Message: "files are larger than 20 MiB in total"}
		}
		mimeType := mime.TypeByExtension(filepath.Ext(p))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		files = append(files, UploadFile{Name: filepath.Base(p), MIME: mimeType, Data: base64.StdEncoding.EncodeToString(b)})
	}
	t, done, err := m.begin(ctx, tabID, client, "files", true)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "upload", map[string]any{"target": target.toJS(), "files": files})
	if err != nil {
		return nil, err
	}
	var out UploadResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad upload result: " + err.Error()}
	}
	return &out, nil
}

// NetworkOptions filters captured fetch/XHR traffic.
type NetworkOptions struct {
	Action      string
	URLContains string
	Method      string
	Status      int
	Since       string
	Limit       int
	IncludeBody bool
	MaxBody     int
	ID          string
	WaitMS      int
}

// NetworkEntry is one captured fetch/XHR.
type NetworkEntry struct {
	ID          string            `json:"id"`
	TS          int64             `json:"ts"`
	Type        string            `json:"type"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Status      int               `json:"status"`
	OK          bool              `json:"ok"`
	MS          int64             `json:"ms"`
	ContentType string            `json:"content_type,omitempty"`
	Size        int               `json:"size,omitempty"`
	Error       string            `json:"error,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
	ReqHeaders  map[string]string `json:"req_headers,omitempty"`
	ResHeaders  map[string]string `json:"res_headers,omitempty"`
	ReqBody     string            `json:"req_body,omitempty"`
	ResBody     string            `json:"res_body,omitempty"`
}

// NetworkResult is a network list/get/clear result.
type NetworkResult struct {
	Entries    []NetworkEntry `json:"entries,omitempty"`
	Entry      *NetworkEntry  `json:"entry,omitempty"`
	Count      int            `json:"count"`
	Dropped    int            `json:"dropped"`
	TotalBytes int            `json:"total_bytes"`
	Cleared    int            `json:"cleared,omitempty"`
}

// Network returns requests captured by the page runtime.
func (m *Manager) Network(ctx context.Context, tabID, client string, opts NetworkOptions) (*NetworkResult, error) {
	t, done, err := m.begin(ctx, tabID, client, "network", false)
	if err != nil {
		return nil, err
	}
	defer done()
	if opts.Action == "" {
		opts.Action = "list"
	}
	if opts.Action == "wait" {
		return t.waitNetwork(ctx, opts)
	}
	args := map[string]any{
		"action":       opts.Action,
		"url_contains": opts.URLContains,
		"method":       opts.Method,
		"since":        opts.Since,
		"limit":        opts.Limit,
		"include_body": opts.IncludeBody,
		"max_body":     opts.MaxBody,
		"id":           opts.ID,
	}
	if opts.Status > 0 {
		args["status"] = opts.Status
	}
	raw, err := t.invoke(ctx, "network", args)
	if err != nil {
		return nil, err
	}
	var res NetworkResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad network result: " + err.Error()}
	}
	return &res, nil
}

func (t *Tab) waitNetwork(ctx context.Context, opts NetworkOptions) (*NetworkResult, error) {
	wait := time.Duration(opts.WaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 5 * time.Second
	}
	if wait > 120*time.Second {
		wait = 120 * time.Second
	}
	started := time.Now()
	deadline := started.Add(wait)
	baseline := opts.Since
	if baseline == "" {
		raw, err := t.invoke(ctx, "network", map[string]any{"action": "list", "limit": 1})
		if err != nil {
			return nil, err
		}
		var page NetworkResult
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, &Error{Code: CodeInternal, Message: err.Error()}
		}
		if len(page.Entries) > 0 {
			baseline = page.Entries[len(page.Entries)-1].ID
		}
	}
	fetch := func(id string, page *NetworkResult) (*NetworkResult, error) {
		getArgs := map[string]any{"action": "get", "id": id}
		if opts.MaxBody > 0 {
			getArgs["max_body"] = opts.MaxBody
		}
		raw, err := t.invoke(ctx, "network", getArgs)
		if err != nil {
			return nil, err
		}
		var got NetworkResult
		if err := json.Unmarshal(raw, &got); err != nil {
			return nil, &Error{Code: CodeInternal, Message: err.Error()}
		}
		if page != nil {
			got.Count = page.Count
			got.Dropped = page.Dropped
			got.TotalBytes = page.TotalBytes
		}
		return &got, nil
	}
	// A click that triggered the request usually completes before the caller
	// can issue network wait. Without an explicit since, accept a match from
	// the last five seconds.
	if opts.Since == "" {
		args := map[string]any{
			"action": "list", "limit": 20,
			"url_contains": opts.URLContains, "method": opts.Method,
		}
		if opts.Status > 0 {
			args["status"] = opts.Status
		}
		raw, err := t.invoke(ctx, "network", args)
		if err == nil {
			var page NetworkResult
			if json.Unmarshal(raw, &page) == nil {
				cutoff := started.UnixMilli() - 5000
				var recent *NetworkEntry
				for i := range page.Entries {
					if page.Entries[i].TS >= cutoff {
						recent = &page.Entries[i]
					}
				}
				if recent != nil {
					return fetch(recent.ID, &page)
				}
			}
		}
	}
	for {
		args := map[string]any{
			"action": "list", "since": baseline, "limit": 20,
			"url_contains": opts.URLContains, "method": opts.Method,
		}
		if opts.Status > 0 {
			args["status"] = opts.Status
		}
		raw, err := t.invoke(ctx, "network", args)
		if err != nil {
			return nil, err
		}
		var page NetworkResult
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, &Error{Code: CodeInternal, Message: err.Error()}
		}
		if len(page.Entries) > 0 {
			return fetch(page.Entries[0].ID, &page)
		}
		if time.Now().After(deadline) {
			return nil, &Error{Code: CodeTimeout, Message: "no matching request within " + wait.String(), Hint: "check the URL filter or increase timeout_ms"}
		}
		select {
		case <-ctx.Done():
			return nil, &Error{Code: CodeTimeout, Message: "request wait canceled"}
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type WaitResult struct {
	Matched   bool   `json:"matched"`
	ElapsedMS int64  `json:"elapsed_ms"`
	URL       string `json:"url,omitempty"`
	Title     string `json:"title,omitempty"`
	Path      string `json:"path,omitempty"`
}

// Wait waits for a load, URL, selector, text or time.
func (m *Manager) Wait(ctx context.Context, tabID, client string, o WaitOptions) (*WaitResult, error) {
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	start := time.Now()
	t, done, err := m.begin(ctx, tabID, client, "wait:"+o.For, false)
	if err != nil {
		return nil, err
	}
	defer done()
	deadline := time.Now().Add(o.Timeout)
	check := func() (bool, error) {
		switch o.For {
		case "load", "idle":
			if t.Page.IsLoading() {
				return false, nil
			}
			if o.For == "idle" {
				time.Sleep(300 * time.Millisecond)
			}
			return true, nil
		case "url":
			return matchURL(o.Value, t.Page.URL()), nil
		case "selector":
			if strings.HasPrefix(o.Value, "text=") {
				lit := strings.Trim(strings.TrimSpace(strings.TrimPrefix(o.Value, "text=")), `"'`)
				b, _ := json.Marshal(lit)
				v, err := t.Page.EvalContext(ctx, "String(document.body ? document.body.innerText : '').includes("+string(b)+")")
				if err != nil {
					return false, nil
				}
				found, _ := v.(bool)
				if o.State == "hidden" {
					return !found, nil
				}
				return found, nil
			}
			target := Target{Selector: o.Value}
			raw, err := t.invoke(ctx, "query", map[string]any{"target": target.toJS(), "state": o.State})
			if err != nil {
				if e, ok := err.(*Error); ok && e.Code == CodeNotFound {
					return false, nil
				}
				return false, err
			}
			var w struct {
				Found bool `json:"found"`
			}
			if err := json.Unmarshal(raw, &w); err != nil {
				return false, err
			}
			return w.Found, nil
		case "download":
			// A download triggered just before the wait is still useful:
			// allow a short grace window.
			cutoff := start.Add(-5 * time.Second)
			for _, d := range m.Downloads() {
				if !d.At.Before(cutoff) && d.Err == "" {
					return true, nil
				}
			}
			return false, nil
		case "text":
			b, _ := json.Marshal(o.Value)
			v, err := t.Page.EvalContext(ctx, "String(document.body ? document.body.innerText : '').includes("+string(b)+")")
			if err != nil {
				return false, nil
			}
			ok, _ := v.(bool)
			return ok, nil
		case "time":
			var ms int64
			if _, err := fmt.Sscan(strings.TrimSpace(o.Value), &ms); err != nil || ms <= 0 {
				// v1 曾静默回退 500ms：非法值当成 0.5s 会让调用方以为等够了。
				return false, &Error{Code: CodeBadRequest, Message: "wait for=time needs value in milliseconds", Hint: "e.g. value=\"1500\""}
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return true, nil
		default:
			return false, &Error{Code: CodeBadRequest, Message: "unknown wait: " + o.For}
		}
	}
	for {
		ok, err := check()
		if err != nil {
			return nil, err
		}
		if ok {
			res := &WaitResult{Matched: true, ElapsedMS: time.Since(start).Milliseconds(), URL: t.Page.URL(), Title: t.titleValue()}
			if o.For == "download" {
				cutoff := start.Add(-5 * time.Second)
				for _, d := range m.Downloads() {
					if !d.At.Before(cutoff) && d.Err == "" {
						res.Path = d.Path
					}
				}
			}
			return res, nil
		}
		if time.Now().After(deadline) {
			return nil, &Error{Code: CodeTimeout, Message: "wait for " + o.For + " timed out", Details: map[string]any{"elapsed_ms": time.Since(start).Milliseconds()}}
		}
		select {
		case <-ctx.Done():
			return nil, &Error{Code: CodeTimeout, Message: ctx.Err().Error()}
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func matchURL(pattern, current string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	if strings.ContainsAny(pattern, "*?") {
		var b strings.Builder
		b.WriteString("(?i)")
		for _, r := range pattern {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		if re, err := regexp.Compile(b.String()); err == nil && re.MatchString(current) {
			return true
		}
	}
	return strings.Contains(current, pattern)
}
