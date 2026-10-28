package gateway_test

// 不启动真实 Core 的 handler 级测试：通过 gateway.Config.Supervisor 注入
// 假监督者，验证鉴权、首页品牌渲染与 Core 错误页的完整中间件链路。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway"
	"github.com/adag/ada-go/internal/gateway/contracts"
)

const testAccessToken = "gateway-handler-test-token"

// fakeSupervisor 只返回一个固定客户端，不 spawn 任何进程。
type fakeSupervisor struct {
	client     contracts.CoreClient
	acquireErr error
}

func (f *fakeSupervisor) Acquire(context.Context, string) (contracts.CoreClient, error) {
	if f.acquireErr != nil {
		return nil, f.acquireErr
	}
	return f.client, nil
}

func (f *fakeSupervisor) Release(string)                    {}
func (f *fakeSupervisor) Shutdown(context.Context) error    { return nil }
func (f *fakeSupervisor) List() []contracts.WorkspaceStatus { return nil }

// fakeCoreClient 按方法名返回预置 JSON；未配置的方法返回错误。
type fakeCoreClient struct {
	responses map[string]json.RawMessage
}

func (f *fakeCoreClient) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	if raw, ok := f.responses[method]; ok {
		return raw, nil
	}
	return nil, fmt.Errorf("fake: 未配置方法 %s", method)
}

func (f *fakeCoreClient) Subscribe(int) (<-chan contracts.Event, func()) {
	ch := make(chan contracts.Event)
	return ch, func() { close(ch) }
}

func (f *fakeCoreClient) Close() error { return nil }

// newFakeGateway 用注入的监督者装配 Gateway 并起 httptest 服务。
func newFakeGateway(t *testing.T, sup contracts.Supervisor) *httptest.Server {
	t.Helper()
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	gw, err := gateway.New(context.Background(), gateway.Config{
		Workspace:  t.TempDir(),
		Product:    filepath.Join(repo, "products", "headless.agent.json"),
		Token:      testAccessToken,
		Supervisor: sup,
	})
	if err != nil {
		t.Fatalf("gateway.New（注入监督者）失败: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func httpGet(t *testing.T, url, token string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s 失败: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp, string(body)
}

// TestGatewayHandlerHomeAndAuth 验证未鉴权跳登录、Bearer 后首页含品牌与线程。
func TestGatewayHandlerHomeAndAuth(t *testing.T) {
	core := &fakeCoreClient{responses: map[string]json.RawMessage{
		"thread.list": json.RawMessage(`{"threads":[{"id":"th_demo","title":"演示会话","updatedAt":"2026-01-01T00:00:00Z"}],"hasMore":false}`),
	}}
	srv := newFakeGateway(t, &fakeSupervisor{client: core})

	resp, body := httpGet(t, srv.URL+"/", "")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("未鉴权访问首页状态 = %d，期望 303 到登录页；正文: %.400s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/login" {
		t.Fatalf("未鉴权 Location = %q，期望 /login", loc)
	}

	resp, body = httpGet(t, srv.URL+"/", testAccessToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bearer 访问首页状态 = %d，期望 200；正文: %.800s", resp.StatusCode, body)
	}
	for _, want := range []string{`class="brand"`, "ada-go", "演示会话"} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少 %q；正文片段: %.800s", want, body)
		}
	}
}

// TestGatewayHandlerCoreErrorPage 验证 Core 不可用时渲染人话错误页而非 500/崩溃。
func TestGatewayHandlerCoreErrorPage(t *testing.T) {
	srv := newFakeGateway(t, &fakeSupervisor{acquireErr: fmt.Errorf("core 未启动")})

	resp, body := httpGet(t, srv.URL+"/", testAccessToken)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("Core 不可用时首页状态 = %d，期望 502；正文: %.800s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "Core 暂时不可用") {
		t.Fatalf("错误页缺少「Core 暂时不可用」；正文片段: %.800s", body)
	}
}
