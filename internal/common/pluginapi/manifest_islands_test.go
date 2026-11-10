package pluginapi_test

// frontend.client / hydrate 清单校验（docs/20 P1-1）：
// 新字段必须保持旧插件兼容，同时把「没有 client 却声明 hydrate」「third-party
// 带 client」「缺 frontend.script」挡在装配期。

import (
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

func islandManifest(trust pluginapi.Trust, client string, caps []pluginapi.Capability, slotHydrate, pageHydrate bool) *pluginapi.Manifest {
	return &pluginapi.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: pluginapi.APIVersionMajor,
		Trust: trust,
		Frontend: &pluginapi.Frontend{
			Capabilities: caps,
			Client:       client,
			Slots: []pluginapi.SlotContribution{{
				Slot: "chat.corner", Template: "slot.tsx", Hydrate: slotHydrate,
			}},
			Pages: []pluginapi.PageContribution{{
				Slug: "providers", Template: "page.tsx", Title: "供应商", Hydrate: pageHydrate,
			}},
		},
	}
}

func TestManifestIslandValidation(t *testing.T) {
	ok := islandManifest(pluginapi.TrustBuiltin, "client.js",
		[]pluginapi.Capability{pluginapi.CapFrontendScript}, true, true)
	if err := ok.Validate("demo"); err != nil {
		t.Fatalf("合法 hydrate 清单被拒: %v", err)
	}

	verified := islandManifest(pluginapi.TrustVerified, "client.js",
		[]pluginapi.Capability{pluginapi.CapFrontendScript}, true, false)
	verified.Sig = "sig"
	if err := verified.Validate("demo"); err != nil {
		t.Fatalf("verified 带 client 应允许: %v", err)
	}

	for _, tc := range []struct {
		name string
		m    *pluginapi.Manifest
		want string
	}{
		{
			"插槽 hydrate 缺 client",
			islandManifest(pluginapi.TrustBuiltin, "", []pluginapi.Capability{pluginapi.CapFrontendScript}, true, false),
			"没有 frontend.client",
		},
		{
			"页面 hydrate 缺 client",
			islandManifest(pluginapi.TrustBuiltin, "", []pluginapi.Capability{pluginapi.CapFrontendScript}, false, true),
			"没有 frontend.client",
		},
		{
			"client 缺 frontend.script",
			islandManifest(pluginapi.TrustBuiltin, "client.js", nil, false, false),
			"frontend.script",
		},
		{
			"third-party 带 client",
			islandManifest(pluginapi.TrustThirdParty, "client.js", []pluginapi.Capability{pluginapi.CapFrontendScript}, false, false),
			"不允许前端 client bundle",
		},
		{
			"client 越界路径",
			islandManifest(pluginapi.TrustBuiltin, "../client.js", []pluginapi.Capability{pluginapi.CapFrontendScript}, false, false),
			"assets 内的 .js",
		},
		{
			"client 非 js",
			islandManifest(pluginapi.TrustBuiltin, "client.mjs", []pluginapi.Capability{pluginapi.CapFrontendScript}, false, false),
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
	m := islandManifest(pluginapi.TrustBuiltin, "", nil, false, false)
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("旧清单被拒: %v", err)
	}
}
