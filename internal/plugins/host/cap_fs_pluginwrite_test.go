package host

// 写插件目录的边界：只允许已安装插件的目录内部，符号链接不得逃逸。

import (
	"os"
	"path/filepath"
	"testing"
)

func makePluginRoot(t *testing.T) (root, demo, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "plugins")
	demo = filepath.Join(root, "demo")
	outside = filepath.Join(base, "outside")
	for _, dir := range []string{demo, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(demo, "plugin.json"), []byte(`{"id":"demo"}`), 0o644); err != nil {
		t.Fatalf("write plugin.json: %v", err)
	}
	return root, demo, outside
}

func TestAllowPluginWrite(t *testing.T) {
	root, demo, outside := makePluginRoot(t)
	roots := []string{root}

	if got, err := allowPluginWrite(roots, filepath.Join(demo, "blocks", "card.tsx")); err != nil || got == "" {
		t.Fatalf("插件内新文件应允许: %v", err)
	}
	if _, err := allowPluginWrite(roots, filepath.Join(demo, "plugin.json")); err != nil {
		t.Fatalf("插件清单应允许编辑: %v", err)
	}

	for name, target := range map[string]string{
		"插件根本身":   root,
		"未安装插件":   filepath.Join(root, "missing", "a.tsx"),
		"非插件目录":   filepath.Join(outside, "a.tsx"),
		"目录穿越":    filepath.Join(demo, "..", "evil", "a.tsx"),
		"插件外绝对路径": outside,
	} {
		if _, err := allowPluginWrite(roots, target); err == nil {
			t.Fatalf("%s 应被拒绝: %s", name, target)
		}
	}
}

func TestAllowPluginWriteRejectsSymlinkEscape(t *testing.T) {
	root, demo, outside := makePluginRoot(t)
	roots := []string{root}

	if err := os.Symlink(outside, filepath.Join(demo, "escape")); err != nil {
		t.Skipf("当前环境不支持创建符号链接: %v", err)
	}
	if _, err := allowPluginWrite(roots, filepath.Join(demo, "escape", "a.tsx")); err == nil {
		t.Fatal("目录符号链接逃逸应被拒绝")
	}

	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(demo, "link.tsx")); err != nil {
		t.Skipf("当前环境不支持创建符号链接: %v", err)
	}
	if _, err := allowPluginWrite(roots, filepath.Join(demo, "link.tsx")); err == nil {
		t.Fatal("文件符号链接逃逸应被拒绝")
	}
}

func TestWritePathWorkspaceAndPlugin(t *testing.T) {
	root, demo, outside := makePluginRoot(t)
	ws := t.TempDir()
	g := &fsGuard{root: ws, mount: "/workspace", pluginRoots: []string{root}}

	real, external, err := g.writePath("/workspace/a.txt")
	if err != nil || external || real != filepath.Join(ws, "a.txt") {
		t.Fatalf("工作区写入 = %q/%v/%v", real, external, err)
	}

	real, external, err = g.writePath(filepath.Join(demo, "blocks", "a.tsx"))
	if err != nil || !external || real == "" {
		t.Fatalf("插件写入 = %q/%v/%v", real, external, err)
	}

	if _, _, err := g.writePath(filepath.Join(outside, "a.tsx")); err == nil {
		t.Fatal("插件根之外的绝对路径应被拒绝")
	}
}
