// Package proto 是 Gateway 与 Core 共享的协议词汇（设计 8.2、13 的依赖约束）。
//
// methods_gen.go 由 tools/genproto 从 spec/proto.json 生成，不允许手工编辑；
// dto.go 是手写 DTO，由 adag verify 的 generated 检查逐字段对照协议 Schema
// （definitions ↔ DTO 的绑定表在 tools/verify/dto.go）。Schema 是唯一手写源：
// 两边漂移时以 spec/proto.json 为准修 DTO，不反向修改契约。
//
// Gateway 与 Core 不共享可变内存：这里只有 DTO 与常量。
package proto

import "encoding/json"

// ---- JSON-RPC 2.0 信封（8.1）----

// Request 是一个 JSON-RPC 2.0 请求。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 是一个 JSON-RPC 2.0 响应。Result 与 Error 互斥。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification 是事件载体：无 id，方法带 evt. 前缀（8.1）。
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError 是错误响应体。Code 取 JSON-RPC 标准码或本包的应用码（8.5）。
type RPCError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData 携带人话与下一步：页面只显示诊断码 + 说明 + 下一步，不显示堆栈（8.5）。
type ErrorData struct {
	Name       string         `json:"name"`
	Diagnostic string         `json:"diagnostic"`
	NextStep   string         `json:"nextStep,omitempty"`
	Retryable  bool           `json:"retryable"`
	Details    map[string]any `json:"details,omitempty"`
}

// Error 让 RPCError 满足 error，便于跨层传递。
func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

// ---- 工具描述符与回执（8.4，逐字段照示例）----

// ToolDescriptor 是工具目录与模型请求共用的描述符。
type ToolDescriptor struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Parameters     json.RawMessage `json:"parameters"`
	Access         string          `json:"access"`
	TimeoutMS      int             `json:"timeoutMs"`
	ParallelSafe   bool            `json:"parallelSafe"`
	Rollback       bool            `json:"rollback"`
	TerminatesTurn bool            `json:"terminatesTurn"`
	PluginID       string          `json:"pluginId"`
}

// ToolResult 是工具结果 entry 的 payload（8.4）。
type ToolResult struct {
	CallID  string         `json:"callId"`
	Tool    string         `json:"tool"`
	Status  string         `json:"status"`
	Content []ContentPart  `json:"content"`
	Details map[string]any `json:"details,omitempty"`
	Receipt ToolReceipt    `json:"receipt"`
}

// ContentPart 给模型；ToolResult.Details 只给界面（INV-10）。
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Mime string `json:"mime,omitempty"`
	Data string `json:"data,omitempty"`
}

