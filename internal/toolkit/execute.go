package toolkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// Invoker 执行插件侧的工具实现。
//
// 这是「消费方定义的接口」，不是第 13 个端口（01 §5.2 冻结了 12 个端口，新增端口需先改设计）：
// M1-C 的 moejs 宿主与 M0.5 的测试替身各自结构化满足它，toolkit 不认识 moejs，也不认识具体插件。
type Invoker interface {
	Invoke(ctx context.Context, pluginID, tool string, args json.RawMessage) (domain.Result, error)
}

// Executor 是工具执行包装：校验 → 失败关闭判定 → 超时 → panic 收敛 → 截断 → 回执（02 §5.4）。
type Executor struct {
	catalog   *Catalog
	invoker   Invoker
	clock     ports.Clock
	changes   *ChangeQueue
	hasPolicy bool
	redact    func(string) string
	maxBytes  int
	maxLines  int
	ctxMeta   func(ctx context.Context) (threadID, runID string)
}

// ExecutorConfig 装配 Executor。
type ExecutorConfig struct {
	Catalog *Catalog
	Invoker Invoker
	Clock   ports.Clock
	// Changes 为空时自动创建（检查点与写串行仍可用）。
	Changes *ChangeQueue
	// HasPolicy 报告当前装配里是否存在 role=tool-policy 插件（5.4 的失败关闭依据）。
	HasPolicy bool
	// Redact 在写入回执与 details 前脱敏；nil 时原样保留。
	Redact func(string) string
	// MaxOutputBytes / MaxOutputLines 为 0 时取默认 50KB / 2000 行。
	MaxOutputBytes int
	MaxOutputLines int
	// CtxMeta 从执行 ctx 取检查点归属（threadId/runId，M5）。nil 表示调用方
	// 不做检查点持久化（回执仍有 chk_ id，但没有归属账目）。
	CtxMeta func(ctx context.Context) (threadID, runID string)
}

// NewExecutor 创建执行器。缺件直接失败，不做「没有时钟就用 time.Now」的隐式兜底（INV-2）。
func NewExecutor(cfg ExecutorConfig) (*Executor, error) {
	if cfg.Catalog == nil {
		return nil, fmt.Errorf("toolkit: Executor 需要 Catalog")
	}
	if cfg.Invoker == nil {
		return nil, fmt.Errorf("toolkit: Executor 需要 Invoker（M1-C 是 moejs 宿主）")
	}
	if cfg.Clock == nil {
		return nil, fmt.Errorf("toolkit: Executor 需要 Clock")
	}
	changes := cfg.Changes
	if changes == nil {
		changes = NewChangeQueue()
	}
	redact := cfg.Redact
	if redact == nil {
		redact = func(s string) string { return s }
	}
	maxBytes := cfg.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = MaxOutputBytes
	}
	maxLines := cfg.MaxOutputLines
	if maxLines <= 0 {
		maxLines = MaxOutputLines
	}
	return &Executor{
		catalog: cfg.Catalog, invoker: cfg.Invoker, clock: cfg.Clock, changes: changes,
		hasPolicy: cfg.HasPolicy, redact: redact, maxBytes: maxBytes, maxLines: maxLines,
		ctxMeta: cfg.CtxMeta,
	}, nil
}

// SetHasPolicy 让装配层在插件启用/禁用后更新策略门禁（禁用策略插件后写/执行立刻被拒，6.8）。
func (e *Executor) SetHasPolicy(has bool) { e.hasPolicy = has }

// Execute 跑一次工具调用，永远返回结构化结果（INV-5：拒绝与错误都是结果，不是异常）。
//
// 引擎负责 tool.beforeCall 钩子；这里只做「没有策略插件时的失败关闭」，
// 不含任何审批规则（1.5.2）。
func (e *Executor) Execute(ctx context.Context, call domain.ToolCall) domain.Result {
	started := e.clock.Now()
	d, ok := e.catalog.Lookup(ctx, call.Tool)
	if !ok {
		return e.receipt(call, started, domain.ReceiptError, "tool_not_found: 该工具当前不可用（插件可能已禁用）", nil)
	}

	s, err := compileSchema(d.Parameters)
	if err != nil {
		// 目录里的 Schema 必须已在校验过；走到这里说明装配有 bug，直接判错误回执。
		return e.receipt(call, started, domain.ReceiptError, "tool_invalid_args: 参数 Schema 无法解析: "+err.Error(), nil)
	}
	if err := s.Validate(call.Args); err != nil {
		return e.receipt(call, started, domain.ReceiptError, "tool_invalid_args: "+err.Error(), nil)
	}

	if !e.hasPolicy && needsPolicy(d.Access) {
		// 空策略的失败关闭：只读工具仍可用（02 §5.4、5.6）。
		return e.denied(call, started, "no policy",
			fmt.Sprintf("没有启用任何 role=tool-policy 插件，%s 类工具 %q 被基座拒绝", d.Access, d.Name))
	}

	var checkpoint string
	var cpMeta CheckpointMeta
	if d.Rollback {
		var threadID, runID string
		if e.ctxMeta != nil {
			threadID, runID = e.ctxMeta(ctx)
		}
		cpMeta = CheckpointMeta{ThreadID: threadID, RunID: runID, CallID: call.CallID, Tool: d.Name}
		checkpoint = e.changes.NewCheckpoint(call, d, cpMeta)
	}
	// 归属随 ctx 传给 Invoker：app 用它填 host.Context（ThreadID/RunID/CallID），
	// 宿主 fs 写入前回调 Checkpoint 把被覆盖文件并入持久化检查点（M5）。
	if cpMeta.CallID != "" {
		ctx = withMeta(ctx, cpMeta)
	}

	res, err := e.invokeWithBudget(ctx, d, call)
	finished := e.clock.Now()
	if err != nil {
		status := domain.ReceiptError
		switch {
		case ctx.Err() != nil:
			status = domain.ReceiptInterrupted
			res = e.timeoutResult(call, "轮次已取消，工具未继续执行")
		case isTimeout(err):
			status = domain.ReceiptTimeout
			res = e.timeoutResult(call, fmt.Sprintf("工具 %q 超过预算 %s", d.Name, timeoutOf(d)))
		default:
			res = e.timeoutResult(call, "工具执行失败: "+err.Error())
		}
		r := e.finalize(call, started, finished, status, res, "")
		r.Receipt.Checkpoint = checkpoint
		return r
	}

	status := res.Receipt.Status
	if status == "" {
		status = domain.ReceiptOK
	}
	r := e.finalize(call, started, finished, status, res, res.Receipt.Reason)
	r.Receipt.Checkpoint = checkpoint
	return r
}

