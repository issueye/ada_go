package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"adagodesign/internal/canvas"
)

// 项目类工具与统一的项目错误映射。
//
// 设计约束：设计稿必须归属一个**已存在**的项目。这条约束的真正拦截点在
// canvas.Store.Add（HTTP 与 MCP 两条入口共用），这里只负责把错误翻译成
// 稳定错误码 + 可执行提示，让模型能自己纠正（先建项目、或换一个已有项目）。

// checkProject 校验项目存在性；空 id 视为 project_required。
func checkProject(d Deps, id string) error {
	if strings.TrimSpace(id) == "" {
		return canvas.ErrProjectRequired
	}
	_, err := d.Store.GetProject(id)
	return err
}

// projectFail 把项目类错误转成 isError 结果（不抛异常，避免触发插件/工具的失败计数）。
func projectFail[O any](out O, err error, id string) (*mcp.CallToolResult, O, error) {
	switch {
	case errors.Is(err, canvas.ErrProjectRequired):
		return failWith(out, "project_required", "新增设计稿必须指定项目",
			"先 project_create 建项目（或 project_list 选一个已有项目），再添加设计稿")
	case errors.Is(err, canvas.ErrUnknownProject), errors.Is(err, canvas.ErrProjectNotFound):
		return failWith(out, "unknown_project", "未知项目 "+id, "用 project_list 查看已有项目")
	case errors.Is(err, canvas.ErrProjectNameEmpty):
		return failWith(out, "bad_request", "项目名不能为空", "给项目一个可读的名字，如「官网改版」")
	case errors.Is(err, canvas.ErrProjectDuplicate):
		return failWith(out, "project_duplicate", "同名项目已存在", "换一个名称，或直接使用已有项目")
	case errors.Is(err, canvas.ErrProjectNotEmpty):
		return failWith(out, "project_not_empty", "项目下仍有设计稿，拒绝删除",
			"先 canvas_clear 清空该项目（会保留一份内存快照），再删除项目")
	case errors.Is(err, canvas.ErrEdgeCrossProject):
		return failWith(out, "edge_cross_project", "连线两端必须属于同一项目",
			"跨项目表达流向请改用分组框或便签")
	case errors.Is(err, canvas.ErrUnknownCanvasRatio):
		return failWith(out, "unknown_ratio", "未知画布比例预设",
			"可用比例："+strings.Join(canvas.CanvasPresetIDs(), " / "))
	case errors.Is(err, canvas.ErrCanvasSizeInvalid):
		return failWith(out, "bad_request", "画布尺寸非法",
			fmt.Sprintf("宽高需同时给出，且在 %d..%d 之间", canvas.MinCanvasSide, canvas.MaxCanvasSide))
	case errors.Is(err, canvas.ErrArtboardResolver):
		return failWith(out, "artboard_unavailable", "当前进程没有画板解析器",
			"这是装配层未注入导致的环境问题（正常 serve/mcp 启动不会出现）")
	}
	return failWith(out, "internal", err.Error(), "")
}

func registerProjectTools(server *mcp.Server, d Deps) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "project_create",
		Description: "创建设计项目（设计稿的归属容器）。**任何新增设计稿之前必须先有项目**；" +
			"同名项目会被拒绝，重复调用请改用 project_list 取已有项目 ID。",
	}, projectCreateTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "project_list",
		Description: "列出全部设计项目及其设计稿数量与最后更新时间；不确定 project 参数该填什么时先调用它。",
	}, projectListTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "project_remove",
		Description: "删除空项目（需 confirm=true）。项目下仍有设计稿时会被拒绝，需先 canvas_clear。",
	}, projectRemoveTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name: "project_canvas",
		Description: "设置当前画布（项目作用域）的版式：比例预设或自定义宽高，free=true 恢复自由画布。" +
			"读取当前版式用 project_list；画布只是版式参考，不限制节点拖拽。",
	}, projectCanvasTool(d))

	mcp.AddTool(server, &mcp.Tool{
		Name: "canvas_resync_artboards",
		Description: "按节点自身的 artboard 重新计算宽高（显式触发，可撤销）。" +
			"画板或画布比例切换**不会**自动迁移已有节点尺寸；需要统一尺寸时显式调用。" +
			"组件只纠正宽度（高度由画布实测回写），整页稿同时纠正宽高；project 省略时处理全部项目。",
	}, canvasResyncArtboardsTool(d))
}

