package domain

import "time"

// 资源限制与超时默认值（09 §9.5 与 06 §6.4/§6.6 的常量表）。
// 放在 domain 的理由：这些是“契约级”默认值，engine、toolkit、plugins、adapters、
// coreproto 都要引用；domain 是唯一允许被所有内部层依赖的包（01 §3.3 依赖方向），
// 放 ports 反而会让不触碰该端口的包被迫 import ports。
// 产品声明（08 §8.6 policies.*）可覆盖墙钟/token/工具超时等项；0 表示不限制。
const (
	// DefaultToolTimeout 是工具执行的默认预算（06 §6.4：取 ToolDef.timeoutMs，默认 120s；
	// 09 §9.5；08 §8.6 policies.toolTimeoutMs）。
	DefaultToolTimeout = 120 * time.Second

	// DefaultHookTimeout 是钩子调用的默认超时（06 §6.6、09 §9.5：200ms）。
	// tool.beforeCall 中 ctx.ui 等待不计入本预算（06 §6.6 约束），由交互期限负责。
	DefaultHookTimeout = 200 * time.Millisecond
	// MaxHookTimeout 是钩子调用的超时上限（06 §6.6：最大 2s；交互返回后剩余逻辑仍受它约束）。
	MaxHookTimeout = 2 * time.Second

	// DefaultScriptCallTimeout 是脚本单次调用的默认超时（06 §6.4/09 §9.5：路由/动作默认 5s）。
	// 工具（120s）与 provider 长流不受它约束；等待 ctx.ui/ctx.exec/http.sse 等宿主长调用时暂停计时。
	DefaultScriptCallTimeout = 5 * time.Second

	// DefaultInteractionTimeout 是用户交互的默认期限（06 §6.4：交互 300s；
	// 03 §5.6 示例 timeoutMs 300_000）。超时解析为 timeout，必须按拒绝处理（INV-4）。
	DefaultInteractionTimeout = 300 * time.Second

	// ProviderConnectTimeout 是 provider 流式请求的建连超时（03 §5.5、06 §6.4：15s）。
	ProviderConnectTimeout = 15 * time.Second
	// ProviderIdleTimeout 是流空闲超时（03 §5.5：90s，可经 ADAG_STREAM_IDLE 调整）。
	ProviderIdleTimeout = 90 * time.Second
	// ProviderTotalTimeout 是单次 provider 流的总时长上限（03 §5.5、06 §6.4：30 分钟）。
	ProviderTotalTimeout = 30 * time.Minute

	// DefaultCommandTimeout 是命令执行的默认超时（09 §9.5、06 §6.4：120s；超时/取消杀整棵进程树）。
	DefaultCommandTimeout = 120 * time.Second

	// RuntimeMemoryLimit 是插件 moejs Runtime 的堆上限（09 §9.5、06 §6.4：64MB）。
	RuntimeMemoryLimit = 64 << 20
	// RuntimeStackSize 是 Runtime 栈上限（06 §6.4 初始化：1<<16）。
	RuntimeStackSize = 1 << 16
	// RuntimeMaxCallDepth 是 Runtime 最大调用深度（09 §9.5、06 §6.4：2000）。
	RuntimeMaxCallDepth = 2000
	// HookRecursionDepthLimit 是钩子内再次触发同一钩子的深度上限（06 §6.6 约束：2）。
	HookRecursionDepthLimit = 2

	// ToolOutputMaxBytes 是单条工具输出截断阈值（02 §5.4、09 §9.5：50KB，统一在适配层做，
	// 回执标记 Truncated）。
	ToolOutputMaxBytes = 50 << 10
	// ToolOutputMaxLines 是单条工具输出行数上限（同上：2000 行）。
	ToolOutputMaxLines = 2000

	// ActionResultMaxBytes 是插件动作/工具结果的可序列化 JSON 上限（05 §6.3、06 §6.4：1MB）。
	ActionResultMaxBytes = 1 << 20
	// RouteResponseMaxBytes 是插件动态路由的响应上限（05 §6.3、07 §7.6：64KB）。
	RouteResponseMaxBytes = 64 << 10
	// PatchFieldTruncateBytes 是补丁中对单字段文本的截断阈值（08 §8.3、09 §9.5：32KB，
	// 截断并标 truncated，完整内容走 thread.entries 或工具详情）。
	PatchFieldTruncateBytes = 32 << 10

	// RPCMessageMaxBytes 是 Gateway↔Core 单条 WebSocket 消息上限（08 §8.1：2MB）。
	RPCMessageMaxBytes = 2 << 20

	// LoopRepeatLimit 是“相同工具+相同参数+相同回执”在同一轮内的累计次数阈值：达到后回
	// loop_detected 错误回执提示模型换方法（02 §5.3：3 次；按指纹累计，repeatable 不计数）。
	LoopRepeatLimit = 3
	// LoopAbortLimit 是同一指纹累计调用按 error 结束轮次的阈值（02 §5.3：5 次）。
	LoopAbortLimit = 5

	// ContextReserveTokens 是压缩预留：估算 token 超过 contextWindow-reserve 即触发压缩
	// （04 §5.7：默认 16k）。
	ContextReserveTokens = 16384
	// CompactKeepTokens 是压缩后保留的最近 token 量（04 §5.7：约 20k）。
	CompactKeepTokens = 20000
	// ContextHighWaterRatio 是轮次结束后「接近上限即后台预压缩」的比例（04 §5.7：80%）。
	ContextHighWaterRatio = 80
)
