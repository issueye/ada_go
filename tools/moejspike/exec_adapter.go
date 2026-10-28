package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"
)

// 这份适配器只回答一个问题：E-5 不上游、不 fork，宿主能不能自己装出 node:child_process。
// 一次性诊断代码，不进入任何生产包。

const (
	defaultMaxBuffer = 4 << 20
	defaultTimeout   = 120 * time.Second
)

// runResult 是命令执行完的纯 Go 结果（错误对象必须在 Loop 上构造，见 buildError）。
type runResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Kind      string // "" 正常 | timeout | aborted | maxbuffer | start | exit
	ErrText   string
	MaxBuffer int
	file      string
}

func (res *runResult) failed() bool { return res.Kind != "" && res.Kind != "exit" }

// childState 跟踪在跑的 child，供取消与「进程树是否死了」取证。
type childState struct {
	mu      sync.Mutex
	live    map[int]int
	pids    map[int]int
	next    int
	cancels map[int]context.CancelFunc
	// killReq 记下「pid 还没分配就被要求杀」——原探针把 cancel 的注册放在
	// runOnce 内部，abort 与 cmd.Start() 抢跑时取消会整个丢掉，
	// E-5 的「取消杀进程树」因此测不到（实测表现为 timeout 而不是 AbortError）。
	killReq map[int]bool
}

func newChildState() *childState {
	return &childState{
		live:    map[int]int{},
		pids:    map[int]int{},
		cancels: map[int]context.CancelFunc{},
		killReq: map[int]bool{},
	}
}

func (st *childState) cancel(id int) {
	st.mu.Lock()
	cf, pid := st.cancels[id], st.pids[id]
	if pid == 0 {
		st.killReq[id] = true
	}
	st.mu.Unlock()
	if cf != nil {
		cf()
	}
	killTree(pid)
}

func (st *childState) running() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.live)
}

// killTree 在 Windows 上用 taskkill /T 连子进程一起杀，其它平台直接杀进程。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// installChildProcess 把宿主版 node:child_process 的绑定装进 Runtime。
func installChildProcess(h *hostContext, st *childState, workspace string) {
	spikeWorkspace = workspace
	h.set("__cmdName", moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		name, _, ok := execSample(moejs.Arg(args, 0).String())
		if !ok {
			return moejs.Undefined(), r.TypeError("spike: 未知取样命令")
		}
		return moejs.String(name), nil
	}))
	h.set("__cmdArgs", moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		_, argv, ok := execSample(moejs.Arg(args, 0).String())
		if !ok {
			return moejs.Undefined(), r.TypeError("spike: 未知取样命令")
		}
		return r.FromGo(argv)
	}))
	h.set("__children", moejs.NativeFunc(func(r *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
		return engine.IntValue(st.running()), nil
	}))
	binding := map[string]any{
		"execFile":     moejs.NativeFunc(childExecFile(h, st)),
		"execFileSync": moejs.NativeFunc(childExecFileSync(h, st)),
	}
	if err := h.rt.SetGlobal("__childProcess", binding); err != nil {
		h.out.setupErr = errors.Join(h.out.setupErr, fmt.Errorf("SetGlobal __childProcess: %w", err))
	}
}

// runRequest 是一次已经过宿主策略检查的执行请求。
type runRequest struct {
	file      string
	args      []string
	dir       string
	env       []string
	timeout   time.Duration
	maxBuffer int
	cancel    *int // 已分配的 child id（异步路径），同步路径为 nil

	// ctx/cancelFn 由调用方在建请求时创建：异步路径必须在把 Promise 交给脚本之前
	// 就把 cancelFn 登记进 childState，否则 abort 早于 cmd.Start() 时会丢取消。
	ctx      context.Context
	cancelFn context.CancelFunc
}