// invokeWithBudget 在描述符预算下调用实现，并把 panic 收敛为错误（5.4：工具 panic 不终止轮次）。
func (e *Executor) invokeWithBudget(ctx context.Context, d domain.Descriptor, call domain.ToolCall) (res domain.Result, err error) {
	cctx, cancel := context.WithTimeout(ctx, timeoutOf(d))
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			res = domain.Result{}
			err = fmt.Errorf("toolkit: 工具 %q panic: %v", d.Name, p)
		}
	}()
	return e.invoker.Invoke(cctx, d.PluginID, d.Name, call.Args)
}

// finalize 统一处理截断、脱敏与回执字段。
func (e *Executor) finalize(call domain.ToolCall, started, finished time.Time, status domain.ReceiptStatus, in domain.Result, reason string) domain.Result {
	parts, truncated := e.truncate(in.Content)
	out := domain.Result{
		Content: parts,
		Details: sanitizeDetails(in.Details, e.redact),
		Receipt: domain.Receipt{
			Status:     status,
			StartedAt:  started.UTC(),
			FinishedAt: finished.UTC(),
			DurationMS: finished.Sub(started).Milliseconds(),
			Truncated:  truncated,
			Reason:     e.redact(reason),
		},
	}
	if out.Content == nil {
		out.Content = []domain.Part{}
	}
	// 结果必须可序列化且不超 1MB（05 §6.3）：装不下就降级成一条文本回执，而不是让上层崩。
	if _, err := jsonSerializable(out); err != nil {
		out.Details = nil
		out.Content = []domain.Part{{Type: domain.PartText, Text: e.redact("[结果过大已省略]")}}
		out.Receipt.Truncated = true
	}
	return out
}

func (e *Executor) receipt(call domain.ToolCall, started time.Time, status domain.ReceiptStatus, msg string, details map[string]any) domain.Result {
	return e.finalize(call, started, e.clock.Now(), status,
		domain.Result{Content: []domain.Part{{Type: domain.PartText, Text: msg}}, Details: details}, msg)
}

func (e *Executor) denied(call domain.ToolCall, started time.Time, reason, msg string) domain.Result {
	res := e.finalize(call, started, e.clock.Now(), domain.ReceiptDenied,
		domain.Result{Content: []domain.Part{{Type: domain.PartText, Text: reason + ": " + msg}}}, msg)
	res.Receipt.Reason = reason
	return res
}

func (e *Executor) timeoutResult(call domain.ToolCall, msg string) domain.Result {
	return domain.Result{Content: []domain.Part{{Type: domain.PartText, Text: msg}}}
}

// truncate 按 50KB / 2000 行截断文本 Part；Details 永不进模型（INV-10）。
func (e *Executor) truncate(parts []domain.Part) ([]domain.Part, bool) {
	if len(parts) == 0 {
		return parts, false
	}
	truncated := false
	out := make([]domain.Part, 0, len(parts))
	for _, p := range parts {
		if p.Type != domain.PartText {
			out = append(out, p)
			continue
		}
		text := p.Text
		if len(text) > e.maxBytes {
			text = text[:e.maxBytes]
			truncated = true
		}
		if lines := strings.Count(text, "\n") + 1; lines > e.maxLines {
			keep := strings.SplitN(text, "\n", e.maxLines+1)
			text = strings.Join(keep[:e.maxLines], "\n")
			truncated = true
		}
		if truncated {
			text += "\n…（输出已截断，完整内容走工具详情）"
		}
		p.Text = text
		out = append(out, p)
	}
	return out, truncated
}

func sanitizeDetails(details map[string]any, redact func(string) string) map[string]any {
	if len(details) == 0 {
		return nil
	}
	out := make(map[string]any, len(details))
	for k, v := range details {
		if s, ok := v.(string); ok {
			out[k] = redact(s)
			continue
		}
		out[k] = v
	}
	return out
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), context.DeadlineExceeded.Error())
}

// needsPolicy 报告该外部效果在没有 role=tool-policy 插件时是否必须失败关闭（02 §5.4）。
func needsPolicy(a domain.Access) bool { return a != domain.AccessRead }
