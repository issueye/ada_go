package cli_test

// CLI 的 E2E：真实产品装配 × moejs 宿主 × SQLite × 引擎。
// fake 模型证明「无密钥可跑」，脚本化模型证明多轮工具链在 CLI 路径上成立。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// 产品未声明的随包插件可以从界面显式启用：plugin_meta.user_set 让装配把它加回来。
func TestSessionEnableUndeclaredPlugin(t *testing.T) {
	cfg := testConfig(t, "")
	ctx := context.Background()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)

	hasTool := func(name string) bool {
		for _, d := range sess.Assembly().Tools.List(ctx) {
			if d.Name == name {
				return true
			}
		}
		return false
	}
	if hasTool("git_status") {
		t.Fatal("headless 产品不应默认装配 tools-git")
	}
	view, ok := viewOf(sess, ctx, "tools-git")
	if !ok || view.Enabled || view.Status != "unused" {
		t.Fatalf("未启用前 tools-git = %+v（ok=%v）", view, ok)
	}

	if _, err := sess.SetPluginEnabled(ctx, "tools-git", true); err != nil {
		t.Fatalf("启用 tools-git: %v", err)
	}
	if !hasTool("git_status") {
		t.Fatal("启用后 tools-git 的工具应出现在目录里")
	}
	view, _ = viewOf(sess, ctx, "tools-git")
	if !view.Enabled || view.Status != "ready" {
		t.Fatalf("启用后 tools-git = %+v", view)
	}

	if _, err := sess.SetPluginEnabled(ctx, "tools-git", false); err != nil {
		t.Fatalf("停用 tools-git: %v", err)
	}
	if hasTool("git_status") {
		t.Fatal("停用后 tools-git 的工具应消失")
	}
	view, _ = viewOf(sess, ctx, "tools-git")
	if view.Enabled || view.Status != "unused" {
		t.Fatalf("停用后 tools-git = %+v", view)
	}

	// 纯前端插件没有后端实例，但从界面同样要能启用/停用（状态来自 frontend 段）。
	if _, err := sess.SetPluginEnabled(ctx, "ui-hello-tsx", true); err != nil {
		t.Fatalf("启用 ui-hello-tsx: %v", err)
	}
	view, ok = viewOf(sess, ctx, "ui-hello-tsx")
	if !ok || !view.Enabled || view.Status != "ready" {
		t.Fatalf("启用后 ui-hello-tsx = %+v（ok=%v）", view, ok)
	}
	if _, err := sess.SetPluginEnabled(ctx, "ui-hello-tsx", false); err != nil {
		t.Fatalf("停用 ui-hello-tsx: %v", err)
	}
	view, _ = viewOf(sess, ctx, "ui-hello-tsx")
	if view.Enabled || view.Status != "unused" {
		t.Fatalf("停用后 ui-hello-tsx = %+v", view)
	}
}

// viewOf 从 plugin.list 投影里取一个插件（测试辅助）。
func viewOf(sess *cli.Session, ctx context.Context, id string) (cli.PluginView, bool) {
	for _, v := range sess.PluginViews(ctx) {
		if v.ID == id {
			return v, true
		}
	}
	return cli.PluginView{}, false
}

