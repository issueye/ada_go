package testkit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// 交互桩的哨兵错误。协议没有为「过期/伪造的 interaction.respond」冻结应用码
// （08 §8.5 的 21 码里没有对应项），因此这里用可判定的哨兵而不是硬凑一个码。
var (
	// ErrUnknownInteraction 表示 requestID 不存在（伪造）。
	ErrUnknownInteraction = errors.New("testkit: 未知的交互请求")
	// ErrNotPending 表示请求已解析或已过期（不得改写状态）。
	ErrNotPending = errors.New("testkit: 交互请求不再是 pending")
	// ErrForbiddenStatus 表示调用方试图伪造 timeout/unavailable（03 §5.6：只能由实现方写入）。
	ErrForbiddenStatus = errors.New("testkit: 调用方不得伪造 timeout/unavailable 状态")
	// ErrThreadMismatch 表示 threadID 与 requestID 不匹配（03 §5.6 要求同时匹配）。
	ErrThreadMismatch = errors.New("testkit: threadId 与 requestId 不匹配")
)

// OutcomeKind 是一次交互的结局类型（10 §11.3「交互」三行 + INV-4 的全部失败方向）。
type OutcomeKind string

const (
	// OutcomeAccept 用户放行（accepted）。
	OutcomeAccept OutcomeKind = "accepted"
	// OutcomeDeny 用户明确拒绝（denied）。
	OutcomeDeny OutcomeKind = "denied"
	// OutcomeCancel 用户或轮次取消（cancelled，03 §5.6 取消段）。
	OutcomeCancel OutcomeKind = "cancelled"
	// OutcomeTimeout 期限到、无人响应（timeout）—— 必须被当作拒绝（INV-4）。
	OutcomeTimeout OutcomeKind = "timeout"
	// OutcomeUnavailable 无 UI / 断线（unavailable）—— 必须被当作拒绝（INV-4）。
	OutcomeUnavailable OutcomeKind = "unavailable"
	// OutcomeWait 保持 pending，等 Respond（用于幂等与并发响应测试）。
	OutcomeWait OutcomeKind = "wait"
)

// Outcome 是脚本化结局：状态 + 可选 value（confirm 的 "true"/"false"、select 的选项、input 文本）。
type Outcome struct {
	Kind  OutcomeKind
	Value string
}

// Accepted / Denied / Cancelled / Timeout / Unavailable / Waiting 是结局构造简写。
func Accepted(value string) Outcome { return Outcome{Kind: OutcomeAccept, Value: value} }
func Denied() Outcome               { return Outcome{Kind: OutcomeDeny} }
func Cancelled() Outcome            { return Outcome{Kind: OutcomeCancel} }
func Timeout() Outcome              { return Outcome{Kind: OutcomeTimeout} }
func Unavailable() Outcome          { return Outcome{Kind: OutcomeUnavailable} }
func Waiting() Outcome              { return Outcome{Kind: OutcomeWait} }

// FakeInteractions 是 ports.InteractionBroker 的替身。
//
// 守的不变量：
//   - INV-4（02 §5.3）：失败方向默认关闭。无 UI 立刻解析为 unavailable、
//     过 ExpiresAt 解析为 timeout，两者都不放行；调用方可用
//     domain.InteractionStatus.IsDenial() 统一判定；
//   - 03 §5.6：一次交互恰好解析一次（ExactlyOnce 断言）；Respond 必须同时匹配
//     threadID 与 requestID、只接受 pending 且未过期的请求、重复响应幂等返回首次结果、
//     过期/伪造返回错误且不改变状态；timeout/unavailable 只能由实现方写入。
type FakeInteractions struct {
	mu sync.Mutex

	clock       ports.Clock
	uiAvailable bool
	watchExpiry bool
	queue       []Outcome

	requests     []ports.InteractionRequest
	pending      map[string]ports.InteractionRequest
	resolved     map[string]ports.InteractionResolution
	resolveCount map[string]int
	respondTry   map[string]int
	waiters      map[string]chan struct{}
}

