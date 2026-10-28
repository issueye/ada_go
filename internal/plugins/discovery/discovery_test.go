package discovery

// 发现层的单元测试：正常加载、坏 manifest 拒绝、id 与目录名一致性、
// 冲突优先级、指纹稳定性与 zip 安装的各种非法输入。

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleManifest = `{
  "id": "hello-echo",
  "name": "Hello Echo",
  "version": "0.1.0",
  "apiVersion": 1,
  "trust": "third-party",
  "backend": { "entry": "index.ts", "capabilities": [] }
}`

const sampleSource = `export default function (agent) {
  agent.registerTool({ name: "echo", description: "回显", parameters: { type: "object" }, access: "read",
    execute: async (args) => ({ content: [{ type: "text", text: String(args.text || "") }] }) });
}
`

func writePlugin(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"plugin.json":  strings.ReplaceAll(sampleManifest, `"hello-echo"`, `"`+id+`"`),
		"index.ts":     sampleSource,
		"lib/util.js":  "export const helper = () => 1;\n",
		"static/a.css": "body { color: red; }\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDirLoadsValidPlugins(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "hello-echo")

	loaded, warnings, err := LoadDirDetailed(root, SourceWorkspace)
	if err != nil {
		t.Fatalf("LoadDirDetailed: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded = %d 个插件，期望 1", len(loaded))
	}
	p := loaded[0]
	m := p.Script.Manifest
	if m == nil || m.ID != "hello-echo" || string(m.Trust) != "third-party" {
		t.Fatalf("manifest 不对: %+v", m)
	}
	if p.Source != SourceWorkspace {
		t.Fatalf("source = %q", p.Source)
	}
	if p.Script.EntryName != "plugin://hello-echo/index.ts" {
		t.Fatalf("entryName = %q", p.Script.EntryName)
	}
	if strings.TrimSpace(p.Script.Source) == "" {
		t.Fatal("入口源码为空")
	}
	// 随包模块与静态资源分桶。
	if _, ok := p.Script.Bundle["lib/util.js"]; !ok {
		t.Fatalf("Bundle 缺少 lib/util.js: %v", keysOf(p.Script.Bundle))
	}
	if _, ok := p.Script.StaticAssets["static/a.css"]; !ok {
		t.Fatalf("StaticAssets 缺少 static/a.css")
	}
	if p.Fingerprint == "" {
		t.Fatal("指纹为空")
	}
	// LoadDir 包装返回同一批脚本。
	scripts, w2, err := LoadDir(root)
	if err != nil || len(w2) != 0 || len(scripts) != 1 {
		t.Fatalf("LoadDir = %d scripts / %v / %v", len(scripts), w2, err)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLoadDirRejectsBadManifest(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "hello-echo")
	bad := strings.Replace(sampleManifest, `"apiVersion": 1`, `"apiVersion": 9`, 1)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := LoadDirDetailed(root, SourceWorkspace)
	if err != nil {
		t.Fatalf("LoadDirDetailed: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("坏清单被加载了: %+v", loaded)
	}
	if len(warnings) != 1 || warnings[0].PluginID != "hello-echo" {
		t.Fatalf("warnings = %+v", warnings)
	}
	if !strings.Contains(warnings[0].Message, "apiVersion") {
		t.Fatalf("警告要点名原因: %+v", warnings[0])
	}
}

func TestLoadDirRequiresIDEqualsDirName(t *testing.T) {
	root := t.TempDir()
	// 目录名与 id 不一致：拒绝（6.2）。
	writePlugin(t, root, "hello-echo")
	if err := os.Rename(filepath.Join(root, "hello-echo"), filepath.Join(root, "wrong-name")); err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := LoadDirDetailed(root, "")
	if err != nil || len(loaded) != 0 {
		t.Fatalf("loaded = %+v / %v", loaded, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "目录名") {
		t.Fatalf("warnings = %+v", warnings)
	}
}

func TestLoadDirMissingDirIsEmpty(t *testing.T) {
	loaded, warnings, err := LoadDirDetailed(filepath.Join(t.TempDir(), "nope"), SourceWorkspace)
	if err != nil || len(loaded) != 0 || len(warnings) != 0 {
		t.Fatalf("不存在的目录应视为空: %+v / %v / %v", loaded, warnings, err)
	}
}

func TestLoadDirSkipsDotAndPlainFiles(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "hello-echo")
	// 安装中间态与散落文件不是插件。
	if err := os.MkdirAll(filepath.Join(root, ".install-tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := LoadDirDetailed(root, "")
	if err != nil || len(loaded) != 1 || len(warnings) != 0 {
		t.Fatalf("loaded=%d warnings=%+v err=%v", len(loaded), warnings, err)
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "hello-echo")
	fp1, err := FingerprintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// mtime 变化不影响指纹。
	if err := os.Chtimes(filepath.Join(dir, "index.ts"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	fp2, err := FingerprintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("指纹随 mtime 变化: %s vs %s", fp1, fp2)
	}
	// 内容变化必须改变指纹。
	if err := os.WriteFile(filepath.Join(dir, "index.ts"), []byte(sampleSource+"\n// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fp3, err := FingerprintDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fp3 == fp1 {
		t.Fatal("内容变化后指纹不变")
	}
}

func TestMergeWorkspacePriority(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "hello-echo")
	ws, _, err := LoadDirDetailed(root, SourceWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	// 全局里同 id 的插件（内容不同目录）应被工作区覆盖并记警告。
	globalRoot := t.TempDir()
	writePlugin(t, globalRoot, "hello-echo")
	writePlugin(t, globalRoot, "other-plugin")
	gl, _, err := LoadDirDetailed(globalRoot, SourceGlobal)
	if err != nil {
		t.Fatal(err)
	}
	merged, warnings := MergeByPriority(ws, gl)
	if len(merged) != 2 {
		t.Fatalf("merged = %d 个插件，期望 2（hello-echo + other-plugin）", len(merged))
	}
	if merged[0].ID() != "hello-echo" || merged[0].Source != SourceWorkspace {
		t.Fatalf("工作区优先没有生效: %+v", merged[0])
	}
	if merged[1].ID() != "other-plugin" || merged[1].Source != SourceGlobal {
		t.Fatalf("全局插件没有进结果: %+v", merged[1])
	}
	conflicts := 0
	for _, w := range warnings {
		if w.PluginID == "hello-echo" {
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("冲突警告 = %d 条，期望 1: %+v", conflicts, warnings)
	}
}

// ---- zip 安装 ----

func zipFiles(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func TestInstallZipTopDirLayout(t *testing.T) {
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "hello-echo.zip")
	zipFiles(t, zipPath, map[string]string{
		"hello-echo/plugin.json": sampleManifest,
		"hello-echo/index.ts":    sampleSource,
		"README.md":              "散落在 zip 根的无关文件",
	})
	id, err := InstallZip(zipPath, ws)
	if err != nil {
		t.Fatalf("InstallZip: %v", err)
	}
	if id != "hello-echo" {
		t.Fatalf("id = %q", id)
	}
	target := filepath.Join(WorkspaceRoot(ws), "hello-echo")
	if _, err := os.Stat(filepath.Join(target, "index.ts")); err != nil {
		t.Fatalf("入口文件没有落位: %v", err)
	}
	// 落位后的目录能被 LoadDirDetailed 识别。
	loaded, _, err := LoadDirDetailed(WorkspaceRoot(ws), SourceWorkspace)
	if err != nil || len(loaded) != 1 || loaded[0].ID() != "hello-echo" {
		t.Fatalf("安装后无法发现: %d / %v", len(loaded), err)
	}
}

func TestInstallZipRootLayout(t *testing.T) {
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "flat.zip")
	zipFiles(t, zipPath, map[string]string{
		"plugin.json": sampleManifest,
		"index.ts":    sampleSource,
	})
	id, err := InstallZip(zipPath, ws)
	if err != nil || id != "hello-echo" {
		t.Fatalf("InstallZip = %q / %v", id, err)
	}
}

func TestInstallZipRejectsTraversal(t *testing.T) {
	ws := t.TempDir()
	for _, name := range []string{"../evil.ts", "/abs/evil.ts", `sub\..\evil.ts`, "C:evil.ts"} {
		zipPath := filepath.Join(t.TempDir(), "bad.zip")
		zipFiles(t, zipPath, map[string]string{
			"hello-echo/plugin.json": sampleManifest,
			name:                     "pwned",
		})
		if _, err := InstallZip(zipPath, ws); err == nil {
			t.Fatalf("条目 %q 没有被拒绝", name)
		}
	}
}

func TestInstallZipRejectsBadLayout(t *testing.T) {
	ws := t.TempDir()
	// 没有清单。
	zipPath := filepath.Join(t.TempDir(), "no-manifest.zip")
	zipFiles(t, zipPath, map[string]string{"hello-echo/index.ts": sampleSource})
	if _, err := InstallZip(zipPath, ws); err == nil {
		t.Fatal("没有 plugin.json 的 zip 被安装了")
	}
	// 多个顶层目录。
	zipPath2 := filepath.Join(t.TempDir(), "multi.zip")
	zipFiles(t, zipPath2, map[string]string{
		"a/plugin.json": sampleManifest,
		"b/plugin.json": sampleManifest,
	})
	if _, err := InstallZip(zipPath2, ws); err == nil {
		t.Fatal("多顶层目录的 zip 被安装了")
	}
}

func TestInstallZipNoOverwrite(t *testing.T) {
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "hello-echo.zip")
	zipFiles(t, zipPath, map[string]string{
		"hello-echo/plugin.json": sampleManifest,
		"hello-echo/index.ts":    sampleSource,
	})
	if _, err := InstallZip(zipPath, ws); err != nil {
		t.Fatalf("第一次安装失败: %v", err)
	}
	target := filepath.Join(WorkspaceRoot(ws), "hello-echo", "index.ts")
	if err := os.WriteFile(target, []byte("// 已被用户修改\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallZip(zipPath, ws); err == nil {
		t.Fatal("同名 id 被覆盖了")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "// 已被用户修改\n" {
		t.Fatalf("旧文件被改动: %q / %v", content, err)
	}
}

func TestInstallZipRejectsBadManifest(t *testing.T) {
	ws := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "bad-manifest.zip")
	zipFiles(t, zipPath, map[string]string{
		"hello-echo/plugin.json": strings.Replace(sampleManifest, `"version": "0.1.0"`, `"version": "not-semver"`, 1),
		"hello-echo/index.ts":    sampleSource,
	})
	if _, err := InstallZip(zipPath, ws); err == nil {
		t.Fatal("坏清单被安装了")
	}
	// 失败后不留安装残留。
	entries, _ := os.ReadDir(WorkspaceRoot(ws))
	if len(entries) != 0 {
		t.Fatalf("失败安装留下残留: %d 项", len(entries))
	}
}
