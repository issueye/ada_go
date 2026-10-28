package coreproto

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// InteractionStore 是待处理交互的持久化回调（04 §5.7 的 interactions 表）。
//
// 与 RunStore 同样是消费方定义的接口：M1-B 的 SQLite 适配器实现它，
// 为 nil 时交互只驻留内存（CLI/测试路径），重启即丢 —— 这一点必须在日志里说明。
type InteractionStore interface {
	SavePending(ctx context.Context, req ports.InteractionRequest) error
	Resolve(ctx context.Context, reqID string, res InteractionRecord) error
	ListPending(ctx context.Context, threadID string) ([]ports.InteractionRequest, error)
}

// InteractionRecord 是一次解析结果的最小账目。
type InteractionRecord struct {
	RequestID  string
	Status     domain.InteractionStatus
	Value      string
	ResolvedAt time.Time
}

// Broker 是 ports.InteractionBroker 的生产实现：通用交互请求桥（设计 1.5.1「安全」）。
//
// 基座只负责把请求送到界面、等待响应并按失败方向返回；它不知道这次询问叫「审批」
// （03 §5.6）。三条硬语义：
//   - 一次交互恰好解析一次；重复响应幂等返回首次结果（INV-4 + 03 §5.6）。
//   - 无 UI 立即解析为 unavailable；超时解析为 timeout；两者都必须被策略插件当作拒绝，
//     基座不替它放行。
//   - 同一线程最多一个待处理交互；第二个请求直接失败而不是排队。
type Broker struct {
	mu      sync.Mutex
	pending map[string]*pendingInteraction
	sink    ports.EventSink
	hooks   ports.HookRunner
	clock   ports.Clock
	ids     ports.IDGen
	store   InteractionStore
	hasUI   func(ctx context.Context, threadID string) bool
	timeout time.Duration
	// resolved 记录已解析交互的首次结果，用于幂等重放。
	resolved map[string]ports.InteractionResolution
}

type pendingInteraction struct {
	req   ports.InteractionRequest
	done  chan ports.InteractionResolution
	once  sync.Once
	res   ports.InteractionResolution
	carry bool // 已投递过终态，防止重复发事件
}

var _ ports.InteractionBroker = (*Broker)(nil)

// BrokerConfig 装配交互桥。
type BrokerConfig struct {
	Sink  ports.EventSink
	Hooks ports.HookRunner
	Clock ports.Clock
	IDs   ports.IDGen
	Store InteractionStore
	HasUI func(ctx context.Context, threadID string) bool
	// Timeout 为 0 时取 domain.DefaultInteractionTimeout（300s）。
	Timeout time.Duration
}

// NewBroker 创建交互桥。Sink 与 Clock 是必需的：发不出去或没期限的交互会永远挂着。
func NewBroker(cfg BrokerConfig) (*Broker, error) {
	if cfg.Sink == nil {
		return nil, fmt.Errorf("coreproto: 交互桥需要 EventSink")
	}
	if cfg.Clock == nil {
		return nil, fmt.Errorf("coreproto: 交互桥需要 Clock")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = domain.DefaultInteractionTimeout
	}
	hasUI := cfg.HasUI
	if hasUI == nil {
		hasUI = func(context.Context, string) bool { return false }
	}
	return &Broker{
		pending:  map[string]*pendingInteraction{},
		resolved: map[string]ports.InteractionResolution{},
		sink:     cfg.Sink, hooks: cfg.Hooks, clock: cfg.Clock, ids: cfg.IDs,
		store: cfg.Store, hasUI: hasUI, timeout: timeout,
	}, nil
}

// HasUI 实现端口：界面是否存在（ctx.ui.available 的基座侧真源）。
func (b *Broker) HasUI(ctx context.Context, threadID string) bool {
	return b.hasUI(ctx, threadID)
}

// Request 发起交互并等待解析。
func (b *Broker) Request(ctx context.Context, req ports.InteractionRequest) (ports.InteractionResolution, error) {
	if req.ThreadID == "" {
		return ports.InteractionResolution{}, domain.NewError(domain.CodeToolInvalidArgs, "交互请求缺少 threadId")
	}
	if req.ID == "" && b.ids != nil {
		id, err := b.ids.NewInteractionID(ctx)
		if err != nil {
			return ports.InteractionResolution{}, err
		}
		req.ID = id
	}
	if req.ID == "" {
		return ports.InteractionResolution{}, domain.NewError(domain.CodeToolInvalidArgs, "交互请求缺少 id")
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = b.clock.Now()
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = req.CreatedAt.Add(b.timeout)
	}
	if req.PluginID == "" {
		return ports.InteractionResolution{}, domain.NewError(domain.CodePluginDenied, "交互请求必须写明发起插件")
	}

	p := &pendingInteraction{req: req, done: make(chan ports.InteractionResolution, 1)}
	b.mu.Lock()
	if existing, ok := b.pending[req.ThreadID]; ok {
		b.mu.Unlock()
		// 同一线程最多一个待处理交互（03 §5.6）：直接拒绝第二个，不排队、不覆盖。
		return ports.InteractionResolution{}, domain.NewError(domain.CodeThreadBusy,
			"线程已有待处理交互 "+existing.req.ID)
	}
	b.pending[req.ThreadID] = p
	b.mu.Unlock()

	if b.store != nil {
		if err := b.store.SavePending(ctx, req); err != nil {
			b.forget(req.ThreadID)
			return ports.InteractionResolution{}, domain.WrapError(domain.NewError(domain.CodeStorageBusy, "交互落库失败"), err)
		}
	}

	// 没有界面：立即 unavailable，绝不解释为「允许」（INV-4）。
	if !b.hasUI(ctx, req.ThreadID) {
		return b.resolve(ctx, p, domain.InteractionUnavailable, "", "当前没有可用界面")
	}

	b.emit(domain.EvtInteractionRequested, protoLike(req))
	if b.hooks != nil {
		_, _ = b.hooks.Run(ctx, ports.HookEvent{
			Point: ports.HookInteractionRequested, ThreadID: req.ThreadID, RunID: req.RunID,
			Snapshot: mustMarshal(map[string]any{"interactionId": req.ID, "kind": string(req.Kind)}),
		})
	}

	wait := time.Until(req.ExpiresAt)
	if wait <= 0 {
		return b.resolve(ctx, p, domain.InteractionTimeout, "", "交互已过期")
	}
	timer := b.clock.After(wait)
	select {
	case res := <-p.done:
		return res, nil
	case <-timer:
		return b.resolve(ctx, p, domain.InteractionTimeout, "", "用户未在期限内响应")
	case <-ctx.Done():
		return b.resolve(ctx, p, domain.InteractionCancelled, "", "轮次已取消")
	}
}

