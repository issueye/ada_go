package design

import (
	"sort"
	"strings"
)

// 字体注册表：把「字族字符串」升级为「带授权元数据的资产」。
//
// 三条硬规则（改动字体前必读）：
//  1. 预览文档禁止 @font-face 外链。srcdoc + foreignObject 截图取不到外链字体，
//     且会把授权问题带进交付物；需要品牌字体时只按本地 family 名引用。
//  2. 品牌字体永不自托管。MiSans 许可允许嵌入但禁止再分发字体文件（须注明使用）；
//     抖音美好体是 OFL 1.1，可自托管但须随附 LICENSE。据此区分 Embeddable 与
//     Redistributable 两个独立位。
//  3. 交付级设计必须 StackCommercial。系统字体（SF Pro / PingFang / Segoe UI）
//     可合法引用但不可嵌入、跨平台不可复现，只能作为尾部兜底，不能作为唯一来源。
type FontLicense string

// 授权类型。
const (
	LicenseOFL         FontLicense = "OFL-1.1"     // 可商用 / 可嵌入 / 可再分发（保留许可）
	LicenseApache      FontLicense = "Apache-2.0"  // 同上
	LicenseISC         FontLicense = "ISC"         // 同上
	LicenseVendorFree  FontLicense = "vendor-free" // 厂商免费商用：可嵌入，禁止再分发字体文件
	LicenseSystemOnly  FontLicense = "system-only" // 纯系统引用：不可嵌入、不可分发、跨端不可复现
	LicenseProprietary FontLicense = "proprietary" // 需单独购买授权
	LicenseUnknown     FontLicense = "unknown"     // 未登记，按不可商用处理
)

// FontSpec 是一款字体的元数据。
type FontSpec struct {
	ID              string      `json:"id"`
	Family          string      `json:"family"` // CSS family 名
	CSSName         string      `json:"-"`      // 需要输出多个关键字的条目（如 -apple-system）
	Script          string      `json:"script"` // latin | hans | mono
	License         FontLicense `json:"license"`
	Commercial      bool        `json:"commercial"`
	Embeddable      bool        `json:"embeddable"`
	Redistributable bool        `json:"redistributable"`
	Attribution     bool        `json:"attribution"` // 需注明使用 / 保留版权声明
	Weights         []int       `json:"weights,omitempty"`
	Homepage        string      `json:"homepage,omitempty"`
}

var fontRegistry = map[string]FontSpec{}

func registerFont(f FontSpec) {
	if f.ID == "" || f.Family == "" {
		panic("design: font id/family is empty")
	}
	if _, dup := fontRegistry[f.ID]; dup {
		panic("design: duplicate font id " + f.ID)
	}
	fontRegistry[f.ID] = f
}

