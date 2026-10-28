package corerpc_test

// Core RPC 的第一个垂直切片验收：真实装配（官方插件 × moejs × SQLite）
// + WebSocket JSON-RPC + 事件通知 + 轮次生命周期。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/adag/ada-go/internal/cli"
	"github.com/adag/ada-go/internal/conformance/testkit"
	"github.com/adag/ada-go/internal/corerpc"
	"github.com/adag/ada-go/internal/ports"
)

func productPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "products", "headless.agent.json")
}

func newSession(t *testing.T, model ports.ModelClient) *cli.Session {
	t.Helper()
	sess, err := cli.Open(context.Background(), cli.Config{
		Workspace:   t.TempDir(),
		DataDir:     t.TempDir(),
		Product:     productPath(t),
		Fake:        model == nil,
		ModelClient: model,
		In:          strings.NewReader(""),
		Out:         discard{},
	})
	if err != nil {
		t.Fatalf("cli.Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	return sess
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func startServer(t *testing.T, sess *cli.Session, token string) (string, *corerpc.Server) {
	t.Helper()
	srv, err := corerpc.New(corerpc.Options{Session: sess, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		ts.Close()
	})
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/rpc", srv
}

type rpcClient struct {
	conn *websocket.Conn

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan wireResponse
	events  chan wireResponse
}

type wireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
	Params  json.RawMessage `json:"params"`
}

func dial(t *testing.T, url, token string) *rpcClient {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	c := &rpcClient{conn: conn, pending: map[int64]chan wireResponse{}, events: make(chan wireResponse, 1024)}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	go c.readLoop()
	return c
}

func (c *rpcClient) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var msg wireResponse
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		if msg.Method != "" && msg.ID == 0 {
			c.events <- msg
			continue
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (c *rpcClient) call(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan wireResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	raw, _ := json.Marshal(params)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(raw)})
	if err := c.conn.Write(context.Background(), websocket.MessageText, req); err != nil {
		t.Fatalf("%s 写失败: %v", method, err)
	}
	select {
	case resp := <-ch:
		if len(resp.Error) > 0 && string(resp.Error) != "null" {
			t.Fatalf("%s 返回错误: %s", method, resp.Error)
		}
		return resp.Result
	case <-time.After(5 * time.Second):
		t.Fatalf("%s 超时", method)
		return nil
	}
}

func (c *rpcClient) waitEvent(t *testing.T, method, statuses string) wireResponse {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-c.events:
			if ev.Method != method {
				continue
			}
			if statuses == "" || strings.Contains(string(ev.Params), statuses) {
				return ev
			}
		case <-deadline:
			t.Fatalf("等待事件 %s(%s) 超时", method, statuses)
		}
	}
}

func TestCoreRPCThreadAndTurn(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")

	var hello map[string]any
	raw := c.call(t, "system.hello", map[string]any{"protoVersion": 1})
	if err := json.Unmarshal(raw, &hello); err != nil {
		t.Fatal(err)
	}
	if hello["productId"] != "headless" || hello["pid"] == nil {
		t.Fatalf("hello = %v", hello)
	}
	if plugins, _ := hello["plugins"].([]any); len(plugins) != 5 {
		t.Fatalf("plugins = %v", hello["plugins"])
	}
	if ws, _ := hello["workspace"].(map[string]any); ws["workspaceKey"] == "" {
		t.Fatalf("workspace = %v", hello["workspace"])
	}

	var created struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	raw = c.call(t, "thread.create", map[string]any{"title": "rpc"})
	if err := json.Unmarshal(raw, &created); err != nil || created.Thread.ID == "" {
		t.Fatalf("thread.create = %s (%v)", raw, err)
	}
	threadID := created.Thread.ID

	var started struct {
		RunID     string `json:"runId"`
		Duplicate bool   `json:"duplicate"`
	}
	params := map[string]any{"threadId": threadID, "idempotencyKey": "idem-0001", "text": "hello rpc"}
	raw = c.call(t, "turn.start", params)
	if err := json.Unmarshal(raw, &started); err != nil || started.RunID == "" {
		t.Fatalf("turn.start = %s (%v)", raw, err)
	}
	if started.Duplicate {
		t.Fatal("首次提交不应是 duplicate")
	}

	// 事件：至少一条文本增量 + 恰好一个 completed 终态。
	var text strings.Builder
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-c.events:
			if ev.Method == "evt.message.delta" {
				var d struct {
					Text string `json:"text"`
				}
				if json.Unmarshal(ev.Params, &d) == nil {
					text.WriteString(d.Text)
				}
			}
			if ev.Method == "evt.run.status" && strings.Contains(string(ev.Params), `"terminal":"completed"`) {
				if !strings.Contains(text.String(), "fake:") {
					t.Fatalf("文本增量 = %q", text.String())
				}
				goto done
			}
		case <-deadline:
			t.Fatalf("没有收到 completed 终态；文本=%q", text.String())
		}
	}
