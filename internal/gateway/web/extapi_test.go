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
