package app_test

// M4 前端插件管线的组合根验收（垂直切片）：
//
//	官方前端插件（embedded）→ frontend.Bridge（TSX→VNode→HTML）→ web.Server（12 插槽 / /ext）
//
// 本文件是测试，允许出现官方插件 id（1.5.5 的边界只约束非测试源码）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/gateway/frontend"
	"github.com/adag/ada-go/internal/gateway/web"
	"github.com/adag/ada-go/plugins/official"
)

// uiFakeCore 只实现 SSR 取数需要的面：thread.get/entries 走固定 fixture，
// action.invoke 可配置成败。
type uiFakeCore struct {
	mu       sync.Mutex
	invokeOK bool
	result   json.RawMessage
	calls    int
}

func (f *uiFakeCore) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "action.invoke":
		f.calls++
		if !f.invokeOK {
			return nil, errors.New("core: action.invoke 尚未实现（M4 桩）")
		}
		return json.RawMessage(`{"ok":true,"result":` + string(f.result) + `}`), nil
	case "thread.get":
		return json.RawMessage(`{"thread":{"id":"th_1","workspace":"/tmp/ws","title":"UI 管线","createdAt":"2026-10-28T09:00:00Z","updatedAt":"2026-10-28T10:00:00Z","archived":false},"items":[],"seq":1,"run":{"runId":"run_1","status":"terminal","terminal":"completed"},"interactions":[],"hasMore":false}`), nil
	case "thread.entries":
		return json.RawMessage(`{"entries":[],"hasMore":false}`), nil
	default:
		return nil, errors.New("uiFakeCore: 未配置 " + method)
	}
}

func (f *uiFakeCore) Subscribe(int) (<-chan contracts.Event, func()) { return nil, func() {} }
func (f *uiFakeCore) Close() error                                   { return nil }

func (f *uiFakeCore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type uiFakeSup struct{ core contracts.CoreClient }

func (s *uiFakeSup) Acquire(context.Context, string) (contracts.CoreClient, error) {
	return s.core, nil
}
func (s *uiFakeSup) Release(string)                    {}
func (s *uiFakeSup) Shutdown(context.Context) error    { return nil }
func (s *uiFakeSup) List() []contracts.WorkspaceStatus { return nil }

func newUIServer(t *testing.T, core *uiFakeCore) *web.Server {
	t.Helper()
	scripts, err := official.FrontendScripts()
	if err != nil {
		t.Fatalf("official.FrontendScripts: %v", err)
	}
	bridge, err := frontend.NewBridge(frontend.BridgeOptions{
		Scripts:     scripts,
		Supervisor:  &uiFakeSup{core: core},
		ProductName: "ada-go",
	})
	if err != nil {
		t.Fatalf("frontend.NewBridge: %v", err)
	}
	srv, err := web.New(web.Config{
		Supervisor:  &uiFakeSup{core: core},
		Workspace:   "/tmp/ws",
		Workspaces:  []string{"/tmp/ws"},
		ProductName: "ada-go",
		Locale:      "zh-CN",
		CSRF:        func(*http.Request) string { return "csrf-token" },
		PluginUI:    bridge,
	})
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func uiGet(t *testing.T, srv *web.Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// 计划卡片（tools-plan 的前端段）挂在 chat.corner：动作成功时用真实计划渲染。
func TestTaskPlanCardInChatCorner(t *testing.T) {
	core := &uiFakeCore{invokeOK: true, result: json.RawMessage(
		`{"plan":{"done":4,"total":6,"steps":[{"step":" golden ","status":"done"},{"step":"待办","status":"pending"}]}}`)}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/thread/th_1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-slot="chat.corner" data-contributors="1"`,
		`<details class="todo-card"`,
		`<span class="todo-card__count">4/6</span>`,
		`class="ada-todo__step is-done"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("会话页缺少 %q", want)
		}
	}
	// 文本由 Go 侧转义：动作结果里的空格保留、HTML 特殊字符不可能逃逸。
	if !strings.Contains(body, "<span> golden </span>") {
		t.Fatalf("步骤标题渲染不对:\n%s", body)
	}
	// 动作预取走了既有 action.invoke（协议合规，未新增 RPC 方法）。
	if core.count() == 0 {
		t.Fatal("计划卡渲染应尝试 action.invoke 预取")
	}
}

// 动作预取失败时插槽贡献为空：没有示例数据兜底，整页仍 200。
func TestTaskPlanCardHiddenWithoutAction(t *testing.T) {
	core := &uiFakeCore{}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/thread/th_1?stub=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /thread/th_1 = %d\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "todo-card") {
		t.Fatal("取不到计划时不应画卡（不允许示例数据兜底）")
	}
	if core.count() == 0 {
		t.Fatal("计划卡渲染应尝试 action.invoke 预取")
	}
}

// /ext 页面：hello 页 200，未知页面 404，静态资源 allowlist。
func TestUIExtRoutes(t *testing.T) {
	core := &uiFakeCore{}
	srv := newUIServer(t, core)

	rec := uiGet(t, srv, "/ext/ui-hello-tsx/hello")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ext/ui-hello-tsx/hello = %d\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"<h2>Hello from TSX</h2>",
		"<li>产品：ada-go</li>",
		`data-ext-plugin="ui-hello-tsx"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("hello 页缺少 %q", want)
		}
	}

	if rec := uiGet(t, srv, "/ext/ui-hello-tsx/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("未知 slug = %d, want 404", rec.Code)
	}
	if rec := uiGet(t, srv, "/ext/no-such-plugin/hello"); rec.Code != http.StatusNotFound {
		t.Fatalf("未知插件 = %d, want 404", rec.Code)
	}
	rec = uiGet(t, srv, "/ext/tools-plan/static/todo.css")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("插件样式 = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := uiGet(t, srv, "/ext/tools-plan/static/evil.exe"); rec.Code != http.StatusNotFound {
		t.Fatalf("扩展名 allowlist 之外的资源 = %d, want 404", rec.Code)
	}
}
