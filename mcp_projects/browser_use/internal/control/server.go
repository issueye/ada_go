package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"browseruse/internal/audit"
	"browseruse/internal/browser"
	"browseruse/internal/config"
	"browseruse/internal/version"
)

// Server is the local loopback API of browser-use.
type Server struct {
	mgr   *browser.Manager
	cfg   config.Config
	token string
	ln    net.Listener
	http  *http.Server
	info  *config.RuntimeInfo
	audit *audit.Log

	jobs *jobStore
	// runFn 允许测试注入执行器；生产路径是 runCall。
	runFn func(ctx context.Context, name string, args json.RawMessage, client string) callResult

	activityMu   sync.Mutex
	activity     []audit.Entry
	scriptActive atomic.Bool

	sweepStop chan struct{}
}

// NewServer listens on 127.0.0.1 and prepares the API.
func NewServer(mgr *browser.Manager, cfg config.Config, log *audit.Log) (*Server, error) {
	bind := cfg.Server.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bind, cfg.Server.Port))
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{mgr: mgr, cfg: cfg, token: token, ln: ln, audit: log, jobs: newJobStore(), sweepStop: make(chan struct{})}
	s.jobs.startSweeper(time.Minute, s.sweepStop)
	s.info = config.NewRuntimeInfo(ln.Addr().(*net.TCPAddr).Port, token)
	s.http = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Start writes the runtime file and serves until Close.
func (s *Server) Start() error {
	if err := config.SaveRuntime(s.info); err != nil {
		return err
	}
	log.Printf("browser-use: listening on %s", s.info.URL())
	go func() {
		if err := s.http.Serve(s.ln); err != nil && err != http.ErrServerClosed {
			log.Printf("browser-use: server: %v", err)
		}
	}()
	return nil
}

// Close stops the server and removes its runtime file.
func (s *Server) Close() {
	if s.sweepStop != nil {
		close(s.sweepStop)
	}
	if s.jobs != nil {
		s.jobs.cancelAll()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.http.Shutdown(ctx)
	config.RemoveRuntime()
}

// Port returns the listening port.
func (s *Server) Port() int { return s.info.Port }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/health" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "app": config.AppName, "version": version.Version,
			"pid": os.Getpid(), "port": s.info.Port,
			"features": []string{
				"async_calls", "events", "dialogs", "snapshot_diff", "wait_events",
				"screenshot_clip", "screenshot_jpeg", "upload_dirs", "mcp_http_token",
			},
		})
		return
	}
	if r.URL.Path == "/mcp" {
		// 常见误配：把 MCP 客户端指向 app 的控制端口。控制 API 不是 MCP，返回
		// 可操作的提示，而不是让鉴权先回 401（并避免 404 触发客户端重连）。
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false,
			"error": map[string]string{
				"code":    "not_mcp",
				"message": "this is the browser-use control API, not MCP; use `browser-use mcp --http --port 8766` with http://127.0.0.1:8766/mcp, or the stdio config (browser-use mcp)",
			},
		})
		return
	}
	if r.Header.Get("Origin") != "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": map[string]string{"code": "forbidden", "message": "browser origins are not allowed"}})
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="browser-use"`)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": map[string]string{"code": "unauthorized", "message": "missing or wrong token"}})
		return
	}
	switch {
	case r.URL.Path == "/v1/tools":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tools": Tools})
	case r.URL.Path == "/v1/call":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": map[string]string{"code": "method_not_allowed", "message": "use POST"}})
			return
		}
		s.handleCall(w, r)
	case r.URL.Path == "/v1/calls":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": map[string]string{"code": "method_not_allowed", "message": "use POST"}})
			return
		}
		s.handleStartCall(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/calls/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/calls/")
		if id == "" || strings.Contains(id, "/") {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": map[string]string{"code": "not_found", "message": "unknown job"}})
			return
		}
		switch r.Method {
		case http.MethodGet:
			s.handleJobGet(w, id)
		case http.MethodDelete:
			s.handleJobCancel(w, id)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": map[string]string{"code": "method_not_allowed", "message": "use GET or DELETE"}})
		}
	case r.URL.Path == "/v1/script":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": map[string]string{"code": "method_not_allowed", "message": "use POST"}})
			return
		}
		s.handleScript(w, r)
	case r.URL.Path == "/v1/events":
		s.handleEvents(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": map[string]string{"code": "not_found", "message": "unknown path"}})
	}
}

func (s *Server) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

type callRequest struct {
	Name      string          `json:"name"`
	Args      json.RawMessage `json:"args"`
	TimeoutMS int             `json:"timeout_ms"`
}

type callResult struct {
	OK       bool             `json:"ok"`
	Tab      *browser.TabInfo `json:"tab,omitempty"`
	Data     any              `json:"data,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
	Error    *browser.Error   `json:"error,omitempty"`
	TimingMS int64            `json:"timing_ms"`
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req callRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, callResult{Error: &browser.Error{Code: browser.CodeBadRequest, Message: "bad JSON: " + err.Error()}})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, callResult{Error: &browser.Error{Code: browser.CodeBadRequest, Message: "name is required"}})
		return
	}
	s.serveCall(w, r, req)
}

