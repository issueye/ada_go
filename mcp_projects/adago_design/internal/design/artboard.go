package design

import (
	"fmt"
	"sort"
	"strings"
)

// 画板（Artboard）：设计稿的尺寸契约。
//
// 命名说明：canvas 包里的 KindFrame 是「画布上的分组框」，与本文件的画板无关，
// 因此这里统一用 Artboard，不要复用 Frame 这个词。
//
// 流程约定：先定画板，再定体系与组件。所有预览/整页模板的宽高都必须来自画板，
// 不允许在 Compose / ComposeScreen 里再写 420 / 390 / 780 这类魔法数。
type Artboard struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Family     string `json:"family"` // mobile | tablet | web | cross
	Width      int    `json:"width"`  // 设计稿逻辑像素 @1x
	Height     int    `json:"height"`
	StatusBar  int    `json:"status_bar"`
	SafeTop    int    `json:"safe_top"`
	SafeBottom int    `json:"safe_bottom"`
	NavBar     int    `json:"nav_bar"`
	TabBar     int    `json:"tab_bar"`
	Columns    int    `json:"columns"`
	Margin     int    `json:"margin"`
	Gutter     int    `json:"gutter"`
	MaxContent int    `json:"max_content,omitempty"` // 内容区最大宽度（0 = 不限制）
	DPR        []int  `json:"dpr"`
}

// ArtboardSpec 是画板的推导值 —— 设计契约的实际内容。
type ArtboardSpec struct {
	Artboard
	ContentWidth  int      `json:"content_width"`
	BodyHeight    int      `json:"body_height"`
	ContentHeight int      `json:"content_height"`
	ExportSizes   []string `json:"export_sizes"`
	Contract      string   `json:"contract"`
}

var artboards = map[string]Artboard{}

func registerArtboard(a Artboard) {
	if a.ID == "" || a.Width <= 0 || a.Height <= 0 {
		panic("design: artboard id/size invalid: " + a.ID)
	}
	if _, dup := artboards[a.ID]; dup {
		panic("design: duplicate artboard id " + a.ID)
	}
	if len(a.DPR) == 0 {
		a.DPR = []int{1, 2, 3}
	}
	if a.Columns == 0 {
		a.Columns = 1
	}
	if a.Gutter == 0 {
		a.Gutter = a.Margin
	}
	artboards[a.ID] = a
}

func init() {
	registerArtboard(Artboard{ID: "iphone-15", Name: "iPhone 15 / 15 Pro", Family: "mobile",
		Width: 393, Height: 852, StatusBar: 54, SafeTop: 0, SafeBottom: 34,
		NavBar: 44, TabBar: 49, Columns: 1, Margin: 16, Gutter: 16, DPR: []int{1, 2, 3}})
	registerArtboard(Artboard{ID: "iphone-se", Name: "iPhone SE（小屏基准）", Family: "mobile",
		Width: 375, Height: 667, StatusBar: 20, SafeTop: 0, SafeBottom: 0,
		NavBar: 44, TabBar: 49, Columns: 1, Margin: 16, Gutter: 16, DPR: []int{1, 2}})
	registerArtboard(Artboard{ID: "android-360", Name: "Android 基准 360dp", Family: "mobile",
		Width: 360, Height: 800, StatusBar: 24, SafeTop: 0, SafeBottom: 0,
		NavBar: 48, TabBar: 56, Columns: 1, Margin: 16, Gutter: 16, DPR: []int{1, 2, 3}})
	registerArtboard(Artboard{ID: "mp-wx", Name: "微信小程序", Family: "mobile",
		Width: 375, Height: 812, StatusBar: 44, SafeTop: 0, SafeBottom: 0,
		NavBar: 44, TabBar: 50, Columns: 1, Margin: 16, Gutter: 16, DPR: []int{1, 2, 3}})
	registerArtboard(Artboard{ID: "ipad-768", Name: "iPad 竖屏", Family: "tablet",
		Width: 768, Height: 1024, StatusBar: 24, SafeTop: 0, SafeBottom: 0,
		NavBar: 50, TabBar: 0, Columns: 2, Margin: 24, Gutter: 16, DPR: []int{1, 2}})
	registerArtboard(Artboard{ID: "web-1280", Name: "Web 桌面 1280", Family: "web",
		Width: 1280, Height: 800, StatusBar: 0, SafeTop: 0, SafeBottom: 0,
		NavBar: 64, TabBar: 0, Columns: 12, Margin: 40, Gutter: 24, DPR: []int{1, 2}})
	registerArtboard(Artboard{ID: "web-1440", Name: "Web 桌面 1440", Family: "web",
		Width: 1440, Height: 900, StatusBar: 0, SafeTop: 0, SafeBottom: 0,
		NavBar: 64, TabBar: 0, Columns: 12, Margin: 60, Gutter: 24, MaxContent: 1200, DPR: []int{1, 2}})
	registerArtboard(Artboard{ID: "desktop-app", Name: "桌面应用壳 1440", Family: "cross",
		Width: 1440, Height: 900, StatusBar: 0, SafeTop: 0, SafeBottom: 0,
		NavBar: 56, TabBar: 0, Columns: 12, Margin: 24, Gutter: 24, MaxContent: 1200, DPR: []int{1, 2}})
}

