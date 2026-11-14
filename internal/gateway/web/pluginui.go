package web

// 插件前端接线面（7.5、7.6）——整页 SSR 移除后的收缩版：
//
//   - web 不再向插件宿主要任何 HTML：12 个插槽与插件页面由客户端运行时
//     经 /frontend-registry 装载（dev-plan §6，S 阶段接线）；
//   - PluginUI 只是标记接口：组合根注入插件渲染桥，web 用类型断言按能力
//     取用静态资源（StaticAssetProvider）、动作绑定（ActionResolver）、
//     插件列表（PluginLister）与 Markdown 动态块（BlockRenderer，见
//     markdown_block.go）；
//   - /ext 路由保留静态资源（512KB 限额）与动作桥（POST 走 CSRF，读动作
//     返回 JSON），两者是冻结契约。

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// PluginUI 是插件前端宿主对 web 暴露的最小标记接口。
// 具体能力全部走下面的可选扩展接口；nil 表示没有任何插件前端。
type PluginUI interface{}

// StaticAssetProvider 由渲染宿主可选实现：提供插件包内的静态资源字节。
type StaticAssetProvider interface {
	StaticAsset(pluginID, name string) ([]byte, bool)
}

// FrontendPluginInfo 是扩展页展示的前端插件摘要。
type FrontendPluginInfo struct {
	ID          string
	Name        string
	Version     string
	Description string
	HasAssets   bool
	Enabled     bool
	Status      string
}

// PluginLister 由渲染宿主可选实现：列出已装配的前端插件（extensions.json 用）。
// r 用于按当前工作区解析启用状态（同 id 的插件在不同工作区可以不同）。
type PluginLister interface {
	List(r *http.Request) []FrontendPluginInfo
}

// ActionBinding 是动作桥解析出的一条绑定：binding 名 → 目标后端动作。
type ActionBinding struct {
	// BackendID 是提供动作的插件 id（ActionEnvelope.pluginId）。
	BackendID string
	// Action 是动作名（ActionEnvelope.action）。
	Action string
	// Mode 是 read 或 write。
	Mode string
}

// ActionResolver 由渲染宿主可选实现：把 /ext/{id}/action/{binding} 解析到绑定。
type ActionResolver interface {
	ActionBinding(pluginID, binding string) (ActionBinding, bool)
}

// ---- /ext 路由 -------------------------------------------------------------

// extAssetExtensions 是插件静态资源的扩展名 allowlist（7.5：styles/scripts/assets）。
// 刻意不含 .html/.htm 与其它文档扩展：插件静态资源只有样式、脚本、图标、数据
// 与 SFC 单文件组件，其余一律 404。
// .vue 以 text/plain 提供——它是 SFC 装载器 fetch 的**文本**，浏览器不会把它
// 当作脚本执行（逻辑在同目录 .js ESM，CSP script-src 'self' 约束不变）。
var extAssetExtensions = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".mjs":   "application/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".vue":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
	".txt":   "text/plain; charset=utf-8",
	".png":   "image/png",
	".woff2": "font/woff2",
}

// extAssetMaxBytes 是单个插件静态资源的大小上限。
const extAssetMaxBytes = 512 << 10

// handleExtStatic 提供插件静态资源 /ext/{id}/static/{name...}。
// 扩展名 allowlist + 规范路径 + 大小上限；命中后可缓存（静态资源是唯一例外，7.6）。
func (s *Server) handleExtStatic(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.cfg.PluginUI.(StaticAssetProvider)
	if !ok || s.cfg.PluginUI == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	name, ok := extAssetName(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, ok := provider.StaticAsset(id, name)
	if !ok || len(data) > extAssetMaxBytes {
		http.NotFound(w, r)
		return
	}
	ct, ok := extAssetExtensions[strings.ToLower(path.Ext(name))]
	if !ok {
		// allowlist 之外的一律 404，而不是猜测 Content-Type。
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// extAssetName 规范化 /ext/{id}/static/ 之后的路径：只接受插件目录内的相对路径。
func extAssetName(urlPath string) (string, bool) {
	const prefix = "/ext/"
	rest := strings.TrimPrefix(urlPath, prefix)
	// 形如 {id}/static/{name...}；截掉 id 与 static 段。
	parts := strings.SplitN(rest, "/static/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	name := parts[1]
	if strings.ContainsAny(name, `\`) {
		return "", false
	}
	if strings.TrimPrefix(path.Clean("/"+name), "/") != name {
		return "", false
	}
	if !fs.ValidPath(name) || strings.Contains(name, "..") {
		return "", false
	}
	if !utf8.ValidString(name) {
		return "", false
	}
	return name, true
}

// handleExtActionRead 以 GET 调用读动作并返回 JSON（协议 action.invoke 的结果原样透出）。
func (s *Server) handleExtActionRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, ok := s.invokeExtAction(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

// handleExtActionWrite 以 POST 调用写动作，随后 303 回来源页（无 JS 的 PRG）。
func (s *Server) handleExtActionWrite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.invokeExtAction(w, r); !ok {
		return
	}
	redirect(w, safeReturnPath(r))
}

// safeReturnPath 取写动作之后的回跳地址：只接受站内路径，其余回首页。
func safeReturnPath(r *http.Request) string {
	ref := r.Referer()
	if ref == "" {
		return "/"
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host != "" && u.Host != r.Host {
		return "/"
	}
	p := u.Path
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	return p
}

// invokeExtAction 是动作桥的共用路径：解析绑定 → action.invoke → 结果 JSON。
// 写动作在 CSRF 之后到达（安全中间件统一校验），这里不重复实现会话逻辑。
func (s *Server) invokeExtAction(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	resolver, ok := s.cfg.PluginUI.(ActionResolver)
	if !ok || s.cfg.PluginUI == nil {
		http.NotFound(w, r)
		return nil, false
	}
	id := r.PathValue("id")
	binding := r.PathValue("name")
	ab, ok := resolver.ActionBinding(id, binding)
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "没有这个动作",
			"插件 "+id+" 没有名为 "+binding+" 的界面动作绑定。请从插件页面重新操作。",
			"", "/", "返回")
		return nil, false
	}
	if r.Method == http.MethodGet && ab.Mode != "read" {
		http.Error(w, "写动作必须以 POST 提交", http.StatusMethodNotAllowed)
		return nil, false
	}
	if r.Method == http.MethodPost && ab.Mode != "write" {
		http.Error(w, "读动作以 GET 调用", http.StatusMethodNotAllowed)
		return nil, false
	}
	if err := s.parseForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "无法读取表单",
			"请求体不是有效的表单，可能超过 1MB 上限。请返回后重试。",
			err.Error(), "/", "返回")
		return nil, false
	}
	input := map[string]any{}
	threadID := ""
	// 表单值进 input（写动作）；query 参数进 input（GET 读动作）。
	collect := func(values url.Values) {
		for key, vs := range values {
			if key == "csrf_token" || len(vs) == 0 {
				continue
			}
			if key == "threadId" {
				threadID = strings.TrimSpace(vs[0])
				continue
			}
			input[key] = vs[0]
		}
	}
	collect(r.URL.Query())
	if r.Method == http.MethodPost {
		collect(r.PostForm)
	}
	envelope := map[string]any{
		"pluginId": ab.BackendID,
		"action":   ab.Action,
		"input":    input,
	}
	if threadID != "" {
		envelope["threadId"] = threadID
	}
	raw, err := s.call(r, "action.invoke", envelope)
	if err != nil {
		s.renderCoreError(w, r, err, safeReturnPath(r))
		return nil, false
	}
	return raw, true
}
