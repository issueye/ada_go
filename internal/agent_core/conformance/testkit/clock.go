package testkit

import (
	"sync"
	"time"
)

// FrozenTime 是替身使用的固定起点（2026-10-28T00:00:00Z，与设计文档版本日一致）。
// 一切 golden 事件序列与回执时间戳都以它为基准，保证可确定回放（04 §5.7）。
var FrozenTime = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

// FakeClock 是 ports.Clock 的测试替身：可推进、可等待，不睡真实时间。
//
// 守的不变量：
//   - INV-4（02 §5.3）：交互 300s 超时、钩子 200ms/2s、provider 空闲 90s 这些
//     「失败方向」只能靠可控时钟在毫秒级测出来；
//   - INV-12：轮次没有步数上限，墙钟/token 上限默认 0 表示不限制——想验证
//     「显式配置的上限才会触发 budget_exceeded」，必须能把时间推过去而不真的等。
//
// After 的语义与 time.After 一致：一次性通道，在 Advance 越过期限后收到那一刻。
// 通道带 1 缓冲，因此即使没有接收者也不会漏掉已触发的定时器（测试里常见
// 「先推进、后接收」的写法）。
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*fakeTimer
}

type fakeTimer struct {
	at    time.Time
	seq   int
	ch    chan time.Time
	fired bool
}

// NewFakeClock 以 FrozenTime 为起点。
func NewFakeClock() *FakeClock { return NewFakeClockAt(FrozenTime) }

// NewFakeClockAt 以给定时刻为起点（nil 安全的构造别名）。
func NewFakeClockAt(t time.Time) *FakeClock {
	return &FakeClock{now: t.UTC()}
}

// Now 返回当前（虚拟）UTC 时刻。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After 返回在 d 之后（虚拟）收到的时刻通道。
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.seq++
	tm := &fakeTimer{at: c.now.Add(d), seq: c.seq, ch: ch}
	c.timers = append(c.timers, tm)
	// 非正期限立即触发（与 time.After 的行为差异刻意选成「立即可读」，
	// 免得测试里出现 0 期限时死等）。
	if d <= 0 {
		fireLocked(tm, c.now)
	}
	return ch
}

// Advance 把虚拟时间往前推 d，并按期限先后触发定时器。
func (c *FakeClock) Advance(d time.Duration) {
	c.AdvanceTo(c.Now().Add(d))
}

// AdvanceTo 推进到指定时刻；等同时刻按创建顺序触发。
func (c *FakeClock) AdvanceTo(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	target := at.UTC()
	if target.Before(c.now) {
		return // 时钟不倒走
	}
	for {
		earliest := -1
		for i, tm := range c.timers {
			if tm.fired || tm.at.After(target) {
				continue
			}
			if earliest < 0 || tm.at.Before(c.timers[earliest].at) ||
				(tm.at.Equal(c.timers[earliest].at) && tm.seq < c.timers[earliest].seq) {
				earliest = i
			}
		}
		if earliest < 0 {
			break
		}
		tm := c.timers[earliest]
		c.now = tm.at
		fireLocked(tm, tm.at)
		c.timers = append(c.timers[:earliest], c.timers[earliest+1:]...)
	}
	c.now = target
}

// Sleep 阻塞直到虚拟时间越过 d：调用方（通常是测试的另一个 goroutine）
// 需要负责 Advance。为免误用死锁，这里带一个真实的看门狗。
func (c *FakeClock) Sleep(d time.Duration) bool {
	ch := c.After(d)
	select {
	case <-ch:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// Pending 返回尚未触发的定时器数量；测试用它确认替身已经挂上了等待。
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// WaitPending 自旋等待出现 n 个未触发定时器（真实时间上限 2s，避免测试挂死）。
func (c *FakeClock) WaitPending(n int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.Pending() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return c.Pending() >= n
}

func fireLocked(tm *fakeTimer, at time.Time) {
	if tm.fired {
		return
	}
	tm.fired = true
	select {
	case tm.ch <- at:
	default:
	}
}
