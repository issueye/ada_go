// Package store 是独立 cron 二进制的任务文件读写。
//
// 契约（与主模块 internal/cronstore 保持同构，两侧注释互指）：
//
//	文件：~/.adago/cron.json（ADAGO_HOME 优先），0600 原子写；
//	锁：~/.adago/cron.lock（gofrs/flock），所有读改写都走「锁 → 读 → 改 → 原子写」。
//	写入方：cron-manager 插件（经 ctx.cron，在 Core 进程）与本二进制（回写
//	lastFiredAt/runs/threadId、消费 triggerRequested）。
//
// cron 表达式语义见 internal/cronexpr/cronexpr.go 顶部；与主模块必须同步修改。
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"adagcron/internal/cronexpr"
)

const (
	// FileName 是任务文件名。
	FileName = "cron.json"
	// LockName 是跨进程锁文件名。
	LockName = "cron.lock"
	// FileVersion 是文件信封版本。
	FileVersion = 1
	// MaxTasks 是任务数上限。
	MaxTasks = 200
	// MaxNameBytes 是任务名长度上限。
	MaxNameBytes = 128
	// MaxTextBytes 是 agent 提示词长度上限。
	MaxTextBytes = 64 << 10
	// MaxBodyBytes 是 http 动作请求体长度上限。
	MaxBodyBytes = 64 << 10
	// MaxHeaderBytes 是单个 header 值长度上限。
	MaxHeaderBytes = 4 << 10
	// MaxHeaders 是 header 条数上限。
	MaxHeaders = 32
	// MaxFileBytes 是 cron.json 的大小上限。
	MaxFileBytes = 1 << 20
	// MaxRunsPerTask 是每个任务保留的运行历史条数（保留最新）。
	MaxRunsPerTask = 20
	// DefaultTimeoutMs 是 http 动作的默认超时。
	DefaultTimeoutMs = 15000
	// MinTimeoutMs 与 MaxTimeoutMs 是 http 动作超时的取值范围。
	MinTimeoutMs = 1000
	MaxTimeoutMs = 120000
)

// 动作类型与运行状态常量（与主模块 internal/cronstore 同名同义）。
const (
	ActionHTTP  = "http"
	ActionAgent = "agent"

	RunOK      = "ok"      // http：2xx
	RunError   = "error"   // http 非 2xx / 请求失败 / 网关返回失败
	RunTimeout = "timeout" // http 超时
	RunBusy    = "busy"    // agent：线程已有运行中轮次（thread_busy）
	RunStarted = "started" // agent：网关已受理 turn.start（完成与否在线程里看）
	RunMissed  = "missed"  // 应用未运行期间错过的调度（不补跑，仅记录）
)

