package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

// project_canvas：画布版式（比例/尺寸）的 MCP 契约。

func TestProjectCanvasToolListed(t *testing.T) {
	sess, _ := newTestSession(t)
	tools, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "project_canvas" {
			return
		}
	}
	t.Fatal("缺少工具 project_canvas")
}

func TestProjectCanvasByRatio(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)

	res := callTool(t, sess, "project_canvas", map[string]any{"project": pid, "ratio": "16:9"})
	if res.IsError {
		t.Fatalf("project_canvas 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out projectCanvasOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Project.Canvas == nil || out.Project.Canvas.Width != 1920 || out.Project.Canvas.Height != 1080 {
		t.Fatalf("画布回包异常: %+v", out.Project.Canvas)
	}
	if !strings.Contains(contentText(t, res), "1920×1080") {
		t.Fatalf("文本缺少尺寸: %s", contentText(t, res))
	}
	// 落库
	stored, err := d.Store.GetProject(pid)
	if err != nil || stored.Canvas == nil || stored.Canvas.Ratio != "16:9" {
		t.Fatalf("画布未落库: %+v (%v)", stored.Canvas, err)
	}

	// project_list 文本应带画布信息
	res = callTool(t, sess, "project_list", map[string]any{})
	if !strings.Contains(contentText(t, res), "1920×1080") {
		t.Fatalf("project_list 应显示画布: %s", contentText(t, res))
	}
}

func TestProjectCanvasCustomAndFree(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)

	res := callTool(t, sess, "project_canvas", map[string]any{"project": pid, "width": 1280, "height": 800})
	if res.IsError {
		t.Fatalf("自定义尺寸失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out projectCanvasOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Project.Canvas == nil || out.Project.Canvas.Ratio != "custom" || out.Project.Canvas.Width != 1280 {
		t.Fatalf("自定义画布异常: %+v", out.Project.Canvas)
	}

	// free 恢复自由画布
	res = callTool(t, sess, "project_canvas", map[string]any{"project": pid, "free": true})
	if res.IsError {
		t.Fatalf("free 失败: %s", contentText(t, res))
	}
	stored, _ := d.Store.GetProject(pid)
	if stored.Canvas != nil {
		t.Fatalf("free 后应为自由画布: %+v", stored.Canvas)
	}
	if !strings.Contains(contentText(t, res), "自由画布") {
		t.Fatalf("文本应提示自由画布: %s", contentText(t, res))
	}
}

func TestProjectCanvasErrors(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)

	// 未知比例：稳定错误码 + 预设清单
	res := callTool(t, sess, "project_canvas", map[string]any{"project": pid, "ratio": "21:9"})
	if !res.IsError || !strings.Contains(contentText(t, res), "unknown_ratio") {
		t.Fatalf("未知比例应报 unknown_ratio: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out projectCanvasOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out.Presets) == 0 {
		t.Fatal("未知比例时应返回可选预设")
	}

	// 非法尺寸
	res = callTool(t, sess, "project_canvas", map[string]any{"project": pid, "width": 100, "height": 100})
	if !res.IsError || !strings.Contains(contentText(t, res), "bad_request") {
		t.Fatalf("非法尺寸应报 bad_request: %s", contentText(t, res))
	}

	// 未指定项目
	res = callTool(t, sess, "project_canvas", map[string]any{"ratio": "16:9"})
	if !res.IsError || !strings.Contains(contentText(t, res), "project_required") {
		t.Fatalf("缺项目应报 project_required: %s", contentText(t, res))
	}

	// 未知项目
	res = callTool(t, sess, "project_canvas", map[string]any{"project": "p-nope", "ratio": "16:9"})
	if !res.IsError || !strings.Contains(contentText(t, res), "unknown_project") {
		t.Fatalf("未知项目应报 unknown_project: %s", contentText(t, res))
	}
}
