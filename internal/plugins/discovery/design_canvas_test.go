package discovery

// 仓库内示例插件 plugins/design-canvas 的回归门禁。
//
// 它是「安装型插件」的参考实现（backend + frontend.blocks，无 pages/slots），
// 清单字段、入口形状、动态块契约任何一处改坏都应该在这里被拦下来 ——
// 而不是等到用户 `adag plugin install` 之后才发现页面是空的。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

// copyTree 把插件目录复制到临时根下的 <id>/，避免测试直接依赖仓库路径的写权限。
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyTree(t, from, to)
			continue
		}
		data, err := os.ReadFile(from)
		if err != nil {
			t.Fatalf("read %s: %v", from, err)
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", to, err)
		}
	}
}

func TestDesignCanvasSamplePlugin(t *testing.T) {
	src := filepath.Join("..", "..", "..", "plugins", "design-canvas")
	if _, err := os.Stat(filepath.Join(src, "plugin.json")); err != nil {
		t.Skipf("示例插件不存在，跳过: %v", err)
	}

	root := t.TempDir()
	dir := filepath.Join(root, "design-canvas")
	copyTree(t, src, dir)

	loaded, warns, err := LoadDirDetailed(root, SourceGlobal)
	if err != nil {
		t.Fatalf("LoadDirDetailed: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("发现阶段不应有警告: %v", warns)
	}
	if len(loaded) != 1 {
		t.Fatalf("应加载 1 个插件，实际 %d", len(loaded))
	}

	p := loaded[0]
	m := p.Script.Manifest
	if m == nil {
		t.Fatal("清单为空")
	}
	if m.ID != "design-canvas" {
		t.Errorf("id = %q", m.ID)
	}
	if m.APIVersion != 1 {
		t.Errorf("apiVersion = %d", m.APIVersion)
	}
	if m.Trust != plugins.TrustThirdParty {
		t.Errorf("trust = %q，示例插件必须是 third-party", m.Trust)
	}
	if p.Source != SourceGlobal || p.Fingerprint == "" {
		t.Errorf("来源/指纹异常: source=%q fingerprint=%q", p.Source, p.Fingerprint)
	}

	// 能力位：只声明实际用到的两个（多声明 = 权限过宽）
	if m.Backend == nil || m.Backend.Entry == "" {
		t.Fatal("缺少 backend.entry")
	}
	got := map[plugins.Capability]bool{}
	for _, c := range m.Backend.Capabilities {
		got[c] = true
	}
	for _, want := range []plugins.Capability{plugins.Capability("net.fetch"), plugins.Capability("state")} {
		if !got[want] {
			t.Errorf("缺少能力 %q", want)
		}
	}
	if len(got) != 2 {
		t.Errorf("能力应只有 net.fetch/state，实际 %v", m.Backend.Capabilities)
	}
	if len(m.Backend.Secrets) != 0 {
		t.Errorf("回环画布不需要密钥，secrets = %v", m.Backend.Secrets)
	}

	// 入口：必须是可执行的 ESM 工厂，且不出现 MCP 风格字段 / 全局 fetch
	entryBytes, err := os.ReadFile(filepath.Join(dir, m.Backend.Entry))
	if err != nil {
		t.Fatalf("读入口: %v", err)
	}
	entry := string(entryBytes)
	if !strings.Contains(entry, "export default function register(agent)") {
		t.Error("入口缺少 export default function register(agent)")
	}
	for _, banned := range []string{"inputSchema:", "require(", "\nimport "} {
		if strings.Contains(entry, banned) {
			t.Errorf("入口出现反模式 %q", banned)
		}
	}

	// 前端段：安装型插件不能声明 pages/slots（声明了也不会被装配）
	if m.Frontend == nil {
		t.Fatal("缺少 frontend 段（动态块）")
	}
	if len(m.Frontend.Pages) != 0 || len(m.Frontend.Slots) != 0 {
		t.Error("安装型插件不应声明 pages/slots：渲染桥只装配随包官方插件")
	}
	if len(m.Frontend.Styles) != 0 || len(m.Frontend.Scripts) != 0 {
		t.Error("安装型插件不应声明 styles/scripts：不会被装配，块要复用宿主令牌类")
	}
	if len(m.Frontend.Blocks) != 1 {
		t.Fatalf("应声明 1 个动态块，实际 %d", len(m.Frontend.Blocks))
	}

	block := m.Frontend.Blocks[0]
	if block.Name != "canvas-ledger" {
		t.Errorf("块名 = %q", block.Name)
	}
	if block.Profile != "display" {
		t.Errorf("profile 必须是 display（interactive 仅 builtin/verified），实际 %q", block.Profile)
	}
	for _, rel := range []string{block.Schema, block.Template} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("清单引用的文件不存在: %s", rel)
		}
	}

	// 块数据契约：封闭 schema（多一个键就整块降级）
	rawSchema, err := os.ReadFile(filepath.Join(dir, block.Schema))
	if err != nil {
		t.Fatalf("读 schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Error("schema 必须 additionalProperties: false")
	}
	required, _ := schema["required"].([]any)
	if len(required) == 0 {
		t.Error("schema 缺少 required")
	}

	// 模板：display profile 白名单之外的元素会让整块降级
	tplBytes, err := os.ReadFile(filepath.Join(dir, block.Template))
	if err != nil {
		t.Fatalf("读模板: %v", err)
	}
	tpl := string(tplBytes)
	for _, banned := range []string{"<img", "<svg", "<form", "<input", "<button", "style=", "dangerouslySetInnerHTML"} {
		if strings.Contains(tpl, banned) {
			t.Errorf("块模板出现 display profile 禁用项 %q", banned)
		}
	}
	if !strings.Contains(tpl, "export default function") {
		t.Error("块模板缺少 export default function")
	}

	// 随包技能：frontmatter 的 name 必须等于目录名
	skillPath := filepath.Join(dir, "skills", "design-workflow", "SKILL.md")
	skillBytes, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("读技能: %v", err)
	}
	if !strings.Contains(string(skillBytes), "name: design-workflow") {
		t.Error("技能 frontmatter 的 name 必须等于目录名")
	}
}
