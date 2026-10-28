package testkit

import (
	"time"

	"github.com/adag/ada-go/internal/domain"
)

// StepKind 是脚本步的类型。前三段对应 03 §5.5 的统一增量
// （text/thinking/tool_call 各有 start/delta/end），后面是流的可控异常。
type StepKind string

const (
	kindTextStart     StepKind = "text.start"
	kindTextDelta     StepKind = "text.delta"
	kindTextEnd       StepKind = "text.end"
	kindThinkingStart StepKind = "thinking.start"
	kindThinkingDelta StepKind = "thinking.delta"
	kindThinkingEnd   StepKind = "thinking.end"
	kindToolStart     StepKind = "tool_call.start"
	kindToolDelta     StepKind = "tool_call.delta"
	kindToolEnd       StepKind = "tool_call.end"
	kindUsage         StepKind = "usage"
	// kindFinish 是唯一的显式终止信号（03 §5.5）。
	kindFinish StepKind = "finish"
	// kindIdle 让流静默 d：空闲超时（默认 90s）与取消贯穿都在这一格测。
	kindIdle StepKind = "idle"
	// kindRecvError 让 Recv 返回注入的错误（provider 失败注入）。
	kindRecvError StepKind = "recv_error"
	// kindEOF 让流在没有 finish 的情况下直接 EOF —— 「EOF ≠ 完成」场景，
	// 消费方必须判成 protocol_error（03 §5.5、domain.CodeProtocolError）。
	kindEOF StepKind = "eof_without_finish"
)

// ScriptStep 是一格脚本；请用下面的构造函数创建，不要手填字段。
type ScriptStep struct {
	Kind StepKind
	// Text 用于 text.*/thinking.* 的 start 与 delta 内容。
	Text string
	// Index / CallID / Tool 用于 tool_call.*（参数按 Index 累积，03 §5.5）。
	Index  int
	CallID string
	Tool   string
	// ToolCallFragment 是 tool_call.delta 的 JSON 参数片段。
	ToolCallFragment string
	// Usage 用于 usage 增量。
	Usage *domain.Usage
	// FinishReason / Marker 用于 finish（Marker 记录厂商的显式终止标记）。
	FinishReason string
	Marker       string
	// Delay 用于 idle；Err 用于 recv_error。
	Delay time.Duration
	Err   error
}

// Text 展开成一串 text.start → text.delta* → text.end。
// 至少给一格，否则会产生「空 text 段」，用于刻意测畸形流时请直接用底层构造函数。
func Text(chunks ...string) []ScriptStep {
	steps := make([]ScriptStep, 0, len(chunks)+2)
	steps = append(steps, ScriptStep{Kind: kindTextStart})
	for _, c := range chunks {
		steps = append(steps, ScriptStep{Kind: kindTextDelta, Text: c})
	}
	return append(steps, ScriptStep{Kind: kindTextEnd})
}

// Thinking 展开成 thinking.start → thinking.delta* → thinking.end。
func Thinking(chunks ...string) []ScriptStep {
	steps := make([]ScriptStep, 0, len(chunks)+2)
	steps = append(steps, ScriptStep{Kind: kindThinkingStart})
	for _, c := range chunks {
		steps = append(steps, ScriptStep{Kind: kindThinkingDelta, Text: c})
	}
	return append(steps, ScriptStep{Kind: kindThinkingEnd})
}

// ToolCall 展开成 tool_call.start → delta*（按 index 的参数片段）→ tool_call.end。
// argChunks 是 JSON 片段序列，拼起来才是一段完整参数：片段组装本身也是被测语义
// （03 §5.5「流结束时整体校验，失败作为工具错误回执」）。
func ToolCall(index int, callID, tool string, argChunks ...string) []ScriptStep {
	steps := make([]ScriptStep, 0, len(argChunks)+3)
	steps = append(steps, ScriptStep{Kind: kindToolStart, Index: index, CallID: callID, Tool: tool})
	for _, frag := range argChunks {
		steps = append(steps, ScriptStep{Kind: kindToolDelta, Index: index, ToolCallFragment: frag})
	}
	return append(steps, ScriptStep{Kind: kindToolEnd, Index: index, CallID: callID, Tool: tool})
}

// Usage 是一格用量增量（每次请求记录 input/output/cache token，03 §5.5）。
func Usage(u domain.Usage) ScriptStep { return ScriptStep{Kind: kindUsage, Usage: &u} }

