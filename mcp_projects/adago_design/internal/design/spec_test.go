package design

import (
	"strings"
	"testing"
)

func TestSpecConformance(t *testing.T) {
	// 现有体系应能产出清单（是否为零取决于 CSS 现状），且最近值必须来自阶梯
	sys, _ := GetSystem("volcano")
	issues := SpecConformance(sys, ThemeLight)
	allowed := map[int]bool{}
	for _, v := range sys.Light.Spacing {
		allowed[v] = true
	}
	for _, v := range sys.Light.Radius {
		allowed[v] = true
	}
	for _, is := range issues {
		if !allowed[is.Nearest] {
			t.Fatalf("最近值 %d 不在阶梯内: %+v", is.Nearest, is)
		}
		if is.Value == float64(is.Nearest) {
			t.Fatalf("恰好等于阶梯的值不应报告: %+v", is)
		}
	}
}

func TestScanCSSInjection(t *testing.T) {
	bad := &System{
		ID: "spec-bad", Name: "SpecBad", Vendor: "test", Family: "web",
		Themes: []string{"light"},
		Light: Tokens{
			Grid:   8,
			Colors: map[string]Color{"primary": {Value: "#165DFF"}},
			Type: TypeScale{Family: "x", Levels: map[string]TypeSpec{
				"body": {Size: 14, Weight: 400, LineHeight: 1.5},
			}},
			Spacing: map[string]int{"md": 16, "lg": 20},
			Radius:  map[string]int{"medium": 4},
			Shadows: map[string]string{"card": "none"},
			Motion:  map[string]string{"fast": "100ms"},
		},
	}
	RegisterOrReplace(bad)
	defer unregisterSystem("spec-bad")
	css := StyleSheet(bad, ThemeLight) + "\n.x{padding:9px 15px;border-radius:6px;gap:3px}"
	issues := scanCSS(bad, ThemeLight, css)
	found15 := false
	foundRadius6 := false
	for _, is := range issues {
		if is.Prop == "padding" && is.Value == 15 && is.Nearest == 16 {
			found15 = true
		}
		if is.Kind == "radius" && is.Value == 6 {
			foundRadius6 = true
		}
	}
	if !found15 {
		t.Fatalf("15px 应报最近 16px: %+v", issues)
	}
	if !foundRadius6 {
		t.Fatalf("6px 圆角应报告: %+v", issues)
	}
	// 3px 属发丝白名单，不应出现
	for _, is := range issues {
		if is.Value == 3 {
			t.Fatalf("3px 发丝值不应报告: %+v", is)
		}
	}
}

func TestAuditIncludesSpecCheck(t *testing.T) {
	res, err := Audit("element", ThemeLight)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "组件规范") {
			found = true
		}
	}
	if !found {
		t.Fatal("audit 应包含组件规范检查")
	}
}
