package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// 编译期断言：生产实现必须满足端口（INV-2）。
var (
	_ ports.SessionStore = (*Store)(nil)
	_ ports.StateStore   = (*Store)(nil)
)

// Store 是 Core 侧的工作区存储句柄：持锁、可写、单写者协程。
//
// 一个工作区同一时刻只能有一个 Store 实例（OS 独占锁保证跨进程唯一，
// Config.WriterQueueSize 等参数在 Open 时冻结）。
type Store struct {
	*Reader
	write *sql.DB
	w     *writer
	lock  *WorkspaceLock
}

// Reader 是只读句柄（CLI 的 export/doctor、以及 Core 内部的读池）。
//
// 它只持有 mode=ro + query_only=ON 的连接：任何写语句在 SQLite 层就是 SQLITE_READONLY，
// 它也不迁移 schema（04 §5.7：migrate 只在 Core 启动、持锁后执行；doctor 只报告版本）。
type Reader struct {
	cfg          Config
	workspaceKey string
	hash         string
	read         *sql.DB
	// ownsRead 表示连接池由本 Reader 创建，Close 时可以关它；
	// Store 复用同一个池，所有权在 Store.Close。
	ownsRead bool
}

// Workspace 返回归一化后的工作区真实路径（threads.workspace 列的值，01 §4.2）。
func (r *Reader) Workspace() string { return r.workspaceKey }

// WorkspaceHash 返回 12 位身份键哈希（线程 id 前缀 th_<hash>_<ulid> 用它）。
func (r *Reader) WorkspaceHash() string { return r.hash }

// DataDir 返回数据目录。
func (r *Reader) DataDir() string { return r.cfg.DataDir }

// DBPath 返回 state.db 路径。
func (r *Reader) DBPath() string { return StateDBPath(r.cfg.DataDir) }

// WALPath 返回 state.db-wal 路径（04 §5.7：本包从不主动删除它）。
func (r *Reader) WALPath() string { return r.DBPath() + "-wal" }

// SHMPath 返回 state.db-shm 路径。
func (r *Reader) SHMPath() string { return r.DBPath() + "-shm" }

// Version 返回当前 user_version（只读，不迁移，04 §5.7：doctor 只报告版本）。
func (r *Reader) Version(ctx context.Context) (int, error) {
	v, err := CurrentVersion(r.read)
	if err != nil {
		return 0, storageCorrupt(err.Error(), err)
	}
	return v, nil
}

// Pragmas 回显连接/库级 PRAGMA 取值（doctor 与测试用它核对 04 §5.7 的配置）。
func (r *Reader) Pragmas(ctx context.Context) (PragmaState, error) {
	p, err := ReadPragmas(ctx, r.read)
	if err != nil {
		return p, storageCorrupt(err.Error(), err)
	}
	return p, nil
}

// WritePragmas 回显唯一写连接上的 PRAGMA 取值（04 §5.7 的库级配置在这里生效：
// journal_mode/synchronous/busy_timeout/foreign_keys/wal_autocheckpoint/journal_size_limit）。
// 注意只读连接池上的 synchronous 是 SQLite 默认值（读连接不写，不继承这个设置）。
func (st *Store) WritePragmas(ctx context.Context) (PragmaState, error) {
	p, err := ReadPragmas(ctx, st.write)
	if err != nil {
		return p, storageCorrupt(err.Error(), err)
	}
	return p, nil
}

// CheckIntegrity 跑 PRAGMA integrity_check（09 §10 doctor 检查项）。
// 返回损坏报告文本与 storage_corrupt 错误；ok 时 report 为 "ok"。
func (r *Reader) CheckIntegrity(ctx context.Context) (string, error) {
	report, err := checkIntegrity(ctx, r.read)
	if err != nil {
		return report, storageCorrupt(fmt.Sprintf("integrity_check 未通过：%s", report), err)
	}
	return report, nil
}

// LockPath 返回本工作区的锁文件路径。
func (r *Store) LockPath() string { return r.lock.Path() }

// QueueStats 返回单写者队列的观测快照。
func (r *Store) QueueStats() QueueStats { return r.w.Stats() }

// Flush 强制把 usage 合并窗口落库（测试与优雅退出前用；走同一条写队列，不破坏单写者）。
func (r *Store) Flush(ctx context.Context) error {
	return r.w.submit(ctx, "usage-flush", func(ctx context.Context, tx *sql.Tx) error {
		return insertUsageRows(ctx, tx, r.w.takePending(), &r.w.usageRows)
	})
}