func init() {
	// 开源可商用（可嵌入、可再分发）——默认栈的构成
	registerFont(FontSpec{ID: "inter", Family: "Inter", Script: "latin", License: LicenseOFL,
		Commercial: true, Embeddable: true, Redistributable: true, Weights: []int{400, 500, 600, 700},
		Homepage: "https://rsms.me/inter/ · SIL OFL 1.1"})
	registerFont(FontSpec{ID: "noto-sans-sc", Family: "Noto Sans SC", Script: "hans", License: LicenseOFL,
		Commercial: true, Embeddable: true, Redistributable: true, Weights: []int{400, 500, 700},
		Homepage: "https://fonts.google.com/noto · SIL OFL 1.1"})
	registerFont(FontSpec{ID: "jetbrains-mono", Family: "JetBrains Mono", Script: "mono", License: LicenseOFL,
		Commercial: true, Embeddable: true, Redistributable: true, Weights: []int{400, 500},
		Homepage: "https://www.jetbrains.com/lp/mono/ · SIL OFL 1.1"})
	registerFont(FontSpec{ID: "roboto", Family: "Roboto", Script: "latin", License: LicenseApache,
		Commercial: true, Embeddable: true, Redistributable: true, Weights: []int{400, 500, 700},
		Homepage: "https://fonts.google.com/specimen/Roboto · Apache-2.0"})

	// 品牌字体（免费商用，但再分发条件不同）
	registerFont(FontSpec{ID: "douyin-sans", Family: "Douyin Sans", Script: "hans", License: LicenseOFL,
		Commercial: true, Embeddable: true, Redistributable: true, Weights: []int{400, 700},
		Homepage: "抖音美好体 · SIL OFL 1.1（可商用可嵌入，禁止单独售卖字体文件）"})
	registerFont(FontSpec{ID: "misans", Family: "MiSans", Script: "hans", License: LicenseVendorFree,
		Commercial: true, Embeddable: true, Redistributable: false, Attribution: true, Weights: []int{400, 500, 700},
		Homepage: "MiSans 字体知识产权许可协议：免费商用、可嵌入，但禁止再分发字体文件，须注明使用"})
	registerFont(FontSpec{ID: "harmonyos-sans", Family: "HarmonyOS Sans SC", Script: "hans", License: LicenseVendorFree,
		Commercial: true, Embeddable: true, Redistributable: false, Weights: []int{400, 500, 700},
		Homepage: "HarmonyOS Sans · 华为免费商用字体"})

	// 系统字体：可合法引用（Commercial=true），但不可嵌入、不可分发、跨端不可复现
	registerFont(FontSpec{ID: "apple-system", Family: "-apple-system", CSSName: "-apple-system, BlinkMacSystemFont",
		Script: "latin", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "sf-pro", Family: "SF Pro Text", Script: "latin", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "pingfang", Family: "PingFang SC", Script: "hans", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "segoe-ui", Family: "Segoe UI", Script: "latin", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "microsoft-yahei", Family: "Microsoft YaHei", Script: "hans", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "menlo", Family: "Menlo", Script: "mono", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "consolas", Family: "Consolas", Script: "mono", License: LicenseSystemOnly, Commercial: true})
	registerFont(FontSpec{ID: "sf-mono", Family: "SFMono-Regular", Script: "mono", License: LicenseSystemOnly, Commercial: true})

	// 专有字体：需购买授权，不得随稿交付
	registerFont(FontSpec{ID: "helvetica-neue", Family: "Helvetica Neue", Script: "latin", License: LicenseProprietary})
	registerFont(FontSpec{ID: "helvetica", Family: "Helvetica", Script: "latin", License: LicenseProprietary})
	registerFont(FontSpec{ID: "arial", Family: "Arial", Script: "latin", License: LicenseProprietary})
}

// DefaultStacks 是商业友好的默认栈：拉丁 Inter（OFL）+ 中文 Noto Sans SC（OFL）
// + 系统兜底。三者均可商用、可嵌入、可再分发，是全平台可复现的最小集。
var DefaultStacks = map[string]string{
	"latin": `Inter, "Noto Sans SC", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`,
	"hans":  `"Noto Sans SC", Inter, system-ui, "PingFang SC", "Microsoft YaHei", sans-serif`,
	"mono":  `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`,
}

// DefaultStack 按脚本取默认栈，未知脚本回落 latin。
func DefaultStack(script string) string {
	if s, ok := DefaultStacks[script]; ok {
		return s
	}
	return DefaultStacks["latin"]
}

var genericFamilies = map[string]bool{
	"sans-serif": true, "serif": true, "monospace": true, "cursive": true, "fantasy": true,
	"system-ui": true, "ui-monospace": true, "ui-sans-serif": true, "ui-serif": true,
	"ui-rounded": true, "emoji": true, "math": true, "fangsong": true, "inherit": true,
}

// quoteFamily 给含空格的 family 加引号。
func quoteFamily(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	if strings.ContainsAny(n, ` "'`) && !strings.HasPrefix(n, `"`) {
		return `"` + n + `"`
	}
	return n
}

// Stack 按字体 ID 顺序拼装 CSS font-family，并追加开源兜底。
// 未登记的 ID 会被忽略（避免把拼错的字体名写进产物）。
func Stack(fontIDs ...string) string {
	parts := make([]string, 0, len(fontIDs)+3)
	seen := map[string]bool{}
	add := func(css string) {
		key := strings.ToLower(css)
		if css == "" || seen[key] {
			return
		}
		seen[key] = true
		parts = append(parts, css)
	}
	for _, id := range fontIDs {
		f, ok := fontRegistry[id]
		if !ok {
			continue
		}
		if f.CSSName != "" {
			for _, one := range strings.Split(f.CSSName, ",") {
				add(quoteFamily(one))
			}
			continue
		}
		add(quoteFamily(f.Family))
	}
	// 兜底：开源可商用 + 系统 + generic
	for _, tail := range []string{"Inter", `"Noto Sans SC"`, "system-ui", "sans-serif"} {
		add(tail)
	}
	return strings.Join(parts, ", ")
}

