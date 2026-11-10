package domain

import (
	"encoding/json"
	"time"
)

// EventKind 是领域事件名，照 08 §8.2 事件表的 13 个 evt.* 名字。
// 事件是 JSON-RPC notification（无 id），方法带 evt. 前缀（08 §8.1）。
type EventKind string

const (
	// EvtReady：Core 就绪、产品、工具数、插件数（工作区级，用 WorkspaceSeq）。
	EvtReady EventKind = "evt.ready"
	// EvtStateSnapshot：线程全量快照（items 窗口、seq、run、interactions）。
	// 断序时客户端请求 thread.get 全量重同步，不猜测缺失（08 §8.3、07 §7.4）。
	EvtStateSnapshot EventKind = "evt.state.snapshot"
	// EvtStatePatch：baseSeq + upsert/remove ops + 指纹；补丁只发变化字段（08 §8.3）。
	EvtStatePatch EventKind = "evt.state.patch"
	// EvtMessageDelta：消息 id、文本/思考增量、seq（08 §8.2）。
	EvtMessageDelta EventKind = "evt.message.delta"
	// EvtItemUpdated：单条 item 结构变化（工具卡状态等，08 §8.2）。
	EvtItemUpdated EventKind = "evt.item.updated"
	// EvtRunStatus：running / waiting_input / finishing / terminal（08 §8.2）。
	// INV-9：错误路径也必须恰好发一次 terminal（02 §5.3）。
	EvtRunStatus EventKind = "evt.run.status"
	// EvtInteractionRequested：交互 id、类型、标题、正文、选项、截止时间（08 §8.2）。
	EvtInteractionRequested EventKind = "evt.interaction.requested"
	// EvtInteractionResolved：accepted / denied / cancelled / timeout / unavailable（08 §8.2）。
	EvtInteractionResolved EventKind = "evt.interaction.resolved"
	// EvtNotice：只给人的提示，永不进模型（08 §8.2、INV-10）。
	EvtNotice EventKind = "evt.notice"
	// EvtUsage：token 与成本增量（08 §8.2；形状见 Usage）。
	EvtUsage EventKind = "evt.usage"
	// EvtPluginChanged：插件状态/贡献变化（工作区级，用 WorkspaceSeq，08 §8.2）。
	EvtPluginChanged EventKind = "evt.plugin.changed"
	// EvtPluginStream：插件 route 的 SSE 数据帧与终止帧（工作区级，streamId 关联）。
	EvtPluginStream EventKind = "evt.plugin.stream"
	// EvtError：可恢复错误 + 诊断码（08 §8.2；诊断码 ADG-<AREA>-<NNN>）。
	EvtError EventKind = "evt.error"
)

// RunStatus 是运行状态机对外的四个状态，照 08 §8.2 evt.run.status 载荷
// （内部状态机见 02 §5.3：idle→preparing→streaming⇄tools→finishing→finished）。
type RunStatus string

const (
	RunRunning      RunStatus = "running"       // 轮次运行中。
	RunWaitingInput RunStatus = "waiting_input" // 等待用户交互；有等待交互的实例不被回收（01 §4.2）。
	RunFinishing    RunStatus = "finishing"     // 收尾：turn.after、用量事件、恰好一次 TurnFinished。
	RunTerminal     RunStatus = "terminal"      // 终态；具体原因见 TerminalReason。
)

// TerminalReason 是轮次终态原因，照 02 §5.3 终止条件表。
// INV-12：轮次没有步数/工具调用次数上限，只能由下列事件之一结束（02 §5.3）。
type TerminalReason string

const (
	// TerminalCompleted：模型响应没有工具调用（自然收束），或调用了 TerminatesTurn=true 的工具。
	TerminalCompleted TerminalReason = "completed"
	// TerminalCancelled：turn.cancel 或等价用户操作；已产生的部分消息保留（02 §5.3、03 §5.6）。
	TerminalCancelled TerminalReason = "cancelled"
	// TerminalError：provider 重试后仍失败、流协议错误、context_overflow、
	// 连续 5 次重复调用触发 loop_detected、不可恢复的宿主错误（02 §5.3）。
	TerminalError TerminalReason = "error"
	// TerminalInterrupted：Core 崩溃/重启，运行轮标记为中断，可重试（02 §5.3）。
	TerminalInterrupted TerminalReason = "interrupted"
	// TerminalBudgetExceeded：仅当产品显式配置了墙钟或 token/成本上限时；默认 0 表示不限制（02 §5.3）。
	TerminalBudgetExceeded TerminalReason = "budget_exceeded"
)

// ItemKey 是投影 item 的稳定键（08 §8.3）：msg:<id> / tool:<callId> /
// interaction:<id> / notice:<id> / compaction:<id>。前端只做“替换目标 + 断序重同步”。
type ItemKey string

// PatchOp 是 evt.state.patch 的一个操作（08 §8.2/§8.3、07 §7.4 示例）。
type PatchOp struct {
	// Op 是 upsert 或 remove。
	Op string `json:"op"`
	// Key 是稳定的 ItemKey。
	Key ItemKey `json:"key"`
	// Fingerprint 是内容哈希；item 按 at 排序，避免流式导致的顺序抖动（08 §8.3）。
	Fingerprint string `json:"fingerprint,omitempty"`
	// Item 是变化后的 JSON；单字段超 32KB 由服务端截断并标 truncated（08 §8.3）。
	Item json.RawMessage `json:"item,omitempty"`
}

// Event 是一条领域事件。序号语义（08 §8.1/§8.3、07 §7.4）：
//   - 线程内 Seq 单调递增，由 Core 的唯一写协程分配（04 §5.7）；
//   - 工作区级事件（evt.ready、evt.plugin.changed）用独立的 WorkspaceSeq；
//   - 所有事件可重放，至少保留到对应 run 结束；总线保留最近 4096 条用于重连补发；
//   - 客户端发现 Seq 不连续 → 请求全量快照重同步，不猜测缺失。
//
// INV-6：Seq 单调；tool.beforeCall（ToolCallStarted）语义必先于同 callId 的完成事件。
type Event struct {
	Kind EventKind `json:"event"`
	// ThreadID 形如 th_<workspaceHash>_<ulid>；工作区级事件可为空（01 §3.1、04 §5.7）。
	ThreadID string `json:"threadId,omitempty"`
	// Seq 是线程内单调递增序号（SSE 的 id 行即它，07 §7.4）。
	Seq int64 `json:"seq,omitempty"`
	// WorkspaceSeq 是工作区级序号，与 Seq 分离计数（08 §8.1）。
	WorkspaceSeq int64 `json:"workspaceSeq,omitempty"`
	// BaseSeq 仅 evt.state.patch 使用：补丁基于的快照序号（08 §8.2）。
	BaseSeq int64 `json:"baseSeq,omitempty"`
	// At 是事件时间；投影按 at 排序（08 §8.3）。
	At time.Time `json:"at"`
	// Payload 是事件载荷 JSON（形状由各事件在 08 §8.2 表中定义）。
	Payload json.RawMessage `json:"payload,omitempty"`
}
