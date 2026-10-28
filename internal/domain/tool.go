package domain

import (
	"encoding/json"
	"time"
)

// Access 是工具的【外部效果声明】，不是审批开关。
// 基座不据此自动放行或拒绝；是否询问/阻止由策略插件在 tool.beforeCall 决定
// （02 §5.4 规则、03 §5.6；取值照 05 §6.3 ToolDef.access）。
type Access string

const (
	// AccessRead 只读外部状态；无策略插件时仍可用（02 §5.4）。
	AccessRead Access = "read"
	// AccessWrite 写外部状态；线程级 KV 不算 write（02 §5.4）；无策略插件时被拒绝。
	AccessWrite Access = "write"
	// AccessExec 执行命令；无策略插件时被拒绝。
	AccessExec Access = "exec"
	// AccessNetwork 网络访问；无策略插件时被拒绝。
	AccessNetwork Access = "network"
)

// Trust 是插件信任级别，照 05 §6.2 清单的 trust 字段。
// 它只决定可用能力与钩子动作等级（replace/abort 仅 builtin，06 §6.6），不改变注册方式。
type Trust string

const (
	// TrustBuiltin：预编译嵌入二进制，只能启用/禁用（05 §6.2、06 §6.9）。
	TrustBuiltin Trust = "builtin"
	// TrustVerified：Ed25519 签名有效（06 §6.9）。
	TrustVerified Trust = "verified"
	// TrustThirdParty：默认，受限（无 exec、无控制面钩子、默认无前端脚本）。
	TrustThirdParty Trust = "third-party"
)

// Descriptor 是工具描述符；字段逐字照 02 §5.4 的 Go 定义，
// JSON 字段名照 08 §8.4（tool.list 与模型请求共用同一份描述符）。
// INV-3：描述符注册表是唯一真源，目录由它派生；装配期冻结，运行期新登记只记日志
// （02 §5.4 规则）。基座不注册任何工具，空基座目录长度为 0。
type Descriptor struct {
	// Name 匹配 ^[a-z][a-z0-9_]{1,63}$（05 §6.3 ToolDef.name）。
	Name string `json:"name"`
	// Description 给模型的说明。
	Description string `json:"description"`
	// Parameters 是 JSON Schema；执行前先校验，失败直接生成 error 回执让模型自己修（02 §5.4）。
	Parameters json.RawMessage `json:"parameters"`
	// Access 是外部效果声明（read|write|exec|network）；基座不据此审批（02 §5.4）。
	Access Access `json:"access"`
	// Timeout 是单次执行预算；缺省取 DefaultToolTimeout（06 §6.4：工具按 timeoutMs，默认 120s）。
	// 协议 JSON 以毫秒计时（08 §8.4 timeoutMs），毫秒↔Duration 的换算由 coreproto 适配层承担。
	Timeout time.Duration `json:"timeoutMs"`
	// ParallelSafe 仅在整批都 ParallelSafe 且产品策略允许时并发，结果按源序回填（02 §5.4）。
	ParallelSafe bool `json:"parallelSafe"`
	// Repeatable 轮询类工具不计入循环检测（02 §5.3/§5.4、05 §6.3）。
	Repeatable bool `json:"repeatable"`
	// Rollback 为 true 时执行前把涉及文件纳入检查点（02 §5.4、09 §9.2）。
	Rollback bool `json:"rollback"`
	// TerminatesTurn 是 finish/ask 这类可终止轮次的标记（02 §5.4；终止条件 completed 行）。
	TerminatesTurn bool `json:"terminatesTurn"`
	// PluginID 是贡献该工具的插件 id（“官方工具插件”或第三方；基座源码不出现具体插件 id）。
	PluginID string `json:"pluginId"`
}

// ToolCall 是模型发起的一次工具调用引用（08 §8.4 回执的 callId/tool；
// tool_call 增量按 index 累积参数片段，03 §5.5）。
// INV-6：同一 callId 的 ToolCallStarted 事件必先于 ToolCallFinished（02 §5.3）。
type ToolCall struct {
	// CallID 形如 call_01J…（08 §8.4 示例），由 IDGen 生成。
	CallID string `json:"callId"`
	// Tool 是 Descriptor.Name。
	Tool string `json:"tool"`
	// Args 是已按 Parameters 校验过的参数快照 JSON。
	Args json.RawMessage `json:"args"`
}

// ReceiptStatus 是回执状态，照 02 §5.4（ok|error|denied|timeout|interrupted）。
type ReceiptStatus string

const (
	// ReceiptOK 成功。
	ReceiptOK ReceiptStatus = "ok"
	// ReceiptError 失败：参数校验失败、工具 panic/错误都转成 error 回执，不终止轮次（02 §5.4）。
	ReceiptError ReceiptStatus = "error"
	// ReceiptDenied 被策略拒绝。
	// 见 02 §5.4 INV-5：拒绝是工具结果（denied），不是异常，也不终止轮次。
	// 无 active 策略插件时基座对 write/exec/network 返回 denied: no policy（02 §5.4、03 §5.6）。
	ReceiptDenied ReceiptStatus = "denied"
	// ReceiptTimeout 超出 Timeout 预算（按失败关闭处理，INV-4）。
	ReceiptTimeout ReceiptStatus = "timeout"
	// ReceiptInterrupted 轮次取消/进程中断；取消后已产生的部分消息保留（02 §5.3、03 §5.6）。
	ReceiptInterrupted ReceiptStatus = "interrupted"
)

// Receipt 是工具执行回执；字段照 02 §5.4 的 Go 定义与 08 §8.4 的回执 JSON。
// 它是“工具结果是数据”的载体（02 §5.3 提示词装配边界）。
type Receipt struct {
	Status ReceiptStatus `json:"status"`
	// StartedAt/FinishedAt 对应 08 的 startedAt/finishedAt（RFC3339）。
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// DurationMS 对应 08 的 durationMs。
	DurationMS int64 `json:"durationMs"`
	// Truncated 标记输出被适配层统一截断（02 §5.4：默认 50KB/2000 行；界面可提示查看完整输出）。
	Truncated bool `json:"truncated"`
	// Checkpoint 是 Rollback 工具纳入的检查点 id（形如 chk_01J…，08 §8.4、09 §9.2）。
	Checkpoint string `json:"checkpoint,omitempty"`
	// Reason 是拒绝/失败原因；denied 时作为工具结果文本回给模型（08 §8.4 拒绝示例）。
	Reason string `json:"reason,omitempty"`
}

// Result 是工具执行结果；字段逐字照 02 §5.4 的 Go 定义。
// content 给模型、details 只给界面（INV-10）、receipt 是状态/耗时/截断/检查点。
// 注：05 §6.3 的插件面 ToolReturn.terminate 到本类型的映射文档未定义，M1-A 前需澄清
// （Descriptor.TerminatesTurn 已覆盖静态标记；动态标记留待契约测试阶段）。
type Result struct {
	// Content 给模型：text / image（08 §8.4 的 content 形态）。
	Content []Part `json:"content"`
	// Details 给界面：diff、路径、计数，永不进模型（02 §5.4、INV-10）。
	Details map[string]any `json:"details,omitempty"`
	// Receipt 状态、耗时、截断、检查点（02 §5.4）。
	Receipt Receipt `json:"receipt"`
}
