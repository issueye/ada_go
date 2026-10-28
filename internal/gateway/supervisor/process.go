package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/gateway/contracts"
	"github.com/adag/ada-go/internal/version"
)

// SpawnSpec 是一次 Core 子进程启动所需的完整参数。
type SpawnSpec struct {
	Binary     string
	Workspace  string // 归一化后的工作区路径
	Product    string
	Provider   string
	Model      string
	BaseURL    string
	DataDir    string
	AllowHosts []string
	Fake       bool
	ParentPID  int
	Token      string
}

// Process 是 Supervisor 需要的最小进程抽象；测试可注入假实现。
//
// Wait 只会被调用一次（由退出监视 goroutine 拥有）；Kill 用于主动停止。
type Process interface {
	Stdout() io.ReadCloser
	PID() int
	Kill() error
	Wait() error
}

// SpawnFunc 拉起一个 Core 子进程。生产实现见 execSpawn。
type SpawnFunc func(ctx context.Context, spec SpawnSpec) (Process, error)

// handshake 是 Core 启动后写到 stdout 的第一行 JSON（见 cmd/adag core）。
type handshake struct {
	Endpoint     string `json:"endpoint"`
	PID          int    `json:"pid"`
	Product      string `json:"product"`
	Version      string `json:"version"`
	ProtoVersion int    `json:"protoVersion"`
	Workspace    string `json:"workspace"`
	ReadyAt      string `json:"readyAt"`
}

// startEntry 完成「spawn → 握手 → Dial」，失败时负责杀掉进程。
// 返回的 exited 通道由 watch 在进程退出后关闭。
func (s *Supervisor) startEntry(ctx context.Context, e *entry) (
	contracts.CoreClient, Process, chan struct{}, string, int, error,
) {
	spec := SpawnSpec{
		Binary:     s.opts.Binary,
		Workspace:  e.key,
		Product:    s.opts.Product,
		Provider:   s.opts.Provider,
		Model:      s.opts.Model,
		BaseURL:    s.opts.BaseURL,
		DataDir:    s.opts.DataDir,
		AllowHosts: append([]string(nil), s.opts.AllowHosts...),
		Fake:       s.opts.Fake,
		ParentPID:  s.opts.ParentPID,
		Token:      s.opts.Token,
	}
	proc, err := s.spawn(s.baseCtx, spec)
	if err != nil {
		return nil, nil, nil, "", 0, fmt.Errorf("supervisor: 拉起 Core 失败（工作区 %s）: %w", e.key, err)
	}

	hs, err := s.awaitHandshake(ctx, proc)
	if err != nil {
		s.abortStart(proc)
		return nil, nil, nil, "", 0, startFailure(e.key, "Core 启动握手", proc, err)
	}
	if s.isClosed() {
		s.abortStart(proc)
		return nil, nil, nil, "", 0, ErrShutdown
	}

	client, err := s.opts.Dial(ctx, hs.Endpoint, s.opts.Token)
	if err != nil {
		s.abortStart(proc)
		return nil, nil, nil, "", 0, startFailure(e.key, "连接 Core", proc, err)
	}
	if s.isClosed() {
		_ = client.Close()
		s.abortStart(proc)
		return nil, nil, nil, "", 0, ErrShutdown
	}

	pid := proc.PID()
	if pid <= 0 {
		pid = hs.PID
	}
	return client, proc, make(chan struct{}), hs.Endpoint, pid, nil
}

// awaitHandshake 在 StartTimeout 内读取 stdout 的第一行并解析握手 JSON。
func (s *Supervisor) awaitHandshake(ctx context.Context, proc Process) (handshake, error) {
	out := proc.Stdout()
	if out == nil {
		return handshake{}, errors.New("Core 进程没有提供 stdout")
	}

	type result struct {
		line []byte
		err  error
	}
	lineCh := make(chan result, 1)
	go func() {
		// 限制单行大小，避免异常子进程用超长输出拖垮 Gateway。
		r := bufio.NewReader(io.LimitReader(out, 64<<10))
		line, err := r.ReadBytes('\n')
		lineCh <- result{line: line, err: err}
	}()

	timer := time.NewTimer(s.opts.StartTimeout)
	defer timer.Stop()
	var res result
	select {
	case res = <-lineCh:
	case <-timer.C:
		return handshake{}, fmt.Errorf("等待 Core 握手行超时（%s）", s.opts.StartTimeout)
	case <-ctx.Done():
		return handshake{}, fmt.Errorf("等待 Core 握手行被取消: %w", ctx.Err())
	}

	trimmed := bytes.TrimSpace(res.line)
	if len(trimmed) == 0 {
		if res.err != nil {
			return handshake{}, fmt.Errorf("读取 Core 握手行失败: %w", res.err)
		}
		return handshake{}, errors.New("Core 没有输出握手行")
	}

	var hs handshake
	if err := json.Unmarshal(trimmed, &hs); err != nil {
		return handshake{}, fmt.Errorf("Core 握手行不是合法 JSON: %w（原文 %.200s）", err, trimmed)
	}
	if strings.TrimSpace(hs.Endpoint) == "" {
		return handshake{}, errors.New("Core 握手行缺少 endpoint")
	}
	if hs.ProtoVersion != 0 && hs.ProtoVersion != version.ProtocolVersion {
		return handshake{}, fmt.Errorf("Core 协议版本不匹配：对端 %d，本端 %d",
			hs.ProtoVersion, version.ProtocolVersion)
	}
	return hs, nil
}

