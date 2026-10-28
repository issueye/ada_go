package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
)

// fakeProcess 是测试用的子进程：stdout 由 io.Pipe 提供，Kill/exit 控制 Wait。
type fakeProcess struct {
	pid int

	r *io.PipeReader
	w *io.PipeWriter

	done    chan struct{}
	once    sync.Once
	exitErr error
}

func newFakeProcess(pid int) *fakeProcess {
	r, w := io.Pipe()
	return &fakeProcess{
		pid:  pid,
		r:    r,
		w:    w,
		done: make(chan struct{}),
	}
}

func (p *fakeProcess) Stdout() io.ReadCloser { return p.r }
func (p *fakeProcess) PID() int              { return p.pid }

func (p *fakeProcess) Kill() error {
	p.stop(nil)
	return nil
}

// exit 模拟进程自行退出（不是被 Kill）。
func (p *fakeProcess) exit(err error) {
	p.stop(err)
}

func (p *fakeProcess) stop(err error) {
	p.once.Do(func() {
		p.exitErr = err
		_ = p.w.Close()
		_ = p.r.Close()
		close(p.done)
	})
}

func (p *fakeProcess) Wait() error {
	<-p.done
	return p.exitErr
}

func (p *fakeProcess) isDone() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// reply 延迟 delay 后写一行握手 JSON。
func (p *fakeProcess) reply(line string, delay time.Duration) {
	go func() {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-p.done:
				return
			}
		}
		_, _ = io.WriteString(p.w, line)
	}()
}

// fakeClient 是测试用的 CoreClient。
type fakeClient struct {
	closed     atomic.Bool
	closeCount atomic.Int32
}

func (c *fakeClient) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if c.closed.Load() {
		return nil, errors.New("fake client: 已关闭")
	}
	return json.RawMessage(`{}`), nil
}

func (c *fakeClient) Subscribe(buffer int) (<-chan contracts.Event, func()) {
	return make(chan contracts.Event), func() {}
}

func (c *fakeClient) Close() error {
	c.closed.Store(true)
	c.closeCount.Add(1)
	return nil
}

type dialCall struct {
	endpoint string
	token    string
}

// harness 串联假 Spawn 与假 Dial，并记录调用次数。
type harness struct {
	mu        sync.Mutex
	spawns    int
	procs     []*fakeProcess
	clients   []*fakeClient
	dialCalls []dialCall
	dialErr   error
	onSpawn   func(index int, p *fakeProcess)
}

func handshakeLine(endpoint string, pid int) string {
	b, _ := json.Marshal(map[string]any{
		"endpoint":     endpoint,
		"pid":          pid,
		"product":      "test-product",
		"version":      "test",
		"protoVersion": 1,
		"workspace":    "test-workspace",
		"readyAt":      time.Now().UTC().Format(time.RFC3339Nano),
	})
	return string(b) + "\n"
}

func (h *harness) newSupervisor(t *testing.T, mutate func(*Options)) *Supervisor {
	t.Helper()

	opts := Options{
		Binary:       "fake-adag",
		Product:      "test-product",
		Token:        "test-token",
		StartTimeout: time.Second,
		MaxCores:     8,
		IdleTimeout:  -1, // 默认关闭空闲回收，避免干扰无关用例
	}
	if mutate != nil {
		mutate(&opts)
	}
	opts.Spawn = func(ctx context.Context, spec SpawnSpec) (Process, error) {
		h.mu.Lock()
		index := h.spawns
		h.spawns++
		p := newFakeProcess(2000 + index)
		h.procs = append(h.procs, p)
		onSpawn := h.onSpawn
		h.mu.Unlock()

		if onSpawn != nil {
			onSpawn(index, p)
		} else {
			line := handshakeLine("127.0.0.1:"+fmt.Sprint(3000+index), p.pid)
			p.reply(line, 0)
		}
		return p, nil
	}
	opts.Dial = func(ctx context.Context, endpoint, token string) (contracts.CoreClient, error) {
		h.mu.Lock()
		h.dialCalls = append(h.dialCalls, dialCall{endpoint: endpoint, token: token})
		err := h.dialErr
		h.mu.Unlock()
		if err != nil {
			return nil, err
		}
		c := &fakeClient{}
		h.mu.Lock()
		h.clients = append(h.clients, c)
		h.mu.Unlock()
		return c, nil
	}

	s, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Shutdown(context.Background())
	})
	return s
}

func (h *harness) spawnCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.spawns
}

func (h *harness) dialCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.dialCalls)
}

func (h *harness) process(i int) *fakeProcess {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.procs[i]
}