// InteractionOption 是构造选项。
type InteractionOption func(*FakeInteractions)

// WithInteractionClock 指定时钟（timeout 判定与 ResolvedAt 都取它）。
func WithInteractionClock(c ports.Clock) InteractionOption {
	return func(f *FakeInteractions) { f.clock = c }
}

// WithoutUI 模拟 CLI/非交互模式或 SSE 断线：Request 立刻解析为 unavailable（INV-4）。
func WithoutUI() InteractionOption {
	return func(f *FakeInteractions) { f.uiAvailable = false }
}

// WithOutcomes 按顺序编排结局；队列用尽后新请求保持 pending 等待 Respond。
func WithOutcomes(outcomes ...Outcome) InteractionOption {
	return func(f *FakeInteractions) { f.queue = append(f.queue, outcomes...) }
}

// WithExpiryWatch 为每个等待中的交互挂一个期限看门狗（走 Clock）：
// 时钟推进过 ExpiresAt 后自动解析为 timeout，不需要调用方再发一次请求。
// 这是 03 §5.6「超时解析为 timeout」的忠实表达；恰好一次的守卫保证
// 看门狗与 Respond 竞争时先落定的那个赢。
func WithExpiryWatch() InteractionOption {
	return func(f *FakeInteractions) { f.watchExpiry = true }
}

// NewFakeInteractions 构造交互桩（默认有 UI、结局队列为空即等待）。
func NewFakeInteractions(opts ...InteractionOption) *FakeInteractions {
	f := &FakeInteractions{
		uiAvailable:  true,
		clock:        NewFakeClock(),
		pending:      make(map[string]ports.InteractionRequest),
		resolved:     make(map[string]ports.InteractionResolution),
		resolveCount: make(map[string]int),
		respondTry:   make(map[string]int),
		waiters:      make(map[string]chan struct{}),
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Queue 追加结局（测试中途改脚本用）。
func (f *FakeInteractions) Queue(outcomes ...Outcome) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, outcomes...)
}

// SetUIAvailable 切换「有没有界面」；false 时后续 Request 一律 unavailable。
func (f *FakeInteractions) SetUIAvailable(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uiAvailable = ok
}

// Clock 返回所用时钟（推进 timeout 用）。
func (f *FakeInteractions) Clock() ports.Clock {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

// Request 实现 ports.InteractionBroker：发起并等待恰好一次解析。
//
// 失败关闭的顺序（03 §5.6）：无 UI → unavailable；已过期限 → timeout；
// 脚本结局 → 对应状态；没有结局 → 等待 Respond 或 ctx 取消（cancelled）。
func (f *FakeInteractions) Request(ctx context.Context, req ports.InteractionRequest) (ports.InteractionResolution, error) {
	if req.ID == "" {
		return ports.InteractionResolution{}, errors.New("testkit: 交互请求需要 id（幂等键的一部分）")
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	ch := make(chan struct{})
	f.waiters[req.ID] = ch
	f.pending[req.ID] = req
	f.mu.Unlock()

	if res, done := f.resolveFirst(ctx, req); done {
		return res, nil
	}
	if f.expiryWatchEnabled() && !req.ExpiresAt.IsZero() {
		go f.armExpiry(req.ID, req.ExpiresAt)
	}

	select {
	case <-ctx.Done():
		return f.resolve(req.ID, domain.InteractionCancelled, ""), nil
	case <-ch:
		return f.result(req.ID)
	}
}

// expiryWatchEnabled 读取看门狗开关（加锁，便于测试里并发调用）。
func (f *FakeInteractions) expiryWatchEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watchExpiry
}

// armExpiry 在期限到点时把交互解析为 timeout（若尚未解析，INV-4）。
func (f *FakeInteractions) armExpiry(id string, expiresAt time.Time) {
	f.mu.Lock()
	clock := f.clock
	now := FrozenTime
	if clock != nil {
		now = clock.Now()
	}
	f.mu.Unlock()
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		f.resolve(id, domain.InteractionTimeout, "")
		return
	}
	if clock == nil {
		select {
		case <-time.After(remaining):
			f.resolve(id, domain.InteractionTimeout, "")
		case <-time.After(domain.DefaultInteractionTimeout * 2):
		}
		return
	}
	select {
	case <-clock.After(remaining):
		f.resolve(id, domain.InteractionTimeout, "")
	}
}

