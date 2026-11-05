package canvas

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

// Error 是带稳定错误码的 API 错误。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// APIError 构造 API 错误。
func APIError(code, message, hint string) *Error {
	return &Error{Code: code, Message: message, Hint: hint}
}

// CatalogSource 由 main 注入（design 包实现），画布不直接依赖设计数据实现。
// 返回值均为可直接 JSON 序列化的结构。
type CatalogSource interface {
	// SystemSummaries 返回全部体系摘要。
	SystemSummaries() any
	// SystemDetail 返回某体系完整数据（Token + 组件清单 + 守则）。
	SystemDetail(id string) (any, bool)
	// ComposePreview 生成一张组件预览完整 HTML 文档。
	ComposePreview(systemID string, components []string, theme, title string) (html string, width, height int, err error)
}

// Server 是画布 HTTP 服务。
type Server struct {
	store   *Store
	tasks   *TaskQueue
	origin  string // 允许的 Origin（http://127.0.0.1:port）
	Catalog CatalogSource
}

// NewServer 构造画布服务。
func NewServer(store *Store, tasks *TaskQueue, origin string) *Server {
	return &Server{store: store, tasks: tasks, origin: origin}
}

// Handler 组装全部路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/nodes", s.handleListNodes)
	mux.HandleFunc("POST /api/nodes", s.handleAddNode)
	mux.HandleFunc("PUT /api/nodes/{id}", s.handleUpdateNode)
	mux.HandleFunc("DELETE /api/nodes/{id}", s.handleDeleteNode)
	mux.HandleFunc("POST /api/nodes/clear", s.handleClearNodes)
	mux.HandleFunc("POST /api/capture", s.handleCaptureCreate)
	mux.HandleFunc("GET /api/tasks", s.handleTaskClaim)
	mux.HandleFunc("POST /api/tasks/{id}/result", s.handleTaskResult)
	mux.HandleFunc("GET /api/capture/{id}", s.handleCaptureWait)
	mux.HandleFunc("GET /api/catalog/systems", s.handleCatalogSystems)
	mux.HandleFunc("GET /api/catalog/systems/{id}", s.handleCatalogSystem)
	mux.HandleFunc("POST /api/catalog/preview", s.handleCatalogPreview)
	// 静态资源
	staticRoot, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticRoot))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.ServeFileFS(w, r, staticRoot, "index.html")
	})
	return s.guard(mux)
}

// guard 统一处理：Origin 校验（写操作）、panic 兜底。
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": Error{Code: "internal", Message: fmt.Sprint(rec)},
				})
			}
		}()
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			origin := r.Header.Get("Origin")
			if origin != "" && !sameLoopbackOrigin(origin) {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": Error{Code: "bad_origin", Message: "cross-origin writes are not allowed"},
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameLoopbackOrigin 仅放行 127.0.0.1/localhost 的 http origin；
// 空 Origin 视为同源/非浏览器直连，放行。
func sameLoopbackOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	host := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "127.0.0.1" || host == "localhost"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, e *Error) {
	writeJSON(w, status, map[string]any{"error": e})
}

func decodeBody[T any](w http.ResponseWriter, r *http.Request, dst *T) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "read body: "+err.Error(), ""))
		return false
	}
	if len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "empty body", ""))
		return false
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "parse json: "+err.Error(), ""))
		return false
	}
	return true
}

