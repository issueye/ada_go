package host

// ctx.processes：进程监督桥（capability: process.supervise，仅 builtin）。
//
// 这里验证三件事：能力位缺失时 ctx.processes 不存在；entry 被解析到插件目录内
// 并拒绝越界；owner 由宿主盖章传给桥（脚本传的 pluginId 不采信）。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins"
)

// fakeSupervisor 记录桥调用，返回固定状态。
type fakeSupervisor struct {
	register   ProcessSpec
	pluginID   string
	registerOK bool
	err        error
	lastOp     string
	// unregistered 记录注销调用（关闭 Runtime 时按注册记录逐个注销，顺序不定）。
	unregistered []string
}

func (f *fakeSupervisor) Register(_ context.Context, pluginID string, spec ProcessSpec) (ProcessStatus, error) {
	f.register, f.pluginID = spec, pluginID
	f.lastOp = "register"
	if f.err != nil {
		return ProcessStatus{}, f.err
	}
	f.registerOK = true
	return ProcessStatus{
		ID: pluginID + "/" + spec.Name, Owner: pluginID, Name: spec.Name, Scope: spec.Scope,
		Runtime: spec.Runtime, PID: 4242, Running: true, Refs: 1,
		StartMode: spec.Start, Restart: spec.Restart,
	}, nil
}

func (f *fakeSupervisor) Ensure(_ context.Context, pluginID, name, _ string) (ProcessStatus, error) {
	f.lastOp = "ensure"
	if f.err != nil {
		return ProcessStatus{}, f.err
	}
	return ProcessStatus{ID: pluginID + "/" + name, Owner: pluginID, Name: name, Running: true}, nil
}

func (f *fakeSupervisor) Stop(_ context.Context, pluginID, name, _ string) (ProcessStatus, error) {
	f.lastOp = "stop"
	if f.err != nil {
		return ProcessStatus{}, f.err
	}
	return ProcessStatus{ID: pluginID + "/" + name, Owner: pluginID, Name: name}, nil
}

func (f *fakeSupervisor) Status(_ context.Context, pluginID, name, _ string) (ProcessStatus, bool) {
	f.lastOp = "status"
	if f.err != nil {
		return ProcessStatus{}, false
	}
	return ProcessStatus{ID: pluginID + "/" + name, Owner: pluginID, Name: name, Running: true}, true
}

func (f *fakeSupervisor) Unregister(_ context.Context, pluginID, name, _ string) error {
	f.lastOp = "unregister"
	f.unregistered = append(f.unregistered, pluginID+"/"+name)
	return f.err
}

func (f *fakeSupervisor) List(_ context.Context, pluginID string) ([]ProcessStatus, error) {
	f.lastOp = "list"
	if f.err != nil {
		return nil, f.err
	}
	return []ProcessStatus{{ID: pluginID + "/scheduler", Owner: pluginID, Name: "scheduler"}}, nil
}

// testScriptWithRoot 造一个带磁盘目录的插件脚本（进程 entry 必须落在该目录内）。
func testScriptWithRoot(t *testing.T, caps *CapabilitySet, source, root string) Script {
	t.Helper()
	sc := testScript(t, caps, source)
	sc.Root = root
	return sc
}

// 桥未注入（CLI 直连）时 ctx.processes 不存在。
func TestProcessesAbsentWithoutBridge(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    execute(args, ctx) { return { content: [{ type: "text", text: String(typeof ctx.processes) }] }; },
  });
}
`
	script := testScript(t, caps, src)
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "undefined" {
		t.Fatalf("没有控制面时不应安装 ctx.processes: %q", got)
	}
}

// 注册成功：entry 被解析成插件目录内的绝对路径，owner 由能力集盖章。
func TestProcessesRegisterResolvesEntry(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	root := t.TempDir()
	scriptPath := filepath.Join(root, "dist", "scheduler.js")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("// noop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      try {
        const st = await ctx.processes.register({
          name: "scheduler", runtime: "node", entry: "dist/scheduler.js",
          start: "on_enable", restart: "on_failure",
          // 伪造身份：宿主必须忽略它。
          owner: "attacker", pluginId: "attacker",
        });
        return { content: [{ type: "text", text: JSON.stringify(st) }] };
      } catch (e) {
        return { content: [{ type: "text", text: "ERR:" + (e && e.code ? e.code : e) + ":" + (e && e.message ? e.message : "") }] };
      }
    },
  });
}
`
	sup := &fakeSupervisor{}
	sc := testScriptWithRoot(t, caps, src, root)
	p, err := Load(context.Background(), sc, caps, Context{Processes: sup},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	hc := Context{PluginID: caps.PluginID, Processes: sup}
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].Text
	if strings.HasPrefix(text, "ERR:") {
		t.Fatalf("注册失败: %s", text)
	}
	if !strings.Contains(text, `"running":true`) || !strings.Contains(text, `"pid":4242`) {
		t.Fatalf("回执缺少状态字段: %s", text)
	}
	if sup.pluginID != caps.PluginID {
		t.Fatalf("owner 应由宿主盖章为 %q，实得 %q", caps.PluginID, sup.pluginID)
	}
	if want := filepath.Join(root, "dist", "scheduler.js"); sup.register.Entry != want {
		t.Fatalf("entry 解析 = %q，期望 %q", sup.register.Entry, want)
	}
	if sup.register.Runtime != "node" || sup.register.Start != "on_enable" || sup.register.Restart != "on_failure" {
		t.Fatalf("spec = %+v", sup.register)
	}
}

