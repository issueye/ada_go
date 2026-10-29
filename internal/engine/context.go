package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// usageAcc 累计一轮内的模型用量（evt.usage 是增量，runs.usage_json 是总量）。
type usageAcc struct{ u domain.Usage }

func (a *usageAcc) Add(other domain.Usage) {
	a.u.InputTokens += other.InputTokens
	a.u.OutputTokens += other.OutputTokens
	a.u.CacheTokens += other.CacheTokens
	a.u.CostMicros += other.CostMicros
}

func (a *usageAcc) Total() int { return int(a.u.InputTokens + a.u.OutputTokens) }

func (a *usageAcc) snapshot() domain.Usage { return a.u }

// windowView 是重建后的模型上下文。
type windowView struct {
	summary  *CompactSummary
	entries  []ports.Entry
	messages []domain.Message
}

// decodeWindow 把 SessionStore 的窗口拆成「摘要 + 保留 entry」。
func decodeWindow(w ports.ContextWindow) (*CompactSummary, []ports.Entry) {
	var summary *CompactSummary
	if w.Compaction != nil {
		var s CompactSummary
		if err := json.Unmarshal(w.Compaction.Payload, &s); err == nil {
			summary = &s
		} else {
			// 摘要读不出来时不猜内容：按无摘要处理，让水位与压缩重新判定。
			summary = &CompactSummary{Markdown: string(w.Compaction.Payload), Failed: true}
		}
	}
	return summary, w.Entries
}

// toMessages 把 entry 还原成发给模型的消息序列（02 §5.3 第 3 步）。
//
// 只有 kind=message 的 entry 进模型；compaction 作为摘要前缀，其它 kind 是元数据。
// 摘要作为一条 system/assistant 回顾消息插入最前，位置固定以保证可复现。
func (r *run) toMessages(summary *CompactSummary, entries []ports.Entry) []domain.Message {
	out := make([]domain.Message, 0, len(entries)+1)
	if summary != nil && strings.TrimSpace(summary.Markdown) != "" {
		out = append(out, domain.Message{
			Role:  domain.RoleSystem,
			Parts: []domain.Part{{Type: domain.PartText, Text: "此前对话的摘要：\n" + summary.Markdown}},
		})
	}
	for _, e := range entries {
		if e.Kind != domain.EntryMessage || len(e.Payload) == 0 {
			continue
		}
		m, ok := decodeEntry(e)
		if !ok {
			// 历史里有无法解析的 entry：跳过并保持序号连续，不因此丢整段上下文。
			r.emitNotice(domain.NoticeWarn, "entry "+e.ID+" 无法解析为消息，已跳过")
			continue
		}
		out = append(out, m)
	}
	return out
}

// systemPrompt 组装系统提示（安全前言 + 插件片段 + 技能元数据）。
func (r *run) systemPrompt() string {
	var prompts PromptSource
	var skills SkillSource
	if r.deps.Prompts != nil {
		prompts = r.deps.Prompts
	}
	if r.deps.Skills != nil {
		skills = r.deps.Skills
	}
	return buildSystem(r.ctx, r.spec, prompts, skills)
}

// params 是本次模型请求的采样参数。
func (r *run) params() ports.Params {
	p := ports.Params{MaxTokens: r.spec.MaxTokens, Temperature: r.spec.Temperature, Thinking: r.spec.Thinking}
	if p.ImagesAllowed == nil {
		allowed := true
		p.ImagesAllowed = &allowed
	}
	return p
}

