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
	resp := c.roundTrip(t, method, params)
	if len(resp.Error) > 0 && string(resp.Error) != "null" {
		t.Fatalf("%s 返回错误: %s", method, resp.Error)
	}
	return resp.Result
}

// callError 断言方法返回错误并返回响应（用于非法参数路径）。
func (c *rpcClient) callError(t *testing.T, method string, params any) wireResponse {
	t.Helper()
	resp := c.roundTrip(t, method, params)
	if len(resp.Error) == 0 || string(resp.Error) == "null" {
		t.Fatalf("%s 期望错误，实际成功: %s", method, resp.Result)
	}
	return resp
}

func (c *rpcClient) roundTrip(t *testing.T, method string, params any) wireResponse {
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
		return resp
	case <-time.After(5 * time.Second):
		t.Fatalf("%s 超时", method)
		return wireResponse{}
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
	if plugins, _ := hello["plugins"].([]any); len(plugins) != 7 {
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

// provider-admin 的 route 后端在真实会话里可用：plugin.http 读目录、切模型。
func TestPluginHTTPProviderAdmin(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "secret")
	c := dial(t, url, "secret")

	raw := c.call(t, "plugin.http", map[string]any{
		"pluginId": "provider-admin", "method": "GET", "path": "/state",
	})
	var state struct {
		OK     bool `json:"ok"`
		Result struct {
			Providers []struct {
				ID     string `json:"id"`
				Models []struct {
					ID string `json:"id"`
				} `json:"models"`
			} `json:"providers"`
			Current struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
			} `json:"current"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("plugin.http 结果不是 JSON: %v (%s)", err, raw)
	}
	if !state.OK || len(state.Result.Providers) == 0 {
		t.Fatalf("GET /state = %s", raw)
	}
	if state.Result.Providers[0].ID != "openai" {
		t.Fatalf("供应商 = %+v", state.Result.Providers)
	}

	raw = c.call(t, "plugin.http", map[string]any{
		"pluginId": "provider-admin", "method": "POST", "path": "/select",
		"body": map[string]any{"providerId": "openai", "modelId": "gpt-4o"},
	})
	var selected struct {
		OK     bool `json:"ok"`
		Result struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		t.Fatalf("select 结果: %v (%s)", err, raw)
	}
	if !selected.OK || selected.Result.ModelID != "gpt-4o" {
		t.Fatalf("POST /select = %s", raw)
	}

	raw = c.call(t, "model.list", map[string]any{})
	var models struct {
		Current struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"current"`
	}
	if err := json.Unmarshal(raw, &models); err != nil {
		t.Fatalf("model.list: %v", err)
	}
	if models.Current.ProviderID != "openai" || models.Current.ModelID != "gpt-4o" {
		t.Fatalf("切换后的当前模型 = %+v", models.Current)
	}

	raw = c.call(t, "plugin.http", map[string]any{
		"pluginId": "provider-admin", "method": "GET", "path": "/missing",
	})
	var missing struct {
		OK       bool `json:"ok"`
		NotFound bool `json:"notFound"`
	}
	_ = json.Unmarshal(raw, &missing)
	if missing.OK || !missing.NotFound {
		t.Fatalf("未知路由 = %s", raw)
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
	// 审计（M5）：turn.cancel 落 audit 表。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := sess.Store().ListAudit(ctx, 100, time.Time{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Kind == "turn.cancel" && r.ThreadID == threadID {
			found = true
		}
	}
	if !found {
		t.Fatalf("turn.cancel 没有落审计: %+v", rows)
	}
}

// ---- M2 剩余方法 ----

func createThread(t *testing.T, c *rpcClient, title string) string {
	t.Helper()
	raw := c.call(t, "thread.create", map[string]any{"title": title})
	var out struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Thread.ID == "" {
		t.Fatalf("thread.create = %s (%v)", raw, err)
	}
	return out.Thread.ID
}

// runTurn 提交一轮并等到 completed 终态（事件流里 terminal 是最后一条）。
func runTurn(t *testing.T, c *rpcClient, threadID, idem, text string) {
	t.Helper()
	raw := c.call(t, "turn.start", map[string]any{
		"threadId": threadID, "idempotencyKey": idem, "text": text,
	})
	var started struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.RunID == "" {
		t.Fatalf("turn.start = %s (%v)", raw, err)
	}
	c.waitEvent(t, "evt.run.status", `"terminal":"completed"`)
}

