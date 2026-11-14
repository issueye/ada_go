package canvas

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestNode(title string) Node {
	return Node{
		Title: title, Kind: KindNote, System: "element",
		HTML:  "<!DOCTYPE html><html><body><p>" + title + "</p></body></html>",
		Width: 420, Height: 320,
	}
}

// newTestProject 建一个测试项目并返回 ID。
// 设计稿必须归属已存在的项目，所以每个要 Add 的用例先建项目。
func newTestProject(t *testing.T, s *Store) string {
	t.Helper()
	p, err := s.AddProject("测试项目", "")
	if err != nil {
		t.Fatalf("AddProject: %v", err)
	}
	return p.ID
}

// addNode 是 Store.Add 的测试封装（自动带项目并断言无错误）。
func addNode(t *testing.T, s *Store, projectID string, n Node) Node {
	t.Helper()
	n.ProjectID = projectID
	created, err := s.Add(n)
	if err != nil {
		t.Fatalf("Add(%s): %v", n.Title, err)
	}
	return created
}

func TestStoreCRUDAndPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("新存储应为空")
	}
	pid := newTestProject(t, s)
	a := addNode(t, s, pid, newTestNode("a"))
	if a.ID == "" || a.Kind != KindNote || a.Width != 420 {
		t.Fatalf("Add 默认值异常: %+v", a)
	}
	b := addNode(t, s, pid, newTestNode("b"))
	x, y := s.AutoPosition(pid, 420, 320)
	b2, err := s.Update(b.ID, func(n *Node) { n.X, n.Y = x, y; n.Title = "b2" })
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if b2.Title != "b2" || b2.X != x || b2.UpdatedAt.Before(b.UpdatedAt) {
		t.Fatalf("Update 未生效: %+v", b2)
	}
	got, err := s.Get(a.ID)
	if err != nil || got.Title != "a" {
		t.Fatalf("Get: %v %+v", err, got)
	}

	// 持久化：重新打开应恢复
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(s2.List()) != 2 {
		t.Fatalf("重开后节点数 = %d, 期望 2", len(s2.List()))
	}
	if _, err := s2.Get(b.ID); err != nil {
		t.Fatalf("重开后应能找到 %s: %v", b.ID, err)
	}

	if ok, _ := s2.Remove(a.ID); !ok {
		t.Fatal("Remove 应返回 true")
	}
	if ok, _ := s2.Remove(a.ID); ok {
		t.Fatal("重复 Remove 应返回 false")
	}
	if n, cerr := s2.Clear(pid); cerr != nil || n != 1 {
		t.Fatalf("Clear = %d (%v), 期望 1", n, cerr)
	}
}