// Finish 是显式终止信号；marker 记录厂商语义（[DONE] / message_stop / response.completed）。
func Finish(reason, marker string) ScriptStep {
	return ScriptStep{Kind: kindFinish, FinishReason: reason, Marker: marker}
}

// Idle 让流静默 d（配合 FakeProvider 的 IdleTimeout 或取消）。
func Idle(d time.Duration) ScriptStep { return ScriptStep{Kind: kindIdle, Delay: d} }

// RecvError 让 Recv 在这一格返回 err（错误注入，测 INV-9「错误路径也不能漏发终态」
// 与 03 §5.5「首个增量之后不自动重试」）。
func RecvError(err error) ScriptStep { return ScriptStep{Kind: kindRecvError, Err: err} }

// EOFWithoutFinish 让流直接 EOF 而不给 finish：必须被消费方判成 protocol_error。
func EOFWithoutFinish() ScriptStep { return ScriptStep{Kind: kindEOF} }

// ScriptedModel 是「一个轮次脚本」的多轮编排（10 §11.3 关键用例的 DSL）。
//
// Turns[i] 是第 i 次 Stream 调用产出的脚本；用 FakeProvider 承接它。
// 之所以按轮编排而不是把 finish 塞进一格：RunTurn 每轮都发一次新的 Stream 请求，
// 终止条件（completed/cancelled/error/…）由引擎判定，替身只负责把流喂对。
type ScriptedModel struct {
	// Turns 是逐轮脚本。
	Turns [][]ScriptStep
	// OpenFailures 让前 N 次 Stream 调用直接返回 OpenErr：
	// 配合 RetryModel 表达 10 §11.3 的「中途错误后重试」与 03 §5.5 的
	// 「只在首个增量之前重试」。
	OpenFailures int
	// OpenErr 是 Stream 阶段注入的错误；缺省用 domain.CodeProviderError。
	OpenErr error
	// LateOpenFailures 让第 N 次之后的 Stream 调用返回「已产出增量后再失败」的场景，
	// 即 RecvError 而不是 OpenErr（测「已开始输出不自动重试」）。
	LateOpenFailures int
	// ExhaustedPolicy 决定 Turns 用尽后的行为：默认 EOFWithoutFinish
	// ——让「测试脚本比实际轮数少」立刻暴露成 protocol_error，而不是挂住。
	ExhaustedPolicy []ScriptStep
}

// NewScriptedModel 用逐轮脚本构造。
func NewScriptedModel(turns ...[]ScriptStep) *ScriptedModel {
	return &ScriptedModel{Turns: turns}
}

// TurnCount 返回编排好的轮数。
func (m *ScriptedModel) TurnCount() int { return len(m.Turns) }

// PopTurn 取出第 idx 次请求对应的脚本（越界时返回 ExhaustedPolicy）。
func (m *ScriptedModel) PopTurn(idx int) []ScriptStep {
	if idx < len(m.Turns) {
		return m.Turns[idx]
	}
	if m.ExhaustedPolicy != nil {
		return m.ExhaustedPolicy
	}
	return []ScriptStep{EOFWithoutFinish()}
}

// ShouldFailOpen 报告第 idx 次 Stream 调用是否应在建立阶段失败。
func (m *ScriptedModel) ShouldFailOpen(idx int) bool {
	return idx < m.OpenFailures
}

// OpenError 返回建立阶段的错误。
func (m *ScriptedModel) OpenError() error {
	if m.OpenErr != nil {
		return m.OpenErr
	}
	return domain.NewError(domain.CodeProviderError, "fake provider: 建立流失败（脚本注入）")
}

// ---------------------------------------------------------------------------
// 场景构造器（10 §11.3 关键用例表的可复用表达）
// ---------------------------------------------------------------------------

// ToolTurn 造一轮「调用一个工具后正常收尾」的脚本。
// args 是完整参数 JSON，会被切成 3 段片段以覆盖组装路径。
func ToolTurn(callID, tool, args string) []ScriptStep {
	steps := []ScriptStep{}
	steps = append(steps, Thinking("思考：该调用 "+tool)...)
	steps = append(steps, ToolCall(0, callID, tool, splitArgs(args)...)...)
	steps = append(steps, Usage(domain.Usage{InputTokens: 100, OutputTokens: 8}))
	steps = append(steps, Finish("tool_calls", "[DONE]"))
	return steps
}

// TextTurn 造一轮「直接给文本、没有工具调用」的脚本（自然收束 → completed）。
func TextTurn(text string) []ScriptStep {
	steps := []ScriptStep{}
	steps = append(steps, Text(text)...)
	steps = append(steps, Usage(domain.Usage{InputTokens: 120, OutputTokens: 12}))
	steps = append(steps, Finish("stop", "[DONE]"))
	return steps
}

