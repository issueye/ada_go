// Package mcpserver 把设计体系目录与画布能力装配为 MCP 工具集。
package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"adagodesign/internal/canvas"
	"adagodesign/internal/design"
)

// ServerVersion 是 MCP 服务器版本。
const ServerVersion = "0.1.0"

const serverName = "adago-design"

// Deps 是 MCP 工具依赖的运行时组件。
type Deps struct {
	Store   *canvas.Store
	Tasks   *canvas.TaskQueue
	BaseURL func() string
	DataDir string
}

// NewServer 创建配置好全部设计工具的 MCP 服务器。
func NewServer(d Deps) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: ServerVersion},
		&mcp.ServerOptions{
			Instructions: "这是设计体系 MCP（adago-design）：内置 Apple HIG、火山 Arco、抖音、小米 MIUI、Element Plus、" +
				"Ant Design、TDesign、Material 3 八套设计体系的 Token 与组件库，并提供浏览器无限画布。" +
				"推荐流程：① project_create 建项目（已有项目用 project_list 查 ID）→ " +
				"② design_artboard 定画板尺寸规范（iPhone 393×852 / Web 1440×900 等）→ " +
				"③ design_systems / design_tokens / design_components / design_search 查阅规范 → " +
				"④ design_preview（组件组合，需带 artboard）或 design_screen（login/settings/feed/profile 整页）摆上画布 → " +
				"⑤ design_audit 走查（含字体授权、图标一致性、尺寸规范）→ canvas_arrange 排版 → " +
				"canvas_screenshot 回看 → canvas_export 导出独立 HTML。" +
				"硬规则：设计稿必须归属于已存在的项目 —— 任何新增设计稿的工具（design_preview / design_screen / " +
				"canvas_add / design_audit / design_derive / design_compare）都必须传 project；没有项目就先 project_create，" +
				"否则会返回 project_required。" +
				"硬规则：图标只能用 design_icons 的 SVG，禁止 emoji 与 ▾ › ✕ ◉ 等字符；" +
				"字体必须来自 design_fonts 中 Commercial=true 的条目；每个预览都要声明画板。" +
				"画布 URL 通过 canvas_url 获取；截图需要画布已在浏览器中打开。",
		})
	registerTools(server, d)
	registerCatalogTools(server)
	registerProjectTools(server, d)
	return server
}

func registerTools(server *mcp.Server, d Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_systems",
		Description: "列出全部内置设计体系（id/名称/厂商/风格要点/主题/关键词）。",
	}, systemsTool())

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_tokens",
		Description: "获取某设计体系的 Design Token：颜色/字型/间距/圆角/阴影/动效，支持主题与分类过滤。",
	}, tokensTool())

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_components",
		Description: "列出某体系的组件清单及用法守则；指定 component 可获取单组件详情与 HTML 片段。",
	}, componentsTool())

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_preview",
		Description: "用某设计体系把若干组件渲染成一张真实 HTML+CSS 预览页；默认自动摆上无限画布并返回画布地址。",
	}, previewTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_add",
		Description: "向无限画布添加任意 HTML 预览卡片（Agent 自绘的页面/组件/便签）。不指定坐标时自动布局。",
	}, addTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_list",
		Description: "列出画布上的节点（标题/体系/类型/坐标/尺寸/标签），可按体系或标签过滤。",
	}, listTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_move",
		Description: "移动画布上的某个节点到指定坐标。",
	}, moveTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_remove",
		Description: "删除画布上的某个节点。",
	}, removeTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_clear",
		Description: "清空画布全部节点（需 confirm=true）。",
	}, clearTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_screenshot",
		Description: "对画布（或指定节点）截图并返回 PNG。需要画布页面已在浏览器打开；8 秒内无页面响应则报 capture_timeout。",
	}, screenshotTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_url",
		Description: "获取无限画布的浏览器访问地址与当前节点数。",
	}, urlTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_compare",
		Description: "对比两套设计体系的 Token（颜色/字型/间距/圆角/阴影/动效）；可选生成一张并排对比板节点摆上画布。",
	}, compareTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_screen",
		Description: "按内置模板（login/settings/feed/profile）用某体系拼装一张真实整页预览，默认自动摆上画布。",
	}, screenTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_search",
		Description: "跨全部体系搜索：体系/组件/Token 名称与说明，返回命中清单。",
	}, searchTool())

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_arrange",
		Description: "把画布节点自动排版成整齐网格（按体系分组排序），适合多体系对比场景。",
	}, arrangeTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_restore",
		Description: "恢复最近一次 canvas_clear 清空的节点（仅内存快照，重启后失效）。",
	}, restoreTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_export",
		Description: "把画布全部节点导出为一张独立 HTML 板（按坐标定位、iframe 还原预览），返回文件路径。",
	}, exportTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_derive",
		Description: "从品牌主色派生自定义设计体系：10 阶色板 + 语义色 + 中性灰（结构继承基底体系），持久化并注册，可直接用于全部设计工具。",
	}, deriveTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_register",
		Description: "注册自定义设计体系：传入 Token JSON（结构缺省字段从基底体系兜底），持久化并注册。",
	}, registerTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "design_audit",
		Description: "设计走查：WCAG 对比度、字号下限、圆角/间距阶梯、状态色完备性，产出评分与报告节点。",
	}, auditTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_connect",
		Description: "在画布两个节点之间画连线（SVG 贝塞尔，可带标签），表达页面流向。",
	}, connectTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "canvas_group",
		Description: "创建分组框：自动包住成员节点的边界（垫底虚线框），用于分区与叙事。",
	}, groupTool(d))
}

// fail 构造带稳定错误码的 isError 结果（不返回 error，避免丢失结构化错误信息）。
// 注意：SDK 会对 isError 结果也做输出 schema 校验，O 的切片/map 字段需非 nil，
// 此类场景请改用 failWith 传入可校验的零值结构。
func fail[O any](code, msg, hint string) (*mcp.CallToolResult, O, error) {
	var zero O
	return failWith(zero, code, msg, hint)
}

