package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/plugins/official"
)

// provider-admin 的 route 必须真正读写 providers.json，并影响模型解析。
func TestProviderAdminConfigLifecycle(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: t.TempDir(), DataDir: dataDir, Product: productPath(t),
		Scripts: scripts, Fake: true, In: strings.NewReader(""), Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	stateRaw, err := sess.CallHTTPAPI(ctx, "provider-admin", "GET", "/state", nil, "")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	var state struct {
		Providers []struct {
			ID     string `json:"id"`
			HasKey bool   `json:"hasApiKey"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(stateRaw, &state); err != nil {
		t.Fatalf("state JSON: %v (%s)", err, stateRaw)
	}
	foundSeed := false
	for _, p := range state.Providers {
		if p.ID == "openai" && len(p.Models) > 0 {
			foundSeed = true
		}
	}
	if !foundSeed {
		t.Fatalf("首次启动没有从适配器播种 openai 供应商: %s", stateRaw)
	}

	save := json.RawMessage(`{"method":"POST","path":"/save","body":{
	  "id":"p_test","name":"测试供应商","protocol":"openai_chat",
	  "baseUrl":"https://example.com/v1","apiKey":"sk-secret-1234",
	  "models":[{"id":"m1","contextWindow":128000,"supportsTools":true,"input":["text"]}]
	}}`)
	saved, err := sess.CallHTTPAPI(ctx, "provider-admin", "POST", "/save", save, "")
	if err != nil {
		t.Fatalf("POST /save: %v", err)
	}
	if strings.Contains(string(saved), "sk-secret-1234") {
		t.Fatalf("保存响应泄露了 API Key: %s", saved)
	}
	var info struct {
		ID         string `json:"id"`
		HasAPIKey  bool   `json:"hasApiKey"`
		APIKeyHint string `json:"apiKeyHint"`
	}
	if err := json.Unmarshal(saved, &info); err != nil {
		t.Fatalf("save JSON: %v (%s)", err, saved)
	}
	if info.ID != "p_test" || !info.HasAPIKey || !strings.Contains(info.APIKeyHint, "1234") {
		t.Fatalf("保存结果 = %+v", info)
	}
	if ref, ok := sess.ResolveModel("p_test", "m1"); !ok || ref.ProviderID != "p_test" {
		t.Fatalf("新供应商的模型没有生效: %+v %v", ref, ok)
	}

	file := filepath.Join(dataDir, "providers.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 providers.json: %v", err)
	}
	if !strings.Contains(string(raw), "sk-secret-1234") || !strings.Contains(string(raw), "p_test") {
		t.Fatalf("providers.json 没有落盘密钥/供应商: %s", raw)
	}

	remove := json.RawMessage(`{"method":"POST","path":"/remove","body":{"id":"p_test"}}`)
	if _, err := sess.CallHTTPAPI(ctx, "provider-admin", "POST", "/remove", remove, ""); err != nil {
		t.Fatalf("POST /remove: %v", err)
	}
	if _, ok := sess.ResolveModel("p_test", "m1"); ok {
		t.Fatal("删除供应商后模型仍然可解析")
	}
	raw, _ = os.ReadFile(file)
	if strings.Contains(string(raw), "p_test") {
		t.Fatalf("providers.json 仍包含已删除供应商: %s", raw)
	}
}
