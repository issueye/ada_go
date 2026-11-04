// Package runner 是独立 cron 二进制的调度循环。
//
// 职责只有一个：读 ~/.adago/cron.json，把到期的任务按动作类型执行——
//   - http：本进程直接发请求（GET/POST、超时、16MB 上限）；
//   - agent：POST {gateway}/api/cron/fire（Bearer 令牌），由网关拉起 Core
//     并 thread.create + turn.start；本进程不等轮次完成，记录「已启动」。
//
// 到期判定：due = Next(baseline)，baseline 优先取 lastFiredAt，未跑过的任务取
// createdAt（错过应用关闭期间的历史点只记一条 missed，不逐个补跑）。
// 执行前先在文件锁内「认领」（写 lastFiredAt），锁外执行，再入锁记结果——
// 锁内不做任何 I/O 动作，避免长时间动作卡住插件侧的 CRUD。
// triggerRequested 标记（管理页「立即运行」）在下一个 tick 立即消费。
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"adagcron/internal/cronexpr"
	"adagcron/internal/store"
)

// maxHTTPResponseBytes 是 http 动作响应体读取上限（与宿主 HTTP 桥一致）。
const maxHTTPResponseBytes = 16 << 20

// fireTimeout 是 agent 动作调网关 /api/cron/fire 的超时。
const fireTimeout = 30 * time.Second

// claimWindow 是「认领」的等待上限：跨进程锁只覆盖毫秒级读写，足够宽裕。
const claimWindow = 10 * time.Second

// Runner 持有调度循环的全部配置与状态。
type Runner struct {
	// File 与 Lock 是任务文件与锁路径（空则用 store 默认全局路径）。
	File string
	Lock string
	// GatewayURL 与 Token 是网关地址与 Bearer 令牌（agent 动作必需）。
	GatewayURL string
	Token      string
	// Log 是运行日志（nil 用标准 log）。
	Log Logger

	client     *http.Client
	fireClient *http.Client
}

// Logger 是 runner 依赖的最小日志接口。
type Logger interface {
	Printf(format string, args ...any)
}

type stdLogger struct{}

func (stdLogger) Printf(format string, args ...any) { log.Printf(format, args...) }

// New 构造一个 Runner。
func New(file, lock, gatewayURL, token string) *Runner {
	return &Runner{
		File:       file,
		Lock:       lock,
		GatewayURL: strings.TrimRight(strings.TrimSpace(gatewayURL), "/"),
		Token:      token,
		client:     &http.Client{},
		fireClient: &http.Client{Timeout: fireTimeout},
	}
}

