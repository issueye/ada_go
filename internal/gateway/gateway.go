// Package gateway 是 M2 的组合根：把工作区监督者、Core RPC relay、
// 安全中间件、SSE 与 SSR 前端接成一个 HTTP 服务。
//
// 依赖方向：gateway 知道 supervisor/relay/security/sse/web；这些子包互不感知，
// 只通过 contracts 与 http.Handler 接口协作。
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/common/domain"
	pluginapi "github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/dispatch"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/processmgr"
	"github.com/adag/ada-go/internal/gateway/relay"
	"github.com/adag/ada-go/internal/gateway/security"
	"github.com/adag/ada-go/internal/gateway/sse"
	"github.com/adag/ada-go/internal/gateway/supervisor"
	"github.com/adag/ada-go/internal/gateway/web"
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
	// Provider/Model/BaseURL/DataDir 原样传给 Core。
	Provider string
	Model    string
	BaseURL  string
	DataDir  string
	Fake     bool
	// Debug 让 Core 把每次出网请求摘要写到自己的 stderr（网关再转发到本进程）。
	Debug bool
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
	// FrontendScripts 是带 frontend 段的插件加载单元（7.5；含统一插件包）：
	// 非空时组合根构建插件宿主桥（/ext 静态资源、动作桥、Markdown 动态块）。
	// 脚本由上层组合根注入（app/ 层）——internal/ 不 import official/。
	FrontendScripts []pluginapi.Script
	// WorkspacesFile 是工作区列表的持久化文件；FilePicker 新登记的工作区
	// 会写到这里，下次启动再合并回列表。空表示不持久化。
	// 显式传 DataDir 时忽略（隔离数据目录不共享用户级工作区列表）。
	WorkspacesFile string
	// Processes 是可选的进程管理器注入点（测试用）：非空时直接使用，
	// 生命周期仍归 Gateway（Shutdown 会关闭它）。nil 时按默认参数创建一个。
	Processes *processmgr.Manager
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
	// control 给 Core 签发内部发送凭据；controlTokens 是这些凭据的表。
	control       *controlPlane
	controlTokens *dispatch.Tokens
	// processes 是 Gateway 名下非 agent-worker 进程的唯一父进程（计划 P3）：
	// 插件注册的 scheduler 之类由它 spawn/重启/回收，agent worker 仍归 Supervisor。
	processes *processmgr.Manager
	// processEndpoint 是 /internal/processes/* 的内部端点（P4 插件进程桥的服务端）。
	// 同时持有「哪个 Core 注册了哪些进程」的索引，供 Core 退出/重签令牌时清理。
	processEndpoint *processmgr.Endpoint
}

// frontendGate 提供「某工作区里某前端插件是否启用」的短期缓存。
// 渲染桥每个插槽贡献都会问一次；缓存避免一次页面渲染打十几次 plugin.list。
type frontendGate struct {
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
	mu           sync.Mutex
	byWorkspace  map[string]frontendEnabledEntry
}

type frontendEnabledEntry struct {
	at       time.Time
	set      map[string]bool
	known    map[string]bool
	allowAll bool // Core 暂时不可用：按启用渲染，避免整页插件区消失
}

const frontendEnabledTTL = 2 * time.Second

func newFrontendGate(sup contracts.Supervisor, workspaceFor func(*http.Request) string) *frontendGate {
	return &frontendGate{sup: sup, workspaceFor: workspaceFor, byWorkspace: map[string]frontendEnabledEntry{}}
}

// Enabled 实现 frontend.BridgeOptions.Enabled。
func (g *frontendGate) Enabled(r *http.Request, pluginID string) bool {
	workspace := ""
	if g.workspaceFor != nil {
		workspace = g.workspaceFor(r)
	}
	entry, err := g.snapshot(workspace)
	if err != nil {
		return true
	}
	return entry.enabledIn(pluginID)
}

// enabledIn 是启用判定的纯逻辑（便于单测）：
//   - Core 明确列出的插件按启用状态；
//   - 未出现在 plugin.list 里的插件视为「Core 不加载的前端-only 插件」
//     （只贡献 Markdown 动态块的第三方包），安装即启用；要停用就移除目录；
//   - Core 不可用时按启用渲染，避免整页插件区消失。
func (e frontendEnabledEntry) enabledIn(pluginID string) bool {
	if e.allowAll {
		return true
	}
	if e.set[pluginID] {
		return true
	}
	if e.known[pluginID] {
		return false
	}
	return true
}

