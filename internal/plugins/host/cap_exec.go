package host

// 本文件是 node:child_process 的宿主实现（12 §6.11.1 E-5、6.13）：
//
//   - 只有 execFile / execFileSync：参数数组直接交给 os/exec，**不经过 shell**；
//     spawn/fork/exec/execSync 不存在（12 §6.12.2 偏差列），不是「存在但能绕过」。
//   - 边界：cwd 只能是工作区虚拟根下、env 只注入清单白名单、maxBuffer 夹到 4MB、
//     timeout 夹到 120s、取消杀整棵进程树。
//   - 每次执行都落一条审计；程序级白名单由产品装配层的 ExecPolicy 注入（Run.Allow 的落点），
//     本包不内置具体程序名单（基座不替产品决定能跑什么）。
//
// 这份实现照 tools/moejspike 的 E-5 尖刺（九条语义全部实测通过），
// 两处已知坑保留注释：参数数组必须定向解码（不能单断言 []any）；
// context/cancel 必须在把 Promise 交给脚本之前登记（否则 abort 早于 Start 会丢取消）。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/engine"

	"github.com/adag/ada-go/internal/plugins"
)

// childProcessShim 是 node:child_process 的模块体：只读 import.meta.binding。
const childProcessShim = `const b = import.meta.binding;
if (b === undefined) throw new Error("Cannot import \"node:child_process\": the host did not install it in this runtime");
export const execFile = b.execFile;
export const execFileSync = b.execFileSync;
export default b;
`

// ExecPolicy 是程序级执行策略（Run.Allow 的落点，设计 6.13）。
//
// 返回非 nil 即拒绝；nil 表示放行。产品装配层（命令工具插件或审批策略插件）
// 注入它来限制「能跑什么」；基座只保证「怎么跑」——无 shell、cwd 不越界、有上限、有审计。
var ExecPolicy func(program string, args []string) error

// execState 跟踪本 Runtime 正在跑的子进程（取消与关闭都要能杀整棵树）。
type execState struct {
	mu      sync.Mutex
	live    map[int]int
	pids    map[int]int
	next    int
	cancels map[int]context.CancelFunc
	// killReq 记「pid 还没分配就被要求杀」：abort 与 Start 抢跑时不能把取消丢掉。
	killReq map[int]bool
	// bg 是后台任务表：与同步命令分开记账，任务在轮次结束后继续跑。
	bg     map[int]*backgroundTask
	bgNext int
	// closed 由 shutdown 置位：关闭后不再接受新的后台任务。
	closed bool
}

func newExecState() *execState {
	return &execState{
		live:    map[int]int{},
		pids:    map[int]int{},
		cancels: map[int]context.CancelFunc{},
		killReq: map[int]bool{},
		bg:      map[int]*backgroundTask{},
		bgNext:  1,
	}
}

// shutdown 结束全部在跑的命令（CloseCapabilities 与 Runtime 关闭都走这里）。
func (e *execState) shutdown() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	e.closed = true
	cancels := make([]context.CancelFunc, 0, len(e.cancels))
	for _, cancel := range e.cancels {
		cancels = append(cancels, cancel)
	}
	pids := make([]int, 0, len(e.pids))
	for _, pid := range e.pids {
		pids = append(pids, pid)
	}
	bg := make([]*backgroundTask, 0, len(e.bg))
	for _, task := range e.bg {
		bg = append(bg, task)
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, pid := range pids {
		killProcessTree(pid)
	}
	for _, task := range bg {
		task.kill()
	}
	return nil
}

func (e *execState) running() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.live)
}

func (e *execState) cancel(id int) {
	e.mu.Lock()
	cancel, pid := e.cancels[id], e.pids[id]
	if pid == 0 {
		e.killReq[id] = true
	}
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	killProcessTree(pid)
}

// installExec 装 node:child_process（能力位 exec + 至少 verified，矩阵已判过一遍）。
func (st *capsState) installExec() error {
	if !st.caps.Has(plugins.CapExec) {
		return nil // 未声明 exec：模块由 ModulePolicy 在链接期拒绝，这里不装绑定
	}
	if !trustAtLeast(st.caps.Trust, plugins.TrustVerified) {
		return nil
	}
	ex := newExecState()
	r := st.rt.Realm()
	rt := st.rt

	binding := r.NewObject()
	for _, name := range []string{"execFile", "execFileSync"} {
		var fn engine.NativeFunc
		if name == "execFile" {
			fn = st.execFile(ex)
		} else {
			fn = st.execFileSync(ex)
		}
		if err := binding.SetProp(r, r.KeyFromGoString(name), rt.Function("child_process."+name, 3, fn)); err != nil {
			return err
		}
	}
	st.ex = ex
	st.child = engine.ObjectValue(binding)
	return nil
}

