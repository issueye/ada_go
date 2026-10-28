package sqlite

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// NotFoundError 表示按键找不到对象（线程/entry/交互）。
//
// 08 §8.5 的 21 个业务码里没有通用的「资源不存在」码（1001 是 workspace_missing、
// 3001 是 tool_not_found，都不该挪用），所以这里给可判定的类型化错误 + ErrNotFound
// 哨兵，由 RPC 层（coreproto，M2）决定映射成什么对外码。已列入「需要裁决」清单。
type NotFoundError struct {
	Kind string // thread|entry|run|interaction|plugin_meta
	ID   string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("sqlite: %s 不存在：%s", e.Kind, e.ID)
}

// Is 让 errors.Is(err, ErrNotFound) 成立。
func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// ErrNotFound 是「找不到对象」的哨兵。
var ErrNotFound = errors.New("sqlite: 对象不存在")

// ---------------------------------------------------------------------------
// 行映射
// ---------------------------------------------------------------------------

const threadColumns = `id, workspace, title, head_entry_id, created_at, updated_at, archived`
const entryColumns = `id, thread_id, parent_id, seq, kind, role, payload, tokens, created_at`

func scanThread(scan func(dest ...any) error) (ports.Thread, error) {
	var (
		t                    ports.Thread
		head                 sql.NullString
		archived             int
		createdAt, updatedAt int64
	)
	err := scan(&t.ID, &t.Workspace, &t.Title, &head, &createdAt, &updatedAt, &archived)
	if err != nil {
		return ports.Thread{}, err
	}
	t.HeadEntryID = head.String
	t.Archived = archived != 0
	t.CreatedAt = fromMS(createdAt)
	t.UpdatedAt = fromMS(updatedAt)
	return t, nil
}

func scanEntry(scan func(dest ...any) error) (ports.Entry, error) {
	var (
		e              ports.Entry
		parent, role   sql.NullString
		payload        []byte
		kind           string
		createdAt, seq int64
	)
	err := scan(&e.ID, &e.ThreadID, &parent, &seq, &kind, &role, &payload, &e.Tokens, &createdAt)
	if err != nil {
		return ports.Entry{}, err
	}
	e.ParentID = parent.String
	e.Seq = seq
	e.Kind = domain.EntryKind(kind)
	e.Role = domain.Role(role.String)
	e.Payload = json.RawMessage(payload)
	e.CreatedAt = fromMS(createdAt)
	return e, nil
}

// ---------------------------------------------------------------------------
// 读侧：线程
// ---------------------------------------------------------------------------

// GetThread 按 id 取线程；找不到返回 *NotFoundError（errors.Is(err, ErrNotFound) 成立）。
func (r *Reader) GetThread(ctx context.Context, threadID string) (ports.Thread, error) {
	if threadID == "" {
		return ports.Thread{}, invalidInput("GetThread 需要 threadID")
	}
	row := r.read.QueryRowContext(ctx,
		`SELECT `+threadColumns+` FROM threads WHERE id = ?`, threadID)
	t, err := scanThread(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Thread{}, &NotFoundError{Kind: "thread", ID: threadID}
	}
	if err != nil {
		return ports.Thread{}, fmt.Errorf("sqlite: 读取线程失败: %w", err)
	}
	return t, nil
}

// threadCursor 是游标内容：按 (updated_at DESC, id DESC) 稳定分页。
type threadCursor struct {
	UpdatedAt int64
	ID        string
}

