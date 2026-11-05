// Package script runs agent-authored JavaScript/TypeScript with moejs and a
// browser API.
package script

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"browseruse/internal/browser"
	"browseruse/internal/config"

	_ "embed"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/eventloop"
)

//go:embed prelude.js
var preludeSource string

var (
	preludeOnce sync.Once
	preludeMod  *moejs.Script
	preludeErr  error
)

func prelude() (*moejs.Script, error) {
	preludeOnce.Do(func() {
		preludeMod, preludeErr = moejs.CompileScript("prelude.js", preludeSource)
	})
	return preludeMod, preludeErr
}

// pruneSourcesOnce 每进程只清一次:脚本源码按天分目录,首次脚本运行时清理
// 保留期之外的旧目录即可,之后无需反复扫描。
var pruneSourcesOnce sync.Once

// PruneSources removes script source directories older than days. 目录名即
// 日期(YYYYMMDD),字典序等于时间序。keep_sources 关掉后旧目录也照清,
// 否则开关留下的历史文件会永远留在盘上。
func PruneSources(days int) {
	pruneSourcesOnce.Do(func() {
		dir, err := config.Dir()
		if err != nil {
			return
		}
		pruneSourceDirs(filepath.Join(dir, "scripts"), days)
	})
}

// pruneSourceDirs 只清理「目录名是日期且早于保留期」的目录;独立出来便于
// 用临时目录测试,不碰真实配置目录。
func pruneSourceDirs(root string, days int) {
	if days <= 0 {
		days = 7
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format("20060102")
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if _, err := time.Parse("20060102", name); err != nil || name >= cutoff {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

// Executor runs one tool call for a script.
type Executor interface {
	Call(ctx context.Context, clientID, name string, args json.RawMessage) (any, error)
}

// Request is a script invocation.
type Request struct {
	Source          string
	Language        string // "js" (default) or "ts"
	Args            json.RawMessage
	Timeout         time.Duration
	MaxOutput       int
	ClientID        string
	ReleaseOnFinish bool
}

// LogLine is one console call.
type LogLine struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// Action is one tool call a script made.
type Action struct {
	Tool string `json:"tool"`
	Tab  string `json:"tab,omitempty"`
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
	MS   int64  `json:"ms"`
}

// Result is a finished script.
type Result struct {
	Result     any       `json:"result"`
	Console    []LogLine `json:"console,omitempty"`
	Actions    []Action  `json:"actions,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Truncated  bool      `json:"truncated,omitempty"`
	ScriptID   string    `json:"script_id"`
}

type consoleBuf struct {
	mu        sync.Mutex
	lines     []LogLine
	bytes     int
	max       int
	truncated bool
}

func (b *consoleBuf) add(level, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bytes+len(text) > b.max {
		b.truncated = true
		return
	}
	b.lines = append(b.lines, LogLine{Level: level, Text: text})
	b.bytes += len(text)
}

func (b *consoleBuf) snapshot() ([]LogLine, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]LogLine(nil), b.lines...), b.truncated
}

type actionBuf struct {
	mu      sync.Mutex
	actions []Action
}

func (b *actionBuf) add(a Action) {
	b.mu.Lock()
	b.actions = append(b.actions, a)
	if len(b.actions) > 50 {
		b.actions = b.actions[len(b.actions)-50:]
	}
	b.mu.Unlock()
}

func (b *actionBuf) snapshot() []Action {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Action(nil), b.actions...)
}

type host struct {
	rt       *moejs.Runtime
	loop     *eventloop.Loop
	exec     Executor
	ctx      context.Context
	clientID string
	maxCalls int
	calls    int
	console  *consoleBuf
	actions  *actionBuf
}

func errEnvelope(err error) map[string]any {
	info := map[string]any{"code": browser.CodeInternal, "message": "tool call failed"}
	var be *browser.Error
	if errors.As(err, &be) {
		info["code"] = be.Code
		info["message"] = be.Message
		if be.Hint != "" {
			info["hint"] = be.Hint
		}
	} else if err != nil {
		info["message"] = err.Error()
	}
	return info
}

// call is __bu_call(name, args) -> Promise<{ok, data|error}>.
func (h *host) call(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	name := moejs.Arg(args, 0).String()
	if name == "script" {
		return h.envelope(false, nil, &browser.Error{Code: browser.CodeScriptUnsupported, Message: "scripts cannot call script"})
	}
	if h.calls >= h.maxCalls {
		return h.envelope(false, nil, &browser.Error{Code: browser.CodeScriptActions, Message: "script made too many tool calls", Hint: "split the script"})
	}
	argVal := moejs.Arg(args, 1)
	var raw json.RawMessage
	if argVal.IsUndefined() || argVal.IsNull() {
		raw = json.RawMessage("{}")
	} else {
		b, err := h.rt.AppendJSON(nil, argVal)
		if err != nil {
			return h.envelope(false, nil, &browser.Error{Code: browser.CodeBadRequest, Message: "bad arguments: " + err.Error()})
		}
		raw = json.RawMessage(b)
	}
	h.calls++
	p, settle := h.loop.NewPromise()
	go func() {
		start := time.Now()
		data, err := h.exec.Call(h.ctx, h.clientID, name, raw)
		code := ""
		if err != nil {
			var be *browser.Error
			if errors.As(err, &be) {
				code = be.Code
			} else {
				code = browser.CodeInternal
			}
		}
		h.actions.add(Action{Tool: name, OK: err == nil, Code: code, MS: time.Since(start).Milliseconds()})
		b, merr := json.Marshal(h.envelopeMap(err == nil, data, err))
		if merr != nil {
			b, _ = json.Marshal(map[string]any{"ok": false, "error": map[string]any{"code": browser.CodeInternal, "message": merr.Error()}})
		}
		settle(func(rt *moejs.Runtime) (moejs.Value, error) {
			return rt.ParseJSON(b)
		})
	}()
	return p, nil
}

func (h *host) envelopeMap(ok bool, data any, err error) map[string]any {
	m := map[string]any{"ok": ok}
	if ok {
		m["data"] = data
	} else {
		m["error"] = errEnvelope(err)
	}
	return m
}

func (h *host) envelope(ok bool, data any, err error) (moejs.Value, error) {
	b, merr := json.Marshal(h.envelopeMap(ok, data, err))
	if merr != nil {
		return moejs.Undefined(), merr
	}
	return h.rt.ParseJSON(b)
}

func (h *host) log(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
	level := moejs.Arg(args, 0).String()
	text := moejs.Arg(args, 1).String()
	h.console.add(level, text)
	return moejs.Undefined(), nil
}

// Run executes a script. The caller owns the context and the executor.
func Run(ctx context.Context, exec Executor, cfg config.ScriptConfig, req Request) (*Result, error) {
	if !cfg.Enabled {
		return nil, &browser.Error{Code: browser.CodeScriptUnsupported, Message: "scripts are disabled"}
	}
	if len(req.Source) == 0 {
		return nil, &browser.Error{Code: browser.CodeBadRequest, Message: "source is required"}
	}
	if len(req.Source) > cfg.MaxSourceBytes {
		return nil, &browser.Error{Code: browser.CodeScriptSource, Message: fmt.Sprintf("source is %d bytes; limit is %d", len(req.Source), cfg.MaxSourceBytes)}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = time.Duration(cfg.TimeoutMS) * time.Millisecond
	}
	maxTimeout := time.Duration(cfg.MaxTimeoutMS) * time.Millisecond
	if maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
	}
	lang := req.Language
	if lang == "" {
		lang = "js"
	}
	start := time.Now()
	scriptID := newID()
	if req.ClientID == "" {
		req.ClientID = "script-" + scriptID
	}
	maxOutput := req.MaxOutput
	if maxOutput <= 0 {
		maxOutput = cfg.MaxOutputBytes
	}
	if cfg.KeepSources {
		if dir, err := config.Dir(); err == nil {
			dir = filepath.Join(dir, "scripts", time.Now().Format("20060102"))
			_ = os.MkdirAll(dir, 0o755)
			ext := ".js"
			if lang == "ts" {
				ext = ".ts"
			}
			_ = os.WriteFile(filepath.Join(dir, scriptID+ext), []byte(req.Source), 0o644)
		}
	}
	PruneSources(cfg.KeepSourcesDays)
	if req.ReleaseOnFinish {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			args, _ := json.Marshal(map[string]any{"action": "release", "client": req.ClientID})
			_, _ = exec.Call(cleanup, req.ClientID, "control", args)
			cancel()
		}()
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	params := "args"
	var userScript *moejs.Script
	var err error
	if lang == "ts" {
		userScript, err = moejs.CompileFunctionTS("script.ts", params, req.Source, moejs.FunctionOptions{Async: true, Context: ctx})
	} else {
		userScript, err = moejs.CompileFunction("script.js", params, "\"use strict\";\n"+req.Source, moejs.FunctionOptions{Async: true, Context: ctx})
	}
	if err != nil {
		return nil, syntaxError(err)
	}
	pre, err := prelude()
	if err != nil {
		return nil, &browser.Error{Code: browser.CodeScriptError, Message: "prelude: " + err.Error()}
	}

	rt := moejs.NewRuntime(moejs.Options{
		MemoryLimit:        int64(cfg.MemoryLimitMB) << 20,
		DisableDynamicCode: !cfg.AllowDynamicCode,
	})
	loop, err := eventloop.New(rt, eventloop.Options{})
	if err != nil {
		return nil, &browser.Error{Code: browser.CodeScriptError, Message: err.Error()}
	}
	console := &consoleBuf{max: maxOutput}
	actions := &actionBuf{}
	h := &host{rt: rt, loop: loop, exec: exec, ctx: ctx, clientID: req.ClientID, maxCalls: cfg.MaxActions, console: console, actions: actions}
	if err := rt.SetGlobal("__bu_call", moejs.NativeFunc(h.call)); err != nil {
		return nil, &browser.Error{Code: browser.CodeScriptError, Message: err.Error()}
	}
	if err := rt.SetGlobal("__bu_log", moejs.NativeFunc(h.log)); err != nil {
		return nil, &browser.Error{Code: browser.CodeScriptError, Message: err.Error()}
	}

	var promise moejs.Value
	interrupted := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			loop.Interrupt(ctx.Err())
		case <-interrupted:
		}
	}()
	runErr := loop.Run(func(rt *moejs.Runtime) error {
		if _, err := rt.RunScript(pre); err != nil {
			return err
		}
		argsJSON := req.Args
		if len(argsJSON) == 0 || string(argsJSON) == "null" {
			argsJSON = json.RawMessage("{}")
		}
		argsVal, err := rt.ParseJSON(argsJSON)
		if err != nil {
			return err
		}
		fn, err := rt.RunScript(userScript)
		if err != nil {
			return err
		}
		p, err := rt.CallFunction(fn, moejs.Undefined(), argsVal)
		if err != nil {
			return err
		}
		promise = p
		return nil
	})
	close(interrupted)

	result := &Result{
		Console:    nil,
		Actions:    actions.snapshot(),
		DurationMS: time.Since(start).Milliseconds(),
		ScriptID:   scriptID,
	}
	consoleLines, truncated := console.snapshot()
	result.Console = consoleLines
	result.Truncated = truncated

	if runErr != nil {
		if errors.Is(runErr, moejs.ErrMemoryLimit) {
			return result, &browser.Error{Code: browser.CodeScriptMemory, Message: "script exceeded the memory limit"}
		}
		var ie *moejs.InterruptedError
		if errors.As(runErr, &ie) {
			if ctx.Err() != nil {
				return result, &browser.Error{Code: browser.CodeScriptTimeout, Message: "script timed out after " + timeout.String()}
			}
			return result, &browser.Error{Code: browser.CodeScriptTimeout, Message: "script was interrupted"}
		}
		var se *moejs.SyntaxError
		if errors.As(runErr, &se) {
			return result, syntaxError(se)
		}
		return result, &browser.Error{Code: browser.CodeScriptError, Message: runErr.Error()}
	}
	if promise.IsUndefined() {
		return result, &browser.Error{Code: browser.CodeScriptHung, Message: "script did not return a promise"}
	}
	state, value, ok := moejs.PromiseResult(promise)
	if !ok || state == moejs.PromisePending {
		return result, &browser.Error{Code: browser.CodeScriptHung, Message: "script promise never settled"}
	}
	if state == moejs.PromiseRejected {
		exc := &moejs.Exception{Value: value, Stack: rt.StackTrace(&moejs.Exception{Value: value})}
		code := browser.CodeScriptError
		hint := ""
		if g, err := rt.ToGo(value); err == nil {
			if m, ok := g.(map[string]any); ok {
				if c, ok := m["code"].(string); ok && c != "" {
					code = c
				}
				if h, ok := m["hint"].(string); ok {
					hint = h
				}
			}
		}
		return result, &browser.Error{Code: code, Message: exc.Error(), Hint: hint}
	}
	if value.IsUndefined() {
		result.Result = nil
	} else {
		b, err := rt.AppendJSON(nil, value)
		if err != nil {
			return result, &browser.Error{Code: browser.CodeScriptError, Message: "result is not JSON: " + err.Error()}
		}
		if len(b) > cfg.MaxResultBytes {
			return result, &browser.Error{Code: browser.CodeScriptResult, Message: fmt.Sprintf("result is %d bytes; limit is %d", len(b), cfg.MaxResultBytes)}
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return result, &browser.Error{Code: browser.CodeScriptError, Message: "result is not JSON: " + err.Error()}
		}
		result.Result = v
	}

	return result, nil
}

func syntaxError(err error) *browser.Error {
	var se *moejs.SyntaxError
	if errors.As(err, &se) {
		return &browser.Error{
			Code:    browser.CodeScriptSyntax,
			Message: fmt.Sprintf("%s:%d:%d: %s", se.File, se.Line, se.Column, se.Message),
			Details: map[string]any{"file": se.File, "line": se.Line, "column": se.Column},
		}
	}
	return &browser.Error{Code: browser.CodeScriptError, Message: err.Error()}
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