// ToolReceipt 是结构化回执：状态、耗时、截断、检查点。
type ToolReceipt struct {
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
	DurationMS int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// ---- 快照与补丁（8.3）----

// Item 是投影里的一条界面项；Key 前缀见 ItemKeyPrefixes，投影按 At 排序。
type Item struct {
	Key         string         `json:"key"`
	Type        string         `json:"type"`
	At          string         `json:"at"`
	Fingerprint string         `json:"fingerprint"`
	Truncated   bool           `json:"truncated,omitempty"`
	Payload     map[string]any `json:"payload"`
}

// Snapshot 是线程全量快照（items 窗口、seq、run、interactions）。
type Snapshot struct {
	ThreadID     string        `json:"threadId"`
	Seq          int64         `json:"seq"`
	Items        []Item        `json:"items"`
	Run          *RunState     `json:"run,omitempty"`
	Interactions []Interaction `json:"interactions"`
}

// PatchOp 是补丁的一条操作；Key 见 ItemKeyPrefixes。
type PatchOp struct {
	Op          string `json:"op"` // upsert | remove
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Item        *Item  `json:"item,omitempty"`
}

// Patch 是增量补丁：baseSeq 之后的变化字段（8.3）。
type Patch struct {
	ThreadID    string    `json:"threadId"`
	Seq         int64     `json:"seq"`
	BaseSeq     int64     `json:"baseSeq"`
	Ops         []PatchOp `json:"ops"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

// MessageDelta 是文本/思考增量；单独走 delta，避免每帧渲染整段 Markdown（7.4）。
type MessageDelta struct {
	ThreadID  string `json:"threadId"`
	MessageID string `json:"messageId"`
	Seq       int64  `json:"seq"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
}

// ItemUpdated 是单条 item 的结构变化（工具卡状态、消息收尾等）。
type ItemUpdated struct {
	ThreadID string `json:"threadId"`
	Seq      int64  `json:"seq"`
	Item     Item   `json:"item"`
}

// RunStatusEvent 是运行状态变化；TerminalResult 只在 status=terminal 时出现（INV-9）。
type RunStatusEvent struct {
	ThreadID       string `json:"threadId"`
	RunID          string `json:"runId"`
	Seq            int64  `json:"seq"`
	Status         string `json:"status"`
	TerminalResult string `json:"terminalResult,omitempty"`
	ErrorCode      int    `json:"errorCode,omitempty"`
	At             string `json:"at"`
}

// InteractionRequested 是交互请求发出（8.2）。
type InteractionRequested struct {
	ThreadID      string   `json:"threadId"`
	InteractionID string   `json:"interactionId"`
	RunID         string   `json:"runId"`
	PluginID      string   `json:"pluginId"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Body          string   `json:"body"`
	Options       []string `json:"options,omitempty"`
	ExpiresAt     string   `json:"expiresAt,omitempty"`
	Seq           int64    `json:"seq"`
}

// InteractionResolved 是交互恰好解析一次的结果（INV-4：timeout/unavailable 都按拒绝处理）。
type InteractionResolved struct {
	ThreadID      string         `json:"threadId"`
	InteractionID string         `json:"interactionId"`
	Result        string         `json:"result"` // accepted | denied | cancelled | timeout | unavailable
	Response      map[string]any `json:"response,omitempty"`
	ResolvedAt    string         `json:"resolvedAt"`
	Seq           int64          `json:"seq"`
}

// PluginChanged 是插件状态/贡献变化（工作区级序号）。
type PluginChanged struct {
	Plugin       PluginSummary `json:"plugin"`
	Change       string        `json:"change"`
	WorkspaceSeq int64         `json:"workspaceSeq"`
}

// ErrorEvent 是可恢复错误 + 诊断码（8.5）。threadId/runId 可空：非线程级错误只有工作区序号。
type ErrorEvent struct {
	Code         int    `json:"code"`
	Name         string `json:"name"`
	Message      string `json:"message"`
	Diagnostic   string `json:"diagnostic"`
	Retryable    bool   `json:"retryable"`
	ThreadID     string `json:"threadId,omitempty"`
	RunID        string `json:"runId,omitempty"`
	WorkspaceSeq int64  `json:"workspaceSeq"`
}

// RunState 是某一轮的状态。Terminal 是终态原因，只在 status=terminal 时出现（INV-9）。
type RunState struct {
	RunID     string `json:"runId"`
	Status    string `json:"status"` // running | waiting_input | finishing | terminal
	Terminal  string `json:"terminal,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
	ErrorCode int    `json:"errorCode,omitempty"`
	Usage     *Usage `json:"usage,omitempty"`
}

// Interaction 是待处理或已解析的用户交互（03 §5.6、04 interactions 表）。
//
// interaction.respond 的 requestId 复用这里的 id：一次交互恰好解析一次，
// 重复响应幂等返回首次结果。
type Interaction struct {
	ID         string         `json:"id"`
	ThreadID   string         `json:"threadId"`
	RunID      string         `json:"runId"`
	CallID     string         `json:"callId,omitempty"`
	PluginID   string         `json:"pluginId"`
	Kind       string         `json:"kind"` // confirm | select | input
	Title      string         `json:"title"`
	Body       string         `json:"body"`
	Options    []string       `json:"options,omitempty"`
	Status     string         `json:"status"` // pending | accepted | denied | cancelled | timeout | unavailable
	Response   map[string]any `json:"response,omitempty"`
	CreatedAt  string         `json:"createdAt"`
	ResolvedAt string         `json:"resolvedAt,omitempty"`
	ExpiresAt  string         `json:"expiresAt,omitempty"`
}

// Notice 是只给人的提示，永不进模型（8.2、INV-10）。
type Notice struct {
	NoticeID string `json:"noticeId"`
	ThreadID string `json:"threadId"`
	Seq      int64  `json:"seq"`
	Level    string `json:"level"` // info | warn | error
	Text     string `json:"text"`
	At       string `json:"at"`
}

// Usage 是一次请求的用量（04 usage 表的聚合视图；costMicros 用微单位避免浮点）。
type Usage struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int    `json:"inputTokens"`
	OutputTokens int    `json:"outputTokens"`
	CacheTokens  int    `json:"cacheTokens"`
	CostMicros   int64  `json:"costMicros"`
}

