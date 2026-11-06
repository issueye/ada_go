package design

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 图标库：24×24 网格、2px 视觉描边的线性图标，统一 currentColor 着色。
//
// 数据组织：一项 = 名字 + 分组 + 别名 + 路径片段。路径集中在 icon_paths.go，
// 由 init 注册；本文件只负责注册表、检索与渲染。
//
// 渲染策略：每个预览文档在 <body> 首部注入一份 <symbol> 精灵（IconSprite），
// 使用处写 <svg class="ds-icon"><use href="#ds-ic-<name>"/></svg>。
// 之所以必须内联而非外链：预览是 iframe srcdoc 文档（相对路径无基准），
// 截图走 SVG foreignObject 序列化 + <img> 光栅化（外链资源会 404 或污点化 canvas）。
//
// 画布 UI 侧的 icons.js 由同一份注册表生成，改图标后跑一次 go generate 即可同步。
//
//go:generate go run ./gen_icons -out ../canvas/static/icons.js
const (
	IconGroupArrow  = "arrow"  // 方向与层级
	IconGroupAction = "action" // 动作与工具
	IconGroupStatus = "status" // 状态与反馈
	IconGroupNav    = "nav"    // 导航入口
	IconGroupMedia  = "media"  // 内容互动
	IconGroupData   = "data"   // 列表 / 表格 / 数据
)

// Icon 是图标库中的一项。
type Icon struct {
	Name   string   `json:"name"`             // 唯一名，kebab-case，如 "chevron-right"
	Group  string   `json:"group"`            // arrow | action | status | nav | media | data
	Tags   []string `json:"tags,omitempty"`   // 别名与检索词（含被替换掉的字符符号）
	Body   string   `json:"-"`                // <path/> / <circle/> 片段，不含 <svg> 外壳
	Filled bool     `json:"filled,omitempty"` // 实心态（用 fill 而非 stroke）
}

var iconRegistry = map[string]Icon{}

// RegisterIcon 注册一个图标（init 中调用）；重名即 panic。
func RegisterIcon(ic Icon) {
	if ic.Name == "" {
		panic("design: icon name is empty")
	}
	if ic.Body == "" {
		panic("design: icon " + ic.Name + " has empty body")
	}
	if _, dup := iconRegistry[ic.Name]; dup {
		panic(fmt.Sprintf("design: duplicate icon name %q", ic.Name))
	}
	if ic.Group == "" {
		ic.Group = IconGroupAction
	}
	iconRegistry[ic.Name] = ic
}

// Icons 返回全部图标（按分组 → 名字排序）。
func Icons() []Icon {
	out := make([]Icon, 0, len(iconRegistry))
	for _, ic := range iconRegistry {
		out = append(out, ic)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GetIcon 按名字查找图标。
func GetIcon(name string) (Icon, bool) {
	ic, ok := iconRegistry[strings.ToLower(strings.TrimSpace(name))]
	return ic, ok
}

// HasIcon 判断图标是否存在。
func HasIcon(name string) bool {
	_, ok := GetIcon(name)
	return ok
}

// SearchIcons 按名字/分组/别名模糊匹配；query 为空时返回全部。
func SearchIcons(query string) []Icon {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return Icons()
	}
	out := make([]Icon, 0, 8)
	for _, ic := range Icons() {
		if strings.Contains(ic.Name, q) || strings.Contains(ic.Group, q) {
			out = append(out, ic)
			continue
		}
		for _, t := range ic.Tags {
			if strings.Contains(strings.ToLower(t), q) {
				out = append(out, ic)
				break
			}
		}
	}
	return out
}

// IconSprite 返回整份 <symbol> 精灵，注入每个预览文档的 <body> 首部。
// 只序列化一次路径数据，N 个图标不重复膨胀文档。
func IconSprite() string {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" aria-hidden="true" focusable="false" `)
	b.WriteString(`style="position:absolute;width:0;height:0;overflow:hidden">`)
	for _, ic := range Icons() {
		b.WriteString(`<symbol id="ds-ic-` + ic.Name + `" viewBox="0 0 24 24">` + ic.Body + `</symbol>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// IconSizes 是允许的光学尺寸档（与 canvas.css / ds-icon--* 对齐）。
var IconSizes = map[int]bool{12: true, 14: true, 16: true, 20: true, 24: true}

func normIconSize(size int) int {
	if IconSizes[size] {
		return size
	}
	return 20
}

// IconRef 渲染精灵引用（同文档内 <use> 有效）；未知图标返回空串。
func IconRef(name string, size int) string {
	if !HasIcon(name) {
		return ""
	}
	cls := "ds-icon ds-icon--" + strconv.Itoa(normIconSize(size))
	return `<svg class="` + cls + `" aria-hidden="true" focusable="false"><use href="#ds-ic-` + name + `"/></svg>`
}

// IconInline 返回可独立使用的完整 <svg>（用于 MCP 片段、独立 HTML 文件）。
func IconInline(name string, size int) string {
	ic, ok := GetIcon(name)
	if !ok {
		return ""
	}
	cls := "ds-icon ds-icon--" + strconv.Itoa(normIconSize(size))
	attrs := `class="` + cls + `" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" aria-hidden="true"`
	if ic.Filled {
		attrs = `class="` + cls + ` ds-icon--filled" viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg" aria-hidden="true"`
	}
	return `<svg ` + attrs + `>` + ic.Body + `</svg>`
}

// iconTokenRe 匹配模板里的图标占位：{{icon:chevron-down}} 或 {{icon:chevron-down:16}}。
// 用占位符而非字符串拼接，让组件模板保持可读、可 diff。
var iconTokenRe = regexp.MustCompile(`\{\{icon:([a-z0-9-]+)(?::(\d+))?\}\}`)

// iconRefRe 匹配渲染产物里的精灵引用，供走查/单测核对 symbol 是否存在。
var iconRefRe = regexp.MustCompile(`href="#(ds-ic-[a-z0-9-]+)"`)

// ExpandIcons 把模板中的 {{icon:name:size}} 展开为精灵引用 <svg>。
func ExpandIcons(s string) string {
	if !strings.Contains(s, "{{icon:") {
		return s
	}
	return iconTokenRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := iconTokenRe.FindStringSubmatch(m)
		size := 20
		if len(sub) > 2 && sub[2] != "" {
			size, _ = strconv.Atoi(sub[2])
		}
		// 未知图标展开为空串，由 audit 的图标检查兜住（不静默变成字符）
		return IconRef(sub[1], size)
	})
}

// IconTokenNames 抽出模板里引用的全部图标名（供走查校验拼写）。
func IconTokenNames(s string) []string {
	ms := iconTokenRe.FindAllStringSubmatch(s, -1)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m[1])
	}
	return out
}

// IconCatalog 返回给 MCP / UI 的图标清单（不含路径数据）。
func IconCatalog(group string) []map[string]any {
	out := make([]map[string]any, 0, len(iconRegistry))
	for _, ic := range Icons() {
		if group != "" && ic.Group != group {
			continue
		}
		svg := IconInline(ic.Name, 24)
		out = append(out, map[string]any{
			"name":  ic.Name,
			"group": ic.Group,
			"tags":  ic.Tags,
			"svg":   svg,
			"usage": `<svg class="ds-icon ds-icon--20"><use href="#ds-ic-` + ic.Name + `"/></svg>`,
		})
	}
	return out
}
