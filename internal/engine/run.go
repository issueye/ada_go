package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// RunStore 记录轮次生命周期（04 §5.7 的 runs 表）。
//
// 与 ToolRunner 同样是消费方定义的接口：引擎只需要「开始了/结束了」两笔账，
// 具体落库由 coreapp 注入 SQLite 实现；为 nil 时只发事件不落库（CLI 与测试路径）。
type RunStore interface {
	StartRun(ctx context.Context, run RunRecord) error
	EndRun(ctx context.Context, run RunRecord) error
}

// RunRecord 是一次轮次的账目。
type RunRecord struct {
	ID        string
	ThreadID  string
	Status    string
	StartedAt time.Time
	EndedAt   time.Time
	ErrorCode string
	Usage     domain.Usage
}

// 线程互斥（INV-7：任何线程在任一时刻最多一个运行中轮次）。
//
// 锁用引用计数登记：轮次结束后清理条目，避免长跑进程里 map 单调增长（09 §9.5 资源限制）。
var (
	threadLocksMu sync.Mutex
	threadLocks   = map[string]*threadLock{}
)

type threadLock struct {
	mu   sync.Mutex
	refs int
}

// acquireThread 尝试占用某线程；已被占用时返回 false（调用方按 thread_busy 处理）。
func acquireThread(threadID string) (*threadLock, bool) {
	threadLocksMu.Lock()
	l := threadLocks[threadID]
	if l == nil {
		l = &threadLock{}
		threadLocks[threadID] = l
	}
	threadLocksMu.Unlock()

	if !l.mu.TryLock() {
		return nil, false
	}
	threadLocksMu.Lock()
	l.refs++
	threadLocksMu.Unlock()
	return l, true
}

// release 归还线程锁；最后一个持有者负责把条目摘掉。
func (l *threadLock) release(threadID string) {
	l.mu.Unlock()
	threadLocksMu.Lock()
	defer threadLocksMu.Unlock()
	if l.refs > 0 {
		l.refs--
	}
	if l.refs == 0 && threadLocks[threadID] == l {
		delete(threadLocks, threadID)
	}
}

// ThreadBusy 探测某线程是否有运行中的轮次；探测不改变占用状态。
func ThreadBusy(threadID string) bool {
	threadLocksMu.Lock()
	l, ok := threadLocks[threadID]
	threadLocksMu.Unlock()
	if !ok {
		return false
	}
	if l.mu.TryLock() {
		l.mu.Unlock()
		return false
	}
	return true
}

// ActiveThreadCount 报告运行中的轮次数量（诊断与 doctor 用）。
func ActiveThreadCount() int {
	threadLocksMu.Lock()
	defer threadLocksMu.Unlock()
	n := 0
	for _, l := range threadLocks {
		if l.refs > 0 {
			n++
		}
	}
	return n
}

// execute 是 02 §5.3 的八步流程。返回终态原因，且保证恰好发一次 TurnFinished（INV-9）。
func (r *run) execute() (terminal domain.TerminalReason) {
	if strings.TrimSpace(r.spec.ThreadID) == "" {
		r.failFast(domain.NewError(domain.CodeToolInvalidArgs, "轮次需要 threadId"))
		return TerminalError
	}
	lock, ok := acquireThread(r.spec.ThreadID)
	if !ok {
		// INV-7：同线程并发第二轮直接拒绝。
		r.emitErrorCode(domain.CodeThreadBusy, "该线程已有运行中的轮次")
		return TerminalError
	}
	defer lock.release(r.spec.ThreadID)

	// 终态收敛：panic、取消、错误都只走这一次（INV-9）。
	var finished bool
	terminate := func(reason domain.TerminalReason) {
		if finished {
			return
		}
		finished = true
		r.finish(reason)
	}

	defer func() {
		if p := recover(); p != nil {
			r.emitNotice(domain.NoticeError, fmt.Sprintf("内核异常已隔离: %v", p))
			terminate(TerminalError)
		}
	}()

	if r.ctx.Err() != nil {
		terminate(TerminalCancelled)
		return TerminalCancelled
	}

	// 1–2. 追加用户消息 entry、记录 run 开始、发 running。
	if err := r.begin(); err != nil {
		d, ok := domain.AsError(err)
		if ok {
			r.emitErrorCode(d.Code, d.Detail)
		} else {
			r.emitNotice(domain.NoticeError, err.Error())
		}
		terminate(TerminalError)
		return TerminalError
	}

	// 3–5. 目录快照与 turn.before。
	tools := r.deps.Tools.List(r.ctx) // 装配期定好，不在每轮扫盘（INV-3）
	if verdict, err := r.hook(HookTurnBefore, nil); err == nil && verdict.Aborted {
		r.emitNotice(domain.NoticeWarn, "轮次被控制面钩子中止: "+verdict.Reason)
		terminate(TerminalError)
		return TerminalError
	}

	reason := r.loop(tools)
	terminate(reason)
	return reason
}