// Respond 提交用户响应；幂等：重复调用返回首次结果（03 §5.6）。
func (b *Broker) Respond(ctx context.Context, threadID, requestID string, status domain.InteractionStatus, value string) (ports.InteractionResolution, error) {
	if status != domain.InteractionAccepted && status != domain.InteractionDenied && status != domain.InteractionCancelled {
		// 调用方不得伪造 timeout/unavailable（那两种只能由期限与无界面产生）。
		return ports.InteractionResolution{}, domain.NewError(domain.CodePluginDenied,
			"响应状态 "+string(status)+" 不允许由调用方提交")
	}
	b.mu.Lock()
	if first, ok := b.resolved[requestID]; ok {
		b.mu.Unlock()
		return first, nil
	}
	p, ok := b.pending[threadID]
	b.mu.Unlock()
	if !ok || p.req.ID != requestID {
		return ports.InteractionResolution{}, domain.NewError(domain.CodePluginDenied,
			"交互 "+requestID+" 不存在、已过期或不属于该线程")
	}
	if p.req.ExpiresAt.Before(b.clock.Now()) {
		return b.resolve(ctx, p, domain.InteractionTimeout, "", "响应到达前已过期")
	}
	return b.resolve(ctx, p, status, value, "")
}

// ListPending 返回线程的待处理交互（重启恢复、interaction.list）。
func (b *Broker) ListPending(ctx context.Context, threadID string) ([]ports.InteractionRequest, error) {
	b.mu.Lock()
	out := make([]ports.InteractionRequest, 0, 1)
	if p, ok := b.pending[threadID]; ok {
		out = append(out, p.req)
	}
	b.mu.Unlock()
	if b.store != nil {
		stored, err := b.store.ListPending(ctx, threadID)
		if err != nil {
			return out, err
		}
		seen := map[string]bool{out0id(out): true}
		for _, r := range stored {
			if !seen[r.ID] {
				out = append(out, r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// resolve 是唯一的解析出口：sync.Once 保证恰好一次（INV-4/INV-9 的交互侧）。
func (b *Broker) resolve(ctx context.Context, p *pendingInteraction, status domain.InteractionStatus, value, reason string) (ports.InteractionResolution, error) {
	res := ports.InteractionResolution{
		RequestID:  p.req.ID,
		Status:     status,
		Value:      value,
		ResolvedAt: b.clock.Now(),
	}
	var first bool
	p.once.Do(func() {
		first = true
		p.res = res
		b.mu.Lock()
		if cur, ok := b.pending[p.req.ThreadID]; ok && cur == p {
			delete(b.pending, p.req.ThreadID)
		}
		b.resolved[p.req.ID] = res
		b.mu.Unlock()
		if b.store != nil {
			_ = b.store.Resolve(ctx, p.req.ID, InteractionRecord{RequestID: p.req.ID, Status: status, Value: value, ResolvedAt: res.ResolvedAt})
		}
		b.emit(domain.EvtInteractionResolved, map[string]any{
			"threadId": p.req.ThreadID, "interactionId": p.req.ID,
			"result": string(status), "response": value, "reason": reason,
			"resolvedAt": res.ResolvedAt,
		})
		p.done <- res
	})
	if !first {
		return p.res, nil
	}
	if status.IsDenial() && reason != "" {
		return res, domain.NewError(domain.CodePluginDenied, "交互按失败方向处理: "+reason)
	}
	return res, nil
}

func (b *Broker) forget(threadID string) {
	b.mu.Lock()
	delete(b.pending, threadID)
	b.mu.Unlock()
}

func (b *Broker) emit(kind domain.EventKind, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = b.sink.Emit(context.Background(), domain.Event{Kind: kind, ThreadID: fieldOf(payload, "threadId"), Payload: raw, At: b.clock.Now()})
}

func protoLike(req ports.InteractionRequest) map[string]any {
	return map[string]any{
		"threadId": req.ThreadID, "interactionId": req.ID, "runId": req.RunID,
		"pluginId": req.PluginID, "kind": string(req.Kind), "title": req.Title,
		"body": req.Body, "options": req.Options, "expiresAt": req.ExpiresAt,
	}
}

func mustMarshal(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

func out0id(xs []ports.InteractionRequest) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0].ID
}

// fieldOf 从载荷里取 threadId（事件序号归属用）。
func fieldOf(payload any, key string) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
