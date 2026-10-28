package main

// adag plugin 命令的离线验收：install 落位、list 表格、reload 发现校验。
// 这些命令只碰发现目录与 zip，不需要运行中的 Core。

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliTestManifest = `{
  "id": "hello-echo",
  "name": "Hello Echo",
  "version": "0.1.0",
  "apiVersion": 1,
  "trust": "third-party",
  "backend": { "entry": "index.ts", "capabilities": [] }
}`

const cliTestSource = `export default function (agent) {
  agent.registerTool({
    name: "echo",
    description: "回显",
    parameters: { type: "object" },
    access: "read",
    execute: async () => ({ content: [{ type: "text", text: "hi" }] }),
  });
}
`

func makeZip(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	zipPath := filepath.Join(dir, "hello-echo.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range files {
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

func TestPluginCommandInstallAndList(t *testing.T) {
	ws := t.TempDir()
	zipPath := makeZip(t, t.TempDir(), map[string]string{
		"hello-echo/plugin.json": cliTestManifest,
		"hello-echo/index.ts":    cliTestSource,
	})

	var installOut strings.Builder
	if err := cmdPlugin([]string{"install", "--zip", zipPath, "--workspace", ws}, &installOut); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	if !strings.Contains(installOut.String(), "hello-echo") {
		t.Fatalf("install 输出要点名插件 id: %q", installOut.String())
	}
	if _, err := os.Stat(filepath.Join(ws, ".adago", "plugins", "hello-echo", "index.ts")); err != nil {
		t.Fatalf("插件没有落位: %v", err)
	}

	var listOut strings.Builder
	if err := cmdPlugin([]string{"list", "--workspace", ws}, &listOut); err != nil {
		t.Fatalf("plugin list: %v", err)
	}
	out := listOut.String()
	for _, want := range []string{"hello-echo", "0.1.0", "third-party", "workspace"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plugin list 缺少 %q:\n%s", want, out)
		}
	}
	// 随包插件也在列表里（trust=builtin、source=builtin）。
	if !strings.Contains(out, "builtin") {
		t.Fatalf("plugin list 缺少随包插件:\n%s", out)
	}
}

func TestPluginCommandReloadOfflineCheck(t *testing.T) {
	ws := t.TempDir()
	zipPath := makeZip(t, t.TempDir(), map[string]string{
		"hello-echo/plugin.json": cliTestManifest,
		"hello-echo/index.ts":    cliTestSource,
	})
	if err := cmdPlugin([]string{"install", "--zip", zipPath, "--workspace", ws}, &strings.Builder{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	var out strings.Builder
	if err := cmdPlugin([]string{"reload", "--workspace", ws, "--plugin", "hello-echo"}, &out); err != nil {
		t.Fatalf("plugin reload: %v", err)
	}
	if !strings.Contains(out.String(), "hello-echo") || !strings.Contains(out.String(), "plugin.reload RPC") {
		t.Fatalf("reload 输出不对:\n%s", out.String())
	}
	// 未知插件要报错。
	if err := cmdPlugin([]string{"reload", "--workspace", ws, "--plugin", "no-such"}, &strings.Builder{}); err == nil {
		t.Fatal("未知插件应该报错")
	}
}

func TestPluginCommandRejectsBrokenZip(t *testing.T) {
	ws := t.TempDir()
	zipPath := makeZip(t, t.TempDir(), map[string]string{
		"hello-echo/index.ts": cliTestSource, // 没有 plugin.json
	})
	var out strings.Builder
	if err := cmdPlugin([]string{"install", "--zip", zipPath, "--workspace", ws}, &out); err == nil {
		t.Fatal("坏 zip 应该被拒绝")
	}
	entries, _ := os.ReadDir(filepath.Join(ws, ".adago", "plugins"))
	if len(entries) != 0 {
		t.Fatalf("失败安装留下残留: %d 项", len(entries))
	}
}
