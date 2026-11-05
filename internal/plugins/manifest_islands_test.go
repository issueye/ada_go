package plugins_test

// frontend.client / hydrate 清单校验（docs/20 P1-1）：
// 新字段必须保持旧插件兼容，同时把「没有 client 却声明 hydrate」「third-party
// 带 client」「缺 frontend.script」挡在装配期。

import (
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
)

func islandManifest(trust plugins.Trust, client string, caps []plugins.Capability, slotHydrate, pageHydrate bool) *plugins.Manifest {
	return &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: trust,
		Frontend: &plugins.Frontend{
			Capabilities: caps,
			Client:       client,
			Slots: []plugins.SlotContribution{{
				Slot: "chat.corner", Template: "slot.tsx", Hydrate: slotHydrate,
			}},
			Pages: []plugins.PageContribution{{
				Slug: "providers", Template: "page.tsx", Title: "供应商", Hydrate: pageHydrate,
			}},
		},
	}
}

func TestManifestIslandValidation(t *testing.T) {
	ok := islandManifest(plugins.TrustBuiltin, "client.js",
		[]plugins.Capability{plugins.CapFrontendScript}, true, true)
	if err := ok.Validate("demo"); err != nil {
		t.Fatalf("合法 hydrate 清单被拒: %v", err)
	}

	for _, tc := range []struct {
		name string
		m    *plugins.Manifest
		want string
	}{
		{
			"插槽 hydrate 缺 client",
			islandManifest(plugins.TrustBuiltin, "", []plugins.Capability{plugins.CapFrontendScript}, true, false),
			"没有 frontend.client",
		},
		{
			"页面 hydrate 缺 client",
			islandManifest(plugins.TrustBuiltin, "", []plugins.Capability{plugins.CapFrontendScript}, false, true),
			"没有 frontend.client",
		},
		{
			"client 缺 frontend.script",
			islandManifest(plugins.TrustBuiltin, "client.js", nil, false, false),
			"frontend.script",
		},
		{
			"third-party 带 client",
			islandManifest(plugins.TrustThirdParty, "client.js", []plugins.Capability{plugins.CapFrontendScript}, false, false),
			"不允许前端 client bundle",
		},
		{
			"client 越界路径",
			islandManifest(plugins.TrustBuiltin, "../client.js", []plugins.Capability{plugins.CapFrontendScript}, false, false),
			"assets 内的 .js",
		},
		{
			"client 非 js",
			islandManifest(plugins.TrustBuiltin, "client.mjs", []plugins.Capability{plugins.CapFrontendScript}, false, false),
			"assets 内的 .js",
		},
	} {
		err := tc.m.Validate("demo")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v，期望包含 %q", tc.name, err, tc.want)
		}
	}
}

// 旧插件不声明新字段时行为不变。
func TestManifestIslandBackwardCompatible(t *testing.T) {
	m := islandManifest(plugins.TrustBuiltin, "", nil, false, false)
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("旧清单被拒: %v", err)
	}
}
