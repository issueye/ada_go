// Package toolkit 是工具机制的唯一实现处：描述符注册表、参数校验、执行包装、
// 结构化回执、文件变更队列与检查点登记（设计 1.5.1「工具机制」）。
//
// 这里刻意「零具体工具」：fs/shell/git/todo/ask/read_skill 全部来自插件贡献（1.5.2）。
// 依赖方向：只 import 标准库 + internal/domain；不 import engine/ports 之外的内部层，
// 也不 import 任何插件宿主——工具怎么被执行由调用方注入的 Invoker 决定（M1-C 是 moejs 宿主）。
package toolkit

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/ports"
)

// namePattern 是工具名规则（05 §6.3：^[a-z][a-z0-9_]{1,63}$）。
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

// Catalog 是描述符注册表：端口的生产实现，也是工具目录的唯一真源（INV-3）。
//
// 装配期一次性 Register，随后 Freeze 变成运行期只读快照；
// 冻结后再 Register 只记警告不生效——要生效就重载插件（06 §6.5、5.4）。
type Catalog struct {
	mu          sync.RWMutex
	descriptors map[string]domain.Descriptor
	frozen      bool
	warnings    []string
}

var _ ports.ToolCatalog = (*Catalog)(nil)

// NewCatalog 创建空目录：空基座的工具目录长度必须是 0（INV-11）。
func NewCatalog() *Catalog {
	return &Catalog{descriptors: map[string]domain.Descriptor{}}
}

// Register 登记一个描述符；冻结后调用只记警告。
func (c *Catalog) Register(d domain.Descriptor) error {
	if err := validateDescriptor(d); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frozen {
		c.warnings = append(c.warnings, fmt.Sprintf("运行期注册工具 %q 被忽略：目录已冻结，重载插件才生效", d.Name))
		return nil
	}
	if existing, ok := c.descriptors[d.Name]; ok {
		// 同名覆盖是 pi 的已知缺陷（01 §2.1「必须避免」），这里直接拒绝。
		return fmt.Errorf("toolkit: 工具名 %q 已由插件 %q 贡献，拒绝重复注册", d.Name, existing.PluginID)
	}
	c.descriptors[d.Name] = d
	return nil
}

// Freeze 结束装配期，目录转为运行期只读。
func (c *Catalog) Freeze() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frozen = true
}

// Frozen 报告目录是否已冻结。
func (c *Catalog) Frozen() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.frozen
}

// Warnings 返回装配期之后的动态注册警告（页面与日志可见，不静默）。
func (c *Catalog) Warnings() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.warnings...)
}

// List 返回按名字排序的目录快照（模型请求与 tool.list 共用同一份，8.4）。
func (c *Catalog) List(context.Context) []domain.Descriptor {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]domain.Descriptor, 0, len(c.descriptors))
	for _, d := range c.descriptors {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup 按名字找描述符。
func (c *Catalog) Lookup(_ context.Context, name string) (domain.Descriptor, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.descriptors[name]
	return d, ok
}

// Remove 摘掉某插件贡献的全部工具：禁用插件后能力必须消失，基座不补位（INV-11）。
func (c *Catalog) Remove(pluginID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for name, d := range c.descriptors {
		if d.PluginID == pluginID {
			delete(c.descriptors, name)
			n++
		}
	}
	return n
}

// AccessesInUse 返回目录里出现过的外部效果类别（产品装配的策略门禁输入，5.4）。
func (c *Catalog) AccessesInUse() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[string]bool{}
	var out []string
	for _, d := range c.descriptors {
		a := string(d.Access)
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}

// ToolSchemas 生成发给模型的声明（03 §5.5 Request.Tools）。
func (c *Catalog) ToolSchemas(ctx context.Context) []ports.ToolSchema {
	list := c.List(ctx)
	out := make([]ports.ToolSchema, 0, len(list))
	for _, d := range list {
		out = append(out, ports.ToolSchema{Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	return out
}

// validateDescriptor 做装配期校验：名字、描述、参数 Schema、access、超时。
func validateDescriptor(d domain.Descriptor) error {
	if !namePattern.MatchString(d.Name) {
		return fmt.Errorf("toolkit: 工具名 %q 不满足 ^[a-z][a-z0-9_]{1,63}$", d.Name)
	}
	if strings.TrimSpace(d.PluginID) == "" {
		return fmt.Errorf("toolkit: 工具 %q 缺少贡献者 pluginId（回执与审计要能追溯到插件）", d.Name)
	}
	if strings.TrimSpace(d.Description) == "" {
		return fmt.Errorf("toolkit: 工具 %q 缺少 description（模型据此决定是否调用）", d.Name)
	}
	switch d.Access {
	case domain.AccessRead, domain.AccessWrite, domain.AccessExec, domain.AccessNetwork:
	default:
		return fmt.Errorf("toolkit: 工具 %q 的 access %q 非法（read|write|exec|network）", d.Name, d.Access)
	}
	if len(d.Parameters) == 0 {
		return fmt.Errorf("toolkit: 工具 %q 缺少 parameters JSON Schema", d.Name)
	}
	if _, err := compileSchema(d.Parameters); err != nil {
		return fmt.Errorf("toolkit: 工具 %q 的参数 Schema 非法: %w", d.Name, err)
	}
	if d.Timeout < 0 {
		return fmt.Errorf("toolkit: 工具 %q 的 timeout 不能为负", d.Name)
	}
	return nil
}

// 默认值与上限（09 §9.5、06 §6.4）。
const (
	// DefaultTimeout 是描述符未声明超时时的预算（工具默认 120s）。
	DefaultTimeout = 120 * time.Second
	// MaxResultBytes 是工具结果的可序列化上限（05 §6.3：1MB）。
	MaxResultBytes = 1 << 20
	// MaxOutputBytes / MaxOutputLines 是单条工具输出的截断阈值（02 §5.4：50KB / 2000 行）。
	MaxOutputBytes = 50 << 10
	MaxOutputLines = 2000
)

// timeoutOf 取描述符预算或默认 120s（06 §6.4：工具取 ToolDef.timeoutMs，不是 5s 脚本默认）。
func timeoutOf(d domain.Descriptor) time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return DefaultTimeout
}

// jsonSerializable 检查结果可序列化且不超过 1MB（05 §6.3）。
func jsonSerializable(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("toolkit: 结果不可 JSON 序列化: %w", err)
	}
	if len(raw) > MaxResultBytes {
		return nil, fmt.Errorf("toolkit: 结果 %d 字节超过上限 %d（05 §6.3）", len(raw), MaxResultBytes)
	}
	return raw, nil
}