func auditStatusText(s string) string {
	switch s {
	case "pass":
		return "通过"
	case "warn":
		return "警告"
	}
	return "失败"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func failWith[O any](out O, code, msg, hint string) (*mcp.CallToolResult, O, error) {
	text := fmt.Sprintf("[ERROR %s] %s", code, msg)
	if hint != "" {
		text += "\n提示: " + hint
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// ---------------- design_systems ----------------

type systemSummary struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Vendor     string   `json:"vendor"`
	Family     string   `json:"family"`
	Summary    string   `json:"summary"`
	Keywords   []string `json:"keywords,omitempty"`
	Themes     []string `json:"themes"`
	Components int      `json:"component_count"`
}

type systemsOut struct {
	Systems []systemSummary `json:"systems"`
}

func systemsTool() mcp.ToolHandlerFor[struct{}, systemsOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, systemsOut, error) {
		var out systemsOut
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("共 %d 套设计体系：\n", len(design.Systems())))
		for _, sys := range design.Systems() {
			s := systemSummary{
				ID: sys.ID, Name: sys.Name, Vendor: sys.Vendor, Family: sys.Family,
				Summary: sys.Style.Summary, Keywords: sys.Style.Keywords,
				Themes: sys.Themes, Components: len(design.Components(sys)),
			}
			out.Systems = append(out.Systems, s)
			sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", sys.ID, sys.Name, sys.Style.Summary))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), out, nil
	}
}

// ---------------- design_tokens ----------------

type tokensIn struct {
	System   string `json:"system" jsonschema:"设计体系 ID（design_systems 返回的 id，如 element）"`
	Theme    string `json:"theme,omitempty" jsonschema:"主题: light|dark，默认 light"`
	Category string `json:"category,omitempty" jsonschema:"分类过滤: all|color|type|spacing|radius|shadow|motion，默认 all"`
}

type tokensOut struct {
	System  string         `json:"system"`
	Theme   string         `json:"theme"`
	Grid    int            `json:"grid"`
	Content map[string]any `json:"content"`
}

func tokensTool() mcp.ToolHandlerFor[tokensIn, tokensOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in tokensIn) (*mcp.CallToolResult, tokensOut, error) {
		sys, ok := design.GetSystem(design.SanitizeID(in.System))
		if !ok {
			return failWith[tokensOut](tokensOut{System: design.SanitizeID(in.System), Content: map[string]any{}},
				"unknown_system", "未知设计体系 "+in.System,
				"先调用 design_systems 查看可用体系")
		}
		theme := design.ThemeKey(design.SanitizeID(in.Theme))
		if theme == "" {
			theme = design.ThemeLight
		}
		if !sys.HasTheme(theme) {
			return failWith[tokensOut](tokensOut{System: sys.ID, Content: map[string]any{}},
				"unknown_theme",
				fmt.Sprintf("体系 %s 不支持主题 %s（可用: %s）", sys.ID, theme, strings.Join(sys.Themes, "/")), "")
		}
		tokens, eff := sys.TokensFor(theme)
		category := design.SanitizeID(in.Category)
		if category == "" {
			category = "all"
		}
		content := map[string]any{}
		switch category {
		case "all":
			content["colors"] = tokens.Colors
			content["type"] = tokens.Type
			content["spacing"] = tokens.Spacing
			content["radius"] = tokens.Radius
			content["shadows"] = tokens.Shadows
			content["motion"] = tokens.Motion
		case "color":
			content["colors"] = tokens.Colors
		case "type":
			content["type"] = tokens.Type
		case "spacing":
			content["spacing"] = tokens.Spacing
		case "radius":
			content["radius"] = tokens.Radius
		case "shadow":
			content["shadows"] = tokens.Shadows
		case "motion":
			content["motion"] = tokens.Motion
		default:
			return failWith[tokensOut](tokensOut{System: sys.ID, Theme: string(eff), Content: map[string]any{}},
				"unknown_category", "未知分类 "+in.Category,
				"all|color|type|spacing|radius|shadow|motion")
		}
		out := tokensOut{System: sys.ID, Theme: string(eff), Grid: tokens.Grid, Content: content}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s（%s 主题）Token [%s]：\n", sys.Name, eff, category)
		if colors, ok := content["colors"].(map[string]design.Color); ok {
			sb.WriteString(fmt.Sprintf("颜色 %d 项；", len(colors)))
		}
		if sp, ok := content["spacing"].(map[string]int); ok {
			sb.WriteString(fmt.Sprintf("间距 %d 项；", len(sp)))
		}
		if rd, ok := content["radius"].(map[string]int); ok {
			sb.WriteString(fmt.Sprintf("圆角 %d 项；", len(rd)))
		}
		sb.WriteString("栅格基数 " + fmt.Sprint(tokens.Grid) + "px")
		return textResult(sb.String()), out, nil
	}
}

// ---------------- design_components ----------------

type componentsIn struct {
	System    string `json:"system" jsonschema:"设计体系 ID"`
	Component string `json:"component,omitempty" jsonschema:"可选，单个组件 ID；提供时返回详情与 HTML 片段"`
}

type componentsOut struct {
	System     string             `json:"system"`
	Components []design.Component `json:"components,omitempty"`
	HTML       string             `json:"html,omitempty"`
}

func componentsTool() mcp.ToolHandlerFor[componentsIn, componentsOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in componentsIn) (*mcp.CallToolResult, componentsOut, error) {
		sys, ok := design.GetSystem(design.SanitizeID(in.System))
		if !ok {
			return failWith[componentsOut](componentsOut{System: design.SanitizeID(in.System), Components: []design.Component{}},
				"unknown_system", "未知设计体系 "+in.System, "")
		}
		if in.Component != "" {
			comp, ok := design.GetComponent(design.SanitizeID(in.Component))
			if !ok {
				return failWith[componentsOut](componentsOut{System: sys.ID, Components: []design.Component{}},
					"unknown_component", "未知组件 "+in.Component,
					"去掉 component 参数可列出全部组件")
			}
			html, err := design.RenderComponent(comp.ID)
			if err != nil {
				return failWith[componentsOut](componentsOut{System: sys.ID, Components: []design.Component{}},
					"render_failed", err.Error(), "")
			}
			var sb strings.Builder
			sb.WriteString(comp.Name + " (" + comp.ID + ")：" + comp.Description + "\n变体: " +
				strings.Join(comp.Variants, ", ") + "\n用法:\n- " + strings.Join(comp.Usage, "\n- ") +
				"\nHTML 片段:\n" + html)
			return textResult(sb.String()),
				componentsOut{System: sys.ID, Components: []design.Component{comp}, HTML: html}, nil
		}
		comps := design.Components(sys)
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("%s 共 %d 个组件：\n", sys.Name, len(comps)))
		for _, c := range comps {
			sb.WriteString(fmt.Sprintf("- %s (%s/%s): %s\n", c.ID, c.Name, c.Category, c.Description))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")),
			componentsOut{System: sys.ID, Components: comps}, nil
	}
}

// ---------------- design_preview ----------------

type previewIn struct {
	Project     string   `json:"project,omitempty" jsonschema:"项目 ID（必填；没有项目先 project_create，或用 project_list 选一个）"`
	System      string   `json:"system" jsonschema:"设计体系 ID"`
	Components  []string `json:"components" jsonschema:"组件 ID 列表（顺序即展示顺序），如 [\"navbar\",\"list\",\"tabbar\"]"`
	Title       string   `json:"title,omitempty" jsonschema:"预览标题，默认「体系名 组件预览」"`
	Theme       string   `json:"theme,omitempty" jsonschema:"light|dark，默认 light"`
	Artboard    string   `json:"artboard,omitempty" jsonschema:"画板 ID（尺寸规范），如 iphone-15 / web-1440；默认按体系 Family 取 design_artboard 的默认值"`
	AddToCanvas *bool    `json:"add_to_canvas,omitempty" jsonschema:"是否摆上画布，默认 true"`
}