// execRequest 是一次已过策略检查的执行。
type execRequest struct {
	file      string
	args      []string
	dir       string
	env       []string
	timeout   time.Duration
	maxBuffer int

	ctx      context.Context
	cancelFn context.CancelFunc
	id       int // 已登记进 execState 的 id（同步路径 -1）
}

// parseExecRequest 落实 6.13 的命令边界（同步命令的默认期限）。
func (st *capsState) parseExecRequest(rt *mojsRuntime, args []moejs.Value) (*execRequest, error) {
	return st.parseExecRequestWithTimeout(rt, args, st.limits.MaxExecTimeout)
}

// parseExecRequestWithTimeout 是 parseExecRequest 的可变期限版本：
// 后台任务允许比同步命令更长的生命周期，但仍必须有上限。
func (st *capsState) parseExecRequestWithTimeout(rt *mojsRuntime, args []moejs.Value, maxTimeout time.Duration) (*execRequest, error) {
	r := rt.Realm()
	limits := st.limits
	if maxTimeout <= 0 {
		maxTimeout = limits.MaxExecTimeout
	}
	req := &execRequest{maxBuffer: limits.MaxExecOutputBytes, timeout: maxTimeout, id: -1}

	fileV := moejs.Arg(args, 0)
	if !fileV.IsString() {
		return nil, throwCoded(r, codeInvalidArgType, "execFile(file, args, opts): file 必须是字符串",
			map[string]any{"file": fileV.String()})
	}
	req.file = fileV.String()
	if strings.TrimSpace(req.file) == "" {
		return nil, throwCoded(r, codeInvalidArgType, "execFile(file, args, opts): file 不能为空", nil)
	}
	// 参数数组必须定向解码：rt.ToGo 对 JS 数组的动态类型不止一种（尖刺踩过）。
	if v := moejs.Arg(args, 1); v.IsObject() {
		var list []string
		if err := rt.ToGoInto(v, &list); err != nil {
			return nil, throwCoded(r, codeInvalidArgType, "execFile(file, args, opts): args 必须是字符串数组: "+err.Error(), nil)
		}
		req.args = list
	}
	if opts := moejs.Arg(args, 2); opts.IsObject() {
		if v, err := rt.Get(opts, "shell"); err == nil && !v.IsNullish() {
			if b, ok := truthyJS(v); ok && b {
				return nil, throwCoded(r, codeAccessDenied,
					"ada-go 不开放 shell（Run.Shell=false）：execFile 的参数数组直接交给 os/exec", nil)
			}
		}
		if v, err := rt.Get(opts, "cwd"); err == nil && !v.IsNullish() {
			real, cerr := st.fsRealPath(v.String())
			if cerr != nil {
				return nil, cerr
			}
			req.dir = real
		}
		if v, err := rt.Get(opts, "timeout"); err == nil {
			if n, ok := jsInt(v); ok && n > 0 {
				req.timeout = time.Duration(n) * time.Millisecond
			}
		} else if v, err := rt.Get(opts, "timeoutMs"); err == nil {
			// ctx.execBackground 用 timeoutMs（与 ctx.exec 一致）；node:child_process
			// 的调用方用 timeout。两者都接受，上限分别由调用方传入。
			if n, ok := jsInt(v); ok && n > 0 {
				req.timeout = time.Duration(n) * time.Millisecond
			}
		}
		if v, err := rt.Get(opts, "maxBuffer"); err == nil {
			if n, ok := jsInt(v); ok && n > 0 {
				req.maxBuffer = int(n)
			}
		}
		if v, err := rt.Get(opts, "env"); err == nil && v.IsObject() {
			var want map[string]string
			if err := rt.ToGoInto(v, &want); err == nil {
				names := make([]string, 0, len(want))
				for name := range want {
					names = append(names, name)
				}
				sortStrings(names)
				for _, name := range names {
					// 只注入清单 backend.env 白名单里、且调用方明确给值的名字。
					if allowed, ok := st.caps.Env[name]; ok {
						req.env = append(req.env, name+"="+allowed)
					}
				}
			}
		}
	}
	if req.timeout <= 0 || req.timeout > maxTimeout {
		req.timeout = maxTimeout
	}
	if req.maxBuffer <= 0 || req.maxBuffer > limits.MaxExecOutputBytes {
		req.maxBuffer = limits.MaxExecOutputBytes
	}
	if ExecPolicy != nil {
		if err := ExecPolicy(req.file, req.args); err != nil {
			return nil, throwCoded(r, codeAccessDenied, "执行策略拒绝: "+err.Error(),
				map[string]any{"program": baseForLog(req.file)})
		}
	}
	return req, nil
}