// parseRequest 落实 6.13 的命令边界：程序名白名单、无 shell、虚拟 cwd 映射、env 白名单。
func parseRequest(rt *moejs.Runtime, args []moejs.Value) (*runRequest, error) {
	r := rt.Realm()
	file, err := rt.ToGo(moejs.Arg(args, 0))
	if err != nil {
		return nil, err
	}
	name, ok := file.(string)
	if !ok {
		return nil, nodeError(r, engine.KindTypeError, "TypeError", "ERR_INVALID_ARG_TYPE", "file 必须是字符串", nil)
	}
	if !allowProgram[name] {
		return nil, nodeError(r, engine.KindError, "Error", "ERR_ACCESS_DENIED", "Run.Allow 不含程序 "+name, nil)
	}
	req := &runRequest{file: name, env: []string{}, timeout: defaultTimeout, maxBuffer: defaultMaxBuffer}
	// bug 修复：rt.ToGo 对 JS 数组给的动态类型不止一种（[]any / []string），
	// 原来的 `goArgs.([]any)` 单类型断言失败时静默丢掉整个参数数组，
	// cmd.exe 于是收到 0 个参数（打印交互 banner 后立即退出 0），
	// 导致 syncFail/maxBuffer/timeout/cancel 四条 E-5 语义全部测不到。
	// 这里改用显式的定向解码，拿不到数组就直接返回错误而不是继续跑。
	if v := moejs.Arg(args, 1); v.IsObject() {
		var list []string
		if err := rt.ToGoInto(v, &list); err != nil {
			return nil, nodeError(r, engine.KindTypeError, "TypeError", "ERR_INVALID_ARG_TYPE", "args 必须是字符串数组: "+err.Error(), nil)
		}
		req.args = list
	}
	if opts := moejs.Arg(args, 2); opts.IsObject() {
		if v, err := rt.Get(opts, "shell"); err == nil {
			if b, err := rt.ToGo(v); err == nil && truthy(b) {
				return nil, nodeError(r, engine.KindError, "Error", "ERR_ACCESS_DENIED", "ada-go 不开放 shell（Run.Shell=false）", nil)
			}
		}
		if v, err := rt.Get(opts, "cwd"); err == nil {
			if s, err := rt.ToGo(v); err == nil {
				if cwd, ok := s.(string); ok && cwd != "" {
					real, err := mapVirtualCwd(cwd)
					if err != nil {
						return nil, nodeError(r, engine.KindError, "Error", "ERR_ACCESS_DENIED", err.Error(), nil)
					}
					req.dir = real
				}
			}
		}
		if v, err := rt.Get(opts, "timeout"); err == nil {
			if n, ok := intOf(v); ok && n > 0 {
				req.timeout = time.Duration(n) * time.Millisecond
			}
		}
		if v, err := rt.Get(opts, "maxBuffer"); err == nil {
			if n, ok := intOf(v); ok && n > 0 {
				req.maxBuffer = n
			}
		}
		if v, err := rt.Get(opts, "env"); err == nil && v.IsObject() {
			var goEnv map[string]string
			if err := rt.ToGoInto(v, &goEnv); err == nil {
				for k, val := range goEnv {
					if !envAllowed(k) {
						continue
					}
					req.env = append(req.env, k+"="+val)
				}
			}
		}
	}
	return req, nil
}

// envAllowed 是 backend.env 白名单的最小形态：清单没列的一律不注入。
func envAllowed(k string) bool {
	switch k {
	case "KEEP", "PATH", "SystemRoot", "WINDIR", "COMSPEC":
		return true
	}
	return false
}

// mapVirtualCwd 把脚本给的虚拟 cwd 映射到工作区，越界拒绝。
func mapVirtualCwd(cwd string) (string, error) {
	if cwd == "/workspace" || cwd == "/" {
		return spikeWorkspace, nil
	}
	return "", fmt.Errorf("cwd %q 不在工作区内（虚拟根 /workspace）", cwd)
}

// spikeWorkspace 由探针夹具设置。
var spikeWorkspace = ""

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	case nil:
		return false
	default:
		return true
	}
}

func intOf(v engine.Value) (int, bool) {
	if n, ok := v.IsInt32(); ok {
		return int(n), true
	}
	return 0, false
}

