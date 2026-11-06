package design

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 品牌派生与自定义体系注册。

var validHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// DeriveInput 是品牌派生的入参。
type DeriveInput struct {
	Primary      string // 品牌主色（#RRGGBB）
	Name         string // 体系名称（如「山茶茶饮」）
	BaseSystemID string // 结构基底体系（颜色之外的全部 Token 继承它）
}

// Derive 以品牌主色重着色一个基底体系，生成自定义设计体系：
// 颜色之外（字型/间距/圆角/阴影/动效/组件/守则结构）全部继承基底，
// 品牌色生成 10 阶色板，语义色/中性色按色相协调派生。
func Derive(in DeriveInput) (*System, error) {
	primary := strings.TrimSpace(in.Primary)
	if !validHex.MatchString(primary) {
		return nil, fmt.Errorf("primary 必须是 #RRGGBB 格式，得到 %q", in.Primary)
	}
	base, ok := GetSystem(SanitizeID(in.BaseSystemID))
	if !ok {
		return nil, fmt.Errorf("unknown base system %q", in.BaseSystemID)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "品牌 " + strings.ToUpper(primary)
	}
	scale, ok := brandScale(primary)
	if !ok {
		return nil, fmt.Errorf("品牌色 %q 无法解析", primary)
	}
	// 语义色相（success/warning/danger/info）
	semHues := map[string]float64{"success": 0.394, "warning": 0.10, "danger": 0.011}
	infoHue := 0.60 // 默认蓝
	if bh, _, _, ok := hexToHSL(primary); ok && bh >= 0.55 && bh <= 0.72 {
		infoHue = bh // 品牌本身就是蓝系，info 跟随
	}

	id := customID(name, primary)
	sys := &System{
		ID:          id,
		Name:        name,
		Vendor:      "自定义 · 品牌派生",
		Family:      base.Family,
		Description: fmt.Sprintf("基于 %s 结构派生的品牌体系，主色 %s。", base.Name, strings.ToUpper(primary)),
		Style: StyleNotes{
			Summary:   base.Style.Summary,
			Keywords:  append([]string{"品牌派生 " + strings.ToUpper(primary)}, base.Style.Keywords...),
			Density:   base.Style.Density,
			Elevation: base.Style.Elevation,
		},
		Themes:    base.Themes,
		Guideline: append([]string{fmt.Sprintf("品牌主色 %s（%s 阶色板 1-10 可用），由 %s 结构派生。", strings.ToUpper(primary), "10", base.Name)}, base.Guideline...),
	}
	sys.Light = recolorTokens(base.Light, scale, semHues, infoHue, false)
	// 派生品牌体系不继承基底品牌字体（否则「某茶饮品牌稿莫名用抖音字体」），
	// 回到可商用可嵌入的默认栈。
	sys.Light.Type.Family = DefaultStacks["latin"]
	if base.Dark != nil {
		dk := recolorTokens(*base.Dark, scale, semHues, infoHue, true)
		dk.Type.Family = DefaultStacks["latin"]
		sys.Dark = &dk
	}
	return sys, nil
}