done:

	// 幂等：同 key 返回首次 runId。
	raw = c.call(t, "turn.start", params)
	var dup struct {
		RunID     string `json:"runId"`
		Duplicate bool   `json:"duplicate"`
	}
	if err := json.Unmarshal(raw, &dup); err != nil || !dup.Duplicate || dup.RunID != started.RunID {
		t.Fatalf("重复提交 = %s", raw)
	}

	var state struct {
		Run struct {
			Status   string `json:"status"`
			Terminal string `json:"terminal"`
		} `json:"run"`
	}
	raw = c.call(t, "turn.get", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &state); err != nil || state.Run.Status != "terminal" || state.Run.Terminal != "completed" {
		t.Fatalf("turn.get = %s", raw)
	}

	var entries struct {
		Entries []struct {
			Seq int64 `json:"seq"`
		} `json:"entries"`
	}
	raw = c.call(t, "thread.entries", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries.Entries) < 2 {
		t.Fatalf("thread.entries = %s", raw)
	}

	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	raw = c.call(t, "tool.list", map[string]any{})
	if err := json.Unmarshal(raw, &tools); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tool.list = %s", raw)
	}

	var models struct {
		Models []any `json:"models"`
	}
	raw = c.call(t, "model.list", map[string]any{})
	if err := json.Unmarshal(raw, &models); err != nil || len(models.Models) == 0 {
		t.Fatalf("model.list = %s", raw)
	}
}

func TestCoreRPCRequiresToken(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	if _, _, err := websocket.Dial(context.Background(), url, nil); err == nil {
		t.Fatal("无 token 的连接必须被拒绝")
	}
	badOrigin := http.Header{}
	badOrigin.Set("Authorization", "Bearer secret")
	badOrigin.Set("Origin", "http://evil.example")
	if _, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: badOrigin}); err == nil {
		t.Fatal("非回环 Origin 必须被拒绝")
	}
	c := dial(t, url, "secret")
	c.call(t, "system.ping", map[string]any{"echo": "x"})
}

func TestCoreRPCCancel(t *testing.T) {
	model := testkit.NewFakeProvider(testkit.NewScriptedModel([]testkit.ScriptStep{
		testkit.Idle(30 * time.Second),
		testkit.Finish("stop", "[DONE]"),
	}))
	sess := newSession(t, model)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	var created struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	raw := c.call(t, "thread.create", map[string]any{"title": "cancel"})
	_ = json.Unmarshal(raw, &created)
	threadID := created.Thread.ID

	c.call(t, "turn.start", map[string]any{"threadId": threadID, "idempotencyKey": "cancel-01", "text": "wait"})
	time.Sleep(100 * time.Millisecond)

	var cancelled struct {
		Run struct {
			Status string `json:"status"`
		} `json:"run"`
	}
	raw = c.call(t, "turn.cancel", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &cancelled); err != nil {
		t.Fatalf("turn.cancel = %s", raw)
	}
	ev := c.waitEvent(t, "evt.run.status", `"terminal":"cancelled"`)
	if !strings.Contains(string(ev.Params), `"terminal":"cancelled"`) {
		t.Fatalf("终态事件 = %s", ev.Params)
	}
}
