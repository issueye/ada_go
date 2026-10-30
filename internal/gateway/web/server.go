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
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
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

// Config 是 SSR 宿主的装配参数。所有外部依赖都从组合根注入，
// 本包不直接 import supervisor、security、sse 等并行开发中的包。
type Config struct {
	// Supervisor 按工作区提供 Core 客户端；SSR 只通过 Core JSON-RPC 取数据。
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
	// ProductName 是产品名（如 ada-go Headless），进顶栏 chip 与页面标题。
	ProductName string
	// BrandName 是品牌名（如 ada-go），进侧栏品牌位与页面标题；默认 ada-go。
	BrandName string
	// Locale 是 <html lang> 的取值，默认 zh-CN。
	Locale string
	// Identity 是顶栏展示的当前身份（可选）。
	Identity string
	// Events 是外部注入的 SSE handler（避免本包 import internal/gateway/sse）。
	Events http.Handler
	// Protect 是外部安全中间件（Host/Origin/CSRF/认证等）；为空时直接暴露路由。
	Protect func(http.Handler) http.Handler
	// CSRF 为每次请求返回 token，模板写入隐藏字段；校验由外部中间件负责。
	CSRF func(*http.Request) string
	// LoginPath 是登录页路径，默认 /login。
	LoginPath string
	// PluginUI 是 M4 的前端插件渲染宿主（7.5）；nil 时全部插槽保持基座占位、
	// /ext 路由 404。组合根用渲染桥实现它，web 不依赖插件宿主的具体实现。
	PluginUI PluginUI
}

// Server 是 SSR 前端宿主。它自身不持有 Core 连接或后台协程。
type Server struct {
	cfg     Config
	tmpl    *template.Template
	handler http.Handler
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
		// slot 只接受基座生成的 template.HTML（见 RenderSlot 注释）。
		"slot": RenderSlot,
		// icon 输出一个内联 SVG（与 a_da 组件库同源）。
		"icon": func(key string, size int) template.HTML { return iconHTML(key, size) },
		// markdown 渲染助手正文（goldmark + bluemonday 清洗）。
		"markdown": renderMarkdown,
		// statusTone 把工具状态映射为徽标色调。
		"statusTone": func(status string) string {
			switch status {
			case "ok":
				return "success"
			case "error":
				return "danger"
			case "denied", "timeout", "interrupted":
				return "warn"
			default:
				return "neutral"
			}
		},
		// toolIcon 给工具行挑一个能一眼认出来的图标：文件、目录、终端、git、计划……
		// 认不出来的工具退回扳手，而不是所有工具都画成终端。
		"toolIcon": toolIcon,
		// lineCount 统计文本行数（工具输出的“共 N 行”）。
		"lineCount": func(s string) int {
			if s == "" {
				return 0
			}
			return strings.Count(s, "\n") + 1
		},
		// asset 输出带内容指纹的静态资源地址（改 CSS/JS 后缓存自动失效）。
		"asset": assetURL,
	}).ParseFS(templateFS, "templates/*.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("web: 解析内嵌模板失败: %w", err)
	}
	s := &Server{cfg: cfg, tmpl: tmpl}
	s.handler = s.routes()
	if cfg.Protect != nil {
		s.handler = cfg.Protect(s.handler)
	}
	return s, nil
}

// Handler 返回可直接挂到 http.Server 的处理器（已套用外部 Protect）。
func (s *Server) Handler() http.Handler { return s.handler }

// Close 目前无需释放资源；SSE handler 的所有权在外部。
func (s *Server) Close() error { return nil }

