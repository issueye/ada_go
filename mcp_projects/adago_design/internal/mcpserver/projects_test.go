package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// 项目工具契约：先建项目，再在项目下加设计稿；未建项目直接加会被拒绝。

func TestProjectToolsListed(t *testing.T) {
	sess, _ := newTestSession(t)
	tools, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"project_create": false, "project_list": false, "project_remove": false}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("缺少工具 %s", name)
		}
	}
}

func TestDesignPreviewRequiresProject(t *testing.T) {
	sess, d := newTestSession(t)

	// 一个项目都没有：直接加设计稿应被拒绝，且不落库
	res := callTool(t, sess, "design_preview", map[string]any{
		"system": "apple", "components": []string{"button"},
	})
	if !res.IsError {
		t.Fatal("未建项目时新增设计稿应失败")
	}
	if !strings.Contains(contentText(t, res), "project_required") {
		t.Fatalf("应报 project_required: %s", contentText(t, res))
	}
	if len(d.Store.List()) != 0 {
		t.Fatal("被拒绝的预览不应落库")
	}

	// 项目 ID 不存在 → unknown_project
	res = callTool(t, sess, "design_preview", map[string]any{
		"project": "p-nope", "system": "apple", "components": []string{"button"},
	})
	if !res.IsError || !strings.Contains(contentText(t, res), "unknown_project") {
		t.Fatalf("未知项目应报 unknown_project: %s", contentText(t, res))
	}

	// canvas_add 同样受约束
	res = callTool(t, sess, "canvas_add", map[string]any{
		"title": "x", "html": "<html></html>",
	})
	if !res.IsError || !strings.Contains(contentText(t, res), "project_required") {
		t.Fatalf("canvas_add 缺项目应报 project_required: %s", contentText(t, res))
	}
}

func TestProjectCreateListRemove(t *testing.T) {
	sess, d := newTestSession(t)

	res := callTool(t, sess, "project_create", map[string]any{"name": "官网改版", "note": "PC + H5"})
	if res.IsError {
		t.Fatalf("project_create 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var created projectCreateOut
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	pid := created.Project.ID
	if pid == "" || created.Project.Name != "官网改版" {
		t.Fatalf("项目字段异常: %+v", created.Project)
	}

	// 同名拒绝
	res = callTool(t, sess, "project_create", map[string]any{"name": "官网改版"})
	if !res.IsError || !strings.Contains(contentText(t, res), "project_duplicate") {
		t.Fatalf("同名应报 project_duplicate: %s", contentText(t, res))
	}
	// 空名拒绝
	res = callTool(t, sess, "project_create", map[string]any{"name": " "})
	if !res.IsError || !strings.Contains(contentText(t, res), "bad_request") {
		t.Fatalf("空名应报 bad_request: %s", contentText(t, res))
	}

	// 在项目下加设计稿
	res = callTool(t, sess, "design_preview", map[string]any{
		"project": pid, "system": "apple", "components": []string{"button"},
	})
	if res.IsError {
		t.Fatalf("带项目预览失败: %s", contentText(t, res))
	}
	if len(d.Store.ListByProject(pid)) != 1 {
		t.Fatal("设计稿应落在指定项目下")
	}

	// 列表：计数与提示
	res = callTool(t, sess, "project_list", map[string]any{})
	if res.IsError {
		t.Fatalf("project_list 失败: %s", contentText(t, res))
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var list projectListOut
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if list.Count != 1 || list.Projects[0].NodeCount != 1 {
		t.Fatalf("项目列表异常: %+v", list)
	}
	if !strings.Contains(contentText(t, res), "官网改版") {
		t.Fatalf("文本应含项目名: %s", contentText(t, res))
	}

	// 非空项目拒绝删除
	res = callTool(t, sess, "project_remove", map[string]any{"id": pid, "confirm": true})
	if !res.IsError || !strings.Contains(contentText(t, res), "project_not_empty") {
		t.Fatalf("非空项目应报 project_not_empty: %s", contentText(t, res))
	}
	// 缺 confirm 拒绝
	res = callTool(t, sess, "project_remove", map[string]any{"id": pid})
	if !res.IsError || !strings.Contains(contentText(t, res), "confirm_required") {
		t.Fatalf("缺 confirm 应报 confirm_required: %s", contentText(t, res))
	}

	// 清空后可删
	res = callTool(t, sess, "canvas_clear", map[string]any{"project": pid, "confirm": true})
	if res.IsError {
		t.Fatalf("canvas_clear 失败: %s", contentText(t, res))
	}
	res = callTool(t, sess, "project_remove", map[string]any{"id": pid, "confirm": true})
	if res.IsError {
		t.Fatalf("空项目应可删除: %s", contentText(t, res))
	}
	if len(d.Store.ListProjects()) != 0 {
		t.Fatal("删除后项目列表应为空")
	}
}

// 项目隔离：A 项目的节点不出现在 B 项目的列表/排版/导出里。
func TestProjectIsolationAcrossTools(t *testing.T) {
	sess, d := newTestSession(t)
	a := testProject(t, d)
	bres := callTool(t, sess, "project_create", map[string]any{"name": "第二个项目"})
	raw, _ := json.Marshal(bres.StructuredContent)
	var created projectCreateOut
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	b := created.Project.ID

	callTool(t, sess, "canvas_add", map[string]any{"project": a, "title": "A1", "html": "<html>a</html>"})
	callTool(t, sess, "canvas_add", map[string]any{"project": b, "title": "B1", "html": "<html>b</html>"})

	res := callTool(t, sess, "canvas_list", map[string]any{"project": a})
	if res.IsError {
		t.Fatalf("canvas_list 失败: %s", contentText(t, res))
	}
	text := contentText(t, res)
	if !strings.Contains(text, "A1") || strings.Contains(text, "B1") {
		t.Fatalf("项目列表应只含本项目节点: %s", text)
	}

	// 导出按项目范围
	res = callTool(t, sess, "canvas_export", map[string]any{"project": a, "title": "A 板"})
	if res.IsError {
		t.Fatalf("canvas_export 失败: %s", contentText(t, res))
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var exp exportOut
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if exp.NodeCount != 1 {
		t.Fatalf("导出节点数 = %d，期望 1（只导出 A 项目）", exp.NodeCount)
	}
}
