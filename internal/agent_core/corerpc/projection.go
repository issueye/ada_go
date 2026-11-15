package corerpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/adag/ada-go/internal/agent_core/coreapp"
	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// ---- helpers ----

// projectedItem 是 item 投影的中间形状；payload 保持 entry 原文。
type projectedItem struct {
	key         string
	typ         string
	at          time.Time
	payload     json.RawMessage
	fingerprint string
}

// items 把最近一批 entry 与待处理交互投影成 Item（按 at 升序，最多 limit 条）。
// hasMore 为 true 表示窗口外仍有更早 entry（或交互被窗口截掉）。
func (s *Server) items(_ context.Context, entries []ports.Entry, pending []ports.InteractionRequest, limit int, entriesHasMore bool) ([]map[string]any, bool) {
	all := make([]projectedItem, 0, len(entries)+len(pending))
	for _, e := range entries {
		if it, ok := projectEntry(e); ok {
			all = append(all, it)
		}
	}
	for _, r := range pending {
		if it, ok := projectInteraction(r); ok {
			all = append(all, it)
		}
	}
	total := len(all)
	sort.SliceStable(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	if len(all) > limit {
		all = all[total-limit:]
	}
	out := make([]map[string]any, 0, len(all))
	for _, it := range all {
		out = append(out, map[string]any{
			"key": it.key, "type": it.typ,
			"at":          it.at.UTC().Format(time.RFC3339Nano),
			"fingerprint": it.fingerprint,
			"payload":     it.payload,
		})
	}
	return out, entriesHasMore || total > limit
}

func projectEntry(e ports.Entry) (projectedItem, bool) {
	payload := e.Payload
	if len(payload) == 0 || !json.Valid(payload) {
		payload = json.RawMessage("{}")
	}
	var typ, key string
	switch e.Kind {
	case domain.EntryMessage:
		if e.Role == domain.RoleTool {
			var body struct {
				CallID string `json:"callId"`
			}
			_ = json.Unmarshal(payload, &body)
			key = "tool:" + body.CallID
			if body.CallID == "" {
				key = "tool:" + e.ID
			}
			typ = "tool"
		} else {
			key = "msg:" + e.ID
			typ = "message"
		}
	case domain.EntryCompaction:
		key = "compaction:" + e.ID
		typ = "compaction"
	default:
		return projectedItem{}, false
	}
	return projectedItem{key: key, typ: typ, at: e.CreatedAt, payload: payload, fingerprint: fingerprintPayload(payload)}, true
}

func projectInteraction(r ports.InteractionRequest) (projectedItem, bool) {
	m := map[string]any{
		"id": r.ID, "threadId": r.ThreadID, "runId": r.RunID,
		"pluginId": r.PluginID, "kind": string(r.Kind), "title": r.Title,
		"body": r.Body, "status": string(domain.InteractionPending),
		"createdAt": r.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if r.CallID != "" {
		m["callId"] = r.CallID
	}
	if len(r.Options) > 0 {
		m["options"] = r.Options
	}
	if !r.ExpiresAt.IsZero() {
		m["expiresAt"] = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return projectedItem{}, false
	}
	return projectedItem{key: "interaction:" + r.ID, typ: "interaction", at: r.CreatedAt,
		payload: payload, fingerprint: fingerprintPayload(payload)}, true
}

func fingerprintPayload(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16]
}

func policyJSON(p coreapp.Policies) map[string]any {
	return map[string]any{
		"toolTimeoutMs": p.ToolTimeoutMS, "turnTimeoutMs": p.TurnTimeoutMS,
		"maxTurnTokens": p.MaxTurnTokens, "loopRepeatLimit": p.LoopRepeatLimit,
		"loopAbortLimit": p.LoopAbortLimit, "parallelTools": p.ParallelTools,
	}
}

func (s *Server) pending(ctx context.Context, threadID string) []ports.InteractionRequest {
	var all []ports.InteractionRequest
	if threadID != "" {
		list, err := s.sess.PendingInteractions(ctx, threadID)
		if err == nil {
			return list
		}
		return nil
	}
	// 未给 threadId 时列本工作区全部线程的 pending。
	page, err := s.sess.ListThreads(ctx, ports.ListThreadsRequest{Workspace: s.sess.Workspace(), Limit: 200, IncludeArchived: true})
	if err != nil {
		return nil
	}
	for _, th := range page.Items {
		list, err := s.sess.PendingInteractions(ctx, th.ID)
		if err == nil {
			all = append(all, list...)
		}
	}
	return all
}

func (s *Server) runState(threadID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runStateLocked(threadID)
}

// noteRunError 把轮次内发过的 evt.error 收敛到 runInfo。
//
// 引擎在轮次内部失败时只返回终态原因（err == nil），turn.start 的等待者拿不到
// *domain.Error，页面因此只能显示一句泛泛的“检查 Core 日志”。错误事件是引擎
// 唯一会带出码与详情的地方，这里按 runId 归到对应轮次，供 run.get/thread.get
// 与终态页使用；后来者覆盖前者（最后一个错误才是结束本轮的那个）。
func (s *Server) noteRunError(evt domain.Event) {
	if evt.Kind != domain.EvtError || evt.ThreadID == "" || len(evt.Payload) == 0 {
		return
	}
	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		RunID   string `json:"runId"`
	}
	if err := json.Unmarshal(evt.Payload, &body); err != nil || body.Code == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.runs[evt.ThreadID]
	if cur == nil || (body.RunID != "" && cur.runID != body.RunID) {
		return
	}
	cur.errorCode = body.Code
	cur.errorDetail = body.Message
}

