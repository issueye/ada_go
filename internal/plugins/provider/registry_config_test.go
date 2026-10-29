package provider_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/plugins/provider"
)

func hostProviderRegistration() host.ProviderRegistration {
	return host.ProviderRegistration{
		PluginID: "adapter",
		Trust:    domain.TrustBuiltin,
		Def: host.ProviderDef{
			ID:     "openai",
			Models: []host.ModelInfo{{ID: "static", ContextWindow: 1000}},
			Stream: host.HandlerRef{Slot: 1},
		},
	}
}

func TestProtocolMapping(t *testing.T) {
	cases := []struct {
		p  provider.Protocol
		id string
		ok bool
	}{
		{provider.ProtocolOpenAIChat, "openai", true},
		{provider.ProtocolOpenAIResponses, "openai-responses", true},
		{provider.ProtocolAnthropic, "anthropic", true},
		{provider.Protocol("nope"), "", false},
	}
	for _, c := range cases {
		if c.ok {
			if got := c.p.AdapterID(); got != c.id {
				t.Fatalf("%s.AdapterID() = %s, want %s", c.p, got, c.id)
			}
			if got := provider.ProtocolForAdapter(c.id); got != c.p {
				t.Fatalf("ProtocolForAdapter(%s) = %s, want %s", c.id, got, c.p)
			}
		}
		if c.p.Valid() != c.ok {
			t.Fatalf("%s.Valid() = %v, want %v", c.p, c.p.Valid(), c.ok)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "providers.json")
	store := provider.NewStore(path)

	if cfgs, exists, err := store.Load(); err != nil || exists || len(cfgs) != 0 {
		t.Fatalf("初次 Load = %+v, %v, %v", cfgs, exists, err)
	}

	in := []provider.Config{{
		ID: "p_1", Name: "测试", Protocol: provider.ProtocolOpenAIChat,
		BaseURL: "https://example.com/v1", APIKey: "sk-secret-1234",
		Models: []provider.Model{{ID: "m1", ContextWindow: 128000, SupportsTools: true, Input: []string{"text", "image"}}},
	}}
	if err := store.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, exists, err := store.Load()
	if err != nil || !exists || len(out) != 1 {
		t.Fatalf("Load = %+v, %v, %v", out, exists, err)
	}
	if out[0].ID != "p_1" || out[0].APIKey != "sk-secret-1234" || len(out[0].Models) != 1 {
		t.Fatalf("配置回读 = %+v", out[0])
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("配置文件权限 = %o, want 600", info.Mode().Perm())
		}
	}

	// 空配置也要落盘：用户删光供应商后不能被当成「首次启动」重新播种。
	if err := store.Save(nil); err != nil {
		t.Fatal(err)
	}
	out, exists, err = store.Load()
	if err != nil || !exists || len(out) != 0 {
		t.Fatalf("空配置 Load = %+v, %v, %v", out, exists, err)
	}
}

func TestRegistryUsesConfiguredProviders(t *testing.T) {
	r := provider.New("")
	if err := r.AddProvider(hostProviderRegistration()); err != nil {
		t.Fatal(err)
	}
	r.SetConfigs([]provider.Config{
		{ID: "a", Name: "A", Protocol: provider.ProtocolOpenAIChat, Models: []provider.Model{{ID: "m1"}, {ID: "m2", Disabled: true}}},
		{ID: "b", Name: "B", Protocol: provider.ProtocolOpenAIChat, Disabled: true, Models: []provider.Model{{ID: "m3"}}},
	})
	if got := r.Providers(); len(got) != 1 || got[0] != "a" {
		t.Fatalf("Providers = %v, want [a]", got)
	}
	if models := r.Models("a"); len(models) != 1 || models[0].ID != "m1" {
		t.Fatalf("Models(a) = %+v", models)
	}
	if models := r.Models("b"); len(models) != 1 || models[0].ID != "m3" {
		t.Fatalf("停用供应商的模型仍应可读（页面展示）: %+v", models)
	}
	if got := r.AdapterIDs(); len(got) != 1 || got[0] != "openai" {
		t.Fatalf("AdapterIDs = %v", got)
	}
}