// UsageDelta 是 evt.usage 的载荷：用量 + 归属线程/轮次/序号。
type UsageDelta struct {
	ThreadID string `json:"threadId"`
	RunID    string `json:"runId"`
	Seq      int64  `json:"seq"`
	Usage    Usage  `json:"usage"`
}

// PluginSummary 是插件状态摘要（不含密钥与宿主路径细节）。
type PluginSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	APIVersion  int    `json:"apiVersion,omitempty"`
	Trust       string `json:"trust"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"` // ready | degraded | disabled | failed
	Fingerprint string `json:"fingerprint"`
	LastError   string `json:"lastError,omitempty"`
	// Source 是第三方插件的来源（workspace|global）；随包插件省略（M3 安装/发现）。
	Source        string         `json:"source,omitempty"`
	Contributions map[string]any `json:"contributions,omitempty"`
}

// ModelRef 是模型选择的最小引用；能力声明见 ModelInfo。
type ModelRef struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

// ModelPricing 是每百万 token 的微单位价格。
type ModelPricing struct {
	InputPerMTok  int64 `json:"inputPerMTok"`
	OutputPerMTok int64 `json:"outputPerMTok"`
}

// ModelInfo 是 provider 声明的模型能力（03 §5.5）。
//
// 图片输入是双门：模型 input 含 image + 产品 capabilities.images，任一说 false 就拒绝。
type ModelInfo struct {
	ID               string        `json:"id"`
	DisplayName      string        `json:"displayName,omitempty"`
	ContextWindow    int           `json:"contextWindow"`
	MaxOutput        int           `json:"maxOutput,omitempty"`
	Input            []string      `json:"input"`
	SupportsTools    bool          `json:"supportsTools"`
	SupportsThinking bool          `json:"supportsThinking,omitempty"`
	Pricing          *ModelPricing `json:"pricing,omitempty"`
}

