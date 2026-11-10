package host

// 静态预检的边界：只编译/链接，不执行入口工厂；语法错误与未授权模块在预检期失败。

import (
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins"
)

func staticScript(t *testing.T, source string, caps []plugins.Capability) (Script, *CapabilitySet) {
	t.Helper()
	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustThirdParty,
		Backend: &plugins.Backend{
			Entry:        "index.ts",
			Capabilities: caps,
		},
	}
	if err := m.Validate("demo"); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	set, err := NewCapabilitySet(m, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("caps: %v", err)
	}
	return Script{
		Manifest:  m,
		EntryName: "plugin://demo/index.ts",
		Source:    source,
	}, set
}

func TestValidateScriptDoesNotExecute(t *testing.T) {
	s, caps := staticScript(t, `throw new Error("装配期不应执行我");
export default function () {}`, nil)
	if err := ValidateScript(s, caps); err != nil {
		t.Fatalf("静态预检不应执行入口：%v", err)
	}
}

func TestValidateScriptRejectsSyntaxError(t *testing.T) {
	s, caps := staticScript(t, `export default function (`, nil)
	if err := ValidateScript(s, caps); err == nil {
		t.Fatal("语法错误应被静态预检拒绝")
	}
}
