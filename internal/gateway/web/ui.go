package web

// 本文件是 a_da 组件库（crates/agent-web/src/ui）的 Go 移植：
//   - 图标 sprite（唯一来源）；
//   - 侧栏（品牌 / 新会话 / 工作区列表 / 搜索 / 会话树 / 系统导航 / 窄轨）；
//   - 行渲染与面板所需的少量 HTML 片段；
//   - Markdown 渲染（助手正文）。
//
// 所有动态文本都必须经过 html 包转义；只有本文件生成的 template.HTML 能进模板。
// 类名、DOM 层级与 a_da 保持同形，CSS 基线（static/ui/*.css）直接复用。

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
	xhtmlatom "golang.org/x/net/html/atom"
)

// ---- 转义与类名 ----

func esc(s string) string { return html.EscapeString(s) }

func classList(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func attr(name, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf(` %s="%s"`, name, esc(value))
}

// ---- 图标 ----

type iconDef struct {
	key  string
	body string
}

var iconDefs = []iconDef{
	{"i-diamond", `<path d="M12 2.5 21.5 12 12 21.5 2.5 12Z"/>`},
	{"i-plus", `<path d="M12 5v14M5 12h14"/>`},
	{"i-minus", `<path d="M5 12h14"/>`},
	{"i-chat", `<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2Z"/>`},
	{"i-grid", `<rect x="4" y="4" width="7" height="7" rx="1.5"/><rect x="13" y="4" width="7" height="7" rx="1.5"/><rect x="4" y="13" width="7" height="7" rx="1.5"/><rect x="13" y="13" width="7" height="7" rx="1.5"/>`},
	{"i-server", `<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 8h.01M7 17h.01"/>`},
	{"i-chart", `<path d="M3 20h18"/><path d="M6 20v-6"/><path d="M11 20V8"/><path d="M16 20v-9"/><path d="M21 20V5"/>`},
	{"i-folder", `<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/>`},
	{"i-image", `<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="1.6"/><path d="m21 15-4.5-4.5L7 20"/>`},
	{"i-search", `<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>`},
	{"i-moon", `<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z"/>`},
	{"i-sun", `<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>`},
	// 本地新增（非 a_da icons.rs 集合）：主题面板「跟随系统」三态用（19-ued §19.2.2）。
	{"i-monitor", `<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/>`},
	{"i-cloud", `<path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"/>`},
	{"i-cloud-fog", `<path d="M4 14.899A7 7 0 1 1 15.71 8h1.79a4.5 4.5 0 0 1 2.5 8.242"/><path d="M16 17H7"/><path d="M17 21H9"/>`},
	{"i-cloud-rain", `<path d="M4 14.899A7 7 0 1 1 15.71 8h1.79a4.5 4.5 0 0 1 2.5 8.242"/><path d="M16 14v6"/><path d="M8 14v6"/><path d="M12 16v6"/>`},
	{"i-refresh", `<path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/>`},
	{"i-stop", `<rect x="7" y="7" width="10" height="10" rx="2"/>`},
	{"i-send", `<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/>`},
	{"i-arrow-left", `<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>`},
	{"i-clock", `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`},
	{"i-check", `<path d="M20 6 9 17l-5-5"/>`},
	{"i-check-circle", `<circle cx="12" cy="12" r="9"/><path d="m8.5 12.5 2.5 2.5 4.5-5"/>`},
	{"i-x", `<path d="M18 6 6 18M6 6l12 12"/>`},
	{"i-x-circle", `<circle cx="12" cy="12" r="9"/><path d="m15 9-6 6M9 9l6 6"/>`},
	{"i-trash", `<path d="M3 6h18"/><path d="M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2"/><path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6"/><path d="M10 11v6M14 11v6"/>`},
	{"i-chevron", `<path d="m9 6 6 6-6 6"/>`},
	{"i-chevron-down", `<path d="m6 9 6 6 6-6"/>`},
	{"i-chevron-up", `<path d="m6 15 6-6 6 6"/>`},
	{"i-arrow-right", `<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>`},
	{"i-circle", `<circle cx="12" cy="12" r="9"/>`},
	{"i-arrow-up", `<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/>`},
	{"i-alert", `<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4"/><path d="M12 17h.01"/>`},
	{"i-bulb", `<path d="M9 18h6"/><path d="M10 21h4"/><path d="M12 3a6 6 0 0 0-3.6 10.8c.6.5.9 1.2.9 2h5.4c0-.8.3-1.5.9-2A6 6 0 0 0 12 3Z"/>`},
	{"i-brain", `<path d="M12 5a3 3 0 0 0-3 3 2.5 2.5 0 0 0-1.5 4.5A2.5 2.5 0 0 0 9 17a3 3 0 0 0 3 2z"/><path d="M12 5a3 3 0 0 1 3 3 2.5 2.5 0 0 1 1.5 4.5A2.5 2.5 0 0 1 15 17a3 3 0 0 1-3 2z"/><path d="M12 5v14"/>`},
	{"i-terminal", `<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>`},
	{"i-bot", `<rect x="5" y="8" width="14" height="11" rx="3"/><path d="M12 8V5"/><circle cx="12" cy="4" r="1"/><path d="M9.5 12.5h.01M14.5 12.5h.01"/><path d="M9.5 16c1.6 1 3.4 1 5 0"/>`},
	{"i-shield", `<path d="M12 22s8-3.6 8-10V5.5L12 2 4 5.5V12c0 6.4 8 10 8 10Z"/>`},
	{"i-shield-check", `<path d="M12 22s8-3.6 8-10V5.5L12 2 4 5.5V12c0 6.4 8 10 8 10Z"/><path d="m9 12 2 2 4-4"/>`},
	{"i-plug", `<path d="M9 3v6M15 3v6"/><path d="M6 9h12v3a6 6 0 0 1-12 0Z"/><path d="M12 18v3"/>`},
	{"i-wrench", `<path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76Z"/>`},
	{"i-external", `<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6"/><path d="M10 14 21 3"/>`},
	{"i-menu", `<path d="M4 6h16M4 12h16M4 18h16"/>`},
	{"i-more", `<path d="M5 12h.01M12 12h.01M19 12h.01" stroke-width="3" stroke-linecap="round"/>`},
	{"i-git-branch", `<line x1="6" x2="6" y1="4" y2="16"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>`},
	{"i-sparkles", `<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9Z"/><path d="M19 15l.8 2.2L22 18l-2.2.8L19 21l-.8-2.2L16 18l2.2-.8Z"/>`},
	{"i-file", `<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8Z"/><path d="M14 3v5h5"/><path d="M9 13h6M9 17h4"/>`},
	{"i-edit", `<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>`},
	{"i-package", `<path d="m7.5 4.3 9 5.2"/><path d="M21 16V8a2 2 0 0 0-1-1.7l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.7l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/>`},
	{"i-code", `<path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/>`},
	{"i-list", `<path d="m3 7 2 2 4-4"/><path d="m3 17 2 2 4-4"/><path d="M13 6h8M13 12h8M13 18h8"/>`},
	{"i-copy", `<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`},
	{"i-layers", `<path d="m12 2 9 5-9 5-9-5 9-5Z"/><path d="m3 12 9 5 9-5"/><path d="m3 17 9 5 9-5"/>`},
	{"i-wave", `<path d="M2 12h2M7 7v10M12 3v18M17 7v10M22 12h-2"/>`},
}

// toolIcon 给工具折叠行挑图标：文件、目录、终端、git、计划各自可辨，
// 认不出来的退回扳手。工具名来自插件（第三方也会有），所以按关键字归类，
// 顺序即优先级（"skill_search" 要落在 skill，而不是 search）。
func toolIcon(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "":
		return "i-wrench"
	case strings.Contains(n, "git"):
		return "i-git-branch"
	case strings.Contains(n, "skill"):
		return "i-sparkles"
	case strings.Contains(n, "plan"), strings.Contains(n, "todo"), strings.Contains(n, "task"):
		return "i-list"
	case strings.Contains(n, "ask"), strings.Contains(n, "question"):
		return "i-bot"
	case strings.Contains(n, "search"), strings.Contains(n, "grep"), strings.Contains(n, "find"):
		return "i-search"
	case strings.Contains(n, "list"), strings.Contains(n, "glob"), strings.Contains(n, "dir"):
		return "i-folder"
	case strings.Contains(n, "write"), strings.Contains(n, "edit"), strings.Contains(n, "patch"):
		return "i-edit"
	case strings.Contains(n, "read"), strings.Contains(n, "file"), strings.Contains(n, "cat"):
		return "i-file"
	case strings.Contains(n, "run"), strings.Contains(n, "shell"), strings.Contains(n, "exec"),
		strings.Contains(n, "command"), strings.Contains(n, "bash"), strings.Contains(n, "terminal"):
		return "i-terminal"
	case strings.Contains(n, "fetch"), strings.Contains(n, "http"), strings.Contains(n, "web"):
		return "i-cloud"
	}
	return "i-wrench"
}

