package cli_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/plugins/official"
)

// 自定义供应商端点（当前无 host 黑白名单）加载配置后应能直接发起流式请求，
// 不能因端点不在启动参数里而被拒绝。
func TestConfiguredProviderStreamAllowsConfiguredHost(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfgJSON := fmt.Sprintf(`{"version":1,"providers":[{
	  "id":"test","name":"Test","protocol":"openai_chat","baseUrl":%q,"apiKey":"sk-test",
	  "models":[{"id":"m1","contextWindow":128000,"supportsTools":true}]
	}]}`, srv.URL)
	if err := os.WriteFile(filepath.Join(dataDir, "providers.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: t.TempDir(), DataDir: dataDir, Product: productPath(t),
		Scripts: scripts, Provider: "test", Model: "m1",
		In: strings.NewReader(""), Out: io.Discard,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	term, err := sess.Run(ctx, "hi")
	if err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("路径 = %q", gotPath)
	}
}

// --debug 应把每次出网请求写到 Err，且不能泄漏 API Key。
func TestDebugModeLogsProviderRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfgJSON := fmt.Sprintf(`{"version":1,"providers":[{
	  "id":"test","name":"Test","protocol":"openai_chat","baseUrl":%q,"apiKey":"sk-test-secret",
	  "models":[{"id":"m1","contextWindow":128000,"supportsTools":true}]
	}]}`, srv.URL)
	if err := os.WriteFile(filepath.Join(dataDir, "providers.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	var debug strings.Builder
	ctx := context.Background()
	sess, err := cli.Open(ctx, cli.Config{
		Workspace: t.TempDir(), DataDir: dataDir, Product: productPath(t),
		Scripts: scripts, Provider: "test", Model: "m1",
		In: strings.NewReader(""), Out: io.Discard, Err: &debug,
		Debug: true,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	if _, err := sess.Run(ctx, "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := debug.String()
	if !strings.Contains(out, ">>> #1 POST "+srv.URL+"/chat/completions") {
		t.Fatalf("调试输出缺少请求行:\n%s", out)
	}
	if !strings.Contains(out, "Authorization: ***") {
		t.Fatalf("Authorization 未脱敏:\n%s", out)
	}
	if strings.Contains(out, "sk-test-secret") {
		t.Fatalf("调试输出泄漏 API Key:\n%s", out)
	}
}
