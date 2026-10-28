package host

// 本文件是 M1-C 宿主的第一批测试：先钉「装配 → 注册 → 调用 → 结算」这条主链，
// 再钉能力边界（fs 越界、只读裁剪、16MB 上限）与 E-2 的定时器上限。
//
// 测试都在 owner goroutine 之外用 Load/CallTool 这类公开面驱动，与 engine/toolkit
// 的真实调用方同一条路径；只有算不出公开面的内部件（realPath、decode*）直接调。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/ports"
)

// testCaps 造一份最小授予集合：后端能力白名单 + 工作区根。
func testCaps(t *testing.T, trust plugins.Trust, caps ...plugins.Capability) *CapabilitySet {
	t.Helper()
	backend := map[plugins.Capability]bool{}
	for _, c := range caps {
		backend[c] = true
	}
	return &CapabilitySet{
		Trust:         trust,
		Backend:       backend,
		Frontend:      map[plugins.Capability]bool{},
		Hooks:         map[string]plugins.HookAction{},
		Env:           map[string]string{},
		WorkspaceRoot: tempWorkspace(t),
		Mount:         DefaultMount,
	}
}

// testScript 造一个最小插件：清单 + 入口源码。
func testScript(t *testing.T, caps *CapabilitySet, source string) Script {
	t.Helper()
	m := &plugins.Manifest{
		ID: "test-plugin", Name: "测试插件", Version: "0.1.0",
		APIVersion: plugins.APIVersionMajor, Trust: caps.Trust,
		Backend: &plugins.Backend{
			Entry:        "index.js",
			Capabilities: caps.granted(),
			Hooks:        map[string]plugins.HookAction{},
		},
	}
	return Script{Manifest: m, EntryName: entryName(m.ID), Source: source}
}

