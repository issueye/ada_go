package app_test

// tool-discovery 系统插件的行为验收：真实装配 coding 产品 + 一个 discovery=true
// 的测试工具，经 find_tool 发现、exec_tool 由宿主桥执行（审批链在 engine.InvokeTool）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/agent_core/conformance/testkit"
	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/agent_core/engine"
	"github.com/adag/ada-go/internal/agent_core/plugins"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/plugins/official"
)

type fixedRunner struct{ res domain.Result }

func (r fixedRunner) Execute(context.Context, domain.ToolCall) domain.Result { return r.res }

func TestToolDiscoveryPluginFindAndExec(t *testing.T) {
	ws := tempDir(t)
	raw, err := os.ReadFile(filepath.Join("..", "products", "coding.agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := coreapp.ParseProduct(raw)
	if err != nil {
		t.Fatal(err)
	}
	base, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	custom := host.Script{
		Manifest: &plugins.Manifest{
			ID: "lazy-probe", Name: "Lazy Probe", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
			Trust: plugins.TrustThirdParty,
			Backend: &plugins.Backend{
				Entry:        "index.js",
				Capabilities: []plugins.Capability{},
			},
		},
		EntryName: "plugin://lazy-probe/index.js",
		Source: `export default function (agent) {
  agent.registerTool({
    name: "lazy_probe", description: "懒加载探针", parameters: { type: "object" }, access: "read",
    discovery: true,
    execute() { return { content: [{ type: "text", text: "lazy-inner" }] }; },
  });
}`,
	}
	frontend, err := official.FrontendScripts()
	if err != nil {
		t.Fatal(err)
	}
	frontendIDs := map[string]bool{}
	for _, sc := range frontend {
		if sc.Manifest != nil {
			frontendIDs[sc.Manifest.ID] = true
		}
	}
	bridge, err := host.NewHTTPBridge(host.HTTPBridgeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bridge.Close() })
	clock := testkit.NewFakeClockAt(testkit.FrozenTime)
	ids := testkit.NewFixedIDGen()
	store := testkit.NewMemoryStore(clock, ids)
	events := testkit.NewRecordingEventSink()
	model := testkit.NewFakeProvider(nil)
	inter := testkit.NewFakeInteractions()
	var asm *app.Assembly
	hc := host.Context{
		Workspace: ws,
		Log:       func(string, string, map[string]any) {},
		HTTP:      bridge,
		Catalog: func(ctx context.Context) ([]domain.Descriptor, error) {
			return asm.ToolCatalog().List(ctx), nil
		},
		InvokeTool: func(ctx context.Context, meta host.CallMeta, call domain.ToolCall) (domain.Result, error) {
			return engine.InvokeTool(ctx, engine.Deps{
				Clock: clock, IDs: ids, Events: events, Session: store, Model: model,
				Tools: asm.ToolCatalog(), Interactions: inter, Hooks: asm.HookChain(),
				Runner: fixedRunner{res: domain.Result{
					Content: []domain.Part{{Type: domain.PartText, Text: "lazy-inner"}},
					Receipt: domain.Receipt{Status: domain.ReceiptOK},
				}},
			}, engine.TurnSpec{ThreadID: meta.ThreadID, RunID: meta.RunID}, call)
		},
	}
	asm, err = app.AssembleWithExtras(context.Background(), p, base, []host.Script{custom}, nil, nil, app.Deps{
		Context: hc, FrontendIDs: frontendIDs,
	})
	if err != nil {
		t.Fatalf("AssembleWithExtras: %v", err)
	}
	defer func() { _ = asm.Close(context.Background()) }()

	inv := asm.ToolInvoker()
	find, err := inv.Invoke(context.Background(), "tool-discovery", "find_tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("find_tool: %v", err)
	}
	if !strings.Contains(find.Content[0].Text, "lazy_probe") {
		t.Fatalf("find_tool 没有列出 lazy_probe: %s", find.Content[0].Text)
	}
	exec, err := inv.Invoke(context.Background(), "tool-discovery", "exec_tool", json.RawMessage(`{"tool":"lazy_probe","args":{}}`))
	if err != nil {
		t.Fatalf("exec_tool: %v", err)
	}
	if !strings.Contains(exec.Content[0].Text, "lazy-inner") {
		t.Fatalf("exec_tool 没有上浮内层结果: %s", exec.Content[0].Text)
	}

	// exec_tool 只能执行发现式工具：注入式工具（read_file 这类）必须被拒，
	// 否则模型多出一条绕过「只调用本轮声明工具」的旁路。
	injected := ""
	for _, d := range asm.ToolCatalog().List(context.Background()) {
		if d.PluginID != "tool-discovery" && !d.Discovery {
			injected = d.Name
			break
		}
	}
	if injected == "" {
		t.Fatal("coding 产品应当有至少一个注入式工具")
	}
	deny, err := inv.Invoke(context.Background(), "tool-discovery", "exec_tool", json.RawMessage(`{"tool":"`+injected+`","args":{}}`))
	if err != nil {
		t.Fatalf("exec_tool 注入式: %v", err)
	}
	if !strings.Contains(deny.Content[0].Text, "tool_not_discoverable") {
		t.Fatalf("注入式工具应被 exec_tool 拒绝，实得 %s", deny.Content[0].Text)
	}

	// 管理面视图与模型声明同源：发现式工具只在 Discovery 视图里，系统入口只在 System 视图里。
	mgr := asm.ToolManager()
	if mgr == nil {
		t.Fatal("装配必须提供 ToolManager")
	}
	discovery := namesOf(mgr.View(context.Background(), domain.ToolVisibleDiscovery))
	if !containsName(discovery, "lazy_probe") {
		t.Fatalf("Discovery 视图缺少 lazy_probe: %v", discovery)
	}
	if containsName(discovery, "find_tool") {
		t.Fatalf("Discovery 视图不应含系统入口: %v", discovery)
	}
	if system := namesOf(mgr.View(context.Background(), domain.ToolVisibleSystem)); len(system) != 2 ||
		!containsName(system, domain.ToolFindName) || !containsName(system, domain.ToolExecName) {
		t.Fatalf("System 视图应恰好是 find_tool/exec_tool: %v", system)
	}
	if injectedView := namesOf(mgr.View(context.Background(), domain.ToolVisibleInjected)); containsName(injectedView, "lazy_probe") {
		t.Fatalf("注入式视图不应含发现式工具: %v", injectedView)
	}
}

// namesOf 取视图里的工具名。
func namesOf(list []domain.Descriptor) []string {
	out := make([]string, 0, len(list))
	for _, d := range list {
		out = append(out, d.Name)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
