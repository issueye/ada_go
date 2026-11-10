package cli_test

// 插件启停状态的存储分层（plugin_state.go）：
//   - 全局 ~/.adago/plugins.json：跨工作区共享的用户决定（默认写入目标）；
//   - 工作区 <workspace>/.adago/plugins.json：仅在「该工作区有单独启停」时存在，
//     条目覆盖全局；
//   - 旧版写在 plugin_meta（SQLite）的用户选择在打开会话时一次性迁移成工作区文件。

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/app"
	"github.com/adag/ada-go/internal/agent_core/cli"
	"github.com/adag/ada-go/internal/common/adapters/sqlite"
	"github.com/adag/ada-go/plugins/official"
)

// openWithPluginState 打开一个会话：globalPath 非空时作为全局启停状态
// （plugins.json）的位置；空串走默认（ADAGO_HOME / 用户主目录）。
func openWithPluginState(t *testing.T, workspace, dataDir, globalPath string) *cli.Session {
	t.Helper()
	scripts, err := official.Scripts()
	if err != nil {
		t.Fatalf("official.Scripts: %v", err)
	}
	sess, err := cli.Open(context.Background(), cli.Config{
		Workspace: workspace, DataDir: dataDir, Product: productPath(t),
		Scripts: scripts, Assembler: app.Assembler{}, Fake: true, In: strings.NewReader(""), Out: io.Discard,
		PluginStatePath: globalPath,
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

func readPluginState(t *testing.T, path string) map[string]struct {
	Enabled bool `json:"enabled"`
} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	var data struct {
		Plugins map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("plugins.json 不是合法 JSON: %v", err)
	}
	return data.Plugins
}

// TestPluginEnabledStateGlobalByDefault：启停默认写全局 plugins.json，
// 另一个工作区（自己的 SQLite）读到同一个决定。
func TestPluginEnabledStateGlobalByDefault(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	globalState := filepath.Join(home, ".adago", "plugins.json")

	wsA, dataA := t.TempDir(), t.TempDir()
	a := openWithPluginState(t, wsA, dataA, "")
	if _, err := a.SetPluginEnabled(ctx, "tools-git", false); err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	entries := readPluginState(t, globalState)
	if e, ok := entries["tools-git"]; !ok || e.Enabled {
		t.Fatalf("全局状态缺少 tools-git=false: %v", entries)
	}
	// 工作区覆盖文件不应被创建（没有单独启停）。
	if _, err := os.Stat(workspaceOverridePathFor(t, wsA)); !os.IsNotExist(err) {
		t.Fatalf("默认启停不应写工作区覆盖文件: %v", err)
	}

	// 工作区 B（独立 SQLite、独立工作区）看到同一个全局决定。
	wsB, dataB := t.TempDir(), t.TempDir()
	b := openWithPluginState(t, wsB, dataB, "")
	if _, err := b.SetPluginEnabled(ctx, "tools-git", true); err != nil {
		t.Fatalf("工作区 B SetPluginEnabled(true): %v", err)
	}
	entries = readPluginState(t, globalState)
	if e, ok := entries["tools-git"]; !ok || !e.Enabled {
		t.Fatalf("全局状态缺少 tools-git=true: %v", entries)
	}
}

// TestPluginEnabledWorkspaceOverride：工作区已有覆盖文件时，启停写工作区并覆盖全局。
func TestPluginEnabledWorkspaceOverride(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	globalState := filepath.Join(home, ".adago", "plugins.json")

	ws, data := t.TempDir(), t.TempDir()
	overridePath := filepath.Join(ws, ".adago", "plugins.json")
	if err := os.MkdirAll(filepath.Dir(overridePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, []byte(`{"version":1,"plugins":{"tools-git":{"enabled":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	sess := openWithPluginState(t, ws, data, "")
	if _, err := sess.SetPluginEnabled(ctx, "tools-git", false); err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	// 覆盖文件被更新；全局文件不受影响。
	entries := readPluginState(t, overridePath)
	if e, ok := entries["tools-git"]; !ok || e.Enabled {
		t.Fatalf("工作区覆盖缺少 tools-git=false: %v", entries)
	}
	if _, err := os.Stat(globalState); !os.IsNotExist(err) {
		t.Fatalf("有覆盖文件时不应写全局: %v", err)
	}
	// 会话内视图与 offline 读取都遵循覆盖。
	if v, ok := sess.PluginViewDetail(ctx, "tools-git"); !ok || v.Enabled {
		t.Fatalf("工作区覆盖未生效: %+v", v)
	}
}

// TestPluginEnabledLegacyWorkspaceOverride：旧工作区目录 .adag/plugins.json
// 只读兼容；第一次切换时把条目物化到 .adago/plugins.json，旧文件保持原样。
func TestPluginEnabledLegacyWorkspaceOverride(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	ws, data := t.TempDir(), t.TempDir()
	legacyPath := filepath.Join(ws, ".adag", "plugins.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"plugins":{"tools-git":{"enabled":true},"token-usage":{"enabled":false}}}`
	if err := os.WriteFile(legacyPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	sess := openWithPluginState(t, ws, data, "")
	if v, ok := sess.PluginViewDetail(ctx, "token-usage"); !ok || v.Enabled {
		t.Fatalf("旧工作区覆盖未生效（token-usage）: %+v", v)
	}
	if v, ok := sess.PluginViewDetail(ctx, "tools-git"); !ok || !v.Enabled {
		t.Fatalf("旧工作区覆盖未生效（tools-git）: %+v", v)
	}

	if _, err := sess.SetPluginEnabled(ctx, "tools-git", false); err != nil {
		t.Fatalf("SetPluginEnabled(false): %v", err)
	}
	wsPath := filepath.Join(ws, ".adago", "plugins.json")
	entries := readPluginState(t, wsPath)
	if e, ok := entries["tools-git"]; !ok || e.Enabled {
		t.Fatalf("新工作区文件缺少 tools-git=false: %v", entries)
	}
	if e, ok := entries["token-usage"]; !ok || e.Enabled {
		t.Fatalf("物化时应保留 token-usage=false: %v", entries)
	}
	old := readPluginState(t, legacyPath)
	if e, ok := old["tools-git"]; !ok || !e.Enabled {
		t.Fatalf("旧文件不应被改写: %v", old)
	}
}

// TestPluginStateMigratedFromSQLite：旧版按工作区写在 plugin_meta 的用户启停
// 在打开会话时一次性迁移成 <workspace>/.adago/plugins.json。
func TestPluginStateMigratedFromSQLite(t *testing.T) {
	ctx := context.Background()
	ws, data := t.TempDir(), t.TempDir()

	// 伪造旧版状态：plugin_meta 里 tools-git 被用户显式停用。
	store, err := sqlite.OpenCore(ctx, sqlite.Config{Workspace: ws, DataDir: data})
	if err != nil {
		t.Fatalf("sqlite.OpenCore: %v", err)
	}
	if err := store.UpsertPluginMeta(ctx, sqlite.PluginMetaRecord{
		ID: "tools-git", Version: "0.1.0", Enabled: false, UserSet: true, Fingerprint: "legacy",
	}); err != nil {
		t.Fatalf("UpsertPluginMeta: %v", err)
	}
	if err := store.Close(ctx); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sess := openWithPluginState(t, ws, data, "")

	overridePath := filepath.Join(ws, ".adago", "plugins.json")
	entries := readPluginState(t, overridePath)
	if e, ok := entries["tools-git"]; !ok || e.Enabled {
		t.Fatalf("迁移后的工作区覆盖缺少 tools-git=false: %v", entries)
	}
	if v, ok := sess.PluginViewDetail(ctx, "tools-git"); !ok || v.Enabled {
		t.Fatalf("迁移后的停用状态未生效: %+v %v", v, err)
	}
}

// workspaceOverridePathFor 供断言用：工作区覆盖文件的路径。
func workspaceOverridePathFor(t *testing.T, workspace string) string {
	t.Helper()
	return filepath.Join(workspace, ".adago", "plugins.json")
}
