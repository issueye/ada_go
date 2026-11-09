package processmgr

// Manager 的单元测试覆盖计划 P3 的验收项：启动/停止/崩溃重启/引用计数/
// lazy/限额/日志/回收。Spawn 一律注入假进程，不依赖真实 node 或 shell。

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProcess 是假进程：Wait 阻塞到 kill 或 exit 被调用。
type fakeProcess struct {
	pid  int
	done chan struct{}
	once sync.Once
	err  error
}

func newFakeProcess(pid int) *fakeProcess {
	return &fakeProcess{pid: pid, done: make(chan struct{})}
}

func (p *fakeProcess) PID() int { return p.pid }

func (p *fakeProcess) Wait() error {
	<-p.done
	return p.err
}

func (p *fakeProcess) Kill() error {
	p.finish(errors.New("killed"))
	return nil
}

// exit 模拟进程自行退出（不是被 Kill）。
func (p *fakeProcess) exit(err error) { p.finish(err) }

func (p *fakeProcess) finish(err error) {
	p.once.Do(func() {
		p.err = err
		close(p.done)
	})
}

// fakeSpawner 为每次 spawn 建一个假进程，并记录调用参数。
type fakeSpawner struct {
	mu    sync.Mutex
	pid   atomic.Int32
	specs []StartSpec
	procs []*fakeProcess
	fail  error
}

func (s *fakeSpawner) spawn(_ context.Context, spec StartSpec, stdout, stderr io.Writer) (Process, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	p := newFakeProcess(int(s.pid.Add(1)) + 100)
	s.mu.Lock()
	s.procs = append(s.procs, p)
	s.mu.Unlock()
	if stdout != nil {
		_, _ = io.WriteString(stdout, "hello from "+spec.Name+"\n")
	}
	if stderr != nil {
		_, _ = io.WriteString(stderr, "token=abc123\n")
	}
	return p, nil
}

func (s *fakeSpawner) last() *fakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.procs) == 0 {
		return nil
	}
	return s.procs[len(s.procs)-1]
}

func (s *fakeSpawner) spawned() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *fakeSpawner) all() []*fakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*fakeProcess(nil), s.procs...)
}

func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	m, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	return m
}

func binarySpec(name string) ProcessSpec {
	return ProcessSpec{
		Name: name, Runtime: RuntimeBinary, Entry: "/bin/sh",
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待条件超时（%s）", timeout)
}

// TestRegisterOnEnableStarts 覆盖 on_enable 首次登记即启动，并带日志与 pid。
func TestRegisterOnEnableStarts(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})

	ref, err := m.Register("plug", ProcessSpec{
		Name: "scheduler", Runtime: RuntimeNode, Entry: "dist/scheduler.js",
		StartMode: StartOnEnable, Restart: RestartNo,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if ref.ID() != "plug/scheduler" {
		t.Fatalf("Ref.ID() = %q，期望 plug/scheduler", ref.ID())
	}
	if spawn.spawned() != 1 {
		t.Fatalf("on_enable 应启动一次，实际 %d 次", spawn.spawned())
	}
	st, ok := m.Status(ref)
	if !ok || !st.Running {
		t.Fatalf("Status = %+v ok=%v，期望运行中", st, ok)
	}
	if st.PID <= 100 {
		t.Fatalf("PID = %d，期望来自假进程", st.PID)
	}

	logs, err := m.Logs(ref, 0)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(logs) < 2 {
		t.Fatalf("日志应有 stdout/stderr 两行，实际 %d 行", len(logs))
	}

	// stderr 的 token=abc123 必须被脱敏。
	for _, line := range logs {
		if strings.Contains(line.Text, "abc123") {
			t.Fatalf("日志未脱敏: %q", line.Text)
		}
	}
}

// TestLazyOnlyStartsOnEnsure 覆盖 lazy 只登记、Ensure 才启动。
func TestLazyOnlyStartsOnEnsure(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})

	ref, err := m.Register("plug", ProcessSpec{Name: "lazy", Entry: "/bin/sh"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if spawn.spawned() != 0 {
		t.Fatalf("lazy 注册不应启动，实际 %d 次", spawn.spawned())
	}
	st, err := m.Ensure(ref)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !st.Running {
		t.Fatalf("Ensure 后应运行: %+v", st)
	}
	// 再次 Ensure 是幂等的。
	if _, err := m.Ensure(ref); err != nil {
		t.Fatalf("再次 Ensure: %v", err)
	}
	if spawn.spawned() != 1 {
		t.Fatalf("Ensure 应只启动一次，实际 %d 次", spawn.spawned())
	}
}

// TestUnregisterStopsAtLastRef 覆盖引用计数：最后一个引用释放才停止。
func TestUnregisterStopsAtLastRef(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})

	// 两个调用方（不同 workspace scope 的 global 同名）只能同形态注册。
	ref1, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	ref2, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if err != nil {
		t.Fatalf("二次 Register: %v", err)
	}
	if spawn.spawned() != 1 {
		t.Fatalf("同名多引用只应启动一次，实际 %d 次", spawn.spawned())
	}
	st, _ := m.Status(ref1)
	if st.Refs != 2 {
		t.Fatalf("Refs = %d，期望 2", st.Refs)
	}

	if err := m.Unregister(ref2); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if st, _ := m.Status(ref1); !st.Running || st.Refs != 1 {
		t.Fatalf("释放一次引用后仍应运行且 Refs=1: %+v", st)
	}
	if err := m.Unregister(ref1); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if _, ok := m.Status(ref1); ok {
		t.Fatal("最后一个引用释放后不应再查到登记")
	}
}

