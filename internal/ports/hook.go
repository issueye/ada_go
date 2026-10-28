package ports

import (
	"context"
	"encoding/json"

	"github.com/adag/ada-go/internal/domain"
)

// HookPoint 是钩子点位名，15 个，照 06 §6.6 表，全部有真实调用方；
// 成对点位必须成对出现（turn.before/after、model.beforeRequest/afterResponse、
// tool.beforeCall/afterCall、compaction.before/after）。
type HookPoint string

const (
	HookSessionStart         HookPoint = "session.start"
	HookSessionEnd           HookPoint = "session.end"
	HookThreadCreated        HookPoint = "thread.created"
	HookUserMessage          HookPoint = "user.message"
	HookTurnBefore           HookPoint = "turn.before"
	HookTurnAfter            HookPoint = "turn.after"
	HookModelBeforeRequest   HookPoint = "model.beforeRequest"
	HookModelAfterResponse   HookPoint = "model.afterResponse"
	HookToolBeforeCall       HookPoint = "tool.beforeCall"
	HookToolAfterCall        HookPoint = "tool.afterCall"
	HookCompactionBefore     HookPoint = "compaction.before"
	HookCompactionAfter      HookPoint = "compaction.after"
	HookInteractionRequested HookPoint = "interaction.requested"
	HookPluginLoaded         HookPoint = "plugin.loaded"
	HookError                HookPoint = "error"
)

// HookAction 是钩子动作分级（06 §6.6）：
//   - observe：只读，任何插件；
//   - note：向界面/日志附加说明，不改变数据面；
//   - deny：仅 tool.beforeCall，拒绝理由作为工具结果（03 §5.6：deny 对所有插件开放）；
//   - replace：仅 builtin + 仅 tool.beforeCall（替换参数后重新校验）与
//     model.beforeRequest（替换系统提示/注入上下文，第三方只能 note，02 §5.3 步骤 c）；
//   - abort：仅 builtin + 仅 turn.before / model.beforeRequest / tool.beforeCall。
type HookAction string

const (
	HookObserve HookAction = "observe"
	HookNote    HookAction = "note"
	HookDeny    HookAction = "deny"
	HookReplace HookAction = "replace"
	HookAbort   HookAction = "abort"
)

// HookEvent 是钩子收到的事件：JSON 快照，不是可变的引擎对象；
// 要改数据必须返回动作，由引擎裁决（06 §6.6 约束）。
// tool.beforeCall 的快照是 { callId, tool, access, input, threadId }，
// input 为已校验参数（06 §6.6）。
type HookEvent struct {
	Point    HookPoint     `json:"point"`
	ThreadID string        `json:"threadId,omitempty"`
	RunID    string        `json:"runId,omitempty"`
	CallID   string        `json:"callId,omitempty"`
	Tool     string        `json:"tool,omitempty"`
	Access   domain.Access `json:"access,omitempty"`
	// Snapshot 是点位相关的 JSON 快照（如 tool.beforeCall 的 input、
	// model.beforeRequest 的请求头、error 的诊断）。
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// HookOutcome 是单个处理器返回的动作与理由（{ action, reason } 形态见 03 §5.6 表）。
type HookOutcome struct {
	PluginID string     `json:"pluginId"`
	Action   HookAction `json:"action"`
	// Reason 是 deny/abort 的理由；denied 时作为工具结果回给模型（08 §8.4 拒绝示例）。
	Reason string `json:"reason,omitempty"`
	// Note 附加给界面/日志的说明（HookNote）。
	Note string `json:"note,omitempty"`
	// Replacement 是 HookReplace 的新内容（参数 JSON 或系统提示），替换后重新校验。
	Replacement json.RawMessage `json:"replacement,omitempty"`
}

// HookVerdict 是引擎裁决后的合流结果。
type HookVerdict struct {
	// Allowed 表示 deny/abort 后为 false；denied 走工具 denied 回执而非异常（INV-5）。
	Allowed bool
	// Aborted 为 true 时轮次按 error 路径结束。
	Aborted bool
	// Replacements 是替换后的参数/提示（重新校验责任在 engine）。
	Replacements json.RawMessage
	// Notes 是聚合的说明（HookNote）。
	Notes []string
	// Reason 是 deny/abort 的人类可读原因。
	Reason string
}

// HookRunner 是钩子调用、超时、动作裁决端口（01 §5.2、06 §6.6）。
// 语义约束：
//   - 超时默认 200ms、上限 2s（domain 常量）；tool.beforeCall 中 ctx.ui 等待不计入 200ms，
//     受交互期限约束，交互返回后剩余逻辑仍受 2s（06 §6.6）；
//   - 钩子内不能再次触发同一钩子：调用深度上限 2（06 §6.6、09 §9.5）；
//   - 失败方向按点位：tool.beforeCall 为 FailClosed（超时/异常=拒绝），
//     其余点位 FailOpen（不阻塞主循环）；超时记 plugin_timeout（06 §6.4/§6.6）。
//
// 生产实现：internal/plugins 钩子链（M1-A/M3）。
// 测试替身：internal/testsupport 的可编程钩子桩（注入超时/异常/各动作以驱动失败方向测试）。
type HookRunner interface {
	// Run 按 priority（同级按插件 id 稳定排序，06 §6.5）依次调用注册的处理器，
	// 汇总动作并给出裁决；任何 deny/abort/超时按点位失败方向收敛。
	Run(ctx context.Context, evt HookEvent) (HookVerdict, error)
	// FailureDirection 返回点位的失败方向（06 §6.6 表的“失败方向”列，供引擎与测试查询）。
	FailureDirection(point HookPoint) domain.FailureDirection
	// AllowedActions 返回点位允许的动作集合（06 §6.6；越权动作裁决为 plugin_denied，08 §8.5 3005）。
	AllowedActions(point HookPoint, trust domain.Trust) []HookAction
}
