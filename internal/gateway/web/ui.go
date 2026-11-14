package web

// 本文件是 a_da 组件库（crates/agent-web/src/ui）的 Go 移植中仍被使用的部分：
//   - 图标（iconHTML 供模板 FuncMap 与 flashHTML 使用）；
//   - Markdown 渲染（goldmark + bluemonday 清洗，markdown_vnode.go 的唯一输入）；
//   - 提示条 flashHTML（login/error 页与 PRG 回执）；
//   - relativeTime（dashboard.json 的相对时间标签）。
//
// 所有动态文本都必须经过 html 包转义；只有本文件生成的 template.HTML 能进模板。

import (
	"fmt"
	"html"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	xhtml "golang.org/x/net/html"
	xhtmlatom "golang.org/x/net/html/atom"
)

// ---- 转义 ----

func esc(s string) string { return html.EscapeString(s) }

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
	{"i-database", `<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>`},
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
// 这是 markdown_vnode.go（HTML → VNode JSON）的唯一上游：浏览器只拿结构化数据。
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

// ---- 相对时间 ----

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