func iconHTML(key string, size int) template.HTML {
	for _, def := range iconDefs {
		if def.key == key {
			return template.HTML(fmt.Sprintf(
				`<svg class="ada-icon" width="%d" height="%d" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">%s</svg>`,
				size, size, def.body,
			))
		}
	}
	return ""
}

// iconSprite 是模板侧唯一图标通道：`<use href="#i-xxx">`。
func iconSprite() template.HTML {
	var b strings.Builder
	b.WriteString(`<svg class="ada-sprite" aria-hidden="true" focusable="false" width="0" height="0">`)
	for _, def := range iconDefs {
		fmt.Fprintf(&b, `<symbol id="%s" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">%s</symbol>`, def.key, def.body)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// ---- Markdown ----

var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(goldmarkhtml.WithHardWraps(), goldmarkhtml.WithXHTML()),
)

var markdownPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").OnElements("code", "pre", "span", "div", "table", "thead", "tbody", "tr", "th", "td")
	p.AllowAttrs("class").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("align").OnElements("th", "td")
	p.AllowElements("details", "summary")
	p.AllowAttrs("class").Globally()
	return p
}()

// renderMarkdown 把模型正文渲染为 HTML（goldmark + bluemonday 清洗）。
// 任何解析错误都退回到纯文本段落，绝不把未清洗的 HTML 放出去。
func renderMarkdown(text string) template.HTML {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var src strings.Builder
	if err := markdownRenderer.Convert([]byte(text), &src); err != nil {
		return template.HTML("<p>" + esc(text) + "</p>")
	}
	clean := markdownPolicy.Sanitize(src.String())
	return template.HTML(decorateExternalLinks(clean))
}