// Invalidate 丢弃一个工作区的缓存；workspace 为空时清空全部。
func (g *frontendGate) Invalidate(workspace string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if workspace == "" {
		g.byWorkspace = map[string]frontendEnabledEntry{}
		return
	}
	delete(g.byWorkspace, workspace)
}

func (g *frontendGate) snapshot(workspace string) (frontendEnabledEntry, error) {
	g.mu.Lock()
	if e, ok := g.byWorkspace[workspace]; ok && time.Since(e.at) < frontendEnabledTTL {
		g.mu.Unlock()
		return e, nil
	}
	g.mu.Unlock()

	// 每个工作区最多每 2s 查一次；失败也缓存 allowAll，避免 Core 故障时
	// 每个插槽都等满超时。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entry := frontendEnabledEntry{at: time.Now()}
	core, err := g.sup.Acquire(ctx, workspace)
	if err != nil {
		entry.allowAll = true
	} else {
		raw, callErr := core.Call(ctx, "plugin.list", map[string]any{})
		g.sup.Release(workspace)
		if callErr != nil {
			entry.allowAll = true
		} else {
			var res struct {
				Plugins []struct {
					ID      string `json:"id"`
					Enabled bool   `json:"enabled"`
					Status  string `json:"status"`
				} `json:"plugins"`
			}
			if jsonErr := json.Unmarshal(raw, &res); jsonErr != nil {
				entry.allowAll = true
			} else {
				entry.set = make(map[string]bool, len(res.Plugins))
				entry.known = make(map[string]bool, len(res.Plugins))
				for _, p := range res.Plugins {
					if p.ID == "" {
						continue
					}
					entry.known[p.ID] = true
					if !p.Enabled {
						continue
					}
					switch p.Status {
					case "", "ready", "degraded":
						entry.set[p.ID] = true
					}
				}
			}
		}
	}
	g.mu.Lock()
	g.byWorkspace[workspace] = entry
	g.mu.Unlock()
	return entry, err
}

