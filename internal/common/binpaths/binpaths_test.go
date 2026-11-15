package binpaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
