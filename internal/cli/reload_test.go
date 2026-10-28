package cli_test

// M3 插件平台验收：第三方插件安装 → 热重载 → 启用持久化 → 失败回滚 → 在途轮次保护。
// 全部走真实装配（官方插件 × moejs × SQLite × 引擎），fake 模型不需要密钥。

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/plugins/discovery"
	"github.com/adag/ada-go/internal/ports"
)

// helloEchoManifest / helloEchoSource 与 plugins/hello-echo 保持同形（示例即文档）。
const helloEchoManifest = `{
  "id": "hello-echo",
  "name": "Hello Echo",
  "version": "0.1.0",
  "apiVersion": 1,
  "description": "测试夹具：只读回显工具",
  "trust": "third-party",
  "backend": { "entry": "index.ts", "capabilities": [] }
}`

const helloEchoSource = `export default function (agent) {
  agent.registerTool({
    name: "echo",
    description: "原样返回 text 参数",
    parameters: { type: "object", properties: { text: { type: "string" } }, required: ["text"] },
    access: "read",
    async execute(args) {
      return { content: [{ type: "text", text: String(args && args.text || "") }] };
    },
  });
}
`

const loudProviderSource = `export default function (agent) {
  agent.registerProvider({
    id: "loud",
    models: [{ id: "loud-1", contextWindow: 1000, supportsTools: false }],
    stream: async (req, ctx) => {},
  });
}
`