// New 装配 Gateway。调用方必须最终调用 Shutdown。
func New(ctx context.Context, cfg Config) (*Gateway, error) {
	workspaces, err := normalizeWorkspaces(cfg.Workspace, cfg.Workspaces)
	if err != nil {
		return nil, err
	}
	// 用户级工作区列表只在默认数据目录下启用；显式 --data-dir 表示隔离运行。
	persistWorkspaces := strings.TrimSpace(cfg.WorkspacesFile) != "" && strings.TrimSpace(cfg.DataDir) == ""
	if persistWorkspaces {
		saved, loadErr := loadWorkspacesFile(cfg.WorkspacesFile)
		if loadErr != nil {
			log.Printf("gateway: 读取工作区列表失败（忽略）: %v", loadErr)
		}
		if len(saved) > 0 {
			merged := append(append([]string(nil), workspaces...), saved...)
			workspaces, err = normalizeWorkspaces("", merged)
			if err != nil {
				return nil, err
			}
		}
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
	frontendScripts := cfg.FrontendScripts

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

	// 内部发送凭据：给每个 Core 签一次，Core 侧据此安装 ctx.dispatch。
	// 插件脚本永远看不到令牌；令牌只经环境变量交给刚拉起的那个进程。
	controlTokens := dispatch.NewTokens()
	control := newControlPlane(controlTokens)

	// 进程管理器：插件注册的长驻进程（cron scheduler 等）挂在 Gateway 名下，
	// 与 agent-worker（Supervisor）生命周期分开；任何启动失败都不得拖垮 HTTP 面。
	//
	// 注入 control 是为了让被管进程拿到「只能发消息」的专用令牌：
	// scheduler 到点要经调度层投递，但它不该继承宿主凭据，也不该能反向注册进程。
	processes := cfg.Processes
	if processes == nil {
		processes, err = processmgr.New(processmgr.Options{
			Logf:    func(format string, args ...any) { log.Printf(format, args...) },
			Control: control,
		})
		if err != nil {
			return nil, fmt.Errorf("gateway: 创建进程管理器失败: %w", err)
		}
	}
	// 后续任一步装配失败都要回收已经拉起的 Core 与被管进程，否则会留下孤儿。
	var sup contracts.Supervisor
	ownSup := false
	cleanupPartial := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = processes.Shutdown(shutdownCtx)
		if ownSup {
			_ = sup.Shutdown(shutdownCtx)
		}
	}

	if cfg.Supervisor != nil {
		// 测试注入：不启动真实 Core，监督者生命周期归调用方。
		sup = cfg.Supervisor
	} else {
		realSup, err := supervisor.New(supervisor.Options{
			Binary: cfg.Binary, Product: cfg.Product, Provider: cfg.Provider, Model: cfg.Model,
			BaseURL: cfg.BaseURL, DataDir: cfg.DataDir, Fake: cfg.Fake,
			Debug: cfg.Debug,
			Token: coreToken, MaxCores: cfg.MaxCores, IdleTimeout: cfg.Idle,
			Dial:    relay.Dial,
			Control: control,
		})
		if err != nil {
			cleanupPartial()
			return nil, err
		}
		sup = realSup
		ownSup = true
	}

	mw, err := security.New(security.Config{
		Token: accessToken, Secure: cfg.Secure,
		AllowedOrigins: cfg.AllowedOrigins, LoginPath: "/login",
		// 图片输入（7.7.4）：multipart 表单携带 ≤4 张 × 8MB 图片（base64 膨胀后
		// ~10.6MB/张），1MB 默认上限会把它挡在安全中间件里。
		MaxBodyBytes: 48 << 20,
	})
	if err != nil {
		cleanupPartial()
		return nil, err
	}

	// 页面、SSE 与插件桥必须使用同一套工作区解析：cookie 索引 + 本次会话
	// 通过 FilePicker 登记的动态工作区。动态列表归 web.Server 管，所以先用
	// 静态解析兜底，web.New 之后再切换到它的动态解析。
	resolveWorkspace := workspaceResolver(workspaces)
	requestWorkspace := newWorkspaceRequestResolver(resolveWorkspace)
	events := &eventHandler{sup: sup, workspaceFor: requestWorkspace.Resolve}
	workspaceEvents := &workspaceEventsHandler{sup: sup, workspaceFor: requestWorkspace.Resolve}
	// 插件宿主桥（7.5）：脚本由上层注入；为空时 /ext 静态资源与动作桥 404、
	// 无 Markdown 动态块、前端注册表为空。插件前端贡献由客户端运行时装载
	// （dev-plan R1），桥只提供静态资源、动作绑定、插件列表与块渲染。
	var pluginUI web.PluginUI
	var registrySource web.RegistrySource
	if len(frontendScripts) > 0 {
		gate := newFrontendGate(sup, requestWorkspace.Resolve)
		bridge, err := frontend.NewBridge(frontend.BridgeOptions{
			Scripts:           frontendScripts,
			WorkspaceFor:      requestWorkspace.Resolve,
			ProductName:       product.Name,
			Enabled:           gate.Enabled,
			InvalidateEnabled: gate.Invalidate,
			MarkdownBlocks:    product.Capabilities.MarkdownBlocks,
			BlockAudit: func(ev frontend.BlockAuditEvent) {
				log.Printf("audit %s plugin=%s renderer=%s block=%s error=%s",
					ev.Kind, ev.PluginID, ev.Renderer, ev.BlockID, ev.Error)
			},
		})
		if err != nil {
			cleanupPartial()
			return nil, fmt.Errorf("gateway: 前端插件装配失败: %w", err)
		}
		pluginUI = bridge
		registrySource = bridge
	}
	webSrv, err := web.New(web.Config{
		Supervisor: sup, Workspace: cfg.Workspace, Workspaces: workspaces,
		WorkspaceFor: resolveWorkspace, CookieSecure: cfg.Secure,
		ProductName: product.Name, BrandName: product.Identity.Name,
		Locale: product.Identity.Locale,
		Events: events, WorkspaceEvents: workspaceEvents, CSRF: mw.CSRFToken, LoginPath: "/login",
		PluginUI: pluginUI, Registry: registrySource,
		WorkspacesChanged: func(list []string) {
			if !persistWorkspaces {
				return
			}
			if saveErr := saveWorkspacesFile(cfg.WorkspacesFile, list); saveErr != nil {
				log.Printf("gateway: 保存工作区列表失败: %v", saveErr)
			}
		},
	})
	if err != nil {
		cleanupPartial()
		return nil, err
	}
	requestWorkspace.Set(webSrv.WorkspaceForRequest)

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

	// 内部端点：与用户鉴权**并列**而不是嵌套——进程没有会话 cookie，
	// 也不该有；认证只靠控制令牌 + 回环来源。
	//
	// 两条内部链共用同一个 mux（/internal/* 前缀已被 internalGate 摘出用户鉴权）：
	// 发送（dispatch）与进程监督（processes）。
	internalMux := http.NewServeMux()
	controlEndpoint, err := dispatch.NewEndpoint(dispatch.EndpointConfig{
		Dispatcher: webSrv.Dispatch(),
		Tokens:     controlTokens,
		Log: func(ident dispatch.Identity, sender dispatch.Sender, req dispatch.SendRequest,
			receipt dispatch.Receipt, sendErr error) {
			if sendErr != nil {
				log.Printf("dispatch: sender=%s workspace=%s thread=%s mode=%s error=%v",
					sender.LogKey(), receipt.Workspace, receipt.ThreadID, receipt.Mode, sendErr)
				return
			}
			log.Printf("dispatch: sender=%s workspace=%s thread=%s mode=%s",
				sender.LogKey(), receipt.Workspace, receipt.ThreadID, receipt.Mode)
		},
	})
	if err != nil {
		cleanupPartial()
		return nil, err
	}
	controlEndpoint.Register(internalMux)

	// 进程监督端点：插件经 Core 宿主桥注册长驻进程，网关按 owner 与限额管理。
	processEndpoint, err := processmgr.NewEndpoint(processmgr.EndpointConfig{
		Manager: processes,
		Tokens:  controlTokens,
		Log: func(ident dispatch.Identity, op string, req processmgr.ProcessRequest,
			st *processmgr.Status, opErr error) {
			if opErr != nil {
				log.Printf("processmgr: op=%s owner=%s name=%s workspace=%s error=%v",
					op, req.PluginID, req.Name, ident.Workspace, opErr)
				return
			}
			pid := 0
			if st != nil {
				pid = st.PID
			}
			log.Printf("processmgr: op=%s owner=%s name=%s workspace=%s pid=%d",
				op, req.PluginID, req.Name, ident.Workspace, pid)
		},
	})
	if err != nil {
		cleanupPartial()
		return nil, err
	}
	processEndpoint.Register(internalMux)
	// 重签令牌 = 旧 Core 退场：先回收它注册过的被管进程，再签发新凭据。
	// 这里在 Serve 之前接上，第一个 Core 拉起时一定已经就位。
	control.onIssue = func(key string) {
		if n := processEndpoint.CleanupKey(key); n > 0 {
			log.Printf("processmgr: Core %s 重签前回收 %d 个被管进程", key, n)
		}
	}

	g := &Gateway{
		cfg: cfg, product: product, token: accessToken, sup: sup, ownSup: ownSup, web: webSrv,
		control: control, controlTokens: controlTokens, processes: processes,
		processEndpoint: processEndpoint,
		handler: internalGate{
			internal: internalMux,
			mw:       mw,
			next:     tokenLoginGate{next: mw.Wrap(root), mw: mw},
		},
	}
	return g, nil
}

