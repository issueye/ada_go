package host

// 本文件是 M1-C 宿主的第一批测试：先钉「装配 → 注册 → 调用 → 结算」这条主链，
// 再钉能力边界（fs 越界、只读裁剪、16MB 上限）与 E-2 的定时器上限。
//
// 测试都在 owner goroutine 之外用 Load/CallTool 这类公开面驱动，与 engine/toolkit
// 的真实调用方同一条路径；只有算不出公开面的内部件（realPath、decode*）直接调。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/pluginapi"
	"github.com/adag/ada-go/internal/common/ports"
)

// testCaps 造一份最小上下文：信任级别 + 工作区根（能力位已移除）。
func testCaps(t *testing.T, trust pluginapi.Trust, _ ...pluginapi.Capability) *CapabilitySet {
	t.Helper()
	return &CapabilitySet{
		PluginID:      "test-plugin",
		Trust:         trust,
		Hooks:         map[string]pluginapi.HookAction{},
		Env:           map[string]string{},
		WorkspaceRoot: tempWorkspace(t),
		Mount:         DefaultMount,
	}
}

// testScript 造一个最小插件：清单 + 入口源码。
func testScript(t *testing.T, caps *CapabilitySet, source string) Script {
	t.Helper()
	m := &pluginapi.Manifest{
		ID: "test-plugin", Name: "测试插件", Version: "0.1.0",
		APIVersion: pluginapi.APIVersionMajor, Trust: caps.Trust,
		Backend: &pluginapi.Backend{
			Entry: "index.js",
			Hooks: map[string]pluginapi.HookAction{},
		},
	}
	return Script{Manifest: m, EntryName: entryName(m.ID), Source: source}
}