// loop 是模型-工具主循环。没有步数上限（INV-12）：只有下面这些事件能让它结束。
func (r *run) loop(tools []domain.Descriptor) domain.TerminalReason {
	for {
		// 6a. 取消、上限、循环。
		if err := r.ctx.Err(); err != nil {
			r.keepPartialMessage()
			return TerminalCancelled
		}
		if reason := r.budgetCheck(); reason != "" {
			return reason
		}

		// 6b. 上下文水位：超限先压缩再请求。
		window, err := r.deps.Session.TraceFromHead(r.ctx, r.spec.ThreadID)
		if err != nil {
			r.emitErrorCode(domain.CodeStorageCorrupt, "读取上下文失败: "+err.Error())
			return TerminalError
		}
		summary, entries := decodeWindow(window)
		messages := r.toMessages(summary, entries)
		used := windowTokens(r.systemPrompt(), summary, entries)
		if needsCompaction(used, r.spec.Model) {
			view, reason, handled := r.compact(summary, entries)
			if handled {
				if reason != "" {
					return reason
				}
				if view != nil {
					summary, entries, messages = view.summary, view.entries, view.messages
				}
			}
		}

		// 6c. model.beforeRequest（第三方只能 note；builtin 可 replace 系统提示）。
		system := r.systemPrompt()
		if verdict, err := r.hook(HookModelBeforeRequest, mustJSON(map[string]any{
			"model": r.spec.Model.ModelID, "messageCount": len(messages),
		})); err == nil {
			if verdict.Aborted {
				return TerminalError
			}
			if len(verdict.Replacements) > 0 {
				if replaced, ok := pullSystemReplacement(verdict.Replacements); ok && replaced != "" {
					system = replaced
				}
			}
		}

		// 6d. 流式请求 + 6e 组装。
		assistant, calls, messageID, reason, ok := r.streamOnce(system, messages)
		if !ok {
			return reason
		}

		if len(calls) == 0 {
			// 自然收束（没有工具调用）。
			r.appendAssistant(assistant, messageID)
			r.afterTurn()
			return TerminalCompleted
		}

		r.appendAssistant(assistant, messageID)

		// 6g. 工具：顺序预检 → 执行 → 结构化回执 → 追加结果。
		result := r.runTools(calls, tools)
		switch result {
		case toolsCancelled:
			return TerminalCancelled
		case toolsErrored:
			return TerminalError
		case toolsTerminated:
			// 调用了 terminate=true 的工具（如 finish）。
			r.afterTurn()
			return TerminalCompleted
		}

		// 6h. steering 安全点：工具批次已落库、下一次模型请求之前排空中途消息。
		// 没有工具调用的自然收束不会到达这里，队列留到下一轮（不强行插话）。
		r.drainSteering()
	}
}

// begin 落用户消息与 run 开始，并广播 running（02 §5.3 第 2 步）。
func (r *run) begin() error {
	if len(r.spec.UserInput.Parts) == 0 && len(r.spec.UserInput.ToolCalls) == 0 {
		return domain.NewError(domain.CodeToolInvalidArgs, "用户消息为空")
	}
	raw, err := json.Marshal(r.spec.UserInput)
	if err != nil {
		return domain.WrapError(domain.NewError(domain.CodeToolInvalidArgs, "用户消息无法编码"), err)
	}
	if _, err := r.deps.Session.AppendEntry(r.ctx, ports.Entry{
		ThreadID: r.spec.ThreadID,
		Kind:     domain.EntryMessage,
		Role:     domain.RoleUser,
		Payload:  raw,
	}); err != nil {
		return err
	}
	r.loops.Reset() // 用户发新消息后计数重置（02 §5.3 循环检测）。

	if r.deps.RunStore != nil {
		_ = r.deps.RunStore.StartRun(r.ctx, RunRecord{ID: r.spec.RunID, ThreadID: r.spec.ThreadID,
			Status: string(domain.RunRunning), StartedAt: r.start})
	}
	r.emitRunStatus(domain.RunRunning, "")
	r.emitHook(HookUserMessage, raw)
	r.emitHook(HookThreadCreated, nil)
	return nil
}

