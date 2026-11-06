package design

import (
	"regexp"
	"strings"
	"testing"
)

func TestIconRegistryInvariants(t *testing.T) {
	icons := Icons()
	if len(icons) < 24 {
		t.Fatalf("图标太少：%d", len(icons))
	}
	nameRe := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	for _, ic := range icons {
		if !nameRe.MatchString(ic.Name) {
			t.Errorf("%q 不符合 kebab-case 命名", ic.Name)
		}
		if strings.TrimSpace(ic.Body) == "" {
			t.Errorf("%q 路径为空", ic.Name)
		}
		if strings.Contains(ic.Body, "http") {
			t.Errorf("%q 含外链引用（预览必须自包含）", ic.Name)
		}
		if strings.Contains(ic.Body, "<text") {
			t.Errorf("%q 用了 <text>，图标必须是矢量路径", ic.Name)
		}
		if ic.Group == "" {
			t.Errorf("%q 缺少分组", ic.Name)
		}
		if !HasIcon(ic.Name) {
			t.Errorf("%q 未被注册", ic.Name)
		}
	}
}

func TestIconSpriteCoversAll(t *testing.T) {
	sprite := IconSprite()
	icons := Icons()
	if got := strings.Count(sprite, "<symbol "); got != len(icons) {
		t.Fatalf("symbol 数 = %d，图标数 = %d", got, len(icons))
	}
	if strings.Contains(sprite, `href="http`) {
		t.Fatal("精灵不应包含外链")
	}
	for _, ic := range icons {
		if !strings.Contains(sprite, `id="ds-ic-`+ic.Name+`"`) {
			t.Fatalf("精灵缺少 %s", ic.Name)
		}
	}
}

func TestExpandIcons(t *testing.T) {
	cases := map[string]string{
		`<span>{{icon:close}}</span>`: `<use href="#ds-ic-close"/>`,
		`{{icon:chevron-down:16}}`:    `ds-icon--16`,
		`{{icon:chevron-down:24}}`:    `ds-icon--24`,
		`无占位符的文本`:                     `无占位符的文本`,
		`{{icon:not-exist}}`:          ``,
	}
	for in, want := range cases {
		got := ExpandIcons(in)
		if !strings.Contains(got, want) {
			t.Errorf("ExpandIcons(%q) = %q，期望包含 %q", in, got, want)
		}
	}
	// 未知图标展开为空串后不应残留占位符
	if got := ExpandIcons(`{{icon:not-exist}}`); strings.Contains(got, "{{") {
		t.Errorf("残留占位符: %q", got)
	}
}

func TestIconSearchByAlias(t *testing.T) {
	for _, q := range []string{"✕", "▾", "›", "◉", "🔍", "♥"} {
		if len(SearchIcons(q)) == 0 {
			t.Errorf("别名 %q 未命中任何图标（替换历史字符时用得到）", q)
		}
	}
	if len(SearchIcons("status")) < 3 {
		t.Error("按分组名检索 status 命中过少")
	}
	if got := SearchIcons("chevron"); len(got) != 4 {
		t.Errorf("chevron 系列应有 4 个，实际 %d", len(got))
	}
}

func TestIconCatalogPayload(t *testing.T) {
	cat := IconCatalog(IconGroupStatus)
	if len(cat) == 0 {
		t.Fatal("status 分组为空")
	}
	for _, item := range cat {
		if item["svg"] == "" || item["usage"] == "" {
			t.Fatalf("目录项缺少 svg/usage: %v", item["name"])
		}
	}
	if len(IconCatalog("")) != len(Icons()) {
		t.Error("空分组应返回全部图标")
	}
}
