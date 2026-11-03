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
