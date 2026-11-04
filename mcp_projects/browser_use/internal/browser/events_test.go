package browser

import (
	"context"
	"testing"
	"time"
)

// TestEventsWaitAndFilter 钉住 /v1/events 与 wait for=tab/control 的游标语义：
// 新事件会唤醒等待；被过滤掉的事件必须推进游标，不能让下一次轮询重复扫描。
func TestEventsWaitAndFilter(t *testing.T) {
	m := &Manager{}
	go func() {
		time.Sleep(20 * time.Millisecond)
		m.emit("tab.created", "t1", "")
	}()

	events, cursor := m.Events(context.Background(), 0, time.Second, nil)
	if len(events) != 1 || events[0].Kind != "tab.created" || events[0].Tab != "t1" {
		t.Fatalf("events = %+v", events)
	}
	if cursor != events[0].Seq {
		t.Fatalf("cursor = %d, want %d", cursor, events[0].Seq)
	}

	m.emit("control.takeover", "", "")
	events, next := m.Events(context.Background(), cursor, 50*time.Millisecond, map[string]bool{"tab.created": true})
	if len(events) != 0 {
		t.Fatalf("filtered events = %+v, want none", events)
	}
	if next <= cursor {
		t.Fatalf("cursor did not advance past filtered events: %d -> %d", cursor, next)
	}

	// A zero timeout is a normal empty long poll, not an error.
	events, _ = m.Events(context.Background(), next, 0, nil)
	if len(events) != 0 {
		t.Fatalf("timeout poll = %+v, want empty", events)
	}
}