// ProviderSummary 是供应商配置摘要（不含密钥，8.2 provider.list）。
type ProviderSummary struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"displayName,omitempty"`
	BaseURL     string      `json:"baseUrl,omitempty"`
	Active      bool        `json:"active"`
	Models      []ModelInfo `json:"models"`
}

// SkillSummary 是技能元数据（装配期只注入 name/description，正文按需读，6.7）。
type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Origin      string `json:"origin"` // plugin | workspace
	PluginID    string `json:"pluginId,omitempty"`
}

// ChangeSet 是一个检查点与其涉及路径（8.2 change.list）。
//
// 「恢复/删除/跳过/失效」四份清单属于 RevertResult（回滚做了什么），
// 不在 ChangeSet（发生了什么）。
type ChangeSet struct {
	ID        string   `json:"id"`
	ThreadID  string   `json:"threadId"`
	RunID     string   `json:"runId,omitempty"`
	ItemKey   string   `json:"itemKey,omitempty"`
	Tool      string   `json:"tool,omitempty"`
	Paths     []string `json:"paths"`
	CreatedAt string   `json:"createdAt"`
}

// RevertResult 是 change.revert 的结果：四份清单，不谎报成功（02 §5.4）。
type RevertResult struct {
	Restored    []string `json:"restored"`
	Deleted     []string `json:"deleted"`
	Skipped     []string `json:"skipped"`
	Invalidated []string `json:"invalidated"`
}

// ActionEnvelope 是 action.invoke 的入参形状（7.6 动作桥）。
type ActionEnvelope struct {
	PluginID string         `json:"pluginId"`
	Action   string         `json:"action"`
	Input    map[string]any `json:"input,omitempty"`
	ThreadID string         `json:"threadId,omitempty"`
}

// RouteRequest 是 route.call 的入参形状（7.6：query、body、headers 子集）。
//
// 契约缺口：设计 7.6 要求「路径参数由宿主原样传入」，但 spec/proto.json 的
// RouteRequest 只有 method/path，没有 pathParams。v1 以 Schema 为准（插件从 Path 自行解析），
// 若要补字段必须先改设计文档再改 spec（见 docs/decisions.md 的开放项）。
type RouteRequest struct {
	PluginID string            `json:"pluginId"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Query    map[string]string `json:"query,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body,omitempty"`
}

// ProductCapabilities 是产品能力位（8.6）。
type ProductCapabilities struct {
	Skills    bool `json:"skills"`
	Images    bool `json:"images"`
	Subagents bool `json:"subagents"`
}

// ProductIdentity 是界面品牌与语言（刻意不含人格）。
type ProductIdentity struct {
	Name   string `json:"name"`
	Locale string `json:"locale"`
}

// ProductPolicies 是引擎与工具运行参数（刻意不含审批规则）。
type ProductPolicies struct {
	ToolTimeoutMS   int `json:"toolTimeoutMs"`
	TurnTimeoutMS   int `json:"turnTimeoutMs"`
	MaxTurnTokens   int `json:"maxTurnTokens"`
	LoopRepeatLimit int `json:"loopRepeatLimit"`
	LoopAbortLimit  int `json:"loopAbortLimit"`
	ParallelTools   int `json:"parallelTools"`
}

// ProductDeclaration 是握手时回交的产品声明摘要（8.6）。
type ProductDeclaration struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Plugins      []string            `json:"plugins"`
	Capabilities ProductCapabilities `json:"capabilities"`
	Identity     ProductIdentity     `json:"identity"`
	Policies     ProductPolicies     `json:"policies"`
}

// WorkspaceInfo 是工作区摘要（路径、产品、配置、数据目录）。
type WorkspaceInfo struct {
	InstanceID   string             `json:"instanceId"`
	Path         string             `json:"path"`
	WorkspaceKey string             `json:"workspaceKey"`
	DataDir      string             `json:"dataDir"`
	Product      ProductDeclaration `json:"product"`
	Config       map[string]any     `json:"config,omitempty"`
	PID          int                `json:"pid,omitempty"`
}

// HelloResult 是握手返回的完整实例摘要（4.2、8.2）。
type HelloResult struct {
	ProtoVersion int                 `json:"protoVersion"`
	APIVersion   string              `json:"apiVersion,omitempty"`
	CoreVersion  string              `json:"coreVersion,omitempty"`
	ProductID    string              `json:"productId"`
	Product      *ProductDeclaration `json:"product,omitempty"`
	Capabilities ProductCapabilities `json:"capabilities"`
	Plugins      []PluginSummary     `json:"plugins"`
	Workspace    WorkspaceInfo       `json:"workspace"`
	PID          int                 `json:"pid"`
	ReadyAt      string              `json:"readyAt"`
}

// ReadyEvent 是 evt.ready 的载荷。
type ReadyEvent struct {
	ProductID    string `json:"productId"`
	ProtoVersion int    `json:"protoVersion"`
	PID          int    `json:"pid"`
	ToolCount    int    `json:"toolCount"`
	PluginCount  int    `json:"pluginCount"`
	ReadyAt      string `json:"readyAt"`
}
