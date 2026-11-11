package canvas

// 迭代 2-①：预览 HTML 的 CSP 注入（外链资源阻断，保住 foreignObject 截图链路）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizePreviewHTMLInjection(t *testing.T) {
	const marker = `default-src 'none'`

	// 完整文档：CSP 紧跟 <head>
	doc := "<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n</head>\n<body><p>x</p></body></html>"
	out := SanitizePreviewHTML(doc)
	if !strings.Contains(out, "<head>\n<meta http-equiv=\"Content-Security-Policy\"") {
		t.Fatalf("CSP 应注入在 <head> 之后: %s", out)
	}
	if !strings.Contains(out, marker) {
		t.Fatalf("CSP 应阻断默认来源: %s", out)
	}
	// 幂等
	if again := SanitizePreviewHTML(out); again != out {
		t.Fatal("重复注入应原样返回")
	}

	// 无 <head> 的完整文档：补一个 <head>
	noHead := "<html><body><p>x</p></body></html>"
	out = SanitizePreviewHTML(noHead)
	if !strings.Contains(out, "<html>\n<head><meta http-equiv") {
		t.Fatalf("无 head 时应补 <head> 注入: %s", out)
	}

	// 纯片段：前置
	out = SanitizePreviewHTML("<p>x</p>")
	if !strings.HasPrefix(out, "<meta http-equiv=\"Content-Security-Policy\"") {
		t.Fatalf("片段应前置 CSP: %s", out)
	}

	// 已自带 CSP：不动
	self := `<html><head><meta http-equiv="Content-Security-Policy" content="default-src 'self'"></head><body></body></html>`
	if got := SanitizePreviewHTML(self); got != self {
		t.Fatal("已声明 CSP 的文档不应被改写")
	}
}

func TestStoreSanitizesNodeHTML(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := newTestProject(t, s)

	// Add：外链图片入库即带 CSP
	n := addNode(t, s, pid, Node{
		Title: "外链卡", Kind: KindNote,
		HTML: `<html><head><title>t</title></head><body><img src="http://example.com/a.png"></body></html>`,
	})
	if !strings.Contains(n.HTML, "Content-Security-Policy") {
		t.Fatal("入库 HTML 应注入 CSP")
	}

	// Update 换正文：同样注入
	up, err := s.Update(n.ID, func(x *Node) { x.HTML = `<html><body><link rel="stylesheet" href="https://cdn.example.com/x.css"></body></html>` })
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(up.HTML, "Content-Security-Policy") {
		t.Fatal("更新后的正文应注入 CSP")
	}
	if !up.HTMLRev.After(up.CreatedAt) {
		t.Fatal("换正文应推进 html_rev")
	}
}

func TestLegacySnapshotGetsCSP(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"version":3,"nodes":[{"id":"n-old","project_id":"p-old","title":"旧","kind":"note",
		"html":"<html><body><img src=\"http://example.com/b.png\"></body></html>",
		"width":300,"height":200,"x":0,"y":0,"z":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "canvas.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("写快照: %v", err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	n, err := s.Get("n-old")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// 无主节点会迁入历史项目（既有迁移逻辑），CSP 在装载时补齐
	if !strings.Contains(n.HTML, "Content-Security-Policy") {
		t.Fatalf("装载旧快照应补 CSP: %s", n.HTML)
	}
}