// budgetCheck 只在显式配置了上限时生效（默认 0 = 不限制）。
func (r *run) budgetCheck() domain.TerminalReason {
	if !r.deadline.IsZero() && r.deps.Clock.Now().After(r.deadline) {
		r.emitErrorCode(domain.CodeBudgetExceeded, "达到显式配置的墙钟上限")
		return TerminalBudget
	}
	if r.spec.MaxTurnTokens > 0 && int(r.usage.Total()) > r.spec.MaxTurnTokens {
		r.emitErrorCode(domain.CodeBudgetExceeded, "达到显式配置的 token 上限")
		return TerminalBudget
	}
	return ""
}

// streamOnce 请求一次模型并组装 assistant 消息（03 §5.5 统一增量）。
func (r *run) streamOnce(system string, messages []domain.Message) (domain.Message, []domain.ToolCall, string, domain.TerminalReason, bool) {
	req := ports.Request{
		Model:    r.spec.Model,
		System:   system,
		Messages: messages,
		Tools:    toolSchemas(r.deps.Tools, r.ctx),
		Params:   r.params(),
	}
	stream, err := r.deps.Model.Stream(r.ctx, req)
	if err != nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return domain.Message{}, nil, "", TerminalCancelled, false
		}
		r.emitErrorCode(domain.CodeProviderError, "模型请求失败: "+err.Error())
		return domain.Message{}, nil, "", TerminalError, false
	}
	defer stream.Close()

	var (
		text      strings.Builder
		thinking  strings.Builder
		byIndex   = map[int]*callAcc{}
		order     []int
		gotFinish bool
		messageID = r.newMessageID()
	)

	for {
		if r.ctx.Err() != nil {
			r.flushPartial(text.String(), thinking.String())
			r.emitErrorCode(domain.CodeTurnAborted, "轮次已取消")
			return domain.Message{}, nil, messageID, TerminalCancelled, false
		}
		d, err := stream.Recv()
		if err != nil {
			if r.ctx.Err() != nil {
				// 取消贯穿（INV-8）：已到达内容保留，终态是 cancelled 而不是协议错误。
				r.flushPartial(text.String(), thinking.String())
				r.emitErrorCode(domain.CodeTurnAborted, "轮次在模型流中被取消")
				return domain.Message{}, nil, messageID, TerminalCancelled, false
			}
			if gotFinish {
				break
			}
			// EOF / 流错误且没有终止信号（03 §5.5：EOF 不等于完成）。
			r.emitErrorCode(domain.CodeProtocolError, "模型流异常结束: "+err.Error())
			r.flushPartial(text.String(), thinking.String())
			return domain.Message{}, nil, messageID, TerminalError, false
		}
		switch d.Kind {
		case ports.DeltaTextStart, ports.DeltaThinkingStart:
			// 起始标记：界面侧建条目，内容仍等 delta。
		case ports.DeltaTextDelta:
			text.WriteString(d.Text)
			r.emitDelta(messageID, "text", d.Text)
		case ports.DeltaThinkingDelta:
			thinking.WriteString(d.Text)
			r.emitDelta(messageID, "thinking", d.Text)
		case ports.DeltaToolCallStart:
			acc := &callAcc{CallID: d.CallID, Name: d.ToolName}
			byIndex[d.Index] = acc
			order = append(order, d.Index)
		case ports.DeltaToolCallDelta:
			acc, ok := byIndex[d.Index]
			if !ok {
				acc = &callAcc{CallID: d.CallID, Name: d.ToolName}
				byIndex[d.Index] = acc
				order = append(order, d.Index)
			}
			if acc.CallID == "" {
				acc.CallID = d.CallID
			}
			if acc.Name == "" {
				acc.Name = d.ToolName
			}
			acc.Args.WriteString(d.ToolCallFragment) // 参数片段按 index 累积
		case ports.DeltaUsage:
			if d.Usage != nil {
				r.usage.Add(*d.Usage)
				r.emitUsage()
			}
		case ports.DeltaFinish:
			gotFinish = true
		}
		if gotFinish {
			break
		}
	}
	if !gotFinish {
		r.emitErrorCode(domain.CodeProtocolError, "模型流提前结束且无终止信号")
		return domain.Message{}, nil, messageID, TerminalError, false
	}

	// 流结束后整体校验工具调用参数；失败作为工具错误回执（03 §5.5）。
	calls := make([]domain.ToolCall, 0, len(order))
	for _, idx := range order {
		acc := byIndex[idx]
		args := []byte(acc.Args.String())
		if len(strings.TrimSpace(string(args))) == 0 {
			args = []byte("{}")
		}
		if !json.Valid(args) {
			r.emitNotice(domain.NoticeWarn, fmt.Sprintf("工具 %s 的参数不是合法 JSON，已按错误回执处理", acc.Name))
			calls = append(calls, domain.ToolCall{CallID: acc.CallID, Tool: acc.Name, Args: json.RawMessage(`{"_malformedJSON":` + quote(acc.Args.String()) + `}`)})
			continue
		}
		calls = append(calls, domain.ToolCall{CallID: acc.CallID, Tool: acc.Name, Args: args})
	}
	if len(calls) == 0 && thinking.Len() == 0 && text.Len() == 0 {
		r.emitNotice(domain.NoticeWarn, "模型返回空响应且无工具调用，按完成收尾")
	}

	assistant := domain.Message{Role: domain.RoleAssistant, Parts: []domain.Part{}}
	if thinking.Len() > 0 {
		assistant.Parts = append(assistant.Parts, domain.Part{Type: domain.PartThinking, Text: thinking.String()})
	}
	if text.Len() > 0 {
		assistant.Parts = append(assistant.Parts, domain.Part{Type: domain.PartText, Text: text.String()})
	}
	assistant.ToolCalls = calls
	return assistant, calls, messageID, "", true
}