// noteRunUsage 把轮次内发过的 evt.usage 增量收敛到 runInfo。
//
// 引擎的 evt.usage 载荷是**本轮累计快照**（usageAcc.snapshot 每次发总量），
// 不是两次之间的差值：这里直接覆盖而不是累加，重复投递不会把数字放大。
// 输入框下方的 tok/s 与缓存命中率都读它（thread.get 的 run.usage）。
func (s *Server) noteRunUsage(evt domain.Event) {
	if evt.Kind != domain.EvtUsage || evt.ThreadID == "" || len(evt.Payload) == 0 {
		return
	}
	var body struct {
		RunID string       `json:"runId"`
		Usage domain.Usage `json:"usage"`
	}
	if err := json.Unmarshal(evt.Payload, &body); err != nil {
		return
	}
	// 只有状态为 running 的轮次需要实时用量：终态之后的补发不再计入，
	// 否则会把「上一轮的迟到事件」盖到新轮次上。
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.runs[evt.ThreadID]
	if cur == nil || cur.status != "running" {
		return
	}
	if body.RunID != "" && cur.runID != body.RunID {
		return
	}
	cur.usage = body.Usage
}

func (s *Server) runStateLocked(threadID string) map[string]any {
	cur := s.runs[threadID]
	if cur == nil {
		return map[string]any{"runId": "", "status": "terminal", "terminal": domain.TerminalCancelled, "startedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	}
	return runStateJSON(cur)
}

// threadRunning 报告某线程是否有未终态的轮次。runs 是进程内 map，查询零 IO，
// thread.list 逐线程调用没有成本（协议 ThreadSummary.running，08 §8.2）。
func (s *Server) threadRunning(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.runs[threadID]
	return cur != nil && cur.status == "running"
}

func runStateJSON(r *runInfo) map[string]any {
	out := map[string]any{
		"runId": r.runID, "status": r.status,
		"startedAt": r.startedAt.Format(time.RFC3339Nano),
	}
	if r.status == "terminal" {
		out["terminal"] = r.terminal
	}
	if r.errorCode != 0 {
		out["errorCode"] = r.errorCode
	}
	if r.errorDetail != "" {
		out["errorDetail"] = r.errorDetail
	}
	if !r.endedAt.IsZero() {
		out["endedAt"] = r.endedAt.Format(time.RFC3339Nano)
	}
	// usage 是本轮累计（不是增量快照）：客户端用 outputTokens / 已运行秒数
	// 算 tok/s，用 inputTokens+cacheTokens 算缓存命中率。
	if r.usage != (domain.Usage{}) {
		out["usage"] = map[string]any{
			"inputTokens":  r.usage.InputTokens,
			"outputTokens": r.usage.OutputTokens,
			"cacheTokens":  r.usage.CacheTokens,
			"costMicros":   r.usage.CostMicros,
		}
	}
	return out
}

func threadJSON(t ports.Thread) map[string]any {
	out := map[string]any{
		"id": t.ID, "workspace": t.Workspace, "title": t.Title,
		"createdAt": t.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updatedAt": t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"archived":  t.Archived,
	}
	if t.HeadEntryID != "" {
		out["headEntryId"] = t.HeadEntryID
	}
	return out
}

func entryJSON(e ports.Entry) map[string]any {
	out := map[string]any{
		"id": e.ID, "threadId": e.ThreadID, "seq": e.Seq,
		"kind": string(e.Kind), "payload": e.Payload, "tokens": e.Tokens,
		"createdAt": e.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if e.ParentID != "" {
		out["parentId"] = e.ParentID
	}
	if e.Role != "" {
		out["role"] = string(e.Role)
	}
	return out
}

func interactionsJSON(reqs []ports.InteractionRequest) []map[string]any {
	out := make([]map[string]any, 0, len(reqs))
	for _, r := range reqs {
		m := map[string]any{
			"id": r.ID, "threadId": r.ThreadID, "runId": r.RunID,
			"pluginId": r.PluginID, "kind": string(r.Kind), "title": r.Title,
			"body": r.Body, "status": string(domain.InteractionPending),
			"createdAt": r.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if r.CallID != "" {
			m["callId"] = r.CallID
		}
		if len(r.Options) > 0 {
			m["options"] = r.Options
		}
		if !r.ExpiresAt.IsZero() {
			m["expiresAt"] = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, m)
	}
	return out
}
