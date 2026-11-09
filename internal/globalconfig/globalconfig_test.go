package globalconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStoreNamespacesAndCRUD(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	a, err := Open("demo-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open("demo-b")
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := a.Get("providers/openai"); err != nil || ok || v != nil {
		t.Fatalf("缺失 key = %v/%v/%v", v, ok, err)
	}
	if err := a.Set("providers/openai", json.RawMessage(`{"baseUrl":"https://example.test"}`)); err != nil {
		t.Fatal(err)
	}
	if err := a.Set("cron", json.RawMessage(`[{"id":"t1"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Set("providers/openai", json.RawMessage(`{"baseUrl":"other"}`)); err != nil {
		t.Fatal(err)
	}
	v, ok, err := a.Get("providers/openai")
	if err != nil || !ok || !strings.Contains(string(v), "example.test") {
		t.Fatalf("读回 = %s/%v/%v", v, ok, err)
	}
	if v, _, _ := b.Get("providers/openai"); strings.Contains(string(v), "example.test") {
		t.Fatalf("命名空间串了: %s", v)
	}
	list, err := a.List("providers/")
	if err != nil || len(list) != 1 || list[0].Key != "providers/openai" {
		t.Fatalf("list = %+v / %v", list, err)
	}
	deleted, err := a.Delete("cron")
	if err != nil || !deleted {
		t.Fatalf("delete = %v/%v", deleted, err)
	}
	if deleted, err := a.Delete("cron"); err != nil || deleted {
		t.Fatalf("重复 delete = %v/%v", deleted, err)
	}
	if list, _ := a.List(""); len(list) != 1 {
		t.Fatalf("删除后 list = %+v", list)
	}
}

func TestStoreValidation(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	s, err := Open("demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../escape", "a/../b", "/abs", strings.Repeat("x", MaxKeyBytes+1)} {
		if err := s.Set(key, json.RawMessage(`1`)); err == nil {
			t.Fatalf("key %q 应被拒绝", key)
		}
	}
	if err := s.Set("bad", json.RawMessage(`{`)); err == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
	if err := s.Set("big", json.RawMessage(`"`+strings.Repeat("x", MaxValueBytes)+`"`)); err == nil {
		t.Fatal("超大 value 应被拒绝")
	}
}

func TestStorePathPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 chmod 不表达 0600，权限由 ACL 与部署边界保证")
	}
	home := t.TempDir()
	t.Setenv("ADAGO_HOME", home)
	s, err := Open("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("k", json.RawMessage(`1`)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, DirName, "demo.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("配置文件权限过宽: %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("配置目录权限过宽: %o", dirInfo.Mode().Perm())
	}
}

func TestStoreRejectsInvalidPluginID(t *testing.T) {
	t.Setenv("ADAGO_HOME", t.TempDir())
	for _, id := range []string{"../evil", "A", "", "x/y"} {
		if _, err := Open(id); err == nil {
			t.Fatalf("id %q 应被拒绝", id)
		}
	}
}
