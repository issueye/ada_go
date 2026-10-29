// Package supervisor 管理 Gateway 名下的 Core 子进程生命周期（M2）。
//
// 一个工作区对应至多一个 Core。Gateway 通过 Acquire 取到该工作区的 RPC 句柄，
// 请求结束调用 Release；空闲实例由后台 janitor 按 IdleTimeout 回收，实例总数
// 受 MaxCores 限制。进程拉起采用「单飞」：同一工作区的并发 Acquire 只会 spawn
// 一个 Core。
//
// 边界：本包只负责进程与客户端句柄的生命周期，不实现 RPC 协议或 HTTP 传输；
// 建连与调用细节由 contracts.DialFunc 注入，测试因此不需要真实 adag 二进制。
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/workspacepath"
)

// ErrShutdown 表示 Supervisor 已关闭，不再接受新的 Acquire。
var ErrShutdown = errors.New("supervisor: 已关闭")

// Options 是 Supervisor 的构造参数。
type Options struct {
	// Binary 是 adag 可执行文件路径；留空取 os.Executable()。
	Binary string
	// Product 是产品声明文件路径，必填（Core 启动参数 --product）。
	Product string
	// Provider、Model、BaseURL、DataDir 是可选启动参数，原样传给 Core。
	Provider string
	Model    string
	BaseURL  string
	DataDir  string
	// AllowHosts 是额外出网 host 白名单（可重复的 --allow-host）。
	Fake       bool
	AllowHosts []string
	// ParentPID 是传给 Core 的父进程 pid；0 表示取当前进程 pid。
	ParentPID int
	// Token 是 Core 的 RPC 令牌：经 ADAG_CORE_TOKEN 环境变量传给子进程，
	// Dial 时使用同一值。
	Token string
	// StartTimeout 是等待 stdout 握手行的上限，默认 5s。
	StartTimeout time.Duration
	// MaxCores 是同时存活的 Core 实例上限，默认 8。达到上限时优先回收
	// 最久未使用且没有在途 lease 的实例。
	MaxCores int
	// IdleTimeout 是空闲实例的回收阈值，默认 15m。0 取默认值；负值表示
	// 关闭空闲回收（janitor 不启动）。
	IdleTimeout time.Duration
	// Dial 建立到 Core 的客户端，必填。测试注入假实现。
	Dial contracts.DialFunc
	// Spawn 拉起 Core 子进程，可选。留空使用 exec.CommandContext 的生产实现；
	// 测试注入假进程。
	Spawn SpawnFunc
}

// Supervisor 是 contracts.Supervisor 的实现。
type Supervisor struct {
	opts         Options
	idleDisabled bool
	spawn        SpawnFunc

	baseCtx context.Context
	cancel  context.CancelFunc

	mu          sync.Mutex
	entries     map[string]*entry // 以归一化工作区路径（sqlite.WorkspaceKey）为键
	closed      bool
	startWG     sync.WaitGroup // 在途的 Acquire 启动流程，Shutdown 有限等待
	janitorDone chan struct{}

	shutdownOnce sync.Once
	shutdownErr  error
}

// entry 是一个工作区的管理状态。
type entry struct {
	// key 是归一化后的工作区路径，也是 registry 的键。
	key string
	// createdAt 只用于 LRU 比较时的兜底时间。
	createdAt time.Time
	// spawnMu 串行化同一工作区的「检查 → 拉起」过程，实现单飞。
	spawnMu sync.Mutex

	mu       sync.Mutex
	client   contracts.CoreClient
	proc     Process
	exited   chan struct{} // 由退出监视 goroutine 在 proc.Wait 返回后关闭
	endpoint string
	pid      int
	ready    bool
	lastErr  string
	leases   int
	lastUsed time.Time
	// busy 表示正在拉起或正在被回收，LRU/空闲回收必须跳过。
	busy bool
	// stopping 表示主动停止，退出监视不应把它记成异常。
	stopping bool
}

func newEntry(key string) *entry {
	return &entry{key: key, createdAt: time.Now()}
}

// 编译期确认实现符合共享契约。
var _ contracts.Supervisor = (*Supervisor)(nil)

