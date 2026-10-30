package frontend

// Bridge 是 web.PluginUI 的渲染桥（M4，设计 7.5、7.6）：
//
//	host.Script（清单 + TSX 模板 + 静态资源）→ host.Renderer（TSX→VNode）
//	→ RenderHTML（VNode→HTML，唯一 template.HTML 出口）→ web 的 12 个插槽与 /ext 页面。
//
// 协议合规取数（不新增 RPC 方法）：插件用既有 registerAction(mode=read) 提供取数
// 动作，本桥通过 supervisor Acquire → action.invoke 拿数据 JSON 作为 props；
// 结果按规范放进 props.data.backend[<前端插件 id>][<绑定名>]（7.5 的投影约定）。
//
// 失败隔离：单个贡献渲染失败只影响该贡献（记录并跳过）；全部失败时 Slot 返回错误，
// 由 web 回退到基座占位。动作预取失败只缺对应 props.data.backend 条目（7.5）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

// BackendActionTimeout 是单个只读动作预取的预算（7.5：单个 300ms）。
var BackendActionTimeout = plugins.BackendActionTimeoutMS * time.Millisecond

// BridgeOptions 是 Bridge 的装配输入。
type BridgeOptions struct {
	// Scripts 是前端插件（frontend-only）的加载单元：清单 + 模板源码（Script.Bundle）
	// + 静态资源（Script.StaticAssets）。
	Scripts []host.Script
	// Supervisor 用于动作预取（action.invoke）与只读数据投影（provider/usage/interaction）；
	// nil 时跳过（props 无对应数据）。
	Supervisor contracts.Supervisor
	// WorkspaceFor 把请求解析成工作区；nil 时用空串（supervisor 默认工作区）。
	WorkspaceFor func(*http.Request) string
	// ProductName 进 props.app.product。
	ProductName string
	// CSRF 为每次请求返回 token（写表单需要；nil 时 props.app.csrf 为空）。
	CSRF func(*http.Request) string
	// Enabled 判断某个插件在当前请求的工作区是否启用；nil 表示全部启用。
	// 前端插件在进程内固定装配，停用/启用通过这个回调在渲染时实时过滤，
	// 不需要重启 Gateway。
	Enabled func(*http.Request, string) bool
	// InvalidateEnabled 让 Enabled 的实现丢弃某个工作区的缓存（启用/停用后调用）。
	InvalidateEnabled func(workspace string)
}

// unit 是一个已登记的前端插件。
type unit struct {
	script    host.Script
	manifest  *plugins.Manifest
	templates map[string]string
}

// slotContrib 是一条「插件 × 插槽」贡献。
type slotContrib struct {
	u       *unit
	contrib plugins.SlotContribution
}

// Bridge 实现 web.PluginUI 与 web 的静态资源 / 动作解析扩展接口。
type Bridge struct {
	byID         map[string]*unit
	slotIndex    map[string][]slotContrib
	renderer     *host.Renderer
	sup          contracts.Supervisor
	workspaceFor func(*http.Request) string
	product      string
	csrf         func(*http.Request) string
	enabled      func(*http.Request, string) bool
	invalidate   func(string)
}

var _ web.PluginUI = (*Bridge)(nil)