// runResult 是命令执行完的纯 Go 结果（错误对象必须在 Loop 上构造）。
type runResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Kind      string // "" 正常 | exit 非零 | timeout | aborted | maxbuffer | start
	ErrText   string
	MaxBuffer int
	file      string
}

func (res *runResult) failed() bool { return res.Kind != "" && res.Kind != "exit" }

// run 执行一次命令（在别的 goroutine 上，不碰 JS）。
func (e *execState) run(req *execRequest) *runResult {
	res := &runResult{MaxBuffer: req.maxBuffer, file: req.file}
	ctx := req.ctx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), req.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, req.file, req.args...)
	cmd.WaitDelay = 2 * time.Second
	configureProcessTree(cmd)
	if req.dir != "" {
		cmd.Dir = req.dir
	}
	if len(req.env) > 0 {
		cmd.Env = req.env
	}
	out := &capWriter{limit: req.maxBuffer}
	errW := &capWriter{limit: req.maxBuffer}
	cmd.Stdout, cmd.Stderr = out, errW

	id := req.id
	if id >= 0 {
		defer func() {
			e.mu.Lock()
			delete(e.live, id)
			delete(e.cancels, id)
			delete(e.pids, id)
			delete(e.killReq, id)
			e.mu.Unlock()
		}()
	}
	if err := cmd.Start(); err != nil {
		// 取消也可能落在 Start 之前：这时报 timeout/aborted，而不是把
		// "context canceled" 当成启动失败（ENOENT）。
		if c := ctx.Err(); c != nil {
			res.Kind, res.ErrText = "aborted", c.Error()
			if errors.Is(c, context.DeadlineExceeded) {
				res.Kind, res.ErrText = "timeout", req.timeout.String()
			}
			return res
		}
		res.Kind, res.ErrText = "start", err.Error()
		return res
	}
	if id >= 0 {
		e.mu.Lock()
		pid := cmd.Process.Pid
		e.live[id] = pid
		e.pids[id] = pid
		early := e.killReq[id]
		e.mu.Unlock()
		if early {
			if req.cancelFn != nil {
				req.cancelFn()
			}
			killProcessTree(pid)
		}
	}
	waitErr := cmd.Wait()
	res.Stdout, res.Stderr = out.String(), errW.String()
	switch {
	case out.Over() || errW.Over():
		res.Kind = "maxbuffer"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Kind, res.ErrText = "timeout", req.timeout.String()
		killProcessTree(cmd.Process.Pid)
	case errors.Is(ctx.Err(), context.Canceled):
		res.Kind = "aborted"
		killProcessTree(cmd.Process.Pid)
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

// capWriter 是带 maxBuffer 的输出缓冲（超限后保留已写入的部分）。
//
// 后台任务会在 checkTask 读取的同时由 cmd.Wait 侧写入，因此必须自带锁；
// total 记录截断前的真实字节数，供检查接口报告。
type capWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	over  bool
	total int
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += len(p)
	if w.buf.Len()+len(p) > w.limit {
		w.over = true
		if keep := w.limit - w.buf.Len(); keep > 0 {
			_, _ = w.buf.Write(p[:keep])
		}
		return len(p), nil
	}
	return w.buf.Write(p)
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// Stats 返回当前已保留的输出、是否截断、截断前的总字节数。
func (w *capWriter) Stats() (string, bool, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String(), w.over, w.total
}

// Over 报告是否发生过截断（并发安全）。
func (w *capWriter) Over() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.over
}

// execFileSync 是同步路径：直接在 Loop 上跑完（不返回 Promise）。
func (st *capsState) execFileSync(ex *execState) engine.NativeFunc {
	return func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		req, err := st.parseExecRequest(st.rt, args)
		if err != nil {
			return moejs.Undefined(), err
		}
		ctx, cancel := context.WithTimeout(st.signalOf(), req.timeout)
		defer cancel()
		req.ctx, req.cancelFn = ctx, cancel
		res := ex.run(req)
		st.auditExec(req, res)
		if res.failed() || res.Kind == "exit" {
			return moejs.Undefined(), execError(r, res)
		}
		return execValue(r, res)
	}
}

