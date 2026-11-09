// Package processmgr 管理由 Gateway 拉起的非 agent-worker 进程（P3）。
//
// 边界（docs/proposals/gateway-dispatch-process-plan.md §6）：
//   - 只负责注册/启动/停止/重启/引用计数/日志/进程树回收，不解释 cron、
//     任务或工作区业务；
//   - 进程命令由调用方给出，P4 的插件桥负责把 entry 解析到插件目录并拒绝越界；
//   - 进程环境不继承宿主内部凭据（ADAG_/ADAGO_），也不把插件脚本交给 shell；
//   - 停止一律杀整棵进程树（Unix 进程组 / Windows taskkill /T）。
package processmgr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 默认值与上限（docs/proposals 的 P0 冻结项）。
const (
	DefaultMaxPerOwner     = 8
	DefaultMaxGlobal       = 32
	DefaultStartTimeout    = 5 * time.Second
	DefaultStopTimeout     = 5 * time.Second
	DefaultRestartBase     = 500 * time.Millisecond
	DefaultRestartMax      = 30 * time.Second
	DefaultRestartResetRun = 30 * time.Second
	DefaultLogLines        = 200
	DefaultLogLineBytes    = 2048
)

// Scope 是进程的可见范围。
type Scope string

const (
	// ScopeGlobal 是跨工作区单例；同名进程只启动一份。
	ScopeGlobal Scope = "global"
	// ScopeWorkspace 是每工作区一份；key 里带工作区路径。
	ScopeWorkspace Scope = "workspace"
)

// StartMode 决定进程何时启动。
type StartMode string

const (
	// StartOnEnable 在首次注册后立即启动。
	StartOnEnable StartMode = "on_enable"
	// StartLazy 只登记，Ensure 时才启动。
	StartLazy StartMode = "lazy"
)

// RestartPolicy 是进程退出后的行为。
type RestartPolicy string

const (
	// RestartNo 不自动重启；进程保持 stopped 状态，等待 Ensure。
	RestartNo RestartPolicy = "no"
	// RestartOnFailure 仅在非零退出/被杀时重启。
	RestartOnFailure RestartPolicy = "on_failure"
	// RestartAlways 无论退出码都重启。
	RestartAlways RestartPolicy = "always"
)

// Runtime 是进程的启动形态。
//
// v1 只有 binary 与 node：binary 直接启动 Entry（或 Command），node 用
// NodeCommand（缺省 node）执行 Entry 脚本。禁止经过 shell，argv 原样传递。
type Runtime string

const (
	RuntimeBinary Runtime = "binary"
	RuntimeNode   Runtime = "node"
)

// ProcessSpec 是一条进程注册。
//
// Owner 由 Register 盖章，调用方不能自填（与插件能力集的归属同一原则）。
type ProcessSpec struct {
	// Name 是进程在 owner 命名空间内的名字（如 scheduler）。
	Name string `json:"name"`
	// Owner 是插件 id；Register 会覆盖为传入的 owner。
	Owner string `json:"owner,omitempty"`
	// Scope 缺省 global。
	Scope Scope `json:"scope,omitempty"`
	// Workspace 是 ScopeWorkspace 的归属工作区（同时作为 cwd）。
	Workspace string `json:"workspace,omitempty"`
	// Runtime 缺省 binary。
	Runtime Runtime `json:"runtime,omitempty"`
	// Entry 是 binary 的可执行文件，或 node 的脚本路径。
	Entry string `json:"entry"`
	// Command 覆盖 runtime 的启动程序（binary 缺省用 Entry；node 缺省用 NodeCommand）。
	Command string `json:"command,omitempty"`
	// Args 是附加参数；node 形态下 Entry 会作为第一个参数。
	Args []string `json:"args,omitempty"`
	// Env 是追加/覆盖到宿主环境上的变量；宿主凭据前缀会被剥掉。
	Env map[string]string `json:"env,omitempty"`
	// StartMode 缺省 lazy。
	StartMode StartMode `json:"startMode,omitempty"`
	// Restart 缺省 no。
	Restart RestartPolicy `json:"restart,omitempty"`
	// StopTimeout 是停止时等待进程退出的上限；缺省 DefaultStopTimeout。
	StopTimeout time.Duration `json:"stopTimeout,omitempty"`
}

