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
	// artboard 为空表示按体系 Family 取默认画板；返回值 artboardID 是实际生效的画板。
	ComposePreview(systemID string, components []string, theme, title, artboard string) (html string, width, height int, artboardID string, err error)
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
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/projects", s.handleAddProject)
	mux.HandleFunc("GET /api/projects/{id}", s.handleGetProject)
	mux.HandleFunc("PUT /api/projects/{id}", s.handleUpdateProject)
	mux.HandleFunc("DELETE /api/projects/{id}", s.handleDeleteProject)
	mux.HandleFunc("GET /api/nodes", s.handleListNodes)
	mux.HandleFunc("POST /api/nodes", s.handleAddNode)
	mux.HandleFunc("PUT /api/nodes/{id}", s.handleUpdateNode)
	mux.HandleFunc("DELETE /api/nodes/{id}", s.handleDeleteNode)
	mux.HandleFunc("POST /api/nodes/clear", s.handleClearNodes)
	mux.HandleFunc("POST /api/arrange", s.handleArrange)
	mux.HandleFunc("POST /api/capture", s.handleCaptureCreate)
	mux.HandleFunc("GET /api/tasks", s.handleTaskClaim)
	mux.HandleFunc("POST /api/tasks/{id}/result", s.handleTaskResult)
	mux.HandleFunc("GET /api/capture/{id}", s.handleCaptureWait)
	mux.HandleFunc("GET /api/edges", s.handleListEdges)
	mux.HandleFunc("POST /api/edges", s.handleAddEdge)
	mux.HandleFunc("DELETE /api/edges/{id}", s.handleDeleteEdge)
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
		"ok": true, "node_count": len(s.store.List()),
		"project_count": len(s.store.ListProjects()),
		"time":          time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project != "" {
		if _, err := s.store.GetProject(project); err != nil {
			writeErr(w, http.StatusNotFound, projectErr(err, project))
			return
		}
	}
	nodes := s.store.ListByProject(project)
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
	Project    string   `json:"project"`
	Title      string   `json:"title"`
	System     string   `json:"system"`
	Kind       string   `json:"kind"`
	HTML       string   `json:"html"`
	Artboard   string   `json:"artboard"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	AutoHeight *bool    `json:"auto_height"`
	X          *float64 `json:"x"`
	Y          *float64 `json:"y"`
	Tags       []string `json:"tags"`
}

func (s *Server) handleAddNode(w http.ResponseWriter, r *http.Request) {
	var in addNodeIn
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Project) == "" {
		writeErr(w, http.StatusBadRequest, APIError("project_required",
			"新增设计稿必须指定项目", "先 POST /api/projects 建项目，或用 project_list 查已有项目"))
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
		ProjectID: in.Project,
		Title:     in.Title, System: in.System, Kind: in.Kind, HTML: in.HTML,
		Artboard: in.Artboard, Width: in.Width, Height: in.Height, Tags: in.Tags,
	}
	if in.X != nil && in.Y != nil {
		n.X, n.Y = *in.X, *in.Y
	}
	// 归属校验与自动排版都在 Store.Add 里做（项目必须已存在）
	created, err := s.store.Add(n)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, in.Project))
		return
	}
	if in.AutoHeight != nil && !*in.AutoHeight {
		if upd, err := s.store.Update(created.ID, func(x *Node) { x.AutoHeight = false }); err == nil {
			created = upd
		}
	}
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
		if in.Artboard != "" {
			n.Artboard = in.Artboard
		}
		if in.AutoHeight != nil {
			n.AutoHeight = *in.AutoHeight
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

func (s *Server) handleClearNodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project string `json:"project"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Project) == "" {
		writeErr(w, http.StatusBadRequest, APIError("project_required",
			"清空必须指定项目", "项目之间是独立作用域，不存在「清空整个画布」"))
		return
	}
	n, err := s.store.Clear(in.Project)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, in.Project))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n, "project": in.Project})
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