func encodeThreadCursor(c threadCursor) string {
	raw := fmt.Sprintf("%d|%s", c.UpdatedAt, c.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeThreadCursor(s string) (threadCursor, error) {
	if s == "" {
		return threadCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return threadCursor{}, invalidInput("线程游标无法解码（必须是本实现颁发的不透明游标）")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return threadCursor{}, invalidInput("线程游标形状非法")
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return threadCursor{}, invalidInput("线程游标里的时间戳非法")
	}
	return threadCursor{UpdatedAt: n, ID: parts[1]}, nil
}

// ListThreads 游标分页列线程（08 thread.list：分页 + 归档过滤，01 §3.1 按当前工作区）。
//
// 排序固定 updated_at DESC, id DESC（id 作 tiebreaker，避免同一毫秒抖动导致漏项）。
// Workspace 为空时列本数据目录的全部线程；非空时必须与本工作区身份键一致。
func (r *Reader) ListThreads(ctx context.Context, req ports.ListThreadsRequest) (ports.ThreadPage, error) {
	if req.Workspace != "" {
		h, err := WorkspaceHash(req.Workspace)
		if err != nil {
			return ports.ThreadPage{}, err
		}
		if h != r.hash {
			return ports.ThreadPage{}, invalidInput(fmt.Sprintf(
				"ListThreads 请求的工作区 %q 不属于本数据目录（%s ≠ %s）", req.Workspace, r.hash, h))
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultThreadListLimit
	}
	if limit > MaxThreadListLimit {
		limit = MaxThreadListLimit
	}

	q := `SELECT ` + threadColumns + ` FROM threads WHERE 1=1`
	args := []any{}
	if !req.IncludeArchived {
		q += ` AND archived = 0`
	}
	if cur, err := decodeThreadCursor(req.Cursor); err != nil {
		return ports.ThreadPage{}, err
	} else if cur.ID != "" {
		q += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, cur.UpdatedAt, cur.UpdatedAt, cur.ID)
	}
	q += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1) // 多取一条判断是否还有下一页

	rows, err := r.read.QueryContext(ctx, q, args...)
	if err != nil {
		return ports.ThreadPage{}, fmt.Errorf("sqlite: 列线程失败: %w", err)
	}
	defer rows.Close()

	var items []ports.Thread
	for rows.Next() {
		t, err := scanThread(rows.Scan)
		if err != nil {
			return ports.ThreadPage{}, fmt.Errorf("sqlite: 读取线程行失败: %w", err)
		}
		items = append(items, t)
		if len(items) > limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return ports.ThreadPage{}, fmt.Errorf("sqlite: 遍历线程失败: %w", err)
	}

	page := ports.ThreadPage{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		page.Items = items[:limit]
		page.NextCursor = encodeThreadCursor(threadCursor{UpdatedAt: ms(last.UpdatedAt), ID: last.ID})
	}
	if page.Items == nil {
		page.Items = []ports.Thread{}
	}
	return page, nil
}

// ---------------------------------------------------------------------------
// 读侧：entry 与上下文重建
// ---------------------------------------------------------------------------

// EntriesBefore 从 fromSeq（含）向前取 limit 条，返回按 Seq 升序的窗口与可继续回翻的游标
// （08 thread.entries）。
//
// 注意语义缺口：seq 是「线程内」单调序号，但线程是 DAG，兄弟分支的 seq 会交错。
// 端口签名只给了 fromSeq，所以这里按 seq 线性回翻（覆盖线性主线的常规用法）；
// 需要严格按某条 head 路径回翻时用 EntriesOnPath（本包扩展，04 §5.7 的 DAG 语义更准）。
func (r *Reader) EntriesBefore(ctx context.Context, threadID string, fromSeq, limit int64) (ports.EntriesPage, error) {
	return r.entriesBefore(ctx, threadID, fromSeq, limit)
}

func (r *Reader) entriesBefore(ctx context.Context, threadID string, fromSeq, limit int64) (ports.EntriesPage, error) {
	if threadID == "" {
		return ports.EntriesPage{}, invalidInput("EntriesBefore 需要 threadID")
	}
	if limit <= 0 {
		limit = 100
	}
	if fromSeq <= 0 {
		// fromSeq<=0 视作「从当前最大 seq 起」（首页：调用方还不知道 head 的 seq）。
		var maxSeq sql.NullInt64
		if err := r.read.QueryRowContext(ctx,
			`SELECT MAX(seq) FROM entries WHERE thread_id = ?`, threadID).Scan(&maxSeq); err != nil {
			return ports.EntriesPage{}, fmt.Errorf("sqlite: 读取最大 seq 失败: %w", err)
		}
		if !maxSeq.Valid {
			return ports.EntriesPage{Items: []ports.Entry{}}, nil
		}
		fromSeq = maxSeq.Int64
	}

	rows, err := r.read.QueryContext(ctx, `
SELECT `+entryColumns+` FROM entries
WHERE thread_id = ? AND seq <= ?
ORDER BY seq DESC LIMIT ?`, threadID, fromSeq, limit)
	if err != nil {
		return ports.EntriesPage{}, fmt.Errorf("sqlite: 分页读 entry 失败: %w", err)
	}
	defer rows.Close()

	var out []ports.Entry
	for rows.Next() {
		e, err := scanEntry(rows.Scan)
		if err != nil {
			return ports.EntriesPage{}, fmt.Errorf("sqlite: 读取 entry 行失败: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return ports.EntriesPage{}, fmt.Errorf("sqlite: 遍历 entry 失败: %w", err)
	}
	// 回翻用 seq 升序返回（前端与模型上下文都按时间正向消费）。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	page := ports.EntriesPage{Items: out}
	if len(out) == int(limit) && len(out) > 0 {
		next := out[0].Seq - 1
		page.NextCursor = encodeSeqCursor(next)
		if next <= 0 {
			page.NextCursor = "" // 已经到根，没有更早的了。
		}
	}
	if page.Items == nil {
		page.Items = []ports.Entry{}
	}
	return page, nil
}

// EntriesOnPath 是 EntriesBefore 的 DAG 正确版：只回翻 head→root 这条路径上的 entry。
// 分支存在时（04 §5.7「编辑并重发」）它才不会出现「把兄弟分支混进历史」的问题。
func (r *Reader) EntriesOnPath(ctx context.Context, threadID, entryID string, limit int64) (ports.EntriesPage, error) {
	path, err := r.pathFrom(ctx, threadID, entryID)
	if err != nil {
		return ports.EntriesPage{}, err
	}
	if limit <= 0 || limit > int64(len(path)) {
		limit = int64(len(path))
	}
	window := path[len(path)-int(limit):]
	return ports.EntriesPage{Items: window}, nil
}

func encodeSeqCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("seq:" + strconv.FormatInt(seq, 10)))
}

// DecodeSeqCursor 解开 entries 游标（coreproto/CLI 拿到 NextCursor 后要换算成 fromSeq）。
// 端口签名 EntriesBefore 收 int64，所以这里把不透明游标翻译回去。
func DecodeSeqCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, invalidInput("entry 游标无法解码")
	}
	s := string(raw)
	if v, ok := strings.CutPrefix(s, "seq:"); ok {
		return strconv.ParseInt(v, 10, 64)
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil { // 容错：允许直接给十进制
		return n, nil
	}
	return 0, invalidInput("entry 游标形状非法")
}

