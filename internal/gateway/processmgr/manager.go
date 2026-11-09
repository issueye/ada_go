// manager.go 是 Gateway 进程管理器的注册表与生命周期（设计 5.3、计划 P3）。
//
// 边界：只做注册、启动、停止、重启、引用计数、限额与日志；不解释 cron、
// 任务或工作区业务（那些归插件与 Dispatcher）。
//
// 并发模型：Manager.mu 保护注册表与每个 entry 的状态；spawn/停止/等待在锁外
// 进行。同一 entry 的启动是单飞的：并发的 Ensure 只拉起一次，其余等待
// startDone 后复用结果。
package processmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options 是 Manager 的构造参数；零值字段取默认值。
type Options struct {
	// Spawn 拉起进程；nil 时用 exec.CommandContext 的生产实现（不经过 shell）。
	Spawn SpawnFunc
	// NodeCommand 是 RuntimeNode 的启动程序；空时用 "node"。
	NodeCommand string
	// MaxPerOwner / MaxGlobal 是注册项上限；<=0 取默认值。
	MaxPerOwner int
	MaxGlobal   int
	// StartTimeout 是单次 spawn 的等待上限；<=0 取 DefaultStartTimeout。
	StartTimeout time.Duration
	// RestartBase / RestartMax / RestartReset 控制退避重启；<=0 取默认值。
	RestartBase  time.Duration
	RestartMax   time.Duration
	RestartReset time.Duration
	// LogLines / LogLineBytes 是每进程日志环的行数与单行字节上限。
	LogLines     int
	LogLineBytes int
	// Logf 记录注册/启动/退出/停止（nil 时静默）。
	Logf func(format string, args ...any)
	// Now 是时钟注入点；nil 时用 time.Now。
	Now func() time.Time
	// Control 给被管进程签发发送凭据（cron scheduler 之类要投递消息的进程）。
	// nil 表示本部署不提供（CLI 直连、无网关）：被管进程拿不到控制令牌。
	Control ControlIssuer
}

// Manager 是网关名下的进程注册表。
type Manager struct {
	opts  Options
	spawn SpawnFunc

	baseCtx context.Context
	cancel  context.CancelFunc

	mu      sync.Mutex
	entries map[string]*entry
	closed  bool
	wg      sync.WaitGroup

	shutdownOnce sync.Once
	shutdownErr  error
}

// entry 是一个进程的注册状态。所有字段由 Manager.mu 保护。
type entry struct {
	key  string
	spec ProcessSpec

	refs int

	proc    Process
	pid     int
	running bool

	// starting 表示一次 spawn 正在进行；startDone 在结束时关闭（成功或失败）。
	starting  bool
	startDone chan struct{}
	startErr  error

	// gen 在每次启动时递增；退出监视只处理自己那一代。
	gen uint64
	// exitCh 由监视 goroutine 在 Wait 返回后关闭；用于停止时等待真实退出。
	exitCh chan struct{}

	startedAt time.Time
	stoppedAt time.Time
	lastErr   string

	restarts     int
	restartDelay time.Duration
	restartTimer *time.Timer

	// stopping 表示主动停止，退出监视不应把它记成异常或自动重启。
	stopping bool
	// stoppedByUser 表示调用方显式 Stop；Ensure 会清除它。
	stoppedByUser bool

	logs    *logRing
	outSink *logSink
	errSink *logSink
}

// keyID 是 owner/name 形式的展示 id。
func (e *entry) keyID() string {
	if e == nil {
		return ""
	}
	return e.spec.Owner + "/" + e.spec.Name
}

// New 创建进程管理器。缺件直接失败（INV-2），不做隐式兜底。
func New(opts Options) (*Manager, error) {
	if opts.Spawn == nil {
		opts.Spawn = execSpawn
	}
	if strings.TrimSpace(opts.NodeCommand) == "" {
		opts.NodeCommand = "node"
	}
	if opts.MaxPerOwner <= 0 {
		opts.MaxPerOwner = DefaultMaxPerOwner
	}
	if opts.MaxGlobal <= 0 {
		opts.MaxGlobal = DefaultMaxGlobal
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = DefaultStartTimeout
	}
	if opts.RestartBase <= 0 {
		opts.RestartBase = DefaultRestartBase
	}
	if opts.RestartMax <= 0 {
		opts.RestartMax = DefaultRestartMax
	}
	if opts.RestartMax < opts.RestartBase {
		opts.RestartMax = opts.RestartBase
	}
	if opts.RestartReset <= 0 {
		opts.RestartReset = DefaultRestartResetRun
	}
	if opts.LogLines <= 0 {
		opts.LogLines = DefaultLogLines
	}
	if opts.LogLineBytes <= 0 {
		opts.LogLineBytes = DefaultLogLineBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		opts: opts, spawn: opts.Spawn, baseCtx: ctx, cancel: cancel,
		entries: map[string]*entry{},
	}, nil
}

