// Package app 是组合根：唯一同时知道「产品声明」与「官方插件目录」的地方
// （设计 5.1、1.5.5、11 §13 的 app/）。
//
// internal/ 不得 import official/，也不得在非测试源码里出现官方插件 id；因此
// 产品选择 × 插件包 × 宿主装配这三件事只能在这里汇合。装配结果是一份运行期目录：
// 工具目录、钩子链、各插件的宿主实例，以及 coreapp.Runtime 的门禁判定。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/adag/ada-go/internal/coreapp"
	"github.com/adag/ada-go/internal/corecontracts"
	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
	"github.com/adag/ada-go/internal/plugins/host"
	"github.com/adag/ada-go/internal/plugins/provider"
	"github.com/adag/ada-go/internal/toolkit"
	"github.com/adag/ada-go/plugins/official"
)

// Deps 是装配期注入的宿主依赖（契约定义在 corecontracts，供 CLI/Core 共用）。
type Deps = corecontracts.Deps

// Assembly 是一次装配的结果。
type Assembly struct {
	Product *coreapp.Product
	Runtime *coreapp.Runtime
	// Plugins 按产品声明顺序排列。
	Plugins []*host.Instance
	// Tools 与 Hooks 是运行期目录，引擎直接消费。
	Tools *toolkit.Catalog
	// ToolManager 是工具管理的唯一入口（注册/摘除/可见性视图/执行路由）。
	// Tools 是它背后的纯注册表；装配与诊断仍可能需要直接读目录。
	manager *toolkit.ToolManager
	Hooks   *plugins.HookChain
	// Providers 是 provider 插件桥成的 ModelClient 目录（可能为空）。
	Providers *provider.Registry
	// Actions 是前端动作目录（pluginID → name → ActionDef，M4 的 action.invoke 数据面）。
	Actions *actionRegistry
	// Routes 是插件动态 HTTP 路由目录（route；Gateway /ext/{id}/api/* 的数据面）。
	Routes *routeRegistry
	// FrontendDecls 记录产品声明里被 Gateway 侧装配的前端插件 id（本侧不装配）。
	FrontendDecls []string
	// skills 是本次装配的技能快照（6.7：插件贡献 + 工作区，同名工作区优先）。
	// 装配完成后不可变；经 Skills / SkillConflicts 读取。
	skills skillTable
}

// PluginInstances 返回本次装配的插件实例（corecontracts.Assembly）。
func (a *Assembly) PluginInstances() []*host.Instance {
	if a == nil {
		return nil
	}
	return a.Plugins
}

// ToolCatalog 返回工具目录。
func (a *Assembly) ToolCatalog() *toolkit.Catalog {
	if a == nil {
		return nil
	}
	return a.Tools
}

// ToolManager 返回工具管理器（工具可见性视图与执行路由的唯一入口）。
func (a *Assembly) ToolManager() *toolkit.ToolManager {
	if a == nil {
		return nil
	}
	return a.manager
}

// ProviderRegistry 返回 provider 目录。
func (a *Assembly) ProviderRegistry() *provider.Registry {
	if a == nil {
		return nil
	}
	return a.Providers
}

// HookChain 返回钩子链。
func (a *Assembly) HookChain() *plugins.HookChain {
	if a == nil {
		return nil
	}
	return a.Hooks
}

// Skills 返回本次装配的技能元数据（不含正文；deps.Skills 与 ctx.skills 用它）。
func (a *Assembly) Skills() []domain.SkillMeta {
	if a == nil {
		return nil
	}
	return a.skills.metasForPrompt()
}

// SkillConflicts 返回同名冲突的技能名（工作区优先已生效；调用方负责记警告，6.7）。
func (a *Assembly) SkillConflicts() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.skills.conflicts...)
}

// ActionDef 返回某插件的一个界面动作定义（action.invoke 用）。
func (a *Assembly) ActionDef(pluginID, name string) (host.ActionDef, bool) {
	if a == nil || a.Actions == nil {
		return host.ActionDef{}, false
	}
	return a.Actions.Lookup(pluginID, name)
}

// Load 从产品声明文件装配（官方插件来自磁盘目录）。
func Load(ctx context.Context, productPath string, deps Deps) (*Assembly, error) {
	p, err := coreapp.LoadProduct(productPath)
	if err != nil {
		return nil, err
	}
	scripts := deps.Scripts
	if scripts == nil {
		// 由调用方注入官方插件脚本，避免 app 依赖具体打包方式。
		return nil, fmt.Errorf("app: 没有插件包可装配（Deps.Scripts 为空；官方包由调用方注入）")
	}
	return Assemble(ctx, p, scripts, deps)
}