func (h *harness) client(i int) *fakeClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clients[i]
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestAcquireSpawnsOnceAndReuses(t *testing.T) {
	h := &harness{}
	s := h.newSupervisor(t, nil)
	ws := t.TempDir()

	first, err := s.Acquire(context.Background(), ws)
	if err != nil {
		t.Fatalf("首次 Acquire: %v", err)
	}
	second, err := s.Acquire(context.Background(), ws)
	if err != nil {
		t.Fatalf("二次 Acquire: %v", err)
	}
	if first != second {
		t.Fatal("同一工作区应复用同一个客户端")
	}
	if got := h.spawnCount(); got != 1 {
		t.Fatalf("spawn 次数 = %d，期望 1", got)
	}
	if got := h.dialCount(); got != 1 {
		t.Fatalf("Dial 次数 = %d，期望 1", got)
	}

	statuses := s.List()
	if len(statuses) != 1 {
		t.Fatalf("List 条目数 = %d，期望 1", len(statuses))
	}
	st := statuses[0]
	if !st.Ready || st.Endpoint == "" || st.PID == 0 {
		t.Fatalf("状态不完整: %+v", st)
	}
	if st.Product != "test-product" {
		t.Fatalf("Product = %q", st.Product)
	}

	s.Release(ws)
	s.Release(ws)
	if got := h.spawnCount(); got != 1 {
		t.Fatalf("Release 不应触发重新 spawn，实际 %d", got)
	}
}

func TestConcurrentAcquireSingleflight(t *testing.T) {
	h := &harness{onSpawn: func(_ int, p *fakeProcess) {
		// 放慢握手，尽量让所有 Acquire 在启动窗口内重叠。
		p.reply(handshakeLine("127.0.0.1:9090", p.pid), 30*time.Millisecond)
	}}
	s := h.newSupervisor(t, nil)
	ws := t.TempDir()

	const n = 12
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []contracts.CoreClient
		errs    []error
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := s.Acquire(context.Background(), ws)
			mu.Lock()
			results = append(results, c)
			errs = append(errs, err)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("并发 Acquire 失败: %v", err)
		}
	}
	for _, c := range results {
		if c != results[0] {
			t.Fatal("并发 Acquire 返回了不同客户端")
		}
	}
	if got := h.spawnCount(); got != 1 {
		t.Fatalf("并发 spawn 次数 = %d，期望 1", got)
	}
	if got := h.dialCount(); got != 1 {
		t.Fatalf("并发 Dial 次数 = %d，期望 1", got)
	}
	for i := 0; i < n; i++ {
		s.Release(ws)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	h := &harness{onSpawn: func(_ int, p *fakeProcess) {
		// 不写握手行，制造超时。
	}}
	s := h.newSupervisor(t, func(o *Options) {
		o.StartTimeout = 50 * time.Millisecond
	})
	ws := t.TempDir()

	_, err := s.Acquire(context.Background(), ws)
	if err == nil {
		t.Fatal("期望握手超时错误")
	}
	if !strings.Contains(err.Error(), "超时") && !strings.Contains(err.Error(), "握手") {
		t.Fatalf("错误信息不可诊断: %v", err)
	}
	if got := h.dialCount(); got != 0 {
		t.Fatalf("握手失败后不应 Dial，实际 %d", got)
	}
	waitFor(t, time.Second, "超时进程被 Kill", func() bool {
		return h.process(0).isDone()
	})
}

func TestDialFailureThenRetry(t *testing.T) {
	h := &harness{}
	h.dialErr = errors.New("dial boom")
	s := h.newSupervisor(t, nil)
	ws := t.TempDir()

	_, err := s.Acquire(context.Background(), ws)
	if err == nil || !strings.Contains(err.Error(), "dial boom") {
		t.Fatalf("期望 Dial 失败错误，实际 %v", err)
	}
	waitFor(t, time.Second, "Dial 失败后进程被 Kill", func() bool {
		return h.process(0).isDone()
	})

	h.mu.Lock()
	h.dialErr = nil
	h.mu.Unlock()

	client, err := s.Acquire(context.Background(), ws)
	if err != nil {
		t.Fatalf("重试 Acquire: %v", err)
	}
	if client == nil {
		t.Fatal("重试后客户端为空")
	}
	if got := h.spawnCount(); got != 2 {
		t.Fatalf("spawn 次数 = %d，期望 2（失败后重启）", got)
	}
}

