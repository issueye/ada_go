package canvas

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestStoreCRUDAndPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("新存储应为空")
	}
	a := s.Add(newTestNode("a"))
	if a.ID == "" || a.Kind != KindNote || a.Width != 420 {
		t.Fatalf("Add 默认值异常: %+v", a)
	}
	b := s.Add(newTestNode("b"))
	x, y := s.AutoPosition(420, 320)
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
	if n := s2.Clear(); n != 1 {
		t.Fatalf("Clear = %d, 期望 1", n)
	}
}

func TestStorePersistFileFormat(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenStore(dir)
	a := s.Add(newTestNode("x"))
	b := s.Add(newTestNode("y"))
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
	if file.Version != 2 || len(file.Nodes) != 2 || len(file.Edges) != 1 {
		t.Fatalf("持久化内容异常: version=%d nodes=%d edges=%d", file.Version, len(file.Nodes), len(file.Edges))
	}
	if file.Edges[0].Label != "流向" {
		t.Fatalf("连线标签异常: %+v", file.Edges[0])
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
	// 迁移后的下一次写入应为 v2
	s.AddEdge("n-old-1", "n-old-1", "") // 自环报错，不写入
	s.Add(newTestNode("new"))
	raw, _ := os.ReadFile(filepath.Join(dir, "canvas.json"))
	var file persistedFile
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 2 {
		t.Fatalf("迁移后应写 v2 格式: %v", err)
	}
}

func TestEdges(t *testing.T) {
	s, _ := OpenStore("")
	a := s.Add(newTestNode("a"))
	b := s.Add(newTestNode("b"))
	c := s.Add(newTestNode("c"))
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
	s.Clear()
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
	x1, y1 := s.AutoPosition(420, 320)
	if x1 != 80 || y1 != 80 {
		t.Fatalf("空画布首个位置 = (%v,%v), 期望 (80,80)", x1, y1)
	}
	s.Add(Node{Title: "1", Width: 420, Height: 320, HTML: "x", X: x1, Y: y1})
	x2, y2 := s.AutoPosition(420, 320)
	if x2 != x1+420+48 {
		t.Fatalf("第二个节点应排在右侧: %v", x2)
	}
	if y2 != y1 {
		t.Fatalf("同行节点 Y 应一致: %v", y2)
	}
}

func TestAutoPositionWrap(t *testing.T) {
	s, _ := OpenStore("")
	s.Add(Node{Title: "wide", Width: 3400, Height: 400, HTML: "x"})
	x, y := s.AutoPosition(800, 400)
	if x != 80 {
		t.Fatalf("超宽后应回到行首 x=80, got %v", x)
	}
	if y <= 400 {
		t.Fatalf("应换到下一行 y>400, got %v", y)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s, _ := OpenStore("")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				n := s.Add(newTestNode("c"))
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

func TestNodeAPIFlow(t *testing.T) {
	_, ts := newTestServer(t)

	res, _ := doJSON(t, "POST", ts.URL+"/api/nodes", map[string]any{
		"title": "卡片", "kind": "component", "system": "volcano",
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
		"system": "fake", "components": []string{"button"},
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
func (fakeCatalog) ComposePreview(system string, comps []string, theme, title string) (string, int, int, error) {
	return "<html>preview</html>", 420, 300, nil
}

func TestCaptureFlow(t *testing.T) {
	srv, ts := newTestServer(t)
	_ = srv
	// 建一个节点
	res0(t, ts, "POST", "/api/nodes", map[string]any{
		"html": "<html><body>x</body></html>", "width": 300, "height": 200, "title": "t",
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

func TestSameLoopbackOrigin(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:8788": true, "http://localhost:8788": true,
		"https://evil.com": false, "http://127.0.0.1.evil.com": false, "": true,
	}
	for origin, want := range cases {
		if got := sameLoopbackOrigin(origin); got != want {
			t.Errorf("sameLoopbackOrigin(%q) = %v, 期望 %v", origin, got, want)
		}
	}
}
