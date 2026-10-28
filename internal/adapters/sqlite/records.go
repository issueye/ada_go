package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// 轮次状态（runs.status）。04 §5.7 没有给 runs 的取值枚举，这里照 08 §8.2
// evt.run.status 与 02 §5.3 的终态集合固化，写入时校验（不吞未知值）。
const (
	RunRunning        = "running"
	RunWaitingInput   = "waiting_input"
	RunFinishing      = "finishing"
	RunCompleted      = "completed"
	RunCancelled      = "cancelled"
	RunError          = "error"
	RunInterrupted    = "interrupted"
	RunBudgetExceeded = "budget_exceeded"
)

// runStatuses 是合法状态集合（02 §5.3 终止条件表 + evt.run.status 中间态）。
var runStatuses = map[string]bool{
	RunRunning: true, RunWaitingInput: true, RunFinishing: true,
	RunCompleted: true, RunCancelled: true, RunError: true,
	RunInterrupted: true, RunBudgetExceeded: true,
}

// terminalRunStatuses 是终态集合（RecoverInterruptedRuns 只处理非终态）。
var terminalRunStatuses = map[string]bool{
	RunCompleted: true, RunCancelled: true, RunError: true,
	RunInterrupted: true, RunBudgetExceeded: true,
}

// Run 是一次轮次记录（04 §5.7 runs 表）。
type Run struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"threadId"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
	ErrorCode string    `json:"errorCode,omitempty"`
	// UsageJSON 是终态时的用量快照（runs.usage_json 列是 TEXT，存 JSON 文本）。
	UsageJSON json.RawMessage `json:"usage,omitempty"`
}

// AuditRecord 是一条审计（04 §5.7 audit 表；09 §10 审计范围）。
type AuditRecord struct {
	ID       int64           `json:"id"`
	At       time.Time       `json:"at"`
	Kind     string          `json:"kind"`
	ThreadID string          `json:"threadId,omitempty"`
	PluginID string          `json:"pluginId,omitempty"`
	Detail   json.RawMessage `json:"detail,omitempty"`
}

// 审计类型（本包用到的几种；插件侧的 kind 由 coreproto 传入）。
const (
	auditThreadDeleted = "thread.deleted"
	auditPluginChanged = "plugin.changed"
	auditBackup        = "storage.backup"
	auditRestore       = "storage.restore"
)

