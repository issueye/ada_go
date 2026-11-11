package mcpserver

// 迭代 2：工具面验收 —— design_audit 节点级走查、design_tokens 精简输出。

import (
	"encoding/json"
	"strings"
	"testing"

	"adagodesign/internal/canvas"
)

func TestAuditNodeID(t *testing.T) {
	sess, d := newTestSession(t)

	proj, err := d.Store.AddProject("走查测试", "")
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	pid := proj.ID
	// 脏节点：外链图片 + 字符图标
	dirty, err := d.Store.Add(canvas.Node{
		ProjectID: pid, Title: "脏稿", Kind: canvas.KindNote,
		HTML: `<html><head><style>body{font-family:"Helvetica Neue"}</style></head><body><img src="http://example.com/a.png"><span>▾</span></body></html>`,
		Width: 420,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	res := callTool(t, sess, "design_audit", map[string]any{"node_id": dirty.ID})
	if res.IsError {
		t.Fatalf("design_audit(node_id) 报错: %s", contentText(t, res))
	}
	var out auditOut
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析出参: %v (%s)", err, raw)
	}
	if out.NodeID != dirty.ID {
		t.Fatalf("node_id 应回显, got %q", out.NodeID)
	}
	if out.Failed < 2 { // 外链资源 + 图标与符号
		t.Fatalf("脏稿应至少 2 项 fail: %+v", out.Checks)
	}
	if out.Score < 100 {
		text := contentText(t, res)
		if !strings.Contains(text, "外链资源") || !strings.Contains(text, "图标与符号") {
			t.Fatalf("文本报告应含 fail 明细: %s", text)
		}
	}

	// 干净节点（声明画板、宽度一致）：全 pass
	clean, err := d.Store.Add(canvas.Node{
		ProjectID: pid, Title: "净稿", Kind: canvas.KindNote,
		Artboard: "iphone-15", Width: 393,
		HTML: "<html><head><style>body{font-family:Inter,sans-serif}</style></head><body><svg class=\"ds-icon\"><use href=\"#ds-ic-close\"/></svg></body></html>",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	res = callTool(t, sess, "design_audit", map[string]any{"node_id": clean.ID})
	var okOut auditOut
	raw, _ = json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &okOut); err != nil {
		t.Fatalf("解析出参: %v", err)
	}
	if okOut.Failed != 0 || okOut.Warned != 0 || okOut.Score != 100 {
		t.Fatalf("净稿应全 pass: %+v", okOut.Checks)
	}

	// 缺节点 → not_found；node_id 与 system 均缺 → bad_request
	res = callTool(t, sess, "design_audit", map[string]any{"node_id": "n-missing"})
	if !res.IsError {
		t.Fatal("缺失节点应报错")
	}
	res = callTool(t, sess, "design_audit", map[string]any{})
	if !res.IsError {
		t.Fatal("node_id 与 system 全缺应报 bad_request")
	}
}

func TestTokensBrief(t *testing.T) {
	sess, _ := newTestSession(t)

	// brief：颜色只有值、无阴影/动效、字阶只有 size/weight
	res := callTool(t, sess, "design_tokens", map[string]any{"system": "ant", "brief": true})
	var out tokensOut
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("解析出参: %v (%s)", err, raw)
	}
	colors, ok := out.Content["colors"].(map[string]any)
	if !ok || colors["primary"] != "#1677FF" {
		t.Fatalf("brief 颜色应为「键 -> 值」: %v", out.Content["colors"])
	}
	for _, absent := range []string{"shadows", "motion"} {
		if _, exists := out.Content[absent]; exists {
			t.Fatalf("brief 不应包含 %s", absent)
		}
	}
	if _, hasUsage := colors["primary-hover"]; hasUsage {
		t.Fatal("brief 颜色不应包含 hover 变体")
	}
	typ, _ := out.Content["type"].(map[string]any)
	if typ == nil || typ["family"] == "" {
		t.Fatalf("brief 字型应含 family: %v", typ)
	}

	// 非 brief：全量输出仍含 shadows/motion 与 usage
	res = callTool(t, sess, "design_tokens", map[string]any{"system": "ant"})
	var full tokensOut
	raw, _ = json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatalf("解析出参: %v", err)
	}
	if _, exists := full.Content["shadows"]; !exists {
		t.Fatal("全量输出应包含 shadows")
	}
	if cm, ok := full.Content["colors"].(map[string]any); ok {
		p, _ := cm["primary"].(map[string]any)
		if p == nil || p["usage"] == "" {
			t.Fatalf("全量颜色应带 usage 说明: %v", p)
		}
	}
}
