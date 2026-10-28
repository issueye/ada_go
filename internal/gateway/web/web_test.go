package web_test

// SSR 外壳的垂直切片验收：假 Supervisor + 假 CoreClient 驱动全部主流程，
// 不依赖真实 Core、SQLite 或网络。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/web"
)

// ---- 测试替身 ----

type callRecord struct {
	Method string
	Params map[string]any
}

type fakeCore struct {
	mu        sync.Mutex
	responses map[string]json.RawMessage
	errs      map[string]error
	calls     []callRecord
}

func newFakeCore() *fakeCore {
	return &fakeCore{
		responses: map[string]json.RawMessage{},
		errs:      map[string]error{},
	}
}

func (f *fakeCore) on(method, raw string) *fakeCore {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[method] = json.RawMessage(raw)
	return f
}

func (f *fakeCore) fail(method string, err error) *fakeCore {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[method] = err
	return f
}

func (f *fakeCore) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := callRecord{Method: method}
	if m, ok := params.(map[string]any); ok {
		rec.Params = make(map[string]any, len(m))
		for k, v := range m {
			rec.Params[k] = v
		}
	}
	f.calls = append(f.calls, rec)
	if err := f.errs[method]; err != nil {
		return nil, err
	}
	raw, ok := f.responses[method]
	if !ok {
		return nil, errors.New("fake: 未配置方法 " + method)
	}
	return raw, nil
}

func (f *fakeCore) Subscribe(int) (<-chan contracts.Event, func()) { return nil, func() {} }
func (f *fakeCore) Close() error                                   { return nil }

func (f *fakeCore) lastCall(method string) (callRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method {
			return f.calls[i], true
		}
	}
	return callRecord{}, false
}

func (f *fakeCore) countCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

type fakeSupervisor struct {
	core     contracts.CoreClient
	mu       sync.Mutex
	acquires int
	releases int
}

func (f *fakeSupervisor) Acquire(ctx context.Context, workspace string) (contracts.CoreClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	return f.core, nil
}

func (f *fakeSupervisor) Release(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
}

func (f *fakeSupervisor) Shutdown(context.Context) error { return nil }
func (f *fakeSupervisor) List() []contracts.WorkspaceStatus {
	return nil
}

// ---- 测试工具 ----

