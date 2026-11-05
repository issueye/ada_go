package mcpserver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"adagodesign/internal/canvas"
)

func TestDesignCompareTool(t *testing.T) {
	sess, d := newTestSession(t)
	res := callTool(t, sess, "design_compare", map[string]any{"system_a": "element", "system_b": "volcano"})
	if res.IsError {
		t.Fatalf("design_compare 失败: %s", contentText(t, res))
	}
	if !strings.Contains(contentText(t, res), "primary") {
		t.Fatal("对比文本应含 primary 差异")
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out compareOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("结构化输出解析失败: %v (%s)", err, raw)
	}
	if out.A != "element" || out.B != "volcano" {
		t.Fatalf("对比头异常: %+v", out)
	}

	// 摆上画布
	before := len(d.Store.List())
	res = callTool(t, sess, "design_compare", map[string]any{
		"system_a": "douyin", "system_b": "material", "theme": "dark", "add_to_canvas": true,
	})
	if res.IsError {
		t.Fatalf("对比板失败: %s", contentText(t, res))
	}
	if len(d.Store.List()) != before+1 {
		t.Fatal("对比板未入画布")
	}
	node := d.Store.List()[len(d.Store.List())-1]
	if node.Kind != canvas.KindPalette || !strings.Contains(node.HTML, "cp-swatch") {
		t.Fatalf("对比板节点异常: kind=%s", node.Kind)
	}

	// 错误路径
	res = callTool(t, sess, "design_compare", map[string]any{"system_a": "ant", "system_b": "ant"})
	if !res.IsError || !strings.Contains(contentText(t, res), "compare_failed") {
		t.Fatalf("自比应报 compare_failed: %s", contentText(t, res))
	}
}

func TestDesignScreenTool(t *testing.T) {
	sess, d := newTestSession(t)
	before := len(d.Store.List())
	res := callTool(t, sess, "design_screen", map[string]any{
		"system": "xiaomi", "template": "login", "theme": "light", "app_name": "米家",
	})
	if res.IsError {
		t.Fatalf("design_screen 失败: %s", contentText(t, res))
	}
	if !strings.Contains(contentText(t, res), "已摆上画布") {
		t.Fatal("屏幕预览应默认入画布")
	}
	nodes := d.Store.List()
	if len(nodes) != before+1 {
		t.Fatalf("节点数应 +1, got %d", len(nodes))
	}
	n := nodes[len(nodes)-1]
	if !strings.Contains(n.HTML, "ds-login-card") || !strings.Contains(n.HTML, "米家") {
		t.Fatal("屏幕节点内容异常")
	}
	if !strings.Contains(n.HTML, "xiaomi skin") {
		t.Fatal("屏幕节点应内嵌 xiaomi 皮肤")
	}
	// 全部模板 × 抽查体系
	for _, tpl := range []string{"settings", "feed", "profile"} {
		res = callTool(t, sess, "design_screen", map[string]any{"system": "material", "template": tpl})
		if res.IsError {
			t.Fatalf("%s 失败: %s", tpl, contentText(t, res))
		}
	}
	// 错误模板
	res = callTool(t, sess, "design_screen", map[string]any{"system": "ant", "template": "dashboard"})
	if !res.IsError || !strings.Contains(contentText(t, res), "screen_failed") {
		t.Fatalf("未知模板应报 screen_failed: %s", contentText(t, res))
	}
}

func TestDesignSearchTool(t *testing.T) {
	sess, _ := newTestSession(t)
	res := callTool(t, sess, "design_search", map[string]any{"query": "button"})
	if res.IsError {
		t.Fatalf("design_search 失败: %s", contentText(t, res))
	}
	if !strings.Contains(contentText(t, res), "[组件]") {
		t.Fatal("应命中组件")
	}
	res = callTool(t, sess, "design_search", map[string]any{"query": "抖音"})
	if res.IsError || !strings.Contains(contentText(t, res), "[体系]") {
		t.Fatalf("抖音搜索异常: %s", contentText(t, res))
	}
	res = callTool(t, sess, "design_search", map[string]any{"query": "  "})
	if res.IsError || !strings.Contains(contentText(t, res), "没有") {
		t.Fatalf("空查询应提示无结果: %s", contentText(t, res))
	}
}