// NewBridge 登记全部前端插件并构建索引。任何单元缺模板/样式都算装配错误
// （不静默丢弃贡献，INV-2）。
func NewBridge(opts BridgeOptions) (*Bridge, error) {
	b := &Bridge{
		byID:         map[string]*unit{},
		slotIndex:    map[string][]slotContrib{},
		sup:          opts.Supervisor,
		workspaceFor: opts.WorkspaceFor,
		product:      opts.ProductName,
		csrf:         opts.CSRF,
		enabled:      opts.Enabled,
		invalidate:   opts.InvalidateEnabled,
	}
	pairs := make([]host.ComponentPair, 0, len(AllowedComponents)+1)
	for _, name := range AllowedComponents {
		pairs = append(pairs, host.ComponentPair{Export: name, Canonical: name})
	}
	pairs = append(pairs, host.ComponentPair{Export: "Todo", Canonical: "TodoList"})
	b.renderer = host.NewRenderer(
		host.WithComponentsModule(host.ComponentModuleSource(pairs)),
		// 注意毫秒换算：frontend 常量是 int 毫秒，Duration 直接传会变成纳秒。
		host.WithRenderBudgets(SlotTimeoutMS*time.Millisecond, PageTimeoutMS*time.Millisecond),
	)

	ids := make([]string, 0, len(opts.Scripts))
	for _, s := range opts.Scripts {
		if s.Manifest == nil {
			return nil, fmt.Errorf("frontend: 前端插件脚本缺少清单")
		}
		ids = append(ids, s.Manifest.ID)
	}
	sort.Strings(ids)

	for _, id := range ids {
		var script host.Script
		for _, s := range opts.Scripts {
			if s.Manifest != nil && s.Manifest.ID == id {
				script = s
				break
			}
		}
		u, err := newUnit(script)
		if err != nil {
			return nil, err
		}
		if _, dup := b.byID[id]; dup {
			return nil, fmt.Errorf("frontend: 前端插件 id 重复 %q", id)
		}
		b.byID[id] = u
		f := u.manifest.Frontend
		for _, sc := range f.Slots {
			b.slotIndex[sc.Slot] = append(b.slotIndex[sc.Slot], slotContrib{u: u, contrib: sc})
		}
		// dashboard 贡献挂到概览页的稳定插槽：模板与插槽贡献走同一条渲染路径。
		for _, dc := range f.Dashboard {
			b.slotIndex["dashboard.widgets"] = append(b.slotIndex["dashboard.widgets"], slotContrib{
				u: u,
				contrib: plugins.SlotContribution{
					Slot: "dashboard.widgets", Template: dc.Template, Order: dc.Order,
				},
			})
		}
	}
	for name := range b.slotIndex {
		list := b.slotIndex[name]
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].contrib.Order != list[j].contrib.Order {
				return list[i].contrib.Order < list[j].contrib.Order
			}
			return list[i].u.manifest.ID < list[j].u.manifest.ID
		})
	}
	return b, nil
}

// newUnit 校验一个前端插件的模板与样式引用。
func newUnit(s host.Script) (*unit, error) {
	u := &unit{script: s, manifest: s.Manifest, templates: map[string]string{}}
	if u.manifest.Frontend == nil {
		return nil, fmt.Errorf("frontend: 插件 %s 没有 frontend 段", u.manifest.ID)
	}
	for name, src := range s.Bundle {
		if strings.HasSuffix(name, ".tsx") && strings.TrimSpace(src) != "" {
			u.templates[name] = src
		}
	}
	// 模板与样式必须真实存在：清单引用缺件是装配错误（1.5.5 的反向承诺）。
	for _, sc := range u.manifest.Frontend.Slots {
		if _, ok := u.templates[sc.Template]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的插槽 %s 引用模板 %q，但包里没有它", u.manifest.ID, sc.Slot, sc.Template)
		}
	}
	for _, pc := range u.manifest.Frontend.Pages {
		if _, ok := u.templates[pc.Template]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的页面 %s 引用模板 %q，但包里没有它", u.manifest.ID, pc.Slug, pc.Template)
		}
	}
	for _, dc := range u.manifest.Frontend.Dashboard {
		if _, ok := u.templates[dc.Template]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的 dashboard 卡片引用模板 %q，但包里没有它", u.manifest.ID, dc.Template)
		}
	}
	for _, style := range u.manifest.Frontend.Styles {
		if _, ok := s.StaticAssets[style]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的样式 %q 不在静态资源里", u.manifest.ID, style)
		}
	}
	for _, script := range u.manifest.Frontend.Scripts {
		if _, ok := s.StaticAssets[script]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的脚本 %q 不在静态资源里", u.manifest.ID, script)
		}
	}
	return u, nil
}

// isEnabled 报告插件在当前请求的工作区是否启用；没有回调时视为全部启用。
func (b *Bridge) isEnabled(r *http.Request, pluginID string) bool {
	if b.enabled == nil || r == nil {
		return true
	}
	return b.enabled(r, pluginID)
}

// InvalidateEnabled 丢弃 Enabled 回调的工作区缓存（web 在 plugin.setEnabled 后调用）。
func (b *Bridge) InvalidateEnabled(workspace string) {
	if b.invalidate != nil {
		b.invalidate(workspace)
	}
}

// threadContextSlots 是只在会话上下文里有意义的插槽。首页/概览页没有
// threadId，渲染它们会让插槽的只读动作带着空 threadId 去预取（例如
// Todo 卡片），插件按失败关闭会拒绝，进而把插件标成降级。
var threadContextSlots = map[string]bool{
	"chat.corner":      true,
	"composer.left":    true,
	"composer.right":   true,
	"composer.footer":  true,
	"composer.above":   true,
	"thread.tray":      true,
	"transcript.after": true,
}

