package host

// 通用长驻子进程能力（capability: process）。
//
// 它不是 MCP 专有 API：给插件一个可写 stdin、按增量读 stdout/stderr、可整树终止的进程句柄。
// MCP stdio 是第一个消费者；基座仍然不知道 MCP。
//
// 边界与 ctx.exec 一致：无 shell、程序策略、cwd scope、env 白名单、输出上限、审计、
// 进程树终止；额外增加每 Runtime 进程数、生命周期与增量读取的限额。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

const (
	// DefaultProcessLifetime 是单个长驻进程的生命周期上限。
	DefaultProcessLifetime = 30 * time.Minute
	// MaxProcessesPerRuntime 是每插件 Runtime 同时保留的进程数（含已结束未回收）。
	MaxProcessesPerRuntime = 8
	// DefaultProcessReadTimeout / MaxProcessReadTimeout 是 read 的等待上限。
	DefaultProcessReadTimeout = 100 * time.Millisecond
	MaxProcessReadTimeout     = 30 * time.Second
	// MaxProcessReadBytes 是单次 read 返回的字节上限（两路各自）。
	MaxProcessReadBytes = 1 << 20
)

// processState 管理一个 Runtime 的全部长驻进程。
type processState struct {
	mu     sync.Mutex
	next   int
	procs  map[int]*managedProcess
	closed bool
}

func newProcessState() *processState {
	return &processState{next: 1, procs: map[int]*managedProcess{}}
}

type managedProcess struct {
	id     int
	pid    int
	mu     sync.Mutex
	stdout bytes.Buffer
	stderr bytes.Buffer
	// total 是截断前的真实字节数；truncated 表示缓冲发生过截断。
	stdoutTotal int
	stderrTotal int
	truncated   bool
	exited      bool
	exitCode    int
	errText     string
	killed      bool
	stdin       io.WriteCloser
	wake        chan struct{}
	cancel      context.CancelFunc
}

// procWriter 把 cmd 的输出写进进程缓冲，并唤醒等待中的 read。
type procWriter struct {
	p      *managedProcess
	stderr bool
}

func (w procWriter) Write(b []byte) (int, error) {
	w.p.writeOutput(b, w.stderr)
	return len(b), nil
}

func (p *managedProcess) writeOutput(b []byte, stderr bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var buf *bytes.Buffer
	var total *int
	if stderr {
		buf, total = &p.stderr, &p.stderrTotal
	} else {
		buf, total = &p.stdout, &p.stdoutTotal
	}
	*total += len(b)
	// 单路缓冲上限与 ctx.exec 一致（4MB）；超限保留已写入部分并标记。
	if buf.Len()+len(b) > currentLimits().MaxExecOutputBytes {
		p.truncated = true
		if keep := currentLimits().MaxExecOutputBytes - buf.Len(); keep > 0 {
			_, _ = buf.Write(b[:keep])
		}
	} else {
		_, _ = buf.Write(b)
	}
	p.notifyLocked()
}

func (p *managedProcess) notifyLocked() {
	close(p.wake)
	p.wake = make(chan struct{})
}

func (p *managedProcess) finish(waitErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exited = true
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
	if p.killed {
		p.exitCode = -1
	} else if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			p.exitCode = ee.ExitCode()
		} else {
			p.errText = waitErr.Error()
			p.exitCode = -1
		}
	}
	p.notifyLocked()
}

// read 返回自上次 read 以来的增量；没有数据时等待 timeout 或进程退出。
func (p *managedProcess) read(ctx context.Context, maxBytes int, timeout time.Duration) (map[string]any, error) {
	if maxBytes <= 0 || maxBytes > MaxProcessReadBytes {
		maxBytes = MaxProcessReadBytes
	}
	if timeout <= 0 || timeout > MaxProcessReadTimeout {
		timeout = DefaultProcessReadTimeout
	}
	for {
		p.mu.Lock()
		out := p.stdout.String()
		errOut := p.stderr.String()
		p.stdout.Reset()
		p.stderr.Reset()
		exited, code, errText := p.exited, p.exitCode, p.errText
		truncated := p.truncated
		if len(out) > maxBytes {
			// 返回头部片段，余量留在缓冲里等下一次 read。
			// 读流协议（stdio MCP 的按行 JSON-RPC）不能丢中间字节：tail 截断会把
			// 一条 >maxBytes 的 JSON 行弄成非法 JSON，插件只能等到超时。
			head, rest := splitHeadUTF8(out, maxBytes)
			out = head
			_, _ = p.stdout.WriteString(rest)
		}
		if len(errOut) > maxBytes {
			// stderr 只用于排障，保留尾部更有信息量。
			errOut = tailUTF8(errOut, maxBytes)
			truncated = true
		}
		if len(out) > 0 || len(errOut) > 0 || exited {
			p.truncated = false
			p.mu.Unlock()
			view := map[string]any{
				"stdout": out, "stderr": errOut, "exited": exited, "truncated": truncated,
			}
			if exited {
				view["code"] = code
				if errText != "" {
					view["error"] = errText
				}
			}
			return view, nil
		}
		wake := p.wake
		p.mu.Unlock()

		timer := time.NewTimer(timeout)
		select {
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			return map[string]any{"stdout": "", "stderr": "", "exited": false, "truncated": false}, nil
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		}
	}
}

