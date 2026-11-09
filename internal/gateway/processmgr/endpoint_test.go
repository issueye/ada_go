package processmgr_test

// 内部进程端点：token 认进程、owner 由 Core 宿主盖章、entry 由网关再校验。
//
// 这里用真实的 processmgr.Manager + 假 spawn，把 HTTP 层的校验与注册表状态一起验。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/gateway/dispatch"
	"github.com/adag/ada-go/internal/gateway/processmgr"
)

// fakeProcess 是 Wait 阻塞到 Kill 的假进程。
type fakeProcess struct {
	pid  int
	done chan struct{}
	once sync.Once
}

func (p *fakeProcess) PID() int { return p.pid }

func (p *fakeProcess) Wait() error {
	<-p.done
	return errors.New("killed")
}

func (p *fakeProcess) Kill() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type fakeSpawner struct {
	mu    sync.Mutex
	next  int
	specs []processmgr.StartSpec
	procs []*fakeProcess
}

func (s *fakeSpawner) spawn(_ context.Context, spec processmgr.StartSpec, _, _ io.Writer) (processmgr.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.specs = append(s.specs, spec)
	p := &fakeProcess{pid: 5000 + s.next, done: make(chan struct{})}
	s.procs = append(s.procs, p)
	return p, nil
}

func (s *fakeSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

// harness 串联管理器、令牌表与端点。
type harness struct {
	manager  *processmgr.Manager
	endpoint *processmgr.Endpoint
	spawn    *fakeSpawner
	tokens   *dispatch.Tokens
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	spawn := &fakeSpawner{}
	mgr, err := processmgr.New(processmgr.Options{Spawn: spawn.spawn, Logf: t.Logf})
	if err != nil {
		t.Fatalf("processmgr.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})
	tokens := dispatch.NewTokens()
	ep, err := processmgr.NewEndpoint(processmgr.EndpointConfig{Manager: mgr, Tokens: tokens})
	if err != nil {
		t.Fatalf("NewEndpoint: %v", err)
	}
	return &harness{manager: mgr, endpoint: ep, spawn: spawn, tokens: tokens}
}

// issue 为一个 Core 签发令牌并返回明文。
func (h *harness) issue(t *testing.T, workspace string) string {
	t.Helper()
	token, err := h.tokens.IssueFor(workspace, dispatch.Identity{
		Workspace: workspace, CoreID: workspace, Key: workspace,
		// Core 的宿主桥既要发消息也要管进程（与 controlPlane 的签发一致）。
		Scopes: []string{dispatch.ScopeDispatchSend, dispatch.ScopeProcessControl},
	})
	if err != nil {
		t.Fatalf("IssueFor: %v", err)
	}
	return token
}

// serve 发一次内部进程请求。
func (h *harness) serve(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
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
	h.endpoint.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) processmgr.ProcessError {
	t.Helper()
	var body struct {
		Error processmgr.ProcessError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON（status=%d body=%s）: %v", rec.Code, rec.Body.String(), err)
	}
	return body.Error
}

// entryFile 在临时目录里造一个可读的 entry 文件。
func entryFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scheduler.js")
	if err := os.WriteFile(path, []byte("// noop\n"), 0o600); err != nil {
		t.Fatalf("写 entry: %v", err)
	}
	return path
}

func registerBody(pluginID, name, entry string) map[string]any {
	return map[string]any{
		"pluginId": pluginID, "name": name, "runtime": "node", "entry": entry,
		"start": "on_enable", "restart": "no",
	}
}

// 没有控制令牌 → 401；非回环来源 → 403。
func TestEndpointRequiresLoopbackAndToken(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")

	noToken := h.serve(t, http.MethodPost, "/internal/processes/register", "", registerBody("cron-manager", "scheduler", entryFile(t)))
	if noToken.Code != http.StatusUnauthorized {
		t.Fatalf("缺令牌 status = %d，期望 401", noToken.Code)
	}

	// 回环判据：把 RemoteAddr 换成非回环地址。
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(registerBody("cron-manager", "scheduler", entryFile(t)))
	req := httptest.NewRequest(http.MethodPost, "/internal/processes/register", &buf)
	req.RemoteAddr = "10.0.0.9:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.endpoint.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非回环来源 status = %d，期望 403", rec.Code)
	}
}

