package canvas

// 迭代 1（P0 加固）的行为验收：
//   #1 持久化：紧凑序列化、备份轮换与降级恢复、写盘失败可见
//   #4 列表剥 HTML、单节点正文接口、SSE 变更推送、Store 版本号
//   #3 节点部分更新的指针语义（区分未传 / 显式置空）
//   #2 同数据目录多实例探测

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------- #1 持久化加固 ----------------

func TestPersistBackupRotationAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pid := newTestProject(t, s)
	a := addNode(t, s, pid, newTestNode("a"))
	addNode(t, s, pid, newTestNode("b")) // 第二次写盘后 .bak 保有上一代快照（含 a）

	raw, err := os.ReadFile(filepath.Join(dir, "canvas.json"))
	if err != nil {
		t.Fatalf("canvas.json 未生成: %v", err)
	}
	// 紧凑序列化：快照是给机器读的，不应再有缩进
	if bytes.Contains(raw, []byte("\n  \"")) {
		t.Fatal("canvas.json 应为紧凑 JSON，不含缩进")
	}
	if _, err := os.Stat(filepath.Join(dir, "canvas.json.bak")); err != nil {
		t.Fatalf("备份未生成: %v", err)
	}

	// 主文件损坏 → 从 .bak 降级恢复（至少要捡回 a）
	if err := os.WriteFile(filepath.Join(dir, "canvas.json"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatalf("写损坏文件: %v", err)
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("应从备份恢复而不是报错: %v", err)
	}
	if _, err := s2.Get(a.ID); err != nil {
		t.Fatalf("备份里应有节点 a: %v", err)
	}
}

func TestPersistErrorRecordedAndHealthVisible(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 把存储路径指向不存在的子目录 → 每次写盘必败（内存数据不受影响）
	s.mu.Lock()
	s.path = filepath.Join(dir, "missing-sub", "canvas.json")
	s.mu.Unlock()

	pid := newTestProject(t, s)
	addNode(t, s, pid, newTestNode("a"))
	if s.PersistError() == nil {
		t.Fatal("写盘失败应记录 PersistError")
	}
	if len(s.List()) != 1 {
		t.Fatal("写盘失败不应丢内存数据")
	}

	srv := NewServer(s, NewTaskQueue(), "")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	_, data := doJSON(t, "GET", ts.URL+"/api/health", nil)
	if pe, _ := data["persist_error"].(string); pe == "" {
		t.Fatalf("健康检查应暴露 persist_error: %v", data)
	}
	if _, ok := data["rev"]; !ok {
		t.Fatalf("健康检查应含 rev: %v", data)
	}
	if _, ok := data["data_dir"]; !ok {
		t.Fatalf("健康检查应含 data_dir: %v", data)
	}

	// 路径修好后，下一次变更应清掉错误标记
	s.mu.Lock()
	s.path = filepath.Join(dir, "canvas.json")
	s.mu.Unlock()
	addNode(t, s, pid, newTestNode("b"))
	if s.PersistError() != nil {
		t.Fatalf("写盘恢复后 PersistError 应清空: %v", s.PersistError())
	}
}

// ---------------- #4 版本号 / 订阅 / 列表形状 ----------------

