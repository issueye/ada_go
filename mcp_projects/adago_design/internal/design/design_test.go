package design

import (
	"regexp"
	"strings"
	"testing"
)

// allSystemIDs 是内置体系清单；新增体系时同步更新。
var allSystemIDs = []string{"ant", "apple", "douyin", "element", "material", "tdesign", "volcano", "xiaomi"}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$|^rgba?\(.*\)$|^linear-gradient\(.*\)$`)

func TestRegistryComplete(t *testing.T) {
	sys := Systems()
	if len(sys) != len(allSystemIDs) {
		t.Fatalf("体系数量 = %d, 期望 %d（%v）", len(sys), len(allSystemIDs), allSystemIDs)
	}
	got := map[string]bool{}
	for _, s := range sys {
		got[s.ID] = true
	}
	for _, id := range allSystemIDs {
		if !got[id] {
			t.Errorf("缺少体系 %q", id)
		}
	}
}

func TestEverySystemTokensValid(t *testing.T) {
	for _, s := range Systems() {
		if s.Name == "" || s.Vendor == "" || s.Description == "" {
			t.Errorf("%s: 元信息不完整", s.ID)
		}
		checkTokens := func(theme string, tok Tokens) {
			if tok.Grid <= 0 {
				t.Errorf("%s/%s: grid 未设置", s.ID, theme)
			}
			if len(tok.Colors) < 10 {
				t.Errorf("%s/%s: 语义色过少 (%d)", s.ID, theme, len(tok.Colors))
			}
			for name, c := range tok.Colors {
				if !hexColor.MatchString(c.Value) {
					t.Errorf("%s/%s: 颜色 %s = %q 不是合法值", s.ID, theme, name, c.Value)
				}
			}
			if tok.Type.Family == "" || len(tok.Type.Levels) < 5 {
				t.Errorf("%s/%s: 字型阶梯不完整", s.ID, theme)
			}
			if len(tok.Spacing) < 5 || len(tok.Radius) < 3 || len(tok.Shadows) < 2 || len(tok.Motion) < 2 {
				t.Errorf("%s/%s: 间距/圆角/阴影/动效 Token 不完整", s.ID, theme)
			}
			for name, r := range tok.Radius {
				if r < 0 || r > 200 {
					t.Errorf("%s/%s: 圆角 %s = %d 超界", s.ID, theme, name, r)
				}
			}
		}
		checkTokens("light", s.Light)
		if s.Dark != nil {
			checkTokens("dark", *s.Dark)
		}
		if len(s.Guideline) == 0 {
			t.Errorf("%s: 缺少使用守则", s.ID)
		}
	}
}

func TestEverySystemRendersAllComponents(t *testing.T) {
	comps := Components(nil) // 全局目录
	if len(comps) < 15 {
		t.Fatalf("组件数 = %d, 期望 ≥15", len(comps))
	}
	for _, s := range Systems() {
		for _, theme := range []ThemeKey{ThemeLight, ThemeDark} {
			css := StyleSheet(s, theme)
			if !strings.Contains(css, "--ds-color-primary") {
				t.Errorf("%s/%s: StyleSheet 缺少 primary 变量", s.ID, theme)
			}
			if !strings.Contains(css, s.ID+" skin") {
				t.Errorf("%s/%s: StyleSheet 缺少皮肤覆写段", s.ID, theme)
			}
		}
		// 每个体系都能产出包含关键类的完整文档
		res, err := Compose(ComposeInput{
			SystemID: s.ID, Theme: ThemeLight,
			Components: []string{"button", "input", "card", "navbar"},
			Title:      "测试",
		})
		if err != nil {
			t.Fatalf("%s: Compose 失败: %v", s.ID, err)
		}
		if res.Missing != nil {
			t.Errorf("%s: 预期组件全部有效, missing=%v", s.ID, res.Missing)
		}
		for _, marker := range []string{"ds-btn", "ds-input", "ds-card", "ds-navbar", "ds-page"} {
			if !strings.Contains(res.HTML, marker) {
				t.Errorf("%s: HTML 缺少 %s", s.ID, marker)
			}
		}
		if !strings.Contains(res.HTML, "<!DOCTYPE html>") {
			t.Errorf("%s: 不是完整文档", s.ID)
		}
		if res.Width <= 0 || res.Height <= 0 {
			t.Errorf("%s: 建议尺寸非法 %dx%d", s.ID, res.Width, res.Height)
		}
	}
}

func TestComposeUnknownPieces(t *testing.T) {
	if _, err := Compose(ComposeInput{SystemID: "nope", Components: []string{"button"}}); err == nil {
		t.Error("未知体系应报错")
	}
	res, err := Compose(ComposeInput{SystemID: "element", Components: []string{"button", "ghost-component"}})
	if err != nil {
		t.Fatalf("合法体系应成功: %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "ghost-component" {
		t.Errorf("missing = %v, 期望 [ghost-component]", res.Missing)
	}
	if _, err := Compose(ComposeInput{SystemID: "element", Components: []string{"ghost-component"}}); err == nil {
		t.Error("全部无效组件应报错")
	}
}

func TestRenderComponentUnknown(t *testing.T) {
	if _, err := RenderComponent("nope"); err == nil {
		t.Error("未知组件应报错")
	}
	out, err := RenderComponent("button")
	if err != nil || !strings.Contains(out, "ds-btn--primary") {
		t.Errorf("button 渲染异常: %v", err)
	}
}

func TestDarkThemeFallback(t *testing.T) {
	sys, _ := GetSystem("apple")
	tok, eff := sys.TokensFor(ThemeDark)
	if eff != ThemeDark || !strings.HasPrefix(tok.Colors["system-background"].Value, "#000") {
		t.Errorf("apple dark 背景异常: %v %s", eff, tok.Colors["system-background"].Value)
	}
	css := StyleSheet(sys, ThemeDark)
	if !strings.Contains(css, "#0A84FF") {
		t.Error("dark CSS 应含暗色 systemBlue")
	}
}

func TestComponentsCatalogFields(t *testing.T) {
	for _, c := range Components(nil) {
		if c.Name == "" || c.Category == "" || c.Description == "" {
			t.Errorf("组件 %s 元信息不完整", c.ID)
		}
		if len(c.Variants) == 0 || len(c.Usage) == 0 {
			t.Errorf("组件 %s 缺少变体或用法", c.ID)
		}
	}
}

func TestStandaloneDoc(t *testing.T) {
	sys, _ := GetSystem("element")
	doc := StandaloneDoc(sys, ThemeLight, "自定义", `<div class="ds-block">hello</div>`)
	if !strings.Contains(doc, "hello") {
		t.Error("正文缺失")
	}
	if !strings.Contains(doc, "--ds-color-primary: #409EFF") {
		t.Error("element 主色变量未注入")
	}
	if !strings.Contains(doc, `data-system="element"`) {
		t.Error("文档未标注体系")
	}
}
