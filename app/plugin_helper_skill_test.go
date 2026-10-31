package app_test

// plugin-helper 的随包技能 create-plugin：headless 产品开启 skills 后，
// read_skill 注册并能读到正文——这是「AI 知道怎么使用本插件」的机制保证。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/adapters"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/toolkit"
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

	if _, ok := asm.Tools.Lookup(context.Background(), "read_skill"); !ok {
		t.Fatalf("skills=true 且有随包技能时必须注册 read_skill（现有：%v）", toolNames(asm))
	}

	exec, err := toolkit.NewExecutor(toolkit.ExecutorConfig{
		Catalog:   asm.Tools,
		Invoker:   asm.ToolInvoker(),
		Clock:     adapters.SystemClock{},
		HasPolicy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := exec.Execute(context.Background(), domain.ToolCall{
		CallID: "c1", Tool: "read_skill", Args: json.RawMessage(`{"skill":"create-plugin"}`),
	})
	if res.Receipt.Status != domain.ReceiptOK {
		t.Fatalf("read_skill(create-plugin) 回执 = %#v", res.Receipt)
	}
	text := res.Content[0].Text
	for _, want := range []string{"create_plugin", "export default function register", ".adago/plugins", "重载"} {
		if !strings.Contains(text, want) {
			t.Fatalf("技能正文缺少 %q:\n%.400s", want, text)
		}
	}
}