// FiftyToolTurnsThenText 表达 10 §11.3：
// 「模型连续 50 轮工具调用后给出文本 → 不因步数上限终止，最终 TurnFinished(completed)」。
// INV-12 的直接证据：50 轮不是上限，只是脚本。
func FiftyToolTurnsThenText(tool string) *ScriptedModel {
	return RepeatedToolTurnsThenText(50, tool, "已完成。")
}

// RepeatedToolTurnsThenText 表达「连续 n 轮相同工具 + 相同参数，最后给文本」。
// callID 每轮不同（引擎生成），工具名与参数完全一致 —— 这正是 02 §5.3 循环检测的输入：
// 连续 3 次得到 loop_detected 错误回执、第 5 次按 error 结束（Repeatable 工具除外）。
func RepeatedToolTurnsThenText(n int, tool, finalText string) *ScriptedModel {
	turns := make([][]ScriptStep, 0, n+1)
	args := `{"path":"/src/main.go"}`
	for i := 0; i < n; i++ {
		turns = append(turns, ToolTurn(fmtCallID(i), tool, args))
	}
	turns = append(turns, TextTurn(finalText))
	return NewScriptedModel(turns...)
}

// IdenticalToolTurns 表达「连续 3 次相同工具 + 相同参数 + 相同回执」：
// 参数逐字一致，回执一致由调用方用 StaticResponder 保证（10 §11.3 循环检测行）。
func IdenticalToolTurns(tool string, n int) *ScriptedModel {
	turns := make([][]ScriptStep, 0, n)
	args := `{"path":"/src/main.go"}`
	for i := 0; i < n; i++ {
		turns = append(turns, ToolTurn(fmtCallID(i), tool, args))
	}
	return NewScriptedModel(turns...)
}

// FinishToolTurn 表达「模型调用 finish 立即收尾」：
// finish 的 Descriptor.TerminatesTurn=true，引擎收到它就不再多问一轮模型（02 §5.3 终止条件）。
func FinishToolTurn(callID string) []ScriptStep {
	steps := []ScriptStep{}
	steps = append(steps, ToolCall(0, callID, "finish", `{"reason":"计划已全部完成"}`)...)
	steps = append(steps, Usage(domain.Usage{InputTokens: 90, OutputTokens: 5}))
	steps = append(steps, Finish("tool_calls", "[DONE]"))
	return steps
}

// ErrorThenSuccess 表达「中途错误后重试」：前 openFailures 次建立失败，之后成功。
// 前面补齐空轮次，保证「第 N 次 Stream 调用」与 Turns[N-1] 对齐
// （失败的调用同样消耗一次请求序号）。
func ErrorThenSuccess(openFailures int, text string) *ScriptedModel {
	turns := make([][]ScriptStep, 0, openFailures+1)
	for i := 0; i < openFailures; i++ {
		turns = append(turns, nil)
	}
	turns = append(turns, TextTurn(text))
	m := NewScriptedModel(turns...)
	m.OpenFailures = openFailures
	return m
}

// MidStreamError 表达「首个增量之后失败」：输出已经开始，不能自动重试（03 §5.5）。
func MidStreamError(err error) *ScriptedModel {
	steps := append([]ScriptStep{}, Text("第一段")...)
	steps = append(steps, RecvError(err))
	return NewScriptedModel(steps)
}

// EOFWithoutFinishScript 表达协议错误：EOF 但没有终止信号（03 §5.5）。
func EOFWithoutFinishScript() *ScriptedModel {
	steps := append([]ScriptStep{}, Text("写了一半")...)
	steps = append(steps, EOFWithoutFinish())
	return NewScriptedModel(steps)
}

// splitArgs 把参数 JSON 切成若干片段，覆盖 03 §5.5 的按 index 组装路径。
func splitArgs(args string) []string {
	if args == "" {
		return []string{"{}"}
	}
	n := len(args)
	switch {
	case n <= 2:
		return []string{args}
	case n <= 6:
		return []string{args[:1], args[1:]}
	default:
		a := n / 3
		return []string{args[:a], args[a : 2*a], args[2*a:]}
	}
}

func fmtCallID(i int) string {
	return "call_" + tail(i)
}

// tail 生成 26 位 ULID 形状尾段（场景构造器用，保证与 FixedIDGen 输出同形）。
func tail(i int) string { return encodeULID(FrozenTime.UnixMilli(), uint64(i+1)) }
