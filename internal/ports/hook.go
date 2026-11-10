package ports

import (
	"context"
	"encoding/json"

	"github.com/adag/ada-go/internal/domain"
)

// HookPoint 是钩子点位名，14 个，照 06 §6.6 表，全部有真实调用方；
// 成对点位必须成对出现（turn.before/after、model.beforeRequest/afterResponse、
// compaction.before/after）。
//
// 审批点位（tool.beforeCall/tool.afterCall）随审批策略整体移除：基座不再有
// 「执行前询问/拒绝」这一层。
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
//   - replace：仅 builtin，仅 model.beforeRequest（替换系统提示/注入上下文，
//     第三方只能 note，02 §5.3 步骤 c）；
//   - abort：仅 builtin，仅 turn.before / model.beforeRequest。
type HookAction string

const (
	HookObserve HookAction = "observe"
	HookNote    HookAction = "note"
	HookReplace HookAction = "replace"
	HookAbort   HookAction = "abort"
)

// HookEvent 是钩子收到的事件：JSON 快照，不是可变的引擎对象；
// 要改数据必须返回动作，由引擎裁决（06 §6.6 约束）。
//
// 快照形态（如 model.beforeRequest 的请求头、error 的诊断）见各点位文档。
type HookEvent struct {
	Point    HookPoint     `json:"point"`
	ThreadID string        `json:"threadId,omitempty"`
	RunID    string        `json:"runId,omitempty"`
	CallID   string        `json:"callId,omitempty"`
	Tool     string        `json:"tool,omitempty"`
	Access   domain.Access `json:"access,omitempty"`
	// Snapshot 是点位相关的 JSON 快照（model.beforeRequest 的请求头、error 的诊断）。
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// HookOutcome 是单个处理器返回的动作与理由（{ action, reason } 形态见 03 §5.6 表）。
type HookOutcome struct {
	PluginID string     `json:"pluginId"`
	Action   HookAction `json:"action"`
	// Reason 是 abort 的理由（轮次按 error 结束）。
	Reason string `json:"reason,omitempty"`
	// Note 附加给界面/日志的说明（HookNote）。
	Note string `json:"note,omitempty"`
	// Replacement 是 HookReplace 的新内容（系统提示/注入上下文），替换后由引擎重新校验。
	Replacement json.RawMessage `json:"replacement,omitempty"`
}

// HookVerdict 是引擎裁决后的合流结果。
type HookVerdict struct {
	// Aborted 为 true 时轮次按 error 路径结束。
	Aborted bool
	// Replacements 是替换后的提示（重新校验责任在 engine）。
	Replacements json.RawMessage
	// Notes 是聚合的说明（HookNote）。
	Notes []string
	// Reason 是 abort 的人类可读原因。
	Reason string
}

// HookRunner 是钩子调用、超时、动作裁决端口（01 §5.2、06 §6.6）。
// 语义约束：
//   - 超时默认 200ms、上限 2s（domain 常量）；
//   - 钩子内不能再次触发同一钩子：调用深度上限 2（06 §6.6、09 §9.5）；
//   - 处理器超时/异常一律按「失败方向为开」收敛：记警告、忽略该处理器，
//     不阻塞主循环（审批点位移除后不再有失败关闭点位）。
//
// 生产实现：internal/plugins 钩子链（M1-A/M3）。
// 测试替身：internal/conformance/testkit 的可编程钩子桩（注入超时/异常/各动作）。
type HookRunner interface {
	// Run 按 priority（同级按插件 id 稳定排序，06 §6.5）依次调用注册的处理器，
	// 汇总动作并给出裁决；abort 立即收敛，其余照常继续。
	Run(ctx context.Context, evt HookEvent) (HookVerdict, error)
	// AllowedActions 返回点位允许的动作集合（06 §6.6；越权动作裁决为 plugin_denied，08 §8.5 3005）。
	AllowedActions(point HookPoint, trust domain.Trust) []HookAction
}
