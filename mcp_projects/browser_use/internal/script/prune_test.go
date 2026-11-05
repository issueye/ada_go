package script

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPruneSourceDirs 钉住 T2.6：只清理「目录名是日期且早于保留期」的目录；
// 今天的目录与非法命名的条目原样保留。keep_sources 关闭后旧目录也照清，
// 否则开关留下的历史文件永远留在盘上。
func TestPruneSourceDirs(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "20200101")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "s1.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	today := filepath.Join(root, time.Now().Format("20060102"))
	if err := os.MkdirAll(today, 0o755); err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(root, "29991231")
	if err := os.MkdirAll(future, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-dir.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruneSourceDirs(root, 7)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("过期日期目录应被删除")
	}
	if _, err := os.Stat(today); err != nil {
		t.Fatal("今天的目录应保留")
	}
	if _, err := os.Stat(future); err != nil {
		t.Fatal("未来日期目录应保留")
	}
	if _, err := os.Stat(filepath.Join(root, "not-a-dir.txt")); err != nil {
		t.Fatal("非日期条目应保留")
	}

	pruneSourceDirs(root, 0) // <=0 回退默认 7,同样只删过期的
	if _, err := os.Stat(today); err != nil {
		t.Fatal("days=0 应回退默认保留期,今天的目录应保留")
	}
}
