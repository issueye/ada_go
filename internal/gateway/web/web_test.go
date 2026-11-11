package web_test

// Web 宿主的垂直切片验收：假 Supervisor + 假 CoreClient 驱动主流程，
// 不依赖真实 Core、SQLite 或网络。整页 SSR 移除后：
//   - 页面路径（GET）一律返回 SPA shell（history fallback）；
//   - 数据走 JSON 端点（transcript/dashboard/extensions/state…）；
//   - POST 表单端点保持 303 PRG；错误页是无 JS 安全页。

import (
	"context"
	"encoding/json"
	"errors"
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

const emptyThreadList = `{"threads":[],"nextCursor":"","hasMore":false}`

const threadGetRunning = `{
  "thread": {"id":"th_1","workspace":"/tmp/ws","title":"测试会话","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},
  "items": [],
  "seq": 3,
  "run": {"runId":"run_1","status":"running","startedAt":"2026-10-28T10:00:00Z"},
  "interactions": [],
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

// ---- SPA shell（history fallback）----

// GET / 返回 SPA shell：#app 容器 + bootstrap JSON 投影 + 模块入口；
// 不再引用已删除的 SSR 脚本，也没有内联脚本。
func TestHomeReturnsShell(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	rec := serve(t, srv, "GET", "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	wantContains(t, body, `<div id="app"></div>`)
	wantContains(t, body, `id="ada-bootstrap"`)
	wantContains(t, body, `<script type="module" src="/static/ada/index.mjs?v=`)
	wantContains(t, body, `<title>`)
	// bootstrap 投影带身份与 CSRF。
	wantContains(t, body, `"productName":"测试产品"`)
	wantContains(t, body, `"csrf":"csrf-token"`)
	// SSR 时代的资源引用必须消失。
	for _, absent := range []string{"ada-render.js", "ada-ui.js", "chat-live.js", "app.js", "islands.js", "plugin-islands.js", "ada-ui.css"} {
		wantNotContains(t, body, absent)
	}
	// 只有外链脚本与 JSON 数据脚本，没有内联 JS。
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "<script") {
			continue
		}
		if !strings.Contains(line, `src=`) && !strings.Contains(line, `type="application/json"`) {
			t.Fatalf("出现内联脚本: %s", line)
		}
	}
}