// internalGate 把内部端点从用户鉴权链里摘出来：
// /internal/* 只走控制令牌（端点自己校验），其余路径照旧走会话鉴权。
//
// 前缀判断而不是精确路由：将来内部端点增加时不需要再动组合根。
type internalGate struct {
	internal http.Handler
	mw       *security.Middleware
	next     http.Handler
}

func (g internalGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/internal/") {
		g.internal.ServeHTTP(w, r)
		return
	}
	g.next.ServeHTTP(w, r)
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

// ControlAccess 返回内部发送端点的根地址与一个用于本工作区的控制令牌。
//
// 供组合根在监听地址确定后调用：网关自己签发、交给它拉起的 Core，
// 不经任何 HTTP 面暴露给插件。
func (g *Gateway) ControlAccess(workspace string) (string, string) {
	if g == nil || g.control == nil {
		return "", ""
	}
	return g.control.ControlAccess(workspace)
}

// SetControlURL 设置内部发送端点的根地址（形如 http://127.0.0.1:52353）。
// 必须在第一个 Core 被拉起之前调用（cmd 在 net.Listen 之后、Serve 之前）。
func (g *Gateway) SetControlURL(url string) {
	if g == nil || g.control == nil {
		return
	}
	g.control.SetURL(url)
}

// Workspace 返回第一个归一化后的工作区路径（单工作区兼容）。
func (g *Gateway) Workspace() string { return g.cfg.Workspace }

// Workspaces 返回全部归一化后的工作区路径（只读副本）。
func (g *Gateway) Workspaces() []string {
	return append([]string(nil), g.cfg.Workspaces...)
}

// Product 返回已加载的产品声明。
func (g *Gateway) Product() *coreapp.Product { return g.product }

// Processes 返回网关名下的进程管理器（P4 的插件进程桥与诊断都从这里接入）。
//
// 返回值可用于注册/查询，但生命周期归 Gateway：调用方不应自行 Shutdown。
func (g *Gateway) Processes() *processmgr.Manager {
	if g == nil {
		return nil
	}
	return g.processes
}

