package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// writeTask 是一次提交给唯一写协程的工作（04 §5.7「单写者协程」）。
//
// fn 只在写协程里执行，所以它内部可以自由分配 seq、读计数器，而不必考虑并发；
// 事务由 runTask 统一包裹：写连接池的 DSN 带 _txlock=immediate，
// 因此 database/sql 的 BeginTx 就是 BEGIN IMMEDIATE（04 §5.7「写事务一律 BEGIN IMMEDIATE」）。
type writeTask struct {
	ctx  context.Context
	kind string
	fn   func(ctx context.Context, tx *sql.Tx) error
	done chan error
}

// usageKey 是 usage 合并窗口的键（04 §5.7：usage 可按窗口合并，绝不丢 entry 或交互）。
type usageKey struct {
	threadID string
	provider string
	model    string
}

// usageRow 是窗口内累加出来的一行 usage。
type usageRow struct {
	threadID     string
	provider     string
	model        string
	inputTokens  int64
	outputTokens int64
	cacheTokens  int64
	costMicros   int64
	count        int
	lastAt       int64 // 窗口内最后一条的 created_at（UnixMilli），作为落库时间
}

// 反压等待时的轮询粒度：队列满时 submit 在条件变量上等空位，
// 10ms 的唤醒只影响「排队多久」，不影响正确性（没有数据会被丢）。
const queuePollInterval = 10 * time.Millisecond

// writer 是唯一的写协程：所有写路径经有界队列串行提交，seq 也在这里分配。
//
// 队列实现用「互斥锁 + 条件变量 + 有界切片」而不是 channel：
// 「检查是否已关闭」与「入队」必须在同一次加锁内完成，否则 Close 与提交会打架，
// 极端调度下会出现「任务进了队列但写协程已经退出」的挂死窗口。
// 队列满时 submit 阻塞等待（04 §5.7 的反压语义：宁可让轮次等待，也不丢 entry/交互）。
//
// 队列之外的写路径只有 usage 合并窗口：submitUsage 只改内存，落库仍由本协程做，
// 所以「唯一写者」这条性质不被破坏。
type writer struct {
	db  *sql.DB
	cfg Config

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []*writeTask
	closed bool

	doneW chan struct{}

	pending map[usageKey]*usageRow

	// 观测计数（atomic，供 QueueStats）。
	submitted atomic.Uint64
	committed atomic.Uint64
	failed    atomic.Uint64
	retried   atomic.Uint64
	usageRows atomic.Uint64 // 实际写入的 usage 行数（合并后）
	usageSeen atomic.Uint64 // 提交过的 usage 条数（合并前）

	lastErr atomic.Value // string：最近一次写失败（doctor 用）
}

func newWriter(db *sql.DB, cfg Config) *writer {
	w := &writer{
		db:      db,
		cfg:     cfg,
		doneW:   make(chan struct{}),
		pending: make(map[usageKey]*usageRow),
	}
	w.cond = sync.NewCond(&w.mu)
	go w.run()
	return w
}

// QueueStats 是队列观测面（debug.state / doctor）。
type QueueStats struct {
	Capacity    int
	Queued      int
	Submitted   uint64
	Committed   uint64
	Failed      uint64
	Retried     uint64
	UsageSeen   uint64
	UsageRows   uint64
	PendingKeys int
	LastError   string
}

// Stats 返回当前队列与计数快照。
func (w *writer) Stats() QueueStats {
	w.mu.Lock()
	pk := len(w.pending)
	q := len(w.queue)
	w.mu.Unlock()
	last, _ := w.lastErr.Load().(string)
	return QueueStats{
		Capacity:    w.cfg.WriterQueueSize,
		Queued:      q,
		Submitted:   w.submitted.Load(),
		Committed:   w.committed.Load(),
		Failed:      w.failed.Load(),
		Retried:     w.retried.Load(),
		UsageSeen:   w.usageSeen.Load(),
		UsageRows:   w.usageRows.Load(),
		PendingKeys: pk,
		LastError:   last,
	}
}

// wake 唤醒一个等待者（写协程取走任务、关闭、或超时轮询都用它）。
func (w *writer) wake() {
	w.mu.Lock()
	w.cond.Broadcast()
	w.mu.Unlock()
}

