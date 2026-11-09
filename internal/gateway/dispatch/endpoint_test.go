package dispatch_test

// 内部发送 API：token 认进程、sender 由服务端盖章、body 里的身份字段无效。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/dispatch"
)

// newEndpoint 装配一个带令牌的端点（httptest 的 RemoteAddr 是 127.0.0.1）。
func newEndpoint(t *testing.T, core *fakeCore, token string, ident dispatch.Identity) (*dispatch.Endpoint, *fakeSupervisor) {
	t.Helper()
	d, sup := newDispatcher(t, core, twoWorkspaces())
	tokens := dispatch.NewTokens()
	if err := tokens.Issue(token, ident); err != nil {
		t.Fatalf("签发令牌: %v", err)
	}
	ep, err := dispatch.NewEndpoint(dispatch.EndpointConfig{Dispatcher: d, Tokens: tokens})
	if err != nil {
		t.Fatalf("NewEndpoint: %v", err)
	}
	mux := http.NewServeMux()
	ep.Register(mux)
	return ep, sup
}

// serve 直接调端点 handler（绕过 mux 便于断言状态码）。
func serve(t *testing.T, ep *dispatch.Endpoint, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("编码请求体: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "127.0.0.1:54321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	ep.Handler().ServeHTTP(rec, req)
	_ = contracts.WorkspaceStatus{}
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) dispatch.ControlError {
	t.Helper()
	var body struct {
		Error dispatch.ControlError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON（status=%d body=%s）: %v", rec.Code, rec.Body.String(), err)
	}
	return body.Error
}

// 传输层故障没有业务码：端点返回 502，调用方能区分「没发出去」与「被拒绝」。
func TestInternalSendRejectsBadRequest(t *testing.T) {
	t.Parallel()
	t.Run("缺令牌", func(t *testing.T) {
		t.Parallel()
		ep, sup := newEndpoint(t, &fakeCore{}, "tok", dispatch.Identity{Workspace: "/ws/a"})
		rec := serve(t, ep, http.MethodPost, "/internal/dispatch/send", "",
			dispatch.ControlRequest{ThreadID: "th_1", Text: "你好"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", rec.Code)
		}
		if len(sup.acquired) != 0 {
			t.Fatal("未鉴权不得取 Core")
		}
	})
	t.Run("错令牌", func(t *testing.T) {
		t.Parallel()
		ep, _ := newEndpoint(t, &fakeCore{}, "tok", dispatch.Identity{Workspace: "/ws/a"})
		rec := serve(t, ep, http.MethodPost, "/internal/dispatch/send", "wrong",
			dispatch.ControlRequest{ThreadID: "th_1", Text: "你好"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("坏 JSON", func(t *testing.T) {
		t.Parallel()
		ep, _ := newEndpoint(t, &fakeCore{}, "tok", dispatch.Identity{Workspace: "/ws/a"})
		req := httptest.NewRequest(http.MethodPost, "/internal/dispatch/send", strings.NewReader("{"))
		req.RemoteAddr = "127.0.0.1:54321"
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		ep.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", rec.Code)
		}
		if code := decodeError(t, rec).Code; code != "invalid_body" {
			t.Fatalf("code = %q", code)
		}
	})
}

// sender 由服务端盖章：请求体里的 kind/id 即使伪造也不会生效。
func TestInternalSendStampsSender(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	var audited []dispatch.Sender
	d, _ := newDispatcher(t, core, twoWorkspaces())
	ident := dispatch.Identity{Workspace: "/ws/a", CoreID: "core-1"}
	tokens := dispatch.NewTokens()
	if err := tokens.Issue("tok", ident); err != nil {
		t.Fatal(err)
	}
	ep, err := dispatch.NewEndpoint(dispatch.EndpointConfig{
		Dispatcher: d, Tokens: tokens,
		Log: func(_ dispatch.Identity, sender dispatch.Sender, _ dispatch.SendRequest, _ dispatch.Receipt, _ error) {
			audited = append(audited, sender)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := serve(t, ep, http.MethodPost, "/internal/dispatch/send", "tok", map[string]any{
		"threadId": "th_1", "text": "定时消息", "mode": "start",
		"pluginId": "cron-manager", "taskId": "task-1",
		"kind": "user", "id": "admin", // 伪造字段：必须被忽略
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(audited) != 1 {
		t.Fatalf("审计 = %v", audited)
	}
	if audited[0].Kind != dispatch.SenderPlugin || audited[0].PluginID != "cron-manager" || audited[0].TaskID != "task-1" {
		t.Fatalf("sender = %+v，应由服务端盖章", audited[0])
	}
}

// 没有 pluginId 的调用视为系统内部投递（不是用户）。
func TestInternalSendWithoutPluginIsSystemSender(t *testing.T) {
	t.Parallel()
	core := &fakeCore{}
	var audited []dispatch.Sender
	d, _ := newDispatcher(t, core, twoWorkspaces())
	tokens := dispatch.NewTokens()
	if err := tokens.Issue("tok", dispatch.Identity{Workspace: "/ws/a"}); err != nil {
		t.Fatal(err)
	}
	ep, err := dispatch.NewEndpoint(dispatch.EndpointConfig{
		Dispatcher: d, Tokens: tokens,
		Log: func(_ dispatch.Identity, sender dispatch.Sender, _ dispatch.SendRequest, _ dispatch.Receipt, _ error) {
			audited = append(audited, sender)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(t, ep, http.MethodPost, "/internal/dispatch/send", "tok",
		dispatch.ControlRequest{ThreadID: "th_1", Text: "自检"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if audited[0].Kind != dispatch.SenderSystem {
		t.Fatalf("sender = %+v", audited[0])
	}
}

// 业务失败映射成 409/400，并带 code。
func TestInternalSendMapsBusinessCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status string
		mode   string
		text   string
		want   int
		code   string
	}{
		{"running", "start", "忙", http.StatusConflict, dispatch.CodeThreadBusy},
		{"waiting_input", "auto", "等等", http.StatusConflict, dispatch.CodeWaitingInput},
		{"", "steer", "空闲", http.StatusConflict, dispatch.CodeNotRunning},
		{"", "auto", "", http.StatusBadRequest, dispatch.CodeEmptyMessage},
	}
	for _, c := range cases {
		core := &fakeCore{status: c.status}
		d, _ := newDispatcher(t, core, twoWorkspaces())
		tokens := dispatch.NewTokens()
		if err := tokens.Issue("tok", dispatch.Identity{Workspace: "/ws/a"}); err != nil {
			t.Fatal(err)
		}
		ep, err := dispatch.NewEndpoint(dispatch.EndpointConfig{Dispatcher: d, Tokens: tokens})
		if err != nil {
			t.Fatal(err)
		}
		rec := serve(t, ep, http.MethodPost, "/internal/dispatch/send", "tok", dispatch.ControlRequest{
			ThreadID: "th_1", Text: c.text, Mode: c.mode,
		})
		if rec.Code != c.want {
			t.Fatalf("%s/%s: status = %d，期望 %d", c.status, c.mode, rec.Code, c.want)
		}
		if got := decodeError(t, rec).Code; got != c.code {
			t.Fatalf("%s/%s: code = %q，期望 %q", c.status, c.mode, got, c.code)
		}
	}
}

// 工作区不存在 → 400 workspace_missing，且不取 Core。
func TestInternalSendWorkspaceMissing(t *testing.T) {
	t.Parallel()
	ep, sup := newEndpoint(t, &fakeCore{}, "tok", dispatch.Identity{Workspace: "/ws/a"})
	rec := serve(t, ep, http.MethodPost, "/internal/dispatch/send", "tok", dispatch.ControlRequest{
		Workspace: "/nope", ThreadID: "th_1", Text: "你好",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if code := decodeError(t, rec).Code; code != dispatch.CodeWorkspaceMissing {
		t.Fatalf("code = %q", code)
	}
	if len(sup.acquired) != 0 {
		t.Fatal("工作区不存在时不得取 Core")
	}
}

// 工作区列表端点也只认令牌。
func TestInternalWorkspacesRequiresToken(t *testing.T) {
	t.Parallel()
	ep, _ := newEndpoint(t, &fakeCore{}, "tok", dispatch.Identity{Workspace: "/ws/a"})
	if rec := serve(t, ep, http.MethodGet, "/internal/dispatch/workspaces", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌 status = %d", rec.Code)
	}
	rec := serve(t, ep, http.MethodGet, "/internal/dispatch/workspaces", "tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Default    string               `json:"default"`
		Workspaces []dispatch.Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应: %v", err)
	}
	if body.Default != "/ws/a" || len(body.Workspaces) != 2 {
		t.Fatalf("body = %+v", body)
	}
}

// 同一键重签会作废旧令牌：Core 重启后旧令牌不该继续可用。
func TestTokensIssueForRevokesPrevious(t *testing.T) {
	t.Parallel()
	tokens := dispatch.NewTokens()
	first, err := tokens.IssueFor("/ws/a", dispatch.Identity{Workspace: "/ws/a", Key: "/ws/a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := tokens.IssueFor("/ws/a", dispatch.Identity{Workspace: "/ws/a", Key: "/ws/a"})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.Len() != 1 {
		t.Fatalf("令牌数 = %d，期望 1", tokens.Len())
	}
	if _, ok := tokens.Lookup(first); ok {
		t.Fatal("旧令牌应已作废")
	}
	if _, ok := tokens.Lookup(second); !ok {
		t.Fatal("新令牌应可用")
	}
	if _, err := tokens.IssueFor("", dispatch.Identity{}); err == nil {
		t.Fatal("空键应失败")
	}
	if err := tokens.Issue("", dispatch.Identity{}); err == nil {
		t.Fatal("空令牌应失败")
	}
}

// 非回环来源一律拒绝（网关只监听回环，但内部端点不依赖这个前提）。
func TestInternalSendRejectsNonLoopback(t *testing.T) {
	t.Parallel()
	ep, _ := newEndpoint(t, &fakeCore{}, "tok", dispatch.Identity{Workspace: "/ws/a"})
	req := httptest.NewRequest(http.MethodPost, "/internal/dispatch/send",
		strings.NewReader(`{"threadId":"th_1","text":"你好"}`))
	req.RemoteAddr = "10.0.0.9:12345"
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	ep.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
}