// LoadOfficial 从产品声明文件装配，插件脚本来自官方插件目录。
//
// 这是「官方插件只经由组合根进入基座」的唯一入口：cmd/ 与 internal/ 都不 import
// official/（设计 01 §3.3、11 §13）。
func LoadOfficial(ctx context.Context, productPath string, deps Deps) (*Assembly, error) {
	scripts, err := OfficialScripts()
	if err != nil {
		return nil, err
	}
	deps.Scripts = scripts
	return Load(ctx, productPath, deps)
}

// OfficialScripts 返回官方插件脚本（按 id 排序）。
//
// 官方插件只从磁盘目录加载（Electron resources 或源码检出）；找不到目录直接
// 返回错误，二进制不内嵌副本。
// M3 会话热重载需要在 Open 时冻结这份 base，之后每次重载都以它为合并基准；
// internal/ 不得 import official/，所以经由组合根转手（01 §3.3）。
func OfficialScripts() ([]host.Script, error) {
	return official.Scripts()
}

// OfficialRoot 返回官方插件目录；详情页的路径/文件清单与加载器共用这份解析。
func OfficialRoot() (string, error) {
	return official.Root()
}

// AssembleWithExtras 在产品声明的插件之外追加第三方插件脚本（M3 插件平台的装配入口）。
//
//   - base 为 nil 时取官方插件目录；否则原样使用（测试注入替身）。
//   - extras 是 discovery 层产出的第三方脚本；启用状态由调用方按 plugin_meta 过滤，
//     官方同 id 的冲突也由调用方剥掉（官方不可被磁盘同名包覆盖，6.9）。
//   - disabled 列出本次不装配的产品插件 id（plugin_meta.enabled=false）。禁用唯一
//     策略插件且目录里仍有写/执行工具时，Runtime.Check 拒绝（plugin.setEnabled 门禁）。
//   - optIn 是产品未声明、但用户显式启用过的随包插件 id：按与产品插件相同的
//     加载/能力/门禁路径追加装配（后端段进目录，前端段记入 frontendDecls）。
//
// 与 Assemble 共用同一条装配路径：清单校验、能力授予、贡献结算与门禁判定完全一致。
func AssembleWithExtras(ctx context.Context, p *coreapp.Product, base, extras []host.Script, disabled, optIn map[string]bool, deps Deps) (*Assembly, error) {
	if len(base) == 0 {
		b, err := OfficialScripts()
		if err != nil {
			return nil, err
		}
		base = b
	}
	return assembleSet(ctx, p, base, extras, disabled, optIn, deps)
}

// Assemble 把产品声明与插件集合结算成运行期目录（06 §6.5 的两阶段在 M3 落地；
// 这里先做「逐插件加载 → 原子提交到目录 → 门禁判定」）。
//
// 缺失插件是装配错误而不是静默跳过：产品声明就是「启用这些插件」的承诺，
// 少一个就等于承诺没兑现（1.5.5 禁用即能力消失的反向）。
func Assemble(ctx context.Context, p *coreapp.Product, scripts []host.Script, deps Deps) (*Assembly, error) {
	return assembleSet(ctx, p, scripts, nil, nil, nil, deps)
}

// Assembler 是组合根对 corecontracts.Assembler 的实现。
//
// 它把具体 *Assembly 适配成接口返回，让 internal/cli 不需要 import app；
// cmd/adag 负责把 Assembler{} 注入 cli.Config。
type Assembler struct{}

func (Assembler) Assemble(ctx context.Context, p *coreapp.Product, scripts []host.Script, deps Deps) (corecontracts.Assembly, error) {
	asm, err := Assemble(ctx, p, scripts, deps)
	return assemblyOrNil(asm, err)
}

func (Assembler) AssembleWithExtras(ctx context.Context, p *coreapp.Product, base, extras []host.Script, disabled, optIn map[string]bool, deps Deps) (corecontracts.Assembly, error) {
	asm, err := AssembleWithExtras(ctx, p, base, extras, disabled, optIn, deps)
	return assemblyOrNil(asm, err)
}

func (Assembler) OfficialScripts() ([]host.Script, error) { return OfficialScripts() }

func (Assembler) FrontendScripts() ([]host.Script, error) { return FrontendScripts() }

func (Assembler) OfficialRoot() (string, error) { return OfficialRoot() }

func assemblyOrNil(asm *Assembly, err error) (corecontracts.Assembly, error) {
	if asm == nil {
		return nil, err
	}
	return asm, err
}

