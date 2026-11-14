package frontend

// Bridge 是 web.PluginUI 的插件宿主桥（7.5、7.6）——整页 SSR 移除后的收缩版：
//
//	清单 frontend 段（blocks/actions/styles/assets）→ 静态资源 / 动作绑定 / 插件列表
//	→ web 的 /ext 静态资源、动作桥与 extensions.json。
//
// Markdown 动态块仍由本桥渲染：数据 Schema → TSX（esbuild + moejs）→ VNode JSON
// → block profile 校验 → 包装节点交给 web（web/markdown_block.go）。
// 插槽/页面的 HTML 渲染已删除：插件前端贡献由客户端运行时装载（dev-plan §2 R1）。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/gateway/web"
)

// BridgeOptions 是 Bridge 的装配输入。
type BridgeOptions struct {
	// Scripts 是前端插件（frontend-only）的加载单元：清单 + 模板源码（Script.Bundle）
	// + 静态资源（Script.StaticAssets）。
	Scripts []pluginapi.Script
	// WorkspaceFor 把请求解析成工作区（block 缓存键的组成部分）。
	WorkspaceFor func(*http.Request) string
	// ProductName 进 block props 的 app.product。
	ProductName string
	// Enabled 判断某个插件在当前请求的工作区是否启用；nil 表示全部启用。
	// 前端插件在进程内固定装配，停用/启用通过这个回调在渲染时实时过滤，
	// 不需要重启 Gateway。
	Enabled func(*http.Request, string) bool
	// InvalidateEnabled 让 Enabled 的实现丢弃某个工作区的缓存（启用/停用后调用）。
	InvalidateEnabled func(workspace string)
	// MarkdownBlocks 是产品 capabilities.markdownBlocks：false 时 Block 渲染入口关闭。
	MarkdownBlocks bool
	// BlockAudit 接收块渲染失败审计（不包含 data 原文）。
	BlockAudit func(BlockAuditEvent)
}

// BlockAuditEvent 是一次 block 渲染失败审计；Error 只含路径/类型/错误类别，不含 data 值。
type BlockAuditEvent struct {
	Kind     string // ui.block.invalid_data | ui.block.render_failed | ui.block.invalid_vnode
	PluginID string
	Renderer string
	BlockID  string
	Error    string
}

// unit 是一个已登记的前端插件。
type unit struct {
	script    pluginapi.Script
	manifest  *pluginapi.Manifest
	templates map[string]string
}

// blockEntry 是一个已登记的 Markdown 动态块渲染器。
type blockEntry struct {
	u       *unit
	def     pluginapi.BlockContribution
	schema  *DataSchema
	profile string
	version int
}

type blockCacheEntry struct {
	raw json.RawMessage
}

const blockCacheLimit = 512

// Bridge 实现 web.PluginUI 与 web 的静态资源 / 动作解析扩展接口。
type Bridge struct {
	byID         map[string]*unit
	blocks       map[string]blockEntry
	renderer     *pluginapi.Renderer
	workspaceFor func(*http.Request) string
	product      string
	enabled      func(*http.Request, string) bool
	invalidate   func(string)

	blockMu        sync.Mutex
	blockCache     map[string]blockCacheEntry
	markdownBlocks bool
	blockAudit     func(BlockAuditEvent)

	genMu sync.Mutex
	gen   int64 // 注册表代数：InvalidateEnabled 递增（/frontend-registry 的 rev）
}

var _ web.PluginUI = (*Bridge)(nil)

