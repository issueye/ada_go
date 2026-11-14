package canvas

// 迭代 3-②：操作历史（撤销/重做）。

import (
	"net/http"
	"strings"
	"testing"
)

func TestUndoRedoNodes(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)

	// 新增 → 撤销消失 → 重做回来
	a := addNode(t, s, pid, newTestNode("甲"))
	if len(s.List()) != 1 {
		t.Fatal("新增后应 1 个节点")
	}
	if desc, ok := s.Undo(); !ok || desc == "" {
		t.Fatal("撤销新增应成功且带描述")
	}
	if len(s.List()) != 0 {
		t.Fatal("撤销后节点应消失")
	}
	if desc, ok := s.Redo(); !ok {
		t.Fatalf("重做应成功: %q", desc)
	}
	if len(s.List()) != 1 {
		t.Fatal("重做后节点应回来")
	}

	// 更新（挪位置）→ 撤销恢复原坐标 → 重做再生效
	updated, _ := s.Update(a.ID, func(n *Node) { n.X, n.Y = 300, 200 })
	got, _ := s.Get(a.ID)
	if got.X != 300 {
		t.Fatalf("更新未生效: %v", got.X)
	}
	if _, ok := s.Undo(); !ok {
		t.Fatal("撤销更新应成功")
	}
	if got, _ = s.Get(a.ID); got.X == 300 {
		t.Fatal("撤销后应恢复原坐标")
	}
	if _, ok := s.Redo(); !ok {
		t.Fatal("重做更新应成功")
	}
	if got, _ = s.Get(a.ID); got.X != 300 || got.ID != updated.ID {
		t.Fatalf("重做后应恢复新坐标: %v", got)
	}

	// 删除 → 撤销复活 → 重做再删
	s.Remove(a.ID)
	if len(s.List()) != 0 {
		t.Fatal("删除后应无节点")
	}
	if _, ok := s.Undo(); !ok {
		t.Fatal("撤销删除应成功")
	}
	if got, _ = s.Get(a.ID); got.Title != "甲" {
		t.Fatalf("撤销删除后节点应完整复活: %+v", got)
	}
	if _, ok := s.Redo(); !ok {
		t.Fatal("重做删除应成功")
	}
	if len(s.List()) != 0 {
		t.Fatal("重做删除后应无节点")
	}
}

func TestUndoEdgesAndClear(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)
	a := addNode(t, s, pid, newTestNode("甲"))
	b := addNode(t, s, pid, newTestNode("乙"))

	// 连线增删可撤销
	edge, err := s.AddEdge(a.ID, b.ID, "流向")
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if _, ok := s.Undo(); !ok {
		t.Fatal("撤销连线应成功")
	}
	if len(s.ListEdges()) != 0 {
		t.Fatal("撤销后连线应消失")
	}
	if _, ok := s.Redo(); !ok {
		t.Fatal("重做连线应成功")
	}
	if len(s.ListEdges()) != 1 {
		t.Fatal("重做后连线应回来")
	}

	// 清空项目可撤销（节点+连线一起回）
	if n, err := s.Clear(pid); err != nil || n != 2 {
		t.Fatalf("Clear = %d, %v", n, err)
	}
	if len(s.List()) != 0 || len(s.ListEdges()) != 0 {
		t.Fatal("清空后应无节点与连线")
	}
	if _, ok := s.Undo(); !ok {
		t.Fatal("撤销清空应成功")
	}
	if len(s.List()) != 2 || len(s.ListEdges()) != 1 {
		t.Fatalf("撤销清空后节点与连线应全部回来: %d 节点 %d 连线", len(s.List()), len(s.ListEdges()))
	}
	got, err := s.Get(a.ID)
	if err != nil || !strings.Contains(got.HTML, "甲") {
		t.Fatalf("恢复的节点正文应完整（v4 分文件重写）: %v", err)
	}
	if _, ok := s.Redo(); !ok {
		t.Fatal("重做清空应成功")
	}
	if len(s.List()) != 0 {
		t.Fatal("重做清空后应无节点")
	}

	// RemoveEdge 可撤销
	if _, ok := s.Undo(); !ok { // 撤销「重做清空」→ 节点回来
		t.Fatal("撤销重做清空应成功")
	}
	if ok, _ := s.RemoveEdge(edge.ID); !ok {
		t.Fatal("删除连线应成功")
	}
	if _, ok := s.Undo(); !ok {
		t.Fatal("撤销删除连线应成功")
	}
	if len(s.ListEdges()) != 1 {
		t.Fatal("连线应恢复")
	}
}

func TestUndoStackSemantics(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)

	// 新操作清空重做栈
	a := addNode(t, s, pid, newTestNode("甲"))
	s.Undo()
	s.Redo()
	addNode(t, s, pid, newTestNode("乙"))
	if _, ok := s.Redo(); ok {
		t.Fatal("新操作后重做栈应清空")
	}

	// 项目增删改不进历史
	s.AddProject("历史外", "")
	if _, r := s.HistoryDepth(); r != 0 {
		t.Fatalf("AddProject 不应进历史（redo 栈被上一操作清空后不应有新条目）")
	}
	s.RemoveProject(s.ListProjects()[0].ID) // 非「历史外」也行：挑一个存在的项目删除
	_, _ = s.Update(a.ID, func(n *Node) { n.Title = "t" })

	// 上限 50 步
	for i := 0; i < 60; i++ {
		addNode(t, s, pid, newTestNode("n"))
	}
	undone := 0
	for {
		if _, ok := s.Undo(); !ok {
			break
		}
		undone++
		if undone > undoLimit+5 {
			t.Fatal("撤销步数应受上限约束")
		}
	}
	if undone != undoLimit {
		t.Fatalf("可撤销步数 = %d, 期望 %d", undone, undoLimit)
	}

	// 历史仅内存：重开即清空
	s2, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Undo(); ok {
		t.Fatal("新存储不应有历史")
	}
}

func TestUndoHTTP(t *testing.T) {
	_, ts := newTestServer(t)
	pid := newHTTPProject(t, ts)
	res0(t, ts, "POST", "/api/nodes", map[string]any{
		"project": pid, "title": "t", "html": "<html></html>", "width": 100, "height": 80,
	})
	data := res0(t, ts, "POST", "/api/undo", map[string]any{})
	if data["done"] != true {
		t.Fatalf("undo 应成功: %v", data)
	}
	if nodes := res0(t, ts, "GET", "/api/nodes?project="+pid, nil)["nodes"].([]any); len(nodes) != 0 {
		t.Fatal("撤销后节点应消失")
	}
	data = res0(t, ts, "POST", "/api/redo", map[string]any{})
	if data["done"] != true {
		t.Fatalf("redo 应成功: %v", data)
	}
	if nodes := res0(t, ts, "GET", "/api/nodes?project="+pid, nil)["nodes"].([]any); len(nodes) != 1 {
		t.Fatal("重做后节点应回来")
	}
	// 回到空历史：redo 会把条目放回撤销栈，所以再撤销一次
	res, data := doJSON(t, "POST", ts.URL+"/api/undo", map[string]any{})
	if res.StatusCode != http.StatusOK || data["done"] != true {
		t.Fatalf("第二次 undo 应成功: %d %v", res.StatusCode, data)
	}
	// 空历史：done=false 但仍 200
	res, data = doJSON(t, "POST", ts.URL+"/api/undo", map[string]any{})
	if res.StatusCode != http.StatusOK || data["done"] != false {
		t.Fatalf("空历史 undo 应 200 + done=false: %d %v", res.StatusCode, data)
	}
}