// routes 注册主流程路由（页面名与 a_da 对齐：/ 与 /threads/{id} 是对话页，
// /overview 是概览，/workspaces 是工作区，/extensions 是扩展）。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleChatHome)
	mux.HandleFunc("GET /threads", s.handleChatHome)
	mux.HandleFunc("POST /threads", s.handleCreateThread)
	mux.HandleFunc("GET /overview", s.handleOverview)
	mux.HandleFunc("GET /partials/dashboard", s.handleOverviewPartial)
	mux.HandleFunc("GET /partials/dashboard.json", s.handleOverviewJSON)
	mux.HandleFunc("GET /partials/extensions.json", s.handleExtensionsJSON)
	mux.HandleFunc("GET /threads/{id}", s.handleThread)
	mux.HandleFunc("POST /threads/{id}/send", s.handleTurn)
	mux.HandleFunc("POST /threads/{id}/abort", s.handleCancel)
	mux.HandleFunc("POST /threads/{id}/delete", s.handleThreadDelete)
	mux.HandleFunc("POST /threads/{id}/interaction/{requestId}", s.handleInteraction)
	// 兼容旧动作路径（M2 的 /turn、/cancel）。
	mux.HandleFunc("POST /threads/{id}/turn", s.handleTurn)
	mux.HandleFunc("POST /threads/{id}/cancel", s.handleCancel)
	// 兼容旧路径（M2 的 /thread/{id}/turn|cancel）。
	mux.HandleFunc("GET /thread/{id}", s.handleThread)
	mux.HandleFunc("POST /thread/{id}/turn", s.handleTurn)
	mux.HandleFunc("POST /thread/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /thread/{id}/interaction/{requestId}", s.handleInteraction)
	mux.HandleFunc("GET /events/{id}", s.handleEvents)
	mux.HandleFunc("POST /model/select", s.handleModelSelect)
	mux.HandleFunc("GET /workspaces", s.handleWorkspaces)
	mux.HandleFunc("GET /workspace", s.handleWorkspaceGet)
	mux.HandleFunc("POST /workspaces/select", s.handleWorkspaceSelect)
	mux.HandleFunc("POST /workspace", s.handleWorkspaceSelect)
	mux.HandleFunc("GET /extensions", s.handleExtensions)
	mux.HandleFunc("GET /extensions/{id}", s.handlePluginDetail)
	mux.HandleFunc("POST /extensions/reload", s.handleExtensionsReload)
	mux.HandleFunc("POST /extensions/{id}/enable", s.handlePluginEnable)
	mux.HandleFunc("POST /extensions/{id}/disable", s.handlePluginDisable)
	mux.HandleFunc("POST /agent-plugins/reload", s.handleAgentPluginsReload)
	mux.HandleFunc("GET /static/", s.handleStatic)
	// M4 插件页面、静态资源与动作桥（7.5、7.6）；PluginUI 缺席时 404。
	mux.HandleFunc("GET /ext/{id}/static/{name...}", s.handleExtStatic)
	mux.HandleFunc("GET /ext/{id}/action/{name}", s.handleExtActionRead)
	mux.HandleFunc("POST /ext/{id}/action/{name}", s.handleExtActionWrite)
	// 插件动态 HTTP API（route）：前端插件/浏览器脚本调用。
	mux.HandleFunc("/ext/{id}/api/{path...}", s.handleExtAPI)
	mux.HandleFunc("GET /ext/{id}/{slug}", s.handleExtPage)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return mux
}

// WorkspaceCookieName 是工作区选择 cookie；值是在 Config.Workspaces 中的索引。
// 它不是秘密，仅用于选择；服务端始终按配置数组校验。
const WorkspaceCookieName = "adag_workspace"

// WorkspaceCookieMaxAge 是工作区选择 cookie 的有效期（30 天，单位秒）。
const WorkspaceCookieMaxAge = 30 * 24 * 60 * 60

// JSClientCookieName 标记「这个浏览器已经在跑脚本」（sidebar-boot.js 首屏种下）。
// 它只用来二选一：有 JS 走实时增量，没有 JS 才退到 meta refresh 轮询。
// 有 JS 时不能再发 meta refresh——Chromium 里删掉 meta 并不会取消已经排队的
// 刷新，运行中的页面会变成每 2 秒整页重载一次。
const JSClientCookieName = "adag_js"