// decorateExternalLinks 给正文里的外部链接补 target/rel：点击在新标签/系统浏览器
// 打开，不把应用页面导航走。输入已经过 bluemonday 清洗，这里只追加两个常量属性；
// 站内相对链接与锚点保持原样。
func decorateExternalLinks(rendered string) string {
	if !strings.Contains(rendered, "<a ") {
		return rendered
	}
	context := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: xhtmlatom.Div}
	nodes, err := xhtml.ParseFragment(strings.NewReader(rendered), context)
	if err != nil {
		return rendered
	}
	var b strings.Builder
	for _, node := range nodes {
		markExternalLinks(node)
		_ = xhtml.Render(&b, node)
	}
	if b.Len() == 0 {
		return rendered
	}
	return b.String()
}

func markExternalLinks(node *xhtml.Node) {
	if node.Type == xhtml.ElementNode && node.Data == "a" {
		for _, a := range node.Attr {
			if a.Key != "href" || !isExternalHref(a.Val) {
				continue
			}
			setNodeAttr(node, "target", "_blank")
			setNodeAttr(node, "rel", "nofollow noopener noreferrer")
			break
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		markExternalLinks(child)
	}
}

func setNodeAttr(node *xhtml.Node, key, value string) {
	for i := range node.Attr {
		if node.Attr[i].Key == key {
			node.Attr[i].Val = value
			return
		}
	}
	node.Attr = append(node.Attr, xhtml.Attribute{Key: key, Val: value})
}

func isExternalHref(href string) bool {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
		return true
	}
	return false
}

// ---- 侧栏 ----

type sidebarThread struct {
	ID        string
	Title     string
	Running   bool
	UpdatedMS int64
}

type sidebarGroup struct {
	Key     string
	Path    string
	Name    string
	Index   int // 在网关工作区列表中的下标；会话链接用 ?ws=<index> 显式切工作区
	Threads []sidebarThread
	Count   int
	Running bool
	Active  bool
}

type sidebarData struct {
	Active             string // chat | overview | extensions | ext:<id>
	Gateway            string
	Groups             []sidebarGroup
	ActiveThreadID     string
	SidebarTop         template.HTML
	SidebarTopCount    int
	SidebarBottom      template.HTML
	SidebarBottomCount int
	Search             bool
	Connected          *bool
	ProductName        string
	BrandName          string
	ProductBadge       string
	// CSRF 进侧栏表单的隐藏字段（双提交校验；桌面壳的 Origin 可能是 null）。
	CSRF string
	// CurrentPath 是当前页面路径：删除会话后回到这里（删的是当前会话则回首页）。
	CurrentPath string
	// PluginNav 是前端插件贡献的侧栏导航。
	PluginNav []pluginNavLink
}

// 主题色板表（docs/19-ued.md §19.2.2）：id 与显示名驱动侧栏主题面板的色卡。
// 三色预览不放 Go 侧——CSP style-src 'self' 禁内联 style，色值在 ui/themes.css
// 的 .ada-swatch--<id> 类里；改名/增删色板必须两边同步（见 themes.css 头注释）。
type themePalette struct {
	ID    string
	Label string
}

