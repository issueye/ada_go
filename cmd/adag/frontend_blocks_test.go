package main

// 第三方 block 装配：工作区优先、官方同 id 遮蔽、坏块只影响自己。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/agent_core/plugins/host"
	"github.com/adag/ada-go/internal/common/pluginapi"
)

func writePluginDir(t *testing.T, root, id string) string {
	t.Helper()
	return writePluginFiles(t, root, id, `"trust":"third-party","backend":{"entry":"index.ts","capabilities":[]},`)
}

// writeBlockOnlyPluginDir 模拟纯前端的 block-only 包：没有 backend（Core 不加载），
// 只在安装目录里贡献 frontend.blocks。
func writeBlockOnlyPluginDir(t *testing.T, root, id string) string {
	t.Helper()
	return writePluginFiles(t, root, id, `"trust":"third-party",`)
}

func writePluginFiles(t *testing.T, root, id, manifestExtra string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	files := map[string]string{
		"plugin.json": `{"id":"` + id + `","name":"` + id + `","version":"0.1.0","apiVersion":1,` +
			manifestExtra +
			`"frontend":{"blocks":[{"name":"chart","schema":"schemas/chart.json","template":"blocks/chart.tsx"}]}}`,
		"schemas/chart.json": `{"type":"object"}`,
		"blocks/chart.tsx":   `export default function () { return <div>chart</div>; }`,
	}
	if strings.Contains(manifestExtra, `"backend"`) {
		files["index.ts"] = `export default function register() {}`
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestLoadBlockScriptsFromRootsPriorityAndShadowing(t *testing.T) {
	ws, global := t.TempDir(), t.TempDir()
	writePluginDir(t, ws, "chart-plugin")
	writePluginDir(t, ws, "github-trending")
	writePluginDir(t, global, "chart-plugin") // 工作区优先，全局这份被忽略
	writePluginDir(t, global, "global-only")

	official := []host.Script{{Manifest: &pluginapi.Manifest{ID: "github-trending"}}}
	got := loadBlockScriptsFromRoots([]string{ws, global}, official)

	ids := map[string]bool{}
	for _, sc := range got {
		ids[sc.Manifest.ID] = true
	}
	if len(got) != 2 || !ids["chart-plugin"] || !ids["global-only"] {
		t.Fatalf("加载结果 = %v", ids)
	}
}

func TestLoadBlockScriptsFromRootsSkipsBadBlocks(t *testing.T) {
	root := t.TempDir()
	writePluginDir(t, root, "good")
	bad := writePluginDir(t, root, "bad")
	if err := os.WriteFile(filepath.Join(bad, "schemas", "chart.json"),
		[]byte(`{"type":"object","unknownKeyword":1}`), 0o644); err != nil {
		t.Fatalf("write bad schema: %v", err)
	}

	got := loadBlockScriptsFromRoots([]string{root}, nil)
	if len(got) != 1 || got[0].Manifest.ID != "good" {
		t.Fatalf("坏块应只跳过自己: %+v", got)
	}
}

// TestLoadBlockScriptsFromRootsBlockOnly 钉住纯前端 block 包：
// 没有 backend 的插件也要被动态块装配发现（Core 不加载它，但 Gateway 需要渲染它的块）。
func TestLoadBlockScriptsFromRootsBlockOnly(t *testing.T) {
	root := t.TempDir()
	writeBlockOnlyPluginDir(t, root, "pure-blocks")
	// 插件根里的普通目录（不是插件）应被静默跳过。
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := loadBlockScriptsFromRoots([]string{root}, nil)
	if len(got) != 1 || got[0].Manifest.ID != "pure-blocks" {
		t.Fatalf("block-only 插件应被发现: %+v", got)
	}
	if got[0].Manifest.Backend != nil {
		t.Fatal("block-only 插件不应出现 backend 段")
	}
}

// TestLoadBlockScriptsFromRootsSkipsNonPluginJSON 钉住坏清单只跳过自己。
func TestLoadBlockScriptsFromRootsSkipsNonPluginJSON(t *testing.T) {
	root := t.TempDir()
	writeBlockOnlyPluginDir(t, root, "good")
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "plugin.json"), []byte(`{"id":"broken"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := loadBlockScriptsFromRoots([]string{root}, nil)
	if len(got) != 1 || got[0].Manifest.ID != "good" {
		t.Fatalf("坏清单应只跳过自己: %+v", got)
	}
}
