package testkit

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

// Retry 是基座 ModelClient 包装层的替身实现（03 §5.5「重试由基座负责，provider 只实现单次流」）。
//
// 冻结的规则，逐条对应 03 §5.5：
//   - 只对 429/5xx/网络错误重试：判定交给 Retryable（缺省 domain.IsRetryable）；
//   - 只在首个增量之前：已经向消费方交付过任意增量后绝不重试，避免重复内容；
//   - 指数退避 + 抖动、最多 3 次：MaxAttempts 默认 4（首试 + 3 次重试），
//     退避 BaseDelay<<k 再乘 [0.5,1.0) 的抖动，等待走 Clock 所以测试不睡真实时间；
//   - 尊重 Retry-After：03 §5.5 要求尊重该头，但 provider 插件面（ProviderContext.http）
//     没有把该头部透给基座的通道 —— 契约缺口，v1 先用退避近似，M1-E 前需冻结传递形状。
//
// 守的不变量：INV-9（错误路径也必须有且只有一个终态 —— 重试耗尽后把最后一次错误原样上抛，
// 引擎据此走 error，不会「重试成功又发一次错误」）。
type Retry struct {
	Inner ports.ModelClient
	// MaxAttempts 是最多建立流次数（含首次）；<=0 时取 4（1 次首试 + 3 次重试，03 §5.5）。
	MaxAttempts int
	// BaseDelay 是首次退避时长；<=0 时取 100ms。后续按 2 的幂放大。
	BaseDelay time.Duration
	// MaxDelay 是单次退避上限；<=0 时取 30s（测试里可以设成毫秒级）。
	MaxDelay time.Duration
	// Clock 非空时退避等待走假时钟（测试自己 Advance）。
	Clock ports.Clock
	// Retryable 判定错误是否可重试；nil 时用 domain.IsRetryable（配合 *domain.Error）。
	Retryable func(err error) bool

	mu       sync.Mutex
	attempts int
	delays   []time.Duration
}

// NewRetry 用默认预算包装一个 ModelClient。
func NewRetry(inner ports.ModelClient) *Retry { return &Retry{Inner: inner} }

// Attempts 返回累计建立流的次数（含首次）。
func (r *Retry) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// Delays 返回历次退避时长（断言「指数退避 + 抖动」的区间）。
func (r *Retry) Delays() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.delays...)
}

// maxAttempts 解析生效的首试+重试总数。
func (r *Retry) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return 4 // 03 §5.5：最多 3 次重试
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
	var de *domain.Error
	if errors.As(err, &de) {
		return domain.IsRetryable(de.Code)
	}
	// 非业务错误按「网络错误」一类处理：03 §5.5 把网络错误列为可重试。
	return true
}

// Stream 实现 ports.ModelClient：建立阶段可重试，首个增量交付后不再重试。
func (r *Retry) Stream(ctx context.Context, req ports.Request) (ports.Stream, error) {
	var lastErr error
	for attempt := 0; attempt < r.maxAttempts(); attempt++ {
		if attempt > 0 {
			d := r.backoff(attempt)
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
			return nil, err // 不可重试（或已被取消）立刻上抛
		}
	}
	return nil, lastErr
}

// backoff 是指数退避 + 抖动（抖动系数落在 [0.5,1)），并夹紧到 MaxDelay。
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

// sleep 等待退避；取消立刻返回 ctx.Err()。
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

// retryStream 是包装层交给引擎的流：只在本格尚未向消费方交付过任何增量时，
// 才允许重新建立一条流继续同一次轮次。
type retryStream struct {
	owner *Retry
	ctx   context.Context
	req   ports.Request

	mu       sync.Mutex
	cur      ports.Stream
	delivery int // 已向消费方交付的增量数
}

// Recv 取下一个增量。
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

// Close 释放当前流。
func (s *retryStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.Close()
}