func TestStorePersistFileFormat(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	pid := newTestProject(t, s)
	a := addNode(t, s, pid, newTestNode("x"))
	b := addNode(t, s, pid, newTestNode("y"))
	if _, err := s.AddEdge(a.ID, b.ID, "流向"); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "canvas.json"))
	if err != nil {
		t.Fatalf("canvas.json 未生成: %v", err)
	}
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("canvas.json 不是合法 JSON: %v", err)
	}
	if file.Version != 4 || len(file.Projects) != 1 || len(file.Nodes) != 2 || len(file.Edges) != 1 {
		t.Fatalf("持久化内容异常: version=%d projects=%d nodes=%d edges=%d",
			file.Version, len(file.Projects), len(file.Nodes), len(file.Edges))
	}
	if file.Edges[0].Label != "流向" {
		t.Fatalf("连线标签异常: %+v", file.Edges[0])
	}
	// v4 分文件：索引不含正文，正文在 nodes/<id>.html
	for _, idx := range file.Nodes {
		if idx.HTML != "" || idx.HTMLFile == "" {
			t.Fatalf("索引应只引用正文文件: %+v", idx)
		}
		rawHTML, err := os.ReadFile(filepath.Join(dir, "nodes", idx.HTMLFile))
		if err != nil || !strings.Contains(string(rawHTML), "<p>") {
			t.Fatalf("正文文件应存在且含内容: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "canvas.json.tmp")); !os.IsNotExist(err) {
		t.Error("临时文件应已被 rename 清理")
	}
}

func TestStoreV1Migration(t *testing.T) {
	dir := t.TempDir()
	// 写一份 v1 裸数组格式
	v1 := `[
		{"id":"n-old-1","title":"旧节点","kind":"note","html":"<html></html>","width":300,"height":200,"x":10,"y":20,"z":3}
	]`
	if err := os.WriteFile(filepath.Join(dir, "canvas.json"), []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("v1 迁移失败: %v", err)
	}
	nodes := s.List()
	if len(nodes) != 1 || nodes[0].ID != "n-old-1" || nodes[0].Z != 3 {
		t.Fatalf("v1 节点迁移异常: %+v", nodes)
	}
	// v1 节点没有 project_id，迁移后应自动落到历史项目
	if nodes[0].ProjectID != LegacyProjectID {
		t.Fatalf("v1 节点应迁入 %s，得到 %q", LegacyProjectID, nodes[0].ProjectID)
	}
	if len(s.ListProjects()) != 1 || s.ListProjects()[0].ID != LegacyProjectID {
		t.Fatalf("迁移后应存在历史项目: %+v", s.ListProjects())
	}
	// 迁移后的下一次写入应为 v4（正文迁到 nodes/ 分文件）
	s.AddEdge("n-old-1", "n-old-1", "") // 自环报错，不写入
	addNode(t, s, LegacyProjectID, newTestNode("new"))
	raw, _ := os.ReadFile(filepath.Join(dir, "canvas.json"))
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 4 {
		t.Fatalf("迁移后应写 v4 格式: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes", "n-old-1.html")); err != nil {
		t.Fatalf("v1 内联正文应迁移为正文文件: %v", err)
	}
}

func TestEdges(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)
	a := addNode(t, s, pid, newTestNode("a"))
	b := addNode(t, s, pid, newTestNode("b"))
	c := addNode(t, s, pid, newTestNode("c"))
	e, err := s.AddEdge(a.ID, b.ID, "下一步")
	if err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if e.ID == "" || e.From != a.ID || e.To != b.ID || e.Label != "下一步" {
		t.Fatalf("连线内容异常: %+v", e)
	}
	// 重复（含反向）应拒绝
	if _, err := s.AddEdge(b.ID, a.ID, ""); err != ErrEdgeDuplicate {
		t.Fatalf("反向重复应 ErrEdgeDuplicate: %v", err)
	}
	// 自环
	if _, err := s.AddEdge(a.ID, a.ID, ""); err != ErrEdgeSelfLoop {
		t.Fatalf("自环应 ErrEdgeSelfLoop: %v", err)
	}
	// 端点缺失
	if _, err := s.AddEdge(a.ID, "n-none", ""); err != ErrEdgeEndpoint {
		t.Fatalf("端点缺失应 ErrEdgeEndpoint: %v", err)
	}
	if len(s.ListEdges()) != 1 {
		t.Fatalf("连线数 = %d", len(s.ListEdges()))
	}
	// 删除节点级联删除连线
	if _, err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.ListEdges()) != 0 {
		t.Fatalf("节点删除后连线应级联删除")
	}
	// 删除连线
	e2, _ := s.AddEdge(b.ID, c.ID, "")
	if ok, _ := s.RemoveEdge(e2.ID); !ok {
		t.Fatal("RemoveEdge 应成功")
	}
	if ok, _ := s.RemoveEdge(e2.ID); ok {
		t.Fatal("重复 RemoveEdge 应 false")
	}
	// Clear + Restore 覆盖连线
	e3, _ := s.AddEdge(b.ID, c.ID, "回环")
	if _, err := s.Clear(pid); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(s.ListEdges()) != 0 {
		t.Fatal("清空后应无连线")
	}
	if n, _, ok := s.RestoreLastClear(); !ok || n != 2 { // a 已被删除，快照只剩 b/c
		t.Fatalf("恢复节点异常: %d %v", n, ok)
	}
	edges := s.ListEdges()
	if len(edges) != 1 || edges[0].ID != e3.ID || edges[0].Label != "回环" {
		t.Fatalf("连线恢复异常: %+v", edges)
	}
}