func (s *Server) handleScript(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, callResult{Error: &browser.Error{Code: browser.CodeBadRequest, Message: "bad JSON: " + err.Error()}})
		return
	}
	s.serveCall(w, r, callRequest{Name: "script", Args: raw})
}

// toolNameSet 用于在启动异步任务前先做一次名称校验。
var toolNameSet = func() map[string]bool {
	m := make(map[string]bool, len(Tools))
	for _, t := range Tools {
		m[t.Name] = true
	}
	return m
}()

const (
	// callTimeoutSlack 给工具自己的 timeout_ms 留出收尾时间：如果没有这层余量，
	// wait(timeout_ms=60000) 会在 60s 整被 context 取消，工具来不及返回自己的
	// timeout 错误，调用方也分不清“等够了”还是“外层预算到了”。
	callTimeoutSlack = 5 * time.Second
	maxCallTimeout   = 10 * time.Minute
)

// callTimeoutFor 计算一次调用的期限：script 用 MaxTimeoutMS；其余工具取
// 顶层 timeout_ms / 默认值与工具参数 timeout_ms（加余量）的较大者。
func (s *Server) callTimeoutFor(name string, args json.RawMessage, reqMS int) time.Duration {
	return CallTimeout(name, args, reqMS, s.cfg)
}