// New 校验必填项并构造 Supervisor。返回的实例会启动空闲回收 janitor
// （IdleTimeout 为负时除外），调用方必须最终调用 Shutdown。
func New(opts Options) (*Supervisor, error) {
	if opts.Dial == nil {
		return nil, errors.New("supervisor: Options.Dial 必填")
	}
	if strings.TrimSpace(opts.Product) == "" {
		return nil, errors.New("supervisor: Options.Product 必填")
	}
	if opts.Binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("supervisor: 无法定位当前可执行文件（显式传 Options.Binary 即可绕开）: %w", err)
		}
		opts.Binary = exe
	}
	if opts.ParentPID == 0 {
		opts.ParentPID = os.Getpid()
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 5 * time.Second
	}
	if opts.MaxCores <= 0 {
		opts.MaxCores = 8
	}
	idleDisabled := opts.IdleTimeout < 0
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 15 * time.Minute
	}

	baseCtx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		opts:         opts,
		idleDisabled: idleDisabled,
		spawn:        opts.Spawn,
		baseCtx:      baseCtx,
		cancel:       cancel,
		entries:      make(map[string]*entry),
		janitorDone:  make(chan struct{}),
	}
	if s.spawn == nil {
		s.spawn = execSpawn
	}
	if idleDisabled {
		close(s.janitorDone)
	} else {
		go s.janitor()
	}
	return s, nil
}

// Acquire 返回一个工作区的 Core 客户端。同一工作区已有 ready 实例时直接复用；
// 进程退出或客户端关闭后，下一次 Acquire 会重新拉起。
func (s *Supervisor) Acquire(ctx context.Context, workspace string) (contracts.CoreClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key, err := workspacepath.Key(workspace)
	if err != nil {
		return nil, fmt.Errorf("supervisor: 归一化工作区 %q 失败: %w", workspace, err)
	}

	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, ErrShutdown
		}
		e := s.entries[key]
		if e == nil {
			e = newEntry(key)
			s.entries[key] = e
		}
		s.mu.Unlock()

		e.spawnMu.Lock()

		// 拿到单飞锁后重新确认条目仍在 registry 中且 Supervisor 未关闭：
		// 条目可能在等待锁期间被 LRU 回收。
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			e.spawnMu.Unlock()
			return nil, ErrShutdown
		}
		if s.entries[key] != e {
			s.mu.Unlock()
			e.spawnMu.Unlock()
			continue
		}

		e.mu.Lock()
		if e.ready && e.client != nil {
			e.leases++
			e.lastUsed = time.Now()
			client := e.client
			e.mu.Unlock()
			s.mu.Unlock()
			e.spawnMu.Unlock()
			return client, nil
		}
		// 先占位，防止 LRU 在拉起过程中回收本条目。
		e.busy = true
		e.mu.Unlock()

		victims, err := s.makeRoomLocked(e)
		if err != nil {
			e.mu.Lock()
			e.busy = false
			e.lastErr = err.Error()
			e.lastUsed = time.Now()
			e.mu.Unlock()
			s.mu.Unlock()
			e.spawnMu.Unlock()
			return nil, err
		}
		s.startWG.Add(1)
		s.mu.Unlock()

		// 回收动作在 registry 锁之外执行，避免 Kill/Wait 阻塞其他工作区。
		for _, v := range victims {
			s.stopEntry(v, 2*time.Second)
		}

		client, proc, exited, endpoint, pid, startErr := s.startEntry(ctx, e)

		if startErr != nil {
			s.startWG.Done()
			e.mu.Lock()
			e.busy = false
			e.lastErr = startErr.Error()
			e.lastUsed = time.Now()
			e.mu.Unlock()
			e.spawnMu.Unlock()
			return nil, startErr
		}

		// 提交状态必须与 Shutdown 的 closed 标记互斥：否则刚拉起的进程可能在
		// 被摘出 registry 之后才挂上，成为无人回收的孤儿。
		s.mu.Lock()
		if s.closed || s.entries[key] != e {
			s.mu.Unlock()
			s.startWG.Done()
			_ = client.Close()
			s.abortStart(proc)
			e.mu.Lock()
			e.busy = false
			e.mu.Unlock()
			e.spawnMu.Unlock()
			return nil, ErrShutdown
		}
		e.mu.Lock()
		e.client = client
		e.proc = proc
		e.exited = exited
		e.endpoint = endpoint
		e.pid = pid
		e.ready = true
		e.lastErr = ""
		e.leases++
		e.lastUsed = time.Now()
		e.busy = false
		e.stopping = false
		e.mu.Unlock()
		s.mu.Unlock()
		s.startWG.Done()
		e.spawnMu.Unlock()

		go s.watch(e, proc, client, exited)
		return client, nil
	}
}

