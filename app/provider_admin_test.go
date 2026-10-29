package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/plugins/host"
)

// provider-admin 的后端路由确实通过 model.admin 能力取数据、切模型。
func TestProviderAdminRoutesUseModelAdmin(t *testing.T) {
	ctx := context.Background()
	var selected struct{ provider, model, thread string }
	hc := host.Context{
		Workspace: t.TempDir(),
		Providers: func() host.ProviderCatalog {
			return host.ProviderCatalog{
				Providers: []host.ProviderInfo{{
					ID: "openai", DisplayName: "openai",
					Models: []host.ProviderModel{{ID: "gpt-4o", ContextWindow: 128000, SupportsTools: true}},
				}},
				Current: host.ModelRef{ProviderID: "openai", ModelID: "gpt-4o"},
			}
		},
		SelectModel: func(_ context.Context, providerID, modelID, threadID string) (host.ModelRef, error) {
			selected.provider, selected.model, selected.thread = providerID, modelID, threadID
			return host.ModelRef{ProviderID: providerID, ModelID: modelID}, nil
		},
	}
	p := testProduct(t, "provider-admin")
	asm := assemble(t, p, hc, nil)

	out, err := asm.CallHTTPAPI(ctx, "provider-admin", "GET", "/state", nil, "")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	for _, want := range []string{`"id":"openai"`, `"modelId":"gpt-4o"`, `"contextWindow":128000`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("GET /state 缺少 %s: %s", want, out)
		}
	}

	req := json.RawMessage(`{"method":"POST","path":"/select","body":{"providerId":"openai","modelId":"gpt-4o"}}`)
	out, err = asm.CallHTTPAPI(ctx, "provider-admin", "POST", "/select", req, "")
	if err != nil {
		t.Fatalf("POST /select: %v", err)
	}
	if selected.provider != "openai" || selected.model != "gpt-4o" {
		t.Fatalf("没有调用 SelectModel: %+v", selected)
	}
	if !strings.Contains(string(out), `"providerId":"openai"`) {
		t.Fatalf("POST /select 结果 = %s", out)
	}

	if _, err := asm.CallHTTPAPI(ctx, "provider-admin", "GET", "/missing", nil, ""); !errors.Is(err, app.ErrRouteNotFound) {
		t.Fatalf("未知路由 = %v, want ErrRouteNotFound", err)
	}
}
