package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// 钩子点位别名：引擎直接用 ports 的常量，这里只是缩短调用点写法。
const (
	HookSessionStart         = ports.HookSessionStart
	HookSessionEnd           = ports.HookSessionEnd
	HookThreadCreated        = ports.HookThreadCreated
	HookUserMessage          = ports.HookUserMessage
	HookTurnBefore           = ports.HookTurnBefore
	HookTurnAfter            = ports.HookTurnAfter
	HookModelBeforeRequest   = ports.HookModelBeforeRequest
	HookModelAfterResponse   = ports.HookModelAfterResponse
	HookToolAfterCall        = ports.HookToolAfterCall
	HookCompactionBefore     = ports.HookCompactionBefore
	HookCompactionAfter      = ports.HookCompactionAfter
	HookInteractionRequested = ports.HookInteractionRequested
	HookPluginLoaded         = ports.HookPluginLoaded
	HookError                = ports.HookError
)

// shortHash 用于事件指纹与循环检测键（可截断，不参与安全判定）。
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// toolOutcome 是一批工具调用执行后的收敛结论。
type toolOutcome int

const (
	toolsContinue toolOutcome = iota
	toolsCancelled
	toolsErrored
	// toolsTerminated：调用了 TerminatesTurn=true 的工具（如 finish），本轮可收尾。
	toolsTerminated
)

// runTools 顺序执行一批工具调用（02 §5.3 第 6g 步、5.4 规则）。
//
// 顺序而不是并发是默认口径：只有整批都 ParallelSafe 且产品策略允许才并发；
// M1 先把语义做对（ParallelTools=1），并发留给 M5 按测量结果开放。
func (r *run) runTools(calls []domain.ToolCall, catalog []domain.Descriptor) toolOutcome {
	// 直呼映射只含注入式工具（规则在 domain.ToolVisibleInjected）。目录快照里
	// 发现式工具仍然存在（exec_tool 的内层查找要靠完整快照），但模型直呼其名
	// 必须 tool_not_found —— 它们的契约是「经 find_tool 发现、exec_tool 执行」。
	byName := make(map[string]domain.Descriptor, len(catalog))
	for _, d := range domain.VisibleTools(catalog, domain.ToolVisibleInjected) {
		byName[d.Name] = d
	}

	// receipted 记录本批已经落库回执的调用。任何提前返回（取消、钩子中止、循环熔断、
	// panic）都靠 defer 给剩下的调用补一条 interrupted 回执：模型上下文里 assistant
	// 声明的 toolCalls 必须逐个有回执，缺一条下一次请求就会被 OpenAI 兼容端点以
	// 「Tool result is missing for tool call …」拒掉，整条线程从此无法继续。
	receipted := make(map[string]bool, len(calls))
	record := func(call domain.ToolCall, res domain.Result) {
		r.appendToolResult(call, res)
		receipted[call.CallID] = true
	}
	deny := func(call domain.ToolCall, reason, detail string) {
		r.appendToolResult(call, domain.Result{
			Content: []domain.Part{{Type: domain.PartText, Text: "interrupted: " + detail}},
			Receipt: domain.Receipt{
				Status:    domain.ReceiptInterrupted,
				StartedAt: r.deps.Clock.Now(), FinishedAt: r.deps.Clock.Now(), Reason: reason,
			},
		})
		receipted[call.CallID] = true
	}
	defer func() {
		// 补写失败不能盖住原始 panic/错误：这一层只保证「尽力补齐」。
		defer func() { _ = recover() }()
		for _, call := range calls {
			if receipted[call.CallID] {
				continue
			}
			r.recordInterrupted(call, "轮次在工具执行期间中断，未产生结果")
		}
	}()

	for _, call := range calls {
		if r.ctx.Err() != nil {
			deny(call, "cancelled", "轮次已取消，未执行该工具")
			r.keepPartialMessage()
			return toolsCancelled
		}
		desc, ok := byName[call.Tool]
		if !ok {
			// 目录是本轮开始时的快照：中途禁用即 tool_not_found（INV-11）。
			res := domain.Result{
				Content: []domain.Part{{Type: domain.PartText, Text: "tool_not_found: 该工具本轮不可用"}},
				Receipt: domain.Receipt{Status: domain.ReceiptError, Reason: "tool_not_found"},
			}
			record(call, res)
			r.emitErrorCode(domain.CodeToolNotFound, "工具 "+call.Tool+" 不在本轮目录里")
			continue
		}

		// 工具执行：基座不做执行前询问（审批策略已移除），直接走统一 Runner。
		res := r.deps.Runner.Execute(r.ctx, domain.ToolCall{CallID: call.CallID, Tool: call.Tool, Args: call.Args})
		obsDesc, obsArgs := desc, call.Args
		record(call, res)
		r.hook(HookToolAfterCall, mustJSON(map[string]any{
			"callId": call.CallID, "tool": call.Tool, "status": string(res.Receipt.Status),
		}))

		switch r.loops.Observe(obsDesc, obsArgs, res) {
		case loopWarn:
			// 连续 3 次相同调用：先给错误回执提示换方法，轮次继续（02 §5.3）。
			r.appendLoopWarning(call, desc)
			r.emitErrorCode(domain.CodeLoopDetected, "检测到连续重复的相同工具调用")
		case loopAbort:
			// 连续 5 次仍未改道：按 error 结束，不是「步数上限」。
			r.emitErrorCode(domain.CodeLoopDetected, "连续重复调用未改道，按错误结束轮次")
			return toolsErrored
		}

		if desc.TerminatesTurn && res.Receipt.Status == domain.ReceiptOK {
			return toolsTerminated
		}
		if res.Receipt.Status == domain.ReceiptTimeout && r.ctx.Err() != nil {
			r.keepPartialMessage()
			return toolsCancelled
		}
	}
	r.keepPartialMessage()
	return toolsContinue
}