// assembleSet 是 Assemble / AssembleWithExtras 的共用实现：
// 先按产品声明挑选（disabled 的容忍缺席），再按 id 排序追加第三方。
func assembleSet(ctx context.Context, p *coreapp.Product, base, extras []host.Script, disabled, optIn map[string]bool, deps Deps) (*Assembly, error) {
	if p == nil {
		return nil, errors.New("app: Assemble 需要非空产品声明")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	byID := make(map[string]host.Script, len(base)+len(extras))
	baseIDs := make(map[string]bool, len(base))
	productIDs := make(map[string]bool, len(p.Plugins))
	for _, id := range p.Plugins {
		productIDs[id] = true
	}
	// extras 只补缺：官方/产品 id 不允许被磁盘同名包覆盖（6.9）。
	for _, s := range extras {
		if s.Manifest == nil {
			return nil, errors.New("app: 第三方插件缺少清单")
		}
		id := s.Manifest.ID
		if _, dup := byID[id]; !dup && !productIDs[id] {
			byID[id] = s
		}
	}
	for _, s := range base {
		if s.Manifest == nil {
			return nil, errors.New("app: 插件缺少清单")
		}
		byID[s.Manifest.ID] = s
		baseIDs[s.Manifest.ID] = true
	}

	picked := make([]host.Script, 0, len(p.Plugins))
	var missing []string
	frontendDecls := make([]string, 0)
	for _, id := range p.Plugins {
		if disabled[id] {
			// 用户已禁用（plugin_meta.enabled=false）：本次装配容忍缺席。
			continue
		}
		if deps.FrontendIDs[id] {
			// M4 前端插件：由 Gateway 渲染桥装配，本侧记录声明。
			// 统一插件（同时声明 backend）的后端段仍要在这里装配；
			// 纯前端插件没有 byID 条目，跳过即可。
			frontendDecls = append(frontendDecls, id)
			s, ok := byID[id]
			if !ok || s.Manifest.Backend == nil {
				continue
			}
			picked = append(picked, s)
			continue
		}
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
	// 用户显式启用的未声明随包插件：与产品插件同一条装配路径追加。
	// 前端段记入 frontendDecls（由 Gateway 渲染桥装配），后端段进目录。
	if len(optIn) > 0 {
		optIDs := make([]string, 0, len(optIn))
		for id := range optIn {
			if productIDs[id] {
				continue
			}
			optIDs = append(optIDs, id)
		}
		sort.Strings(optIDs)
		for _, id := range optIDs {
			s, hasScript := byID[id]
			if !hasScript {
				// 纯前端插件没有后端加载单元：只登记 frontendDecls，
				// 由 Gateway 渲染桥按启停状态决定是否渲染。
				if deps.FrontendIDs[id] {
					frontendDecls = append(frontendDecls, id)
				}
				continue
			}
			if deps.FrontendIDs[id] {
				frontendDecls = append(frontendDecls, id)
			}
			if s.Manifest.Backend == nil {
				continue
			}
			picked = append(picked, s)
		}
	}
	// 第三方按 id 排序追加：与官方同一条装配路径，顺序确定保证回放确定性（6.5）。
	extraIDs := make([]string, 0, len(extras))
	for _, s := range extras {
		id := s.Manifest.ID
		if productIDs[id] || baseIDs[id] {
			continue // 与随包插件同 id 的第三方已被官方覆盖，不重复装配（6.9）
		}
		if _, ok := byID[id]; ok {
			extraIDs = append(extraIDs, id)
		}
	}
	sort.Strings(extraIDs)
	for _, id := range extraIDs {
		picked = append(picked, byID[id])
	}

	tools := toolkit.NewCatalog()
	hooks := plugins.NewHookChain()
	providers := provider.New(deps.BaseURL)
	providers.HostContext = deps.Context
	actions := newActionRegistry()
	routes := newRouteRegistry()
	frontend := newFrontendRegistry()
	prompts := newPromptRegistry()
	invoker := &toolInvoker{plugins: map[string]*host.Instance{}, base: deps.Context}
	// 工具管理器：Catalog 是它背后的注册表，工具执行路由（Owner → 插件实例）也是它。
	// 装配期注册统一走 Manager，冻结后（运行期新注册）由 Catalog 记警告不生效。
	toolMgr, err := toolkit.NewToolManager(tools, invoker)
	if err != nil {
		return nil, err
	}
	asm := &Assembly{Product: p, Tools: tools, manager: toolMgr, Hooks: hooks, Providers: providers,
		Actions: actions, Routes: routes, FrontendDecls: frontendDecls}

	// 技能表在插件加载前结算：插件注册期需要看到技能元数据（ctx.skills），
	// 产品 capabilities.skills 是技能通道的唯一门控。
	skillTable := settleSkills(picked, deps.Context.Workspace, func(msg string) {
		if deps.OnWarning != nil {
			deps.OnWarning(msg)
		}
	})
	asm.skills = skillTable
	baseCtx := deps.Context
	baseCtx.Skills = nil
	// 环境变量模块的用户变量：注入到每个插件启动的子进程（exec/后台/process.start）。
	baseCtx.EnvVars = deps.EnvVars
	if p.Capabilities.Skills {
		baseCtx.Skills = asm.Skills
	}
	invoker.base = baseCtx

	for _, script := range picked {
		caps, err := host.NewCapabilitySet(script.Manifest, deps.Context.Workspace, deps.HostEnv, deps.Secrets)
		if err != nil {
			_ = asm.Close(context.Background())
			return nil, fmt.Errorf("app: 插件 %s 的能力集合非法: %w", script.Manifest.ID, err)
		}
		hc := baseCtx
		hc.PluginID = script.Manifest.ID
		hc.Settings = deps.Settings[script.Manifest.ID]
		inst, err := host.Load(ctx, script, caps, hc, host.Options{
			Targets: host.Targets{Tools: toolMgr, Hooks: hooks, Providers: providers,
				Actions: actions, Routes: routes, Frontend: frontend, Prompts: prompts},
		})
		if err != nil {
			_ = asm.Close(context.Background())
			return nil, fmt.Errorf("app: 装配插件 %s 失败: %w", script.Manifest.ID, err)
		}
		asm.Plugins = append(asm.Plugins, inst)
		invoker.plugins[inst.ID()] = inst
		if err := providers.Bind(inst.ID(), inst); err != nil {
			_ = asm.Close(context.Background())
			return nil, err
		}
		if c := inst.Contribution(); c != nil && len(c.Providers) > 0 && deps.Context.HTTP == nil {
			_ = asm.Close(context.Background())
			return nil, fmt.Errorf("app: 插件 %s 注册了 provider，但装配没有注入 HTTP 桥（失败关闭）", inst.ID())
		}
	}
	tools.Freeze()

	rt := &coreapp.Runtime{
		Product:       p,
		ToolCount:     len(tools.List(ctx)),
		AccessesInUse: tools.AccessesInUse(),
	}
	for _, inst := range asm.Plugins {
		if c := inst.Contribution(); c != nil {
			rt.ProviderCount += len(c.Providers)
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

// ToolInvoker 返回 toolkit.Executor 需要的插件调用路由。
//
// 返回的是 ToolManager 而不是底层的 pluginInvoker：路由的归属以目录为准，
// 上层传错 pluginID 会在 Manager 里被拒绝，而不是投给另一个插件。
func (a *Assembly) ToolInvoker() toolkit.Invoker {
	if a == nil || a.manager == nil {
		return nil
	}
	return a.manager
}

// toolInvoker 按「工具名 → 贡献它的插件实例」路由调用。
//
// 工具名在装配期已由 Catalog 去重，因此这里不需要再猜；找不到就是禁用/装配漂移，
// 返回错误让 Executor 走 error 回执（INV-5）。
type toolInvoker struct {
	plugins map[string]*host.Instance
	base    host.Context
}

var _ toolkit.Invoker = (*toolInvoker)(nil)

func (t *toolInvoker) Invoke(ctx context.Context, pluginID, tool string, args json.RawMessage) (domain.Result, error) {
	inst := t.plugins[pluginID]
	if inst == nil {
		return domain.Result{}, fmt.Errorf("app: 工具 %s 的插件 %s 未加载", tool, pluginID)
	}
	ref, ok := inst.ToolRef(tool)
	if !ok {
		return domain.Result{}, fmt.Errorf("app: 插件 %s 没有工具 %s", pluginID, tool)
	}
	hc := t.base
	hc.PluginID = pluginID
	hc.Signal = ctx
	hc.Now = time.Now().UTC()
	// 检查点归属（M5）：Executor 把 threadId/runId/callId/tool 放进 ctx，
	// 这里换入 host.Context；宿主 fs 写入前回调 Checkpoint 用它找检查点。
	if meta, ok := toolkit.MetaFrom(ctx); ok {
		hc.ThreadID, hc.RunID, hc.CallID = meta.ThreadID, meta.RunID, meta.CallID
	}
	return inst.CallTool(ctx, ref, args, hc)
}

// ProviderCount 返回已绑定的 provider 数（CLI/doctor 诊断用）。
func (a *Assembly) ProviderCount() int {
	if a == nil || a.Providers == nil {
		return 0
	}
	return len(a.Providers.Providers())
}

// FrontendScripts 返回官方前端脚本（M4；含同时声明 backend 的统一插件）。
// 与 OfficialScripts 一样从磁盘 plugins/official 加载；找不到目录直接报错。
// Gateway 的渲染桥用它们做 TSX→VNode→HTML；Session 只用 id 集合。
func FrontendScripts() ([]host.Script, error) {
	return official.FrontendScripts()
}
