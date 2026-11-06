package design

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestComponentClassCoverage 校验每个组件 HTML 引用的 class 都在 StyleSheet 中有定义
// （防止拼写错误或漏写样式导致的裸组件）。
func TestComponentClassCoverage(t *testing.T) {
	for _, s := range Systems() {
		css := StyleSheet(s, ThemeLight) + StyleSheet(s, ThemeDark)
		for _, comp := range Components(nil) {
			html, err := RenderComponent(comp.ID)
			if err != nil {
				t.Fatalf("%s: %v", comp.ID, err)
			}
			classRe := regexp.MustCompile(`class="([^"]+)"`)
			for _, m := range classRe.FindAllStringSubmatch(html, -1) {
				for _, cls := range strings.Fields(m[1]) {
					cls = strings.TrimPrefix(cls, "ds-")
					base := strings.Split(cls, "--")[0]
					// CSS 中的类名带 ds- 前缀；块/元素/修饰都要查
					if !strings.Contains(css, ".ds-"+cls) && !strings.Contains(css, ".ds-"+base) {
						t.Errorf("%s: 组件 %s 引用的 .%s 无样式定义", s.ID, comp.ID, cls)
					}
				}
			}
		}
	}
}

// TestAllSystemsSpecZero 断言全部体系×主题的组件 CSS 字面值规范违例为零
// （padding/margin/gap/border-radius/font-size 都必须落在 Token 阶梯上——强制规范门禁）。
func TestAllSystemsSpecZero(t *testing.T) {
	for _, s := range Systems() {
		for _, theme := range s.Themes {
			issues := SpecConformance(s, ThemeKey(theme))
			if len(issues) != 0 {
				names := make([]string, 0, len(issues))
				for _, is := range issues {
					names = append(names, is.Prop+" "+strconv.FormatFloat(is.Value, 'f', -1, 64)+"→"+strconv.Itoa(is.Nearest))
				}
				t.Errorf("%s/%s: %d 处规范违例: %s", s.ID, theme, len(issues), strings.Join(names, "；"))
			}
		}
	}
}
