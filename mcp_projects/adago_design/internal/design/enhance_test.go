package design

import (
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	res, err := Compare("element", "volcano", ThemeLight)
	if err != nil {
		t.Fatal(err)
	}
	if res.A != "element" || res.B != "volcano" || res.Theme != "light" {
		t.Fatalf("对比头异常: %+v", res)
	}
	// primary 不同（#409EFF vs #165DFF）
	found := false
	for _, c := range res.Colors.Changed {
		if strings.HasPrefix(c, "primary: #409EFF") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有 primary 颜色差异: %v", res.Colors.Changed)
	}
	summary := res.Summary()
	if !strings.Contains(summary, "element") || !strings.Contains(summary, "volcano") {
		t.Fatalf("摘要异常: %s", summary)
	}
	// 自比与未知体系应报错
	if _, err := Compare("element", "element", ThemeLight); err == nil {
		t.Error("自比应报错")
	}
	if _, err := Compare("element", "nope", ThemeLight); err == nil {
		t.Error("未知体系应报错")
	}
	// 深色主题可用
	if _, err := Compare("apple", "xiaomi", ThemeDark); err != nil {
		t.Errorf("dark 对比失败: %v", err)
	}
}

func TestCompareBoardHTML(t *testing.T) {
	doc, w, err := CompareBoardHTML("douyin", "material", ThemeDark)
	if err != nil {
		t.Fatal(err)
	}
	if w != 420 {
		t.Fatalf("宽度 = %d", w)
	}
	for _, marker := range []string{"抖音设计体系", "Material Design 3", "cp-swatch", "compare"} {
		if !strings.Contains(doc, marker) {
			t.Errorf("对比板缺少 %q", marker)
		}
	}
	// 内联样式应包含双方 primary 值
	if !strings.Contains(doc, "#FE2C55") || !strings.Contains(doc, "#D0BCFF") {
		t.Error("对比板应内联双方主色值")
	}
	if _, _, err := CompareBoardHTML("douyin", "douyin", ThemeLight); err == nil {
		t.Error("同体系对比应报错")
	}
}

func TestSearch(t *testing.T) {
	// 组件搜索
	hits := Search("button")
	if len(hits) == 0 || hits[0].Kind != "component" || hits[0].ID != "button" {
		t.Fatalf("button 搜索异常: %+v", hits)
	}
	// 体系搜索
	hits = Search("抖音")
	if len(hits) == 0 || !strings.Contains(hits[0].Title, "抖音") {
		t.Fatalf("抖音搜索异常: %+v", hits[:1])
	}
	// token 名称搜索
	hits = Search("primary")
	if len(hits) < 8 {
		t.Fatalf("primary 应命中至少 8 套体系: %d", len(hits))
	}
	// 空查询
	if Search("  ") != nil {
		t.Error("空查询应返回 nil")
	}
}

func TestScreenTemplates(t *testing.T) {
	tpls := ScreenTemplates()
	if len(tpls) != 4 {
		t.Fatalf("模板数 = %d, 期望 4", len(tpls))
	}
	for _, s := range Systems() {
		for _, tpl := range []string{"login", "settings", "feed", "profile"} {
			res, err := ComposeScreen(ScreenInput{SystemID: s.ID, Template: tpl, Theme: ThemeLight})
			if err != nil {
				t.Fatalf("%s/%s: %v", s.ID, tpl, err)
			}
			if !strings.Contains(res.HTML, "ds-screen") {
				t.Fatalf("%s/%s: 缺少屏幕容器", s.ID, tpl)
			}
			if res.Width <= 0 || res.Height <= 0 {
				t.Fatalf("%s/%s: 尺寸非法", s.ID, tpl)
			}
		}
	}
	// 特定模板的结构断言
	res, err := ComposeScreen(ScreenInput{SystemID: "douyin", Template: "login", Theme: ThemeDark, AppName: "抖仓"})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"ds-login-card", "ds-btn--primary ds-btn--large", "记住我"} {
		if !strings.Contains(res.HTML, marker) {
			t.Errorf("login 缺少 %q", marker)
		}
	}
	if !strings.Contains(res.HTML, "douyin skin") || !strings.Contains(res.HTML, "#121212") {
		t.Error("login 应内嵌 douyin 暗色皮肤")
	}
	// navbar+tabbar
	res, err = ComposeScreen(ScreenInput{SystemID: "ant", Template: "settings", Theme: ThemeLight})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.HTML, "ds-navbar") || !strings.Contains(res.HTML, "ds-tabbar") || !strings.Contains(res.HTML, "ds-switch--on") {
		t.Error("settings 应包含导航栏/标签栏/开关")
	}
	// 错误路径
	if _, err := ComposeScreen(ScreenInput{SystemID: "nope", Template: "login"}); err == nil {
		t.Error("未知体系应报错")
	}
	if _, err := ComposeScreen(ScreenInput{SystemID: "ant", Template: "nope"}); err == nil {
		t.Error("未知模板应报错")
	}
}