func TestAutoPositionEmptyAndRow(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)
	x1, y1 := s.AutoPosition(pid, 420, 320)
	if x1 != 80 || y1 != 80 {
		t.Fatalf("空画布首个位置 = (%v,%v), 期望 (80,80)", x1, y1)
	}
	addNode(t, s, pid, Node{Title: "1", Width: 420, Height: 320, HTML: "x", X: x1, Y: y1})
	x2, y2 := s.AutoPosition(pid, 420, 320)
	if x2 != x1+420+48 {
		t.Fatalf("第二个节点应排在右侧: %v", x2)
	}
	if y2 != y1 {
		t.Fatalf("同行节点 Y 应一致: %v", y2)
	}
}

func TestAutoPositionWrap(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)
	addNode(t, s, pid, Node{Title: "wide", Width: 3400, Height: 400, HTML: "x"})
	x, y := s.AutoPosition(pid, 800, 400)
	if x != 80 {
		t.Fatalf("超宽后应回到行首 x=80, got %v", x)
	}
	if y <= 400 {
		t.Fatalf("应换到下一行 y>400, got %v", y)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s, _ := OpenStore("")
	pid := newTestProject(t, s)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				node := newTestNode("c")
				node.ProjectID = pid
				n, err := s.Add(node)
				if err != nil {
					continue
				}
				s.Update(n.ID, func(nn *Node) { nn.Title = "m" })
				s.List()
				s.Get(n.ID)
			}
		}()
	}
	wg.Wait()
	if len(s.List()) != 160 {
		t.Fatalf("并发 Add 后数量 = %d, 期望 160", len(s.List()))
	}
}

// ---- HTTP 层 ----

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, NewTaskQueue(), "")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	var data map[string]any
	_ = json.NewDecoder(res.Body).Decode(&data)
	res.Body.Close()
	return res, data
}

// newHTTPProject 通过 HTTP 建项目并返回 ID（HTTP 用例新增节点前必须先建项目）。
func newHTTPProject(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	res, data := doJSON(t, "POST", ts.URL+"/api/projects", map[string]any{"name": "HTTP 用例项目"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("建项目 status = %d: %v", res.StatusCode, data)
	}
	return data["project"].(map[string]any)["id"].(string)
}

func TestNodeAPIFlow(t *testing.T) {
	_, ts := newTestServer(t)
	pid := newHTTPProject(t, ts)

	res, _ := doJSON(t, "POST", ts.URL+"/api/nodes", map[string]any{
		"project": pid, "title": "卡片", "kind": "component", "system": "volcano",
		"html": "<html><body>hello</body></html>", "width": 420, "height": 300,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("add status = %d", res.StatusCode)
	}
	node := dataOf(t, res0(t, ts, "GET", "/api/nodes", nil), "nodes").([]any)[0].(map[string]any)
	id := node["id"].(string)
	if node["title"] != "卡片" {
		t.Fatalf("title = %v", node["title"])
	}

	res, _ = doJSON(t, "PUT", ts.URL+"/api/nodes/"+id, map[string]any{"x": 300.5, "y": 120.0, "title": "卡片2"})
	if res.StatusCode != 200 {
		t.Fatalf("update status = %d", res.StatusCode)
	}
	res, _ = doJSON(t, "GET", ts.URL+"/api/nodes", nil)
	node = res0(t, ts, "GET", "/api/nodes", nil)["nodes"].([]any)[0].(map[string]any)
	if node["x"] != 300.5 || node["title"] != "卡片2" {
		t.Fatalf("update 未生效: %v", node)
	}

	res, _ = doJSON(t, "GET", ts.URL+"/api/nodes?system=apple", nil)
	if len(res0(t, ts, "GET", "/api/nodes?system=apple", nil)["nodes"].([]any)) != 0 {
		t.Fatal("apple 过滤应为空")
	}

	res, _ = doJSON(t, "DELETE", ts.URL+"/api/nodes/"+id, nil)
	if res.StatusCode != 200 {
		t.Fatalf("delete status = %d", res.StatusCode)
	}
	if res.StatusCode = statusCode(t, ts, "DELETE", "/api/nodes/"+id, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("重复删除应 404, got %d", res.StatusCode)
	}
}

// res0 重新请求并返回 JSON（简化测试书写）。
func res0(t *testing.T, ts *httptest.Server, method, path string, body any) map[string]any {
	t.Helper()
	res, data := doJSON(t, method, ts.URL+path, body)
	if res.StatusCode >= 400 {
		t.Fatalf("%s %s -> %d: %v", method, path, res.StatusCode, data)
	}
	return data
}

func dataOf(t *testing.T, m map[string]any, key string) any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("响应缺少 %s: %v", key, m)
	}
	return v
}