// Slot 实现 web.PluginUI：渲染一个插槽的全部贡献。
func (b *Bridge) Slot(name string, r *http.Request) (template.HTML, int, error) {
	if threadContextSlots[name] && threadIDFromRequest(r) == "" {
		return "", 0, nil
	}
	entries := b.slotIndex[name]
	if len(entries) == 0 {
		return "", 0, nil
	}
	var bodies []string
	failures := 0
	for _, e := range entries {
		if !b.isEnabled(r, e.u.manifest.ID) {
			continue
		}
		html, err := b.renderEntry(e.u, e.contrib.Template, "slot", name, r, b.renderer.RenderTemplate)
		if err != nil {
			failures++
			log.Printf("frontend: 插件 %s 的插槽 %s 渲染失败（跳过该贡献）: %v", e.u.manifest.ID, name, err)
			continue
		}
		if html == "" {
			continue // 模板返回 null：该贡献留空（不计失败）
		}
		bodies = append(bodies, string(html))
	}
	if len(bodies) == 0 && failures > 0 {
		return "", 0, fmt.Errorf("frontend: 插槽 %s 的 %d 个贡献全部渲染失败", name, failures)
	}
	return template.HTML(strings.Join(bodies, "\n")), len(bodies), nil
}

// Page 实现 web.PluginUI：渲染 /ext/{id}/{slug} 页面。
// 插件或 slug 不存在时返回包装了 web.ErrExtNotFound 的错误（web 据此给 404）。
func (b *Bridge) Page(pluginID, slug string, r *http.Request) (template.HTML, error) {
	u, ok := b.byID[pluginID]
	if !ok || !b.isEnabled(r, pluginID) {
		return "", fmt.Errorf("%w: 没有前端插件 %q", web.ErrExtNotFound, pluginID)
	}
	for _, pc := range u.manifest.Frontend.Pages {
		if pc.Slug != slug {
			continue
		}
		html, err := b.renderEntry(u, pc.Template, "page", slug, r, b.renderer.RenderPage)
		if err != nil {
			return "", err
		}
		if html == "" {
			return "", fmt.Errorf("frontend: 页面 %s/%s 渲染为空（模板返回 null）", pluginID, slug)
		}
		return html, nil
	}
	return "", fmt.Errorf("%w: 插件 %s 没有页面 %q", web.ErrExtNotFound, pluginID, slug)
}

// PageTitle 实现 web.PageMeta。
func (b *Bridge) PageTitle(pluginID, slug string) string {
	u, ok := b.byID[pluginID]
	if !ok {
		return ""
	}
	for _, pc := range u.manifest.Frontend.Pages {
		if pc.Slug == slug {
			return pc.Title
		}
	}
	return ""
}

// Styles 实现 web.PluginUI：插件样式的 /ext URL。
func (b *Bridge) Styles() []string {
	return b.assetURLs(func(f *plugins.Frontend) []string { return f.Styles })
}

// Scripts 实现 web.PluginUI：插件脚本的 /ext URL（清单已保证能力位与信任级别）。
func (b *Bridge) Scripts() []string {
	return b.assetURLs(func(f *plugins.Frontend) []string { return f.Scripts })
}

func (b *Bridge) assetURLs(pick func(*plugins.Frontend) []string) []string {
	ids := make([]string, 0, len(b.byID))
	for id := range b.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []string
	for _, id := range ids {
		u := b.byID[id]
		for _, name := range pick(u.manifest.Frontend) {
			base := "/ext/" + url.PathEscape(id) + "/static/" + url.PathEscape(name)
			// 插件静态资源本身可长缓存；URL 带内容指纹，改 CSS/JS 后自动失效。
			if data, ok := u.script.StaticAssets[name]; ok {
				sum := sha256.Sum256(data)
				base += "?v=" + hex.EncodeToString(sum[:4])
			}
			out = append(out, base)
		}
	}
	return out
}

