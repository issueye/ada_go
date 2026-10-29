package cli_test

// CLI 的 E2E：真实产品装配 × moejs 宿主 × SQLite × 引擎。
// fake 模型证明「无密钥可跑」，脚本化模型证明多轮工具链在 CLI 路径上成立。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/plugins/official"
)

func testConfig(t *testing.T, model string) cli.Config {
	t.Helper()
	ws := t.TempDir()
	data := t.TempDir()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	return cli.Config{
		Workspace: ws,
		DataDir:   data,
		Product:   productPath(t),
		Scripts:   scripts,
		Fake:      model == "",
		In:        strings.NewReader(""),
	}
}

func productPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "products", "headless.agent.json")
}

func TestSessionFakeRunAndExport(t *testing.T) {
	cfg := testConfig(t, "")
	var out strings.Builder
	cfg.Out = &out
	ctx := context.Background()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)

	if term, err := sess.Run(ctx, "hello"); err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}
	if !strings.Contains(out.String(), "fake: hello") {
		t.Fatalf("输出 = %q", out.String())
	}
	threadID := sess.CurrentThread()
	if threadID == "" {
		t.Fatal("没有创建线程")
	}
	items, err := cli.ListThreads(ctx, cfg.Workspace, cfg.DataDir, 10)
	if err != nil || len(items) != 1 || items[0].ID != threadID {
		t.Fatalf("ListThreads = %+v / %v", items, err)
	}
	var jsonl strings.Builder
	summary, err := cli.ExportThread(ctx, cfg.Workspace, cfg.DataDir, threadID, "jsonl", &jsonl)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if summary.Entries != 2 {
		t.Fatalf("entry 数 = %d，期望 2", summary.Entries)
	}
	if !strings.Contains(jsonl.String(), `"type":"entry"`) {
		t.Fatalf("导出内容不对: %s", jsonl.String())
	}
}

func TestSessionScriptedToolTurn(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewFakeProvider(testkit.NewScriptedModel(
		testkit.ToolTurn("call_1", "read_file", `{"path":"/workspace/a.txt"}`),
		testkit.TextTurn("读到了 alpha"),
	))
	var out strings.Builder
	cfg := cli.Config{
		Workspace:   ws,
		DataDir:     data,
		Product:     productPath(t),
		Scripts:     scripts,
		ModelClient: fake,
		In:          strings.NewReader(""),
		Out:         &out,
	}
	ctx := context.Background()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)
	term, err := sess.Run(ctx, "读 a.txt")
	if err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}
	if !strings.Contains(out.String(), "读到了 alpha") {
		t.Fatalf("输出 = %q", out.String())
	}
	if fake.RequestCount() < 2 {
		t.Fatalf("模型请求数 = %d，期望至少 2（工具轮 + 收尾）", fake.RequestCount())
	}
}

func TestSessionFailsClosedWithoutProvider(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Fake = false
	cfg.Product = filepath.Join(filepath.Dir(productPath(t)), "skeleton.agent.json")
	ctx := context.Background()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)
	if _, err := sess.Run(ctx, "hello"); err == nil {
		t.Fatal("没有 provider 且未 --fake 时必须失败关闭")
	}
}