// submit 把一次写投递给唯一写协程并等待结果。
//
// 队列满时这里阻塞——这正是 04 §5.7 要的反压（宁可让轮次等待，也不丢 entry/交互）。
// 只有两种情况不入队：调用方 ctx 取消、Store 已关闭；两者都返回可判定错误，
// 绝不静默丢弃。
func (w *writer) submit(ctx context.Context, kind string, fn func(ctx context.Context, tx *sql.Tx) error) error {
	t := &writeTask{ctx: ctx, kind: kind, fn: fn, done: make(chan error, 1)}

	w.mu.Lock()
	for {
		if w.closed {
			w.mu.Unlock()
			return w.closedErr(kind)
		}
		if len(w.queue) < w.cfg.WriterQueueSize {
			w.queue = append(w.queue, t)
			w.submitted.Add(1)
			w.cond.Broadcast()
			w.mu.Unlock()
			break
		}
		// 队列满：等空位（带 ctx 检查的超时唤醒，避免取消后永久挂住）。
		if err := ctx.Err(); err != nil {
			w.mu.Unlock()
			w.failed.Add(1)
			return abort(fmt.Sprintf("写队列反压期间调用方取消（%s）", kind), err)
		}
		tmr := time.AfterFunc(queuePollInterval, w.wake)
		w.cond.Wait()
		tmr.Stop()
	}

	select {
	case err := <-t.done:
		if err != nil {
			w.failed.Add(1)
			w.lastErr.Store(kind + ": " + err.Error())
		} else {
			w.committed.Add(1)
		}
		return err
	case <-ctx.Done():
		// 任务已入队：写协程执行到它时会看到 ctx 取消并放弃（不写半条），
		// 这里立即把取消回传给调用方，done 由带缓冲的通道承接不会堵住写协程。
		w.failed.Add(1)
		return abort(fmt.Sprintf("写事务等待期间调用方取消（%s）", kind), ctx.Err())
	}
}

func (w *writer) closedErr(kind string) error {
	return fail(domain.CodeCoreBusy, fmt.Sprintf(
		"存储已关闭，拒绝新的写操作（%s）：Close 会取消在途写并排空已入队任务", kind), nil)
}

// abort 表示调用方取消导致的写中止（不是存储故障）。
func abort(detail string, cause error) *domain.Error {
	return fail(domain.CodeTurnAborted, detail+": "+cause.Error(), cause)
}

// submitUsage 记录一次用量：进入合并窗口，不占用写队列（04 §5.7）。
//
// 窗口由写协程按 UsageFlushWindow 定时落库，Close 时强制落库；只有进程被 kill 才可能
// 丢掉尚未落库的 usage（设计允许的档），entry 与交互走 submit，不受此影响。
func (w *writer) submitUsage(u UsageRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.closedErr("usage")
	}
	key := usageKey{threadID: u.ThreadID, provider: u.Provider, model: u.Model}
	row, ok := w.pending[key]
	if !ok {
		row = &usageRow{threadID: u.ThreadID, provider: u.Provider, model: u.Model, lastAt: u.CreatedAtMS}
		w.pending[key] = row
	}
	row.inputTokens += u.InputTokens
	row.outputTokens += u.OutputTokens
	row.cacheTokens += u.CacheTokens
	row.costMicros += u.CostMicros
	row.count++
	if u.CreatedAtMS > row.lastAt {
		row.lastAt = u.CreatedAtMS
	}
	w.usageSeen.Add(1)
	return nil
}

// wait 取下一个任务：
//   - (task, false)：拿到任务；
//   - (nil, false)：已关闭且队列排空，写协程该退出；
//   - (nil, true) ：usage 窗口到点（虚假唤醒也走这里，只是提前落一次库，无害）。
func (w *writer) wait() (*writeTask, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if len(w.queue) > 0 {
			t := w.queue[0]
			w.queue = w.queue[1:]
			w.cond.Broadcast() // 让因队列满而等待的提交者看到空位
			return t, false
		}
		if w.closed {
			return nil, false
		}
		tmr := time.AfterFunc(w.cfg.UsageFlushWindow, w.wake)
		w.cond.Wait()
		tmr.Stop()
		if len(w.queue) > 0 || w.closed {
			continue
		}
		return nil, true
	}
}

// run 是唯一写协程主体。
func (w *writer) run() {
	defer close(w.doneW)
	for {
		t, timeout := w.wait()
		if timeout {
			w.flushUsage(w.takePending())
			continue
		}
		if t == nil {
			break // 关闭：wait 已确认队列为空。
		}
		w.runTask(t)
	}
	// 到这里 closed=true 且不会再有新任务入队（入队与关闭检查在同一次加锁里完成）。
	w.flushUsage(w.takePending())
}

// takePending 原子地取走当前窗口内累积的 usage。
func (w *writer) takePending() []*usageRow {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return nil
	}
	out := make([]*usageRow, 0, len(w.pending))
	for _, r := range w.pending {
		out = append(out, r)
	}
	w.pending = make(map[usageKey]*usageRow)
	return out
}

