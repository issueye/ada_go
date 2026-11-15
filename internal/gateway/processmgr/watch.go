package processmgr

import "time"

// watch 监视一个进程退出：更新状态、关闭 exitCh、按策略退避重启。
func (m *Manager) watch(e *entry, proc Process, gen uint64, stdout, stderr *logSink) {
	err := proc.Wait()
	if stdout != nil {
		stdout.flush()
	}
	if stderr != nil {
		stderr.flush()
	}

	m.mu.Lock()
	if e.gen != gen {
		// 这一代已经被停止/注销；不要覆盖新一代的状态。
		m.mu.Unlock()
		return
	}
	e.proc = nil
	e.pid = 0
	e.running = false
	e.stoppedAt = m.now()
	if err != nil {
		e.lastErr = err.Error()
	} else {
		e.lastErr = ""
	}
	if e.exitCh != nil {
		close(e.exitCh)
		e.exitCh = nil
	}
	stopping := e.stopping
	e.stopping = false
	byUser := e.stoppedByUser
	m.mu.Unlock()

	m.logf("processmgr: 退出 %s pid=%d err=%v", e.keyID(), proc.PID(), err)
	if stopping || byUser {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || e.refs == 0 {
		return
	}
	policy := e.spec.Restart
	failed := err != nil
	if policy == RestartNo || (policy == RestartOnFailure && !failed) {
		return
	}
	// 稳定运行一段时间后重置退避，避免「偶发崩溃」把延迟永久推高。
	if runFor := m.now().Sub(e.startedAt); runFor >= m.opts.RestartReset {
		e.restarts = 0
	}
	e.restarts++
	e.restartDelay = m.backoff(e.restarts)
	delay := e.restartDelay
	timer := time.AfterFunc(delay, func() {
		m.restartEntry(e, gen)
	})
	e.restartTimer = timer
	m.logf("processmgr: %s 将在 %s 后重启（第 %d 次）", e.keyID(), delay, e.restarts)
}

// backoff 计算第 n 次连续重启的延迟（指数退避，上限 RestartMax）。
func (m *Manager) backoff(n int) time.Duration {
	delay := m.opts.RestartBase
	for i := 1; i < n; i++ {
		if delay >= m.opts.RestartMax {
			return m.opts.RestartMax
		}
		delay *= 2
	}
	if delay > m.opts.RestartMax {
		delay = m.opts.RestartMax
	}
	return delay
}

// restartEntry 由退避计时器触发；同一次退出只有一个计时器。
func (m *Manager) restartEntry(e *entry, gen uint64) {
	m.mu.Lock()
	if m.closed || e.gen != gen || e.stopping || e.stoppedByUser {
		m.mu.Unlock()
		return
	}
	if _, ok := m.entries[e.key]; !ok {
		m.mu.Unlock()
		return
	}
	e.restartTimer = nil
	m.mu.Unlock()
	if err := m.ensureStarted(e); err != nil {
		m.logf("processmgr: 重启 %s 失败: %v", e.keyID(), err)
	}
}
