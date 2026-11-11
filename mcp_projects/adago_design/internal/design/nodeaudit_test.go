package design

// 迭代 2-③：节点级走查（AuditNodeHTML）。

import (
	"strings"
	"testing"
)

func TestNodeAuditClean(t *testing.T) {
	rep := AuditNodeHTML(NodeAuditInput{
		HTML:   "<!DOCTYPE html><html><head><style>p{color:#333;font-family:Inter,sans-serif}</style></head><body><svg class=\"ds-icon\"><use href=\"#ds-ic-close\"/></svg></body></html>",
		Width:  393,
		Height: 852,
		Artboard: "iphone-15",
	})
	if rep.Failed != 0 || rep.Warned != 0 {
		t.Fatalf("干净节点不应有 warn/fail: %+v", rep.Checks)
	}
	if rep.Score != 100 {
		t.Fatalf("score = %d, 期望 100", rep.Score)
	}
	for _, c := range rep.Checks {
		if c.Status != "pass" {
			t.Fatalf("全部检查应为 pass: %+v", c)
		}
	}
}

func TestNodeAuditExternalRefFails(t *testing.T) {
	rep := AuditNodeHTML(NodeAuditInput{
		HTML: `<html><body><img src="http://example.com/a.png"><link rel="stylesheet" href="https://cdn.example.com/x.css"></body></html>`,
	})
	var ext *AuditCheck
	for i := range rep.Checks {
		if rep.Checks[i].Name == "外链资源" {
			ext = &rep.Checks[i]
		}
	}
	if ext == nil || ext.Status != "fail" {
		t.Fatalf("外链资源应 fail: %+v", rep.Checks)
	}
	if !strings.Contains(ext.Detail, "http://example.com/a.png") {
		t.Fatalf("报告应列出命中地址: %s", ext.Detail)
	}
	// 1 fail（外链）+ 1 warn（未声明画板）：100 - 25 - 10 = 65
	if rep.Score != 65 {
		t.Fatalf("评分 = %d, 期望 65（fail -25 + 未声明画板 warn -10）", rep.Score)
	}
}

func TestNodeAuditEmojiFails(t *testing.T) {
	rep := AuditNodeHTML(NodeAuditInput{HTML: "<html><body><span>▾</span><span>🔍</span></body></html>"})
	if rep.Failed == 0 {
		t.Fatalf("emoji/字符图标应 fail: %+v", rep.Checks)
	}
	for _, c := range rep.Checks {
		if c.Name == "图标与符号" && c.Status == "fail" && !strings.Contains(c.Detail, "🔍") {
			t.Fatalf("报告应包含命中的符号: %s", c.Detail)
		}
	}
}

func TestNodeAuditArtboardContract(t *testing.T) {
	// 未声明画板 → warn
	rep := AuditNodeHTML(NodeAuditInput{HTML: "<html></html>", Width: 420})
	if s := checkStatus(rep, "尺寸规范"); s != "warn" {
		t.Fatalf("未声明画板应 warn, got %s", s)
	}
	// 宽度与画板不一致 → warn
	rep = AuditNodeHTML(NodeAuditInput{HTML: "<html></html>", Width: 420, Artboard: "iphone-15"})
	if s := checkStatus(rep, "尺寸规范"); s != "warn" {
		t.Fatalf("宽度偏离画板应 warn, got %s", s)
	}
	// 一致 → pass
	rep = AuditNodeHTML(NodeAuditInput{HTML: "<html></html>", Width: 393, Artboard: "iphone-15"})
	if s := checkStatus(rep, "尺寸规范"); s != "pass" {
		t.Fatalf("画板一致应 pass, got %s", s)
	}
}

func TestNodeAuditFontLicense(t *testing.T) {
	// 专有字体 → warn
	rep := AuditNodeHTML(NodeAuditInput{
		HTML: `<html><head><style>body{font-family:"Helvetica Neue",sans-serif}</style></head><body></body></html>`,
	})
	if s := checkStatus(rep, "字体授权"); s != "warn" {
		t.Fatalf("专有字体应 warn, got %s", s)
	}
	// 未声明 → pass（继承体系默认栈）
	rep = AuditNodeHTML(NodeAuditInput{HTML: "<html><body></body></html>"})
	if s := checkStatus(rep, "字体授权"); s != "pass" {
		t.Fatalf("未声明字族应 pass, got %s", s)
	}
}

func checkStatus(rep *NodeAuditReport, name string) string {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c.Status
		}
	}
	return "(缺检查项 " + name + ")"
}
