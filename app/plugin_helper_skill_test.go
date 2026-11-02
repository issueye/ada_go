package app_test

// plugin-helper 的随包技能 plugin-manual：headless 产品开启 skills 后，
// 装配结果暴露 name/description/绝对 path，模型按 path 用 read_file 读正文。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

func TestPluginHelperSkillReadable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "products", "headless.agent.json"))
	if err != nil {
		t.Fatalf("读取 headless 产品声明: %v", err)
	}
	p, err := coreapp.ParseProduct(raw)
	if err != nil {
		t.Fatalf("ParseProduct: %v", err)
	}
	if !p.Capabilities.Skills {
		t.Fatal("headless 必须开启 skills，否则插件随包技能不可读")
	}
	hc := host.Context{Workspace: tempDir(t), Log: func(string, string, map[string]any) {}}
	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{})
	if err != nil {
		t.Fatalf("NewHTTPBridge: %v", err)
	}
	t.Cleanup(func() { bridge.Close() })
	hc.HTTP = bridge
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	// token-usage 等纯前端插件由 Gateway 侧装配；这里只需把前端 id 集合交给装配器。
	frontend, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	frontendIDs := map[string]bool{}
	for _, sc := range frontend {
		if sc.Manifest != nil {
			frontendIDs[sc.Manifest.ID] = true
		}
	}
	asm, err := app.Assemble(context.Background(), p, scripts, app.Deps{Context: hc, FrontendIDs: frontendIDs})
	if err != nil {
		t.Fatalf("app.Assemble: %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })

	var skill *domain.SkillMeta
	for _, m := range asm.Skills() {
		if m.Name == "plugin-manual" {
			meta := m
			skill = &meta
		}
	}
	if skill == nil {
		t.Fatalf("skills=true 且有随包技能时应暴露 plugin-manual（现有：%+v）", asm.Skills())
	}
	if !filepath.IsAbs(skill.Path) {
		t.Fatalf("plugin-manual path 不是绝对路径: %q", skill.Path)
	}
	data, err := os.ReadFile(skill.Path)
	if err != nil {
		t.Fatalf("按 path 读取 plugin-manual 失败（%s）: %v", skill.Path, err)
	}
	text := string(data)
	// 手册要同时覆盖「创建插件」与「adag-ui 动态块」两块内容：
	// 少了后者，模型就只会写 Markdown 纯文本，不会用随包 renderer 出卡片/图表。
	for _, want := range []string{
		"create_plugin", "export default function register", ".adago/plugins", "重载",
		"adag-ui", "ui-charts/bar", "token-usage/usage-card", "fallback",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("技能正文缺少 %q:\n%.400s", want, text)
		}
	}
}
