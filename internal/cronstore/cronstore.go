// Package cronstore 是定时任务（cron-manager 插件 + cron 二进制）共享的任务存储。
//
// 单一文件：~/.adago/cron.json（ADAGO_HOME 优先，与 env.json / plugins.json 同一
// 约定），0600 原子写；跨进程读改写用 ~/.adago/cron.lock（gofrs/flock）串行化。
// 写入方有三处：插件 ctx.cron（管理页 CRUD，经 Core 进程）、cron 二进制（回写
// lastFiredAt / runs / threadId / 消费 triggerRequested）；都走 Update 的
// 「锁 → 读 → 改 → 原子写」路径，不做覆盖式整文件写。
//
// JSON 契约与独立二进制 mcp_projects/cron 的 internal/store 保持同构：
// cron 表达式语义见 cronexpr.go 顶部；两侧注释互指，改字段必须同步。
//
// 本包不承担执行；到期判定与触发在 cron 二进制（internal/runner）。
// 动作只有 agent（AI 任务）：到期后经网关 /api/cron/fire 送达 core；HTTP 动作已移除。
package cronstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
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
	// MaxWorkspaceBytes 是任务目标工作区路径长度上限。
	MaxWorkspaceBytes = 1024
	// MaxFileBytes 是 cron.json 的大小上限。
	MaxFileBytes = 1 << 20
	// MaxRunsPerTask 是每个任务保留的运行历史条数（保留最新）。
	MaxRunsPerTask = 20
)

// 动作类型与运行状态常量。
const (
	// ActionAgent 是唯一支持的动作：到期后经网关把提示词交给 AI。
	ActionAgent = "agent"
	// ActionHTTP 是已移除的旧动作类型；保留常量只为给历史文件一条可读的
	// 跳过原因，新建/更新一律拒绝。
	ActionHTTP = "http"

	RunError   = "error"   // 网关返回失败 / 请求失败
	RunBusy    = "busy"    // agent：线程已有运行中轮次（thread_busy）
	RunStarted = "started" // agent：网关已受理 turn.start（完成与否在线程里看）
	RunMissed  = "missed"  // 应用未运行期间错过的调度（不补跑，仅记录）
)

