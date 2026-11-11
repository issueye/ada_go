package official_test

// 磁盘加载：桌面打包把 plugins/official 放进 Electron resources，运行时就地加载。

import (
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/plugins/official"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
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
	for _, want := range []string{"tools-plan", "tools-fs", "provider-openai-chat", "plugin-helper", "ui-charts", "mcp-client", "env-manager"} {
		if !backend[want] {
			t.Fatalf("后端插件缺少 %q（现有：%v）", want, backend)
		}
	}
	for _, s := range scripts {
		if s.Manifest != nil && s.Manifest.ID == "provider-admin" {
			if s.Manifest.Backend == nil || s.Manifest.Backend.ConfigNamespace != "provider-config" {
				t.Fatalf("provider-admin configNamespace = %+v", s.Manifest.Backend)
			}
		}
		if s.Manifest != nil && (s.Manifest.ID == "token-usage" || s.Manifest.ID == "composer-telemetry") {
			ok := false
			if s.Manifest.Backend != nil {
				for _, c := range s.Manifest.Backend.Capabilities {
					if string(c) == "events.read" {
						ok = true
					}
				}
			}
			if !ok {
				t.Fatalf("%s 后端应声明 events.read: %+v", s.Manifest.ID, s.Manifest.Backend)
			}
		}
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
	// SSR 前端资产已删（dev-plan R4）；M 阶段 slots/pages 以 component（SFC）形态回归
	// （proposals/spa-frontend-framework.md §7.1）。SSR 期字段（client/scripts/
	// hydrate/clientOnly/template）在结构体层面已不存在，这里钉住：官方插件不允许
	// client bundle 与 scripts 静态 JS（可执行代码只允许 SFC + ESM 形态）。
	for _, want := range []string{"tools-plan", "provider-admin", "token-usage", "ui-hello", "ui-charts", "mcp-client", "env-manager", "composer-telemetry"} {
		if !fe[want] {
			t.Fatalf("前端插件缺少 %q（现有：%v）", want, fe)
		}
	}
	for _, s := range frontend {
		if s.Manifest == nil || s.Manifest.Frontend == nil {
			continue
		}
		f := s.Manifest.Frontend
		if f.Client != "" || len(f.Scripts) != 0 {
			t.Fatalf("%s 的 frontend 段仍声明 SSR 期可执行资产（client/scripts）: %+v", s.Manifest.ID, f)
		}
	}
	if fe["plugin-helper"] {
		t.Fatal("plugin-helper 不应有前端段：创建插件只走对话工具（前端向导页已移除）")
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

// TestOfficialPluginBlocksAvoidNativeSelect：插件 TSX（随包只余 block 模板）
// 不能用原生 <select>——原生弹层由系统绘制，样式与键盘行为都不受控。
// （页面/脚本面已随 SSR 删除；SFC 时代的等价检查进 verify preflight，V 阶段落地。）
func TestOfficialPluginBlocksAvoidNativeSelect(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	pages, err := filepath.Glob(filepath.Join(root, "*", "*.tsx"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("没有扫描到官方插件 TSX 模板: %v", err)
	}
	checks := []struct {
		name string
		re   *regexp.Regexp
	}{
		// 只匹配小写原生标签；<Select> 是 ssr:components 的宿组件，不算原生下拉。
		{"原生 <select> 标签", regexp.MustCompile(`<select[\s>]`)},
		{"动态创建原生 select", regexp.MustCompile(`createElement\(\s*["']select["']`)},
	}
	for _, path := range pages {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", path, err)
		}
		for _, check := range checks {
			if loc := check.re.FindIndex(raw); loc != nil {
				line := 1 + strings.Count(string(raw[:loc[0]]), "\n")
				t.Fatalf("%s:%d 使用了%s；请改用 ssr:components 的 Select 组件", path, line, check.name)
			}
		}
	}
}