func statusCode(t *testing.T, ts *httptest.Server, method, path string, body any) int {
	t.Helper()
	res, _ := doJSON(t, method, ts.URL+path, body)
	return res.StatusCode
}

func TestNodeAPIValidation(t *testing.T) {
	_, ts := newTestServer(t)
	if got := statusCode(t, ts, "POST", "/api/nodes", map[string]any{"title": "no html"}); got != 400 {
		t.Fatalf("缺少 html 应 400, got %d", got)
	}
	if got := statusCode(t, ts, "POST", "/api/nodes", map[string]any{"html": "x", "kind": "bogus"}); got != 400 {
		t.Fatalf("非法 kind 应 400, got %d", got)
	}
	if got := statusCode(t, ts, "PUT", "/api/nodes/n-missing", map[string]any{"title": "x"}); got != 404 {
		t.Fatalf("更新不存在节点应 404, got %d", got)
	}
	if got := statusCode(t, ts, "POST", "/api/nodes", nil); got != 400 {
		t.Fatalf("空 body 应 400, got %d", got)
	}
}

func TestCrossOriginWriteRejected(t *testing.T) {
	_, ts := newTestServer(t)
	req, _ := http.NewRequest("POST", ts.URL+"/api/nodes/clear", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("跨域写应 403, got %d", res.StatusCode)
	}
}

func TestCatalogEndpoints(t *testing.T) {
	srv, ts := newTestServer(t)
	srv.Catalog = fakeCatalog{}
	if got := statusCode(t, ts, "GET", "/api/catalog/systems", nil); got != 200 {
		t.Fatalf("systems status = %d", got)
	}
	if got := statusCode(t, ts, "GET", "/api/catalog/systems/fake", nil); got != 200 {
		t.Fatalf("system detail status = %d", got)
	}
	if got := statusCode(t, ts, "GET", "/api/catalog/systems/nope", nil); got != 404 {
		t.Fatalf("未知体系应 404, got %d", got)
	}
	res, data := doJSON(t, "POST", ts.URL+"/api/catalog/preview", map[string]any{
		"project": newHTTPProject(t, ts), "system": "fake", "components": []string{"button"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("preview status = %d: %v", res.StatusCode, data)
	}
}

type fakeCatalog struct{}

func (fakeCatalog) SystemSummaries() any { return []map[string]any{{"id": "fake", "name": "Fake"}} }
func (fakeCatalog) SystemDetail(id string) (any, bool) {
	if id != "fake" {
		return nil, false
	}
	return map[string]any{"id": "fake", "components": []map[string]any{{"id": "button", "name": "按钮"}}}, true
}
func (fakeCatalog) ComposePreview(system string, comps []string, theme, title, artboard string) (string, int, int, string, error) {
	return "<html>preview</html>", 420, 300, "iphone-15", nil
}

func TestCaptureFlow(t *testing.T) {
	srv, ts := newTestServer(t)
	_ = srv
	// 建一个节点（先建项目）
	res0(t, ts, "POST", "/api/nodes", map[string]any{
		"project": newHTTPProject(t, ts),
		"html":    "<html><body>x</body></html>", "width": 300, "height": 200, "title": "t",
	})
	nodes := res0(t, ts, "GET", "/api/nodes", nil)["nodes"].([]any)
	nodeID := nodes[0].(map[string]any)["id"].(string)

	// UI 侧消费者：领取任务并回传 PNG（可停止，避免阻塞 ts.Close）
	done := make(chan struct{})
	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		client := &http.Client{Timeout: 1500 * time.Millisecond}
		for {
			select {
			case <-done:
				return
			default:
			}
			req, _ := http.NewRequest("GET", ts.URL+"/api/tasks", nil)
			res, err := client.Do(req)
			if err != nil {
				continue
			}
			var data map[string]any
			_ = json.NewDecoder(res.Body).Decode(&data)
			res.Body.Close()
			task, ok := data["task"].(map[string]any)
			if !ok || task == nil {
				continue
			}
			payload := map[string]any{"png_base64": base64.StdEncoding.EncodeToString([]byte("fakepng"))}
			if task["node_id"] == nodeID {
				payload["png_base64"] = base64.StdEncoding.EncodeToString([]byte("nodepng"))
			}
			raw, _ := json.Marshal(payload)
			req2, _ := http.NewRequest("POST", ts.URL+"/api/tasks/"+task["id"].(string)+"/result", bytes.NewReader(raw))
			req2.Header.Set("Content-Type", "application/json")
			if res2, err := client.Do(req2); err == nil {
				res2.Body.Close()
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		consumer.Wait()
	})

	res, data := doJSON(t, "POST", ts.URL+"/api/capture", map[string]any{"node_id": nodeID, "scale": 2})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("capture create: %d", res.StatusCode)
	}
	taskID := data["task"].(map[string]any)["id"].(string)

	start := time.Now()
	res, data = doJSON(t, "GET", ts.URL+"/api/capture/"+taskID, nil)
	if res.StatusCode != 200 {
		t.Fatalf("capture wait: %d %v", res.StatusCode, data)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("截图回传应迅速（已有 UI 消费者）")
	}
	png := data["result"].(map[string]any)["png_base64"].(string)
	if got, _ := base64.StdEncoding.DecodeString(png); string(got) != "nodepng" {
		t.Fatalf("png = %q", got)
	}

	// 未知节点的截图请求应 404
	if got := statusCode(t, ts, "POST", "/api/capture", map[string]any{"node_id": "n-none"}); got != 404 {
		t.Fatalf("未知节点 capture 应 404, got %d", got)
	}
}

func TestCaptureTimeoutWhenNoUI(t *testing.T) {
	_, ts := newTestServer(t)
	res, data := doJSON(t, "POST", ts.URL+"/api/capture", map[string]any{})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", res.StatusCode)
	}
	taskID := data["task"].(map[string]any)["id"].(string)
	res, data = doJSON(t, "GET", ts.URL+"/api/capture/"+taskID+"?timeout=200", nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("无 UI 时应 409, got %d: %v", res.StatusCode, data)
	}
	errObj := data["error"].(map[string]any)
	if errObj["code"] != "capture_timeout" {
		t.Fatalf("错误码 = %v", errObj["code"])
	}
}