type previewOut struct {
	System     string   `json:"system"`
	Theme      string   `json:"theme"`
	Artboard   string   `json:"artboard"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	Missing    []string `json:"missing,omitempty"`
	NodeID     string   `json:"node_id,omitempty"`
	CanvasURL  string   `json:"canvas_url,omitempty"`
	ExportPath string   `json:"export_path,omitempty"`
	HTMLBytes  int      `json:"html_bytes"`
}

func previewTool(d Deps) mcp.ToolHandlerFor[previewIn, previewOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in previewIn) (*mcp.CallToolResult, previewOut, error) {
		if perr := checkProject(d, in.Project); perr != nil {
			return projectFail[previewOut](previewOut{System: design.SanitizeID(in.System)}, perr, in.Project)
		}
		res, err := design.Compose(design.ComposeInput{
			SystemID:   design.SanitizeID(in.System),
			Components: in.Components,
			Title:      in.Title,
			Theme:      design.ThemeKey(design.SanitizeID(in.Theme)),
			Artboard:   in.Artboard,
		})
		if err != nil {
			return fail[previewOut]("compose_failed", err.Error(),
				"用 design_systems / design_components 确认体系与组件 ID；用 design_artboard 确认画板 ID")
		}
		out := previewOut{
			System: res.SystemID, Theme: string(res.Theme), Artboard: res.Artboard,
			Width: res.Width, Height: res.Height, Missing: res.Missing, HTMLBytes: len(res.HTML),
		}
		add := true
		if in.AddToCanvas != nil {
			add = *in.AddToCanvas
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "已生成 %s（%s）预览：%d 组件，画板 %s，%dx%d，HTML %d 字节",
			res.SystemID, res.Theme, len(in.Components)-len(res.Missing), res.Artboard, res.Width, res.Height, len(res.HTML))
		if add {
			node, aerr := d.Store.Add(canvas.Node{
				ProjectID: in.Project,
				Title:     nodeTitle(in.Title, res.SystemID), System: res.SystemID,
				Kind: canvas.KindComponent, HTML: res.HTML, Artboard: res.Artboard,
				Width: res.Width, Height: res.Height,
				Tags: append([]string{res.SystemID, string(res.Theme), "artboard:" + res.Artboard, "project:" + in.Project}, in.Components...),
			})
			if aerr != nil {
				return projectFail[previewOut](out, aerr, in.Project)
			}
			out.NodeID = node.ID
			out.CanvasURL = d.BaseURL()
			path, perr := exportHTML(d.DataDir, node.ID, res.HTML)
			if perr == nil {
				out.ExportPath = path
			}
			fmt.Fprintf(&sb, "\n已摆上画布：节点 %s（%s）", node.ID, out.CanvasURL)
			if out.ExportPath != "" {
				fmt.Fprintf(&sb, "\nHTML 文件: %s", out.ExportPath)
			}
			if len(res.Missing) > 0 {
				fmt.Fprintf(&sb, "\n[WARN] 无效组件已跳过: %s", strings.Join(res.Missing, ", "))
			}
		}
		return textResult(sb.String()), out, nil
	}
}

func nodeTitle(title, systemID string) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	return systemID + " 组件预览"
}

func exportHTML(dataDir, nodeID, html string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("no data dir")
	}
	dir := filepath.Join(dataDir, "exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, nodeID+".html")
	if err := os.WriteFile(p, []byte(html), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// ---------------- canvas_add ----------------

type canvasAddIn struct {
	Project  string   `json:"project,omitempty" jsonschema:"项目 ID（必填；没有项目先 project_create，或用 project_list 选一个）"`
	Title    string   `json:"title" jsonschema:"节点标题"`
	HTML     string   `json:"html" jsonschema:"完整 HTML 文档或片段（画布用沙箱 iframe 渲染）"`
	Kind     string   `json:"kind,omitempty" jsonschema:"screen|component|palette|note，默认 note"`
	System   string   `json:"system,omitempty" jsonschema:"关联的设计体系 ID（可选）"`
	Artboard string   `json:"artboard,omitempty" jsonschema:"画板 ID（尺寸规范）；给出时未指定 width/height 则取画板尺寸"`
	Width    int      `json:"width,omitempty" jsonschema:"渲染宽度；默认取画板宽度，无画板时 420"`
	Height   int      `json:"height,omitempty" jsonschema:"渲染高度；默认取画板高度，无画板时 320"`
	X        *float64 `json:"x,omitempty" jsonschema:"画布 X 坐标；与 y 一起提供则不自动布局"`
	Y        *float64 `json:"y,omitempty" jsonschema:"画布 Y 坐标"`
	Tags     []string `json:"tags,omitempty" jsonschema:"标签"`
}

type canvasAddOut struct {
	Node      canvas.Node `json:"node"`
	CanvasURL string      `json:"canvas_url"`
}

func addTool(d Deps) mcp.ToolHandlerFor[canvasAddIn, canvasAddOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in canvasAddIn) (*mcp.CallToolResult, canvasAddOut, error) {
		if strings.TrimSpace(in.HTML) == "" {
			return fail[canvasAddOut]("bad_request", "html 不能为空", "提供完整 HTML 文档或片段")
		}
		if in.Kind != "" && !canvas.ValidKind(in.Kind) {
			return fail[canvasAddOut]("bad_request", "kind 须为 screen|component|palette|note", "")
		}
		tags := in.Tags
		if in.Artboard != "" {
			ab, ok := design.GetArtboard(in.Artboard)
			if !ok {
				return fail[canvasAddOut]("unknown_artboard", "未知画板 "+in.Artboard,
					"用 design_artboard 查看可用画板")
			}
			if in.Width <= 0 {
				in.Width = ab.Width
			}
			if in.Height <= 0 {
				in.Height = ab.Height
			}
			tags = append(append([]string{}, tags...), "artboard:"+ab.ID)
		}
		if perr := checkProject(d, in.Project); perr != nil {
			return projectFail[canvasAddOut](canvasAddOut{}, perr, in.Project)
		}
		n := canvas.Node{
			ProjectID: in.Project,
			Title:     in.Title, Kind: in.Kind, System: in.System, HTML: in.HTML,
			Artboard: in.Artboard, Tags: tags,
			Width: in.Width, Height: in.Height,
		}
		if in.X != nil && in.Y != nil {
			n.X, n.Y = *in.X, *in.Y
		}
		created, aerr := d.Store.Add(n)
		if aerr != nil {
			return projectFail[canvasAddOut](canvasAddOut{}, aerr, in.Project)
		}
		out := canvasAddOut{Node: created, CanvasURL: d.BaseURL()}
		return textResult(fmt.Sprintf("已添加节点 %s「%s」(%dx%d @ %.0f,%.0f)。画布: %s",
				created.ID, created.Title, created.Width, created.Height, created.X, created.Y, out.CanvasURL)),
			out, nil
	}
}

// ---------------- canvas_list / move / remove / clear ----------------

type canvasListIn struct {
	Project string `json:"project,omitempty" jsonschema:"按项目过滤（可选）"`
	System  string `json:"system,omitempty" jsonschema:"按体系 ID 过滤"`
	Tag     string `json:"tag,omitempty" jsonschema:"按标签过滤"`
}

type nodeSummary struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Title     string   `json:"title"`
	System    string   `json:"system,omitempty"`
	Kind      string   `json:"kind"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Tags      []string `json:"tags,omitempty"`
}

