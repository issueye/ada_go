package domain

import "time"

// Role 是消息角色，取值来源 04 §5.7 的 entries.role 列（user|assistant|tool|system）。
type Role string

const (
	RoleUser      Role = "user"      // 用户消息。
	RoleAssistant Role = "assistant" // 助手消息：文本、思考、工具调用（02 §5.3 步骤 e）。
	RoleTool      Role = "tool"      // 工具结果消息：content 给模型，details 永不进模型（INV-10）。
	RoleSystem    Role = "system"    // 系统提示：基座安全前言 + 人格插件 + 技能元数据（02 §5.3 装配边界）。
)

// PartType 是内容部件类型，照 08 §8.4 的 content 形态：text / image。
type PartType string

const (
	PartText  PartType = "text"  // {"type":"text","text":"..."}。
	PartImage PartType = "image" // {"type":"image","mime":"...","data":"<base64>"}。
)

// Part 是消息的一个内容部件（08 §8.4）。
// 图片输入受双门约束：模型能力位 + 产品能力位，任一说 false 就拒绝而不是悄悄丢图（03 §5.5）。
type Part struct {
	Type PartType `json:"type"`
	// Text 仅在 Type=text 时有值。
	Text string `json:"text,omitempty"`
	// Mime/Data 仅在 Type=image 时有值；Data 为 base64（08 §8.4 示例）。
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
}

// Message 是【发给模型】的消息：只允许进入模型上下文的内容。
// 双层消息模型的另一半见 AgentMessage；转换只有一个边界（01 §2.1）。
// INV-10：UI-only 的 notice/details 永不进入本类型（02 §5.3、09 §9.4）。
type Message struct {
	Role Role `json:"role"`
	// Parts 是给模型的正文（text/image 部件）。
	Parts []Part `json:"parts"`
	// ToolCalls 是 assistant 消息发起的工具调用（02 §5.3 步骤 e）。
	ToolCalls []ToolCall `json:"toolCalls,omitempty"`
	// CallID 是 tool 角色消息所对应的调用 id（08 §8.4 回执的 callId）。
	CallID string `json:"callId,omitempty"`
}

// NoticeLevel 是 UI-only 通知的级别，照 05 §6.3 UIContext.notify 的
// info|warn|error。Notice 永不进模型（INV-10，08 §8.2 evt.notice“只给人的提示”）。
type NoticeLevel string

const (
	NoticeInfo  NoticeLevel = "info"
	NoticeWarn  NoticeLevel = "warn"
	NoticeError NoticeLevel = "error"
)

// AgentMessage 是【引擎/UI 层】消息：可含 UI-only 内容（01 §2.1 借鉴 pi 的双层模型）。
// 与发给模型的 Message 的区分方式：Model 字段为 nil 表示该条整体是 UI-only（例如 notice），
// 绝不参与模型上下文重建；Details/Notice 字段永不随 Model 下发（INV-10）。
// 转换边界由 engine 的上下文转换实现（M1-A），domain 只提供形状。
type AgentMessage struct {
	// ID 为 entry id（ULID，04 §5.7）。
	ID string `json:"id"`
	// Kind 为该 entry 的类型（04 entries.kind）。
	Kind EntryKind `json:"kind"`
	// Role 为消息角色；非 message 类 entry 可为空。
	Role Role `json:"role,omitempty"`
	// Model 是投影给模型的部分；nil 表示 UI-only，永不进模型（INV-10）。
	Model *Message `json:"model,omitempty"`
	// Details 只给界面（diff、路径、计数…），永不进模型（02 §5.4、09 §9.4）。
	Details map[string]any `json:"details,omitempty"`
	// Notice 是只给人的提示（evt.notice 语义，08 §8.2）；永不进模型（INV-10）。
	Notice *Notice `json:"notice,omitempty"`
	// CreatedAt 对应 04 entries.created_at（UnixMilli）。
	CreatedAt time.Time `json:"createdAt"`
}

// Notice 是一条 UI-only 通知（evt.notice 的载荷形状，08 §8.2；级别来自 05 §6.3）。
// INV-10：Notice 及其字段永不出现在模型消息里。
type Notice struct {
	Level NoticeLevel `json:"level"`
	Text  string      `json:"text"`
	// Diag 是可选诊断码字符串（ADG-<AREA>-<NNN>，08 §8.5）。
	Diag string `json:"diag,omitempty"`
}

// EntryKind 是会话 entry 的类型，照 04 §5.7 的 entries.kind 列
// （message|compaction|branch_summary|model_change|plugin_state|custom）。
type EntryKind string

const (
	// EntryMessage 是普通消息 entry（user/assistant/tool）。
	EntryMessage EntryKind = "message"
	// EntryCompaction 是压缩摘要 entry：追加而非改写，恢复时从 head 回溯到最近一条
	// （04 §5.7 “压缩也是一条 entry”）。
	EntryCompaction EntryKind = "compaction"
	// EntryBranchSummary 是分支摘要 entry（04 §5.7 kind 列）。
	EntryBranchSummary EntryKind = "branch_summary"
	// EntryModelChange 记录模型切换（04 §5.7 kind 列；model.set 的审计）。
	EntryModelChange EntryKind = "model_change"
	// EntryPluginState 记录插件写入的状态 entry（04 §5.7 kind 列）。
	EntryPluginState EntryKind = "plugin_state"
	// EntryCustom 是插件贡献的自定义 entry（04 §5.7 kind 列）。
	EntryCustom EntryKind = "custom"
)

// Usage 是一次模型请求的用量（03 §5.5：每次请求记录 input/output/cache token，
// 有价显示估算成本、无价只显示 token；字段照 04 usage 表与 08 evt.usage）。
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	// CacheTokens 是缓存命中 token（04 usage.cache_tokens）。
	CacheTokens int64 `json:"cacheTokens"`
	// CostMicros 是估算成本，微单位（04 usage.cost_micros）；无定价时为 0，只显示 token。
	CostMicros int64 `json:"costMicros"`
}
