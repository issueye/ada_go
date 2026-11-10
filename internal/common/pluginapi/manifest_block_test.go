package pluginapi_test

// Markdown 动态块清单校验：非法字段在加载期拒绝，避免把坏 renderer 带进运行期。

import (
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

func blockManifest(blocks ...pluginapi.BlockContribution) *pluginapi.Manifest {
	return &pluginapi.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: pluginapi.APIVersionMajor,
		Trust:    pluginapi.TrustBuiltin,
		Frontend: &pluginapi.Frontend{Blocks: blocks},
	}
}

func TestManifestBlockValidation(t *testing.T) {
	ok := pluginapi.BlockContribution{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx"}

	m := blockManifest(ok)
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("合法 block 被拒: %v", err)
	}
	if got := m.Frontend.Blocks[0].Profile; got != "display" {
		t.Fatalf("缺省 profile = %q，期望 display", got)
	}

	for _, tc := range []struct {
		name   string
		blocks []pluginapi.BlockContribution
		want   string
	}{
		{"非法名", []pluginapi.BlockContribution{{Name: "Card", Schema: "schemas/card.json", Template: "blocks/card.tsx"}}, "block 名"},
		{"重名", []pluginapi.BlockContribution{ok, ok}, "重复声明"},
		{"schema 绝对路径", []pluginapi.BlockContribution{{Name: "card", Schema: "/tmp/card.json", Template: "blocks/card.tsx"}}, "schema"},
		{"schema 非 json", []pluginapi.BlockContribution{{Name: "card", Schema: "schemas/card.ts", Template: "blocks/card.tsx"}}, "schema"},
		{"template 越界", []pluginapi.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "../card.tsx"}}, "template"},
		{"template 非 tsx", []pluginapi.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.js"}}, "template"},
		{"未知 profile", []pluginapi.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx", Profile: "realtime"}}, "未知 profile"},
		{"version 非正", []pluginapi.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx", Version: -1}}, "version"},
	} {
		err := blockManifest(tc.blocks...).Validate("demo")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

// TestManifestBlockVersionAndPathDefaults 钉住 version 缺省 1 与路径归一：
// 加载器按 TrimSpace 后的键存模板/Schema，装配侧读的是同一份清单。
func TestManifestBlockVersionAndPathDefaults(t *testing.T) {
	m := blockManifest(pluginapi.BlockContribution{
		Name:     "card",
		Schema:   "  schemas/card.json  ",
		Template: " blocks/card.tsx\t",
	})
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("合法 block 被拒: %v", err)
	}
	b := m.Frontend.Blocks[0]
	if b.Version != 1 {
		t.Fatalf("缺省 version = %d，期望 1", b.Version)
	}
	if b.Schema != "schemas/card.json" || b.Template != "blocks/card.tsx" {
		t.Fatalf("路径未归一: schema=%q template=%q", b.Schema, b.Template)
	}

	explicit := blockManifest(pluginapi.BlockContribution{
		Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx", Version: 2,
	})
	if err := explicit.Validate("demo"); err != nil {
		t.Fatalf("显式 version 2 被拒: %v", err)
	}
	if got := explicit.Frontend.Blocks[0].Version; got != 2 {
		t.Fatalf("显式 version = %d，期望 2", got)
	}
}

func TestManifestInteractiveBlockRequiresTrust(t *testing.T) {
	interactive := pluginapi.BlockContribution{
		Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx", Profile: "interactive",
	}
	if err := blockManifest(interactive).Validate("demo"); err != nil {
		t.Fatalf("builtin 的 interactive block 应通过清单校验: %v", err)
	}

	m := blockManifest(interactive)
	m.Trust = pluginapi.TrustThirdParty
	if err := m.Validate("demo"); err == nil || !strings.Contains(err.Error(), "交互") && !strings.Contains(err.Error(), "信任级别") {
		t.Fatalf("third-party interactive block 应被拒: %v", err)
	}
}
