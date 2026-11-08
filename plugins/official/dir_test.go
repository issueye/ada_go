package official_test

// 磁盘加载：桌面打包把 plugins/official 放进 Electron resources，运行时就地加载。

import (
	"github.com/adag/ada-go/internal/plugins/host"
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
	for _, want := range []string{"tools-plan", "tools-fs", "provider-openai-chat", "policy-default", "plugin-helper", "ui-charts", "mcp-client", "env-manager"} {
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
	for _, want := range []string{"tools-plan", "provider-admin", "token-usage", "ui-hello-tsx", "ui-charts", "mcp-client", "env-manager", "composer-telemetry"} {
		if !fe[want] {
			t.Fatalf("前端插件缺少 %q（现有：%v）", want, fe)
		}
	}
	if fe["plugin-helper"] {
		t.Fatal("plugin-helper 不应有前端段：创建插件只走对话工具（前端向导页已移除）")
	}
}

// TestClientBundlesHonorCSP 钉住随包浏览器 bundle：pluginbuild 产物不能含
// eval/new Function；client.tsx 与 static/client.js 的同步由 pluginbuild --check
// 在发布流程里校验（这里至少守住已提交产物）。
func TestClientBundlesHonorCSP(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("*", "static", "client.js"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) < 3 {
		t.Fatalf("随包 client bundle 数量 = %d，期望至少 3（ui-hello-tsx/token-usage/provider-admin）", len(matches))
	}
	danger := regexp.MustCompile(`\beval\s*\(|new\s+Function\s*\(`)
	for _, name := range matches {
		data, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("读取 %s: %v", name, rerr)
		}
		if loc := danger.FindIndex(data); loc != nil {
			t.Fatalf("%s 含 CSP 禁止的 eval/new Function（字节 %d 附近）", name, loc[0])
		}
	}
}

// TestClientBundleSizes 记录随包 bundle 体积并守住 512KB 上限（与 extAssetMaxBytes 同口径）。
func TestClientBundleSizes(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("*", "static", "client.js"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	const maxBytes = 512 << 10
	for _, name := range matches {
		info, serr := os.Stat(name)
		if serr != nil {
			t.Fatalf("Stat %s: %v", name, serr)
		}
		t.Logf("%s = %d bytes", name, info.Size())
		if info.Size() > maxBytes {
			t.Fatalf("%s = %d bytes，超过 %d 上限", name, info.Size(), maxBytes)
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

// TestOfficialPluginPagesAvoidNativeSelect：插件页的下拉必须走宿主 Select 组件
// （SSR 成 <ada-select>），不能用原生 <select>——原生弹层由系统绘制，样式与键盘
// 行为都不受控。页面源码与浏览器脚本都按同一规则检查。
func TestOfficialPluginPagesAvoidNativeSelect(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	pages, err := filepath.Glob(filepath.Join(root, "*", "*.tsx"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("没有扫描到官方插件页面模板: %v", err)
	}
	scripts, err := filepath.Glob(filepath.Join(root, "*", "static", "*.js"))
	if err != nil {
		t.Fatalf("Glob static: %v", err)
	}
	files := append(pages, scripts...)
	checks := []struct {
		name string
		re   *regexp.Regexp
	}{
		// 只匹配小写原生标签；<Select> 是 ssr:components 的宿组件，不算原生下拉。
		{"原生 <select> 标签", regexp.MustCompile(`<select[\s>]`)},
		{"动态创建原生 select", regexp.MustCompile(`createElement\(\s*["']select["']`)},
	}
	for _, path := range files {
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

// TestOfficialPluginScriptsAvoidNativeDialogs：官方插件页面脚本不能用原生
// window.confirm/alert/prompt——确认统一走基座的共享 ConfirmDialog（window.adaConfirm）。
func TestOfficialPluginScriptsAvoidNativeDialogs(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(root, "*", "static", "*.js"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("没有扫描到任何官方插件页面脚本")
	}
	native := regexp.MustCompile(`window\.(?:confirm|alert|prompt)\s*\(`)
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s: %v", path, err)
		}
		if loc := native.FindIndex(raw); loc != nil {
			line := 1 + strings.Count(string(raw[:loc[0]]), "\n")
			t.Fatalf("%s:%d 使用了原生对话框；请改用 window.adaConfirm / 页面内提示", path, line)
		}
	}
}