// ---------------- handlers ----------------

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "node_count": len(s.store.List()), "time": time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes := s.store.List()
	if sys := r.URL.Query().Get("system"); sys != "" {
		filtered := nodes[:0:0]
		for _, n := range nodes {
			if n.System == sys {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

type addNodeIn struct {
	Title  string   `json:"title"`
	System string   `json:"system"`
	Kind   string   `json:"kind"`
	HTML   string   `json:"html"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	X      *float64 `json:"x"`
	Y      *float64 `json:"y"`
	Tags   []string `json:"tags"`
}

func (s *Server) handleAddNode(w http.ResponseWriter, r *http.Request) {
	var in addNodeIn
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.HTML) == "" {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "html is required", ""))
		return
	}
	if in.Kind == "" {
		in.Kind = KindNote
	}
	if !ValidKind(in.Kind) {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "invalid kind: "+in.Kind, "screen|component|palette|note"))
		return
	}
	n := Node{
		Title: in.Title, System: in.System, Kind: in.Kind, HTML: in.HTML,
		Width: in.Width, Height: in.Height, Tags: in.Tags,
	}
	if in.X != nil && in.Y != nil {
		n.X, n.Y = *in.X, *in.Y
	} else {
		n.X, n.Y = s.store.AutoPosition(in.Width, in.Height)
	}
	created := s.store.Add(n)
	writeJSON(w, http.StatusCreated, map[string]any{"node": created})
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in addNodeIn
	if !decodeBody(w, r, &in) {
		return
	}
	updated, err := s.store.Update(id, func(n *Node) {
		if in.Title != "" {
			n.Title = in.Title
		}
		if in.HTML != "" {
			n.HTML = in.HTML
		}
		if in.System != "" {
			n.System = in.System
		}
		if in.Width > 0 {
			n.Width = in.Width
		}
		if in.Height > 0 {
			n.Height = in.Height
		}
		if in.X != nil {
			n.X = *in.X
		}
		if in.Y != nil {
			n.Y = *in.Y
		}
		if in.Tags != nil {
			n.Tags = in.Tags
		}
	})
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, APIError("not_found", "node "+id+" not found", ""))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, APIError("internal", err.Error(), ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": updated})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	removed, err := s.store.Remove(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, APIError("internal", err.Error(), ""))
		return
	}
	if !removed {
		writeErr(w, http.StatusNotFound, APIError("not_found", "node "+id+" not found", ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": id})
}

func (s *Server) handleClearNodes(w http.ResponseWriter, _ *http.Request) {
	n := s.store.Clear()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

type captureIn struct {
	NodeID string  `json:"node_id"`
	Scale  float64 `json:"scale"`
}

func (s *Server) handleCaptureCreate(w http.ResponseWriter, r *http.Request) {
	var in captureIn
	if !decodeBody(w, r, &in) {
		return
	}
	if in.NodeID != "" {
		if _, err := s.store.Get(in.NodeID); err != nil {
			writeErr(w, http.StatusNotFound, APIError("not_found", "node "+in.NodeID+" not found", ""))
			return
		}
	}
	if in.Scale <= 0 {
		in.Scale = 1
	}
	task := s.tasks.Create(in.NodeID, in.Scale)
	writeJSON(w, http.StatusCreated, map[string]any{"task": task, "poll": "/api/capture/" + task.ID})
}

// handleTaskClaim 是 UI 侧长轮询领取。
func (s *Server) handleTaskClaim(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	task := s.tasks.Claim(ctx)
	if task == nil {
		writeJSON(w, http.StatusOK, map[string]any{"task": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleTaskResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var res CaptureResult
	if !decodeBody(w, r, &res) {
		return
	}
	if !s.tasks.Complete(id, res) {
		writeErr(w, http.StatusNotFound, APIError("not_found", "task "+id+" not found", ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCaptureWait 是 tool 侧长轮询取结果。
func (s *Server) handleCaptureWait(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	timeout := 9 * time.Second
	if v := r.URL.Query().Get("timeout"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 && ms <= 15000 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout+2*time.Second)
	defer cancel()
	res := s.tasks.Wait(ctx, id)
	if res == nil {
		writeErr(w, http.StatusConflict, APIError("capture_timeout",
			"画布页面未在时限内回传截图",
			"先在浏览器打开画布地址（canvas_url），再重试 canvas_screenshot"))
		return
	}
	if res.Error != "" {
		writeJSON(w, http.StatusOK, map[string]any{"result": res, "ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "ok": true})
}

// ---------------- catalog ----------------

func (s *Server) handleCatalogSystems(w http.ResponseWriter, _ *http.Request) {
	if s.Catalog == nil {
		writeErr(w, http.StatusNotImplemented, APIError("no_catalog", "设计目录未装配", ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"systems": s.Catalog.SystemSummaries()})
}

func (s *Server) handleCatalogSystem(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
		writeErr(w, http.StatusNotImplemented, APIError("no_catalog", "设计目录未装配", ""))
		return
	}
	id := r.PathValue("id")
	detail, ok := s.Catalog.SystemDetail(id)
	if !ok {
		writeErr(w, http.StatusNotFound, APIError("unknown_system", "未知设计体系 "+id, ""))
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type catalogPreviewIn struct {
	System     string   `json:"system"`
	Components []string `json:"components"`
	Theme      string   `json:"theme"`
	Title      string   `json:"title"`
}

func (s *Server) handleCatalogPreview(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
		writeErr(w, http.StatusNotImplemented, APIError("no_catalog", "设计目录未装配", ""))
		return
	}
	var in catalogPreviewIn
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Components) == 0 {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "components is required", ""))
		return
	}
	if len(in.Components) > 12 {
		in.Components = in.Components[:12]
	}
	html, w2, h, err := s.Catalog.ComposePreview(in.System, in.Components, in.Theme, in.Title)
	if err != nil {
		writeErr(w, http.StatusBadRequest, APIError("compose_failed", err.Error(), ""))
		return
	}
	node := Node{
		Title: firstNonEmpty(in.Title, in.System+" 预览"), System: in.System,
		Kind: KindComponent, HTML: html, Width: w2, Height: h,
		Tags: append([]string{in.System, firstNonEmpty(in.Theme, "light")}, in.Components...),
	}
	node.X, node.Y = s.store.AutoPosition(w2, h)
	created := s.store.Add(node)
	writeJSON(w, http.StatusCreated, map[string]any{"node": created})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