// 注册成功：on_enable 立即启动，owner 由请求体里的 pluginId 盖章。
func TestEndpointRegisterStartsProcess(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	rec := h.serve(t, http.MethodPost, "/internal/processes/register", token,
		registerBody("cron-manager", "scheduler", entryFile(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out processmgr.ProcessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("回执: %v", err)
	}
	if out.Ref == nil || out.Ref.Owner != "cron-manager" || out.Ref.Name != "scheduler" {
		t.Fatalf("回执 ref = %+v", out.Ref)
	}
	if out.Status == nil || !out.Status.Running {
		t.Fatalf("on_enable 应已启动: %+v", out.Status)
	}
	if h.spawn.count() != 1 {
		t.Fatalf("应拉起一个进程，实际 %d", h.spawn.count())
	}
	// node 形态：命令用宿主 node，entry 是第一个参数（绝对路径）。
	spec := h.spawn.specs[0]
	if filepath.Base(spec.Command) != "node" && spec.Command != "node" {
		t.Fatalf("node runtime 的 Command = %q", spec.Command)
	}
	if len(spec.Args) == 0 || !filepath.IsAbs(spec.Args[0]) {
		t.Fatalf("node runtime 应把绝对路径 entry 作为第一个参数，实际 %v", spec.Args)
	}
}

// 非法 pluginId 必须被拒绝：owner 由宿主盖章，脚本不能自填任意值。
func TestEndpointRejectsBadPluginID(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	for _, id := range []string{"", "Cron-Manager", "a", "../x", "plugin-with space"} {
		body := registerBody(id, "scheduler", entryFile(t))
		rec := h.serve(t, http.MethodPost, "/internal/processes/register", token, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("pluginId %q status = %d，期望 400（body=%s）", id, rec.Code, rec.Body.String())
		}
		if code := decodeError(t, rec).Code; code != string(processmgr.CodeInvalidSpec) {
			t.Fatalf("pluginId %q error code = %q，期望 %s", id, code, processmgr.CodeInvalidSpec)
		}
	}
}

// entry 必须是绝对路径且真实存在：网关不接受相对路径，也不替 Core 解析插件目录。
func TestEndpointRejectsBadEntry(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	cases := []struct {
		name  string
		entry string
	}{
		{"相对路径", "dist/scheduler.js"},
		{"不存在的绝对路径", filepath.Join(t.TempDir(), "missing.js")},
		{"目录", t.TempDir()},
		{"空", ""},
	}
	for _, c := range cases {
		body := registerBody("cron-manager", "scheduler", c.entry)
		rec := h.serve(t, http.MethodPost, "/internal/processes/register", token, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d，期望 400（body=%s）", c.name, rec.Code, rec.Body.String())
		}
	}
}

// 未注册的进程：ensure/stop/unregister/status 都返回 process_not_found（409）。
func TestEndpointMissingProcess(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	for _, op := range []string{"ensure", "stop", "unregister"} {
		rec := h.serve(t, http.MethodPost, "/internal/processes/"+op, token,
			map[string]any{"pluginId": "cron-manager", "name": "nope"})
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s status = %d，期望 409", op, rec.Code)
		}
		if code := decodeError(t, rec).Code; code != string(processmgr.CodeNotFound) {
			t.Fatalf("%s code = %q，期望 %s", op, code, processmgr.CodeNotFound)
		}
	}
	rec := h.serve(t, http.MethodGet, "/internal/processes/status?pluginId=cron-manager&name=nope", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status 查询 status = %d，期望 409", rec.Code)
	}
}

// workspace scope 只能用令牌绑定的工作区：请求里给别的工作区也没用。
func TestEndpointWorkspaceScopeBoundToToken(t *testing.T) {
	h := newHarness(t)
	bound := t.TempDir()
	token := h.issue(t, bound)
	other := t.TempDir()
	body := registerBody("cron-manager", "worker", entryFile(t))
	body["scope"] = "workspace"
	body["workspace"] = other // 应被忽略
	rec := h.serve(t, http.MethodPost, "/internal/processes/register", token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out processmgr.ProcessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("回执: %v", err)
	}
	if out.Ref == nil || out.Ref.Workspace != bound {
		t.Fatalf("workspace 应取令牌绑定值 %q，实得 %+v", bound, out.Ref)
	}
	if out.Status == nil || out.Status.Workspace != bound {
		t.Fatalf("状态里的 workspace 也应是绑定值: %+v", out.Status)
	}
}

// 引用计数：同一 Core 重复注册同形态只加引用；清理时按同样次数释放。
func TestEndpointReferenceCounting(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	body := registerBody("cron-manager", "scheduler", entryFile(t))
	for i := 0; i < 2; i++ {
		rec := h.serve(t, http.MethodPost, "/internal/processes/register", token, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次 register status = %d body = %s", i+1, rec.Code, rec.Body.String())
		}
	}
	if h.spawn.count() != 1 {
		t.Fatalf("同名多引用只应启动一次，实际 %d", h.spawn.count())
	}
	// 端点按「成功注册次数」记录引用：CleanupCore 必须释放同样次数，否则会永久漏一个引用。
	if n := h.endpoint.CleanupCore(dispatch.Identity{Workspace: "/ws/a", Key: "/ws/a"}); n != 2 {
		t.Fatalf("清理数 = %d，期望 2（两次注册 → 两次释放）", n)
	}
	if _, ok := h.manager.Status(processmgr.Ref{Owner: "cron-manager", Name: "scheduler"}); ok {
		t.Fatal("两次引用都释放后不应保留登记")
	}
}

// 同名但换了 entry/形态必须报 process_exists。
//
// 静默吞掉会让插件改完 entry 后以为生效，实际还在跑旧脚本——这类「看起来成功
// 其实没生效」的失败比报错更难查。
func TestEndpointRegisterRejectsChangedSpec(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	first := entryFile(t)
	if rec := h.serve(t, http.MethodPost, "/internal/processes/register", token,
		registerBody("cron-manager", "scheduler", first)); rec.Code != http.StatusOK {
		t.Fatalf("首次注册 status = %d body = %s", rec.Code, rec.Body.String())
	}

	// 同一名字，换成另一个 entry。
	second := entryFile(t)
	rec := h.serve(t, http.MethodPost, "/internal/processes/register", token,
		registerBody("cron-manager", "scheduler", second))
	if rec.Code != http.StatusConflict {
		t.Fatalf("换 entry 后 status = %d，期望 409（body=%s）", rec.Code, rec.Body.String())
	}
	if code := decodeError(t, rec).Code; code != string(processmgr.CodeExists) {
		t.Fatalf("error code = %q，期望 %s", code, processmgr.CodeExists)
	}

	// 同名同形态才是合法的重复注册（只加引用）。
	if rec := h.serve(t, http.MethodPost, "/internal/processes/register", token,
		registerBody("cron-manager", "scheduler", first)); rec.Code != http.StatusOK {
		t.Fatalf("同形态重复注册 status = %d，期望 200（body=%s）", rec.Code, rec.Body.String())
	}
	if h.spawn.count() != 1 {
		t.Fatalf("同名同形态只应启动一次，实际 %d", h.spawn.count())
	}
}

// list 只返回本插件自己的进程。
func TestEndpointListFiltersByOwner(t *testing.T) {
	h := newHarness(t)
	tokenA := h.issue(t, "/ws/a")
	tokenB := h.issue(t, "/ws/b")
	entry := entryFile(t)
	for _, tc := range []struct{ token, pluginID, name string }{
		{tokenA, "cron-manager", "scheduler"},
		{tokenB, "other-plugin", "worker"},
	} {
		rec := h.serve(t, http.MethodPost, "/internal/processes/register", tc.token,
			registerBody(tc.pluginID, tc.name, entry))
		if rec.Code != http.StatusOK {
			t.Fatalf("register %s status = %d", tc.name, rec.Code)
		}
	}
	rec := h.serve(t, http.MethodGet, "/internal/processes/list?pluginId=cron-manager", tokenA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var out processmgr.ProcessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("回执: %v", err)
	}
	if len(out.Statuses) != 1 || out.Statuses[0].Owner != "cron-manager" {
		t.Fatalf("list 结果 = %+v，期望只有 cron-manager 的 1 条", out.Statuses)
	}
}

// Core 重签令牌 → 旧登记被清理（新 Core 不会继承旧引用计数）。
func TestEndpointCleanupKeyOnReissue(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	rec := h.serve(t, http.MethodPost, "/internal/processes/register", token,
		registerBody("cron-manager", "scheduler", entryFile(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("register status = %d body = %s", rec.Code, rec.Body.String())
	}
	if n := h.endpoint.CleanupKey("/ws/a"); n != 1 {
		t.Fatalf("CleanupKey = %d，期望 1", n)
	}
	if _, ok := h.manager.Status(processmgr.Ref{Owner: "cron-manager", Name: "scheduler"}); ok {
		t.Fatal("清理后不应保留登记")
	}
	// 清理只影响索引：端点仍然可用（旧令牌在 IssueFor 重签前依然有效）。
	if rec := h.serve(t, http.MethodGet, "/internal/processes/list?pluginId=cron-manager", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("清理后旧令牌仍应能调列表: %d", rec.Code)
	}
}

// 非法 scope/runtime/start/restart 一律 400。
func TestEndpointRejectsBadEnums(t *testing.T) {
	h := newHarness(t)
	token := h.issue(t, "/ws/a")
	entry := entryFile(t)
	cases := []struct {
		field string
		value string
	}{
		{"scope", "galaxy"},
		{"runtime", "python"},
		{"start", "always"},
		{"restart", "on_crash"},
	}
	for _, c := range cases {
		body := registerBody("cron-manager", "scheduler", entry)
		body[c.field] = c.value
		rec := h.serve(t, http.MethodPost, "/internal/processes/register", token, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s=%q status = %d，期望 400（body=%s）", c.field, c.value, rec.Code, rec.Body.String())
		}
	}
}