// TestRegisterRejectsMismatchedSpec 覆盖同名不同形态注册。
func TestRegisterRejectsMismatchedSpec(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	if _, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if CodeOf(err) != CodeExists {
		t.Fatalf("重复注册不同形态应返回 %s，实际 %v", CodeExists, err)
	}
}

// TestLimits 覆盖每 owner 与全局上限。
func TestLimits(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn, MaxPerOwner: 2, MaxGlobal: 3})

	for _, name := range []string{"a", "b"} {
		if _, err := m.Register("plug", ProcessSpec{Name: name, Entry: "/bin/sh"}); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	_, err := m.Register("plug", ProcessSpec{Name: "c", Entry: "/bin/sh"})
	if CodeOf(err) != CodeLimit {
		t.Fatalf("每 owner 上限应返回 %s，实际 %v", CodeLimit, err)
	}

	// 另一个 owner 还能注册一个（全局 3），第四个全局失败。
	if _, err := m.Register("other", ProcessSpec{Name: "a", Entry: "/bin/sh"}); err != nil {
		t.Fatalf("Register other: %v", err)
	}
	_, err = m.Register("third", ProcessSpec{Name: "a", Entry: "/bin/sh"})
	if CodeOf(err) != CodeLimit {
		t.Fatalf("全局上限应返回 %s，实际 %v", CodeLimit, err)
	}
}

// TestCrashRestartOnFailure 覆盖 on_failure 退避重启。
func TestCrashRestartOnFailure(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{
		Spawn: spawn.spawn, RestartBase: 10 * time.Millisecond, RestartMax: 40 * time.Millisecond,
	})

	ref, err := m.Register("plug", ProcessSpec{
		Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable, Restart: RestartOnFailure,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	fp := spawn.last()
	fp.exit(errors.New("boom"))
	waitFor(t, 2*time.Second, func() bool { return spawn.spawned() >= 2 })
	waitFor(t, 2*time.Second, func() bool {
		st, _ := m.Status(ref)
		return st.Running && st.Restarts == 1
	})
	st, _ := m.Status(ref)
	if st.LastError != "" {
		// 重启成功后 lastError 应已清空。
		t.Fatalf("重启成功后 lastError 应清空，实际 %q", st.LastError)
	}
}

// TestRestartPolicyNoStops 覆盖 restart=no：崩溃后保持 stopped，等 Ensure 手动拉起。
func TestRestartPolicyNoStops(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})

	ref, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	spawn.last().exit(nil)
	waitFor(t, 2*time.Second, func() bool {
		st, _ := m.Status(ref)
		return !st.Running
	})
	if spawn.spawned() != 1 {
		t.Fatalf("restart=no 不应自动重启，实际启动 %d 次", spawn.spawned())
	}
	if _, err := m.Ensure(ref); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if spawn.spawned() != 2 {
		t.Fatalf("Ensure 应重新拉起，实际启动 %d 次", spawn.spawned())
	}
}

