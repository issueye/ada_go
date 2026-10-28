package ports

import (
	"context"

	"github.com/adag/ada-go/internal/domain"
)

// EventSink 是领域事件出口端口（01 §5.2）。
// 语义约束（08 §8.1、02 §5.3 INV-6/INV-9）：
//   - 线程内 Seq 由 Core 唯一写协程单调分配；工作区级事件用 WorkspaceSeq 独立计数；
//   - evt.run.status 的 terminal 每轮恰好一次（INV-9），错误路径也不能漏发/重发；
//   - 事件至少保留到对应 run 结束，总线保留最近 4096 条用于重连补发（08 §8.3）；
//   - Emit 不阻塞主循环：SSE 断开的客户端不影响轮次（07 §7.4）。
//
// 生产实现：internal/coreproto 的事件发射器（01 §5.2，M1-A/M2）。
// 测试替身：internal/testsupport 的事件记录器（development-plan M0.5；
// INV-6 事件序列 golden 测试靠它回放，02 §5.3）。
type EventSink interface {
	// Emit 投递一条领域事件；序号字段（Seq/WorkspaceSeq）由实现方在写协程内补齐后广播。
	Emit(ctx context.Context, evt domain.Event) error
}
