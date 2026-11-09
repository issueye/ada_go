package gateway_test

// 内部发送端点：与用户鉴权并列而不是嵌套。
//
// 进程没有会话 cookie，也不该有；认证只靠控制令牌 + 回环来源。
// 反过来，用户会话 token 也不能当控制令牌用——两条链互不串用。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway"
	"github.com/adag/ada-go/internal/gateway/contracts"
)

// 内部端点走自己的认证：没有控制令牌一律 401（不是会话鉴权的 401 页面）。
func TestInternalDispatchRequiresControlToken(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	core := &fakeCoreClient{responses: map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"th_1"}}`),
		"turn.get":      json.RawMessage(`{"run":{"status":""}}`),
		"turn.start":    json.RawMessage(`{}`),
	}}
	srv := newInternalGateway(t, ws, core)

	// 带用户 token 但没有控制令牌：仍然 401。
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/dispatch/send",
		strings.NewReader(`{"threadId":"th_1","text":"你好"}`))
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testAccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d，期望 401", resp.StatusCode)
	}
}

// 控制令牌可以投递，且请求体里的身份字段被忽略。
func TestInternalDispatchWithControlToken(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	core := &fakeCoreClient{responses: map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"th_1"}}`),
		"turn.get":      json.RawMessage(`{"run":{"status":""}}`),
		"turn.start":    json.RawMessage(`{}`),
	}}
	gw, srv := newInternalGatewayFull(t, ws, core)
	_, token := gw.ControlAccess(ws)
	if token == "" {
		t.Fatal("必须能为本工作区签发控制令牌")
	}

	body := `{"threadId":"th_1","text":"定时消息","mode":"start","pluginId":"cron-manager","taskId":"t-1","kind":"user"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/internal/dispatch/send", strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, raw)
	}
	var out struct {
		Receipt struct {
			ThreadID string `json:"threadId"`
			Mode     string `json:"mode"`
		} `json:"receipt"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("回执: %v", err)
	}
	if out.Receipt.ThreadID != "th_1" || out.Receipt.Mode != "start" {
		t.Fatalf("回执 = %+v", out.Receipt)
	}
}

// /internal/* 之外的路径仍走会话鉴权（没 token 被重定向到登录页）。
func TestUserRoutesStillRequireSession(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	core := &fakeCoreClient{responses: map[string]json.RawMessage{
		"thread.list": json.RawMessage(`{"threads":[]}`),
	}}
	srv := newInternalGateway(t, ws, core)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("首页 status = %d，期望 303 到登录页", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("Location = %q，期望 /login", loc)
	}
}

// newInternalGateway 装配不带控制面地址的网关（用于 401 断言）。
func newInternalGateway(t *testing.T, ws string, core contracts.CoreClient) *httptest.Server {
	t.Helper()
	gw, srv := newInternalGatewayFull(t, ws, core)
	_ = gw
	return srv
}

// newInternalGatewayFull 装配带控制面地址的网关并起 httptest 服务。
func newInternalGatewayFull(t *testing.T, ws string, core contracts.CoreClient) (*gateway.Gateway, *httptest.Server) {
	t.Helper()
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	gw, err := gateway.New(context.Background(), gateway.Config{
		Workspace:  ws,
		Product:    filepath.Join(repo, "products", "headless.agent.json"),
		Token:      testAccessToken,
		Supervisor: &fakeSupervisor{client: core},
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	// 控制面地址在监听之后才知道（生产里由 cmd 在 net.Listen 后设置）。
	gw.SetControlURL(srv.URL)
	return gw, srv
}
