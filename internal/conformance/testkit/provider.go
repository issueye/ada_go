package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// ErrNoFinish 是「EOF 但没有终止信号」的内部哨兵；Collect 把它翻译成
// domain.CodeProtocolError（03 §5.5：EOF 不等于完成）。
var ErrNoFinish = errors.New("testkit: 流在未给出显式 finish 的情况下结束")

// ErrStreamIdle 是空闲超时命中；默认预算 domain.ProviderIdleTimeout（90s，03 §5.5）。
var ErrStreamIdle = errors.New("testkit: 模型流空闲超时")

// ErrStreamClosed 表示对已关闭的流继续 Recv。
var ErrStreamClosed = errors.New("testkit: 流已关闭")

// callOwner 是一个 tool_call index 的归属（callId + 工具名）。
type callOwner struct {
	id   string
	tool string
}

// FakeProvider 是 ports.ModelClient 的脚本驱动替身。
//
// 守的不变量：
//   - INV-8（02 §5.3）：取消必须穿透模型阶段 —— ctx 取消后 Recv 立刻返回 ctx.Err()，
//     Idle 格也不会把测试挂住；
//   - INV-9：错误注入格（RecvError / OpenFailures）让「错误路径也恰好一个终态」可测；
//   - INV-12：50 轮脚本 = 51 次 Stream 请求，替身不会自作主张地「限量」；
//   - 03 §5.5：统一增量、显式终止信号、EOF ≠ 完成、空闲期限。
//
// 用法：NewFakeProvider(ScriptedModel)。每次 Stream 调用消耗一轮脚本，
// 请求原文记在 Requests() 里供断言（例如 INV-10：Messages 里没有 UI-only 内容）。
type FakeProvider struct {
	mu sync.Mutex

	model    *ScriptedModel
	seq      int
	requests []ports.Request
	streams  []*FakeStream

	// Clock 非空时，Idle 格用假时钟等待（测试自己 Advance）；
	// 为空时用真实计时器，但 ctx 取消总能立刻返回，所以不会真的等 90s。
	Clock ports.Clock
	// IdleTimeout > 0 时启用空闲判定：Idle(d) 且 d > IdleTimeout 直接失败；
	// 0 表示沿用 domain.ProviderIdleTimeout（90s，可经 ADAG_STREAM_IDLE 覆盖）。
	IdleTimeout time.Duration
	// OnStreamStart / OnStreamClose 观察流生命周期（取消贯穿断言用）。
	OnStreamStart func(req ports.Request)
	OnStreamClose func(threadHint string)
}

// NewFakeProvider 以脚本模型构造替身；model 为 nil 时用「单轮空文本 + finish」。
func NewFakeProvider(model *ScriptedModel) *FakeProvider {
	if model == nil {
		model = NewScriptedModel(TextTurn("ok"))
	}
	return &FakeProvider{model: model}
}

// Stream 实现 ports.ModelClient：建立阶段可按脚本注入失败（OpenFailures）。
func (p *FakeProvider) Stream(ctx context.Context, req ports.Request) (ports.Stream, error) {
	p.mu.Lock()
	idx := p.seq
	p.seq++
	p.requests = append(p.requests, req)
	openFail := p.model.ShouldFailOpen(idx)
	openErr := p.model.OpenError()
	steps := p.model.PopTurn(idx)
	startHook := p.OnStreamStart
	closeHook := p.OnStreamClose
	p.mu.Unlock()

	if openFail {
		return nil, openErr
	}
	idleBudget := p.IdleTimeout
	if idleBudget == 0 {
		idleBudget = domain.ProviderIdleTimeout
	}
	s := &FakeStream{
		ctx:         ctx,
		steps:       steps,
		clock:       p.Clock,
		idleTimeout: idleBudget,
		onClose: func() {
			if closeHook != nil {
				closeHook(req.Model.ModelID)
			}
		},
	}
	p.mu.Lock()
	p.streams = append(p.streams, s)
	p.mu.Unlock()
	if startHook != nil {
		startHook(req)
	}
	return s, nil
}

// Requests 返回已收到的请求快照（按发起顺序）。
func (p *FakeProvider) Requests() []ports.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ports.Request(nil), p.requests...)
}

// RequestCount 返回已发起的模型请求次数 —— 断言「轮次确实跑了 51 问」而不是被步数上限砍掉。
func (p *FakeProvider) RequestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

// Streams 返回已建立的流（供取消/关闭断言）。
func (p *FakeProvider) Streams() []*FakeStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*FakeStream(nil), p.streams...)
}

// OpenStreamCount 返回尚未 Close 的流数（INV-8：取消后必须全部释放）。
func (p *FakeProvider) OpenStreamCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, s := range p.streams {
		if !s.IsClosed() {
			n++
		}
	}
	return n
}

// Reset 回到初始状态（同一测试内重放脚本）。
func (p *FakeProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq = 0
	p.requests = nil
	p.streams = nil
}

