package supervisor

import (
	"time"
)

// janitor 周期性回收空闲实例：lease 为 0 且 lastUsed 超过 IdleTimeout。
// 间隔取 IdleTimeout/2，最长 1 分钟，最短 10 毫秒（便于测试用极短超时）。
func (s *Supervisor) janitor() {
	defer close(s.janitorDone)

	interval := s.opts.IdleTimeout / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.baseCtx.Done():
			return
		case <-ticker.C:
			s.reapIdle()
		}
	}
}

// reapIdle 把过期实例从 registry 摘除，并在锁外停止它们。
func (s *Supervisor) reapIdle() {
	now := time.Now()
	var victims []*entry

	s.mu.Lock()
	for key, e := range s.entries {
		e.mu.Lock()
		last := e.lastUsed
		if last.IsZero() {
			last = e.createdAt
		}
		expired := e.leases == 0 &&
			!e.busy &&
			!e.stopping &&
			(e.client != nil || e.proc != nil) &&
			now.Sub(last) >= s.opts.IdleTimeout
		if expired {
			// 先占位，避免同一轮或并发 Acquire 再把这个条目当成可用。
			e.busy = true
		}
		e.mu.Unlock()
		if expired {
			delete(s.entries, key)
			victims = append(victims, e)
		}
	}
	s.mu.Unlock()

	for _, e := range victims {
		s.stopEntry(e, 2*time.Second)
	}
}
