package toolkit

// ToolManager 是工具管理的唯一入口（设计 4.5、开发计划 P5）：
// 注册、按 owner 摘除、可见性投影、turn 级快照与执行路由都从这里走，
// 上层（装配、宿主桥、引擎、Core RPC）不再各自拼一套。
//
// 它【不做】的事同样是契约：
//   - 不跑 tool.beforeCall / 策略 / 失败关闭——审批链只有 engine 一份；
//   - 不实现具体工具——执行仍交给插件的 Runner（本包只认 Invoker 接口）；
//   - 不做运行期加载——冻结后注册只记警告，要生效就重载插件（06 §6.5）。
//
// Catalog 是它背后的纯注册表（去重、冻结、保留名、Schema 校验），
// Manager 在其上加 owner 归属校验、可见性视图与一个单调版本号。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/adag/ada-go/internal/common/domain"
	"github.com/adag/ada-go/internal/common/ports"
)

// ToolManager 集中管理一份工具目录。
//
// 并发语义：读路径（View/Lookup/Snapshot/Invoke）只拿读锁；注册与摘除拿写锁并推进版本号。
// 版本号用于诊断与测试断言「装配换了一代」，不作为运行时缓存失效键。
type ToolManager struct {
	catalog *Catalog
	router  Invoker

	mu      sync.RWMutex
	version uint64
}

var (
	_ ports.ToolCatalog = (*ToolManager)(nil)
	_ Invoker           = (*ToolManager)(nil)
)

// NewToolManager 用一份目录与一个插件路由建管理器。
//
// router 是「工具名 → 贡献它的插件实例」的实现（生产实现是组合根的 pluginInvoker）：
// Manager 自己不认识 moejs、不认识插件实例，只认这个消费方接口。
func NewToolManager(catalog *Catalog, router Invoker) (*ToolManager, error) {
	if catalog == nil {
		return nil, fmt.Errorf("toolkit: ToolManager 需要 Catalog")
	}
	if router == nil {
		return nil, fmt.Errorf("toolkit: ToolManager 需要 Invoker（工具执行路由）")
	}
	return &ToolManager{catalog: catalog, router: router, version: 1}, nil
}

// Catalog 返回背后的注册表（装配层仍然需要它做冻结与状态统计）。
func (m *ToolManager) Catalog() *Catalog { return m.catalog }

// Register 登记一个描述符；owner 取自描述符自己的 PluginID。
//
// 这是宿主 staging 提交用的形状（host.ToolRegistry）：贡献者已经由清单盖章，
// 这里只校验它非空（pluginId 缺失的工具无法审计，直接拒绝）。
func (m *ToolManager) Register(d domain.Descriptor) error {
	return m.RegisterOwned(d.PluginID, d)
}

// RegisterOwned 由调用方显式盖章 owner 后登记。
//
// 声明里带了 pluginId 却与 owner 不一致的，一律拒绝：工具不能冒领别人的贡献
// （否则禁用 A 插件会把 B 的工具一起摘掉）。
func (m *ToolManager) RegisterOwned(owner string, d domain.Descriptor) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("toolkit: 工具 %q 缺少 owner（贡献者 id 由宿主盖章）", d.Name)
	}
	if d.PluginID != "" && d.PluginID != owner {
		return fmt.Errorf("toolkit: 工具 %q 声明的贡献者 %q 与注册方 %q 不一致", d.Name, d.PluginID, owner)
	}
	d.PluginID = owner

	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.catalog.Frozen()
	if err := m.catalog.Register(d); err != nil {
		return err
	}
	// 冻结后 Catalog 只记警告不生效：版本号也不能动，否则快照看起来「换了」其实没换。
	if !before {
		m.version++
	}
	return nil
}

// Remove 摘掉某插件贡献的全部工具，返回条数（禁用插件后能力必须消失，INV-11）。
func (m *ToolManager) Remove(pluginID string) int {
	return m.RemoveOwner(pluginID)
}