// Artboards 返回全部画板（按 family → id 排序）。
func Artboards() []Artboard {
	out := make([]Artboard, 0, len(artboards))
	for _, a := range artboards {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Family != out[j].Family {
			return out[i].Family < out[j].Family
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// GetArtboard 按 ID 查找画板。
func GetArtboard(id string) (Artboard, bool) {
	a, ok := artboards[strings.ToLower(strings.TrimSpace(id))]
	return a, ok
}

// DefaultArtboardFor 按体系 Family 给出默认画板 —— 全仓唯一的默认尺寸来源。
func DefaultArtboardFor(family string) Artboard {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "mobile":
		return artboards["iphone-15"]
	case "tablet":
		return artboards["ipad-768"]
	case "cross":
		return artboards["desktop-app"]
	default:
		return artboards["web-1440"]
	}
}

// ResolveArtboard 优先使用显式 ID，否则按 Family 取默认值。
// 第二个返回值为 false 表示显式 ID 非法（调用方应报 unknown_artboard）。
func ResolveArtboard(id, family string) (Artboard, bool) {
	if strings.TrimSpace(id) == "" {
		return DefaultArtboardFor(family), true
	}
	a, ok := GetArtboard(id)
	return a, ok
}

// Spec 计算推导值 + 人读契约文本。
func (a Artboard) Spec() ArtboardSpec {
	contentW := a.Width - 2*a.Margin
	if a.MaxContent > 0 && contentW > a.MaxContent {
		contentW = a.MaxContent
	}
	bodyH := a.Height - a.StatusBar - a.SafeTop - a.SafeBottom
	contentH := bodyH - a.NavBar - a.TabBar
	if contentH < 0 {
		contentH = 0
	}
	sizes := make([]string, 0, len(a.DPR))
	for _, m := range a.DPR {
		sizes = append(sizes, fmt.Sprintf("@%dx %d×%d", m, a.Width*m, a.Height*m))
	}
	s := ArtboardSpec{
		Artboard: a, ContentWidth: contentW, BodyHeight: bodyH,
		ContentHeight: contentH, ExportSizes: sizes,
	}
	s.Contract = fmt.Sprintf(
		"%s · %d×%d @1x（导出 %s）；状态栏 %d / 安全区 上%d 下%d / 导航 %d / 标签栏 %d；"+
			"内容区 %d×%d，边距 %d，栅格 %d 列 × %d。约定：间距取 4 的倍数；坐标以画板左上角为原点；"+
			"不得输出非本画板尺寸的预览。",
		a.Name, a.Width, a.Height, strings.Join(sizes, " / "),
		a.StatusBar, a.SafeTop, a.SafeBottom, a.NavBar, a.TabBar,
		contentW, contentH, a.Margin, a.Columns, a.Gutter)
	return s
}

// BoardWidth 是「报告板」类节点（体系对比板 / 品牌色板 / 走查报告）的固定卡宽。
// 它们不是设计稿，不参与画板尺寸契约，故与 Artboard 分开命名，避免再被当成
// 「预览宽度」到处硬编码。
const BoardWidth = 420

// PageCSS 生成画板相关的 CSS 变量，注入预览文档的 <style>。
// cssBase 里的 .ds-page 通过这些变量取宽度与内边距，不再写死 560px。
func PageCSS(a Artboard) string {
	var b strings.Builder
	b.WriteString(":root{")
	fmt.Fprintf(&b, "--ds-ab-w:%dpx;--ds-ab-h:%dpx;", a.Width, a.Height)
	fmt.Fprintf(&b, "--ds-ab-content-w:%dpx;--ds-ab-margin:%dpx;", a.Spec().ContentWidth, a.Margin)
	fmt.Fprintf(&b, "--ds-ab-gutter:%dpx;--ds-ab-nav:%dpx;--ds-ab-tab:%dpx;", a.Gutter, a.NavBar, a.TabBar)
	fmt.Fprintf(&b, "--ds-ab-status:%dpx;--ds-ab-safe-b:%dpx;", a.StatusBar, a.SafeBottom)
	fmt.Fprintf(&b, "--ds-ab-pad-y:%dpx;--ds-ab-pad-b:%dpx;", a.Margin, a.Margin+24)
	b.WriteString("}\n")
	return b.String()
}

// ArtboardCatalog 返回给 MCP / UI 的画板清单（含推导值）。
func ArtboardCatalog(family string) []map[string]any {
	out := make([]map[string]any, 0, len(artboards))
	for _, a := range Artboards() {
		if family != "" && a.Family != family {
			continue
		}
		s := a.Spec()
		out = append(out, map[string]any{
			"id": a.ID, "name": a.Name, "family": a.Family,
			"width": a.Width, "height": a.Height,
			"content_width": s.ContentWidth, "content_height": s.ContentHeight,
			"margin": a.Margin, "gutter": a.Gutter, "columns": a.Columns,
			"export_sizes": s.ExportSizes, "contract": s.Contract,
		})
	}
	return out
}
