package binpaths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func programName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func writeProgram(t *testing.T, dir, base string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, programName(base))
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDirsOrder(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	dirs, err := Dirs(ws)
	if err != nil {
		t.Fatalf("Dirs: %v", err)
	}
	if len(dirs) != 2 {
		t.Fatalf("dirs = %+v", dirs)
	}
	if dirs[0].Scope != "workspace" || dirs[1].Scope != "global" {
		t.Fatalf("scope 顺序不符: %+v", dirs)
	}
	if got, want := dirs[0].Path, filepath.Join(ws, ".adago", "bin"); got != want {
		t.Fatalf("workspace dir = %q，期望 %q", got, want)
	}
	if got, want := dirs[1].Path, filepath.Join(home, "bin"); got != want {
		t.Fatalf("global dir = %q，期望 %q", got, want)
	}
}

func TestPrependPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	dirs, err := Dirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathListSeparator)
	base := strings.Join([]string{"/usr/bin", dirs[1].Path}, sep)
	got := PrependPath(base, dirs)
	parts := strings.Split(got, sep)
	if len(parts) != 3 {
		t.Fatalf("应去重后保留 3 条: %v", parts)
	}
	if parts[0] != dirs[0].Path || parts[1] != dirs[1].Path || parts[2] != "/usr/bin" {
		t.Fatalf("PATH 顺序不符: %v", parts)
	}
	if got := PrependPath("", dirs); got != strings.Join([]string{dirs[0].Path, dirs[1].Path}, sep) {
		t.Fatalf("空 PATH: %q", got)
	}
}

func TestList(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	dirs, err := Dirs(ws)
	if err != nil {
		t.Fatal(err)
	}
	writeProgram(t, dirs[0].Path, "alpha")
	writeProgram(t, dirs[1].Path, "beta")
	// 非可执行文件与子目录都不算服务程序。
	if err := os.WriteFile(filepath.Join(dirs[0].Path, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dirs[0].Path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	programs, warnings := List(dirs, 0)
	if len(warnings) != 0 {
		t.Fatalf("不应有警告: %v", warnings)
	}
	if len(programs) != 2 {
		t.Fatalf("programs = %+v", programs)
	}
	if programs[0].Scope != "workspace" || programs[0].Name != programName("alpha") {
		t.Fatalf("工作区程序应在前面: %+v", programs[0])
	}
	if programs[1].Scope != "global" || programs[1].Name != programName("beta") {
		t.Fatalf("用户级程序: %+v", programs[1])
	}
	if programs[0].Size <= 0 || programs[0].Modified == "" || programs[0].Path == "" {
		t.Fatalf("程序元数据不完整: %+v", programs[0])
	}

	// 目录不存在不报错。
	missing, warnings := List([]Dir{{Scope: "workspace", Path: filepath.Join(t.TempDir(), "nope")}}, 0)
	if len(missing) != 0 || len(warnings) != 0 {
		t.Fatalf("缺失目录: %v %v", missing, warnings)
	}

	// 上限与警告。
	for i := 0; i < 3; i++ {
		writeProgram(t, dirs[0].Path, "extra"+string(rune('a'+i)))
	}
	limited, warnings := List(dirs, 2)
	if len(limited) != 2 {
		t.Fatalf("上限未生效: %+v", limited)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "上限") {
		t.Fatalf("超限应有警告: %v", warnings)
	}
}

func TestIsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		for _, name := range []string{"a.exe", "a.CMD", "a.bat", "a.ps1", "a.com"} {
			if !IsExecutable(name, 0) {
				t.Fatalf("%q 应是可执行文件", name)
			}
		}
		for _, name := range []string{"a.txt", "a.dll", "a"} {
			if IsExecutable(name, 0) {
				t.Fatalf("%q 不应算可执行文件", name)
			}
		}
		return
	}
	if !IsExecutable("a", 0o755) {
		t.Fatal("带可执行位的文件应被识别")
	}
	if IsExecutable("a", 0o644) {
		t.Fatal("普通文件不应被识别")
	}
}