// resolveFirst 处理能立即定案的情形。
func (f *FakeInteractions) resolveFirst(ctx context.Context, req ports.InteractionRequest) (ports.InteractionResolution, bool) {
	f.mu.Lock()
	var (
		out     Outcome
		hasOut  bool
		clock   = f.clock
		hasUI   = f.uiAvailable
		expires = req.ExpiresAt
	)
	if len(f.queue) > 0 {
		out = f.queue[0]
		f.queue = f.queue[1:]
		hasOut = true
	}
	f.mu.Unlock()

	if !hasUI {
		return f.resolve(req.ID, domain.InteractionUnavailable, ""), true // INV-4
	}
	now := FrozenTime
	if clock != nil {
		now = clock.Now()
	}
	if !expires.IsZero() && !now.Before(expires) {
		return f.resolve(req.ID, domain.InteractionTimeout, ""), true // INV-4
	}
	if !hasOut || out.Kind == OutcomeWait {
		return ports.InteractionResolution{}, false
	}
	status := domain.InteractionStatus(out.Kind)
	if ctx.Err() != nil && status != domain.InteractionCancelled {
		status = domain.InteractionCancelled
	}
	return f.resolve(req.ID, status, out.Value), true
}

// Respond 实现 ports.InteractionBroker（幂等、双键匹配、不得伪造失败态）。
func (f *FakeInteractions) Respond(_ context.Context, threadID, requestID string, status domain.InteractionStatus, value string) (ports.InteractionResolution, error) {
	f.mu.Lock()
	f.respondTry[requestID]++
	switch status {
	case domain.InteractionTimeout, domain.InteractionUnavailable:
		f.mu.Unlock()
		return ports.InteractionResolution{}, fmt.Errorf("%w: 调用方给了 %q", ErrForbiddenStatus, status)
	case domain.InteractionAccepted, domain.InteractionDenied, domain.InteractionCancelled:
	default:
		f.mu.Unlock()
		return ports.InteractionResolution{}, fmt.Errorf("testkit: Respond 不接受状态 %q", status)
	}
	req, isPending := f.pending[requestID]
	first, alreadyResolved := f.resolved[requestID]
	f.mu.Unlock()

	if alreadyResolved {
		// 一次交互恰好解析一次（03 §5.6）：
		//   - 由用户响应落定的（accepted/denied/cancelled）→ 幂等返回首次结果；
		//   - 由失败方向落定的（timeout/unavailable）→ 返回错误且不改变状态，
		//     迟到的点击不能把拒绝翻成放行（客户端无法伪造已响应，10 §11.3）。
		if first.Status == domain.InteractionTimeout || first.Status == domain.InteractionUnavailable {
			return ports.InteractionResolution{},
				fmt.Errorf("%w: %q 已按 %s 解析，迟到的响应不改写状态", ErrNotPending, requestID, first.Status)
		}
		return first, nil
	}
	if !isPending {
		return ports.InteractionResolution{}, fmt.Errorf("%w: %q", ErrUnknownInteraction, requestID)
	}
	if threadID != "" && req.ThreadID != "" && threadID != req.ThreadID {
		return ports.InteractionResolution{}, fmt.Errorf("%w: %q vs %q", ErrThreadMismatch, threadID, req.ThreadID)
	}
	now := FrozenTime
	if c := f.Clock(); c != nil {
		now = c.Now()
	}
	if !req.ExpiresAt.IsZero() && !now.Before(req.ExpiresAt) {
		// 过期响应：返回错误且不改变状态（03 §5.6）；真正的 timeout 由期限路径写入。
		return ports.InteractionResolution{}, fmt.Errorf("%w: %q 已过期", ErrNotPending, requestID)
	}
	return f.resolve(requestID, status, value), nil
}