var themePalettes = []themePalette{
	{"black-swan", "黑天鹅"},
	{"moss", "苔绿"},
	{"ocean", "深海"},
	{"dusk", "暮色"},
}

var themeModes = []struct {
	ID    string
	Label string
	Icon  string
}{
	{"light", "亮色", "i-sun"},
	{"dark", "暗色", "i-moon"},
	{"system", "跟随系统", "i-monitor"},
}

// renderThemePanel 输出侧栏底部的主题面板（默认 hidden，themes.js 开合）。
// 弹层四要素（19-ued §19.5.1）：点外与 Esc 关闭、开合同步 aria-expanded 在
// themes.js 的委托里；色卡/三态用 role=radio + aria-checked 表达选中态。
func renderThemePanel() template.HTML {
	var b strings.Builder
	b.WriteString(`<div class="ada-theme-pop" id="ada-theme-panel" data-theme-panel aria-label="主题设置" hidden>`)
	b.WriteString(`<div class="ada-theme-pop__group" role="radiogroup" aria-label="明暗模式">`)
	for _, m := range themeModes {
		fmt.Fprintf(&b,
			`<button type="button" class="ada-theme-pop__item" role="radio" aria-checked="false" data-theme-mode="%s" title="%s">%s<span>%s</span></button>`,
			m.ID, m.Label, iconHTML(m.Icon, 14), m.Label,
		)
	}
	b.WriteString(`</div><div class="ada-theme-pop__group" role="radiogroup" aria-label="主题色板">`)
	for _, p := range themePalettes {
		fmt.Fprintf(&b,
			`<button type="button" class="ada-theme-pop__item" role="radio" aria-checked="false" data-theme-pick="%s" title="%s"><span class="ada-swatch ada-swatch--%s" aria-hidden="true"><i></i></span><span>%s</span></button>`,
			p.ID, p.Label, p.ID, p.Label,
		)
	}
	b.WriteString(`</div></div>`)
	return template.HTML(b.String())
}

