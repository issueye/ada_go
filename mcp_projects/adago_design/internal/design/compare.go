package design

import (
	"fmt"
	"sort"
	"strings"
)

// CompareSection 是一组 Token 的双侧对比结果。
type CompareSection struct {
	Key       string   `json:"key"`
	OnlyA     []string `json:"only_a,omitempty"`
	OnlyB     []string `json:"only_b,omitempty"`
	Changed   []string `json:"changed,omitempty"` // "key: a → b"
	SameCount int      `json:"same_count"`
}

// TypeChange 是一档字型的对比。
type TypeChange struct {
	Level string    `json:"level"`
	A     *TypeSpec `json:"a,omitempty"`
	B     *TypeSpec `json:"b,omitempty"`
	Same  bool      `json:"same"`
}

// CompareResult 是两套体系的全量对比。
type CompareResult struct {
	A       string         `json:"a"`
	B       string         `json:"b"`
	Theme   string         `json:"theme"`
	Colors  CompareSection `json:"colors"`
	Spacing CompareSection `json:"spacing"`
	Radius  CompareSection `json:"radius"`
	Shadows CompareSection `json:"shadows"`
	Motion  CompareSection `json:"motion"`
	Type    []TypeChange   `json:"type"`
}

// Compare 对比两套体系在指定主题下的 Token。
func Compare(aID, bID string, theme ThemeKey) (*CompareResult, error) {
	a, ok := GetSystem(SanitizeID(aID))
	if !ok {
		return nil, fmt.Errorf("unknown system %q", aID)
	}
	b, ok := GetSystem(SanitizeID(bID))
	if !ok {
		return nil, fmt.Errorf("unknown system %q", bID)
	}
	if a.ID == b.ID {
		return nil, fmt.Errorf("cannot compare system %q with itself", a.ID)
	}
	ta, _ := a.TokensFor(theme)
	tb, _ := b.TokensFor(theme)
	res := &CompareResult{A: a.ID, B: b.ID, Theme: string(theme)}
	res.Colors = compareMaps("color", ta.Colors, tb.Colors,
		func(v Color) string { return v.Value })
	res.Spacing = compareMaps("spacing", ta.Spacing, tb.Spacing,
		func(v int) string { return fmt.Sprintf("%dpx", v) })
	res.Radius = compareMaps("radius", ta.Radius, tb.Radius,
		func(v int) string { return fmt.Sprintf("%dpx", v) })
	res.Shadows = compareMaps("shadow", ta.Shadows, tb.Shadows,
		func(v string) string { return v })
	res.Motion = compareMaps("motion", ta.Motion, tb.Motion,
		func(v string) string { return v })
	res.Type = compareTypes(ta.Type, tb.Type)
	return res, nil
}

// compareMaps 通用 map 对比（vA/vB 经 to 字符串化）。
func compareMaps[V any](key string, ma, mb map[string]V, to func(V) string) CompareSection {
	sec := CompareSection{Key: key}
	names := map[string]bool{}
	for k := range ma {
		names[k] = true
	}
	for k := range mb {
		names[k] = true
	}
	all := make([]string, 0, len(names))
	for k := range names {
		all = append(all, k)
	}
	sort.Strings(all)
	for _, k := range all {
		va, inA := ma[k]
		vb, inB := mb[k]
		switch {
		case inA && !inB:
			sec.OnlyA = append(sec.OnlyA, k)
		case inB && !inA:
			sec.OnlyB = append(sec.OnlyB, k)
		case to(va) == to(vb):
			sec.SameCount++
		default:
			sec.Changed = append(sec.Changed, fmt.Sprintf("%s: %s → %s", k, to(va), to(vb)))
		}
	}
	return sec
}

// compareTypes 字型阶梯对比（并集）。
func compareTypes(ta, tb TypeScale) []TypeChange {
	names := map[string]bool{}
	for k := range ta.Levels {
		names[k] = true
	}
	for k := range tb.Levels {
		names[k] = true
	}
	all := make([]string, 0, len(names))
	for k := range names {
		all = append(all, k)
	}
	sort.Strings(all)
	out := make([]TypeChange, 0, len(all))
	for _, k := range all {
		a, inA := ta.Levels[k]
		b, inB := tb.Levels[k]
		tc := TypeChange{Level: k}
		if inA {
			cp := a
			tc.A = &cp
		}
		if inB {
			cp := b
			tc.B = &cp
		}
		tc.Same = inA && inB && a.Size == b.Size && a.Weight == b.Weight
		out = append(out, tc)
	}
	return out
}

// CompareSummary 生成给人读的对比摘要（差异键名一并列出，便于 Agent 直接引用）。
func (r *CompareResult) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "对比 %s vs %s（%s）：\n", r.A, r.B, r.Theme)
	sec := func(name string, s CompareSection, maxList int) {
		fmt.Fprintf(&sb, "- %s：相同 %d；仅 %s 有 %d；仅 %s 有 %d；不同 %d",
			name, s.SameCount, r.A, len(s.OnlyA), r.B, len(s.OnlyB), len(s.Changed))
		if len(s.Changed) > 0 {
			sb.WriteString("（")
			for i, c := range s.Changed {
				if i >= maxList {
					fmt.Fprintf(&sb, " 等 %d 项", len(s.Changed))
					break
				}
				if i > 0 {
					sb.WriteString("；")
				}
				sb.WriteString(c)
			}
			sb.WriteString("）")
		}
		sb.WriteString("\n")
	}
	sec("颜色", r.Colors, 6)
	sec("间距", r.Spacing, 4)
	sec("圆角", r.Radius, 6)
	sec("阴影", r.Shadows, 3)
	sec("动效", r.Motion, 3)
	same, diff := 0, 0
	for _, t := range r.Type {
		if t.Same {
			same++
		} else {
			diff++
		}
	}
	fmt.Fprintf(&sb, "- 字型：相同 %d 档；差异 %d 档", same, diff)
	return sb.String()
}