// execFile 是异步路径：返回 Promise，工作协程跑命令，结果回 Loop 结算。
func (st *capsState) execFile(ex *execState) engine.NativeFunc {
	return func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		req, err := st.parseExecRequest(st.rt, args)
		if err != nil {
			return moejs.Undefined(), err
		}
		// 绑定脚本给的 signal：abort 时杀进程树（取消同时经宿主 context 贯穿）。
		base := st.signalOf()
		if opts := moejs.Arg(args, 2); opts.IsObject() {
			if sig, err := st.rt.Get(opts, "signal"); err == nil && sig.IsObject() {
				if v, err := st.rt.Get(sig, "aborted"); err == nil {
					if b, ok := truthyJS(v); ok && b {
						return rejectedPromise(st, "AbortError", codeAbort, "This operation was aborted")
					}
				}
			}
		}
		ctx, cancel := context.WithTimeout(base, req.timeout)
		req.ctx, req.cancelFn = ctx, cancel
		ex.mu.Lock()
		id := ex.next
		ex.next++
		ex.live[id] = 0
		ex.cancels[id] = cancel
		ex.mu.Unlock()
		req.id = id

		promise, settle := st.loop.NewPromise()
		go func() {
			defer cancel()
			res := ex.run(req)
			st.auditExec(req, res)
			settle(func(rt *mojsRuntime) (moejs.Value, error) {
				if res.failed() || res.Kind == "exit" {
					return moejs.Undefined(), execError(rt.Realm(), res)
				}
				return execValue(rt.Realm(), res)
			})
		}()
		return promise, nil
	}
}

// rejectedPromise 返回一个已经 reject 的 Promise（abort 早于启动时用）。
func rejectedPromise(st *capsState, name, code, msg string) (moejs.Value, error) {
	promise, settle := st.loop.NewPromise()
	go settle(func(rt *mojsRuntime) (moejs.Value, error) {
		r := rt.Realm()
		return moejs.Undefined(), throwExecError(r, name, code, msg, nil)
	})
	return promise, nil
}

// auditExec 记一条命令审计（程序、参数摘要、字节数、结果）。
func (st *capsState) auditExec(req *execRequest, res *runResult) {
	result := "ok"
	if res.Kind != "" && res.Kind != "exit" {
		result = res.Kind
	}
	st.audit("child_process", "execFile", req.file, len(res.Stdout)+len(res.Stderr), result, map[string]any{
		"args":   strings.Join(req.args, " "),
		"exit":   res.ExitCode,
		"cwd":    baseForLog(req.dir),
		"output": len(res.Stdout) + len(res.Stderr),
	})
}

// execError 把纯 Go 结果翻译成 Node 的失败形状（必须在 Loop 上调用）。
func execError(r *engine.Realm, res *runResult) error {
	switch res.Kind {
	case "timeout":
		return throwExecError(r, "Error", codeTimeout,
			fmt.Sprintf("命令超时（%s）: %s", res.ErrText, baseForLog(res.file)),
			map[string]any{"killed": true, "signal": "SIGTERM", "stdout": res.Stdout, "stderr": res.Stderr})
	case "aborted":
		return throwExecError(r, "AbortError", codeAbort, "This operation was aborted",
			map[string]any{"killed": true, "stdout": res.Stdout, "stderr": res.Stderr})
	case "maxbuffer":
		return throwExecError(r, "Error", codeMaxBuffer,
			fmt.Sprintf("stdout/stderr 超过 maxBuffer %d 字节", res.MaxBuffer),
			map[string]any{"limit": res.MaxBuffer, "stdout": res.Stdout})
	case "exit":
		return throwExecError(r, "Error", res.ExitCode,
			fmt.Sprintf("Command failed: %s (exit %d)", baseForLog(res.file), res.ExitCode),
			map[string]any{"killed": false, "stdout": res.Stdout, "stderr": res.Stderr})
	default:
		return throwExecError(r, "Error", "ENOENT", res.ErrText, nil)
	}
}