// Action 是任务触发时要执行的动作；type 决定哪些字段生效。
type Action struct {
	Type      string            `json:"type"`
	Method    string            `json:"method,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMs int               `json:"timeoutMs,omitempty"`
	Text      string            `json:"text,omitempty"`
}

// Run 是一条运行历史。At 是「到期的调度时刻」（RFC3339），不是执行完成时刻。
type Run struct {
	At         string `json:"at"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Detail     string `json:"detail,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	ThreadID   string `json:"threadId,omitempty"`
}

// Task 是一条定时任务定义与其运行历史。
type Task struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	Cron             string `json:"cron"`
	Action           Action `json:"action"`
	ThreadID         string `json:"threadId,omitempty"`
	TriggerRequested bool   `json:"triggerRequested,omitempty"`
	LastFiredAt      string `json:"lastFiredAt,omitempty"`
	CreatedAt        string `json:"createdAt,omitempty"`
	UpdatedAt        string `json:"updatedAt,omitempty"`
	Runs             []Run  `json:"runs,omitempty"`
}

// file 是 cron.json 的信封形状。
type file struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
}

// GlobalDir 返回用户级数据目录：ADAGO_HOME 优先，否则 ~/.adago。
func GlobalDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ADAGO_HOME")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cron/store: 取用户主目录失败: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("cron/store: 用户主目录为空")
	}
	return filepath.Join(home, ".adago"), nil
}

// GlobalPath 返回 cron.json 路径；LockPath 返回跨进程锁路径。
func GlobalPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

func LockPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, LockName), nil
}

// Load 读取 cron.json：文件不存在返回空表。单条任务非法时跳过并返回警告，
// 不让一条手改坏数据拖垮整个调度器。
func Load(path string) ([]Task, []string, error) {
	tasks := []Task{}
	if strings.TrimSpace(path) == "" {
		return tasks, nil, errors.New("cron/store: 需要文件路径")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tasks, nil, nil
		}
		return tasks, nil, fmt.Errorf("cron/store: 读取 %s 失败: %w", path, err)
	}
	if len(raw) > MaxFileBytes {
		return tasks, nil, fmt.Errorf("cron/store: %s 超过 %d 字节上限", path, MaxFileBytes)
	}
	var body file
	if err := json.Unmarshal(raw, &body); err != nil {
		// 损坏文件备份后按空表启动：调度器不能因坏文件拒启。
		bak := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405")
		_ = os.Rename(path, bak)
		return tasks, []string{fmt.Sprintf("%s: 解析失败已备份为 %s，按空表启动", path, filepath.Base(bak))}, nil
	}
	warns := []string{}
	if body.Version != FileVersion && body.Version != 0 {
		warns = append(warns, fmt.Sprintf("%s: 未知版本 %d，按 %d 解析", path, body.Version, FileVersion))
	}
	for i := range body.Tasks {
		t := body.Tasks[i]
		if err := Validate(&t); err != nil {
			warns = append(warns, fmt.Sprintf("%s: 跳过任务 %q: %v", path, t.Name, err))
			continue
		}
		tasks = append(tasks, t)
	}
	return tasks, warns, nil
}

// Save 校验并原子写 cron.json（0600，目录 0700）。
func Save(path string, tasks []Task) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("cron/store: 需要文件路径")
	}
	if len(tasks) > MaxTasks {
		return fmt.Errorf("cron/store: 任务数超过上限 %d", MaxTasks)
	}
	for i := range tasks {
		if err := Validate(&tasks[i]); err != nil {
			return fmt.Errorf("cron/store: 任务 %q: %w", tasks[i].Name, err)
		}
	}
	body, err := json.MarshalIndent(file{Version: FileVersion, Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("cron/store: 序列化失败: %w", err)
	}
	body = append(body, '\n')
	if len(body) > MaxFileBytes {
		return fmt.Errorf("cron/store: 内容超过 %d 字节上限", MaxFileBytes)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cron/store: 创建目录 %s 失败: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("cron/store: 创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cron/store: 设置权限失败: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cron/store: 写入失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cron/store: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("cron/store: 替换 %s 失败: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

// Update 在跨进程文件锁内做一次读改写：锁 → 读 → fn → 原子写 → 解锁。
// 与插件侧（ctx.cron）的写在这里串行化；wait 是最长等锁时间。
func Update(path, lockPath string, wait time.Duration, fn func(tasks *[]Task) error) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(lockPath) == "" {
		return errors.New("cron/store: Update 需要文件与锁路径")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("cron/store: 创建锁目录失败: %w", err)
	}
	fl := flock.New(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("cron/store: 获取任务文件锁失败: %w", err)
	}
	if !ok {
		return fmt.Errorf("cron/store: 等待任务文件锁超时（%s）", wait)
	}
	defer func() { _ = fl.Unlock() }()

	cur, _, err := Load(path)
	if err != nil {
		return err
	}
	if err := fn(&cur); err != nil {
		return err
	}
	return Save(path, cur)
}

// AppendRun 把一条运行历史追加到任务（保留最新 MaxRunsPerTask 条）。
// 只改内存切片，落盘由调用方的 Update fn 负责。
func AppendRun(t *Task, run Run) {
	t.Runs = append(t.Runs, run)
	if len(t.Runs) > MaxRunsPerTask {
		t.Runs = t.Runs[len(t.Runs)-MaxRunsPerTask:]
	}
}

// Validate 校验一条任务；错误可直接展示。与主模块 cronstore.ValidateTask 同构。
func Validate(t *Task) error {
	if strings.TrimSpace(t.ID) == "" || len(t.ID) > 64 {
		return errors.New("任务 id 非法")
	}
	if strings.ContainsFunc(t.ID, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return errors.New("任务 id 不能含空白或控制字符")
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return errors.New("任务名不能为空")
	}
	if len(name) > MaxNameBytes {
		return fmt.Errorf("任务名超过 %d 字节", MaxNameBytes)
	}
	if _, err := cronexpr.Parse(t.Cron); err != nil {
		return err
	}
	return validateAction(&t.Action)
}

func validateAction(a *Action) error {
	switch a.Type {
	case ActionHTTP:
		method := strings.ToUpper(strings.TrimSpace(a.Method))
		if method != "GET" && method != "POST" {
			return fmt.Errorf("http 动作的 method 只能是 GET 或 POST（收到 %q）", a.Method)
		}
		a.Method = method
		u := strings.TrimSpace(a.URL)
		if u == "" {
			return errors.New("http 动作需要 url")
		}
		if len(u) > 2048 {
			return errors.New("http 动作的 url 超过 2048 字节")
		}
		parsed, perr := url.Parse(u)
		if perr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("http 动作的 url 必须是合法的 http(s) 地址")
		}
		if parsed.User != nil {
			return errors.New("http 动作的 url 不能内嵌用户信息（user:pass@host）")
		}
		if len(a.Headers) > MaxHeaders {
			return fmt.Errorf("http 动作的 headers 超过 %d 条", MaxHeaders)
		}
		for k, v := range a.Headers {
			if strings.TrimSpace(k) == "" || strings.ContainsAny(k, ": \t\r\n") {
				return fmt.Errorf("header 名 %q 非法", k)
			}
			if len(v) > MaxHeaderBytes {
				return fmt.Errorf("header %q 的值超过 %d 字节", k, MaxHeaderBytes)
			}
		}
		if len(a.Body) > MaxBodyBytes {
			return fmt.Errorf("http 动作的 body 超过 %d 字节", MaxBodyBytes)
		}
		if a.TimeoutMs != 0 && (a.TimeoutMs < MinTimeoutMs || a.TimeoutMs > MaxTimeoutMs) {
			return fmt.Errorf("http 动作的 timeoutMs 应在 %d-%d 之间", MinTimeoutMs, MaxTimeoutMs)
		}
		return nil
	case ActionAgent:
		if strings.TrimSpace(a.Text) == "" {
			return errors.New("agent 动作需要提示词 text")
		}
		if len(a.Text) > MaxTextBytes {
			return fmt.Errorf("agent 动作的提示词超过 %d 字节", MaxTextBytes)
		}
		return nil
	default:
		return fmt.Errorf("动作类型只能是 http 或 agent（收到 %q）", a.Type)
	}
}

// NewID 生成任务 id：crn_ + 24 位随机十六进制。
func NewID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cron/store: 生成 id 失败: %w", err)
	}
	return "crn_" + hex.EncodeToString(b[:]), nil
}
