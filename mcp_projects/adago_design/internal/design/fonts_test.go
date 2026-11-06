package design

import (
	"strings"
	"testing"
)

func TestAllSystemsFontsCommercial(t *testing.T) {
	for _, sys := range Systems() {
		for _, th := range []ThemeKey{ThemeLight, ThemeDark} {
			if !sys.HasTheme(th) {
				continue
			}
			tok, _ := sys.TokensFor(th)
			ok, bad := StackCommercial(tok.Type.Family)
			if !ok {
				t.Errorf("%s/%s 含不可商用字体：%v（字族 %q）", sys.ID, th, bad, tok.Type.Family)
			}
		}
	}
}

// 专有字体（Helvetica 系）不得出现在任何体系的字族里。
func TestNoProprietaryFonts(t *testing.T) {
	for _, sys := range Systems() {
		fam := strings.ToLower(sys.Light.Type.Family)
		for _, bad := range []string{"helvetica neue", "helvetica,", "arial"} {
			if strings.Contains(fam, bad) {
				t.Errorf("%s 字族含专有字体 %q：%s", sys.ID, bad, sys.Light.Type.Family)
			}
		}
	}
}

// 每个体系都必须带可嵌入的开源兜底，否则在 Android/Linux/CI 上排版不可复现。
func TestSystemsHaveEmbeddableFallback(t *testing.T) {
	for _, sys := range Systems() {
		hasEmbeddable := false
		for _, f := range ResolveStack(sys.Light.Type.Family) {
			if f.Embeddable {
				hasEmbeddable = true
				break
			}
		}
		if !hasEmbeddable {
			t.Errorf("%s 字族无任何可嵌入字体：%s", sys.ID, sys.Light.Type.Family)
		}
	}
}

func TestDefaultStacks(t *testing.T) {
	for script, stack := range DefaultStacks {
		ok, bad := StackCommercial(stack)
		if !ok {
			t.Errorf("%s 默认栈不可商用：%v", script, bad)
		}
		if !strings.HasSuffix(stack, "sans-serif") && !strings.HasSuffix(stack, "monospace") {
			t.Errorf("%s 默认栈缺少 generic 兜底：%s", script, stack)
		}
	}
}

func TestResolveStack(t *testing.T) {
	specs := ResolveStack(`"Helvetica Neue", Helvetica, "PingFang SC", sans-serif`)
	if len(specs) != 3 {
		t.Fatalf("解析到 %d 项，期望 3（generic 应被忽略）", len(specs))
	}
	if specs[0].License != LicenseProprietary {
		t.Errorf("Helvetica Neue 应为 proprietary，得到 %s", specs[0].License)
	}
	if specs[2].License != LicenseSystemOnly {
		t.Errorf("PingFang SC 应为 system-only，得到 %s", specs[2].License)
	}

	ok, bad := StackCommercial(`"Helvetica Neue", sans-serif`)
	if ok || len(bad) != 1 {
		t.Errorf("含专有字体的栈应判定不可商用：ok=%v bad=%v", ok, bad)
	}

	if ok, _ := StackCommercial(DefaultStacks["latin"]); !ok {
		t.Error("默认栈应可商用")
	}
}

func TestMisouLicenseFlags(t *testing.T) {
	mi, ok := GetFont("misans")
	if !ok {
		t.Fatal("缺少 MiSans 登记")
	}
	if !mi.Commercial || !mi.Embeddable {
		t.Error("MiSans 应可商用且可嵌入")
	}
	if mi.Redistributable {
		t.Error("MiSans 许可禁止再分发字体文件，Redistributable 应为 false")
	}
	if !mi.Attribution {
		t.Error("MiSans 要求注明使用，Attribution 应为 true")
	}

	dy, _ := GetFont("douyin-sans")
	if dy.License != LicenseOFL || !dy.Redistributable {
		t.Errorf("抖音美好体为 OFL，应可再分发：%+v", dy.License)
	}
}

func TestStackLicenseLabel(t *testing.T) {
	label := StackLicenseLabel(DefaultStacks["latin"])
	if !strings.Contains(label, "OFL") {
		t.Errorf("默认栈标签应含 OFL：%s", label)
	}
	if got := StackLicenseLabel("sans-serif"); got != "generic" {
		t.Errorf("纯 generic 栈标签 = %q", got)
	}
}

// 派生体系不应继承基底的品牌字体（否则品牌稿会用错字体）。
func TestDeriveUsesDefaultFont(t *testing.T) {
	sys, err := Derive(DeriveInput{Primary: "#2F6BFF", Name: "测试品牌", BaseSystemID: "douyin"})
	if err != nil {
		t.Fatal(err)
	}
	if sys.Light.Type.Family != DefaultStacks["latin"] {
		t.Errorf("派生体系字族 = %q，期望默认可商用栈", sys.Light.Type.Family)
	}
}
