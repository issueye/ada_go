package plugins_test

// Markdown 动态块清单校验：非法字段在加载期拒绝，避免把坏 renderer 带进运行期。

import (
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

func blockManifest(blocks ...plugins.BlockContribution) *plugins.Manifest {
	return &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust:    plugins.TrustBuiltin,
		Frontend: &plugins.Frontend{Blocks: blocks},
	}
}

func TestManifestBlockValidation(t *testing.T) {
	ok := plugins.BlockContribution{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx"}

	m := blockManifest(ok)
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("合法 block 被拒: %v", err)
	}
	if got := m.Frontend.Blocks[0].Profile; got != "display" {
		t.Fatalf("缺省 profile = %q，期望 display", got)
	}

	for _, tc := range []struct {
		name   string
		blocks []plugins.BlockContribution
		want   string
	}{
		{"非法名", []plugins.BlockContribution{{Name: "Card", Schema: "schemas/card.json", Template: "blocks/card.tsx"}}, "block 名"},
		{"重名", []plugins.BlockContribution{ok, ok}, "重复声明"},
		{"schema 绝对路径", []plugins.BlockContribution{{Name: "card", Schema: "/tmp/card.json", Template: "blocks/card.tsx"}}, "schema"},
		{"schema 非 json", []plugins.BlockContribution{{Name: "card", Schema: "schemas/card.ts", Template: "blocks/card.tsx"}}, "schema"},
		{"template 越界", []plugins.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "../card.tsx"}}, "template"},
		{"template 非 tsx", []plugins.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.js"}}, "template"},
		{"未知 profile", []plugins.BlockContribution{{Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx", Profile: "realtime"}}, "未知 profile"},
	} {
		err := blockManifest(tc.blocks...).Validate("demo")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

func TestManifestInteractiveBlockRequiresTrust(t *testing.T) {
	interactive := plugins.BlockContribution{
		Name: "card", Schema: "schemas/card.json", Template: "blocks/card.tsx", Profile: "interactive",
	}
	if err := blockManifest(interactive).Validate("demo"); err != nil {
		t.Fatalf("builtin 的 interactive block 应通过清单校验: %v", err)
	}

	m := blockManifest(interactive)
	m.Trust = plugins.TrustThirdParty
	if err := m.Validate("demo"); err == nil || !strings.Contains(err.Error(), "交互") && !strings.Contains(err.Error(), "信任级别") {
		t.Fatalf("third-party interactive block 应被拒: %v", err)
	}
}