// acquire 按当前请求的工作区取一次 Core 客户端，返回成对的释放函数。
func (s *Server) acquire(ctx context.Context, r *http.Request) (contracts.CoreClient, func(), error) {
	workspace, _ := s.resolveWorkspace(r)
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

// base 填充页面外壳；threads 用于侧栏会话树（可为空）。
func (s *Server) base(r *http.Request, page, title string, threads []threadSummaryDTO, activeThreadID string, connected bool) baseData {
	workspace, index := s.resolveWorkspace(r)
	// 插件插槽、样式与脚本由 PluginUI 填充；单槽失败回退基座占位（pluginui.go）。
	slots, counts := s.pluginSlots(r)
	name := filepath.Base(workspace)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = workspace
	}
	gateway := "http://" + r.Host
	conn := connected
	csrf := s.csrf(r)
	pluginNav := s.pluginNavLinks()
	badge := productBadge(s.cfg.BrandName, s.cfg.ProductName)
	sidebar := renderSidebar(sidebarData{
		Active:             page,
		WorkspaceName:      name,
		WorkspacePath:      workspace,
		Gateway:            gateway,
		Groups:             s.sidebarGroups(workspace, threads),
		ActiveThreadID:     activeThreadID,
		SidebarTop:         slots.SidebarTop,
		SidebarTopCount:    counts.SidebarTop,
		SidebarBottom:      slots.SidebarBottom,
		SidebarBottomCount: counts.SidebarBottom,
		// 搜索框跟随「有会话可搜」而不是只跟随聊天页：概览/插件页的侧栏
		// 同样列着会话，没有搜索会让同一份侧栏在页面之间功能不一致。
		Search:       page == "chat" || len(threads) > 0,
		Connected:    &conn,
		ProductName:  s.cfg.ProductName,
		BrandName:    s.cfg.BrandName,
		ProductBadge: badge,
		CSRF:         csrf,
		CurrentPath:  r.URL.Path,
		PluginNav:    pluginNav,
	})
	return baseData{
		Title:               title,
		Page:                page,
		ProductName:         s.cfg.ProductName,
		BrandName:           s.cfg.BrandName,
		ProductBadge:        badge,
		Flash:               flashFromQuery(r),
		Locale:              s.cfg.Locale,
		Identity:            s.cfg.Identity,
		LoginPath:           s.cfg.LoginPath,
		CSRF:                csrf,
		Workspace:           workspace,
		WorkspaceName:       name,
		WorkspaceIndex:      index,
		Workspaces:          s.workspaceOptions(),
		ShowWorkspacePicker: len(s.cfg.Workspaces) > 1,
		Gateway:             gateway,
		ActiveThreadID:      activeThreadID,
		Connected:           connected,
		PluginNav:           pluginNav,
		Sidebar:             sidebar,
		Icons:               iconSprite(),
		ConfirmDialog:       confirmDialogHTML(),
		Slots:               slots,
		SlotCounts:          counts,
		PluginStyles:        s.pluginStyles(),
		PluginScripts:       s.pluginScripts(),
	}
}

// sidebarGroups 把当前工作区的会话铺成一组。其它工作区需要各自的 Core 查询，
// 这里不猜、不显示假计数；切换入口在侧栏的 workspace switch（/workspaces）。
func (s *Server) sidebarGroups(workspace string, threads []threadSummaryDTO) []sidebarGroup {
	rows := make([]sidebarThread, 0, len(threads))
	for _, t := range threads {
		rows = append(rows, sidebarThread{
			ID:        t.ID,
			Title:     t.Title,
			UpdatedMS: parseTimeMS(t.UpdatedAt),
		})
	}
	name := filepath.Base(workspace)
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = workspace
	}
	return []sidebarGroup{{
		Key: workspace, Path: workspace, Name: name,
		Threads: rows, Count: len(rows), Active: true,
		Running: false,
	}}
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
// 优先使用注入的 WorkspaceFor；否则按 cookie 索引解析；任何异常回退到默认。
func (s *Server) resolveWorkspace(r *http.Request) (string, int) {
	list := s.cfg.Workspaces
	if len(list) == 0 {
		return s.cfg.Workspace, 0
	}
	if s.cfg.WorkspaceFor != nil {
		if workspace := s.cfg.WorkspaceFor(r); workspace != "" {
			if i := workspaceIndex(list, workspace); i >= 0 {
				return list[i], i
			}
		}
	}
	index := workspaceIndexFromCookie(r, len(list))
	return list[index], index
}

func workspaceIndex(list []string, workspace string) int {
	for i, candidate := range list {
		if candidate == workspace {
			return i
		}
	}
	return -1
}

// workspaceIndexFromCookie 读取 adag_workspace（索引）；缺失或越界返回 0。
func workspaceIndexFromCookie(r *http.Request, size int) int {
	if size <= 1 {
		return 0
	}
	c, err := r.Cookie(WorkspaceCookieName)
	if err != nil {
		return 0
	}
	index, err := strconv.Atoi(strings.TrimSpace(c.Value))
	if err != nil || index < 0 || index >= size {
		return 0
	}
	return index
}

// workspaceOptions 生成选择器选项：显示 basename 与完整路径。
func (s *Server) workspaceOptions() []workspaceOption {
	options := make([]workspaceOption, 0, len(s.cfg.Workspaces))
	for i, workspace := range s.cfg.Workspaces {
		name := filepath.Base(workspace)
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = workspace
		}
		options = append(options, workspaceOption{Index: i, Name: name, Path: workspace})
	}
	return options
}

func (s *Server) csrf(r *http.Request) string {
	if s.cfg.CSRF == nil {
		return ""
	}
	return s.cfg.CSRF(r)
}

// render 输出 HTML 页面；动态页面一律 no-store（09 §9.3）。
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

// parseLimit 解析分页 limit，并夹在 thread.list 的 1–200 范围内。
func parseLimit(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	if n > 200 {
		return 200
	}
	return n
}