type canvasListOut struct {
	Count int           `json:"count"`
	Nodes []nodeSummary `json:"nodes"`
}

func listTool(d Deps) mcp.ToolHandlerFor[canvasListIn, canvasListOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in canvasListIn) (*mcp.CallToolResult, canvasListOut, error) {
		out := canvasListOut{Nodes: []nodeSummary{}}
		var sb strings.Builder
		for _, n := range d.Store.ListByProject(in.Project) {
			if in.System != "" && n.System != in.System {
				continue
			}
			if in.Tag != "" && !containsTag(n.Tags, in.Tag) {
				continue
			}
			out.Nodes = append(out.Nodes, nodeSummary{
				ID: n.ID, ProjectID: n.ProjectID, Title: n.Title, System: n.System, Kind: n.Kind,
				X: n.X, Y: n.Y, Width: n.Width, Height: n.Height, Tags: n.Tags,
			})
			sb.WriteString(fmt.Sprintf("- %s 「%s」 %s/%s (%dx%d @ %.0f,%.0f)\n",
				n.ID, n.Title, n.System, n.Kind, n.Width, n.Height, n.X, n.Y))
		}
		out.Count = len(out.Nodes)
		if out.Count == 0 {
			return textResult("画布上没有匹配的节点。"), out, nil
		}
		return textResult(fmt.Sprintf("共 %d 个节点：\n%s", out.Count, strings.TrimRight(sb.String(), "\n"))), out, nil
	}
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

type canvasMoveIn struct {
	ID string  `json:"id" jsonschema:"节点 ID"`
	X  float64 `json:"x" jsonschema:"目标 X"`
	Y  float64 `json:"y" jsonschema:"目标 Y"`
}

type canvasMoveOut struct {
	Node canvas.Node `json:"node"`
}

func moveTool(d Deps) mcp.ToolHandlerFor[canvasMoveIn, canvasMoveOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in canvasMoveIn) (*mcp.CallToolResult, canvasMoveOut, error) {
		n, err := d.Store.Update(in.ID, func(nn *canvas.Node) { nn.X, nn.Y = in.X, in.Y })
		if err == canvas.ErrNotFound {
			return fail[canvasMoveOut]("not_found", "节点 "+in.ID+" 不存在", "用 canvas_list 查看现有节点")
		}
		if err != nil {
			return fail[canvasMoveOut]("internal", err.Error(), "")
		}
		return textResult(fmt.Sprintf("已移动 %s 到 (%.0f, %.0f)。", n.ID, n.X, n.Y)), canvasMoveOut{Node: n}, nil
	}
}

type canvasRemoveIn struct {
	ID string `json:"id" jsonschema:"节点 ID"`
}

func removeTool(d Deps) mcp.ToolHandlerFor[canvasRemoveIn, struct{}] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in canvasRemoveIn) (*mcp.CallToolResult, struct{}, error) {
		ok, err := d.Store.Remove(in.ID)
		if err != nil {
			return fail[struct{}]("internal", err.Error(), "")
		}
		if !ok {
			return fail[struct{}]("not_found", "节点 "+in.ID+" 不存在", "")
		}
		return textResult("已删除节点 " + in.ID + "。"), struct{}{}, nil
	}
}

type canvasClearIn struct {
	Project string `json:"project,omitempty" jsonschema:"项目 ID（必填；只清空该项目）"`
	Confirm *bool  `json:"confirm,omitempty" jsonschema:"必须显式传 true 才会清空"`
}

func clearTool(d Deps) mcp.ToolHandlerFor[canvasClearIn, struct{}] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in canvasClearIn) (*mcp.CallToolResult, struct{}, error) {
		if in.Confirm == nil || !*in.Confirm {
			return fail[struct{}]("confirm_required", "清空画布需要 confirm=true", "该操作不可撤销，请先 canvas_list 确认")
		}
		if perr := checkProject(d, in.Project); perr != nil {
			return projectFail[struct{}](struct{}{}, perr, in.Project)
		}
		n, cerr := d.Store.Clear(in.Project)
		if cerr != nil {
			return projectFail[struct{}](struct{}{}, cerr, in.Project)
		}
		return textResult(fmt.Sprintf("已清空项目 %s 的 %d 个节点（可用 canvas_restore 恢复）。", in.Project, n)), struct{}{}, nil
	}
}

// ---------------- canvas_screenshot ----------------

type screenshotIn struct {
	NodeID string  `json:"node_id,omitempty" jsonschema:"可选；提供则只截该节点，否则截全画布"`
	Scale  float64 `json:"scale,omitempty" jsonschema:"缩放倍率，默认 1；2 表示 2x 高清"`
}

