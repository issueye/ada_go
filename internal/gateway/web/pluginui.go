package web

// M4 的前端插件接线面（设计 7.2、7.5、7.6）：
//
//   - PluginUI 是 web 与插件渲染宿主之间的唯一接缝：web 不 import plugins/host，
//     组合根（internal/gateway）用渲染桥实现它并注入 Config.PluginUI；
//   - 12 个稳定插槽由 PluginUI.Slot 填充（baseData.Slots），单槽失败只影响该槽
//     （回退到基座占位），绝不影响整页；
//   - /ext 路由提供插件页面、静态资源与动作桥（POST 走 CSRF，读动作返回 JSON）。

import (
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

// ErrExtNotFound 由 PluginUI 实现包装，表示「插件/页面不存在」（404），
// 与「渲染失败」（502）区分开。
var ErrExtNotFound = errors.New("web: 插件页面不存在")

// PluginUI 是插件 UI 渲染宿主对 web 暴露的最小接口。
//
// Slot 返回该插槽的插件 HTML 与贡献者数量；返回错误时 web 用基座占位替代该槽，
// 页面其余部分照常渲染（7.3 失败隔离）。Page 是 /ext/{id}/{slug} 独立页面。
type PluginUI interface {
	Slot(name string, r *http.Request) (template.HTML, int, error)
	Page(pluginID, slug string, r *http.Request) (template.HTML, error)
	Styles() []string
	Scripts() []string
}

// StaticAssetProvider 由渲染宿主可选实现：提供插件包内的静态资源字节。
type StaticAssetProvider interface {
	StaticAsset(pluginID, name string) ([]byte, bool)
}

// FrontendNavLink 是前端插件声明的侧栏导航项。
type FrontendNavLink struct {
	Label string
	Slug  string
}

// FrontendPluginInfo 是扩展页展示的前端插件摘要。
type FrontendPluginInfo struct {
	ID           string
	Name         string
	Version      string
	Description  string
	NavCount     int
	PageCount    int
	SlotCount    int
	FirstPage    string
	HasDashboard bool
	HasAssets    bool
	Enabled      bool
	Status       string
	Errors       []string
	Nav          []FrontendNavLink
}

// PluginLister 由渲染宿主可选实现：列出已装配的前端插件（扩展页/概览用）。
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

// pluginSlots 通过 PluginUI 填充 12 个插槽；单个插槽失败回退为空（基座占位）。
func (s *Server) pluginSlots(r *http.Request) (slotViews, slotCounts) {
	var views slotViews
	var counts slotCounts
	if s.cfg.PluginUI == nil {
		return views, counts
	}
	fill := func(name string, dst *template.HTML, cnt *int) {
		body, n, err := s.cfg.PluginUI.Slot(name, r)
		if err != nil {
			// 7.3 失败隔离：该槽留空并记录；页面其余部分照常。
			log.Printf("web: 插槽 %s 渲染失败（回退基座占位）: %v", name, err)
			return
		}
		*dst = body
		*cnt = n
	}
	fill("sidebar.top", &views.SidebarTop, &counts.SidebarTop)
	fill("sidebar.bottom", &views.SidebarBottom, &counts.SidebarBottom)
	fill("topbar.right", &views.TopbarRight, &counts.TopbarRight)
	fill("chat.corner", &views.ChatCorner, &counts.ChatCorner)
	fill("composer.left", &views.ComposerLeft, &counts.ComposerLeft)
	fill("composer.right", &views.ComposerRight, &counts.ComposerRight)
	fill("composer.footer", &views.ComposerFooter, &counts.ComposerFooter)
	fill("composer.above", &views.ComposerAbove, &counts.ComposerAbove)
	fill("thread.tray", &views.ThreadTray, &counts.ThreadTray)
	fill("transcript.after", &views.TranscriptAfter, &counts.TranscriptAfter)
	fill("extensions.list", &views.ExtensionsList, &counts.ExtensionsList)
	fill("dashboard.widgets", &views.DashboardWidgets, &counts.DashboardWidgets)
	return views, counts
}

// pluginStyles 收集插件样式的 <link> 目标；PluginUI 未注入时为空。
func (s *Server) pluginStyles() []string {
	if s.cfg.PluginUI == nil {
		return nil
	}
	return s.cfg.PluginUI.Styles()
}

// pluginScripts 收集插件脚本的 <script> 目标（需 frontend.script 能力，7.5）。
func (s *Server) pluginScripts() []string {
	if s.cfg.PluginUI == nil {
		return nil
	}
	return s.cfg.PluginUI.Scripts()
}

// pluginNavLinks 收集前端插件声明的侧栏导航（供应商 / Token 用量等）。
// 只收启用的插件：停用后入口必须一起消失（INV-11）。
func (s *Server) pluginNavLinks(r *http.Request) []pluginNavLink {
	lister, ok := s.cfg.PluginUI.(PluginLister)
	if !ok {
		return nil
	}
	var out []pluginNavLink
	for _, p := range lister.List(r) {
		if !p.Enabled {
			continue
		}
		for _, n := range p.Nav {
			if strings.TrimSpace(n.Label) == "" || strings.TrimSpace(n.Slug) == "" {
				continue
			}
			out = append(out, pluginNavLink{
				PluginID: p.ID,
				Label:    n.Label,
				Href:     "/ext/" + p.ID + "/" + n.Slug,
			})
		}
	}
	return out
}

// ---- /ext 路由 -------------------------------------------------------------

// PageMeta 由渲染宿主可选实现：给出页面标题（清单 pages[].title）。
type PageMeta interface {
	PageTitle(pluginID, slug string) string
}

// handleExtPage 渲染插件页面 /ext/{id}/{slug}。
func (s *Server) handleExtPage(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PluginUI == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	slug := r.PathValue("slug")
	title := slug
	if pm, ok := s.cfg.PluginUI.(PageMeta); ok {
		if t := strings.TrimSpace(pm.PageTitle(id, slug)); t != "" {
			title = t
		}
	}
	content, err := s.cfg.PluginUI.Page(id, slug, r)
	if err != nil {
		if errors.Is(err, ErrExtNotFound) {
			s.renderError(w, r, http.StatusNotFound, "没有这个扩展页面",
				"插件 "+id+" 没有名为 "+slug+" 的页面，或插件未启用。请从侧栏导航重新进入。",
				"", "/", "返回")
			return
		}
		s.renderError(w, r, http.StatusBadGateway, "扩展页面暂时不可用",
			"插件 "+id+" 的页面渲染失败。请稍后重试；若持续出现，请检查插件状态。",
			err.Error(), "/", "返回")
		return
	}
	data := extPageData{
		baseData: s.base(r, "ext:"+id, title, nil, "", true),
		PluginID: id,
		Slug:     slug,
		Content:  content,
	}
	s.render(w, http.StatusOK, "ext", data)
}

// extAssetExtensions 是插件静态资源的扩展名 allowlist（7.5：styles/scripts/assets）。
// 刻意不含 .html/.htm/.svg 之外的文档与可执行扩展：插件静态资源只有样式、脚本、
// 图标与数据，其余一律 404。
var extAssetExtensions = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
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

// extPageData 是 /ext/{id}/{slug} 的模板数据。
type extPageData struct {
	baseData
	PluginID string
	Slug     string
	// Content 是经过 VNode 校验与转义的插件页面 HTML（唯一 template.HTML 出口）。
	Content template.HTML
}
