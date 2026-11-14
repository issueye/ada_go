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
	Diff       bool           `json:"diff,omitempty"`
	Changes    int            `json:"changes,omitempty"`
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
	Diff     bool   `json:"diff,omitempty"`
}

type snapshotWire struct {
	Text       string         `json:"text"`
	Nodes      []SnapshotNode `json:"nodes"`
	Version    int            `json:"version"`
	Truncated  bool           `json:"truncated"`
	Diff       bool           `json:"diff"`
	Changes    int            `json:"changes"`
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
	// Warnings collects non-fatal problems, e.g. an action result the page
	// runtime returned in an unexpected shape.
	Warnings []string `json:"warnings,omitempty"`
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
	Answer       any     `json:"answer,omitempty"`
	TS           int64   `json:"ts"`
}

// DialogOptions select a dialogs action.
type DialogOptions struct {
	Action string
	Text   string
}

// DialogResult is the dialogs list/clear/accept/dismiss result.
type DialogResult struct {
	Dialogs []Dialog       `json:"dialogs"`
	Policy  map[string]any `json:"policy,omitempty"`
	Cleared int            `json:"cleared,omitempty"`
}

// Dialogs lists the alert/confirm/prompt calls the page made, or sets the
// policy used for the next confirm/prompt. Page dialogs are synchronous, so the
// policy is applied immediately instead of blocking the page for a human.
func (m *Manager) Dialogs(ctx context.Context, tabID, client string, opts DialogOptions) (*DialogResult, error) {
	t, done, err := m.begin(ctx, tabID, client, "dialogs", false)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "dialogs", func() map[string]any {
		args := map[string]any{"action": opts.Action}
		if opts.Text != "" {
			args["text"] = opts.Text
		}
		return args
	}())
	if err != nil {
		return nil, err
	}
	var out DialogResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return &out, nil
}

func (t *Tab) dialogList(ctx context.Context) ([]Dialog, error) {
	raw, err := t.invoke(ctx, "dialogs", map[string]any{"action": "list"})
	if err != nil {
		return nil, err
	}
	var out DialogResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return out.Dialogs, nil
}

// ReadResult is the readable text of a page or element.
type ReadResult struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Format    string `json:"format,omitempty"`
	URL       string `json:"url"`
	Title     string `json:"title"`
	Chars     int    `json:"chars"`
}

// Read returns the page's readable text, or links/tables/markdown views.
func (m *Manager) Read(ctx context.Context, tabID, client, selector, format string, maxChars int) (*ReadResult, error) {
	t, done, err := m.begin(ctx, tabID, client, "read", false)
	if err != nil {
		return nil, err
	}
	defer done()
	raw, err := t.invoke(ctx, "read", map[string]any{"selector": selector, "format": format, "max_chars": maxChars})
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
	if mutating {
		if m.UserControlled() {
			return nil, nil, &Error{Code: CodeControlLocked, Message: "the human has taken over", Hint: "ask the human to resume"}
		}
		if m.Paused() {
			return nil, nil, &Error{Code: CodeControlLocked, Message: "the agent is paused", Hint: "ask the human to resume"}
		}
	}
	t := m.Tab(tabID)
	if t == nil {
		return nil, nil, errorf(CodeInvalidTab, "no tab %q", tabID)
	}
	if !m.UserControlled() && !m.Paused() {
		var err error
		t, err = m.Acquire(tabID, client)
		if err != nil {
			return nil, nil, err
		}
	}
	if m.cfg.Agent.FollowAgent && !m.Paused() && m.ActiveID() != t.ID {
		_ = m.Activate(t.ID)
	}
	t.actionMu.Lock()
	t.setAction(action)
	t.actionBusy.Store(true)
	if err := t.ensureRuntime(ctx); err != nil {
		t.actionMu.Unlock()
		t.actionBusy.Store(false)
		return nil, nil, err
	}
	if m.cfg.Control.Mask && !m.UserControlled() && !m.Paused() {
		t.setInputLock(true)
	}
	if mutating && t.consumeHumanBlocked(ctx) {
		// 遮罩期有人类输入被拦(用户在 agent 工作时试图点击页面)。发事件让
		// 模型决定是否释放租约让用户先操作;不自动暂停——误触打断整个自动化
		// 流程的代价比打断一次点击更高。
		m.emit("control.human_active", t.ID, "")
	}
	m.notify()
	// While the action holds the slot, keep its lease alive: expireLoop would
	// otherwise release a lease whose idle window ended mid-action, drop the
	// mask and unlock the page under a still-running click or wait.
	stop := make(chan struct{})
	if !m.UserControlled() && !m.Paused() {
		go t.leaseHeartbeat(client, m.heartbeatInterval(), m.leaseDuration(), stop)
	}
	done := func() {
		t.actionMu.Unlock()
		t.actionBusy.Store(false)
		close(stop)
		m.notify()
	}
	return t, done, nil
}

