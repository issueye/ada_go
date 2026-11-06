package design

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 组件 CSS 强制规范扫描：字面 px 值必须落在 Token 阶梯上（或白名单发丝值）。

var specDeclRe = regexp.MustCompile(`(?m)(padding[a-z-]*|margin[a-z-]*|gap|border-radius|font-size)\s*:\s*([^;}]+)`)
var pxNumRe = regexp.MustCompile(`(\d+(?:\.\d+)?)px`)

// SpecIssue 是一处偏离规范阶梯的字面值。
type SpecIssue struct {
	Prop    string  `json:"prop"`    // padding / border-radius ...
	Value   float64 `json:"value"`   // 字面 px 值
	Nearest int     `json:"nearest"` // 最近的规范值
	Kind    string  `json:"kind"`    // spacing | radius
}

// 发丝值白名单：0-3px 属于常规发丝/微调，不视为违规。
var hairline = map[float64]bool{0: true, 1: true, 2: true, 3: true}

// nearestIn 在集合中找最接近的值。
func nearestIn(v float64, set []int) int {
	best, bestDiff := 0, 1<<30
	for _, s := range set {
		if d := int(math.Abs(v - float64(s))); d < bestDiff {
			best, bestDiff = s, d
		}
	}
	return best
}

// SpecConformance 扫描某体系样式表中 padding/margin/gap/border-radius 的
// 字面 px 值，返回偏离 Token 阶梯的清单（var(--ds-*) 引用视为合规）。
func SpecConformance(sys *System, theme ThemeKey) []SpecIssue {
	return scanCSS(sys, theme, StyleSheet(sys, theme))
}

// scanCSS 是 SpecConformance 的实现体，css 参数便于测试注入。
func scanCSS(sys *System, theme ThemeKey, css string) []SpecIssue {
	tokens, _ := sys.TokensFor(theme)
	spacingVals := make([]int, 0, len(tokens.Spacing)+2)
	spacingVals = append(spacingVals, 0)
	for _, v := range tokens.Spacing {
		spacingVals = append(spacingVals, v)
	}
	sort.Ints(spacingVals)
	radiusVals := make([]int, 0, len(tokens.Radius)+2)
	radiusVals = append(radiusVals, 0)
	for _, v := range tokens.Radius {
		radiusVals = append(radiusVals, v)
	}
	sort.Ints(radiusVals)
	typeVals := make([]int, 0, len(tokens.Type.Levels))
	for _, spec := range tokens.Type.Levels {
		typeVals = append(typeVals, spec.Size)
	}
	sort.Ints(typeVals)

	seen := map[string]bool{}
	issues := make([]SpecIssue, 0, 8)
	for _, m := range specDeclRe.FindAllStringSubmatch(css, -1) {
		prop := strings.ToLower(m[1])
		kind := "spacing"
		if strings.HasPrefix(prop, "border-radius") {
			kind = "radius"
		}
		if prop == "font-size" {
			kind = "type"
		}
		// var(--ds-space-*/var(--ds-radius-*) 驱动的值天然合规，跳过（含兜底值）
		if kind == "spacing" && strings.Contains(m[2], "var(--ds-space") {
			continue
		}
		if kind == "radius" && strings.Contains(m[2], "var(--ds-radius") {
			continue
		}
		if kind == "type" && (strings.Contains(m[2], "inherit") || strings.Contains(m[2], "var(--ds-fs")) {
			continue
		}
		for _, pm := range pxNumRe.FindAllStringSubmatch(m[2], -1) {
			v, _ := strconv.ParseFloat(pm[1], 64)
			key := kind + "|" + strconv.FormatFloat(v, 'f', -1, 64)
			if seen[key] {
				continue
			}
			seen[key] = true
			whitelist := map[float64]bool{}
			if kind == "spacing" || kind == "radius" {
				whitelist = hairline
			}
			if kind == "radius" {
				whitelist[100] = true
				whitelist[999] = true
			}
			if whitelist[v] {
				continue
			}
			var vals []int
			if kind == "spacing" {
				vals = spacingVals
			} else if kind == "radius" {
				vals = radiusVals
			} else {
				vals = typeVals
			}
			nearest := nearestIn(v, vals)
			if int(v) == nearest {
				continue // 恰好落在阶梯上
			}
			issues = append(issues, SpecIssue{
				Prop: prop, Value: v, Nearest: nearest, Kind: kind,
			})
		}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Kind != issues[j].Kind {
			return issues[i].Kind < issues[j].Kind
		}
		return issues[i].Value < issues[j].Value
	})
	return issues
}

// appendSpecCheck 把组件规范检查加入走查结果。
func appendSpecCheck(res *AuditResult, sys *System, theme ThemeKey) {
	issues := SpecConformance(sys, theme)
	switch {
	case len(issues) == 0:
		add(res, "pass", "组件规范（字面值）", "padding/margin/gap/border-radius 字面值全部落在 Token 阶梯上")
	case len(issues) <= 4:
		add(res, "warn", "组件规范（字面值）", fmt.Sprintf("%d 处字面值偏离阶梯（示例: %s），建议归一", len(issues), specIssueText(issues[:1])))
	default:
		add(res, "fail", "组件规范（字面值）", fmt.Sprintf("%d 处字面值偏离阶梯（示例: %s）", len(issues), specIssueText(issues[:2])))
	}
}

func specIssueText(issues []SpecIssue) string {
	parts := make([]string, 0, len(issues))
	for _, is := range issues {
		parts = append(parts, fmt.Sprintf("%s %gpx→%dpx", is.Prop, is.Value, is.Nearest))
	}
	return strings.Join(parts, "；")
}

func add(res *AuditResult, status, name, detail string, severity ...string) AuditCheck {
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