// nodeError 在 Loop 上构造 Node 形状的 Error：name/code/killed/stdout/stderr 等字段齐备。
func nodeError(r *moejs.Realm, kind engine.ErrorKind, name string, code any, message string, extra map[string]any) error {
	v := r.NewErrorValue(kind, engine.FromGoString(message))
	o := v.AsObject()
	if name != "" {
		_ = o.SetProp(r, r.KeyFromGoString("name"), moejs.String(name))
	}
	if code != nil {
		gv, err := r.FromGo(code)
		if err == nil {
			_ = o.SetProp(r, r.KeyFromGoString("code"), gv)
		}
	}
	for k, x := range extra {
		gv, err := r.FromGo(x)
		if err != nil {
			continue
		}
		_ = o.SetProp(r, r.KeyFromGoString(k), gv)
	}
	return r.Throw(v)
}

// resultError 把纯 Go 的执行结果翻译成 Node 的失败形状（必须在 Loop 上调用）。
func resultError(r *moejs.Realm, res *runResult) error {
	switch res.Kind {
	case "timeout":
		return nodeError(r, engine.KindError, "Error", "ETIMEDOUT",
			fmt.Sprintf("命令超时（%s）: %s", res.ErrText, res.file),
			map[string]any{"killed": true, "signal": "SIGTERM", "stdout": res.Stdout, "stderr": res.Stderr})
	case "aborted":
		return nodeError(r, engine.KindError, "AbortError", "ABORT_ERR", "This operation was aborted",
			map[string]any{"killed": true, "stdout": res.Stdout, "stderr": res.Stderr})
	case "maxbuffer":
		return nodeError(r, engine.KindError, "Error", "ERR_CHILD_PROCESS_STDIO_MAXBUFFER",
			fmt.Sprintf("stdout/stderr 超过 maxBuffer %d 字节", res.MaxBuffer),
			map[string]any{"limit": res.MaxBuffer, "stdout": res.Stdout})
	case "exit":
		return nodeError(r, engine.KindError, "Error", res.ExitCode,
			fmt.Sprintf("Command failed: %s (exit %d)", res.file, res.ExitCode),
			map[string]any{"killed": false, "stdout": res.Stdout, "stderr": res.Stderr})
	default:
		return nodeError(r, engine.KindError, "Error", "ENOENT", res.ErrText, nil)
	}
}

// runOnce 执行一次命令（在别的 goroutine 上，不碰 JS）。
func runOnce(st *childState, req *runRequest) *runResult {
	res := &runResult{MaxBuffer: req.maxBuffer, file: req.file}
	ctx := req.ctx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), req.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, req.file, req.args...)
	cmd.WaitDelay = 2 * time.Second
	if req.dir != "" {
		cmd.Dir = req.dir
	}
	if len(req.env) > 0 {
		cmd.Env = req.env
	}
	out := &capWriter{limit: req.maxBuffer}
	errW := &capWriter{limit: req.maxBuffer}
	cmd.Stdout, cmd.Stderr = out, errW
	id := -1
	if req.cancel != nil {
		id = *req.cancel
		defer func() {
			st.mu.Lock()
			delete(st.live, id)
			delete(st.cancels, id)
			delete(st.pids, id)
			delete(st.killReq, id)
			st.mu.Unlock()
		}()
	}
	if err := cmd.Start(); err != nil {
		// 取消也可能落在 Start 之前（exec.CommandContext 先看 ctx）：
		// 这时要报 aborted/timeout，而不是把「context canceled」当成启动失败（ENOENT）。
		if c := ctx.Err(); c != nil {
			res.Kind = "aborted"
			res.ErrText = c.Error()
			if errors.Is(c, context.DeadlineExceeded) {
				res.Kind, res.ErrText = "timeout", req.timeout.String()
			}
			return res
		}
		res.Kind, res.ErrText = "start", err.Error()
		return res
	}
	if id >= 0 {
		st.mu.Lock()
		st.live[id] = cmd.Process.Pid
		st.pids[id] = cmd.Process.Pid
		earlyKill := st.killReq[id]
		st.mu.Unlock()
		if earlyKill {
			// abort 早于 Start：此刻才拿到 pid，补一次杀进程树。
			if req.cancelFn != nil {
				req.cancelFn()
			}
			killTree(cmd.Process.Pid)
		}
	}
	waitErr := cmd.Wait()
	res.Stdout, res.Stderr = out.String(), errW.String()
	switch {
	case out.over || errW.over:
		res.Kind = "maxbuffer"
	case ctx.Err() == context.DeadlineExceeded:
		res.Kind, res.ErrText = "timeout", req.timeout.String()
		killTree(cmd.Process.Pid)
	case ctx.Err() == context.Canceled:
		res.Kind = "aborted"
		killTree(cmd.Process.Pid)
	case waitErr != nil:
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			res.ExitCode, res.Kind = ee.ExitCode(), "exit"
		} else {
			res.Kind, res.ErrText = "start", waitErr.Error()
		}
	}
	return res
}