// RemoveOwner 是 Remove 的显式命名：按 owner 整体摘除，不留半截工具。
func (m *ToolManager) RemoveOwner(pluginID string) int {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.catalog.Remove(pluginID)
	if n > 0 {
		m.version++
	}
	return n
}

// List 实现 ports.ToolCatalog：返回全量快照（tool.list、诊断与引擎的 turn 快照源）。
//
// 引擎拿到这份全量后自己按 ToolVisibleInjected 过滤再发给模型；
// 这里不做过滤，是因为同一份快照还要支撑 exec_tool 的内层查找。
func (m *ToolManager) List(ctx context.Context) []domain.Descriptor { return m.catalog.List(ctx) }

// Lookup 实现 ports.ToolCatalog：按名字找描述符（含发现式工具）。
func (m *ToolManager) Lookup(ctx context.Context, name string) (domain.Descriptor, bool) {
	return m.catalog.Lookup(ctx, name)
}

// View 按可见性给出一份过滤后的快照；规则在 domain，本方法只做投影。
func (m *ToolManager) View(ctx context.Context, v domain.ToolVisibility) []domain.Descriptor {
	return domain.VisibleTools(m.List(ctx), v)
}

// Snapshot 是某一代目录的不可变快照。
//
// 一个 turn 取一次：模型声明、执行查找与循环检测都用同一份，
// 中途禁用插件不会让「已经发出去的声明」与「执行时的目录」错位。
type Snapshot struct {
	// Version 是装配代次（注册/摘除即推进）。
	Version uint64
	// Tools 是全量描述符（按名字排序）。
	Tools []Descriptor
	// Index 是按名字的索引，与 Tools 同源。
	Index map[string]Descriptor
}

// Descriptor 是快照里条目类型的别名，让调用方不必再 import domain。
type Descriptor = domain.Descriptor

// Visible 返回快照里属于该视图的条目。
func (s Snapshot) Visible(v domain.ToolVisibility) []Descriptor {
	return domain.VisibleTools(s.Tools, v)
}

// Lookup 在快照里按名字查找。
func (s Snapshot) Lookup(name string) (Descriptor, bool) {
	d, ok := s.Index[name]
	return d, ok
}

// Snapshot 取当前这一代的快照。
func (m *ToolManager) Snapshot(ctx context.Context) Snapshot {
	list := m.List(ctx)
	index := make(map[string]Descriptor, len(list))
	for _, d := range list {
		index[d.Name] = d
	}
	m.mu.RLock()
	version := m.version
	m.mu.RUnlock()
	return Snapshot{Version: version, Tools: list, Index: index}
}

// Version 返回当前代次。
func (m *ToolManager) Version() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.version
}

// Invoke 实现 toolkit.Invoker：执行路由的唯一入口。
//
// 归属以【目录】为准而不是以调用方传参为准：pluginID 与描述符不一致时拒绝，
// 这样即使某条上层路径传错了归属，也不会把调用投给另一个插件。
// 审批链不在这里——engine 已经在调用前跑完 beforeCall 与策略判定。
func (m *ToolManager) Invoke(ctx context.Context, pluginID, tool string, args json.RawMessage) (domain.Result, error) {
	d, ok := m.catalog.Lookup(ctx, tool)
	if !ok {
		return domain.Result{}, fmt.Errorf("toolkit: 工具 %q 不在目录里（插件可能已禁用）", tool)
	}
	if pluginID != "" && d.PluginID != pluginID {
		return domain.Result{}, fmt.Errorf("toolkit: 工具 %q 的贡献者是 %q，与调用归属 %q 不一致", tool, d.PluginID, pluginID)
	}
	return m.router.Invoke(ctx, d.PluginID, tool, args)
}

// Names 返回某视图下的工具名（稳定排序；诊断与测试用）。
func (m *ToolManager) Names(ctx context.Context, v domain.ToolVisibility) []string {
	list := m.View(ctx, v)
	out := make([]string, 0, len(list))
	for _, d := range list {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return out
}
