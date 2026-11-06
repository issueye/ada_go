package design

import (
	"strings"
	"testing"
)

func TestArtboardCatalog(t *testing.T) {
	boards := Artboards()
	if len(boards) != 8 {
		t.Fatalf("画板数 = %d，期望 8", len(boards))
	}
	for _, a := range boards {
		spec := a.Spec()
		if spec.ContentWidth <= 0 {
			t.Errorf("%s 内容宽非法：%d", a.ID, spec.ContentWidth)
		}
		if a.MaxContent > 0 && spec.ContentWidth > a.MaxContent {
			t.Errorf("%s 内容宽 %d 超过上限 %d", a.ID, spec.ContentWidth, a.MaxContent)
		}
		if spec.BodyHeight <= 0 || spec.ContentHeight < 0 {
			t.Errorf("%s 高度推导非法：body=%d content=%d", a.ID, spec.BodyHeight, spec.ContentHeight)
		}
		if spec.ContentHeight > spec.BodyHeight {
			t.Errorf("%s 内容高大于 body 高", a.ID)
		}
		if len(spec.ExportSizes) == 0 || !strings.Contains(spec.ExportSizes[0], "@1x") {
			t.Errorf("%s 导出尺寸缺失：%v", a.ID, spec.ExportSizes)
		}
		if a.Margin%4 != 0 || a.Gutter%4 != 0 {
			t.Errorf("%s 边距/栅格不是 4 的倍数", a.ID)
		}
	}
}

func TestArtboardKnownValues(t *testing.T) {
	iphone, ok := GetArtboard("iphone-15")
	if !ok {
		t.Fatal("缺少 iphone-15")
	}
	if iphone.Width != 393 || iphone.Height != 852 {
		t.Fatalf("iphone-15 尺寸 = %d×%d", iphone.Width, iphone.Height)
	}
	spec := iphone.Spec()
	if spec.ContentWidth != 361 {
		t.Errorf("内容宽 = %d，期望 361（393-2×16）", spec.ContentWidth)
	}
	if spec.BodyHeight != 764 {
		t.Errorf("body 高 = %d，期望 764（852-54-0-34）", spec.BodyHeight)
	}
	if spec.ContentHeight != 671 {
		t.Errorf("内容区高 = %d，期望 671（764-44-49）", spec.ContentHeight)
	}
	if !strings.Contains(spec.ExportSizes[1], "@2x 786×1704") {
		t.Errorf("导出尺寸错误：%v", spec.ExportSizes)
	}
	if !strings.Contains(spec.Contract, "361") {
		t.Error("契约文本应包含内容宽")
	}

	web, _ := GetArtboard("web-1440")
	if w := web.Spec().ContentWidth; w != 1200 {
		t.Errorf("web-1440 内容宽 = %d，期望被 MaxContent 收敛到 1200", w)
	}
}

func TestArtboardDefaultsPerFamily(t *testing.T) {
	cases := map[string]string{
		"mobile": "iphone-15", "tablet": "ipad-768",
		"web": "web-1440", "cross": "desktop-app", "": "web-1440", "unknown": "web-1440",
	}
	for family, want := range cases {
		if got := DefaultArtboardFor(family).ID; got != want {
			t.Errorf("DefaultArtboardFor(%q) = %q，期望 %q", family, got, want)
		}
	}
}

func TestResolveArtboard(t *testing.T) {
	if a, ok := ResolveArtboard("", "mobile"); !ok || a.ID != "iphone-15" {
		t.Errorf("空 ID 应按 family 取默认，得到 %q ok=%v", a.ID, ok)
	}
	if a, ok := ResolveArtboard("mp-wx", "web"); !ok || a.ID != "mp-wx" {
		t.Errorf("显式 ID 应优先，得到 %q ok=%v", a.ID, ok)
	}
	if _, ok := ResolveArtboard("nope", "web"); ok {
		t.Error("非法画板 ID 应返回 false")
	}
}

func TestPageCSSVars(t *testing.T) {
	iphone, _ := GetArtboard("iphone-15")
	css := PageCSS(iphone)
	for _, want := range []string{"--ds-ab-w:393px", "--ds-ab-content-w:361px", "--ds-ab-margin:16px", "--ds-ab-tab:49px"} {
		if !strings.Contains(css, want) {
			t.Errorf("PageCSS 缺少 %q", want)
		}
	}
}

// 预览文档的宽高必须来自画板，不得再有 420/390/560/780 这类魔法数。
func TestComposeUsesArtboard(t *testing.T) {
	res, err := Compose(ComposeInput{SystemID: "apple", Components: []string{"button"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Artboard != "iphone-15" || res.Width != 393 {
		t.Fatalf("apple 默认画板 = %q 宽 %d", res.Artboard, res.Width)
	}
	if !strings.Contains(res.HTML, `data-artboard="iphone-15"`) {
		t.Error("文档缺少 data-artboard 标注")
	}
	if !strings.Contains(res.HTML, "--ds-ab-content-w:361px") {
		t.Error("文档缺少画板 CSS 变量")
	}
	if !strings.Contains(res.HTML, "<symbol id=\"ds-ic-") {
		t.Error("文档缺少内联图标精灵")
	}

	web, err := Compose(ComposeInput{SystemID: "element", Components: []string{"button"}})
	if err != nil {
		t.Fatal(err)
	}
	if web.Artboard != "web-1440" || web.Width != 1440 {
		t.Fatalf("element 默认画板 = %q 宽 %d", web.Artboard, web.Width)
	}

	custom, err := Compose(ComposeInput{SystemID: "apple", Components: []string{"button"}, Artboard: "ipad-768"})
	if err != nil {
		t.Fatal(err)
	}
	if custom.Width != 768 {
		t.Fatalf("显式画板未生效：%d", custom.Width)
	}

	if _, err := Compose(ComposeInput{SystemID: "apple", Components: []string{"button"}, Artboard: "nope"}); err == nil {
		t.Error("非法画板应报错")
	}
}

func TestComposeScreenUsesArtboardHeight(t *testing.T) {
	res, err := ComposeScreen(ScreenInput{SystemID: "apple", Template: "settings"})
	if err != nil {
		t.Fatal(err)
	}
	iphone, _ := GetArtboard("iphone-15")
	if res.Height != iphone.Spec().BodyHeight {
		t.Errorf("整页高度 = %d，期望画板 body 高 %d", res.Height, iphone.Spec().BodyHeight)
	}
	if res.Width != 393 {
		t.Errorf("整页宽度 = %d", res.Width)
	}
}
