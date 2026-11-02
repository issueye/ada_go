package official_test

// 磁盘加载：桌面打包把 plugins/official 放进 Electron resources，运行时就地加载。

import (
	"os"
	"testing"

	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

func TestScriptsFromRepoDir(t *testing.T) {
	root, err := os.Getwd() // plugins/official
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	scripts, err := official.ScriptsFromDir(root)
	if err != nil {
		t.Fatalf("ScriptsFromDir: %v", err)
	}
	backend := map[string]bool{}
	for _, s := range scripts {
		if s.Manifest != nil {
			backend[s.Manifest.ID] = true
		}
	}
	for _, want := range []string{"tools-plan", "tools-fs", "provider-openai-chat", "policy-default", "plugin-helper", "ui-charts", "mcp-client"} {
		if !backend[want] {
			t.Fatalf("后端插件缺少 %q（现有：%v）", want, backend)
		}
	}
	if backend["token-usage"] {
		t.Fatal("纯前端插件不应出现在后端脚本里")
	}

	frontend, err := official.FrontendScriptsFromDir(root)
	if err != nil {
		t.Fatalf("FrontendScriptsFromDir: %v", err)
	}
	fe := map[string]bool{}
	for _, s := range frontend {
		if s.Manifest != nil {
			fe[s.Manifest.ID] = true
		}
	}
	for _, want := range []string{"tools-plan", "provider-admin", "token-usage", "plugin-helper", "ui-hello-tsx", "ui-charts", "mcp-client"} {
		if !fe[want] {
			t.Fatalf("前端插件缺少 %q（现有：%v）", want, fe)
		}
	}
}

// TestMCPSkillPackaged 钉住 mcp-client 随包技能：插件目录里要真的有
// skills/mcp-tools/SKILL.md，并能被宿主解析成 name/description。
func TestMCPSkillPackaged(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	scripts, err := official.ScriptsFromDir(root)
	if err != nil {
		t.Fatalf("ScriptsFromDir: %v", err)
	}
	for _, s := range scripts {
		if s.Manifest == nil || s.Manifest.ID != "mcp-client" {
			continue
		}
		skills, warns := host.ScriptSkills(s)
		if len(warns) != 0 {
			t.Fatalf("mcp-client 技能解析警告: %v", warns)
		}
		if len(skills) != 1 || skills[0].Meta.Name != "mcp-tools" {
			t.Fatalf("mcp-client 应带技能 mcp-tools: %+v", skills)
		}
		if skills[0].Meta.Description == "" || skills[0].Meta.Path == "" {
			t.Fatalf("技能元数据不完整: %+v", skills[0].Meta)
		}
		return
	}
	t.Fatal("没有加载到 mcp-client 后端脚本")
}