func newServer(t *testing.T, core *fakeCore) (*web.Server, *fakeSupervisor) {
	t.Helper()
	sup := &fakeSupervisor{core: core}
	srv, err := web.New(web.Config{
		Supervisor:  sup,
		Workspace:   "/tmp/ws",
		ProductName: "测试产品",
		Locale:      "zh-CN",
		Identity:    "tester",
		CSRF:        func(*http.Request) string { return "csrf-token" },
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, sup
}

func serve(t *testing.T, srv *web.Server, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func wantContains(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("页面缺少 %q\n--- body ---\n%s", want, body)
	}
}

func wantNotContains(t *testing.T, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Fatalf("页面不应包含 %q\n--- body ---\n%s", unwanted, body)
	}
}

const threadGetRunning = `{
  "thread": {"id":"th_1","workspace":"/tmp/ws","title":"测试会话","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},
  "items": [],
  "seq": 3,
  "run": {"runId":"run_1","status":"running","startedAt":"2026-10-28T10:00:00Z"},
  "interactions": [{
    "id":"ir_1","threadId":"th_1","runId":"run_1","pluginId":"policy-default",
    "kind":"select","title":"批准操作","body":"是否允许执行？",
    "options":["允许一次","拒绝"],"status":"pending","createdAt":"2026-10-28T10:00:01Z"
  }],
  "hasMore": false
}`

const entriesDescending = `{
  "entries": [
    {
      "id":"e3","threadId":"th_1","seq":3,"kind":"message","role":"tool",
      "payload":{"callId":"call_1","tool":"read_file","status":"ok",
        "content":[{"type":"text","text":"file body"}],
        "receipt":{"status":"ok","durationMs":12,"truncated":true}},
      "createdAt":"2026-10-28T10:00:03Z"
    },
    {
      "id":"e2","threadId":"th_1","seq":2,"kind":"message","role":"assistant",
      "payload":{"role":"assistant","parts":[
        {"type":"thinking","text":"先想一下"},
        {"type":"text","text":"答案是 42"}
      ]},
      "createdAt":"2026-10-28T10:00:02Z"
    },
    {
      "id":"e1","threadId":"th_1","seq":1,"kind":"message","role":"user",
      "payload":{"role":"user","parts":[{"type":"text","text":"这是问题"}]},
      "createdAt":"2026-10-28T10:00:01Z"
    }
  ],
  "hasMore": false
}`

// ---- 用例 ----

func TestHomeRendersThreadList(t *testing.T) {
	core := newFakeCore().on("thread.list", `{
	  "threads":[
	    {"id":"th_1","title":"第一个会话","updatedAt":"2026-10-28T10:00:00Z","archived":false},
	    {"id":"th_2","title":"第二个会话","updatedAt":"2026-10-28T09:00:00Z","archived":true}
	  ],
	  "nextCursor":"","hasMore":false
	}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	wantContains(t, body, "第一个会话")
	wantContains(t, body, "第二个会话")
	wantContains(t, body, `action="/threads"`)
	wantContains(t, body, "新会话")
	wantContains(t, body, "最近会话")
	wantContains(t, body, `href="/thread/th_1"`)

	call, ok := core.lastCall("thread.list")
	if !ok {
		t.Fatal("没有调用 thread.list")
	}
	if got := call.Params["limit"]; got != 20 {
		t.Fatalf("thread.list limit = %v, want 20", got)
	}
}

func TestCreateThreadCallsCoreAndRedirects(t *testing.T) {
	core := newFakeCore().on("thread.create", `{"thread":{"id":"th_new","workspace":"/tmp/ws","title":"新想法","createdAt":"2026-10-28T10:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false}}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads", url.Values{"title": {"新想法"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /threads = %d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/thread/th_new" {
		t.Fatalf("Location = %q, want /thread/th_new", loc)
	}
	call, ok := core.lastCall("thread.create")
	if !ok {
		t.Fatal("没有调用 thread.create")
	}
	if call.Params["title"] != "新想法" {
		t.Fatalf("thread.create title = %v", call.Params["title"])
	}
}

func TestThreadPageRendersTranscriptToolCardAndTray(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesDescending)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// 线程头、运行状态与无 JS 轮询。
	wantContains(t, body, "测试会话")
	wantContains(t, body, "运行中")
	wantContains(t, body, `http-equiv="refresh"`)
	wantContains(t, body, `content="2"`)

	// 用户消息 / 思考 / 助手正文。
	wantContains(t, body, "这是问题")
	wantContains(t, body, "先想一下")
	wantContains(t, body, "答案是 42")

	// 工具卡字段。
	wantContains(t, body, "read_file")
	wantContains(t, body, "call_1")
	wantContains(t, body, "12 ms")
	wantContains(t, body, "已截断")

	// 交互托盘：select 表单与选项。
	wantContains(t, body, "批准操作")
	wantContains(t, body, "允许一次")
	wantContains(t, body, `name="decision"`)
	wantContains(t, body, `/thread/th_1/cancel`)

	// Composer 表单。
	wantContains(t, body, `action="/thread/th_1/turn"`)
	wantContains(t, body, `name="text"`)

	call, ok := core.lastCall("thread.entries")
	if !ok {
		t.Fatal("没有调用 thread.entries")
	}
	if got := call.Params["limit"]; got != 100 {
		t.Fatalf("thread.entries limit = %v, want 100", got)
	}
}

func TestTurnStartUsesTextAndIdempotencyKey(t *testing.T) {
	core := newFakeCore().on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/thread/th_1/turn", url.Values{
		"text":           {"你好，世界"},
		"idempotencyKey": {"abcdefgh"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("turn.start = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/thread/th_1" {
		t.Fatalf("Location = %q", loc)
	}
	call, _ := core.lastCall("turn.start")
	if call.Params["text"] != "你好，世界" {
		t.Fatalf("text = %v", call.Params["text"])
	}
	if call.Params["idempotencyKey"] != "abcdefgh" {
		t.Fatalf("idempotencyKey = %v", call.Params["idempotencyKey"])
	}
	if call.Params["threadId"] != "th_1" {
		t.Fatalf("threadId = %v", call.Params["threadId"])
	}
}

func TestTurnStartGeneratesKeyAndRejectsEmptyText(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	rec := serve(t, srv, "POST", "/thread/th_1/turn", url.Values{"text": {"   "}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空消息 = %d, want 400", rec.Code)
	}
	wantContains(t, rec.Body.String(), "消息不能为空")

	core := newFakeCore().on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv2, _ := newServer(t, core)
	rec = serve(t, srv2, "POST", "/thread/th_1/turn", url.Values{"text": {"你好"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("缺省幂等键 = %d, want 303", rec.Code)
	}
	call, _ := core.lastCall("turn.start")
	key, _ := call.Params["idempotencyKey"].(string)
	if len(key) < 8 || len(key) > 64 {
		t.Fatalf("生成的幂等键长度不合法: %q", key)
	}
}

func TestCancelCallsTurnCancel(t *testing.T) {
	core := newFakeCore().on("turn.cancel", `{"run":{"runId":"run_1","status":"terminal","terminal":"cancelled","startedAt":"2026-10-28T10:00:00Z"}}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/thread/th_1/cancel", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("turn.cancel = %d", rec.Code)
	}
	call, ok := core.lastCall("turn.cancel")
	if !ok {
		t.Fatal("没有调用 turn.cancel")
	}
	if call.Params["threadId"] != "th_1" {
		t.Fatalf("threadId = %v", call.Params["threadId"])
	}
}

func TestInteractionRespondMapsDecisionAndValue(t *testing.T) {
	const resolved = `{"interactionId":"ir_1","threadId":"th_1","status":"accepted","resolvedAt":"2026-10-28T10:00:02Z"}`
	core := newFakeCore().on("interaction.respond", resolved)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/thread/th_1/interaction/ir_1", url.Values{
		"decision": {"accept"},
		"kind":     {"input"},
		"value":    {"允许"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("interaction.respond = %d (body=%s)", rec.Code, rec.Body.String())
	}
	call, ok := core.lastCall("interaction.respond")
	if !ok {
		t.Fatal("没有调用 interaction.respond")
	}
	if call.Params["requestId"] != "ir_1" || call.Params["decision"] != "accept" {
		t.Fatalf("参数映射错误: %#v", call.Params)
	}
	value, ok := call.Params["value"].(map[string]any)
	if !ok || value["text"] != "允许" {
		t.Fatalf("input value = %#v", call.Params["value"])
	}

	// select 映射到 option。
	rec = serve(t, srv, "POST", "/thread/th_1/interaction/ir_1", url.Values{
		"decision": {"accept"},
		"kind":     {"select"},
		"value":    {"允许一次"},
	})
	call, _ = core.lastCall("interaction.respond")
	value, _ = call.Params["value"].(map[string]any)
	if value["option"] != "允许一次" {
		t.Fatalf("select value = %#v", call.Params["value"])
	}

	// deny 不携带 value。
	rec = serve(t, srv, "POST", "/thread/th_1/interaction/ir_1", url.Values{
		"decision": {"deny"},
		"value":    {"忽略"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deny = %d", rec.Code)
	}
	call, _ = core.lastCall("interaction.respond")
	if _, exists := call.Params["value"]; exists {
		t.Fatalf("deny 不应带 value: %#v", call.Params)
	}

	// 非法 decision 400，且不调用 Core。
	before := core.countCalls("interaction.respond")
	rec = serve(t, srv, "POST", "/thread/th_1/interaction/ir_1", url.Values{"decision": {"maybe"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 decision = %d, want 400", rec.Code)
	}
	if after := core.countCalls("interaction.respond"); after != before {
		t.Fatalf("非法 decision 不应调用 Core（%d -> %d）", before, after)
	}
}

func TestCoreErrorRendersPageInsteadOfPanicking(t *testing.T) {
	core := newFakeCore().fail("thread.get", errors.New("thread not found"))
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/thread/th_missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("线程不存在 = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	wantContains(t, body, "没有找到这个会话")
	wantContains(t, body, "重试")
	wantContains(t, body, "/thread/th_missing")

	// Core 不可用（非「不存在」）走 502，也不 panic。
	core2 := newFakeCore().fail("thread.get", errors.New("dial core: connection refused"))
	srv2, _ := newServer(t, core2)
	rec = serve(t, srv2, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Core 不可用 = %d, want 502", rec.Code)
	}
	wantContains(t, rec.Body.String(), "Core 暂时不可用")
}

func TestTemplateEscapesMessageContent(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"role":  "user",
		"parts": []map[string]any{{"type": "text", "text": `<script>alert("x")</script>`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := json.Marshal(map[string]any{
		"entries": []map[string]any{{
			"id": "e1", "threadId": "th_1", "seq": 1, "kind": "message", "role": "user",
			"payload": json.RawMessage(payload), "createdAt": "2026-10-28T10:00:00Z",
		}},
		"hasMore": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	get := `{
	  "thread":{"id":"th_1","workspace":"/tmp/ws","title":"转义测试","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},
	  "items":[],"seq":1,
	  "run":{"runId":"run_1","status":"terminal","terminal":"completed","startedAt":"2026-10-28T10:00:00Z","endedAt":"2026-10-28T10:00:05Z"},
	  "interactions":[],"hasMore":false
	}`
	core := newFakeCore().
		on("thread.get", get).
		on("thread.entries", string(entries))
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d", rec.Code)
	}
	body := rec.Body.String()
	wantNotContains(t, body, `<script>alert("x")</script>`)
	wantContains(t, body, "&lt;script&gt;")
}

func TestAncillaryRoutes(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	rec := serve(t, srv, "GET", "/healthz", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("/healthz = %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(t, srv, "GET", "/static/app.css", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/static/app.css = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("CSS Content-Type = %q", ct)
	}

	rec = serve(t, srv, "GET", "/events/th_1", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未注入 Events 时应 404，得到 %d", rec.Code)
	}

	rec = serve(t, srv, "GET", "/login", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/login = %d", rec.Code)
	}
	wantContains(t, rec.Body.String(), "登录")

	rec = serve(t, srv, "POST", "/login", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /login = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestNewRequiresSupervisor(t *testing.T) {
	if _, err := web.New(web.Config{}); err == nil {
		t.Fatal("缺少 Supervisor 时 New 应返回错误")
	}
}