type screenshotOut struct {
	OK     bool   `json:"ok"`
	NodeID string `json:"node_id,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

func screenshotTool(d Deps) mcp.ToolHandlerFor[screenshotIn, screenshotOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, screenshotOut, error) {
		scale := in.Scale
		if scale <= 0 {
			scale = 1
		}
		if scale > 4 {
			scale = 4
		}
		var target canvas.Node
		if in.NodeID != "" {
			n, err := d.Store.Get(in.NodeID)
			if err == canvas.ErrNotFound {
				return fail[screenshotOut]("not_found", "节点 "+in.NodeID+" 不存在", "")
			}
			if err != nil {
				return fail[screenshotOut]("internal", err.Error(), "")
			}
			target = n
		}
		task := d.Tasks.Create(in.NodeID, scale)
		res := d.Tasks.Wait(ctx, task.ID)
		if res == nil {
			return fail[screenshotOut]("capture_timeout", "画布页面未在时限内回传截图",
				"请先在浏览器打开画布 "+d.BaseURL()+"，再重试")
		}
		if res.Error != "" {
			return fail[screenshotOut]("capture_failed", res.Error, "检查节点是否仍在画布上")
		}
		out := screenshotOut{OK: true, NodeID: in.NodeID}
		if in.NodeID != "" {
			out.Width = int(float64(target.Width) * scale)
			out.Height = int(float64(target.Height) * scale)
		}
		// ImageContent.Data 是原始字节（SDK 序列化为 base64）；任务队列回传的是
		// base64 文本，这里必须先解码，否则客户端会拿到双重编码。
		rawPNG, err := base64.StdEncoding.DecodeString(res.PNGBase64)
		if err != nil {
			return fail[screenshotOut]("capture_failed", "截图数据解码失败: "+err.Error(), "")
		}
		img := &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("截图完成（%s）。",
					map[bool]string{true: "节点 " + in.NodeID, false: "全画布"}[in.NodeID != ""])},
				&mcp.ImageContent{Data: rawPNG, MIMEType: "image/png"},
			},
		}
		return img, out, nil
	}
}

// ---------------- canvas_url ----------------

type canvasURLOut struct {
	URL       string `json:"url"`
	NodeCount int    `json:"node_count"`
}

func urlTool(d Deps) mcp.ToolHandlerFor[struct{}, canvasURLOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, canvasURLOut, error) {
		url := d.BaseURL()
		out := canvasURLOut{URL: url, NodeCount: len(d.Store.List())}
		return textResult(fmt.Sprintf("无限画布地址: %s（浏览器打开后可用 canvas_screenshot 截图）", url)), out, nil
	}
}

// ---------------- design_compare ----------------

type compareIn struct {
	Project     string `json:"project,omitempty" jsonschema:"生成对比板节点时必填"`
	SystemA     string `json:"system_a" jsonschema:"体系 A 的 ID"`
	SystemB     string `json:"system_b" jsonschema:"体系 B 的 ID"`
	Theme       string `json:"theme,omitempty" jsonschema:"light|dark，默认 light"`
	AddToCanvas *bool  `json:"add_to_canvas,omitempty" jsonschema:"是否生成对比板节点摆上画布，默认 false"`
}

type compareOut struct {
	A       string                `json:"a"`
	B       string                `json:"b"`
	Theme   string                `json:"theme"`
	Colors  design.CompareSection `json:"colors"`
	Spacing design.CompareSection `json:"spacing"`
	Radius  design.CompareSection `json:"radius"`
	Shadows design.CompareSection `json:"shadows"`
	Motion  design.CompareSection `json:"motion"`
	Type    []design.TypeChange   `json:"type"`
	NodeID  string                `json:"node_id,omitempty"`
}

func compareTool(d Deps) mcp.ToolHandlerFor[compareIn, compareOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in compareIn) (*mcp.CallToolResult, compareOut, error) {
		theme := design.ThemeKey(design.SanitizeID(in.Theme))
		if theme == "" {
			theme = design.ThemeLight
		}
		res, err := design.Compare(in.SystemA, in.SystemB, theme)
		if err != nil {
			return failWith[compareOut](compareOut{A: in.SystemA, B: in.SystemB, Theme: string(theme)},
				"compare_failed", err.Error(), "用 design_systems 查看可用体系")
		}
		out := compareOut{
			A: res.A, B: res.B, Theme: res.Theme, Colors: res.Colors,
			Spacing: res.Spacing, Radius: res.Radius, Shadows: res.Shadows,
			Motion: res.Motion, Type: res.Type,
		}
		text := res.Summary()
		if in.AddToCanvas != nil && *in.AddToCanvas {
			if perr := checkProject(d, in.Project); perr != nil {
				return projectFail[compareOut](out, perr, in.Project)
			}
			boardHTML, _, berr := design.CompareBoardHTML(res.A, res.B, theme)
			if berr == nil {
				node, aerr := d.Store.Add(canvas.Node{
					ProjectID: in.Project,
					Title:     res.A + " vs " + res.B, System: res.A, Kind: canvas.KindPalette,
					HTML: boardHTML, Width: 420, Height: 460,
					Tags: []string{"compare", res.A, res.B, string(theme), "project:" + in.Project},
				})
				if aerr != nil {
					return projectFail[compareOut](out, aerr, in.Project)
				}
				out.NodeID = node.ID
				text += fmt.Sprintf("\n对比板已摆上画布：节点 %s（%s）", node.ID, d.BaseURL())
			}
		}
		return textResult(text), out, nil
	}
}

// ---------------- design_screen ----------------

type screenIn struct {
	Project     string `json:"project,omitempty" jsonschema:"项目 ID（必填；没有项目先 project_create，或用 project_list 选一个）"`
	System      string `json:"system" jsonschema:"设计体系 ID"`
	Template    string `json:"template" jsonschema:"模板: login|settings|feed|profile"`
	Title       string `json:"title,omitempty" jsonschema:"预览标题，默认「体系名 · 模板名」"`
	Theme       string `json:"theme,omitempty" jsonschema:"light|dark，默认 light"`
	AppName     string `json:"app_name,omitempty" jsonschema:"品牌块/顶栏应用名，默认「我的应用」"`
	Artboard    string `json:"artboard,omitempty" jsonschema:"画板 ID（尺寸规范），默认按体系 Family 取 design_artboard 的默认值"`
	AddToCanvas *bool  `json:"add_to_canvas,omitempty" jsonschema:"默认 true"`
}

type screenOut struct {
	System    string   `json:"system"`
	Template  string   `json:"template"`
	Theme     string   `json:"theme"`
	Artboard  string   `json:"artboard"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Uses      []string `json:"uses,omitempty"`
	NodeID    string   `json:"node_id,omitempty"`
	CanvasURL string   `json:"canvas_url,omitempty"`
}

func screenTool(d Deps) mcp.ToolHandlerFor[screenIn, screenOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in screenIn) (*mcp.CallToolResult, screenOut, error) {
		if perr := checkProject(d, in.Project); perr != nil {
			return projectFail[screenOut](screenOut{System: in.System, Template: in.Template}, perr, in.Project)
		}
		res, err := design.ComposeScreen(design.ScreenInput{
			SystemID: design.SanitizeID(in.System), Template: in.Template,
			Theme: design.ThemeKey(design.SanitizeID(in.Theme)), Title: in.Title,
			AppName: in.AppName, Artboard: in.Artboard,
		})
		if err != nil {
			return failWith[screenOut](screenOut{System: in.System, Template: in.Template},
				"screen_failed", err.Error(), "模板: login|settings|feed|profile；画板用 design_artboard 查询")
		}
		tpl, _ := design.GetScreenTemplate(in.Template)
		out := screenOut{System: res.SystemID, Template: tpl.ID, Theme: string(res.Theme),
			Artboard: res.Artboard, Width: res.Width, Height: res.Height, Uses: tpl.Components}
		title := in.Title
		if title == "" {
			title = res.SystemID + " · " + tpl.Name
		}
		add := true
		if in.AddToCanvas != nil {
			add = *in.AddToCanvas
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "已生成 %s（%s）「%s」整页预览：画板 %s，%dx%d，用到组件 %s",
			res.SystemID, res.Theme, tpl.Name, res.Artboard, res.Width, res.Height, strings.Join(tpl.Components, "/"))
		if add {
			node, aerr := d.Store.Add(canvas.Node{
				ProjectID: in.Project,
				Title:     title, System: res.SystemID, Kind: canvas.KindScreen, HTML: res.HTML,
				Artboard: res.Artboard, Width: res.Width, Height: res.Height,
				Tags: append([]string{res.SystemID, string(res.Theme), tpl.ID, "artboard:" + res.Artboard, "project:" + in.Project}, tpl.Components...),
			})
			if aerr != nil {
				return projectFail[screenOut](out, aerr, in.Project)
			}
			out.NodeID = node.ID
			out.CanvasURL = d.BaseURL()
			fmt.Fprintf(&sb, "\n已摆上画布：节点 %s（%s）", node.ID, out.CanvasURL)
		}
		return textResult(sb.String()), out, nil
	}
}