// throwExecError 构造带 name/code 的 Error；code 可以是字符串，也可以是数字退出码。
func throwExecError(r *engine.Realm, name string, code any, message string, extra map[string]any) error {
	v := r.NewErrorValue(engine.KindError, engine.FromGoString(message))
	o := v.AsObject()
	if o == nil {
		return r.Throw(v)
	}
	_ = o.SetProp(r, r.KeyFromGoString("name"), moejs.String(name))
	if gv, err := r.FromGo(code); err == nil {
		_ = o.SetProp(r, r.KeyFromGoString("code"), gv)
	}
	for k, val := range extra {
		gv, err := r.FromGo(val)
		if err != nil {
			continue
		}
		_ = o.SetProp(r, r.KeyFromGoString(k), gv)
	}
	return r.Throw(v)
}

// execValue 把结果转成 Node 的 {stdout, stderr, code, killed}。
func execValue(r *engine.Realm, res *runResult) (moejs.Value, error) {
	return r.FromGo(map[string]any{
		"stdout": res.Stdout, "stderr": res.Stderr, "code": res.ExitCode, "killed": false,
	})
}

// truthyJS 读一个 JS 布尔（数字/字符串按 JS 的真值规则）。
func truthyJS(v moejs.Value) (bool, bool) {
	if v.IsUndefined() || v.IsNull() {
		return false, true
	}
	if n, ok := v.IsInt32(); ok {
		return n != 0, true
	}
	if v.IsNumber() {
		return v.AsNumber() != 0, true
	}
	if v.IsString() {
		return v.String() != "", true
	}
	return true, true // 对象/数组：真值
}

