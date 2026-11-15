package host

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/adag/ada-go/internal/common/domain"
)

// Budgets 是各档预算（06 §6.4、09 §9.5）。零值字段由 withDefaults 补默认；
// 测试用小值覆盖「死循环超时」这类断言（不能拿 120s 真实预算跑单测）。
type Budgets struct {
	// Tool 是工具调用的兜底预算；描述符里的 timeoutMs 优先（02 §5.4）。
	Tool time.Duration
	// Hook 是钩子的软预算（默认 200ms）。
	Hook time.Duration
	// HookCap 是钩子预算上限（2s）：要得更长也会被夹到这里（06 §6.6）。
	HookCap time.Duration
	// Action 与 Route 是界面动作与动态路由的预算（默认 5s）。
	Action time.Duration
	Route  time.Duration
	// Factory 是入口工厂（含 Promise 结算）的预算。
	Factory time.Duration
	// ProviderConnect / ProviderIdle / ProviderTotal 是 provider 流的三档期限（03 §5.5）。
	ProviderConnect time.Duration
	ProviderIdle    time.Duration
	ProviderTotal   time.Duration
	// Queue 是调用队列的缓冲长度。
	Queue int
	// ResultBytes 是单次结果上限（1MB）；RouteBytes 是路由响应上限（64KB）。
	ResultBytes int
	RouteBytes  int
	// DeadlineGrace 是「比调用方 ctx 期限提前多少开火」，让 plugin_timeout 与
	// 上层的 timeout/interrupted 判定不要撞在同一毫秒上（02 §5.4 回执状态）。
	DeadlineGrace time.Duration
	// InterruptGrace 是投递中断之后等待任务回执的宽限期。
	InterruptGrace time.Duration
}

// withDefaults 补齐零值字段并夹住钩子上限。
func (b Budgets) withDefaults() Budgets {
	if b.Tool <= 0 {
		b.Tool = DefaultToolTimeout
	}
	if b.Hook <= 0 {
		b.Hook = DefaultHookTimeout
	}
	if b.HookCap <= 0 {
		b.HookCap = MaxHookTimeout
	}
	if b.Hook > b.HookCap {
		b.Hook = b.HookCap
	}
	if b.Action <= 0 {
		b.Action = DefaultActionTimeout
	}
	if b.Route <= 0 {
		b.Route = DefaultCallTimeout
	}
	if b.Factory <= 0 {
		b.Factory = DefaultCallTimeout
	}
	if b.ProviderConnect <= 0 {
		b.ProviderConnect = domain.ProviderConnectTimeout
	}
	if b.ProviderIdle <= 0 {
		b.ProviderIdle = domain.ProviderIdleTimeout
	}
	if b.ProviderTotal <= 0 {
		b.ProviderTotal = domain.ProviderTotalTimeout
	}
	if b.Queue <= 0 {
		b.Queue = 64
	}
	if b.ResultBytes <= 0 {
		b.ResultBytes = MaxResultBytes
	}
	if b.RouteBytes <= 0 {
		b.RouteBytes = domain.RouteResponseMaxBytes
	}
	if b.DeadlineGrace <= 0 {
		b.DeadlineGrace = 50 * time.Millisecond
	}
	if b.InterruptGrace <= 0 {
		b.InterruptGrace = 2 * time.Second
	}
	return b
}

// newBudgetFor 按档位建预算；provider 用「建连 → 空闲」两段。
//
// 调用方 ctx 带 deadline 时夹到「deadline - DeadlineGrace」，保证 plugin_timeout
// 而不是上层的 interrupted 先发生（02 §5.4 的回执状态由错误类型决定）。
func (p *Instance) newBudgetFor(c *call) *callBudget {
	b := p.budgets
	var soft, wall time.Duration
	switch c.kind {
	case callTool:
		soft = b.Tool
		if d, ok := p.descriptorOf(c.name); ok && d.Timeout > 0 {
			soft = d.Timeout
		}
	case callHook:
		soft = b.Hook
		if d, ok := p.hookTimeoutOf(c.name); ok && d > 0 {
			soft = d
		}
		if soft > b.HookCap {
			soft = b.HookCap
		}
	case callAction:
		soft = b.Action
		if d, ok := p.actionTimeoutOf(c.name); ok && d > 0 {
			soft = d
		}
	case callRoute:
		soft = b.Route
	case callRouteStream:
		// SSE 路由是长连接：脚本空闲预算用 provider 的空闲档，总时长共用 30 分钟墙钟。
		soft, wall = b.ProviderIdle, b.ProviderTotal
	case callProvider:
		soft, wall = b.ProviderConnect, b.ProviderTotal
	default:
		soft = b.Factory
	}
	if left, ok := deadlineOf(c.signal); ok {
		clamped := left - b.DeadlineGrace
		if clamped <= 0 {
			clamped = time.Millisecond
		}
		if soft > clamped {
			soft = clamped
		}
	}
	return newCallBudget(soft, wall)
}

// callBudget 是一次调用的脚本时间预算：宿主长调用在飞时暂停（06 §6.4）。
type callBudget struct {
	mu        sync.Mutex
	soft      time.Duration
	used      time.Duration
	segStart  time.Time
	running   bool
	inflight  int
	timer     *time.Timer
	wall      time.Duration
	wallTimer *time.Timer
	stopped   bool
	onFire    func(reason error)
}