// ---------------- design_search ----------------

type searchIn struct {
	Query string `json:"query" jsonschema:"关键词，如「button」「抖音」「primary」「radius」"`
}

type searchOut struct {
	Count int                `json:"count"`
	Hits  []design.SearchHit `json:"hits"`
}

func searchTool() mcp.ToolHandlerFor[searchIn, searchOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		hits := design.Search(in.Query)
		out := searchOut{Count: len(hits), Hits: hits}
		if len(hits) == 0 {
			return textResult(fmt.Sprintf("没有与 %q 相关的结果。", in.Query)), out, nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "共 %d 条命中：\n", len(hits))
		for i, h := range hits {
			if i >= 20 {
				fmt.Fprintf(&sb, "… 其余 %d 条见结构化输出\n", len(hits)-20)
				break
			}
			kind := map[string]string{"system": "体系", "component": "组件", "token": "Token"}[h.Kind]
			sb.WriteString(fmt.Sprintf("%d. [%s] %s — %s (%s)\n", i+1, kind, h.Title, h.Snippet, h.ID))
		}
		return textResult(strings.TrimRight(sb.String(), "\n")), out, nil
	}
}

// ---------------- canvas_arrange ----------------

type arrangeIn struct {
	Project string `json:"project,omitempty" jsonschema:"只排版该项目；留空表示全部"`
	Columns int    `json:"columns,omitempty" jsonschema:"每行节点数，默认按 sqrt(节点数) 取整；范围 1-8"`
}

type arrangeOut struct {
	Moved   int      `json:"moved"`
	Columns int      `json:"columns"`
	Rows    int      `json:"rows"`
	Order   []string `json:"order,omitempty"`
}

func arrangeTool(d Deps) mcp.ToolHandlerFor[arrangeIn, arrangeOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in arrangeIn) (*mcp.CallToolResult, arrangeOut, error) {
		nodes := d.Store.ListByProject(in.Project)
		if len(nodes) == 0 {
			return textResult("该项目下没有节点，无需排版。"), arrangeOut{Columns: 0, Order: []string{}}, nil
		}
		// 排序：体系 -> 种类 -> 创建时间（同体系节点聚在一起）
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].System != nodes[j].System {
				return nodes[i].System < nodes[j].System
			}
			if nodes[i].Kind != nodes[j].Kind {
				return nodes[i].Kind < nodes[j].Kind
			}
			return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
		})
		cols := in.Columns
		if cols <= 0 {
			cols = int(math.Sqrt(float64(len(nodes))))
		}
		if cols < 1 {
			cols = 1
		}
		if cols > 8 {
			cols = 8
		}
		const gap = 48
		colW := make([]float64, cols)
		rowH := make([]float64, (len(nodes)+cols-1)/cols)
		out := arrangeOut{Columns: cols, Order: make([]string, 0, len(nodes))}
		for i, n := range nodes {
			c, r := i%cols, i/cols
			x := 80.0
			for k := 0; k < c; k++ {
				x += colW[k] + gap
			}
			y := 80.0
			for k := 0; k < r; k++ {
				y += rowH[k] + gap
			}
			if cw := float64(n.Width); cw > colW[c] {
				colW[c] = cw
			}
			if ch := float64(n.Height); ch > rowH[r] {
				rowH[r] = ch
			}
			if _, err := d.Store.Update(n.ID, func(nn *canvas.Node) { nn.X, nn.Y = x, y }); err == nil {
				out.Moved++
				out.Order = append(out.Order, n.ID)
			}
		}
		out.Rows = (len(nodes) + cols - 1) / cols
		return textResult(fmt.Sprintf("已按 %d 列排版 %d 个节点（%d 行，体系分组优先）。", cols, out.Moved, out.Rows)), out, nil
	}
}

// ---------------- canvas_restore ----------------

type restoreOut struct {
	Restored  int    `json:"restored"`
	ClearedAt string `json:"cleared_at,omitempty"`
}

func restoreTool(d Deps) mcp.ToolHandlerFor[struct{}, restoreOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, restoreOut, error) {
		n, at, ok := d.Store.RestoreLastClear()
		if !ok {
			return textResult("没有可恢复的清空快照。"), restoreOut{}, nil
		}
		out := restoreOut{Restored: n, ClearedAt: at.Local().Format("2006-01-02 15:04:05")}
		return textResult(fmt.Sprintf("已恢复 %d 个节点（清空时间 %s）。", n, out.ClearedAt)), out, nil
	}
}

// ---------------- canvas_export ----------------

type exportIn struct {
	Project string `json:"project,omitempty" jsonschema:"只导出该项目；留空表示全部"`
	Title   string `json:"title,omitempty" jsonschema:"导出板标题，默认「adago-design 画布导出」"`
}

type exportOut struct {
	Path      string `json:"path"`
	NodeCount int    `json:"node_count"`
	Bytes     int    `json:"bytes"`
}

func exportTool(d Deps) mcp.ToolHandlerFor[exportIn, exportOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in exportIn) (*mcp.CallToolResult, exportOut, error) {
		nodes := d.Store.ListByProject(in.Project)
		if len(nodes) == 0 {
			return fail[exportOut]("empty_canvas", "该项目下没有可导出的内容", "先 design_preview 或 canvas_add 添加节点")
		}
		htmlDoc := canvas.ExportBoard(nodes, in.Title)
		name := fmt.Sprintf("board-%s.html", time.Now().Format("20060102-150405"))
		dir := filepath.Join(d.DataDir, "exports")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fail[exportOut]("internal", err.Error(), "")
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(htmlDoc), 0o644); err != nil {
			return fail[exportOut]("internal", err.Error(), "")
		}
		out := exportOut{Path: p, NodeCount: len(nodes), Bytes: len(htmlDoc)}
		return textResult(fmt.Sprintf("已导出 %d 个节点到 %s（%d 字节）。浏览器直接打开即可查看。", len(nodes), p, len(htmlDoc))), out, nil
	}
}

// ---------------- design_derive ----------------

type deriveIn struct {
	Project     string `json:"project,omitempty" jsonschema:"生成色板节点时必填"`
	Primary     string `json:"primary" jsonschema:"品牌主色，#RRGGBB 格式（如 #C8412B）"`
	Name        string `json:"name" jsonschema:"体系名称（如「山茶茶饮」）"`
	BaseSystem  string `json:"base_system" jsonschema:"结构基底体系 ID（颜色之外的 Token 继承它），如 volcano/element/douyin"`
	AddToCanvas *bool  `json:"add_to_canvas,omitempty" jsonschema:"是否生成色板预览节点摆上画布，默认 true"`
}

type deriveOut struct {
	SystemID   string   `json:"system_id"`
	Name       string   `json:"name"`
	BaseSystem string   `json:"base_system"`
	Primary    string   `json:"primary"`
	Palette    []string `json:"palette"`
	Themes     []string `json:"themes"`
	NodeID     string   `json:"node_id,omitempty"`
	CanvasURL  string   `json:"canvas_url,omitempty"`
}