func TestStaticIndex(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("index status = %d", res.StatusCode)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(res.Body)
	if !bytes.Contains(buf.Bytes(), []byte("adago-design")) {
		t.Fatal("index.html 内容异常")
	}
}

// ES 模块由浏览器强制校验 MIME：Windows 注册表可能把 .js 映成 text/plain，
// 服务端必须显式覆盖，否则整页模块加载失败（且经典脚本时代不会暴露）。
func TestStaticModuleMIME(t *testing.T) {
	_, ts := newTestServer(t)
	cases := map[string]string{
		"/js/main.js":  "javascript",
		"/js/state.js": "javascript",
		"/tokens.css":  "text/css",
	}
	for path, want := range cases {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, want) {
			t.Errorf("%s Content-Type = %q，应包含 %q", path, ct, want)
		}
	}
}

func TestLoopbackHostAndSameOrigin(t *testing.T) {
	hosts := map[string]bool{
		"127.0.0.1:8788": true, "localhost:8788": true, "[::1]:8788": true,
		"127.0.0.1": true, "evil.example:8788": false,
		"127.0.0.1.evil.com:8788": false, "": false,
	}
	for host, want := range hosts {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, 期望 %v", host, got, want)
		}
	}

	origins := []struct {
		origin, host string
		want         bool
	}{
		{"http://127.0.0.1:8788", "127.0.0.1:8788", true},
		{"http://localhost:8788", "localhost:8788", true},
		{"http://LOCALHOST:8788", "localhost:8788", true},   // Host 大小写不敏感
		{"http://127.0.0.1:8788", "127.0.0.1:9999", false},  // 跨端口
		{"https://127.0.0.1:8788", "127.0.0.1:8788", false}, // 跨协议
		{"http://evil.example:8788", "127.0.0.1:8788", false},
		{"null", "127.0.0.1:8788", false},
	}
	for _, c := range origins {
		if got := sameOrigin(c.origin, c.host); got != c.want {
			t.Errorf("sameOrigin(%q, %q) = %v, 期望 %v", c.origin, c.host, got, c.want)
		}
	}
}