// 未匹配路径的 GET 也是 shell（客户端路由给出 404 视图）；
// 其余方法渲染 404 错误页。
func TestUnknownPathFallsBackToShell(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	rec := serve(t, srv, "GET", "/no/such/page", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 未知路径 = %d, want 200 (shell)", rec.Code)
	}
	wantContains(t, rec.Body.String(), `<div id="app"></div>`)
	wantContains(t, rec.Body.String(), `"route":"/no/such/page"`)

	rec = serve(t, srv, "POST", "/no/such/page", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST 未知路径 = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	wantContains(t, body, "页面不存在")
	wantContains(t, body, "/no/such/page")
	// 不再出现 Go 默认的裸文本 404。
	wantNotContains(t, body, "404 page not found")
}

// 概览/工作区/扩展等旧页面路径现在都返回 shell。
func TestLegacyPagePathsServeShell(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	for _, target := range []string{"/overview", "/workspaces", "/extensions", "/threads/th_1", "/thread/th_1"} {
		rec := serve(t, srv, "GET", target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (shell)", target, rec.Code)
		}
		wantContains(t, rec.Body.String(), `id="ada-bootstrap"`)
	}
}

// ---- 创建会话 ---------------------------------------------------------------

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

	// 会话数据端点必须落在动态工作区的 Core 上。
	page := serveWithCookies(t, srv, "GET", "/partials/transcript/th_dyn", nil, cookie)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /partials/transcript/th_dyn = %d (body=%s)", page.Code, page.Body.String())
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

// ---- 对话流 JSON 数据行 ------------------------------------------------------

// transcriptJSON 拉取 /partials/transcript/{id} 并解码为通用形状。
func transcriptJSON(t *testing.T, srv *web.Server, threadID string) map[string]any {
	t.Helper()
	rec := serve(t, srv, "GET", "/partials/transcript/"+threadID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET transcript = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("transcript Content-Type = %q", ct)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("transcript 不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	return payload
}

// 全部行都是纯数据行：不再有 HTML 字段；助手行保持 VNode parts；
// 用户/工具/notice 行按 transcriptRowState 注释里的形状输出。
func TestTranscriptJSONDataRows(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesAscending)
	srv, _ := newServer(t, core)

	payload := transcriptJSON(t, srv, "th_1")
	if payload["threadId"] != "th_1" || payload["runStatus"] != "running" {
		t.Fatalf("thread/run = %v/%v", payload["threadId"], payload["runStatus"])
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	// 没有任何行带服务端渲染的 HTML。
	wantNotContains(t, string(raw), `"html"`)
	// 用户消息原文不能以未转义 HTML 形态出现（JSON 字符串转义是浏览器的职责）。
	wantNotContains(t, string(raw), `<script>`)

	rows, _ := payload["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}

	// row0：用户行是 parts 数据。
	user, _ := rows[0].(map[string]any)
	if user["key"] != "e1" || user["kind"] != "user" {
		t.Fatalf("row0 = %+v", user)
	}
	userParts, _ := user["parts"].([]any)
	if len(userParts) != 1 {
		t.Fatalf("user parts = %+v", userParts)
	}
	part0, _ := userParts[0].(map[string]any)
	if part0["type"] != "text" || part0["text"] != "这是问题" {
		t.Fatalf("user part = %+v", part0)
	}

	// row1：助手行保持 VNode 形态（thinking 纯文本 + text 段 nodes）。
	assistant, _ := rows[1].(map[string]any)
	if assistant["key"] != "e2" || assistant["kind"] != "assistant" {
		t.Fatalf("row1 = %+v", assistant)
	}
	assistantParts, _ := assistant["parts"].([]any)
	if len(assistantParts) != 2 {
		t.Fatalf("assistant parts = %+v", assistantParts)
	}
	thinking, _ := assistantParts[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["text"] != "先想一下" {
		t.Fatalf("thinking part = %+v", thinking)
	}
	textPart, _ := assistantParts[1].(map[string]any)
	nodesJSON, _ := json.Marshal(textPart["nodes"])
	if textPart["type"] != "text" || !strings.Contains(string(nodesJSON), "答案是 42") {
		t.Fatalf("text part = %+v", textPart)
	}

	// row2：工具行是 tool 对象（旧 SSR 模板 row→tool 的字段投影）。
	tool, _ := rows[2].(map[string]any)
	if tool["key"] != "tool:call_1" || tool["kind"] != "tool" {
		t.Fatalf("row2 = %+v", tool)
	}
	toolObj, _ := tool["tool"].(map[string]any)
	if toolObj == nil || toolObj["name"] != "read_file" || toolObj["status"] != "ok" {
		t.Fatalf("tool 对象 = %+v", toolObj)
	}
	if toolObj["durationMs"] != float64(12) || toolObj["truncated"] != true {
		t.Fatalf("tool receipt 字段 = %+v", toolObj)
	}
	if toolObj["output"] != "file body" {
		t.Fatalf("tool output = %v", toolObj["output"])
	}
}

// 空闲页的用户行带「编辑并重发」上下文；运行中不带（避免改写落库中的历史）。
func TestTranscriptUserRowEditContext(t *testing.T) {
	idle := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesResend)
	srv, _ := newServer(t, idle)

	raw, err := json.Marshal(transcriptJSON(t, srv, "th_1"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{
		`"canEdit":true`,
		`"editKey":"`,
		`"editText":"第二问"`,
		`"threadId":"th_1"`,
		`"csrf":"csrf-token"`,
	} {
		wantContains(t, body, want)
	}

	running := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entriesResend)
	srv2, _ := newServer(t, running)
	raw2, err := json.Marshal(transcriptJSON(t, srv2, "th_1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{`"canEdit"`, `"editKey"`, `"editText"`, `/resend`} {
		wantNotContains(t, string(raw2), absent)
	}
}

// 用户行里的图片以 data URL 两半输出（mime/data），由客户端拼 src。
func TestTranscriptUserImagePart(t *testing.T) {
	entries := `{"entries":[{
	  "id":"e1","threadId":"th_1","seq":1,"kind":"message","role":"user",
	  "payload":{"role":"user","parts":[
	    {"type":"text","text":"看这张图"},
	    {"type":"image","mime":"image/png","data":"iVBORw0KGgo="}
	  ]},
	  "createdAt":"2026-10-28T10:00:01Z"}],"hasMore":false}`
	core := newFakeCore().
		on("thread.get", threadGetRunning).
		on("thread.entries", entries)
	srv, _ := newServer(t, core)

	raw, err := json.Marshal(transcriptJSON(t, srv, "th_1"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{`"mime":"image/png"`, `"data":"iVBORw0KGgo="`, "看这张图"} {
		wantContains(t, body, want)
	}
}

// 用户消息里的 HTML 文本以 JSON 字符串形态输出（编码转义），而不是未转义 HTML。
func TestTranscriptJSONEscapesMessageContent(t *testing.T) {
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
	core := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", string(entries))
	srv, _ := newServer(t, core)

	raw, err := json.Marshal(transcriptJSON(t, srv, "th_1"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	wantNotContains(t, body, `<script>alert("x")</script>`)
	// JSON 编码把 < 转成 \u003c（json.Marshal 默认行为），解码后仍是原文。
	wantContains(t, body, `\u003cscript\u003e`)
}

// ---- 会话状态与投递 ---------------------------------------------------------

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

// Core 失败要转成人话错误页：线程不存在 404，其余 502，都不 panic。
// （页面路径已全部改为 shell fallback，错误页只由 POST 端点触达。）
func TestCoreErrorRendersPageInsteadOfPanicking(t *testing.T) {
	core := newFakeCore().fail("turn.cancel", errors.New("thread not found"))
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "POST", "/threads/th_missing/abort", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("线程不存在 = %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	wantContains(t, body, "没有找到这个会话")
	wantContains(t, body, "重试")
	wantContains(t, body, "/threads/th_missing")

	// Core 不可用（非「不存在」）走 502，也不 panic。
	core2 := newFakeCore().fail("turn.cancel", errors.New("dial core: connection refused"))
	srv2, _ := newServer(t, core2)
	rec = serve(t, srv2, "POST", "/threads/th_1/abort", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Core 不可用 = %d, want 502", rec.Code)
	}
	wantContains(t, rec.Body.String(), "Core 暂时不可用")
}

// ---- 工作区与周边路由 -------------------------------------------------------

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

	// 旧的重定向端点已删除：GET /workspace 落入 shell fallback。
	rec = serve(t, srv, "GET", "/workspace", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="ada-bootstrap"`) {
		t.Fatalf("GET /workspace = %d，期望 shell", rec.Code)
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

// 数据端点必须用 cookie 指定的工作区取 Core（索引校验后越界回退第一个）。
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
			rec := serveWithCookies(t, srv, "GET", "/partials/dashboard.json", nil,
				&http.Cookie{Name: web.WorkspaceCookieName, Value: tc.value})
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /partials/dashboard.json = %d", rec.Code)
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

// ---- 静态资源 ---------------------------------------------------------------

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
		{"/static/api.js", ""},
		{"/static/ada-select.js", ""},
		{"/static/ada-modal.js", ""},
		{"/static/file-picker.js", ""},
		{"/static/chat-images.js", ""},
		{"/static/theme.js", ""},
		{"/static/themes.js", ""},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			rec := serve(t, srv, "GET", tc.target, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s = %d", tc.target, rec.Code)
			}
			if strings.HasSuffix(tc.target, ".css") {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
					t.Fatalf("%s Content-Type = %q", tc.target, ct)
				}
			} else {
				if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
					t.Fatalf("%s Content-Type = %q", tc.target, ct)
				}
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
	srv, _ := newServer(t, newFakeCore())
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

// ---- 插件 -------------------------------------------------------------------

// 插件文件预览：只允许清单内的相对路径；内容由服务端读取，前端按路径拉取。
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

	res := serve(t, srv, "GET", "/extensions/demo-plugin/file?path=index.ts", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("GET file = %d, body=%s", res.Code, res.Body.String())
	}
	var file struct {
		Path     string `json:"path"`
		Content  string `json:"content"`
		Binary   bool   `json:"binary"`
		Markdown bool   `json:"markdown"`
		Size     int64  `json:"size"`
		SizeText string `json:"sizeLabel"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &file); err != nil {
		t.Fatalf("file JSON: %v (%s)", err, res.Body.String())
	}
	if file.Path != "index.ts" || !strings.Contains(file.Content, "answer = 42") || file.Binary || file.Size == 0 {
		t.Fatalf("文件预览内容错误: %+v", file)
	}
	if file.Markdown {
		t.Fatal("index.ts 不应带 markdown 标记")
	}
	// 高亮/预览 HTML 已随 SSR 移除：JSON 不再携带 html/previewHtml 字段。
	wantNotContains(t, res.Body.String(), `"html"`)
	wantNotContains(t, res.Body.String(), `"previewHtml"`)

	binary := serve(t, srv, "GET", "/extensions/demo-plugin/file?path=binary.bin", nil)
	if binary.Code != http.StatusOK || !strings.Contains(binary.Body.String(), `"binary":true`) {
		t.Fatalf("二进制文件 = %d %s", binary.Code, binary.Body.String())
	}

	// Markdown 文件带 markdown 标记：预览渲染由前端做。
	mdRes := serve(t, srv, "GET", "/extensions/demo-plugin/file?path=README.md", nil)
	var mdFile struct {
		Markdown bool   `json:"markdown"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(mdRes.Body.Bytes(), &mdFile); err != nil {
		t.Fatalf("README JSON: %v (%s)", err, mdRes.Body.String())
	}
	if !mdFile.Markdown || !strings.Contains(mdFile.Content, "hello plugin") {
		t.Fatalf("Markdown 文件标记缺失: %+v", mdFile)
	}
	for _, unsafe := range []string{"../secret.txt", "static/../../secret.txt", "missing.ts"} {
		if rec := serve(t, srv, "GET", "/extensions/demo-plugin/file?path="+url.QueryEscape(unsafe), nil); rec.Code != http.StatusNotFound {
			t.Fatalf("非法路径 %q = %d, want 404 (body=%s)", unsafe, rec.Code, rec.Body.String())
		}
	}
}

// 业务拒绝（例如插件启停被后端拒绝）回到来源页用提示条说明，而不是 502 错误页。
func TestPluginToggleRejectionShowsFlash(t *testing.T) {
	core := newFakeCore().
		on("thread.list", emptyThreadList).
		on("plugin.list", `{"plugins":[{"id":"demo-plugin","name":"示例插件","enabled":true,"status":"ready","contributions":{"backend":true}}]}`).
		fail("plugin.setEnabled", errors.New("relay: RPC 错误 3005: plugin demo-plugin 正在被装配占用，暂时不能停用"))
	srv, _ := newServer(t, core)

	req := httptest.NewRequest(http.MethodPost, "/extensions/demo-plugin/disable", nil)
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
	wantContains(t, msg, "正在被装配占用")
	wantNotContains(t, msg, "relay:")
	wantNotContains(t, msg, "RPC 错误")

	// 回跳地址是页面路径 → shell；flash 提示条由 shell 直接表达。
	page := serve(t, srv, "GET", loc, nil).Body.String()
	wantContains(t, page, "正在被装配占用")
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

// ---- JSON 端点（概览/Markdown AST） ----------------------------------------

func TestOverviewJSONEndpoint(t *testing.T) {
	core := newFakeCore().on("thread.list", `{
	  "threads":[{"id":"th_1","title":"第一个会话","updatedAt":"2026-10-28T10:00:00Z","archived":false}],
	  "nextCursor":"","hasMore":false
	}`)
	srv, _ := newServer(t, core)

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

	// Core 不可用时如实报 503，不伪造空概览。
	core2 := newFakeCore().fail("thread.list", errors.New("core down"))
	srv2, _ := newServer(t, core2)
	rec = serve(t, srv2, "GET", "/partials/dashboard.json", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Core 失败 = %d, want 503", rec.Code)
	}
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

// HTML 版 /markdown 已删除：ast 是唯一路径。
func TestMarkdownHTMLEndpointRemoved(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())
	req := httptest.NewRequest(http.MethodPost, "/markdown", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /markdown = %d, want 404", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/markdown.ast", strings.NewReader("not json"))
	req2.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("ast 非法 JSON = %d, want 400", rec2.Code)
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/markdown.ast",
		strings.NewReader(`{"text":"`+strings.Repeat("a", 512<<10)+`"}`))
	req3.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ast 超限 = %d, want 413", rec3.Code)
	}
}

// ---- 12 插槽词汇表 ----------------------------------------------------------

// 宿主模板已随 SSR 删除，但 12 个插槽名仍是冻结契约：
// CheckSlotCoverage 守护词汇表自身的一致性（adag verify 的 checkSlots 复用它）。
func TestCheckSlotCoverage(t *testing.T) {
	if err := web.CheckSlotCoverage(); err != nil {
		t.Fatalf("CheckSlotCoverage: %v", err)
	}
}

// ---- 会话删除/归档 ----------------------------------------------------------

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

// ---- SPA 数据端点（基座页面重建新增）----

// /partials/threads.json：thread.list 的展示投影（侧栏/会话列表页共用）。
func TestThreadsJSONEndpoint(t *testing.T) {
	core := newFakeCore().on("thread.list", `{"threads":[
		{"id":"th_1","title":"排查构建失败","headEntryId":"e9","updatedAt":"2026-11-01T09:00:00Z","archived":false,"running":true},
		{"id":"th_2","title":"写周报","headEntryId":"e3","updatedAt":"2026-10-30T18:00:00Z","archived":false,"running":false}
	],"nextCursor":"","hasMore":false}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/threads.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/threads.json = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Workspace     string `json:"workspace"`
		WorkspaceName string `json:"workspaceName"`
		Threads       []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updatedAt"`
			Running   bool   `json:"running"`
			Archived  bool   `json:"archived"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (body=%s)", err, rec.Body.String())
	}
	if out.Workspace != "/tmp/ws" || out.WorkspaceName != "ws" {
		t.Fatalf("workspace 投影 = %q / %q", out.Workspace, out.WorkspaceName)
	}
	if len(out.Threads) != 2 || out.Threads[0].ID != "th_1" || out.Threads[0].Title != "排查构建失败" || !out.Threads[0].Running {
		t.Fatalf("threads = %+v", out.Threads)
	}
}

// ?path= 只接受网关已知的工作区；未知目录直接 400，不把任意路径拉起 Core。
func TestThreadsJSONRejectsUnknownWorkspace(t *testing.T) {
	core := newFakeCore().on("thread.list", `{"threads":[],"nextCursor":"","hasMore":false}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/threads.json?path="+url.QueryEscape("/elsewhere"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知工作区 = %d，期望 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// ?limit= 透传给 thread.list（侧栏只取最近 N 条）。
func TestThreadsJSONPassesLimit(t *testing.T) {
	core := newFakeCore().on("thread.list", `{"threads":[],"nextCursor":"","hasMore":false}`)
	srv, _ := newServer(t, core)

	serve(t, srv, "GET", "/partials/threads.json?limit=50", nil)
	call, ok := core.lastCall("thread.list")
	if !ok || call.Params["limit"] != 50 {
		t.Fatalf("thread.list 参数 = %+v，期望 limit=50", call.Params)
	}
}

// /partials/model.json：model.list 的展示投影（HEAD 模板选项注入的 JSON 等价物）。
func TestModelJSONEndpoint(t *testing.T) {
	core := newFakeCore().on("model.list", `{"models":[
		{"model":{"providerId":"openai","modelId":"gpt-4o"},"info":{"displayName":"GPT-4o","thinkingLevels":["low","high"]}},
		{"model":{"providerId":"custom","modelId":"deepseek-r1"},"info":{"displayName":"DeepSeek R1"}}
	],"current":{"providerId":"custom","modelId":"deepseek-r1","thinkingLevel":"high"}}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/model.json?threadId=th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partials/model.json = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Models []struct {
			Value          string   `json:"value"`
			ProviderID     string   `json:"providerId"`
			ModelID        string   `json:"modelId"`
			Label          string   `json:"label"`
			ShortLabel     string   `json:"shortLabel"`
			Current        bool     `json:"current"`
			ThinkingLevels []string `json:"thinkingLevels"`
		} `json:"models"`
		CurrentLabel string `json:"currentLabel"`
		CurrentValue string `json:"currentValue"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(out.Models) != 2 {
		t.Fatalf("models = %+v", out.Models)
	}
	first := out.Models[0]
	if first.Value != "openai|gpt-4o" || first.Label != "GPT-4o · gpt-4o" || first.ShortLabel != "GPT-4o" {
		t.Fatalf("第一项投影 = %+v", first)
	}
	if len(first.ThinkingLevels) != 2 || first.ThinkingLevels[0] != "low" {
		t.Fatalf("thinkingLevels = %+v", first.ThinkingLevels)
	}
	if out.CurrentLabel != "DeepSeek R1" || out.CurrentValue != "custom|deepseek-r1" {
		t.Fatalf("current 投影 = %q / %q", out.CurrentLabel, out.CurrentValue)
	}
	if call, ok := core.lastCall("model.list"); !ok || call.Params["threadId"] != "th_1" {
		t.Fatalf("model.list 参数 = %+v", call.Params)
	}
}

// model.list 失败时不阻塞页面：返回「选择模型」占位投影。
func TestModelJSONCoreError(t *testing.T) {
	core := newFakeCore().fail("model.list", errors.New("core down"))
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/model.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("model.list 失败 = %d，期望 200 占位", rec.Code)
	}
	var out struct {
		CurrentLabel string `json:"currentLabel"`
		Models       []any  `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CurrentLabel != "选择模型" || out.Models == nil {
		t.Fatalf("占位投影 = %q / %+v", out.CurrentLabel, out.Models)
	}
}

// /threads/{id}/state 现在携带 pending 交互投影（基座托盘外壳的数据面）；
// form 交互的题目从 body 解析成 q0..qN 形态，已处理的交互不再出现。
func TestThreadStateIncludesInteractions(t *testing.T) {
	core := newFakeCore().on("thread.get", `{
		"thread":{"id":"th_1","workspace":"","title":"","createdAt":"","updatedAt":"","archived":false},
		"items":[],"seq":1,
		"run":{"runId":"run_1","status":"waiting_input"},
		"interactions":[
			{"id":"ix_1","threadId":"th_1","kind":"confirm","title":"允许写文件","body":"","status":"pending","options":[]},
			{"id":"ix_2","threadId":"th_1","kind":"form","title":"补充信息","body":"[{\"question\":\"叫什么\",\"kind\":\"input\"},{\"question\":\"确定吗\",\"kind\":\"confirm\"}]","status":"pending","options":[]},
			{"id":"ix_3","threadId":"th_1","kind":"select","title":"选一个","body":"","status":"resolved","options":["a","b"]}
		],
		"hasMore":false}`)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/threads/th_1/state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("state = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Interactions []struct {
			ID        string   `json:"id"`
			Kind      string   `json:"kind"`
			Title     string   `json:"title"`
			Body      string   `json:"body"`
			Options   []string `json:"options"`
			Questions []struct {
				Index    int    `json:"index"`
				Question string `json:"question"`
				Kind     string `json:"kind"`
			} `json:"questions"`
		} `json:"interactions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(out.Interactions) != 2 {
		t.Fatalf("只应保留 pending 交互: %+v", out.Interactions)
	}
	form := out.Interactions[1]
	if form.ID != "ix_2" || form.Body != "" || len(form.Questions) != 2 {
		t.Fatalf("form 交互投影 = %+v", form)
	}
	if form.Questions[0].Index != 0 || form.Questions[0].Kind != "input" || form.Questions[1].Kind != "confirm" {
		t.Fatalf("题目投影 = %+v", form.Questions)
	}
}

// transcript ?before 游标：透传给 thread.entries，响应带 hasMore/nextCursor
// （「加载更早消息」的数据面）。
func TestTranscriptBeforePagination(t *testing.T) {
	threadGet := `{"thread":{"id":"th_1","workspace":"","title":"","createdAt":"","updatedAt":"","archived":false},"items":[],"seq":1,"run":{"status":"terminal","terminal":"completed"},"interactions":[],"hasMore":false}`
	core := newFakeCore()
	core.callFn = func(method string, params any) (json.RawMessage, error) {
		if method == "thread.entries" {
			m, _ := params.(map[string]any)
			if m["cursor"] != "c previous" {
				return nil, errors.New("cursor 未透传")
			}
			if m["limit"] != 50 {
				return nil, errors.New("limit 未透传")
			}
			// 至少一条 entry：entriesUsable 才为真，hasMore/nextCursor 才会投影。
			return json.RawMessage(`{"entries":[{"id":"e0","threadId":"th_1","seq":1,"kind":"message","role":"user","payload":{"role":"user","parts":[{"type":"text","text":"旧问题"}]},"createdAt":"2026-10-28T09:59:00Z"}],"nextCursor":"c older","hasMore":true}`), nil
		}
		if method == "thread.get" {
			return json.RawMessage(threadGet), nil
		}
		return nil, errors.New("fake: 未配置方法 " + method)
	}
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/transcript/th_1?before="+url.QueryEscape("c previous")+"&limit=50", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("transcript?before = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		HasMore    bool   `json:"hasMore"`
		NextCursor string `json:"nextCursor"`
		Rows       []any  `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.HasMore || out.NextCursor != "c older" {
		t.Fatalf("分页投影 = %+v", out)
	}
}

// 不带 before 的常规请求不带分页字段（rows-only 形态保持不变）。
func TestTranscriptJSONNoPaginationFieldsByDefault(t *testing.T) {
	core := newFakeCore().
		on("thread.get", threadGetIdle).
		on("thread.entries", entriesAscending)
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/transcript/th_1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("transcript = %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["hasMore"]; ok {
		t.Fatalf("常规请求不应带 hasMore: %s", rec.Body.String())
	}
	if _, ok := raw["nextCursor"]; ok {
		t.Fatalf("常规请求不应带 nextCursor: %s", rec.Body.String())
	}
}

// /partials/extensions.json?detail= 按详情口径返回该插件（Files/Readme），
// 供插件详情页复用同一端点。
func TestExtensionsJSONDetailIncludesFiles(t *testing.T) {
	core := newFakeCore()
	core.callFn = func(method string, params any) (json.RawMessage, error) {
		if method == "plugin.list" {
			m, _ := params.(map[string]any)
			detailID, _ := m["detailId"].(string)
			if detailID != "" && detailID != "demo" {
				return json.RawMessage(`{"plugins":[]}`), nil
			}
			files := `[]`
			readme := ""
			if detailID == "demo" {
				files = `[{"path":"plugin.json","size":120,"sizeLabel":"120 B"}]`
				readme = "# 演示插件"
			}
			return json.RawMessage(`{"plugins":[{"id":"demo","name":"演示","version":"1.0.0","enabled":true,"status":"ready","readme":"` + readme + `","files":` + files + `,"contributions":{"backend":true}}]}`), nil
		}
		return nil, errors.New("fake: 未配置方法 " + method)
	}
	srv, _ := newServer(t, core)

	rec := serve(t, srv, "GET", "/partials/extensions.json?detail=demo", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("extensions.json?detail = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Plugins []struct {
			ID     string `json:"id"`
			Readme string `json:"readme"`
			Files  []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Plugins) != 1 || out.Plugins[0].ID != "demo" {
		t.Fatalf("plugins = %+v", out.Plugins)
	}
	if len(out.Plugins[0].Files) != 1 || out.Plugins[0].Files[0].Path != "plugin.json" {
		t.Fatalf("详情口径应包含文件清单: %+v", out.Plugins[0].Files)
	}
	if out.Plugins[0].Readme == "" {
		t.Fatal("详情口径应包含 README")
	}
}