// pathFrom 沿 parent_id 回溯 head→root，返回按 Seq 升序的路径（04 §5.7 的 DAG）。
//
// 用递归 CTE 一次取回，避免 N 次往返；MaxPathDepth 是防御式上限。
func (r *Reader) pathFrom(ctx context.Context, threadID, headEntryID string) ([]ports.Entry, error) {
	if headEntryID == "" {
		return []ports.Entry{}, nil
	}
	rows, err := r.read.QueryContext(ctx, `
WITH RECURSIVE chain(`+entryColumns+`, depth) AS (
  SELECT `+entryColumns+`, 1 FROM entries
  WHERE thread_id = ? AND id = ?
  UNION ALL
  SELECT e.id, e.thread_id, e.parent_id, e.seq, e.kind, e.role, e.payload, e.tokens, e.created_at, c.depth + 1
  FROM entries e JOIN chain c ON e.id = c.parent_id
  WHERE e.thread_id = ? AND c.depth < ?
)
SELECT `+entryColumns+` FROM chain ORDER BY seq ASC`,
		threadID, headEntryID, threadID, MaxPathDepth)
	if err != nil {
		return nil, fmt.Errorf("sqlite: 回溯 entry 路径失败: %w", err)
	}
	defer rows.Close()

	var out []ports.Entry
	for rows.Next() {
		e, err := scanEntry(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite: 读取路径 entry 行失败: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: 遍历路径失败: %w", err)
	}
	return out, nil
}

// TraceFromHead 从 head 回溯到最近 compaction，重建模型上下文（04 §5.7 恢复会话、02 §5.3 步骤 3）。
//
// 规则（照文档原话「从 head 回溯到最近 compaction entry，放入摘要，再从
// first_kept_entry_id 重放」）：
//  1. 取 head→root 的路径（按 seq 升序）；
//  2. 找路径上最后一条成功的 compaction（payload.compactionFailed=true 的不算压缩点，
//     这是 04 §5.7「摘要失败不丢历史，只标记」的直接后果）；
//  3. Compaction = 该 entry；Entries = 从 first_kept_entry_id（含）起、到 head 为止的路径 entry，
//     即「摘要 + 保留段」，压缩点本身不进 Entries（它已经变成摘要）；
//  4. 没有压缩点 → Entries 是整条路径，Compaction 为 nil。
func (r *Reader) TraceFromHead(ctx context.Context, threadID string) (ports.ContextWindow, error) {
	t, err := r.GetThread(ctx, threadID)
	if err != nil {
		return ports.ContextWindow{}, err
	}
	path, err := r.pathFrom(ctx, threadID, t.HeadEntryID)
	if err != nil {
		return ports.ContextWindow{}, err
	}
	win := ports.ContextWindow{HeadEntryID: t.HeadEntryID, Entries: []ports.Entry{}}

	cut := -1
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Kind != domain.EntryCompaction {
			continue
		}
		var p CompactionPayload
		if err := json.Unmarshal(path[i].Payload, &p); err != nil {
			return ports.ContextWindow{}, storageCorrupt(fmt.Sprintf(
				"compaction entry %s 的 payload 不是合法 JSON：%v", path[i].ID, err), err)
		}
		if p.Failed {
			continue // 失败的压缩不改变重放范围。
		}
		cut = i
		c := path[i]
		win.Compaction = &c
		break
	}
	start := 0
	if cut >= 0 {
		start = cut + 1
		var p CompactionPayload
		_ = json.Unmarshal(win.Compaction.Payload, &p) // 已在上一步校验过
		if p.FirstKeptEntryID != "" {
			if idx := indexOfID(path, p.FirstKeptEntryID); idx >= 0 && idx < cut {
				start = idx // 从 first_kept_entry_id 重放（保留段仍在压缩点之前）
			}
		}
		// 压缩点自己不进 Entries（它已经变成 Compaction 摘要），但保留段 [start, cut) 要进。
		win.Entries = append(win.Entries, path[start:cut]...)
		win.Entries = append(win.Entries, path[cut+1:]...)
	} else {
		win.Entries = append(win.Entries, path[start:]...)
	}
	return win, nil
}

