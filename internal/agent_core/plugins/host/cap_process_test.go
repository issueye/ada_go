package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins"
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

// TestProcessBigHelper 往 stdout 写 10KB 后退出：用来钉住 process.read 的分片语义。
func TestProcessBigHelper(t *testing.T) {
	if os.Getenv("ADAG_PROCESS_HELPER") != "1" {
		return
	}
	_, _ = os.Stdout.Write([]byte(strings.Repeat("0123456789", 1000)))
	os.Exit(0)
}

// TestProcessReadKeepsRemainder：read(maxBytes) 必须返回头部片段并把余量留到下一次，
// 不能 tail 截断丢字节——stdio MCP 的按行 JSON-RPC 依赖这一点。
func TestProcessReadKeepsRemainder(t *testing.T) {
	caps := testCaps(t, plugins.TrustVerified, plugins.CapProcess)
	caps.Env = map[string]string{"ADAG_PROCESS_HELPER": "1"}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_big",
    description: "大输出探针",
    parameters: { type: "object" },
    access: "exec",
    execute: async (args, ctx) => {
      const p = await ctx.process.start(args.exe, args.argv, { timeoutMs: 20000 });
      let text = "";
      for (let i = 0; i < 400; i++) {
        const r = await p.read({ timeoutMs: 1000, maxBytes: 128 });
        text += r.stdout;
        if (r.exited && !r.stdout) break;
      }
      p.kill();
      return { content: [{ type: "text", text }], details: { bytes: text.length } };
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
		"exe": os.Args[0], "argv": []string{"-test.run=TestProcessBigHelper"},
	})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	want := strings.Repeat("0123456789", 1000)
	if len(res.Content) != 1 || res.Content[0].Text != want {
		got := ""
		if len(res.Content) == 1 {
			got = res.Content[0].Text
		}
		t.Fatalf("分片读取丢字节：want %d bytes, got %d bytes", len(want), len(got))
	}
}

func TestSplitHeadUTF8(t *testing.T) {
	s := "a中b" // 1 + 3 + 1 = 5 bytes
	head, rest := splitHeadUTF8(s, 3)
	if head != "a" || rest != "中b" {
		t.Fatalf("切点未回退到字符边界：head=%q rest=%q", head, rest)
	}
	head, rest = splitHeadUTF8(s, 4)
	if head != "a中" || rest != "b" {
		t.Fatalf("整字符切分：head=%q rest=%q", head, rest)
	}
	if head, rest := splitHeadUTF8(s, 10); head != s || rest != "" {
		t.Fatalf("未超限时应原样返回：head=%q rest=%q", head, rest)
	}
}
