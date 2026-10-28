package provider

// 基座重试包装（03 §5.5）：provider 只实现单次流，重试统一在这里做。
//
// 规则：
//   - 只对 429/5xx/网络错误重试（HTTPError.Retryable 或 domain.IsRetryable）；
//   - 只在首个增量之前；已经交付过内容的流绝不重放；
//   - 指数退避 + 抖动，最多 3 次重试（首试 + 3）；
//   - 尊重 Retry-After（*host.HTTPError 携带）。

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// Retry 是 ModelClient 的重试包装。
type Retry struct {
	Inner ports.ModelClient
	// MaxAttempts 为 0 时取 4（首试 + 3 次重试）。
	MaxAttempts int
	// BaseDelay 为 0 时取 100ms；MaxDelay 为 0 时取 30s。
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Clock     ports.Clock
	// Retryable 可覆盖默认判定（测试或特殊 provider 用）。
	Retryable func(error) bool

	mu       sync.Mutex
	attempts int
	delays   []time.Duration
}

var _ ports.ModelClient = (*Retry)(nil)

// NewRetry 用默认预算包装一个 ModelClient。
func NewRetry(inner ports.ModelClient) *Retry { return &Retry{Inner: inner} }

// Attempts 返回累计建立流的次数（含首试）。
func (r *Retry) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// Delays 返回历次退避时长。
func (r *Retry) Delays() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.delays...)
}

func (r *Retry) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return 4
}

func (r *Retry) baseDelay() time.Duration {
	if r.BaseDelay > 0 {
		return r.BaseDelay
	}
	return 100 * time.Millisecond
}

func (r *Retry) retryable(err error) bool {
	if r.Retryable != nil {
		return r.Retryable(err)
	}
	var mark interface{ Retryable() bool }
	if errors.As(err, &mark) {
		return mark.Retryable()
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return domain.IsRetryable(de.Code)
	}
	// 非业务错误按网络错误处理（03 §5.5）。
	return true
}

// Stream 实现端口：建连失败可重试，交付首个增量后不再重试。
func (r *Retry) Stream(ctx context.Context, req ports.Request) (ports.Stream, error) {
	if r.Inner == nil {
		return nil, domain.NewError(domain.CodeCoreUnavailable, "重试包装缺少 ModelClient")
	}
	var lastErr error
	for attempt := 0; attempt < r.maxAttempts(); attempt++ {
		if attempt > 0 {
			d := retryDelay(lastErr, r.backoff(attempt))
			r.mu.Lock()
			r.delays = append(r.delays, d)
			r.mu.Unlock()
			if err := r.sleep(ctx, d); err != nil {
				return nil, err
			}
		}
		r.mu.Lock()
		r.attempts++
		r.mu.Unlock()

		s, err := r.Inner.Stream(ctx, req)
		if err == nil {
			return &retryStream{owner: r, ctx: ctx, req: req, cur: s}, nil
		}
		lastErr = err
		if ctx.Err() != nil || !r.retryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (r *Retry) backoff(attempt int) time.Duration {
	base := r.baseDelay()
	ceiling := r.MaxDelay
	if ceiling <= 0 {
		ceiling = 30 * time.Second
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d > ceiling {
			d = ceiling
			break
		}
	}
	out := time.Duration(float64(d) * (0.5 + rand.Float64()*0.5))
	if out > ceiling {
		return ceiling
	}
	return out
}

func (r *Retry) sleep(ctx context.Context, d time.Duration) error {
	if r.Clock != nil {
		select {
		case <-r.Clock.After(d):
			return ctx.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryDelay 让服务端的 Retry-After 覆盖本地退避（仍夹在 MaxDelay 内）。
func retryDelay(err error, fallback time.Duration) time.Duration {
	var after interface{ RetryAfterDuration() time.Duration }
	if errors.As(err, &after) {
		if d := after.RetryAfterDuration(); d > 0 {
			return d
		}
	}
	return fallback
}

// retryStream 是对外流：只在尚未交付任何增量时允许重建。
type retryStream struct {
	owner *Retry
	ctx   context.Context
	req   ports.Request

	mu       sync.Mutex
	cur      ports.Stream
	delivery int
}

func (s *retryStream) Recv() (ports.Delta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for attempt := 0; ; attempt++ {
		d, err := s.cur.Recv()
		if err == nil {
			s.delivery++
			return d, nil
		}
		if !errors.Is(err, io.EOF) && s.delivery == 0 && attempt+1 < s.owner.maxAttempts() &&
			s.ctx.Err() == nil && s.owner.retryable(err) {
			_ = s.cur.Close()
			next, nerr := s.owner.Inner.Stream(s.ctx, s.req)
			if nerr != nil {
				return ports.Delta{}, nerr
			}
			s.owner.mu.Lock()
			s.owner.attempts++
			s.owner.mu.Unlock()
			s.cur = next
			continue
		}
		return ports.Delta{}, err
	}
}

func (s *retryStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.Close()
}
