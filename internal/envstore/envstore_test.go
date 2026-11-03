package envstore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".adago", FileName)
	vars := map[string]string{"GITHUB_TOKEN": "ghp_secret", "EMPTY": ""}
	if err := SaveFile(path, vars); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	got, warns, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("不应有警告: %v", warns)
	}
	if got["GITHUB_TOKEN"] != "ghp_secret" || got["EMPTY"] != "" {
		t.Fatalf("读回内容不符: %v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"version": 1`) || !strings.Contains(text, `"vars"`) {
		t.Fatalf("应写成带 version/vars 的信封: %s", text)
	}
	if strings.Contains(text, "tmp-") {
		t.Fatalf("不应残留临时文件名: %s", text)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("权限 = %o，期望 600", perm)
		}
	}
}

func TestLoadFileMissingIsEmpty(t *testing.T) {
	vars, warns, err := LoadFile(filepath.Join(t.TempDir(), "nope", FileName))
	if err != nil {
		t.Fatalf("缺失文件不应报错: %v", err)
	}
	if len(vars) != 0 || len(warns) != 0 {
		t.Fatalf("缺失文件应返回空: %v %v", vars, warns)
	}
}

func TestLoadFlatForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`{"API_KEY":"x","PORT":"8080"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	vars, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars["API_KEY"] != "x" || vars["PORT"] != "8080" {
		t.Fatalf("扁平形式解析不符: %v", vars)
	}
}

func TestLoadSkipsInvalidEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	body := `{"version":1,"vars":{"GOOD":"1","BAD-KEY":"x","bad key":"y","N":"2"}}`
	// 非字符串值单独一份（污染 vars 会让整个 vars 解析失败）。
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	vars, warns, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars["GOOD"] != "1" || vars["N"] != "2" {
		t.Fatalf("合法变量丢失: %v", vars)
	}
	if _, ok := vars["BAD-KEY"]; ok {
		t.Fatal("非法键应被跳过")
	}
	if len(warns) != 2 {
		t.Fatalf("两个非法键应有警告: %v", warns)
	}
	for _, w := range warns {
		if strings.Contains(w, "1") || strings.Contains(w, "x") {
			// 警告只能出现路径与键名，不能出现值；这里粗查是否有值 1 混入键名之外。
			if strings.Contains(w, `"1"`) || strings.Contains(w, "=1") {
				t.Fatalf("警告泄露变量值: %s", w)
			}
		}
	}
}

func TestLoadForWorkspacePrecedence(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	if err := os.MkdirAll(filepath.Join(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveFile(filepath.Join(home, FileName), map[string]string{"A": "global", "B": "keep"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".adago"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveFile(filepath.Join(ws, ".adago", FileName), map[string]string{"A": "workspace"}); err != nil {
		t.Fatal(err)
	}
	vars, warns := LoadForWorkspace(ws)
	if len(warns) != 0 {
		t.Fatalf("不应有警告: %v", warns)
	}
	if vars["A"] != "workspace" || vars["B"] != "keep" {
		t.Fatalf("合并优先级不符: %v", vars)
	}
}

// TestLoadForWorkspaceLegacyFallback：新目录没有 env.json 时读旧 .adag/env.json，
// 并给出迁移警告；新文件出现后旧文件不再参与合并。
func TestLoadForWorkspaceLegacyFallback(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	if err := SaveFile(filepath.Join(home, FileName), map[string]string{"GLOBAL": "g"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveFile(filepath.Join(ws, ".adag", FileName), map[string]string{"OLD": "1", "GLOBAL": "old"}); err != nil {
		t.Fatal(err)
	}

	vars, warns := LoadForWorkspace(ws)
	if vars["OLD"] != "1" || vars["GLOBAL"] != "old" {
		t.Fatalf("旧工作区变量未生效: %v", vars)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "旧目录") {
		t.Fatalf("应有旧目录警告: %v", warns)
	}

	if err := os.MkdirAll(filepath.Join(ws, ".adago"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveFile(filepath.Join(ws, ".adago", FileName), map[string]string{"NEW": "2"}); err != nil {
		t.Fatal(err)
	}
	vars, warns = LoadForWorkspace(ws)
	if vars["NEW"] != "2" || vars["OLD"] != "" {
		t.Fatalf("新文件应覆盖旧文件: %v", vars)
	}
	if vars["GLOBAL"] != "g" {
		t.Fatalf("全局变量应保留: %v", vars)
	}
	if len(warns) != 0 {
		t.Fatalf("新文件存在时不应有旧目录警告: %v", warns)
	}
}

// TestMigrateWorkspace：第一次写新目录前把旧 .adag/env.json 物化过去，
// 之后 Set/Unset 只改新文件。
func TestMigrateWorkspace(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	if err := os.MkdirAll(filepath.Join(ws, ".adag"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveFile(filepath.Join(ws, ".adag", FileName), map[string]string{"A": "1", "B": "2"}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateWorkspace(ws); err != nil {
		t.Fatalf("MigrateWorkspace: %v", err)
	}
	newPath, err := WorkspacePath(ws)
	if err != nil {
		t.Fatal(err)
	}
	vars, _, err := LoadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if vars["A"] != "1" || vars["B"] != "2" {
		t.Fatalf("迁移后内容不符: %v", vars)
	}
	if _, _, err := Unset(newPath, "A"); err != nil {
		t.Fatalf("Unset: %v", err)
	}
	vars, _, _ = LoadFile(newPath)
	if _, ok := vars["A"]; ok {
		t.Fatalf("A 应被删除: %v", vars)
	}
	if vars["B"] != "2" {
		t.Fatalf("B 应保留: %v", vars)
	}
	// 旧文件保持原样（只读兼容）。
	oldVars, _, _ := LoadFile(filepath.Join(ws, ".adag", FileName))
	if oldVars["A"] != "1" {
		t.Fatalf("旧文件不应被改写: %v", oldVars)
	}
}
func TestSetUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if _, err := Set(path, "A", "1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := Set(path, "B", "2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	vars, _, err := LoadFile(path)
	if err != nil || vars["A"] != "1" || vars["B"] != "2" {
		t.Fatalf("Set 后内容: %v %v", vars, err)
	}
	if _, changed, err := Unset(path, "A"); err != nil || !changed {
		t.Fatalf("Unset: changed=%v err=%v", changed, err)
	}
	if _, changed, err := Unset(path, "MISSING"); err != nil || changed {
		t.Fatalf("删除不存在的键: changed=%v err=%v", changed, err)
	}
	vars, _, _ = LoadFile(path)
	if _, ok := vars["A"]; ok {
		t.Fatal("A 应被删除")
	}
}

func TestValidateAndParse(t *testing.T) {
	for _, bad := range []string{"", "1ABC", "BAD-KEY", "A B", "A=B", "键", "ADAG_DEBUG"} {
		if err := ValidateKey(bad); err == nil {
			t.Fatalf("非法键 %q 应被拒绝", bad)
		}
	}
	for _, good := range []string{"A", "_A", "A1", "_1"} {
		if err := ValidateKey(good); err != nil {
			t.Fatalf("合法键 %q 被拒绝: %v", good, err)
		}
	}
	if err := ValidateValue(strings.Repeat("x", MaxValueBytes+1)); err == nil {
		t.Fatal("超长值应被拒绝")
	}
	if _, _, err := ParseAssignment("NOEQ"); err == nil {
		t.Fatal("缺少 = 应被拒绝")
	}
	key, value, err := ParseAssignment("TOKEN=a=b c")
	if err != nil {
		t.Fatalf("ParseAssignment: %v", err)
	}
	if key != "TOKEN" || value != "a=b c" {
		t.Fatalf("只在第一个 = 切分: %q %q", key, value)
	}
}