// abortStart 杀掉一个尚未交给 watch 的进程，并尽量解除 stdout 读取阻塞。
func (s *Supervisor) abortStart(proc Process) {
	if proc == nil {
		return
	}
	_ = proc.Kill()
	if out := proc.Stdout(); out != nil {
		_ = out.Close()
	}
	_ = proc.Wait()
}

// watch 监视进程退出：标记不可用、记录 lastError、关闭客户端，
// 使后续 Acquire 可以重新拉起。
func (s *Supervisor) watch(e *entry, proc Process, client contracts.CoreClient, exited chan struct{}) {
	err := proc.Wait()

	closed := s.isClosed()
	e.mu.Lock()
	intentional := e.stopping || closed
	if e.proc == proc {
		e.proc = nil
		e.client = nil
		e.exited = nil
		e.endpoint = ""
		e.pid = 0
		e.ready = false
	}
	if !intentional {
		if err != nil {
			e.lastErr = "Core 进程退出: " + err.Error()
		} else {
			e.lastErr = "Core 进程已退出"
		}
	}
	e.mu.Unlock()

	_ = client.Close()
	close(exited)
}

// isClosed 读取 Supervisor 的关闭状态。
func (s *Supervisor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// startFailure 给启动错误附上 Core 的 stderr 片段，便于诊断。
func startFailure(workspace, stage string, proc Process, err error) error {
	base := fmt.Errorf("supervisor: %s失败（工作区 %s）: %w", stage, workspace, err)
	if snippet := stderrSnippet(proc); snippet != "" {
		return fmt.Errorf("%w；Core stderr: %s", base, snippet)
	}
	return base
}

// stderrCarrier 由生产进程实现，用于在启动失败时带上 stderr 尾部。
type stderrCarrier interface {
	StderrSnippet() string
}

func stderrSnippet(proc Process) string {
	if c, ok := proc.(stderrCarrier); ok {
		return c.StderrSnippet()
	}
	return ""
}

// execProcess 是生产 Spawn 返回的 Process 实现。
type execProcess struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *tailBuffer
	wait   sync.Once
	err    error
}

func (p *execProcess) Stdout() io.ReadCloser { return p.stdout }
func (p *execProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}
func (p *execProcess) Wait() error {
	p.wait.Do(func() { p.err = p.cmd.Wait() })
	return p.err
}

func (p *execProcess) StderrSnippet() string { return p.stderr.String() }

// execSpawn 用 exec.CommandContext 拉起 Core。ctx 来自 Supervisor 的生命周期，
// 不使用请求 ctx，避免一次请求结束就误杀长期存活的 Core。
func execSpawn(ctx context.Context, spec SpawnSpec) (Process, error) {
	args := []string{
		"core",
		"--workspace", spec.Workspace,
		"--product", spec.Product,
		"--parent-pid", strconv.Itoa(spec.ParentPID),
		"--listen", "127.0.0.1:0",
	}
	if spec.Provider != "" {
		args = append(args, "--provider", spec.Provider)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.BaseURL != "" {
		args = append(args, "--base-url", spec.BaseURL)
	}
	if spec.DataDir != "" {
		args = append(args, "--data-dir", spec.DataDir)
	}
	if spec.Fake {
		args = append(args, "--fake")
	}
	for _, h := range spec.AllowHosts {
		args = append(args, "--allow-host", h)
	}

	cmd := exec.CommandContext(ctx, spec.Binary, args...)
	cmd.Env = append(os.Environ(), "ADAG_CORE_TOKEN="+spec.Token)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	stderr := &tailBuffer{max: 8 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 %s 失败: %w", spec.Binary, err)
	}
	return &execProcess{cmd: cmd, stdout: stdout, stderr: stderr}, nil
}

// tailBuffer 只保留写入内容的最后 max 字节，供启动失败时诊断。
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if b.max > 0 && len(b.buf) > b.max {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}