func (m *Manager) now() time.Time {
	if m == nil || m.opts.Now == nil {
		return time.Now()
	}
	return m.opts.Now()
}

func (m *Manager) logf(format string, args ...any) {
	if m == nil || m.opts.Logf == nil {
		return
	}
	m.opts.Logf(format, args...)
}

// Register 登记一条进程并增加一次引用。
//
//   - 同一个 key（owner/name/scope）重复注册时只增加引用计数；spec 不一致返回
//     process_exists（避免同名进程被两个调用方以不同形态启动）；
//   - on_enable 在首次登记后立即启动；lazy 只登记；
//   - owner 由本方法盖章，spec.Owner 被覆盖。
//
// 启动失败时登记仍然保留（调用方可修好环境后 Ensure 重试），错误以
// process_start_failed 返回。
func (m *Manager) Register(owner string, spec ProcessSpec) (Ref, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Ref{}, newError(CodeInvalidSpec, "注册进程缺少 owner（贡献者 id 由宿主盖章）")
	}
	normalized, err := normalizeSpec(owner, spec)
	if err != nil {
		return Ref{}, err
	}
	ref := Ref{Owner: normalized.Owner, Name: normalized.Name, Workspace: normalized.Workspace}
	key := refKey(ref)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Ref{}, newError(CodeStartFailed, "进程管理器已关闭")
	}
	if e, ok := m.entries[key]; ok {
		if !reflect.DeepEqual(e.spec, normalized) {
			m.mu.Unlock()
			return Ref{}, newError(CodeExists,
				"进程 %s 已以不同形态注册（现有 scope=%s runtime=%s entry=%s）",
				ref.ID(), e.spec.Scope, e.spec.Runtime, e.spec.Entry)
		}
		e.refs++
		refs := e.refs
		// 正在启动时也走 ensureStarted：它会等这一代落定，避免并发注册者
		// 在启动失败时拿到「已经登记但没运行」的假成功。
		start := e.spec.StartMode == StartOnEnable && !e.running
		m.mu.Unlock()
		m.logf("processmgr: 注册 %s refs=%d（已存在）", ref.ID(), refs)
		if start {
			if startErr := m.ensureStarted(e); startErr != nil {
				return ref, startErr
			}
		}
		return ref, nil
	}
	if countOwner(m.entries, owner) >= m.opts.MaxPerOwner {
		m.mu.Unlock()
		return Ref{}, newError(CodeLimit,
			"插件 %s 的进程数达到上限 %d", owner, m.opts.MaxPerOwner)
	}
	if len(m.entries) >= m.opts.MaxGlobal {
		m.mu.Unlock()
		return Ref{}, newError(CodeLimit, "全局进程数达到上限 %d", m.opts.MaxGlobal)
	}
	e := &entry{
		key: key, spec: normalized, refs: 1,
		logs: newLogRing(m.opts.LogLines, m.opts.LogLineBytes),
	}
	m.entries[key] = e
	start := normalized.StartMode == StartOnEnable
	m.mu.Unlock()

	m.logf("processmgr: 注册 %s scope=%s start=%s", ref.ID(), normalized.Scope, normalized.StartMode)
	if start {
		if startErr := m.ensureStarted(e); startErr != nil {
			return ref, startErr
		}
	}
	return ref, nil
}

// Ensure 确保进程正在运行；lazy 进程在这里启动，崩溃后也在这里重试。
func (m *Manager) Ensure(ref Ref) (Status, error) {
	e, err := m.lookup(ref)
	if err != nil {
		return Status{}, err
	}
	if err := m.ensureStarted(e); err != nil {
		return m.statusOfEntry(e), err
	}
	return m.statusOfEntry(e), nil
}

// Stop 停止进程但保留登记与引用；之后的 Ensure 会重新启动。
func (m *Manager) Stop(ref Ref) error {
	e, err := m.lookup(ref)
	if err != nil {
		return err
	}
	m.mu.Lock()
	e.stoppedByUser = true
	timeout := e.spec.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	m.mu.Unlock()

	proc, exitCh := m.beginStop(e)
	stopErr := m.stopProc(proc, exitCh, timeout)
	// 无论是否等到干净退出，令牌都必须回收：进程可能已经半死。
	m.revokeControl(e.spec)
	if stopErr != nil {
		return stopErr
	}
	m.logf("processmgr: 停止 %s（保留登记）", e.keyID())
	return nil
}

