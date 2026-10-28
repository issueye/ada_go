package engine

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// pendingDelta 累积当前助手消息的增量，用于取消时保留部分内容（INV-8）。
type pendingDelta struct {
	text     strings.Builder
	thinking strings.Builder
}

func (p *pendingDelta) WriteText(s string)     { p.text.WriteString(s) }
func (p *pendingDelta) WriteThinking(s string) { p.thinking.WriteString(s) }
func (p *pendingDelta) Text() string           { return p.text.String() }
func (p *pendingDelta) Thinking() string       { return p.thinking.String() }
func (p *pendingDelta) Reset()                 { p.text.Reset(); p.thinking.Reset() }
func (p *pendingDelta) Len() int               { return p.text.Len() + p.thinking.Len() }

// emit 发一条领域事件。序号由 EventSink 实现补齐（8.1），引擎不自己编号。
func (r *run) emit(kind domain.EventKind, payload any) {
	evt := domain.Event{
		Kind:     kind,
		ThreadID: r.spec.ThreadID,
		At:       r.deps.Clock.Now(),
	}
	if payload != nil {
		evt.Payload = mustJSON(payload)
	}
	if err := r.deps.Events.Emit(r.ctx, evt); err != nil {
		// 事件发不出去不影响轮次正确性，但必须可见：记一条 notice 级日志事件。
		r.emitNotice(domain.NoticeWarn, "事件发射失败: "+err.Error())
	}
}

// emitRunStatus 广播运行状态；terminal 非空表示这是终态（INV-9 保证只发一次）。
func (r *run) emitRunStatus(status domain.RunStatus, terminal domain.TerminalReason) {
	body := map[string]any{"runId": r.spec.RunID, "status": string(status), "at": r.deps.Clock.Now()}
	if terminal != "" {
		body["terminal"] = string(terminal)
	}
	r.emit(domain.EvtRunStatus, body)
}

// emitDelta 发文本/思考增量（7.4：增量单独走 delta，避免每帧渲染整段 Markdown）。
func (r *run) emitDelta(messageID, partKind, text string) {
	if text == "" {
		return
	}
	body := map[string]any{"messageId": messageID, "partKind": partKind, "seq": 0}
	if partKind == "thinking" {
		body["thinking"] = text
		r.pending.WriteThinking(text)
	} else {
		body["text"] = text
		r.pending.WriteText(text)
	}
	r.emit(domain.EvtMessageDelta, body)
}

// emitMessageEnd 结束一条消息（界面侧据此把 delta 收敛成条目）。
func (r *run) emitMessageEnd(messageID string) {
	if messageID == "" {
		return
	}
	r.emit(domain.EvtItemUpdated, map[string]any{
		"key":         "msg:" + messageID,
		"status":      "end",
		"at":          r.deps.Clock.Now(),
		"fingerprint": fingerprintOf(messageID, r.pending.Text()),
	})
}

// emitItemUpdated 更新一条界面项（工具卡状态等，8.2）。
func (r *run) emitItemUpdated(key string, payload json.RawMessage) {
	r.emit(domain.EvtItemUpdated, map[string]any{"key": key, "item": payload, "at": r.deps.Clock.Now()})
}

// emitUsage 广播用量增量（8.2 evt.usage；永不进模型）。
func (r *run) emitUsage() {
	r.emit(domain.EvtUsage, map[string]any{"runId": r.spec.RunID, "usage": r.usage.snapshot()})
}

// emitNotice 是只给人的提示（INV-10：绝不进模型消息）。
func (r *run) emitNotice(level domain.NoticeLevel, text string) {
	r.emit(domain.EvtNotice, map[string]any{"level": string(level), "text": text, "at": r.deps.Clock.Now()})
}

// emitErrorCode 发可恢复错误事件（8.2 evt.error）。
func (r *run) emitErrorCode(code domain.Code, detail string) {
	r.emitError(domain.NewError(code, detail))
}

func (r *run) emitError(err *domain.Error) {
	body := map[string]any{
		"code": int(err.Code), "name": err.Name, "message": err.Detail,
		"runId": r.spec.RunID, "at": r.deps.Clock.Now(),
	}
	if err.Diag != "" {
		body["diag"] = err.Diag
	}
	r.emit(domain.EvtError, body)
	r.hook(HookError, mustJSON(body))
}

// hook 调用一次钩子点位并返回裁决；引擎不解释业务规则，只收敛动作（06 §6.6）。
func (r *run) hook(point ports.HookPoint, snapshot json.RawMessage) (ports.HookVerdict, error) {
	evt := ports.HookEvent{
		Point:    point,
		ThreadID: r.spec.ThreadID,
		RunID:    r.spec.RunID,
		Snapshot: snapshot,
	}
	return r.deps.Hooks.Run(r.ctx, evt)
}

// emitHook 是「只观察」的钩子触发点（记录但不取裁决）。
func (r *run) emitHook(point ports.HookPoint, snapshot json.RawMessage) {
	if _, err := r.hook(point, snapshot); err != nil {
		// 观察型钩子失败方向为开：不影响主流程，但错误必须可见。
		r.emitNotice(domain.NoticeWarn, string(point)+" 钩子失败: "+err.Error())
	}
}

// newMessageID 生成本条助手消息的投影 id。
func (r *run) newMessageID() string {
	id, err := r.deps.IDs.NewMessageID(r.ctx)
	if err != nil {
		return ""
	}
	return id
}

// newCallID 为缺少 id 的工具调用补一个（provider 不给 index 对齐时的兜底）。
func (r *run) newCallID(ctx context.Context) string {
	id, err := r.deps.IDs.NewCallID(ctx)
	if err != nil {
		return ""
	}
	return id
}

// fingerprintOf 是事件里的内容指纹（8.3：投影按 at + fingerprint 去抖）。
func fingerprintOf(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p)
		b.WriteByte(0)
	}
	return shortHash(b.String())
}
