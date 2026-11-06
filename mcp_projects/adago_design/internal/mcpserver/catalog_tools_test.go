package mcpserver

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"adagodesign/internal/design"
)

// 新增的目录类工具必须都注册成功。
func TestCatalogToolsListed(t *testing.T) {
	sess, _ := newTestSession(t)
	tools, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"design_icons": false, "design_fonts": false, "design_artboard": false}
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

func TestDesignIconsTool(t *testing.T) {
	sess, _ := newTestSession(t)

	res := callTool(t, sess, "design_icons", map[string]any{"group": "status"})
	if res.IsError {
		t.Fatalf("design_icons 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out iconsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Count == 0 || len(out.Icons) != out.Count {
		t.Fatalf("count=%d len=%d", out.Count, len(out.Icons))
	}
	for _, ic := range out.Icons {
		if ic.Group != "status" {
			t.Errorf("%s 分组 = %s", ic.Name, ic.Group)
		}
		if !strings.Contains(ic.HTML, "<use href=\"#ds-ic-") {
			t.Errorf("%s HTML 不是精灵引用: %s", ic.Name, ic.HTML)
		}
	}

	// 别名检索：历史字符符号应能查到替代图标
	res = callTool(t, sess, "design_icons", map[string]any{"query": "✕"})
	if res.IsError {
		t.Fatalf("别名检索失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("close")) {
		t.Fatal("查 ✕ 应命中 close")
	}

	// inline 形式
	res = callTool(t, sess, "design_icons", map[string]any{"query": "home", "format": "inline"})
	if res.IsError {
		t.Fatalf("inline 失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("<svg class=\"ds-icon")) {
		t.Fatal("inline 应返回完整 <svg>")
	}
}

func TestDesignFontsTool(t *testing.T) {
	sess, _ := newTestSession(t)
	res := callTool(t, sess, "design_fonts", map[string]any{})
	if res.IsError {
		t.Fatalf("design_fonts 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out fontsOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(out.Fonts) < 10 {
		t.Fatalf("字体太少: %d", len(out.Fonts))
	}
	if len(out.Policy) == 0 {
		t.Fatal("缺少授权策略说明")
	}
	var misans *design.FontSpec
	for i := range out.Fonts {
		if out.Fonts[i].ID == "misans" {
			misans = &out.Fonts[i]
		}
	}
	if misans == nil || !misans.Commercial || misans.Redistributable {
		t.Fatalf("MiSans 授权标记异常: %+v", misans)
	}
	// 中文脚本过滤
	res = callTool(t, sess, "design_fonts", map[string]any{"script": "hans"})
	if res.IsError {
		t.Fatalf("按脚本过滤失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("Noto Sans SC")) {
		t.Fatal("hans 过滤应包含 Noto Sans SC")
	}
}

func TestDesignArtboardTool(t *testing.T) {
	sess, _ := newTestSession(t)

	res := callTool(t, sess, "design_artboard", map[string]any{})
	if res.IsError {
		t.Fatalf("design_artboard 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out artboardOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Count != 8 {
		t.Fatalf("画板数 = %d", out.Count)
	}

	res = callTool(t, sess, "design_artboard", map[string]any{"artboard": "iphone-15"})
	if res.IsError {
		t.Fatalf("指定画板失败: %s", contentText(t, res))
	}
	raw, _ = json.Marshal(res.StructuredContent)
	out = artboardOut{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Selected == nil || out.Selected.ContentWidth != 361 {
		t.Fatalf("iphone-15 内容宽异常: %+v", out.Selected)
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("尺寸")) && !bytes.Contains([]byte(contentText(t, res)), []byte("画板")) {
		t.Fatalf("文本输出缺少契约: %s", contentText(t, res))
	}

	// 非法画板：稳定错误码
	res = callTool(t, sess, "design_artboard", map[string]any{"artboard": "nope"})
	if !res.IsError {
		t.Fatal("非法画板应返回 isError")
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("unknown_artboard")) {
		t.Fatalf("错误码缺失: %s", contentText(t, res))
	}

	// 按 family 取默认
	res = callTool(t, sess, "design_artboard", map[string]any{"family": "mobile"})
	if res.IsError {
		t.Fatalf("按 family 查询失败: %s", contentText(t, res))
	}
	if !bytes.Contains([]byte(contentText(t, res)), []byte("iphone-15")) {
		t.Fatal("family=mobile 默认应为 iphone-15")
	}
}

// design_preview 必须把画板一路透传到节点上。
func TestPreviewCarriesArtboard(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)
	res := callTool(t, sess, "design_preview", map[string]any{
		"project": pid, "system": "apple", "components": []string{"button"}, "artboard": "ipad-768",
	})
	if res.IsError {
		t.Fatalf("design_preview 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out previewOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Artboard != "ipad-768" || out.Width != 768 {
		t.Fatalf("画板透传失败: %+v", out)
	}
	node, err := d.Store.Get(out.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Artboard != "ipad-768" || !node.AutoHeight || node.Kind != "component" {
		t.Fatalf("节点字段异常: artboard=%q auto=%v kind=%q", node.Artboard, node.AutoHeight, node.Kind)
	}
}

func TestCanvasAddWithArtboard(t *testing.T) {
	sess, d := newTestSession(t)
	pid := testProject(t, d)
	res := callTool(t, sess, "canvas_add", map[string]any{
		"project": pid, "title": "自绘页", "html": "<html><body>hi</body></html>", "artboard": "mp-wx",
	})
	if res.IsError {
		t.Fatalf("canvas_add 失败: %s", contentText(t, res))
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out canvasAddOut
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if out.Node.Width != 375 || out.Node.Artboard != "mp-wx" {
		t.Fatalf("画板尺寸未生效: %dx%d %q", out.Node.Width, out.Node.Height, out.Node.Artboard)
	}
	res = callTool(t, sess, "canvas_add", map[string]any{
		"project": pid, "title": "x", "html": "<html></html>", "artboard": "nope",
	})
	if !res.IsError {
		t.Fatal("非法画板应报错")
	}
}
