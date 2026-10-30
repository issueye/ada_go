package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// TestSingleWriterConcurrentAppends 是 M1-B 的核心用例：并发追加 1000 条 entry，
// 断言 seq 连续无重复、没有 SQLITE_BUSY 逃逸到业务层、队列反压不丢数据（04 §5.7）。
func TestSingleWriterConcurrentAppends(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	cfg := testConfig(t, ws, dataDir)
	cfg.WriterQueueSize = 32 // 故意小于 1000：逼出反压路径，证明「宁可等待」而不是丢。
	st := openStore(t, cfg)
	// 基线：OpenCore 自己会做两次启动善后写（标记 interrupted 轮次、过期交互），
	// 断言队列计数时用增量，避免和启动耦合。
	base := st.QueueStats()

	const writers = 10
	const perWriter = 100
	threads := make([]ports.Thread, writers)
	for i := range threads {
		threads[i] = mustThread(t, st, fmt.Sprintf("并发线程 %d", i))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := st.AppendEntry(ctx, ports.Entry{
					ThreadID: threads[w].ID,
					Kind:     domain.EntryMessage,
					Role:     domain.RoleAssistant,
					Payload:  json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
				})
				if err != nil {
					errCh <- fmt.Errorf("writer %d append %d: %w", w, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("并发写出现错误（SQLITE_BUSY 逃逸或丢数据）：%v", err)
	}

	total := 0
	for _, th := range threads {
		var maxSeq, count int64
		if err := st.write.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq),0), COUNT(*) FROM entries WHERE thread_id = ?`, th.ID).
			Scan(&maxSeq, &count); err != nil {
			t.Fatalf("查 seq: %v", err)
		}
		if count != perWriter {
			t.Fatalf("线程 %s 有 %d 条 entry，期望 %d（反压期间丢数据）", th.ID, count, perWriter)
		}
		if maxSeq != perWriter {
			t.Fatalf("线程 %s 的最大 seq=%d，期望 %d（seq 不连续或有重复）", th.ID, maxSeq, perWriter)
		}
		// seq 必须严格 1..N 无重复。
		var dup int
		if err := st.write.QueryRowContext(ctx, `
SELECT COUNT(*) FROM (SELECT seq FROM entries WHERE thread_id = ? GROUP BY seq HAVING COUNT(*) > 1)`, th.ID).
			Scan(&dup); err != nil {
			t.Fatalf("查重复 seq: %v", err)
		}
		if dup != 0 {
			t.Fatalf("线程 %s 有 %d 个重复 seq", th.ID, dup)
		}
		total += int(count)
	}
	if total != writers*perWriter {
		t.Fatalf("总计 %d 条，期望 %d", total, writers*perWriter)
	}

	stats := st.QueueStats()
	// 队列里走过的写 = 线程创建（writers 次）+ entry 追加（total 次）。
	submitted := stats.Submitted - base.Submitted
	committed := stats.Committed - base.Committed
	if want := uint64(writers + total); submitted != want {
		t.Fatalf("队列增量 submitted=%d，期望 %d（%d 次建线程 + %d 次追加）", submitted, want, writers, total)
	}
	if committed != submitted {
		t.Fatalf("committed=%d ≠ submitted=%d：有写没落库", committed, submitted)
	}
	if stats.Failed != base.Failed {
		t.Fatalf("不应有失败写：%+v", stats)
	}
	if stats.Queued != 0 {
		t.Fatalf("全部写完成后队列应为空：%+v", stats)
	}
	if stats.Retried > base.Retried+uint64(writers*perWriter) {
		t.Fatalf("退避重试次数异常：%+v", stats)
	}
	t.Logf("并发 %d 写者 × %d 条：入队 %d、提交 %d、退避重试 %d 次（队列容量 %d）",
		writers, perWriter, submitted, committed, stats.Retried-base.Retried, stats.Capacity)
}

// TestQueueBackpressureDoesNotLoseEntries 单独证明「队列满时等待而不是丢」：
// 队列容量 1、写 50 条，一条都不能少。
func TestQueueBackpressureDoesNotLoseEntries(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	cfg := testConfig(t, ws, dataDir)
	cfg.WriterQueueSize = 1
	st := openStore(t, cfg)
	th := mustThread(t, st, "反压线程")

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.AppendEntry(ctx, ports.Entry{
				ThreadID: th.ID, Kind: domain.EntryMessage, Role: domain.RoleUser,
				Payload: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("反压期间出现写失败：%v", err)
	}
	var count int
	if err := st.write.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE thread_id = ?`, th.ID).Scan(&count); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if count != n {
		t.Fatalf("写入 %d 条，期望 %d：队列满时丢了数据", count, n)
	}
}