// Release 表示一次请求结束：只递减 lease 并刷新 lastUsed，不关闭 Core。
func (s *Supervisor) Release(workspace string) {
	key, err := workspacepath.Key(workspace)
	if err != nil {
		return
	}
	s.mu.Lock()
	e := s.entries[key]
	s.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.leases > 0 {
		e.leases--
	}
	e.lastUsed = time.Now()
	e.mu.Unlock()
}

// List 返回所有已知工作区的状态快照，按 workspace 排序。
func (s *Supervisor) List() []contracts.WorkspaceStatus {
	s.mu.Lock()
	entries := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()

	statuses := make([]contracts.WorkspaceStatus, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		st := contracts.WorkspaceStatus{
			Workspace: e.key,
			Product:   s.opts.Product,
			Endpoint:  e.endpoint,
			PID:       e.pid,
			Ready:     e.ready,
			LastError: e.lastErr,
		}
		e.mu.Unlock()
		statuses = append(statuses, st)
	}
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].Workspace < statuses[j].Workspace
	})
	return statuses
}

// Shutdown 幂等：停止 janitor、关闭所有客户端、杀掉子进程并有限等待退出。
func (s *Supervisor) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		victims := make([]*entry, 0, len(s.entries))
		for _, e := range s.entries {
			victims = append(victims, e)
		}
		s.entries = make(map[string]*entry)
		s.mu.Unlock()

		// 取消基座 context：生产 spawn 的 exec.CommandContext 会因此杀掉子进程。
		s.cancel()

		// 等待 janitor 退出，避免它与下面的关闭动作并发。
		if s.janitorDone != nil {
			select {
			case <-s.janitorDone:
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}

		var wg sync.WaitGroup
		for _, e := range victims {
			wg.Add(1)
			go func(e *entry) {
				defer wg.Done()
				s.stopEntry(e, 2*time.Second)
			}(e)
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}

		// 等待仍在进行的启动流程自行收尾（它们会在发现 closed 后杀掉刚拉起的进程）。
		startDone := make(chan struct{})
		go func() {
			s.startWG.Wait()
			close(startDone)
		}()
		select {
		case <-startDone:
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	})
	return s.shutdownErr
}

// makeRoomLocked 在不超过 MaxCores 的前提下为 target 腾出位置。
// 调用方必须持有 s.mu；被选中的实例会从 registry 删除并标记 busy，
// 由调用方在锁外 stopEntry。
func (s *Supervisor) makeRoomLocked(target *entry) ([]*entry, error) {
	var victims []*entry
	for s.activeCountLocked() >= s.opts.MaxCores {
		v := s.pickVictimLocked(target)
		if v == nil {
			return nil, fmt.Errorf(
				"supervisor: Core 数量已达上限 %d，且没有无在途 lease 的空闲实例可回收（工作区 %s）",
				s.opts.MaxCores, target.key)
		}
		delete(s.entries, v.key)
		v.mu.Lock()
		v.busy = true
		v.mu.Unlock()
		victims = append(victims, v)
	}
	return victims, nil
}

// activeCountLocked 统计真正持有进程/客户端的实例数。
func (s *Supervisor) activeCountLocked() int {
	n := 0
	for _, e := range s.entries {
		e.mu.Lock()
		if e.client != nil || e.proc != nil {
			n++
		}
		e.mu.Unlock()
	}
	return n
}

// pickVictimLocked 选出最久未使用、且没有在途 lease 的实例。
func (s *Supervisor) pickVictimLocked(except *entry) *entry {
	var (
		best   *entry
		latest time.Time
	)
	for _, e := range s.entries {
		if e == except {
			continue
		}
		e.mu.Lock()
		idle := e.leases == 0 && !e.busy && !e.stopping
		hasInstance := e.client != nil || e.proc != nil
		last := e.lastUsed
		if last.IsZero() {
			last = e.createdAt
		}
		e.mu.Unlock()
		if !idle || !hasInstance {
			continue
		}
		if best == nil || last.Before(latest) {
			best, latest = e, last
		}
	}
	return best
}

// stopEntry 主动停止一个条目：关闭客户端、杀掉进程并有限等待退出监视收尾。
func (s *Supervisor) stopEntry(e *entry, timeout time.Duration) {
	e.mu.Lock()
	e.stopping = true
	e.busy = false
	client := e.client
	proc := e.proc
	exited := e.exited
	e.client = nil
	e.proc = nil
	e.exited = nil
	e.endpoint = ""
	e.pid = 0
	e.ready = false
	e.mu.Unlock()

	if client != nil {
		_ = client.Close()
	}
	if proc != nil {
		_ = proc.Kill()
		if exited != nil {
			select {
			case <-exited:
			case <-time.After(timeout):
			}
		}
	}
}