// StaticAsset 实现 web.StaticAssetProvider。
func (b *Bridge) StaticAsset(pluginID, name string) ([]byte, bool) {
	u, ok := b.byID[pluginID]
	if !ok {
		return nil, false
	}
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `\`) {
		return nil, false
	}
	data, ok := u.script.StaticAssets[name]
	return data, ok
}

// ActionBinding 实现 web.ActionResolver：绑定名 → 目标后端动作（协议 ActionEnvelope）。
func (b *Bridge) ActionBinding(pluginID, binding string) (web.ActionBinding, bool) {
	u, ok := b.byID[pluginID]
	if !ok {
		return web.ActionBinding{}, false
	}
	ab, ok := u.manifest.Frontend.Actions[binding]
	if !ok {
		return web.ActionBinding{}, false
	}
	mode := "write"
	if ab.Read {
		mode = "read"
	}
	return web.ActionBinding{BackendID: ab.Backend, Action: ab.Action, Mode: mode}, true
}

// ---- 渲染 ------------------------------------------------------------------

// renderEntry 渲染一个模板贡献：props → TSX → VNode → HTML。
func (b *Bridge) renderEntry(u *unit, templateName, kind, name string, r *http.Request, render func(string, json.RawMessage) (json.RawMessage, error)) (template.HTML, error) {
	src, ok := u.templates[templateName]
	if !ok {
		return "", fmt.Errorf("frontend: 插件 %s 缺少模板 %q", u.manifest.ID, templateName)
	}
	props, err := b.buildProps(u, r, kind, name)
	if err != nil {
		return "", err
	}
	vnode, err := render(src, props)
	if err != nil {
		return "", err
	}
	return RenderHTML(vnode)
}

// renderProps 是传给 TSX 的 props 形状（7.5 的投影；插件不能借 props 读别的线程或文件）。
type renderProps struct {
	Plugin pluginInfo `json:"plugin"`
	App    appInfo    `json:"app"`
	Slot   string     `json:"slot,omitempty"`
	Page   string     `json:"page,omitempty"`
	Data   struct {
		Thread       *threadRef                            `json:"thread,omitempty"`
		Backend      map[string]map[string]json.RawMessage `json:"backend,omitempty"`
		Providers    *providersProjection                  `json:"providers,omitempty"`
		Usage        *usageProjection                      `json:"usage,omitempty"`
		Interactions []interactionProjection               `json:"interactions,omitempty"`
	} `json:"data"`
}

type pluginInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type appInfo struct {
	Workspace string `json:"workspace"`
	Product   string `json:"product"`
	Route     string `json:"route"`
	// CSRF 让插件页面的写表单能过双提交校验（与基座模板里的 csrf_token 同源）。
	CSRF string `json:"csrf,omitempty"`
}

type threadRef struct {
	ID string `json:"id"`
}

// buildProps 组装 props：插件身份 + 应用投影 + 只读动作预取结果。
func (b *Bridge) buildProps(u *unit, r *http.Request, kind, name string) (json.RawMessage, error) {
	p := renderProps{
		Plugin: pluginInfo{ID: u.manifest.ID, Name: u.manifest.Name, Version: u.manifest.Version},
		App: appInfo{
			Workspace: b.workspace(r),
			Product:   b.product,
			Route:     r.URL.Path,
		},
	}
	if b.csrf != nil {
		p.App.CSRF = b.csrf(r)
	}
	switch kind {
	case "slot":
		p.Slot = name
	case "page":
		p.Page = name
	}
	threadID := threadIDFromRequest(r)
	if threadID != "" {
		p.Data.Thread = &threadRef{ID: threadID}
	}
	if len(u.manifest.Frontend.Actions) > 0 {
		p.Data.Backend = map[string]map[string]json.RawMessage{}
		for bindingName, binding := range u.manifest.Frontend.Actions {
			if !binding.Read {
				continue // 写动作必须用户点击（7.5），不在 SSR 预取
			}
			raw, err := b.invokeAction(r, binding, threadID)
			if err != nil {
				// 预取失败只影响对应 backend 条目（7.5），不是插槽失败。
				log.Printf("frontend: 插件 %s 预取动作 %q 失败: %v", u.manifest.ID, bindingName, err)
				continue
			}
			if p.Data.Backend[u.manifest.ID] == nil {
				p.Data.Backend[u.manifest.ID] = map[string]json.RawMessage{}
			}
			p.Data.Backend[u.manifest.ID][bindingName] = raw
		}
	}
	// 只读数据投影：供应商/模型、用量、待答交互。统一由 backend.read 能力位约束。
	if hasCapability(u.manifest.Frontend.Capabilities, plugins.CapBackendRead) {
		if providers, err := b.loadProviders(r); err == nil {
			p.Data.Providers = providers
		} else {
			log.Printf("frontend: 插件 %s 读取供应商投影失败: %v", u.manifest.ID, err)
		}
		if usage, err := b.loadUsage(r, threadID); err == nil {
			p.Data.Usage = usage
		} else {
			log.Printf("frontend: 插件 %s 读取用量投影失败: %v", u.manifest.ID, err)
		}
		if threadID != "" {
			if interactions, err := b.loadInteractions(r, threadID); err == nil {
				p.Data.Interactions = interactions
			} else {
				log.Printf("frontend: 插件 %s 读取待答交互失败: %v", u.manifest.ID, err)
			}
		}
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("frontend: props 序列化失败: %w", err)
	}
	return raw, nil
}

// invokeAction 通过既有 action.invoke 取一次只读动作结果（预算 = BackendActionTimeoutMS）。
func (b *Bridge) invokeAction(r *http.Request, binding plugins.ActionBinding, threadID string) (json.RawMessage, error) {
	if b.sup == nil {
		return nil, fmt.Errorf("frontend: 装配没有注入 Supervisor，跳过动作预取")
	}
	ctx, cancel := context.WithTimeout(r.Context(), BackendActionTimeout)
	defer cancel()
	workspace := b.workspace(r)
	core, err := b.sup.Acquire(ctx, workspace)
	if err != nil {
		return nil, err
	}
	defer b.sup.Release(workspace)
	envelope := map[string]any{
		"pluginId": binding.Backend,
		"action":   binding.Action,
		"input":    map[string]any{},
	}
	if threadID != "" {
		envelope["threadId"] = threadID
	}
	raw, err := core.Call(ctx, "action.invoke", envelope)
	if err != nil {
		return nil, err
	}
	var res struct {
		Ok     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("frontend: action.invoke 返回值无法解析: %w", err)
	}
	if !res.Ok {
		return nil, fmt.Errorf("frontend: 动作 %s/%s 失败: %s", binding.Backend, binding.Action, res.Error)
	}
	return res.Result, nil
}

func (b *Bridge) workspace(r *http.Request) string {
	if b.workspaceFor == nil {
		return ""
	}
	return b.workspaceFor(r)
}

// threadIDFromRequest 从 query、页面路径或 Referer 取会话上下文。
// 具体规则与插件 route 共用 web.ThreadIDFromRequest，避免两套解析漂移。
func threadIDFromRequest(r *http.Request) string {
	return web.ThreadIDFromRequest(r)
}

// SlotBindingName 是插槽取数动作的绑定名约定（7.5 的规范名编码）：
// `slot_<插槽名，非 [a-z0-9] 字符替换为 _>`，如 thread.tray → slot_thread_tray。
// 之所以不是字面 `slot:<slotName>`：动作 binding 名受冻结规范约束
// （spec/manifest.schema.json frontend.actions propertyNames 与 ActionEnvelope.action
// 都是 ^[a-z][a-z0-9_]{1,63}$），冒号/点不合法；下划线编码是可 express 的最接近形态。
func SlotBindingName(slot string) string {
	return "slot_" + sanitizeBindingName(slot)
}

// PageBindingName 是页面取数动作的绑定名约定：page_<slug>。
func PageBindingName(slug string) string {
	return "page_" + sanitizeBindingName(slug)
}

func sanitizeBindingName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// List 实现 web.PluginLister：列出已装配的前端插件（扩展页与概览计数用）。
//
// 装配期失败的插件不会进 byID（NewBridge 直接报错）。是否启用按当前请求的
// 工作区实时判断：停用的插件仍列在扩展页（可以重新启用），但不进侧栏导航。
func (b *Bridge) List(r *http.Request) []web.FrontendPluginInfo {
	ids := make([]string, 0, len(b.byID))
	for id := range b.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]web.FrontendPluginInfo, 0, len(ids))
	for _, id := range ids {
		u := b.byID[id]
		m := u.manifest
		enabled := b.isEnabled(r, id)
		status := "ready"
		if !enabled {
			status = "disabled"
		}
		info := web.FrontendPluginInfo{
			ID: m.ID, Name: m.Name, Version: m.Version, Description: m.Description,
			Enabled: enabled, Status: status,
		}
		if m.Frontend != nil {
			info.NavCount = len(m.Frontend.Nav)
			info.PageCount = len(m.Frontend.Pages)
			info.SlotCount = len(m.Frontend.Slots)
			info.HasDashboard = len(m.Frontend.Dashboard) > 0
			info.HasAssets = m.Frontend.Assets != "" || len(u.script.StaticAssets) > 0
			if len(m.Frontend.Pages) > 0 {
				info.FirstPage = m.Frontend.Pages[0].Slug
			}
			for _, n := range m.Frontend.Nav {
				if strings.TrimSpace(n.Label) == "" || strings.TrimSpace(n.Slug) == "" {
					continue
				}
				info.Nav = append(info.Nav, web.FrontendNavLink{Label: n.Label, Slug: n.Slug})
			}
		}
		out = append(out, info)
	}
	return out
}

var _ web.PluginLister = (*Bridge)(nil)
