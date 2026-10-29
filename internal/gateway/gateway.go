// Package gateway 是 M2 的组合根：把工作区监督者、Core RPC relay、
// 安全中间件、SSE 与 SSR 前端接成一个 HTTP 服务。
//
// 依赖方向：gateway 知道 supervisor/relay/security/sse/web；这些子包互不感知，
// 只通过 contracts 与 http.Handler 接口协作。
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/relay"
	"github.com/adag/ada-go/internal/gateway/security"
	"github.com/adag/ada-go/internal/gateway/sse"
	"github.com/adag/ada-go/internal/gateway/supervisor"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/internal/plugins/host"
)

// Config 是 Gateway 的装配输入。
type Config struct {
	// Workspace 是单工作区兼容字段；Workspaces 为空时使用它。
	Workspace string
	// Workspaces 是 Gateway 管理的全部工作区。New 会归一化（绝对路径、去重），
	// 至少需要一个；页面可通过 cookie 在这些工作区之间切换。
	Workspaces []string
	Product    string
	// Binary 是 adag 可执行文件（spawn Core 用）；空时 supervisor 取 os.Executable。
	Binary string
	// Provider/Model/BaseURL/DataDir/AllowHosts 原样传给 Core。
	Provider   string
	Model      string
	BaseURL    string
	DataDir    string
	AllowHosts []string
	Fake       bool
	// Token 是 Gateway 的访问令牌；空时自动生成。
	Token string
	// CoreToken 是 Gateway↔Core 的 RPC 令牌；空时自动生成。
	CoreToken string
	MaxCores  int
	Idle      time.Duration
	// Secure 控制 cookie 的 Secure 属性。
	Secure bool
	// AllowedOrigins 是额外允许的跨源 Origin。
	AllowedOrigins []string
	// Supervisor 是可选的监督者注入点：非空时跳过真实 Core 进程装配，
	// 生命周期也由调用方负责（Gateway.Shutdown 不会关闭它）。测试用于
	// 在不 spawn Core 的情况下验证鉴权、SSR 与错误页。
	Supervisor contracts.Supervisor
	// FrontendScripts 是前端插件（frontend-only）的加载单元（M4，7.5）：
	// 非空时组合根构建 web.PluginUI 渲染桥，SSR 填充插槽并提供 /ext 页面。
	// 脚本由上层组合根注入（app/ 层）——internal/ 不 import official/。
	FrontendScripts []host.Script
}

// Gateway 持有 HTTP handler 与它管理的 Core 进程。
type Gateway struct {
	cfg     Config
	product *coreapp.Product
	token   string
	sup     contracts.Supervisor
	ownSup  bool
	web     *web.Server
	handler http.Handler
}

