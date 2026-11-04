package browser

import (
	"context"
	"sync"
	"time"
)

// Event is one observable browser/control change. Agents use the /v1/events
// long poll to wait for a new tab, a download or a human takeover instead of
// polling status/snapshot in a loop.
type Event struct {
	Seq     uint64    `json:"seq"`
	TS      time.Time `json:"ts"`
	Kind    string    `json:"kind"`
	Tab     string    `json:"tab,omitempty"`
	Message string    `json:"message,omitempty"`
}

const maxEvents = 1000

// eventFields are embedded in Manager so tests can construct a zero Manager and
// still use Events/emit.
type eventFields struct {
	eventMu  sync.Mutex
	events   []Event
	eventSeq uint64
	eventCh  chan struct{}
}

func (m *Manager) ensureEventChLocked() {
	if m.eventCh == nil {
		m.eventCh = make(chan struct{})
	}
}

// emit appends one event and wakes every long poll. It is safe from event
// callbacks (it never blocks on the UI thread).
func (m *Manager) emit(kind, tab, message string) {
	m.eventMu.Lock()
	m.ensureEventChLocked()
	m.eventSeq++
	m.events = append(m.events, Event{
		Seq: m.eventSeq, TS: time.Now(), Kind: kind, Tab: tab, Message: message,
	})
	if len(m.events) > maxEvents {
		m.events = append([]Event(nil), m.events[len(m.events)-maxEvents:]...)
	}
	close(m.eventCh)
	m.eventCh = make(chan struct{})
	m.eventMu.Unlock()
}

// EventCursor returns the latest event sequence.
func (m *Manager) EventCursor() uint64 {
	m.eventMu.Lock()
	defer m.eventMu.Unlock()
	return m.eventSeq
}

// Events returns events after the cursor, waiting up to timeout for at least
// one matching event. A timeout returns an empty slice and the advanced cursor,
// which is not an error (same convention as console/network wait).
func (m *Manager) Events(ctx context.Context, after uint64, timeout time.Duration, kinds map[string]bool) ([]Event, uint64) {
	if timeout < 0 {
		timeout = 0
	}
	deadline := time.Now().Add(timeout)
	for {
		m.eventMu.Lock()
		m.ensureEventChLocked()
		latest := m.eventSeq
		var out []Event
		for _, e := range m.events {
			if e.Seq <= after {
				continue
			}
			if kinds != nil && !kinds[e.Kind] {
				continue
			}
			out = append(out, e)
		}
		ch := m.eventCh
		m.eventMu.Unlock()

		if len(out) > 0 {
			return out, latest
		}
		// Filtered-out events must not make the next poll scan them again.
		if after < latest {
			after = latest
		}
		if timeout == 0 || !time.Now().Before(deadline) {
			return nil, after
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil, after
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, after
		case <-ch:
			timer.Stop()
		case <-timer.C:
			return nil, after
		}
	}
}
