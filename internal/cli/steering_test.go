package cli_test

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/adag/ada-go/internal/cli"
)

// TestSessionSteeringQueueConcurrent 校验线程级 steering 队列在并发入队/排空时
// 不丢消息、不重复分配位置，并在轮次结束时丢弃残留、拒绝新的入队。
// 可以重复跑或用 -race 放大竞争。
func TestSessionSteeringQueueConcurrent(t *testing.T) {
	cfg := testConfig(t, "")
	cfg.Out = io.Discard
	cfg.Err = io.Discard
	sess, err := cli.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close(context.Background())

	const threadID = "th_steer_concurrency"
	const workers = 32
	sess.BeginTurn(threadID)

	positions := make(chan int, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pos, err := sess.EnqueueSteering(threadID, fmt.Sprintf("中途消息-%d", i))
			if err != nil {
				errs <- err
				return
			}
			positions <- pos
		}(i)
	}
	wg.Wait()
	close(positions)
	close(errs)
	for err := range errs {
		t.Fatalf("并发入队失败: %v", err)
	}

	seen := map[int]bool{}
	for pos := range positions {
		if pos < 1 || pos > workers {
			t.Fatalf("position = %d，期望 1..%d", pos, workers)
		}
		if seen[pos] {
			t.Fatalf("position %d 被重复分配", pos)
		}
		seen[pos] = true
	}
	if len(seen) != workers {
		t.Fatalf("位置数 = %d，期望 %d", len(seen), workers)
	}

	drained := sess.DrainSteering(context.Background(), threadID)
	if len(drained) != workers {
		t.Fatalf("排空消息 = %d，期望 %d", len(drained), workers)
	}
	if again := sess.DrainSteering(context.Background(), threadID); len(again) != 0 {
		t.Fatalf("二次排空返回了 %d 条消息", len(again))
	}

	// 轮次结束：残留消息必须被丢弃，之后的入队必须明确失败（不静默）。
	if _, err := sess.EnqueueSteering(threadID, "未排空的残留"); err != nil {
		t.Fatalf("运行中入队失败: %v", err)
	}
	sess.EndTurn(threadID)
	if left := sess.DrainSteering(context.Background(), threadID); len(left) != 0 {
		t.Fatalf("轮次结束后仍有 %d 条残留消息", len(left))
	}
	if _, err := sess.EnqueueSteering(threadID, "结束后"); err == nil {
		t.Fatal("轮次结束后仍接受 steering 入队")
	}
}