// recolorTokens 按键名规则重着色一份 Token。
func recolorTokens(t Tokens, scale []string, semHues map[string]float64, infoHue float64, dark bool) Tokens {
	out := t
	out.Colors = make(map[string]Color, len(t.Colors))
	// 中性灰底色（带品牌色相的低饱和灰）
	h, _, _, _ := hexToHSL(scale[5])
	neutral := func(l float64) string { return hslToHex(h, 0.08, l) }
	neutralDark := func(l float64) string { return hslToHex(h, 0.10, l) }
	for key, old := range t.Colors {
		k := strings.ToLower(key)
		semValue := func(hue float64, base string) string {
			baseL := 0.42
			if _, _, bl, ok := hexToHSL(base); ok {
				baseL = clampL(bl, 0.38, 0.5)
			}
			v := hslToHex(hue, 0.62, baseL)
			if dark {
				v = hslToHex(hue, 0.55, 0.55)
			}
			return v
		}
		semBg := func(hue float64) string {
			if dark {
				return hslToHex(hue, 0.45, 0.14)
			}
			return hslToHex(hue, 0.72, 0.92)
		}
		newVal, derived := "", false
		switch {
		case k == "primary" || k == "system-blue" || k == "accent-blue" || k == "brand":
			newVal, derived = scale[5], true
		case k == "on-primary" || k == "on-primary-container":
			newVal, derived = contrastOn(scale[5], dark), true
		case strings.HasPrefix(k, "primary") && (strings.Contains(k, "hover") || strings.Contains(k, "light")):
			newVal, derived = scale[4], true
		case strings.HasPrefix(k, "primary") && (strings.Contains(k, "active") || strings.Contains(k, "dark")):
			newVal, derived = scale[6], true
		case strings.HasPrefix(k, "primary") && strings.Contains(k, "bg"):
			newVal, derived = scale[0], true
		case strings.HasPrefix(k, "primary") && strings.Contains(k, "container"):
			newVal, derived = scale[1], true
		case k == "accent" || k == "secondary" || k == "tertiary":
			newVal, derived = hslToHex(h+0.033, 0.55, clampL(0.52, 0.3, 0.6)), true
		case strings.HasPrefix(k, "success") && strings.HasSuffix(k, "-bg"):
			newVal, derived = semBg(semHues["success"]), true
		case strings.HasPrefix(k, "success"):
			newVal, derived = semValue(semHues["success"], old.Value), true
		case strings.HasPrefix(k, "warning") && strings.HasSuffix(k, "-bg"):
			newVal, derived = semBg(semHues["warning"]), true
		case strings.HasPrefix(k, "warning"):
			newVal, derived = semValue(semHues["warning"], old.Value), true
		case (strings.HasPrefix(k, "danger") || strings.HasPrefix(k, "error")) && strings.HasSuffix(k, "-bg"):
			newVal, derived = semBg(semHues["danger"]), true
		case strings.HasPrefix(k, "danger") || strings.HasPrefix(k, "error"):
			newVal, derived = semValue(semHues["danger"], old.Value), true
		case strings.HasPrefix(k, "info") && strings.HasSuffix(k, "-bg"):
			newVal, derived = semBg(infoHue), true
		case strings.HasPrefix(k, "info"):
			newVal, derived = scale[5], true
		case k == "ink":
			// 抖音墨黑：浅色主题作文本/深底，深色主题仍保持深色（toast 底）
			if dark {
				newVal, derived = neutralDark(0.12), true
			} else {
				newVal, derived = neutral(0.13), true
			}
		case strings.HasPrefix(k, "text") || strings.HasPrefix(k, "label") || strings.HasPrefix(k, "on-surface"):
			lv := 0.74
			switch {
			case strings.Contains(k, "1") || strings.HasSuffix(k, "primary") || strings.Contains(k, "title"):
				lv = 0.13
			case strings.Contains(k, "2") || strings.Contains(k, "secondary") || strings.Contains(k, "regular"):
				lv = 0.30
			case strings.Contains(k, "3") || strings.Contains(k, "placeholder"):
				lv = 0.52
			}
			if dark {
				newVal, derived = neutralDark(clampL(1-lv, 0.1, 0.95)), true
			} else {
				newVal, derived = neutral(lv), true
			}
		case strings.HasPrefix(k, "border") || k == "separator":
			if dark {
				newVal, derived = neutralDark(0.22), true
			} else {
				newVal, derived = neutral(0.84), true
			}
		case strings.Contains(k, "fill") || strings.Contains(k, "bg-2") || strings.Contains(k, "bg-3") || strings.Contains(k, "secondary-system-background") || strings.Contains(k, "tertiary-system-background") || strings.Contains(k, "surface-container"):
			if dark {
				newVal, derived = neutralDark(0.13), true
			} else {
				newVal, derived = neutral(0.955), true
			}
		case k == "bg" || k == "system-background" || k == "bg-1" || strings.Contains(k, "page"):
			if dark {
				newVal, derived = neutralDark(0.07), true
			} else {
				newVal, derived = neutral(0.975), true
			}
		case k == "surface" || strings.Contains(k, "surface") || strings.Contains(k, "container"):
			if dark {
				newVal, derived = neutralDark(0.12), true
			} else {
				newVal, derived = neutral(1.0), true
			}
		}
		if !derived || newVal == "" {
			out.Colors[key] = old // 装饰色等保留基底
			continue
		}
		out.Colors[key] = Color{Value: newVal, Usage: old.Usage}
	}
	return out
}

