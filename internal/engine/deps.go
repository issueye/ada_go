package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// Deps 是一次轮次需要的全部端口。任何一个为 nil 都直接拒绝启动轮次（INV-2：不允许 nil 端口）。
//
// 引擎只认识 ports 与 domain：不知道 moejs、不知道 SQLite、不知道 Gin（设计 3.3、5.1）。
// 工具执行通过消费方自定义的 ToolRunner 注入（12 个端口是冻结集合，工具机制属于 toolkit）。
type Deps struct {
	Clock        ports.Clock
	IDs          ports.IDGen
	Events       ports.EventSink
	Session      ports.SessionStore
	Model        ports.ModelClient
	Tools        ports.ToolCatalog
	Interactions ports.InteractionBroker
	Hooks        ports.HookRunner

	// Runner 执行具体工具（生产实现是 internal/toolkit.Executor，测试替身在 conformance/testkit）。
	Runner ToolRunner

	// Compactor 生成摘要（03 §5.5：摘要本身也要走模型）；nil 时压缩不可用，
	// 超水位直接按 context_overflow 结束，而不是静默截断历史。
	Compactor Compactor

	// Prompts 提供系统提示（安全前言由基座固定，人格与领域指令来自插件）。
	Prompts PromptSource

	// Skills 注入技能元数据（6.7：装配期只注入 name/description）。
	Skills SkillSource

	// RunStore 为 nil 时轮次账目只发事件不落库（CLI/测试路径）。
	RunStore RunStore

	// HasPolicy 报告当前装配里是否存在 role=tool-policy 插件（02 §5.4 的失败关闭依据）。
	// nil 视为「无策略」：安全判定默认关闭。
	HasPolicy func() bool
}

// ToolRunner 执行一次工具调用并返回结构化结果（INV-5：拒绝与错误都是结果）。
type ToolRunner interface {
	Execute(ctx context.Context, call domain.ToolCall) domain.Result
}

// Validate 断言端口齐备。
func (d Deps) Validate() error {
	missing := []string{}
	if d.Clock == nil {
		missing = append(missing, "Clock")
	}
	if d.IDs == nil {
		missing = append(missing, "IDGen")
	}
	if d.Events == nil {
		missing = append(missing, "EventSink")
	}
	if d.Session == nil {
		missing = append(missing, "SessionStore")
	}
	if d.Model == nil {
		missing = append(missing, "ModelClient")
	}
	if d.Tools == nil {
		missing = append(missing, "ToolCatalog")
	}
	if d.Interactions == nil {
		missing = append(missing, "InteractionBroker")
	}
	if d.Hooks == nil {
		missing = append(missing, "HookRunner")
	}
	if d.Runner == nil {
		missing = append(missing, "ToolRunner")
	}
	if len(missing) > 0 {
		return domain.NewError(domain.CodeCoreUnavailable, "引擎端口缺失: "+joinMissing(missing))
	}
	return nil
}

// TurnSpec 是一次轮次的输入（M0 只冻结字段口径，M1-A 用它跑真实循环）。
//
// 终止条件相关字段全部来自产品声明的 policies（08 §8.6）：步数与工具调用次数永远不是上限，
// 墙钟与 token 上限默认 0 表示不限制（INV-12）。
type TurnSpec struct {
	ThreadID       string
	RunID          string
	IdempotencyKey string

	UserInput domain.Message
	// System 是本轮附加的上下文说明（如工作区提示）；不是人格（08 §8.6 identity 不含人格）。
	System      string
	Model       ports.ModelRef
	MaxTokens   int
	Temperature *float64
	Thinking    *bool

	TurnTimeoutMS   int
	MaxTurnTokens   int
	LoopRepeatLimit int
	LoopAbortLimit  int
	ParallelTools   int

	// ReserveTokens 是压缩预留（04 §5.7：contextWindow - reserve(16k)）。
	ReserveTokens int
	// CompactKeepTokens 是压缩后保留的最近 token 量（默认约 20k）。
	CompactKeepTokens int
}

// withDefaults 补齐可缺省的运行参数。
func (s *TurnSpec) withDefaults() {
	if s.LoopRepeatLimit <= 0 {
		s.LoopRepeatLimit = domain.LoopRepeatLimit
	}
	if s.LoopAbortLimit <= s.LoopRepeatLimit {
		s.LoopAbortLimit = domain.LoopAbortLimit
	}
	if s.ReserveTokens <= 0 {
		s.ReserveTokens = domain.ContextReserveTokens
	}
	if s.CompactKeepTokens <= 0 {
		s.CompactKeepTokens = domain.CompactKeepTokens
	}
	if s.ParallelTools <= 0 {
		s.ParallelTools = 1
	}
}

// Run 是全系统唯一的多轮循环入口（INV-1）。
//
// 状态机（02 §5.3）：
//
//	idle → preparing → streaming → tools → finishing → finished
//
// 返回终态原因；无论如何都恰好发出一次 TurnFinished（INV-9）。
// ctx 取消贯穿模型流、工具执行与等待中的交互（INV-8），取消后仍发 cancelled 终态。
func Run(ctx context.Context, deps Deps, spec TurnSpec) domain.TerminalReason {
	if err := deps.Validate(); err != nil {
		return TerminalError
	}
	r := newRun(ctx, deps, spec)
	return r.execute()
}

// run 是一次轮次的执行态。它不写回 spec，避免调用方复用结构体时串状态。
type run struct {
	ctx      context.Context
	deps     Deps
	spec     TurnSpec
	threadMu *sync.Mutex

	start    time.Time
	deadline time.Time
	pending  pendingDelta
	usage    usageAcc

	seqGuard   sync.Mutex
	finishedAt *time.Time

	loops *loopDetector
	// 幂等：同一 IdempotencyKey 重复提交不再开第二轮。
}

func newRun(ctx context.Context, deps Deps, spec TurnSpec) *run {
	spec.withDefaults()
	r := &run{ctx: ctx, deps: deps, spec: spec, loops: newLoopDetector(spec.LoopRepeatLimit, spec.LoopAbortLimit)}
	r.start = deps.Clock.Now()
	if spec.TurnTimeoutMS > 0 {
		r.deadline = r.start.Add(time.Duration(spec.TurnTimeoutMS) * time.Millisecond)
	}
	return r
}

// joinMissing 拼装缺失端口名。
func joinMissing(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// mustJSON 把载荷编码失败转成 panic 级的内部错误：事件载荷都是本包构造的字面量，
// 编码失败意味着代码写错，不该被当成业务错误吞掉。
func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{"marshalError":` + strconv.Quote(err.Error()) + `}`)
	}
	return raw
}
