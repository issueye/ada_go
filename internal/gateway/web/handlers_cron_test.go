package web_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adag/ada-go/internal/gateway/relay"
	"github.com/adag/ada-go/internal/gateway/web"
)

// cronFirePost 向 /api/cron/fire 发一个 JSON 请求（机器端点，不走表单）。
func cronFirePost(t *testing.T, srv *web.Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/cron/fire", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestCronFireCreatesThreadAndStartsTurn(t *testing.T) {
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"thr_new"}}`),
		"turn.start":    json.RawMessage(`{"ok":true}`),
	}
	srv, sup := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_1","taskName":"早报","text":"总结昨天","idempotencyKey":"cron-crn_1-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Started  bool   `json:"started"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if !out.Started || out.ThreadID != "thr_new" {
		t.Fatalf("started/threadId 不符: %+v", out)
	}
	if got := sup.lastWorkspace(); got != "/tmp/ws" {
		t.Fatalf("应落在主工作区，得到 %q", got)
	}
	createCall, ok := core.lastCall("thread.create")
	if !ok {
		t.Fatalf("缺少 thread.create 调用")
	}
	if createCall.Params["title"] != "定时任务: 早报" {
		t.Fatalf("线程标题不符: %+v", createCall.Params)
	}
	turnCall, ok := core.lastCall("turn.start")
	if !ok {
		t.Fatalf("缺少 turn.start 调用")
	}
	if turnCall.Params["threadId"] != "thr_new" || turnCall.Params["text"] != "总结昨天" {
		t.Fatalf("turn.start 参数不符: %+v", turnCall.Params)
	}
	if turnCall.Params["idempotencyKey"] != "cron-crn_1-1" {
		t.Fatalf("幂等键未透传: %+v", turnCall.Params)
	}
}

func TestCronFireReusesProvidedThread(t *testing.T) {
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.get": json.RawMessage(`{"thread":{"id":"thr_kept"}}`),
		"turn.start": json.RawMessage(`{"ok":true}`),
	}
	srv, _ := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_2","taskName":"日报","text":"生成日报","threadId":"thr_kept"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if core.countCalls("thread.create") != 0 {
		t.Fatalf("带 threadId 时不应再创建线程")
	}
	turnCall, ok := core.lastCall("turn.start")
	if !ok || turnCall.Params["threadId"] != "thr_kept" {
		t.Fatalf("turn.start 应复用线程: %+v %v", turnCall, ok)
	}
}

func TestCronFireThreadBusyIsNotAnError(t *testing.T) {
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"thr_busy"}}`),
	}
	core.errs = map[string]error{
		"turn.start": &relay.RPCError{Code: 2001, Message: "线程已有运行中轮次"},
	}
	srv, _ := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_3","taskName":"x","text":"hi"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("thread_busy 应返回 200，得到 %d", rec.Code)
	}
	var out struct {
		Started bool   `json:"started"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if out.Started || out.Reason != "thread_busy" {
		t.Fatalf("应返回 started=false reason=thread_busy: %+v", out)
	}
}

func TestCronFireTurnFailurePropagates(t *testing.T) {
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"thr_x"}}`),
	}
	core.errs = map[string]error{
		"turn.start": errors.New("core 爆炸"),
	}
	srv, _ := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_4","taskName":"x","text":"hi"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("turn.start 失败应 502，得到 %d", rec.Code)
	}
	var out struct {
		ThreadID string `json:"threadId"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if out.ThreadID != "thr_x" || !strings.Contains(out.Error, "core 爆炸") {
		t.Fatalf("错误信息不符: %+v", out)
	}
}

func TestCronFireRebuildsDeletedThread(t *testing.T) {
	// 带来的 threadId 已被用户删除：本次触发就重建线程并启动轮次，
	// 不能跳过这一轮（否则「立即运行」会静默丢失）。
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"thr_new"}}`),
		"turn.start":    json.RawMessage(`{"ok":true}`),
	}
	core.errs = map[string]error{
		"thread.get": &relay.RPCError{Code: -32602, Message: "thread.get: 线程不存在"},
	}
	srv, _ := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_6","taskName":"x","text":"hi","threadId":"thr_gone"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Started   bool   `json:"started"`
		ThreadID  string `json:"threadId"`
		Recreated bool   `json:"recreated"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if !out.Started || out.ThreadID != "thr_new" || !out.Recreated {
		t.Fatalf("应在重建线程后启动: %+v", out)
	}
	turn, ok := core.lastCall("turn.start")
	if !ok || turn.Params["threadId"] != "thr_new" {
		t.Fatalf("轮次应落在新线程: %+v %v", turn.Params, ok)
	}
}