func TestRevAndSubscription(t *testing.T) {
	s, _ := OpenStore("") // 内存库也推进 rev（rev 不依赖写盘）
	before := s.Rev()
	ch, cancel := s.Subscribe()
	defer cancel()

	pid := newTestProject(t, s)
	addNode(t, s, pid, newTestNode("a"))
	if s.Rev() <= before {
		t.Fatalf("变更应推进 rev: %d -> %d", before, s.Rev())
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("订阅者应收到变更通知")
	}

	cancel()
	addNode(t, s, pid, newTestNode("b"))
	select {
	case <-ch:
		t.Fatal("取消订阅后不应再收到通知")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestNodeListStripsHTML(t *testing.T) {
	_, ts := newTestServer(t)
	pid := newHTTPProject(t, ts)
	res0(t, ts, "POST", "/api/nodes", map[string]any{
		"project": pid, "title": "带正文", "html": "<html><body>x</body></html>",
		"width": 300, "height": 200,
	})

	// 默认列表：html 键整个消失，html_rev 存在
	nodes := dataOf(t, res0(t, ts, "GET", "/api/nodes?project="+pid, nil), "nodes").([]any)
	node := nodes[0].(map[string]any)
	if _, has := node["html"]; has {
		t.Fatal("默认列表不应含 html 键")
	}
	if _, has := node["html_rev"]; !has {
		t.Fatal("列表应含 html_rev（客户端据此判断要不要取正文）")
	}
	id := node["id"].(string)

	// include=html：带正文
	full := dataOf(t, res0(t, ts, "GET", "/api/nodes?project="+pid+"&include=html", nil), "nodes").([]any)[0].(map[string]any)
	if full["html"] == "" {
		t.Fatal("include=html 应返回正文")
	}
	// 单节点接口：带正文
	one := res0(t, ts, "GET", "/api/nodes/"+id, nil)["node"].(map[string]any)
	if one["html"] == "" {
		t.Fatal("单节点接口应返回正文")
	}
	if got := statusCode(t, ts, "GET", "/api/nodes/n-missing", nil); got != http.StatusNotFound {
		t.Fatalf("缺失节点应 404, got %d", got)
	}
}

func TestEventsSSE(t *testing.T) {
	srv, ts := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %s", ct)
	}

	rd := bufio.NewReader(res.Body)
	readChange := func() string {
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("读流中断: %v", err)
			}
			if strings.HasPrefix(line, "event: change") {
				dataLine, _ := rd.ReadString('\n')
				return dataLine
			}
		}
	}

	// 连接建立即推一条当前 rev
	if first := readChange(); !strings.Contains(first, `"rev":`) {
		t.Fatalf("首条事件缺 rev: %s", first)
	}
	// 存储变更 → 再推一条（rev 单调递增）
	pid := newTestProject(t, srv.store)
	addNode(t, srv.store, pid, newTestNode("a"))
	if second := readChange(); !strings.Contains(second, `"rev":`) {
		t.Fatalf("变更事件缺 rev: %s", second)
	}
}

// ---------------- #3 部分更新的指针语义 ----------------

func TestUpdatePointerSemantics(t *testing.T) {
	_, ts := newTestServer(t)
	pid := newHTTPProject(t, ts)
	res0(t, ts, "POST", "/api/nodes", map[string]any{
		"project": pid, "title": "原标题", "html": "<p>v1</p>",
		"width": 300, "height": 200,
	})
	fetchNode := func() map[string]any {
		return dataOf(t, res0(t, ts, "GET", "/api/nodes?project="+pid+"&include=html", nil), "nodes").([]any)[0].(map[string]any)
	}
	node := fetchNode()
	id := node["id"].(string)
	rev0 := node["html_rev"].(string)

	// 只挪位置：标题/正文/html_rev 都不动（正文断言用包含 —— 入库时 HTML 会前置 CSP meta）
	res0(t, ts, "PUT", "/api/nodes/"+id, map[string]any{"x": 100.0, "y": 50.0})
	node = fetchNode()
	if node["title"] != "原标题" || !strings.Contains(node["html"].(string), "<p>v1</p>") {
		t.Fatalf("位置更新不应动标题/正文: %v", node)
	}
	if node["html_rev"] != rev0 {
		t.Fatal("位置更新不应推进 html_rev")
	}

	// 显式空串 = 置空（旧实现用零值判断，标题永远清不掉）
	res0(t, ts, "PUT", "/api/nodes/"+id, map[string]any{"title": ""})
	if node = fetchNode(); node["title"] != "" {
		t.Fatalf("显式空串应能清空标题: %v", node["title"])
	}

	// 换正文：html 与 html_rev 同步变化
	res0(t, ts, "PUT", "/api/nodes/"+id, map[string]any{"html": "<p>v2</p>"})
	node = fetchNode()
	if !strings.Contains(node["html"].(string), "<p>v2</p>") {
		t.Fatalf("正文应更新: %v", node["html"])
	}
	if node["html_rev"] == rev0 {
		t.Fatal("换正文应推进 html_rev")
	}

	// 未传字段（width/height/x/y/title…）保持原值
	if node["width"].(float64) != 300 || node["height"].(float64) != 200 {
		t.Fatalf("未传字段不应被改: %v", node)
	}
}

// ---------------- #2 多实例探测 ----------------

func TestProbeInstance(t *testing.T) {
	dir := t.TempDir()
	srv, ts := newTestServer(t)
	srv.DataDir = dir
	port, err := strconv.Atoi(strings.TrimPrefix(ts.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatalf("解析端口: %v", err)
	}

	url, hit := ProbeInstance(port, dir)
	if !hit {
		t.Fatal("同数据目录的实例应被探测到")
	}
	if url != ts.URL {
		t.Fatalf("探测地址 = %s, 期望 %s", url, ts.URL)
	}
	// 不同数据目录 = 不冲突（各自占各自端口，可共存）
	if _, hit := ProbeInstance(port, filepath.Join(dir, "elsewhere")); hit {
		t.Fatal("不同数据目录不应算冲突")
	}
}