// TestLoadRegisterAndCallTool 是主链测试：入口工厂注册工具 → 装配提交 → 调用 → 结算。
func TestLoadRegisterAndCallTool(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
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

// registerTool 的 discovery 位从脚本 JSON 一路带到引擎描述符（05 §6.3）：
// 缺省 false（注入式），true 时不注入模型清单、只经 find_tool/exec_tool 使用。
func TestRegisterToolDiscoveryFlag(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "lazy_tool",
    description: "发现式工具",
    parameters: { type: "object", properties: {} },
    access: "read",
    discovery: true,
    execute() { return { content: [{ type: "text", text: "ok" }] }; },
  });
  agent.registerTool({
    name: "eager_tool",
    description: "注入式工具",
    parameters: { type: "object", properties: {} },
    access: "read",
    execute() { return { content: [{ type: "text", text: "ok" }] }; },
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

	contrib := p.Contribution()
	if contrib == nil || len(contrib.Tools) != 2 {
		t.Fatalf("贡献工具数 = %+v，期望 2 个", contrib)
	}
	descs := map[string]domain.Descriptor{}
	for _, def := range contrib.Tools {
		descs[def.Name] = def.Descriptor(contrib.PluginID)
	}
	if !descs["lazy_tool"].Discovery {
		t.Fatalf("lazy_tool 应是发现式: %+v", descs["lazy_tool"])
	}
	if descs["eager_tool"].Discovery {
		t.Fatalf("eager_tool 缺省应是注入式: %+v", descs["eager_tool"])
	}
}

// TestRebuildRunsFactoryWithStaging 钉重建路径的 staging 接线：
// 一次超时/取消后 dirty Runtime 会在下一次调用前重建并重跑入口工厂；
// 工厂必须能看到新的 staging，否则会误报「agent API 已冻结」。
func TestRebuildRunsFactoryWithStaging(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({
    name: "rebuild_probe",
    description: "重建探针",
    parameters: { type: "object" },
    access: "read",
    execute() { return { content: [{ type: "text", text: "ok" }] }; },
  });
}
`
	tools := &recordingTools{}
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: tools}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	// 模拟工具超时/取消后的 dirty 状态；下一次调用会触发 rebuild。
	p.markDirty()
	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("rebuild 后调用失败: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "ok" {
		t.Fatalf("rebuild 后结果 = %+v，期望工厂重建后注册的工具可用（ok）", res.Content)
	}
	if p.Dirty() {
		t.Fatal("rebuild 成功后 dirty 应清零")
	}
}

// TestFactoryAssemblyContext 钉入口工厂的第二个只读参数（MCP 装配期发现的前提）。
func TestFactoryAssemblyContext(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapFSRead, pluginapi.CapNetFetch)
	caps.Secrets = []string{"mcp_token"}
	bridge, err := NewHTTPBridge(HTTPBridgeConfig{})
	if err != nil {
		t.Fatalf("NewHTTPBridge: %v", err)
	}
	defer bridge.Close()

	src := `
export default function (agent, ctx) {
  const seen = [
    ctx.workspace,
    ctx.settings && ctx.settings.marker,
    typeof (ctx.fs && ctx.fs.readText),
    typeof ctx.fetch,
    typeof ctx.secret,
  ].join("|");
  agent.registerTool({
    name: "ctx_probe",
    description: "装配期上下文探针",
    parameters: { type: "object" },
    access: "read",
    execute() { return { content: [{ type: "text", text: seen }] }; },
  });
}
`
	hc := Context{
		Workspace: tempWorkspace(t),
		Settings:  map[string]any{"marker": "yes"},
		Secret:    func(string) (string, bool) { return "s3cr3t", true },
		HTTP:      bridge,
	}
	p, err := Load(context.Background(), testScript(t, caps, src), caps, hc, Options{
		Targets: Targets{Tools: &recordingTools{}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	res, err := p.CallTool(context.Background(), p.Contribution().Tools[0].Handler, json.RawMessage(`{}`), hc)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	want := DefaultMount + "|yes|function|function|function"
	if len(res.Content) != 1 || res.Content[0].Text != want {
		t.Fatalf("装配期上下文 = %+v，期望 %q", res.Content, want)
	}
}

// TestSecondRegistrationIsIgnoredAfterFreeze 钉「运行期再注册只记警告」（06 §6.5）。
func TestSecondRegistrationIsIgnoredAfterFreeze(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
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

// TestFSGuardRealPath 钉工作区边界：越界一律拒绝（09 §9.2）。
func TestFSGuardRealPath(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty, pluginapi.CapFSRead)
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
	caps := testCaps(t, pluginapi.TrustThirdParty, pluginapi.CapFSRead)
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
	out, err := decodeHookOutcome("p", json.RawMessage(`{"action":"abort","reason":"不行"}`))
	if err != nil {
		t.Fatalf("decodeHookOutcome: %v", err)
	}
	if out.Action != ports.HookAbort || out.Reason != "不行" {
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

	caps := testCaps(t, pluginapi.TrustThirdParty)
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

// TestSuccessfulCallsDoNotCountAsFailures 钉一个曾经的真 bug：
// owner 的 collectOutcome 与调用方 roundTrip 抢同一个回执通道，抢输的一方会把
// 「已经成功的调用」误判成「Promise 未结算」，于是每次成功都累加失败计数，
// 三次之后插件被自动禁用。这里连打 5 次成功调用，失败计数必须保持 0。
func TestSuccessfulCallsDoNotCountAsFailures(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({ name: "ok", description: "ok", parameters: { type: "object" }, access: "read",
    execute() { return { content: [{ type: "text", text: "fine" }] }; } });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	handler := p.Contribution().Tools[0].Handler
	for i := 0; i < 5; i++ {
		res, err := p.CallTool(context.Background(), handler, json.RawMessage(`{}`), Context{})
		if err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i+1, err)
		}
		if len(res.Content) != 1 || res.Content[0].Text != "fine" {
			t.Fatalf("第 %d 次内容 = %+v", i+1, res.Content)
		}
	}
	failures, disabled := p.Status()
	if failures != 0 {
		t.Fatalf("成功调用累计了 %d 次失败（应保持 0）；warnings=%v", failures, p.Warnings())
	}
	if disabled {
		t.Fatal("插件被误禁用")
	}
}

// TestBreakerAutoRecoversAfterCooldown 钉熔断自动恢复（06 §6.4）：三连败禁用后
// 冷却期内拒绝并提示自动恢复；冷却到点下一次调用半开放行——重建重跑工厂后本次
// 调用成功即完全恢复（计数、冷却清零），不需要任何人工重载。
func TestBreakerAutoRecoversAfterCooldown(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({ name: "flaky", description: "flaky", parameters: { type: "object" }, access: "read",
    execute(args) {
      if (args && args.fail) { throw new Error("boom"); }
      return { content: [{ type: "text", text: "recovered" }] };
    } });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	handler := p.Contribution().Tools[0].Handler
	// 三连败：熔断禁用。
	for i := 0; i < 3; i++ {
		if _, err := p.CallTool(context.Background(), handler, json.RawMessage(`{"fail":true}`), Context{}); err == nil {
			t.Fatalf("第 %d 次调用应失败", i+1)
		}
	}
	if failures, disabled := p.Status(); !disabled || failures != 3 {
		t.Fatalf("三连败后应禁用：failures=%d disabled=%v", failures, disabled)
	}

	// 冷却期内：拒绝并提示自动恢复（不再是「需手动重载」）。
	_, err = p.CallTool(context.Background(), handler, json.RawMessage(`{}`), Context{})
	if err == nil || !strings.Contains(err.Error(), "自动恢复") {
		t.Fatalf("冷却期内应拒绝并提示自动恢复: %v", err)
	}
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("禁用错误应保持 ErrDisabled 哨兵: %v", err)
	}

	// 把禁用时刻拨回：冷却视为已过（不等真实 30s）。
	p.mu.Lock()
	p.disabledAt = p.disabledAt.Add(-DisabledCooldownInit - time.Second)
	p.mu.Unlock()

	// 半开探测：重建重跑工厂，本次调用（不带 fail）成功即恢复。
	res, err := p.CallTool(context.Background(), handler, json.RawMessage(`{}`), Context{})
	if err != nil {
		t.Fatalf("冷却到点后半开调用应放行并成功: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "recovered" {
		t.Fatalf("恢复后内容 = %+v", res.Content)
	}
	if failures, disabled := p.Status(); disabled || failures != 0 {
		t.Fatalf("探测成功后应完全恢复：failures=%d disabled=%v", failures, disabled)
	}
	p.mu.Lock()
	cd := p.cooldown
	p.mu.Unlock()
	if cd != 0 {
		t.Fatalf("成功调用应清零冷却，实际 %s", cd)
	}
}

// TestBreakerBacksOffWhenProbeKeepsFailing 钉指数退避：插件一直坏时，每次
// 「冷却到点 → 半开探测 → 试探失败」把冷却翻倍（30s → 60s → 120s，上限 10min）。
func TestBreakerBacksOffWhenProbeKeepsFailing(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustThirdParty)
	src := `
export default function (agent) {
  agent.registerTool({ name: "broken", description: "broken", parameters: { type: "object" }, access: "read",
    execute() { throw new Error("still broken"); } });
}
`
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{}, Options{Targets: Targets{Tools: &recordingTools{}}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	handler := p.Contribution().Tools[0].Handler
	for i := 0; i < 3; i++ {
		if _, err := p.CallTool(context.Background(), handler, json.RawMessage(`{}`), Context{}); err == nil {
			t.Fatalf("第 %d 次调用应失败", i+1)
		}
	}
	assertCooldown := func(want time.Duration) {
		t.Helper()
		p.mu.Lock()
		cd, disabled := p.cooldown, p.disabled
		p.mu.Unlock()
		if !disabled || cd != want {
			t.Fatalf("冷却应翻倍至 %s：cooldown=%s disabled=%v", want, cd, disabled)
		}
	}
	// 两轮「拨回冷却 → 半开探测 → 试探再失败」：翻倍推进，第二轮起触顶封在上限。
	for _, want := range []time.Duration{DisabledCooldownInit * 2, min(DisabledCooldownInit*4, DisabledCooldownMax)} {
		p.mu.Lock()
		p.disabledAt = p.disabledAt.Add(-p.cooldown - time.Second)
		p.mu.Unlock()
		// 试探调用返回它自己的原始错误；重新熔断记进 lastError。
		if _, err := p.CallTool(context.Background(), handler, json.RawMessage(`{}`), Context{}); err == nil {
			t.Fatal("试探调用应失败")
		}
		assertCooldown(want)
		// 紧接着的调用应处于新一轮冷却期，文案带上探测失败原因。
		_, err := p.CallTool(context.Background(), handler, json.RawMessage(`{}`), Context{})
		if err == nil || !strings.Contains(err.Error(), "恢复探测失败") {
			t.Fatalf("试探失败后应重新进入冷却并说明原因: %v", err)
		}
	}
	if failures, disabled := p.Status(); !disabled {
		t.Fatalf("持续失败应保持禁用：failures=%d disabled=%v", failures, disabled)
	}
}

// recordingRoutes 收集 route 登记项。
type recordingRoutes struct{ regs []RouteRegistration }

func (r *recordingRoutes) AddRoute(reg RouteRegistration) error {
	r.regs = append(r.regs, reg)
	return nil
}

// route 支持 (method, path, handler) 与对象形式。
func TestRouteRegistersAndCalls(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin)
	src := "export default function (agent) {\n" +
		"  agent.route({ method: 'POST', path: '/echo', handler: function (req) { return { echo: req.body }; } });\n" +
		"  agent.route('GET', '/ping', function () { return { ok: true }; });\n" +
		"}"
	routes := &recordingRoutes{}
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{},
		Options{Targets: Targets{Routes: routes}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	if len(routes.regs) != 2 {
		t.Fatalf("HTTP 路由登记数 = %d, want 2: %+v", len(routes.regs), routes.regs)
	}
	var echo RouteDef
	for _, reg := range routes.regs {
		if reg.Def.Path == "/echo" {
			echo = reg.Def
		}
	}
	if echo.Method != "POST" || echo.Handler.Slot == 0 {
		t.Fatalf("对象形式的 route 没登记成 POST /echo: %+v", echo)
	}
	out, err := p.CallRoute(context.Background(), echo.Handler,
		json.RawMessage(`{"method":"POST","path":"/echo","body":{"n":2}}`), Context{})
	if err != nil {
		t.Fatalf("CallRoute: %v", err)
	}
	if !strings.Contains(string(out), `"n":2`) {
		t.Fatalf("路由结果 = %s", out)
	}
}

// SSE 路由：登记 sse:true，处理器用 ctx.sse.send 推帧，宿主按帧回调。
func TestSSERouteStreams(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin)
	src := "export default function (agent) {\n" +
		"  agent.route({ method: 'GET', path: '/events', sse: true, handler: function (req, ctx) {\n" +
		"    ctx.sse.send({ n: 1 });\n" +
		"    ctx.sse.send({ n: 2 }, 'update');\n" +
		"  } });\n" +
		"  agent.route('GET', '/plain', function () { return { ok: true }; });\n" +
		"}"
	routes := &recordingRoutes{}
	p, err := Load(context.Background(), testScript(t, caps, src), caps, Context{},
		Options{Targets: Targets{Routes: routes}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	var sseDef RouteDef
	for _, reg := range routes.regs {
		if reg.Def.Path == "/events" {
			sseDef = reg.Def
		}
		if reg.Def.Path == "/plain" && reg.Def.SSE {
			t.Fatalf("普通路由不应标记 SSE: %+v", reg.Def)
		}
	}
	if !sseDef.SSE || sseDef.Method != "GET" {
		t.Fatalf("SSE 路由登记 = %+v", sseDef)
	}

	var frames []RouteEvent
	err = p.CallRouteStream(context.Background(), sseDef.Handler,
		json.RawMessage(`{"method":"GET","path":"/events"}`), Context{},
		func(ev RouteEvent) error {
			frames = append(frames, ev)
			return nil
		})
	if err != nil {
		t.Fatalf("CallRouteStream: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("帧数 = %d: %+v", len(frames), frames)
	}
	if frames[0].Event != "" || !strings.Contains(string(frames[0].Data), `"n":1`) {
		t.Fatalf("第 1 帧 = %+v", frames[0])
	}
	if frames[1].Event != "update" || !strings.Contains(string(frames[1].Data), `"n":2`) {
		t.Fatalf("第 2 帧 = %+v", frames[1])
	}
}

// model.admin：ctx.providers 只在声明能力后安装，list/select 都走宿主回调。
func TestModelAdminRoutes(t *testing.T) {
	caps := testCaps(t, pluginapi.TrustBuiltin, pluginapi.CapModelAdmin)
	src := "export default function (agent) {\n" +
		"  agent.route('GET', '/state', function (req, ctx) { return ctx.providers.list(); });\n" +
		"  agent.route('POST', '/select', function (req, ctx) {\n" +
		"    return ctx.providers.select(req.body.providerId, req.body.modelId);\n" +
		"  });\n" +
		"}"
	routes := &recordingRoutes{}
	hc := Context{
		Providers: func() ProviderCatalog {
			return ProviderCatalog{
				Providers: []ProviderInfo{{ID: "openai", Models: []ProviderModel{{ID: "gpt-4o", ContextWindow: 128000}}}},
				Current:   ModelRef{ProviderID: "openai", ModelID: "gpt-4o"},
			}
		},
		SelectModel: func(_ context.Context, providerID, modelID, threadID string) (ModelRef, error) {
			return ModelRef{ProviderID: providerID, ModelID: modelID}, nil
		},
	}
	p, err := Load(context.Background(), testScript(t, caps, src), caps, hc,
		Options{Targets: Targets{Routes: routes}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(context.Background()) }()

	var state RouteDef
	var selectRoute RouteDef
	for _, reg := range routes.regs {
		switch reg.Def.Path {
		case "/state":
			state = reg.Def
		case "/select":
			selectRoute = reg.Def
		}
	}
	out, err := p.CallRoute(context.Background(), state.Handler,
		json.RawMessage(`{"method":"GET","path":"/state"}`), hc)
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	if !strings.Contains(string(out), `"gpt-4o"`) {
		t.Fatalf("providers.list 结果 = %s", out)
	}

	out, err = p.CallRoute(context.Background(), selectRoute.Handler,
		json.RawMessage(`{"method":"POST","path":"/select","body":{"providerId":"openai","modelId":"gpt-4o"}}`), hc)
	if err != nil {
		t.Fatalf("POST /select: %v", err)
	}
	if !strings.Contains(string(out), `"providerId":"openai"`) {
		t.Fatalf("providers.select 结果 = %s", out)
	}
}
