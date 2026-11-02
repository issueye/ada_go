package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

// TestProcessHelper 是子进程侧的测试助手：读 stdin 行，回写 echo:<line>。
// 普通测试运行时直接返回；只有被 ctx.process.start 拉起（带环境变量）时才进入循环。
func TestProcessHelper(t *testing.T) {
	if os.Getenv("ADAG_PROCESS_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fmt.Printf("echo:%s\n", sc.Text())
	}
	os.Exit(0)
}

func TestProcessContext(t *testing.T) {
	caps := testCaps(t, plugins.TrustVerified, plugins.CapProcess)
	caps.Env = map[string]string{"ADAG_PROCESS_HELPER": "1"}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe",
    description: "长驻进程探针",
    parameters: { type: "object" },
    access: "exec",
    execute: async (args, ctx) => {
      const p = await ctx.process.start(args.exe, args.argv, { timeoutMs: 10000 });
      p.write("hello\n");
      let text = "";
      for (let i = 0; i < 20; i++) {
        const r = await p.read({ timeoutMs: 500 });
        text += r.stdout;
        if (text.includes("echo:hello")) break;
      }
      p.kill();
      return { content: [{ type: "text", text }], details: { pid: p.pid, taskId: p.taskId } };
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
		"exe": os.Args[0], "argv": []string{"-test.run=TestProcessHelper"},
	})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echo:hello\n" {
		t.Fatalf("进程输出 = %q", res.Content)
	}
	if res.Details["pid"].(float64) <= 0 {
		t.Fatalf("pid = %v", res.Details["pid"])
	}
}