// contrastOn 返回在品牌色上可读的前景色。
func contrastOn(brand string, dark bool) string {
	if cr, ok := ContrastRatio("#FFFFFF", brand); ok && cr >= 3.0 {
		return "#FFFFFF"
	}
	if dark {
		return hslToHex(0, 0, 0.12)
	}
	return hslToHex(0, 0, 0.12)
}

// customID 从名称生成稳定 ID；与内置冲突时加后缀。
func customID(name, primary string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r > 127: // 中文：跳过，靠 primary 兜底
			continue
		default:
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "brand-" + strings.ToLower(strings.TrimPrefix(strings.ToUpper(primary), "#"))
	}
	if _, taken := GetSystem(id); taken {
		id += "-x"
	}
	return id
}

// ---------------------------------------------------------------------------
// 自定义体系持久化
// ---------------------------------------------------------------------------

// RegisterOrReplace 注册或覆盖一个体系（自定义体系用；内置 Register 仍禁重复）。
func RegisterOrReplace(s *System) {
	registry[s.ID] = s
}

// SaveCustomSystem 把自定义体系写入 <dir>/custom-systems/<id>.json 并注册。
func SaveCustomSystem(sys *System, dir string) error {
	if dir == "" {
		RegisterOrReplace(sys)
		return nil
	}
	sub := filepath.Join(dir, "custom-systems")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		return fmt.Errorf("create custom-systems dir: %w", err)
	}
	raw, err := json.MarshalIndent(sys, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(sub, sys.ID+".json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		return err
	}
	RegisterOrReplace(sys)
	return nil
}

// LoadCustomSystems 从目录加载全部自定义体系（启动时调用）。
func LoadCustomSystems(dir string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	sub := filepath.Join(dir, "custom-systems")
	entries, err := os.ReadDir(sub)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sub, e.Name()))
		if err != nil {
			continue
		}
		var sys System
		if err := json.Unmarshal(raw, &sys); err != nil || sys.ID == "" {
			continue
		}
		RegisterOrReplace(&sys)
		n++
	}
	return n, nil
}