// TestLoadRegisterAndCallTool 是主链测试：入口工厂注册工具 → 装配提交 → 调用 → 结算。
func TestLoadRegisterAndCallTool(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "echo",
    description: "回显输入",
    parameters: { type: "object", properties: { text: { type: "string" } } },
    access: "read",
    execute(args, ctx) {
      return { content: [{ type: "text", text: "echo:" + args.text }], details: { plugin: ctx.workspace } };
    },
  });
}
`
	script := testScript(t, caps, src)

	tools := &recordingTools{}
	p, err := Load(context.Background(), script, caps, Context{}, Options{
		Targets: Targets{Tools: tools},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	contrib := p.Contribution()
	if contrib == nil || len(contrib.Tools) != 1 {
		t.Fatalf("贡献工具数 = %+v，期望 1 个", contrib)
	}
	def := contrib.Tools[0]
	if def.Name != "echo" || def.Handler.Slot == 0 {
		t.Fatalf("工具定义不完整: %+v", def)
	}
	if got := tools.names(); len(got) != 1 || got[0] != "echo" {
		t.Fatalf("工具目录 = %v，期望 [echo]", got)
	}

	res, err := p.CallTool(context.Background(), def.Handler, json.RawMessage(`{"text":"hi"}`), Context{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echo:hi" {
		t.Fatalf("结果内容 = %+v", res.Content)
	}
	if res.Receipt.Status != domain.ReceiptOK {
		t.Fatalf("回执状态 = %q", res.Receipt.Status)
	}
	if res.Details["plugin"] != DefaultMount {
		t.Fatalf("ctx.workspace = %v，期望 %q", res.Details["plugin"], DefaultMount)
	}
}

// TestSecondRegistrationIsIgnoredAfterFreeze 钉「运行期再注册只记警告」（06 §6.5）。
func TestSecondRegistrationIsIgnoredAfterFreeze(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({ name: "first", description: "1", parameters: { type: "object" }, access: "read",
    execute() { return { content: [{ type: "text", text: "1" }] }; } });
  globalThis.__late = () => agent.registerTool({ name: "second", description: "2",
    parameters: { type: "object" }, access: "read", execute() { return { content: [] }; } });
}
`
	script := testScript(t, caps, src)
	tools := &recordingTools{}
	p, err := Load(context.Background(), script, caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	// 冻结后再注册：不改变目录，只留一条警告。
	contrib := p.Contribution()
	if _, err := p.CallTool(context.Background(), contrib.Tools[0].Handler, json.RawMessage(`{}`), Context{}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := tools.names(); len(got) != 1 || got[0] != "first" {
		t.Fatalf("冻结后目录 = %v，期望仍是 [first]", got)
	}
}

// TestModulePolicyGating 钉链接期能力判定（12 §6.12.1(1)）。
func TestModulePolicyGating(t *testing.T) {
	ro := testCaps(t, plugins.TrustThirdParty, plugins.CapFSRead)
	policy := NewModulePolicy(ro)
	if err := policy.Allow("node:fs"); err != nil {
		t.Fatalf("有 fs.read 时 node:fs 应放行: %v", err)
	}
	if err := policy.Allow("node:child_process"); err == nil {
		t.Fatal("没有 exec 时 node:child_process 必须在链接期失败")
	} else if !strings.Contains(err.Error(), codeAccessDenied) {
		t.Fatalf("错误应带 %s: %v", codeAccessDenied, err)
	}
	if err := policy.Allow("node:http"); err == nil || !strings.Contains(err.Error(), codeModuleNotFound) {
		t.Fatalf("node:http 必须报 %s: %v", codeModuleNotFound, err)
	}
	if err := policy.Allow("left-pad"); err != nil {
		t.Fatalf("非内置名交给宿主模块表，不应被策略拒绝: %v", err)
	}
	if err := policy.Allow("native.node"); err == nil || !strings.Contains(err.Error(), codeNativeAddon) {
		t.Fatalf(".node 原生扩展必须报 %s: %v", codeNativeAddon, err)
	}
}

// TestFSGuardRealPath 钉工作区边界：越界一律拒绝（09 §9.2）。
func TestFSGuardRealPath(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty, plugins.CapFSRead)
	st := &capsState{caps: caps, limits: DefaultLimits()}
	guard, err := newFSGuard(st)
	if err != nil {
		t.Fatalf("newFSGuard: %v", err)
	}
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/workspace/a.txt", "a.txt", true},
		{"a.txt", "a.txt", true},
		{"/workspace/sub/b.txt", "sub/b.txt", true},
		{"/workspace/../etc/passwd", "", false},
		{"/etc/passwd", "", false},
		{"/workspace/a/../../b", "", false},
	}
	for _, c := range cases {
		got, err := guard.realPath(c.in)
		if c.ok && err != nil {
			t.Fatalf("realPath(%q) 失败: %v", c.in, err)
		}
		if !c.ok {
			if err == nil {
				t.Fatalf("realPath(%q) 应被拒绝，实际得到 %q", c.in, got)
			}
			continue
		}
		if filepath.Base(got) != filepath.Base(c.want) {
			t.Fatalf("realPath(%q) = %q，期望以 %q 结尾", c.in, got, c.want)
		}
		if !strings.HasPrefix(got, guard.root) {
			t.Fatalf("realPath(%q) = %q 越出根 %q", c.in, got, guard.root)
		}
	}
}

