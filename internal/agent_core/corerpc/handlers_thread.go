package corerpc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// ---- thread ----

func (s *Server) handleThreadCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Title string `json:"title"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.create: " + err.Error())
	}
	th, err := s.sess.CreateThread(ctx, p.Title)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Limit    int    `json:"limit"`
		Cursor   string `json:"cursor"`
		Archived bool   `json:"archived"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.list: " + err.Error())
	}
	page, err := s.sess.ListThreads(ctx, ports.ListThreadsRequest{
		Workspace: s.sess.Workspace(), Limit: p.Limit, Cursor: p.Cursor,
		IncludeArchived: true,
	})
	if err != nil {
		return nil, rpcError(err)
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, th := range page.Items {
		if th.Archived != p.Archived {
			continue
		}
		item := threadJSON(th)
		item["running"] = s.threadRunning(th.ID)
		items = append(items, item)
	}
	return map[string]any{"threads": items, "nextCursor": page.NextCursor, "hasMore": page.NextCursor != ""}, nil
}

func (s *Server) handleThreadGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID  string `json:"threadId"`
		ItemLimit int    `json:"itemLimit"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("thread.get: 需要 threadId")
	}
	limit := p.ItemLimit
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, invalid("thread.get: itemLimit 必须在 1..500")
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	// 只读当前 head 路径：分支（编辑并重发）之后，被舍弃的兄弟分支不能再出现在
	// 界面与模型上下文里（04 §5.7）。seq 仍用于分页游标。
	page, err := s.sess.Store().EntriesOnPath(ctx, p.ThreadID, th.HeadEntryID, 0, int64(limit))
	if err != nil {
		return nil, rpcError(err)
	}
	pending := s.pending(ctx, p.ThreadID)
	items, hasMore := s.items(ctx, page.Items, pending, limit, page.NextCursor != "")
	out := map[string]any{
		"thread":       threadJSON(th),
		"items":        items,
		"seq":          s.sess.LastSeq(p.ThreadID),
		"run":          s.runState(p.ThreadID),
		"interactions": interactionsJSON(pending),
		"hasMore":      hasMore,
	}
	if stats, err := s.contextStats(ctx, p.ThreadID); err == nil {
		out["context"] = stats
	}
	return out, nil
}

// contextStats 估算当前上下文占用，并拆成三段：系统提示词、工具声明、对话消息。
//
// 消息窗口从 head 回溯到最近压缩点累加 entry.Tokens；系统提示与工具 Schema 不在
// entry 里，由会话按引擎同一套装配/估算给出（engine.SystemPromptTokens 等）。
// 三段合计才是这次请求真正要发送的内容，环上的占比也按合计算 —— 与压缩水位同源。
// 仍是估算值：界面用「约」展示，不当作精确计费数据。
func (s *Server) contextStats(ctx context.Context, threadID string) (map[string]any, error) {
	window, err := s.sess.Store().TraceFromHead(ctx, threadID)
	if err != nil {
		return nil, err
	}
	messages := 0
	if window.Compaction != nil {
		messages += int(window.Compaction.Tokens)
	}
	for _, e := range window.Entries {
		messages += int(e.Tokens)
	}
	if messages < 0 {
		messages = 0
	}
	systemTokens, toolTokens := s.sess.ContextBreakdown(ctx, threadID)
	tokens := messages + systemTokens + toolTokens
	ref := s.sess.ModelRefFor(threadID)
	limit := int(ref.ContextWindow)
	percent := 0
	if limit > 0 {
		percent = (tokens*100 + limit/2) / limit
		if percent > 100 {
			percent = 100
		}
	}
	return map[string]any{
		"tokens": tokens, "messages": messages, "system": systemTokens, "tools": toolTokens,
		"window": limit, "percent": percent,
	}, nil
}

func (s *Server) handleThreadEntries(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Cursor   string `json:"cursor"`
		Limit    int64  `json:"limit"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("thread.entries: 需要 threadId")
	}
	fromSeq, err := decodeSeqCursor(p.Cursor)
	if err != nil {
		return nil, invalid("thread.entries: " + err.Error())
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	// 历史分页同样只走 head 路径；游标是路径上更早一条 entry 的 seq。
	limit := p.Limit
	if limit <= 0 {
		limit = 100 // 与 EntriesBefore 的缺省窗口保持一致
	}
	page, err := s.sess.Store().EntriesOnPath(ctx, p.ThreadID, th.HeadEntryID, fromSeq, limit)
	if err != nil {
		return nil, rpcError(err)
	}
	out := make([]map[string]any, 0, len(page.Items))
	for _, e := range page.Items {
		out = append(out, entryJSON(e))
	}
	return map[string]any{"entries": out, "nextCursor": page.NextCursor, "hasMore": page.NextCursor != ""}, nil
}

