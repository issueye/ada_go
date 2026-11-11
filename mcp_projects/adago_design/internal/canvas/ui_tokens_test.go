// ui_tokens_test.go 是画布界面设计系统的机器门禁。
//
// 规范见 DESIGN-SYSTEM-UI.md：颜色/字号/圆角/层级只允许来自 tokens.css，
// 组件样式不得写原始色值，JS 不得复制颜色常量或使用原生 prompt/confirm。
// 任何一条被破坏都应在这个文件里先红，而不是等视觉走查才发现。
package canvas

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("读取 static/%s: %v", name, err)
	}
	return string(b)
}

// allStaticJS 返回画布 UI 的全部 JS 源码（icons.js 为 Go 侧生成物，一并纳入
// 颜色扫描）。拆分后脚本分布在 static/js/ 下，门禁按目录聚合，新增文件自动覆盖。
func allStaticJS(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := staticFS.ReadDir("static")
	if err != nil {
		t.Fatalf("枚举 static: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			out[e.Name()] = readStatic(t, e.Name())
		}
	}
	sub, err := staticFS.ReadDir("static/js")
	if err != nil {
		t.Fatalf("枚举 static/js: %v", err)
	}
	for _, e := range sub {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
			out["js/"+e.Name()] = readStatic(t, "js/"+e.Name())
		}
	}
	if len(out) < 10 {
		t.Fatalf("JS 文件数异常（拆分后应 ≥13 个）: %d", len(out))
	}
	return out
}

