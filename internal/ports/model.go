package ports

import (
	"context"
	"encoding/json"

	"github.com/adag/ada-go/internal/domain"
)

// ModelClient 是流式请求统一模型的端口（01 §5.2、03 §5.5）。
// 基座只拥有本契约与归一化流 DTO；具体供应商由 provider 插件注册、
// 由宿主桥接成本端口的实现（03 §5.5），基座不知道任何厂商的 URL/鉴权头/SSE 细节。
//
// 重试语义（03 §5.5）：重试由基座的 ModelClient 包装层负责（429/5xx/网络错误、
// 仅首个增量之前、指数退避+抖动、最多 3 次、尊重 Retry-After）；
// provider 只实现单次流。已经开始输出后不自动重试。
//
// 生产实现：internal/adapters 中由 provider 插件桥接的适配器（M1-E 起步，
// 由“官方 Chat 插件”提供首个实现，03 §5.5；基座源码不出现插件 id）。
// 测试替身：internal/testsupport/fakeprovider（脚本化模型流，development-plan M0.5/M1-A）。
type ModelClient interface {
	// Stream 发起一次流式请求；ctx 取消贯穿到 HTTP 请求（INV-8）。
	Stream(ctx context.Context, req Request) (Stream, error)
}

// Stream 是一次模型响应的增量流（03 §5.5）。
// 终止信号必须显式：收到 Kind=finish 的 Delta 才算正常结束；
// EOF 不等于完成——没有 finish 的 EOF 是流协议错误（domain.CodeProtocolError，03 §5.5）。
type Stream interface {
	// Recv 取下一个统一增量；文本、思考、工具调用参数、用量、结束都走这一个通道（03 §5.5）。
	Recv() (Delta, error)
	// Close 释放流；取消或提前结束时必须调用。
	Close() error
}

// ModelRef 引用一个模型：产品配置只按 id 引用，能力声明来自 provider 插件（03 §5.5 ModelInfo）。
type ModelRef struct {
	// ProviderID 是注册该模型的 provider 插件 id（由装配期目录提供）。
	ProviderID string `json:"providerId"`
	// ModelID 是产品配置中的模型名。
	ModelID string `json:"modelId"`
	// ContextWindow 单位 token（03 §5.5 ModelInfo.contextWindow；
	// 上下文水位与压缩触发依据它，04 §5.7）。
	ContextWindow int64 `json:"contextWindow"`
	// MaxOutput 可选输出上限（03 §5.5 maxOutput）。
	MaxOutput int64 `json:"maxOutput,omitempty"`
	// Input 声明输入模态 text/image（03 §5.5 input）。
	// 图片输入双门：模型能力位 + 产品能力位，任一说 false 就拒绝而不是悄悄丢图（03 §5.5）。
	Input []string `json:"input,omitempty"`
	// SupportsTools / SupportsThinking 能力位（03 §5.5）。
	SupportsTools    bool `json:"supportsTools"`
	SupportsThinking bool `json:"supportsThinking,omitempty"`
	// InputPerMTok / OutputPerMTok 定价，用于估算 Usage.CostMicros；无价时只显示 token（03 §5.5）。
	InputPerMTok  float64 `json:"inputPerMTok,omitempty"`
	OutputPerMTok float64 `json:"outputPerMTok,omitempty"`
}

// ToolSchema 是给模型的工具声明：由 Descriptor 裁剪而来（03 §5.5 Request.Tools；
// tool.list 与模型请求共用同一份描述符，08 §8.4）。
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Params 是采样与推理参数（03 §5.5：temperature、max tokens、thinking 等）。
type Params struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
	MaxTokens   int      `json:"maxTokens,omitempty"`
	// Thinking 非 nil 时显式开关思考；仅模型 SupportsThinking 时有效（03 §5.5）。
	Thinking *bool `json:"thinking,omitempty"`
	// ImagesAllowed 是【产品能力位】门（08 §8.6 capabilities.images）：与 ModelRef.Input
	// 的模型能力位构成图片输入双门，任一说 false 就拒绝带图请求，而不是悄悄丢图（03 §5.5）。
	ImagesAllowed *bool `json:"imagesAllowed,omitempty"`
	// Extra 是 provider 私有参数的透传位；基座不解释其内容。
	Extra json.RawMessage `json:"extra,omitempty"`
}

// Request 是一次模型请求，字段照 03 §5.5 的 Go 定义。
// Messages 是【已转换、已裁剪】的模型消息：INV-10 保证其中没有 UI-only 内容（02 §5.3）。
// 超时分层：建连 15s、空闲 90s、总时长 30min（domain 常量，03 §5.5、06 §6.4）。
type Request struct {
	Model    ModelRef         `json:"model"`
	System   string           `json:"system"`
	Messages []domain.Message `json:"messages"`
	Tools    []ToolSchema     `json:"tools,omitempty"`
	Params   Params           `json:"params,omitempty"`
}

// DeltaKind 是统一增量类型，照 03 §5.5：
// text.start/delta/end、thinking.start/delta/end、tool_call.start/delta/end、usage、finish。
type DeltaKind string

const (
	DeltaTextStart     DeltaKind = "text.start"
	DeltaTextDelta     DeltaKind = "text.delta"
	DeltaTextEnd       DeltaKind = "text.end"
	DeltaThinkingStart DeltaKind = "thinking.start"
	DeltaThinkingDelta DeltaKind = "thinking.delta"
	DeltaThinkingEnd   DeltaKind = "thinking.end"
	DeltaToolCallStart DeltaKind = "tool_call.start"
	DeltaToolCallDelta DeltaKind = "tool_call.delta"
	DeltaToolCallEnd   DeltaKind = "tool_call.end"
	DeltaUsage         DeltaKind = "usage"
	DeltaFinish        DeltaKind = "finish"
)

// Delta 是一条统一增量（03 §5.5）。
//   - text/thinking 增量用 Text 承载；
//   - tool_call 参数按 Index 累积 JSON 片段（ToolCallFragment），流结束时整体校验，
//     失败作为工具 error 回执而不是协议错误（03 §5.5、02 §5.4）；
//   - usage 增量填 Usage；
//   - finish 是唯一的显式终止信号（StartMarker 记录厂商的终止标记，如 [DONE]、
//     message_stop、response.completed，便于诊断“EOF 无终止”类错误）。
type Delta struct {
	Kind DeltaKind `json:"kind"`
	// Text 用于 text.* / thinking.* 的 start（可空）、delta 内容与 end（可空）。
	Text string `json:"text,omitempty"`
	// Index 是 tool_call 参数的累积序号（03 §5.5）。
	Index int `json:"index,omitempty"`
	// CallID / ToolName 在 tool_call.start 提供；片段阶段可为空。
	CallID   string `json:"callId,omitempty"`
	ToolName string `json:"tool,omitempty"`
	// ToolCallFragment 是 tool_call.delta 的 JSON 参数片段。
	ToolCallFragment string `json:"argsFragment,omitempty"`
	// Usage 在 Kind=usage 时给出（每次请求记录 input/output/cache token，03 §5.5）。
	Usage *domain.Usage `json:"usage,omitempty"`
	// FinishReason 在 Kind=finish 时给出厂商语义的结束原因。
	FinishReason string `json:"finishReason,omitempty"`
	// StartMarker 记录收到的是哪个显式终止标记（03 §5.5）。
	StartMarker string `json:"terminalMarker,omitempty"`
}
