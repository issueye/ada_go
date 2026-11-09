package host

// ctx.config 的边界：只授予 global.config；命名空间按插件 id 隔离；
// 值走 JSON，路径/权限/原子写由 internal/globalconfig 负责。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/globalconfig"
	"github.com/adag/ada-go/internal/plugins"
)

func TestGlobalConfigContextMember(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	caps := testCaps(t, plugins.TrustVerified, plugins.CapGlobalConfig)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "cfg_probe", description: "配置探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      await ctx.config.set("providers/openai", { baseUrl: "https://example.test" });
      const one = await ctx.config.get("providers/openai");
      const list = await ctx.config.list("providers/");
      const removed = await ctx.config.delete("providers/openai");
      const after = await ctx.config.get("providers/openai");
      return { content: [{ type: "text", text: JSON.stringify({ one, list, removed, after }) }] };
    },
  });
}
`
	script := testScript(t, caps, src)
	tools := &recordingTools{}
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	def := p.Contribution().Tools[0]
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "example.test") || !strings.Contains(text, `"removed":true`) {
		t.Fatalf("ctx.config 结果 = %s", text)
	}
	if !strings.Contains(text, `"after":null`) {
		t.Fatalf("删除后应为 null: %s", text)
	}
	path := filepath.Join(home, globalconfig.DirName, "test-plugin.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置文件未落盘 %s: %v", path, err)
	}
}

func TestGlobalConfigNotInstalledWithoutCapability(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	caps := testCaps(t, plugins.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "cfg_probe", description: "配置探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) {
      return { content: [{ type: "text", text: String(typeof ctx.config) }] };
    },
  });
}
`
	script := testScript(t, caps, src)
	tools := &recordingTools{}
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	def := p.Contribution().Tools[0]
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "undefined" {
		t.Fatalf("未授予时 ctx.config 应为 undefined，得到 %q", got)
	}
}