// splitHeadUTF8 把 s 在 max 字节处切成两段，切点退到一个完整 UTF-8 字符边界。
// 与 tailUTF8 相反：这里不丢数据，余量由调用方留到下一次 read。
func splitHeadUTF8(s string, max int) (string, string) {
	if max <= 0 || len(s) <= max {
		return s, ""
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], s[cut:]
}

// kill 关闭 stdin 并杀整棵进程树。
func (p *managedProcess) kill() {
	p.mu.Lock()
	if p.exited || p.killed {
		p.mu.Unlock()
		return
	}
	p.killed = true
	pid := p.pid
	stdin := p.stdin
	cancel := p.cancel
	p.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cancel != nil {
		cancel()
	}
	killProcessTree(pid)
}

func (s *processState) shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	procs := make([]*managedProcess, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.mu.Unlock()
	for _, p := range procs {
		p.kill()
	}
}

// startProcess 是 ctx.process.start 的宿主实现；返回 taskId（句柄由调用方构造）。
func (s *processState) startProcess(st *capsState, args []moejs.Value) (int, error) {
	r := st.rt.Realm()
	file := strings.TrimSpace(moejs.Arg(args, 0).String())
	if file == "" {
		return 0, throwCoded(r, codeInvalidArgType, "ctx.process.start(cmd, args): cmd 必须是非空字符串", nil)
	}
	var argv []string
	if v := moejs.Arg(args, 1); v.IsObject() {
		if err := st.rt.ToGoInto(v, &argv); err != nil {
			return 0, throwCoded(r, codeInvalidArgType, "ctx.process.start(cmd, args): args 必须是字符串数组: "+err.Error(), nil)
		}
	}
	if ExecPolicy != nil {
		if err := ExecPolicy(file, argv); err != nil {
			return 0, throwCoded(r, codeAccessDenied, "执行策略拒绝: "+err.Error(), nil)
		}
	}

	req := &execRequest{file: file, args: argv, maxBuffer: st.limits.MaxExecOutputBytes, timeout: DefaultProcessLifetime, baseEnv: st.baseEnv, binDirs: st.binDirs}
	cwd := st.caps.WorkspaceRoot
	envNames := make([]string, 0, len(st.caps.Env))
	for name := range st.caps.Env {
		envNames = append(envNames, name)
	}
	if opts := moejs.Arg(args, 2); opts.IsObject() {
		if v, err := st.rt.Get(opts, "cwd"); err == nil && !v.IsNullish() {
			real, cerr := st.fsRealPath(v.String())
			if cerr != nil {
				return 0, cerr
			}
			cwd = real
		}
		if v, err := st.rt.Get(opts, "timeoutMs"); err == nil {
			if n, ok := jsInt(v); ok && n > 0 {
				req.timeout = time.Duration(n) * time.Millisecond
			}
		}
		if v, err := st.rt.Get(opts, "env"); err == nil && v.IsObject() {
			var want []string
			if err := st.rt.ToGoInto(v, &want); err == nil {
				envNames = envNames[:0]
				for _, name := range want {
					if _, ok := st.caps.Env[name]; ok {
						envNames = append(envNames, name)
					}
				}
			}
		}
	}
	if req.timeout <= 0 || req.timeout > DefaultProcessLifetime {
		req.timeout = DefaultProcessLifetime
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, throwCoded(r, codeOutOfRange, "ctx.process.start: Runtime 已关闭", nil)
	}
	for id, p := range s.procs {
		p.mu.Lock()
		done := p.exited
		p.mu.Unlock()
		if done {
			delete(s.procs, id)
		}
	}
	if len(s.procs) >= MaxProcessesPerRuntime {
		s.mu.Unlock()
		return 0, throwCoded(r, codeOutOfRange,
			fmt.Sprintf("ctx.process.start: 同时运行的进程数达到上限 %d", MaxProcessesPerRuntime), nil)
	}
	id := s.next
	s.next++
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), req.timeout)
	cmd := exec.CommandContext(ctx, req.file, req.args...)
	cmd.WaitDelay = 2 * time.Second
	configureProcessTree(cmd)
	if cwd != "" {
		cmd.Dir = cwd
	}
	// 只注入清单白名单里的宿主变量，再叠加环境变量模块的用户变量（同名覆盖）。
	baseEnv := make([]string, 0, len(envNames))
	if len(envNames) > 0 {
		sortStrings(envNames)
		for _, name := range envNames {
			baseEnv = append(baseEnv, name+"="+st.caps.Env[name])
		}
	}
	cmd.Env = applyBinPath(mergeEnv(baseEnv, st.baseEnv), st.binDirs)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return 0, throwCoded(r, codeOutOfRange, "ctx.process.start: 建 stdin 失败: "+err.Error(), nil)
	}
	p := &managedProcess{
		stdin: stdin, wake: make(chan struct{}), cancel: cancel,
	}
	cmd.Stdout = procWriter{p: p}
	cmd.Stderr = procWriter{p: p, stderr: true}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		cancel()
		return 0, throwCoded(r, codeOutOfRange, "ctx.process.start: "+err.Error(), nil)
	}
	p.pid = cmd.Process.Pid

	s.mu.Lock()
	s.procs[id] = p
	s.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		if ctx.Err() != nil {
			killProcessTree(p.pid)
		}
		p.finish(waitErr)
		st.audit("process", "exit", req.file, p.stdoutTotal+p.stderrTotal, "exit", map[string]any{
			"pid": p.pid, "code": p.exitCode, "taskId": id,
		})
	}()

	st.audit("process", "start", req.file, 0, "started", map[string]any{
		"args": backgroundSummary(req.file, req.args), "cwd": baseForLog(cwd), "pid": p.pid, "taskId": id,
	})
	return id, nil
}

