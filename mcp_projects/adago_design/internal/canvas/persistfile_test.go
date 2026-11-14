package canvas

// 迭代 3-①：v4 分文件存储的行为验收 —— 索引 + nodes/<id>.html。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV4RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := newTestProject(t, s)
	a := addNode(t, s, pid, newTestNode("甲"))
	b := addNode(t, s, pid, newTestNode("乙"))

	// 重开：正文从分文件恢复
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	gotA, err := s2.Get(a.ID)
	if err != nil || !strings.Contains(gotA.HTML, "甲") {
		t.Fatalf("正文应从文件恢复: %v %+v", err, gotA)
	}
	gotB, _ := s2.Get(b.ID)
	if !strings.Contains(gotB.HTML, "乙") {
		t.Fatalf("节点乙正文缺失: %+v", gotB)
	}

	// Update：改过的正文文件应更新
	s2.Update(a.ID, func(n *Node) { n.HTML = "<html><body>甲-改</body></html>" })
	raw, err := os.ReadFile(filepath.Join(dir, "nodes", a.ID+".html"))
	if err != nil || !strings.Contains(string(raw), "甲-改") {
		t.Fatalf("更新后的正文文件未重写: %v %s", err, raw)
	}

	// Remove：正文文件应删除
	s2.Remove(b.ID)
	if _, err := os.Stat(filepath.Join(dir, "nodes", b.ID+".html")); !os.IsNotExist(err) {
		t.Fatal("删除节点后正文文件应删除")
	}
}

func TestV4IndexOmitsHTML(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	pid := newTestProject(t, s)
	addNode(t, s, pid, newTestNode("带正文"))
	raw, _ := os.ReadFile(filepath.Join(dir, "canvas.json"))
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("解析索引: %v", err)
	}
	if len(file.Nodes) != 1 || file.Nodes[0].HTMLFile == "" {
		t.Fatalf("索引应引用正文文件: %+v", file.Nodes)
	}
	// 索引体量：不含正文（正文 60+ 字节，索引节点段应远小于正文）
	if strings.Contains(string(raw), newTestNode("带正文").HTML) {
		t.Fatal("索引不应内联正文")
	}
}

func TestV4ClearHTMLRemovesFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	pid := newTestProject(t, s)
	n := addNode(t, s, pid, newTestNode("x"))
	// 正文清空：旧文件应删除（而非残留陈旧内容）
	if _, err := s.Update(n.ID, func(x *Node) { x.HTML = "" }); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes", n.ID+".html")); !os.IsNotExist(err) {
		t.Fatal("正文清空后旧文件应删除")
	}
}

func TestV4OrphanHTMLCollected(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	pid := newTestProject(t, s)
	n := addNode(t, s, pid, newTestNode("x"))
	// 进程外删节点（直接改索引）：重开时应清理孤儿正文文件
	raw, _ := os.ReadFile(filepath.Join(dir, "canvas.json"))
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	file.Nodes = nil
	out, _ := json.Marshal(file)
	if err := os.WriteFile(filepath.Join(dir, "canvas.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes", n.ID+".html")); !os.IsNotExist(err) {
		t.Fatal("孤儿正文文件应被 GC")
	}
}
