package web_test

// SSR 外壳的垂直切片验收：假 Supervisor + 假 CoreClient 驱动全部主流程，
// 不依赖真实 Core、SQLite 或网络。

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	callFn    func(method string, params any) (json.RawMessage, error)
	subs      map[int]chan contracts.Event
	nextSub   int
}

func newFakeCore() *fakeCore {
	return &fakeCore{
		responses: map[string]json.RawMessage{},
		errs:      map[string]error{},
		subs:      map[int]chan contracts.Event{},
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
	rec := callRecord{Method: method}
	if m, ok := params.(map[string]any); ok {
		rec.Params = make(map[string]any, len(m))
		for k, v := range m {
			rec.Params[k] = v
		}
	}
	f.calls = append(f.calls, rec)
	fn := f.callFn
	callErr := f.errs[method]
	raw, configured := f.responses[method]
	f.mu.Unlock()
	if fn != nil {
		return fn(method, params)
	}
	if callErr != nil {
		return nil, callErr
	}
	if !configured {
		return nil, errors.New("fake: 未配置方法 " + method)
	}
	return raw, nil
}

func (f *fakeCore) Subscribe(buffer int) (<-chan contracts.Event, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = map[int]chan contracts.Event{}
	}
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan contracts.Event, buffer)
	id := f.nextSub
	f.nextSub++
	f.subs[id] = ch
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if c, ok := f.subs[id]; ok {
			delete(f.subs, id)
			close(c)
		}
	}
}

// push 向当前全部订阅者投递一条 Core 通知（测试用）。
func (f *fakeCore) push(method, params string) {
	f.mu.Lock()
	channels := make([]chan contracts.Event, 0, len(f.subs))
	for _, ch := range f.subs {
		channels = append(channels, ch)
	}
	f.mu.Unlock()
	for _, ch := range channels {
		select {
		case ch <- contracts.Event{Method: method, Params: json.RawMessage(params)}:
		default:
		}
	}
}

func (f *fakeCore) Close() error { return nil }

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
	acquired []string
}

func (f *fakeSupervisor) Acquire(ctx context.Context, workspace string) (contracts.CoreClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires++
	f.acquired = append(f.acquired, workspace)
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

func (f *fakeSupervisor) lastWorkspace() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.acquired) == 0 {
		return ""
	}
	return f.acquired[len(f.acquired)-1]
}

// ---- 测试工具 ----

func newServer(t *testing.T, core *fakeCore) (*web.Server, *fakeSupervisor) {
	t.Helper()
	return newServerWithWorkspaces(t, core, []string{"/tmp/ws"})
}

