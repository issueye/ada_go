package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// MemoryStore 是 ports.SessionStore 的测试替身，保持 append-only 与 seq 语义（INV-2）。
//
// 它不是 SQLite 的简化版实现，而是「语义等价的最小模型」：
//   - entry 写入后不可修改；分支只移动 head 指针（04 §5.7）。
//   - Seq 在线程内单调且唯一（对应 unique index entries_seq）。
//   - TraceFromHead 从 head 沿 parent 回溯到最近 compaction，再从 firstKeptEntryId 重放。
//
// 生产实现是 internal/adapters/sqlite（M1-B）；两者必须通过同一组断言
// （见 internal/adapters/sqlite 的分支/回溯测试与本包的 TestMemoryStoreTrace）。
type MemoryStore struct {
	mu      sync.Mutex
	threads map[string]*threadState
	clock   ports.Clock
	ids     ports.IDGen
	// deleted 记软删（DeleteThread：软删 + 清理任务，08 thread.delete）。
	deleted map[string]bool
}

type threadState struct {
	thread  ports.Thread
	entries []ports.Entry
	seq     int64
}

var _ ports.SessionStore = (*MemoryStore)(nil)

// NewMemoryStore 创建内存会话库。clock/ids 为 nil 时用系统时钟与自增 id。
func NewMemoryStore(clock ports.Clock, ids ports.IDGen) *MemoryStore {
	if clock == nil {
		clock = NewFakeClockAt(time.Now().UTC())
	}
	if ids == nil {
		ids = NewFixedIDGen()
	}
	return &MemoryStore{
		threads: map[string]*threadState{},
		deleted: map[string]bool{},
		clock:   clock, ids: ids,
	}
}

// CreateThread 实现端口。
func (m *MemoryStore) CreateThread(ctx context.Context, workspace, title string) (ports.Thread, error) {
	if strings.TrimSpace(workspace) == "" {
		return ports.Thread{}, domain.NewError(domain.CodeWorkspaceMissing, "CreateThread 需要 workspace")
	}
	id, err := m.ids.NewThreadID(ctx, workspaceHash(workspace))
	if err != nil {
		return ports.Thread{}, err
	}
	now := m.clock.Now()
	th := ports.Thread{ID: id, Workspace: workspace, Title: title, CreatedAt: now, UpdatedAt: now}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.threads[id]; ok {
		return ports.Thread{}, fmt.Errorf("testkit: 线程 id 重复 %s", id)
	}
	m.threads[id] = &threadState{thread: th}
	return th, nil
}

// MustCreateThread 是给测试用的快速版（忽略错误直接 fatal 由调用方负责）。
func (m *MemoryStore) MustCreateThread(workspace, title string) (ports.Thread, error) {
	return m.CreateThread(context.Background(), workspace, title)
}

