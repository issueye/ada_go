package processmgr

import "time"

// beginStop 标记主动停止并返回当前进程与退出通道。
//
// 若一次 spawn 正在进行，先等它落定（启动方看到 stopping 会自行杀掉结果）。
func (m *Manager) beginStop(e *entry) (Process, chan struct{}) {
	for {
		m.mu.Lock()
		e.stopping = true
		if e.restartTimer != nil {
			e.restartTimer.Stop()
			e.restartTimer = nil
		}
		if e.starting {
			ch := e.startDone
			m.mu.Unlock()
			<-ch
			continue
		}
		proc, exitCh := e.proc, e.exitCh
		m.mu.Unlock()
		return proc, exitCh
	}
}

// withControlEnv 给这次启动注入控制凭据，并在进程停止时回收。
//
// 每次启动都重新签发：崩溃重启后旧令牌不该继续可用；同 key 重签会作废旧令牌，
// 令牌表也不会随重启次数增长。
func (m *Manager) withControlEnv(spec ProcessSpec, env []string) []string {
	if m.opts.Control == nil {
		return env
	}
	owner, name, workspace := controlKeyOf(spec)
	url, token := m.opts.Control.IssueForProcess(owner, name, workspace)
	if url == "" || token == "" {
		return env
	}
	return injectControlEnv(env, url, token)
}

// revokeControl 回收进程的控制令牌（停止/注销/关闭时调用）。
func (m *Manager) revokeControl(spec ProcessSpec) {
	if m.opts.Control == nil {
		return
	}
	owner, name, workspace := controlKeyOf(spec)
	m.opts.Control.RevokeProcess(owner, name, workspace)
}

// stopProc 杀整棵进程树并等待监视 goroutine 确认退出。
func (m *Manager) stopProc(proc Process, exitCh chan struct{}, timeout time.Duration) error {
	if proc == nil {
		return nil
	}
	_ = proc.Kill()
	if exitCh == nil {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-exitCh:
		return nil
	case <-timer.C:
		// 再补一刀；仍然等不到就如实报超时，让调用方知道可能有残留。
		_ = proc.Kill()
		return newError(CodeStopFailed, "等待进程退出超过 %s", timeout)
	}
}
