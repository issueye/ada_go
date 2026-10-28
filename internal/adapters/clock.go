// Package adapters 放基础设施适配器（时钟、id、路径 scope、密钥）。
//
// 依赖方向：只 import 标准库 + internal/domain + internal/ports + 已锁定的第三方库；
// 不得 import engine/coreapp/gateway/plugins（设计 3.3 依赖向下）。
// SQLite 适配器在子包 internal/adapters/sqlite，插件宿主在 internal/plugins/host。
package adapters

import (
	"time"

	"github.com/adag/ada-go/internal/ports"
)

// SystemClock 是 ports.Clock 的生产实现：固定 UTC，避免本地时区进入 created_at（04 §5.7）。
//
// 测试替身是 internal/conformance/testkit.FakeClock（INV-2：端口必须有替身）。
type SystemClock struct{}

// 编译期断言：生产实现必须满足端口。
var _ ports.Clock = SystemClock{}

// Now 返回当前 UTC 时刻。
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// After 返回 d 之后收到时刻的通道。
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
