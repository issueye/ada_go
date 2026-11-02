package host

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBackgroundTaskLimit 钉每个 Runtime 的后台任务上限与已结束任务回收。
func TestBackgroundTaskLimit(t *testing.T) {
	cmd, argv := "cmd.exe", []string{"/c", "ping", "-n", "6", "127.0.0.1"}
	if runtime.GOOS != "windows" {
		cmd, argv = "/bin/sh", []string{"-c", "sleep 5"}
	}
	e := newExecState()
	defer func() { _ = e.shutdown() }()

	start := func() (*backgroundTask, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		return e.startBackground(&execRequest{
			file: cmd, args: argv, timeout: 5 * time.Second, maxBuffer: 1024,
			ctx: ctx, cancelFn: cancel,
		}, 1)
	}

	task, err := start()
	if err != nil {
		t.Fatalf("第一个后台任务: %v", err)
	}
	if task.id <= 0 {
		t.Fatalf("taskId = %d，期望正整数", task.id)
	}
	if _, err := start(); err == nil {
		t.Fatal("达到上限后第二个后台任务应被拒绝")
	}
	if !task.kill() {
		t.Fatal("kill 应作用于 running 任务")
	}
	waitStatus(t, e, task.id, bgKilled)

	// 已结束任务应被下次 start 回收，而不是永久占住上限。
	second, err := start()
	if err != nil {
		t.Fatalf("回收后应能启动新任务: %v", err)
	}
	second.kill()
	waitStatus(t, e, second.id, bgKilled)
}

// waitStatus 轮询任务状态，直到命中或超时。
func waitStatus(t *testing.T, e *execState, id int, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		view, ok := e.checkBackground(id)
		if !ok {
			t.Fatalf("任务 %d 消失", id)
		}
		if view["status"] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 %d 状态未到 %q：%#v", id, want, view)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTailUTF8(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"abcdef", 3, "def"},
		{"你好世界", 5, "界"}, // 5 字节会切在「世」中间，只能保留完整的「界」
		{"short", 10, "short"},
		{"", 2, ""},
	}
	for _, c := range cases {
		if got := tailUTF8(c.in, c.max); got != c.want {
			t.Fatalf("tailUTF8(%q,%d) = %q，期望 %q", c.in, c.max, got, c.want)
		}
	}
	if out := tailUTF8(strings.Repeat("x", 100), 10); len(out) != 10 {
		t.Fatalf("尾部长度 = %d，期望 10", len(out))
	}
}
