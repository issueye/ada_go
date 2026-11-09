package host

// 后台命令：ctx.execBackground / ctx.checkTask / ctx.waitTask / ctx.killTask 的宿主实现。
//
// 语义（12 §6.13、02 §5.4）：
//   - 后台任务与同步 execFile 走同一份命令边界：无 shell、程序策略、cwd scope、
//     env 白名单、输出截断、进程树取消；
//   - 任务在轮次结束后继续跑，用 taskId 轮询/等待/终止；插件 Runtime 关闭时全部杀掉；
//   - 生命周期有上限（MaxBackgroundTimeout），超时杀整棵进程树；
//   - 每个 Runtime 的任务数有上限，已结束任务在下次启动时回收。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	bgRunning = "running"
	bgExited  = "exited"
	bgTimeout = "timeout"
	bgKilled  = "killed"
	bgFailed  = "failed"

	// bgCheckOutputBytes 是 checkTask 单路输出返回上限：工具回执最终还会按
	// 50KB/2000 行截断，这里先取尾部 64KB，避免把 4MB 缓冲塞进一次 RPC。
	bgCheckOutputBytes = 64 << 10

	// DefaultWaitTaskTimeout / MaxWaitTaskTimeout 是 waitTask 单次等待的缺省值与上限。
	// 上限低于工具默认预算（120s），给取消与回执留出余量。
	DefaultWaitTaskTimeout = 30 * time.Second
	MaxWaitTaskTimeout     = 60 * time.Second
)

// backgroundTask 是一个已启动的后台命令。
type backgroundTask struct {
	mu        sync.Mutex
	id        int
	pid       int
	startedAt time.Time
	endedAt   time.Time
	status    string
	exitCode  int
	errText   string
	killed    bool
	truncated bool
	stdout    *capWriter
	stderr    *capWriter
	cancel    context.CancelFunc
	// doneCh 在任务进入终态时关闭，供 waitTask 阻塞等待（不要在锁外 close）。
	doneCh chan struct{}
}

// startBackground 启动一条后台命令。调用方负责已经通过 parseExecRequestWithTimeout
// 完成能力、cwd、env 与程序策略检查；这里的 req.ctx 应为独立于轮次的生命周期 context。
func (e *execState) startBackground(req *execRequest, maxTasks int) (*backgroundTask, error) {
	if e == nil {
		return nil, errors.New("execState 为空")
	}
	if maxTasks <= 0 {
		maxTasks = 1
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("execState 已关闭：不能再启动后台任务")
	}
	if e.bg == nil {
		e.bg = map[int]*backgroundTask{}
	}
	// 先回收已结束任务；仍满则拒绝，避免无界内存增长。
	for id, task := range e.bg {
		if task.done() {
			delete(e.bg, id)
		}
	}
	if len(e.bg) >= maxTasks {
		e.mu.Unlock()
		return nil, fmt.Errorf("后台任务数达到上限 %d（先 kill_task 或等待结束）", maxTasks)
	}
	id := e.bgNext
	e.bgNext++
	e.mu.Unlock()

	ctx := req.ctx
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), req.timeout)
		req.cancelFn = cancel
	}
	cmd := exec.CommandContext(ctx, req.file, req.args...)
	cmd.WaitDelay = 2 * time.Second
	configureProcessTree(cmd)
	if req.dir != "" {
		cmd.Dir = req.dir
	}
	// 与同步 execFile 同一份环境语义：继承/白名单 + 环境变量模块的用户变量。
	// 同样先摘掉宿主内部变量（后台任务最容易被误当成「干净环境」）。
	baseEnv := req.env
	if len(baseEnv) == 0 {
		baseEnv = stripHostEnv(os.Environ())
	}
	cmd.Env = applyBinPath(mergeEnv(baseEnv, req.baseEnv), req.binDirs)
	stdout := &capWriter{limit: req.maxBuffer}
	stderr := &capWriter{limit: req.maxBuffer}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	if err := cmd.Start(); err != nil {
		if req.cancelFn != nil {
			req.cancelFn()
		}
		return nil, err
	}

	task := &backgroundTask{
		id:        id,
		pid:       cmd.Process.Pid,
		startedAt: time.Now().UTC(),
		status:    bgRunning,
		stdout:    stdout,
		stderr:    stderr,
		cancel:    req.cancelFn,
		doneCh:    make(chan struct{}),
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		if task.cancel != nil {
			task.cancel()
		}
		killProcessTree(task.pid)
		return nil, errors.New("execState 已关闭：不能再启动后台任务")
	}
	e.bg[id] = task
	e.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		// 超时/取消时 CommandContext 只保证直接子进程退出；再向整个进程组
		// 补一刀，覆盖命令自己派生的孙进程。
		if ctx.Err() != nil {
			killProcessTree(task.pid)
		}
		task.finish(ctx, waitErr)
	}()
	return task, nil
}