// callAcc 按 index 累积一次工具调用的参数片段。
type callAcc struct {
	CallID string
	Name   string
	Args   strings.Builder
}

// afterTurn 跑 turn.after 钩子、发用量并做后台预压缩（02 §5.3 第 7 步）。
func (r *run) afterTurn() {
	r.hook(HookTurnAfter, nil)
	if r.spec.MaxTurnTokens > 0 || r.spec.Model.ContextWindow > 0 {
		if window, err := r.deps.Session.TraceFromHead(r.ctx, r.spec.ThreadID); err == nil {
			summary, entries := decodeWindow(window)
			if needsCompaction(windowTokens(r.systemPrompt(), summary, entries), r.spec.Model) {
				// 后台预压缩：结果已落 compaction entry 供下一轮使用，本轮不重算。
				_, _, _ = r.compact(summary, entries)
			}
		}
	}
}

// finish 发且只发一次终态（INV-9）。
func (r *run) finish(reason domain.TerminalReason) {
	status := domain.RunFinishing
	r.emitRunStatus(status, "")
	r.emitRunStatus(domain.RunTerminal, reason)
	if r.deps.RunStore != nil {
		rec := RunRecord{ID: r.spec.RunID, ThreadID: r.spec.ThreadID,
			Status: string(reason), StartedAt: r.start, EndedAt: r.deps.Clock.Now(), Usage: r.usage.snapshot()}
		if reason == TerminalError {
			rec.ErrorCode = domain.CodeName(domain.CodeProviderError)
		}
		_ = r.deps.RunStore.EndRun(r.ctx, rec)
	}
	if reason == TerminalCancelled {
		r.emitHook(HookError, mustJSON(map[string]any{"reason": "cancelled"}))
	}
}

func (r *run) failFast(err *domain.Error) {
	r.emitErrorCode(err.Code, err.Detail)
	r.finish(TerminalError)
}