func deriveTool(d Deps) mcp.ToolHandlerFor[deriveIn, deriveOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in deriveIn) (*mcp.CallToolResult, deriveOut, error) {
		sys, err := design.Derive(design.DeriveInput{
			Primary: in.Primary, Name: in.Name, BaseSystemID: in.BaseSystem,
		})
		if err != nil {
			return failWith[deriveOut](deriveOut{Primary: in.Primary, BaseSystem: in.BaseSystem},
				"derive_failed", err.Error(), "primary 用 #RRGGBB；base_system 用 design_systems 查询")
		}
		if err := design.SaveCustomSystem(sys, d.DataDir); err != nil {
			return failWith[deriveOut](deriveOut{SystemID: sys.ID},
				"persist_failed", err.Error(), "")
		}
		scale, _ := design.BrandPalette(in.Primary)
		out := deriveOut{
			SystemID: sys.ID, Name: sys.Name, BaseSystem: design.SanitizeID(in.BaseSystem),
			Primary: strings.ToUpper(in.Primary), Palette: scale, Themes: sys.Themes,
		}
		text := fmt.Sprintf("已派生品牌体系「%s」（%s，基底 %s）并持久化。ID: %s\n10 阶色板: %s",
			sys.Name, out.Primary, out.BaseSystem, sys.ID, strings.Join(scale, " "))
		add := true
		if in.AddToCanvas != nil {
			add = *in.AddToCanvas
		}
		if add {
			if perr := checkProject(d, in.Project); perr != nil {
				return projectFail[deriveOut](out, perr, in.Project)
			}
			boardHTML, _, berr := design.DeriveBoardHTML(sys, scale)
			if berr == nil {
				node, aerr := d.Store.Add(canvas.Node{
					ProjectID: in.Project,
					Title:     sys.Name + " · 品牌色板", System: sys.ID, Kind: canvas.KindPalette,
					HTML: boardHTML, Width: 420, Height: 480,
					Tags: []string{"derive", sys.ID, out.Primary, "project:" + in.Project},
				})
				if aerr != nil {
					return projectFail[deriveOut](out, aerr, in.Project)
				}
				out.NodeID = node.ID
				out.CanvasURL = d.BaseURL()
				text += fmt.Sprintf("\n色板已摆上画布：节点 %s（%s）", node.ID, out.CanvasURL)
			}
		}
		return textResult(text), out, nil
	}
}

// ---------------- design_register ----------------

type registerIn struct {
	Project     string `json:"project,omitempty" jsonschema:"生成验证节点时必填"`
	Name        string `json:"name" jsonschema:"体系名称"`
	TokensJSON  string `json:"tokens_json" jsonschema:"light 主题 Token 的 JSON（colors/type/spacing/radius/shadows/motion/grid 字段）"`
	DarkJSON    string `json:"dark_json,omitempty" jsonschema:"可选，dark 主题 Token JSON"`
	Family      string `json:"family,omitempty" jsonschema:"mobile|web|cross，默认继承 base_system"`
	BaseSystem  string `json:"base_system,omitempty" jsonschema:"结构兜底体系 ID，缺省字段从中复制，默认 volcano"`
	AddToCanvas *bool  `json:"add_to_canvas,omitempty" jsonschema:"默认 false"`
}

type registerOut struct {
	SystemID string   `json:"system_id"`
	Name     string   `json:"name"`
	Themes   []string `json:"themes"`
	NodeID   string   `json:"node_id,omitempty"`
}

func registerTool(d Deps) mcp.ToolHandlerFor[registerIn, registerOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in registerIn) (*mcp.CallToolResult, registerOut, error) {
		var light design.Tokens
		if err := json.Unmarshal([]byte(in.TokensJSON), &light); err != nil {
			return failWith[registerOut](registerOut{Name: in.Name}, "bad_tokens",
				"tokens_json 解析失败: "+err.Error(), "")
		}
		base, ok := design.GetSystem(design.SanitizeID(in.BaseSystem))
		if !ok {
			base, _ = design.GetSystem("volcano")
		}
		// 结构兜底：空字段从基底复制
		if light.Grid <= 0 {
			light.Grid = base.Light.Grid
		}
		if light.Type.Family == "" {
			light.Type = base.Light.Type
		}
		if len(light.Type.Levels) == 0 {
			light.Type.Levels = base.Light.Type.Levels
		}
		if len(light.Spacing) == 0 {
			light.Spacing = base.Light.Spacing
		}
		if len(light.Radius) == 0 {
			light.Radius = base.Light.Radius
		}
		if len(light.Shadows) == 0 {
			light.Shadows = base.Light.Shadows
		}
		if len(light.Motion) == 0 {
			light.Motion = base.Light.Motion
		}
		sys := &design.System{
			Name: in.Name, Vendor: "自定义", Family: firstNonEmpty(in.Family, base.Family),
			Themes: []string{"light"}, Light: light,
			Description: "自定义注册的设计体系。",
			Style:       design.StyleNotes{Summary: "自定义注册体系：" + in.Name},
		}
		if err := design.ValidateCustom(sys); err != nil {
			return failWith[registerOut](registerOut{Name: in.Name}, "invalid_tokens", err.Error(),
				"检查 colors 是否为 hex/rgba、字号与圆角是否越界")
		}
		if strings.TrimSpace(in.DarkJSON) != "" {
			var dark design.Tokens
			if err := json.Unmarshal([]byte(in.DarkJSON), &dark); err != nil {
				return failWith[registerOut](registerOut{Name: in.Name}, "bad_tokens",
					"dark_json 解析失败: "+err.Error(), "")
			}
			if dark.Grid <= 0 {
				dark.Grid = light.Grid
			}
			if len(dark.Type.Levels) == 0 {
				dark.Type = light.Type
			}
			sys.Dark = &dark
			sys.Themes = []string{"light", "dark"}
		}
		id := design.ClaimCustomID(in.Name, d.DataDir)
		sys.ID = id
		if err := design.SaveCustomSystem(sys, d.DataDir); err != nil {
			return failWith[registerOut](registerOut{Name: in.Name}, "persist_failed", err.Error(), "")
		}
		out := registerOut{SystemID: sys.ID, Name: sys.Name, Themes: sys.Themes}
		text := fmt.Sprintf("已注册自定义体系「%s」，ID: %s（主题: %s），已持久化，可直接用于 design_tokens/design_preview/design_screen。",
			sys.Name, sys.ID, strings.Join(sys.Themes, "/"))
		if in.AddToCanvas != nil && *in.AddToCanvas {
			if perr := checkProject(d, in.Project); perr != nil {
				return projectFail[registerOut](out, perr, in.Project)
			}
			res, err := design.Compose(design.ComposeInput{SystemID: sys.ID, Theme: design.ThemeLight,
				Components: []string{"button", "card", "input"}, Title: sys.Name + " · 注册验证"})
			if err == nil {
				node, aerr := d.Store.Add(canvas.Node{
					ProjectID: in.Project,
					Title:     sys.Name + " · 注册验证", System: sys.ID, Kind: canvas.KindComponent,
					HTML: res.HTML, Width: res.Width, Height: res.Height,
					Tags: []string{"register", sys.ID, "project:" + in.Project},
				})
				if aerr != nil {
					return projectFail[registerOut](out, aerr, in.Project)
				}
				out.NodeID = node.ID
				text += fmt.Sprintf("\n注册验证预览已摆上画布：节点 %s", node.ID)
			}
		}
		return textResult(text), out, nil
	}
}