// Unregister 释放一次引用；最后一个引用释放时停止并删除登记。
func (m *Manager) Unregister(ref Ref) error {
	key := refKey(ref)
	m.mu.Lock()
	e, ok := m.entries[key]
	if !ok {
		m.mu.Unlock()
		return newError(CodeNotFound, "没有找到进程 %s", ref.ID())
	}
	if e.refs > 1 {
		e.refs--
		refs := e.refs
		m.mu.Unlock()
		m.logf("processmgr: 注销 %s refs=%d", ref.ID(), refs)
		return nil
	}
	// 最后一个引用：先摘掉登记，避免并发 Register 再把已停止的条目当成可用。
	e.refs = 0
	delete(m.entries, key)
	timeout := e.spec.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	m.mu.Unlock()

	proc, exitCh := m.beginStop(e)
	err := m.stopProc(proc, exitCh, timeout)
	m.revokeControl(e.spec)
	if err != nil {
		return err
	}
	m.logf("processmgr: 注销 %s（已停止）", e.keyID())
	return nil
}

// UnregisterAll 清掉某个 owner 在某工作区的全部登记与引用（Core 断连/插件禁用清理）。
//
// 与 Unregister 不同：这里不管引用计数，条目直接删除（调用方已经不再持有它）。
// workspace 为空只匹配 global 进程；返回实际清理的条目数。
func (m *Manager) UnregisterAll(owner, workspace string) int {
	owner = strings.TrimSpace(owner)
	ws := strings.TrimSpace(workspace)
	if ws != "" {
		if abs, err := filepath.Abs(ws); err == nil {
			ws = abs
		}
	}
	m.mu.Lock()
	victims := make([]*entry, 0, len(m.entries))
	for key, e := range m.entries {
		if e.spec.Owner != owner || e.spec.Workspace != ws {
			continue
		}
		delete(m.entries, key)
		e.refs = 0
		victims = append(victims, e)
	}
	m.mu.Unlock()

	n := 0
	for _, e := range victims {
		if err := m.stopEntryForRemoval(e); err != nil {
			m.logf("processmgr: 清理 %s 失败: %v", e.keyID(), err)
			continue
		}
		n++
	}
	return n
}

// stopEntryForRemoval 停止一个已经从注册表摘除的条目；错误只记日志不影响其他条目。
func (m *Manager) stopEntryForRemoval(e *entry) error {
	m.mu.Lock()
	timeout := e.spec.StopTimeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	m.mu.Unlock()
	proc, exitCh := m.beginStop(e)
	err := m.stopProc(proc, exitCh, timeout)
	m.revokeControl(e.spec)
	if err != nil {
		return err
	}
	m.logf("processmgr: 清理 %s（已停止）", e.keyID())
	return nil
}

// Status 返回单个进程的快照。
func (m *Manager) Status(ref Ref) (Status, bool) {
	e, err := m.lookup(ref)
	if err != nil {
		return Status{}, false
	}
	return m.statusOfEntry(e), true
}

// statusOfEntry 在持锁状态下生成快照，避免读到半更新的状态。
func (m *Manager) statusOfEntry(e *entry) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return statusOf(e)
}

// List 返回全部登记（按 ID 排序，供诊断与测试）。
func (m *Manager) List() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, statusOf(e))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Workspace < out[j].Workspace
	})
	return out
}

// Logs 返回进程最近 limit 行日志（limit<=0 表示全部保留行）。
func (m *Manager) Logs(ref Ref, limit int) ([]LogLine, error) {
	e, err := m.lookup(ref)
	if err != nil {
		return nil, err
	}
	return e.logs.snapshot(limit), nil
}