// cssBlock 取出某个选择器块内的声明文本（足够用于扁平令牌文件）。
func cssBlock(css, selector string) string {
	idx := strings.Index(css, selector+" {")
	if idx < 0 {
		return ""
	}
	rest := css[idx+len(selector)+2:]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// cssVar 读取块内的自定义属性值（去空白）。
func cssVar(block, name string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*:\s*([^;]+);`)
	if m := re.FindStringSubmatch(block); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// 语义令牌清单：明暗两套都必须齐全，缺一即 fail（新增令牌要同步张清单）。
var requiredTokens = []string{
	"--ui-canvas", "--ui-surface", "--ui-elevated", "--ui-subtle", "--ui-subtle-strong",
	"--ui-line", "--ui-line-strong",
	"--ui-ink", "--ui-ink-2", "--ui-ink-3",
	"--ui-brand", "--ui-on-brand", "--ui-brand-soft",
	"--ui-success-soft", "--ui-success-ink",
	"--ui-warn-soft", "--ui-warn-ink",
	"--ui-danger-soft", "--ui-danger-ink",
	"--ui-ins-a", "--ui-ins-b", "--ui-on-ins",
	"--ui-focus", "--ui-scrim",
}

func TestUITokensComplete(t *testing.T) {
	css := readStatic(t, "tokens.css")
	light := cssBlock(css, ":root")
	dark := cssBlock(css, `html[data-theme="dark"]`)
	if light == "" || dark == "" {
		t.Fatal("tokens.css 必须同时定义 :root（纸面）与 html[data-theme=\"dark\"]（墨纸）")
	}
	for _, name := range requiredTokens {
		if v := cssVar(light, name); v == "" {
			t.Errorf("纸面主题缺少令牌 %s", name)
		}
		if v := cssVar(dark, name); v == "" {
			t.Errorf("墨纸主题缺少令牌 %s", name)
		}
	}
	// 布局常量也要在，JS 通过 getComputedStyle 读它们
	for _, name := range []string{"--ui-gutter", "--ui-dock-w", "--ui-inspector-w",
		"--ui-top-safe", "--ui-node-head-h"} {
		if cssVar(light, name) == "" {
			t.Errorf("缺少布局令牌 %s", name)
		}
	}
}

// ---- WCAG 对比度 ----

func parseHex(t *testing.T, s string) (r, g, b float64) {
	t.Helper()
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		t.Fatalf("对比度断言只支持十六进制色值，收到 %q", s)
	}
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		t.Fatalf("无法解析颜色 %q", s)
	}
	vals := make([]float64, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			t.Fatalf("无法解析颜色 %q: %v", s, err)
		}
		vals[i] = float64(n) / 255.0
	}
	return vals[0], vals[1], vals[2]
}

func relLum(t *testing.T, s string) float64 {
	t.Helper()
	r, g, b := parseHex(t, s)
	lin := func(v float64) float64 {
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrastRatio(t *testing.T, fg, bg string) float64 {
	t.Helper()
	l1, l2 := relLum(t, fg), relLum(t, bg)
	hi, lo := math.Max(l1, l2), math.Min(l1, l2)
	return (hi + 0.05) / (lo + 0.05)
}

func TestUITokenContrast(t *testing.T) {
	css := readStatic(t, "tokens.css")
	themes := map[string]string{
		"纸面": cssBlock(css, ":root"),
		"墨纸": cssBlock(css, `html[data-theme="dark"]`),
	}
	pairs := []struct{ fg, bg, what string }{
		{"--ui-ink", "--ui-canvas", "正文 / 画布"},
		{"--ui-ink", "--ui-surface", "正文 / 浮窗"},
		{"--ui-ink-2", "--ui-surface", "次要字 / 浮窗"},
		{"--ui-ink-3", "--ui-surface", "辅助字 / 浮窗"},
		{"--ui-ink-3", "--ui-elevated", "辅助字 / 卡片"},
		{"--ui-on-brand", "--ui-brand", "品牌底上的字"},
		{"--ui-success-ink", "--ui-success-soft", "成功态文字"},
		{"--ui-warn-ink", "--ui-warn-soft", "警告态文字"},
		{"--ui-danger-ink", "--ui-danger-soft", "危险态文字"},
		{"--ui-on-ins", "--ui-ins-a", "A 标注上的字"},
		{"--ui-on-ins", "--ui-ins-b", "B 标注上的字"},
	}
	for theme, block := range themes {
		for _, p := range pairs {
			fg, bg := cssVar(block, p.fg), cssVar(block, p.bg)
			if fg == "" || bg == "" {
				t.Errorf("%s: 缺令牌 %s 或 %s", theme, p.fg, p.bg)
				continue
			}
			if got := contrastRatio(t, fg, bg); got < 4.5 {
				t.Errorf("%s: %s 对比度 %.2f < 4.5（%s on %s）",
					theme, p.what, got, fg, bg)
			}
		}
	}
}

// ---- 组件样式：不允许原始值 ----

func TestCanvasCSSUsesTokensOnly(t *testing.T) {
	css := readStatic(t, "canvas.css")

	hexRe := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	for _, m := range hexRe.FindAllString(css, -1) {
		t.Errorf("canvas.css 出现原始色值 %s（应引用 --ui-* 令牌）", m)
	}

	rgbRe := regexp.MustCompile(`(?i)\b(rgba?|hsla?)\(`)
	for _, m := range rgbRe.FindAllString(css, -1) {
		t.Errorf("canvas.css 出现颜色函数 %s（应引用 --ui-* 令牌）", m)
	}

	sizeRe := regexp.MustCompile(`font-size\s*:\s*[0-9.]+px`)
	for _, m := range sizeRe.FindAllString(css, -1) {
		t.Errorf("canvas.css 出现字号字面值 %q（应用 --ui-text-*）", m)
	}

	radiusRe := regexp.MustCompile(`border-radius\s*:[^;}]*[0-9]+px`)
	for _, m := range radiusRe.FindAllString(css, -1) {
		t.Errorf("canvas.css 出现圆角字面值 %q（应用 --ui-radius-*）", m)
	}

	// 八个魔法数不能在组件层重新出现（层级必须走 --ui-z-*）
	zRe := regexp.MustCompile(`z-index\s*:\s*(?:[0-9]{2,}|9999)`)
	for _, m := range zRe.FindAllString(css, -1) {
		t.Errorf("canvas.css 出现层级魔法数 %q（应用 --ui-z-*）", m)
	}
}

func TestCanvasCSSAccessibilityRules(t *testing.T) {
	css := readStatic(t, "canvas.css")
	for _, want := range []string{
		":focus-visible",         // 焦点环兜底
		"prefers-reduced-motion", // 减少动效
		".empty-hint",            // 空态三件套载体
	} {
		if !strings.Contains(css, want) {
			t.Errorf("canvas.css 缺少 %s 规则", want)
		}
	}
}

func TestCanvasJSNoLiteralColors(t *testing.T) {
	for name, js := range allStaticJS(t) {
		hexRe := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
		for _, m := range hexRe.FindAllString(js, -1) {
			// 允许 SVG 片段标识引用（#ds-ic-*）之外的一切都会被上面匹配，
			// 因此这里只排除 use href 里的图标 id。
			if strings.HasPrefix(m, "#ds-ic") || strings.HasPrefix(m, "#arrow") {
				continue
			}
			t.Errorf("%s 出现原始色值 %s（应从 --ui-* 令牌读）", name, m)
		}
	}
}

func TestCanvasJSNoNativeDialogs(t *testing.T) {
	jsFiles := allStaticJS(t)
	// 注释里会提到「原生 prompt/confirm」，只检查代码行，避免误报
	var names []string
	var all []string
	for name, js := range jsFiles {
		names = append(names, name)
		all = append(all, stripLineComments(js))
	}
	code := strings.Join(all, "\n")
	for _, bad := range []string{"prompt(", "confirm("} {
		if strings.Contains(code, bad) {
			t.Errorf("画布 JS 仍在使用原生 %s（应走 uiAsk/uiConfirm 弹层）: %v", bad, names)
		}
	}
	for _, want := range []string{"function uiAsk(", "function uiConfirm(", "function isAskOpen("} {
		if !strings.Contains(code, want) {
			t.Errorf("画布 JS 缺少 %s（应在 js/core.js / js/main.js）", want)
		}
	}
}

// stripLineComments 去掉 // 行注释（足够用于本文件的朴素检查）。
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

func TestIndexLoadsTokensFirst(t *testing.T) {
	html := readStatic(t, "index.html")
	tok := strings.Index(html, `href="tokens.css"`)
	cvs := strings.Index(html, `href="canvas.css"`)
	if tok < 0 || cvs < 0 {
		t.Fatal("index.html 必须同时加载 tokens.css 与 canvas.css")
	}
	if tok > cvs {
		t.Error("tokens.css 必须先于 canvas.css 加载")
	}
	if !strings.Contains(html, `data-theme="__ADAGO_THEME__"`) {
		t.Error("index.html 需要在 html 上留 data-theme 占位，供服务端注入缺省主题")
	}
	if !strings.Contains(html, `id="ask-modal"`) {
		t.Error("index.html 需要 ask-modal 骨架（替代原生 prompt/confirm）")
	}
}

func TestServeIndexInjectsTheme(t *testing.T) {
	cases := map[string]string{"light": "light", "dark": "dark", "": "light", "DARK": "dark"}
	for in, want := range cases {
		if got := NormalizeTheme(in); got != want {
			t.Errorf("NormalizeTheme(%q) = %q，期望 %q", in, got, want)
		}
	}
}
