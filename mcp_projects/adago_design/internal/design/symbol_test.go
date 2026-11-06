package design

import (
	"strings"
	"testing"
)

// 门禁：所有会被渲染的产物里，不得出现 emoji / dingbat / 当作图标用的字符符号。
// 这是「问题 5」的回归防线 —— 只靠人工 grep 迟早会漏。
func TestNoEmojiInRenderedOutput(t *testing.T) {
	for _, sys := range Systems() {
		for _, th := range []ThemeKey{ThemeLight, ThemeDark} {
			if !sys.HasTheme(th) {
				continue
			}
			for _, doc := range iconCorpus(sys, th) {
				if hits := symbolRe.FindAllString(doc.Text, -1); len(hits) > 0 {
					t.Errorf("%s/%s %s 含字符图标 %q", sys.ID, th, doc.Name, strings.Join(dedupe(hits), " "))
				}
			}
		}
	}
}

// 组件/模板里引用的 {{icon:...}} 必须都能解析，否则展开时会被静默吞掉。
func TestAllIconTokensResolve(t *testing.T) {
	for _, c := range Components(nil) {
		raw, err := componentTemplate(c.ID)
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		for _, name := range IconTokenNames(raw) {
			if !HasIcon(name) {
				t.Errorf("组件 %s 引用了不存在的图标 %q", c.ID, name)
			}
		}
	}
	for _, body := range []string{loginBody("演示"), settingsBody("演示"), feedBody("演示"), profileBody("演示")} {
		for _, name := range IconTokenNames(body) {
			if !HasIcon(name) {
				t.Errorf("整页模板引用了不存在的图标 %q", name)
			}
		}
	}
}

// 渲染产物里的 <use> 引用必须都指向内联精灵里存在的 symbol。
func TestIconRefsExistInSprite(t *testing.T) {
	res, err := Compose(ComposeInput{SystemID: "apple", Components: []string{
		"button", "checkbox", "alert", "select", "list", "tabbar", "toast", "navbar",
	}})
	if err != nil {
		t.Fatal(err)
	}
	html := res.HTML
	sprite := IconSprite()
	for _, ref := range iconRefRe.FindAllStringSubmatch(html, -1) {
		if !strings.Contains(sprite, `id="`+ref[1]+`"`) {
			t.Errorf("引用了精灵里不存在的 symbol：%s", ref[1])
		}
	}
	if !strings.Contains(html, `href="#ds-ic-check"`) {
		t.Error("复选框应使用 check 图标")
	}
	if !strings.Contains(html, `href="#ds-ic-chevron-right"`) {
		t.Error("列表应使用 chevron-right 图标")
	}
	if strings.Contains(html, "font-size:18px") {
		t.Error("tabbar 图标不应再用字号撑尺寸")
	}
}

// 走查里的「图标与符号」「字体授权」「尺寸规范」三项必须存在且不失败。
func TestAuditIncludesNewChecks(t *testing.T) {
	res, err := Audit("apple", ThemeLight)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"图标与符号": false, "字体授权": false, "尺寸规范": false}
	for _, c := range res.Checks {
		if _, ok := want[c.Name]; ok {
			want[c.Name] = true
			if c.Status == "fail" {
				t.Errorf("检查项 %s 失败：%s", c.Name, c.Detail)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("走查缺少检查项 %q", name)
		}
	}
}