// resolve 落定一次解析（恰好一次的守卫在这里）。
func (f *FakeInteractions) resolve(requestID string, status domain.InteractionStatus, value string) ports.InteractionResolution {
	f.mu.Lock()
	defer f.mu.Unlock()
	if first, ok := f.resolved[requestID]; ok {
		return first // 幂等：永不二次改写
	}
	now := FrozenTime
	if f.clock != nil {
		now = f.clock.Now()
	}
	res := ports.InteractionResolution{RequestID: requestID, Status: status, Value: value, ResolvedAt: now}
	f.resolved[requestID] = res
	f.resolveCount[requestID]++
	delete(f.pending, requestID)
	if ch, ok := f.waiters[requestID]; ok {
		close(ch)
		delete(f.waiters, requestID)
	}
	return res
}

func (f *FakeInteractions) result(requestID string) (ports.InteractionResolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.resolved[requestID]
	if !ok {
		return ports.InteractionResolution{}, fmt.Errorf("%w: %q", ErrNotPending, requestID)
	}
	return res, nil
}

// ListPending 实现 ports.InteractionBroker（重启恢复/interaction.list 的替身）。
func (f *FakeInteractions) ListPending(_ context.Context, threadID string) ([]ports.InteractionRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ports.InteractionRequest
	for _, req := range f.requests {
		if _, ok := f.pending[req.ID]; !ok {
			continue
		}
		if threadID != "" && req.ThreadID != threadID {
			continue
		}
		out = append(out, req)
	}
	return out, nil
}

// HasUI 实现 ports.InteractionBroker（ctx.ui.available 的基座侧真源）。
func (f *FakeInteractions) HasUI(_ context.Context, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uiAvailable
}

// ExactlyOnce 断言「一次交互恰好解析一次」：解析次数必须为 1。
func (f *FakeInteractions) ExactlyOnce(requestID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.resolveCount[requestID]
	switch {
	case n == 0:
		return fmt.Errorf("testkit: 交互 %s 从未被解析（等待 0 次也不允许，必须恰好一次）", requestID)
	case n > 1:
		return fmt.Errorf("testkit: 交互 %s 被解析了 %d 次（03 §5.6 要求恰好一次）", requestID, n)
	}
	return nil
}

// Resolution 返回某请求的解析结果。
func (f *FakeInteractions) Resolution(requestID string) (ports.InteractionResolution, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.resolved[requestID]
	return r, ok
}

// RequestCount 返回收到的交互请求数。
func (f *FakeInteractions) RequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// RespondAttempts 返回对某请求的 Respond 调用次数（幂等断言用）。
func (f *FakeInteractions) RespondAttempts(requestID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.respondTry[requestID]
}

// DeniedOrWorse 报告某次解析是否必须被策略插件当作拒绝（INV-4 的判定入口）。
func (f *FakeInteractions) DeniedOrWorse(requestID string) bool {
	r, ok := f.Resolution(requestID)
	if !ok {
		return true // 没解析成功就按失败关闭
	}
	return r.Status.IsDenial()
}

// InteractionRequestAt 造一条交互请求：默认期限 domain.DefaultInteractionTimeout（300s）。
func InteractionRequest(id, threadID, pluginID string, kind domain.InteractionKind, title, body string) ports.InteractionRequest {
	now := FrozenTime
	return ports.InteractionRequest{
		ID:        id,
		ThreadID:  threadID,
		PluginID:  pluginID,
		Kind:      kind,
		Title:     title,
		Body:      body,
		CreatedAt: now,
		ExpiresAt: now.Add(domain.DefaultInteractionTimeout),
	}
}
