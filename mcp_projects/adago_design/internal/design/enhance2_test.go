package design

import (
	"strings"
	"testing"
)

func TestColorParseAndContrast(t *testing.T) {
	r, g, b, a, ok := parseColor("#FF0000")
	if !ok || r != 1 || g != 0 || b != 0 || a != 1 {
		t.Fatalf("#FF0000 解析异常: %v %v %v %v", r, g, b, a)
	}
	if _, _, _, _, ok := parseColor("#161823BF"); !ok {
		t.Fatal("#RRGGBBAA 应可解析")
	}
	if _, _, _, _, ok := parseColor("rgba(22,24,35,0.75)"); !ok {
		t.Fatal("rgba() 应可解析")
	}
	if _, _, _, _, ok := parseColor("linear-gradient(a,b)"); ok {
		t.Fatal("渐变不应可解析")
	}
	// 黑白对比度 21
	cr, _ := ContrastRatio("#000000", "#FFFFFF")
	if cr < 20.9 || cr > 21.1 {
		t.Fatalf("黑白对比度 = %v", cr)
	}
	// 同色 = 1
	cr, _ = ContrastRatio("#409EFF", "#409EFF")
	if cr < 0.99 || cr > 1.01 {
		t.Fatalf("同色对比度 = %v", cr)
	}
	// 半透明文本合成：75% 黑 on 白应接近 0.25 黑的对比度
	full, _ := ContrastRatio("#161823", "#FFFFFF")
	partial, _ := ContrastRatio("#161823BF", "#FFFFFF")
	if partial <= 1 || partial >= full {
		t.Fatalf("半透明合成对比度异常: partial=%v full=%v", partial, full)
	}
}

func TestBrandScale(t *testing.T) {
	scale, ok := brandScale("#165DFF")
	if !ok || len(scale) != 10 {
		t.Fatalf("色阶生成异常: %v %v", ok, scale)
	}
	if scale[5] != "#165DFF" {
		t.Fatalf("6 阶应保留原色: %s", scale[5])
	}
	// 浅阶到深阶亮度单调递减
	prevL := 1.1
	for i, hex := range scale {
		_, _, l, ok := hexToHSL(hex)
		if !ok {
			t.Fatalf("生成色 %s 无法解析", hex)
		}
		if l > prevL+1e-9 {
			t.Fatalf("色阶亮度在 %d 处回升: %s l=%v prev=%v", i, hex, l, prevL)
		}
		prevL = l
	}
	if _, ok := brandScale("not-a-color"); ok {
		t.Fatal("非法色应失败")
	}
}

func TestDerive(t *testing.T) {
	sys, err := Derive(DeriveInput{Primary: "#C8412B", Name: "山茶茶饮", BaseSystemID: "volcano"})
	if err != nil {
		t.Fatal(err)
	}
	if sys.ID == "" || sys.Name != "山茶茶饮" || sys.Family != "web" {
		t.Fatalf("派生体系头异常: %+v", sys)
	}
	if sys.Light.Colors["primary"].Value != "#C8412B" {
		t.Fatalf("primary 应为品牌色: %s", sys.Light.Colors["primary"].Value)
	}
	// primary-hover/active 应来自色阶且不同于主色
	if sys.Light.Colors["primary-hover"].Value == sys.Light.Colors["primary"].Value {
		t.Fatal("hover 应与主色不同")
	}
	// 中性文本应为低饱和深灰
	_, s, l, _ := hexToHSL(sys.Light.Colors["text-1"].Value)
	if l > 0.25 || s > 0.2 {
		t.Fatalf("text-1 应为低饱和深色: %s (s=%v l=%v)", sys.Light.Colors["text-1"].Value, s, l)
	}
	// 结构继承基底
	if len(sys.Light.Type.Levels) != len(sys.Light.Type.Levels) || sys.Light.Grid != 8 {
		t.Fatal("结构应继承基底")
	}
	// 深色主题
	if sys.Dark == nil || sys.Dark.Colors["primary"].Value != "#C8412B" {
		t.Fatal("dark 主题应存在且主色一致")
	}
	// 深色下 bg 为暗色
	_, _, bgL, _ := hexToHSL(sys.Dark.Colors["bg"].Value)
	if bgL > 0.2 {
		t.Fatalf("dark bg 应为暗色: %s", sys.Dark.Colors["bg"].Value)
	}
	// 全部派生色合法
	for name, c := range sys.Light.Colors {
		if _, _, _, _, ok := parseColor(c.Value); !ok && !strings.Contains(c.Value, "gradient") {
			t.Fatalf("派生色 %s=%s 非法", name, c.Value)
		}
	}
	// 错误路径
	if _, err := Derive(DeriveInput{Primary: "red", Name: "x", BaseSystemID: "volcano"}); err == nil {
		t.Fatal("非法 primary 应报错")
	}
	if _, err := Derive(DeriveInput{Primary: "#C8412B", Name: "x", BaseSystemID: "nope"}); err == nil {
		t.Fatal("未知基底应报错")
	}
}