// Fonts 返回全部已登记字体（按 script → id 排序）。
func Fonts() []FontSpec {
	out := make([]FontSpec, 0, len(fontRegistry))
	for _, f := range fontRegistry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Script != out[j].Script {
			return out[i].Script < out[j].Script
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// GetFont 按 ID 查找字体。
func GetFont(id string) (FontSpec, bool) {
	f, ok := fontRegistry[strings.ToLower(strings.TrimSpace(id))]
	return f, ok
}

// ResolveStack 解析一条 CSS font-family 串，逐项标注授权。
// 泛型关键字（sans-serif / system-ui 等）不返回，视为安全兜底。
func ResolveStack(family string) []FontSpec {
	out := make([]FontSpec, 0, 6)
	seen := map[string]bool{}
	for _, raw := range strings.Split(family, ",") {
		name := strings.Trim(strings.TrimSpace(raw), `"'`)
		key := strings.ToLower(name)
		if name == "" || genericFamilies[key] || seen[key] {
			continue
		}
		seen[key] = true
		if f, ok := lookupFontByName(key); ok {
			out = append(out, f)
			continue
		}
		out = append(out, FontSpec{ID: "unknown:" + key, Family: name, License: LicenseUnknown})
	}
	return out
}

// lookupFontByName 按字族名（含 CSS 关键字别名）反查注册表。
func lookupFontByName(lower string) (FontSpec, bool) {
	for _, f := range fontRegistry {
		if strings.ToLower(f.Family) == lower {
			return f, true
		}
	}
	switch lower {
	case "-apple-system", "blinkmacsystemfont", "apple-system":
		return fontRegistry["apple-system"], true
	case "sf pro", "sf pro text", "sf pro display":
		return fontRegistry["sf-pro"], true
	case "ui-monospace":
		return fontRegistry["jetbrains-mono"], true
	case "sfmono-regular":
		return fontRegistry["sf-mono"], true
	case "msyh":
		return fontRegistry["microsoft-yahei"], true
	}
	return FontSpec{}, false
}

// StackCommercial 判断整条栈是否可商用；返回不可商用的字体名。
func StackCommercial(family string) (bool, []string) {
	var bad []string
	for _, f := range ResolveStack(family) {
		if !f.Commercial {
			bad = append(bad, f.Family)
		}
	}
	return len(bad) == 0, bad
}

// StackReproducible 判断整条栈是否跨平台可复现：一旦出现 system-only 字体
// （SF Pro / PingFang / Segoe UI / 微软雅黑），在 Android/Linux/CI 上必然退化。
// 仅当它位于栈尾兜底、且前面已有可嵌入字体时才算可接受 —— 这里给出完整清单，
// 由调用方（走查）决定严重程度。
func StackReproducible(family string) (bool, []string) {
	var sys []string
	for _, f := range ResolveStack(family) {
		if f.License == LicenseSystemOnly {
			sys = append(sys, f.Family)
		}
	}
	return len(sys) == 0, sys
}

// StackLicenseLabel 归纳一条栈的授权标签（用于产物注释与走查展示）。
func StackLicenseLabel(family string) string {
	specs := ResolveStack(family)
	if len(specs) == 0 {
		return "generic"
	}
	counts := map[FontLicense]int{}
	for _, f := range specs {
		counts[f.License]++
	}
	order := []FontLicense{LicenseOFL, LicenseApache, LicenseISC, LicenseVendorFree, LicenseSystemOnly, LicenseProprietary, LicenseUnknown}
	parts := make([]string, 0, len(counts))
	for _, lic := range order {
		if counts[lic] > 0 {
			parts = append(parts, string(lic))
		}
	}
	return strings.Join(parts, " + ")
}
