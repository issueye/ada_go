package domain

// InteractionKind 是通用交互的类型，取值来源 04 §5.7 的 interactions.kind 列
// （confirm|select|input|form；对应 05 §6.3 UIContext 的 confirm/select/input/form）。
// 它是通用交互原语、不含审批语义；审批规则属于“官方默认策略插件”（03 §5.6）。
type InteractionKind string

const (
	InteractionConfirm InteractionKind = "confirm" // 是/否。
	InteractionSelect  InteractionKind = "select"  // 从选项中选一个。
	InteractionInput   InteractionKind = "input"   // 自由文本输入。
	// InteractionForm 是多题分步表单：Body 存题目数组（JSON），回答是
	// {"answers":[...]} 的 JSON 串（与题目同序）。界面用「上一题/下一题/提交」翻阅。
	InteractionForm InteractionKind = "form"
)

// InteractionStatus 是用户交互的状态机，取值来源 04 §5.7 的 interactions.status 列
// 与 03 §5.6（一次交互恰好解析一次）。
type InteractionStatus string

const (
	// InteractionPending 已持久化、等待响应；待处理交互重启后可恢复（03 §5.6）。
	InteractionPending InteractionStatus = "pending"
	// InteractionAccepted 用户明确同意（confirm=true / 选择了项 / 提交了输入）。
	InteractionAccepted InteractionStatus = "accepted"
	// InteractionDenied 用户明确拒绝。
	InteractionDenied InteractionStatus = "denied"
	// InteractionCancelled 用户或轮次取消；取消时未决交互标记 cancelled（03 §5.6 取消段）。
	InteractionCancelled InteractionStatus = "cancelled"
	// InteractionTimeout 等待超过交互期限（默认 300s）。见 INV-4：必须按拒绝处理。
	InteractionTimeout InteractionStatus = "timeout"
	// InteractionUnavailable SSE 断线/无 UI（CLI、非交互模式）。见 INV-4：必须按拒绝处理。
	InteractionUnavailable InteractionStatus = "unavailable"
)

// FailureDirection 是“安全判定拿不准时往哪边失败”的方向声明。
// 基座的失败判定默认关闭（FailClosed）；FailOpen 仅出现在文档明确“不阻塞主循环”
// 的观测类钩子点位（06 §6.6 表的“失败方向”列）。
type FailureDirection string

const (
	// FailClosed 失败即拒绝：交互超时记 timeout、断线/无 UI 记 unavailable，
	// 策略插件必须都当作拒绝，基座不会替它放行（02 §5.3 INV-4、03 §5.6、06 §6.6 tool.beforeCall 行）。
	// 无 active 策略插件时，write/exec/network 工具返回 denied: no policy ——
	// 这是“空策略”的失败关闭，不是审批规则（02 §5.4）。
	FailClosed FailureDirection = "closed"
	// FailOpen 失败即继续：仅适用于 observe/note 类点位与“写/执行工具立即拒绝”路径之外的
	// 非安全点；超时/异常不阻塞主循环（06 §6.6 约束）。
	FailOpen FailureDirection = "open"
)

// Direction 返回交互状态的默认失败方向：只有 accepted 是用户明确放行，
// 其余状态（含未知值）一律按关闭（拒绝）处理。
// 依据 INV-4（02 §5.3）：timeout/unavailable 都必须按拒绝处理。
func (s InteractionStatus) Direction() FailureDirection {
	if s == InteractionAccepted {
		return FailOpen // 已放行，不属于安全失败。
	}
	return FailClosed
}

// IsDenial 报告交互状态是否应被策略插件当作拒绝（INV-4 的判定辅助，
// 供 M1-A 的策略链与契约测试使用；基座不替插件做裁决）。
// 未知/空值按拒绝处理（失败关闭优先于失败开放）。
func (s InteractionStatus) IsDenial() bool { return s != InteractionAccepted }