// capWriter 是带 maxBuffer 的输出缓冲。
type capWriter struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.limit {
		w.over = true
		if keep := w.limit - w.buf.Len(); keep > 0 {
			w.buf.Write(p[:keep])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *capWriter) String() string { return w.buf.String() }

// valueOf 把执行结果转成 JS 对象（Node 的 {stdout, stderr, code, killed}）。
func valueOf(r *moejs.Realm, res *runResult) (moejs.Value, error) {
	return r.FromGo(map[string]any{
		"stdout": res.Stdout, "stderr": res.Stderr, "code": res.ExitCode, "killed": false,
	})
}

func childExecFileSync(h *hostContext, st *childState) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		req, err := parseRequest(h.rt, args)
		if err != nil {
			return moejs.Undefined(), err
		}
		ctx, cancel := context.WithTimeout(context.Background(), req.timeout)
		defer cancel()
		req.ctx, req.cancelFn = ctx, cancel
		res := runOnce(st, req)
		if res.failed() || res.Kind == "exit" {
			return moejs.Undefined(), resultError(r, res)
		}
		return valueOf(r, res)
	}
}

func childExecFile(h *hostContext, st *childState) moejs.NativeFunc {
	return func(r *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		req, err := parseRequest(h.rt, args)
		if err != nil {
			return moejs.Undefined(), err
		}
		p, settle := h.loop.NewPromise()
		st.mu.Lock()
		id := st.next
		st.next++
		st.live[id] = 0
		st.mu.Unlock()
		req.cancel = &id
		// context 在把 Promise 交给脚本之前建好并登记：脚本可能在下一个语句就 abort，
		// 那时 runOnce 甚至还没 Start，pid 还不存在（childState.killReq 兜住这一窗口）。
		ctx, cancel := context.WithTimeout(context.Background(), req.timeout)
		req.ctx, req.cancelFn = ctx, cancel
		st.mu.Lock()
		st.cancels[id] = cancel
		st.mu.Unlock()
		if opts := moejs.Arg(args, 2); opts.IsObject() {
			if sig, err := h.rt.Get(opts, "signal"); err == nil && sig.IsObject() {
				if aborted, err := h.rt.Get(sig, "aborted"); err == nil {
					if b, _ := h.rt.ToGo(aborted); truthy(b) {
						st.mu.Lock()
						delete(st.live, id)
						delete(st.cancels, id)
						st.mu.Unlock()
						cancel() // 这条路径不会启动工作协程，就地释放 context
						go settle(func(rt *moejs.Runtime) (moejs.Value, error) {
							return moejs.Undefined(), nodeError(rt.Realm(), engine.KindError, "AbortError", "ABORT_ERR", "This operation was aborted", nil)
						})
						return p, nil
					}
				}
				if add, err := h.rt.Get(sig, "addEventListener"); err == nil && engine.IsCallable(add) {
					handler := h.rt.Function("__onAbort", 0, func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
						st.cancel(id)
						return moejs.Undefined(), nil
					})
					if _, err := h.rt.CallFunction(add, sig, moejs.String("abort"), handler); err != nil {
						return moejs.Undefined(), err
					}
				}
			}
		}
		go func() {
			// cancel 必须在工作协程里释放：NativeFunc 返回时子进程还在跑，
			// 放在外层会立刻把刚启动的命令杀掉。
			defer cancel()
			res := runOnce(st, req)
			settle(func(rt *moejs.Runtime) (moejs.Value, error) {
				if res.failed() || res.Kind == "exit" {
					return moejs.Undefined(), resultError(rt.Realm(), res)
				}
				return valueOf(rt.Realm(), res)
			})
		}()
		return p, nil
	}
}