// finish 收敛任务终态。
func (t *backgroundTask) finish(ctx context.Context, waitErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.endedAt = time.Now().UTC()
	t.truncated = t.stdout.Over() || t.stderr.Over()
	switch {
	case t.killed:
		t.status = bgKilled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		t.status = bgTimeout
		t.errText = ctx.Err().Error()
	case errors.Is(ctx.Err(), context.Canceled):
		t.status = bgKilled
	case waitErr != nil:
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			t.status = bgExited
			t.exitCode = ee.ExitCode()
		} else {
			t.status = bgFailed
			t.errText = waitErr.Error()
		}
	default:
		t.status = bgExited
	}
	if t.doneCh != nil {
		close(t.doneCh)
	}
}

// done 报告任务是否已到终态（用 status，而不是 endedAt，防止竞态窗口）。
func (t *backgroundTask) done() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status != bgRunning
}

// kill 取消并杀整棵进程树；返回是否真的从 running 变为 killed。
func (t *backgroundTask) kill() bool {
	t.mu.Lock()
	if t.status != bgRunning {
		t.mu.Unlock()
		return false
	}
	t.killed = true
	pid := t.pid
	cancel := t.cancel
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	killProcessTree(pid)
	return true
}

// view 生成给脚本的只读快照；单路输出超过 bgCheckOutputBytes 时取尾部。
func (t *backgroundTask) view() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()

	stdout, outOver, outTotal := t.stdout.Stats()
	stderr, errOver, errTotal := t.stderr.Stats()
	truncated := t.truncated || outOver || errOver
	if len(stdout) > bgCheckOutputBytes {
		stdout = tailUTF8(stdout, bgCheckOutputBytes)
		truncated = true
	}
	if len(stderr) > bgCheckOutputBytes {
		stderr = tailUTF8(stderr, bgCheckOutputBytes)
		truncated = true
	}

	out := map[string]any{
		"taskId":      t.id,
		"pid":         t.pid,
		"status":      t.status,
		"code":        t.exitCode,
		"stdout":      stdout,
		"stderr":      stderr,
		"truncated":   truncated,
		"stdoutBytes": outTotal,
		"stderrBytes": errTotal,
		"startedAt":   t.startedAt.Format(time.RFC3339Nano),
	}
	if !t.endedAt.IsZero() {
		out["endedAt"] = t.endedAt.Format(time.RFC3339Nano)
	}
	if t.errText != "" {
		out["error"] = t.errText
	}
	return out
}

// checkBackground 返回任务快照；第二个返回值为 false 表示任务不存在。
func (e *execState) checkBackground(id int) (map[string]any, bool) {
	e.mu.Lock()
	task, ok := e.bg[id]
	e.mu.Unlock()
	if !ok {
		return nil, false
	}
	return task.view(), true
}

// waitBackground 阻塞到任务进入终态、等待预算耗尽或调用被取消。
//
// found=false 表示任务不存在；timedOut=true 表示预算先到（任务可能仍在 running）；
// err 只会在 ctx 被取消时返回，调用方据此把回执标成取消/超时。
func (e *execState) waitBackground(ctx context.Context, id int, timeout time.Duration) (view map[string]any, found, timedOut bool, err error) {
	e.mu.Lock()
	task, ok := e.bg[id]
	e.mu.Unlock()
	if !ok {
		return nil, false, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout < 0 {
		timeout = 0
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-task.doneCh:
		return task.view(), true, false, nil
	case <-timer.C:
		// 预算与任务结束同时就绪时以终态为准，避免报「仍在运行」。
		if task.done() {
			return task.view(), true, false, nil
		}
		return task.view(), true, true, nil
	case <-ctx.Done():
		return task.view(), true, false, ctx.Err()
	}
}

// killBackground 终止任务；found 表示任务存在，killed 表示本次确实从 running 终止。
func (e *execState) killBackground(id int) (found, killed bool) {
	e.mu.Lock()
	task, ok := e.bg[id]
	e.mu.Unlock()
	if !ok {
		return false, false
	}
	return true, task.kill()
}

// tailUTF8 取字符串尾部 max 字节，并保证起点是合法 UTF-8 边界。
func tailUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// backgroundSummary 是审计/日志用的一行参数摘要。
func backgroundSummary(file string, args []string) string {
	if len(args) == 0 {
		return file
	}
	return file + " " + strings.Join(args, " ")
}
