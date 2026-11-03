package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/binpaths"
	"github.com/adag/ada-go/internal/plugins"
)

// TestPathHelper 是子进程助手：打印 PATH 后退出（父进程里未设置门禁变量，直接返回）。
func TestPathHelper(t *testing.T) {
	if os.Getenv("ADAG_PATH_HELPER") != "1" {
		return
	}
	fmt.Printf("PATH=%s\n", os.Getenv("PATH"))
}

// TestExecPrependsServiceBinPath 钉住 ctx.exec 的 PATH：工作区 .adago/bin 与用户级
// ~/.adago/bin 前置到 PATH（工作区优先、去重），原有 PATH 保留在后面。
func TestExecPrependsServiceBinPath(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	t.Setenv("ADAG_PATH_HELPER", "1")

	caps := testCaps(t, plugins.TrustVerified, plugins.CapExec)
	dirs, err := binpaths.Dirs(caps.WorkspaceRoot)
	if err != nil {
		t.Fatalf("binpaths.Dirs: %v", err)
	}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "path_probe",
    description: "PATH 探针",
    parameters: { type: "object" },
    access: "exec",
    execute: async (args, ctx) => {
      const r = await ctx.exec(args.exe, args.argv, { timeoutMs: 20000 });
      return { content: [{ type: "text", text: r.stdout }] };
    },
  });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{
		Targets: Targets{Tools: &recordingTools{}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	args, _ := json.Marshal(map[string]any{
		"exe": os.Args[0], "argv": []string{"-test.run=TestPathHelper"},
	})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	line := ""
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "PATH=") {
			line = strings.TrimPrefix(ln, "PATH=")
			break
		}
	}
	if line == "" {
		t.Fatalf("没有拿到 PATH: %q", text)
	}
	sep := string(os.PathListSeparator)
	parts := strings.Split(line, sep)
	if len(parts) < 3 {
		t.Fatalf("PATH 太短: %v", parts)
	}
	if parts[0] != dirs[0].Path || parts[1] != dirs[1].Path {
		t.Fatalf("服务程序目录应前置（工作区优先）: %v", parts[:2])
	}
	if got := strings.Count(line, dirs[0].Path); got != 1 {
		t.Fatalf("PATH 里重复了工作区目录: %v", parts)
	}
	hostFirst := strings.Split(strings.TrimSpace(os.Getenv("PATH")), sep)[0]
	if hostFirst != "" {
		idx := -1
		for i, part := range parts {
			if part == hostFirst {
				idx = i
				break
			}
		}
		if idx < 2 {
			t.Fatalf("原有 PATH 应在服务程序目录之后: %v", parts)
		}
	}
}

// TestProcessStartsWithServiceBinPath 钉住 ctx.process.start 的 PATH：白名单里
// 没有 PATH 时，用服务程序目录填一个；不会凭空继承宿主 PATH。
func TestProcessStartsWithServiceBinPath(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())

	caps := testCaps(t, plugins.TrustVerified, plugins.CapProcess)
	caps.Env = map[string]string{"ADAG_PATH_HELPER": "1"}
	dirs, err := binpaths.Dirs(caps.WorkspaceRoot)
	if err != nil {
		t.Fatalf("binpaths.Dirs: %v", err)
	}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "path_proc",
    description: "长驻进程 PATH 探针",
    parameters: { type: "object" },
    access: "exec",
    execute: async (args, ctx) => {
      const p = await ctx.process.start(args.exe, args.argv, { timeoutMs: 20000, env: ["ADAG_PATH_HELPER"] });
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
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{
		Targets: Targets{Tools: &recordingTools{}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	args, _ := json.Marshal(map[string]any{
		"exe": os.Args[0], "argv": []string{"-test.run=TestPathHelper"},
	})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	line := ""
	for _, ln := range strings.Split(text, "\n") {
		if strings.HasPrefix(ln, "PATH=") {
			line = strings.TrimPrefix(ln, "PATH=")
			break
		}
	}
	if line == "" {
		t.Fatalf("没有拿到 PATH: %q", text)
	}
	want := strings.Join([]string{dirs[0].Path, dirs[1].Path}, string(os.PathListSeparator))
	if line != want {
		t.Fatalf("PATH = %q，期望只有服务程序目录 %q", line, want)
	}
}
