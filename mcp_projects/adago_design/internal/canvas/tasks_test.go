package canvas

import (
	"context"
	"testing"
	"time"
)

func waitForTaskWaiters(t *testing.T, q *TaskQueue, key string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		q.mu.Lock()
		n := len(q.waiters[key])
		q.mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 %q 的 waiter 超时（当前 %d）", key, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTaskQueueGCExpiresStaleTasks(t *testing.T) {
	q := NewTaskQueue()
	fresh := q.Create("n-fresh", 1)
	stale := q.Create("n-stale", 1)

	q.mu.Lock()
	q.tasks[stale.ID].CreatedAt = time.Now().Add(-captureTaskTTL - time.Minute)
	q.mu.Unlock()

	q.GC()

	q.mu.Lock()
	_, freshOK := q.tasks[fresh.ID]
	_, staleOK := q.tasks[stale.ID]
	pending := append([]string(nil), q.pending...)
	q.mu.Unlock()

	if !freshOK {
		t.Fatal("未超时任务不应被清理")
	}
	if staleOK {
		t.Fatal("超时任务应被清理（无论是否回填结果）")
	}
	if len(pending) != 1 || pending[0] != fresh.ID {
		t.Fatalf("pending 应只保留未超时任务，得到 %v", pending)
	}
}

func TestTaskQueueGCWakesWaiter(t *testing.T) {
	q := NewTaskQueue()
	task := q.Create("n-wake", 1)

	done := make(chan *CaptureResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		done <- q.Wait(ctx, task.ID)
	}()
	waitForTaskWaiters(t, q, task.ID, 1)

	q.mu.Lock()
	q.tasks[task.ID].CreatedAt = time.Now().Add(-captureTaskTTL - time.Minute)
	q.mu.Unlock()
	q.GC()

	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("任务被 GC 后 Wait 应返回 nil, got %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("GC 未唤醒等待中的 Wait")
	}
}

func TestTaskQueueClaimSkipsStalePending(t *testing.T) {
	q := NewTaskQueue()
	q.mu.Lock()
	q.pending = append(q.pending, "cap-missing") // 模拟已被 GC 的陈旧 pending
	q.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := q.Claim(ctx); got != nil {
		t.Fatalf("应跳过陈旧 pending，得到 %+v", got)
	}
}

func TestTaskQueueWaitTimeoutRemovesWaiter(t *testing.T) {
	q := NewTaskQueue()
	task := q.Create("n-timeout", 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = q.Wait(ctx, task.ID)
		close(done)
	}()
	waitForTaskWaiters(t, q, task.ID, 1)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消后 Wait 未返回")
	}

	q.mu.Lock()
	n := len(q.waiters[task.ID])
	q.mu.Unlock()
	if n != 0 {
		t.Fatalf("Wait 超时/取消后 waiter 未清理（剩 %d）", n)
	}
}

func TestTaskQueueClaimTimeoutRemovesWaiter(t *testing.T) {
	q := NewTaskQueue()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := q.Claim(ctx); got != nil {
		t.Fatalf("空队列 Claim 应返回 nil, got %+v", got)
	}

	q.mu.Lock()
	n := len(q.waiters["claim"])
	q.mu.Unlock()
	if n != 0 {
		t.Fatalf("Claim 超时后 waiter 未清理（剩 %d）", n)
	}
}
