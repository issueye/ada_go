package host

// 本文件钉三个「宿主自建模块」的落地行为：
//   - node:fs 按能力安装、只读时写成员调用即抛 ERR_ACCESS_DENIED；
//   - node:child_process 只有 execFile/execFileSync，且真的执行（E-5）；
//   - 全局 process 是宿主给的受限形状（argv/env/cwd/platform）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

// TestNodeFSReadAndWriteTrim 钉 12 §6.12.2 的 fs 偏差：只读时写成员「调用即抛」。
func TestNodeFSReadAndWriteTrim(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty, plugins.CapFSRead)
	if err := os.WriteFile(filepath.Join(caps.WorkspaceRoot, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := `
import fs from "node:fs";
import fsp from "node:fs/promises";
export default function (agent) {
  agent.registerTool({ name: "read_it", description: "读", parameters: { type: "object" }, access: "read",
    execute() { return { content: [{ type: "text", text: fs.readFileSync("/workspace/note.txt", "utf8") }] }; } });
  agent.registerTool({ name: "write_it", description: "写", parameters: { type: "object" }, access: "write",
    execute() {
      try { fs.writeFileSync("/workspace/x.txt", "nope"); }
      catch (e) { return { content: [{ type: "text", text: e.code }] }; }
      return { content: [{ type: "text", text: "wrote" }] };
    } });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	byName := map[string]HandlerRef{}
	for _, tool := range p.Contribution().Tools {
		byName[tool.Name] = tool.Handler
	}
	res, err := p.CallTool(context.Background(), byName["read_it"], json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("read_it: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "hello" {
		t.Fatalf("read_it 内容 = %+v", res.Content)
	}
	res, err = p.CallTool(context.Background(), byName["write_it"], json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("write_it: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != codeAccessDenied {
		t.Fatalf("只读时 writeFileSync 应抛 %s，实际 = %+v", codeAccessDenied, res.Content)
	}
	if _, err := os.Stat(filepath.Join(caps.WorkspaceRoot, "x.txt")); err == nil {
		t.Fatal("被拒绝的写不应真的落盘")
	}
}

// TestNodeFSWithoutCapabilityFailsAtLink 钉「未声明能力 = 链接期失败」。
func TestNodeFSWithoutCapabilityFailsAtLink(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty) // 没有 fs.read/fs.write
	src := `
import fs from "node:fs";
export default function (agent) {}
`
	_, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{})
	if err == nil {
		t.Fatal("没有 fs 能力时 import node:fs 必须在装配期失败")
	}
	if !contains(err.Error(), codeAccessDenied) && !contains(err.Error(), "fs.read") {
		t.Fatalf("错误应点名缺失能力（12 §6.14）：%v", err)
	}
}

// TestChildProcessExecFileSync 钉 E-5：execFile 真的执行、且没有 spawn/exec。
func TestChildProcessExecFileSync(t *testing.T) {
	caps := testCaps(t, plugins.TrustVerified, plugins.CapExec)
	cmd, argv := "cmd.exe", []string{"/c", "echo", "spike-out"}
	if runtime.GOOS != "windows" {
		cmd, argv = "/bin/echo", []string{"spike-out"}
	}
	src := `
import child from "node:child_process";
export default function (agent) {
  agent.registerTool({ name: "run_it", description: "跑", parameters: { type: "object" }, access: "exec",
    execute(args) {
      const out = child.execFileSync(args.cmd, args.argv, { cwd: "/workspace" });
      const members = Object.keys(child).sort().join(",");
      return { content: [{ type: "text", text: String(out.stdout).trim() + "|" + members }] };
    } });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	args, _ := json.Marshal(map[string]any{"cmd": cmd, "argv": argv})
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, args, Context{})
	if err != nil {
		t.Fatalf("run_it: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("内容 = %+v", res.Content)
	}
	text := res.Content[0].Text
	if !contains(text, "spike-out") {
		t.Fatalf("stdout 应为 spike-out，实际 %q", text)
	}
	if contains(text, "spawn") || contains(text, "execSync,") || contains(text, "fork") {
		t.Fatalf("child_process 只应暴露 execFile/execFileSync：%q", text)
	}
}

// TestProcessGlobal 钉全局 process 的受限形状（12 §6.12.2 process 行）。
func TestProcessGlobal(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty)
	caps.Env = map[string]string{"FOO": "bar"}
	src := `
export default function (agent) {
  agent.registerTool({ name: "env_it", description: "读环境", parameters: { type: "object" }, access: "read",
    execute() {
      return { content: [{ type: "text",
        text: [process.platform, process.cwd(), process.env.FOO, typeof process.kill].join("|") }] };
    } });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("env_it: %v", err)
	}
	want := processPlatform() + "|" + DefaultMount + "|bar|undefined"
	if len(res.Content) != 1 || res.Content[0].Text != want {
		t.Fatalf("process 形状 = %+v，期望 %q", res.Content, want)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestHookRegistrationAndCall 钉钩子这条独立通道：on(point, fn) → 钩子链 → 动作结算。
func TestHookRegistrationAndCall(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty)
	caps.Hooks["turn.before"] = plugins.ActionNote
	src := `
export default function (agent) {
  agent.on("turn.before", (evt, ctx) => ({ action: "note", note: "n:" + evt.point }));
}
`
	script := testScript(t, caps, src)
	script.Manifest.Backend.Hooks = map[string]plugins.HookAction{"turn.before": plugins.ActionNote}
	chain := plugins.NewHookChain()
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Hooks: chain}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	verdict, err := chain.Run(context.Background(), ports.HookEvent{Point: ports.HookTurnBefore, ThreadID: "th_1"})
	if err != nil {
		t.Fatalf("HookChain.Run: %v", err)
	}
	if !verdict.Allowed {
		t.Fatalf("note 动作不应改变允许状态: %+v", verdict)
	}
	if len(verdict.Notes) != 1 || verdict.Notes[0] != "n:turn.before" {
		t.Fatalf("notes = %+v，期望 [n:turn.before]", verdict.Notes)
	}
}
