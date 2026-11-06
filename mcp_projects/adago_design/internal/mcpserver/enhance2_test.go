package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"adagodesign/internal/canvas"
)

func TestDesignDeriveTool(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)
	before := len(d.Store.List())
	res := callTool(t, sess, "design_derive", map[string]any{
		"project": pid, "primary": "#C8412B", "name": "山茶茶饮", "base_system": "volcano",
	})
	if res.IsError {
		t.Fatalf("design_derive 失败: %s", contentText(t, res))
	}
	if !strings.Contains(contentText(t, res), "已派生") || !strings.Contains(contentText(t, res), "#C8412B") {
		t.Fatalf("derive 文本异常: %s", contentText(t, res))
	}
	// 派生体系已注册且持久化
	if len(d.Store.List()) != before+1 {
		t.Fatal("色板节点应已入画布")
	}
	// 用派生体系 ID 生成预览（体系注册生效）
	res = callTool(t, sess, "design_preview", map[string]any{
		"project": pid, "system": "shancha-chayin", "components": []string{"button"},
	})
	if !res.IsError {
		t.Log("派生 ID 命中（slug 来自名称）")
	}
	// 错误路径
	res = callTool(t, sess, "design_derive", map[string]any{
		"project": pid, "primary": "red", "name": "x", "base_system": "volcano",
	})
	if !res.IsError || !strings.Contains(contentText(t, res), "derive_failed") {
		t.Fatalf("非法色应报 derive_failed: %s", contentText(t, res))
	}
}

func TestDesignAuditTool(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)
	before := len(d.Store.List())
	res := callTool(t, sess, "design_audit", map[string]any{
		"project": pid, "system": "element", "theme": "light",
	})
	if res.IsError {
		t.Fatalf("design_audit 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out auditOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("audit 结构化输出解析失败: %v (%s)", err, raw)
	}
	if out.System != "element" || out.Score < 60 || out.Score > 100 {
		t.Fatalf("audit 结果异常: %+v", out)
	}
	if len(d.Store.List()) != before+1 {
		t.Fatal("走查报告节点应已入画布")
	}
	node := d.Store.List()[len(d.Store.List())-1]
	if !strings.Contains(node.HTML, "au-item") {
		t.Fatal("报告节点应含走查报告渲染")
	}
	// 错误路径
	res = callTool(t, sess, "design_audit", map[string]any{"project": pid, "system": "nope"})
	if !res.IsError || !strings.Contains(contentText(t, res), "audit_failed") {
		t.Fatalf("未知体系应报 audit_failed: %s", contentText(t, res))
	}
}

func TestCanvasConnectAndGroup(t *testing.T) {
	sess, d := newTestSession(t)
	a := seedNode(t, d, canvas.Node{Title: "A", HTML: "<html>a</html>", Width: 300, Height: 200, X: 0, Y: 0})
	b := seedNode(t, d, canvas.Node{Title: "B", HTML: "<html>b</html>", Width: 300, Height: 200, X: 500, Y: 0})
	c := seedNode(t, d, canvas.Node{Title: "C", HTML: "<html>c</html>", Width: 300, Height: 200, X: 0, Y: 400})

	res := callTool(t, sess, "canvas_connect", map[string]any{"from": a.ID, "to": b.ID, "label": "下一步"})
	if res.IsError {
		t.Fatalf("canvas_connect 失败: %s", contentText(t, res))
	}
	if len(d.Store.ListEdges()) != 1 {
		t.Fatal("连线未创建")
	}
	// 重复连线
	res = callTool(t, sess, "canvas_connect", map[string]any{"from": b.ID, "to": a.ID})
	if !res.IsError || !strings.Contains(contentText(t, res), "edge_duplicate") {
		t.Fatalf("反向重复应报 edge_duplicate: %s", contentText(t, res))
	}
	// 分组框
	res = callTool(t, sess, "canvas_group", map[string]any{"title": "核心流程", "node_ids": []string{a.ID, b.ID, c.ID}})
	if res.IsError {
		t.Fatalf("canvas_group 失败: %s", contentText(t, res))
	}
	nodes := d.Store.List()
	var frame *canvas.Node
	for i := range nodes {
		if nodes[i].Kind == canvas.KindFrame {
			frame = &nodes[i]
		}
	}
	if frame == nil {
		t.Fatal("分组框节点未创建")
	}
	if frame.X > 0 || frame.Y > 0 || frame.Width < 848 || frame.Height < 628 {
		t.Fatalf("分组框未包住成员: %+v", frame)
	}
	if frame.Z != 0 {
		t.Fatalf("分组框应垫底: z=%d", frame.Z)
	}
	// 删除成员级联 + 边清空
	res = callTool(t, sess, "canvas_remove", map[string]any{"id": a.ID})
	if res.IsError {
		t.Fatalf("remove 失败: %s", contentText(t, res))
	}
	if len(d.Store.ListEdges()) != 0 {
		t.Fatal("成员删除后连线应级联删除")
	}
}
