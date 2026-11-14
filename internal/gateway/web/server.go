package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/dispatch"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// staticPrefix 是内嵌静态资源的 URL 前缀。
const staticPrefix = "/static/"

// staticVersion 是内嵌静态资源的内容指纹。模板用 ?v=<指纹> 引用资源，
// 这样改了 CSS/JS 之后浏览器缓存会自动失效（静态资源本身仍可长缓存）。
var staticVersion = computeStaticVersion()

func computeStaticVersion() string {
	h := sha256.New()
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, readErr := staticFS.ReadFile(p)
		if readErr != nil {
			return nil
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// assetURL 生成带内容指纹的静态资源地址。
func assetURL(name string) string {
	return staticPrefix + name + "?v=" + staticVersion
}

// maxFormBytes 是 Gateway 表单请求体上限（09 §9.5：请求体 1MB）。
const maxFormBytes = 1 << 20

// maxMultipartMemory 是图片表单的内存阈值：超过部分 ParseMultipartForm
// 自动落临时文件。整包上限由安全中间件的 MaxBodyBytes（48MB）兜底。
const maxMultipartMemory = 8 << 20

// Config 是 Web 宿主的装配参数。所有外部依赖都从组合根注入，
// 本包不直接 import supervisor、security、sse 等并行开发中的包。
type Config struct {
	// Supervisor 按工作区提供 Core 客户端；只通过 Core JSON-RPC 取数据。
	Supervisor contracts.Supervisor
	// Workspace 是单工作区默认值（兼容旧调用）；Workspaces 为空时使用它。
	Workspace string
	// Workspaces 是可切换的工作区列表；第一个是默认工作区。
	Workspaces []string
	// WorkspaceFor 把请求解析成工作区路径；为空时按 adag_workspace cookie
	// （值是在 Workspaces 中的索引）解析，缺失/越界回退到第一个。
	WorkspaceFor func(*http.Request) string
	// CookieSecure 控制工作区选择 cookie 的 Secure 属性。
	CookieSecure bool
	// WorkspacesChanged 在 FilePicker 新增工作区后回调，参数是完整列表。
	// 回调在内部工作区锁内执行，必须快速返回，且不能回头调用 Server。
	WorkspacesChanged func([]string)
	// ProductName 是产品名（如 ada-go Headless），进 bootstrap 投影与页面标题。
	ProductName string
	// BrandName 是品牌名（如 ada-go），进 bootstrap 投影与页面标题；默认 ada-go。
	BrandName string
	// Locale 是 <html lang> 的取值，默认 zh-CN。
	Locale string
	// Identity 是当前身份（可选），进 bootstrap 投影。
	Identity string
	// Events 是外部注入的 SSE handler（避免本包 import internal/gateway/sse）。
	Events http.Handler
	// WorkspaceEvents 是工作区级 SSE handler（/events/workspace：evt.plugin.changed
	// 等；插件启停的浏览器感知面）。nil 时该端点 501。
	WorkspaceEvents http.Handler
	// Registry 是前端贡献注册表数据源（/frontend-registry；nil 时返回空表）。
	Registry RegistrySource
	// Protect 是外部安全中间件（Host/Origin/CSRF/认证等）；为空时直接暴露路由。
	Protect func(http.Handler) http.Handler
	// CSRF 为每次请求返回 token，模板写入隐藏字段；校验由外部中间件负责。
	CSRF func(*http.Request) string
	// LoginPath 是登录页路径，默认 /login。
	LoginPath string
	// PluginUI 是插件前端宿主（nil 时 /ext 静态资源与动作桥 404、无动态块）。
	// 整页 SSR 移除后它不再产出任何 HTML：插件贡献由客户端运行时经
	// /frontend-registry 装载；宿主只经扩展接口提供静态资源、动作绑定、
	// 插件列表与 Markdown 动态块（见 pluginui.go / markdown_block.go）。
	PluginUI PluginUI
}

// Server 是 Web 宿主。它自身不持有 Core 连接或后台协程。
type Server struct {
	cfg     Config
	tmpl    *template.Template
	handler http.Handler

	// workspaces 是配置工作区 + 本次会话通过 FilePicker 打开的工作区。
	// 索引仍是 cookie 的值；动态追加只在本次 Gateway 生命周期内有效。
	workspaceMu sync.RWMutex
	workspaces  []string

	// dispatch 是唯一的消息发送路径（建线程 + turn.start/steer）。
	dispatch *dispatch.Dispatcher
}

// New 解析内嵌模板并构造路由。Supervisor 是必需依赖。
func New(cfg Config) (*Server, error) {
	if cfg.Supervisor == nil {
		return nil, fmt.Errorf("web: Config.Supervisor 不能为空")
	}
	if cfg.ProductName == "" {
		cfg.ProductName = "ada-go"
	}
	if cfg.BrandName == "" {
		cfg.BrandName = "ada-go"
	}
	if cfg.Locale == "" {
		cfg.Locale = "zh-CN"
	}
	if cfg.LoginPath == "" {
		cfg.LoginPath = "/login"
	}
	cfg.Workspaces = normalizeWorkspaceList(cfg.Workspace, cfg.Workspaces)
	if cfg.Workspace == "" && len(cfg.Workspaces) > 0 {
		cfg.Workspace = cfg.Workspaces[0]
	}
	tmpl, err := template.New("web").Funcs(template.FuncMap{
		// icon 输出一个内联 SVG（与 a_da 组件库同源）。
		"icon": func(key string, size int) template.HTML { return iconHTML(key, size) },
		// asset 输出带内容指纹的静态资源地址（改 CSS/JS 后缓存自动失效）。
		"asset": assetURL,
	}).ParseFS(templateFS, "templates/*.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("web: 解析内嵌模板失败: %w", err)
	}
	s := &Server{cfg: cfg, tmpl: tmpl, workspaces: append([]string(nil), cfg.Workspaces...)}
	// 消息发送只有一条路径：表单处理器与插件投递都走同一个 Dispatcher。
	disp, err := dispatch.New(dispatch.Config{Supervisor: cfg.Supervisor, Workspaces: serverWorkspaces{s: s}})
	if err != nil {
		return nil, fmt.Errorf("web: 装配调度层失败: %w", err)
	}
	s.dispatch = disp
	s.handler = s.routes()
	if cfg.Protect != nil {
		s.handler = cfg.Protect(s.handler)
	}
	return s, nil
}

// Handler 返回可直接挂到 http.Server 的处理器（已套用外部 Protect）。
func (s *Server) Handler() http.Handler { return s.handler }

// Dispatch 返回本网关的调度层（内部发送端点与诊断共用同一份）。
func (s *Server) Dispatch() *dispatch.Dispatcher { return s.dispatch }

// Close 目前无需释放资源；SSE handler 的所有权在外部。
func (s *Server) Close() error { return nil }

// routes 注册主流程路由。
//
// 页面路径全部收敛为 history fallback：GET 返回 SPA shell（handleShell），
// 数据由客户端按路径取 JSON 端点；POST 表单端点保持 303 PRG。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	// JSON 数据端点（SPA 取数面，dev-plan R3）。
	mux.HandleFunc("GET /frontend-registry", s.handleFrontendRegistry)
	mux.HandleFunc("GET /partials/dashboard.json", s.handleOverviewJSON)
	mux.HandleFunc("GET /partials/extensions.json", s.handleExtensionsJSON)
	mux.HandleFunc("GET /partials/threads.json", s.handleThreadsJSON)
	mux.HandleFunc("GET /partials/model.json", s.handleModelJSON)
	mux.HandleFunc("GET /partials/transcript/{id}", s.handleTranscriptJSON)
	mux.HandleFunc("GET /threads/{id}/state", s.handleThreadState)
	mux.HandleFunc("POST /markdown.ast", s.handleMarkdownAST)
	mux.HandleFunc("GET /api/workspaces", s.handleWorkspacesAPI)
	mux.HandleFunc("GET /api/fs/list", s.handleFSList)
	mux.HandleFunc("GET /extensions/{id}/file", s.handlePluginFile)
	// 表单端点（POST → 303 PRG；登录/错误页是无 JS 安全页）。
	mux.HandleFunc("POST /threads", s.handleCreateThread)
	mux.HandleFunc("POST /threads/{id}/send", s.handleTurn)
	mux.HandleFunc("POST /threads/{id}/resend", s.handleTurnResend)
	mux.HandleFunc("POST /threads/{id}/abort", s.handleCancel)
	mux.HandleFunc("POST /threads/{id}/archive", s.handleThreadArchive)
	mux.HandleFunc("POST /threads/{id}/delete", s.handleThreadDelete)
	mux.HandleFunc("POST /threads/{id}/interaction/{requestId}", s.handleInteraction)
	// 兼容旧动作路径（M2 的 /turn、/cancel）。
	mux.HandleFunc("POST /threads/{id}/turn", s.handleTurn)
	mux.HandleFunc("POST /threads/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /thread/{id}/turn", s.handleTurn)
	mux.HandleFunc("POST /thread/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /thread/{id}/interaction/{requestId}", s.handleInteraction)
	mux.HandleFunc("POST /model/select", s.handleModelSelect)
	mux.HandleFunc("POST /workspaces/select", s.handleWorkspaceSelect)
	mux.HandleFunc("POST /workspace", s.handleWorkspaceSelect)
	mux.HandleFunc("POST /api/workspace/select", s.handleWorkspaceOpen)
	mux.HandleFunc("POST /workspaces/mkdir", s.handleWorkspaceMkdir)
	mux.HandleFunc("POST /extensions/reload", s.handleExtensionsReload)
	mux.HandleFunc("POST /extensions/{id}/enable", s.handlePluginEnable)
	mux.HandleFunc("POST /extensions/{id}/disable", s.handlePluginDisable)
	mux.HandleFunc("POST /agent-plugins/reload", s.handleAgentPluginsReload)
	// 实时与基础设施。
	mux.HandleFunc("GET /events/{id}", s.handleEvents)
	mux.HandleFunc("GET /events/workspace", func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.WorkspaceEvents == nil {
			http.Error(w, "工作区事件流未装配", http.StatusNotImplemented)
			return
		}
		s.cfg.WorkspaceEvents.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /static/", s.handleStatic)
	// 插件静态资源、动作桥与 route 桥（7.5、7.6）；PluginUI 缺席时 404。
	mux.HandleFunc("GET /ext/{id}/static/{name...}", s.handleExtStatic)
	mux.HandleFunc("GET /ext/{id}/action/{name}", s.handleExtActionRead)
	mux.HandleFunc("POST /ext/{id}/action/{name}", s.handleExtActionWrite)
	mux.HandleFunc("/ext/{id}/api/{path...}", s.handleExtAPI)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	// history fallback：页面路径（/、/threads/{id}、/overview、/workspaces、
	// /extensions、/ext/{id}/… …）的 GET 一律返回 shell；其余方法 404 错误页。
	// 放最后；上面更具体的模式优先匹配。
	mux.HandleFunc("/{path...}", s.handleFallback)
	return mux
}

// WorkspaceCookieName 是工作区选择 cookie；值是在 Config.Workspaces 中的索引。
// 它不是秘密，仅用于选择；服务端始终按配置数组校验。
const WorkspaceCookieName = "adag_workspace"

// WorkspaceCookieMaxAge 是工作区选择 cookie 的有效期（30 天，单位秒）。
const WorkspaceCookieMaxAge = 30 * 24 * 60 * 60

// acquire 按当前请求的工作区取一次 Core 客户端，返回成对的释放函数。
func (s *Server) acquire(ctx context.Context, r *http.Request) (contracts.CoreClient, func(), error) {
	workspace, _ := s.resolveWorkspace(r)
	return s.acquireWorkspace(ctx, workspace)
}

// acquireWorkspace 明确指定工作区取 Core；新建任务时请求体里的 workspace
// 尚未写进 cookie，不能走 resolveWorkspace。
func (s *Server) acquireWorkspace(ctx context.Context, workspace string) (contracts.CoreClient, func(), error) {
	c, err := s.cfg.Supervisor.Acquire(ctx, workspace)
	if err != nil {
		return nil, nil, err
	}
	release := func() { s.cfg.Supervisor.Release(workspace) }
	return c, release, nil
}

// call 在单次请求内取客户端并调用一个方法。
func (s *Server) call(r *http.Request, method string, params any) (json.RawMessage, error) {
	c, release, err := s.acquire(r.Context(), r)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.Call(r.Context(), method, params)
}

// callWorkspace 与 call 相同，但显式指定工作区（用于“先选工作区，再建线程”）。
func (s *Server) callWorkspace(r *http.Request, workspace, method string, params any) (json.RawMessage, error) {
	c, release, err := s.acquireWorkspace(r.Context(), workspace)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.Call(r.Context(), method, params)
}

// base 填充 shell/login/error 共用的最小外壳，并生成 bootstrap JSON 投影。
func (s *Server) base(r *http.Request, title string) baseData {
	workspace, index := s.resolveWorkspace(r)
	name := workspaceName(workspace)
	csrf := s.csrf(r)
	gateway := "http://" + r.Host
	badge := productBadge(s.cfg.BrandName, s.cfg.ProductName)
	boot := bootstrapData{
		Route:          r.URL.Path,
		Locale:         s.cfg.Locale,
		Title:          title,
		ProductName:    s.cfg.ProductName,
		BrandName:      s.cfg.BrandName,
		ProductBadge:   badge,
		Identity:       s.cfg.Identity,
		LoginPath:      s.cfg.LoginPath,
		CSRF:           csrf,
		Workspace:      workspace,
		WorkspaceName:  name,
		WorkspaceIndex: index,
		Workspaces:     s.workspaceOptions(),
		Gateway:        gateway,
	}
	raw, err := json.Marshal(boot)
	if err != nil {
		// bootstrap 不可省略（客户端首屏依赖）：退化成最小可用投影。
		raw = []byte(`{"route":"/","productName":"ada-go","brandName":"ada-go","loginPath":"/login"}`)
	}
	return baseData{
		Title:         title,
		ProductName:   s.cfg.ProductName,
		BrandName:     s.cfg.BrandName,
		ProductBadge:  badge,
		Locale:        s.cfg.Locale,
		Identity:      s.cfg.Identity,
		LoginPath:     s.cfg.LoginPath,
		CSRF:          csrf,
		Workspace:     workspace,
		WorkspaceName: name,
		Workspaces:    boot.Workspaces,
		Gateway:       gateway,
		Flash:         flashFromQuery(r),
		BootstrapJSON: template.JS(raw),
	}
}

// normalizeWorkspaceList 合并单/多工作区配置并去重，保持配置顺序。
func normalizeWorkspaceList(single string, many []string) []string {
	if len(many) == 0 {
		if strings.TrimSpace(single) == "" {
			return nil
		}
		return []string{single}
	}
	out := make([]string, 0, len(many))
	seen := make(map[string]struct{}, len(many))
	for _, workspace := range many {
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			continue
		}
		if _, ok := seen[workspace]; ok {
			continue
		}
		seen[workspace] = struct{}{}
		out = append(out, workspace)
	}
	return out
}

// resolveWorkspace 返回当前请求的工作区及索引。
// 优先级：?ws=<index>（跨工作区导航）→ cookie → WorkspaceFor。
func (s *Server) resolveWorkspace(r *http.Request) (string, int) {
	list := s.workspaceList()
	if len(list) == 0 {
		return s.cfg.Workspace, 0
	}
	if workspace, index, ok := s.workspaceFromQuery(r); ok {
		return workspace, index
	}
	if index, ok := workspaceIndexFromCookie(r, len(list)); ok {
		return list[index], index
	}
	if s.cfg.WorkspaceFor != nil {
		if workspace := s.cfg.WorkspaceFor(r); workspace != "" {
			if i := workspaceIndexFolded(list, workspace); i >= 0 {
				return list[i], i
			}
		}
	}
	return list[0], 0
}

// workspaceFromQuery 解析侧栏会话链接携带的 ?ws=<index>（显式跨工作区导航）。
func (s *Server) workspaceFromQuery(r *http.Request) (string, int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("ws"))
	if raw == "" {
		return "", 0, false
	}
	index, err := strconv.Atoi(raw)
	list := s.workspaceList()
	if err != nil || index < 0 || index >= len(list) {
		return "", 0, false
	}
	return list[index], index, true
}

