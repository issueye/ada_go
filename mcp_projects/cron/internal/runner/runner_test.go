package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"adagcron/internal/store"
)

// fakeGateway 模拟网关 /api/cron/fire：校验 Bearer，返回可编程的响应。
type fakeGateway struct {
	server   *httptest.Server
	status   int
	resp     fireResponse
	lastBody fireRequest
	lastAuth string
	called   int
}

func newFakeGateway(t *testing.T, status int, resp fireResponse) *fakeGateway {
	t.Helper()
	g := &fakeGateway{status: status, resp: resp}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/cron/fire", func(w http.ResponseWriter, r *http.Request) {
		g.called++
		g.lastAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&g.lastBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(g.status)
		_ = json.NewEncoder(w).Encode(g.resp)
	})
	g.server = httptest.NewServer(mux)
	t.Cleanup(g.server.Close)
	return g
}

func setupTasks(t *testing.T, tasks ...store.Task) (path, lock string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, store.FileName)
	lock = filepath.Join(dir, store.LockName)
	if len(tasks) > 0 {
		if err := store.Save(path, tasks); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return path, lock
}

func TestTickFiresHTTPDueTask(t *testing.T) {
	hit := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// created=过去，cron 每分钟 → 当前时刻必然已有到期点（missed 语义不触发，
	// 因为 LastFiredAt 为空 + created 早于到期点 → 走 missed？不：missed 只在
	// LastFiredAt 为空时记一条并推进，之后下一轮正常跑。本用例先制造已跑过的状态。
	path, lock := setupTasks(t, store.Task{
		ID: "crn_http", Name: "ping", Enabled: true, Cron: "* * * * *",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		Action:    store.Action{Type: store.ActionHTTP, Method: "GET", URL: srv.URL},
	})
	// 预推进 lastFiredAt 到 1 分钟前：本轮必有到期点。
	_ = store.Update(path, lock, 5*time.Second, func(all *[]store.Task) error {
		(*all)[0].LastFiredAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		return nil
	})

	r := New(path, lock, "", "")
	r.tick(context.Background())

	select {
	case <-hit:
	case <-time.After(3 * time.Second):
		t.Fatalf("http 动作未被触发")
	}
	loaded, _, err := store.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded[0].Runs) != 1 || loaded[0].Runs[0].Status != store.RunOK {
		t.Fatalf("运行历史不符: %+v", loaded[0].Runs)
	}
}

func TestTickMissedRecordsOnce(t *testing.T) {
	path, lock := setupTasks(t, store.Task{
		ID: "crn_missed", Name: "m", Enabled: true, Cron: "* * * * *",
		CreatedAt: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
		Action:    store.Action{Type: store.ActionAgent, Text: "不应执行"},
	})
	g := newFakeGateway(t, http.StatusOK, fireResponse{Started: true})
	r := New(path, lock, g.server.URL, "tok")

	r.tick(context.Background())
	r.tick(context.Background())

	loaded, _, err := store.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded[0].Runs) != 1 || loaded[0].Runs[0].Status != store.RunMissed {
		t.Fatalf("应恰好一条 missed 历史: %+v", loaded[0].Runs)
	}
	if g.called != 0 {
		t.Fatalf("missed 不应执行动作，网关被调 %d 次", g.called)
	}
}

func TestTickAgentFireAndThreadReuse(t *testing.T) {
	g := newFakeGateway(t, http.StatusOK, fireResponse{Started: true, ThreadID: "thr_1"})
	path, lock := setupTasks(t, store.Task{
		ID: "crn_agent", Name: "ai", Enabled: true, Cron: "* * * * *",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		Action:    store.Action{Type: store.ActionAgent, Text: "早报"},
	})
	_ = store.Update(path, lock, 5*time.Second, func(all *[]store.Task) error {
		(*all)[0].LastFiredAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		return nil
	})

	r := New(path, lock, g.server.URL, "sekret")
	r.tick(context.Background())

	if g.called != 1 {
		t.Fatalf("网关应被调 1 次，实际 %d", g.called)
	}
	if g.lastAuth != "Bearer sekret" {
		t.Fatalf("鉴权头不符: %q", g.lastAuth)
	}
	if g.lastBody.TaskID != "crn_agent" || g.lastBody.Text != "早报" || g.lastBody.IdempotencyKey == "" {
		t.Fatalf("请求体不符: %+v", g.lastBody)
	}
	loaded, _, _ := store.Load(path)
	if loaded[0].Runs[0].Status != store.RunStarted {
		t.Fatalf("状态应为 started: %+v", loaded[0].Runs[0])
	}
}

func TestTickAgentBusyRecorded(t *testing.T) {
	g := newFakeGateway(t, http.StatusOK, fireResponse{Started: false, Reason: "thread_busy", ThreadID: "thr_9"})
	path, lock := setupTasks(t, store.Task{
		ID: "crn_busy", Name: "b", Enabled: true, Cron: "* * * * *",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		Action:    store.Action{Type: store.ActionAgent, Text: "x"},
	})
	_ = store.Update(path, lock, 5*time.Second, func(all *[]store.Task) error {
		(*all)[0].LastFiredAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
		return nil
	})

	r := New(path, lock, g.server.URL, "tok")
	r.tick(context.Background())

	loaded, _, _ := store.Load(path)
	if loaded[0].Runs[0].Status != store.RunBusy {
		t.Fatalf("状态应为 busy: %+v", loaded[0].Runs[0])
	}
}

func TestTickTriggerRequestedFiresImmediately(t *testing.T) {
	g := newFakeGateway(t, http.StatusOK, fireResponse{Started: true, ThreadID: "thr_t"})
	path, lock := setupTasks(t, store.Task{
		ID: "crn_trigger", Name: "t", Enabled: false, Cron: "0 9 * * *",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		TriggerRequested: true,
		Action:           store.Action{Type: store.ActionAgent, Text: "立即跑"},
	})
	r := New(path, lock, g.server.URL, "tok")
	r.tick(context.Background())

	if g.called != 1 {
		t.Fatalf("立即运行应触发网关调用，实际 %d", g.called)
	}
	loaded, _, _ := store.Load(path)
	if loaded[0].TriggerRequested {
		t.Fatalf("trigger 标记应被清零")
	}
	if loaded[0].Runs[0].Status != store.RunStarted {
		t.Fatalf("状态应为 started: %+v", loaded[0].Runs[0])
	}
}

func TestTickDisabledTaskSkipped(t *testing.T) {
	path, lock := setupTasks(t, store.Task{
		ID: "crn_off", Name: "off", Enabled: false, Cron: "* * * * *",
		CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		Action:    store.Action{Type: store.ActionAgent, Text: "x"},
	})
	g := newFakeGateway(t, http.StatusOK, fireResponse{Started: true})
	r := New(path, lock, g.server.URL, "tok")
	r.tick(context.Background())
	if g.called != 0 {
		t.Fatalf("停用任务不应触发网关")
	}
	loaded, _, _ := store.Load(path)
	if len(loaded[0].Runs) != 0 {
		t.Fatalf("停用任务不应有历史: %+v", loaded[0].Runs)
	}
}
