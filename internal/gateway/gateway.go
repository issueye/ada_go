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
	"strings"
	"time"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/relay"
	"github.com/adag/ada-go/internal/gateway/security"
	"github.com/adag/ada-go/internal/gateway/sse"
	"github.com/adag/ada-go/internal/gateway/supervisor"
	"github.com/adag/ada-go/internal/gateway/web"
)

// Config 是 Gateway 的装配输入。
type Config struct {
	Workspace string
	Product   string
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
}

// Gateway 持有 HTTP handler 与它管理的 Core 进程。
type Gateway struct {
	cfg     Config
	product *coreapp.Product
	token   string
	sup     *supervisor.Supervisor
	web     *web.Server
	handler http.Handler
}

// New 装配 Gateway。调用方必须最终调用 Shutdown。
func New(ctx context.Context, cfg Config) (*Gateway, error) {
	if strings.TrimSpace(cfg.Workspace) == "" {
		return nil, fmt.Errorf("gateway: 需要工作区目录")
	}
	abs, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	cfg.Workspace = abs
	if strings.TrimSpace(cfg.Product) == "" {
		return nil, fmt.Errorf("gateway: 需要产品声明")
	}
	product, err := coreapp.LoadProduct(cfg.Product)
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

	sup, err := supervisor.New(supervisor.Options{
		Binary: cfg.Binary, Product: cfg.Product, Provider: cfg.Provider, Model: cfg.Model,
		BaseURL: cfg.BaseURL, DataDir: cfg.DataDir, AllowHosts: cfg.AllowHosts, Fake: cfg.Fake,
		Token: coreToken, MaxCores: cfg.MaxCores, IdleTimeout: cfg.Idle,
		Dial: relay.Dial,
	})
	if err != nil {
		return nil, err
	}

	mw, err := security.New(security.Config{
		Token: accessToken, Secure: cfg.Secure,
		AllowedOrigins: cfg.AllowedOrigins, LoginPath: "/login",
	})
	if err != nil {
		_ = sup.Shutdown(context.Background())
		return nil, err
	}

	events := &eventHandler{sup: sup, workspace: abs}
	webSrv, err := web.New(web.Config{
		Supervisor: sup, Workspace: abs,
		ProductName: product.Name, Locale: product.Identity.Locale,
		Events: events, CSRF: mw.CSRFToken, LoginPath: "/login",
	})
	if err != nil {
		_ = sup.Shutdown(context.Background())
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
		cfg: cfg, product: product, token: accessToken, sup: sup, web: webSrv,
		handler: mw.Wrap(root),
	}
	return g, nil
}

// Handler 返回可直接挂到 http.Server 的 handler。
func (g *Gateway) Handler() http.Handler { return g.handler }

// AccessToken 返回本次 Gateway 的访问令牌（自动生成时用于首次配对展示）。
func (g *Gateway) AccessToken() string { return g.token }

// Workspace 返回归一化后的工作区路径。
func (g *Gateway) Workspace() string { return g.cfg.Workspace }

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
	if g.sup != nil {
		return g.sup.Shutdown(ctx)
	}
	return nil
}

// eventHandler 为每条 SSE 连接取一个 Core 句柄，连接结束即 Release。
// 每个连接一个 Hub 意味着断线后 Last-Event-ID 回放只在本次连接内有界；
// 跨连接重同步由客户端调用 thread.get 完成。
type eventHandler struct {
	sup       contracts.Supervisor
	workspace string
	opts      sse.Options
}

func (h *eventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	core, err := h.sup.Acquire(r.Context(), h.workspace)
	if err != nil {
		http.Error(w, "Core 不可用: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer h.sup.Release(h.workspace)
	hub := sse.New(core, h.opts)
	defer hub.Close()
	hub.ServeHTTP(w, r)
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