func TestCanvasArrangeRestoreExport(t *testing.T) {
	sess, d := newTestSession(t)
	// 造 6 个节点（两体系交错，验证 arrange 会按体系聚合）
	for i, sys := range []string{"element", "douyin", "element", "douyin", "element", "douyin"} {
		d.Store.Add(canvas.Node{
			Title: sys + "-" + string(rune('a'+i)), System: sys, Kind: canvas.KindComponent,
			HTML: "<html><body>" + sys + "</body></html>", Width: 300, Height: 200,
		})
	}
	res := callTool(t, sess, "canvas_arrange", nil)
	if res.IsError {
		t.Fatalf("canvas_arrange 失败: %s", contentText(t, res))
	}
	nodes := d.Store.List()
	// 验证按体系聚合：同一体系的节点 x 应相同（列内同组相邻）
	elementX := map[float64]int{}
	for _, n := range nodes {
		if n.System == "element" {
			elementX[n.X]++
		}
	}
	if len(elementX) > 2 {
		t.Fatalf("element 节点应聚在少量列: %v", elementX)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var arr arrangeOut
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("arrange 结构化输出解析失败: %v", err)
	}
	if arr.Moved != 6 || arr.Columns < 1 {
		t.Fatalf("arrange 结果异常: %+v", arr)
	}

	// clear + restore
	res = callTool(t, sess, "canvas_clear", map[string]any{"confirm": true})
	if res.IsError {
		t.Fatalf("canvas_clear 失败: %s", contentText(t, res))
	}
	if len(d.Store.List()) != 0 {
		t.Fatal("清空后应无节点")
	}
	res = callTool(t, sess, "canvas_restore", nil)
	if res.IsError || !strings.Contains(contentText(t, res), "已恢复 6 个节点") {
		t.Fatalf("canvas_restore 应恢复 6 个节点: %s", contentText(t, res))
	}
	if len(d.Store.List()) != 6 {
		t.Fatalf("恢复后节点数 = %d", len(d.Store.List()))
	}
	// 再 restore 应无快照
	res = callTool(t, sess, "canvas_restore", nil)
	if res.IsError || !strings.Contains(contentText(t, res), "没有可恢复") {
		t.Fatalf("二次恢复应提示无快照: %s", contentText(t, res))
	}

	// export
	res = callTool(t, sess, "canvas_export", map[string]any{"title": "对比板"})
	if res.IsError {
		t.Fatalf("canvas_export 失败: %s", contentText(t, res))
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var exp exportOut
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatalf("export 结构化输出解析失败: %v (%s)", err, raw)
	}
	if exp.NodeCount != 6 || exp.Bytes <= 0 {
		t.Fatalf("export 结果异常: %+v", exp)
	}
	rawHTML, err := os.ReadFile(exp.Path)
	if err != nil {
		t.Fatalf("导出文件不存在: %v", err)
	}
	for _, marker := range []string{"<iframe", "board", "element", "douyin"} {
		if !strings.Contains(string(rawHTML), marker) {
			t.Fatalf("导出文件缺少 %q", marker)
		}
	}
	// 空画布导出
	callTool(t, sess, "canvas_clear", map[string]any{"confirm": true})
	res = callTool(t, sess, "canvas_export", nil)
	if !res.IsError || !strings.Contains(contentText(t, res), "empty_canvas") {
		t.Fatalf("空画布导出应报 empty_canvas: %s", contentText(t, res))
	}
	callTool(t, sess, "canvas_restore", nil) // 收尾恢复，避免影响其他测试（快照已用尽则忽略）
}