// TestCancelledWriterDoesNotHang 验证调用方取消时 submit 立刻返回可判定错误，
// 且写协程不会因此卡死（后续写仍然成功）。
func TestCancelledWriterDoesNotHang(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	st := openStore(t, testConfig(t, ws, dataDir))
	th := mustThread(t, st, "取消线程")

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := st.AppendEntry(cancelledCtx, ports.Entry{
		ThreadID: th.ID, Kind: domain.EntryMessage, Role: domain.RoleUser,
		Payload: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatalf("已取消的 context 竟然写成功了")
	}
	de, ok := domain.AsError(err)
	if !ok {
		t.Fatalf("取消错误必须是业务错误，得到 %T", err)
	}
	if de.Code != domain.CodeTurnAborted {
		t.Fatalf("取消码=%d，期望 %d turn_aborted", de.Code, domain.CodeTurnAborted)
	}

	// 写协程必须还活着。
	ctx := context.Background()
	e, err := st.AppendEntry(ctx, ports.Entry{
		ThreadID: th.ID, Kind: domain.EntryMessage, Role: domain.RoleUser,
		Payload: json.RawMessage(`{"after":"cancel"}`),
	})
	if err != nil {
		t.Fatalf("取消之后继续写失败（写协程卡死了？）：%v", err)
	}
	if e.Seq == 0 {
		t.Fatalf("seq 没被分配")
	}
}

// TestUsageCoalescingNeverLoses 验证 usage 按窗口合并但总量不丢（04 §5.7）。
func TestUsageCoalescingNeverLoses(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	cfg := testConfig(t, ws, dataDir)
	cfg.UsageFlushWindow = 10 * time.Millisecond
	st := openStore(t, cfg)
	th := mustThread(t, st, "用量线程")

	const n = 200
	var wantIn, wantOut int64
	for i := 0; i < n; i++ {
		if err := st.RecordUsage(ctx, UsageRecord{
			ThreadID: th.ID, Provider: "p-test", Model: "m-test",
			InputTokens: 10, OutputTokens: 5, CacheTokens: 1, CostMicros: 7,
		}); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
		wantIn += 10
		wantOut += 5
	}
	// 等合并窗口落库（也验证 Close 之外的路径不丢）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := st.Flush(ctx); err != nil {
			t.Fatalf("Flush: %v", err)
		}
		agg, err := st.UsageByThread(ctx, th.ID)
		if err != nil {
			t.Fatalf("UsageByThread: %v", err)
		}
		if len(agg) == 1 && agg[0].Input == wantIn && agg[0].Output == wantOut {
			stats := st.QueueStats()
			if stats.UsageSeen != n {
				t.Fatalf("usageSeen=%d，期望 %d", stats.UsageSeen, n)
			}
			if stats.UsageRows >= n {
				t.Fatalf("usageRows=%d：没有发生窗口合并（期望明显少于 %d）", stats.UsageRows, n)
			}
			t.Logf("%d 条用量合并成 %d 行，总量不丢（input=%d output=%d）",
				stats.UsageSeen, stats.UsageRows, agg[0].Input, agg[0].Output)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("用量合并落库超时或总量不对")
}

// TestCloseCancelsInFlightAndDrains 验证 Close：已接受的写全部落库，
// 关闭后的写被拒绝并给出可判定错误（不静默丢弃）。
func TestCloseCancelsInFlightAndDrains(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	cfg := testConfig(t, ws, dataDir)
	cfg.WriterQueueSize = 4
	st := openStore(t, cfg)
	th := mustThread(t, st, "关闭线程")

	const n = 40
	var wg sync.WaitGroup
	accepted := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.AppendEntry(ctx, ports.Entry{
				ThreadID: th.ID, Kind: domain.EntryMessage, Role: domain.RoleUser,
				Payload: json.RawMessage(`{"i":` + strconv.Itoa(i) + `}`),
			})
			accepted <- err
		}(i)
	}
	wg.Wait()
	close(accepted)
	for err := range accepted {
		if err != nil {
			t.Fatalf("关闭前的写不该失败：%v", err)
		}
	}

	if err := st.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var count int
	r, err := OpenReadOnly(ctx, testConfig(t, ws, dataDir))
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer r.Close()
	if err := r.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM entries WHERE thread_id = ?`, th.ID).Scan(&count); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if count != n {
		t.Fatalf("Close 后排空了 %d/%d 条", count, n)
	}

	// 关闭之后再写：必须返回可判定错误（core_busy：实例正在回收）。
	if _, err := st.AppendEntry(ctx, ports.Entry{
		ThreadID: th.ID, Kind: domain.EntryMessage, Role: domain.RoleUser, Payload: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatalf("Close 之后竟然还能写")
	} else {
		de, ok := domain.AsError(err)
		if !ok || de.Code != domain.CodeCoreBusy {
			t.Fatalf("关闭后写的错误不对：%T %v", err, err)
		}
	}
}

// TestAppendEntryDefaultsParentToHead 钉住线性追加语义：省略 ParentID 时接到当前
// head。漏掉这一步，parent_id 全是 NULL，TraceFromHead 只回放最新一条 entry，
// 模型在第一次工具调用后就丢掉用户指令与工具结果（表现为「答非所问」）。
func TestAppendEntryDefaultsParentToHead(t *testing.T) {
	t.Parallel()
	ws, dataDir := newWorkspace(t)
	ctx := context.Background()
	st := openStore(t, testConfig(t, ws, dataDir))
	th := mustThread(t, st, "线性追加")

	first := appendMessage(t, st, th.ID, "", "一")
	if first.ParentID != "" {
		t.Fatalf("首条 entry 不该有父节点，got %q", first.ParentID)
	}
	second := appendMessage(t, st, th.ID, "", "二")
	if second.ParentID != first.ID {
		t.Fatalf("第二条 ParentID = %q，期望当前 head %q", second.ParentID, first.ID)
	}
	third := appendMessage(t, st, th.ID, "", "三")
	if third.ParentID != second.ID {
		t.Fatalf("第三条 ParentID = %q，期望当前 head %q", third.ParentID, second.ID)
	}

	win, err := st.TraceFromHead(ctx, th.ID)
	if err != nil {
		t.Fatalf("TraceFromHead: %v", err)
	}
	if len(win.Entries) != 3 {
		t.Fatalf("TraceFromHead 回放 %d 条，期望 3 条（parent 链断开）", len(win.Entries))
	}
	if win.Entries[0].ID != first.ID || win.Entries[2].ID != third.ID {
		t.Fatalf("回放顺序不对: %s..%s", win.Entries[0].ID, win.Entries[2].ID)
	}

	// 显式 ParentID 仍表示分支写入，不能被 head 覆盖。
	branch := appendMessage(t, st, th.ID, first.ID, "分支")
	if branch.ParentID != first.ID {
		t.Fatalf("显式 ParentID = %q，期望 %q", branch.ParentID, first.ID)
	}
}