// Shutdown 停止全部进程并回收；之后 Register/Ensure 返回已关闭。
//
// ctx 只约束等待时间：进程已被 Kill，等待的是退出监视 goroutine。
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.shutdownOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		entries := make([]*entry, 0, len(m.entries))
		for key, e := range m.entries {
			if e.restartTimer != nil {
				e.restartTimer.Stop()
				e.restartTimer = nil
			}
			e.stopping = true
			e.refs = 0
			entries = append(entries, e)
			delete(m.entries, key)
		}
		m.mu.Unlock()
		m.cancel()

		for _, e := range entries {
			m.mu.Lock()
			proc, exitCh := e.proc, e.exitCh
			timeout := e.spec.StopTimeout
			if timeout <= 0 {
				timeout = DefaultStopTimeout
			}
			m.mu.Unlock()
			if err := m.stopProc(proc, exitCh, timeout); err != nil {
				m.shutdownErr = err
			}
			// 令牌随实例回收：关闭后旧进程手里的凭据不能继续可用。
			m.revokeControl(e.spec)
		}

		done := make(chan struct{})
		go func() {
			m.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			if m.shutdownErr == nil {
				m.shutdownErr = ctx.Err()
			}
		}
	})
	return m.shutdownErr
}

// lookup 按 Ref 查登记；workspace 会先归一化。
func (m *Manager) lookup(ref Ref) (*entry, error) {
	key := refKey(ref)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		// 已关闭时用与 Register 相同的错误码：调用方按「管理器不可用」分支，
		// 而不是把「关掉之后查不到」误判成「名字写错了」。
		return nil, newError(CodeStartFailed, "进程管理器已关闭")
	}
	e, ok := m.entries[key]
	if !ok {
		return nil, newError(CodeNotFound, "没有找到进程 %s", ref.ID())
	}
	return e, nil
}

// ensureStarted 单飞启动 entry。可以在任意 goroutine 上调用。
func (m *Manager) ensureStarted(e *entry) error {
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return newError(CodeStartFailed, "进程管理器已关闭")
		}
		if _, ok := m.entries[e.key]; !ok {
			m.mu.Unlock()
			return newError(CodeNotFound, "进程 %s 已注销", e.keyID())
		}
		if e.running {
			m.mu.Unlock()
			return nil
		}
		if e.starting {
			ch := e.startDone
			m.mu.Unlock()
			<-ch
			m.mu.Lock()
			// 等待者不重跑 spawn，但必须如实回报这一代的结果：启动失败时
			// 返回错误，而不是「没在跑但也没错」。
			running, startErr := e.running, e.startErr
			m.mu.Unlock()
			if running {
				return nil
			}
			if startErr != nil {
				return startErr
			}
			return nil
		}

		// 真正启动：先占位，锁外 spawn。
		e.starting = true
		e.startDone = make(chan struct{})
		e.startErr = nil
		e.stoppedByUser = false
		e.stopping = false
		if e.restartTimer != nil {
			e.restartTimer.Stop()
			e.restartTimer = nil
		}
		e.gen++
		gen := e.gen
		e.outSink = newLogSink(e.logs, "stdout")
		e.errSink = newLogSink(e.logs, "stderr")
		spec := e.spec
		outSink, errSink := e.outSink, e.errSink
		m.mu.Unlock()

		startSpec, resolveErr := resolveStartSpec(spec, m.opts.NodeCommand)
		if resolveErr == nil {
			startSpec.Env = m.withControlEnv(spec, startSpec.Env)
		}
		var proc Process
		if resolveErr == nil {
			proc, resolveErr = m.spawnWithTimeout(startSpec, outSink, errSink)
		}

		m.mu.Lock()
		e.starting = false
		if resolveErr != nil {
			startErr := wrapError(CodeStartFailed, resolveErr, "启动进程 %s 失败", e.keyID())
			e.startErr = startErr
			e.lastErr = startErr.Error()
			close(e.startDone)
			m.mu.Unlock()
			m.logf("processmgr: 启动 %s 失败: %v", e.keyID(), startErr)
			return startErr
		}
		// 启动期间被停止/注销：这次 spawn 的结果没有归属，直接杀掉。
		if e.gen != gen || e.stopping || m.closed {
			close(e.startDone)
			m.mu.Unlock()
			_ = proc.Kill()
			m.logf("processmgr: %s 在启动过程中被停止", e.keyID())
			return newError(CodeStartFailed, "进程 %s 在启动过程中被停止", e.keyID())
		}
		e.proc = proc
		e.pid = proc.PID()
		e.running = true
		e.startedAt = m.now()
		e.stoppedAt = time.Time{}
		e.lastErr = ""
		e.exitCh = make(chan struct{})
		close(e.startDone)
		// 退出监视必须在持锁期间登记进 WaitGroup：否则 Shutdown 的 wg.Wait
		// 可能先看到零计数返回，随后这个 goroutine 才被 Add 进来——Add 在
		// Wait 之后是未定义行为，退出监视也会漏回收。
		m.wg.Add(1)
		m.mu.Unlock()

		go func() {
			defer m.wg.Done()
			m.watch(e, proc, gen, outSink, errSink)
		}()
		m.logf("processmgr: 启动 %s pid=%d", e.keyID(), proc.PID())
		return nil
	}
}

