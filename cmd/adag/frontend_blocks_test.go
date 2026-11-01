package main

// 第三方 block 装配：工作区优先、官方同 id 遮蔽、坏块只影响自己。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
)

func writePluginDir(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	files := map[string]string{
		"plugin.json": `{"id":"` + id + `","name":"` + id + `","version":"0.1.0","apiVersion":1,` +
			`"trust":"third-party","backend":{"entry":"index.ts","capabilities":[]},` +
			`"frontend":{"blocks":[{"name":"chart","schema":"schemas/chart.json","template":"blocks/chart.tsx"}]}}`,
		"index.ts":           `export default function register() {}`,
		"schemas/chart.json": `{"type":"object"}`,
		"blocks/chart.tsx":   `export default function () { return <div>chart</div>; }`,
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

	official := []host.Script{{Manifest: &plugins.Manifest{ID: "github-trending"}}}
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
