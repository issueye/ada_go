package canvas

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// 项目约束与生命周期：设计稿必须归属一个已存在的项目。

func TestAddRequiresExistingProject(t *testing.T) {
	s, _ := OpenStore("")

	if _, err := s.Add(newTestNode("no-project")); err != ErrProjectRequired {
		t.Fatalf("空 project 应 ErrProjectRequired，得到 %v", err)
	}
	if _, err := s.Add(Node{ProjectID: "p-nope", Title: "x", HTML: "x"}); err != ErrUnknownProject {
		t.Fatalf("未知 project 应 ErrUnknownProject，得到 %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("被拒绝的节点不应落库")
	}
}

func TestProjectLifecycle(t *testing.T) {
	s, _ := OpenStore("")

	p, err := s.AddProject("官网改版", "PC + H5")
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	if p.ID == "" || p.Name != "官网改版" || p.Note != "PC + H5" {
		t.Fatalf("项目字段异常: %+v", p)
	}
	if _, err := s.AddProject("  ", ""); err != ErrProjectNameEmpty {
		t.Fatalf("空名应 ErrProjectNameEmpty，得到 %v", err)
	}
	if _, err := s.AddProject("官网改版", ""); err != ErrProjectDuplicate {
		t.Fatalf("同名应 ErrProjectDuplicate，得到 %v", err)
	}

	if got, err := s.GetProject(p.ID); err != nil || got.Name != "官网改版" {
		t.Fatalf("GetProject: %v %+v", err, got)
	}
	if _, err := s.GetProject("p-nope"); err != ErrProjectNotFound {
		t.Fatalf("未知项目应 ErrProjectNotFound，得到 %v", err)
	}

	// 有设计稿时拒绝删除，避免「删项目」变成静默丢数据
	node := addNode(t, s, p.ID, newTestNode("首页"))
	if err := s.RemoveProject(p.ID); err != ErrProjectNotEmpty {
		t.Fatalf("非空项目应 ErrProjectNotEmpty，得到 %v", err)
	}

	// 改名 + 计数
	if renamed, err := s.UpdateProject(p.ID, "官网改版 v2", ""); err != nil || renamed.Name != "官网改版 v2" {
		t.Fatalf("UpdateProject: %v %+v", err, renamed)
	}
	sums := s.ListProjects()
	if len(sums) != 1 || sums[0].NodeCount != 1 || sums[0].ID != p.ID {
		t.Fatalf("项目摘要异常: %+v", sums)
	}

	// 清空后可删除
	if n, err := s.Clear(p.ID); err != nil || n != 1 {
		t.Fatalf("Clear = %d (%v)", n, err)
	}
	if _, err := s.Get(node.ID); err == nil {
		t.Fatal("清空后节点应不存在")
	}
	if err := s.RemoveProject(p.ID); err != nil {
		t.Fatalf("清空后应可删除: %v", err)
	}
	if len(s.ListProjects()) != 0 {
		t.Fatal("删除后项目列表应为空")
	}
}

func TestProjectScoping(t *testing.T) {
	s, _ := OpenStore("")
	p1, _ := s.AddProject("项目一", "")
	p2, _ := s.AddProject("项目二", "")

	a := addNode(t, s, p1.ID, newTestNode("a"))
	b := addNode(t, s, p2.ID, newTestNode("b"))

	if got := s.ListByProject(p1.ID); len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("项目一节点异常: %+v", got)
	}
	if got := s.ListByProject(p2.ID); len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("项目二节点异常: %+v", got)
	}
	if got := s.ListByProject(""); len(got) != 2 {
		t.Fatalf("空 project 应返回全部，得到 %d", len(got))
	}

	// 自动排版按项目独立：两个项目各自从 (80,80) 起
	if a.X != 80 || a.Y != 80 || b.X != 80 || b.Y != 80 {
		t.Fatalf("不同项目应各自从原点铺开: a=(%v,%v) b=(%v,%v)", a.X, a.Y, b.X, b.Y)
	}

	// 清空项目一只影响自己
	if n, err := s.Clear(p1.ID); err != nil || n != 1 {
		t.Fatalf("Clear(p1) = %d (%v)", n, err)
	}
	if _, err := s.Get(a.ID); err == nil {
		t.Fatal("p1 的节点应被清除")
	}
	if _, err := s.Get(b.ID); err != nil {
		t.Fatal("p2 的节点不应受影响")
	}
}

