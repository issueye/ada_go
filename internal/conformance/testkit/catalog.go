package testkit

import (
	"context"
	"sync"

	"github.com/adag/ada-go/internal/domain"
)

// EmptyCatalog 是 ports.ToolCatalog 的「零插件空基座」替身。
//
// 守的不变量：
//   - INV-11（02 §5.3/§5.4）：基座无业务兜底 —— 空基座的目录长度必须是 0，
//     Lookup 必须一律 false（对应 08 §8.5 的 3001 tool_not_found），基座不补位；
//   - INV-3：目录由描述符注册表派生，本替身模拟「注册表为空」的那一侧。
type EmptyCatalog struct{}

// NewEmptyCatalog 构造空目录。
func NewEmptyCatalog() *EmptyCatalog { return &EmptyCatalog{} }

// List 恒为空（02 §5.4「空基座的工具目录长度为 0」）。
func (EmptyCatalog) List(context.Context) []domain.Descriptor { return nil }

// Lookup 恒为 false。
func (EmptyCatalog) Lookup(context.Context, string) (domain.Descriptor, bool) {
	return domain.Descriptor{}, false
}

// StaticCatalog 是 ports.ToolCatalog 的可注入替身：
// 测试用它在装配期「冻结」一份目录，验证禁用某个贡献后能力确实消失（INV-11）。
type StaticCatalog struct {
	mu   sync.RWMutex
	desc []domain.Descriptor
}

// NewStaticCatalog 用给定描述符构造目录（按注册序保存，List 不做二次排序）。
func NewStaticCatalog(descs ...domain.Descriptor) *StaticCatalog {
	return &StaticCatalog{desc: append([]domain.Descriptor(nil), descs...)}
}

// List 返回当前目录快照。
func (c *StaticCatalog) List(context.Context) []domain.Descriptor {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]domain.Descriptor(nil), c.desc...)
}

// Lookup 按名称查描述符。
func (c *StaticCatalog) Lookup(_ context.Context, name string) (domain.Descriptor, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, d := range c.desc {
		if d.Name == name {
			return d, true
		}
	}
	return domain.Descriptor{}, false
}

// Disable 按插件 id 移除贡献（「禁用某个贡献插件后，它登记的工具必须从目录里消失、
// 基座不补位」的机器化表达，INV-11）。返回被移除的条数。
func (c *StaticCatalog) Disable(pluginID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var kept []domain.Descriptor
	removed := 0
	for _, d := range c.desc {
		if d.PluginID == pluginID {
			removed++
			continue
		}
		kept = append(kept, d)
	}
	c.desc = kept
	return removed
}

// Descriptor 造一个最小合法的工具描述符（名字规则 ^[a-z][a-z0-9_]{1,63}$，05 §6.3）。
func Descriptor(name, pluginID string, access domain.Access) domain.Descriptor {
	return domain.Descriptor{
		Name:        name,
		Description: "契约测试用的 " + name,
		Parameters:  []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
		Access:      access,
		Timeout:     domain.DefaultToolTimeout,
		PluginID:    pluginID,
	}
}