// Shutdown 停止 HTTP 之外的资源：被管进程、Core 子进程与后台 janitor。
//
// 顺序是先停被管进程（它们是插件拉起的，可能正在向 Core 投递消息），
// 再关 Supervisor：反过来会让最后一个在途投递撞上已关闭的 Core。
func (g *Gateway) Shutdown(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if g.web != nil {
		_ = g.web.Close()
	}
	// 控制令牌随网关闭而失效：即使有残留进程持有旧令牌，也调不通已关闭的网关。
	if g.control != nil {
		g.control.revokeControls()
	}
	var procErr error
	if g.processes != nil {
		procErr = g.processes.Shutdown(ctx)
	}
	// 进程本身已由 Manager 回收；这里只丢掉「哪个 Core 注册过什么」的索引。
	if g.processEndpoint != nil {
		g.processEndpoint.CleanupAll()
	}
	if g.sup != nil && g.ownSup {
		if err := g.sup.Shutdown(ctx); err != nil && procErr == nil {
			return err
		}
	}
	return procErr
}

// workspaceRequestResolver 允许 SSE 与前端插件桥在 web.Server 创建后
// 复用同一套动态工作区解析（含 FilePicker 登记的工作区）。创建前先静态兜底。
type workspaceRequestResolver struct {
	mu      sync.RWMutex
	resolve func(*http.Request) string
}

func newWorkspaceRequestResolver(resolve func(*http.Request) string) *workspaceRequestResolver {
	return &workspaceRequestResolver{resolve: resolve}
}

func (r *workspaceRequestResolver) Resolve(req *http.Request) string {
	r.mu.RLock()
	resolve := r.resolve
	r.mu.RUnlock()
	if resolve == nil {
		return ""
	}
	return resolve(req)
}

func (r *workspaceRequestResolver) Set(resolve func(*http.Request) string) {
	r.mu.Lock()
	r.resolve = resolve
	r.mu.Unlock()
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

// workspaceEventsHandler 是 GET /events/workspace：工作区级事件
// （evt.ready / evt.plugin.changed，docs/08 §8.2）的 SSE 流。
//
// 线程级事件不进这条流（sse.Hub 按 threadId 分发并跳过无 threadId 的事件，
// hub.go 的 run()）；反过来工作区事件也独立成流，seq 取 params.workspaceSeq。
// 事件稀疏：不做合并窗口，15s 心跳；不做历史回放——断线重连的客户端以
// GET /frontend-registry 全量重同步为准（dev-plan §8.3）。
type workspaceEventsHandler struct {
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
}

func (h *workspaceEventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	events, cancel := core.Subscribe(64)
	defer cancel()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "连接不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case evt, alive := <-events:
			if !alive {
				return
			}
			if !isWorkspaceEvent(evt.Method) {
				continue
			}
			var buf bytes.Buffer
			if seq := workspaceSeq(evt.Params); seq > 0 {
				fmt.Fprintf(&buf, "id: %d\n", seq)
			}
			fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", evt.Method, evt.Params)
			if _, err := w.Write(buf.Bytes()); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func isWorkspaceEvent(method string) bool {
	return method == string(domain.EvtReady) || method == string(domain.EvtPluginChanged)
}

func workspaceSeq(params json.RawMessage) int64 {
	var meta struct {
		WorkspaceSeq int64 `json:"workspaceSeq"`
	}
	_ = json.Unmarshal(params, &meta)
	return meta.WorkspaceSeq
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

// workspacesFileData 是用户级工作区列表的落盘形状。
type workspacesFileData struct {
	Workspaces []string `json:"workspaces"`
}

// loadWorkspacesFile 读取用户上次打开过的工作区；不存在的目录直接跳过，
// 避免移动/删除项目后启动列表里留下永远打不开的条目。
func loadWorkspacesFile(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var data workspacesFileData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	out := make([]string, 0, len(data.Workspaces))
	for _, candidate := range data.Workspaces {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, abs)
	}
	return out, nil
}

// saveWorkspacesFile 原子写入工作区列表（0600，同目录临时文件 + rename）。
func saveWorkspacesFile(path string, workspaces []string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(workspacesFileData{
		Workspaces: append([]string(nil), workspaces...),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("工作区列表无法序列化: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建工作区状态目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".workspaces-*.json")
	if err != nil {
		return fmt.Errorf("创建工作区状态临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置工作区状态权限失败: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入工作区列表失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭工作区状态文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换工作区列表失败: %w", err)
	}
	return nil
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