// Ref 指向一个已注册进程。Workspace 只对 ScopeWorkspace 有意义。
type Ref struct {
	Owner     string
	Name      string
	Workspace string
}

// ID 返回 owner/name 形式的展示 id。
func (r Ref) ID() string { return r.Owner + "/" + r.Name }

// Status 是进程的可观测快照。
type Status struct {
	ID         string        `json:"id"`
	Owner      string        `json:"owner"`
	Name       string        `json:"name"`
	Scope      Scope         `json:"scope"`
	Workspace  string        `json:"workspace,omitempty"`
	Runtime    Runtime       `json:"runtime"`
	PID        int           `json:"pid,omitempty"`
	Running    bool          `json:"running"`
	Restarts   int           `json:"restarts"`
	Refs       int           `json:"refs"`
	StartMode  StartMode     `json:"startMode"`
	Restart    RestartPolicy `json:"restart"`
	StartedAt  *time.Time    `json:"startedAt,omitempty"`
	StoppedAt  *time.Time    `json:"stoppedAt,omitempty"`
	LastError  string        `json:"lastError,omitempty"`
	DroppedLog int           `json:"droppedLog,omitempty"`
}

// LogLine 是一行进程输出。
type LogLine struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Stream string    `json:"stream"` // stdout | stderr
	Text   string    `json:"text"`
}

// ErrorCode 是进程管理的业务错误码（与提案 P0 的错误码一致）。
type ErrorCode string

const (
	// CodeExists 表示同一 key 已注册了不同的 spec。
	CodeExists ErrorCode = "process_exists"
	// CodeNotFound 表示没有找到注册项。
	CodeNotFound ErrorCode = "process_not_found"
	// CodeLimit 表示超过每 owner/全局进程上限。
	CodeLimit ErrorCode = "process_limit"
	// CodeStartFailed 表示启动失败。
	CodeStartFailed ErrorCode = "process_start_failed"
	// CodeStopFailed 表示停止时等待退出超时。
	CodeStopFailed ErrorCode = "process_stop_failed"
	// CodeInvalidSpec 表示 spec 形状非法。
	CodeInvalidSpec ErrorCode = "process_invalid_spec"
)

// Error 是带业务错误码的错误；调用方按 Code 分支，不解析文案。
type Error struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("processmgr: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("processmgr: %s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf 返回错误的业务码；不是 *Error 时返回空。
func CodeOf(err error) ErrorCode {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func newError(code ErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func wrapError(code ErrorCode, err error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Err: err}
}

// StartSpec 是实际传给 Spawn 的启动参数（已解析 runtime/环境）。
type StartSpec struct {
	Owner     string
	Name      string
	Scope     Scope
	Workspace string
	Command   string
	Args      []string
	Dir       string
	Env       []string
}

// Process 是 Manager 需要的最小进程抽象；测试可注入假实现。
//
// Wait 只会被调用一次（由退出监视 goroutine 拥有）；Kill 用于主动停止并杀进程树。
type Process interface {
	PID() int
	Wait() error
	Kill() error
}

// SpawnFunc 拉起一个进程；stdout/stderr 由调用方提供，Spawn 负责把子进程输出接进去。
//
// 生产实现用 exec.Cmd 的 Stdout/Stderr 字段（而不是自建管道）：cmd.Wait 会等待
// 复制 goroutine 结束，因此进程退出前的最后一行不会丢。
type SpawnFunc func(ctx context.Context, spec StartSpec, stdout, stderr io.Writer) (Process, error)

// execProcess 是生产 Spawn 返回的 Process 实现。
type execProcess struct {
	cmd  *exec.Cmd
	wait sync.Once
	err  error
}

func (p *execProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execProcess) Wait() error {
	p.wait.Do(func() { p.err = p.cmd.Wait() })
	return p.err
}

func (p *execProcess) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	killProcessTree(p.cmd.Process.Pid)
	// 进程组/taskkill 已覆盖直接子进程；这里再兜底一次 Pid，避免平台命令
	// 不存在时留下父进程。
	_ = p.cmd.Process.Kill()
	return nil
}

// execSpawn 用 exec.Command 拉起进程，不经过 shell。
func execSpawn(ctx context.Context, spec StartSpec, stdout, stderr io.Writer) (Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("启动命令为空")
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	// 被管进程可能派生孙进程，而孙进程会继承 stdout/stderr 管道的写端：
	// 父进程退出后 Wait 仍会等这些写端关闭。WaitDelay 让 Wait 在进程退出后
	// 有限等待 I/O，然后自己关掉管道，避免「进程已死但 Wait 永不返回」。
	// 取 1s：进程树已由 Kill 覆盖，剩下的只是管道尾流。
	cmd.WaitDelay = time.Second
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stderr != nil {
		cmd.Stderr = stderr
	}
	configureProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 %s 失败: %w", spec.Command, err)
	}
	return &execProcess{cmd: cmd}, nil
}

