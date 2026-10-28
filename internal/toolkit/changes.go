package toolkit

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// ChangeQueue 提供两件事（设计 1.5.1「工具机制」、02 §5.4）：
//
//  1. 同一路径的写串行：多个写工具（或并发批次里的写）落在同一路径时排队执行，
//     避免互相覆盖；不同路径互不阻塞。
//  2. 检查点登记：Descriptor.Rollback=true 的工具在执行前把涉及文件纳入检查点，
//     回滚接口如实返回「恢复 / 删除 / 跳过 / 失效」四份清单，不谎报成功。
//
// 实际的字节读写由宿主 fs 能力（M1-C）与文件工具（M1-F）完成，它们都通过 Serialize
// 包住自己的写入；本类型只管顺序与账本，不碰文件系统。
type ChangeQueue struct {
	mu       sync.Mutex
	locks    map[string]*sync.Mutex
	checkpts map[string]*Checkpoint
	order    []string
	maxOpen  int
	now      func() time.Time
}

// Checkpoint 是一次可回滚执行留下的账本。
type Checkpoint struct {
	ID        string    `json:"checkpointId"`
	ThreadID  string    `json:"threadId"`
	RunID     string    `json:"runId,omitempty"`
	CallID    string    `json:"callId"`
	Tool      string    `json:"tool"`
	Paths     []string  `json:"paths"`
	CreatedAt time.Time `json:"createdAt"`
	// 四份清单由回滚执行方（M5）填充；这里只提供容器与幂等追加。
	Restored    []string `json:"restored,omitempty"`
	Deleted     []string `json:"deleted,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	Invalidated []string `json:"invalidated,omitempty"`
}

// CheckpointMeta 是一笔检查点的归属（M5）：Executor 在工具执行前已知，
// 经 ctx 传给 Invoker，最终落到 host.Context（fs 写入回调要用它找检查点）。
type CheckpointMeta struct {
	ThreadID string
	RunID    string
	CallID   string
	Tool     string
}

// metaCtxKey 携带本笔工具调用的检查点归属（CheckpointMeta）。
type metaCtxKey struct{}

// withMeta 把检查点归属放进 ctx（Executor 在调用 Invoker 前注入）。
func withMeta(ctx context.Context, meta CheckpointMeta) context.Context {
	return context.WithValue(ctx, metaCtxKey{}, meta)
}

// MetaFrom 取出本笔工具调用的检查点归属；非工具调用路径返回 false。
// app 的 Invoker 用它填充 host.Context（ThreadID/RunID/CallID），
// 宿主 fs 写入回调据此把被覆盖文件并入对应的持久化检查点。
func MetaFrom(ctx context.Context) (CheckpointMeta, bool) {
	if ctx == nil {
		return CheckpointMeta{}, false
	}
	meta, ok := ctx.Value(metaCtxKey{}).(CheckpointMeta)
	return meta, ok
}

// NewChangeQueue 创建变更队列。maxOpen 限制同时驻留的检查点数量（0 表示 256）。
func NewChangeQueue() *ChangeQueue {
	return &ChangeQueue{
		locks:    map[string]*sync.Mutex{},
		checkpts: map[string]*Checkpoint{},
		maxOpen:  256,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Serialize 对同一组路径串行执行 fn。
//
// 路径按字典序加锁，避免两笔写以相反顺序取锁造成死锁。
func (q *ChangeQueue) Serialize(ctx context.Context, paths []string, fn func(context.Context) error) error {
	if len(paths) == 0 {
		return fn(ctx)
	}
	clean := append([]string(nil), paths...)
	sort.Strings(clean)
	unlock := func() {}
	for _, p := range clean {
		l := q.lockFor(p)
		l.Lock()
		prev := unlock
		unlock = func() { prev(); l.Unlock() }
	}
	defer unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx)
}

func (q *ChangeQueue) lockFor(path string) *sync.Mutex {
	q.mu.Lock()
	defer q.mu.Unlock()
	l, ok := q.locks[path]
	if !ok {
		l = &sync.Mutex{}
		q.locks[path] = l
		q.order = append(q.order, path)
	}
	return l
}

// NewCheckpoint 登记一次回滚检查点，返回检查点 id。
//
// meta 带 ThreadID/RunID（Executor 从 ctx 取，Session 提供）；Tool/CallID 来自
// 描述符与调用。文件快照与恢复由 Session 侧的持久化账目（cli.changeStore）完成：
// 宿主 fs 写入前回调 Checkpoint(meta, path)，把被覆盖文件备份进数据目录 changes/。
// 内存账本只服务回执与串行；change.list 以磁盘 manifest 为真源（进程重启后仍在）。
func (q *ChangeQueue) NewCheckpoint(call domain.ToolCall, d domain.Descriptor, meta CheckpointMeta) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	id := "chk_" + call.CallID
	cp := &Checkpoint{
		ID:        id,
		ThreadID:  meta.ThreadID,
		RunID:     meta.RunID,
		CallID:    call.CallID,
		Tool:      d.Name,
		CreatedAt: q.now(),
		Paths:     nil,
	}
	q.checkpts[id] = cp
	q.trimLocked()
	return id
}

// Lookup 按检查点 id 查账本（回滚方与快照回调用它补齐归属元数据）。
func (q *ChangeQueue) Lookup(id string) (Checkpoint, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp, ok := q.checkpts[id]
	if !ok {
		return Checkpoint{}, false
	}
	return *cp, true
}

// SetCheckpointThread 在轮次 id 已知后补齐归属（Executor 先于引擎知道 runId 之前创建）。
func (q *ChangeQueue) SetCheckpointThread(id, threadID, runID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp, ok := q.checkpts[id]
	if !ok {
		return fmt.Errorf("toolkit: 检查点 %s 不存在", id)
	}
	cp.ThreadID, cp.RunID = threadID, runID
	return nil
}

// AddPaths 把受影响路径并入检查点（宿主 fs 写入时回调）。
func (q *ChangeQueue) AddPaths(checkpointID string, paths ...string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp, ok := q.checkpts[checkpointID]
	if !ok {
		return fmt.Errorf("toolkit: 检查点 %s 不存在或已过期", checkpointID)
	}
	for _, p := range paths {
		seen := false
		for _, have := range cp.Paths {
			if have == p {
				seen = true
				break
			}
		}
		if !seen {
			cp.Paths = append(cp.Paths, p)
		}
	}
	sort.Strings(cp.Paths)
	return nil
}

// List 返回线程的检查点（change.list，8.2）。
func (q *ChangeQueue) List(threadID string) []Checkpoint {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []Checkpoint{}
	for _, id := range q.orderCheckpoints(threadID) {
		out = append(out, *q.checkpts[id])
	}
	return out
}

func (q *ChangeQueue) orderCheckpoints(threadID string) []string {
	var ids []string
	for id, cp := range q.checkpts {
		if threadID == "" || cp.ThreadID == threadID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Drop 在回滚或过期后移除检查点。
func (q *ChangeQueue) Drop(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.checkpts, id)
}

func (q *ChangeQueue) trimLocked() {
	if len(q.checkpts) <= q.maxOpen {
		return
	}
	var ids []string
	for id := range q.checkpts {
		ids = append(ids, id)
	}
	sort.Strings(ids) // ULID/前缀可排序：最先创建的最先淘汰。
	for _, id := range ids {
		if len(q.checkpts) <= q.maxOpen {
			break
		}
		delete(q.checkpts, id)
	}
}
