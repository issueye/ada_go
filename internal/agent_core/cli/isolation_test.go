package cli_test

// 装配隔离：用户显式启用、产品未声明的随包插件坏掉时不能拖垮整个产品；
// 产品声明的插件失败仍然是产品失败（声明就是承诺）。

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/agent_core/cli"
	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/agent_core/corecontracts"
	"github.com/adag/ada-go/internal/agent_core/plugins/host"
)

// failingOptInAssembler 对指定插件返回装配错误：optIn 里的随包插件命中 failID，
// failAny=true 时所有装配都失败（用来验证产品声明的插件不可隔离）。
type failingOptInAssembler struct {
	app.Assembler
	failID  string
	failAny bool
}

func (a failingOptInAssembler) AssembleWithExtras(
	ctx context.Context, p *coreapp.Product, base, extras []host.Script,
	disabled, optIn map[string]bool, deps corecontracts.Deps,
) (corecontracts.Assembly, error) {
	if a.failAny || optIn[a.failID] {
		return nil, fmt.Errorf("app: 装配插件 %s 失败: 测试注入的装配错误", a.failID)
	}
	return a.Assembler.AssembleWithExtras(ctx, p, base, extras, disabled, optIn, deps)
}

// TestOptInBundledPluginFailureIsIsolated：用户启用但坏掉的随包插件应被隔离，
// Core 照常起来，状态视图为 failed 且带可读错误。
func TestOptInBundledPluginFailureIsIsolated(t *testing.T) {
	cfg := testConfig(t, "")
	// 启用产品未声明的随包插件：进入 optIn（产品声明保持原样）。
	state := `{"version":1,"plugins":{"mcp-client":{"enabled":true}}}`
	if err := os.WriteFile(cfg.PluginStatePath, []byte(state), 0o600); err != nil {
		t.Fatalf("写 plugins.json: %v", err)
	}
	cfg.Assembler = failingOptInAssembler{failID: "mcp-client"}
	sess := openSession(t, cfg)

	ctx := context.Background()
	found := false
	for _, v := range sess.PluginViews(ctx) {
		if v.ID != "mcp-client" {
			continue
		}
		found = true
		if v.Status != "failed" || v.LastError == "" {
			t.Fatalf("被隔离的随包插件状态: %+v", v)
		}
	}
	if !found {
		t.Fatal("插件视图里没有 mcp-client")
	}
}

// TestDeclaredPluginFailureStillFailsAssembly：产品声明的插件失败不可被隔离。
func TestDeclaredPluginFailureStillFailsAssembly(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Assembler = failingOptInAssembler{failID: "tools-plan", failAny: true}
	if _, err := cli.Open(context.Background(), cfg); err == nil {
		t.Fatal("产品声明的插件装配失败时，Open 必须失败")
	}
}