// OpenCore 是唯一的写打开路径（04 §5.7）。顺序刻意如此：
//
//  1. 参数与身份键归一（01 §4.2）；
//  2. 网络盘判定：state.db 必须在本地文件系统，检测到网络盘或判不出来都拒绝启动；
//  3. 取 OS 独占锁：拿不到就返回 workspace_locked（带持有者 pid），此后再也不打开数据库；
//  4. 打开写连接池（WAL/synchronous/busy_timeout/foreign_keys/_txlock=immediate），
//     执行事务迁移并校验 schema 形状；
//  5. 若打开前存在 -wal（上次非正常退出）→ 让 SQLite 先自愈，再跑 integrity_check，
//     失败返回 storage_corrupt；本包绝不删除 -wal/-shm；
//  6. 打开只读连接池（mode=ro + query_only=ON）；
//  7. 启动唯一写协程；
//  8. 崩溃善后：把上次遗留的 running 轮次标记 interrupted、把过期交互标记 timeout。
func OpenCore(ctx context.Context, cfg Config) (*Store, error) {
	cfg, err := cfg.normalized()
	if err != nil {
		return nil, invalidInput(err.Error())
	}
	key, err := WorkspaceKey(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	hash, err := WorkspaceHash(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(key); err != nil {
		return nil, fail(domain.CodeWorkspaceMissing, fmt.Sprintf("工作区目录不存在或不可访问：%s", key), err)
	}

	// 2) 网络盘闸门（在创建任何文件之前）。
	if err := EnsureLocalDataDir(cfg.DataDir); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, storageCorrupt(fmt.Sprintf("创建数据目录失败 %s", cfg.DataDir), err)
	}

	// 3) OS 独占锁：拿不到就不打开数据库。
	lock, err := AcquireWorkspaceLock(cfg.DataDir, hash)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = lock.Release() }

	// 打开之前记录 -wal 是否存在：这是「上次非正常退出」的信号，
	// 用来决定要不要跑 integrity_check（SQLite 自己会做 WAL 恢复，我们不干预）。
	dbPath := StateDBPath(cfg.DataDir)
	hadWAL := fileExists(dbPath + "-wal")

	writeDB, err := openPool(ctx, writerDSN(cfg), 1) // 唯一写连接：池上限 1，物理上杜绝第二个写者
	if err != nil {
		cleanup()
		return nil, storageCorrupt("打开写连接失败: "+err.Error(), err)
	}
	if _, err := writeDB.ExecContext(ctx, `PRAGMA journal_mode`); err != nil {
		_ = writeDB.Close()
		cleanup()
		return nil, storageCorrupt("确认 journal_mode 失败: "+err.Error(), err)
	}

	if _, err := migrate(ctx, writeDB); err != nil {
		_ = writeDB.Close()
		cleanup()
		return nil, err // 迁移失败已经回滚：拒绝启动，不留半个 schema。
	}
	// 版本号对得上也要核对对象齐全：手工删过表的库照样拒绝启动（04 §5.7 失败方向）。
	// 这是一条 sqlite_master 查询，代价可忽略。
	if err := verifySchemaShape(ctx, writeDB); err != nil {
		_ = writeDB.Close()
		cleanup()
		return nil, err
	}

	if hadWAL && *cfg.VerifyOnRecovery {
		report, err := checkIntegrity(ctx, writeDB)
		if err != nil {
			_ = writeDB.Close()
			cleanup()
			return nil, storageCorrupt(fmt.Sprintf(
				"WAL 恢复后完整性检查未通过（report=%s）：请用 adag doctor 或从备份恢复（04 §5.7 故障语义）", report), err)
		}
	}

	readDB, err := openPool(ctx, readerDSN(cfg.DataDir), MaxReadConns)
	if err != nil {
		_ = writeDB.Close()
		cleanup()
		return nil, storageCorrupt("打开只读连接池失败: "+err.Error(), err)
	}

	base := &Reader{cfg: cfg, workspaceKey: key, hash: hash, read: readDB, ownsRead: true}
	st := &Store{Reader: base, write: writeDB, w: newWriter(writeDB, cfg), lock: lock}

	// 8) 崩溃善后（同步执行，不走写队列之前先保证状态机自洽）。
	if _, err := st.RecoverInterruptedRuns(ctx); err != nil {
		_ = st.Close(ctx)
		return nil, err
	}
	if _, err := st.ExpireInteractions(ctx); err != nil {
		_ = st.Close(ctx)
		return nil, err
	}
	return st, nil
}