// runTask 在事务里执行一个写任务，带 SQLITE_BUSY/SQLITE_LOCKED 指数退避重试
// （最多 MaxBusyRetries 次，04 §5.7）。
func (w *writer) runTask(t *writeTask) {
	if t == nil {
		return
	}
	var last error
	for attempt := 0; attempt <= MaxBusyRetries; attempt++ {
		if err := t.ctx.Err(); err != nil {
			t.done <- abort(fmt.Sprintf("写任务执行前调用方已取消（%s）", t.kind), err)
			return
		}
		if attempt > 0 {
			w.retried.Add(1)
			select {
			case <-time.After(BusyBackoffBase << (attempt - 1)):
			case <-t.ctx.Done():
			}
		}
		err := w.execOnce(t)
		if err == nil {
			t.done <- nil
			return
		}
		last = err
		if isCorruptError(err) {
			t.done <- storageCorrupt("写事务遇到 SQLITE_CORRUPT: "+err.Error(), err)
			return
		}
		if !isBusyError(err) {
			t.done <- err // 非 busy 类错误不重试（重放业务语句可能重复副作用）。
			return
		}
	}
	t.done <- storageBusy(fmt.Sprintf(
		"写事务在 %d 次指数退避重试后仍是 SQLITE_BUSY/SQLITE_LOCKED（%s）: %v", MaxBusyRetries, t.kind, last), last)
}

// execOnce 执行一次事务；闭包里的任何写都只可能来自这个协程。
func (w *writer) execOnce(t *writeTask) error {
	tx, err := w.db.BeginTx(t.ctx, nil)
	if err != nil {
		if isBusyError(err) {
			return err
		}
		return fmt.Errorf("sqlite: 开启写事务失败: %w", err)
	}
	if err := t.fn(t.ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		if isBusyError(err) {
			return err
		}
		return fmt.Errorf("sqlite: 提交写事务失败: %w", err)
	}
	return nil
}

// flushUsage 把一批合并后的 usage 落库（仍然只由写协程执行）。
func (w *writer) flushUsage(rows []*usageRow) {
	if len(rows) == 0 {
		return
	}
	ctx := context.Background()
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		w.lastErr.Store("usage begin: " + err.Error())
		w.restorePending(rows) // 不丢：放回窗口，下一轮再试。
		return
	}
	if err := insertUsageRows(ctx, tx, rows, &w.usageRows); err != nil {
		w.lastErr.Store("usage write: " + err.Error())
		_ = tx.Rollback()
		w.restorePending(rows)
		return
	}
	if err := tx.Commit(); err != nil {
		w.lastErr.Store("usage commit: " + err.Error())
		w.restorePending(rows)
	}
}

// restorePending 把落库失败的窗口放回内存（宁可合并失败也不丢统计）。
func (w *writer) restorePending(rows []*usageRow) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range rows {
		key := usageKey{threadID: r.threadID, provider: r.provider, model: r.model}
		if cur, ok := w.pending[key]; ok {
			cur.inputTokens += r.inputTokens
			cur.outputTokens += r.outputTokens
			cur.cacheTokens += r.cacheTokens
			cur.costMicros += r.costMicros
			cur.count += r.count
			if r.lastAt > cur.lastAt {
				cur.lastAt = r.lastAt
			}
			continue
		}
		w.pending[key] = r
	}
}

// close 关闭写侧：拒绝新提交 → 等写协程排空队列并落 usage → 返回。
//
// ctx 超时只影响「等多久」：写协程仍会把已入队的任务跑完，超时后 Close 返回错误，
// 由调用方决定是拉长预算还是升级诊断。
func (w *writer) close(ctx context.Context) error {
	w.mu.Lock()
	w.closed = true
	w.cond.Broadcast()
	w.mu.Unlock()

	select {
	case <-w.doneW:
		return nil
	case <-ctx.Done():
		w.mu.Lock()
		left := len(w.queue)
		w.mu.Unlock()
		return storageBusy(fmt.Sprintf(
			"等待写队列排空超时（剩余 %d 个任务）：已提交的写不会丢，请加大 CloseTimeout", left), ctx.Err())
	}
}

// insertUsageRows 把合并后的 usage 写入 usage 表（tx 必须是写协程持有的事务）。
func insertUsageRows(ctx context.Context, tx *sql.Tx, rows []*usageRow, counter *atomic.Uint64) error {
	for _, r := range rows {
		res, err := tx.ExecContext(ctx, `
INSERT INTO usage(thread_id, provider, model, input_tokens, output_tokens, cache_tokens, cost_micros, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			r.threadID, r.provider, r.model, r.inputTokens, r.outputTokens, r.cacheTokens, r.costMicros, r.lastAt)
		if err != nil {
			return fmt.Errorf("sqlite: 写 usage 失败: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && counter != nil {
			counter.Add(uint64(n))
		}
	}
	return nil
}