func writePluginDir(t *testing.T, workspace, id, manifest, source string) string {
	t.Helper()
	dir := filepath.Join(workspace, ".adago", "plugins", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func makeHelloEchoZip(t *testing.T) string {
	t.Helper()
	zipPath := filepath.Join(t.TempDir(), "hello-echo.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"hello-echo/plugin.json": helloEchoManifest,
		"hello-echo/index.ts":    helloEchoSource,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
}

// catalogToolNames 返回当前目录里的全部工具名。
func catalogToolNames(t *testing.T, sess *cli.Session) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, d := range sess.Assembly().Tools.List(context.Background()) {
		out[d.Name] = true
	}
	return out
}

func assertEcho(t *testing.T, sess *cli.Session, want bool, why string) {
	t.Helper()
	got := catalogToolNames(t, sess)["echo"]
	if got != want {
		t.Fatalf("%s: echo 工具存在=%v，期望 %v", why, got, want)
	}
}

func openSession(t *testing.T, cfg cli.Config) *cli.Session {
	t.Helper()
	sess, err := cli.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

// TestPluginInstallReloadAndToggle 是 M3 的主链路验收：
// 安装 → 重载出现 echo → 禁用工具消失（INV-11）→ 再启用恢复。
func TestPluginInstallReloadAndToggle(t *testing.T) {
	cfg := testConfig(t, "")
	ctx := context.Background()
	sess := openSession(t, cfg)
	assertEcho(t, sess, false, "安装前不应有第三方工具")

	id, err := discovery.InstallZip(makeHelloEchoZip(t), cfg.Workspace)
	if err != nil || id != "hello-echo" {
		t.Fatalf("InstallZip = %q / %v", id, err)
	}
	if _, err := sess.ReloadPlugins(ctx, ""); err != nil {
		t.Fatalf("ReloadPlugins: %v", err)
	}
	assertEcho(t, sess, true, "重载后 echo 应该出现")

	view, err := sess.SetPluginEnabled(ctx, "hello-echo", false)
	if err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	if view.Enabled || view.Status != "disabled" {
		t.Fatalf("禁用后的视图: %+v", view)
	}
	assertEcho(t, sess, false, "禁用后工具必须消失（INV-11）")

	if _, err := sess.SetPluginEnabled(ctx, "hello-echo", true); err != nil {
		t.Fatalf("SetPluginEnabled(true): %v", err)
	}
	assertEcho(t, sess, true, "重新启用后工具应恢复")

	// plugin.list 视图：第三方带 enabled/status/fingerprint/source。
	var found *cli.PluginView
	for _, v := range sess.PluginViews(ctx) {
		if v.ID == "hello-echo" {
			copy := v
			found = &copy
		}
	}
	if found == nil {
		t.Fatal("PluginViews 没有 hello-echo")
	}
	if found.Source != discovery.SourceWorkspace || found.Fingerprint == "" ||
		!found.Enabled || found.Status != "ready" {
		t.Fatalf("第三方视图不完整: %+v", found)
	}
}

// TestSetEnabledPersistsAcrossRestart：启用状态落在 plugin_meta，重启 Session 后仍生效。
func TestSetEnabledPersistsAcrossRestart(t *testing.T) {
	cfg := testConfig(t, "")
	ctx := context.Background()
	writePluginDir(t, cfg.Workspace, "hello-echo", helloEchoManifest, helloEchoSource)

	sess := openSession(t, cfg)
	assertEcho(t, sess, true, "缺省启用：插件目录存在即加载")
	if _, err := sess.SetPluginEnabled(ctx, "hello-echo", false); err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	assertEcho(t, sess, false, "禁用后消失")
	if err := sess.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 同一工作区与数据目录重启：禁用状态必须还在。
	sess2, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("重开 Session: %v", err)
	}
	defer sess2.Close(ctx)
	assertEcho(t, sess2, false, "重启后禁用状态应保持")
	for _, v := range sess2.PluginViews(ctx) {
		if v.ID == "hello-echo" && (v.Enabled || v.Status != "disabled") {
			t.Fatalf("重启后的视图: %+v", v)
		}
	}
	if _, err := sess2.SetPluginEnabled(ctx, "hello-echo", true); err != nil {
		t.Fatalf("重新启用: %v", err)
	}
	assertEcho(t, sess2, true, "重新启用后恢复")
}

// TestReloadFailureKeepsOldGeneration：坏插件让重载失败时，旧代保持原样
// （echo 还在），修复后重载成功。
func TestReloadFailureKeepsOldGeneration(t *testing.T) {
	cfg := testConfig(t, "")
	ctx := context.Background()
	dir := writePluginDir(t, cfg.Workspace, "hello-echo", helloEchoManifest, helloEchoSource)
	sess := openSession(t, cfg)
	assertEcho(t, sess, true, "初始装配已包含 echo")

	// 弄坏入口源码：发现层拒绝 → 重载失败回滚。
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte("export default function ( {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.ReloadPlugins(ctx, ""); err == nil {
		t.Fatal("坏插件的重载应该失败")
	}
	assertEcho(t, sess, true, "失败回滚：旧代必须原样保留（Core 不降级为空）")

	// 修复后重载成功。
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(helloEchoSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.ReloadPlugins(ctx, ""); err != nil {
		t.Fatalf("修复后的重载: %v", err)
	}
	assertEcho(t, sess, true, "修复后 echo 回到目录")
}

// TestThirdPartyProviderRejectedReadable：第三方脚本尝试 registerProvider 时
// 装配失败且报错可读（点名插件与原因），旧代保留。
func TestThirdPartyProviderRejectedReadable(t *testing.T) {
	cfg := testConfig(t, "")
	ctx := context.Background()
	writePluginDir(t, cfg.Workspace, "hello-echo", helloEchoManifest, helloEchoSource)
	sess := openSession(t, cfg)
	assertEcho(t, sess, true, "初始装配已包含 echo")

	writePluginDir(t, cfg.Workspace, "loud-provider", strings.Replace(helloEchoManifest, "hello-echo", "loud-provider", 1), loudProviderSource)
	_, err := sess.ReloadPlugins(ctx, "")
	if err == nil {
		t.Fatal("第三方注册 provider 应该让重载失败")
	}
	msg := err.Error()
	if !strings.Contains(msg, "loud-provider") {
		t.Fatalf("报错要点名插件: %v", err)
	}
	if !strings.Contains(msg, "provider") {
		t.Fatalf("报错要说明原因（不能注册 provider）: %v", err)
	}
	assertEcho(t, sess, true, "失败回滚：旧代保留")

	// 插件目录存在时就启动：隔离坏插件，Core 照常起来，状态 failed + 可读错误。
	sess2Cfg := testConfig(t, "")
	writePluginDir(t, sess2Cfg.Workspace, "loud-provider", strings.Replace(helloEchoManifest, "hello-echo", "loud-provider", 1), loudProviderSource)
	sess2 := openSession(t, sess2Cfg)
	for _, v := range sess2.PluginViews(ctx) {
		if v.ID == "loud-provider" && (v.Status != "failed" || v.LastError == "") {
			t.Fatalf("被隔离插件的状态: %+v", v)
		}
	}
}

// ---- 在途轮次保护 ----

// gatedModel 是一个等信号才放行的模型：用来制造「轮次在途」的窗口。
type gatedModel struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedModel() *gatedModel {
	return &gatedModel{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedModel) Stream(ctx context.Context, req ports.Request) (ports.Stream, error) {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &gatedStream{}, nil
}

type gatedStream struct{ sent bool }

func (s *gatedStream) Recv() (ports.Delta, error) {
	if s.sent {
		return ports.Delta{}, io.EOF
	}
	s.sent = true
	return ports.Delta{Kind: ports.DeltaFinish, FinishReason: "stop"}, nil
}

func (s *gatedStream) Close() error { return nil }

// TestReloadWaitsForInflightTurn：热重载必须等在途轮次结束再切旧代
// （上限 30s），不能杀掉在途的模型调用。
func TestReloadWaitsForInflightTurn(t *testing.T) {
	cfg := testConfig(t, "")
	writePluginDir(t, cfg.Workspace, "hello-echo", helloEchoManifest, helloEchoSource)
	cfg.ModelClient = newGatedModel()
	cfg.Fake = false
	ctx := context.Background()
	sess, err := cli.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	assertEcho(t, sess, true, "初始装配已包含 echo")

	th, err := sess.NewThread(ctx, "in-flight")
	if err != nil {
		t.Fatal(err)
	}
	runID, err := sess.NewRunID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	model := cfg.ModelClient.(*gatedModel)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := sess.RunInThreadReady(ctx, th.ID, runID, "阻塞中", ready)
		done <- err
	}()
	<-ready         // 轮次已登记活跃
	<-model.entered // 模型调用已在途
	reloadDone := make(chan error, 1)
	go func() {
		_, err := sess.ReloadPlugins(ctx, "")
		reloadDone <- err
	}()
	select {
	case err := <-reloadDone:
		t.Fatalf("重载没有等待在途轮次就返回了: %v", err)
	case <-time.After(300 * time.Millisecond):
		// 期望行为：重载仍阻塞等待。
	}
	close(model.release) // 放行轮次
	if err := <-done; err != nil {
		t.Fatalf("被阻塞的轮次: %v", err)
	}
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("轮次结束后的重载: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("轮次结束后重载没有完成")
	}
	assertEcho(t, sess, true, "重载后 echo 仍在")
}

// TestReloadUnknownPlugin：重载不存在的插件返回明确错误。
func TestReloadUnknownPlugin(t *testing.T) {
	cfg := testConfig(t, "")
	sess := openSession(t, cfg)
	if _, err := sess.ReloadPlugins(context.Background(), "no-such-plugin"); err == nil {
		t.Fatal("重载不存在的插件应该报错")
	}
}
