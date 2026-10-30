package gateway

// 多工作区解析的单元测试：不启动真实 Core 进程，只验证归一化与
// cookie → 工作区的解析规则（web 与 eventHandler 共用同一个 resolver）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/web"
)

func TestGatewayRejectsSharedDataDirForMultipleWorkspaces(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	_, err := New(context.Background(), Config{
		Workspace: dirA, Workspaces: []string{dirA, dirB},
		Product: filepath.Join("..", "..", "products", "headless.agent.json"),
		DataDir: filepath.Join(t.TempDir(), "shared"),
	})
	if err == nil {
		t.Fatal("多工作区共享 data-dir 必须被拒绝：两个 Core 会同时写同一个 SQLite")
	}
}

func TestNormalizeWorkspaces(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	absA, err := filepath.Abs(dirA)
	if err != nil {
		t.Fatal(err)
	}
	absB, err := filepath.Abs(dirB)
	if err != nil {
		t.Fatal(err)
	}

	got, err := normalizeWorkspaces("", []string{dirA, dirB, dirA, "  "})
	if err != nil {
		t.Fatalf("normalizeWorkspaces: %v", err)
	}
	want := []string{absA, absB}
	if len(got) != len(want) {
		t.Fatalf("归一化结果 = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("归一化结果[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// 旧字段兼容：Workspaces 为空时用 Workspace。
	got, err = normalizeWorkspaces(dirB, nil)
	if err != nil {
		t.Fatalf("旧字段归一路径失败: %v", err)
	}
	if len(got) != 1 || got[0] != absB {
		t.Fatalf("旧字段归一化 = %#v, want [%q]", got, absB)
	}

	if _, err := normalizeWorkspaces("", nil); err == nil {
		t.Fatal("没有工作区时应返回错误")
	}
}

func TestWorkspaceResolverCookieRules(t *testing.T) {
	workspaces := []string{"/ws/a", "/ws/b"}
	resolve := workspaceResolver(workspaces)

	cases := []struct {
		name   string
		cookie *http.Cookie
		want   string
	}{
		{"无 cookie", nil, "/ws/a"},
		{"索引 1", &http.Cookie{Name: web.WorkspaceCookieName, Value: "1"}, "/ws/b"},
		{"越界", &http.Cookie{Name: web.WorkspaceCookieName, Value: "2"}, "/ws/a"},
		{"负数", &http.Cookie{Name: web.WorkspaceCookieName, Value: "-1"}, "/ws/a"},
		{"非数字", &http.Cookie{Name: web.WorkspaceCookieName, Value: "x"}, "/ws/a"},
		{"空值", &http.Cookie{Name: web.WorkspaceCookieName, Value: ""}, "/ws/a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			if got := resolve(req); got != tc.want {
				t.Fatalf("resolver = %q, want %q", got, tc.want)
			}
		})
	}
}

// stubCore 只满足 contracts.CoreClient；不会产生事件。
type stubCore struct{}

func (stubCore) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (stubCore) Subscribe(int) (<-chan contracts.Event, func()) {
	ch := make(chan contracts.Event)
	close(ch)
	return ch, func() {}
}

func (stubCore) Close() error { return nil }

// eventRecordingSupervisor 记录 eventHandler 实际 Acquire 的工作区。
type eventRecordingSupervisor struct {
	acquired []string
}

func (s *eventRecordingSupervisor) Acquire(_ context.Context, workspace string) (contracts.CoreClient, error) {
	s.acquired = append(s.acquired, workspace)
	return stubCore{}, nil
}

func (s *eventRecordingSupervisor) Release(string)                    {}
func (s *eventRecordingSupervisor) Shutdown(context.Context) error    { return nil }
func (s *eventRecordingSupervisor) List() []contracts.WorkspaceStatus { return nil }

func TestEventHandlerUsesCookieWorkspace(t *testing.T) {
	sup := &eventRecordingSupervisor{}
	handler := &eventHandler{
		sup:          sup,
		workspaceFor: workspaceResolver([]string{"/ws/a", "/ws/b"}),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即结束 SSE 循环，避免测试挂起。
	req := httptest.NewRequest(http.MethodGet, "/events/th_1", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: web.WorkspaceCookieName, Value: "1"})
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if len(sup.acquired) != 1 {
		t.Fatalf("Acquire 调用次数 = %d, want 1", len(sup.acquired))
	}
	if sup.acquired[0] != "/ws/b" {
		t.Fatalf("eventHandler 工作区 = %q, want /ws/b", sup.acquired[0])
	}
}

// FilePicker 新登记的工作区不在启动配置里；SSE 必须和页面使用同一套
// 动态解析，否则会话页有内容、事件流却连在旧 Core 上，表现为一直空白。
func TestEventHandlerUsesDynamicWorkspace(t *testing.T) {
	base := t.TempDir()
	dynamic := t.TempDir()
	product, err := filepath.Abs(filepath.Join("..", "..", "products", "headless.agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	const token = "gateway-dynamic-workspace-token"
	acquired := make(chan string, 4)
	sup := &acquireChanSupervisor{workspaces: acquired}
	gw, err := New(context.Background(), Config{
		Workspace:  base,
		Product:    product,
		Token:      token,
		Supervisor: sup,
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)

	form := url.Values{"path": {dynamic}}.Encode()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/workspace/select", strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/workspace/select: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/workspace/select = %d", res.StatusCode)
	}
	var workspaceCookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == web.WorkspaceCookieName {
			workspaceCookie = c
		}
	}
	if workspaceCookie == nil {
		t.Fatal("没有写工作区 cookie")
	}

	eventsReq, err := http.NewRequest(http.MethodGet, srv.URL+"/events/th_dynamic", nil)
	if err != nil {
		t.Fatal(err)
	}
	eventsReq.Header.Set("Authorization", "Bearer "+token)
	eventsReq.AddCookie(workspaceCookie)
	eventsRes, err := (&http.Client{Timeout: 3 * time.Second}).Do(eventsReq)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer eventsRes.Body.Close()
	if ct := eventsRes.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("GET /events Content-Type = %q", ct)
	}
	select {
	case got := <-acquired:
		real, err := filepath.EvalSymlinks(dynamic)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Clean(got) != filepath.Clean(real) {
			t.Fatalf("SSE 使用的工作区 = %q，期望动态工作区 %q", got, real)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("eventHandler 没有 Acquire 工作区")
	}
}

type acquireChanSupervisor struct {
	workspaces chan string
}

func (s *acquireChanSupervisor) Acquire(_ context.Context, workspace string) (contracts.CoreClient, error) {
	s.workspaces <- workspace
	return stubCore{}, nil
}

func (s *acquireChanSupervisor) Release(string)                    {}
func (s *acquireChanSupervisor) Shutdown(context.Context) error    { return nil }
func (s *acquireChanSupervisor) List() []contracts.WorkspaceStatus { return nil }

// FilePicker 打开的工作区必须落盘，重启 Gateway 后仍在列表里。
func TestGatewayPersistsDynamicWorkspace(t *testing.T) {
	base := t.TempDir()
	dynamic := t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "workspaces.json")
	product, err := filepath.Abs(filepath.Join("..", "..", "products", "headless.agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	const token = "gateway-workspace-persist-token"
	newGateway := func() *Gateway {
		t.Helper()
		gw, err := New(context.Background(), Config{
			Workspace:      base,
			Product:        product,
			Token:          token,
			WorkspacesFile: stateFile,
			Supervisor:     &eventRecordingSupervisor{},
		})
		if err != nil {
			t.Fatalf("gateway.New: %v", err)
		}
		t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
		return gw
	}

	gw := newGateway()
	srv := httptest.NewServer(gw.Handler())
	form := url.Values{"path": {dynamic}}.Encode()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/workspace/select", strings.NewReader(form))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/workspace/select: %v", err)
	}
	defer res.Body.Close()
	srv.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/workspace/select = %d", res.StatusCode)
	}

	raw, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("工作区列表没有落盘: %v", err)
	}
	if !strings.Contains(string(raw), filepath.Base(dynamic)) {
		t.Fatalf("工作区列表缺少 %s: %s", dynamic, raw)
	}

	// 模拟重启：同一状态文件、同一启动工作区。
	restarted := newGateway()
	realDynamic, err := filepath.EvalSymlinks(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ws := range restarted.Workspaces() {
		if candidate, err := filepath.EvalSymlinks(ws); err == nil && filepath.Clean(candidate) == filepath.Clean(realDynamic) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("重启后工作区列表 = %v，缺少 %s", restarted.Workspaces(), dynamic)
	}
}
