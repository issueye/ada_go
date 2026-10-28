package host

// 技能发现（6.7）的单元验收：frontmatter 解析、包内扫描、非法技能跳过并告警。

import (
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

func TestParseSkillOK(t *testing.T) {
	raw := "---\nname: clean-code\ndescription: \"改动纪律：先读后改。\"\n---\n\n# 正文标题\n\n第一步。\n"
	meta, body, err := ParseSkill("clean-code", raw)
	if err != nil {
		t.Fatalf("ParseSkill: %v", err)
	}
	if meta.Name != "clean-code" || meta.Description != "改动纪律：先读后改。" {
		t.Fatalf("meta = %+v", meta)
	}
	if body != "\n# 正文标题\n\n第一步。\n" && strings.TrimSpace(body) != "# 正文标题\n\n第一步。" {
		t.Fatalf("body = %q", body)
	}
	if strings.Contains(body, "description") {
		t.Fatalf("正文不应包含 frontmatter: %q", body)
	}
}

func TestParseSkillNameOptionalButMustMatch(t *testing.T) {
	// 缺 name 字段：名字以目录名为准，允许（Agent Skills 的 name 只用于校验一致性）。
	if _, _, err := ParseSkill("my-skill", "---\ndescription: ok\n---\n正文"); err != nil {
		t.Fatalf("缺 name 应允许: %v", err)
	}
	// name 与目录名不一致：拒绝。
	if _, _, err := ParseSkill("my-skill", "---\nname: other\ndescription: ok\n---\n正文"); err == nil {
		t.Fatal("name 与目录名不一致必须报错")
	}
}

func TestParseSkillRejects(t *testing.T) {
	cases := []struct {
		name    string
		dirName string
		raw     string
		wantErr string
	}{
		{"缺 description", "a", "---\nname: a\n---\n正文", "description"},
		{"没有 frontmatter", "a", "直接正文", "frontmatter"},
		{"frontmatter 未闭合", "a", "---\ndescription: ok\n正文", "闭合"},
		{"目录名非法", "Bad_Name", "---\ndescription: ok\n---\n正文", "目录名"},
		{"description 超长", "a", "---\ndescription: " + strings.Repeat("长", 1100) + "\n---\n正文", "1024"},
	}
	for _, tc := range cases {
		_, _, err := ParseSkill(tc.dirName, tc.raw)
		if err == nil {
			t.Fatalf("%s: 期望报错", tc.name)
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("%s: 错误 %q 应包含 %q", tc.name, err.Error(), tc.wantErr)
		}
	}
}

func TestScriptSkillsScan(t *testing.T) {
	script := Script{
		Manifest: &plugins.Manifest{ID: "demo"},
		StaticAssets: map[string][]byte{
			"skills/beta/SKILL.md":      []byte("---\nname: beta\ndescription: 第二个\n---\nB 正文"),
			"skills/alpha/SKILL.md":     []byte("---\ndescription: 第一个\n---\nA 正文"),
			"skills/broken/SKILL.md":    []byte("没有 frontmatter"),
			"skills/deep/nest/SKILL.md": []byte("---\ndescription: 路径形状非法\n---\nX"),
			"README.md":                 []byte("无关文件"),
		},
	}
	skills, warns := ScriptSkills(script)
	if len(skills) != 2 {
		t.Fatalf("应解析出 2 个技能（跳过非法），得到 %d：%+v（warns=%v）", len(skills), skills, warns)
	}
	// 按名字排序：map 遍历顺序不定，顺序必须稳定。
	if skills[0].Meta.Name != "alpha" || skills[1].Meta.Name != "beta" {
		t.Fatalf("技能应按名字排序: %+v", skills)
	}
	if skills[0].Meta.Origin != SkillOriginPlugin || skills[0].Meta.PluginID != "demo" {
		t.Fatalf("origin/pluginId 未填: %+v", skills[0].Meta)
	}
	if skills[0].Body != "\nA 正文" && strings.TrimSpace(skills[0].Body) != "A 正文" {
		t.Fatalf("body = %q", skills[0].Body)
	}
	if len(warns) != 2 {
		t.Fatalf("两个非法技能都应记警告: %v", warns)
	}
	joined := strings.Join(warns, "; ")
	if !strings.Contains(joined, "broken") || !strings.Contains(joined, "nest") {
		t.Fatalf("警告应点名被跳过的技能: %v", warns)
	}
}

func TestScriptSkillsEmpty(t *testing.T) {
	skills, warns := ScriptSkills(Script{Manifest: &plugins.Manifest{ID: "demo"}})
	if len(skills) != 0 || len(warns) != 0 {
		t.Fatalf("没有技能时应返回空: %+v %v", skills, warns)
	}
}