func (t *Tab) ensureRuntime(ctx context.Context) error {
	if t.closing.Load() {
		return &Error{Code: CodeTabClosed, Message: "tab is closed"}
	}
	if t.runtimeReady.Load() {
		return nil
	}
	src := t.runtimeSource()
	for i := 0; i < 3; i++ {
		if v, err := t.Page.EvalContext(ctx, "typeof window.__browserUse"); err == nil && v == "object" {
			t.runtimeReady.Store(true)
			return nil
		}
		if _, err := t.Page.EvalContext(ctx, src); err == nil {
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

// runtimeSource re-evaluates the exact preload source this tab was created
// with, so a restored page still shares the process frame token.
func (t *Tab) runtimeSource() string {
	if t.frameToken == "" {
		return runtime.Source()
	}
	return runtime.SourceWithToken(t.frameToken)
}

func (t *Tab) invoke(ctx context.Context, op string, args any) (json.RawMessage, error) {
	// closing 先判:关闭中的 tab,Page 还没清(避免与读者数据竞争),但视图可能
	// 已经 destroy,再 eval 只会拿到 mygo 内部错误,而不是这里的语义化码。
	if t.closing.Load() {
		return nil, &Error{Code: CodeTabClosed, Message: "tab is closed"}
	}
	if t.Page == nil {
		return nil, &Error{Code: CodeTabClosed, Message: "tab has no page"}
	}
	payload, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	token, err := json.Marshal(t.frameToken)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	// The token is the runtime's per-process secret; it lets Go drive iframe
	// relay and the input mask, while page scripts calling the same invoke
	// without it are rejected.
	code := "window.__browserUse.invoke(" + string(payload) + "," + string(token) + ")"
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

// decodeWire unmarshals an action result into its wire shape and records a
// warning on the receipt when the runtime returned something unreadable.
// A silently dropped result (_ = json.Unmarshal) turned the receipt into zero
// values that read like a successful no-op.
func decodeWire(r *Receipt, raw json.RawMessage, v any) {
	if err := json.Unmarshal(raw, v); err != nil {
		r.Warnings = append(r.Warnings, "unreadable action result: "+err.Error())
	}
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
	args := map[string]any{"scope": opts.Scope, "max_chars": opts.MaxChars, "filter": opts.Filter, "diff": opts.Diff}
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
		Diff: w.Diff, Changes: w.Changes,
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
	r := &Receipt{}
	var w clickWire
	decodeWire(r, raw, &w)
	r.Clicked, r.Revealed = w.Clicked, w.Revealed
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
	r := &Receipt{}
	var w typeWire
	decodeWire(r, raw, &w)
	r.Typed, r.Submitted = w.Typed, w.Submitted
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
	r := &Receipt{}
	var w pressWire
	decodeWire(r, raw, &w)
	r.Pressed = w.Pressed
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
	r := &Receipt{}
	var w selectWire
	decodeWire(r, raw, &w)
	r.Selected = w.Selected
	r.URL = t.Page.URL()
	return r, nil
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
	if err := json.Unmarshal(raw, &w); err != nil {
		// 没有 Receipt 可挂 warning:滚动结果不可读时如实报 internal。
		return nil, &Error{Code: CodeInternal, Message: "bad scroll result: " + err.Error()}
	}
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
	r := &Receipt{}
	var w hoverWire
	decodeWire(r, raw, &w)
	r.Hovered = w.Hovered
	r.URL = t.Page.URL()
	return r, nil
}

// DragOptions configure a drag between two elements.
type DragOptions struct {
	HTML5  bool   // force the HTML5 DnD event sequence
	Text   string // payload for the HTML5 DataTransfer
	Steps  int    // pointer-segment steps between source and target
	WaitMs int    // actionability wait override (ms)
}

// DragResult reports a completed drag.
type DragResult struct {
	Dragged bool          `json:"dragged"`
	From    *SnapshotNode `json:"from,omitempty"`
	To      *SnapshotNode `json:"to,omitempty"`
	HTML5   bool          `json:"html5"`
	URL     string        `json:"url,omitempty"`
	Title   string        `json:"title,omitempty"`
}

type dragWire struct {
	Dragged bool          `json:"dragged"`
	From    *SnapshotNode `json:"from"`
	To      *SnapshotNode `json:"to"`
	HTML5   bool          `json:"html5"`
	URL     string        `json:"url"`
	Title   string        `json:"title"`
}

// Drag drags from one element to another: the HTML5 DnD sequence
// (dragstart/dragover/drop) runs when the source is draggable or a text
// payload is given, and the pointer step sequence always runs so custom
// (mousemove-driven) drag implementations see it too.
func (m *Manager) Drag(ctx context.Context, tabID, client string, from, to Target, o DragOptions) (*DragResult, error) {
	if err := from.Validate(); err != nil {
		return nil, wrapTargetErr(err, "from")
	}
	if err := to.Validate(); err != nil {
		return nil, wrapTargetErr(err, "to")
	}
	t, done, err := m.begin(ctx, tabID, client, "drag", true)
	if err != nil {
		return nil, err
	}
	defer done()
	args := map[string]any{
		"from": from.toJS(),
		"to":   to.toJS(),
	}
	if o.HTML5 {
		args["html5"] = true
	}
	if o.Text != "" {
		args["text"] = o.Text
	}
	if o.Steps > 0 {
		args["steps"] = o.Steps
	}
	if o.WaitMs > 0 {
		args["wait_ms"] = o.WaitMs
	}
	raw, err := t.invoke(ctx, "drag", args)
	if err != nil {
		return nil, err
	}
	var w dragWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad drag result: " + err.Error()}
	}
	return &DragResult{
		Dragged: w.Dragged, From: w.From, To: w.To, HTML5: w.HTML5,
		URL: w.URL, Title: w.Title,
	}, nil
}

// wrapTargetErr marks which of the two drag targets failed validation.
func wrapTargetErr(err error, which string) error {
	if be, ok := err.(*Error); ok {
		return &Error{Code: be.Code, Message: which + ": " + be.Message, Hint: be.Hint, Details: be.Details}
	}
	return err
}

// StorageOptions select a web-storage action.
type StorageOptions struct {
	Kind   string `json:"kind"`   // local (default) or session
	Action string `json:"action"` // list (default) | get | set | clear
	Key    string `json:"key"`
	Value  string `json:"value"`
}

// StorageResult is a web-storage list/get/set/clear result.
type StorageResult struct {
	Kind      string         `json:"kind"`
	Count     int            `json:"count,omitempty"`
	Keys      []StorageEntry `json:"keys,omitempty"`
	Total     int            `json:"total_bytes,omitempty"`
	Key       string         `json:"key,omitempty"`
	Found     bool           `json:"found,omitempty"`
	Value     string         `json:"value,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
	Bytes     int            `json:"bytes,omitempty"`
	Removed   bool           `json:"removed,omitempty"`
	Cleared   int            `json:"cleared,omitempty"`
}

// StorageEntry is one storage key with its value size.
type StorageEntry struct {
	Key   string `json:"key"`
	Bytes int    `json:"bytes"`
}

// Storage reads or writes the page origin's localStorage/sessionStorage.
// set/clear mutate the page and are gated like other write actions.
func (m *Manager) Storage(ctx context.Context, tabID, client string, opts StorageOptions) (*StorageResult, error) {
	mutating := opts.Action == "set" || opts.Action == "clear"
	t, done, err := m.begin(ctx, tabID, client, "storage:"+opts.Action, mutating)
	if err != nil {
		return nil, err
	}
	defer done()
	args := map[string]any{"kind": opts.Kind, "action": opts.Action}
	if opts.Key != "" {
		args["key"] = opts.Key
	}
	if opts.Value != "" {
		args["value"] = opts.Value
	}
	raw, err := t.invoke(ctx, "storage", args)
	if err != nil {
		return nil, err
	}
	var out StorageResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad storage result: " + err.Error()}
	}
	return &out, nil
}

// consumeHumanBlocked reports (and resets) whether the input lock intercepted
// human input since the last action. Non-fatal on failure: a page without the
// runtime answer is treated as "no human activity".
func (t *Tab) consumeHumanBlocked(ctx context.Context) bool {
	raw, err := t.invoke(ctx, "humanBlocked", map[string]any{"action": "consume"})
	if err != nil {
		return false
	}
	var out struct {
		Blocked bool `json:"blocked"`
	}
	return json.Unmarshal(raw, &out) == nil && out.Blocked
}

// NavigateOptions configure navigation.
type NavigateOptions struct {
	WaitUntil string
	Timeout   time.Duration
	// Human marks a navigation started by the browser UI (address bar, back,
	// forward, reload). Human navigation must not acquire an agent lease or
	// engage the input lock: otherwise typing a URL would make the browser look
	// agent-controlled and lock the page for the idle-release period.
	Human bool
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
	if o.Human {
		return m.navigateHuman(ctx, tabID, action, rawURL, o)
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
	return m.performNavigation(ctx, t, action, rawURL, o)
}

// navigateHuman runs a navigation started by the human. It deliberately skips
// begin(): no lease, no input lock and no navigation action recorded as an agent
// action. The tab is still serialized with agent actions so a human reload
// cannot race an in-flight click on the same page.
func (m *Manager) navigateHuman(ctx context.Context, tabID, action, rawURL string, o NavigateOptions) (*TabInfo, error) {
	if tabID == "" {
		if action != "goto" {
			return nil, &Error{Code: CodeNoTab, Message: "no active tab", Hint: "open a tab first"}
		}
		t, err := m.NewTab(ctx, rawURL, TabOptions{Owner: OwnerUser, Activate: true})
		if err != nil {
			return nil, err
		}
		if o.WaitUntil != "none" {
			_ = t.waitLoad(ctx, o.Timeout)
		}
		info := t.info(true)
		return &info, nil
	}
	t := m.Tab(tabID)
	if t == nil {
		return nil, errorf(CodeInvalidTab, "no tab %q", tabID)
	}
	if t.Page == nil {
		return nil, &Error{Code: CodeTabClosed, Message: "tab has no page"}
	}
	t.actionMu.Lock()
	defer m.notify()
	defer t.actionMu.Unlock()
	return m.performNavigation(ctx, t, action, rawURL, o)
}

// performNavigation applies a navigation and waits for the resulting load. It
// is shared by agent and human paths; callers own the tab action lock.
func (m *Manager) performNavigation(ctx context.Context, t *Tab, action, rawURL string, o NavigateOptions) (*TabInfo, error) {
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

// allowedUploadPath resolves a local path and enforces security.upload_dirs
// (the write-side boundary; read_file is intentionally unrestricted).
func (m *Manager) allowedUploadPath(raw string) (string, error) {
	p := expandLocalPath(raw)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	dirs := m.cfg.Security.UploadDirs
	if len(dirs) == 0 {
		return p, nil
	}
	for _, dir := range dirs {
		root := expandLocalPath(dir)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return p, nil
		}
	}
	return "", &Error{
		Code:    CodeBlockedAction,
		Message: "file is outside security.upload_dirs: " + raw,
		Hint:    "add its directory to security.upload_dirs in config.json",
	}
}

var pathEnvVar = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)

func expandLocalPath(p string) string {
	p = pathEnvVar.ReplaceAllStringFunc(p, func(m string) string {
		return os.Getenv(m[1 : len(m)-1])
	})
	p = os.ExpandEnv(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[1:])
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
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
	for _, rawPath := range paths {
		p, err := m.allowedUploadPath(rawPath)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, &Error{Code: CodeBadRequest, Message: "file: " + err.Error()}
		}
		if !info.Mode().IsRegular() {
			return nil, &Error{Code: CodeBadRequest, Message: "not a regular file: " + rawPath}
		}
		if info.Size() > 20<<20 {
			return nil, &Error{Code: CodeBadRequest, Message: "file is larger than 20 MiB: " + rawPath}
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

// NetworkResult is a network list/get/clear/wait result.
type NetworkResult struct {
	Entries    []NetworkEntry `json:"entries,omitempty"`
	Entry      *NetworkEntry  `json:"entry,omitempty"`
	Count      int            `json:"count"`
	Dropped    int            `json:"dropped"`
	TotalBytes int            `json:"total_bytes"`
	Cleared    int            `json:"cleared,omitempty"`
	Matched    bool           `json:"matched,omitempty"`
	ElapsedMS  int64          `json:"elapsed_ms,omitempty"`
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

// waitNetwork 等一条匹配的请求。等待在页面里做(一个挂起 promise,由
// netRecord 直接 resolve),不再 100ms 轮询 —— 与 console wait 同一套机制;
// 页面定时器/人类点击触发的请求照样命中。超时是 matched:false,这里转成
// 与旧轮询相同的 timeout 错误(工具契约不变)。
func (t *Tab) waitNetwork(ctx context.Context, opts NetworkOptions) (*NetworkResult, error) {
	wait := time.Duration(opts.WaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 5 * time.Second
	}
	if wait > 120*time.Second {
		wait = 120 * time.Second
	}
	args := map[string]any{
		"action":  "wait",
		"wait_ms": wait.Milliseconds(),
	}
	if opts.URLContains != "" {
		args["url_contains"] = opts.URLContains
	}
	if opts.Method != "" {
		args["method"] = opts.Method
	}
	if opts.Since != "" {
		args["since"] = opts.Since
	}
	if opts.Status > 0 {
		args["status"] = opts.Status
	}
	if opts.MaxBody > 0 {
		args["max_body"] = opts.MaxBody
	}
	raw, err := t.invoke(ctx, "network", args)
	if err != nil {
		return nil, err
	}
	var page NetworkResult
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	if !page.Matched {
		return nil, &Error{Code: CodeTimeout, Message: "no matching request within " + wait.String(), Hint: "check the URL filter or increase timeout_ms"}
	}
	return &page, nil
}

// ConsoleOptions filters console output captured by the page runtime.
type ConsoleOptions struct {
	Action       string
	Level        string
	TextContains string
	Source       string
	Since        string
	Limit        int
	ID           string
	WaitMS       int
}

// ConsoleEntry is one console line or page error.
type ConsoleEntry struct {
	ID        string `json:"id"`
	TS        int64  `json:"ts"`
	Level     string `json:"level"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	URL       string `json:"url,omitempty"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ConsoleResult is a console list/get/clear/wait result.
type ConsoleResult struct {
	Entries    []ConsoleEntry `json:"entries,omitempty"`
	Entry      *ConsoleEntry  `json:"entry,omitempty"`
	Count      int            `json:"count"`
	Dropped    int            `json:"dropped"`
	TotalBytes int            `json:"total_bytes"`
	Cleared    int            `json:"cleared,omitempty"`
	Matched    bool           `json:"matched"`
	ElapsedMS  int64          `json:"elapsed_ms,omitempty"`
}

// Console returns console output captured in the page: console.* calls,
// uncaught errors and unhandled rejections. wait blocks until a matching line
// arrives or the budget runs out; an empty result is not an error.
func (m *Manager) Console(ctx context.Context, tabID, client string, opts ConsoleOptions) (*ConsoleResult, error) {
	if !validConsoleLevel(opts.Level) {
		return nil, &Error{Code: CodeBadRequest, Message: "level must be log/info/warn/error/debug"}
	}
	if !validConsoleSource(opts.Source) {
		return nil, &Error{Code: CodeBadRequest, Message: "source must be console/exception/rejection"}
	}
	t, done, err := m.begin(ctx, tabID, client, "console", false)
	if err != nil {
		return nil, err
	}
	defer done()
	if opts.Action == "" {
		opts.Action = "list"
	}
	if opts.Action == "wait" {
		return t.waitConsole(ctx, opts)
	}
	raw, err := t.invoke(ctx, "console", consoleArgs(opts.Action, opts))
	if err != nil {
		return nil, err
	}
	var res ConsoleResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad console result: " + err.Error()}
	}
	res.Matched = len(res.Entries) > 0 || res.Entry != nil
	return &res, nil
}

func validConsoleLevel(level string) bool {
	switch level {
	case "", "log", "info", "warn", "error", "debug":
		return true
	}
	return false
}

func validConsoleSource(source string) bool {
	switch source {
	case "", "console", "exception", "rejection":
		return true
	}
	return false
}

func consoleArgs(action string, opts ConsoleOptions) map[string]any {
	return map[string]any{
		"action":        action,
		"level":         opts.Level,
		"text_contains": opts.TextContains,
		"source":        opts.Source,
		"since":         opts.Since,
		"limit":         opts.Limit,
		"id":            opts.ID,
	}
}

func (t *Tab) waitConsole(ctx context.Context, opts ConsoleOptions) (*ConsoleResult, error) {
	wait := time.Duration(opts.WaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 5 * time.Second
	}
	if wait > 120*time.Second {
		wait = 120 * time.Second
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	started := time.Now()
	// Without a caller cursor, a line logged while the triggering action was
	// running (last five seconds) counts as a hit. This keeps "click, then
	// console wait" working even though the click already finished.
	if opts.Since == "" {
		page, err := t.consoleList(ctx, opts)
		if err != nil {
			return nil, err
		}
		cutoff := started.UnixMilli() - 5000
		var recent []ConsoleEntry
		for _, e := range page.Entries {
			if e.TS >= cutoff {
				recent = append(recent, e)
			}
		}
		if len(recent) > 0 {
			page.Entries = recent
			page.Matched = true
			page.ElapsedMS = time.Since(started).Milliseconds()
			return page, nil
		}
		if len(page.Entries) > 0 {
			opts.Since = page.Entries[len(page.Entries)-1].ID
		}
	}
	// The wait happens inside the page: one pending promise resolves as soon as
	// a matching line is recorded, instead of Go polling eval every 100 ms.
	args := consoleArgs("wait", opts)
	args["timeout_ms"] = int(wait / time.Millisecond)
	raw, err := t.invoke(ctx, "console", args)
	if err != nil {
		return nil, err
	}
	var page ConsoleResult
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "bad console result: " + err.Error()}
	}
	page.Matched = len(page.Entries) > 0
	page.ElapsedMS = time.Since(started).Milliseconds()
	return &page, nil
}

func (t *Tab) consoleList(ctx context.Context, opts ConsoleOptions) (*ConsoleResult, error) {
	raw, err := t.invoke(ctx, "console", consoleArgs("list", ConsoleOptions{
		Limit: opts.Limit, Level: opts.Level, TextContains: opts.TextContains, Source: opts.Source,
	}))
	if err != nil {
		return nil, err
	}
	var page ConsoleResult
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, &Error{Code: CodeInternal, Message: err.Error()}
	}
	return &page, nil
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
	// Event-based waits only consider events that happen after this call; the
	// begin() control.locked event above must not satisfy wait for=control.
	eventAfter := m.EventCursor()
	dialogBaseline := 0
	if o.For == "dialog" {
		if list, err := t.dialogList(ctx); err == nil {
			// A dialog triggered by the action just before wait should still
			// count, mirroring the 5s grace used by wait for=download.
			cutoff := time.Now().Add(-5 * time.Second).UnixMilli()
			for _, d := range list {
				if d.TS < cutoff {
					dialogBaseline++
				}
			}
		}
	}
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
		case "tab", "control":
			kinds := map[string]bool{
				"tab.created": true, "tab.closed": true, "tab.activated": true,
			}
			if o.For == "control" {
				kinds = map[string]bool{
					"control.takeover": true, "control.released": true,
					"control.resumed": true, "control.paused": true,
				}
			}
			events, cursor := m.Events(ctx, eventAfter, 50*time.Millisecond, kinds)
			eventAfter = cursor
			return len(events) > 0, nil
		case "dialog":
			list, err := t.dialogList(ctx)
			if err != nil {
				return false, err
			}
			return len(list) > dialogBaseline, nil
		case "url":
			// Read location.href from the page: Go's cached Page.URL() may lag
			// behind pushState/hash changes in SPAs.
			raw, err := t.invoke(ctx, "info", map[string]any{})
			if err != nil {
				return false, nil
			}
			var info struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(raw, &info) != nil {
				return false, nil
			}
			return matchURL(o.Value, info.URL), nil
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
