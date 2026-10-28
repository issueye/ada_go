package engine

import (
	"encoding/json"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// drainSteering 是 steering 的唯一安全点：每个工具批次执行完、工具结果 entry
// 已追加之后、下一次模型请求之前调用。
//
// 不打断正在进行的模型流或工具：这里只做队列排空与 entry 追加；追加的消息
// 会随下一次 TraceFromHead 重建进入模型请求。没有工具调用的轮次不会走到这里，
// 因此队列会原样留给下一轮（02 §5.3 的自然收束路径不强插消息）。
func (r *run) drainSteering() {
	if r.deps.Steer == nil || r.ctx.Err() != nil {
		return
	}
	for _, m := range r.deps.Steer.DrainSteering(r.ctx, r.spec.ThreadID) {
		r.appendSteeringMessage(m)
	}
}

// appendSteeringMessage 把一条排空消息落成 user entry。
//
// 与正常用户消息保持同一口径：kind=message、role=user、parent=head，
// 并发出 user.message 钩子与一条 item 更新，让界面能看到这条中途消息；
// 它不产生新的终态（INV-9 仍由本轮唯一一次 finish 保证）。
func (r *run) appendSteeringMessage(m domain.Message) {
	if len(m.Parts) == 0 && len(m.ToolCalls) == 0 {
		return
	}
	// steering 队列的语义是「用户中途插话」：role 固定为 user，丢弃工具调用字段。
	m.Role = domain.RoleUser
	m.ToolCalls = nil
	m.CallID = ""

	raw, err := json.Marshal(m)
	if err != nil {
		r.emitNotice(domain.NoticeError, "中途消息无法编码: "+err.Error())
		return
	}
	entry, err := r.deps.Session.AppendEntry(r.ctx, ports.Entry{
		ThreadID: r.spec.ThreadID,
		ParentID: r.headID(),
		Kind:     domain.EntryMessage,
		Role:     domain.RoleUser,
		Payload:  raw,
		Tokens:   int64(estimateTokens(string(raw))),
	})
	if err != nil {
		r.emitErrorCode(domain.CodeStorageBusy, "追加中途消息失败: "+err.Error())
		return
	}

	r.emitItemUpdated("msg:"+entry.ID, mustJSON(map[string]any{
		"id": entry.ID, "seq": entry.Seq, "role": string(domain.RoleUser),
		"parts": m.Parts, "status": "end", "at": entry.CreatedAt,
	}))
	r.emitHook(HookUserMessage, raw)
}