type threadResult struct {
	Thread struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		HeadEntryID string `json:"headEntryId"`
		Archived    bool   `json:"archived"`
	} `json:"thread"`
}

func TestThreadLifecycleMethods(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	threadID := createThread(t, c, "lifecycle")
	runTurn(t, c, threadID, "lifecycle-1", "hello")

	var entries struct {
		Entries []struct {
			ID  string `json:"id"`
			Seq int64  `json:"seq"`
		} `json:"entries"`
	}
	raw := c.call(t, "thread.entries", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries.Entries) < 2 {
		t.Fatalf("thread.entries = %s (%v)", raw, err)
	}
	firstEntry := entries.Entries[0].ID

	// rename
	raw = c.call(t, "thread.rename", map[string]any{"threadId": threadID, "title": "renamed"})
	var renamed threadResult
	if err := json.Unmarshal(raw, &renamed); err != nil || renamed.Thread.Title != "renamed" {
		t.Fatalf("thread.rename = %s (%v)", raw, err)
	}
	raw = c.call(t, "thread.get", map[string]any{"threadId": threadID})
	var got threadResult
	if err := json.Unmarshal(raw, &got); err != nil || got.Thread.Title != "renamed" {
		t.Fatalf("thread.get after rename = %s (%v)", raw, err)
	}

	// archive / unarchive，list 与 get 都能看到状态变化
	raw = c.call(t, "thread.archive", map[string]any{"threadId": threadID, "archived": true})
	if err := json.Unmarshal(raw, &got); err != nil || !got.Thread.Archived {
		t.Fatalf("thread.archive(true) = %s (%v)", raw, err)
	}
	var list struct {
		Threads []struct {
			ID       string `json:"id"`
			Archived bool   `json:"archived"`
		} `json:"threads"`
	}
	raw = c.call(t, "thread.list", map[string]any{"archived": true})
	if err := json.Unmarshal(raw, &list); err != nil || !containsThread(list.Threads, threadID) {
		t.Fatalf("归档后 thread.list(archived=true) = %s", raw)
	}
	raw = c.call(t, "thread.list", map[string]any{"archived": false})
	if err := json.Unmarshal(raw, &list); err != nil || containsThread(list.Threads, threadID) {
		t.Fatalf("归档后 thread.list(archived=false) = %s", raw)
	}
	c.call(t, "thread.archive", map[string]any{"threadId": threadID, "archived": false})

	// branch：head 移到首条 entry
	raw = c.call(t, "thread.branch", map[string]any{"threadId": threadID, "entryId": firstEntry})
	if err := json.Unmarshal(raw, &got); err != nil || got.Thread.HeadEntryID != firstEntry {
		t.Fatalf("thread.branch = %s (%v)", raw, err)
	}

	// branch 校验 entry 属于线程
	otherID := createThread(t, c, "other")
	runTurn(t, c, otherID, "other-0001", "other")
	var otherEntries struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	raw = c.call(t, "thread.entries", map[string]any{"threadId": otherID})
	if err := json.Unmarshal(raw, &otherEntries); err != nil || len(otherEntries.Entries) == 0 {
		t.Fatalf("other thread.entries = %s (%v)", raw, err)
	}
	c.callError(t, "thread.branch", map[string]any{"threadId": threadID, "entryId": otherEntries.Entries[0].ID})

	// delete：软删后 archived=true，并在归档列表可见
	raw = c.call(t, "thread.delete", map[string]any{"threadId": threadID})
	var deleted struct {
		Deleted          bool `json:"deleted"`
		CleanupScheduled bool `json:"cleanupScheduled"`
	}
	if err := json.Unmarshal(raw, &deleted); err != nil || !deleted.Deleted || !deleted.CleanupScheduled {
		t.Fatalf("thread.delete = %s (%v)", raw, err)
	}
	raw = c.call(t, "thread.get", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &got); err != nil || !got.Thread.Archived {
		t.Fatalf("删除后 thread.get = %s (%v)", raw, err)
	}
	raw = c.call(t, "thread.list", map[string]any{"archived": true})
	if err := json.Unmarshal(raw, &list); err != nil || !containsThread(list.Threads, threadID) {
		t.Fatalf("删除后 thread.list(archived=true) = %s", raw)
	}
}

