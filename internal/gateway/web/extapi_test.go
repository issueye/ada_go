package web_test

// 插件动态 HTTP API（route）在 Gateway 侧的转发契约：
// 统一走 Core 的 plugin.http，把请求方法/路径/查询/JSON 体透传，
// 并把插件的 404/405 与失败收敛成统一的 JSON 错误形状。

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveExt(t *testing.T, srv interface {
	Handler() http.Handler
}, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestExtAPIForwardsToPluginHTTP(t *testing.T) {
	core := newFakeCore().on("plugin.http",
		`{"ok":true,"result":{"echo":{"n":2},"plugin":"tools-fs"}}`)
	srv, _ := newServer(t, core)

	rec := serveExt(t, srv, http.MethodPost, "/ext/tools-fs/api/echo?path=a%20b&threadId=th_9", `{"n":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 = %d, want 200；body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q", got)
	}
	wantContains(t, rec.Body.String(), `"plugin":"tools-fs"`)

	call, ok := core.lastCall("plugin.http")
	if !ok {
		t.Fatal("没有调用 plugin.http")
	}
	if call.Params["pluginId"] != "tools-fs" || call.Params["method"] != "POST" || call.Params["path"] != "/echo" {
		t.Fatalf("plugin.http 参数 = %+v", call.Params)
	}
	query, _ := call.Params["query"].(map[string]string)
	if query["path"] != "a b" {
		t.Fatalf("query = %+v", query)
	}
	body, _ := call.Params["body"].(map[string]any)
	if body["n"] != float64(2) {
		t.Fatalf("body = %+v", body)
	}
	if call.Params["threadId"] != "th_9" {
		t.Fatalf("threadId = %v", call.Params["threadId"])
	}
}

// 没有显式 query 时，从 Referer 的会话页路径推断线程。
func TestExtAPIThreadFromReferer(t *testing.T) {
	core := newFakeCore().on("plugin.http", `{"ok":true,"result":null}`)
	srv, _ := newServer(t, core)

	req := httptest.NewRequest(http.MethodGet, "/ext/tools-fs/api/ping", nil)
	req.Header.Set("Referer", "http://127.0.0.1:52353/threads/th_7?tab=chat")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 = %d, body=%s", rec.Code, rec.Body.String())
	}
	call, ok := core.lastCall("plugin.http")
	if !ok || call.Params["threadId"] != "th_7" {
		t.Fatalf("Referer 没有带出 threadId: %+v", call.Params)
	}

	// 非法线程 id（含路径分隔符）不作为上下文传下去。
	req = httptest.NewRequest(http.MethodGet, "/ext/tools-fs/api/ping?threadId=th%2Fbad", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if call, _ := core.lastCall("plugin.http"); call.Params["threadId"] != nil {
		t.Fatalf("非法 threadId 被接受: %+v", call.Params)
	}
}

func TestExtAPIMapsPluginErrors(t *testing.T) {
	core := newFakeCore().
		on("plugin.http", `{"ok":false,"notFound":true,"error":"没有这个接口"}`)
	srv, _ := newServer(t, core)

	rec := serveExt(t, srv, http.MethodGet, "/ext/tools-fs/api/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("notFound 状态 = %d, want 404", rec.Code)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("错误体不是 JSON: %v (%s)", err, rec.Body.String())
	}
	if payload.Error.Message != "没有这个接口" {
		t.Fatalf("错误消息 = %q", payload.Error.Message)
	}

	core.on("plugin.http", `{"ok":false,"methodNotAllowed":true,"error":"方法不允许"}`)
	rec = serveExt(t, srv, http.MethodGet, "/ext/tools-fs/api/echo", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("methodNotAllowed 状态 = %d, want 405", rec.Code)
	}

	core.on("plugin.http", `{"ok":false,"status":406,"error":"该接口是 SSE，请用 EventSource 订阅"}`)
	rec = serveExt(t, srv, http.MethodGet, "/ext/tools-fs/api/events", "")
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("SSE 路由的普通请求状态 = %d, want 406", rec.Code)
	}

	core.on("plugin.http", `{"ok":false,"error":"插件已停用"}`)
	rec = serveExt(t, srv, http.MethodGet, "/ext/tools-fs/api/echo", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("插件失败状态 = %d, want 502", rec.Code)
	}

	core.fail("plugin.http", errors.New("core 连接已关闭"))
	rec = serveExt(t, srv, http.MethodGet, "/ext/tools-fs/api/echo", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("Core 不可用状态 = %d, want 502", rec.Code)
	}
	wantContains(t, rec.Body.String(), "插件接口暂时不可用")
}

func TestExtAPIRejectsBadRequests(t *testing.T) {
	core := newFakeCore().on("plugin.http", `{"ok":true,"result":null}`)
	srv, _ := newServer(t, core)

	rec := serveExt(t, srv, http.MethodPut, "/ext/tools-fs/api/echo", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT 状态 = %d, want 405", rec.Code)
	}

	rec = serveExt(t, srv, http.MethodPost, "/ext/tools-fs/api/echo", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 状态 = %d, want 400", rec.Code)
	}
	if core.countCalls("plugin.http") != 0 {
		t.Fatal("坏请求不应转发到插件")
	}
}

func TestExtAPINormalizesEmptyResult(t *testing.T) {
	core := newFakeCore().on("plugin.http", `{"ok":true}`)
	srv, _ := newServer(t, core)
	rec := serveExt(t, srv, http.MethodGet, "/ext/tools-fs/api/ping", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "null" {
		t.Fatalf("空结果 = %d %q, want 200 null", rec.Code, rec.Body.String())
	}
}

// SSE 路由：Accept: text/event-stream 时走 prepare → 事件转发 → start/cancel。
func TestExtAPISSE(t *testing.T) {
	core := newFakeCore()
	core.callFn = func(method string, params any) (json.RawMessage, error) {
		if method != "plugin.httpStream" {
			return nil, errors.New("未预期的方法 " + method)
		}
		p, _ := params.(map[string]any)
		action, _ := p["action"].(string)
		streamID, _ := p["streamId"].(string)
		switch action {
		case "prepare":
			// 订阅在 prepare 之前建立，这里投递的帧会先入队。
			core.push("evt.plugin.stream", `{"streamId":"`+streamID+`","event":"update","data":{"ok":true}}`)
			core.push("evt.plugin.stream", `{"streamId":"`+streamID+`","done":true}`)
			return json.RawMessage(`{"prepared":true}`), nil
		case "start":
			return json.RawMessage(`{"started":true}`), nil
		case "cancel":
			return json.RawMessage(`{"cancelled":true}`), nil
		}
		return nil, errors.New("未知 action " + action)
	}
	srv, _ := newServer(t, core)

	req := httptest.NewRequest(http.MethodGet, "/ext/provider-admin/api/events?threadId=th_1", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态 = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	for _, want := range []string{"event: update", `data: {"ok":true}`, "event: done"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("SSE 响应缺少 %q:\n%s", want, rec.Body.String())
		}
	}
	core.mu.Lock()
	foundPrepare := false
	for _, c := range core.calls {
		if c.Method == "plugin.httpStream" && c.Params["action"] == "prepare" {
			foundPrepare = c.Params["pluginId"] == "provider-admin" &&
				c.Params["path"] == "/events" && c.Params["threadId"] == "th_1"
		}
	}
	core.mu.Unlock()
	if !foundPrepare {
		t.Fatalf("prepare 参数不对: %+v", core.calls)
	}
	if core.countCalls("plugin.httpStream") != 3 {
		t.Fatalf("plugin.httpStream 调用次数 = %d, want 3（prepare/start/cancel）", core.countCalls("plugin.httpStream"))
	}
}

// prepare 同步返回 406 时，浏览器拿到的是普通 JSON 错误，而不是 200 的空流。
func TestExtAPISSEPrepareRejected(t *testing.T) {
	core := newFakeCore()
	core.callFn = func(string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"prepared":false,"status":406,"error":"该接口不是 SSE"}`), nil
	}
	srv, _ := newServer(t, core)

	req := httptest.NewRequest(http.MethodGet, "/ext/provider-admin/api/state", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("状态 = %d, want 406; body=%s", rec.Code, rec.Body.String())
	}
	wantContains(t, rec.Body.String(), "该接口不是 SSE")
}