// handleArrange 按项目分组重排（「全部」视图用）。
func (s *Server) handleArrange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GroupBy string `json:"group_by"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	if in.GroupBy != "project" {
		writeErr(w, http.StatusBadRequest, APIError("bad_request",
			"group_by 只支持 project", ""))
		return
	}
	moved, err := s.store.ArrangeByProject()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, APIError("internal", err.Error(), ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved})
}

// ---------------- edges ----------------

func (s *Server) handleListEdges(w http.ResponseWriter, r *http.Request) {
	edges := s.store.ListEdges()
	if project := strings.TrimSpace(r.URL.Query().Get("project")); project != "" {
		filtered := make([]Edge, 0, len(edges))
		for _, e := range edges {
			a, errA := s.store.Get(e.From)
			b, errB := s.store.Get(e.To)
			if errA == nil && errB == nil && a.ProjectID == project && b.ProjectID == project {
				filtered = append(filtered, e)
			}
		}
		edges = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"edges": edges})
}

type addEdgeIn struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

func (s *Server) handleAddEdge(w http.ResponseWriter, r *http.Request) {
	var in addEdgeIn
	if !decodeBody(w, r, &in) {
		return
	}
	edge, err := s.store.AddEdge(in.From, in.To, in.Label)
	switch {
	case errors.Is(err, ErrEdgeEndpoint):
		writeErr(w, http.StatusNotFound, APIError("not_found", "连线端点节点不存在", "canvas_list 查看现有节点"))
		return
	case errors.Is(err, ErrEdgeDuplicate):
		writeErr(w, http.StatusConflict, APIError("edge_duplicate", "两节点间已存在连线", ""))
		return
	case errors.Is(err, ErrEdgeSelfLoop):
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "连线不可自环", ""))
		return
	case errors.Is(err, ErrEdgeCrossProject):
		writeErr(w, http.StatusBadRequest, projectErr(err, ""))
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, APIError("internal", err.Error(), ""))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"edge": edge})
}

func (s *Server) handleDeleteEdge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	removed, err := s.store.RemoveEdge(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, APIError("internal", err.Error(), ""))
		return
	}
	if !removed {
		writeErr(w, http.StatusNotFound, APIError("not_found", "连线 "+id+" 不存在", ""))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": id})
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
	Project    string   `json:"project"`
	System     string   `json:"system"`
	Components []string `json:"components"`
	Theme      string   `json:"theme"`
	Title      string   `json:"title"`
	Artboard   string   `json:"artboard"`
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
	if strings.TrimSpace(in.Project) == "" {
		writeErr(w, http.StatusBadRequest, APIError("project_required",
			"新增设计稿必须指定项目", "先创建项目，再在项目下添加预览"))
		return
	}
	if len(in.Components) == 0 {
		writeErr(w, http.StatusBadRequest, APIError("bad_request", "components is required", ""))
		return
	}
	if len(in.Components) > 24 {
		in.Components = in.Components[:24]
	}
	html, w2, h, abID, err := s.Catalog.ComposePreview(in.System, in.Components, in.Theme, in.Title, in.Artboard)
	if err != nil {
		writeErr(w, http.StatusBadRequest, APIError("compose_failed", err.Error(), ""))
		return
	}
	tags := append([]string{in.System, firstNonEmpty(in.Theme, "light")}, in.Components...)
	if abID != "" {
		tags = append(tags, "artboard:"+abID)
	}
	tags = append(tags, "project:"+in.Project)
	node := Node{
		ProjectID: in.Project,
		Title:     firstNonEmpty(in.Title, in.System+" 预览"), System: in.System,
		Kind: KindComponent, HTML: html, Artboard: abID, Width: w2, Height: h,
		Tags: tags,
	}
	created, err := s.store.Add(node)
	if err != nil {
		writeErr(w, statusForProjectErr(err), projectErr(err, in.Project))
		return
	}
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
