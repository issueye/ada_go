package host

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

// TestEnvHelper 是被 exec / process 拉起的子进程助手：打印托管环境变量后退出。
// 只有子进程里 ADA_PROBE_ENV 非空时才进入（正常 go test 直接返回）。
func TestEnvHelper(t *testing.T) {
	if os.Getenv("ADA_PROBE_ENV") == "" {
		return
	}
	_, _ = os.Stdout.WriteString("probe=" + os.Getenv("ADA_PROBE_ENV") + ";host=" + os.Getenv("ADA_HOSTONLY_ENV") + "\n")
	os.Exit(0)
}

// TestExecInjectsManagedEnv 钉住环境变量模块的注入：ctx.exec 的子进程看到托管变量
// （同名覆盖宿主环境），未显式给 env 时仍继承宿主环境的其它变量。
func TestExecInjectsManagedEnv(t *testing.T) {
	t.Setenv("ADA_PROBE_ENV", "host")
	t.Setenv("ADA_HOSTONLY_ENV", "host-only")

	caps := testCaps(t, pluginapi.TrustVerified, pluginapi.CapExec)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "env_exec",
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
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{
		EnvVars: map[string]string{"ADA_PROBE_ENV": "managed"},
	}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	args, _ := json.Marshal(map[string]any{
		"exe": os.Args[0], "argv": []string{"-test.run=TestEnvHelper"},
	})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("结果 = %+v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "probe=managed") {
		t.Fatalf("托管变量应覆盖宿主环境: %q", text)
	}
	if !strings.Contains(text, "host=host-only") {
		t.Fatalf("未显式给 env 时应继续继承宿主环境: %q", text)
	}
}

// TestProcessInjectsManagedEnv 钉住 ctx.process.start 的注入：只给清单白名单
// 与托管变量，不继承宿主环境。
func TestProcessInjectsManagedEnv(t *testing.T) {
	t.Setenv("ADA_HOSTONLY_ENV", "host-only")

	caps := testCaps(t, pluginapi.TrustVerified, pluginapi.CapProcess)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "env_proc",
    description: "长驻进程环境变量探针",
    parameters: { type: "object" },
    access: "exec",
    execute: async (args, ctx) => {
      const p = await ctx.process.start(args.exe, args.argv, { timeoutMs: 20000 });
      let text = "";
      for (let i = 0; i < 40; i++) {
        const r = await p.read({ timeoutMs: 1000, maxBytes: 65536 });
        text += r.stdout;
        if (r.exited) break;
      }
      return { content: [{ type: "text", text }] };
    },
  });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{
		EnvVars: map[string]string{"ADA_PROBE_ENV": "managed"},
	}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	args, _ := json.Marshal(map[string]any{
		"exe": os.Args[0], "argv": []string{"-test.run=TestEnvHelper"},
	})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("结果 = %+v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "probe=managed") {
		t.Fatalf("托管变量应注入长驻进程: %q", text)
	}
	if strings.Contains(text, "host=host-only") {
		t.Fatalf("process.start 不应继承宿主环境: %q", text)
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"A=1", "B=2", "PATH=/bin"}
	out := mergeEnv(base, map[string]string{"B": "override", "C": "3"})
	joined := strings.Join(out, "\n")
	for _, want := range []string{"A=1", "B=override", "C=3", "PATH=/bin"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少 %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "B=") != 1 {
		t.Fatalf("同名变量应被覆盖而不是重复:\n%s", joined)
	}
	if got := mergeEnv(base, nil); len(got) != len(base) {
		t.Fatalf("没有覆盖时应原样返回: %v", got)
	}
}
