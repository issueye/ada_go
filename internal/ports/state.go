package ports

import (
	"context"
	"encoding/json"
)

// StateKey 是插件状态的键。命名空间规则（05 §6.3 ToolContext：threadId 存在，
// “插件状态按线程命名空间隔离”）：调用方把 `t:<threadID>/<key>` 组合进 Key；
// 存储层主键是 (plugin_id, key)（04 §5.7 plugin_state 表），本端口不猜业务前缀。
type StateKey struct {
	PluginID string `json:"pluginId"`
	Key      string `json:"key"`
}

// StateStore 是插件 KV 与线程级状态端口（01 §5.2；存储落 04 §5.7 plugin_state 表）。
// 语义约束：
//   - value 只接受 JSON 可序列化值，编码后上限 1MB（05 §6.3 动作结果同规则，06 §6.4）；
//   - 多实例共享状态必须走本端口，模块级变量不保证跨实例一致（06 §6.4）；
//   - 写操作进入 Core 单写者队列（04 §5.7），读走 WAL 读连接。
//
// 生产实现：internal/adapters/sqlite（plugin_state 表，M1-B）。
// 测试替身：internal/testsupport 的内存 KV（INV-2）。
type StateStore interface {
	// Get 读取键值；found=false 表示不存在（对应 ctx.state.get 返回 null，05 §6.3）。
	Get(ctx context.Context, key StateKey) (value json.RawMessage, found bool, err error)
	// Set 写入键值（upsert）；value 必须已 JSON 编码。
	Set(ctx context.Context, key StateKey, value json.RawMessage) error
	// Delete 删除键；幂等。
	Delete(ctx context.Context, key StateKey) error
	// ListKeys 列举指定前缀的键（供插件状态页/审计，按 key 稳定排序）。
	ListKeys(ctx context.Context, pluginID, prefix string) ([]string, error)
}