func TestCronFireRebuildsArchivedThread(t *testing.T) {
	// thread.delete 是软删：thread.get 仍能返回线程，但 archived=true。
	// 网关必须把它当作「已删除」并在本次触发里重建。
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.get":    json.RawMessage(`{"thread":{"id":"thr_old","archived":true}}`),
		"thread.create": json.RawMessage(`{"thread":{"id":"thr_new"}}`),
		"turn.start":    json.RawMessage(`{"ok":true}`),
	}
	srv, _ := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_10","taskName":"x","text":"hi","threadId":"thr_old"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Started   bool   `json:"started"`
		ThreadID  string `json:"threadId"`
		Recreated bool   `json:"recreated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if !out.Started || out.ThreadID != "thr_new" || !out.Recreated {
		t.Fatalf("软删线程应重建后启动: %+v", out)
	}
	turn, ok := core.lastCall("turn.start")
	if !ok || turn.Params["threadId"] != "thr_new" {
		t.Fatalf("轮次应落在新线程: %+v %v", turn.Params, ok)
	}
}

func TestCronFireThreadCheckTransientError(t *testing.T) {
	// thread.get 的非「线程不存在」错误不能当成删除：保留原线程并明确失败。
	core := newFakeCore()
	core.errs = map[string]error{
		"thread.get": errors.New("core 爆炸"),
	}
	srv, _ := newServer(t, core)

	rec := cronFirePost(t, srv, `{"taskId":"crn_9","taskName":"x","text":"hi","threadId":"thr_live"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if core.countCalls("thread.create") != 0 || core.countCalls("turn.start") != 0 {
		t.Fatal("线程校验失败时不应重建线程或启动轮次")
	}
}

func TestCronFireUsesRequestedWorkspace(t *testing.T) {
	core := newFakeCore()
	core.responses = map[string]json.RawMessage{
		"thread.create": json.RawMessage(`{"thread":{"id":"thr_b"}}`),
		"turn.start":    json.RawMessage(`{"ok":true}`),
	}
	srv, sup := newServerWithWorkspaces(t, core, []string{"/tmp/ws-a", "/tmp/ws-b"})

	rec := cronFirePost(t, srv, `{"taskId":"crn_7","taskName":"x","text":"hi","workspace":"/tmp/ws-b"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := sup.lastWorkspace(); got != "/tmp/ws-b" {
		t.Fatalf("任务指定的工作区未生效，实际拉起 %q", got)
	}
}

func TestCronFireRejectsUnknownWorkspace(t *testing.T) {
	srv, sup := newServerWithWorkspaces(t, newFakeCore(), []string{"/tmp/ws-a"})

	rec := cronFirePost(t, srv, `{"taskId":"crn_8","taskName":"x","text":"hi","workspace":"/tmp/elsewhere"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知工作区应 400，得到 %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !strings.Contains(out.Error, "不在网关的工作区列表") {
		t.Fatalf("错误信息应说明工作区不可用: %v %+v", err, out)
	}
	if got := sup.lastWorkspace(); got != "" {
		t.Fatalf("未知工作区不应拉起任何 Core，实际 %q", got)
	}
}

func TestWorkspacesAPIListsWorkspacesAndDefault(t *testing.T) {
	srv, _ := newServerWithWorkspaces(t, newFakeCore(), []string{"/tmp/ws-a", "/tmp/ws-b"})

	rec := serve(t, srv, "GET", "/api/workspaces", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Default    string `json:"default"`
		Workspaces []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v", err)
	}
	if out.Default != "/tmp/ws-a" {
		t.Fatalf("默认工作区 = %q，期望列表第一个", out.Default)
	}
	if len(out.Workspaces) != 2 || out.Workspaces[0].Path != "/tmp/ws-a" || out.Workspaces[1].Path != "/tmp/ws-b" {
		t.Fatalf("工作区列表不符: %+v", out.Workspaces)
	}
	if out.Workspaces[0].Name != "ws-a" {
		t.Fatalf("工作区名应取目录名: %+v", out.Workspaces[0])
	}
}

func TestCronFireValidatesInput(t *testing.T) {
	srv, _ := newServer(t, newFakeCore())

	cases := []struct {
		name string
		body string
		code int
	}{
		{"缺 taskId", `{"text":"hi"}`, http.StatusBadRequest},
		{"缺 text", `{"taskId":"crn_5"}`, http.StatusBadRequest},
		{"非法 JSON", `{not-json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := cronFirePost(t, srv, tc.body)
		if rec.Code != tc.code {
			t.Errorf("%s: status = %d，期望 %d", tc.name, rec.Code, tc.code)
		}
	}
}