// 插件工具必须能从 ctx.threadId 拿到会话 ID（tools-plan 按会话读写 TODO 文件）。
// 这里跑完整引擎/执行器路径，防止归属只在 Rollback 工具上注入的回归。
func TestSessionPluginToolReceivesThreadID(t *testing.T) {
	ws := t.TempDir()
	data := t.TempDir()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewFakeProvider(testkit.NewScriptedModel(
		testkit.ToolTurn("call_plan", "read_plan", `{}`),
		testkit.TextTurn("没有计划"),
	))
	cfg := cli.Config{
		Workspace: ws, DataDir: data, Product: productPath(t), Scripts: scripts,
		ModelClient: fake, In: strings.NewReader(""),
	}
	ctx := context.Background()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)

	if term, err := sess.Run(ctx, "看看计划"); err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}
	var jsonl strings.Builder
	if _, err := cli.ExportThread(ctx, cfg.Workspace, cfg.DataDir, sess.CurrentThread(), "jsonl", &jsonl); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.Contains(jsonl.String(), "ctx.threadId 为空") {
		t.Fatalf("工具没有拿到会话 ID:\n%s", jsonl.String())
	}
	if !strings.Contains(jsonl.String(), "计划已清空") {
		t.Fatalf("read_plan 没有正常返回:\n%s", jsonl.String())
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

// ask_user 的提问必须真的走到界面：HasUI=true 时经交互桥等待答案并回填工具结果。
func TestSessionAskUserRoundTripsThroughUI(t *testing.T) {
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewFakeProvider(testkit.NewScriptedModel(
		testkit.ToolTurn("call_ask", "ask_user", `{"question":"你叫什么名字？","kind":"input"}`),
		testkit.TextTurn("好的"),
	))
	cfg := cli.Config{
		Workspace:   t.TempDir(),
		DataDir:     t.TempDir(),
		Product:     productPath(t),
		Scripts:     scripts,
		ModelClient: fake,
		In:          strings.NewReader(""),
		HasUI:       true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)

	// 模拟界面：等交互出现，按 input 回答。
	go func() {
		for i := 0; i < 400; i++ {
			threadID := sess.CurrentThread()
			if threadID != "" {
				if list, err := sess.PendingInteractions(ctx, threadID); err == nil && len(list) > 0 {
					_, _ = sess.RespondInteraction(ctx, threadID, list[0].ID, domain.InteractionAccepted, "Ada")
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	if term, err := sess.Run(ctx, "问我名字"); err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v", term, err)
	}
	var jsonl strings.Builder
	if _, err := cli.ExportThread(ctx, cfg.Workspace, cfg.DataDir, sess.CurrentThread(), "jsonl", &jsonl); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(jsonl.String(), "用户的回答（input）：Ada") {
		t.Fatalf("工具结果里没有界面给出的答案:\n%s", jsonl.String())
	}

	// 答题后的第二次模型请求必须同时带上原始指令和工具结果：问答不是轮次终点。
	reqs := fake.Requests()
	if len(reqs) < 2 {
		t.Fatalf("模型请求数 = %d，期望至少 2（答题后继续本轮）", len(reqs))
	}
	var contextText strings.Builder
	for _, m := range reqs[1].Messages {
		for _, p := range m.Parts {
			contextText.WriteString(p.Text)
		}
	}
	if !strings.Contains(contextText.String(), "问我名字") {
		t.Fatalf("第二次请求丢了原始指令（parent 链/上下文重建有问题）:\n%s", contextText.String())
	}
	if !strings.Contains(contextText.String(), "Ada") {
		t.Fatalf("第二次请求丢了界面回答:\n%s", contextText.String())
	}
}

// 没有界面（HasUI=false）时 ask_user 必须失败关闭，而不是挂住等一个不会来的答案。
func TestSessionAskUserFailsClosedWithoutUI(t *testing.T) {
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatal(err)
	}
	fake := testkit.NewFakeProvider(testkit.NewScriptedModel(
		testkit.ToolTurn("call_ask", "ask_user", `{"question":"你叫什么名字？","kind":"input"}`),
		testkit.TextTurn("好的"),
	))
	cfg := cli.Config{
		Workspace:   t.TempDir(),
		DataDir:     t.TempDir(),
		Product:     productPath(t),
		Scripts:     scripts,
		ModelClient: fake,
		In:          strings.NewReader(""),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(ctx)
	if term, err := sess.Run(ctx, "问我名字"); err != nil || term != domain.TerminalCompleted {
		t.Fatalf("Run = %s / %v（工具失败不应拖垮整轮）", term, err)
	}
	var jsonl strings.Builder
	if _, err := cli.ExportThread(ctx, cfg.Workspace, cfg.DataDir, sess.CurrentThread(), "jsonl", &jsonl); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(jsonl.String(), "用户界面") {
		t.Fatalf("无界面时应报失败关闭原因:\n%s", jsonl.String())
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