func indexOfID(path []ports.Entry, id string) int {
	for i, e := range path {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// HeadEntry 返回线程 head 对应的 entry（head 为空时返回 ErrNotFound）。
func (r *Reader) HeadEntry(ctx context.Context, threadID string) (ports.Entry, error) {
	t, err := r.GetThread(ctx, threadID)
	if err != nil {
		return ports.Entry{}, err
	}
	if t.HeadEntryID == "" {
		return ports.Entry{}, &NotFoundError{Kind: "entry", ID: "(head 为空)"}
	}
	return r.GetEntry(ctx, t.HeadEntryID)
}

// GetEntry 按 id 取 entry（跨线程；entry id 是 ULID，全局唯一）。
func (r *Reader) GetEntry(ctx context.Context, entryID string) (ports.Entry, error) {
	row := r.read.QueryRowContext(ctx,
		`SELECT `+entryColumns+` FROM entries WHERE id = ?`, entryID)
	e, err := scanEntry(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Entry{}, &NotFoundError{Kind: "entry", ID: entryID}
	}
	if err != nil {
		return ports.Entry{}, fmt.Errorf("sqlite: 读取 entry 失败: %w", err)
	}
	return e, nil
}

// ThreadStats 是线程的粗粒度统计（doctor/debug 用）。
type ThreadStats struct {
	ThreadID  string
	Entries   int64
	MaxSeq    int64
	HeadEmpty bool
	UpdatedAt time.Time
}

// Stats 返回线程统计（只读）。
func (r *Reader) Stats(ctx context.Context, threadID string) (ThreadStats, error) {
	var s ThreadStats
	s.ThreadID = threadID
	var maxSeq sql.NullInt64
	err := r.read.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(seq) FROM entries WHERE thread_id = ?`, threadID).Scan(&s.Entries, &maxSeq)
	if err != nil {
		return s, fmt.Errorf("sqlite: 统计线程失败: %w", err)
	}
	s.MaxSeq = maxSeq.Int64
	var head sql.NullString
	var updatedAt int64
	err = r.read.QueryRowContext(ctx, `SELECT head_entry_id, updated_at FROM threads WHERE id = ?`, threadID).
		Scan(&head, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, &NotFoundError{Kind: "thread", ID: threadID}
	}
	if err != nil {
		return s, fmt.Errorf("sqlite: 统计线程失败: %w", err)
	}
	s.HeadEmpty = head.String == ""
	s.UpdatedAt = fromMS(updatedAt)
	return s, nil
}
