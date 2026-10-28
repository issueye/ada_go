package coreproto

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
	"github.com/adag/ada-go/internal/proto"
)

// Emitter 是 ports.EventSink 的生产实现：补齐序号、缓冲可回放事件、向订阅者扇出（设计 8.1、8.3）。
//
// 序号规则：线程内 Seq 单调递增（INV-6），工作区级事件用独立 WorkspaceSeq；
// 两者都由这里分配，引擎与插件都不能改（1.5.6）。
// 缓冲：保留最近 proto.EventBuffer 条用于重连补发；溢出后只发快照，由订阅方回退到
// thread.get 全量重同步（8.3「不猜测缺失」）。
type Emitter struct {
	mu      sync.Mutex
	thread  map[string]int64
	wsSeq   int64
	buffer  []domain.Event
	maxBuf  int
	subs    map[int64]func(domain.Event)
	nextSub int64
	clock   ports.Clock
}

var _ ports.EventSink = (*Emitter)(nil)

// NewEmitter 创建事件发射器。clock 为 nil 时用系统时钟。
func NewEmitter(clock ports.Clock) *Emitter {
	if clock == nil {
		clock = systemClock{}
	}
	return &Emitter{
		thread: map[string]int64{},
		buffer: make([]domain.Event, 0, proto.EventBuffer),
		maxBuf: proto.EventBuffer,
		subs:   map[int64]func(domain.Event){},
		clock:  clock,
	}
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now().UTC() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Emit 补序号后广播。实现 ports.EventSink。
func (e *Emitter) Emit(_ context.Context, evt domain.Event) error {
	if evt.Kind == "" {
		return fmt.Errorf("coreproto: 事件缺少 kind")
	}
	if _, ok := proto.LookupEvent(string(evt.Kind)); !ok {
		// 插件不能扩展协议（1.5.2）：未声明的事件名直接拒绝。
		return fmt.Errorf("coreproto: 未声明的事件 %q", evt.Kind)
	}
	e.mu.Lock()
	meta, _ := proto.LookupEvent(string(evt.Kind))
	if evt.At.IsZero() {
		evt.At = e.clock.Now()
	}
	switch {
	case meta.Scope == "workspace":
		if evt.WorkspaceSeq == 0 {
			e.wsSeq++
			evt.WorkspaceSeq = e.wsSeq
		}
	default:
		if evt.ThreadID == "" {
			e.mu.Unlock()
			return fmt.Errorf("coreproto: 线程级事件 %s 缺少 threadId", evt.Kind)
		}
		if evt.ThreadID != "" {
			e.thread[evt.ThreadID]++
			evt.Seq = e.thread[evt.ThreadID]
		}
	}
	e.appendLocked(evt)
	subs := make([]func(domain.Event), 0, len(e.subs))
	for _, f := range e.subs {
		subs = append(subs, f)
	}
	e.mu.Unlock()

	for _, f := range subs {
		f(evt)
	}
	return nil
}

func (e *Emitter) appendLocked(evt domain.Event) {
	if len(e.buffer) >= e.maxBuf {
		// 溢出：丢弃最旧的一半，订阅端只能靠快照补齐（8.3）。
		drop := e.maxBuf / 2
		e.buffer = append(e.buffer[drop:drop:drop], e.buffer[drop:]...)
		e.buffer = append([]domain.Event(nil), e.buffer...)
	}
	e.buffer = append(e.buffer, evt)
}

// Subscribe 注册一个扇出回调；返回的取消函数必须调用以免泄漏（Gateway relay 用）。
func (e *Emitter) Subscribe(fn func(domain.Event)) func() {
	e.mu.Lock()
	id := e.nextSub
	e.nextSub++
	e.subs[id] = fn
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		delete(e.subs, id)
		e.mu.Unlock()
	}
}

// Replay 返回 fromSeq 之后（不含）该线程的事件；缺口过大时 ok=false，调用方改发快照。
func (e *Emitter) Replay(threadID string, fromSeq int64) ([]domain.Event, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.buffer) == 0 {
		return nil, true
	}
	// 缓冲里最早的线程事件序号若大于 fromSeq+1，说明已经断了。
	oldestSeen := int64(0)
	var out []domain.Event
	for _, evt := range e.buffer {
		if evt.ThreadID != threadID {
			continue
		}
		if oldestSeen == 0 || evt.Seq < oldestSeen {
			oldestSeen = evt.Seq
		}
		if evt.Seq > fromSeq {
			out = append(out, evt)
		}
	}
	if oldestSeen > 0 && fromSeq > 0 && oldestSeen > fromSeq+1 {
		return nil, false
	}
	return out, true
}

// LastSeq 返回某线程已分配到的最大序号。
func (e *Emitter) LastSeq(threadID string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.thread[threadID]
}

// Notification 把领域事件编码成 JSON-RPC notification（8.1：事件无 id）。
func Notification(evt domain.Event) (*proto.Notification, error) {
	return NewEvent(string(evt.Kind), evt)
}

// SnapshotPayload 是 evt.state.snapshot 的载荷构造 helper（8.3）。
func SnapshotPayload(s proto.Snapshot) json.RawMessage {
	raw, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