// spawnWithTimeout 在 StartTimeout 内拿到进程；超时后杀掉落单的进程。
func (m *Manager) spawnWithTimeout(spec StartSpec, stdout, stderr *logSink) (Process, error) {
	type result struct {
		proc Process
		err  error
	}
	ctx, cancel := context.WithTimeout(m.baseCtx, m.opts.StartTimeout)
	defer cancel()
	ch := make(chan result, 1)
	go func() {
		p, err := m.spawn(ctx, spec, stdout, stderr)
		ch <- result{proc: p, err: err}
	}()
	select {
	case r := <-ch:
		return r.proc, r.err
	case <-ctx.Done():
		// spawn 可能稍后才返回：等它落地再杀掉，避免留下无人认领的进程。
		go func() {
			r := <-ch
			if r.proc != nil {
				_ = r.proc.Kill()
			}
		}()
		return nil, fmt.Errorf("等待启动超过 %s", m.opts.StartTimeout)
	}
}

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

// normalizeSpec 归一化并校验注册形态。
func normalizeSpec(owner string, spec ProcessSpec) (ProcessSpec, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程缺少 name")
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程名 %q 不能包含路径分隔符", name)
	}
	entry := strings.TrimSpace(spec.Entry)
	if entry == "" {
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 缺少 entry", name)
	}
	out := spec
	out.Owner = owner
	out.Name = name
	out.Entry = entry

	scope := out.Scope
	if scope == "" {
		scope = ScopeGlobal
	}
	switch scope {
	case ScopeGlobal, ScopeWorkspace:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 scope %q 未知", name, out.Scope)
	}
	out.Scope = scope

	workspace := strings.TrimSpace(out.Workspace)
	if scope == ScopeGlobal {
		if workspace != "" {
			return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 是 global scope，不能指定 workspace", name)
		}
	} else {
		if workspace == "" {
			return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 是 workspace scope，缺少 workspace", name)
		}
		abs, err := filepath.Abs(workspace)
		if err != nil {
			return ProcessSpec{}, wrapError(CodeInvalidSpec, err, "进程 %s 的 workspace %q 非法", name, workspace)
		}
		info, statErr := os.Stat(abs)
		if statErr != nil || !info.IsDir() {
			return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 workspace %q 不是可访问目录", name, abs)
		}
		out.Workspace = abs
	}

	rt := out.Runtime
	if rt == "" {
		rt = RuntimeBinary
	}
	switch rt {
	case RuntimeBinary, RuntimeNode:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 runtime %q 未知（只有 binary/node）", name, rt)
	}
	out.Runtime = rt

	mode := out.StartMode
	if mode == "" {
		mode = StartLazy
	}
	switch mode {
	case StartLazy, StartOnEnable:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 startMode %q 未知", name, out.StartMode)
	}
	out.StartMode = mode

	restart := out.Restart
	if restart == "" {
		restart = RestartNo
	}
	switch restart {
	case RestartNo, RestartOnFailure, RestartAlways:
	default:
		return ProcessSpec{}, newError(CodeInvalidSpec, "进程 %s 的 restart %q 未知", name, out.Restart)
	}
	out.Restart = restart
	if out.StopTimeout <= 0 {
		out.StopTimeout = DefaultStopTimeout
	}
	if len(out.Args) == 0 {
		out.Args = nil
	}
	if len(out.Env) == 0 {
		out.Env = nil
	}
	return out, nil
}

// refKey 是注册表的键。workspace 先归一化，保证同一目录的不同写法命中同一条目。
func refKey(ref Ref) string {
	owner := strings.TrimSpace(ref.Owner)
	name := strings.TrimSpace(ref.Name)
	workspace := strings.TrimSpace(ref.Workspace)
	if workspace != "" {
		if abs, err := filepath.Abs(workspace); err == nil {
			workspace = abs
		}
	}
	return owner + "\x1f" + name + "\x1f" + workspace
}

// countOwner 统计某个 owner 已登记的条目数（调用方持锁）。
func countOwner(entries map[string]*entry, owner string) int {
	n := 0
	for _, e := range entries {
		if e.spec.Owner == owner {
			n++
		}
	}
	return n
}
