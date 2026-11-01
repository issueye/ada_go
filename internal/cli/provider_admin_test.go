package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/plugins/official"
)

// provider-admin 的 route 必须真正读写 providers.json，并影响模型解析。
func TestProviderAdminConfigLifecycle(t *testing.T) {
	ctx := context.Background()
	// 播种只在密钥已通过环境变量/secrets 提供时发生。
	t.Setenv("OPENAI_API_KEY", "sk-seed-test")
	dataDir := t.TempDir()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: t.TempDir(), DataDir: dataDir, Product: productPath(t),
		Scripts: scripts, Assembler: app.Assembler{}, Fake: true, In: strings.NewReader(""), Out: io.Discard,
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
	  "proxy":"http://127.0.0.1:7890","headers":{"X-Test":"1"},
	  "models":[{"id":"m1","contextWindow":128000,"supportsTools":true,"supportsThinking":true,"thinkingLevels":["low","high","max"],"input":["text"]}]
	}}`)
	saved, err := sess.CallHTTPAPI(ctx, "provider-admin", "POST", "/save", save, "")
	if err != nil {
		t.Fatalf("POST /save: %v", err)
	}
	if strings.Contains(string(saved), "sk-secret-1234") {
		t.Fatalf("保存响应泄露了 API Key: %s", saved)
	}
	var info struct {
		ID         string            `json:"id"`
		HasAPIKey  bool              `json:"hasApiKey"`
		APIKeyHint string            `json:"apiKeyHint"`
		Proxy      string            `json:"proxy"`
		Headers    map[string]string `json:"headers"`
		Models     []struct {
			ThinkingLevels []string `json:"thinkingLevels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(saved, &info); err != nil {
		t.Fatalf("save JSON: %v (%s)", err, saved)
	}
	if info.ID != "p_test" || !info.HasAPIKey || !strings.Contains(info.APIKeyHint, "1234") {
		t.Fatalf("保存结果 = %+v", info)
	}
	if info.Proxy != "http://127.0.0.1:7890" || info.Headers["X-Test"] != "1" {
		t.Fatalf("代理/请求头未保存: %+v", info)
	}
	if len(info.Models) != 1 || !slices.Equal(info.Models[0].ThinkingLevels, []string{"low", "high", "max"}) {
		t.Fatalf("推理等级未保存: %+v", info.Models)
	}
	if ref, ok := sess.ResolveModel("p_test", "m1"); !ok || ref.ProviderID != "p_test" {
		t.Fatalf("新供应商的模型没有生效: %+v %v", ref, ok)
	} else if ref.ThinkingLevel != "low" {
		t.Fatalf("默认推理等级 = %q，期望第一档 low", ref.ThinkingLevel)
	}

	file := filepath.Join(dataDir, "providers.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 providers.json: %v", err)
	}
	if !strings.Contains(string(raw), "sk-secret-1234") || !strings.Contains(string(raw), "p_test") {
		t.Fatalf("providers.json 没有落盘密钥/供应商: %s", raw)
	}
	if !strings.Contains(string(raw), "127.0.0.1:7890") || !strings.Contains(string(raw), "X-Test") ||
		!strings.Contains(string(raw), `"thinkingLevels"`) {
		t.Fatalf("providers.json 没有落盘代理/请求头/推理等级: %s", raw)
	}

	// 非法配置必须由后端拒绝，而不是写进 providers.json。
	for name, body := range map[string]string{
		"proxy":   `{"id":"p_bad","name":"bad","protocol":"openai_chat","proxy":"ftp://example.com","models":[]}`,
		"headers": `{"id":"p_bad","name":"bad","protocol":"openai_chat","headers":{"Host":"x"},"models":[]}`,
	} {
		req := json.RawMessage(`{"method":"POST","path":"/save","body":` + body + `}`)
		if _, err := sess.CallHTTPAPI(ctx, "provider-admin", "POST", "/save", req, ""); err == nil {
			t.Fatalf("%s 非法配置没有被拒绝", name)
		}
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

// 供应商配置是机器级共享的：切换工作区（不同数据目录）仍看到同一套配置；
// 没有密钥时也不会凭空播种 OpenAI 模型。
func TestProviderConfigSharedAcrossWorkspaces(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ADAG_SECRET_OPENAI_API_KEY", "")
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	open := func(name string) *cli.Session {
		t.Helper()
		workspace := filepath.Join(home, name)
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", workspace, err)
		}
		sess, err := cli.Open(ctx, cli.Config{
			Workspace: workspace, Product: productPath(t),
			Scripts: scripts, Assembler: app.Assembler{}, Fake: true, In: strings.NewReader(""), Out: io.Discard,
		})
		if err != nil {
			t.Fatalf("cli.Open(%s): %v", name, err)
		}
		t.Cleanup(func() { _ = sess.Close(context.Background()) })
		return sess
	}
	providersOf := func(sess *cli.Session) []string {
		t.Helper()
		raw, err := sess.CallHTTPAPI(ctx, "provider-admin", "GET", "/state", nil, "")
		if err != nil {
			t.Fatalf("GET /state: %v", err)
		}
		var state struct {
			Providers []struct {
				ID string `json:"id"`
			} `json:"providers"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("state JSON: %v (%s)", err, raw)
		}
		out := make([]string, 0, len(state.Providers))
		for _, p := range state.Providers {
			out = append(out, p.ID)
		}
		return out
	}

	a := open("ws-a")
	if ids := providersOf(a); len(ids) != 0 {
		t.Fatalf("没有密钥却播种了供应商: %v", ids)
	}
	// b 先启动，保存后再通过刷新读到同一份共享配置。
	b := open("ws-b")
	save := json.RawMessage(`{"method":"POST","path":"/save","body":{
	  "id":"p_shared","name":"共享供应商","protocol":"openai_chat",
	  "baseUrl":"https://example.com/v1","apiKey":"sk-shared-1234",
	  "models":[{"id":"m1","contextWindow":128000,"supportsTools":true,"input":["text"]}]
	}}`)
	if _, err := a.CallHTTPAPI(ctx, "provider-admin", "POST", "/save", save, ""); err != nil {
		t.Fatalf("POST /save: %v", err)
	}
	if ids := providersOf(b); !slices.Contains(ids, "p_shared") {
		t.Fatalf("切换工作区后没有看到共享供应商: %v", ids)
	}
	if _, ok := b.ResolveModel("p_shared", "m1"); !ok {
		t.Fatal("共享供应商的模型在另一个工作区不可解析")
	}
	global := filepath.Join(home, ".adago", "providers.json")
	if _, err := os.Stat(global); err != nil {
		t.Fatalf("共享配置没有写到 %s: %v", global, err)
	}
}

// 升级迁移：旧版本按工作区保存的供应商配置要进入共享配置，
// 且不能把旧版本自动播种的、没有密钥的默认项一起带过来。
func TestProviderConfigMigratesLegacyWorkspace(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ADAG_SECRET_OPENAI_API_KEY", "")
	legacyDir := filepath.Join(home, ".adago", "workspaces", "legacy")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"version":1,"providers":[
	  {"id":"openai","name":"OpenAI Chat（兼容端点）","protocol":"openai_chat","baseUrl":"https://api.openai.com/v1","models":[{"id":"gpt-4o"}]},
	  {"id":"p_legacy","name":"旧供应商","protocol":"openai_chat","baseUrl":"https://legacy.example/v1","apiKey":"sk-legacy","models":[{"id":"legacy-model"}]}
	]}`
	if err := os.WriteFile(filepath.Join(legacyDir, "providers.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	workspace := filepath.Join(home, "fresh")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: workspace, Product: productPath(t),
		Scripts: scripts, Assembler: app.Assembler{}, Fake: true, In: strings.NewReader(""), Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	raw, err := sess.CallHTTPAPI(ctx, "provider-admin", "GET", "/state", nil, "")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	if !strings.Contains(string(raw), "p_legacy") {
		t.Fatalf("旧供应商没有迁移到共享配置: %s", raw)
	}
	if strings.Contains(string(raw), `"id":"openai"`) {
		t.Fatalf("旧版自动播种的 openai 被误迁移: %s", raw)
	}
	if _, ok := sess.ResolveModel("p_legacy", "legacy-model"); !ok {
		t.Fatal("迁移后的模型不可解析")
	}
}