// NewBridge 登记全部前端插件并构建索引。任何单元缺模板/样式都算装配错误
// （不静默丢弃贡献，INV-2）。
func NewBridge(opts BridgeOptions) (*Bridge, error) {
	b := &Bridge{
		byID:           map[string]*unit{},
		blocks:         map[string]blockEntry{},
		blockCache:     map[string]blockCacheEntry{},
		workspaceFor:   opts.WorkspaceFor,
		product:        opts.ProductName,
		enabled:        opts.Enabled,
		invalidate:     opts.InvalidateEnabled,
		markdownBlocks: opts.MarkdownBlocks,
		blockAudit:     opts.BlockAudit,
	}
	pairs := make([]pluginapi.ComponentPair, 0, len(AllowedComponents)+1)
	for _, name := range AllowedComponents {
		pairs = append(pairs, pluginapi.ComponentPair{Export: name, Canonical: name})
	}
	pairs = append(pairs, pluginapi.ComponentPair{Export: "Todo", Canonical: "TodoList"})
	b.renderer = pluginapi.NewRenderer(
		pluginapi.WithComponentsModule(pluginapi.ComponentModuleSource(pairs)),
		// 动态块用独立预算：Schema 的 x-limits.renderTimeoutMs 与这里必须同源，
		// 否则门禁比对的不是真正生效的值。
		pluginapi.WithBlockRenderBudget(web.MarkdownBlockRenderTimeoutMS*time.Millisecond),
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
		var script pluginapi.Script
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
		// Markdown 动态块：模板在 Bundle，数据 Schema 在 Script.Schemas。
		for _, bd := range u.manifest.Frontend.Blocks {
			rendererID := u.manifest.ID + "/" + bd.Name
			if _, dup := b.blocks[rendererID]; dup {
				return nil, fmt.Errorf("frontend: block renderer 重复 %q", rendererID)
			}
			raw, ok := script.Schemas[bd.Schema]
			if !ok || len(bytes.TrimSpace(raw)) == 0 {
				return nil, fmt.Errorf("frontend: 插件 %s 的 block %s 缺少 Schema %q", u.manifest.ID, bd.Name, bd.Schema)
			}
			schema, err := CompileDataSchema(raw)
			if err != nil {
				return nil, fmt.Errorf("frontend: 插件 %s 的 block %s Schema 非法: %w", u.manifest.ID, bd.Name, err)
			}
			profile := bd.Profile
			if profile == "" {
				profile = "display"
			}
			version := bd.Version
			if version < 1 {
				version = 1
			}
			b.blocks[rendererID] = blockEntry{u: u, def: bd, schema: schema, profile: profile, version: version}
		}
	}
	return b, nil
}