func TestCrossProjectEdgeRejected(t *testing.T) {
	s, _ := OpenStore("")
	p1, _ := s.AddProject("项目一", "")
	p2, _ := s.AddProject("项目二", "")
	a := addNode(t, s, p1.ID, newTestNode("a"))
	b := addNode(t, s, p2.ID, newTestNode("b"))

	if _, err := s.AddEdge(a.ID, b.ID, ""); err != ErrEdgeCrossProject {
		t.Fatalf("跨项目连线应 ErrEdgeCrossProject，得到 %v", err)
	}
	if len(s.ListEdges()) != 0 {
		t.Fatal("跨项目连线不应落库")
	}
}

func TestHTTPProjectEndpoints(t *testing.T) {
	_, ts := newTestServer(t)

	res, data := doJSON(t, "POST", ts.URL+"/api/projects", map[string]any{"name": "验收项目", "note": "回归"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d: %v", res.StatusCode, data)
	}
	pid := data["project"].(map[string]any)["id"].(string)

	if got := statusCode(t, ts, "GET", "/api/projects", nil); got != 200 {
		t.Fatalf("list status = %d", got)
	}
	if got := statusCode(t, ts, "GET", "/api/projects/"+pid, nil); got != 200 {
		t.Fatalf("get status = %d", got)
	}
	if got := statusCode(t, ts, "GET", "/api/projects/p-nope", nil); got != 404 {
		t.Fatalf("未知项目应 404, got %d", got)
	}
	// 同名重复
	res, data = doJSON(t, "POST", ts.URL+"/api/projects", map[string]any{"name": "验收项目"})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("同名应 409, got %d: %v", res.StatusCode, data)
	}
	if code := dataOf(t, data, "error").(map[string]any)["code"]; code != "project_duplicate" {
		t.Fatalf("错误码 = %v", code)
	}

	// 非空项目拒绝删除
	res0(t, ts, "POST", "/api/nodes", map[string]any{
		"project": pid, "html": "<html></html>", "title": "n",
	})
	res, data = doJSON(t, "DELETE", ts.URL+"/api/projects/"+pid, nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("非空项目删除应 409, got %d: %v", res.StatusCode, data)
	}
	// 清空后可删
	if got := statusCode(t, ts, "POST", "/api/nodes/clear", map[string]any{"project": pid}); got != 200 {
		t.Fatalf("clear status = %d", got)
	}
	if got := statusCode(t, ts, "DELETE", "/api/projects/"+pid, nil); got != 200 {
		t.Fatalf("删除 status = %d", got)
	}
}

func TestHTTPProjectRequired(t *testing.T) {
	_, ts := newTestServer(t)

	// 不存在的项目 → 404 unknown_project
	res, data := doJSON(t, "POST", ts.URL+"/api/nodes", map[string]any{
		"project": "p-nope", "html": "<html></html>",
	})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("未知项目应 404, got %d: %v", res.StatusCode, data)
	}

	// 不带项目 → 400 project_required
	res, data = doJSON(t, "POST", ts.URL+"/api/nodes", map[string]any{"html": "<html></html>"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺项目应 400, got %d: %v", res.StatusCode, data)
	}
	if code := dataOf(t, data, "error").(map[string]any)["code"]; code != "project_required" {
		t.Fatalf("错误码 = %v", code)
	}

	// 清空也必须带项目
	res, _ = doJSON(t, "POST", ts.URL+"/api/nodes/clear", map[string]any{})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("清空缺项目应 400, got %d", res.StatusCode)
	}

	// 节点列表按项目过滤
	pid := newHTTPProject(t, ts)
	res0(t, ts, "POST", "/api/nodes", map[string]any{"project": pid, "html": "<html></html>", "title": "a"})
	if got := len(res0(t, ts, "GET", "/api/nodes?project="+pid, nil)["nodes"].([]any)); got != 1 {
		t.Fatalf("项目过滤数量 = %d", got)
	}
	if got := statusCode(t, ts, "GET", "/api/nodes?project=p-nope", nil); got != 404 {
		t.Fatalf("未知项目过滤应 404, got %d", got)
	}
}