// Action 是任务触发时要执行的动作；当前只有 AI 任务一种。
type Action struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Run 是一条运行历史。At 是「到期的调度时刻」（RFC3339），不是执行完成时刻。
type Run struct {
	At         string `json:"at"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Detail     string `json:"detail,omitempty"`
	ThreadID   string `json:"threadId,omitempty"`
}

// Task 是一条定时任务定义与其运行历史。
type Task struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Cron    string `json:"cron"`
	Action  Action `json:"action"`
	// Workspace 是任务触发的目标工作区（绝对路径）；空表示默认工作区
	// （网关列表第一个，即“综合工作区”）。路径是否有效由网关在触发时判定：
	// 不在工作区列表里会明确失败，而不是悄悄换地方执行。
	Workspace        string `json:"workspace,omitempty"`
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
		return "", fmt.Errorf("cronstore: 取用户主目录失败: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("cronstore: 用户主目录为空")
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

// NewID 生成任务 id：crn_ + 24 位随机十六进制。
func NewID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cronstore: 生成 id 失败: %w", err)
	}
	return "crn_" + hex.EncodeToString(b[:]), nil
}

// LoadFile 读取 cron.json：文件不存在返回空表。单条任务非法时跳过并返回警告，
// 不让一条手改坏数据拖垮整个管理页。
func LoadFile(path string) (*file, []string, error) {
	out := &file{Version: FileVersion, Tasks: []Task{}}
	if strings.TrimSpace(path) == "" {
		return out, nil, errors.New("cronstore: 需要文件路径")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil, nil
		}
		return out, nil, fmt.Errorf("cronstore: 读取 %s 失败: %w", path, err)
	}
	if len(raw) > MaxFileBytes {
		return out, nil, fmt.Errorf("cronstore: %s 超过 %d 字节上限", path, MaxFileBytes)
	}
	var body file
	if err := json.Unmarshal(raw, &body); err != nil {
		return out, nil, fmt.Errorf("cronstore: %s 不是合法 JSON: %w", path, err)
	}
	warns := []string{}
	if body.Version != FileVersion && body.Version != 0 {
		warns = append(warns, fmt.Sprintf("%s: 未知版本 %d，按 %d 解析", path, body.Version, FileVersion))
	}
	for i := range body.Tasks {
		t := body.Tasks[i]
		if err := ValidateTask(&t); err != nil {
			warns = append(warns, fmt.Sprintf("%s: 跳过任务 %q: %v", path, t.Name, err))
			continue
		}
		out.Tasks = append(out.Tasks, t)
	}
	return out, warns, nil
}

// SaveFile 校验并原子写 cron.json（0600，目录 0700）。
func SaveFile(path string, tasks []Task) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("cronstore: 需要文件路径")
	}
	if len(tasks) > MaxTasks {
		return fmt.Errorf("cronstore: 任务数超过上限 %d", MaxTasks)
	}
	for i := range tasks {
		if err := ValidateTask(&tasks[i]); err != nil {
			return fmt.Errorf("cronstore: 任务 %q: %w", tasks[i].Name, err)
		}
	}
	body, err := json.MarshalIndent(file{Version: FileVersion, Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("cronstore: 序列化失败: %w", err)
	}
	body = append(body, '\n')
	if len(body) > MaxFileBytes {
		return fmt.Errorf("cronstore: 内容超过 %d 字节上限", MaxFileBytes)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cronstore: 创建目录 %s 失败: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("cronstore: 创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cronstore: 设置权限失败: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cronstore: 写入失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cronstore: 关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("cronstore: 替换 %s 失败: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

// Update 在跨进程文件锁内做一次读改写：锁 → 读 → fn → 原子写 → 解锁。
// fn 里的修改要么落盘要么整体不发生；并发写（插件 CRUD 与 cron 二进制回写）
// 在这里串行化。wait 是最长等锁时间（本包不把 context 暴露进公开 API）。
func Update(path string, lockPath string, wait time.Duration, fn func(tasks *[]Task) error) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(lockPath) == "" {
		return errors.New("cronstore: Update 需要文件与锁路径")
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("cronstore: 创建锁目录失败: %w", err)
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	fl := flock.New(lockPath)
	ok, err := fl.TryLockContext(lockCtx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("cronstore: 获取任务文件锁失败: %w", err)
	}
	if !ok {
		return fmt.Errorf("cronstore: 等待任务文件锁超时（%s）", wait)
	}
	defer func() { _ = fl.Unlock() }()

	cur, _, err := LoadFile(path)
	if err != nil {
		return err
	}
	tasks := cur.Tasks
	if err := fn(&tasks); err != nil {
		return err
	}
	return SaveFile(path, tasks)
}

// AppendRun 把一条运行历史追加到任务（保留最新 MaxRunsPerTask 条）。
// 只改内存切片，落盘由调用方的 Update fn 负责。
func AppendRun(t *Task, run Run) {
	t.Runs = append(t.Runs, run)
	if len(t.Runs) > MaxRunsPerTask {
		t.Runs = t.Runs[len(t.Runs)-MaxRunsPerTask:]
	}
}

// ValidateTask 校验一条任务；错误可直接展示。
func ValidateTask(t *Task) error {
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
	if strings.ContainsFunc(name, func(r rune) bool { return r < ' ' }) {
		return errors.New("任务名不能含控制字符")
	}
	if _, err := ParseCron(t.Cron); err != nil {
		return err
	}
	t.Workspace = strings.TrimSpace(t.Workspace)
	if len(t.Workspace) > MaxWorkspaceBytes {
		return fmt.Errorf("工作区路径超过 %d 字节", MaxWorkspaceBytes)
	}
	if strings.ContainsFunc(t.Workspace, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("工作区路径不能含控制字符")
	}
	return validateAction(&t.Action)
}

func validateAction(a *Action) error {
	switch a.Type {
	case ActionAgent:
		if strings.TrimSpace(a.Text) == "" {
			return errors.New("agent 动作需要提示词 text")
		}
		if len(a.Text) > MaxTextBytes {
			return fmt.Errorf("agent 动作的提示词超过 %d 字节", MaxTextBytes)
		}
		return nil
	case ActionHTTP:
		return errors.New("HTTP 动作已移除：定时任务只支持 AI 任务（agent）")
	default:
		return fmt.Errorf("动作类型只能是 agent（收到 %q）", a.Type)
	}
}

// NextAfter 返回任务在 after 之后的下一个触发时刻（供管理页预览 nextRunAt）。
func NextAfter(t *Task, after time.Time) (time.Time, error) {
	s, err := ParseCron(t.Cron)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after), nil
}
