package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/adag/ada-go/internal/ports"
)

// ExportFormat 是 adag export 的输出格式（09 §10：JSONL / Markdown）。
type ExportFormat string

const (
	// ExportJSONL 每行一个对象（thread/entry/run/interaction），机器可回放。
	ExportJSONL ExportFormat = "jsonl"
	// ExportMarkdown 人读格式：线程标题 + 按 seq 排列的 entry。
	ExportMarkdown ExportFormat = "markdown"
)

// ExportRequest 是一次导出请求（09 §10：adag export --thread <id>）。
// ThreadID 为空时导出全部线程；EntriesBySeq 一律按线程内 seq 升序导出。
type ExportRequest struct {
	ThreadID string
	Format   ExportFormat
}

// ExportSummary 是导出统计（CLI 打印与测试断言用）。
type ExportSummary struct {
	Threads  int
	Entries  int
	Runs     int
	Lines    int
	Format   ExportFormat
	Snapshot string // 快照一致性证据：导出的 (线程数, 最大 seq) 指纹
}

// Export 用单个读事务拿一致快照导出（04 §5.7：导出用单个读事务拿一致快照；
// 结束立即 wal_checkpoint(PASSIVE)，避免长读阻止 checkpoint）。
//
// 只读句柄与 Core 句柄都能调它：读走 mode=ro 连接池，写不阻塞读。
// 导出的 entry 是线程全部 seq（含被 MoveHead 放弃的分支），因为 append-only 历史
// 才是审计与回放的真相（04 §5.7）。
func (r *Reader) Export(ctx context.Context, w io.Writer, req ExportRequest) (ExportSummary, error) {
	format := req.Format
	if format == "" {
		format = ExportJSONL
	}
	if format != ExportJSONL && format != ExportMarkdown {
		return ExportSummary{}, invalidInput(fmt.Sprintf("不支持的导出格式 %q（只支持 jsonl/markdown）", format))
	}
	summary := ExportSummary{Format: format}

	// 单个只读事务＝一致快照。WAL 下读不阻塞写，写也不会让这份快照半途变化。
	tx, err := r.read.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, storageBusy("开启导出快照事务失败: "+err.Error(), err)
	}

	threads, err := r.exportThreads(ctx, tx, req.ThreadID)
	if err != nil {
		_ = tx.Rollback()
		return summary, err
	}
	if err := r.writeExport(ctx, tx, w, format, threads, &summary); err != nil {
		_ = tx.Rollback()
		return summary, err
	}
	if err := tx.Commit(); err != nil {
		return summary, storageBusy("提交导出快照失败: "+err.Error(), err)
	}

	// 快照读完，立即 PASSIVE checkpoint（04 §5.7）。被并发读者挡住不算错误。
	if _, err := checkpoint(ctx, r.read, "PASSIVE"); err != nil {
		summary.Snapshot += " (checkpoint 未完成: " + err.Error() + ")"
	}
	return summary, nil
}

type exportThread struct {
	Thread ports.Thread
	// MaxSeq 用于快照指纹：同一份快照里每个线程的最大 seq 是确定的。
	MaxSeq int64
}

