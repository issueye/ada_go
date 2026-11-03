package control

// 异步任务（/v1/calls）的边界：启动立即返回、结果可轮询、可取消、并发上限，
// 以及「客户端断开不影响任务」。全部用注入的 runFn，不依赖 GUI/浏览器。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"browseruse/internal/browser"
	"browseruse/internal/config"
)

const testToken = "test-token"

func newAsyncTestServer(run func(ctx context.Context, name string, args json.RawMessage, client string) callResult) *httptest.Server {
	s := &Server{cfg: config.Default(), token: testToken, jobs: newJobStore(), runFn: run}
	return httptest.NewServer(s)
}

func doJSON(t *testing.T, method, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Client-Id", "adag-thread-1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("%s %s: decode: %v", method, url, err)
	}
	return res, out
}

func jobOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	job, ok := out["job"].(map[string]any)
	if !ok {
		t.Fatalf("响应里没有 job：%v", out)
	}
	return job
}

// TestAsyncCallStartAndPoll 是主路径：启动立即返回，轮询到 done 拿到结果。
func TestAsyncCallStartAndPoll(t *testing.T) {
	srv := newAsyncTestServer(func(ctx context.Context, name string, args json.RawMessage, client string) callResult {
		time.Sleep(120 * time.Millisecond)
		return callResult{OK: true, Data: map[string]any{"name": name, "client": client}, TimingMS: 120}
	})
	defer srv.Close()

	start := time.Now()
	res, out := doJSON(t, http.MethodPost, srv.URL+"/v1/calls",
		`{"name":"status","args":{"include_tabs":true},"timeout_ms":5000}`)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("启动应返回 202，实得 %d：%v", res.StatusCode, out)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("启动请求花了 %s：长任务必须在启动时立即返回", elapsed)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("没有 job id：%v", out)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, got := doJSON(t, http.MethodGet, srv.URL+"/v1/calls/"+id, "")
		job := jobOf(t, got)
		if job["status"] == "done" {
			result, _ := job["result"].(map[string]any)
			if result["ok"] != true {
				t.Fatalf("任务结果不是 ok：%v", result)
			}
			data, _ := result["data"].(map[string]any)
			if data["name"] != "status" {
				t.Fatalf("结果数据不对：%v", data)
			}
			// client 归因必须保留调用方给的值，不能退化成 cli-<pid>。
			if data["client"] != "adag-thread-1" {
				t.Fatalf("client 归因丢失：%v", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务没有在期限内完成：%v", job)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// TestAsyncCallCancel 钉「取消」：DELETE 后任务收尾为 cancelled，不会再执行完。
func TestAsyncCallCancel(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	srv := newAsyncTestServer(func(ctx context.Context, name string, args json.RawMessage, client string) callResult {
		close(started)
		<-ctx.Done()
		close(finished)
		return callResult{Error: &browser.Error{Code: browser.CodeBlockedAction, Message: "cancelled"}}
	})
	defer srv.Close()

	_, out := doJSON(t, http.MethodPost, srv.URL+"/v1/calls", `{"name":"script","timeout_ms":60000}`)
	id, _ := out["id"].(string)
	<-started
	res, out := doJSON(t, http.MethodDelete, srv.URL+"/v1/calls/"+id, "")
	if res.StatusCode != http.StatusOK || out["cancelled"] != true {
		t.Fatalf("DELETE 应确认取消，实得 %d：%v", res.StatusCode, out)
	}
	job := jobOf(t, out)
	if job["status"] != "cancelled" {
		t.Fatalf("取消后状态应为 cancelled：%v", job)
	}
	<-finished
	// 任务自己收尾后，状态不能被 finish 覆盖回 done。
	_, got := doJSON(t, http.MethodGet, srv.URL+"/v1/calls/"+id, "")
	if jobOf(t, got)["status"] != "cancelled" {
		t.Fatalf("finish 覆盖了 cancelled 状态：%v", jobOf(t, got))
	}
}

// TestAsyncCallConcurrencyLimit 钉并发上限（429），避免任务把 Runtime 拖死。
func TestAsyncCallConcurrencyLimit(t *testing.T) {
	release := make(chan struct{})
	srv := newAsyncTestServer(func(ctx context.Context, name string, args json.RawMessage, client string) callResult {
		<-release
		return callResult{OK: true}
	})
	defer srv.Close()
	defer close(release)

	for i := 0; i < defaultMaxRunning; i++ {
		res, out := doJSON(t, http.MethodPost, srv.URL+"/v1/calls", `{"name":"status"}`)
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("第 %d 个任务应启动成功，实得 %d：%v", i+1, res.StatusCode, out)
		}
	}
	res, _ := doJSON(t, http.MethodPost, srv.URL+"/v1/calls", `{"name":"status"}`)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("超过并发上限应返回 429，实得 %d", res.StatusCode)
	}
}

// TestAsyncCallRejectsUnknownTool 钉启动前校验：未知工具直接 400，不占任务槽。
func TestAsyncCallRejectsUnknownTool(t *testing.T) {
	srv := newAsyncTestServer(func(ctx context.Context, name string, args json.RawMessage, client string) callResult {
		t.Error("未知工具不应该被执行")
		return callResult{}
	})
	defer srv.Close()
	res, out := doJSON(t, http.MethodPost, srv.URL+"/v1/calls", `{"name":"nope"}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("未知工具应返回 400，实得 %d：%v", res.StatusCode, out)
	}
}

// TestAsyncCallSurvivesClientDisconnect 钉核心动机：客户端断开（或超时）后任务继续，
// 结果仍然可轮询——这正是 /v1/call 同步模型做不到的事。
func TestAsyncCallSurvivesClientDisconnect(t *testing.T) {
	outcome := make(chan bool, 1) // true = 被取消
	srv := newAsyncTestServer(func(ctx context.Context, name string, args json.RawMessage, client string) callResult {
		select {
		case <-ctx.Done():
			outcome <- true
		case <-time.After(200 * time.Millisecond):
			outcome <- false
		}
		return callResult{OK: true, Data: map[string]any{"survived": true}}
	})
	defer srv.Close()

	// 发完启动请求就断开：模拟调用方超时/进程退出。
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/calls", strings.NewReader(`{"name":"script"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	_ = res.Body.Close()
	id, _ := out["id"].(string)

	if <-outcome {
		t.Fatal("任务被调用方的断开取消了：任务必须与请求解耦")
	}
	_, got := doJSON(t, http.MethodGet, srv.URL+"/v1/calls/"+id, "")
	if job := jobOf(t, got); job["status"] != "done" {
		t.Fatalf("断开后任务应正常完成：%v", job)
	}
}