// RemoveCustomSystem 注销并删除自定义体系的持久化文件；内置体系不可删。
func RemoveCustomSystem(id, dir string) error {
	sys, ok := GetSystem(id)
	if !ok {
		return fmt.Errorf("unknown system %q", id)
	}
	if sys.Vendor != "自定义 · 品牌派生" {
		return fmt.Errorf("system %q is builtin and cannot be removed", id)
	}
	delete(registry, id)
	if dir != "" {
		p := filepath.Join(dir, "custom-systems", id+".json")
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ValidateCustom 校验自定义体系的合法性（register 前调用）。
func ValidateCustom(s *System) error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	check := func(themeName string, t Tokens) error {
		if t.Grid <= 0 {
			return fmt.Errorf("%s: grid 必须为正", themeName)
		}
		if len(t.Colors) < 3 {
			return fmt.Errorf("%s: colors 至少 3 个语义色", themeName)
		}
		for name, c := range t.Colors {
			if _, _, _, _, ok := parseColor(c.Value); !ok && !strings.Contains(strings.ToLower(c.Value), "gradient") {
				return fmt.Errorf("%s: 颜色 %s=%q 不是合法 hex/rgba/渐变", themeName, name, c.Value)
			}
		}
		if len(t.Type.Levels) == 0 || t.Type.Family == "" {
			return fmt.Errorf("%s: 字型阶梯不完整（family/levels）", themeName)
		}
		for lv, spec := range t.Type.Levels {
			if spec.Size <= 0 || spec.Size > 200 {
				return fmt.Errorf("%s: 字号 %s=%d 超界", themeName, lv, spec.Size)
			}
		}
		for name, r := range t.Radius {
			if r < 0 || r > 200 {
				return fmt.Errorf("%s: 圆角 %s=%d 超界", themeName, name, r)
			}
		}
		return nil
	}
	if err := check("light", s.Light); err != nil {
		return err
	}
	if s.Dark != nil {
		if err := check("dark", *s.Dark); err != nil {
			return err
		}
	}
	return nil
}

// ClaimCustomID 为注册体系生成不冲突的 ID（基于名称 slug，冲突自动加序号）。
func ClaimCustomID(name, dir string) string {
	base := customID(name, "")
	if _, taken := GetSystem(base); !taken {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s-%d", base, i)
		if _, taken := GetSystem(id); !taken {
			return id
		}
	}
}

// DeriveBoardHTML 渲染派生体系的品牌色板看板（画布节点用）。
func DeriveBoardHTML(sys *System, scale []string) (string, int, error) {
	var b strings.Builder
	b.WriteString(`<main class="dv-wrap"><header><span class="dv-name">` + esc(sys.Name) +
		`</span><span class="dv-meta">品牌派生 · ` + esc(sys.ID) + ` · 基底 ` + esc(sys.Style.Keywords[0]) + `</span></header>`)
	b.WriteString(`<div class="dv-scale">`)
	for i, c := range scale {
		cls := ""
		if i >= len(scale)-3 {
			cls = " light"
		}
		b.WriteString(`<div class="dv-chip` + cls + `" style="background:` + esc(c) + `"><span>` + esc(fmt.Sprint(i+1)) + `</span><b>` + esc(c) + `</b></div>`)
	}
	b.WriteString(`</div><section class="dv-sem">`)
	for _, name := range []string{"primary", "success", "warning", "danger"} {
		if c, ok := sys.Light.Colors[name]; ok {
			b.WriteString(`<div class="dv-sem-item"><span class="dv-dot" style="background:` + esc(c.Value) + `"></span>` + esc(name) + ` <small>` + esc(c.Value) + `</small></div>`)
		}
	}
	b.WriteString(`</section><section class="dv-note">结构继承自基底体系（字型/间距/圆角/阴影/动效），可直接用 ` + esc(sys.ID) + ` 作为 system ID 生成预览。</section></main>`)
	const css = `
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:"Segoe UI",system-ui,"PingFang SC","Microsoft YaHei",sans-serif;background:#101116;color:#e8e9ee;padding:14px;font-size:12px}
.dv-wrap{display:flex;flex-direction:column;gap:12px}
header{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}
.dv-name{font-size:16px;font-weight:700}
.dv-meta{color:#8a8fa0;font-size:11px}
.dv-scale{display:grid;grid-template-columns:repeat(5,1fr);gap:6px}
.dv-chip{height:52px;border-radius:9px;position:relative;border:1px solid rgba(255,255,255,.12);display:flex;flex-direction:column;justify-content:flex-end;padding:4px 6px}
.dv-chip span{font-size:9px;color:#fff;opacity:.8}
.dv-chip b{font-size:8px;color:#fff;opacity:.95;font-weight:600}
.dv-chip.light span,.dv-chip.light b{color:rgba(0,0,0,.65)}
.dv-sem{display:flex;flex-direction:column;gap:6px;border:1px solid #2a2b36;border-radius:9px;padding:9px}
.dv-sem-item{display:flex;align-items:center;gap:8px}
.dv-dot{width:18px;height:18px;border-radius:50%;flex:none;border:1px solid rgba(255,255,255,.15)}
.dv-sem-item small{color:#8a8fa0}
.dv-note{color:#8a8fa0;font-size:11px;border:1px dashed #2a2b36;border-radius:9px;padding:8px 10px}
`
	doc := "<!DOCTYPE html>\n<html lang=\"zh-CN\" data-kind=\"derive\"><head><meta charset=\"utf-8\"><title>品牌色板 " +
		esc(sys.Name) + "</title><style>" + css + "</style></head>\n<body>\n" + b.String() + "\n</body>\n</html>\n"
	return doc, BoardWidth, nil
}
