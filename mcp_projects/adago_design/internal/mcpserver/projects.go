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
		fmt.Fprintf(&sb, "已创建项目「%s」，ID: %s。\n下一步：design_preview / design_screen / canvas_add 时把 project 设为 %s，设计稿才会落到这个项目下。",
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
			fmt.Fprintf(&sb, "- %s（%s）：%d 个节点%s，更新于 %s\n",
				p.ID, p.Name, p.NodeCount, note, p.UpdatedAt.Format("2006-01-02 15:04"))
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
