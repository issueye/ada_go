package cli

import (
	"slices"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
)

func TestNormalizeProviderModelsThinkingLevels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   host.ProviderModel
		want    []string
		wantErr bool
	}{
		{
			name:  "levels",
			model: host.ProviderModel{ID: "m", ThinkingLevels: []string{"low", "high", "max"}},
			want:  []string{"low", "high", "max"},
		},
		{
			name:  "normalizes",
			model: host.ProviderModel{ID: "m", ThinkingLevels: []string{" HIGH ", "low", "high"}},
			want:  []string{"high", "low"},
		},
		{name: "default", model: host.ProviderModel{ID: "m"}, want: nil},
		{
			name:    "invalid char",
			model:   host.ProviderModel{ID: "m", ThinkingLevels: []string{"bad level"}},
			wantErr: true,
		},
		{
			name: "too many",
			model: host.ProviderModel{ID: "m", ThinkingLevels: []string{
				"l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9",
			}},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := normalizeProviderModels([]host.ProviderModel{tc.model})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望拒绝，得到 %+v", out)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 1 || !slices.Equal(out[0].ThinkingLevels, tc.want) {
				t.Fatalf("thinkingLevels = %+v，期望 %v", out, tc.want)
			}
			if len(tc.want) > 0 && !out[0].SupportsThinking {
				t.Fatal("配置推理等级时应自动标记 supportsThinking")
			}
		})
	}
}

func TestNormalizeProviderProxyAndHeaders(t *testing.T) {
	if _, err := normalizeProviderProxy("ftp://example.com"); err == nil {
		t.Fatal("ftp 代理应被拒绝")
	}
	if got, err := normalizeProviderProxy("socks5://127.0.0.1:1080"); err != nil || got == "" {
		t.Fatalf("socks5 代理应通过: %q %v", got, err)
	}
	if _, err := normalizeProviderHeaders(map[string]string{"Host": "x"}); err == nil {
		t.Fatal("Host 头应被拒绝")
	}
	got, err := normalizeProviderHeaders(map[string]string{"X-Test": "1"})
	if err != nil || got["X-Test"] != "1" {
		t.Fatalf("自定义头 = %#v, %v", got, err)
	}
}
