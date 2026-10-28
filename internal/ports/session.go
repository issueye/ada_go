package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// Thread 是线程投影，字段照 04 §5.7 的 threads 表。
// ID 形如 th_<workspaceHash>_<ulid>（01 §3.1）。HeadEntryID 是 DAG 的唯一指针：
// 分支切换只改它，不改写历史（04 §5.7）。
type Thread struct {
	ID          string    `json:"id"`
	Workspace   string    `json:"workspace"`
	Title       string    `json:"title"`
	HeadEntryID string    `json:"headEntryId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Archived    bool      `json:"archived"`
}

// Entry 是一条 append-only 会话 entry，字段照 04 §5.7 的 entries 表。
// entry 一旦写入不可修改（04 §5.7）；ParentID 为空表示根 entry；
// Seq 是线程内单调序号（unique index entries_seq）。
type Entry struct {
	ID        string           `json:"id"`
	ThreadID  string           `json:"threadId"`
	ParentID  string           `json:"parentId,omitempty"`
	Seq       int64            `json:"seq"`
	Kind      domain.EntryKind `json:"kind"`
	Role      domain.Role      `json:"role,omitempty"`
	Payload   json.RawMessage  `json:"payload"`
	Tokens    int64            `json:"tokens"`
	CreatedAt time.Time        `json:"createdAt"`
}

// ListThreadsRequest 是线程列表分页请求（08 thread.list：分页 + 归档过滤；
// 列表按当前工作区展示，01 §3.1）。
type ListThreadsRequest struct {
	Workspace       string
	Limit           int
	Cursor          string // 不透明游标；首页为空串，之后用返回的 NextCursor。
	IncludeArchived bool
}

// ThreadPage 是游标分页结果；NextCursor 为空表示没有更多。
type ThreadPage struct {
	Items      []Thread
	NextCursor string
}

// EntriesPage 是历史分页结果（08 thread.entries：游标 + 反向窗口）。
type EntriesPage struct {
	Items      []Entry
	NextCursor string
}

// ContextWindow 是从 head 回溯重建出的模型上下文（02 §5.3 步骤 3）：
// 从 head 回溯到最近 compaction entry，放入摘要，再从 first_kept_entry_id 重放（04 §5.7）。
type ContextWindow struct {
	// Compaction 是最近一条压缩 entry（可为 nil：从未压缩）。
	Compaction *Entry
	// Entries 是自 compaction（或根）起的 entry，按 Seq 升序。
	Entries []Entry
	// HeadEntryID 是回溯起点。
	HeadEntryID string
}

// SessionStore 是线程、entry、游标、分支端口（01 §5.2、04 §5.7）。
// 一切修改经 Core 唯一写协程串行提交；拿不到工作区 OS 独占锁时返回
// workspace_locked（04 §5.7，domain.CodeWorkspaceLocked）。Gateway 不打开 state.db。
//
// 生产实现：internal/adapters/sqlite（M1-B）。
// 测试替身：internal/testsupport 的内存 store（保持 append-only 与 seq 语义，INV-2）。
type SessionStore interface {
	// CreateThread 新建线程；id 由 IDGen 生成（th_<workspaceHash>_<ulid>）。
	CreateThread(ctx context.Context, workspace, title string) (Thread, error)
	// GetThread 按 id 取线程。
	GetThread(ctx context.Context, threadID string) (Thread, error)
	// ListThreads 游标分页列线程。
	ListThreads(ctx context.Context, req ListThreadsRequest) (ThreadPage, error)
	// RenameThread 重命名（08 thread.rename）。
	RenameThread(ctx context.Context, threadID, title string) error
	// SetArchived 归档/恢复（08 thread.archive）。
	SetArchived(ctx context.Context, threadID string, archived bool) error
	// DeleteThread 软删 + 交给清理任务（08 thread.delete、04：archived/清理语义；
	// 不做物理删除，保证审计与回放）。
	DeleteThread(ctx context.Context, threadID string) error

	// AppendEntry 追加 entry：append-only、写入后不可修改（04 §5.7）。
	// Seq 由实现方在单写者协程内分配；ParentID 指定 DAG 父节点。
	// 返回落库后的 entry（含分配的 Seq 与 ID）。
	AppendEntry(ctx context.Context, e Entry) (Entry, error)
	// EntriesBefore 从 fromSeq（含）向前取 limit 条（08 thread.entries 历史分页），
	// 返回按 Seq 升序的窗口与可继续回翻的 NextCursor（空表示已到根）。
	EntriesBefore(ctx context.Context, threadID string, fromSeq, limit int64) (EntriesPage, error)
	// TraceFromHead 从 head 回溯到最近 compaction，重建模型上下文（04 §5.7 恢复会话）。
	TraceFromHead(ctx context.Context, threadID string) (ContextWindow, error)
	// AppendCompaction 追加压缩 entry（04 §5.7：压缩也是一条 entry，
	// payload 含摘要与 first_kept_entry_id）；失败路径标记 compaction_failed 但不丢历史。
	AppendCompaction(ctx context.Context, threadID, parentID string, summary json.RawMessage, firstKeptEntryID string) (Entry, error)
	// MoveHead 移动 head 指针实现分支：不改写历史（04 §5.7“编辑并重发”；
	// 旧分支保留，审计与回放不受影响）。
	MoveHead(ctx context.Context, threadID, entryID string) error
}
