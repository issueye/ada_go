package gateway_test

// Gateway 与被管进程（计划 P3）的接线：进程管理器随 Gateway 装配、随 Gateway
// Shutdown 回收，且装配失败路径不留下孤儿。
//
// 这里不拉起真实进程：Processes 注入点用假 spawn 构建，只验证生命周期接线。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adag/ada-go/internal/gateway"
	"github.com/adag/ada-go/internal/gateway/processmgr"
)

// gatewayFakeProcess 是 Wait 阻塞到 Kill 的假进程。
type gatewayFakeProcess struct {
	pid  int
	done chan struct{}
	once sync.Once
}

func (p *gatewayFakeProcess) PID() int { return p.pid }

func (p *gatewayFakeProcess) Wait() error {
	<-p.done
	return errors.New("killed")
}

func (p *gatewayFakeProcess) Kill() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

// gatewayFakeSpawner 记录被拉起的进程。
type gatewayFakeSpawner struct {
	mu    sync.Mutex
	next  int
	procs []*gatewayFakeProcess
}

func (s *gatewayFakeSpawner) spawn(_ context.Context, spec processmgr.StartSpec, stdout, stderr io.Writer) (processmgr.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	p := &gatewayFakeProcess{pid: 9000 + s.next, done: make(chan struct{})}
	s.procs = append(s.procs, p)
	if stdout != nil {
		_, _ = io.WriteString(stdout, "started "+spec.Name+"\n")
	}
	return p, nil
}

func (s *gatewayFakeSpawner) all() []*gatewayFakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*gatewayFakeProcess(nil), s.procs...)
}

// TestGatewayShutdownReapsManagedProcesses 验证 Shutdown 停止插件注册的进程。
func TestGatewayShutdownReapsManagedProcesses(t *testing.T) {
	spawn := &gatewayFakeSpawner{}
	procs, err := processmgr.New(processmgr.Options{Spawn: spawn.spawn})
	if err != nil {
		t.Fatalf("processmgr.New: %v", err)
	}
	if _, err := procs.Register("cron-manager", processmgr.ProcessSpec{
		Name: "scheduler", Entry: "dist/scheduler.js", Runtime: processmgr.RuntimeNode,
		StartMode: processmgr.StartOnEnable, Restart: processmgr.RestartOnFailure,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	started := spawn.all()
	if len(started) != 1 {
		t.Fatalf("on_enable 应启动一个进程，实际 %d", len(started))
	}

	repo, err := findRepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	gw, err := gateway.New(context.Background(), gateway.Config{
		Workspace: t.TempDir(),
		Product:   filepath.Join(repo, "products", "headless.agent.json"),
		Token:     testAccessToken,
		Supervisor: &fakeSupervisor{client: &fakeCoreClient{responses: map[string]json.RawMessage{
			"thread.list": json.RawMessage(`{"threads":[]}`),
		}}},
		Processes: procs,
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	if got := gw.Processes(); got != procs {
		t.Fatalf("Gateway.Processes() 应返回注入的管理器")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-started[0].done:
	case <-time.After(2 * time.Second):
		t.Fatal("Gateway 退出后进程仍在运行")
	}
	if listed := procs.List(); len(listed) != 0 {
		t.Fatalf("Shutdown 后不应保留登记: %+v", listed)
	}
	// 关闭后的注册必须被拒绝，避免插件在关停途中再拉起进程。
	if _, err := procs.Register("cron-manager", processmgr.ProcessSpec{Name: "again", Entry: "x"}); processmgr.CodeOf(err) != processmgr.CodeStartFailed {
		t.Fatalf("Shutdown 后注册应被拒绝，实际 %v", err)
	}
}

// TestGatewayNewWithoutSupervisorStillHasProcessManager：默认装配也带管理器，
// 且没有 Supervisor 时 Shutdown 仍然安全。
func TestGatewayNewWithoutSupervisorStillHasProcessManager(t *testing.T) {
	repo, err := findRepoRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}
	gw, err := gateway.New(context.Background(), gateway.Config{
		Workspace:  t.TempDir(),
		Product:    filepath.Join(repo, "products", "headless.agent.json"),
		Token:      testAccessToken,
		Supervisor: &fakeSupervisor{client: &fakeCoreClient{responses: map[string]json.RawMessage{}}},
	})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	pm := gw.Processes()
	if pm == nil {
		t.Fatal("默认装配应创建进程管理器")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := pm.Register("plug", processmgr.ProcessSpec{Name: "x", Entry: "y"}); processmgr.CodeOf(err) != processmgr.CodeStartFailed {
		t.Fatalf("Shutdown 后注册应被拒绝，实际 %v", err)
	}
}