// Run 启动 tick 循环，直到 ctx 取消。interval 是 tick 周期。
func (r *Runner) Run(ctx context.Context, interval time.Duration) {
	logf := r.logger()
	logf.Printf("调度器启动（tick=%s，任务文件=%s）", interval, r.path())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	r.tick(ctx) // 启动立即跑一轮：处理错过的调度与遗留的 trigger 标记
	for {
		select {
		case <-ctx.Done():
			logf.Printf("调度器退出")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) logger() Logger {
	if r.Log == nil {
		return stdLogger{}
	}
	return r.Log
}

func (r *Runner) path() string {
	if r.File != "" {
		return r.File
	}
	p, err := store.GlobalPath()
	if err != nil {
		return store.FileName
	}
	return p
}

func (r *Runner) lockPath() string {
	if r.Lock != "" {
		return r.Lock
	}
	p, err := store.LockPath()
	if err != nil {
		return store.LockName
	}
	return p
}

// dueInfo 是一个到期任务在本轮的执行计划。
type dueInfo struct {
	id     string
	due    time.Time // 到期的调度时刻
	missed bool      // 应用关闭期间错过的历史：只记一条 missed，不执行
	manual bool      // triggerRequested：立即运行，清标记并执行
}

// tick 跑一轮：读文件 → 找到期任务 → 认领 → 执行 → 记录。
func (r *Runner) tick(ctx context.Context) {
	logf := r.logger()
	path, lock := r.path(), r.lockPath()
	now := time.Now()

	tasks, warns, err := store.Load(path)
	if err != nil {
		logf.Printf("读取任务失败: %v", err)
		return
	}
	for _, w := range warns {
		logf.Printf("警告: %s", w)
	}

	var dueTasks []dueInfo
	for i := range tasks {
		t := &tasks[i]
		if t.TriggerRequested {
			dueTasks = append(dueTasks, dueInfo{id: t.ID, due: now, manual: true})
			continue
		}
		if !t.Enabled {
			continue
		}
		sched, perr := cronexpr.Parse(t.Cron)
		if perr != nil {
			logf.Printf("任务 %q 的表达式 %q 非法: %v", t.Name, t.Cron, perr)
			continue
		}
		baseline := time.Time{}
		if t.LastFiredAt != "" {
			if v, perr := time.Parse(time.RFC3339, t.LastFiredAt); perr == nil {
				baseline = v
			}
		}
		if baseline.IsZero() {
			// 从未跑过：从创建时刻起算下一个触发点（应用关闭期间的历史点
			// 由 missed 分支处理，不逐个补跑）。
			if v, perr := time.Parse(time.RFC3339, t.CreatedAt); perr == nil {
				baseline = v
			} else {
				baseline = now
			}
		}
		due := sched.Next(baseline)
		if due.IsZero() || due.After(now) {
			continue
		}
		// due ≤ now 且从未跑过：应用关闭期间错过的历史，只记一条。
		if t.LastFiredAt == "" {
			dueTasks = append(dueTasks, dueInfo{id: t.ID, missed: true})
			continue
		}
		dueTasks = append(dueTasks, dueInfo{id: t.ID, due: due})
	}

	for _, d := range dueTasks {
		if ctx.Err() != nil {
			return
		}
		// 1) 认领：锁内按 id 重定位并推进 lastFiredAt（并发期间插件可能改过列表）。
		claimed, snapshot := r.claim(path, lock, d, now)
		if !claimed {
			continue
		}
		if d.missed {
			// 错过的调度：只记历史，不执行动作。
			r.record(path, lock, snapshot.ID, store.Run{
				At:     now.UTC().Format(time.RFC3339),
				Status: store.RunMissed,
				Detail: "应用未运行，错过该次调度（不补跑）",
			}, "", false)
			continue
		}
		// 2) 锁外执行动作。
		run, clearThread := r.execute(ctx, &snapshot, d.due)
		// 3) 入锁记录结果；agent 动作顺带回写线程 id 供下次复用
		//    （clearThread 表示线程已不存在，清掉让下次运行重建）。
		r.record(path, lock, snapshot.ID, run, run.ThreadID, clearThread)
	}
}

// claim 在文件锁内认领一次触发：推进 lastFiredAt（或清 trigger 标记），
// 返回是否认领成功与任务的快照。
func (r *Runner) claim(path, lock string, d dueInfo, now time.Time) (bool, store.Task) {
	var snapshot store.Task
	claimed := false
	_ = store.Update(path, lock, claimWindow, func(all *[]store.Task) error {
		for j := range *all {
			cur := &(*all)[j]
			if cur.ID != d.id {
				continue
			}
			if d.manual {
				if !cur.TriggerRequested {
					return nil // 标记已被消费过
				}
				// 立即运行：清标记，lastFiredAt 用 now（本次的真实触发时刻）。
				cur.TriggerRequested = false
				cur.LastFiredAt = now.UTC().Format(time.RFC3339)
				cur.UpdatedAt = cur.LastFiredAt
				snapshot = *cur
				claimed = true
				return nil
			}
			if d.missed {
				if cur.LastFiredAt != "" {
					return nil // 已被处理过（并发或上轮认领）
				}
				// 错过：不执行动作，lastFiredAt 推进到 now，下轮按正常调度走。
				cur.LastFiredAt = now.UTC().Format(time.RFC3339)
				cur.UpdatedAt = cur.LastFiredAt
				snapshot = *cur
				claimed = true
				return nil
			}
			if !cur.Enabled || cur.TriggerRequested {
				// 已被停用，或已有新的立即运行标记排队：本轮调度跳过。
				return nil
			}
			// 防重：锁内再确认 lastFiredAt < due。
			if cur.LastFiredAt != "" {
				if v, perr := time.Parse(time.RFC3339, cur.LastFiredAt); perr == nil && !v.Before(d.due) {
					return nil
				}
			}
			cur.LastFiredAt = d.due.UTC().Format(time.RFC3339)
			snapshot = *cur
			claimed = true
			return nil
		}
		return nil
	})
	return claimed, snapshot
}

// record 在文件锁内追加一条运行历史；threadID 非空时回写任务的线程 id，
// clearThread 时清掉（线程已不存在，下次运行重建）。
func (r *Runner) record(path, lock, taskID string, run store.Run, threadID string, clearThread bool) {
	_ = store.Update(path, lock, claimWindow, func(all *[]store.Task) error {
		for j := range *all {
			if (*all)[j].ID != taskID {
				continue
			}
			cur := &(*all)[j]
			store.AppendRun(cur, run)
			switch {
			case clearThread:
				cur.ThreadID = ""
				cur.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			case threadID != "" && cur.ThreadID != threadID:
				cur.ThreadID = threadID
				cur.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			}
			return nil
		}
		return nil
	})
}

// execute 执行一个任务的动作，返回运行历史条目与是否清除任务线程
// （agent 动作的线程已不存在时为 true）。
func (r *Runner) execute(ctx context.Context, t *store.Task, due time.Time) (store.Run, bool) {
	logf := r.logger()
	run := store.Run{At: due.UTC().Format(time.RFC3339)}
	start := time.Now()
	var clearThread bool
	switch t.Action.Type {
	case store.ActionHTTP:
		status, httpStatus, detail := r.fireHTTP(ctx, &t.Action)
		run.Status, run.HTTPStatus, run.Detail = status, httpStatus, detail
	case store.ActionAgent:
		status, threadID, detail, agentClear := r.fireAgent(ctx, t)
		run.Status, run.ThreadID, run.Detail = status, threadID, detail
		clearThread = agentClear
	default:
		run.Status = store.RunError
		run.Detail = "未知动作类型 " + t.Action.Type
	}
	run.DurationMs = time.Since(start).Milliseconds()
	logf.Printf("任务 %q（%s）→ %s（%dms）%s", t.Name, t.Action.Type, run.Status, run.DurationMs, run.Detail)
	return run, clearThread
}

// fireHTTP 执行 http 动作；返回 (status, httpStatus, detail)。
func (r *Runner) fireHTTP(ctx context.Context, a *store.Action) (string, int, string) {
	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method != "GET" && method != "POST" {
		method = "GET"
	}
	timeout := time.Duration(a.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Duration(store.DefaultTimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if method == "POST" && a.Body != "" {
		body = strings.NewReader(a.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.URL, body)
	if err != nil {
		return store.RunError, 0, "构造请求失败: " + err.Error()
	}
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return store.RunTimeout, 0, "请求超时（" + timeout.String() + "）"
		}
		return store.RunError, 0, "请求失败: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return store.RunOK, resp.StatusCode, ""
	}
	return store.RunError, resp.StatusCode, "非 2xx 响应"
}

// fireRequest 是 POST /api/cron/fire 的请求体（与网关 handlers_cron.go 同构）。
type fireRequest struct {
	TaskID         string `json:"taskId"`
	TaskName       string `json:"taskName"`
	Text           string `json:"text"`
	ThreadID       string `json:"threadId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// fireResponse 是网关的返回（与网关 handlers_cron.go 同构）。
type fireResponse struct {
	Started  bool   `json:"started"`
	Reason   string `json:"reason,omitempty"`
	ThreadID string `json:"threadId,omitempty"`
	Error    string `json:"error,omitempty"`
}

// fireAgent 把 agent 动作经网关转发给 Core；返回 (status, threadID, detail, clearThread)。
// clearThread 表示任务专用线程已不存在，调用方应清掉 task.threadId 让下次运行重建。
func (r *Runner) fireAgent(ctx context.Context, t *store.Task) (string, string, string, bool) {
	if r.GatewayURL == "" || r.Token == "" {
		return store.RunError, "", "缺少网关地址或令牌（--gateway-url/--token）", false
	}
	payload := fireRequest{
		TaskID:         t.ID,
		TaskName:       t.Name,
		Text:           t.Action.Text,
		ThreadID:       t.ThreadID,
		IdempotencyKey: fmt.Sprintf("cron-%s-%d", t.ID, time.Now().Unix()),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return store.RunError, "", "编码请求失败: " + err.Error(), false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.GatewayURL+"/api/cron/fire", bytes.NewReader(raw))
	if err != nil {
		return store.RunError, "", "构造请求失败: " + err.Error(), false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.Token)
	resp, err := r.fireClient.Do(req)
	if err != nil {
		return store.RunError, "", "调网关失败: " + err.Error(), false
	}
	defer func() { _ = resp.Body.Close() }()
	var out fireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return store.RunError, "", fmt.Sprintf("网关响应异常（HTTP %d）", resp.StatusCode), false
	}
	switch {
	case resp.StatusCode == http.StatusOK && out.Started:
		return store.RunStarted, out.ThreadID, "网关已受理，AI 轮次在线程里执行", false
	case resp.StatusCode == http.StatusOK && out.Reason == "thread_busy":
		return store.RunBusy, out.ThreadID, "线程已有运行中轮次，跳过本次", false
	case resp.StatusCode == http.StatusOK && out.Reason == "thread_missing":
		return store.RunError, "", "任务线程已不存在，已清除；下次运行将自动重建线程", true
	case resp.StatusCode == http.StatusUnauthorized:
		return store.RunError, out.ThreadID, "网关令牌无效（401）", false
	default:
		return store.RunError, out.ThreadID, fmt.Sprintf("网关返回 HTTP %d: %s", resp.StatusCode, out.Error), false
	}
}