// WorkspaceForRequest 返回与页面渲染一致的工作区路径（含 FilePicker 动态登记）。
// Gateway 的 SSE、插件渲染桥等旁路入口必须使用它，否则切到新工作区后
// 页面读的是新 Core、事件流却连在旧 Core 上，表现为会话页一直空白。
func (s *Server) WorkspaceForRequest(r *http.Request) string {
	workspace, _ := s.resolveWorkspace(r)
	return workspace
}

// workspaceList 返回配置工作区 + 本次会话打开的工作区（只读副本）。
func (s *Server) workspaceList() []string {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	return append([]string(nil), s.workspaces...)
}

// serverWorkspaces 把页面自己的工作区列表接给调度层。
//
// 列表的所有权仍在页面（cookie 索引、FilePicker 动态登记），调度层只问不存：
// 两处各存一份，切换工作区后必然有一处是旧的。
type serverWorkspaces struct{ s *Server }

func (w serverWorkspaces) Default() string { return w.s.primaryWorkspace() }

func (w serverWorkspaces) List() []dispatch.Workspace {
	options := w.s.workspaceOptions()
	out := make([]dispatch.Workspace, 0, len(options))
	for _, opt := range options {
		out = append(out, dispatch.Workspace{Name: opt.Name, Path: opt.Path})
	}
	return out
}