// TestFSGuardReadLimit 钉 16MB 上限（E-6：上游没有单次读写上限）。
func TestFSGuardReadLimit(t *testing.T) {
	caps := testCaps(t, plugins.TrustThirdParty, plugins.CapFSRead)
	st := &capsState{caps: caps, limits: Limits{MaxFileBytes: 16, MaxDirEntries: 4, MaxPathBytes: 4096,
		MaxFetchBytes: 1 << 20, MaxExecOutputBytes: 1 << 20, MaxExecTimeout: time.Second, MaxTimers: 8}}
	guard, err := newFSGuard(st)
	if err != nil {
		t.Fatalf("newFSGuard: %v", err)
	}
	big := filepath.Join(guard.root, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", 32)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := guard.checkFileSize("/workspace/big.txt"); err == nil {
		t.Fatal("超过上限的文件必须在读取前被拒绝")
	} else if !strings.Contains(err.Error(), codeFileTooLarge) {
		t.Fatalf("错误应带 %s: %v", codeFileTooLarge, err)
	}
	small := filepath.Join(guard.root, "small.txt")
	if err := os.WriteFile(small, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := guard.checkFileSize("/workspace/small.txt"); err != nil {
		t.Fatalf("小文件不应被拒绝: %v", err)
	}
}

// TestDecodeToolResult / TestDecodeHookOutcome 钉 JSON 结算的形状与拒绝方向。
func TestDecodeToolResult(t *testing.T) {
	start := time.Now().Add(-time.Second)
	res, err := decodeToolResult("p", json.RawMessage(`{"content":[{"type":"text","text":"a"},{"type":"image","mime":"image/png","data":"AA=="}],"details":{"k":1}}`), start)
	if err != nil {
		t.Fatalf("decodeToolResult: %v", err)
	}
	if len(res.Content) != 2 || res.Content[0].Type != domain.PartText || res.Content[1].Type != domain.PartImage {
		t.Fatalf("部件 = %+v", res.Content)
	}
	if res.Receipt.Status != domain.ReceiptOK || res.Receipt.DurationMS < 0 {
		t.Fatalf("回执 = %+v", res.Receipt)
	}
	if _, err := decodeToolResult("p", json.RawMessage(`{"content":[{"type":"audio"}]}`), start); err == nil {
		t.Fatal("未知部件类型必须报错，不能静默丢弃")
	}
	if _, err := decodeToolResult("p", json.RawMessage(`{"content":[{"type":"image"}]}`), start); err == nil {
		t.Fatal("image 缺 mime/data 必须报错")
	}
}

func TestDecodeHookOutcome(t *testing.T) {
	out, err := decodeHookOutcome("p", json.RawMessage(`{"action":"deny","reason":"不行"}`))
	if err != nil {
		t.Fatalf("decodeHookOutcome: %v", err)
	}
	if out.Action != ports.HookDeny || out.Reason != "不行" {
		t.Fatalf("结果 = %+v", out)
	}
	def, err := decodeHookOutcome("p", json.RawMessage(`{}`))
	if err != nil || def.Action != ports.HookObserve {
		t.Fatalf("缺省动作应为 observe: %+v, %v", def, err)
	}
	if _, err := decodeHookOutcome("p", json.RawMessage(`{"action":"nope"}`)); err == nil {
		t.Fatal("未知动作必须报错")
	}
}

// TestTimerLimit 钉 E-2：每 Runtime 的未完成定时器上限（上游不产生 ERR_TIMER_LIMIT）。
func TestTimerLimit(t *testing.T) {
	SetLimits(Limits{MaxTimers: 4})
	defer SetLimits(DefaultLimits())

	caps := testCaps(t, plugins.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({ name: "spin", description: "建定时器", parameters: { type: "object" }, access: "read",
    execute() {
      const ids = [];
      try { for (let i = 0; i < 10; i++) ids.push(setTimeout(() => {}, 50)); }
      catch (e) { return { content: [{ type: "text", text: e.code }] }; }
      return { content: [{ type: "text", text: "no-limit" }] };
    } });
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
	if len(res.Content) == 0 || res.Content[0].Text != codeTimerLimit {
		t.Fatalf("定时器超限应抛 %s，实际 = %+v", codeTimerLimit, res.Content)
	}
}

// recordingTools 是 ToolRegistry 的测试替身。
type recordingTools struct {
	mu  sync.Mutex
	def []domain.Descriptor
}

func (r *recordingTools) Register(d domain.Descriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.def = append(r.def, d)
	return nil
}

func (r *recordingTools) Remove(pluginID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	kept := r.def[:0]
	for _, d := range r.def {
		if d.PluginID == pluginID {
			n++
			continue
		}
		kept = append(kept, d)
	}
	r.def = kept
	return n
}

func (r *recordingTools) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.def))
	for _, d := range r.def {
		out = append(out, d.Name)
	}
	return out
}

// tempWorkspace 造工作区并用「可重试的删除」清理。
//
// Windows 上上游 node.API 的目录句柄释放与 TempDir 的 RemoveAll 会竞争：
// t.TempDir() 的清理失败会把一个本来通过的功能测试判成失败（抖动）。这里改成
// 显式目录 + 重试删除，最多等 2s。
func tempWorkspace(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "adago-ws-")
	if err != nil {
		t.Fatalf("建临时工作区: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = os.RemoveAll(dir)
	})
	return dir
}
