package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"adagodesign/internal/design"
)

// 目录类工具：图标库、字体授权、画板尺寸规范。
// 这三个是「规范层」的查询入口，设计流程第一步就该用它们，
// 避免 Agent 用 emoji 当图标、用不可商用字体、用拍出来的画布尺寸。
func registerCatalogTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "design_icons",
		Description: "查询内置 SVG 图标库（24 网格线性图标）。生成界面时必须用它取图标，" +
			"不得使用 emoji 或 ▾ › ✕ ◉ 等字符当图标。",
	}, iconsTool())

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_fonts",
		Description: "查询字体授权注册表：哪些字体可商用、可嵌入、可再分发，以及各脚本的默认字族栈。",
	}, fontsTool())

	mcp.AddTool(server, &mcp.Tool{
		Name: "design_artboard",
		Description: "查询画板（设计稿尺寸规范）：移动/平板/Web/桌面应用的画板尺寸、安全区、栅格与内容区推导值。" +
			"设计流程第一步先定画板，再生成预览。",
	}, artboardTool())
}

// ---------------- design_icons ----------------

type iconsIn struct {
	Query  string `json:"query,omitempty" jsonschema:"按名称/分组/别名模糊匹配，如 close、⚠、chevron、home"`
	Group  string `json:"group,omitempty" jsonschema:"分组: arrow|action|status|nav|media|data"`
	Format string `json:"format,omitempty" jsonschema:"sprite（默认，返回 <use> 引用写法）|inline（返回独立 <svg>）"`
}

type iconItem struct {
	Name  string   `json:"name"`
	Group string   `json:"group"`
	Tags  []string `json:"tags,omitempty"`
	HTML  string   `json:"html"`
}

type iconsOut struct {
	Count int        `json:"count"`
	Icons []iconItem `json:"icons"`
}

func iconsTool() mcp.ToolHandlerFor[iconsIn, iconsOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in iconsIn) (*mcp.CallToolResult, iconsOut, error) {
		list := design.SearchIcons(in.Query)
		if in.Group != "" {
			filtered := make([]design.Icon, 0, len(list))
			for _, ic := range list {
				if ic.Group == in.Group {
					filtered = append(filtered, ic)
				}
			}
			list = filtered
		}
		out := iconsOut{Icons: make([]iconItem, 0, len(list))}
		for _, ic := range list {
			html := `<svg class="ds-icon ds-icon--20"><use href="#ds-ic-` + ic.Name + `"/></svg>`
			if in.Format == "inline" {
				html = design.IconInline(ic.Name, 20)
			}
			out.Icons = append(out.Icons, iconItem{Name: ic.Name, Group: ic.Group, Tags: ic.Tags, HTML: html})
		}
		out.Count = len(out.Icons)

		var sb strings.Builder
		switch {
		case out.Count == 0:
			fmt.Fprintf(&sb, "没有匹配 %q 的图标。可用分组：arrow/action/status/nav/media/data。", in.Query)
		default:
			fmt.Fprintf(&sb, "共 %d 个图标（%d 个可用）：\n", out.Count, len(design.Icons()))
			for _, it := range out.Icons {
				fmt.Fprintf(&sb, "- %s [%s] %s\n", it.Name, it.Group, it.HTML)
			}
			sb.WriteString("\n用法：预览文档已内联图标精灵，直接用上面的 <use> 引用即可；" +
				"图标颜色继承 currentColor，尺寸用 ds-icon--12/14/16/20/24。")
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), out, nil
	}
}

// ---------------- design_fonts ----------------

type fontsIn struct {
	Script string `json:"script,omitempty" jsonschema:"latin|hans|mono，留空返回全部"`
}

type fontsOut struct {
	Fonts         []design.FontSpec `json:"fonts"`
	DefaultStacks map[string]string `json:"default_stacks"`
	Policy        []string          `json:"policy"`
}

func fontsTool() mcp.ToolHandlerFor[fontsIn, fontsOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in fontsIn) (*mcp.CallToolResult, fontsOut, error) {
		all := design.Fonts()
		out := fontsOut{DefaultStacks: design.DefaultStacks, Fonts: make([]design.FontSpec, 0, len(all))}
		for _, f := range all {
			if in.Script != "" && f.Script != in.Script {
				continue
			}
			out.Fonts = append(out.Fonts, f)
		}
		out.Policy = []string{
			"预览文档禁止 @font-face 外链（srcdoc + foreignObject 截图取不到，且会引入授权问题）",
			"品牌字体只按本地 family 名引用；Redistributable=false 的字体（如 MiSans）不得随包分发字体文件",
			"交付级设计必须选择 Commercial=true 的字体；系统字体只作尾部兜底，不能作为唯一来源",
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "字体 %d 款；默认栈：latin=%s\nhans=%s\n", len(out.Fonts),
			design.DefaultStacks["latin"], design.DefaultStacks["hans"])
		for _, f := range out.Fonts {
			flags := make([]string, 0, 3)
			if f.Commercial {
				flags = append(flags, "可商用")
			}
			if f.Embeddable {
				flags = append(flags, "可嵌入")
			}
			if f.Redistributable {
				flags = append(flags, "可再分发")
			}
			fmt.Fprintf(&sb, "- %s（%s，%s）%s\n", f.Family, f.ID, f.License, strings.Join(flags, "/"))
		}
		sb.WriteString("\n规则：\n- " + strings.Join(out.Policy, "\n- "))
		return textResult(sb.String()), out, nil
	}
}

// ---------------- design_artboard ----------------

type artboardIn struct {
	Artboard string `json:"artboard,omitempty" jsonschema:"画板 ID，如 iphone-15 / web-1440；留空则列出全部"`
	Family   string `json:"family,omitempty" jsonschema:"mobile|tablet|web|cross，用于过滤或取默认画板"`
}

type artboardOut struct {
	Count    int                  `json:"count"`
	Boards   []map[string]any     `json:"boards"`
	Selected *design.ArtboardSpec `json:"selected,omitempty"`
}

func artboardTool() mcp.ToolHandlerFor[artboardIn, artboardOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in artboardIn) (*mcp.CallToolResult, artboardOut, error) {
		if in.Artboard != "" {
			ab, ok := design.GetArtboard(in.Artboard)
			if !ok {
				return fail[artboardOut]("unknown_artboard", "未知画板 "+in.Artboard,
					"去掉 artboard 参数可列出全部画板")
			}
			spec := ab.Spec()
			out := artboardOut{Count: 1, Boards: []map[string]any{{
				"id": ab.ID, "name": ab.Name, "family": ab.Family,
				"width": ab.Width, "height": ab.Height,
			}}, Selected: &spec}
			return textResult(spec.Contract), out, nil
		}
		boards := design.ArtboardCatalog(in.Family)
		out := artboardOut{Count: len(boards), Boards: boards}
		var sb strings.Builder
		fmt.Fprintf(&sb, "共 %d 块画板：\n", len(boards))
		for _, b := range boards {
			fmt.Fprintf(&sb, "- %s · %v×%v（%v），内容区 %v×%v\n",
				b["id"], b["width"], b["height"], b["family"], b["content_width"], b["content_height"])
		}
		if in.Family != "" {
			spec := design.DefaultArtboardFor(in.Family).Spec()
			out.Selected = &spec
			sb.WriteString("\n" + spec.Contract)
		}
		return textResult(sb.String()), out, nil
	}
}