// FakeStream 是一次模型响应的增量流（ports.Stream 替身）。
type FakeStream struct {
	ctx         context.Context
	clock       ports.Clock
	idleTimeout time.Duration
	onClose     func()

	mu       sync.Mutex
	steps    []ScriptStep
	i        int
	closed   bool
	received int
	sawFi    bool
}

// Recv 按脚本产出统一增量。
//
// 语义与 ports.Stream 对齐：
//   - kindEOF → io.EOF（EOF 但无终止信号，调用方必须判成 protocol_error，03 §5.5）；
//   - 脚本用尽同样返回 io.EOF；
//   - kindIdle → 在 ctx 取消与空闲期限之间等待，然后继续下一格；
//   - ctx 已取消 → 返回 ctx.Err()（INV-8）。
func (s *FakeStream) Recv() (ports.Delta, error) {
	if err := s.ctx.Err(); err != nil {
		return ports.Delta{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.closed {
			return ports.Delta{}, ErrStreamClosed
		}
		if s.i >= len(s.steps) {
			return ports.Delta{}, io.EOF
		}
		st := s.steps[s.i]
		s.i++

		switch st.Kind {
		case kindEOF:
			return ports.Delta{}, io.EOF
		case kindRecvError:
			err := st.Err
			if err == nil {
				err = domain.NewError(domain.CodeProviderError, "fake provider: 流中错误（脚本注入）")
			}
			return ports.Delta{}, err
		case kindIdle:
			if err := s.waitLocked(st.Delay); err != nil {
				return ports.Delta{}, err
			}
			continue // 静默不产生增量
		}

		s.received++
		if st.Kind == kindFinish {
			s.sawFi = true
		}
		return ports.Delta{
			Kind:             deltaKind(st.Kind),
			Text:             st.Text,
			Index:            st.Index,
			CallID:           st.CallID,
			ToolName:         st.Tool,
			ToolCallFragment: st.ToolCallFragment,
			Usage:            st.Usage,
			FinishReason:     st.FinishReason,
			StartMarker:      st.Marker,
		}, nil
	}
}

// waitLocked 等待一格静默；调用者已持有 mu。
func (s *FakeStream) waitLocked(d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if s.idleTimeout > 0 && d > s.idleTimeout {
		// 空闲期限命中：不真的等，直接按失败关闭（03 §5.5 的 90s 语义）。
		return fmt.Errorf("%w: 静默 %s 超过预算 %s", ErrStreamIdle, d, s.idleTimeout)
	}
	if s.clock != nil {
		select {
		case <-s.clock.After(d):
			return s.ctx.Err()
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return s.ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Close 释放流（幂等）。
func (s *FakeStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cb := s.onClose
	s.mu.Unlock()
	if cb != nil {
		cb()
	}
	return nil
}

// IsClosed 报告流是否已关闭。
func (s *FakeStream) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Received 返回已产出的增量数（Retry 包装层用它判定「首个增量之前」）。
func (s *FakeStream) Received() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received
}

// SawFinish 报告该流是否给出过显式终止信号（EOF 语义断言用）。
func (s *FakeStream) SawFinish() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sawFi
}

func deltaKind(k StepKind) ports.DeltaKind {
	switch k {
	case kindTextStart:
		return ports.DeltaTextStart
	case kindTextDelta:
		return ports.DeltaTextDelta
	case kindTextEnd:
		return ports.DeltaTextEnd
	case kindThinkingStart:
		return ports.DeltaThinkingStart
	case kindThinkingDelta:
		return ports.DeltaThinkingDelta
	case kindThinkingEnd:
		return ports.DeltaThinkingEnd
	case kindToolStart:
		return ports.DeltaToolCallStart
	case kindToolDelta:
		return ports.DeltaToolCallDelta
	case kindToolEnd:
		return ports.DeltaToolCallEnd
	case kindUsage:
		return ports.DeltaUsage
	case kindFinish:
		return ports.DeltaFinish
	default:
		return ports.DeltaKind(k)
	}
}

// Assembly 是一次模型流按 03 §5.5 规则组装后的结果。
//
// 它是「provider 归一化流 → 引擎可消费的 assistant 消息」这段契约的可执行表达
// （M1-A 的引擎把 Assembly 变成 domain.Message）：文本、思考、按 index 累积并
// 整体校验的工具调用、用量、显式终止信号。
type Assembly struct {
	Text          string
	Thinking      string
	Calls         []domain.ToolCall
	Usage         domain.Usage
	HasUsage      bool
	FinishSeen    bool
	FinishReason  string
	TerminalMark  string
	DeltaCount    int
	InvalidArgs   []string // 组装后不是合法 JSON 的 callId（应转成工具 error 回执）
	ProtocolError error    // EOF 无终止信号时非空（domain.CodeProtocolError）
	StreamErr     error    // 脚本注入的流中错误原样透出
}

// HasToolCalls 报告这次响应是否带工具调用（02 §5.3 步骤 f 的分叉条件）。
func (a Assembly) HasToolCalls() bool { return len(a.Calls) > 0 }

// Collect 按 03 §5.5 消费一整条流并组装：
//   - 没有 finish 的 EOF → 返回 protocol_error（EOF ≠ 完成）；
//   - tool_call 参数按 index 累积，流结束时整体 JSON 校验，失败进 InvalidArgs
//     ——由引擎转成该工具的 error 回执，而不是协议错误（03 §5.5、02 §5.4）；
//   - 流中错误原样返回（引擎据此走 error 终态，INV-9），已收到的部分保留在返回值里
//     （取消后保留部分消息，02 §5.3）。
func Collect(ctx context.Context, s ports.Stream) (Assembly, error) {
	var a Assembly
	if s == nil {
		return a, errors.New("testkit: Collect 需要非空流")
	}
	defer func() { _ = s.Close() }()

	frags := map[int]string{}
	owner := map[int]callOwner{}
	var order []int

	remember := func(idx int, o callOwner) {
		if _, ok := owner[idx]; !ok {
			order = append(order, idx)
			owner[idx] = o
			return
		}
		cur := owner[idx]
		if o.id != "" {
			cur.id = o.id
		}
		if o.tool != "" {
			cur.tool = o.tool
		}
		owner[idx] = cur
	}

	for {
		if err := ctx.Err(); err != nil {
			a.StreamErr = err
			finalizeCalls(&a, frags, owner, order)
			return a, err
		}
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			finalizeCalls(&a, frags, owner, order)
			if !a.FinishSeen {
				a.ProtocolError = domain.WrapError(
					domain.NewError(domain.CodeProtocolError, "模型流在没有终止信号时结束"), ErrNoFinish)
				return a, a.ProtocolError
			}
			return a, nil
		}
		if err != nil {
			finalizeCalls(&a, frags, owner, order)
			a.StreamErr = err
			return a, err
		}
		a.DeltaCount++
		switch d.Kind {
		case ports.DeltaTextStart, ports.DeltaTextDelta, ports.DeltaTextEnd:
			a.Text += d.Text
		case ports.DeltaThinkingStart, ports.DeltaThinkingDelta, ports.DeltaThinkingEnd:
			a.Thinking += d.Text
		case ports.DeltaToolCallStart:
			remember(d.Index, callOwner{id: d.CallID, tool: d.ToolName})
		case ports.DeltaToolCallDelta:
			remember(d.Index, callOwner{id: d.CallID, tool: d.ToolName})
			frags[d.Index] += d.ToolCallFragment
		case ports.DeltaToolCallEnd:
			remember(d.Index, callOwner{id: d.CallID, tool: d.ToolName})
		case ports.DeltaUsage:
			if d.Usage != nil {
				a.Usage = *d.Usage
				a.HasUsage = true
			}
		case ports.DeltaFinish:
			a.FinishSeen = true
			a.FinishReason = d.FinishReason
			a.TerminalMark = d.StartMarker
		default:
			a.ProtocolError = domain.NewError(domain.CodeProtocolError,
				fmt.Sprintf("未知的统一增量类型 %q", string(d.Kind)))
			finalizeCalls(&a, frags, owner, order)
			return a, a.ProtocolError
		}
	}
}

// finalizeCalls 把按 index 累积的片段变成 domain.ToolCall 并做整体 JSON 校验。
func finalizeCalls(a *Assembly, frags map[int]string, owner map[int]callOwner, order []int) {
	idxs := append([]int(nil), order...)
	sort.Ints(idxs)
	for _, idx := range idxs {
		o := owner[idx]
		raw := frags[idx]
		if raw == "" {
			raw = "{}"
		}
		if !json.Valid([]byte(raw)) {
			a.InvalidArgs = append(a.InvalidArgs, o.id)
		}
		a.Calls = append(a.Calls, domain.ToolCall{CallID: o.id, Tool: o.tool, Args: json.RawMessage(raw)})
	}
}

// Responder 把工具调用映射成回执（10 §11.3「相同工具 + 相同参数 + 相同回执」里
// 「相同回执」一侧由它保证：固定内容 ⇒ 回执可比较）。
type Responder func(call domain.ToolCall) domain.Result

// FixedResponder 返回内容恒定的成功回执；循环检测测试用它制造「相同回执」。
func FixedResponder(text string) Responder {
	return func(domain.ToolCall) domain.Result {
		return domain.Result{
			Content: []domain.Part{{Type: domain.PartText, Text: text}},
			Receipt: domain.Receipt{Status: domain.ReceiptOK},
		}
	}
}

// DeniedResponder 返回 denied 回执（INV-5：拒绝是工具结果，不是异常，也不终止轮次）。
func DeniedResponder(reason string) Responder {
	return func(domain.ToolCall) domain.Result {
		return domain.Result{
			Content: []domain.Part{{Type: domain.PartText, Text: "denied: " + reason}},
			Receipt: domain.Receipt{Status: domain.ReceiptDenied, Reason: reason},
		}
	}
}