// TestRestartOnFailureSkipsCleanExit：正常退出（err=nil）不触发 on_failure 重启。
func TestRestartOnFailureSkipsCleanExit(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn, RestartBase: 10 * time.Millisecond})

	ref, err := m.Register("plug", ProcessSpec{
		Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable, Restart: RestartOnFailure,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	spawn.last().exit(nil)
	waitFor(t, 2*time.Second, func() bool {
		st, _ := m.Status(ref)
		return !st.Running
	})
	time.Sleep(50 * time.Millisecond)
	if spawn.spawned() != 1 {
		t.Fatalf("正常退出不应按 on_failure 重启，实际 %d 次", spawn.spawned())
	}
}

// TestStopKeepsRegistration：Stop 后登记与引用仍在，Ensure 可再启动。
func TestStopKeepsRegistration(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	ref, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := m.Stop(ref); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	st, ok := m.Status(ref)
	if !ok || st.Running || st.Refs != 1 {
		t.Fatalf("Stop 后应保留登记且不运行: %+v ok=%v", st, ok)
	}
	if _, err := m.Ensure(ref); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if spawn.spawned() != 2 {
		t.Fatalf("Ensure 应重新拉起，实际 %d 次", spawn.spawned())
	}
}

// TestStartFailureKeepsRegistration：启动失败仍保留登记，修好后 Ensure 可重试。
func TestStartFailureKeepsRegistration(t *testing.T) {
	spawn := &fakeSpawner{fail: errors.New("node 不存在")}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	ref, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", StartMode: StartOnEnable})
	if err != nil {
		if CodeOf(err) != CodeStartFailed {
			t.Fatalf("启动失败错误码 = %q，期望 %s（err=%v）", CodeOf(err), CodeStartFailed, err)
		}
	}
	st, ok := m.Status(ref)
	if !ok {
		t.Fatal("启动失败也应保留登记")
	}
	if st.Running {
		t.Fatalf("启动失败不应标记运行: %+v", st)
	}
	spawn.fail = nil
	if _, err := m.Ensure(ref); err != nil {
		t.Fatalf("修好后 Ensure: %v", err)
	}
}

// TestShutdownStopsAll：Shutdown 停止全部进程且之后拒绝新注册。
func TestShutdownStopsAll(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	for _, name := range []string{"a", "b"} {
		if _, err := m.Register("plug", ProcessSpec{Name: name, Entry: "/bin/sh", StartMode: StartOnEnable}); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("Shutdown 后登记应清空: %+v", m.List())
	}
	spawn.mu.Lock()
	procs := append([]*fakeProcess(nil), spawn.procs...)
	spawn.mu.Unlock()
	for i, p := range procs {
		select {
		case <-p.done:
		case <-time.After(time.Second):
			t.Fatalf("第 %d 个进程未被回收", i)
		}
	}
	if _, err := m.Register("plug", ProcessSpec{Name: "c", Entry: "/bin/sh"}); CodeOf(err) != CodeStartFailed {
		t.Fatalf("关闭后注册应被拒绝，实际 %v", err)
	}
}

// TestUnregisterAllByOwnerWorkspace：Core 断连时按 owner+workspace 清理。
func TestUnregisterAllByOwnerWorkspace(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	dir := t.TempDir()

	wsSpec := ProcessSpec{Name: "ws", Scope: ScopeWorkspace, Workspace: dir, Entry: "/bin/sh",
		StartMode: StartOnEnable}
	if _, err := m.Register("plug", wsSpec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := m.Register("plug", ProcessSpec{Name: "global", Entry: "/bin/sh"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if n := m.UnregisterAll("plug", dir); n != 1 {
		t.Fatalf("按 owner+workspace 清理数 = %d，期望 1", n)
	}
	if len(m.List()) != 1 {
		t.Fatalf("global 进程不应被清理: %+v", m.List())
	}
	// 断连清理必须真的停掉进程，且不依赖引用计数（多引用也只清一次）。
	if st := spawn.all(); len(st) != 1 {
		t.Fatalf("workspace 进程应只启动一次，实际 %d", len(st))
	}
	select {
	case <-spawn.last().done:
	case <-time.After(2 * time.Second):
		t.Fatal("UnregisterAll 后 workspace 进程仍在运行")
	}
}

// TestUnregisterAllIgnoresRefs：多引用登记的条目也被整体清掉。
func TestUnregisterAllIgnoresRefs(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	dir := t.TempDir()
	spec := ProcessSpec{Name: "ws", Scope: ScopeWorkspace, Workspace: dir, Entry: "/bin/sh",
		StartMode: StartOnEnable}
	if _, err := m.Register("plug", spec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := m.Register("plug", spec); err != nil {
		t.Fatalf("二次 Register: %v", err)
	}
	if n := m.UnregisterAll("plug", dir); n != 1 {
		t.Fatalf("清理数 = %d，期望 1（引用计数不阻止断连清理）", n)
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("清理后不应保留登记: %+v", m.List())
	}
}

// TestRegisterStampsOwner 覆盖 owner 盖章与非法形态校验。
func TestRegisterStampsOwner(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})

	if _, err := m.Register("", ProcessSpec{Name: "s", Entry: "/bin/sh"}); CodeOf(err) != CodeInvalidSpec {
		t.Fatalf("缺少 owner 应返回 %s，实际 %v", CodeInvalidSpec, err)
	}
	if _, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh", Owner: "evil"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ref, err := m.Register("plug", ProcessSpec{Name: "t", Entry: "/bin/sh", Scope: ScopeWorkspace})
	if err == nil {
		t.Fatal("workspace scope 缺少 workspace 应被拒绝")
	}
	_ = ref

	_, err = m.Register("plug", ProcessSpec{Name: "u", Entry: "/bin/sh", Runtime: "python"})
	if CodeOf(err) != CodeInvalidSpec {
		t.Fatalf("未知 runtime 应返回 %s，实际 %v", CodeInvalidSpec, err)
	}
	if CodeOf((func() error {
		_, e := m.Register("plug", ProcessSpec{Name: "v", Entry: "x", Scope: "galaxy"})
		return e
	})()) != CodeInvalidSpec {
		t.Fatalf("未知 scope 应返回 %s", CodeInvalidSpec)
	}

	// 已注册的条目 owner 必须是盖章值，而不是 spec 里自称的。
	for _, st := range m.List() {
		if st.Owner != "plug" {
			t.Fatalf("Status.Owner = %q，期望 plug", st.Owner)
		}
	}
}

// TestHostEnvIsStripped：宿主凭据不进被管进程环境。
func TestHostEnvIsStripped(t *testing.T) {
	spec := StartSpec{Env: buildEnv(map[string]string{
		"ADAG_CORE_TOKEN":             "secret",
		"ADAGO_GATEWAY_CONTROL_TOKEN": "secret2",
		"EXTRA":                       "ok",
	})}
	if got := strings.Join(spec.Env, "\n"); strings.Contains(got, "secret") {
		t.Fatalf("宿主凭据未被剥掉: %s", got)
	}
	found := false
	for _, kv := range spec.Env {
		if strings.HasPrefix(kv, "EXTRA=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("显式 env 应保留: %v", spec.Env)
	}
}

// TestNodeRuntimeResolvesEntry：node 形态把 entry 作为第一个参数，缺省 node。
func TestNodeRuntimeResolvesEntry(t *testing.T) {
	start, err := resolveStartSpec(ProcessSpec{
		Name: "s", Runtime: RuntimeNode, Entry: "dist/scheduler.js", Args: []string{"--flag"},
	}, "node-custom")
	if err != nil {
		t.Fatalf("resolveStartSpec: %v", err)
	}
	if start.Command != "node-custom" {
		t.Fatalf("Command = %q，期望 node-custom", start.Command)
	}
	if len(start.Args) != 2 || start.Args[0] != "dist/scheduler.js" || start.Args[1] != "--flag" {
		t.Fatalf("Args = %v，期望 [dist/scheduler.js --flag]", start.Args)
	}

	start, err = resolveStartSpec(ProcessSpec{Name: "b", Runtime: RuntimeBinary, Entry: "/bin/echo", Args: []string{"hi"}}, "node")
	if err != nil {
		t.Fatalf("resolveStartSpec: %v", err)
	}
	if start.Command != "/bin/echo" || len(start.Args) != 1 || start.Args[0] != "hi" {
		t.Fatalf("binary 形态 = %+v", start)
	}
}

// TestLogRingTruncates：日志按行数封顶，单行按字节截断。
func TestLogRingTruncates(t *testing.T) {
	ring := newLogRing(3, 8)
	for i := 0; i < 5; i++ {
		ring.add("stdout", "line-with-content")
	}
	lines := ring.snapshot(0)
	if len(lines) != 3 {
		t.Fatalf("日志环应保留 3 行，实际 %d", len(lines))
	}
	if ring.droppedCount() != 2 {
		t.Fatalf("dropped = %d，期望 2", ring.droppedCount())
	}
	for _, line := range lines {
		if !strings.HasSuffix(line.Text, "（截断）") {
			t.Fatalf("超长行应被截断: %q", line.Text)
		}
	}
}

// TestLogSinkFlushKeepsPartialLine：退出前没换行的最后一行也要落环形缓冲。
func TestLogSinkFlushKeepsPartialLine(t *testing.T) {
	ring := newLogRing(10, 64)
	sink := newLogSink(ring, "stdout")
	if _, err := sink.Write([]byte("no-newline")); err != nil {
		t.Fatal(err)
	}
	sink.flush()
	lines := ring.snapshot(0)
	if len(lines) != 1 || lines[0].Text != "no-newline" {
		t.Fatalf("flush 后日志 = %+v", lines)
	}
}

// TestConcurrentEnsureIsSingleFlight：并发 Ensure 只拉起一次。
func TestConcurrentEnsureIsSingleFlight(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	ref, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Ensure(ref); err != nil {
				t.Errorf("Ensure: %v", err)
			}
		}()
	}
	wg.Wait()
	if spawn.spawned() != 1 {
		t.Fatalf("并发 Ensure 应只启动一次，实际 %d 次", spawn.spawned())
	}
}

// TestStatusOfDefaults：零值 spec 的状态快照给缺省值，不返回空串。
func TestStatusOfDefaults(t *testing.T) {
	spawn := &fakeSpawner{}
	m := newTestManager(t, Options{Spawn: spawn.spawn})
	ref, err := m.Register("plug", ProcessSpec{Name: "s", Entry: "/bin/sh"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	st, ok := m.Status(ref)
	if !ok {
		t.Fatal("应能查到登记")
	}
	if st.Runtime != RuntimeBinary || st.StartMode != StartLazy || st.Restart != RestartNo {
		t.Fatalf("缺省值 = runtime %q start %q restart %q", st.Runtime, st.StartMode, st.Restart)
	}
}
