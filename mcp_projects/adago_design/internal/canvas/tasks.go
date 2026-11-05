package canvas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// CaptureTask 是一次画布/节点截图请求。
type CaptureTask struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id,omitempty"` // 空 = 全画布
	Scale     float64   `json:"scale"`
	CreatedAt time.Time `json:"created_at"`
}

// CaptureResult 是 UI 回传的截图结果。
type CaptureResult struct {
	PNGBase64 string `json:"png_base64"`
	Error     string `json:"error,omitempty"`
}

// TaskQueue 管理截图任务：tool 侧创建/等待，UI 侧领取/回填。
type TaskQueue struct {
	mu      sync.Mutex
	seq     int
	pending []string                   // 待领取任务 ID（FIFO）
	tasks   map[string]*CaptureTask    // 全部任务
	results map[string]*CaptureResult  // 已回填结果
	waiters map[string][]chan struct{} // 等待结果/领取的本地唤醒
}

// NewTaskQueue 创建队列。
func NewTaskQueue() *TaskQueue {
	return &TaskQueue{
		tasks:   map[string]*CaptureTask{},
		results: map[string]*CaptureResult{},
		waiters: map[string][]chan struct{}{},
	}
}

func randomID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b[:]))
}

// Create 入队一个截图任务。
func (q *TaskQueue) Create(nodeID string, scale float64) *CaptureTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	t := &CaptureTask{
		ID:        randomID("cap"),
		NodeID:    nodeID,
		Scale:     scale,
		CreatedAt: time.Now(),
	}
	q.tasks[t.ID] = t
	q.pending = append(q.pending, t.ID)
	q.wakeLocked("claim")
	return t
}

// Claim 领取一个待处理任务；10s 内没有新任务返回 nil（长轮询）。
func (q *TaskQueue) Claim(ctx context.Context) *CaptureTask {
	q.mu.Lock()
	for {
		if n := len(q.pending); n > 0 {
			id := q.pending[0]
			q.pending = q.pending[1:]
			t := q.tasks[id]
			q.mu.Unlock()
			return t
		}
		ch := make(chan struct{}, 1)
		q.waiters["claim"] = append(q.waiters["claim"], ch)
		q.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
			return nil
		}
		q.mu.Lock()
	}
}

// Complete 回填任务结果；返回任务是否存在。
func (q *TaskQueue) Complete(id string, res CaptureResult) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.tasks[id]; !ok {
		return false
	}
	q.results[id] = &res
	q.wakeLocked(id)
	return true
}

// Wait 等待任务结果；8s 超时返回 nil。
func (q *TaskQueue) Wait(ctx context.Context, id string) *CaptureResult {
	deadline := time.Now().Add(8 * time.Second)
	q.mu.Lock()
	for {
		if r, ok := q.results[id]; ok {
			q.mu.Unlock()
			return r
		}
		if _, ok := q.tasks[id]; !ok {
			q.mu.Unlock()
			return nil
		}
		ch := make(chan struct{}, 1)
		q.waiters[id] = append(q.waiters[id], ch)
		q.mu.Unlock()
		remain := time.Until(deadline)
		if remain <= 0 {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil
		case <-time.After(remain):
			return nil
		}
		q.mu.Lock()
	}
}

// GC 清理 5 分钟前的完成任务，防泄漏。
func (q *TaskQueue) GC() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, t := range q.tasks {
		if _, done := q.results[id]; done && time.Since(t.CreatedAt) > 5*time.Minute {
			delete(q.tasks, id)
			delete(q.results, id)
		}
	}
}

// wakeLocked 唤醒等待者（调用方持锁）。
func (q *TaskQueue) wakeLocked(key string) {
	for _, ch := range q.waiters[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	delete(q.waiters, key)
}