// OpenReadOnly 打开只读句柄（CLI 的 export/doctor，04 §5.7「CLI 只读」）。
//
// 它不加工作区锁（读不阻塞写，也不该被写挡住），不迁移 schema，
// 并且用 mode=ro + query_only=ON 双重只读；state.db 不存在时直接报错而不是创建。
// Workspace 留空时按 DataDir 反推（只报告，不校验身份键）。
func OpenReadOnly(ctx context.Context, cfg Config) (*Reader, error) {
	if cfg.DataDir == "" {
		if cfg.Workspace == "" {
			return nil, invalidInput("OpenReadOnly 需要 Workspace 或 DataDir 其中之一")
		}
		dir, err := DefaultDataDir(cfg.Workspace)
		if err != nil {
			return nil, err
		}
		cfg.DataDir = dir
	}
	if cfg.Workspace == "" {
		cfg.Workspace = filepath.Dir(cfg.DataDir)
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.NewID == nil {
		cfg.NewID = newULID
	}
	key, err := WorkspaceKey(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	hash, err := WorkspaceHash(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	readDB, err := openReadOnlyDB(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	r := &Reader{cfg: cfg, workspaceKey: key, hash: hash, read: readDB, ownsRead: true}

	v, err := CurrentVersion(readDB)
	if err != nil {
		_ = readDB.Close()
		return nil, storageCorrupt(err.Error(), err)
	}
	if v != SchemaVersion() {
		_ = readDB.Close()
		return nil, storageCorrupt(fmt.Sprintf(
			"数据库 schema 版本 %d 与本二进制期望的 %d 不一致：只读命令不做迁移，请先让 Core 启动一次（04 §5.7）",
			v, SchemaVersion()), nil)
	}
	return r, nil
}

// Close 关闭只读句柄（Store 复用它，见 Store.Close）。
func (r *Reader) Close() error {
	if r == nil || r.read == nil || !r.ownsRead {
		return nil
	}
	if err := r.read.Close(); err != nil {
		return fmt.Errorf("sqlite: 关闭只读连接池失败: %w", err)
	}
	r.read = nil
	return nil
}

// Close 关闭 Core 侧句柄：取消在途写 → 排空队列 → checkpoint → 关连接 → 释放 OS 锁。
//
// 顺序不能换：先让写协程把已接受的写落库，再关连接，最后释放锁——
// 反过来会让第二个 Core 在本进程还有未提交事务时拿到锁。
func (st *Store) Close(ctx context.Context) error {
	if st == nil {
		return nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, st.cfg.CloseTimeout)
	defer cancel()

	var errs []string
	if err := st.w.close(timeoutCtx); err != nil {
		errs = append(errs, err.Error())
	}
	// 写已经全部落库，趁连接还开着做一次 PASSIVE checkpoint，收敛 WAL（04 §5.7 读侧规则）。
	if st.write != nil {
		if _, err := checkpoint(ctx, st.write, "PASSIVE"); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := st.Reader.Close(); err != nil {
		errs = append(errs, err.Error())
	}
	if st.write != nil {
		if err := st.write.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("关闭写连接池失败: %v", err))
		}
		st.write = nil
	}
	if err := st.lock.Release(); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fail(domain.CodeStorageBusy, "关闭存储时出现问题: "+strings.Join(errs, "; "), nil)
	}
	return nil
}

// checkpointOnce 在只读池上做一次 wal_checkpoint（只读连接也能做 PASSIVE checkpoint）。
func (r *Reader) checkpointOnce(ctx context.Context, mode string) error {
	if r.read == nil {
		return nil
	}
	_, err := checkpoint(ctx, r.read, mode)
	return err
}

// fileExists 是「存在且是普通文件」的判定。
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !fi.IsDir()
}

// ---------------------------------------------------------------------------
// ports.SessionStore：写侧（全部经唯一写协程）
// ---------------------------------------------------------------------------

// CreateThread 新建线程。id 形状 th_<workspaceHash>_<ulid>（01 §4.2、04 §5.7）：
// 本包不 import adapters/idgen，用 Config.NewID（默认 crypto/rand ULID）+ 自身身份键拼出，
// 保证「Core 生成、Gateway 靠前缀路由」这条契约不变。
func (st *Store) CreateThread(ctx context.Context, workspace, title string) (ports.Thread, error) {
	if workspace != "" {
		h, err := WorkspaceHash(workspace)
		if err != nil {
			return ports.Thread{}, err
		}
		if h != st.hash {
			return ports.Thread{}, invalidInput(fmt.Sprintf(
				"CreateThread 的 workspace %q 不属于本数据目录的工作区（身份键哈希 %s ≠ %s）",
				workspace, st.hash, h))
		}
	}
	id, err := st.cfg.NewID(ctx)
	if err != nil {
		return ports.Thread{}, err
	}
	thread := ports.Thread{
		ID:        "th_" + st.hash + "_" + id,
		Workspace: st.workspaceKey,
		Title:     title,
		CreatedAt: st.cfg.Now(),
		UpdatedAt: st.cfg.Now(),
		Archived:  false,
	}
	err = st.w.submit(ctx, "thread.create", func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO threads(id, workspace, title, head_entry_id, created_at, updated_at, archived)
VALUES(?, ?, ?, NULL, ?, ?, 0)`,
			thread.ID, thread.Workspace, thread.Title, ms(thread.CreatedAt), ms(thread.UpdatedAt))
		if err != nil {
			return fmt.Errorf("sqlite: 创建线程失败: %w", err)
		}
		return nil
	})
	if err != nil {
		return ports.Thread{}, err
	}
	return thread, nil
}

// AppendEntry 追加一条 entry：append-only、parent_id 形成 DAG、线程内唯一 seq（04 §5.7）。
//
// 契约细节：
//   - seq 在唯一写协程内分配（SELECT MAX(seq)+1 FOR UPDATE 语义由单写者 + BEGIN IMMEDIATE 提供）；
//   - ID 为空时由 Config.NewID 生成（ULID）；
//   - ParentID 非空时必须属于同一线程（否则拒绝，不猜父节点）；
//   - 写入后把 threads.head_entry_id 指向新 entry：线性追加正是靠这一步成立，
//     分支切换用 MoveHead，历史不改写（04 §5.7「编辑并重发」）。
func (st *Store) AppendEntry(ctx context.Context, e ports.Entry) (ports.Entry, error) {
	if err := validateEntry(e); err != nil {
		return ports.Entry{}, err
	}
	if e.ID == "" {
		id, err := st.cfg.NewID(ctx)
		if err != nil {
			return ports.Entry{}, err
		}
		e.ID = id
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = st.cfg.Now()
	}
	if e.Payload == nil {
		e.Payload = json.RawMessage("{}")
	}

	var assigned int64
	err := st.w.submit(ctx, "entry.append", func(ctx context.Context, tx *sql.Tx) error {
		seq, err := nextSeq(ctx, tx, e.ThreadID)
		if err != nil {
			return err
		}
		if err := requireThread(ctx, tx, e.ThreadID); err != nil {
			return err
		}
		if e.ParentID != "" {
			if err := requireEntryInThread(ctx, tx, e.ThreadID, e.ParentID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO entries(id, thread_id, parent_id, seq, kind, role, payload, tokens, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.ThreadID, nilIfEmpty(e.ParentID), seq, string(e.Kind), nilIfEmpty(string(e.Role)),
			[]byte(e.Payload), e.Tokens, ms(e.CreatedAt)); err != nil {
			return fmt.Errorf("sqlite: 追加 entry 失败: %w", err)
		}
		if err := setHead(ctx, tx, e.ThreadID, e.ID, e.CreatedAt); err != nil {
			return err
		}
		assigned = seq
		return nil
	})
	if err != nil {
		return ports.Entry{}, err
	}
	e.Seq = assigned
	return e, nil
}

// AppendCompaction 追加压缩 entry（04 §5.7：压缩也是一条 entry，
// payload 含摘要与 first_kept_entry_id）。失败路径用 failed=true 标记，历史不丢。
func (st *Store) AppendCompaction(ctx context.Context, threadID, parentID string, summary json.RawMessage, firstKeptEntryID string) (ports.Entry, error) {
	payload, err := json.Marshal(CompactionPayload{
		Summary:          orEmptyJSON(summary),
		FirstKeptEntryID: firstKeptEntryID,
	})
	if err != nil {
		return ports.Entry{}, invalidInput("压缩 entry 的 payload 无法编码: " + err.Error())
	}
	return st.AppendEntry(ctx, ports.Entry{
		ThreadID: threadID,
		ParentID: parentID,
		Kind:     domain.EntryCompaction,
		Role:     domain.RoleSystem,
		Payload:  payload,
	})
}

// MarkCompactionFailed 记录「摘要失败」标记（04 §5.7：失败不丢历史，只标记并继续用裁剪兜底）。
// 它是一条 compaction kind 的 entry，payload.failed=true 且没有 firstKeptEntryId：
// 回溯时这样的 entry 不作为压缩点（历史仍完整重放）。
func (st *Store) MarkCompactionFailed(ctx context.Context, threadID, parentID, reason string) (ports.Entry, error) {
	payload, err := json.Marshal(CompactionPayload{Failed: true, Reason: reason})
	if err != nil {
		return ports.Entry{}, invalidInput("压缩失败标记无法编码: " + err.Error())
	}
	return st.AppendEntry(ctx, ports.Entry{
		ThreadID: threadID,
		ParentID: parentID,
		Kind:     domain.EntryCompaction,
		Role:     domain.RoleSystem,
		Payload:  payload,
	})
}

// MoveHead 移动 head 指针实现分支：不改写历史（04 §5.7）。
// entryID 必须属于该线程；传空串表示退回「无 head」（空线程语义）。
func (st *Store) MoveHead(ctx context.Context, threadID, entryID string) error {
	return st.w.submit(ctx, "thread.moveHead", func(ctx context.Context, tx *sql.Tx) error {
		if err := requireThread(ctx, tx, threadID); err != nil {
			return err
		}
		if entryID != "" {
			if err := requireEntryInThread(ctx, tx, threadID, entryID); err != nil {
				return err
			}
		}
		return setHead(ctx, tx, threadID, entryID, st.cfg.Now())
	})
}

// RenameThread 重命名（08 thread.rename）。
func (st *Store) RenameThread(ctx context.Context, threadID, title string) error {
	return st.w.submit(ctx, "thread.rename", func(ctx context.Context, tx *sql.Tx) error {
		if err := requireThread(ctx, tx, threadID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE threads SET title = ?, updated_at = ? WHERE id = ?`,
			title, ms(st.cfg.Now()), threadID)
		if err != nil {
			return fmt.Errorf("sqlite: 重命名线程失败: %w", err)
		}
		return rowsAffectedCheck(res, "重命名未命中线程")
	})
}