func TestCustomSystemPersist(t *testing.T) {
	dir := t.TempDir()
	sys, err := Derive(DeriveInput{Primary: "#2F7D5B", Name: "Camellia Green", BaseSystemID: "element"})
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveCustomSystem(sys, dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := GetSystem(sys.ID); !ok {
		t.Fatal("保存后应已注册")
	}
	// 重新加载
	n, err := LoadCustomSystems(dir)
	if err != nil || n != 1 {
		t.Fatalf("LoadCustomSystems = %d, %v", n, err)
	}
	// 内置体系不可删
	if err := RemoveCustomSystem("element", dir); err == nil {
		t.Fatal("内置体系应拒绝删除")
	}
	// 自定义可删
	if err := RemoveCustomSystem(sys.ID, dir); err != nil {
		t.Fatalf("删除自定义体系失败: %v", err)
	}
	if _, ok := GetSystem(sys.ID); ok {
		t.Fatal("删除后应注销")
	}
	if err := RemoveCustomSystem(sys.ID, dir); err == nil {
		t.Fatal("重复删除应报错")
	}
}

func TestAudit(t *testing.T) {
	res, err := Audit("element", ThemeLight)
	if err != nil {
		t.Fatal(err)
	}
	if res.Score < 60 {
		t.Fatalf("element 走查分数过低: %d (%+v)", res.Score, res.Checks)
	}
	hasContrast := false
	for _, c := range res.Checks {
		if strings.HasPrefix(c.Name, "对比度") && c.Status == "pass" {
			hasContrast = true
		}
	}
	if !hasContrast {
		t.Fatal("应存在通过的对比度检查")
	}
	// 深色
	if _, err2 := Audit("volcano", ThemeDark); err2 != nil {
		t.Fatalf("dark 走查失败: %v", err2)
	}
	// 错误路径
	if _, err := Audit("nope", ThemeLight); err == nil {
		t.Fatal("未知体系应报错")
	}
	// 低对比体系应得低分：构造一个文本与底色几乎相同的体系
	bad := &System{
		ID: "bad-test", Name: "Bad", Vendor: "test", Family: "web",
		Themes: []string{"light"},
		Light: Tokens{
			Grid: 8,
			Colors: map[string]Color{
				"bg":      {Value: "#EEEEEE"},
				"text-1":  {Value: "#DDDDDD"},
				"primary": {Value: "#EEEEEE"},
			},
			Type: TypeScale{Family: "x", Levels: map[string]TypeSpec{
				"body": {Size: 14, Weight: 400, LineHeight: 1.5}, "tiny": {Size: 9, Weight: 400, LineHeight: 1.2},
			}},
			Spacing: map[string]int{"s": 8},
			Radius:  map[string]int{"s": 4},
			Shadows: map[string]string{"card": "none"},
			Motion:  map[string]string{"fast": "100ms"},
		},
	}
	RegisterOrReplace(bad)
	defer unregisterSystem("bad-test")
	res, err = Audit("bad-test", ThemeLight)
	if err != nil {
		t.Fatal(err)
	}
	if res.Score >= 40 || res.Failed < 2 {
		t.Fatalf("低质量体系应低分: score=%d failed=%d", res.Score, res.Failed)
	}
}
