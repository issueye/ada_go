package app_test

// 组合根的验收：产品声明 × 嵌入的官方插件 → 宿主装配 → 真实调用。
//
// 这些用例天然允许出现官方插件 id（internal/ 的非测试源码不行，这里是 app 的测试），
// 也正好证明「官方插件与第三方走同一入口」：装配路径里没有任何 Go 侧注册 API。

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// testProduct 造一份最小产品声明。
func testProduct(t *testing.T, ids ...string) *coreapp.Product {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": "test", "name": "测试产品", "plugins": ids,
		"capabilities": map[string]any{"skills": false, "images": true, "subagents": false},
		"identity":     map[string]any{"name": "ada-go", "locale": "zh-CN"},
		"policies": map[string]any{
			"toolTimeoutMs": 120000, "turnTimeoutMs": 0, "maxTurnTokens": 0,
			"loopRepeatLimit": 3, "loopAbortLimit": 5, "parallelTools": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := coreapp.ParseProduct(raw)
	if err != nil {
		t.Fatalf("ParseProduct: %v", err)
	}
	return p
}

// assemble 用嵌入的官方插件装配一份产品。
func assemble(t *testing.T, p *coreapp.Product, hc host.Context, settings map[string]map[string]any) *app.Assembly {
	t.Helper()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	asm, err := app.Assemble(context.Background(), p, scripts, app.Deps{Context: hc, Settings: settings})
	if err != nil {
		t.Fatalf("app.Assemble: %v", err)
	}
	t.Cleanup(func() { _ = asm.Close(context.Background()) })
	return asm
}

// callTool 在拥有该工具的插件实例上调用它（工具可以来自任意插件）。
func callTool(t *testing.T, asm *app.Assembly, hc host.Context, tool string, args any) (string, domain.Result) {
	t.Helper()
	if _, ok := asm.Tools.Lookup(context.Background(), tool); !ok {
		t.Fatalf("工具目录里没有 %q（现有：%v）", tool, toolNames(asm))
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range asm.Plugins {
		c := inst.Contribution()
		if c == nil {
			continue
		}
		for _, td := range c.Tools {
			if td.Name != tool {
				continue
			}
			callCtx := hc
			callCtx.PluginID = inst.ID()
			if callCtx.Workspace == "" {
				callCtx.Workspace = "/workspace"
			}
			res, err := inst.CallTool(context.Background(), td.Handler, raw, callCtx)
			if err != nil {
				t.Fatalf("%s: %v", tool, err)
			}
			if len(res.Content) == 0 {
				t.Fatalf("%s 没有返回内容", tool)
			}
			return res.Content[0].Text, res
		}
	}
	t.Fatalf("没有插件贡献工具 %q", tool)
	return "", domain.Result{}
}

func toolNames(asm *app.Assembly) []string {
	var out []string
	for _, d := range asm.Tools.List(context.Background()) {
		out = append(out, d.Name)
	}
	return out
}

// TestToolsPlan 钉 tools-plan 的完整链路：装配 → 注册 → 调 update_plan → read_plan。
func TestToolsPlan(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{
		Workspace: ws,
		ThreadID:  "th_test_plan",
		Log:       func(string, string, map[string]any) {},
	}
	p := testProduct(t, "tools-plan")
	asm := assemble(t, p, hc, nil)

	names := toolNames(asm)
	sort.Strings(names)
	if strings.Join(names, ",") != "read_plan,update_plan" {
		t.Fatalf("工具目录 = %v，期望 [read_plan update_plan]", names)
	}

	text, res := callTool(t, asm, hc, "update_plan", map[string]any{
		"steps": []map[string]any{
			{"step": "读代码", "status": "done"},
			{"step": "改实现", "status": "in_progress"},
			{"step": "跑测试"},
		},
	})
	if !strings.Contains(text, "1/3 完成") || !strings.Contains(text, "[~] 改实现") {
		t.Fatalf("update_plan 输出 = %q", text)
	}
	if steps, ok := res.Details["steps"].([]any); !ok || len(steps) != 3 {
		t.Fatalf("details.steps = %#v", res.Details["steps"])
	}

	text, _ = callTool(t, asm, hc, "read_plan", map[string]any{})
	if !strings.Contains(text, "[x] 读代码") || !strings.Contains(text, "[ ] 跑测试") {
		t.Fatalf("read_plan 输出 = %q", text)
	}

	// 数据必须落在按会话命名的文件里，并由工具自己读写。
	todo := filepath.Join(ws, ".adag", "todo", "TODO_th_test_plan.json")
	raw, err := os.ReadFile(todo)
	if err != nil {
		t.Fatalf("TODO 文件未写出（%s）: %v", todo, err)
	}
	for _, want := range []string{`"threadId": "th_test_plan"`, `"steps"`, `"改实现"`, `"in_progress"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("TODO 文件缺少 %q:\n%s", want, raw)
		}
	}

	// 没有 threadId 的插槽预取（首页）只读动作必须返回空计划，而不是抛错。
	// 抛错会被宿主记成插件降级，整张 Todo 卡片从此不可用。
	def, ok := asm.ActionDef("tools-plan", "plan")
	if !ok {
		t.Fatal("tools-plan 没有注册 plan 动作")
	}
	inst, _ := asm.Plugin("tools-plan")
	noThread := host.Context{Workspace: ws, Log: func(string, string, map[string]any) {}}
	actRaw, err := inst.CallAction(context.Background(), def.Handler, json.RawMessage(`{}`), noThread)
	if err != nil {
		t.Fatalf("无 threadId 的 plan 动作不应失败: %v", err)
	}
	if !strings.Contains(string(actRaw), `"total":0`) {
		t.Fatalf("无 threadId 时应返回空计划: %s", actRaw)
	}
}

// 旧版本线程级 state KV 里的计划：首次读文件时迁移到 .adag/todo/TODO_<threadId>.json。
func TestToolsPlanMigratesLegacyState(t *testing.T) {
	ws := tempDir(t)
	hc := host.Context{
		Workspace: ws,
		ThreadID:  "th_legacy_plan",
		State: func(_ context.Context, op host.StateOp) (json.RawMessage, error) {
			if op.Kind == "get" && op.Key == "plan" {
				return json.RawMessage(`[{"step":"旧计划","status":"done"}]`), nil
			}
			return json.RawMessage("null"), nil
		},
		Log: func(string, string, map[string]any) {},
	}
	asm := assemble(t, testProduct(t, "tools-plan"), hc, nil)

	text, _ := callTool(t, asm, hc, "read_plan", map[string]any{})
	if !strings.Contains(text, "1/1 完成") || !strings.Contains(text, "旧计划") {
		t.Fatalf("read_plan 没读到旧 KV 计划: %q", text)
	}
	raw, err := os.ReadFile(filepath.Join(ws, ".adag", "todo", "TODO_th_legacy_plan.json"))
	if err != nil {
		t.Fatalf("旧计划没有迁移到 TODO 文件: %v", err)
	}
	if !strings.Contains(string(raw), "旧计划") {
		t.Fatalf("迁移后的文件内容不对:\n%s", raw)
	}
}

// TestMissingPluginFailsClosed 钉「产品声明了但没随包提供」是装配错误，不是静默跳过。
func TestMissingPluginFailsClosed(t *testing.T) {
	p := testProduct(t, "tools-plan", "no-such-plugin")
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.Assemble(context.Background(), p, scripts, app.Deps{Context: host.Context{Workspace: "/workspace"}})
	if err == nil {
		t.Fatal("缺失插件必须让装配失败")
	}
	if !strings.Contains(err.Error(), "no-such-plugin") {
		t.Fatalf("错误应点名缺失插件：%v", err)
	}
}

// TestWriteToolsNeedPolicy 钉 coreapp 的门禁：目录里有写/执行工具但没策略插件 → 装配失败。
func TestWriteToolsNeedPolicy(t *testing.T) {
	p := testProduct(t, "tools-fs")
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	asm, err := app.Assemble(context.Background(), p, scripts, app.Deps{
		Context: host.Context{Workspace: t.TempDir(), Log: func(string, string, map[string]any) {}},
	})
	if err == nil {
		_ = asm.Close(context.Background())
		t.Fatal("有 write 工具但没有 policy 插件时必须失败")
	}
	if !strings.Contains(err.Error(), "tool-policy") {
		t.Fatalf("错误应点名缺少 tool-policy：%v", err)
	}
}

// TestOfficialDistInSync 在 node 可用时校验 dist 与 index.ts 同步（CI 没有 node 就跳过）。
func TestOfficialDistInSync(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过 dist 同步检查")
	}
	cmd := exec.Command(node, "plugins/official/build.mjs", "--check")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dist 与源码不同步（运行 node plugins/official/build.mjs）：%v\n%s", err, out)
	}
}

// repoRoot 从测试工作目录回溯到仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根（go.mod）")
		}
		dir = parent
	}
}