// CompareBoardHTML 生成双体系并排对比板（画布节点用，内联样式不依赖皮肤变量）。
func CompareBoardHTML(aID, bID string, theme ThemeKey) (string, int, error) {
	res, err := Compare(aID, bID, theme)
	if err != nil {
		return "", 0, err
	}
	sa, _ := GetSystem(res.A)
	sbSys, _ := GetSystem(res.B)
	ta, _ := sa.TokensFor(theme)
	tb, _ := sbSys.TokensFor(theme)

	var b strings.Builder
	b.WriteString(`<main class="cp-wrap"><header class="cp-head"><span class="cp-name">` +
		esc(sa.Name) + `</span><span class="cp-vs">vs</span><span class="cp-name">` + esc(sbSys.Name) +
		`</span><span class="cp-theme">` + esc(string(theme)) + `</span></header>`)

	// 主色与功能色并排
	b.WriteString(`<section class="cp-row"><div class="cp-col">`)
	writeSwatch(&b, "primary", sa, ta.Colors["primary"])
	for _, name := range []string{"success", "warning", "danger"} {
		if c, ok := ta.Colors[name]; ok {
			writeSwatch(&b, name, sa, c)
		}
	}
	b.WriteString(`</div><div class="cp-col">`)
	writeSwatch(&b, "primary", sbSys, tb.Colors["primary"])
	for _, name := range []string{"success", "warning", "danger"} {
		if c, ok := tb.Colors[name]; ok {
			writeSwatch(&b, name, sbSys, c)
		}
	}
	b.WriteString(`</div></section>`)

	// 圆角/间距 chips
	b.WriteString(`<section class="cp-row"><div class="cp-col">` + chipsOf(ta.Radius, "R") +
		`</div><div class="cp-col">` + chipsOf(tb.Radius, "R") + `</div></section>`)

	// 差异要点
	b.WriteString(`<section class="cp-diff">`)
	if len(res.Colors.Changed) > 0 {
		b.WriteString(`<div class="cp-diff-title">颜色差异（前 6）</div><ul>`)
		for i, c := range res.Colors.Changed {
			if i >= 6 {
				break
			}
			b.WriteString("<li>" + esc(c) + "</li>")
		}
		b.WriteString("</ul>")
	}
	if len(res.Radius.Changed) > 0 {
		b.WriteString(`<div class="cp-diff-title">圆角差异</div><ul>`)
		for _, c := range res.Radius.Changed {
			b.WriteString("<li>" + esc(c) + "</li>")
		}
		b.WriteString("</ul>")
	}
	b.WriteString(`</section></main>`)

	const boardCSS = `
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:"Segoe UI",system-ui,"PingFang SC","Microsoft YaHei",sans-serif;background:#101116;color:#e8e9ee;padding:16px;font-size:13px}
.cp-wrap{display:flex;flex-direction:column;gap:14px}
.cp-head{display:flex;align-items:baseline;gap:10px;flex-wrap:wrap}
.cp-name{font-size:15px;font-weight:700}
.cp-vs{color:#8a8fa0;font-size:12px}
.cp-theme{color:#8a8fa0;font-size:11px;margin-left:auto}
.cp-row{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.cp-col{border:1px solid #2a2b36;border-radius:10px;padding:10px;display:flex;flex-direction:column;gap:8px}
.cp-swatch{display:flex;align-items:center;gap:8px}
.cp-chip-color{width:34px;height:22px;border-radius:5px;flex:none;border:1px solid rgba(255,255,255,.15)}
.cp-swatch small{color:#8a8fa0;font-size:10px}
.cp-chips{display:flex;flex-wrap:wrap;gap:5px}
.cp-chip{font-size:10px;padding:2px 8px;border-radius:999px;background:#1d1e26;border:1px solid #2a2b36;color:#a0a3ad}
.cp-diff{border:1px solid #2a2b36;border-radius:10px;padding:10px}
.cp-diff-title{font-size:11px;font-weight:700;color:#a0a3ad;margin:6px 0 4px}
.cp-diff ul{margin-left:16px}
.cp-diff li{font-size:11px;color:#c9cdd4;margin:3px 0;word-break:break-all}
`
	doc := "<!DOCTYPE html>\n<html lang=\"zh-CN\" data-kind=\"compare\"><head><meta charset=\"utf-8\"><title>对比 " +
		esc(sa.ID) + " vs " + esc(sbSys.ID) + "</title><style>" + boardCSS + "</style></head>\n<body>\n" +
		b.String() + "\n</body>\n</html>\n"
	return doc, 420, nil
}

// writeSwatch 写一枚色板行。
func writeSwatch(b *strings.Builder, name string, sys *System, c Color) {
	if c.Value == "" {
		return
	}
	b.WriteString(`<div class="cp-swatch"><span class="cp-chip-color" style="background:` + esc(c.Value) + `"></span>` +
		`<span>` + esc(name) + ` <small>` + esc(c.Value) + `</small></span></div>`)
}

// chipsOf 把 int map 渲染成 chips。
func chipsOf(m map[string]int, prefix string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(`<div class="cp-chips">`)
	for _, k := range keys {
		b.WriteString(`<span class="cp-chip">` + esc(prefix+" "+k+" "+fmt.Sprint(m[k])) + `</span>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}