// processHandleObject 构造 ctx.process 返回的句柄。
func processHandleObject(st *capsState, id int) moejs.Value {
	s := st.proc
	r := st.rt.Realm()
	obj := r.NewObject()
	_ = obj.SetProp(r, r.KeyFromGoString("pid"), moejs.Number(float64(s.procPID(id))))
	_ = obj.SetProp(r, r.KeyFromGoString("taskId"), moejs.Number(float64(id)))
	_ = obj.SetProp(r, r.KeyFromGoString("write"), st.rt.Function("process.write", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		p := s.get(id)
		if p == nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "process.write: 进程不存在", nil)
		}
		p.mu.Lock()
		stdin, exited := p.stdin, p.exited
		p.mu.Unlock()
		if exited || stdin == nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "process.write: 进程已结束", nil)
		}
		if _, err := io.WriteString(stdin, moejs.Arg(args, 0).String()); err != nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "process.write: "+err.Error(), nil)
		}
		return moejs.Undefined(), nil
	}))
	_ = obj.SetProp(r, r.KeyFromGoString("read"), st.rt.Function("process.read", 1, func(rr *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		p := s.get(id)
		if p == nil {
			return moejs.Undefined(), throwCoded(rr, codeInvalidArgType, "process.read: 进程不存在", nil)
		}
		maxBytes, timeout := MaxProcessReadBytes, DefaultProcessReadTimeout
		if opts := moejs.Arg(args, 0); opts.IsObject() {
			if v, err := st.rt.Get(opts, "maxBytes"); err == nil {
				if n, ok := jsInt(v); ok && n > 0 {
					maxBytes = int(n)
				}
			}
			if v, err := st.rt.Get(opts, "timeoutMs"); err == nil {
				if n, ok := jsInt(v); ok && n > 0 {
					timeout = time.Duration(n) * time.Millisecond
				}
			}
		}
		// 句柄可能在装配期创建、运行期调用；不能用装配期 ctx 的取消信号，
		// 否则 Open 返回后一次取消就会让所有后续 read 立即失败。等待上限由
		// read 的 timeoutMs 与脚本侧请求 deadline 共同约束。
		base := context.Background()
		promise, settle := st.loop.NewPromise()
		go func() {
			view, err := p.read(base, maxBytes, timeout)
			settle(func(rt *mojsRuntime) (moejs.Value, error) {
				if err != nil {
					return moejs.Undefined(), rt.Realm().TypeError("process.read: %v", err)
				}
				return rt.FromGo(view)
			})
		}()
		return promise, nil
	}))
	_ = obj.SetProp(r, r.KeyFromGoString("kill"), st.rt.Function("process.kill", 0, func(_ *engine.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		if p := s.get(id); p != nil {
			p.kill()
		}
		return moejs.Undefined(), nil
	}))
	return engine.ObjectValue(obj)
}

func (s *processState) get(id int) *managedProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[id]
}

func (s *processState) procPID(id int) int {
	if p := s.get(id); p != nil {
		return p.pid
	}
	return 0
}

// addProcessContextMember 给 ToolContext 补 process（capability: process）。
func addProcessContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	if st.proc == nil {
		return nil
	}
	r := st.rt.Realm()
	obj := r.NewObject()
	_ = obj.SetProp(r, r.KeyFromGoString("start"), st.rt.Function("process.start", 3, func(_ *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		id, err := st.proc.startProcess(st, args)
		if err != nil {
			return moejs.Undefined(), err
		}
		promise, settle := st.loop.NewPromise()
		settle(func(rt *mojsRuntime) (moejs.Value, error) {
			return processHandleObject(st, id), nil
		})
		return promise, nil
	}))
	return put("process", engine.ObjectValue(obj))
}