// renderSidebar 复刻 a_da 的侧栏 DOM（展开态 + 56px 窄轨）。
func renderSidebar(d sidebarData) template.HTML {
	var rail strings.Builder
	rail.WriteString(`<div class="ada-rail">`)
	// 术语收敛（19-ued §19.2.3）：thread 统一「会话」，不再用「对话」。
	railTitle := "返回会话"
	rail.WriteString(fmt.Sprintf(
		`<a class="ada-rail__mark" href="/" title="%s" aria-label="%s"><img class="ada-rail__logo" src="%s" alt="" width="24" height="24" decoding="async"></a>`,
		railTitle, railTitle, assetURL("logo.png"),
	))
	rail.WriteString(`<a class="ada-rail__btn ada-rail__btn--primary" href="/" data-new-task-link title="新建任务" aria-label="新建任务">`)
	rail.WriteString(string(iconHTML("i-plus", 14)))
	rail.WriteString(`</a>`)
	if d.Search {
		rail.WriteString(`<button class="ada-rail__btn" type="button" data-rail-search title="搜索会话（Ctrl K）" aria-label="搜索会话">`)
		rail.WriteString(string(iconHTML("i-search", 18)))
		rail.WriteString(`</button>`)
	}
	head := d.headGroup()
	if len(head.Threads) > 0 {
		rail.WriteString(`<span class="ada-rail__sep"></span>`)
		for i, t := range head.Threads {
			if i >= 8 {
				break
			}
			initial := "·"
			if trimmed := strings.TrimSpace(t.Title); trimmed != "" {
				initial = string([]rune(trimmed)[0])
			}
			active := d.ActiveThreadID != "" && t.ID == d.ActiveThreadID
			rail.WriteString(fmt.Sprintf(
				`<a class="%s" href="/threads/%s"%s>%s</a>`,
				classList("ada-rail__thumb", boolClass(active, "is-active")),
				esc(t.ID), attr("title", t.Title), esc(initial),
			))
		}
	}
	rail.WriteString(`<span class="ada-rail__spacer"></span>`)
	rail.WriteString(railLink(d.Active, "extensions", "i-plug", "插件", "/extensions"))
	rail.WriteString(railLink(d.Active, "overview", "i-chart", "概览", "/overview"))
	rail.WriteString(`<button class="ada-rail__btn" type="button" data-theme-toggle title="切换深浅色" aria-label="切换深浅色">`)
	rail.WriteString(string(iconHTML("i-moon", 14)))
	rail.WriteString(`</button></div>`)

	var b strings.Builder
	b.WriteString(`<aside class="ada-sidebar" id="chat-sidebar" aria-label="侧栏"><div class="ada-side">`)
	// 品牌位：始终显示 logo + 品牌名 + 产品徽标；二级页链接回对话。
	brandLabel := "返回会话"
	badge := ""
	if d.ProductBadge != "" {
		badge = fmt.Sprintf(`<span class="ada-brand__badge">%s</span>`, esc(d.ProductBadge))
	}
	b.WriteString(fmt.Sprintf(
		`<a href="/" class="ada-brand" aria-label="%s" title="%s"><span class="ada-brand__mark ada-brand__mark--logo"><img class="ada-brand__logo" src="%s" alt="" width="28" height="28" decoding="async"></span><span class="ada-brand__name">%s</span>%s</a>`,
		brandLabel, brandLabel, assetURL("logo.png"), esc(d.BrandName), badge,
	))
	b.WriteString(string(RenderSlot("sidebar.top", d.SidebarTop, d.SidebarTopCount, "")))
	// 新建任务
	b.WriteString(`<a class="ada-new ada-btn ada-btn--secondary ada-btn--sm" href="/" data-new-task-link title="新建任务（Ctrl N）">`)
	b.WriteString(string(iconHTML("i-plus", 14)))
	b.WriteString(`<span>新建任务</span><kbd class="ada-new__kbd">Ctrl N</kbd></a>`)
	// 工作区列表直接铺在侧栏：每个工作区一组会话，切换不再跳单独的管理页。
	// 右侧「+」打开工作区目录（有 JS 走 FilePicker，无 JS 退回 /workspaces 目录页）。
	b.WriteString(`<div class="ada-ws-head"><span class="ada-ws-head__label">工作区</span>`)
	b.WriteString(fmt.Sprintf(
		`<a class="ada-ws-head__add" href="/workspaces" data-open-workspace data-current-workspace="%s" title="打开工作区…" aria-label="打开工作区…">%s</a>`,
		esc(head.Path), iconHTML("i-plus", 14),
	))
	b.WriteString(`</div>`)
	if d.Search {
		b.WriteString(fmt.Sprintf(
			`<div class="ada-search">%s<input id="chat-thread-search" class="ada-search__input" type="search" placeholder="搜索会话…" autocomplete="off" aria-label="搜索会话"><kbd class="ada-search__kbd">Ctrl K</kbd><span class="ada-search__count" hidden></span></div>`,
			iconHTML("i-search", 14),
		))
	}
	// 会话树
	threadListClass := "ada-thread-list"
	if len(d.Groups) > 1 {
		// 有其它工作区时拆成上下两块：当前工作区固定，其它工作区单独滚动。
		threadListClass += " ada-thread-list--split"
	}
	b.WriteString(fmt.Sprintf(`<div class="%s" id="thread-list">`, threadListClass))
	b.WriteString(renderThreadTree(d.Groups, d.ActiveThreadID, d.CSRF, d.CurrentPath))
	b.WriteString(`</div>`)
	// 系统导航
	b.WriteString(`<nav class="ada-nav ada-nav--util" aria-label="系统">`)
	if len(d.PluginNav) > 0 {
		// 分组标签不能叫「插件」：下面紧跟的条目里就有一个「插件」（/extensions
		// 管理页），同名分组 + 同名条目让人分不清哪一行才是插件清单入口。
		b.WriteString(`<div class="ada-nav__label"><span>插件页面</span></div>`)
		// 插件页多时用滚动条展示全部条目（固定高度内滚动），不再截断成「还有 N 个…」。
		b.WriteString(`<div class="ada-nav__scroll" role="group" aria-label="插件页面" tabindex="0">`)
		for _, link := range d.PluginNav {
			active := d.Active == "ext:"+link.PluginID
			current := ""
			if active {
				current = ` aria-current="page"`
			}
			b.WriteString(fmt.Sprintf(
				`<a class="%s" href="%s"%s%s>%s<span class="ada-nav__text">%s</span></a>`,
				classList("ada-nav__item", boolClass(active, "is-active")),
				esc(link.Href), current, attr("title", link.Label), iconHTML("i-plug", 14), esc(link.Label),
			))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(navItem(d.Active, "extensions", "i-plug", "插件", "/extensions"))
	b.WriteString(navItem(d.Active, "overview", "i-chart", "概览", "/overview"))
	b.WriteString(`</nav>`)
	b.WriteString(string(RenderSlot("sidebar.bottom", d.SidebarBottom, d.SidebarBottomCount, "")))
	// 底部：主题 + 网关地址
	dot, status := "", ""
	if d.Connected != nil {
		if *d.Connected {
			dot, status = `<span class="ada-gateway__dot is-on"></span>`, "已连接"
		} else {
			dot, status = `<span class="ada-gateway__dot is-off"></span>`, "未连接"
		}
	}
	title := d.Gateway
	if status != "" {
		title = status + " · " + d.Gateway
	}
	// 底部：主题面板 + 网关地址。主题按钮升级为面板触发器（19-ued §19.2.2）：
	// 面板内含明暗三态与色板选择；rail 上的 data-theme-toggle 仍是快速明暗翻转。
	b.WriteString(fmt.Sprintf(
		`<div class="ada-sidebar__foot"><div class="ada-theme-wrap"><button class="ada-icon-btn ada-theme" type="button" data-theme-panel-toggle aria-haspopup="true" aria-expanded="false" aria-controls="ada-theme-panel" title="主题设置" aria-label="主题设置"><span class="ada-theme__moon">%s</span><span class="ada-theme__sun">%s</span></button>%s</div><span class="ada-gateway"%s>%s<span class="ada-gateway__addr">%s</span></span></div>`,
		iconHTML("i-moon", 14), iconHTML("i-sun", 14), renderThemePanel(), attr("title", title), dot, esc(displayAddress(d.Gateway)),
	))
	b.WriteString(`</div>`)
	// 右侧拖拽条：宽屏展开态可左右调整侧栏宽度（JS 接管；窄屏抽屉/窄轨下隐藏）。
	b.WriteString(`<div class="ada-sidebar__resizer" data-sidebar-resizer role="separator" aria-orientation="vertical" aria-label="调整侧栏宽度" aria-valuemin="200" aria-valuemax="420" aria-valuenow="264" title="拖动调整侧栏宽度（双击恢复默认）" tabindex="0"></div>`)
	b.WriteString(rail.String())
	b.WriteString(`</aside>`)
	return template.HTML(b.String())
}

func (d sidebarData) headGroup() sidebarGroup {
	for _, g := range d.Groups {
		if g.Active {
			return g
		}
	}
	if len(d.Groups) > 0 {
		return d.Groups[0]
	}
	return sidebarGroup{}
}

func boolClass(v bool, cls string) string {
	if v {
		return cls
	}
	return ""
}

// csrfInput 输出双提交校验用的隐藏字段；空 token 时输出空串（Protect 未配置）。
func csrfInput(token string) string {
	if token == "" {
		return ""
	}
	return `<input type="hidden" name="csrf_token" value="` + esc(token) + `">`
}

func navItem(active, key, icon, label, href string) string {
	cls := classList("ada-nav__item", boolClass(active == key, "is-active"))
	current := ""
	if active == key {
		current = ` aria-current="page"`
	}
	return fmt.Sprintf(`<a class="%s" href="%s"%s>%s<span class="ada-nav__text">%s</span></a>`,
		cls, esc(href), current, iconHTML(icon, 14), esc(label))
}

func railLink(active, key, icon, label, href string) string {
	cls := classList("ada-rail__btn", boolClass(active == key, "is-active"))
	return fmt.Sprintf(`<a class="%s" href="%s"%s>%s</a>`, cls, esc(href), attr("title", label), iconHTML(icon, 18))
}

// renderThreadTree 渲染侧栏的工作区/会话树：当前工作区默认展开并直接列出会话；
// 其它工作区各是一个折叠的 <details>，展开时由 app.js 调
// GET /partials/workspace-threads 懒加载会话（跨工作区查询会各自拉起 Core，
// 不适合在每次页面渲染时全量做）。
func renderThreadTree(groups []sidebarGroup, activeThreadID, csrf, currentPath string) string {
	if len(groups) == 0 {
		return `<p class="ada-list-empty">还没有会话</p>`
	}
	active := 0
	for i := range groups {
		if groups[i].Active {
			active = i
			break
		}
	}
	var b strings.Builder
	b.WriteString(`<div class="ada-ws-scroll ada-ws-scroll--active" data-ws-active>`)
	b.WriteString(renderWorkspaceGroup(groups[active], activeThreadID, csrf, currentPath))
	b.WriteString(`</div>`)
	others := make([]sidebarGroup, 0, len(groups)-1)
	for i, g := range groups {
		if i != active {
			others = append(others, g)
		}
	}
	if len(others) > 0 {
		// 其它工作区是独立滚动块：滚轮只在这里滚，当前工作区不会跟着滚出视口。
		b.WriteString(`<div class="ada-ws-scroll ada-ws-scroll--others" data-ws-others>`)
		b.WriteString(fmt.Sprintf(
			`<p class="ada-ws__section" data-ws-section><span class="ada-ws__section-label">其它工作区</span><span class="ada-ws__section-count">%d</span></p>`,
			len(others),
		))
		b.WriteString(`<div class="ada-ws-scroll--workArea">`)
		for _, g := range others {
			b.WriteString(renderWorkspaceGroup(g, activeThreadID, csrf, currentPath))
		}
		b.WriteString(`</div>`)
		b.WriteString(`</div>`)
	}
	return b.String()
}

// renderWorkspaceGroup 渲染一个工作区组：当前组默认展开、会话已由服务端渲染；
// 其它组默认折叠、会话由 app.js 在展开时懒加载。
func renderWorkspaceGroup(g sidebarGroup, activeThreadID, csrf, currentPath string) string {
	var rows strings.Builder
	open := ""
	count := fmt.Sprintf(`<span class="ada-ws__count">%d</span>`, g.Count)
	if g.Active {
		open = " open"
		if len(g.Threads) == 0 {
			rows.WriteString(`<p class="ada-ws__empty">还没有会话</p>`)
		} else {
			for _, t := range g.Threads {
				rows.WriteString(renderThreadRow(t, activeThreadID, csrf, currentPath, ""))
			}
		}
	} else {
		count = `<span class="ada-ws__count" data-ws-count hidden></span>`
		rows.WriteString(`<p class="ada-ws__empty" data-ws-loading>展开以加载会话…</p>`)
	}
	running := ""
	if g.Running {
		running = `<span class="ada-ws__running" title="该工作区有会话正在运行"><span class="ada-dot ada-dot--warn"></span></span>`
	}
	var actions string
	if g.Active {
		actions = fmt.Sprintf(
			`<div class="ada-ws__actions"><a class="ada-ws__action" href="/?ws=%d" title="在此工作区新建任务" aria-label="在此工作区新建任务">%s</a></div>`,
			g.Index, iconHTML("i-plus", 14),
		)
	} else {
		actions = fmt.Sprintf(
			`<div class="ada-ws__actions"><form method="post" action="/workspaces/select"><input type="hidden" name="path" value="%s">%s<button class="ada-ws__action" type="submit" title="切换到此工作区" aria-label="切换到此工作区">%s</button></form></div>`,
			esc(g.Path), csrfInput(csrf), iconHTML("i-check", 14),
		)
	}
	return fmt.Sprintf(
		`<div class="ada-ws" data-ws-key="%s" data-ws-path="%s" data-ws-index="%d"><details class="ada-ws__details" id="%s"%s><summary class="ada-ws__summary"%s>%s%s<span class="ada-ws__name">%s</span>%s%s</summary><div class="ada-ws__threads" data-ws-threads>%s</div></details>%s</div>`,
		esc(g.Key), esc(g.Path), g.Index, wsDOMID(g.Key), open, attr("title", g.Path),
		iconHTML("i-chevron", 12), iconHTML("i-folder", 14), esc(g.Name), running, count, rows.String(), actions,
	)
}

// renderWorkspaceThreadRows 渲染一个工作区的会话行（侧栏懒加载片段用）。
// wsIndex >= 0 时行链接带 ?ws=<index>：点击后网关把工作区 cookie 切到该工作区。
func renderWorkspaceThreadRows(threads []threadSummaryDTO, wsIndex int, csrf, currentPath string) string {
	if len(threads) == 0 {
		return `<p class="ada-ws__empty">还没有会话</p>`
	}
	query := ""
	if wsIndex >= 0 {
		query = "?ws=" + strconv.Itoa(wsIndex)
	}
	var b strings.Builder
	for _, t := range threads {
		b.WriteString(renderThreadRow(sidebarThread{
			ID:        t.ID,
			Title:     t.Title,
			UpdatedMS: parseTimeMS(t.UpdatedAt),
			Running:   t.Running,
		}, "", csrf, currentPath, query))
	}
	return b.String()
}

func renderThreadRow(t sidebarThread, activeThreadID, csrf, currentPath, wsQuery string) string {
	active := activeThreadID != "" && t.ID == activeThreadID
	dot := ""
	if t.Running {
		dot = `<span class="ada-thread__dot" title="运行中" aria-label="运行中"></span>`
	}
	elapsed := relativeTime(t.UpdatedMS)
	timeHTML := ""
	if elapsed != "" {
		timeHTML = fmt.Sprintf(`<span class="ada-thread__time"%s>%s</span>`, attr("title", "最后活动："+elapsed), elapsed)
	}
	// 运行中的会话不提供删除：避免删掉仍在一轮里的线程。其余会话在悬停/聚焦时
	// 显示垃圾桶按钮，提交前由 app.js 的 data-confirm 弹窗确认。
	actions := ""
	if !t.Running {
		actions = fmt.Sprintf(
			`<div class="ada-thread__actions"><form method="post" action="/threads/%s/delete%s" data-confirm="删除会话「%s」？删除后无法恢复。"><input type="hidden" name="next" value="%s">%s<button class="ada-thread__action" type="submit" title="删除会话" aria-label="删除会话">%s</button></form></div>`,
			esc(t.ID), wsQuery, esc(t.Title), esc(currentPath), csrfInput(csrf), iconHTML("i-trash", 14),
		)
	}
	return fmt.Sprintf(
		`<div class="%s" data-thread-row="%s"><a class="ada-thread__link" href="/threads/%s%s"%s><span class="ada-thread__title">%s</span><span class="ada-thread__meta">%s%s</span></a>%s</div>`,
		classList("ada-thread", boolClass(active, "is-active")),
		esc(t.ID), esc(t.ID), wsQuery, attr("title", t.Title), esc(t.Title), dot, timeHTML, actions,
	)
}

// wsDOMID 把工作区路径映射成稳定的 DOM id（FNV-1a），避免路径里的分隔符/空格
// 进 id 属性；同一工作区在多次渲染里得到同一个 id。
func wsDOMID(key string) string {
	var hash uint32 = 0x811c9dc5
	for _, b := range []byte(key) {
		hash ^= uint32(b)
		hash *= 0x01000193
	}
	return fmt.Sprintf("ws-%08x", hash)
}

func relativeTime(atMS int64) string {
	if atMS == 0 {
		return ""
	}
	now := time.Now().UnixMilli()
	if atMS >= now {
		return "刚刚"
	}
	secs := (now - atMS) / 1000
	switch {
	case secs < 60:
		return "刚刚"
	case secs < 3600:
		return fmt.Sprintf("%d 分钟前", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%d 小时前", secs/3600)
	case secs < 604800:
		return fmt.Sprintf("%d 天前", secs/86400)
	case secs < 2592000:
		return fmt.Sprintf("%d 周前", secs/604800)
	case secs < 31536000:
		return fmt.Sprintf("%d 个月前", secs/2592000)
	default:
		return fmt.Sprintf("%d 年前", secs/31536000)
	}
}

func displayAddress(gateway string) string {
	trimmed := strings.TrimSpace(gateway)
	for _, scheme := range []string{"http://", "https://", "ws://", "wss://", "pipe://", "unix://"} {
		if strings.HasPrefix(trimmed, scheme) {
			return strings.TrimSuffix(strings.TrimPrefix(trimmed, scheme), "/")
		}
	}
	return trimmed
}

// ---- 共享确认弹窗 ----

func confirmDialogHTML() template.HTML {
	return template.HTML(`<ada-modal class="ada-modal" data-modal="confirm" data-confirm-dialog hidden>
  <div class="ada-modal__backdrop" data-modal-close data-confirm-cancel></div>
  <div class="ada-modal__panel" role="dialog" aria-modal="true" aria-labelledby="ada-confirm-title">
    <h2 class="ada-modal__title" id="ada-confirm-title">确认操作</h2>
    <p class="ada-modal__message" data-confirm-message>确认执行这个操作？</p>
    <div class="ada-modal__actions"><button class="ada-btn ada-btn--secondary ada-btn--sm" type="button" data-modal-close data-confirm-cancel>取消</button><button class="ada-btn ada-btn--primary ada-btn--sm" type="button" data-confirm-ok data-modal-autofocus>确认</button></div>
  </div>
</ada-modal>`)
}

// ---- 提示条 ----

func flashHTML(kind, message string) template.HTML {
	if message == "" {
		return ""
	}
	if kind == "" {
		kind = "info"
	}
	tone := map[string]string{"error": "alert-danger", "success": "alert-success"}[kind]
	if tone == "" {
		tone = "alert-info"
	}
	role := "status"
	if kind == "error" {
		role = "alert"
	}
	return template.HTML(fmt.Sprintf(
		`<div class="ada-flash alert %s" data-flash="%s" role="%s"><p class="alert-message">%s</p><button class="ada-flash__close" type="button" data-dismiss aria-label="关闭提示" title="关闭">%s</button></div>`,
		tone, esc(kind), role, esc(message), iconHTML("i-x", 12),
	))
}