// v2（无 projects）加载后应立刻回写 v3，避免「API 已是项目制、磁盘还是旧格式」。
func TestV2MigrationPersistsV3(t *testing.T) {
	dir := t.TempDir()
	v2 := `{"version":2,"nodes":[
		{"id":"n-1","title":"旧稿","kind":"note","html":"<html></html>","width":300,"height":200,"x":10,"y":20,"z":1}
	],"edges":[]}`
	if err := os.WriteFile(filepath.Join(dir, "canvas.json"), []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "canvas.json"))
	if err != nil {
		t.Fatalf("读回 canvas.json: %v", err)
	}
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if file.Version != 3 {
		t.Fatalf("迁移后磁盘版本 = %d，期望 3", file.Version)
	}
	if len(file.Projects) != 1 || file.Projects[0].ID != LegacyProjectID {
		t.Fatalf("磁盘项目段异常: %+v", file.Projects)
	}
	if len(file.Nodes) != 1 || file.Nodes[0].ProjectID != LegacyProjectID {
		t.Fatalf("节点未写入 project_id: %+v", file.Nodes)
	}
	if got := s.ListProjects(); len(got) != 1 || got[0].NodeCount != 1 {
		t.Fatalf("内存项目摘要异常: %+v", got)
	}

	// 二次打开不应再产生新项目（迁移幂等）
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("二次 OpenStore: %v", err)
	}
	if got := s2.ListProjects(); len(got) != 1 {
		t.Fatalf("迁移应幂等，得到 %d 个项目", len(got))
	}
}

// 全部视图：不同项目的节点不能重叠，按项目分行后行首留白、行内横排。
func TestArrangeByProject(t *testing.T) {
	s, _ := OpenStore("")
	p1, _ := s.AddProject("项目一", "")
	p2, _ := s.AddProject("项目二", "")
	a := addNode(t, s, p1.ID, newTestNode("a"))
	addNode(t, s, p1.ID, newTestNode("b"))
	c := addNode(t, s, p2.ID, newTestNode("c"))

	// 前置条件：不同项目各自从 (80,80) 起排，因此在「全部」视图里会完全重叠
	if a.X != c.X || a.Y != c.Y {
		t.Fatalf("前置条件不成立：a=(%v,%v) c=(%v,%v)", a.X, a.Y, c.X, c.Y)
	}

	moved, err := s.ArrangeByProject()
	if err != nil {
		t.Fatalf("ArrangeByProject: %v", err)
	}
	if moved != 3 {
		t.Fatalf("moved = %d，期望 3", moved)
	}
	got := map[string]Node{}
	for _, n := range s.List() {
		got[n.Title] = n
	}
	if got["a"].Y != got["b"].Y {
		t.Fatalf("同项目应同一行: a.y=%v b.y=%v", got["a"].Y, got["b"].Y)
	}
	if got["c"].Y <= got["a"].Y {
		t.Fatalf("第二个项目应排到下一行: c.y=%v a.y=%v", got["c"].Y, got["a"].Y)
	}
	if got["a"].X < 200 {
		t.Fatalf("行首应留出标签位（x≥200），得到 %v", got["a"].X)
	}

	// 幂等：再跑一次不应再移动
	if again, _ := s.ArrangeByProject(); again != 0 {
		t.Fatalf("二次排版 moved = %d，期望 0", again)
	}
}
