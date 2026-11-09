package processmgr_test

// 端到端：把 cron-manager 的 scheduler.js 当真实被管进程拉起来，验证
// 「注册 → 注入控制凭据 → 读配置 → 到点经内部端点投递 → 写运行历史」整条链。
//
// 没有 node 时跳过（CI 的部分腿没有 node）；这里不依赖真实网关，用 httptest
// 冒充 /internal/dispatch/send。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/gateway/processmgr"
)

// dispatchStub 记录收到的投递请求，并返回一个固定回执。
type dispatchStub struct {
	mu    sync.Mutex
	bodie []map[string]any
	auth  []string
	srv   *httptest.Server
}

func newDispatchStub(t *testing.T) *dispatchStub {
	t.Helper()
	s := &dispatchStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/dispatch/send", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.bodie = append(s.bodie, body)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"receipt": map[string]any{
				"workspace": body["workspace"], "threadId": "th_sched",
				"runId": "run_sched", "mode": "start",
			},
		})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// waitUntil 轮询条件直到成立或超时（与包内 waitFor 分开命名，避免与外部测试包冲突）。
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%s）", timeout)
}

func (s *dispatchStub) calls() ([]map[string]any, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodie...), append([]string(nil), s.auth...)
}

// stubIssuer 给被管进程签发一个固定令牌，并返回 httptest 的地址。
type stubIssuer struct{ url string }

func (s stubIssuer) IssueForProcess(owner, name, workspace string) (string, string) {
	return s.url, "proc-token"
}

func (s stubIssuer) RevokeProcess(string, string, string) {}

// repoRoot 从包目录向上找 go.mod。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到仓库根")
		}
		dir = parent
	}
}

// TestSchedulerProcessDispatchesOnDueTask 覆盖 P6 的核心链路。
func TestSchedulerProcessDispatchesOnDueTask(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过真实进程测试")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过调度进程端到端测试")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "plugins", "official", "cron-manager", "dist", "scheduler.js")
	if _, statErr := os.Stat(script); statErr != nil {
		t.Skipf("缺少 scheduler.js（先运行 node plugins/official/build.mjs）: %v", statErr)
	}

	// 配置：一个「每分钟」任务。scheduler 第一拍就会命中。
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tasks := map[string]any{
		"version": 1,
		"values": map[string]any{
			"tasks": []map[string]any{{
				"id": "t1", "name": "每分钟", "enabled": true, "cron": "* * * * *",
				"workspace": "/ws/a", "action": map[string]any{"type": "agent", "text": "生成日报"},
			}},
		},
	}
	raw, _ := json.Marshal(tasks)
	if err := os.WriteFile(filepath.Join(configDir, "cron.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	stub := newDispatchStub(t)
	mgr, err := processmgr.New(processmgr.Options{
		Control: stubIssuer{url: stub.srv.URL},
		Logf:    t.Logf,
		// node 由测试显式指定，避免依赖 PATH 之外的解析差异。
		NodeCommand: node,
	})
	if err != nil {
		t.Fatalf("processmgr.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Shutdown(ctx)
	})

	// scheduler 自己按 ADAGO_HOME 找配置；这里显式注入。
	ref, err := mgr.Register("cron-manager", processmgr.ProcessSpec{
		Name: "scheduler", Scope: processmgr.ScopeGlobal, Runtime: processmgr.RuntimeNode,
		Entry: script, StartMode: processmgr.StartOnEnable, Restart: processmgr.RestartNo,
		Env: map[string]string{"ADAGO_HOME": home},
		// 停止要留余量：node 退出 + 进程树回收。
		StopTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 等第一拍投递（tick 间隔 30s，但首拍在启动时立即执行）。
	deadline := time.Now().Add(20 * time.Second)
	var bodies []map[string]any
	var auths []string
	for time.Now().Before(deadline) {
		bodies, auths = stub.calls()
		if len(bodies) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(bodies) == 0 {
		logs, _ := mgr.Logs(ref, 0)
		t.Fatalf("调度进程没有投递任何消息；日志：%v", logs)
	}

	got := bodies[0]
	if got["text"] != "生成日报" || got["title"] != "每分钟" || got["mode"] != "start" {
		t.Fatalf("投递请求 = %+v", got)
	}
	if got["workspace"] != "/ws/a" {
		t.Fatalf("workspace = %v，期望 /ws/a", got["workspace"])
	}
	if key, _ := got["idempotencyKey"].(string); !strings.HasPrefix(key, "cron:t1:") {
		t.Fatalf("幂等键 = %q，期望以 cron:t1: 开头", key)
	}
	if got["taskId"] != "t1" {
		t.Fatalf("taskId = %v，期望 t1", got["taskId"])
	}
	if len(auths) == 0 || auths[0] != "Bearer proc-token" {
		t.Fatalf("投递没有带控制令牌: %v", auths)
	}

	// 运行历史写回同一份配置（scheduler 只打补丁，不整份覆盖）。
	waitUntil(t, 10*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(configDir, "cron.json"))
		if err != nil {
			return false
		}
		return strings.Contains(string(raw), `"threadId": "th_sched"`) &&
			strings.Contains(string(raw), `"status": "started"`)
	})
	final, err := os.ReadFile(filepath.Join(configDir, "cron.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 任务定义字段必须原样保留（scheduler 不新建/不改定义）。
	for _, want := range []string{`"cron": "* * * * *"`, `"生成日报"`, `"每分钟"`} {
		if !strings.Contains(string(final), want) {
			t.Fatalf("运行历史写回覆盖了任务定义，缺少 %s: %s", want, final)
		}
	}

	// 停止后进程不再投递。
	if err := mgr.Unregister(ref); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	before, _ := stub.calls()
	time.Sleep(500 * time.Millisecond)
	after, _ := stub.calls()
	if len(after) != len(before) {
		t.Fatalf("注销后仍有投递：%d → %d", len(before), len(after))
	}
}