// newUnit 校验一个前端插件的模板与样式引用。
func newUnit(s pluginapi.Script) (*unit, error) {
	u := &unit{script: s, manifest: s.Manifest, templates: map[string]string{}}
	if u.manifest.Frontend == nil {
		return nil, fmt.Errorf("frontend: 插件 %s 没有 frontend 段", u.manifest.ID)
	}
	for name, src := range s.Bundle {
		if strings.HasSuffix(name, ".tsx") && strings.TrimSpace(src) != "" {
			u.templates[name] = src
		}
	}
	// 组件与样式必须真实存在：清单引用缺件是装配错误（1.5.5 的反向承诺）。
	for _, sc := range u.manifest.Frontend.Slots {
		if _, ok := s.StaticAssets[sc.Component]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的插槽 %s 引用组件 %q，但 assets 里没有它", u.manifest.ID, sc.Slot, sc.Component)
		}
	}
	for _, pc := range u.manifest.Frontend.Pages {
		if _, ok := s.StaticAssets[pc.Component]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的页面 %s 引用组件 %q，但 assets 里没有它", u.manifest.ID, pc.Slug, pc.Component)
		}
	}
	for _, dc := range u.manifest.Frontend.Dashboard {
		if _, ok := s.StaticAssets[dc.Component]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的 dashboard 卡片引用组件 %q，但 assets 里没有它", u.manifest.ID, dc.Component)
		}
	}
	for _, bd := range u.manifest.Frontend.Blocks {
		if _, ok := u.templates[bd.Template]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的 block %s 引用模板 %q，但包里没有它", u.manifest.ID, bd.Name, bd.Template)
		}
	}
	for _, style := range u.manifest.Frontend.Styles {
		if _, ok := s.StaticAssets[style]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的样式 %q 不在静态资源里", u.manifest.ID, style)
		}
	}
	if client := strings.TrimSpace(u.manifest.Frontend.Client); client != "" {
		if _, ok := s.StaticAssets[client]; !ok {
			return nil, fmt.Errorf("frontend: 插件 %s 的 client %q 不在静态资源里", u.manifest.ID, client)
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
// 同时递增注册表代数：evt.plugin.changed 推给浏览器后，/frontend-registry 的
// rev 变化让客户端可以校验拿到的是新数据。
func (b *Bridge) InvalidateEnabled(workspace string) {
	b.genMu.Lock()
	b.gen++
	b.genMu.Unlock()
	if b.invalidate != nil {
		b.invalidate(workspace)
	}
}

// RegistryGen 实现 web.RegistrySource：注册表代数（每次启停变更 +1）。
func (b *Bridge) RegistryGen() int64 {
	b.genMu.Lock()
	defer b.genMu.Unlock()
	return b.gen
}

// Registry 实现 web.RegistrySource：当前工作区已启用插件的全部前端贡献。
// 只含启用的插件（停用即从插槽/页面/导航消失——修复旧 P3 缺口）；
// component URL 带内容指纹（?v=），文件变化浏览器重新装载（修复旧 P1 缺口）。
func (b *Bridge) Registry(r *http.Request) []web.RegistryPlugin {
	ids := make([]string, 0, len(b.byID))
	for id := range b.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]web.RegistryPlugin, 0, len(ids))
	for _, id := range ids {
		u := b.byID[id]
		if !b.isEnabled(r, id) {
			continue
		}
		m := u.manifest
		p := web.RegistryPlugin{
			ID: m.ID, Name: m.Name, Trust: string(m.Trust), Version: m.Version,
		}
		if fe := m.Frontend; fe != nil {
			for _, sc := range fe.Slots {
				p.Slots = append(p.Slots, web.RegistrySlot{
					Slot: sc.Slot, Component: b.componentURL(id, sc.Component, u), Order: sc.Order,
				})
			}
			for _, pc := range fe.Pages {
				p.Pages = append(p.Pages, web.RegistryPage{
					Slug: pc.Slug, Component: b.componentURL(id, pc.Component, u), Title: pc.Title,
				})
			}
			for _, nc := range fe.Nav {
				p.Nav = append(p.Nav, web.RegistryNav{
					Path: "/ext/" + id + "/" + nc.Slug, Label: nc.Label, Icon: nc.Icon, Order: nc.Order,
				})
			}
			for _, dc := range fe.Dashboard {
				p.Dashboard = append(p.Dashboard, web.RegistryDashboard{
					Component: b.componentURL(id, dc.Component, u), Order: dc.Order,
				})
			}
			if len(fe.Actions) > 0 {
				p.Actions = make(map[string]web.RegistryAction, len(fe.Actions))
				for binding, a := range fe.Actions {
					p.Actions[binding] = web.RegistryAction{Confirm: a.Confirm, Read: a.Read}
				}
			}
			for _, st := range fe.Styles {
				p.Styles = append(p.Styles, b.componentURL(id, st, u))
			}
		}
		out = append(out, p)
	}
	return out
}

// componentURL 生成带内容指纹的 SFC 装载 URL。
func (b *Bridge) componentURL(pluginID, component string, u *unit) string {
	url := "/ext/" + pluginID + "/static/" + component
	if data, ok := u.script.StaticAssets[component]; ok {
		sum := sha256.Sum256(data)
		url += "?v=" + hex.EncodeToString(sum[:4])
	}
	return url
}

// MarkdownBlocksEnabled 实现 web 的可选能力查询。
func (b *Bridge) MarkdownBlocksEnabled() bool { return b != nil && b.markdownBlocks }

