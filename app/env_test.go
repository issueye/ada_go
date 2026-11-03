package app_test

// 环境变量模块的装配链路：Deps.EnvVars → host.Context.EnvVars → 插件子进程环境。

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// TestAppEnvHelper 是被装配链路测试拉起的子进程助手：打印托管变量后退出。
// 正常 go test 下 ADAG_ENV_PROBE 为空，直接返回。
func TestAppEnvHelper(t *testing.T) {
	if os.Getenv("ADAG_ENV_PROBE") == "" {
		return
	}
	_, _ = os.Stdout.WriteString("probe=" + os.Getenv("ADAG_ENV_PROBE") + "\n")
}

func TestAssemblyInjectsEnvVars(t *testing.T) {
	src := `
export default function (agent) {
  agent.registerTool({
    name: "env_probe",
    description: "环境变量探针",
    parameters: { type: "object" },
    access: "exec",
    execute: async (args, ctx) => {
      const r = await ctx.exec(args.exe, args.argv, { timeoutMs: 20000 });
      return { content: [{ type: "text", text: r.stdout }] };
    },
  });
}
`
	m := &plugins.Manifest{
		ID: "env-fixture", Name: "环境变量夹具", Version: "0.1.0",
		APIVersion: plugins.APIVersionMajor, Trust: plugins.TrustVerified,
		Backend: &plugins.Backend{Entry: "index.js", Capabilities: []plugins.Capability{plugins.CapExec}},
	}
	script := host.Script{
		Manifest:  m,
		EntryName: host.EntryScheme + "://env-fixture/index.js",
		Source:    src,
	}
	ws := tempDir(t)
	hc := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	officialScripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	scripts := append(officialScripts, script)
	asm, err := app.Assemble(context.Background(), testProduct(t, "env-fixture", "policy-default"), scripts, app.Deps{
		Context: hc,
		EnvVars: map[string]string{"ADAG_ENV_PROBE": "from-deps"},
	})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	defer func() { _ = asm.Close(context.Background()) }()

	args := map[string]any{"exe": os.Args[0], "argv": []string{"-test.run=TestAppEnvHelper"}}
	text, _ := callTool(t, asm, hc, "env_probe", args)
	if !strings.Contains(text, "probe=from-deps") {
		t.Fatalf("子进程没拿到 Deps.EnvVars: %q", text)
	}
}