// SetArchived 归档/恢复（08 thread.archive）。
func (st *Store) SetArchived(ctx context.Context, threadID string, archived bool) error {
	return st.w.submit(ctx, "thread.archive", func(ctx context.Context, tx *sql.Tx) error {
		if err := requireThread(ctx, tx, threadID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE threads SET archived = ?, updated_at = ? WHERE id = ?`,
			intOf(archived), ms(st.cfg.Now()), threadID)
		if err != nil {
			return fmt.Errorf("sqlite: 归档线程失败: %w", err)
		}
		return rowsAffectedCheck(res, "归档未命中线程")
	})
}

// DeleteThread 软删（08 thread.delete：软删 + 清理任务）。
//
// 04 §5.7 的 threads 表没有 deleted 列，DDL 已冻结，本包不发明列：
// 软删 = archived=1 + 一条 audit(kind='thread.deleted') 记录，物理清理由清理任务
// 按审计行执行（保证审计与回放不丢，04 §5.7「entry 一旦写入不可修改」）。
func (st *Store) DeleteThread(ctx context.Context, threadID string) error {
	return st.w.submit(ctx, "thread.delete", func(ctx context.Context, tx *sql.Tx) error {
		if err := requireThread(ctx, tx, threadID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET archived = 1, updated_at = ? WHERE id = ?`,
			ms(st.cfg.Now()), threadID); err != nil {
			return fmt.Errorf("sqlite: 软删线程失败: %w", err)
		}
		detail, _ := json.Marshal(map[string]any{"action": "softDelete", "threadId": threadID})
		if err := insertAudit(ctx, tx, auditRow{
			at: ms(st.cfg.Now()), kind: auditThreadDeleted, threadID: threadID, detail: detail,
		}); err != nil {
			return err
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// 共享的写侧小工具（只在写协程持有的 tx 里调用）
// ---------------------------------------------------------------------------

// CompactionPayload 是 compaction entry 的 payload 形状（04 §5.7 只说「含摘要与
// first_kept_entry_id」，具体 JSON 字段由本包定义并在此固化）。
type CompactionPayload struct {
	// Summary 是结构化摘要正文（04 §5.7：目标/已完成/未完成/改动文件/关键决策/风险）。
	Summary json.RawMessage `json:"summary"`
	// FirstKeptEntryID 是重放起点（04 §5.7「再从 first_kept_entry_id 重放」）。
	FirstKeptEntryID string `json:"firstKeptEntryId,omitempty"`
	// Failed 标记摘要失败（compaction_failed 语义）：回溯时不当作压缩点。
	Failed bool `json:"compactionFailed,omitempty"`
	// Reason 是失败原因（仅 Failed=true 时有值）。
	Reason string `json:"reason,omitempty"`
}

// nextSeq 在写事务里取线程内下一个序号（seq 由单写者协程分配，04 §5.7）。
// 单写者 + BEGIN IMMEDIATE 保证「读最大值 + 插一条」之间没有第二个写者；
// entries_seq 唯一索引是第二道闸（万一有，插入直接失败并回滚）。
func nextSeq(ctx context.Context, tx *sql.Tx, threadID string) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM entries WHERE thread_id = ?`, threadID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("sqlite: 分配 seq 失败: %w", err)
	}
	return seq, nil
}

// ms 把时间换成 UnixMilli（04 §5.7 的 created_at 列是 INTEGER）。
func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

// fromMS 把 UnixMilli 还原成 UTC 时间。
func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// intOf 把 bool 变成 SQLite 的 0/1。
func intOf(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nilIfEmpty 把空串写成 NULL（parent_id/role 列允许 NULL，空串会被当成合法值，语义模糊）。
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// orEmptyJSON 保证 payload 永不为 nil。
func orEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// validateEntry 做写前的形状校验（枚举、payload 上限）：畸形数据一律拒绝，不落库。
func validateEntry(e ports.Entry) error {
	if e.ThreadID == "" {
		return invalidInput("entry 缺少 threadId")
	}
	switch e.Kind {
	case domain.EntryMessage, domain.EntryCompaction, domain.EntryBranchSummary,
		domain.EntryModelChange, domain.EntryPluginState, domain.EntryCustom:
	default:
		return invalidInput(fmt.Sprintf("entry.kind %q 不在 04 §5.7 的枚举里", e.Kind))
	}
	switch e.Role {
	case "", domain.RoleUser, domain.RoleAssistant, domain.RoleTool, domain.RoleSystem:
	default:
		return invalidInput(fmt.Sprintf("entry.role %q 不在 04 §5.7 的枚举里", e.Role))
	}
	if len(e.Payload) > MaxPayloadBytes {
		return invalidInput(fmt.Sprintf("entry payload %d 字节超过上限 %d", len(e.Payload), MaxPayloadBytes))
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return invalidInput("entry payload 不是合法 JSON（04 §5.7：payload BLOB 存 JSON）")
	}
	return nil
}

// requireThread 在线程不存在时给出可判定错误（而不是 FK 的模糊报错）。
func requireThread(ctx context.Context, tx *sql.Tx, threadID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM threads WHERE id = ?`, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return invalidInput(fmt.Sprintf("线程不存在：%s", threadID))
	}
	if err != nil {
		return fmt.Errorf("sqlite: 查询线程失败: %w", err)
	}
	return nil
}

// requireEntryInThread 校验 entry 属于该线程（DAG 不能跨线程牵，分支不能指向别人的历史）。
func requireEntryInThread(ctx context.Context, tx *sql.Tx, threadID, entryID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM entries WHERE id = ? AND thread_id = ?`, entryID, threadID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return invalidInput(fmt.Sprintf("entry %s 不属于线程 %s：DAG 的父节点必须在同一线程内", entryID, threadID))
	}
	if err != nil {
		return fmt.Errorf("sqlite: 查询 entry 失败: %w", err)
	}
	return nil
}

// setHead 移动 head 指针（唯一的「分支」动作，04 §5.7）。
func setHead(ctx context.Context, tx *sql.Tx, threadID, entryID string, at time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET head_entry_id = ?, updated_at = ? WHERE id = ?`,
		nilIfEmpty(entryID), ms(at), threadID); err != nil {
		return fmt.Errorf("sqlite: 更新 head_entry_id 失败: %w", err)
	}
	return nil
}

// rowsAffectedCheck 把「没命中任何行」变成显式错误。
func rowsAffectedCheck(res sql.Result, msg string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	if n == 0 {
		return invalidInput(msg)
	}
	return nil
}
