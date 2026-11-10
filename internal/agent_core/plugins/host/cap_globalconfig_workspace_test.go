package host

// ctx.config.workspace：工作区级命名空间与机器级隔离。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

func TestGlobalConfigWorkspaceScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	ws := t.TempDir()
	caps := testCaps(t, pluginapi.TrustVerified, pluginapi.CapGlobalConfig)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "ws_cfg", description: "工作区配置", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      await ctx.config.set("k", "global");
      await ctx.config.workspace.set("k", "workspace");
      const g = await ctx.config.get("k");
      const gp = ctx.config.path();
      const wp = ctx.config.workspace.path();
      const w = await ctx.config.workspace.get("k");
      return { content: [{ type: "text", text: JSON.stringify({ g, w, gp, wp }) }] };
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
	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{}`), Context{Workspace: ws})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, `"g":"global"`) || !strings.Contains(text, `"w":"workspace"`) {
		t.Fatalf("作用域隔离失败: %s", text)
	}
	wsFile := filepath.Join(ws, ".adago", "config", "test-plugin.json")
	if _, err := os.Stat(wsFile); err != nil {
		t.Fatalf("工作区配置未落盘 %s: %v", wsFile, err)
	}
	machineFile := filepath.Join(home, "config", "test-plugin.json")
	if _, err := os.Stat(machineFile); err != nil {
		t.Fatalf("机器级配置未落盘 %s: %v", machineFile, err)
	}
}