// 越界 entry 在宿主侧就被拒绝（不依赖网关再拦一次）。
func TestProcessesRejectsEscapingEntry(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	root := t.TempDir()
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      try {
        await ctx.processes.register({ name: "evil", entry: args.entry });
        return { content: [{ type: "text", text: "OK" }] };
      } catch (e) {
        return { content: [{ type: "text", text: "ERR:" + (e && e.code ? e.code : e) }] };
      }
    },
  });
}
`
	sup := &fakeSupervisor{}
	sc := testScriptWithRoot(t, caps, src, root)
	p, err := Load(context.Background(), sc, caps, Context{Processes: sup},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	cases := []string{
		"../evil.js",
		"../../outside.js",
		filepath.Join(root, "..", "evil.js"),
		"/absolute/evil.js",
		"dist/../../evil.js",
	}
	for _, entry := range cases {
		req, _ := json.Marshal(map[string]string{"entry": entry})
		res, callErr := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, req,
			Context{PluginID: caps.PluginID, Processes: sup})
		if callErr != nil {
			t.Fatalf("CallTool(%q): %v", entry, callErr)
		}
		if got := strings.TrimSpace(res.Content[0].Text); got != "ERR:ERR_ACCESS_DENIED" {
			t.Fatalf("entry %q 应被拒绝，实得 %q", entry, got)
		}
	}
	if sup.registerOK {
		t.Fatal("越界 entry 不应触达桥")
	}
}

// 桥返回错误时，脚本拿到的是固定错误码而不是静默成功。
func TestProcessesSurfacesBridgeError(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "scheduler.js"), []byte("//"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      try {
        await ctx.processes.ensure("scheduler");
        return { content: [{ type: "text", text: "OK" }] };
      } catch (e) {
        return { content: [{ type: "text", text: "ERR:" + (e && e.code ? e.code : e) }] };
      }
    },
  });
}
`
	sup := &fakeSupervisor{err: errors.New("process_not_found: 没有找到进程 test-plugin/scheduler")}
	sc := testScriptWithRoot(t, caps, src, root)
	p, err := Load(context.Background(), sc, caps, Context{Processes: sup},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, callErr := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`),
		Context{PluginID: caps.PluginID, Processes: sup})
	if callErr != nil {
		t.Fatalf("CallTool: %v", callErr)
	}
	if got := strings.TrimSpace(res.Content[0].Text); !strings.HasPrefix(got, "ERR:ERR_OUT_OF_RANGE") {
		t.Fatalf("桥失败应抛固定错误码，实得 %q", got)
	}
}

// list 返回本插件的进程快照。
func TestProcessesList(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const list = await ctx.processes.list();
      return { content: [{ type: "text", text: JSON.stringify(list) }] };
    },
  });
}
`
	sup := &fakeSupervisor{}
	script := testScript(t, caps, src)
	p, err := Load(context.Background(), script, caps, Context{Processes: sup},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()
	res, callErr := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`),
		Context{PluginID: caps.PluginID, Processes: sup})
	if callErr != nil {
		t.Fatalf("CallTool: %v", callErr)
	}
	text := strings.TrimSpace(res.Content[0].Text)
	if !strings.Contains(text, `"name":"scheduler"`) {
		t.Fatalf("list 结果 = %s", text)
	}
}

// 关闭 Runtime 时按「本插件注册过哪些进程」逐个注销：插件禁用/重载后调度进程不能
// 继续跑（06 §6.4「关闭 Runtime 要取消它启动的所有异步工作」）。
func TestProcessesReleasedOnClose(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	root := t.TempDir()
	entryPath := filepath.Join(root, "dist", "scheduler.js")
	if err := os.MkdirAll(filepath.Dir(entryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("// noop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      const st = await ctx.processes.register({ name: "scheduler", scope: "global", entry: "dist/scheduler.js" });
      return { content: [{ type: "text", text: String(st.name) }] };
    },
  });
}
`
	sup := &fakeSupervisor{}
	sc := testScriptWithRoot(t, caps, src, root)
	p, err := Load(context.Background(), sc, caps, Context{Processes: sup},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	res, callErr := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`),
		Context{PluginID: caps.PluginID, Processes: sup})
	if callErr != nil {
		t.Fatalf("CallTool: %v", callErr)
	}
	if got := strings.TrimSpace(res.Content[0].Text); got != "scheduler" {
		t.Fatalf("注册回执 = %q", got)
	}

	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := sup.unregistered
	if len(got) != 1 || got[0] != caps.PluginID+"/scheduler" {
		t.Fatalf("关闭 Runtime 应注销 scheduler，实得 %v", got)
	}
}

// 显式 unregister 之后关闭 Runtime 不再重复注销（引用计数不能被减两次）。
func TestProcessesExplicitUnregisterIsNotRepeated(t *testing.T) {
	caps := testCaps(t, plugins.TrustBuiltin, plugins.CapProcessSupervise)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "scheduler.js"), []byte("//"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := `
export default function (agent) {
  agent.registerTool({
    name: "proc_probe", description: "进程探针", parameters: { type: "object" }, access: "read",
    async execute(args, ctx) {
      await ctx.processes.register({ name: "scheduler", entry: "scheduler.js" });
      await ctx.processes.unregister("scheduler");
      return { content: [{ type: "text", text: "done" }] };
    },
  });
}
`
	sup := &fakeSupervisor{}
	sc := testScriptWithRoot(t, caps, src, root)
	p, err := Load(context.Background(), sc, caps, Context{Processes: sup},
		Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, callErr := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`),
		Context{PluginID: caps.PluginID, Processes: sup}); callErr != nil {
		t.Fatalf("CallTool: %v", callErr)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(sup.unregistered) != 1 {
		t.Fatalf("显式注销后关闭不应重复注销，实得 %v", sup.unregistered)
	}
}