// ---------------- design_audit ----------------

type auditIn struct {
	Project     string `json:"project,omitempty" jsonschema:"生成走查报告时必填"`
	System      string `json:"system" jsonschema:"体系 ID"`
	Theme       string `json:"theme,omitempty" jsonschema:"light|dark，默认 light"`
	AddToCanvas *bool  `json:"add_to_canvas,omitempty" jsonschema:"是否生成走查报告节点摆上画布，默认 true"`
}

type auditOut struct {
	System string              `json:"system"`
	Theme  string              `json:"theme"`
	Score  int                 `json:"score"`
	Passed int                 `json:"passed"`
	Warned int                 `json:"warned"`
	Failed int                 `json:"failed"`
	Checks []design.AuditCheck `json:"checks"`
	NodeID string              `json:"node_id,omitempty"`
}

func auditTool(d Deps) mcp.ToolHandlerFor[auditIn, auditOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in auditIn) (*mcp.CallToolResult, auditOut, error) {
		res, err := design.Audit(in.System, design.ThemeKey(design.SanitizeID(in.Theme)))
		if err != nil {
			return failWith[auditOut](auditOut{System: in.System}, "audit_failed", err.Error(),
				"用 design_systems 查看可用体系")
		}
		out := auditOut{System: res.System, Theme: res.Theme, Score: res.Score,
			Passed: res.Passed, Warned: res.Warned, Failed: res.Failed, Checks: res.Checks}
		text := res.Summary
		for _, c := range res.Checks {
			if c.Status != "pass" {
				text += fmt.Sprintf("\n[%s] %s: %s", auditStatusText(c.Status), c.Name, c.Detail)
			}
		}
		add := true
		if in.AddToCanvas != nil {
			add = *in.AddToCanvas
		}
		if add {
			if perr := checkProject(d, in.Project); perr != nil {
				return projectFail[auditOut](out, perr, in.Project)
			}
			node, aerr := d.Store.Add(canvas.Node{
				ProjectID: in.Project,
				Title:     res.System + " · 走查报告",
				System:    res.System,
				Kind:      canvas.KindPalette,
				HTML:      design.AuditBoardHTML(res),
				Width:     420, Height: 520,
				Tags: []string{"audit", res.System, res.Theme, fmt.Sprint(res.Score), "project:" + in.Project},
			})
			if aerr != nil {
				return projectFail[auditOut](out, aerr, in.Project)
			}
			out.NodeID = node.ID
			text += fmt.Sprintf("\n走查报告已摆上画布：节点 %s", node.ID)
		}
		return textResult(text), out, nil
	}
}

// ---------------- canvas_connect / canvas_group ----------------

type connectIn struct {
	From  string `json:"from" jsonschema:"源节点 ID"`
	To    string `json:"to" jsonschema:"目标节点 ID"`
	Label string `json:"label,omitempty" jsonschema:"连线标签（如「下一步」）"`
}

type connectOut struct {
	Edge canvas.Edge `json:"edge"`
}

func connectTool(d Deps) mcp.ToolHandlerFor[connectIn, connectOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in connectIn) (*mcp.CallToolResult, connectOut, error) {
		edge, err := d.Store.AddEdge(in.From, in.To, in.Label)
		switch {
		case err == canvas.ErrEdgeEndpoint:
			return failWith[connectOut](connectOut{}, "not_found", "连线端点节点不存在", "用 canvas_list 查看现有节点")
		case err == canvas.ErrEdgeDuplicate:
			return failWith[connectOut](connectOut{}, "edge_duplicate", "两节点间已存在连线", "")
		case err == canvas.ErrEdgeSelfLoop:
			return failWith[connectOut](connectOut{}, "bad_request", "连线不可自环", "")
		case err != nil:
			return failWith[connectOut](connectOut{}, "internal", err.Error(), "")
		}
		return textResult(fmt.Sprintf("已连线 %s → %s（%s）。", in.From, in.To, firstNonEmpty(edge.Label, "无标签"))),
			connectOut{Edge: edge}, nil
	}
}

type groupIn struct {
	Title   string   `json:"title" jsonschema:"分组框标题"`
	NodeIDs []string `json:"node_ids" jsonschema:"成员节点 ID 列表（框自动包住成员边界）"`
	Padding int      `json:"padding,omitempty" jsonschema:"内边距，默认 28"`
}

type groupOut struct {
	Node   canvas.Node `json:"node"`
	Member int         `json:"member"`
}

func groupTool(d Deps) mcp.ToolHandlerFor[groupIn, groupOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in groupIn) (*mcp.CallToolResult, groupOut, error) {
		if len(in.NodeIDs) == 0 {
			return failWith[groupOut](groupOut{}, "bad_request", "node_ids 不能为空", "")
		}
		minX, minY := 1e18, 1e18
		maxX, maxY := -1e18, -1e18
		member := 0
		project := ""
		for _, id := range in.NodeIDs {
			n, err := d.Store.Get(id)
			if err != nil {
				continue
			}
			// 分组框必须和成员同项目：从成员推导，跨项目直接拒绝
			if project == "" {
				project = n.ProjectID
			} else if project != n.ProjectID {
				return failWith[groupOut](groupOut{}, "cross_project",
					"成员节点分属不同项目，无法放同一个分组框", "按项目分别 canvas_group")
			}
			member++
			minX = math.Min(minX, n.X)
			minY = math.Min(minY, n.Y)
			maxX = math.Max(maxX, n.X+float64(n.Width))
			maxY = math.Max(maxY, n.Y+float64(n.Height))
		}
		if member == 0 {
			return failWith[groupOut](groupOut{}, "not_found", "成员节点都不存在", "")
		}
		pad := float64(in.Padding)
		if pad <= 0 {
			pad = 28
		}
		frame, ferr := d.Store.Add(canvas.Node{
			ProjectID: project,
			Title:     in.Title,
			Kind:      canvas.KindFrame,
			Width:     int(maxX - minX + pad*2),
			Height:    int(maxY - minY + pad*2 + 24),
			X:         minX - pad,
			Y:         minY - pad,
			Tags:      append([]string{"frame", "project:" + project}, in.NodeIDs...),
		})
		if ferr != nil {
			return projectFail[groupOut](groupOut{}, ferr, project)
		}
		return textResult(fmt.Sprintf("分组框「%s」已创建（节点 %s，包住 %d 个成员，%dx%d @ %.0f,%.0f）。",
				frame.Title, frame.ID, member, frame.Width, frame.Height, frame.X, frame.Y)),
			groupOut{Node: frame, Member: member}, nil
	}
}