// RenderBlock 渲染一个 Markdown 动态块：数据 Schema → TSX → VNode → block profile 校验
// → 包装成单个 div 节点的 VNode JSON（HTML 表达已随 SSR 删除，由客户端渲染）。
// version 是封套声明的块数据版本（缺省 1），必须与插件清单一致。
// 模板返回 null 时 raw 为字面量 null、err 为 nil：web 层据此跳过该块。
func (b *Bridge) RenderBlock(r *http.Request, rendererID, blockID string, version int, data json.RawMessage) (json.RawMessage, error) {
	entry, ok := b.blocks[strings.TrimSpace(rendererID)]
	if !ok {
		b.auditBlockID("ui.block.render_failed", rendererID, blockID, "renderer_missing")
		return nil, fmt.Errorf("frontend: 没有 block renderer %q", rendererID)
	}
	if !b.isEnabled(r, entry.u.manifest.ID) {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "plugin_disabled")
		return nil, fmt.Errorf("frontend: 插件 %s 未启用", entry.u.manifest.ID)
	}
	if !b.markdownBlocks {
		return nil, fmt.Errorf("frontend: 产品未启用 Markdown 动态块")
	}
	// MVP 只支持 display profile；interactive 等后续 profile 一律 fail-closed，
	// 避免清单里声明了交互却按展示块渲染出可提交的表单。
	if entry.profile != "display" {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "unsupported_profile")
		return nil, fmt.Errorf("frontend: block %s 的 profile %q 暂不支持（MVP 仅 display）", rendererID, entry.profile)
	}
	if version < 1 {
		version = 1
	}
	if version != entry.version {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "unsupported_version")
		return nil, fmt.Errorf("frontend: block %s 的 version %d 与插件声明的 %d 不一致", rendererID, version, entry.version)
	}
	cacheKey := b.blockCacheKey(entry, r, rendererID, blockID, data)
	if raw, ok := b.cachedBlock(cacheKey); ok {
		return raw, nil
	}
	parsed, err := decodeJSONValue(data)
	if err != nil {
		b.auditBlock("ui.block.invalid_data", entry, rendererID, blockID, "invalid_json")
		return nil, fmt.Errorf("frontend: block %s 的 data 不是合法 JSON: %w", rendererID, err)
	}
	if err := entry.schema.ValidateData(parsed); err != nil {
		b.auditBlock("ui.block.invalid_data", entry, rendererID, blockID, "schema_validation")
		return nil, err
	}
	props, err := b.buildBlockProps(entry, r, blockID, rendererID, data)
	if err != nil {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "props_error")
		return nil, err
	}
	src, ok := entry.u.templates[entry.def.Template]
	if !ok {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "template_missing")
		return nil, fmt.Errorf("frontend: 插件 %s 的 block %s 缺少模板 %q", entry.u.manifest.ID, entry.def.Name, entry.def.Template)
	}
	vnode, err := b.renderer.RenderBlockTemplate(src, props)
	if err != nil {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "render_error")
		return nil, err
	}
	// 模板返回 null 表示「这个块没有内容可展示」，是合法结果而不是失败：
	// 与插槽的 null 语义一致（pluginapi.Renderer 用字面量 null 表示留空）。
	// 不能把它当零值 VNode 去校验，否则会误报 invalid_vnode 并画出错误卡。
	if trimmed := bytes.TrimSpace(vnode); len(trimmed) == 0 || string(trimmed) == "null" {
		b.storeBlock(cacheKey, json.RawMessage("null"))
		return json.RawMessage("null"), nil
	}
	var root Node
	if err := json.Unmarshal(vnode, &root); err != nil {
		b.auditBlock("ui.block.invalid_vnode", entry, rendererID, blockID, "invalid_json")
		return nil, fmt.Errorf("frontend: block %s 返回的 VNode 非法: %w", rendererID, err)
	}
	if root.T == "" {
		// 非 null 但没有根节点类型：模板返回了空对象/标量，按 VNode 非法处理，
		// 用独立错误码与 profile 违规区分开。
		b.auditBlock("ui.block.invalid_vnode", entry, rendererID, blockID, "empty_root")
		return nil, fmt.Errorf("frontend: block %s 返回的 VNode 缺少根节点", rendererID)
	}
	if err := ValidateBlockNode(root); err != nil {
		b.auditBlock("ui.block.invalid_vnode", entry, rendererID, blockID, "profile_violation")
		return nil, err
	}
	wrapped, err := json.Marshal(struct {
		T string            `json:"t"`
		P map[string]any    `json:"p"`
		C []json.RawMessage `json:"c"`
	}{
		T: "div",
		P: map[string]any{
			"class":              "adag-ui-block",
			"data-adag-renderer": rendererID,
		},
		C: []json.RawMessage{vnode},
	})
	if err != nil {
		b.auditBlock("ui.block.render_failed", entry, rendererID, blockID, "wrap_error")
		return nil, fmt.Errorf("frontend: block %s 包装 VNode 失败: %w", rendererID, err)
	}
	b.storeBlock(cacheKey, wrapped)
	return wrapped, nil
}

