package host

import (
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins"
)

// TestCapabilitySetEnvFallsBackToHostEnv 钉住 backend.env 的生产语义：
// 组合根没有注入 HostEnv 时，仍按清单白名单从宿主进程环境取值；
// 未声明的名字不注入，宿主不存在的名字也不伪造成空串。
func TestCapabilitySetEnvFallsBackToHostEnv(t *testing.T) {
	t.Setenv("ADAG_TEST_ENV_FALLBACK", "fallback")
	t.Setenv("ADAG_TEST_ENV_UNUSED", "ignored")

	m := &plugins.Manifest{
		ID: "demo", Name: "Demo", Version: "0.1.0", APIVersion: plugins.APIVersionMajor,
		Trust: plugins.TrustBuiltin,
		Backend: &plugins.Backend{
			Entry:        "index.js",
			Capabilities: []plugins.Capability{plugins.CapFSRead},
			Env:          []string{"ADAG_TEST_ENV_FALLBACK", "ADAG_TEST_ENV_MISSING"},
		},
	}
	caps, err := NewCapabilitySet(m, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("NewCapabilitySet: %v", err)
	}
	if got := caps.Env["ADAG_TEST_ENV_FALLBACK"]; got != "fallback" {
		t.Fatalf("声明的环境变量应从宿主回退注入，实际 %q", got)
	}
	if _, ok := caps.Env["ADAG_TEST_ENV_MISSING"]; ok {
		t.Fatal("宿主不存在的名字不应伪造成空串")
	}
	if _, ok := caps.Env["ADAG_TEST_ENV_UNUSED"]; ok {
		t.Fatal("未在清单声明的名字不得注入")
	}

	// 显式注入优先：组合根给了值就以它为准（装配期冻结）。
	caps, err = NewCapabilitySet(m, t.TempDir(), map[string]string{"ADAG_TEST_ENV_FALLBACK": "injected"}, nil)
	if err != nil {
		t.Fatalf("NewCapabilitySet: %v", err)
	}
	if got := caps.Env["ADAG_TEST_ENV_FALLBACK"]; got != "injected" {
		t.Fatalf("显式 HostEnv 应优先，实际 %q", got)
	}
}