// Resolve 只接受「当前已在列表里」的工作区：调度层不为调用方登记新工作区，
// 那属于页面的 FilePicker 流程（要写 cookie、要回调 WorkspacesChanged）。
//
// 比较复用页面自己的 workspaceIndexFolded（大小写与真实落点都算同一处），
// 否则同一工作区在页面能用、在调度层却被判成不存在。
func (w serverWorkspaces) Resolve(path string) (string, bool) {
	list := w.s.workspaceList()
	if index := workspaceIndexFolded(list, path); index >= 0 {
		return list[index], true
	}
	return "", false
}

// registerWorkspace 把 FilePicker 选中的目录登记成本次会话的工作区。
// 已存在（按真实路径比较）时复用原索引；否则追加到列表末尾。
func (s *Server) registerWorkspace(path string) (int, bool) {
	real, err := realPath(path)
	if err != nil {
		return -1, false
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return -1, false
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	for i, candidate := range s.workspaces {
		if candidateReal, err := realPath(candidate); err == nil && samePath(candidateReal, real) {
			return i, true
		}
	}
	s.workspaces = append(s.workspaces, real)
	index := len(s.workspaces) - 1
	if s.cfg.WorkspacesChanged != nil {
		s.cfg.WorkspacesChanged(append([]string(nil), s.workspaces...))
	}
	return index, true
}

func workspaceIndex(list []string, workspace string) int {
	for i, candidate := range list {
		if candidate == workspace {
			return i
		}
	}
	return -1
}

// workspaceIndexFromCookie 读取 adag_workspace（索引）；缺失或越界返回 false。
func workspaceIndexFromCookie(r *http.Request, size int) (int, bool) {
	if size <= 0 {
		return 0, false
	}
	c, err := r.Cookie(WorkspaceCookieName)
	if err != nil {
		return 0, false
	}
	index, err := strconv.Atoi(strings.TrimSpace(c.Value))
	if err != nil || index < 0 || index >= size {
		return 0, false
	}
	return index, true
}

// workspaceOptions 生成选择器选项：显示 basename 与完整路径。
func (s *Server) workspaceOptions() []workspaceOption {
	list := s.workspaceList()
	options := make([]workspaceOption, 0, len(list))
	for i, workspace := range list {
		options = append(options, workspaceOption{Index: i, Name: workspaceName(workspace), Path: workspace})
	}
	return options
}

// workspaceName 是工作区在选择器/bootstrap 里的显示名：目录名，根目录退回完整路径。
func workspaceName(path string) string {
	name := filepath.Base(path)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return path
	}
	return name
}