// New 装配 Gateway。调用方必须最终调用 Shutdown。
func New(ctx context.Context, cfg Config) (*Gateway, error) {
	workspaces, err := normalizeWorkspaces(cfg.Workspace, cfg.Workspaces)
	if err != nil {
		return nil, err
	}
	cfg.Workspaces = workspaces
	cfg.Workspace = workspaces[0]
	if len(workspaces) > 1 && strings.TrimSpace(cfg.DataDir) != "" {
		// 每个工作区必须有自己的 state.db（Core 单写者 + 工作区锁）。
		// 多工作区共用 --data-dir 会让两个 Core 同时写同一个 SQLite，直接拒绝。
		return nil, fmt.Errorf("gateway: 多工作区不能共享 --data-dir；留空让每个 Core 使用各自的默认目录")
	}
	if strings.TrimSpace(cfg.Product) == "" {
		return nil, fmt.Errorf("gateway: 需要产品声明")
	}
	product, err := coreapp.LoadProduct(cfg.Product)
	if err != nil {
		return nil, err
	}
	frontendScripts, err := selectFrontendScripts(cfg.FrontendScripts, product)
	if err != nil {
		return nil, err
	}

	accessToken := strings.TrimSpace(cfg.Token)
	if accessToken == "" {
		accessToken, err = randomToken()
		if err != nil {
			return nil, err
		}
	}
	coreToken := strings.TrimSpace(cfg.CoreToken)
	if coreToken == "" {
		coreToken, err = randomToken()
		if err != nil {
			return nil, err
		}
	}

	var sup contracts.Supervisor
	ownSup := false
	if cfg.Supervisor != nil {
		// 测试注入：不启动真实 Core，监督者生命周期归调用方。
		sup = cfg.Supervisor
	} else {
		realSup, err := supervisor.New(supervisor.Options{
			Binary: cfg.Binary, Product: cfg.Product, Provider: cfg.Provider, Model: cfg.Model,
			BaseURL: cfg.BaseURL, DataDir: cfg.DataDir, AllowHosts: cfg.AllowHosts, Fake: cfg.Fake,
			Token: coreToken, MaxCores: cfg.MaxCores, IdleTimeout: cfg.Idle,
			Dial: relay.Dial,
		})
		if err != nil {
			return nil, err
		}
		sup = realSup
		ownSup = true
	}

	mw, err := security.New(security.Config{
		Token: accessToken, Secure: cfg.Secure,
		AllowedOrigins: cfg.AllowedOrigins, LoginPath: "/login",
	})
	if err != nil {
		if ownSup {
			_ = sup.Shutdown(context.Background())
		}
		return nil, err
	}

	// web 与 SSE 使用同一个解析器：cookie 索引 → 配置里的工作区，
	// 越界/损坏时回退到第一个工作区。两个入口不会出现工作区不一致。
	resolveWorkspace := workspaceResolver(workspaces)
	events := &eventHandler{sup: sup, workspaceFor: resolveWorkspace}
	// M4 前端插件渲染桥（7.5）：脚本由上层注入；为空时全部插槽保持基座占位。
	var pluginUI web.PluginUI
	if len(frontendScripts) > 0 {
		bridge, err := frontend.NewBridge(frontend.BridgeOptions{
			Scripts:      frontendScripts,
			Supervisor:   sup,
			WorkspaceFor: resolveWorkspace,
			ProductName:  product.Name,
			CSRF:         mw.CSRFToken,
		})
		if err != nil {
			if ownSup {
				_ = sup.Shutdown(context.Background())
			}
			return nil, fmt.Errorf("gateway: 前端插件装配失败: %w", err)
		}
		pluginUI = bridge
	}
	webSrv, err := web.New(web.Config{
		Supervisor: sup, Workspace: cfg.Workspace, Workspaces: workspaces,
		WorkspaceFor: resolveWorkspace, CookieSecure: cfg.Secure,
		ProductName: product.Name, BrandName: product.Identity.Name,
		Locale: product.Identity.Locale,
		Events: events, CSRF: mw.CSRFToken, LoginPath: "/login",
		PluginUI: pluginUI,
	})
	if err != nil {
		if ownSup {
			_ = sup.Shutdown(context.Background())
		}
		return nil, err
	}

	root := http.NewServeMux()
	root.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// v1 单用户：登录页只负责下发会话与 CSRF cookie；
			// 真正的凭据校验属于后续版本的配置/配对流程。
			mw.IssueSession(w, r)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		webSrv.Handler().ServeHTTP(w, r)
	})
	root.Handle("/", webSrv.Handler())

	g := &Gateway{
		cfg: cfg, product: product, token: accessToken, sup: sup, ownSup: ownSup, web: webSrv,
		handler: tokenLoginGate{next: mw.Wrap(root), mw: mw},
	}
	return g, nil
}

// tokenLoginGate 支持「?token= 自动登录」（a_da 桌面壳/托盘的同款语义）：
// GET 带 token 且匹配 → 下发会话 cookie → 303 到去掉 token 的同路径，
// 网关令牌不留在地址栏与历史记录；不匹配/缺失则走正常鉴权链。
type tokenLoginGate struct {
	next http.Handler
	mw   *security.Middleware
}

func (h tokenLoginGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if tk := r.URL.Query().Get("token"); tk != "" && h.mw.CheckToken(tk) {
			h.mw.IssueSession(w, r)
			u := *r.URL
			q := u.Query()
			q.Del("token")
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
	}
	h.next.ServeHTTP(w, r)
}

// Handler 返回可直接挂到 http.Server 的 handler。
func (g *Gateway) Handler() http.Handler { return g.handler }

