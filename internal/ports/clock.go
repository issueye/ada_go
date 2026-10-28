package ports

import "time"

// Clock 是时间与超时端口（01 §5.2）。取消与超时一律用 context，不另造 CancelToken（01 §5.2）。
//
// 生产实现：internal/adapters/clock（系统时钟，M1-A）。
// 测试替身：internal/testsupport 的假时钟——可推进、可断言超时行为，支撑
// 交互 timeout、钩子 200ms/2s、provider 空闲 90s 等失败方向的不变量测试（02 §5.3 INV-4、10 §11）。
type Clock interface {
	// Now 返回当前 UTC 时刻；一切 created_at/started_at/seq 附带时间都出自这里，
	// 保证测试可确定回放（04 §5.7）。
	Now() time.Time
	// After 返回在 d 之后收到的时刻通道（空闲 90s、交互 300s 等期限用它计时；
	// 轮次取消本身仍走 context，见 01 §5.2）。
	After(d time.Duration) <-chan time.Time
}