func (s *Server) csrf(r *http.Request) string {
	if s.cfg.CSRF == nil {
		return ""
	}
	return s.cfg.CSRF(r)
}

// render 输出 HTML 页面（只剩 shell/login/error 三个模板）；动态页面一律 no-store（09 §9.3）。
func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("web: 渲染模板 %s 失败: %v", name, err)
	}
}

// redirect 用 303 让 POST 回到 GET 页面（无 JS 的 PRG 流程）。
func redirect(w http.ResponseWriter, path string) {
	w.Header().Set("Location", path)
	w.WriteHeader(http.StatusSeeOther)
}

// threadPath 生成会话页路径（与 a_da 对齐）。
func threadPath(id string) string { return "/threads/" + url.PathEscape(id) }

// newIdempotencyKey 生成 turn.start 需要的幂等键（8–64 字符）。
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	// 极端情况下退化为进程内唯一串；仍满足长度要求。
	return fmt.Sprintf("idem-%d-%d", time.Now().UnixNano(), fallbackSeq.Add(1))
}

var fallbackSeq atomic.Uint64

// flashFromQuery 把 ?error= / ?notice= 转成页面提示条（插件启停等动作的业务回执）。
func flashFromQuery(r *http.Request) template.HTML {
	q := r.URL.Query()
	if msg := strings.TrimSpace(q.Get("error")); msg != "" {
		return flashHTML("error", clip(msg, 600))
	}
	if msg := strings.TrimSpace(q.Get("notice")); msg != "" {
		return flashHTML("success", clip(msg, 600))
	}
	return ""
}

// productBadge 从“品牌名 + 产品名”里拆出产品徽标（ada-go Headless → Headless）。
func productBadge(brand, product string) string {
	brand = strings.TrimSpace(brand)
	product = strings.TrimSpace(product)
	if product == "" || product == brand {
		return ""
	}
	if brand != "" && len(product) >= len(brand) && strings.EqualFold(product[:len(brand)], brand) {
		if rest := strings.TrimSpace(product[len(brand):]); rest != "" {
			return rest
		}
	}
	return product
}

// parseTimeMS 把 Core 的 RFC3339 时间转成 epoch 毫秒；失败返回 0（不显示相对时间）。
func parseTimeMS(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UnixMilli()
		}
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	return 0
}
