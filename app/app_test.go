package app_test

// 组合根的验收：产品声明 × 嵌入的官方插件 → 宿主装配 → 真实调用。
//
// 这些用例天然允许出现官方插件 id（internal/ 的非测试源码不行，这里是 app 的测试），
// 也正好证明「官方插件与第三方走同一入口」：装配路径里没有任何 Go 侧注册 API。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/plugins/official"
)

// memState 是线程级 KV 的测试替身（host.Context.State 的形状）。
type memState struct {
	mu   sync.Mutex
	data map[string]json.RawMessage
}

func newMemState() *memState { return &memState{data: map[string]json.RawMessage{}} }

func (m *memState) state(_ context.Context, op host.StateOp) (json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch op.Kind {
	case "get":
		if v, ok := m.data[op.Key]; ok {
			return v, nil
		}
		return json.RawMessage("null"), nil
	case "set":
		m.data[op.Key] = op.Value
		return nil, nil
	case "delete":
		delete(m.data, op.Key)
		return nil, nil
	case "list":
		out := map[string]json.RawMessage{}
		for k, v := range m.data {
			if op.Prefix == "" || strings.HasPrefix(k, op.Prefix) {
				out[k] = v
			}
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("未知 state 操作 %q", op.Kind)
}

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
	state := newMemState()
	hc := host.Context{
		Workspace: "/workspace",
		State:     state.state,
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