func newCallBudget(soft, wall time.Duration) *callBudget {
	return &callBudget{soft: soft, wall: wall}
}

func (b *callBudget) begin(onFire func(reason error)) {
	b.mu.Lock()
	b.onFire = onFire
	b.used = 0
	b.segStart = time.Now()
	b.running = true
	b.armLocked()
	if b.wall > 0 {
		b.wallTimer = time.AfterFunc(b.wall, func() { b.fire(errInterruptTimeout) })
	}
	b.mu.Unlock()
}

func (b *callBudget) softLimit() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.soft
}

// armLocked 按「剩余脚本时间」重新武装计时器；宿主长调用在飞时不武装。
func (b *callBudget) armLocked() {
	if b.stopped || !b.running || b.inflight > 0 || b.onFire == nil {
		return
	}
	left := b.soft - b.used
	if left <= 0 {
		left = time.Millisecond
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(left, func() { b.fire(errInterruptTimeout) })
}

func (b *callBudget) fire(reason error) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	fn := b.onFire
	b.mu.Unlock()
	if fn != nil {
		fn(reason)
	}
}

// pause 进入宿主长调用：暂停脚本计时（期限由各自的桥负责）。
func (b *callBudget) pause() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.inflight++
	if b.running {
		b.used += time.Since(b.segStart)
		b.running = false
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.mu.Unlock()
}

// resume 结束宿主长调用：恢复计时。
func (b *callBudget) resume() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.inflight > 0 {
		b.inflight--
	}
	if b.inflight == 0 && !b.stopped {
		b.running = true
		b.segStart = time.Now()
		b.armLocked()
	}
	b.mu.Unlock()
}

// window 换档：provider 的首个增量之后把软预算换成空闲窗口。
func (b *callBudget) window(d time.Duration) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.soft = d
	b.used = 0
	b.segStart = time.Now()
	b.running = !b.stopped
	b.armLocked()
	b.mu.Unlock()
}

func (b *callBudget) stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.stopped = true
	b.running = false
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if b.wallTimer != nil {
		b.wallTimer.Stop()
		b.wallTimer = nil
	}
	b.mu.Unlock()
}

// hostAsync 把一次宿主长调用包装成脚本里的 Promise（06 §6.4 宿主异步桥）。
//
// fn 在别的 goroutine 上执行，只碰 Go 值；结果回到 loop 上结算。
// 期间暂停脚本预算计时（06 §6.4），并解除调用方 deadline（否则 200ms 的钩子预算
// 会误杀一次 300s 的交互）。
func (p *Instance) hostAsync(c *call, name string, fn func(ctx context.Context) (any, error)) moejs.Value {
	return p.hostAsyncTimeout(c, name, defaultHostCallTimeout, fn)
}

// hostAsyncTimeout 与 hostAsync 相同，但允许调用方给出更长的兜底期限
// （provider 的 SSE 流是 30 分钟级长任务，不能被 300s 的交互兜底误杀，03 §5.5）。
func (p *Instance) hostAsyncTimeout(c *call, name string, timeout time.Duration, fn func(ctx context.Context) (any, error)) moejs.Value {
	promise, settle := p.loop.NewPromise()
	ctx, release := p.hostContextFor(c, timeout)
	var once sync.Once
	releaseOnce := func() { once.Do(release) }
	c.trackCancel(releaseOnce)
	c.budget.pause()
	go func() {
		defer releaseOnce()
		v, err := fn(ctx)
		settle(func(rrt *moejs.Runtime) (moejs.Value, error) {
			if !c.cancelled.Load() {
				c.budget.resume()
			}
			if err != nil {
				// 业务错误（HTTP 状态、出网边界、取消）保持原样穿过 Promise，
				// 让 provider 桥能按错误码决定是否重试（03 §5.5）。
				var de *domain.Error
				if errors.As(err, &de) {
					return moejs.Undefined(), err
				}
				// HTTP 状态错误同样原样穿过；重试包装需要 Status/Retry-After。
				var he *HTTPError
				if errors.As(err, &he) {
					return moejs.Undefined(), err
				}
				return moejs.Undefined(), rrt.Realm().TypeError("host: %s 失败: %v", name, err)
			}
			return rrt.FromGo(v)
		})
	}()
	return promise
}

// hostContextFor 派生宿主长调用的 context：
//   - 保留「显式取消」（轮次取消必须贯穿到桥，INV-8）；
//   - 不传递调用方 deadline（脚本预算/钩子 200ms 不是宿主长调用的期限，06 §6.4）；
//   - 自带 timeout 兜底，避免桥实现忘记期限时永久挂住。
func (p *Instance) hostContextFor(c *call, timeout time.Duration) (context.Context, func()) {
	if timeout <= 0 {
		timeout = defaultHostCallTimeout
	}
	parent := context.Background()
	if c.signal != nil {
		parent = c.signal
	}
	base, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	stop := make(chan struct{})
	go func() {
		select {
		case <-parent.Done():
			if errors.Is(context.Cause(parent), context.DeadlineExceeded) {
				// 只是脚本预算/上层 deadline 到期：长调用由各自的期限负责，不因此取消。
				return
			}
			cancel()
		case <-stop:
		}
	}()
	return base, func() {
		close(stop)
		cancel()
	}
}