// resolveStartSpec 把注册 spec 解析成可执行参数。
func resolveStartSpec(spec ProcessSpec, nodeCommand string) (StartSpec, error) {
	runtimeKind := spec.Runtime
	if runtimeKind == "" {
		runtimeKind = RuntimeBinary
	}
	entry := strings.TrimSpace(spec.Entry)
	if entry == "" {
		return StartSpec{}, newError(CodeInvalidSpec, "%s 缺少 entry", spec.Name)
	}
	out := StartSpec{
		Owner:     spec.Owner,
		Name:      spec.Name,
		Scope:     spec.Scope,
		Workspace: spec.Workspace,
	}
	switch runtimeKind {
	case RuntimeBinary:
		out.Command = strings.TrimSpace(spec.Command)
		if out.Command == "" {
			out.Command = entry
		}
		out.Args = append([]string(nil), spec.Args...)
	case RuntimeNode:
		out.Command = strings.TrimSpace(spec.Command)
		if out.Command == "" {
			out.Command = nodeCommand
		}
		if out.Command == "" {
			out.Command = "node"
		}
		out.Args = append([]string{entry}, spec.Args...)
	default:
		return StartSpec{}, newError(CodeInvalidSpec, "%s 的 runtime %q 未知（只有 binary/node）", spec.Name, runtimeKind)
	}
	if spec.Scope == ScopeWorkspace {
		if strings.TrimSpace(spec.Workspace) == "" {
			return StartSpec{}, newError(CodeInvalidSpec, "%s 是 workspace scope 但缺少 workspace", spec.Name)
		}
		out.Dir = spec.Workspace
	}
	out.Env = buildEnv(spec.Env)
	return out, nil
}

// buildEnv 继承宿主环境但剥掉宿主凭据，再叠加显式 Env。
// 输出按变量名排序，保证同一输入得到同一份环境块。
func buildEnv(overrides map[string]string) []string {
	base := stripHostEnv(os.Environ())
	if len(overrides) == 0 {
		return base
	}
	type item struct{ name, value string }
	values := make(map[string]item, len(base)+len(overrides))
	for _, raw := range base {
		i := strings.IndexByte(raw, '=')
		if i <= 0 {
			continue
		}
		values[envKey(raw[:i])] = item{name: raw[:i], value: raw[i+1:]}
	}
	for name, value := range overrides {
		if isHostEnvName(name) {
			continue // 显式 env 也不能把宿主凭据交给被管进程
		}
		values[envKey(name)] = item{name: name, value: value}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		it := values[key]
		out = append(out, it.name+"="+it.value)
	}
	return out
}

var hostEnvPrefixes = []string{"ADAG_", "ADAGO_"}

// hostEnvAllow 是「前缀像宿主变量、但本质是路径指针」的少数名字：
// 被管进程要读同一份配置，必须知道数据目录在哪。
//
// 只放路径/开关类变量，不放任何令牌：令牌由网关按进程单独签发（见 control.go）。
var hostEnvAllow = map[string]bool{
	"ADAGO_HOME":                true,
	"ADAG_OFFICIAL_PLUGINS_DIR": true,
}

func isHostEnvName(name string) bool {
	up := strings.ToUpper(name)
	if hostEnvAllow[up] {
		return false
	}
	for _, prefix := range hostEnvPrefixes {
		if strings.HasPrefix(up, prefix) {
			return true
		}
	}
	return false
}

