package design

import (
	"fmt"
	"regexp"
	"strings"
)

// 走查的补充检查项：字体授权、图标与符号一致性、尺寸规范。
// 与 spec.go 的「字面值规范」并列，统一用 add(res, ...) 记账。

// symbolRe 覆盖 emoji、dingbat 与常见「当作图标用的字符符号」。
// 之所以要扫描它们：字符图标在不同平台字体覆盖不一致（Linux/CI 直接豆腐块），
// 且无法继承 Token 的描边与颜色，会破坏截图可比性。
var symbolRe = regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2190}-\x{21FF}\x{22EF}` +
	`\x{2300}-\x{23FF}\x{25A0}-\x{27BF}\x{2B00}-\x{2BFF}\x{2039}\x{203A}\x{FE0F}]`)

// appendFontCheck 检查字族串的商用与可复现性。
func appendFontCheck(res *AuditResult, tok Tokens) {
	fam := tok.Type.Family
	commercial, bad := StackCommercial(fam)
	reproducible, sysFonts := StackReproducible(fam)
	label := StackLicenseLabel(fam)
	switch {
	case !commercial:
		add(res, "fail", "字体授权",
			"含不可商用/未登记字体："+strings.Join(bad, "、")+"；改用 Inter / Noto Sans SC 等 OFL 字体")
	case len(sysFonts) > 0:
		add(res, "warn", "字体授权",
			"引用系统字体 "+strings.Join(sysFonts, "、")+"（不可嵌入、Android/Linux/CI 会退化）；"+
				"栈内已含开源兜底，授权："+label)
	case !reproducible:
		add(res, "warn", "字体授权", "字族不可跨平台复现，建议前置可嵌入字体；授权："+label)
	default:
		add(res, "pass", "字体授权", "可商用、可嵌入、可再分发（"+label+"）")
	}
}

// appendSizeCheck 检查尺寸规范：每个体系都必须能落到一块画板上。
func appendSizeCheck(res *AuditResult, sys *System) {
	ab := DefaultArtboardFor(sys.Family)
	spec := ab.Spec()
	if spec.ContentWidth <= 0 || spec.ContentHeight <= 0 {
		add(res, "fail", "尺寸规范", fmt.Sprintf("画板 %s 推导出非法内容区（%d×%d）",
			ab.ID, spec.ContentWidth, spec.ContentHeight))
		return
	}
	if ab.Margin%4 != 0 || ab.Gutter%4 != 0 {
		add(res, "warn", "尺寸规范", fmt.Sprintf("画板 %s 的边距 %d / 栅格 %d 不是 4 的倍数",
			ab.ID, ab.Margin, ab.Gutter))
		return
	}
	add(res, "pass", "尺寸规范", fmt.Sprintf("默认画板 %s %d×%d，内容区 %d×%d（边距 %d，栅格 %d×%d）",
		ab.ID, ab.Width, ab.Height, spec.ContentWidth, spec.ContentHeight, ab.Margin, ab.Columns, ab.Gutter))
}

// iconCorpus 收集全部会被渲染的 HTML/CSS 片段（供符号扫描）。
func iconCorpus(sys *System, theme ThemeKey) []struct{ Name, Text string } {
	corpus := []struct{ Name, Text string }{
		{"StyleSheet", StyleSheet(sys, theme)},
	}
	for _, c := range Components(sys) {
		if raw, err := componentTemplate(c.ID); err == nil {
			corpus = append(corpus, struct{ Name, Text string }{"组件 " + c.ID, ExpandIcons(raw)})
		}
	}
	bodies := map[string]string{
		"login": loginBody("演示"), "settings": settingsBody("演示"),
		"feed": feedBody("演示"), "profile": profileBody("演示"),
	}
	for id, body := range bodies {
		corpus = append(corpus, struct{ Name, Text string }{"模板 " + id, ExpandIcons(body)})
	}
	return corpus
}

// appendIconCheck 检查图标与符号一致性：
//  1. 渲染产物里不得残留 emoji / dingbat / 字符图标；
//  2. 模板引用的 {{icon:...}} 必须都能在图标库里解析（拼错会在展开时静默消失）。
func appendIconCheck(res *AuditResult, sys *System, theme ThemeKey) {
	var hits []string
	for _, doc := range iconCorpus(sys, theme) {
		for _, m := range symbolRe.FindAllString(doc.Text, -1) {
			hits = append(hits, fmt.Sprintf("%s 含 %q", doc.Name, m))
		}
	}

	var unknown []string
	for _, c := range Components(sys) {
		raw, err := componentTemplate(c.ID)
		if err != nil {
			continue
		}
		for _, name := range IconTokenNames(raw) {
			if !HasIcon(name) {
				unknown = append(unknown, c.ID+"→"+name)
			}
		}
	}

	switch {
	case len(unknown) > 0:
		add(res, "fail", "图标与符号", "模板引用了不存在的图标："+strings.Join(dedupe(unknown), "、"))
	case len(hits) > 0:
		add(res, "fail", "图标与符号", fmt.Sprintf("%d 处仍用字符当图标（示例：%s），改用 design_icons 的 SVG",
			len(hits), strings.Join(dedupe(hits)[:minInt(2, len(dedupe(hits)))], "；")))
	default:
		add(res, "pass", "图标与符号", fmt.Sprintf("全部图标来自内置 SVG 图标库（%d 个可用），无 emoji/字符图标", len(iconRegistry)))
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
