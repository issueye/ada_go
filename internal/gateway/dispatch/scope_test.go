package dispatch_test

// 令牌范围与身份：Core 的令牌能发消息、能管进程；被管进程的令牌只能发消息，
// 且 sender 由令牌里的 PluginID 盖章（进程不能冒充别的插件）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adag/ada-go/internal/gateway/dispatch"
)

// issueEndpoint 装配一个只带给定身份令牌的端点。
func issueEndpoint(t *testing.T, ident dispatch.Identity) (*dispatch.Endpoint, *fakeCore) {
	t.Helper()
	core := &fakeCore{threadID: "th_1"}
	d, _ := newDispatcher(t, core, twoWorkspaces())
	tokens := dispatch.NewTokens()
	if err := tokens.Issue("tok", ident); err != nil {
		t.Fatalf("签发令牌: %v", err)
	}
	ep, err := dispatch.NewEndpoint(dispatch.EndpointConfig{Dispatcher: d, Tokens: tokens})
	if err != nil {
		t.Fatalf("NewEndpoint: %v", err)
	}
	return ep, core
}

func postSend(t *testing.T, ep *dispatch.Endpoint, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatalf("编码: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/dispatch/send", &buf)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	ep.Handler().ServeHTTP(rec, req)
	return rec
}

// 令牌没有发送范围 → 403（而不是 401：令牌是真的，只是没被授予这条链）。
func TestDispatchSendRequiresScope(t *testing.T) {
	ep, _ := issueEndpoint(t, dispatch.Identity{Workspace: "/ws/a", Scopes: []string{dispatch.ScopeProcessControl}})
	rec := postSend(t, ep, "tok", dispatch.ControlRequest{ThreadID: "th_1", Text: "你好"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d，期望 403（body=%s）", rec.Code, rec.Body.String())
	}
}

// Core 令牌带发送范围时正常投递。
func TestDispatchSendWithScope(t *testing.T) {
	ep, _ := issueEndpoint(t, dispatch.Identity{
		Workspace: "/ws/a", Scopes: []string{dispatch.ScopeDispatchSend, dispatch.ScopeProcessControl},
	})
	rec := postSend(t, ep, "tok", dispatch.ControlRequest{ThreadID: "th_1", Text: "你好"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// 令牌里写死 PluginID 时以令牌为准：进程改不了 sender，也不能冒充别的插件。
func TestSenderStampedByTokenPluginID(t *testing.T) {
	core := &fakeCore{threadID: "th_1"}
	d, _ := newDispatcher(t, core, twoWorkspaces())
	tokens := dispatch.NewTokens()
	if err := tokens.Issue("tok", dispatch.Identity{
		Workspace: "/ws/a", Key: "proc:cron-manager/scheduler/",
		Scopes: []string{dispatch.ScopeDispatchSend}, PluginID: "cron-manager",
	}); err != nil {
		t.Fatal(err)
	}
	var audited []dispatch.Sender
	ep, err := dispatch.NewEndpoint(dispatch.EndpointConfig{
		Dispatcher: d, Tokens: tokens,
		Log: func(_ dispatch.Identity, sender dispatch.Sender, _ dispatch.SendRequest, _ dispatch.Receipt, _ error) {
			audited = append(audited, sender)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 请求体里伪造另一个插件身份。
	rec := postSend(t, ep, "tok", dispatch.ControlRequest{
		ThreadID: "th_1", Text: "定时消息", PluginID: "attacker", TaskID: "t-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(audited) != 1 {
		t.Fatalf("审计记录数 = %d", len(audited))
	}
	got := audited[0]
	// sender 必须来自令牌，而不是请求体里的 attacker。
	if got.PluginID != "cron-manager" || got.Kind != dispatch.SenderPlugin {
		t.Fatalf("sender 应由令牌盖章为 cron-manager，实得 %+v", got)
	}
	if got.TaskID != "t-1" {
		t.Fatalf("taskId = %q，期望 t-1", got.TaskID)
	}
}

// 没有 PluginID 的 Core 令牌仍按请求体的 pluginId 盖章（那个值由 Core 宿主按能力集给出）。
func TestSenderFromBodyForCoreToken(t *testing.T) {
	ep, _ := issueEndpoint(t, dispatch.Identity{Workspace: "/ws/a", Scopes: []string{dispatch.ScopeDispatchSend}})
	rec := postSend(t, ep, "tok", dispatch.ControlRequest{
		ThreadID: "th_1", Text: "插件消息", PluginID: "cron-manager", TaskID: "t-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// 没有显式范围一律拒绝：签发点必须自己说明要什么权限。
func TestNoScopesIsDenied(t *testing.T) {
	ep, _ := issueEndpoint(t, dispatch.Identity{Workspace: "/ws/a"})
	if rec := postSend(t, ep, "tok", dispatch.ControlRequest{ThreadID: "th_1", Text: "你好"}); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d，期望 403（body=%s）", rec.Code, rec.Body.String())
	}
	ident := dispatch.Identity{}
	if ident.HasScope("anything") {
		t.Fatal("空 Scopes 不应授予任何范围")
	}
	if (dispatch.Identity{Scopes: []string{dispatch.ScopeDispatchSend}}).HasScope(dispatch.ScopeProcessControl) {
		t.Fatal("只授予发送范围时不应拿到进程范围")
	}
}