// addExecContextMember 给 ToolContext 补 exec / 后台任务四件套
// （05 §6.3；能力位 exec，仅 verified/builtin）。
func addExecContextMember(st *capsState, hc Context, put func(string, moejs.Value) error) error {
	ex := st.ex
	if ex == nil || !st.caps.Has(plugins.CapExec) {
		return nil
	}
	if err := put("exec", st.rt.Function("exec", 3, func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		cmd := moejs.Arg(args, 0).String()
		var argv []string
		if v := moejs.Arg(args, 1); v.IsObject() {
			if err := st.rt.ToGoInto(v, &argv); err != nil {
				return moejs.Undefined(), throwCoded(r, codeInvalidArgType, "ctx.exec(cmd, args): args 必须是字符串数组", nil)
			}
		}
		if ExecPolicy != nil {
			if err := ExecPolicy(cmd, argv); err != nil {
				return moejs.Undefined(), throwCoded(r, codeAccessDenied, "执行策略拒绝: "+err.Error(), nil)
			}
		}
		req := &execRequest{file: cmd, args: argv, maxBuffer: st.limits.MaxExecOutputBytes, timeout: st.limits.MaxExecTimeout, id: -1}
		if opts := moejs.Arg(args, 2); opts.IsObject() {
			if v, err := st.rt.Get(opts, "cwd"); err == nil && !v.IsNullish() {
				real, cerr := st.fsRealPath(v.String())
				if cerr != nil {
					return moejs.Undefined(), cerr
				}
				req.dir = real
			}
			if v, err := st.rt.Get(opts, "timeoutMs"); err == nil {
				if n, ok := jsInt(v); ok && n > 0 {
					req.timeout = time.Duration(n) * time.Millisecond
					if req.timeout > st.limits.MaxExecTimeout {
						req.timeout = st.limits.MaxExecTimeout
					}
				}
			}
		}
		base := hc.Signal
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithTimeout(base, req.timeout)
		req.ctx, req.cancelFn = ctx, cancel
		promise, settle := st.loop.NewPromise()
		go func() {
			defer cancel()
			res := ex.run(req)
			st.auditExec(req, res)
			settle(func(rt *mojsRuntime) (moejs.Value, error) {
				out := map[string]any{"code": res.ExitCode, "stdout": res.Stdout, "stderr": res.Stderr}
				if res.failed() {
					out["error"] = res.Kind
				}
				return rt.FromGo(out)
			})
		}()
		return promise, nil
	})); err != nil {
		return err
	}

	// 后台任务：与 ctx.exec 共用同一条命令边界；任务生命周期独立于当前轮次，
	// 由 checkTask/waitTask/killTask 管理，插件 Runtime 关闭时统一杀掉。
	if err := put("execBackground", st.rt.Function("execBackground", 3, func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		req, err := st.parseExecRequestWithTimeout(st.rt, args, st.limits.MaxBackgroundTimeout)
		if err != nil {
			return moejs.Undefined(), err
		}
		ctx, cancel := context.WithTimeout(context.Background(), req.timeout)
		req.ctx, req.cancelFn = ctx, cancel
		task, err := ex.startBackground(req, st.limits.MaxBackgroundTasks)
		if err != nil {
			cancel()
			return moejs.Undefined(), throwCoded(r, codeOutOfRange, "ctx.execBackground: "+err.Error(), nil)
		}
		st.audit("child_process", "execBackground", req.file, 0, "started", map[string]any{
			"args":   backgroundSummary(req.file, req.args),
			"cwd":    baseForLog(req.dir),
			"taskId": task.id,
			"pid":    task.pid,
		})
		promise, settle := st.loop.NewPromise()
		settle(func(rt *mojsRuntime) (moejs.Value, error) {
			return rt.FromGo(map[string]any{"taskId": task.id, "pid": task.pid})
		})
		return promise, nil
	})); err != nil {
		return err
	}
	if err := put("checkTask", st.rt.Function("checkTask", 1, func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		id, ok := jsInt(moejs.Arg(args, 0))
		if !ok || id <= 0 {
			return moejs.Undefined(), throwCoded(r, codeInvalidArgType, "ctx.checkTask(taskId): taskId 必须是正整数", nil)
		}
		view, found := ex.checkBackground(int(id))
		if !found {
			return moejs.Undefined(), throwCoded(r, codeOutOfRange, fmt.Sprintf("ctx.checkTask(%d): 没有这个后台任务", id), nil)
		}
		return st.rt.FromGo(view)
	})); err != nil {
		return err
	}
	// waitTask 是 checkTask 的阻塞版本：等到终态或等待预算耗尽再返回，
	// 让工具层一次调用拿到结果，而不是让模型反复轮询。
	if err := put("waitTask", st.rt.Function("waitTask", 2, func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		id, ok := jsInt(moejs.Arg(args, 0))
		if !ok || id <= 0 {
			return moejs.Undefined(), throwCoded(r, codeInvalidArgType, "ctx.waitTask(taskId, timeoutMs): taskId 必须是正整数", nil)
		}
		timeout := DefaultWaitTaskTimeout
		if v := moejs.Arg(args, 1); !v.IsUndefined() && !v.IsNull() {
			n, ok := jsInt(v)
			if !ok || n <= 0 {
				return moejs.Undefined(), throwCoded(r, codeInvalidArgType, "ctx.waitTask(taskId, timeoutMs): timeoutMs 必须是正整数", nil)
			}
			timeout = time.Duration(n) * time.Millisecond
			if timeout > MaxWaitTaskTimeout {
				timeout = MaxWaitTaskTimeout
			}
		}
		ctx := st.signalOf()
		promise, settle := st.loop.NewPromise()
		go func() {
			started := time.Now()
			view, found, timedOut, werr := ex.waitBackground(ctx, int(id), timeout)
			waited := time.Since(started).Milliseconds()
			settle(func(rt *mojsRuntime) (moejs.Value, error) {
				if werr != nil {
					return moejs.Undefined(), throwCoded(rt.Realm(), codeAbort, "ctx.waitTask: 已取消", nil)
				}
				if !found {
					return moejs.Undefined(), throwCoded(rt.Realm(), codeOutOfRange,
						fmt.Sprintf("ctx.waitTask(%d): 没有这个后台任务", id), nil)
				}
				view["timedOut"] = timedOut
				view["waitedMs"] = waited
				return rt.FromGo(view)
			})
		}()
		return promise, nil
	})); err != nil {
		return err
	}
	if err := put("killTask", st.rt.Function("killTask", 1, func(r *engine.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		id, ok := jsInt(moejs.Arg(args, 0))
		if !ok || id <= 0 {
			return moejs.Undefined(), throwCoded(r, codeInvalidArgType, "ctx.killTask(taskId): taskId 必须是正整数", nil)
		}
		found, killed := ex.killBackground(int(id))
		if found {
			st.audit("child_process", "killTask", fmt.Sprintf("#%d", id), 0, "killed", map[string]any{
				"taskId": id, "killed": killed,
			})
		}
		return st.rt.FromGo(map[string]any{"taskId": id, "found": found, "killed": killed})
	})); err != nil {
		return err
	}
	return nil
}

// fsRealPath 在 cap_fs.go 里实现（同一份工作区根与虚拟挂载点）：绝对虚拟路径
// 映射到工作区内的真实路径，越界或不存在按 ERR_ACCESS_DENIED。
