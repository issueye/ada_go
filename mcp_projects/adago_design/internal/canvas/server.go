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
	// Theme 是界面缺省主题（light|dark），来自 -theme；用户手动切换的
	// localStorage 覆盖在页面 bootstrap 里完成（见 static/index.html）。
	Theme string
	// DataDir 是本实例的数据目录；健康检查回显它，供多实例探测比对
	// 「是否撞在同一份数据上」（见 instance.go 的 ProbeInstance）。
	DataDir string
}

// NewServer 构造画布服务。
func NewServer(store *Store, tasks *TaskQueue, origin string) *Server {
	return &Server{store: store, tasks: tasks, origin: origin, Theme: ThemeLight}
}

// 界面主题取值；缺省为纸面（浅色），与 UED 的 Black Swan 缺省态一致。
const (
	ThemeLight = "light"
	ThemeDark  = "dark"
)

// NormalizeTheme 把任意输入收敛成合法主题名。
func NormalizeTheme(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), ThemeDark) {
		return ThemeDark
	}
	return ThemeLight
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
	mux.HandleFunc("PUT /api/projects/{id}/canvas", s.handleSetProjectCanvas)
	mux.HandleFunc("GET /api/canvas/presets", s.handleCanvasPresets)
	mux.HandleFunc("GET /api/nodes", s.handleListNodes)
	mux.HandleFunc("POST /api/nodes", s.handleAddNode)
	mux.HandleFunc("GET /api/nodes/{id}", s.handleGetNode)
	mux.HandleFunc("PUT /api/nodes/{id}", s.handleUpdateNode)
	mux.HandleFunc("DELETE /api/nodes/{id}", s.handleDeleteNode)
	mux.HandleFunc("POST /api/nodes/clear", s.handleClearNodes)
	mux.HandleFunc("POST /api/arrange", s.handleArrange)
	mux.HandleFunc("POST /api/undo", s.handleUndo)
	mux.HandleFunc("POST /api/redo", s.handleRedo)
	mux.HandleFunc("GET /api/events", s.handleEvents)
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
		s.serveIndex(w, r, staticRoot)
	})
	return s.guard(mux)
}

// serveIndex 输出 index.html，并把缺省主题注入 data-theme 占位符。
// 用占位符替换而不是字符串拼接：主题名只可能是 light|dark（白名单收敛），
// 且静态文件与其测试保持原样可读。
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, staticRoot fs.FS) {
	raw, err := fs.ReadFile(staticRoot, "index.html")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": Error{Code: "internal", Message: "index.html 不可用"},
		})
		return
	}
	theme := NormalizeTheme(s.Theme)
	body := strings.Replace(string(raw), "data-theme=\"__ADAGO_THEME__\"",
		`data-theme="`+theme+`"`, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(body))
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
	m := map[string]any{
		"ok":            true,
		"server":        "adago-design",
		"node_count":    len(s.store.List()),
		"project_count": len(s.store.ListProjects()),
		"data_dir":      s.DataDir,
		"rev":           s.store.Rev(),
		"time":          time.Now().Format(time.RFC3339),
	}
	if perr := s.store.PersistError(); perr != nil {
		m["persist_error"] = perr.Error()
	}
	writeJSON(w, http.StatusOK, m)
}

// handleListNodes 返回节点列表。
//
// 默认**剥掉 html 大字段**（每张预览文档约 10KB+）：列表轮询只该传元数据，
// 2s 轮询 × N 节点 × 每个标签页全量拉 HTML 是纯浪费。客户端需要正文时走
// GET /api/nodes/{id} 按单节点取，或用 ?include=html 一次性拿全量（首屏/切项目）。
// nodeMeta 用空指针把 html 键从 JSON 里整个去掉，而不是置空字符串 ——
// 区分「没有正文信息」与「正文为空」。
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
	if r.URL.Query().Get("include") == "html" {
		writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
		return
	}
	metas := make([]nodeMeta, 0, len(nodes))
	for i := range nodes {
		metas = append(metas, nodeMeta{Node: nodes[i]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": metas})
}

// nodeMeta 是节点列表视图：用空指针字段遮蔽嵌入 Node 的 html，
// 让 "html" 键从 JSON 输出里彻底消失（omitempty 对 nil 指针生效）。
type nodeMeta struct {
	Node
	HTML *struct{} `json:"html,omitempty"`
}

// handleGetNode 返回单个节点的完整数据（含 html 正文）。
// 列表默认剥 HTML 后，这是画布 UI 与脚本按需取正文的入口。
func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, &Error{Code: "not_found", Message: "节点不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": n})
}

// handleUndo / handleRedo 撤销与重做最近一次画布内容操作（迭代 3-②）。
// 历史在 Store 内存栈里，进程级共享：任意入口（UI 快捷键 / MCP 工具 /
// HTTP）撤销后，SSE 会把刷新推给所有打开的画布页。
func (s *Server) handleUndo(w http.ResponseWriter, _ *http.Request) {
	desc, ok := s.store.Undo()
	u, r := s.store.HistoryDepth()
	writeJSON(w, http.StatusOK, map[string]any{"done": ok, "description": desc, "undo_depth": u, "redo_depth": r})
}

func (s *Server) handleRedo(w http.ResponseWriter, _ *http.Request) {
	desc, ok := s.store.Redo()
	u, r := s.store.HistoryDepth()
	writeJSON(w, http.StatusOK, map[string]any{"done": ok, "description": desc, "undo_depth": u, "redo_depth": r})
}

// handleEvents 以 SSE 推送存储变更（GET /api/events）。
// 连接建立先发一条当前版本号；此后每次存储变更推一条 change 事件，
// 15s 一条注释心跳保活。客户端收到事件就拉一次节点列表（列表已剥 HTML，代价很小），
// 轮询保留作降级兜底。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, &Error{Code: "internal", Message: "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, "retry: 2000\n\n")
	events, cancel := s.store.Subscribe()
	defer cancel()
	writeRev := func() {
		fmt.Fprintf(w, "event: change\ndata: {\"rev\":%d}\n\n", s.store.Rev())
		fl.Flush()
	}
	writeRev()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-events:
			writeRev()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
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

// updateNodeIn 是节点部分更新的入参：全部用指针表达「未传 / 传了」，
// PUT 语义下可以区分「没给字段」与「显式置空」（旧实现用零值判断，
// 导致标题等字段永远清不掉、空 HTML 无法表达）。
type updateNodeIn struct {
	Title      *string  `json:"title"`
	System     *string  `json:"system"`
	Artboard   *string  `json:"artboard"`
	HTML       *string  `json:"html"`
	Width      *int     `json:"width"`
	Height     *int     `json:"height"`
	AutoHeight *bool    `json:"auto_height"`
	X          *float64 `json:"x"`
	Y          *float64 `json:"y"`
	Tags       []string `json:"tags"`
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in updateNodeIn
	if !decodeBody(w, r, &in) {
		return
	}
	updated, err := s.store.Update(id, func(n *Node) {
		if in.Title != nil {
			n.Title = *in.Title
		}
		if in.HTML != nil {
			n.HTML = *in.HTML
		}
		if in.System != nil {
			n.System = *in.System
		}
		if in.Artboard != nil {
			n.Artboard = *in.Artboard
		}
		if in.AutoHeight != nil {
			n.AutoHeight = *in.AutoHeight
		}
		if in.Width != nil && *in.Width > 0 {
			n.Width = *in.Width
		}
		if in.Height != nil && *in.Height > 0 {
			n.Height = *in.Height
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
