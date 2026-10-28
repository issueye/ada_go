// Package app 是组合根：唯一同时知道「产品声明」与「嵌入的官方插件包」的地方
// （设计 5.1、1.5.5、11 §13 的 app/）。
//
// internal/ 不得 import official/，也不得在非测试源码里出现官方插件 id；因此
// 产品选择 × 插件包 × 宿主装配这三件事只能在这里汇合。装配结果是一份运行期目录：
// 工具目录、钩子链、各插件的宿主实例，以及 coreapp.Runtime 的门禁判定。
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/toolkit"
)

// Deps 是装配期注入的宿主依赖。
type Deps struct {
	// Context 是给每个插件调用的宿主上下文（Workspace/Signal/Log/State/UI/HTTP/Secret）。
	// PluginID 与 Settings 由装配器按插件覆盖，调用方不用填。
	Context host.Context
	// HostEnv 是宿主的进程环境（清单 backend.env 白名单与它求交，06 §6.2）。
	HostEnv map[string]string
	// Secrets 报告某个密钥名是否可用；nil 表示「清单声明的都能取到」。
	Secrets func(name string) bool
	// Settings 是按插件 id 的用户设置（[plugins.settings.<id>]，13 §附录 B）。
	Settings map[string]map[string]any
	// Scripts 为空时用嵌入的官方插件；测试可以注入替身。
	Scripts []host.Script
}

// Assembly 是一次装配的结果。
type Assembly struct {
	Product *coreapp.Product
	Runtime *coreapp.Runtime
	// Plugins 按产品声明顺序排列。
	Plugins []*host.Instance
	// Tools 与 Hooks 是运行期目录，引擎直接消费。
	Tools *toolkit.Catalog
	Hooks *plugins.HookChain
}

// Load 从产品声明文件装配（官方插件来自嵌入包）。
func Load(ctx context.Context, productPath string, deps Deps) (*Assembly, error) {
	p, err := coreapp.LoadProduct(productPath)
	if err != nil {
		return nil, err
	}
	scripts := deps.Scripts
	if scripts == nil {
		// 由调用方注入官方包，避免 app 依赖具体打包方式（cmd 侧做 embed）。
		return nil, fmt.Errorf("app: 没有插件包可装配（Deps.Scripts 为空；官方包由调用方注入）")
	}
	return Assemble(ctx, p, scripts, deps)
}

// Assemble 把产品声明与插件集合结算成运行期目录（06 §6.5 的两阶段在 M3 落地；
// 这里先做「逐插件加载 → 原子提交到目录 → 门禁判定」）。
//
// 缺失插件是装配错误而不是静默跳过：产品声明就是「启用这些插件」的承诺，
// 少一个就等于承诺没兑现（1.5.5 禁用即能力消失的反向）。
func Assemble(ctx context.Context, p *coreapp.Product, scripts []host.Script, deps Deps) (*Assembly, error) {
	if p == nil {
		return nil, errors.New("app: Assemble 需要非空产品声明")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	byID := make(map[string]host.Script, len(scripts))
	for _, s := range scripts {
		if s.Manifest == nil {
			return nil, errors.New("app: 插件缺少清单")
		}
		byID[s.Manifest.ID] = s
	}

	picked := make([]host.Script, 0, len(p.Plugins))
	var missing []string
	for _, id := range p.Plugins {
		s, ok := byID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		picked = append(picked, s)
	}
	if len(missing) > 0 {
		have := make([]string, 0, len(byID))
		for id := range byID {
			have = append(have, id)
		}
		sort.Strings(have)
		return nil, fmt.Errorf("app: 产品 %s 声明的插件没有随包提供：%s（可用：%s）",
			p.ID, strings.Join(missing, ", "), strings.Join(have, ", "))
	}

	tools := toolkit.NewCatalog()
	hooks := plugins.NewHookChain()
	asm := &Assembly{Product: p, Tools: tools, Hooks: hooks}

	for _, script := range picked {
		caps, err := host.NewCapabilitySet(script.Manifest, deps.Context.Workspace, deps.HostEnv, deps.Secrets)
		if err != nil {
			_ = asm.Close(context.Background())
			return nil, fmt.Errorf("app: 插件 %s 的能力集合非法: %w", script.Manifest.ID, err)
		}
		hc := deps.Context
		hc.PluginID = script.Manifest.ID
		hc.Settings = deps.Settings[script.Manifest.ID]
		inst, err := host.Load(ctx, script, caps, hc, host.Options{
			Targets: host.Targets{Tools: tools, Hooks: hooks},
		})
		if err != nil {
			_ = asm.Close(context.Background())
			return nil, fmt.Errorf("app: 装配插件 %s 失败: %w", script.Manifest.ID, err)
		}
		asm.Plugins = append(asm.Plugins, inst)
	}

	rt := &coreapp.Runtime{
		Product:       p,
		ToolCount:     len(tools.List(ctx)),
		AccessesInUse: tools.AccessesInUse(),
	}
	for _, inst := range asm.Plugins {
		if c := inst.Contribution(); c != nil {
			rt.ProviderCount += len(c.Providers)
		}
		if isPolicyPlugin(inst.Manifest()) {
			rt.PolicyPluginActive = true
		}
	}
	if err := rt.Check(); err != nil {
		_ = asm.Close(context.Background())
		return nil, err
	}
	asm.Runtime = rt
	return asm, nil
}

// Close 关闭全部插件实例（释放 Runtime 与 fs 句柄）。
func (a *Assembly) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var errs []error
	for i := len(a.Plugins) - 1; i >= 0; i-- {
		if err := a.Plugins[i].Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("关闭插件 %s: %w", a.Plugins[i].ID(), err))
		}
	}
	a.Plugins = nil
	return errors.Join(errs...)
}

// Plugin 按 id 找宿主实例（诊断与测试用）。
func (a *Assembly) Plugin(id string) (*host.Instance, bool) {
	for _, inst := range a.Plugins {
		if inst.ID() == id {
			return inst, true
		}
	}
	return nil, false
}

// isPolicyPlugin 报告插件是不是审批策略角色（清单 backend.roles）。
//
// 基座只认「有没有策略插件」，不解释它的规则（05 §6.2、03 §5.6）。
func isPolicyPlugin(m *plugins.Manifest) bool {
	if m == nil || m.Backend == nil {
		return false
	}
	for _, role := range m.Backend.Roles {
		if role == plugins.RoleToolPolicy {
			return true
		}
	}
	return false
}