func containsThread(threads []struct {
	ID       string `json:"id"`
	Archived bool   `json:"archived"`
}, id string) bool {
	for _, th := range threads {
		if th.ID == id {
			return true
		}
	}
	return false
}

func TestWorkspaceUpdatePolicy(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	type policy struct {
		ToolTimeoutMS   int `json:"toolTimeoutMs"`
		TurnTimeoutMS   int `json:"turnTimeoutMs"`
		MaxTurnTokens   int `json:"maxTurnTokens"`
		LoopRepeatLimit int `json:"loopRepeatLimit"`
		LoopAbortLimit  int `json:"loopAbortLimit"`
		ParallelTools   int `json:"parallelTools"`
	}
	var res struct {
		Policies policy `json:"policies"`
	}
	read := func(params map[string]any) policy {
		t.Helper()
		raw := c.call(t, "workspace.updatePolicy", params)
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("workspace.updatePolicy = %s (%v)", raw, err)
		}
		return res.Policies
	}

	base := read(map[string]any{})
	if base.LoopRepeatLimit != 3 || base.LoopAbortLimit != 5 || base.ParallelTools != 1 {
		t.Fatalf("默认策略 = %+v", base)
	}

	p := read(map[string]any{"policies": map[string]any{"loopRepeatLimit": 2}})
	if p.LoopRepeatLimit != 2 || p.LoopAbortLimit != 5 {
		t.Fatalf("合并 loopRepeatLimit 后 = %+v", p)
	}
	p = read(map[string]any{"policies": map[string]any{"parallelTools": 4, "toolTimeoutMs": 1000}})
	if p.LoopRepeatLimit != 2 || p.LoopAbortLimit != 5 || p.ParallelTools != 4 || p.ToolTimeoutMS != 1000 {
		t.Fatalf("合并 parallelTools/toolTimeoutMs 后 = %+v", p)
	}
	p = read(map[string]any{"policies": map[string]any{"turnTimeoutMs": 1234, "maxTurnTokens": 9999}})
	if p.TurnTimeoutMS != 1234 || p.MaxTurnTokens != 9999 || p.ParallelTools != 4 {
		t.Fatalf("合并 turnTimeoutMs/maxTurnTokens 后 = %+v", p)
	}

	for _, bad := range []map[string]any{
		{"policies": map[string]any{"loopAbortLimit": 2}},  // == loopRepeatLimit
		{"policies": map[string]any{"loopRepeatLimit": 9}}, // abort(5) <= repeat(9)
		{"policies": map[string]any{"loopRepeatLimit": 0}},
		{"policies": map[string]any{"parallelTools": 0}},
		{"policies": map[string]any{"toolTimeoutMs": -1}},
	} {
		c.callError(t, "workspace.updatePolicy", bad)
	}
}