// ---------------- canvas_resync_artboards ----------------

type canvasResyncArtboardsIn struct {
	Project string `json:"project,omitempty" jsonschema:"项目 ID；省略则处理全部项目（project_list 查询）"`
}

type canvasResyncArtboardsOut struct {
	canvas.ResyncResult
	UndoHint string `json:"undo_hint"`
}

func canvasResyncArtboardsTool(d Deps) mcp.ToolHandlerFor[canvasResyncArtboardsIn, canvasResyncArtboardsOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in canvasResyncArtboardsIn) (*mcp.CallToolResult, canvasResyncArtboardsOut, error) {
		res, err := d.Store.ResyncArtboards(in.Project)
		if err != nil {
			return projectFail[canvasResyncArtboardsOut](canvasResyncArtboardsOut{}, err, in.Project)
		}
		out := canvasResyncArtboardsOut{ResyncResult: res, UndoHint: "canvas_undo"}
		if res.Changed == 0 {
			msg := fmt.Sprintf("没有需要调整的节点（检查 %d 个，跳过 %d 个）。", res.Total, res.Skipped)
			if len(res.UnknownArtboards) > 0 {
				msg += "未知画板：" + strings.Join(res.UnknownArtboards, " / ") + "。"
			}
			return textResult(msg), out, nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "已按画板重同步 %d 个节点（检查 %d 个，跳过 %d 个）：\n",
			res.Changed, res.Total, res.Skipped)
		for _, c := range res.Nodes {
			fmt.Fprintf(&sb, "- %s（%s / %s）：%d×%d → %d×%d\n",
				c.Title, c.ID, c.Artboard, c.FromWidth, c.FromHeight, c.ToWidth, c.ToHeight)
		}
		if len(res.UnknownArtboards) > 0 {
			sb.WriteString("未知画板（已跳过）：" + strings.Join(res.UnknownArtboards, " / ") + "\n")
		}
		sb.WriteString("这是一次可撤销的批量调整：canvas_undo 可整体回退。")
		return textResult(sb.String()), out, nil
	}
}

// ---------------- project_create ----------------

type projectCreateIn struct {
	Name string `json:"name" jsonschema:"项目名称（必填，同名会被拒绝），如「官网改版」"`
	Note string `json:"note,omitempty" jsonschema:"备注：目标、范围、验收口径等"`
}

type projectCreateOut struct {
	Project canvas.Project `json:"project"`
}

func projectCreateTool(d Deps) mcp.ToolHandlerFor[projectCreateIn, projectCreateOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in projectCreateIn) (*mcp.CallToolResult, projectCreateOut, error) {
		p, err := d.Store.AddProject(in.Name, in.Note)
		if err != nil {
			return projectFail[projectCreateOut](projectCreateOut{}, err, "")
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "已创建项目「%s」，ID: %s。\n下一步：design_preview / design_screen / canvas_add 时把 project 设为 %s，设计稿才会落到这个项目下。\n需要固定画布比例时调用 project_canvas（16:9 / 9:16 / 4:3 等）。",
			p.Name, p.ID, p.ID)
		return textResult(sb.String()), projectCreateOut{Project: p}, nil
	}
}

// ---------------- project_list ----------------

type projectListIn struct{}

type projectListOut struct {
	Count    int                     `json:"count"`
	Projects []canvas.ProjectSummary `json:"projects"`
}

