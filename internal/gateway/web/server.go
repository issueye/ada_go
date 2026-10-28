package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
)

//go:embed templates/*.html.tmpl
var templateFS embed.FS

//go:embed static/app.css
var appCSS []byte

// maxFormBytes 是 Gateway 表单请求体上限（09 §9.5：请求体 1MB）。
const maxFormBytes = 1 << 20

// Config 是 SSR 宿主的装配参数。所有外部依赖都从组合根注入，
// 本包不直接 import supervisor、security、sse 等并行开发中的包。
type Config struct {
	// Supervisor 按工作区提供 Core 客户端；SSR 只通过 Core JSON-RPC 取数据。
	Supervisor contracts.Supervisor
	// Workspace 是当前页面绑定的工作区（传给 Supervisor.Acquire）。
	Workspace string
	// ProductName 是品牌名，默认为 ada-go。
	ProductName string
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
	if cfg.Locale == "" {
		cfg.Locale = "zh-CN"
	}
	if cfg.LoginPath == "" {
		cfg.LoginPath = "/login"
	}
	tmpl, err := template.New("web").ParseFS(templateFS, "templates/*.html.tmpl")
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

// routes 注册 M2 主流程路由。使用 Go 1.22 起的方法 + 路径参数模式。
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /threads", s.handleThreads)
	mux.HandleFunc("POST /threads", s.handleCreateThread)
	mux.HandleFunc("GET /thread/{id}", s.handleThread)
	mux.HandleFunc("POST /thread/{id}/turn", s.handleTurn)
	mux.HandleFunc("POST /thread/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /thread/{id}/interaction/{requestId}", s.handleInteraction)
	mux.HandleFunc("GET /events/{id}", s.handleEvents)
	mux.HandleFunc("GET /static/app.css", s.handleCSS)
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	return mux
}

// acquire 取一次 Core 客户端，返回成对的释放函数。
func (s *Server) acquire(ctx context.Context) (contracts.CoreClient, func(), error) {
	c, err := s.cfg.Supervisor.Acquire(ctx, s.cfg.Workspace)
	if err != nil {
		return nil, nil, err
	}
	release := func() { s.cfg.Supervisor.Release(s.cfg.Workspace) }
	return c, release, nil
}

// call 在单次请求内取客户端并调用一个方法。
func (s *Server) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c, release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return c.Call(ctx, method, params)
}

// base 填充所有页面共用的外壳字段。
func (s *Server) base(r *http.Request, title string) baseData {
	return baseData{
		Title:       title,
		ProductName: s.cfg.ProductName,
		Locale:      s.cfg.Locale,
		Identity:    s.cfg.Identity,
		LoginPath:   s.cfg.LoginPath,
		CSRF:        s.csrf(r),
		Workspace:   s.cfg.Workspace,
	}
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

// threadPath 生成会话页路径。
func threadPath(id string) string { return "/thread/" + url.PathEscape(id) }

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