// UsageRecord 是一次模型请求用量（04 §5.7 usage 表、domain.Usage）。
type UsageRecord struct {
	ThreadID     string    `json:"threadId"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int64     `json:"inputTokens"`
	OutputTokens int64     `json:"outputTokens"`
	CacheTokens  int64     `json:"cacheTokens"`
	CostMicros   int64     `json:"costMicros"`
	CreatedAt    time.Time `json:"createdAt"`
	// CreatedAtMS 是合并窗口用的毫秒时间戳；为 0 时由 RecordUsage 从 CreatedAt/Now 填。
	CreatedAtMS int64 `json:"-"`
}

// PluginMetaRecord 是插件安装元信息（04 §5.7 plugin_meta 表）。
//
// 04 §5.7 明确：plugin install 只写插件目录、不直接写 plugin_meta；启用/禁用由
// Gateway 发 RPC、Core 落库——所以本包的写入口只有下面的 UpsertPluginMeta /
// SetPluginEnabled，它们都只可能由持锁的 Core 调用。
type PluginMetaRecord struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Enabled     bool      `json:"enabled"`
	Fingerprint string    `json:"fingerprint"`
	InstalledAt time.Time `json:"installedAt"`
	LastError   string    `json:"lastError,omitempty"`
}

// ---------------------------------------------------------------------------
// runs
// ---------------------------------------------------------------------------

// StartRun 记录轮次开始（02 §5.3 步骤 2）。status 留空按 running。
func (st *Store) StartRun(ctx context.Context, run Run) (Run, error) {
	if run.ID == "" {
		id, err := st.cfg.NewID(ctx)
		if err != nil {
			return Run{}, err
		}
		run.ID = id
	}
	if run.ThreadID == "" {
		return Run{}, invalidInput("run 缺少 threadId")
	}
	if run.Status == "" {
		run.Status = RunRunning
	}
	if !runStatuses[run.Status] {
		return Run{}, invalidInput(fmt.Sprintf("run.status %q 不在允许的轮次状态里", run.Status))
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = st.cfg.Now()
	}
	err := st.w.submit(ctx, "run.start", func(ctx context.Context, tx *sql.Tx) error {
		if err := requireThread(ctx, tx, run.ThreadID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO runs(id, thread_id, status, started_at, ended_at, error_code, usage_json)
VALUES(?, ?, ?, ?, NULL, NULL, NULL)`,
			run.ID, run.ThreadID, run.Status, ms(run.StartedAt))
		if err != nil {
			return fmt.Errorf("sqlite: 记录轮次开始失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// FinishRun 记录轮次终态（02 §5.3 步骤 8：恰好一个终态）。
// 已经处于终态的 run 再结束会被拒绝——INV-9「错误路径也不能漏发/重发」在存储层的对应。
func (st *Store) FinishRun(ctx context.Context, runID, status, errorCode string, usage json.RawMessage) error {
	if !runStatuses[status] {
		return invalidInput(fmt.Sprintf("run.status %q 不在允许的轮次状态里", status))
	}
	if !terminalRunStatuses[status] {
		return invalidInput(fmt.Sprintf("FinishRun 需要终态，收到中间态 %q", status))
	}
	return st.w.submit(ctx, "run.finish", func(ctx context.Context, tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, runID).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return &NotFoundError{Kind: "run", ID: runID}
		}
		if err != nil {
			return fmt.Errorf("sqlite: 读取轮次状态失败: %w", err)
		}
		if terminalRunStatuses[cur] {
			return invalidInput(fmt.Sprintf("轮次 %s 已经是终态 %s，不允许再次结束", runID, cur))
		}
		var usageText any
		if len(usage) > 0 {
			usageText = string(usage)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE runs SET status = ?, ended_at = ?, error_code = ?, usage_json = ? WHERE id = ?`,
			status, ms(st.cfg.Now()), nilIfEmpty(errorCode), usageText, runID); err != nil {
			return fmt.Errorf("sqlite: 记录轮次终态失败: %w", err)
		}
		return nil
	})
}

// RecoverInterruptedRuns 把上次进程遗留的非终态轮次标记为 interrupted
// （02 §5.3 终态表：Core 崩溃/重启 → 运行轮标记为中断，可重试）。
// 由 OpenCore 在启动时调用一次。
func (st *Store) RecoverInterruptedRuns(ctx context.Context) (int, error) {
	marked := 0
	err := st.w.submit(ctx, "run.recover", func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
UPDATE runs SET status = ?, ended_at = ?, error_code = ?
WHERE ended_at IS NULL AND status NOT IN ('completed','cancelled','error','interrupted','budget_exceeded')`,
			RunInterrupted, ms(st.cfg.Now()), domain.CodeName(domain.CodeCoreUnavailable))
		if err != nil {
			return fmt.Errorf("sqlite: 标记中断轮次失败: %w", err)
		}
		n, err := res.RowsAffected()
		if err == nil {
			marked = int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}

// GetRun 读一次轮次（只读）。
func (r *Reader) GetRun(ctx context.Context, runID string) (Run, error) {
	var (
		run                  Run
		ended                sql.NullInt64
		errorCode, usageText sql.NullString
		startedAt            int64
	)
	err := r.read.QueryRowContext(ctx, `
SELECT id, thread_id, status, started_at, ended_at, error_code, usage_json
FROM runs WHERE id = ?`, runID).
		Scan(&run.ID, &run.ThreadID, &run.Status, &startedAt, &ended, &errorCode, &usageText)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, &NotFoundError{Kind: "run", ID: runID}
	}
	if err != nil {
		return Run{}, fmt.Errorf("sqlite: 读取轮次失败: %w", err)
	}
	run.StartedAt = fromMS(startedAt)
	if ended.Valid {
		run.EndedAt = fromMS(ended.Int64)
	}
	run.ErrorCode = errorCode.String
	if usageText.String != "" {
		run.UsageJSON = json.RawMessage(usageText.String)
	}
	return run, nil
}

// ListRuns 列线程的轮次（按 started_at 倒序，只读）。
func (r *Reader) ListRuns(ctx context.Context, threadID string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = DefaultThreadListLimit
	}
	rows, err := r.read.QueryContext(ctx, `
SELECT id, thread_id, status, started_at, ended_at, error_code, usage_json
FROM runs WHERE thread_id = ? ORDER BY started_at DESC LIMIT ?`, threadID, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 列轮次失败: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var (
			run                  Run
			ended                sql.NullInt64
			errorCode, usageText sql.NullString
			startedAt            int64
		)
		if err := rows.Scan(&run.ID, &run.ThreadID, &run.Status, &startedAt, &ended, &errorCode, &usageText); err != nil {
			return nil, fmt.Errorf("sqlite: 读取轮次行失败: %w", err)
		}
		run.StartedAt = fromMS(startedAt)
		if ended.Valid {
			run.EndedAt = fromMS(ended.Int64)
		}
		run.ErrorCode = errorCode.String
		if usageText.String != "" {
			run.UsageJSON = json.RawMessage(usageText.String)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// interactions（04 §5.7 interactions 表；恰好解析一次的持久化，03 §5.6）
// ---------------------------------------------------------------------------

// Interaction 是落库后的交互记录（ports.InteractionRequest + 解析结果）。
type Interaction struct {
	ports.InteractionRequest
	Status     domain.InteractionStatus `json:"status"`
	Value      string                   `json:"value,omitempty"`
	ResolvedAt time.Time                `json:"resolvedAt,omitempty"`
}

// CreateInteraction 持久化一条待处理交互（pending）。
//
// 03 §5.6/ports 的约束：同一线程最多一个 pending——已有 pending 时返回 thread_busy，
// 由调用方（InteractionBroker 实现）决定是排队还是报错；这里不静默覆盖。
func (st *Store) CreateInteraction(ctx context.Context, req ports.InteractionRequest) error {
	if req.ID == "" {
		id, err := st.cfg.NewID(ctx)
		if err != nil {
			return err
		}
		req.ID = id
	}
	if req.ThreadID == "" {
		return invalidInput("交互缺少 threadId")
	}
	switch req.Kind {
	case domain.InteractionConfirm, domain.InteractionSelect, domain.InteractionInput:
	default:
		return invalidInput(fmt.Sprintf("交互 kind %q 不在 04 §5.7 的枚举里", req.Kind))
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = st.cfg.Now()
	}
	options := any(nil)
	if len(req.Options) > 0 {
		b, err := json.Marshal(req.Options)
		if err != nil {
			return invalidInput("交互 options 无法编码: " + err.Error())
		}
		options = string(b)
	}
	return st.w.submit(ctx, "interaction.create", func(ctx context.Context, tx *sql.Tx) error {
		if err := requireThread(ctx, tx, req.ThreadID); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM interactions WHERE thread_id = ? AND status = 'pending'`, req.ThreadID).
			Scan(&pending); err != nil {
			return fmt.Errorf("sqlite: 统计待处理交互失败: %w", err)
		}
		if pending > 0 {
			return fail(domain.CodeThreadBusy, fmt.Sprintf(
				"线程 %s 已有 %d 个待处理交互：同一线程最多一个（03 §5.6）", req.ThreadID, pending), nil)
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO interactions(id, thread_id, run_id, call_id, plugin_id, kind, title, body, options_json,
                         status, response_json, created_at, resolved_at, expires_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', NULL, ?, NULL, ?)`,
			req.ID, req.ThreadID, req.RunID, nilIfEmpty(req.CallID), req.PluginID, string(req.Kind),
			req.Title, req.Body, options, ms(req.CreatedAt),
			nilIfEmptyMS(req.ExpiresAt))
		if err != nil {
			return fmt.Errorf("sqlite: 持久化交互失败: %w", err)
		}
		return nil
	})
}

// ResolveInteraction 恰好一次地解析交互（pending → accepted|denied|cancelled|timeout|unavailable）。
//
// 幂等：重复响应返回首次结果（03 §5.6「重复响应幂等返回首次结果」）。
// 期限：已过期且仍 pending 的交互，只有 timeout/unavailable 能把它写成终态；
// accepted/denied/cancelled 一律拒绝（INV-4：过期不能被判成放行）。
func (st *Store) ResolveInteraction(ctx context.Context, threadID, requestID string,
	status domain.InteractionStatus, value string) (ports.InteractionResolution, error) {
	switch status {
	case domain.InteractionAccepted, domain.InteractionDenied, domain.InteractionCancelled,
		domain.InteractionTimeout, domain.InteractionUnavailable:
	default:
		return ports.InteractionResolution{}, invalidInput(fmt.Sprintf(
			"交互状态 %q 不是可写入的解析结果（pending 不是解析结果）", status))
	}
	res := ports.InteractionResolution{RequestID: requestID, Status: status, Value: value}
	err := st.w.submit(ctx, "interaction.resolve", func(ctx context.Context, tx *sql.Tx) error {
		var (
			curStatus             string
			createdAt, resolvedAt sql.NullInt64
			expiresAt             sql.NullInt64
			resp                  sql.NullString
		)
		err := tx.QueryRowContext(ctx, `
SELECT status, created_at, resolved_at, expires_at, response_json
FROM interactions WHERE id = ? AND thread_id = ?`, requestID, threadID).
			Scan(&curStatus, &createdAt, &resolvedAt, &expiresAt, &resp)
		if errors.Is(err, sql.ErrNoRows) {
			return &NotFoundError{Kind: "interaction", ID: requestID}
		}
		if err != nil {
			return fmt.Errorf("sqlite: 读取交互失败: %w", err)
		}
		if curStatus != string(domain.InteractionPending) {
			// 已经解析过：幂等返回首次结果。
			var v struct {
				Value string `json:"value"`
			}
			if resp.String != "" {
				_ = json.Unmarshal([]byte(resp.String), &v)
			}
			res.Status = domain.InteractionStatus(curStatus)
			res.Value = v.Value
			if resolvedAt.Valid {
				res.ResolvedAt = fromMS(resolvedAt.Int64)
			} else {
				res.ResolvedAt = st.cfg.Now()
			}
			return nil
		}
		expired := expiresAt.Valid && expiresAt.Int64 <= ms(st.cfg.Now())
		if expired && status.Direction() == domain.FailOpen {
			return fail(domain.CodePluginDenied, fmt.Sprintf(
				"交互 %s 已过期限，不能再被接受（INV-4：过期必须按拒绝处理）", requestID), nil)
		}
		detail := map[string]any{"value": value}
		b, _ := json.Marshal(detail)
		if _, err := tx.ExecContext(ctx, `
UPDATE interactions SET status = ?, response_json = ?, resolved_at = ? WHERE id = ? AND thread_id = ?`,
			string(status), string(b), ms(st.cfg.Now()), requestID, threadID); err != nil {
			return fmt.Errorf("sqlite: 解析交互失败: %w", err)
		}
		res.ResolvedAt = st.cfg.Now()
		return nil
	})
	if err != nil {
		return ports.InteractionResolution{}, err
	}
	return res, nil
}

// ListPendingInteractions 列出线程的待处理交互（重启恢复、interaction.list，08 §8.2）。
func (r *Reader) ListPendingInteractions(ctx context.Context, threadID string) ([]Interaction, error) {
	return r.listInteractions(ctx, threadID, `status = 'pending'`)
}

// ListInteractions 列出线程的全部交互（审计/排障用）。
func (r *Reader) ListInteractions(ctx context.Context, threadID string) ([]Interaction, error) {
	return r.listInteractions(ctx, threadID, `1=1`)
}

func (r *Reader) listInteractions(ctx context.Context, threadID, cond string) ([]Interaction, error) {
	rows, err := r.read.QueryContext(ctx, `
SELECT id, thread_id, run_id, call_id, plugin_id, kind, title, body, options_json,
       status, response_json, created_at, resolved_at, expires_at
FROM interactions WHERE thread_id = ? AND `+cond+` ORDER BY created_at ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读交互失败: %w", err)
	}
	defer rows.Close()
	var out []Interaction
	for rows.Next() {
		it, err := scanInteraction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func scanInteraction(rows *sql.Rows) (Interaction, error) {
	var (
		it                               Interaction
		call                             sql.NullString
		options                          sql.NullString
		resp                             sql.NullString
		createdAt, resolvedAt, expiresAt sql.NullInt64
		kind, status                     string
	)
	err := rows.Scan(&it.ID, &it.ThreadID, &it.RunID, &call, &it.PluginID, &kind, &it.Title, &it.Body,
		&options, &status, &resp, &createdAt, &resolvedAt, &expiresAt)
	if err != nil {
		return Interaction{}, fmt.Errorf("sqlite: 读取交互行失败: %w", err)
	}
	it.CallID = call.String
	it.Kind = domain.InteractionKind(kind)
	it.Status = domain.InteractionStatus(status)
	it.CreatedAt = fromMS(createdAt.Int64)
	if resolvedAt.Valid {
		it.ResolvedAt = fromMS(resolvedAt.Int64)
	}
	if expiresAt.Valid {
		it.ExpiresAt = fromMS(expiresAt.Int64)
	}
	if options.String != "" {
		_ = json.Unmarshal([]byte(options.String), &it.Options)
	}
	if resp.String != "" {
		var v struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal([]byte(resp.String), &v)
		it.Value = v.Value
	}
	return it, nil
}

// ExpireInteractions 把已过期限但仍 pending 的交互解析为 timeout（INV-4：按拒绝处理）。
// 由 OpenCore 启动时与 Broker 的定时任务调用。
func (st *Store) ExpireInteractions(ctx context.Context) (int, error) {
	now := st.cfg.Now()
	count := 0
	err := st.w.submit(ctx, "interaction.expire", func(ctx context.Context, tx *sql.Tx) error {
		b, _ := json.Marshal(map[string]any{"value": "", "reason": "expired"})
		res, err := tx.ExecContext(ctx, `
UPDATE interactions SET status = ?, response_json = ?, resolved_at = ?
WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= ?`,
			string(domain.InteractionTimeout), string(b), ms(now), ms(now))
		if err != nil {
			return fmt.Errorf("sqlite: 过期交互失败: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			count = int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// usage / audit / plugin_meta
// ---------------------------------------------------------------------------

// RecordUsage 记一次用量（04 §5.7：usage 可按窗口合并，绝不丢 entry 或交互）。
func (st *Store) RecordUsage(ctx context.Context, u UsageRecord) error {
	if u.ThreadID == "" || u.Provider == "" || u.Model == "" {
		return invalidInput("usage 需要 threadId/provider/model")
	}
	if u.CreatedAtMS == 0 {
		if u.CreatedAt.IsZero() {
			u.CreatedAt = st.cfg.Now()
		}
		u.CreatedAtMS = ms(u.CreatedAt)
	}
	return st.w.submitUsage(u)
}

// UsageAggregate 是 usage 表的聚合视图（08 usage.get）。
type UsageAggregate struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Input    int64  `json:"inputTokens"`
	Output   int64  `json:"outputTokens"`
	Cache    int64  `json:"cacheTokens"`
	Cost     int64  `json:"costMicros"`
}

// UsageByThread 返回线程的用量汇总（只读；provider+model 分组）。
func (r *Reader) UsageByThread(ctx context.Context, threadID string) ([]UsageAggregate, error) {
	return r.usageQuery(ctx, `WHERE thread_id = ?`, threadID)
}

// UsageWorkspace 返回整个工作区的用量汇总（08 usage.get 的工作区口径）。
func (r *Reader) UsageWorkspace(ctx context.Context) ([]UsageAggregate, error) {
	return r.usageQuery(ctx, `WHERE 1=1`)
}

func (r *Reader) usageQuery(ctx context.Context, where string, args ...any) ([]UsageAggregate, error) {
	rows, err := r.read.QueryContext(ctx, `
SELECT provider, model,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_tokens),0), COALESCE(SUM(cost_micros),0)
FROM usage `+where+`
GROUP BY provider, model ORDER BY provider, model`, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 汇总用量失败: %w", err)
	}
	defer rows.Close()
	var out []UsageAggregate
	for rows.Next() {
		var a UsageAggregate
		if err := rows.Scan(&a.Provider, &a.Model, &a.Input, &a.Output, &a.Cache, &a.Cost); err != nil {
			return nil, fmt.Errorf("sqlite: 读取用量行失败: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecordAudit 写一条审计（09 §10：交互请求/响应、工具执行、插件加载/重载/禁用、鉴权失败、配置变更）。
func (st *Store) RecordAudit(ctx context.Context, rec AuditRecord) error {
	if rec.Kind == "" {
		return invalidInput("audit 需要 kind")
	}
	if rec.At.IsZero() {
		rec.At = st.cfg.Now()
	}
	return st.w.submit(ctx, "audit.append", func(ctx context.Context, tx *sql.Tx) error {
		return insertAudit(ctx, tx, auditRow{
			at: ms(rec.At), kind: rec.Kind, threadID: rec.ThreadID, pluginID: rec.PluginID, detail: rec.Detail,
		})
	})
}

type auditRow struct {
	id       int64
	at       int64
	kind     string
	threadID string
	pluginID string
	detail   json.RawMessage
}

func insertAudit(ctx context.Context, tx *sql.Tx, r auditRow) error {
	var detail any
	if len(r.detail) > 0 {
		detail = string(r.detail)
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO audit(at, kind, thread_id, plugin_id, detail_json) VALUES(?, ?, ?, ?, ?)`,
		r.at, r.kind, nilIfEmpty(r.threadID), nilIfEmpty(r.pluginID), detail)
	if err != nil {
		return fmt.Errorf("sqlite: 写审计失败: %w", err)
	}
	return nil
}

// ListAudit 读审计（默认按时间倒序；since 为零值时不加时间下界）。
func (r *Reader) ListAudit(ctx context.Context, limit int, since time.Time) ([]AuditRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT id, at, kind, thread_id, plugin_id, detail_json FROM audit`
	args := []any{}
	where := []string{}
	if !since.IsZero() {
		where = append(where, `at >= ?`)
		args = append(args, ms(since))
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读审计失败: %w", err)
	}
	defer rows.Close()
	var out []AuditRecord
	for rows.Next() {
		var (
			rec                    AuditRecord
			thread, plugin, detail sql.NullString
			at                     int64
		)
		if err := rows.Scan(&rec.ID, &at, &rec.Kind, &thread, &plugin, &detail); err != nil {
			return nil, fmt.Errorf("sqlite: 读取审计行失败: %w", err)
		}
		rec.At = fromMS(at)
		rec.ThreadID = thread.String
		rec.PluginID = plugin.String
		if detail.String != "" {
			rec.Detail = json.RawMessage(detail.String)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// PurgeAudit 按保留期清理审计行（09 §10：默认保留 30 天）。
// 只清 audit：04 §5.7 的 entries 永不物理删除（审计与回放依赖它）。
func (st *Store) PurgeAudit(ctx context.Context, olderThan time.Time) (int, error) {
	if olderThan.IsZero() {
		return 0, invalidInput("PurgeAudit 需要明确的保留期上界，不接受零值（防误删）")
	}
	count := 0
	err := st.w.submit(ctx, "audit.purge", func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM audit WHERE at < ?`, ms(olderThan))
		if err != nil {
			return fmt.Errorf("sqlite: 清理审计失败: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			count = int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// UpsertPluginMeta 写入/更新插件元信息（Core 侧；04 §5.7 的 plugin_meta）。
func (st *Store) UpsertPluginMeta(ctx context.Context, m PluginMetaRecord) error {
	if m.ID == "" || m.Fingerprint == "" {
		return invalidInput("plugin_meta 需要 id 与 fingerprint")
	}
	if m.InstalledAt.IsZero() {
		m.InstalledAt = st.cfg.Now()
	}
	return st.w.submit(ctx, "plugin.upsertMeta", func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO plugin_meta(id, version, enabled, fingerprint, installed_at, last_error)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  version = excluded.version,
  enabled = excluded.enabled,
  fingerprint = excluded.fingerprint,
  installed_at = excluded.installed_at,
  last_error = excluded.last_error`,
			m.ID, m.Version, intOf(m.Enabled), m.Fingerprint, ms(m.InstalledAt), nilIfEmpty(m.LastError))
		if err != nil {
			return fmt.Errorf("sqlite: 写 plugin_meta 失败: %w", err)
		}
		return nil
	})
}

// SetPluginEnabled 启用/禁用插件（08 plugin.setEnabled 的落库面）。
func (st *Store) SetPluginEnabled(ctx context.Context, pluginID string, enabled bool, lastError string) error {
	return st.w.submit(ctx, "plugin.setEnabled", func(ctx context.Context, tx *sql.Tx) error {
		var got string
		err := tx.QueryRowContext(ctx, `SELECT id FROM plugin_meta WHERE id = ?`, pluginID).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			return &NotFoundError{Kind: "plugin_meta", ID: pluginID}
		}
		if err != nil {
			return fmt.Errorf("sqlite: 读取 plugin_meta 失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE plugin_meta SET enabled = ?, last_error = ? WHERE id = ?`,
			intOf(enabled), nilIfEmpty(lastError), pluginID); err != nil {
			return fmt.Errorf("sqlite: 更新 plugin_meta 失败: %w", err)
		}
		return insertAudit(ctx, tx, auditRow{
			at: ms(st.cfg.Now()), kind: auditPluginChanged, pluginID: pluginID,
			detail: json.RawMessage(fmt.Sprintf(`{"enabled":%v}`, enabled)),
		})
	})
}

// ListPluginMeta 列插件元信息（只读）。
func (r *Reader) ListPluginMeta(ctx context.Context) ([]PluginMetaRecord, error) {
	rows, err := r.read.QueryContext(ctx, `
SELECT id, version, enabled, fingerprint, installed_at, last_error
FROM plugin_meta ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 读 plugin_meta 失败: %w", err)
	}
	defer rows.Close()
	var out []PluginMetaRecord
	for rows.Next() {
		var (
			m           PluginMetaRecord
			enabled     int
			installedAt int64
			LastError   sql.NullString
		)
		if err := rows.Scan(&m.ID, &m.Version, &enabled, &m.Fingerprint, &installedAt, &LastError); err != nil {
			return nil, fmt.Errorf("sqlite: 读取 plugin_meta 行失败: %w", err)
		}
		m.Enabled = enabled != 0
		m.InstalledAt = fromMS(installedAt)
		m.LastError = LastError.String
		out = append(out, m)
	}
	return out, rows.Err()
}

// nilIfEmptyMS 把零时间写成 NULL（expires_at 允许 NULL＝永不过期）。
func nilIfEmptyMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ms(t)
}
