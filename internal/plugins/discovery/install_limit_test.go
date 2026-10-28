package discovery

// zip 安装上限的边界测试：用小阈值跑「超限拒绝」路径，不真造 20MB 的包。

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallZipRejectsOversizeFile(t *testing.T) {
	restore := shrinkZipLimits(64, MaxZipEntries, MaxUnpackBytes)
	defer restore()
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "big.zip")
	zipFiles(t, zipPath, map[string]string{
		"hello-echo/plugin.json": sampleManifest,
		"hello-echo/index.ts":    sampleSource,
	})
	fi, err := os.Stat(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= 64 {
		t.Fatalf("测试 zip 本身只有 %d 字节，没到阈值", fi.Size())
	}
	if _, err := InstallZip(zipPath, ws); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超限 zip 没有被拒绝: %v", err)
	}
}

func TestInstallZipRejectsTooManyEntries(t *testing.T) {
	restore := shrinkZipLimits(MaxZipBytes, 2, MaxUnpackBytes)
	defer restore()
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "many.zip")
	files := map[string]string{
		"hello-echo/plugin.json": sampleManifest,
		"hello-echo/index.ts":    sampleSource,
		"hello-echo/extra.js":    "export const x = 1;\n",
	}
	zipFiles(t, zipPath, files)
	if _, err := InstallZip(zipPath, ws); err == nil || !strings.Contains(err.Error(), "条目") {
		t.Fatalf("超条目 zip 没有被拒绝: %v", err)
	}
}

func TestInstallZipRejectsUnpackBudget(t *testing.T) {
	restore := shrinkZipLimits(MaxZipBytes, MaxZipEntries, 512)
	defer restore()
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "fat.zip")
	zipFiles(t, zipPath, map[string]string{
		"hello-echo/plugin.json": sampleManifest,
		"hello-echo/index.ts":    strings.Repeat("export const x = 1;\n", 200),
	})
	if _, err := InstallZip(zipPath, ws); err == nil || !strings.Contains(err.Error(), "解压总量") {
		t.Fatalf("超解压预算没有被拒绝: %v", err)
	}
	// 失败不留残留。
	if entries, _ := os.ReadDir(WorkspaceRoot(ws)); len(entries) != 0 {
		t.Fatalf("失败安装留下残留: %d 项", len(entries))
	}
}

// TestInstallZipSymlinkRejected：符号链接条目一律拒绝（路径逃逸的另一个入口）。
func TestInstallZipSymlinkRejected(t *testing.T) {
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "link.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "hello-echo/link.ts", Method: zip.Deflate}
	h.SetMode(0o777 | os.ModeSymlink)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("/etc/passwd")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallZip(zipPath, ws); err == nil {
		t.Fatal("符号链接条目没有被拒绝")
	}
}

// TestInstallZipBackslashEntries：PowerShell 等工具写出的反斜杠分隔条目名
// 归一成 / 后照常安装，且 .. 穿越检查仍然生效。
func TestInstallZipBackslashEntries(t *testing.T) {
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "ps.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range []struct{ name, body string }{
		{`hello-echo\plugin.json`, sampleManifest},
		{`hello-echo\index.ts`, sampleSource},
	} {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := InstallZip(zipPath, ws)
	if err != nil || id != "hello-echo" {
		t.Fatalf("反斜杠条目应归一后安装: %q / %v", id, err)
	}

	// 归一不放过穿越：..\.. 依然拒绝。
	zipPath2 := filepath.Join(t.TempDir(), "ps-evil.zip")
	f2, err := os.Create(zipPath2)
	if err != nil {
		t.Fatal(err)
	}
	zw2 := zip.NewWriter(f2)
	w2, err := zw2.Create(`hello-echo\..\..\evil.ts`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Write([]byte("pwned")); err != nil {
		t.Fatal(err)
	}
	if err := zw2.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f2.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallZip(zipPath2, ws); err == nil {
		t.Fatal("反斜杠形式的 .. 没有被拒绝")
	}
}
