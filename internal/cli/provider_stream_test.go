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

// 配置里的供应商端点不在启动参数 allowlist 里时，Session 也要在加载配置后放行，
// 否则添加自定义供应商后第一次对话必然报 “HTTP 目标 ... 不在 allowlist 内”。
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