func newServerWithWorkspaces(t *testing.T, core *fakeCore, workspaces []string) (*web.Server, *fakeSupervisor) {
	t.Helper()
	sup := &fakeSupervisor{core: core}
	srv, err := web.New(web.Config{
		Supervisor:  sup,
		Workspace:   workspaces[0],
		Workspaces:  workspaces,
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
	return serveWithCookies(t, srv, method, target, form)
}

func serveWithCookies(t *testing.T, srv *web.Server, method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
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
	for _, c := range cookies {
		req.AddCookie(c)
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

const entriesAscending = `{
  "entries": [
    {
      "id":"e1","threadId":"th_1","seq":1,"kind":"message","role":"user",
      "payload":{"role":"user","parts":[{"type":"text","text":"这是问题"}]},
      "createdAt":"2026-10-28T10:00:01Z"
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
      "id":"e3","threadId":"th_1","seq":3,"kind":"message","role":"tool",
      "payload":{"callId":"call_1","tool":"read_file","status":"ok",
        "content":[{"type":"text","text":"file body"}],
        "details":{"path":"/tmp/demo.txt"},
        "receipt":{"status":"ok","durationMs":12,"truncated":true}},
      "createdAt":"2026-10-28T10:00:03Z"
    }
  ],
  "hasMore": false
}`

const threadGetIdle = `{
  "thread": {"id":"th_1","workspace":"/tmp/ws","title":"测试会话","headEntryId":"e3","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},
  "items": [],
  "seq": 3,
  "run": {"runId":"run_1","status":"terminal","terminal":"completed","startedAt":"2026-10-28T10:00:00Z","endedAt":"2026-10-28T10:00:05Z"},
  "interactions": [],
  "hasMore": false
}`

// entriesResend 是 head 路径上的两问两答：e3 带图，便于验证编辑重发沿用附件。
const entriesResend = `{
  "entries": [
    {
      "id":"e1","threadId":"th_1","seq":1,"kind":"message","role":"user",
      "payload":{"role":"user","parts":[{"type":"text","text":"第一问"}]},
      "createdAt":"2026-10-28T10:00:01Z"
    },
    {
      "id":"e2","threadId":"th_1","seq":2,"kind":"message","role":"assistant",
      "payload":{"role":"assistant","parts":[{"type":"text","text":"第一答"}]},
      "createdAt":"2026-10-28T10:00:02Z"
    },
    {
      "id":"e3","threadId":"th_1","parentId":"e2","seq":3,"kind":"message","role":"user",
      "payload":{"role":"user","parts":[
        {"type":"image","mime":"image/png","data":"aW1n"},
        {"type":"text","text":"第二问"}
      ]},
      "createdAt":"2026-10-28T10:00:03Z"
    },
    {
      "id":"e4","threadId":"th_1","parentId":"e3","seq":4,"kind":"message","role":"assistant",
      "payload":{"role":"assistant","parts":[{"type":"text","text":"第二答"}]},
      "createdAt":"2026-10-28T10:00:04Z"
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
	wantContains(t, body, "新建任务")
	wantContains(t, body, "ada-newtask")
	wantContains(t, body, `data-new-task`)
	wantContains(t, body, `data-workspace-native`)
	wantContains(t, body, `data-workspace-controls`)
	wantContains(t, body, "选择工作区")
	wantContains(t, body, "file-picker.js")
	wantContains(t, body, "composer-draft.js")
	wantContains(t, body, `data-draft-key="new-task"`)
	wantContains(t, body, `data-thread-running="0"`)
	wantContains(t, body, "描述你要完成的任务")
	wantContains(t, body, `href="/threads/th_1"`)
	wantContains(t, body, "ada-thread-list")
	wantContains(t, body, `action="/threads/th_1/delete"`)
	wantContains(t, body, "删除会话「第一个会话」")
	wantContains(t, body, `name="next" value="/"`)

	call, ok := core.lastCall("thread.list")
	if !ok {
		t.Fatal("没有调用 thread.list")
	}
	if got := call.Params["limit"]; got != 50 {
		t.Fatalf("thread.list limit = %v, want 50", got)
	}
}

func TestCreateThreadCallsCoreAndRedirects(t *testing.T) {
	core := newFakeCore().on("thread.create", `{"thread":{"id":"th_new","workspace":"/tmp/ws","title":"新想法","createdAt":"2026-10-28T10:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false}}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads", url.Values{"title": {"新想法"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /threads = %d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/threads/th_new" {
		t.Fatalf("Location = %q, want /threads/th_new", loc)
	}
	call, ok := core.lastCall("thread.create")
	if !ok {
		t.Fatal("没有调用 thread.create")
	}
	if call.Params["title"] != "新想法" {
		t.Fatalf("thread.create title = %v", call.Params["title"])
	}
}

// 新建任务可以带工作区与第一条消息：thread.create 与 turn.start 都必须
// 落在所选工作区，并且回跳前把工作区 cookie 写好。
func TestCreateThreadWithWorkspaceAndFirstMessage(t *testing.T) {
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	core := newFakeCore().
		on("thread.create", `{"thread":{"id":"th_2"}}`).
		on("turn.start", `{"runId":"run_2","duplicate":false,"seq":0}`)
	srv, sup := newServerWithWorkspaces(t, core, []string{ws1, ws2})

	rec := serve(t, srv, "POST", "/threads", url.Values{
		"workspace": {ws2},
		"text":      {"第一个任务\n第二行"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /threads = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/threads/th_2" {
		t.Fatalf("Location = %q", loc)
	}
	realWS2, err := filepath.EvalSymlinks(ws2)
	if err != nil {
		t.Fatal(err)
	}
	if got := sup.lastWorkspace(); got != filepath.Clean(realWS2) {
		t.Fatalf("最后使用的工作区 = %q，期望 %q", got, realWS2)
	}
	create, _ := core.lastCall("thread.create")
	if create.Params["title"] != "第一个任务" {
		t.Fatalf("标题应取任务首行，实得 %v", create.Params["title"])
	}
	start, ok := core.lastCall("turn.start")
	if !ok || start.Params["text"] != "第一个任务\n第二行" {
		t.Fatalf("首条消息没有进入 turn.start: %#v", start.Params)
	}
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == web.WorkspaceCookieName {
			found = true
			if c.Value != "1" {
				t.Fatalf("workspace cookie = %q，期望 1", c.Value)
			}
		}
	}
	if !found {
		t.Fatal("创建任务后没有写工作区 cookie")
	}

	bad := serve(t, srv, "POST", "/threads", url.Values{
		"workspace": {filepath.Join(t.TempDir(), "missing")},
		"text":      {"x"},
	})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("不存在的目录 = %d，期望 400", bad.Code)
	}
}

// FilePicker 选中的任意目录都可以登记成本次会话的工作区。
func TestCreateThreadOpensNewWorkspace(t *testing.T) {
	newWs := t.TempDir()
	core := newFakeCore().
		on("thread.create", `{"thread":{"id":"th_dynamic"}}`).
		on("turn.start", `{"runId":"run_dynamic"}`)
	srv, sup := newServerWithWorkspaces(t, core, []string{"/tmp/ws"})

	rec := serve(t, srv, "POST", "/threads", url.Values{
		"workspace": {newWs},
		"text":      {"在任意目录里开任务"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /threads = %d (body=%s)", rec.Code, rec.Body.String())
	}
	real, err := filepath.EvalSymlinks(newWs)
	if err != nil {
		t.Fatal(err)
	}
	if got := sup.lastWorkspace(); got != filepath.Clean(real) {
		t.Fatalf("工作区 = %q，期望 %q", got, filepath.Clean(real))
	}
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == web.WorkspaceCookieName {
			found = true
			if c.Value != "1" {
				t.Fatalf("动态工作区 cookie = %q，期望 1", c.Value)
			}
		}
	}
	if !found {
		t.Fatal("动态工作区没有写 cookie")
	}
}

// 动态工作区必须盖过 Gateway 注入的 WorkspaceFor：后者只认识启动配置，
// 否则新建任务后 thread.get 会落到默认工作区，报“thread 不存在”。
func TestDynamicWorkspaceBeatsStaleWorkspaceFor(t *testing.T) {
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	dyn := t.TempDir()
	core := newFakeCore().
		on("thread.create", `{"thread":{"id":"th_dyn"}}`).
		on("turn.start", `{"runId":"run_dyn"}`).
		on("thread.get", `{"thread":{"id":"th_dyn","workspace":"","title":"动态工作区会话","createdAt":"2026-10-28T10:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},"items":[],"seq":1,"run":{"runId":"run_dyn","status":"terminal","terminal":"completed"},"interactions":[],"hasMore":false}`).
		on("thread.entries", `{"entries":[],"hasMore":false}`)
	sup := &fakeSupervisor{core: core}
	srv, err := web.New(web.Config{
		Supervisor: sup, Workspace: ws1, Workspaces: []string{ws1, ws2},
		// 模拟 Gateway 的旧解析器：只认识配置里的第一个工作区。
		WorkspaceFor: func(*http.Request) string { return ws1 },
		ProductName:  "测试产品", Locale: "zh-CN",
		CSRF: func(*http.Request) string { return "csrf-token" },
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	rec := serve(t, srv, "POST", "/threads", url.Values{
		"workspace": {dyn},
		"text":      {"动态工作区任务"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /threads = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.WorkspaceCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("没有写工作区 cookie")
	}

	page := serveWithCookies(t, srv, "GET", "/threads/th_dyn", nil, cookie)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /threads/th_dyn = %d (body=%s)", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "没有找到这个会话") {
		t.Fatalf("动态工作区会话被解析到了错误 Core:\n%s", page.Body.String())
	}
	real, err := filepath.EvalSymlinks(dyn)
	if err != nil {
		t.Fatal(err)
	}
	if got := sup.lastWorkspace(); got != filepath.Clean(real) {
		t.Fatalf("thread.get 使用的工作区 = %q，期望 %q", got, filepath.Clean(real))
	}
}

// FilePicker 选中即写 cookie：模型选择触发的 reload 不能把选择丢掉。
func TestWorkspaceSelectAPIWritesCookie(t *testing.T) {
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	srv, _ := newServerWithWorkspaces(t, newFakeCore(), []string{ws1, ws2})

	rec := serve(t, srv, "POST", "/api/workspace/select", url.Values{"path": {ws2}})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/workspace/select = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Workspace string `json:"workspace"`
		Index     int    `json:"index"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应 JSON: %v", err)
	}
	if body.Index != 1 {
		t.Fatalf("index = %d，期望 1", body.Index)
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.WorkspaceCookieName {
			found = true
			if c.Value != "1" {
				t.Fatalf("cookie = %q，期望 1", c.Value)
			}
		}
	}
	if !found {
		t.Fatal("没有写工作区 cookie")
	}
	if bad := serve(t, srv, "POST", "/api/workspace/select", url.Values{"path": {filepath.Join(ws1, "missing")}}); bad.Code != http.StatusBadRequest {
		t.Fatalf("不存在目录 = %d，期望 400", bad.Code)
	}
}

// FilePicker 的目录 API：只列目录；Windows 入口给盘符，工作区另作快捷入口。
func TestFSListAPI(t *testing.T) {
	ws := t.TempDir()
	other := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(ws, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ws, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := newServerWithWorkspaces(t, newFakeCore(), []string{ws, other})

	type listResponse struct {
		Path       string `json:"path"`
		Parent     string `json:"parent"`
		Selectable bool   `json:"selectable"`
		Roots      []struct {
			Label string `json:"label"`
			Path  string `json:"path"`
		} `json:"roots"`
		Workspaces []struct {
			Label string `json:"label"`
			Path  string `json:"path"`
		} `json:"workspaces"`
		Entries []struct {
			Name       string `json:"name"`
			Path       string `json:"path"`
			Selectable bool   `json:"selectable"`
		} `json:"entries"`
	}

	// 不带 path：Windows 先给盘符，已打开的工作区作为快捷入口。
	boot := serve(t, srv, "GET", "/api/fs/list", nil)
	var bootResp listResponse
	if err := json.Unmarshal(boot.Body.Bytes(), &bootResp); err != nil {
		t.Fatalf("解析入口响应: %v", err)
	}
	if boot.Code != http.StatusOK || bootResp.Path != "" || bootResp.Selectable {
		t.Fatalf("入口响应 = %d path=%q selectable=%v", boot.Code, bootResp.Path, bootResp.Selectable)
	}
	if len(bootResp.Roots) == 0 {
		t.Fatal("入口响应缺少磁盘/根目录")
	}
	if len(bootResp.Workspaces) != 2 {
		t.Fatalf("工作区快捷入口 = %d，期望 2", len(bootResp.Workspaces))
	}

	rec := serve(t, srv, "GET", "/api/fs/list?path="+url.QueryEscape(ws), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/fs/list = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var root listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &root); err != nil {
		t.Fatalf("解析目录响应: %v", err)
	}
	if !root.Selectable {
		t.Fatal("任何目录都应能作为工作区")
	}
	names := map[string]bool{}
	for _, entry := range root.Entries {
		names[entry.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Fatalf("目录项缺少子目录: %#v", root.Entries)
	}
	if names["note.txt"] {
		t.Fatal("目录 API 不应列出文件")
	}

	sub := serve(t, srv, "GET", "/api/fs/list?path="+url.QueryEscape(filepath.Join(ws, "alpha")), nil)
	var subResp listResponse
	if err := json.Unmarshal(sub.Body.Bytes(), &subResp); err != nil {
		t.Fatalf("解析子目录响应: %v", err)
	}
	if sub.Code != http.StatusOK || !subResp.Selectable {
		t.Fatalf("子目录 = %d selectable=%v，期望 200 且可选", sub.Code, subResp.Selectable)
	}
	if subResp.Parent == "" {
		t.Fatal("子目录应返回 parent")
	}

	// 现在允许跨工作区浏览（Windows 盘符模型的必然结果）。
	outside := serve(t, srv, "GET", "/api/fs/list?path="+url.QueryEscape(filepath.Dir(ws)), nil)
	if outside.Code != http.StatusOK {
		t.Fatalf("工作区外 = %d，期望 200", outside.Code)
	}
	missing := serve(t, srv, "GET", "/api/fs/list?path="+url.QueryEscape(filepath.Join(ws, "missing-dir")), nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("不存在目录 = %d，期望 404", missing.Code)
	}
}

// 已经在跑脚本的浏览器（adag_js=1）不再拿 meta refresh：运行中的页面由
// chat-live.js 吃 SSE 增量。Chromium 里删掉已解析的 meta 不会取消排队的刷新，
// 发了就等于整个运行期每 2 秒整页重载一次。
func TestThreadPageSkipsMetaRefreshForJSClient(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending)
	srv, _ := newServer(t, core)

	rec := serveWithCookies(t, srv, "GET", "/thread/th_1", nil,
		&http.Cookie{Name: web.JSClientCookieName, Value: "1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); strings.Contains(body, `http-equiv="refresh"`) {
		t.Fatal("有 JS 的浏览器不该拿到 meta refresh")
	}
}

func TestThreadPageRendersTranscriptToolCardAndTray(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending)
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

	// 工具卡字段。工具按名字挑图标：read_file 是文件图标，不是统一的终端。
	wantContains(t, body, "read_file")
	wantContains(t, body, "call_1")
	wantContains(t, body, "12 ms")
	wantContains(t, body, "已截断")
	wantContains(t, body, `M14 3H7a2 2 0 0 0-2 2v14`) // i-file 的第一段路径

	// 交互托盘：select 表单与选项；无 JS 时由 noscript 原生 select 提交，
	// 自绘组件的隐藏 input 默认 disabled（JS 接管后才启用）。
	wantContains(t, body, "批准操作")
	wantContains(t, body, "允许一次")
	wantContains(t, body, `name="decision"`)
	wantContains(t, body, `name="value"`)
	wantContains(t, body, `ada-select__fallback`)
	wantContains(t, body, `data-select-input value="允许一次" disabled`)
	wantContains(t, body, `/threads/th_1/abort`)
	// 中止入口只在顶栏；输入区不再重复放「中止」按钮，避免和队列/指标抢空间。
	// POST /abort 仍必须带 CSRF 字段，否则会被安全中间件以 csrf_invalid 拒绝。
	if got := strings.Count(body, `action="/threads/th_1/abort" data-confirm="确定中止当前轮次吗？"><input type="hidden" name="csrf_token"`); got != 1 {
		t.Fatalf("带 CSRF 的中止表单 = %d，期望 1（仅顶栏）", got)
	}
	// 辅助区整块由插件岛渲染：基座只留空壳宿主，不再输出缓存命中/上下文环。
	wantContains(t, body, `<div id="composer-aux"><div class="ada-slot" data-slot="composer.footer"`)
	if strings.Contains(body, "缓存命中") {
		t.Fatal("缓存命中已迁到插件岛，基座页面不该再出现它（没有 SSR 回退）")
	}

	// Composer 表单。整页刷新（模型/工作区切换、桌面壳热更新）时草稿按会话恢复。
	wantContains(t, body, `action="/threads/th_1/send"`)
	wantContains(t, body, `name="text"`)
	wantContains(t, body, `data-draft-key="thread:th_1"`)
	wantContains(t, body, `data-thread-id="th_1"`)
	wantContains(t, body, `data-outbox`)
	wantContains(t, body, "深度求索中")
	wantContains(t, body, `data-run-started-at="2026-10-28T10:00:00Z"`)
	wantContains(t, body, "composer-draft.js")

	call, ok := core.lastCall("thread.entries")
	if !ok {
		t.Fatal("没有调用 thread.entries")
	}
	if got := call.Params["limit"]; got != 100 {
		t.Fatalf("thread.entries limit = %v, want 100", got)
	}
}

// 输入框辅助区已迁到插件岛：基座页面只输出空壳宿主，指标由
// GET /threads/{id}/state 提供（见 TestThreadStateJSON）。
func TestThreadPageLeavesComposerAuxToPluginIsland(t *testing.T) {
	core := newFakeCore().
		on("thread.get", `{"thread":{"id":"th_1","workspace":"","title":"指标会话","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},"items":[],"seq":1,"run":{"runId":"run_1","status":"terminal","terminal":"completed"},"context":{"tokens":705000,"window":1048576,"percent":67},"interactions":[],"hasMore":false}`).
		on("thread.entries", `{"entries":[],"hasMore":false}`).
		on("thread.list", emptyThreadList).
		on("usage.get", `{"thread":{"inputTokens":1000,"cacheTokens":990},"workspace":{}}`)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	wantContains(t, body, `data-slot="composer.footer"`)
	// 没有 SSR 回退：没有 JS / 插件停用时辅助区整块空白。
	for _, absent := range []string{"ada-token-ring", "ada-context", "缓存命中", "工作区："} {
		if strings.Contains(body, absent) {
			t.Fatalf("基座不应再渲染辅助区内容 %q（已整体迁到插件岛）", absent)
		}
	}
}

// 遥测轮询端点：只返回 run / context 两块通用会话投影；用量由插件自己的
// route 聚合（events.read），基座不再调用 usage.get。
func TestThreadStateJSON(t *testing.T) {
	core := newFakeCore().
		on("thread.get", `{"thread":{"id":"th_1","workspace":"","title":"指标会话","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},"items":[],"seq":1,"run":{"runId":"run_1","status":"running","startedAt":"2026-10-28T10:00:00Z"},"context":{"tokens":215000,"messages":152000,"system":1500,"tools":7700,"window":1048576,"percent":21},"interactions":[],"hasMore":false}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/threads/th_1/state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /threads/th_1/state = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	var out struct {
		Run struct {
			Status    string `json:"status"`
			StartedAt string `json:"startedAt"`
		} `json:"run"`
		Context *struct {
			Tokens   int `json:"tokens"`
			Messages int `json:"messages"`
			Window   int `json:"window"`
			Percent  int `json:"percent"`
		} `json:"context"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%s)", err, rec.Body.String())
	}
	if out.Run.Status != "running" || out.Run.StartedAt != "2026-10-28T10:00:00Z" {
		t.Fatalf("run = %+v", out.Run)
	}
	if out.Context == nil || out.Context.Tokens != 215000 || out.Context.Window != 1048576 || out.Context.Percent != 21 {
		t.Fatalf("context = %+v", out.Context)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["usage"]; ok {
		t.Fatalf("基座不应再返回 usage 投影: %s", rec.Body.String())
	}
	if _, ok := core.lastCall("usage.get"); ok {
		t.Fatal("端点不应再调用 usage.get")
	}
	// 端点只拉投影所需的最小数据：thread.get 不应拉整份历史。
	if call, ok := core.lastCall("thread.get"); !ok || call.Params["itemLimit"] != 1 {
		t.Fatalf("thread.get 参数 = %+v，期望 itemLimit=1", call.Params)
	}
}

// thread.get 失败时端点如实报错，不伪造空状态。
func TestThreadStateJSONCoreError(t *testing.T) {
	core := newFakeCore().fail("thread.get", errors.New("core down"))
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/threads/th_1/state", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("GET /threads/th_1/state = %d，期望 502", rec.Code)
	}
}

func TestThreadPageExposesLiveStreamHooks(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d", rec.Code)
	}
	body := rec.Body.String()
	wantContains(t, body, `data-thread-id="th_1"`)
	wantContains(t, body, `data-run-status="running"`)
	wantContains(t, body, `<script src="/static/app.js?v=`)
	wantContains(t, body, `<script src="/static/chat-live.js?v=`)
	wantContains(t, body, `id="live-stream"`)
	wantContains(t, body, `id="live-status"`)
}

func TestStaticAppJSServed(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	for _, target := range []string{"/static/app.js", "/static/chat-live.js", "/static/composer-draft.js"} {
		rec := serve(t, srv, "GET", target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
			t.Fatalf("%s Content-Type = %q", target, ct)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s X-Content-Type-Options = %q", target, got)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
			t.Fatalf("%s Cache-Control = %q", target, cc)
		}
	}
	// a_da 外壳脚本：提示条 + 确认弹窗 + 侧栏 + 会话右键菜单。
	appJS := serve(t, srv, "GET", "/static/app.js", nil).Body.String()
	wantContains(t, appJS, "data-confirm")
	wantContains(t, appJS, "contextmenu")
	wantContains(t, appJS, "data-thread-menu-action")
	wantContains(t, appJS, "/archive")
	wantContains(t, appJS, "AdaOutbox")
	wantContains(t, appJS, "data-outbox-action")
	wantContains(t, appJS, "a_da_outbox:")
	wantContains(t, serve(t, srv, "GET", "/static/components.js", nil).Body.String(), "tickRunStatusElapsed")
	// 实时增强脚本：SSE 增量。
	live := serve(t, srv, "GET", "/static/chat-live.js", nil).Body.String()
	wantContains(t, live, "EventSource")
	wantContains(t, live, "evt.message.delta")
	wantContains(t, live, "AdaOutbox.onRunEnd")
	// 实时块必须保存思考文本节点：handleDelta 通过 entry.thinkingText 追加增量，
	// 漏存时每个思考事件都会抛 TypeError，界面只剩正文没有实时思考。
	wantContains(t, live, "thinkingText: thinkingText")
	wantContains(t, live, "entry.thinkingText.textContent")
	// 实时思考与落库后的详情卡共用一套结构，不再保留第二种 .live-thinking 样式。
	wantContains(t, live, "ada-disclosure__summary")
	wantContains(t, live, "ada-thinking")
	wantNotContains(t, live, "live-thinking")

	// 草稿脚本：按 data-draft-key 恢复，提交与页面卸载都要走同一套清理/落盘。
	draft := serve(t, srv, "GET", "/static/composer-draft.js", nil).Body.String()
	wantContains(t, draft, "data-draft-key")
	wantContains(t, draft, "a_da_draft:")
	wantContains(t, draft, "pagehide")
}

// TestAskWizardEnablesAllFieldsBeforeSubmit 钉多题分步表单的提交契约：非当前步的字段
// 是 disabled（19-ued §19.5.5「未到达步骤的字段必须 disabled」），而 disabled 控件不参与
// 表单提交；末步提交前必须重新启用全部步骤，否则只提交末题，服务端按 q0、q1… 拼出的
// answers 缺项，ask_user 会以「回答数量与问题不一致」失败（ERR_ASK_DENIED）。
func TestAskWizardEnablesAllFieldsBeforeSubmit(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	js := serve(t, srv, "GET", "/static/app.js", nil).Body.String()
	wantContains(t, js, "data-ask-form")
	wantContains(t, js, "setStepEnabled(steps[s], true)")
}

func TestTurnStartUsesTextAndIdempotencyKey(t *testing.T) {
	core := newFakeCore().on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/send", url.Values{
		"text":           {"你好，世界"},
		"idempotencyKey": {"abcdefgh"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("turn.start = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/threads/th_1" {
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

	rec := serve(t, srv, "POST", "/threads/th_1/send", url.Values{"text": {"   "}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空消息 = %d, want 400", rec.Code)
	}
	wantContains(t, rec.Body.String(), "消息不能为空")

	core := newFakeCore().on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv2, _ := newServer(t, core)
	rec = serve(t, srv2, "POST", "/threads/th_1/send", url.Values{"text": {"你好"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("缺省幂等键 = %d, want 303", rec.Code)
	}
	call, _ := core.lastCall("turn.start")
	key, _ := call.Params["idempotencyKey"].(string)
	if len(key) < 8 || len(key) > 64 {
		t.Fatalf("生成的幂等键长度不合法: %q", key)
	}
}

// 运行中发送不再撞 thread_busy 整页报错，而是排进 steering 队列。
// 编辑重发：head 移到目标消息的父节点，再 turn.start；旧分支保留但不显示。
func TestTurnResendBranchesToParentAndStartsTurn(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesResend).
		on("thread.branch", `{"thread":{"id":"th_1"}}`).
		on("turn.start", `{"runId":"run_2","duplicate":false,"seq":5}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/resend", url.Values{
		"entryId":        {"e3"},
		"text":           {"改写后的第二问"},
		"idempotencyKey": {"edit-key-0001"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("resend = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "notice=") {
		t.Fatalf("Location = %q，期望带成功提示", loc)
	}
	branch, ok := core.lastCall("thread.branch")
	if !ok || branch.Params["entryId"] != "e2" {
		t.Fatalf("thread.branch = %+v，期望 entryId=e2（目标消息的父节点）", branch)
	}
	start, ok := core.lastCall("turn.start")
	if !ok || start.Params["text"] != "改写后的第二问" || start.Params["threadId"] != "th_1" {
		t.Fatalf("turn.start = %+v", start)
	}
	// 编辑框只改文字：原消息里的图片必须沿用，不能因为重发丢附件。
	images, _ := start.Params["images"].([]map[string]any)
	if len(images) != 1 || images[0]["mime"] != "image/png" {
		t.Fatalf("turn.start images = %#v，期望沿用原消息的图片", start.Params["images"])
	}
	if key, _ := start.Params["idempotencyKey"].(string); key != "edit-key-0001" {
		t.Fatalf("idempotencyKey = %q，期望沿用表单里的幂等键", key)
	}
}

// 首条消息没有父节点：编辑重发必须用空 entryId 清空 head，而不是伪造父节点。
func TestTurnResendFromRootClearsHead(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesResend).
		on("thread.branch", `{"thread":{"id":"th_1"}}`).
		on("turn.start", `{"runId":"run_3","duplicate":false,"seq":5}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/resend", url.Values{
		"entryId": {"e1"},
		"text":    {"改写后的第一问"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("resend root = %d (body=%s)", rec.Code, rec.Body.String())
	}
	branch, ok := core.lastCall("thread.branch")
	if !ok {
		t.Fatal("没有调用 thread.branch")
	}
	if v, exists := branch.Params["entryId"]; !exists || v != "" {
		t.Fatalf("thread.branch entryId = %#v, exists=%v，期望空串（清空 head）", v, exists)
	}
	if start, _ := core.lastCall("turn.start"); start.Params["text"] != "改写后的第一问" {
		t.Fatalf("turn.start = %+v", start)
	}
}

// 运行中/非用户消息不能改写历史；前者提示先中止，后者明确只能编辑用户消息。
func TestTurnResendRejectsUnsafeTargets(t *testing.T) {
	running := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesResend)
	srv, _ := newServer(t, running)
	rec := serve(t, srv, "POST", "/threads/th_1/resend", url.Values{
		"entryId": {"e3"}, "text": {"不该生效"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Fatalf("运行中 resend = %d / %q", rec.Code, rec.Header().Get("Location"))
	}
	if running.countCalls("thread.branch") != 0 || running.countCalls("turn.start") != 0 {
		t.Fatal("运行中不能调用 thread.branch / turn.start")
	}

	assistant := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesResend)
	srv2, _ := newServer(t, assistant)
	rec = serve(t, srv2, "POST", "/threads/th_1/resend", url.Values{
		"entryId": {"e2"}, "text": {"改助手消息"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Fatalf("助手消息 resend = %d / %q", rec.Code, rec.Header().Get("Location"))
	}
	if assistant.countCalls("thread.branch") != 0 || assistant.countCalls("turn.start") != 0 {
		t.Fatal("非用户消息不能触发分支或起轮")
	}
}

// 起轮失败时把 head 拨回原分支，避免会话停在半截。
func TestTurnResendRestoresHeadWhenStartFails(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesResend).
		on("thread.branch", `{"thread":{"id":"th_1"}}`).
		fail("turn.start", errors.New("thread_busy"))
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/resend", url.Values{
		"entryId": {"e3"}, "text": {"改写"},
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("起轮失败应返回 502，得到 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if core.countCalls("thread.branch") != 2 {
		t.Fatalf("thread.branch 调用 %d 次，期望 2（分支 + 回滚）", core.countCalls("thread.branch"))
	}
	last, _ := core.lastCall("thread.branch")
	if last.Params["entryId"] != "e3" {
		t.Fatalf("回滚 branch = %+v，期望回到原 head e3", last)
	}
}

// 空闲页的用户消息渲染“编辑并重发”内联表单；运行中不渲染（避免改写落库中的历史）。
func TestThreadPageRendersEditResendForm(t *testing.T) {
	idle := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesResend)
	srv, _ := newServer(t, idle)
	body := serve(t, srv, "GET", "/threads/th_1", nil).Body.String()

	for _, want := range []string{
		`action="/threads/th_1/resend"`,
		`name="entryId" value="e3"`,
		`name="idempotencyKey" value="`,
		`name="csrf_token" value="csrf-token"`,
		">第二问</textarea>",
		"这条消息及其之后的所有对话会被舍弃",
	} {
		wantContains(t, body, want)
	}

	running := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesResend)
	srv2, _ := newServer(t, running)
	wantNotContains(t, serve(t, srv2, "GET", "/threads/th_1", nil).Body.String(), "/resend")
}

func TestTurnSendSteersWhileRunning(t *testing.T) {
	core := newFakeCore().
		on("turn.get", `{"run":{"runId":"run_1","status":"running","startedAt":"2026-10-28T10:00:00Z"},"queueLength":0}`).
		on("turn.steer", `{"queued":true,"position":1}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/send", url.Values{"text": {"补充一下"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("运行中发送 = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/threads/th_1?notice=") {
		t.Fatalf("Location = %q，期望带排队提示", loc)
	}
	call, ok := core.lastCall("turn.steer")
	if !ok {
		t.Fatal("运行中发送应调用 turn.steer")
	}
	if call.Params["text"] != "补充一下" {
		t.Fatalf("turn.steer text = %v", call.Params["text"])
	}
	if _, ok := core.lastCall("turn.start"); ok {
		t.Fatal("运行中发送不应再调用 turn.start")
	}
}

// 等待用户回答交互时，新消息应当提示先回答，而不是报 thread_busy。
func TestTurnSendWhileWaitingInputRedirects(t *testing.T) {
	core := newFakeCore().
		on("turn.get", `{"run":{"runId":"run_1","status":"waiting_input","startedAt":"2026-10-28T10:00:00Z"},"queueLength":0}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/send", url.Values{"text": {"先等等"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("等待交互时发送 = %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/threads/th_1?error=") {
		t.Fatalf("Location = %q，期望提示先回答交互", loc)
	}
	if _, ok := core.lastCall("turn.start"); ok {
		t.Fatal("等待交互时不应启动新一轮")
	}
}

// 无 JS 浏览器用原生 select 提交 model：开始轮次前先切换线程模型。
func TestTurnSendAppliesModelFromNativeSelect(t *testing.T) {
	core := newFakeCore().
		on("turn.get", `{"run":{"status":"terminal","terminal":"completed"},"queueLength":0}`).
		on("model.set", `{"model":{"providerId":"openai","modelId":"gpt-4o"},"scope":"thread"}`).
		on("turn.start", `{"runId":"run_1","duplicate":false,"seq":1}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/send", url.Values{
		"text":  {"你好"},
		"model": {"openai|gpt-4o"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("带 model 发送 = %d (body=%s)", rec.Code, rec.Body.String())
	}
	call, ok := core.lastCall("model.set")
	if !ok {
		t.Fatal("带 model 的发送应先调用 model.set")
	}
	model, ok := call.Params["model"].(map[string]any)
	if !ok || model["providerId"] != "openai" || model["modelId"] != "gpt-4o" {
		t.Fatalf("model.set 参数 = %#v", call.Params["model"])
	}
	if call.Params["threadId"] != "th_1" {
		t.Fatalf("model.set threadId = %v", call.Params["threadId"])
	}
	if _, ok := core.lastCall("turn.start"); !ok {
		t.Fatal("模型切换后应继续 turn.start")
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
	wantContains(t, body, "/threads/th_missing")

	// Core 不可用（非「不存在」）走 502，也不 panic。
	core2 := newFakeCore().fail("thread.get", errors.New("dial core: connection refused"))
	srv2, _ := newServer(t, core2)
	rec = serve(t, srv2, "GET", "/thread/th_1", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Core 不可用 = %d, want 502", rec.Code)
	}
	wantContains(t, rec.Body.String(), "Core 暂时不可用")
}

// 未匹配的路径渲染带外壳的 404，而不是裸文本 page not found。
func TestNotFoundRendersNavigablePage(t *testing.T) {
	core := newFakeCore().on("thread.list", emptyThreadList)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/no/such/page", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知路径 = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"页面不存在",
		"请求路径",
		"/no/such/page",
		`href="/"`,
		`href="/extensions"`,
		`href="/overview"`,
		`class="ada-shell"`,
		`class="ada-sidebar`,
	} {
		wantContains(t, body, want)
	}
	// 不再出现 Go 默认的裸文本 404。
	wantNotContains(t, body, "404 page not found")
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
	wantContains(t, body, `data-run-status="terminal"`)
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

const emptyThreadList = `{"threads":[],"nextCursor":"","hasMore":false}`

func TestWorkspacePickerOnlyWhenMultiple(t *testing.T) {
	core := newFakeCore().on("thread.list", emptyThreadList)

	single, _ := newServer(t, core)
	body := serve(t, single, "GET", "/", nil).Body.String()
	// 单工作区也保留「打开工作区」入口（/workspaces）；内联 index 选择表单已移除。
	wantContains(t, body, `href="/workspaces"`)
	wantNotContains(t, body, `action="/workspace"`)

	multi, _ := newServerWithWorkspaces(t, core, []string{"/tmp/ws-a", "/tmp/ws-b"})
	rec := serve(t, multi, "GET", "/workspaces", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /workspaces = %d", rec.Code)
	}
	body = rec.Body.String()
	wantContains(t, body, `action="/workspaces/select"`)
	wantContains(t, body, `name="path"`)
	wantContains(t, body, "ws-a")
	wantContains(t, body, "/tmp/ws-a")
	wantContains(t, body, "ws-b")
	wantContains(t, body, "/tmp/ws-b")
}

func TestWorkspaceSelectSetsCookieAndRedirects(t *testing.T) {
	srv, _ := newServerWithWorkspaces(t, newFakeCore(), []string{"/tmp/ws-a", "/tmp/ws-b"})

	rec := serve(t, srv, "POST", "/workspaces/select", url.Values{"path": {"/tmp/ws-b"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /workspaces/select = %d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("Location = %q, want /", loc)
	}

	var chosen *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == web.WorkspaceCookieName {
			chosen = c
		}
	}
	if chosen == nil {
		t.Fatalf("没有设置 %s cookie", web.WorkspaceCookieName)
	}
	if chosen.Value != "1" {
		t.Fatalf("cookie 值 = %q, want 1", chosen.Value)
	}
	if chosen.Path != "/" {
		t.Fatalf("cookie Path = %q, want /", chosen.Path)
	}
	if chosen.MaxAge != web.WorkspaceCookieMaxAge {
		t.Fatalf("cookie MaxAge = %d, want %d", chosen.MaxAge, web.WorkspaceCookieMaxAge)
	}
	if chosen.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie SameSite = %v, want Lax", chosen.SameSite)
	}
	if chosen.HttpOnly {
		t.Fatal("工作区选择 cookie 不应为 HttpOnly（选择器需要可读）")
	}

	// GET 只做 PRG 回工作区页。
	rec = serve(t, srv, "GET", "/workspace", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/workspaces" {
		t.Fatalf("GET /workspace = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestWorkspaceSelectRejectsInvalidIndex(t *testing.T) {
	srv, _ := newServerWithWorkspaces(t, newFakeCore(), []string{"/tmp/ws-a", "/tmp/ws-b"})

	for _, raw := range []string{"2", "-1", "abc", ""} {
		rec := serve(t, srv, "POST", "/workspace", url.Values{"index": {raw}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /workspace index=%q = %d, want 400", raw, rec.Code)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == web.WorkspaceCookieName {
				t.Fatalf("非法 index=%q 不应设置工作区 cookie", raw)
			}
		}
	}
}

func TestAcquireUsesCookieWorkspace(t *testing.T) {
	workspaces := []string{"/tmp/ws-a", "/tmp/ws-b"}

	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"索引 1", "1", "/tmp/ws-b"},
		{"越界", "9", "/tmp/ws-a"},
		{"负数", "-1", "/tmp/ws-a"},
		{"非数字", "abc", "/tmp/ws-a"},
		{"空值", "", "/tmp/ws-a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := newFakeCore().on("thread.list", emptyThreadList)
			srv, sup := newServerWithWorkspaces(t, core, workspaces)
			rec := serveWithCookies(t, srv, "GET", "/", nil,
				&http.Cookie{Name: web.WorkspaceCookieName, Value: tc.value})
			if rec.Code != http.StatusOK {
				t.Fatalf("GET / = %d", rec.Code)
			}
			if got := sup.lastWorkspace(); got != tc.want {
				t.Fatalf("Acquire 工作区 = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewRequiresSupervisor(t *testing.T) {
	if _, err := web.New(web.Config{}); err == nil {
		t.Fatal("缺少 Supervisor 时 New 应返回错误")
	}
}

// 导入的 a_da 基线资产必须能通过内嵌静态服务取到，且带正确的头。
func TestStaticBaselineAssetsServed(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	cases := []struct {
		target string
		want   string
	}{
		{"/static/tokens.css", "--ph-canvas"},
		{"/static/app.css", "--ph-canvas"},
		{"/static/ui/components.css", ".ada-"},
		{"/static/ui/layout.css", ""},
		{"/static/ui/widgets.css", ""},
		{"/static/ui/shell.css", ".ada-"},
		{"/static/ui/pages.css", ""},
		{"/static/app-local.css", ".shell"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			rec := serve(t, srv, "GET", tc.target, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d", tc.target, rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
				t.Fatalf("%s Content-Type = %q", tc.target, ct)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("%s X-Content-Type-Options = %q", tc.target, got)
			}
			if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
				t.Fatalf("%s Cache-Control = %q", tc.target, cc)
			}
			if tc.want != "" {
				wantContains(t, rec.Body.String(), tc.want)
			}
		})
	}

	// tokens.css 来自 a_da：必须带来源 commit 与 SHA-256。
	body := serve(t, srv, "GET", "/static/tokens.css", nil).Body.String()
	wantContains(t, body, "a_da")
	wantContains(t, body, "ae2f8ffe1679ce25c490c7af0a788d6575669fa0")
	wantContains(t, body, "SHA-256")
}

// 运行状态条是「弱化表面」，不能借用深色代码面板 token：浅色主题下
// --ph-code-bg 是近黑色（.markdown-body pre 的底色），收尾时状态条会闪成黑块。
func TestLiveStatusUsesSubtleSurface(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	css := serve(t, srv, "GET", "/static/app-local.css", nil).Body.String()

	start := strings.Index(css, ".live-status {")
	if start < 0 {
		t.Fatal("app-local.css 缺少 .live-status 规则")
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatal(".live-status 规则没有闭合")
	}
	rule := css[start : start+end]
	wantContains(t, rule, "var(--ph-subtle)")
	wantNotContains(t, rule, "var(--ph-code-bg)")
}

// 静态服务只暴露 embed FS 内的普通文件，不因 .. 或目录路径泄漏仓库文件。
func TestStaticRejectsTraversalAndMissing(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	for _, target := range []string{
		"/static/",
		"/static/missing.css",
		"/static/ui/",
		"/static/../go.mod",
		"/static/%2e%2e/go.mod",
		"/static/ui/../server.go",
	} {
		rec := serve(t, srv, "GET", target, nil)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s 不应被静态服务命中", target)
		}
		wantNotContains(t, rec.Body.String(), "module github.com/adag/ada-go")
	}
}

// head 里的样式顺序固定：tokens → a_da 产物 → ui 组件层 → 本地覆盖。
func TestHeadLoadsBaselineCSSInOrder(t *testing.T) {
	core := newFakeCore().on("thread.list", emptyThreadList)
	srv, _ := newServer(t, core)
	body := serve(t, srv, "GET", "/", nil).Body.String()

	order := []string{
		`/static/tokens.css`,
		`/static/app.css`,
		`/static/ui/components.css`,
		`/static/ui/layout.css`,
		`/static/ui/widgets.css`,
		`/static/ui/shell.css`,
		`/static/ui/pages.css`,
		`/static/app-local.css`,
	}
	last := -1
	for _, href := range order {
		idx := strings.Index(body, href)
		if idx < 0 {
			t.Fatalf("head 缺少 %s", href)
		}
		if idx < last {
			t.Fatalf("head 样式顺序错误：%s 出现在前一个之前", href)
		}
		last = idx
	}
}

// slotNamesInOrder 按文档顺序抽取 data-slot 值（golden 断言用）。
func slotNamesInOrder(body string) []string {
	const prefix = `data-slot="`
	out := []string{}
	for {
		index := strings.Index(body, prefix)
		if index < 0 {
			return out
		}
		body = body[index+len(prefix):]
		end := strings.Index(body, `"`)
		if end < 0 {
			return out
		}
		out = append(out, body[:end])
		body = body[end:]
	}
}

// 12 个插槽按 a_da 的页面归属分布：对话页 10 个，扩展页与概览页各 1 个。
func TestAllTwelveSlotsHostedAcrossPages(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending).
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[]}`)
	srv, _ := newServerWithWorkspaces(t, core, []string{"/tmp/ws-a", "/tmp/ws-b"})

	chatBody := serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	got := slotNamesInOrder(chatBody)
	want := []string{
		"sidebar.top",
		"sidebar.bottom",
		"topbar.right",
		"chat.corner",
		"transcript.after",
		"composer.above",
		"thread.tray",
		"composer.left",
		"composer.right",
		"composer.footer",
	}
	if len(got) != len(want) {
		t.Fatalf("对话页 data-slot 数量 = %d, want %d：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("对话页第 %d 个 data-slot = %q, want %q（全部：%v）", i+1, got[i], want[i], got)
		}
	}
	if strings.Count(chatBody, `class="ada-slot"`) != len(want) {
		t.Fatalf("对话页 ada-slot 容器数量 = %d, want %d", strings.Count(chatBody, `class="ada-slot"`), len(want))
	}

	extBody := serve(t, srv, "GET", "/extensions", nil).Body.String()
	wantContains(t, extBody, `data-slot="extensions.list"`)

	ovBody := serve(t, srv, "GET", "/overview", nil).Body.String()
	wantContains(t, ovBody, `data-slot="dashboard.widgets"`)

	// 三页合起来必须刚好覆盖 12 个契约插槽。
	all := map[string]bool{}
	for _, body := range []string{chatBody, extBody, ovBody} {
		for _, name := range slotNamesInOrder(body) {
			all[name] = true
		}
	}
	for _, name := range []string{
		"sidebar.top", "sidebar.bottom", "topbar.right", "chat.corner",
		"composer.left", "composer.right", "composer.footer", "composer.above",
		"thread.tray", "transcript.after", "extensions.list", "dashboard.widgets",
	} {
		if !all[name] {
			t.Fatalf("插槽 %s 没有宿主位置（已见：%v）", name, all)
		}
	}
}

// 工作区与插件详情也是 AdaUI 岛：服务端标记 + 初始状态 + 资源按页加载。
func TestWorkspacesAndPluginDetailUseAdaUI(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[{"id":"tools-fs","name":"文件工具","version":"1.2.0","enabled":true,"status":"ready","contributions":{"tools":["read"],"frontend":false,"backend":true}}]}`)
	srv, _ := newServerWithWorkspaces(t, core, []string{"/tmp/ws-a", "/tmp/ws-b"})

	body := serve(t, srv, "GET", "/workspaces", nil).Body.String()
	for _, want := range []string{
		`data-ada-island="workspaces"`,
		`data-ada-state=`,
		`ada-render.js`,
		`ada-ui.js`,
		`islands.js`,
		`ada-ui.css`,
		`action="/workspaces/select"`,
		`name="path"`,
		"ws-a",
	} {
		wantContains(t, body, want)
	}

	detail := serve(t, srv, "GET", "/extensions/tools-fs", nil).Body.String()
	for _, want := range []string{
		`data-ada-island="plugin-detail"`,
		`data-ada-state=`,
		`文件工具`,
		`ada-render.js`,
		`ada-ui.js`,
		`islands.js`,
		`ada-ui.css`,
		`action="/extensions/tools-fs/disable"`,
	} {
		wantContains(t, detail, want)
	}
}

// 概览页是第一个 AdaUI 客户端岛：SSR 回退仍在，同时暴露 JSON 状态与轮询接口。
func TestOverviewIslandAndDashboardJSON(t *testing.T) {
	core := newFakeCore().on("thread.list", `{
	  "threads":[{"id":"th_1","title":"第一个会话","updatedAt":"2026-10-28T10:00:00Z","archived":false}],
	  "nextCursor":"","hasMore":false
	}`)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/overview", nil).Body.String()
	for _, want := range []string{
		`data-ada-island="overview"`,
		`data-ada-state=`,
		`ada-render.js`,
		`ada-ui.js`,
		`islands.js`,
		`ada-ui.css`,
		`第一个会话`,
		`ov-kpis`,
		`ov-contrib`,
		`dashboard.widgets`,
		`ov-status`,
	} {
		wantContains(t, body, want)
	}
	// 旧 htmx 轮询必须让位给岛，否则会把客户端 DOM 整段换掉。
	wantNotContains(t, body, `hx-get="/partials/dashboard"`)

	rec := serve(t, srv, "GET", "/partials/dashboard.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/dashboard.json = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("dashboard.json Content-Type = %q", got)
	}
	var state struct {
		Stats []struct {
			Label string `json:"label"`
			Value string `json:"value"`
			Hint  string `json:"hint"`
			Tone  string `json:"tone"`
		} `json:"stats"`
		Recent []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			UpdatedLabel string `json:"updatedLabel"`
		} `json:"recent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("dashboard.json 不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if len(state.Stats) != 4 {
		t.Fatalf("stats 数量 = %d, want 4", len(state.Stats))
	}
	if len(state.Recent) != 1 || state.Recent[0].ID != "th_1" || state.Recent[0].Title != "第一个会话" {
		t.Fatalf("recent = %+v", state.Recent)
	}
	if state.Recent[0].UpdatedLabel == "" {
		t.Fatal("recent.updatedLabel 为空")
	}
}

// 插件列表是第二个 AdaUI 客户端岛；SSR 表格仍在，JSON 供客户端/刷新使用。
func TestExtensionsIslandAndJSON(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[{"id":"tools-fs","name":"文件工具","version":"1.2.0","enabled":true,"status":"ready","contributions":{"tools":["read"],"frontend":false,"backend":true}}]}`)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/extensions", nil).Body.String()
	for _, want := range []string{
		`data-ada-island="extensions"`,
		`data-ada-state=`,
		`文件工具`,
		`ex-toolbar`,
		`ex-row--head`,
		`ex-seg`,
		`ada-render.js`,
		`ada-ui.js`,
		`islands.js`,
	} {
		wantContains(t, body, want)
	}

	rec := serve(t, srv, "GET", "/partials/extensions.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/extensions.json = %d, want 200", rec.Code)
	}
	var state struct {
		Plugins []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
			Backend bool   `json:"backend"`
		} `json:"plugins"`
		Count int    `json:"count"`
		CSRF  string `json:"csrf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatalf("extensions.json 不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if state.Count != 1 || len(state.Plugins) != 1 {
		t.Fatalf("plugins/count = %+v/%d", state.Plugins, state.Count)
	}
	if state.Plugins[0].ID != "tools-fs" || state.Plugins[0].Name != "文件工具" || !state.Plugins[0].Backend {
		t.Fatalf("plugin = %+v", state.Plugins[0])
	}
	if state.CSRF != "csrf-token" {
		t.Fatalf("csrf = %q, want csrf-token", state.CSRF)
	}
}

// 对话流岛：首屏继续 SSR，浏览器拿 key + 服务端行 HTML 做 keyed patch。
func TestThreadTranscriptIslandAndJSON(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/threads/th_1", nil).Body.String()
	for _, want := range []string{
		`id="transcript-island"`,
		`data-ada-island="transcript"`,
		`data-ada-state=`,
		`ada-render.js`,
		`ada-ui.js`,
		`islands.js`,
		`ada-ui.css`,
		`data-thread-id="th_1"`,
		`data-item-key="e1"`,
	} {
		wantContains(t, body, want)
	}

	rec := serve(t, srv, "GET", "/partials/transcript/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/transcript/th_1 = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("transcript Content-Type = %q", got)
	}
	var payload struct {
		ThreadID  string `json:"threadId"`
		RunStatus string `json:"runStatus"`
		Rows      []struct {
			Key   string `json:"key"`
			Kind  string `json:"kind"`
			HTML  string `json:"html"`
			Parts []struct {
				Type  string          `json:"type"`
				Text  string          `json:"text"`
				Nodes json.RawMessage `json:"nodes"`
			} `json:"parts"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("transcript 不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	if payload.ThreadID != "th_1" || payload.RunStatus != "running" {
		t.Fatalf("thread/run = %q/%q", payload.ThreadID, payload.RunStatus)
	}
	if len(payload.Rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(payload.Rows))
	}
	if payload.Rows[0].Key != "e1" || !strings.Contains(payload.Rows[0].HTML, "这是问题") {
		t.Fatalf("row0 = %+v", payload.Rows[0])
	}
	// 助手行给的是结构化 parts：thinking 纯文本 + text 段 VNode 数据。
	if payload.Rows[1].Key != "e2" || payload.Rows[1].Kind != "assistant" || len(payload.Rows[1].Parts) != 2 {
		t.Fatalf("row1 = %+v", payload.Rows[1])
	}
	if payload.Rows[1].Parts[0].Type != "thinking" || payload.Rows[1].Parts[0].Text != "先想一下" {
		t.Fatalf("row1 thinking = %+v", payload.Rows[1].Parts[0])
	}
	if payload.Rows[1].Parts[1].Type != "text" || !strings.Contains(string(payload.Rows[1].Parts[1].Nodes), "答案是 42") {
		t.Fatalf("row1 text nodes = %+v", payload.Rows[1].Parts[1])
	}
	if payload.Rows[1].HTML != "" {
		t.Fatalf("assistant row 不应再带 HTML: %q", payload.Rows[1].HTML)
	}
	if payload.Rows[2].Key != "tool:call_1" || !strings.Contains(payload.Rows[2].HTML, "read_file") {
		t.Fatalf("row2 = %+v", payload.Rows[2])
	}
}

// a_da 外壳与 M2 组件的关键 class 必须真的出现在 DOM 里。
func TestThreadPageUsesAdaShellAndComponentDOM(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending)
	srv, _ := newServerWithWorkspaces(t, core, []string{"/tmp/ws-a", "/tmp/ws-b"})

	body := serve(t, srv, "GET", "/thread/th_1", nil).Body.String()

	for _, class := range []string{
		`class="ada-shell"`,
		`class="ada-sidebar"`,
		`class="ada-sidebar__resizer"`,
		`class="ada-side"`,
		`class="ada-ws"`,
		`class="ada-ws__summary"`,
		`class="ada-search"`,
		`class="ada-thread-list ada-thread-list--split"`,
		`class="ada-rail"`,
		`class="ada-main"`,
		`class="ada-topbar"`,
		`class="ada-topbar__actions"`,
		`class="ada-scroll"`,
		`class="ada-wrap"`,
		`class="ada-corner-dock"`,
		`class="ada-chat-viewport"`,
		`class="ada-trays"`,
		`class="ada-composer"`,
		`class="ada-composer__card"`,
		`class="ada-composer__group ada-composer__group--left"`,
		`class="ada-composer__group ada-composer__group--right"`,
		`class="ada-send"`,
		// 辅助区外壳由基座保留，内容由插件岛填充（插件未启用时这里是空壳）。
		`id="composer-aux"`,
		`data-slot="composer.footer"`,
		`class="ada-msg ada-msg--user"`,
		`class="ada-msg ada-msg--assistant"`,
		`class="ada-disclosure" data-kind="tool"`,
		`class="ada-avatar"`,
	} {
		wantContains(t, body, class)
	}

	// 时间顺序：user → assistant → tool，不能反向（EntriesBefore 已是升序）。
	userAt := strings.Index(body, "这是问题")
	assistantAt := strings.Index(body, "答案是 42")
	toolAt := strings.Index(body, "read_file")
	if userAt < 0 || assistantAt < userAt || toolAt < assistantAt {
		t.Fatalf("对话顺序不对：user=%d assistant=%d tool=%d", userAt, assistantAt, toolAt)
	}

	// 思考内容按普通说明文字渲染，不再塞进深色 code panel。
	wantContains(t, body, `class="ada-thinking"`)
	wantContains(t, body, "先想一下")

	// 工具卡的展示字段（duration / truncated / details）保留；成功的工具不再挂
	// 「成功」徽标——成功由行样式默认表达，失败才用红色左条与红色工具名。
	if strings.Contains(body, ">成功<") {
		t.Fatal("成功的工具不应再显示「成功」徽标")
	}
	wantContains(t, body, "12 ms")

	// 折叠偏好必须在侧栏 DOM 之前同步应用，否则每次导航都会“展开→收起”闪一下。
	bootAt := strings.Index(body, "sidebar-boot.js?v=")
	sidebarAt := strings.Index(body, `class="ada-sidebar"`)
	if bootAt < 0 || sidebarAt < 0 || bootAt > sidebarAt {
		t.Fatalf("sidebar-boot.js 必须在 .ada-sidebar 之前加载（boot=%d sidebar=%d）", bootAt, sidebarAt)
	}
	wantContains(t, body, "已截断")
	wantContains(t, body, "详情")

	// 无 JS / 实时增强契约仍在。
	wantContains(t, body, `id="transcript"`)
	wantContains(t, body, `id="live-stream"`)
	wantContains(t, body, `id="live-status"`)
	wantContains(t, body, `data-thread-id="th_1"`)
	wantContains(t, body, `data-run-status="running"`)
	wantContains(t, body, `<script src="/static/app.js?v=`)
	wantContains(t, body, `<script src="/static/chat-live.js?v=`)
	wantContains(t, body, `href="/workspaces"`)
	wantContains(t, body, `href="/overview"`)
	wantContains(t, body, `href="/extensions"`)
}

// 插件列表不再分前端/后端；归属只在详情页展示。
func TestPluginsUnifiedListAndDetail(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[
			{"id":"tools-fs","name":"文件工具","version":"1.2.0","enabled":true,"status":"ready","contributions":{"tools":["read","write"],"hooks":["tool.beforeCall"],"skills":[],"frontend":false,"backend":true}},
			{"id":"tools-git","name":"Git 工具","version":"1.2.0","enabled":false,"status":"unused","contributions":{"tools":[],"frontend":false,"backend":true}}
		]}`)
	ui := &stubUI{list: []web.FrontendPluginInfo{
		{ID: "ui-demo", Name: "演示前端", Version: "0.1.0", Status: "ready", PageCount: 1, SlotCount: 2, NavCount: 1, FirstPage: "home"},
	}}
	srv, _ := newServerWithUI(t, core, ui)

	body := serve(t, srv, "GET", "/extensions", nil).Body.String()
	wantContains(t, body, "文件工具")
	wantContains(t, body, "演示前端")
	// 随包但产品未声明的插件也在列表里，用「未启用」而不是 failed 表示。
	wantContains(t, body, "Git 工具")
	wantContains(t, body, "未启用")
	wantNotContains(t, body, "前端插件")
	wantNotContains(t, body, "后端插件")
	wantContains(t, body, `href="/extensions/tools-fs"`)
	wantContains(t, body, `href="/extensions/ui-demo"`)

	backend := serve(t, srv, "GET", "/extensions/tools-fs", nil).Body.String()
	wantContains(t, backend, "后端插件")
	wantContains(t, backend, "文件工具")
	wantContains(t, backend, "工具")

	unused := serve(t, srv, "GET", "/extensions/tools-git", nil).Body.String()
	wantContains(t, unused, "后端插件")
	wantContains(t, unused, "未启用")
	wantContains(t, unused, "当前产品没有默认启用这个插件")
	// 所有插件都能从界面启用/停用：unused 也要有「启用」。
	wantContains(t, unused, "action=\"/extensions/tools-git/enable\"")

	frontend := serve(t, srv, "GET", "/extensions/ui-demo", nil).Body.String()
	wantContains(t, frontend, "前端插件")
	wantContains(t, frontend, "演示前端")
	// 有独立页面的插件：页头动作组里同时给出「打开插件页面」。
	wantContains(t, frontend, `href="/ext/ui-demo/home"`)
	wantContains(t, frontend, "打开插件页面")

	if rec := serve(t, srv, "GET", "/extensions/missing", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("未知插件详情 = %d, want 404", rec.Code)
	}
}

// 插件详情：左侧文件树、右侧默认渲染 README；README/文件只对详情请求带
// detailId 拉取，列表轮询保持轻量。
func TestPluginDetailShowsReadmeAndFiles(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[{
			"id":"tools-fs","name":"文件工具","version":"1.2.0","apiVersion":1,"trust":"builtin",
			"enabled":true,"status":"ready","source":"workspace","path":"/tmp/plugins/tools-fs",
			"readme":"# 文件工具\n\n读写工作区文件。",
			"files":[
				{"path":"plugin.json","size":120},
				{"path":"static/todo.css","size":2048,"depth":1}
			],
			"contributions":{"tools":["read"],"frontend":true,"backend":true}
		}]}`)
	ui := &stubUI{list: []web.FrontendPluginInfo{{
		ID: "tools-fs", Name: "文件工具", Version: "1.2.0", Status: "ready",
		PageCount: 2, SlotCount: 3, NavCount: 1,
	}}}
	srv, _ := newServerWithUI(t, core, ui)

	body := serve(t, srv, "GET", "/extensions/tools-fs", nil).Body.String()
	for _, want := range []string{
		`class="pd-root"`,
		// 元数据从页面底部提到首屏信息条：字段、能力徽章、路径复制都在岛容器内。
		`class="pd-meta"`,
		`aria-label="插件信息"`,
		`pd-field__label">状态`,
		`pd-field__label">信任级别`,
		`pd-field__label">来源`,
		"插件目录",
		"1 工具", // 能力徽章（原来挤在三个 stat 里）
		"2 页面",
		"3 插槽",
		`data-pd-copy="path"`,
		`class="pd-doc__head"`,
		"README",
		"<h1>文件工具</h1>",
		"/tmp/plugins/tools-fs",
		"static/todo.css",
		"2.0 KB",
		"readmeNodes",
	} {
		wantContains(t, body, want)
	}
	if call, ok := core.lastCall("plugin.list"); !ok || call.Params["detailId"] != "tools-fs" {
		t.Fatalf("详情页 plugin.list 没有带 detailId: %+v", call.Params)
	}

	// 插件级动作（停用/启用、打开插件页面、返回列表）在页头：它们必须出现在岛屿容器之前，
	// 这样切文件、翻到「插件信息」面板时按钮不会跟着面板滚走。
	islandAt := strings.Index(body, `id="plugin-detail-island"`)
	if islandAt < 0 {
		t.Fatal("详情页缺少 plugin-detail-island 容器")
	}
	head := body[:islandAt]
	for _, want := range []string{
		`class="pd-head__title"`,
		`class="pd-chip pd-chip--mono"`,
		`class="pd-state`,
		`data-tip=`,
		`ada-section-head__actions pd-head__actions`,
		`action="/extensions/tools-fs/disable"`,
		`href="/extensions"`,
	} {
		wantContains(t, head, want)
	}

	// 列表页不需要 README/文件，不应每次轮询都读盘。
	serve(t, srv, "GET", "/extensions", nil)
	if call, ok := core.lastCall("plugin.list"); ok {
		if _, has := call.Params["detailId"]; has {
			t.Fatalf("列表页不应请求 detailId: %+v", call.Params)
		}
	}
}

// 插件文件预览：只允许清单内的相对路径；内容由服务端读取，前端点击按路径拉取。
func TestPluginFilePreview(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "demo-plugin")
	if err := os.MkdirAll(filepath.Join(root, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Demo\n\nhello plugin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.ts"), []byte("export const answer = 42;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "static", "todo.css"), []byte(".todo { color: red; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	// 父目录里的文件不在插件根内；即使伪造进文件清单也必须拒绝。
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("do not leak"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []map[string]any{
		{"path": "README.md", "size": 20},
		{"path": "index.ts", "size": 27},
		{"path": "static/todo.css", "size": 22, "depth": 1},
		{"path": "binary.bin", "size": 4},
		{"path": "../secret.txt", "size": 11},
	}
	payload, err := json.Marshal(map[string]any{"plugins": []map[string]any{{
		"id": "demo-plugin", "name": "演示插件", "path": root, "enabled": true, "status": "ready",
		"readme": "# Demo", "files": files,
		"contributions": map[string]any{"backend": true},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	core := newFakeCore().on("thread.list", emptyThreadList).on("plugin.list", string(payload))
	srv, _ := newServer(t, core)

	// 默认选中 README，服务端直接渲染内容；左侧文件树保留所有路径。
	page := serve(t, srv, "GET", "/extensions/demo-plugin", nil).Body.String()
	for _, want := range []string{
		`class="pd-tree__list"`,
		`data-plugin-file-path="index.ts"`,
		`data-plugin-file-content`,
		"hello plugin",
		`class="pd-tree__item is-active"`,
	} {
		wantContains(t, page, want)
	}

	// 无 JS 的回退：?file= 切换服务端渲染的选中文件。
	indexPage := serve(t, srv, "GET", "/extensions/demo-plugin?file=index.ts", nil).Body.String()
	wantContains(t, indexPage, "export const answer = 42;")

	// Markdown 文件带「预览 / 源码」分段；源码态正文仍是高亮代码块。
	wantContains(t, page, `class="pd-seg"`)
	wantContains(t, page, `class="pd-doc__code`)
	if strings.Contains(page, "pd-doc__preview") {
		t.Fatal("默认应是源码态，不应直接渲染预览")
	}
	// ?view=md 是无 JS 的预览入口：同一份 README 由服务端渲染成 HTML。
	preview := serve(t, srv, "GET", "/extensions/demo-plugin?file=README.md&view=md", nil).Body.String()
	for _, want := range []string{
		`class="markdown-body ada-markdown pd-doc__preview"`,
		"<h1>Demo</h1>",
		"切换到源码",
	} {
		wantContains(t, preview, want)
	}
	// 非 Markdown 文件没有这个分段控件。
	if strings.Contains(indexPage, `class="pd-seg"`) {
		t.Fatal("index.ts 不是 Markdown，不应出现预览/源码分段")
	}

	res := serve(t, srv, "GET", "/extensions/demo-plugin/file?path=index.ts", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("GET file = %d, body=%s", res.Code, res.Body.String())
	}
	var file struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Binary   bool   `json:"binary"`
		Size     int64  `json:"size"`
		SizeText string `json:"sizeLabel"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &file); err != nil {
		t.Fatalf("file JSON: %v (%s)", err, res.Body.String())
	}
	if file.Path != "index.ts" || !strings.Contains(file.Content, "answer = 42") || file.Binary || file.Size == 0 {
		t.Fatalf("文件预览内容错误: %+v", file)
	}

	binary := serve(t, srv, "GET", "/extensions/demo-plugin/file?path=binary.bin", nil)
	if binary.Code != http.StatusOK || !strings.Contains(binary.Body.String(), `"binary":true`) {
		t.Fatalf("二进制文件 = %d %s", binary.Code, binary.Body.String())
	}

	// 岛屿（有 JS 时）靠文件 JSON 里的 previewHtml 做切换，这里钉住字段存在且已渲染。
	mdRes := serve(t, srv, "GET", "/extensions/demo-plugin/file?path=README.md", nil)
	var mdFile struct {
		Markdown    bool   `json:"markdown"`
		PreviewHTML string `json:"previewHtml"`
	}
	if err := json.Unmarshal(mdRes.Body.Bytes(), &mdFile); err != nil {
		t.Fatalf("README JSON: %v (%s)", err, mdRes.Body.String())
	}
	if !mdFile.Markdown || !strings.Contains(mdFile.PreviewHTML, "<h1>Demo</h1>") {
		t.Fatalf("Markdown 预览字段缺失: %+v", mdFile)
	}
	if strings.Contains(res.Body.String(), `"markdown":true`) {
		t.Fatal("index.ts 不应带 markdown 标记")
	}
	for _, unsafe := range []string{"../secret.txt", "static/../../secret.txt", "missing.ts"} {
		if rec := serve(t, srv, "GET", "/extensions/demo-plugin/file?path="+url.QueryEscape(unsafe), nil); rec.Code != http.StatusNotFound {
			t.Fatalf("非法路径 %q = %d, want 404 (body=%s)", unsafe, rec.Code, rec.Body.String())
		}
	}
}

// 业务拒绝（例如禁用唯一审批策略插件）回到插件页用提示条说明，而不是 502 错误页。
func TestPluginToggleRejectionShowsFlash(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[{"id":"policy-default","name":"默认审批策略","enabled":true,"status":"ready","contributions":{"backend":true}}]}`).
		fail("plugin.setEnabled", errors.New("relay: RPC 错误 3005: plugin policy-default 是审批策略插件，目录里仍有写/执行工具，禁用会被拒绝（失败关闭，5.4）"))
	srv, _ := newServer(t, core)

	req := httptest.NewRequest(http.MethodPost, "/extensions/policy-default/disable", nil)
	req.Header.Set("Referer", "http://example.com/extensions")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("停用被拒 = %d, want 303 (body=%s)", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/extensions?error=") {
		t.Fatalf("Location = %q, want /extensions?error=…", loc)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	msg := u.Query().Get("error")
	wantContains(t, msg, "审批策略插件")
	wantNotContains(t, msg, "relay:")
	wantNotContains(t, msg, "RPC 错误")

	page := serve(t, srv, "GET", loc, nil).Body.String()
	wantContains(t, page, "审批策略插件")
	wantContains(t, page, `data-flash="error"`)
	wantNotContains(t, page, "Core 暂时不可用")
}

// 页面统一 no-referrer，浏览器不带 Referer；插件启停/重载必须靠表单里的
// next 回到插件页，而不是退回会话首页。
func TestPluginActionsReturnToExtensionsWithoutReferer(t *testing.T) {
	core := newFakeCore().
		on("plugin.setEnabled", `{}`).
		on("plugin.reload", `{}`)
	srv, _ := newServer(t, core)

	for _, tc := range []struct {
		target string
		next   string
		want   string
	}{
		{"/extensions/tools-fs/disable", "/extensions", "/extensions?notice="},
		{"/extensions/tools-fs/enable", "/extensions", "/extensions?notice="},
		{"/extensions/tools-fs/disable", "/extensions/tools-fs", "/extensions/tools-fs?notice="},
		{"/agent-plugins/reload", "/extensions", "/extensions?notice="},
	} {
		rec := serve(t, srv, "POST", tc.target, url.Values{"next": {tc.next}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s = %d (body=%s)", tc.target, rec.Code, rec.Body.String())
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, tc.want) {
			t.Fatalf("%s 回跳 = %q，期望 %s…（无 Referer）", tc.target, loc, tc.want)
		}
	}
}

// RenderSlot 是唯一的插槽输出函数：签名固定为 template.HTML，空贡献回退。
func TestRenderSlotWrapsBodyAndFallsBack(t *testing.T) {
	// 编译期固定签名：body/fallback 只能是 template.HTML，普通 string 不会通过。
	var _ func(string, template.HTML, int, template.HTML) template.HTML = web.RenderSlot

	withBody := string(web.RenderSlot("sidebar.top", template.HTML("<b>插件内容</b>"), 2, template.HTML("<i>回退</i>")))
	wantContains(t, withBody, `class="ada-slot" data-slot="sidebar.top" data-contributors="2"`)
	wantContains(t, withBody, "<b>插件内容</b>")
	wantNotContains(t, withBody, "<i>回退</i>")

	fallback := string(web.RenderSlot("composer.left", "", 0, template.HTML("<i>回退</i>")))
	wantContains(t, fallback, `class="ada-slot" data-slot="composer.left" data-contributors="0"`)
	wantContains(t, fallback, "<i>回退</i>")

	empty := string(web.RenderSlot("composer.right", "", 0, ""))
	wantContains(t, empty, `class="ada-slot" data-slot="composer.right" data-contributors="0"`)
	wantNotContains(t, empty, "回退")

	// 插槽名同样只输出一次且被转义，不会造成属性注入。
	injected := string(web.RenderSlot(`a"b`, "", 0, ""))
	wantContains(t, injected, `data-slot="a&#34;b"`)
}

func TestThreadDeleteCallsCoreAndRedirects(t *testing.T) {
	core := newFakeCore().on("thread.delete", `{"deleted":true,"cleanupScheduled":true}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/delete", url.Values{"next": {"/threads/th_2"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态 = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/threads/th_2?notice=") {
		t.Fatalf("回跳 = %q, want /threads/th_2?notice=…", loc)
	}
	call, ok := core.lastCall("thread.delete")
	if !ok || call.Params["threadId"] != "th_1" {
		t.Fatalf("thread.delete 参数 = %+v", call.Params)
	}

	// 删除当前会话：回首页，避免跳到已经不存在的 /threads/{id}。
	rec = serve(t, srv, "POST", "/threads/th_1/delete", url.Values{"next": {"/threads/th_1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态 = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/?notice=") {
		t.Fatalf("当前会话回跳 = %q, want /?notice=…", loc)
	}
}

func TestThreadArchiveCallsCoreAndRedirects(t *testing.T) {
	core := newFakeCore().on("thread.archive", `{"archived":true}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/archive", url.Values{"next": {"/threads/th_2"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态 = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "/threads/th_2?notice=") {
		t.Fatalf("回跳 = %q, want /threads/th_2?notice=…", loc)
	}
	call, ok := core.lastCall("thread.archive")
	if !ok || call.Params["threadId"] != "th_1" || call.Params["archived"] != true {
		t.Fatalf("thread.archive 参数 = %+v", call.Params)
	}

	// 归档当前会话：回首页，避免刷新后继续停在已从侧栏消失的页面。
	rec = serve(t, srv, "POST", "/threads/th_1/archive", url.Values{"next": {"/threads/th_1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态 = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/?notice=") {
		t.Fatalf("当前会话回跳 = %q, want /?notice=…", loc)
	}
}

func TestThreadArchiveRejectsRunningThread(t *testing.T) {
	core := newFakeCore().on("turn.get", `{"run":{"status":"running"},"queueLength":0}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_1/archive", url.Values{"next": {"/threads/th_1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("状态 = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=") {
		t.Fatalf("运行中归档回跳 = %q，期望带 error", loc)
	}
	if _, ok := core.lastCall("thread.archive"); ok {
		t.Fatal("运行中的会话不应调用 thread.archive")
	}
}

func TestThreadComposerShowsModelPicker(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending).
		on("model.list", `{"models":[
		  {"model":{"providerId":"openai","modelId":"gpt-4o"},"info":{"displayName":"GPT-4o","contextWindow":128000,"supportsTools":true}},
		  {"model":{"providerId":"custom","modelId":"deepseek"},"info":{"displayName":"DeepSeek","contextWindow":1000000,"supportsTools":true,"thinkingLevels":["low","high","max"]}}
		],"current":{"providerId":"custom","modelId":"deepseek","thinkingLevel":"high"}}`)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/thread/th_1", nil).Body.String()
	wantContains(t, body, `data-model-picker`)
	wantContains(t, body, `<ada-select class="ada-select ada-model-picker"`)
	wantContains(t, body, `data-value="openai|gpt-4o"`)
	wantContains(t, body, `data-value="custom|deepseek"`)
	wantContains(t, body, `data-label="DeepSeek"`)
	wantContains(t, body, `data-select-label`)
	wantContains(t, body, `data-levels="low|high|max"`)
	wantContains(t, body, `data-current-level="high"`)
	wantContains(t, body, `ada-model-picker__chevron`)
	wantNotContains(t, body, `data-model-level-picker`)
	// 无 JS 时的原生 select 回退：随消息提交，基座在 turn.start 前切模型。
	wantContains(t, body, `name="model"`)
	wantContains(t, body, `ada-select__fallback`)
	// 供应商管理入口由 provider-admin 插件贡献到 composer.left，
	// 基座模板只保留插槽宿位（不再写死插件 id 与页面路由）。
	wantContains(t, body, `data-slot="composer.left"`)
}

// 模型选择/推理等级切换：thinkingLevel 必须透传到 model.set。
func TestModelSelectPassesThinkingLevel(t *testing.T) {
	core := newFakeCore().on("model.set", `{"model":{"providerId":"custom","modelId":"deepseek","thinkingLevel":"high"},"scope":"thread"}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/model/select", url.Values{
		"providerId":    {"custom"},
		"modelId":       {"deepseek"},
		"thinkingLevel": {"high"},
		"threadId":      {"th_1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /model/select = %d (body=%s)", rec.Code, rec.Body.String())
	}
	call, ok := core.lastCall("model.set")
	if !ok {
		t.Fatal("没有调用 model.set")
	}
	model, _ := call.Params["model"].(map[string]any)
	if model["providerId"] != "custom" || model["modelId"] != "deepseek" || model["thinkingLevel"] != "high" {
		t.Fatalf("model.set 参数 = %#v", call.Params["model"])
	}
	if call.Params["threadId"] != "th_1" {
		t.Fatalf("threadId = %v", call.Params["threadId"])
	}
}

func TestThreadPageShowsRunError(t *testing.T) {
	core := newFakeCore().
		on("thread.get", `{
		  "thread": {"id":"th_err","workspace":"/tmp/ws","title":"失败会话","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},
		  "items": [], "seq": 1,
		  "run": {"runId":"run_err","status":"terminal","terminal":"error","errorCode":5001,"errorDetail":"provider: HTTP 401 invalid api key","startedAt":"2026-10-28T10:00:00Z","endedAt":"2026-10-28T10:00:01Z"},
		  "interactions": [], "hasMore": false
		}`).
		on("thread.entries", `{"entries":[],"hasMore":false}`).
		on("thread.list", emptyThreadList).
		on("model.list", `{"models":[],"current":{"providerId":"","modelId":""}}`)
	srv, _ := newServer(t, core)

	body := serve(t, srv, "GET", "/thread/th_err", nil).Body.String()
	wantContains(t, body, "本轮失败")
	wantContains(t, body, "供应商调用失败")
	wantContains(t, body, "错误码 5001")
	wantContains(t, body, "HTTP 401 invalid api key")
}

// 实时流把累积文本发到 /markdown：必须复用服务端渲染管线并清洗掉原始 HTML。
func TestMarkdownEndpointRendersAndSanitizes(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	body := "{\"text\":\"**粗体**\\n\\n```go\\nfmt.Println(1)\\n```\\n\\n<script>alert(1)</script>\"}"
	req := httptest.NewRequest(http.MethodPost, "/markdown", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	wantContains(t, out, "<strong>粗体</strong>")
	wantContains(t, out, "<pre><code")
	wantNotContains(t, out, "<script")
}

func TestMarkdownASTEndpointReturnsVNodeData(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	body := "{\"text\":\"**粗体**\\n\\n```go\\nfmt.Println(1)\\n```\\n\\n<script>alert(1)</script>\"}"
	req := httptest.NewRequest(http.MethodPost, "/markdown.ast", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	out := rec.Body.String()
	wantContains(t, out, `"t":"strong"`)
	wantContains(t, out, `"t":"pre"`)
	wantContains(t, out, `"t":"code"`)
	wantContains(t, out, "粗体")
	wantNotContains(t, out, `"t":"script"`)
}

func TestMarkdownEndpointRejectsInvalidJSON(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	req := httptest.NewRequest(http.MethodPost, "/markdown", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMarkdownEndpointRejectsOversizedBody(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	req := httptest.NewRequest(http.MethodPost, "/markdown",
		strings.NewReader(`{"text":"`+strings.Repeat("a", 512<<10)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}