func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func stripHostEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, raw := range env {
		i := strings.IndexByte(raw, '=')
		if i <= 0 {
			continue
		}
		if isHostEnvName(raw[:i]) {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// logRing 是每进程的环形日志缓冲。按行数封顶，单行按字节截断。
type logRing struct {
	mu      sync.Mutex
	maxLine int
	maxKeep int
	seq     int64
	lines   []LogLine
	dropped int
}

func newLogRing(maxLines, maxLineBytes int) *logRing {
	if maxLines <= 0 {
		maxLines = DefaultLogLines
	}
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultLogLineBytes
	}
	return &logRing{maxLine: maxLineBytes, maxKeep: maxLines}
}

func (r *logRing) add(stream, text string) {
	if r == nil {
		return
	}
	text = strings.TrimRight(text, "\r\n")
	text = truncateUTF8(text, r.maxLine)
	text = redactLog(text)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.lines = append(r.lines, LogLine{Seq: r.seq, At: time.Now().UTC(), Stream: stream, Text: text})
	if len(r.lines) > r.maxKeep {
		drop := len(r.lines) - r.maxKeep
		r.lines = append(r.lines[:0], r.lines[drop:]...)
		r.dropped += drop
	}
}

func (r *logRing) snapshot(limit int) []LogLine {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > len(r.lines) {
		limit = len(r.lines)
	}
	out := make([]LogLine, limit)
	copy(out, r.lines[len(r.lines)-limit:])
	return out
}

func (r *logRing) droppedCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// logSink 把进程输出按行切进日志环。没有换行的长块按 maxLine 分批落盘，
// 避免一个只写不换行的进程把内存拖大。
type logSink struct {
	ring   *logRing
	stream string

	mu  sync.Mutex
	buf []byte
}

func newLogSink(ring *logRing, stream string) *logSink {
	return &logSink{ring: ring, stream: stream}
}

func (w *logSink) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			if len(w.buf) >= w.ring.maxLine {
				w.ring.add(w.stream, string(w.buf))
				w.buf = nil
			}
			break
		}
		w.ring.add(w.stream, string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if size > 0 && r != utf8.RuneError {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + "…（截断）"
}

var (
	secretPairRe = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?key|secret|token|password|passwd|authorization|cookie)(\s*[=:]\s*)([^\s,;]+)`)
	bearerRe     = regexp.MustCompile(`(?i)\b(bearer|basic)\s+([A-Za-z0-9._~+/=-]{6,})`)
)

// redactLog 遮蔽日志里的常见凭据形态：key=value 与 Bearer token。
func redactLog(line string) string {
	if line == "" {
		return line
	}
	out := secretPairRe.ReplaceAllString(line, "$1$2***")
	out = bearerRe.ReplaceAllString(out, "$1 ***")
	return out
}

// statusOf 把 entry 转成对外快照（由 manager.go 调用）。
func statusOf(e *entry) Status {
	if e == nil {
		return Status{}
	}
	st := Status{
		ID:         e.keyID(),
		Owner:      e.spec.Owner,
		Name:       e.spec.Name,
		Scope:      e.spec.Scope,
		Workspace:  e.spec.Workspace,
		Runtime:    e.spec.Runtime,
		PID:        e.pid,
		Running:    e.running,
		Restarts:   e.restarts,
		Refs:       e.refs,
		StartMode:  e.spec.StartMode,
		Restart:    e.spec.Restart,
		LastError:  e.lastErr,
		DroppedLog: e.logs.droppedCount(),
	}
	if st.Runtime == "" {
		st.Runtime = RuntimeBinary
	}
	if st.StartMode == "" {
		st.StartMode = StartLazy
	}
	if st.Restart == "" {
		st.Restart = RestartNo
	}
	if !e.startedAt.IsZero() {
		t := e.startedAt
		st.StartedAt = &t
	}
	if !e.stoppedAt.IsZero() {
		t := e.stoppedAt
		st.StoppedAt = &t
	}
	return st
}

// flush 落盘尚未遇到换行的尾部（进程退出前最后一行可能没有换行）。
func (w *logSink) flush() {
	if w == nil || w.ring == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.ring.add(w.stream, string(w.buf))
		w.buf = nil
	}
}