// CallTimeout is the deadline budget of one tool call, shared by /v1/call and
// the MCP layer. The MCP client cap must be derived from this same budget:
// with an independent constant (5min) that equals Script.MaxTimeoutMS, a
// maximal script hits both deadlines together and nobody can tell which side
// timed out.
func CallTimeout(name string, args json.RawMessage, reqMS int, cfg config.Config) time.Duration {
	timeout := time.Duration(reqMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(cfg.Agent.DefaultTimeoutMS) * time.Millisecond
	}
	if name == "script" {
		timeout = time.Duration(cfg.Script.MaxTimeoutMS) * time.Millisecond
	} else if toolMS := timeoutFromArgs(args); toolMS > 0 {
		// 工具参数里的 timeout_ms 是该工具自己的等待预算；外层 deadline 必须比它长，
		// 否则 wait/click 还没到自己的超时就被 context 取消，错误归因也会变成 timeout。
		if d := time.Duration(toolMS)*time.Millisecond + callTimeoutSlack; d > timeout {
			timeout = d
		}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > maxCallTimeout {
		timeout = maxCallTimeout
	}
	return timeout
}

// timeoutFromArgs reads the tool-level timeout_ms. Tools without that field (or
// with malformed args) fall back to the top-level/default budget.
func timeoutFromArgs(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var req struct {
		TimeoutMS int `json:"timeout_ms"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0
	}
	if req.TimeoutMS < 0 {
		return 0
	}
	return req.TimeoutMS
}

// handleEvents is a JSON long poll over the Manager event ring. Clients pass
// after=<cursor> (0 for the oldest retained event), optional kinds=a,b and an
// optional timeout_ms (default 30s, max 120s). A timeout is a normal empty
// result with an advanced cursor, not an error.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": map[string]string{"code": "method_not_allowed", "message": "use GET"}})
		return
	}
	q := r.URL.Query()
	after, _ := strconv.ParseUint(strings.TrimSpace(q.Get("after")), 10, 64)
	timeoutMS, _ := strconv.Atoi(strings.TrimSpace(q.Get("timeout_ms")))
	if timeoutMS <= 0 {
		timeoutMS = 30000
	}
	if timeoutMS > 120000 {
		timeoutMS = 120000
	}
	var kinds map[string]bool
	if raw := strings.TrimSpace(q.Get("kinds")); raw != "" {
		kinds = map[string]bool{}
		for _, k := range strings.Split(raw, ",") {
			if k = strings.TrimSpace(k); k != "" {
				kinds[k] = true
			}
		}
	}
	events, cursor := s.mgr.Events(r.Context(), after, time.Duration(timeoutMS)*time.Millisecond, kinds)
	if events == nil {
		events = []browser.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cursor": cursor, "events": events})
}

// run 执行一次工具调用；测试可注入 runFn，生产路径是 runCall。
func (s *Server) run(ctx context.Context, name string, args json.RawMessage, client string) callResult {
	if s.runFn != nil {
		return s.runFn(ctx, name, args, client)
	}
	return s.runCall(ctx, name, args, client)
}

// runCall 执行一次工具调用并落审计（同步与异步任务共用同一条路径）。
func (s *Server) runCall(ctx context.Context, name string, args json.RawMessage, client string) callResult {
	if client == "" {
		client = "http"
	}
	start := time.Now()
	out, err := s.execute(ctx, client, name, args)
	res := callResult{TimingMS: time.Since(start).Milliseconds()}
	tabID := ""
	if out != nil {
		tabID = out.TabID
	}
	if tabID == "" {
		tabID = s.mgr.ActiveID()
	}
	entry := audit.Entry{
		TS: start, Tool: name, Tab: tabID, Client: client,
		OK: err == nil, MS: time.Since(start).Milliseconds(),
		Args: audit.RedactArgs(name, args),
	}
	if t := s.mgr.Tab(tabID); t != nil {
		entry.URL = t.URL()
	}
	if err != nil {
		entry.Code = asError(err).Code
	}
	s.record(entry)
	if err != nil {
		res.Error = asError(err)
		return res
	}
	res.OK = true
	if out != nil {
		res.Data = out.Data
		if out.TabID != "" {
			if t := s.mgr.Tab(out.TabID); t != nil {
				info := t.Info(s.mgr.ActiveID() == out.TabID)
				res.Tab = &info
			}
		}
	}
	if res.Tab == nil {
		if a := s.mgr.Active(); a != nil {
			info := a.Info(true)
			res.Tab = &info
		}
	}
	return res
}

func (s *Server) serveCall(w http.ResponseWriter, r *http.Request, req callRequest) {
	timeout := s.callTimeoutFor(req.Name, req.Args, req.TimeoutMS)
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	client := strings.TrimSpace(r.Header.Get("X-Client-Id"))
	if client == "" {
		client = "http"
	}
	writeJSON(w, http.StatusOK, s.run(ctx, req.Name, req.Args, client))
}

// handleStartCall 启动一个后台任务并立即返回 id（响应头在毫秒级发出）。
func (s *Server) handleStartCall(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	var req callRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": map[string]string{"code": "bad_request", "message": "bad JSON: " + err.Error()}})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": map[string]string{"code": "bad_request", "message": "name is required"}})
		return
	}
	if !toolNameSet[req.Name] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": map[string]string{"code": "bad_request", "message": "unknown tool: " + req.Name}})
		return
	}
	client := strings.TrimSpace(r.Header.Get("X-Client-Id"))
	if client == "" {
		client = "http"
	}
	timeout := s.callTimeoutFor(req.Name, req.Args, req.TimeoutMS)
	job, err := s.jobs.create(req.Name, client, timeout)
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": map[string]string{"code": "busy", "message": err.Error()}})
		return
	}
	go func() {
		res := s.run(job.ctx, job.name, req.Args, job.client)
		status := jobDone
		if errors.Is(job.ctx.Err(), context.Canceled) {
			status = jobCancelled
		}
		s.jobs.finish(job.id, res, status)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok": true, "id": job.id, "name": job.name,
		"status": string(jobRunning), "timeout_ms": timeout.Milliseconds(),
	})
}

func (s *Server) handleJobGet(w http.ResponseWriter, id string) {
	view, ok := s.jobs.get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": map[string]string{"code": "not_found", "message": "unknown job"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": view})
}

func (s *Server) handleJobCancel(w http.ResponseWriter, id string) {
	view, ok := s.jobs.cancel(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": map[string]string{"code": "not_found", "message": "unknown job"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": true, "job": view})
}

func (s *Server) record(e audit.Entry) {
	if s.audit != nil {
		s.audit.Write(e)
	}
	s.activityMu.Lock()
	s.activity = append(s.activity, e)
	if len(s.activity) > 100 {
		s.activity = s.activity[len(s.activity)-100:]
	}
	s.activityMu.Unlock()
}

// Activity returns the most recent tool calls.
func (s *Server) Activity() []audit.Entry {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	return append([]audit.Entry(nil), s.activity...)
}

func asError(err error) *browser.Error {
	if e, ok := err.(*browser.Error); ok {
		return e
	}
	return &browser.Error{Code: browser.CodeInternal, Message: err.Error()}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