// GetThread 实现端口。
func (m *MemoryStore) GetThread(_ context.Context, threadID string) (ports.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[threadID]
	if !ok || m.deleted[threadID] {
		return ports.Thread{}, domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	return st.thread, nil
}

// RenameThread 实现端口。
func (m *MemoryStore) RenameThread(_ context.Context, threadID, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[threadID]
	if !ok {
		return domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	st.thread.Title = title
	st.thread.UpdatedAt = m.clock.Now()
	return nil
}

// SetArchived 实现端口。
func (m *MemoryStore) SetArchived(_ context.Context, threadID string, archived bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[threadID]
	if !ok {
		return domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	st.thread.Archived = archived
	st.thread.UpdatedAt = m.clock.Now()
	return nil
}

// DeleteThread 实现端口：软删，历史保留（审计与回放要求）。
func (m *MemoryStore) DeleteThread(_ context.Context, threadID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.threads[threadID]; !ok {
		return domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	m.deleted[threadID] = true
	return nil
}

// ListThreads 实现端口：按工作区过滤 + 游标分页。
func (m *MemoryStore) ListThreads(_ context.Context, req ports.ListThreadsRequest) (ports.ThreadPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.threads))
	for id := range m.threads {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	var out ports.ThreadPage
	started := req.Cursor == ""
	for _, id := range ids {
		st := m.threads[id]
		if !started {
			if id != req.Cursor {
				continue
			}
			started = true
			continue // 游标指向「上一页最后一条」，本身不再返回。
		}
		if st.thread.Workspace != req.Workspace || m.deleted[id] {
			continue
		}
		if st.thread.Archived && !req.IncludeArchived {
			continue
		}
		if len(out.Items) >= limit {
			out.NextCursor = out.Items[len(out.Items)-1].ID
			return out, nil
		}
		out.Items = append(out.Items, st.thread)
	}
	return out, nil
}

// AppendEntry 实现端口：assign seq + parent，写后不可修改。
func (m *MemoryStore) AppendEntry(_ context.Context, e ports.Entry) (ports.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[e.ThreadID]
	if !ok {
		return ports.Entry{}, domain.NewError(domain.CodeThreadBusy, "线程不存在: "+e.ThreadID)
	}
	if e.ID == "" {
		id, err := m.ids.NewULID(context.Background())
		if err != nil {
			return ports.Entry{}, err
		}
		e.ID = id
	}
	for _, have := range st.entries {
		if have.ID == e.ID {
			return ports.Entry{}, fmt.Errorf("testkit: entry id 重复 %s", e.ID)
		}
	}
	if e.ParentID == "" {
		e.ParentID = st.thread.HeadEntryID
	}
	st.seq++
	e.Seq = st.seq
	if e.CreatedAt.IsZero() {
		e.CreatedAt = m.clock.Now()
	}
	if e.Kind == "" {
		e.Kind = domain.EntryMessage
	}
	st.entries = append(st.entries, e)
	st.thread.HeadEntryID = e.ID
	st.thread.UpdatedAt = e.CreatedAt
	return e, nil
}

// EntriesBefore 实现端口：fromSeq 之前的反向窗口（08 thread.entries）。
func (m *MemoryStore) EntriesBefore(_ context.Context, threadID string, fromSeq, limit int64) (ports.EntriesPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[threadID]
	if !ok {
		return ports.EntriesPage{}, domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	if fromSeq <= 0 {
		fromSeq = st.seq + 1
	}
	if limit <= 0 {
		limit = 50
	}
	var page ports.EntriesPage
	for i := len(st.entries) - 1; i >= 0 && int64(len(page.Items)) < limit; i-- {
		e := st.entries[i]
		if e.Seq >= fromSeq {
			continue
		}
		page.Items = append(page.Items, e)
	}
	if len(page.Items) > 0 && int64(len(page.Items)) == limit {
		page.NextCursor = fmt.Sprintf("%d", page.Items[len(page.Items)-1].Seq)
	}
	return page, nil
}

// TraceFromHead 实现端口：head → 根回溯，遇最近 compaction 停下并按 firstKeptEntryId 重放。
func (m *MemoryStore) TraceFromHead(_ context.Context, threadID string) (ports.ContextWindow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[threadID]
	if !ok {
		return ports.ContextWindow{}, domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	byID := make(map[string]ports.Entry, len(st.entries))
	for _, e := range st.entries {
		byID[e.ID] = e
	}
	chain := []ports.Entry{}
	cur := st.thread.HeadEntryID
	for cur != "" {
		e, ok := byID[cur]
		if !ok {
			break
		}
		chain = append(chain, e)
		cur = e.ParentID
		if len(chain) > len(st.entries) {
			return ports.ContextWindow{}, fmt.Errorf("testkit: entry 链出现环")
		}
	}
	// chain 是从 head 到根；翻成 seq 升序。
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	win := ports.ContextWindow{HeadEntryID: st.thread.HeadEntryID}
	start := 0
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].Kind != domain.EntryCompaction {
			continue
		}
		cp := chain[i]
		win.Compaction = &cp
		kept := firstKeptOf(cp.Payload)
		for j := i + 1; j < len(chain); j++ {
			if kept == "" || chain[j].ID == kept {
				start = j
				break
			}
			start = j
		}
		if kept != "" {
			for j := i + 1; j < len(chain); j++ {
				if chain[j].ID == kept {
					start = j
					break
				}
			}
		}
		break
	}
	win.Entries = append(win.Entries, chain[start:]...)
	return win, nil
}

// AppendCompaction 实现端口：追加一条 compaction entry（摘要 + firstKeptEntryId）。
func (m *MemoryStore) AppendCompaction(ctx context.Context, threadID, parentID string, summary json.RawMessage, firstKeptEntryID string) (ports.Entry, error) {
	payload, err := withFirstKept(summary, firstKeptEntryID)
	if err != nil {
		return ports.Entry{}, err
	}
	return m.AppendEntry(ctx, ports.Entry{
		ThreadID: threadID, ParentID: parentID,
		Kind: domain.EntryCompaction, Payload: payload,
	})
}

// MoveHead 实现端口：分支切换只移动指针，不改写历史（04 §5.7）。
func (m *MemoryStore) MoveHead(_ context.Context, threadID, entryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.threads[threadID]
	if !ok {
		return domain.NewError(domain.CodeThreadBusy, "线程不存在: "+threadID)
	}
	if entryID != "" {
		found := false
		for _, e := range st.entries {
			if e.ID == entryID {
				found = true
				break
			}
		}
		if !found {
			return domain.NewError(domain.CodeThreadBusy, "entry 不在线程链上: "+entryID)
		}
	}
	st.thread.HeadEntryID = entryID
	st.thread.UpdatedAt = m.clock.Now()
	return nil
}

// Entries 返回某线程的全部 entry（测试断言用）。
func (m *MemoryStore) Entries(threadID string) []ports.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.threads[threadID]
	if st == nil {
		return nil
	}
	return append([]ports.Entry(nil), st.entries...)
}

// ThreadCount 返回未删除线程数。
func (m *MemoryStore) ThreadCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id := range m.threads {
		if !m.deleted[id] {
			n++
		}
	}
	return n
}

func firstKeptOf(payload json.RawMessage) string {
	var v struct {
		FirstKeptEntryID string `json:"firstKeptEntryId"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return ""
	}
	return v.FirstKeptEntryID
}

func withFirstKept(summary json.RawMessage, firstKept string) (json.RawMessage, error) {
	var obj map[string]any
	if len(summary) == 0 {
		obj = map[string]any{}
	} else if err := json.Unmarshal(summary, &obj); err != nil {
		return nil, fmt.Errorf("testkit: 压缩摘要必须是 JSON 对象: %w", err)
	}
	obj["firstKeptEntryId"] = firstKept
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// workspaceHash 给出稳定短哈希（线程 id 前缀 th_<hash>，01 §4.2 用 12 位）。
func workspaceHash(p string) string {
	sum := fmt.Sprintf("%016x", fnvHash(p))
	return sum[:12]
}

func fnvHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
