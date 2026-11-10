package plugins

import "testing"

// global.config 是 verified/builtin 能力：third-party 声明必须装配期失败。
func TestGlobalConfigCapabilityTrust(t *testing.T) {
	m := &Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: APIVersionMajor,
		Trust: TrustVerified, Sig: "sig",
		Backend: &Backend{
			Entry:        "index.ts",
			Capabilities: []Capability{CapGlobalConfig},
		},
	}
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("verified 应允许 global.config: %v", err)
	}
	m.Trust = TrustThirdParty
	if err := m.Validate("demo"); err == nil {
		t.Fatal("third-party 声明 global.config 应被拒绝")
	}
}