func (r *Reader) exportThreads(ctx context.Context, tx *sql.Tx, only string) ([]exportThread, error) {
	q := `SELECT ` + threadColumns + ` FROM threads`
	args := []any{}
	if only != "" {
		q += ` WHERE id = ?`
		args = append(args, only)
	}
	q += ` ORDER BY id ASC`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 导出读线程失败: %w", err)
	}
	defer rows.Close()
	var out []exportThread
	for rows.Next() {
		t, err := scanThread(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 导出读线程失败: %w", err)
		}
		if only != "" && t.ID != only {
			return nil, &NotFoundError{Kind: "thread", ID: only}
		}
		out = append(out, exportThread{Thread: t})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 导出读线程失败: %w", err)
	}
	for i := range out {
		var maxSeq sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM entries WHERE thread_id = ?`, out[i].Thread.ID).
			Scan(&maxSeq); err != nil {
			return nil, fmt.Errorf("sqlite: 导出读 seq 失败: %w", err)
		}
		out[i].MaxSeq = maxSeq.Int64
	}
	return out, nil
}

func (r *Reader) writeExport(ctx context.Context, tx *sql.Tx, w io.Writer, format ExportFormat,
	threads []exportThread, summary *ExportSummary) error {

	enc := json.NewEncoder(w)
	writeLine := func(obj any) error { return enc.Encode(obj) }

	fingerprint := make([]byte, 0, 64)
	for _, th := range threads {
		summary.Threads++
		fingerprint = append(fingerprint, fmt.Sprintf("%s:%d;", th.Thread.ID, th.MaxSeq)...)

		if format == ExportMarkdown {
			if _, err := fmt.Fprintf(w, "# %s\n\n- 线程：%s\n- head：%s\n- 归档：%v\n\n",
				orDefault(th.Thread.Title, "(无标题)"), th.Thread.ID,
				orDefault(th.Thread.HeadEntryID, "(空)"), th.Thread.Archived); err != nil {
				return fmt.Errorf("sqlite: 导出写出失败: %w", err)
			}
		} else {
			if err := writeLine(map[string]any{"type": "thread", "thread": th.Thread}); err != nil {
				return fmt.Errorf("sqlite: 导出写出失败: %w", err)
			}
			summary.Lines++
		}

		// entry：按 seq 升序，含所有分支（append-only 真相）。
		rows, err := tx.QueryContext(ctx, `SELECT `+entryColumns+` FROM entries WHERE thread_id = ? ORDER BY seq ASC`,
			th.Thread.ID)
		if err != nil {
			return fmt.Errorf("sqlite: 导出读 entry 失败: %w", err)
		}
		for rows.Next() {
			e, err := scanEntry(rows.Scan)
			if err != nil {
				rows.Close()
				return fmt.Errorf("sqlite: 导出读 entry 失败: %w", err)
			}
			summary.Entries++
			if format == ExportMarkdown {
				if err := writeEntryMarkdown(w, e); err != nil {
					rows.Close()
					return err
				}
			} else {
				if err := writeLine(map[string]any{"type": "entry", "entry": e}); err != nil {
					rows.Close()
					return fmt.Errorf("sqlite: 导出写出失败: %w", err)
				}
				summary.Lines++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("sqlite: 导出读 entry 失败: %w", err)
		}
		rows.Close()

		// runs 与 interactions 只在 JSONL 里给（它们不是模型上下文，Markdown 面向人读）。
		if format == ExportJSONL {
			runs, err := r.runsIn(ctx, tx, th.Thread.ID)
			if err != nil {
				return err
			}
			for _, run := range runs {
				if err := writeLine(map[string]any{"type": "run", "run": run}); err != nil {
					return fmt.Errorf("sqlite: 导出写出失败: %w", err)
				}
				summary.Lines++
				summary.Runs++
			}
			items, err := r.interactionsIn(ctx, tx, th.Thread.ID)
			if err != nil {
				return err
			}
			for _, it := range items {
				if err := writeLine(map[string]any{"type": "interaction", "interaction": it}); err != nil {
					return fmt.Errorf("sqlite: 导出写出失败: %w", err)
				}
				summary.Lines++
			}
		}
	}
	summary.Snapshot = fmt.Sprintf("threads=%d;entries=%d;指纹=%x", summary.Threads, summary.Entries, fingerprint)
	return nil
}

func (r *Reader) runsIn(ctx context.Context, tx *sql.Tx, threadID string) ([]Run, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, thread_id, status, started_at, ended_at, error_code, usage_json
FROM runs WHERE thread_id = ? ORDER BY started_at ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 导出读轮次失败: %w", err)
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
			return nil, fmt.Errorf("sqlite: 导出读轮次失败: %w", err)
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

func (r *Reader) interactionsIn(ctx context.Context, tx *sql.Tx, threadID string) ([]Interaction, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, thread_id, run_id, call_id, plugin_id, kind, title, body, options_json,
       status, response_json, created_at, resolved_at, expires_at
FROM interactions WHERE thread_id = ? ORDER BY created_at ASC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 导出读交互失败: %w", err)
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

func writeEntryMarkdown(w io.Writer, e ports.Entry) error {
	role := string(e.Role)
	if role == "" {
		role = string(e.Kind)
	}
	if _, err := fmt.Fprintf(w, "## %d · %s · %s\n\n```json\n%s\n```\n\n",
		e.Seq, e.CreatedAt.Format(time.RFC3339), role, string(e.Payload)); err != nil {
		return fmt.Errorf("sqlite: 导出写出失败: %w", err)
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// ---------------------------------------------------------------------------
// 在线备份与恢复
// ---------------------------------------------------------------------------

// Backup 用 VACUUM INTO 在运行中生成一致性数据库副本（04 §5.7 备份）。
//
// 关键约束：
//   - 用专用连接，只写目标文件、不改源库；
//   - 绝不复制 state.db / -wal / -shm（那是「拷到一个不确定的瞬间」）；
//   - 目标必须不存在：VACUUM INTO 遇到已存在文件本身会报错，这里提前给出人话；
//   - 结束后立即校验产物（integrity_check + schema 形状 + user_version），
//     校验不过就返回错误并删除这个半成品副本（只删自己刚写的目标文件）。
func (st *Store) Backup(ctx context.Context, dest string) (BackupInfo, error) {
	info := BackupInfo{Dest: dest}
	if dest == "" {
		return info, invalidInput("backup 需要目标路径")
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return info, fmt.Errorf("sqlite: 目标路径归一化失败 %s: %w", dest, err)
	}
	info.Dest = abs
	if _, err := os.Stat(abs); err == nil {
		return info, invalidInput(fmt.Sprintf("备份目标已存在：%s（不覆盖既有备份，避免误毁上一份可用副本）", abs))
	}
	if samePath(abs, st.DBPath()) {
		return info, invalidInput("备份目标不能是 state.db 本身")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return info, fmt.Errorf("sqlite: 创建备份目录失败: %w", err)
	}

	// 专用连接：与写队列/读池都分开，保证 VACUUM 不会和业务语句共享连接状态。
	conn, err := openSingle(ctx, singleDSN(st.DataDir()))
	if err != nil {
		return info, storageBusy("备份连接打不开: "+err.Error(), err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, abs); err != nil {
		return info, storageBusy("VACUUM INTO 失败: "+err.Error(), err)
	}
	if err := verifyBackupFile(ctx, abs); err != nil {
		_ = os.Remove(abs) // 只删自己刚写的半成品。
		return info, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return info, fmt.Errorf("sqlite: 备份文件写完后无法读取: %w", err)
	}
	info.Bytes = fi.Size()
	info.SchemaVersion = SchemaVersion()

	if err := st.RecordAudit(ctx, AuditRecord{
		Kind:   auditBackup,
		Detail: json.RawMessage(fmt.Sprintf(`{"dest":%q,"bytes":%d}`, abs, fi.Size())),
	}); err != nil {
		return info, err
	}
	return info, nil
}

// BackupInfo 是一次备份的结果摘要。
type BackupInfo struct {
	Dest          string `json:"dest"`
	Bytes         int64  `json:"bytes"`
	SchemaVersion int    `json:"schemaVersion"`
}

func verifyBackupFile(ctx context.Context, path string) error {
	db, err := openSingle(ctx, readOnlyDSNForFile(path))
	if err != nil {
		return storageCorrupt(fmt.Sprintf("备份产物打不开 %s: %v", path, err), err)
	}
	defer db.Close()
	report, err := checkIntegrity(ctx, db)
	if err != nil {
		return storageCorrupt(fmt.Sprintf("备份产物完整性检查未通过 %s：%s", path, report), err)
	}
	v, err := CurrentVersion(db)
	if err != nil {
		return storageCorrupt(err.Error(), err)
	}
	if v > SchemaVersion() {
		return storageCorrupt(fmt.Sprintf("备份产物 schema 版本 %d 高于本二进制 %d", v, SchemaVersion()), nil)
	}
	return nil
}

// samePath 比较两个路径是否指向同一个文件（避免把库备份进自己）。
func samePath(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	if aerr != nil || berr != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ai, bi)
}

// RestoreOptions 是恢复的可选行为。
type RestoreOptions struct {
	// AsidePrefix 是被换下的 state.db 的前缀，空时用 "state.db.pre-restore-"。
	// 被换下的库永远保留（恢复是运维动作，回滚材料不能丢）；由调用方/清理任务决定何时删。
	AsidePrefix string
}

// RestoreResult 是一次恢复的摘要。
type RestoreResult struct {
	Replaced      string    `json:"replaced"`
	Aside         string    `json:"aside,omitempty"`
	RemovedWAL    bool      `json:"removedWal"`
	RemovedShm    bool      `json:"removedShm"`
	BackupVersion int       `json:"backupSchemaVersion"`
	At            time.Time `json:"at"`
}

// Restore 用备份文件替换数据目录里的 state.db（04 §5.7：恢复时停 Core、校验备份、
// 原子替换并删除旧 -wal/-shm）。
//
// 「停 Core」这条前提在代码里由接收者保证：只有持有工作区独占锁的人才能调本方法，
// 而锁只在 Core 不运行时才可能拿到（没有 Store 也能单独 AcquireWorkspaceLock 做运维）。
// 删除 -wal/-shm 只发生在这里：Core 运行路径（OpenCore/Store/Reader）从不删这两个文件。
func (l *WorkspaceLock) Restore(ctx context.Context, backupPath string, opts RestoreOptions) (RestoreResult, error) {
	res := RestoreResult{}
	if l == nil {
		return res, invalidInput("Restore 必须由持有工作区独占锁的调用方执行（04 §5.7：恢复时停 Core）")
	}
	if backupPath == "" {
		return res, invalidInput("Restore 需要备份文件路径")
	}
	backupAbs, err := filepath.Abs(backupPath)
	if err != nil {
		return res, fmt.Errorf("sqlite: 备份路径归一化失败: %w", err)
	}
	if !fileExists(backupAbs) {
		return res, storageCorrupt(fmt.Sprintf("备份文件不存在：%s", backupAbs), nil)
	}
	if err := verifyBackupFile(ctx, backupAbs); err != nil {
		return res, err
	}

	dbPath := StateDBPath(l.dataDir)
	tmpPath := dbPath + ".restoring"
	_ = os.Remove(tmpPath) // 上一次崩溃留下的半成品（不是 state.db，也不是 -wal）。

	// 1) 把备份复制到同目录的临时名（同盘才可能原子 rename）。
	if err := copyFile(backupAbs, tmpPath, 0o600); err != nil {
		return res, storageCorrupt(fmt.Sprintf("复制备份到临时文件失败：%v", err), err)
	}

	// 2) 被换下的 state.db 先移开（保留可回滚的原文）。
	prefix := opts.AsidePrefix
	if prefix == "" {
		prefix = "state.db.pre-restore-"
	}
	aside := filepath.Join(filepath.Dir(dbPath), prefix+fmt.Sprint(time.Now().UTC().UnixNano()))
	hadCurrent := fileExists(dbPath)
	if hadCurrent {
		if err := os.Rename(dbPath, aside); err != nil {
			_ = os.Remove(tmpPath)
			return res, storageCorrupt(fmt.Sprintf("移开现有 state.db 失败：%v", err), err)
		}
	}

	// 3) 原子替换。失败就把移开的文件放回去。
	if err := os.Rename(tmpPath, dbPath); err != nil {
		if hadCurrent {
			_ = os.Rename(aside, dbPath)
		}
		_ = os.Remove(tmpPath)
		return res, storageCorrupt(fmt.Sprintf("原子替换 state.db 失败：%v", err), err)
	}

	// 4) 删除旧 -wal/-shm（此时 Core 已停，且新 state.db 与旧 WAL 不成对，留着必炸）。
	res.RemovedWAL = removeIfExists(dbPath + "-wal")
	res.RemovedShm = removeIfExists(dbPath + "-shm")

	res.Replaced = dbPath
	res.Aside = aside // 被换下的库永远保留：恢复是运维动作，回滚材料不能丢。
	if v, err := backupVersion(ctx, backupAbs); err == nil {
		res.BackupVersion = v
	}
	res.At = time.Now().UTC()
	return res, nil
}

func backupVersion(ctx context.Context, path string) (int, error) {
	db, err := openSingle(ctx, readOnlyDSNForFile(path))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	return CurrentVersion(db)
}

// removeIfExists 删一个文件；不存在返回 false（不是错误）。
func removeIfExists(path string) bool {
	if !fileExists(path) {
		return false
	}
	return os.Remove(path) == nil
}

// copyFile 全量复制（同步落盘：备份/恢复是运维动作，宁可慢也要写完）。
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Checkpoint 让调用方显式收敛 WAL（04 §5.7：停 Core 后可 wal_checkpoint(TRUNCATE) 再复制）。
// mode 只接受 PASSIVE/FULL/RESTART/TRUNCATE。
func (st *Store) Checkpoint(ctx context.Context, mode string) (bool, error) {
	switch mode {
	case "PASSIVE", "FULL", "RESTART", "TRUNCATE":
	default:
		return false, invalidInput(fmt.Sprintf("非法 wal_checkpoint 模式 %q", mode))
	}
	if mode != "PASSIVE" && st.w.Stats().Queued > 0 {
		return false, storageBusy("还有排队中的写任务，不能用 "+mode+" 模式 checkpoint（请先 Flush 或稍后再试）", nil)
	}
	return checkpoint(ctx, st.write, mode)
}

// Checkpoint 是只读句柄上的 PASSIVE checkpoint（长读结束后收敛 WAL）。
func (r *Reader) Checkpoint(ctx context.Context) error { return r.checkpointOnce(ctx, "PASSIVE") }
