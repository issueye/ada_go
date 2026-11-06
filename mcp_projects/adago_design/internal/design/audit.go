package design

import (
	"fmt"
	"sort"
	"strings"
)

// 设计走查：对比度（WCAG）、字号下限、Token 完备性等自动检查。

// AuditCheck 是一项走查结果。
type AuditCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // pass | warn | fail
	Detail   string `json:"detail"`
	Severity string `json:"severity,omitempty"` // aa | aaa（对比度项）
}

// AuditResult 是一份走查报告。
type AuditResult struct {
	System  string       `json:"system"`
	Theme   string       `json:"theme"`
	Score   int          `json:"score"` // 0-100
	Passed  int          `json:"passed"`
	Warned  int          `json:"warned"`
	Failed  int          `json:"failed"`
	Checks  []AuditCheck `json:"checks"`
	Summary string       `json:"summary"`
}

// Audit 对某体系某主题做设计走查。
func Audit(systemID string, theme ThemeKey) (*AuditResult, error) {
	sys, ok := GetSystem(SanitizeID(systemID))
	if !ok {
		return nil, fmt.Errorf("unknown system %q", systemID)
	}
	if theme == "" {
		theme = ThemeLight
	}
	if !sys.HasTheme(theme) {
		return nil, fmt.Errorf("system %q has no %s theme", sys.ID, theme)
	}
	tok, eff := sys.TokensFor(theme)
	res := &AuditResult{System: sys.ID, Theme: string(eff), Checks: []AuditCheck{}}
	add := func(status, name, detail string, severity ...string) AuditCheck {
		c := AuditCheck{Name: name, Status: status, Detail: detail}
		if len(severity) > 0 {
			c.Severity = severity[0]
		}
		res.Checks = append(res.Checks, c)
		switch status {
		case "pass":
			res.Passed++
		case "warn":
			res.Warned++
		default:
			res.Failed++
		}
		return c
	}

	// 1. 关键文本对对比度（WCAG AA 正文 ≥4.5，大字/UI ≥3.0）
	bgKey := firstExisting(tok.Colors, "bg", "system-background", "surface", "bg-1")
	bgVal := "#FFFFFF"
	if bgKey != "" {
		bgVal = tok.Colors[bgKey].Value
	}
	type pair struct{ fgKey, bgKey, role string }
	pairs := []pair{}
	for _, fg := range []string{"text-1", "text-primary", "label", "ink"} {
		if _, ok := tok.Colors[fg]; ok {
			pairs = append(pairs, pair{fg, bgKey, "一级文本"})
			break
		}
	}
	for _, fg := range []string{"text-2", "text-regular", "secondary-label"} {
		if _, ok := tok.Colors[fg]; ok {
			pairs = append(pairs, pair{fg, bgKey, "二级文本"})
			break
		}
	}
	for _, fg := range []string{"text-3", "text-secondary", "text-placeholder"} {
		if _, ok := tok.Colors[fg]; ok {
			pairs = append(pairs, pair{fg, bgKey, "三级/占位文本"})
			break
		}
	}
	if onP, ok := tok.Colors["on-primary"]; ok {
		_ = onP
		pairs = append(pairs, pair{"on-primary", "primary", "主按钮文字"})
	} else if inv, ok := tok.Colors["text-inverse"]; ok {
		_ = inv
		pairs = append(pairs, pair{"text-inverse", "primary", "主按钮文字"})
	} else {
		pairs = append(pairs, pair{"primary", bgKey, "主色与底（按钮可见性）"})
	}
	for _, p := range pairs {
		fgVal := tok.Colors[p.fgKey].Value
		bg2 := bgVal
		if p.bgKey != "" {
			if c, ok := tok.Colors[p.bgKey]; ok {
				bg2 = c.Value
			}
		}
		cr, ok := ContrastRatio(fgVal, bg2)
		if !ok {
			add("warn", "对比度 · "+p.role, fmt.Sprintf("%s 无法解析", fgVal))
			continue
		}
		role := p.role
		switch {
		case strings.Contains(role, "主按钮"):
			if cr >= 3.0 {
				add("pass", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1（UI 组件 ≥3.0）", p.fgKey, p.bgKey, cr), "aa")
			} else {
				add("fail", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1，低于 UI 组件 3.0", p.fgKey, p.bgKey, cr), "aa")
			}
		case strings.Contains(role, "三级"):
			if cr >= 3.0 {
				add("pass", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1（辅助文本 ≥3.0，建议 4.5）", p.fgKey, p.bgKey, cr), "aa")
			} else {
				add("warn", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1，辅助文本建议 ≥3.0", p.fgKey, p.bgKey, cr), "aa")
			}
		default:
			if cr >= 4.5 {
				add("pass", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1（正文 AA ≥4.5）", p.fgKey, p.bgKey, cr), "aa")
			} else if cr >= 3.0 {
				add("warn", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1，仅满足大字（AA 正文需 4.5）", p.fgKey, p.bgKey, cr), "aa")
			} else {
				add("fail", "对比度 · "+role, fmt.Sprintf("%s on %s = %.2f:1，不可读", p.fgKey, p.bgKey, cr), "aa")
			}
		}
	}

	// 2. 字号下限
	minSize, minLevel := 999, ""
	levels := sortedStrKeys(tok.Type.Levels)
	for _, lv := range levels {
		s := tok.Type.Levels[lv].Size
		if s < minSize {
			minSize, minLevel = s, lv
		}
	}
	if minSize >= 12 {
		add("pass", "字号下限", fmt.Sprintf("最小档 %s = %dpx（≥12）", minLevel, minSize))
	} else {
		add("fail", "字号下限", fmt.Sprintf("最小档 %s = %dpx，低于 12px 可读底线", minLevel, minSize))
	}

	// 3. 圆角阶梯
	radiusKeys := sortedStrKeys(tok.Radius)
	if len(radiusKeys) >= 3 {
		add("pass", "圆角阶梯", fmt.Sprintf("%d 档（%s）", len(radiusKeys), strings.Join(radiusKeys, "/")))
	} else {
		add("warn", "圆角阶梯", fmt.Sprintf("仅 %d 档，建议至少 small/medium/large", len(radiusKeys)))
	}

	// 4. 状态色完备
	missing := []string{}
	for _, sem := range []string{"success", "warning", "danger"} {
		found := false
		for k := range tok.Colors {
			if strings.HasPrefix(strings.ToLower(k), sem) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, sem)
		}
	}
	if len(missing) == 0 {
		add("pass", "状态色完备", "success / warning / danger 均已定义")
	} else {
		add("fail", "状态色完备", "缺少："+strings.Join(missing, "/"))
	}

	// 5. 语义色数量
	if len(tok.Colors) >= 10 {
		add("pass", "语义色规模", fmt.Sprintf("%d 个语义色", len(tok.Colors)))
	} else {
		add("warn", "语义色规模", fmt.Sprintf("仅 %d 个语义色，覆盖可能不足", len(tok.Colors)))
	}

	// 6. 间距阶梯
	spKeys := sortedIntKeys(tok.Spacing)
	if len(spKeys) >= 5 {
		add("pass", "间距阶梯", fmt.Sprintf("%d 档（%s）", len(spKeys), strings.Join(spKeys, "/")))
	} else {
		add("warn", "间距阶梯", fmt.Sprintf("仅 %d 档", len(spKeys)))
	}

	// 7. 组件 CSS 字面值规范（padding/margin/gap/border-radius 必须落在 Token 阶梯）
	appendSpecCheck(res, sys, eff)

	// 评分：fail -18，warn -6
	res.Score = 100 - res.Failed*18 - res.Warned*6
	if res.Score < 0 {
		res.Score = 0
	}
	sort.Slice(res.Checks, func(i, j int) bool {
		rank := map[string]int{"fail": 0, "warn": 1, "pass": 2}
		return rank[res.Checks[i].Status] < rank[res.Checks[j].Status]
	})
	res.Summary = fmt.Sprintf("%s（%s）走查：得分 %d/100，通过 %d，警告 %d，失败 %d。",
		sys.Name, eff, res.Score, res.Passed, res.Warned, res.Failed)
	return res, nil
}

// firstExisting 按顺序取第一个存在的键。
func firstExisting(m map[string]Color, keys ...string) string {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return k
		}
	}
	return ""
}

// AuditBoardHTML 把走查报告渲染成画布节点文档（内联样式）。
func AuditBoardHTML(res *AuditResult) string {
	var b strings.Builder
	b.WriteString(`<main class="au-wrap"><header class="au-head"><span class="au-name">` + esc(res.System) +
		`</span><span class="au-theme">` + esc(res.Theme) + `</span><span class="au-score s` + fmt.Sprint(scoreBand(res.Score)) + `">` +
		fmt.Sprint(res.Score) + `</span></header>`)
	b.WriteString(`<div class="au-stats">通过 ` + fmt.Sprint(res.Passed) + ` · 警告 ` + fmt.Sprint(res.Warned) + ` · 失败 ` + fmt.Sprint(res.Failed) + `</div><section class="au-list">`)
	for _, c := range res.Checks {
		cls := map[string]string{"pass": "ok", "warn": "wn", "fail": "bad"}[c.Status]
		b.WriteString(`<div class="au-item ` + cls + `"><span class="au-badge">` + esc(statusText(c.Status)) +
			`</span><div><div class="au-item-name">` + esc(c.Name) + `</div><div class="au-item-detail">` + esc(c.Detail) + `</div></div></div>`)
	}
	b.WriteString(`</section></main>`)
	const css = `
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:"Segoe UI",system-ui,"PingFang SC","Microsoft YaHei",sans-serif;background:#101116;color:#e8e9ee;padding:14px;font-size:12px}
.au-wrap{display:flex;flex-direction:column;gap:10px}
.au-head{display:flex;align-items:center;gap:10px}
.au-name{font-size:15px;font-weight:700}
.au-theme{color:#8a8fa0;font-size:11px}
.au-score{margin-left:auto;width:44px;height:44px;border-radius:12px;display:flex;align-items:center;justify-content:center;font-size:18px;font-weight:800}
.au-score.s2{background:#2F7D5B;color:#fff}
.au-score.s1{background:#B58A2E;color:#fff}
.au-score.s0{background:#FE2C55;color:#fff}
.au-stats{color:#8a8fa0;font-size:11px}
.au-list{display:flex;flex-direction:column;gap:6px}
.au-item{display:flex;gap:8px;align-items:flex-start;border:1px solid #2a2b36;border-radius:9px;padding:7px 9px}
.au-item.ok{border-left:3px solid #2F7D5B}
.au-item.wn{border-left:3px solid #B58A2E}
.au-item.bad{border-left:3px solid #FE2C55}
.au-badge{flex:none;font-size:10px;padding:1px 8px;border-radius:999px;background:#1d1e26;color:#a0a3ad;margin-top:1px}
.au-item-name{font-weight:600;font-size:12px}
.au-item-detail{color:#a0a3ad;font-size:11px;margin-top:2px;word-break:break-all}
`
	return "<!DOCTYPE html>\n<html lang=\"zh-CN\" data-kind=\"audit\"><head><meta charset=\"utf-8\"><title>走查 " +
		esc(res.System) + "</title><style>" + css + "</style></head>\n<body>\n" + b.String() + "\n</body>\n</html>\n"
}

func scoreBand(score int) int {
	switch {
	case score >= 85:
		return 2
	case score >= 60:
		return 1
	}
	return 0
}

func statusText(s string) string {
	switch s {
	case "pass":
		return "通过"
	case "warn":
		return "警告"
	}
	return "失败"
}