// AccessToken 返回本次 Gateway 的访问令牌（自动生成时用于首次配对展示）。
func (g *Gateway) AccessToken() string { return g.token }

// Workspace 返回第一个归一化后的工作区路径（单工作区兼容）。
func (g *Gateway) Workspace() string { return g.cfg.Workspace }

// Workspaces 返回全部归一化后的工作区路径（只读副本）。
func (g *Gateway) Workspaces() []string {
	return append([]string(nil), g.cfg.Workspaces...)
}

// Product 返回已加载的产品声明。
func (g *Gateway) Product() *coreapp.Product { return g.product }

// Shutdown 停止 HTTP 之外的资源：Core 子进程与后台 janitor。
func (g *Gateway) Shutdown(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if g.web != nil {
		_ = g.web.Close()
	}
	if g.sup != nil && g.ownSup {
		return g.sup.Shutdown(ctx)
	}
	return nil
}

// eventHandler 为每条 SSE 连接取一个 Core 句柄，连接结束即 Release。
// 每个连接一个 Hub 意味着断线后 Last-Event-ID 回放只在本次连接内有界；
// 跨连接重同步由客户端调用 thread.get 完成。
type eventHandler struct {
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
	opts         sse.Options
}

func (h *eventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	workspace := ""
	if h.workspaceFor != nil {
		workspace = h.workspaceFor(r)
	}
	core, err := h.sup.Acquire(r.Context(), workspace)
	if err != nil {
		http.Error(w, "Core 不可用: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer h.sup.Release(workspace)
	hub := sse.New(core, h.opts)
	defer hub.Close()
	hub.ServeHTTP(w, r)
}

// normalizeWorkspaces 合并单/多工作区配置：Workspaces 非空时优先；
// 每个路径取绝对路径，去重并保持顺序；至少保留一个。
func normalizeWorkspaces(workspace string, workspaces []string) ([]string, error) {
	raw := workspaces
	if len(raw) == 0 {
		raw = []string{workspace}
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, candidate := range raw {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, fmt.Errorf("gateway: 归一化工作区 %q 失败: %w", candidate, err)
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gateway: 需要工作区目录")
	}
	return out, nil
}

// workspaceResolver 把 adag_workspace cookie（配置数组索引）解析成工作区路径。
// cookie 不是秘密，只用于选择；缺失、非数字或越界都回退到第一个工作区。
func workspaceResolver(workspaces []string) func(*http.Request) string {
	return func(r *http.Request) string {
		if len(workspaces) == 0 {
			return ""
		}
		if c, err := r.Cookie(web.WorkspaceCookieName); err == nil {
			if n, convErr := strconv.Atoi(strings.TrimSpace(c.Value)); convErr == nil && n >= 0 && n < len(workspaces) {
				return workspaces[n]
			}
		}
		return workspaces[0]
	}
}

func randomToken() (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("gateway: 生成令牌失败: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// DefaultIdle 是 Gateway 默认的空闲回收时间（与 supervisor 默认一致）。
const DefaultIdle = 15 * time.Minute

// selectFrontendScripts 按产品声明过滤前端插件：产品声明是「启用哪些插件」的承诺，
// 前端插件也一样——只渲染产品启用且随包提供的前端插件；未启用的不进渲染桥。
// 产品声明了前端 id 但脚本缺失时，由 Session 侧的装配校验统一报错（这里不重复）。
func selectFrontendScripts(scripts []host.Script, product *coreapp.Product) ([]host.Script, error) {
	if len(scripts) == 0 {
		return nil, nil
	}
	declared := map[string]bool{}
	for _, id := range product.Plugins {
		declared[id] = true
	}
	ids := make([]string, 0, len(scripts))
	byID := map[string]host.Script{}
	for _, s := range scripts {
		if s.Manifest == nil {
			return nil, fmt.Errorf("gateway: 前端插件缺少清单")
		}
		byID[s.Manifest.ID] = s
		ids = append(ids, s.Manifest.ID)
	}
	sort.Strings(ids)
	out := make([]host.Script, 0, len(ids))
	for _, id := range ids {
		if declared[id] {
			out = append(out, byID[id])
		}
	}
	return out, nil
}