// auditBlock 只记录失败类别与归属，不记录 data 或渲染器错误消息。
func (b *Bridge) auditBlock(kind string, entry blockEntry, rendererID, blockID, code string) {
	b.audit(kind, entry.u.manifest.ID, rendererID, blockID, code)
}

// auditBlockID 用于 renderer 尚未解析成功（没有插件归属）的失败。
func (b *Bridge) auditBlockID(kind, rendererID, blockID, code string) {
	pluginID := ""
	if i := strings.IndexByte(rendererID, '/'); i > 0 {
		pluginID = rendererID[:i]
	}
	b.audit(kind, pluginID, rendererID, blockID, code)
}

func (b *Bridge) audit(kind, pluginID, rendererID, blockID, code string) {
	if b.blockAudit == nil {
		return
	}
	b.blockAudit(BlockAuditEvent{
		Kind:     kind,
		PluginID: pluginID,
		Renderer: rendererID,
		BlockID:  blockID,
		Error:    code,
	})
}

func (b *Bridge) blockCacheKey(entry blockEntry, r *http.Request, rendererID, blockID string, data json.RawMessage) string {
	sum := sha256.Sum256(data)
	route, workspace := "", ""
	if r != nil {
		route = r.URL.Path
		workspace = b.workspace(r)
	}
	return strings.Join([]string{
		rendererID, blockID, entry.u.manifest.Version, workspace, route, hex.EncodeToString(sum[:]),
	}, "\x00")
}

func (b *Bridge) cachedBlock(key string) (json.RawMessage, bool) {
	b.blockMu.Lock()
	defer b.blockMu.Unlock()
	entry, ok := b.blockCache[key]
	if !ok {
		return nil, false
	}
	return append(json.RawMessage(nil), entry.raw...), true
}

func (b *Bridge) storeBlock(key string, raw json.RawMessage) {
	b.blockMu.Lock()
	defer b.blockMu.Unlock()
	if len(b.blockCache) >= blockCacheLimit {
		b.blockCache = map[string]blockCacheEntry{}
	}
	b.blockCache[key] = blockCacheEntry{raw: append(json.RawMessage(nil), raw...)}
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Bridge) buildBlockProps(entry blockEntry, r *http.Request, blockID, rendererID string, data json.RawMessage) (json.RawMessage, error) {
	p := renderProps{
		Plugin: pluginInfo{ID: entry.u.manifest.ID, Name: entry.u.manifest.Name, Version: entry.u.manifest.Version},
		App:    appInfo{Product: b.product},
		Block:  &blockInfo{ID: blockID, Renderer: rendererID, Version: entry.version, Data: data},
	}
	if r != nil {
		p.App.Workspace = b.workspace(r)
		p.App.Route = r.URL.Path
	}
	// 块是只读展示，不能带 CSRF token：props 会进入模型可写的 VNode，
	// 一旦被渲染成文本就把令牌泄漏进对话记录。
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("frontend: block props 序列化失败: %w", err)
	}
	return raw, nil
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

// renderProps 是传给 TSX 的 props 形状（block 渲染用；插件不能借 props 读别的线程或文件）。
type renderProps struct {
	Plugin pluginInfo `json:"plugin"`
	App    appInfo    `json:"app"`
	Block  *blockInfo `json:"block,omitempty"`
}

// blockInfo 是动态块传给 TSX 的 props.block。
type blockInfo struct {
	ID       string          `json:"id,omitempty"`
	Renderer string          `json:"renderer"`
	Version  int             `json:"version"`
	Data     json.RawMessage `json:"data"`
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
}

func (b *Bridge) workspace(r *http.Request) string {
	if b.workspaceFor == nil {
		return ""
	}
	return b.workspaceFor(r)
}

// List 实现 web.PluginLister：列出已装配的前端插件（extensions.json 用）。
//
// 装配期失败的插件不会进 byID（NewBridge 直接报错）。是否启用按当前请求的
// 工作区实时判断：停用的插件仍列在扩展页（可以重新启用）。
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
			HasAssets: m.Frontend.Assets != "" || len(u.script.StaticAssets) > 0,
		}
		out = append(out, info)
	}
	return out
}

var _ web.PluginLister = (*Bridge)(nil)