func (s *Server) handleThreadRename(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Title    string `json:"title"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.rename: " + err.Error())
	}
	if p.ThreadID == "" {
		return nil, invalid("thread.rename: 需要 threadId")
	}
	if strings.TrimSpace(p.Title) == "" {
		return nil, invalid("thread.rename: title 不能为空")
	}
	if len([]rune(p.Title)) > 200 {
		return nil, invalid("thread.rename: title 不能超过 200 字符")
	}
	if err := s.sess.Store().RenameThread(ctx, p.ThreadID, p.Title); err != nil {
		return nil, rpcError(err)
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadArchive(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		Archived *bool  `json:"archived"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.archive: " + err.Error())
	}
	if p.ThreadID == "" || p.Archived == nil {
		return nil, invalid("thread.archive: 需要 threadId 与 archived")
	}
	if err := s.sess.Store().SetArchived(ctx, p.ThreadID, *p.Archived); err != nil {
		return nil, rpcError(err)
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadBranch(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
		EntryID  string `json:"entryId"`
		Title    string `json:"title"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, invalid("thread.branch: " + err.Error())
	}
	if p.ThreadID == "" {
		return nil, invalid("thread.branch: 需要 threadId")
	}
	if len([]rune(p.Title)) > 200 {
		return nil, invalid("thread.branch: title 不能超过 200 字符")
	}
	if _, err := s.sess.GetThread(ctx, p.ThreadID); err != nil {
		return nil, rpcError(err)
	}
	// 运行中移动 head 会让正在落库的轮次读到另一条路径，直接拒绝。
	s.mu.Lock()
	busy := s.runs[p.ThreadID] != nil && s.runs[p.ThreadID].status == "running"
	s.mu.Unlock()
	if busy {
		return nil, rpcError(domain.NewError(domain.CodeThreadBusy, "线程已有运行中轮次"))
	}
	// entryId 为空表示清空 head（从根分支）：编辑重发首条消息时需要。
	// MoveHead 的端口语义已支持空串（04 §5.7：只改指针，不改写历史）。
	if p.EntryID != "" {
		entry, err := s.sess.Store().GetEntry(ctx, p.EntryID)
		if err != nil {
			return nil, rpcError(err)
		}
		if entry.ThreadID != p.ThreadID {
			return nil, invalid("thread.branch: entry 不属于该线程")
		}
	}
	if p.Title != "" {
		if err := s.sess.Store().RenameThread(ctx, p.ThreadID, p.Title); err != nil {
			return nil, rpcError(err)
		}
	}
	if err := s.sess.Store().MoveHead(ctx, p.ThreadID, p.EntryID); err != nil {
		return nil, rpcError(err)
	}
	th, err := s.sess.GetThread(ctx, p.ThreadID)
	if err != nil {
		return nil, rpcError(err)
	}
	return map[string]any{"thread": threadJSON(th)}, nil
}

func (s *Server) handleThreadDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if err := decode(raw, &p); err != nil || p.ThreadID == "" {
		return nil, invalid("thread.delete: 需要 threadId")
	}
	if err := s.sess.Store().DeleteThread(ctx, p.ThreadID); err != nil {
		return nil, rpcError(err)
	}
	s.sess.ForgetThreadModel(p.ThreadID)
	return map[string]any{"deleted": true, "cleanupScheduled": true}, nil
}