func TestRestartAfterProcessExit(t *testing.T) {
	h := &harness{}
	s := h.newSupervisor(t, nil)
	ws := t.TempDir()

	first, err := s.Acquire(context.Background(), ws)
	if err != nil {
		t.Fatalf("首次 Acquire: %v", err)
	}
	proc := h.process(0)
	proc.exit(errors.New("boom"))

	waitFor(t, 2*time.Second, "状态变为未就绪并记录错误", func() bool {
		st := s.List()
		return len(st) == 1 && !st[0].Ready && strings.Contains(st[0].LastError, "boom")
	})
	if !h.client(0).closed.Load() {
		t.Fatal("进程退出后旧客户端应被关闭")
	}

	second, err := s.Acquire(context.Background(), ws)
	if err != nil {
		t.Fatalf("重启 Acquire: %v", err)
	}
	if second == first {
		t.Fatal("重启后应返回新的客户端")
	}
	if got := h.spawnCount(); got != 2 {
		t.Fatalf("spawn 次数 = %d，期望 2", got)
	}
	st := s.List()
	if len(st) != 1 || !st[0].Ready || st[0].LastError != "" {
		t.Fatalf("重启后状态异常: %+v", st)
	}
}

func TestMaxCoresEvictsLeastRecentlyUsed(t *testing.T) {
	h := &harness{}
	s := h.newSupervisor(t, func(o *Options) {
		o.MaxCores = 2
	})
	wsA, wsB, wsC := t.TempDir(), t.TempDir(), t.TempDir()

	for _, ws := range []string{wsA, wsB} {
		if _, err := s.Acquire(context.Background(), ws); err != nil {
			t.Fatalf("Acquire %s: %v", ws, err)
		}
		s.Release(ws)
		time.Sleep(15 * time.Millisecond)
	}

	if _, err := s.Acquire(context.Background(), wsC); err != nil {
		t.Fatalf("Acquire C: %v", err)
	}

	if got := h.spawnCount(); got != 3 {
		t.Fatalf("spawn 次数 = %d，期望 3", got)
	}
	waitFor(t, time.Second, "最久未使用的 A 被回收", func() bool {
		return h.process(0).isDone() && h.client(0).closed.Load()
	})

	statuses := s.List()
	if len(statuses) != 2 {
		t.Fatalf("List 条目数 = %d，期望 2: %+v", len(statuses), statuses)
	}
	for i := 1; i < len(statuses); i++ {
		if statuses[i-1].Workspace > statuses[i].Workspace {
			t.Fatalf("List 未按 workspace 排序: %+v", statuses)
		}
	}
	// A 应已不在列表中；B 与 C 仍在。
	// 归一化键在 Windows 会小写，这里用相同规则比较。
	foundA := false
	for _, st := range statuses {
		if strings.Contains(strings.ToLower(st.Workspace), strings.ToLower(wsA)) {
			foundA = true
		}
	}
	if foundA {
		t.Fatalf("A 应被回收: %+v", statuses)
	}
}

func TestMaxCoresNoEvictableReturnsError(t *testing.T) {
	h := &harness{}
	s := h.newSupervisor(t, func(o *Options) {
		o.MaxCores = 1
	})
	wsA, wsB := t.TempDir(), t.TempDir()

	if _, err := s.Acquire(context.Background(), wsA); err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	_, err := s.Acquire(context.Background(), wsB)
	if err == nil {
		t.Fatal("A 有在途 lease，B 应因达到上限而失败")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("错误信息应明确指出上限: %v", err)
	}

	s.Release(wsA)
	if _, err := s.Acquire(context.Background(), wsB); err != nil {
		t.Fatalf("释放 A 后 Acquire B 应成功: %v", err)
	}
}

func TestIdleReap(t *testing.T) {
	h := &harness{}
	s := h.newSupervisor(t, func(o *Options) {
		o.IdleTimeout = 100 * time.Millisecond
	})
	ws := t.TempDir()

	if _, err := s.Acquire(context.Background(), ws); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	s.Release(ws)

	waitFor(t, 3*time.Second, "空闲实例被 janitor 回收", func() bool {
		return len(s.List()) == 0
	})
	if !h.process(0).isDone() {
		t.Fatal("回收后进程应被 Kill")
	}
	if !h.client(0).closed.Load() {
		t.Fatal("回收后客户端应被关闭")
	}
}

func TestShutdownIdempotent(t *testing.T) {
	h := &harness{}
	s := h.newSupervisor(t, nil)
	ws := t.TempDir()

	if _, err := s.Acquire(context.Background(), ws); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("首次 Shutdown: %v", err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("二次 Shutdown 应幂等: %v", err)
	}
	if !h.process(0).isDone() {
		t.Fatal("Shutdown 应杀掉子进程")
	}
	if !h.client(0).closed.Load() {
		t.Fatal("Shutdown 应关闭客户端")
	}
	if _, err := s.Acquire(context.Background(), ws); !errors.Is(err, ErrShutdown) {
		t.Fatalf("关闭后 Acquire 应返回 ErrShutdown，实际 %v", err)
	}
	if got := len(s.List()); got != 0 {
		t.Fatalf("Shutdown 后 List 应为空，实际 %d", got)
	}
}