// toolSchemas 把目录快照转成模型可见的工具声明。
func toolSchemas(cat ports.ToolCatalog, ctx context.Context) []ports.ToolSchema {
	list := cat.List(ctx)
	out := make([]ports.ToolSchema, 0, len(list))
	for _, d := range list {
		out = append(out, ports.ToolSchema{Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	return out
}

// compact 触发一次压缩：compaction.before → 摘要 → 落 entry → compaction.after（04 §5.7）。
//
// 返回值 handled=false 表示「不需要压/没装 Compactor」，reason 非空表示必须以该终态结束。
// 摘要失败不丢历史：标记 compaction_failed 后继续（用裁剪兜底）；若压缩+裁剪后仍超窗，
// 以 context_overflow 结束本轮 —— 这是错误终态，不是隐藏的步数上限（INV-12）。
func (r *run) compact(summary *CompactSummary, entries []ports.Entry) (*windowView, domain.TerminalReason, bool) {
	if r.deps.Compactor == nil {
		r.emitNotice(domain.NoticeWarn, "未配置压缩器：上下文超出水位后将直接按 context_overflow 结束")
		return nil, TerminalError, true
	}
	r.hook(HookCompactionBefore, mustJSON(map[string]any{"entries": len(entries)}))

	window := r.toMessages(summary, entries)
	used := windowTokens(r.systemPrompt(), summary, entries)
	overflowRisk := r.spec.Model.ContextWindow > 0 && used >= int(r.spec.Model.ContextWindow)

	res, err := r.deps.Compactor.Compact(r.ctx, CompactRequest{
		ThreadID:   r.spec.ThreadID,
		RunID:      r.spec.RunID,
		Model:      r.spec.Model,
		System:     r.systemPrompt(),
		Previous:   summary,
		Entries:    entries,
		KeepTokens: r.spec.CompactKeepTokens,
	})
	if err != nil {
		// 失败不丢历史：先记 compaction_failed，能撑过去就继续。
		r.emitNotice(domain.NoticeWarn, "压缩失败，继续使用裁剪兜底: "+err.Error())
		if overflowRisk {
			r.emitErrorCode(domain.CodeContextOverflow, "压缩失败且上下文已超出窗口")
			return nil, TerminalError, true
		}
		r.hook(HookCompactionAfter, mustJSON(map[string]any{"failed": true}))
		return &windowView{summary: summary, entries: entries, messages: window}, "", false
	}

	summaryOut := res.Summary
	summaryOut.FirstKeptEntryID = res.FirstKeptEntry
	payload := mustJSON(summaryOut)
	comp, err := r.deps.Session.AppendCompaction(r.ctx, r.spec.ThreadID, r.headID(), payload, res.FirstKeptEntry)
	if err != nil {
		r.emitErrorCode(domain.CodeStorageBusy, "写入压缩 entry 失败: "+err.Error())
		return nil, TerminalError, true
	}
	kept := res.KeptEntries
	// 压缩后重算：仍放不下就按 context_overflow 结束（04 §5.7 最后一条）。
	after := windowTokens(r.systemPrompt(), &res.Summary, kept)
	if r.spec.Model.ContextWindow > 0 && after >= int(r.spec.Model.ContextWindow) {
		r.emitErrorCode(domain.CodeContextOverflow, "压缩与裁剪后仍超出模型窗口")
		return nil, TerminalError, true
	}
	r.hook(HookCompactionAfter, mustJSON(map[string]any{"compactionId": comp.ID, "kept": len(kept)}))
	return &windowView{summary: &summaryOut, entries: kept, messages: r.toMessages(&summaryOut, kept)}, "", true
}

// headID 返回当前线程 head entry id（压缩 entry 的 parent）。
func (r *run) headID() string {
	t, err := r.deps.Session.GetThread(r.ctx, r.spec.ThreadID)
	if err != nil {
		return ""
	}
	return t.HeadEntryID
}

// appendAssistant 落助手消息 entry（含 toolCalls，供回执配对）。
func (r *run) appendAssistant(m domain.Message) {
	defer r.pending.Reset() // 增量缓冲已随本条消息落库，避免取消路径重复落一条。
	raw, err := json.Marshal(m)
	if err != nil {
		r.emitNotice(domain.NoticeError, "助手消息无法编码: "+err.Error())
		return
	}
	if _, err := r.deps.Session.AppendEntry(r.ctx, ports.Entry{
		ThreadID: r.spec.ThreadID, Kind: domain.EntryMessage, Role: domain.RoleAssistant, Payload: raw,
		Tokens: int64(estimateTokens(string(raw))),
	}); err != nil {
		r.emitErrorCode(domain.CodeStorageBusy, "追加助手消息失败: "+err.Error())
	}
}

// appendToolResult 落工具结果 entry：content 给模型，details/input 只给界面（INV-10）。
//
// input 是这一次调用的入参原文：界面的工具行要显示「调了什么」，展开面板也要能看
// 完整参数。它不进模型上下文（模型只看到 content 与自己的 tool_call）。
func (r *run) appendToolResult(call domain.ToolCall, res domain.Result) {
	payload := mustJSON(map[string]any{
		"callId":  call.CallID,
		"tool":    call.Tool,
		"status":  string(res.Receipt.Status),
		"input":   json.RawMessage(nonEmptyArgs(call.Args)),
		"content": res.Content,
		"receipt": res.Receipt,
	})
	if _, err := r.deps.Session.AppendEntry(r.ctx, ports.Entry{
		ThreadID: r.spec.ThreadID, Kind: domain.EntryMessage, Role: domain.RoleTool,
		Payload: payload, Tokens: int64(estimateTokens(string(payload))),
	}); err != nil {
		r.emitErrorCode(domain.CodeStorageBusy, "追加工具回执失败: "+err.Error())
	}
	r.emitItemUpdated("tool:"+call.CallID, mustJSON(map[string]any{
		"callId": call.CallID, "tool": call.Tool, "status": string(res.Receipt.Status),
		"details": res.Details, "receipt": res.Receipt,
	}))
}

// flushPartial 把已产生的部分输出落 entry（取消后内容必须保留，02 §5.3 终止条件）。
func (r *run) flushPartial(text, thinking string) {
	if strings.TrimSpace(text+thinking) == "" {
		return
	}
	var parts []domain.Part
	if thinking != "" {
		parts = append(parts, domain.Part{Type: domain.PartThinking, Text: thinking})
	}
	if text != "" {
		parts = append(parts, domain.Part{Type: domain.PartText, Text: text})
	}
	r.appendAssistant(domain.Message{Role: domain.RoleAssistant, Parts: parts})
}

// keepPartialMessage 在取消路径上确保已到达的内容不丢。
func (r *run) keepPartialMessage() {
	if r.pending.Len() == 0 {
		return
	}
	r.flushPartial(r.pending.Text(), r.pending.Thinking())
	r.pending.Reset()
}

// quote 是字符串转 JSON 字面量的最小实现（避免引入 strconv 之外的依赖）。
func quote(s string) string { return strconv.Quote(s) }

// pullSystemReplacement 从钩子替换载荷里取系统提示。
func pullSystemReplacement(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var v struct {
		System string `json:"system"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.System == "" {
		return "", false
	}
	return v.System, true
}

// decodeEntry 把一条 entry 原文还原成发给模型的消息。
//
// 工具回执的载荷形状是 08 §8.4 的 {callId,tool,status,content,receipt}，
// 不是 domain.Message 的 {role,parts,callId}：这里做显式映射，
// 并且只取 content —— receipt/details 属于界面（INV-10）。
func decodeEntry(e ports.Entry) (domain.Message, bool) {
	if len(e.Payload) == 0 {
		return domain.Message{}, false
	}
	var probe struct {
		CallID  string        `json:"callId"`
		Content []domain.Part `json:"content"`
		Parts   []domain.Part `json:"parts"`
	}
	if err := json.Unmarshal(e.Payload, &probe); err != nil {
		return domain.Message{}, false
	}
	if e.Role == domain.RoleTool || probe.Content != nil {
		if probe.CallID == "" {
			return domain.Message{}, false
		}
		return domain.Message{Role: domain.RoleTool, Parts: probe.Content, CallID: probe.CallID}, true
	}
	var m domain.Message
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		return domain.Message{}, false
	}
	if m.Role == "" {
		m.Role = e.Role
	}
	return m, true
}