func projectListTool(d Deps) mcp.ToolHandlerFor[projectListIn, projectListOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ projectListIn) (*mcp.CallToolResult, projectListOut, error) {
		projects := d.Store.ListProjects()
		out := projectListOut{Count: len(projects), Projects: projects}
		if len(projects) == 0 {
			return textResult("还没有任何项目。先调用 project_create 建一个（设计稿必须归属项目，不能直接新增）。"), out, nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "共 %d 个项目：\n", len(projects))
		for _, p := range projects {
			note := ""
			if p.Note != "" {
				note = " — " + p.Note
			}
			canvasInfo := ""
			if p.Canvas != nil {
				canvasInfo = "，画布 " + p.Canvas.Caption()
			}
			fmt.Fprintf(&sb, "- %s（%s）：%d 个节点%s%s，更新于 %s\n",
				p.ID, p.Name, p.NodeCount, canvasInfo, note, p.UpdatedAt.Format("2006-01-02 15:04"))
		}
		sb.WriteString("\n新增设计稿时把 project 设为上面的 ID。")
		return textResult(strings.TrimRight(sb.String(), "\n")), out, nil
	}
}

// ---------------- project_remove ----------------

type projectRemoveIn struct {
	ID      string `json:"id" jsonschema:"项目 ID（project_list 查询）"`
	Confirm *bool  `json:"confirm,omitempty" jsonschema:"必须显式传 true 才会删除"`
}

type projectRemoveOut struct {
	Removed string `json:"removed"`
}

func projectRemoveTool(d Deps) mcp.ToolHandlerFor[projectRemoveIn, projectRemoveOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in projectRemoveIn) (*mcp.CallToolResult, projectRemoveOut, error) {
		if in.Confirm == nil || !*in.Confirm {
			return failWith[projectRemoveOut](projectRemoveOut{}, "confirm_required",
				"删除项目需要 confirm=true", "先 project_list 确认项目 ID；空项目才可删除")
		}
		if err := d.Store.RemoveProject(strings.TrimSpace(in.ID)); err != nil {
			return projectFail[projectRemoveOut](projectRemoveOut{}, err, in.ID)
		}
		return textResult(fmt.Sprintf("已删除项目 %s。", in.ID)), projectRemoveOut{Removed: in.ID}, nil
	}
}

// ---------------- project_canvas ----------------

type projectCanvasIn struct {
	Project string `json:"project,omitempty" jsonschema:"项目 ID（画布作用域）；用 project_list 查询"`
	Ratio   string `json:"ratio,omitempty" jsonschema:"比例预设 ID：16:9|16:10|4:3|3:2|1:1|3:4|2:3|9:16；与 width/height 二选一"`
	Width   int    `json:"width,omitempty" jsonschema:"自定义画布宽（px，320..8192）"`
	Height  int    `json:"height,omitempty" jsonschema:"自定义画布高（px，320..8192）"`
	Free    bool   `json:"free,omitempty" jsonschema:"true = 恢复自由画布（无边界）"`
}

type projectCanvasOut struct {
	Project canvas.Project        `json:"project"`
	Presets []canvas.CanvasPreset `json:"presets,omitempty"`
}

func projectCanvasTool(d Deps) mcp.ToolHandlerFor[projectCanvasIn, projectCanvasOut] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in projectCanvasIn) (*mcp.CallToolResult, projectCanvasOut, error) {
		if perr := checkProject(d, in.Project); perr != nil {
			return projectFail[projectCanvasOut](projectCanvasOut{}, perr, in.Project)
		}
		var cfg *canvas.ProjectCanvas
		if !in.Free {
			cfg = &canvas.ProjectCanvas{Ratio: in.Ratio, Width: in.Width, Height: in.Height}
		}
		p, err := d.Store.SetProjectCanvas(in.Project, cfg)
		if err != nil {
			out := projectCanvasOut{}
			// 比例写错时把可选值一并返回，模型可以立刻自纠
			if errors.Is(err, canvas.ErrUnknownCanvasRatio) || errors.Is(err, canvas.ErrCanvasSizeInvalid) {
				out.Presets = canvas.CanvasPresets()
			}
			return projectFail[projectCanvasOut](out, err, in.Project)
		}
		out := projectCanvasOut{Project: p}
		if p.Canvas == nil {
			return textResult(fmt.Sprintf("画布「%s」已恢复为自由画布（无边界）。", p.Name)), out, nil
		}
		return textResult(fmt.Sprintf("画布「%s」已设为 %s。\n提示：画布边界只作版式参考，不影响节点拖拽；在画布 UI 点「居中」或按 0 可把画布居中展示。",
			p.Name, p.Canvas.Caption())), out, nil
	}
}
