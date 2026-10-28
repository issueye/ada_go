package ports

import "context"

// IDGen 是唯一 id 端口（01 §5.2）。所有标识符形状集中在这里，避免各处手拼：
//   - 线程 id：th_<workspaceHash>_<ulid>，Gateway 解析前缀即可路由到实例（01 §3.1/§4.2、04 §5.7）；
//   - entry/run/interaction id：ULID（04 §5.7），可排序、崩溃后可恢复；
//   - 调用 id：call_01J…（08 §8.4 回执示例），工具调用与 tool_call.* 增量共享；
//   - 检查点 id：chk_01J…（08 §8.4 receipt.checkpoint、09 §9.2）。
//
// 生产实现：internal/adapters/idgen（crypto/rand + ULID，M1-A；INV-2）。
// 测试替身：internal/testsupport 的确定性 id 源（golden 事件序列回放需要固定 id，02 §5.3 INV-6）。
type IDGen interface {
	// NewThreadID 生成 th_<workspaceHash>_<ulid>。
	NewThreadID(ctx context.Context, workspaceHash string) (string, error)
	// NewULID 生成裸 ULID（entry id 等，04 §5.7）。
	NewULID(ctx context.Context) (string, error)
	// NewCallID 生成工具调用 id（call_ 前缀）。
	NewCallID(ctx context.Context) (string, error)
	// NewRunID 生成轮次 id（runs.id，04 §5.7）。
	NewRunID(ctx context.Context) (string, error)
	// NewCheckpointID 生成检查点 id（chk_ 前缀；仅 Descriptor.Rollback=true 时）。
	NewCheckpointID(ctx context.Context) (string, error)
	// NewMessageID 生成投影消息 id（evt.message.delta 的 messageId，08 §8.2）。
	NewMessageID(ctx context.Context) (string, error)
	// NewInteractionID 生成交互请求 id（interactions.id，04 §5.7；
	// InteractionBroker 以它 + threadID 做幂等键，03 §5.6）。
	NewInteractionID(ctx context.Context) (string, error)
}