// recordInterrupted 给「本轮没来得及执行」的调用补回执。
//
// 场景是取消、钩子中止、循环熔断与 panic：这些路径不会再回到模型请求，
// 但历史里 assistant 的 toolCalls 仍然存在，缺回执的上下文会让后续轮次
// 永远被端点拒绝，因此这里必须把缺口补上（02 §5.4 interrupted）。
func (r *run) recordInterrupted(call domain.ToolCall, detail string) {
	now := r.deps.Clock.Now()
	res := domain.Result{
		Content: []domain.Part{{Type: domain.PartText, Text: "interrupted: " + detail}},
		Receipt: domain.Receipt{Status: domain.ReceiptInterrupted, StartedAt: now, FinishedAt: now, Reason: "interrupted"},
	}
	r.appendToolResult(call, res)
}

// appendLoopWarning 追加一条只给模型的提示性工具回执，打断「原地打转」。
func (r *run) appendLoopWarning(call domain.ToolCall, desc domain.Descriptor) {
	payload := mustJSON(map[string]any{
		"callId": call.CallID, "tool": call.Tool, "status": string(domain.ReceiptError),
		"content": []domain.Part{{Type: domain.PartText, Text: "loop_detected: 你已经用相同参数调用 " + desc.Name + " 多次且结果相同。换一种方法，或直接把结论告诉用户。"}},
		"receipt": domain.Receipt{Status: domain.ReceiptError, Reason: "loop_detected"},
	})
	if _, err := r.deps.Session.AppendEntry(r.ctx, ports.Entry{
		ThreadID: r.spec.ThreadID, Kind: domain.EntryMessage, Role: domain.RoleTool, Payload: payload,
	}); err != nil {
		r.emitErrorCode(domain.CodeStorageBusy, "写入循环提示失败: "+err.Error())
	}
}

// nonEmptyArgs 保证工具回执里的 input 永远是合法 JSON 对象。
func nonEmptyArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

// elapsed 返回本轮已用时（诊断与 budget_check 共用）。
func (r *run) elapsed() time.Duration { return r.deps.Clock.Now().Sub(r.start) }

// watchCtx 是取消贯穿的显式写法：所有阶段共用同一个 ctx（INV-8）。
func (r *run) watchCtx() context.Context { return r.ctx }