func TestModelSetScopes(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	var catalog struct {
		Models []struct {
			Model struct {
				ProviderID string `json:"providerId"`
				ModelID    string `json:"modelId"`
			} `json:"model"`
		} `json:"models"`
	}
	raw := c.call(t, "model.list", map[string]any{})
	if err := json.Unmarshal(raw, &catalog); err != nil || len(catalog.Models) < 2 {
		t.Fatalf("model.list = %s (%v)", raw, err)
	}
	wsModel := catalog.Models[0].Model
	threadModel := catalog.Models[1].Model
	threadID := createThread(t, c, "model-scope")

	raw = c.call(t, "model.set", map[string]any{
		"model": map[string]any{"providerId": wsModel.ProviderID, "modelId": wsModel.ModelID},
	})
	var set struct {
		Model struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"model"`
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &set); err != nil || set.Scope != "workspace" ||
		set.Model.ProviderID != wsModel.ProviderID || set.Model.ModelID != wsModel.ModelID {
		t.Fatalf("model.set workspace = %s (%v)", raw, err)
	}

	raw = c.call(t, "model.set", map[string]any{
		"model":    map[string]any{"providerId": threadModel.ProviderID, "modelId": threadModel.ModelID},
		"threadId": threadID,
	})
	if err := json.Unmarshal(raw, &set); err != nil || set.Scope != "thread" ||
		set.Model.ProviderID != threadModel.ProviderID || set.Model.ModelID != threadModel.ModelID {
		t.Fatalf("model.set thread = %s (%v)", raw, err)
	}

	var current struct {
		Current struct {
			ProviderID string `json:"providerId"`
			ModelID    string `json:"modelId"`
		} `json:"current"`
	}
	raw = c.call(t, "model.list", map[string]any{"threadId": threadID})
	if err := json.Unmarshal(raw, &current); err != nil ||
		current.Current.ProviderID != threadModel.ProviderID || current.Current.ModelID != threadModel.ModelID {
		t.Fatalf("线程当前模型 = %s (%v)", raw, err)
	}
	raw = c.call(t, "model.list", map[string]any{})
	if err := json.Unmarshal(raw, &current); err != nil ||
		current.Current.ProviderID != wsModel.ProviderID || current.Current.ModelID != wsModel.ModelID {
		t.Fatalf("工作区当前模型 = %s (%v)", raw, err)
	}

	c.callError(t, "model.set", map[string]any{
		"model": map[string]any{"providerId": "no-such-provider", "modelId": "x"},
	})
	c.callError(t, "model.set", map[string]any{
		"model": map[string]any{"providerId": wsModel.ProviderID, "modelId": "no-such-model"},
	})
}

type usageShape struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	CacheTokens  int    `json:"cacheTokens"`
	CostMicros   int64  `json:"costMicros"`
}

func (u usageShape) total() int { return u.InputTokens + u.OutputTokens + u.CacheTokens }

func TestUsageGetAggregates(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")
	threadID := createThread(t, c, "usage")
	runTurn(t, c, threadID, "usage-0001", "hello usage")

	var res struct {
		Thread    usageShape `json:"thread"`
		Workspace usageShape `json:"workspace"`
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		raw := c.call(t, "usage.get", map[string]any{"threadId": threadID})
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("usage.get = %s (%v)", raw, err)
		}
		if res.Thread.total() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("thread 用量仍为零: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res.Thread.Provider != "fake" || res.Thread.Model != "fake-1" {
		t.Fatalf("thread 用量归属 = %+v", res.Thread)
	}
	if res.Workspace.total() < res.Thread.total() {
		t.Fatalf("workspace=%+v thread=%+v", res.Workspace, res.Thread)
	}

	raw := c.call(t, "usage.get", map[string]any{})
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("usage.get workspace = %s (%v)", raw, err)
	}
	if res.Thread != (usageShape{}) {
		t.Fatalf("未给 threadId 时 thread 应为零值: %+v", res.Thread)
	}
	if res.Workspace.total() == 0 {
		t.Fatalf("workspace 用量为零: %s", raw)
	}
}

func TestThreadGetItemsProjection(t *testing.T) {
	model := testkit.NewFakeProvider(testkit.NewScriptedModel(
		testkit.ToolTurn("call_1", "read_file", `{"path":"missing.txt"}`),
		testkit.TextTurn("done"),
	))
	sess := newSession(t, model)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")
	threadID := createThread(t, c, "items")
	runTurn(t, c, threadID, "items-0001", "read something")

	type item struct {
		Key         string          `json:"key"`
		Type        string          `json:"type"`
		At          string          `json:"at"`
		Fingerprint string          `json:"fingerprint"`
		Payload     json.RawMessage `json:"payload"`
	}
	readItems := func(limit int) ([]item, bool) {
		t.Helper()
		raw := c.call(t, "thread.get", map[string]any{"threadId": threadID, "itemLimit": limit})
		var snap struct {
			Items   []item `json:"items"`
			Seq     int64  `json:"seq"`
			HasMore bool   `json:"hasMore"`
		}
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatalf("thread.get = %s (%v)", raw, err)
		}
		if snap.Seq <= 0 {
			t.Fatalf("thread.get seq = %d", snap.Seq)
		}
		return snap.Items, snap.HasMore
	}

	items, hasMore := readItems(100)
	if len(items) < 4 {
		t.Fatalf("items = %+v", items)
	}
	if hasMore {
		t.Fatal("全量窗口不应 hasMore")
	}
	var sawMsg, sawTool, sawAssistant bool
	for _, it := range items {
		if len(it.Fingerprint) != 16 {
			t.Fatalf("fingerprint = %q", it.Fingerprint)
		}
		if it.At == "" || len(it.Payload) == 0 {
			t.Fatalf("item 缺 at/payload: %+v", it)
		}
		switch {
		case it.Type == "tool" && it.Key == "tool:call_1":
			sawTool = true
		case it.Type == "message" && strings.HasPrefix(it.Key, "msg:"):
			sawMsg = true
			var body struct {
				Role string `json:"role"`
			}
			_ = json.Unmarshal(it.Payload, &body)
			if body.Role == "assistant" {
				sawAssistant = true
			}
		default:
			t.Fatalf("意外的 item: %+v", it)
		}
	}
	if !sawMsg || !sawTool || !sawAssistant {
		t.Fatalf("缺失条目: msg=%v tool=%v assistant=%v items=%+v", sawMsg, sawTool, sawAssistant, items)
	}

	limited, limitedMore := readItems(2)
	if len(limited) != 2 {
		t.Fatalf("itemLimit=2 得到 %d 条", len(limited))
	}
	if !limitedMore {
		t.Fatal("窗口外仍应有更早 items")
	}
	one, _ := readItems(1)
	if len(one) != 1 {
		t.Fatalf("itemLimit=1 得到 %d 条", len(one))
	}
}

// TestCoreRPCChangeListAndRevert 是 change.list / change.revert 的协议面验收（M5）：
// 参数校验、空账目形状与回滚错误路径。完整的「写文件→checkpoint→revert」领域
// 链路在 internal/cli 的 changes_test.go（真实工具执行 + 持久化快照）。
func TestCoreRPCChangeListAndRevert(t *testing.T) {
	sess := newSession(t, nil)
	url, _ := startServer(t, sess, "")
	c := dial(t, url, "")

	// 空账目：changeSets 是空数组而不是 null，hasMore=false。
	res := c.call(t, "change.list", map[string]any{})
	var listOut struct {
		ChangeSets []json.RawMessage `json:"changeSets"`
		NextCursor string            `json:"nextCursor"`
		HasMore    bool              `json:"hasMore"`
	}
	if err := json.Unmarshal(res, &listOut); err != nil {
		t.Fatalf("change.list 解码: %v", err)
	}
	if listOut.ChangeSets == nil || len(listOut.ChangeSets) != 0 || listOut.HasMore {
		t.Fatalf("空账目形状不对: %s", string(res))
	}

	// limit 越界：invalid_params。
	if resp := c.callError(t, "change.list", map[string]any{"limit": 500}); !strings.Contains(string(resp.Error), "limit") {
		t.Fatalf("limit=500 应当报 limit 越界: %s", string(resp.Error))
	}
	// 游标非法：invalid_params。
	c.callError(t, "change.list", map[string]any{"cursor": "!!not-base64!!"})

	// change.revert 参数校验。
	c.callError(t, "change.revert", map[string]any{"scope": "item"})                    // 缺 threadId
	c.callError(t, "change.revert", map[string]any{"threadId": "th", "scope": "bogus"}) // scope 非法
	c.callError(t, "change.revert", map[string]any{"threadId": "th", "scope": "item"})  // 缺 checkpointId
	c.callError(t, "change.revert", map[string]any{"threadId": "th", "scope": "file"})  // 缺 path

	// 未知的检查点/线程：错误而不是成功（不谎报）。
	c.callError(t, "change.revert", map[string]any{
		"threadId": "th_missing", "scope": "item", "checkpointId": "chk_missing"})
	c.callError(t, "change.revert", map[string]any{
		"threadId": "th_missing", "scope": "file", "path": "/workspace/x.txt"})

	// scope=all 且线程没有检查点：同样报错。
	c.callError(t, "change.revert", map[string]any{"threadId": "th_missing", "scope": "all"})
}
