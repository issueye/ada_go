package host

// 本文件是贡献的收集容器与「校验后原子提交」（06 §6.5 两阶段装配）：
//
//	staging（工厂调用期间收集） → Validate（重名/命名/能力/钩子等级/插槽）
//	        → 原子提交：provider 目录 → 工具目录 → 提示词 → 动作 → 路由 → 钩子链 → 前端注册表
//	        → 运行期只读（frozen）
//
// 任何一项非法都要回滚整个插件（其它插件不受影响），错误记在实例的 lastError 与摘要上。

import (
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"

	"github.com/adag/ada-go/internal/domain"
	"github.com/adag/ada-go/internal/plugins"
)

// 登记项类别（槽位键的前缀；重建时逐字比对）。
const (
	slotKindTool     = "tool"
	slotKindAction   = "action"
	slotKindRoute    = "route"
	slotKindProvider = "provider"
	slotKindHook     = "hook"
)

// Staging 是一次装配期内收集到的登记项（06 §6.5 的 staging）。
type Staging struct {
	p    *Instance
	data Contribution
	// keys 是「登记项 → 槽位」的登记表；重建时必须逐字复用已提交贡献的槽位。
	keys map[string]int
	// hookSeq 给同点位的多个钩子编号，让槽位键稳定。
	hookSeq map[string]int
}

func newStaging(p *Instance) *Staging {
	c := Contribution{
		PluginID: p.id,
		Version:  p.scriptVersion(),
		Trust:    p.caps.Trust,
		Frontend: FrontendContribution{Actions: map[string]plugins.ActionBinding{}},
	}
	return &Staging{p: p, data: c, keys: map[string]int{}, hookSeq: map[string]int{}}
}

// add 分配（或复用）一个登记项的槽位并登记脚本函数。
func (s *Staging) add(kind, ident string, fn moejs.Value) (int, error) {
	slot, err := s.alloc(kind, ident)
	if err != nil {
		return 0, err
	}
	s.p.bindHandler(slot, fn)
	return slot, nil
}

// alloc 取一个登记项的槽位。
//
// 首次装配：递增分配（0 保留给「没有句柄」，contrib.Validate 依赖这一点）。
// 重建：必须命中已提交贡献里的同一槽位，否则说明重载后登记表变了 —— 已提交的
// HandlerRef 会指向错误的函数，必须拒绝而不是将错就错（06 §6.5 运行期只读）。
func (s *Staging) alloc(kind, ident string) (int, error) {
	key := slotKey(kind, ident)
	if expect := s.p.expectSlots; expect != nil {
		slot, ok := expect[key]
		if !ok {
			return 0, fmt.Errorf("host: 插件 %s 重建时多出了登记项 %q（已提交的贡献里没有它，06 §6.5）", s.p.id, key)
		}
		s.keys[key] = slot
		return slot, nil
	}
	if slot, ok := s.keys[key]; ok {
		return slot, nil
	}
	slot := len(s.handlersView())
	s.growHandlers(slot + 1)
	s.keys[key] = slot
	return slot, nil
}

// handlersView / growHandlers 是 owner goroutine 上的槽位表操作。
func (s *Staging) handlersView() []moejs.Value { return s.p.handlers }

func (s *Staging) growHandlers(n int) {
	for len(s.p.handlers) < n {
		s.p.handlers = append(s.p.handlers, moejs.Value{})
	}
}

// nextHookOrdinal 返回该点位上的第几个钩子（1 起）。
func (s *Staging) nextHookOrdinal(point string) int {
	s.hookSeq[point]++
	return s.hookSeq[point]
}

// matchSlots 校验重建后的登记表与已提交贡献完全一致（不少不多）。
func (s *Staging) matchSlots(expect map[string]int) error {
	for key := range expect {
		if _, ok := s.keys[key]; !ok {
			return fmt.Errorf("host: 插件 %s 重建后缺少登记项 %q（入口工厂这次没有注册它，06 §6.5）", s.p.id, key)
		}
	}
	return nil
}

// slotKey 是登记项的稳定标识。
func slotKey(kind, ident string) string { return kind + ":" + ident }

// contribution 返回结算用的贡献（补齐清单侧的前端登记）。
func (s *Staging) contribution() *Contribution {
	c := s.data
	c.Frontend = s.p.frontendContribution()
	return &c
}

// ToolRegistry 是工具目录（生产实现：toolkit.Catalog）。
type ToolRegistry interface {
	Register(d domain.Descriptor) error
	Remove(pluginID string) int
}

// HookRegistry 是钩子链（生产实现：plugins.HookChain）。
type HookRegistry interface {
	Register(h *plugins.HookHandler) error
	RemovePlugin(pluginID string) int
}

// ProviderRegistration 是一条 provider 登记（M1-E 的 ModelClient 桥按它建适配器）。
type ProviderRegistration struct {
	PluginID string
	Def      ProviderDef
	Trust    domain.Trust
}

// ProviderRegistry 是 provider 目录。
type ProviderRegistry interface {
	RegisterProvider(reg ProviderRegistration) error
}

// PromptRegistration 是一条系统提示词登记（02 §5.3 装配边界里的插件人格段）。
type PromptRegistration struct {
	PluginID string
	Def      PromptDef
}

// PromptRegistry 是提示词目录。
type PromptRegistry interface {
	RegisterPrompt(reg PromptRegistration) error
}

// ActionRegistration 是一条界面动作登记（POST /ext/{id}/action/{name}）。
type ActionRegistration struct {
	PluginID string
	Def      ActionDef
}

// ActionRegistry 是动作注册表。
type ActionRegistry interface {
	RegisterAction(reg ActionRegistration) error
}

// RouteRegistration 是一条动态路由登记（7.6）。
type RouteRegistration struct {
	PluginID string
	Def      RouteDef
}

// RouteRegistry 是路由注册表。
type RouteRegistry interface {
	RegisterRoute(reg RouteRegistration) error
}

// FrontendRegistration 是前端贡献登记（7.2/7.5；渲染宿主在 Gateway 侧）。
type FrontendRegistration struct {
	PluginID     string
	Contribution FrontendContribution
}

// FrontendRegistry 是前端注册表。
type FrontendRegistry interface {
	RegisterFrontend(reg FrontendRegistration) error
}

// PluginRegistry 是一个目录共有的「按插件整体摘除」操作（回滚与禁用路径）。
type PluginRegistry interface {
	RemovePlugin(pluginID string) int
}

// Targets 是原子提交的七类目录。字段为 nil 表示装配层不需要这一类贡献；
// 有对应贡献却缺目录 → 装配失败（不静默丢弃贡献，INV-2）。
type Targets struct {
	Providers ProviderRegistry
	Tools     ToolRegistry
	Prompts   PromptRegistry
	Actions   ActionRegistry
	Routes    RouteRegistry
	Hooks     HookRegistry
	Frontend  FrontendRegistry

	// Removers 是每类目录的摘除函数；nil 时用目录自身的 RemovePlugin。
	Removers []PluginRegistry
}

// commit 按 06 §6.5 的顺序提交；任一失败即回滚已提交的部分。
//
// hooks 把一条 HookDef 变成钩子链能登记的处理器：钩子的调用方必须是插件实例
// （它要排进该 Runtime 的队列），而 Targets 不知道实例，所以由装配方注入。
func (t Targets) commit(c *Contribution, caps *CapabilitySet, hooks func(HookDef) (*plugins.HookHandler, error)) error {
	var undo []func()
	fail := func(err error) error {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		return err
	}
	rollbackAll := func(reg PluginRegistry) func() {
		return func() { _ = reg.RemovePlugin(c.PluginID) }
	}

	if len(c.Providers) > 0 {
		if t.Providers == nil {
			return fmt.Errorf("host: 插件 %s 注册了 provider，但装配缺少 provider 目录（06 §6.5）", c.PluginID)
		}
		if pr, ok := t.Providers.(PluginRegistry); ok {
			undo = append(undo, rollbackAll(pr))
		}
		for _, pv := range c.Providers {
			reg := ProviderRegistration{PluginID: c.PluginID, Def: pv, Trust: caps.DomainTrust()}
			if err := t.Providers.RegisterProvider(reg); err != nil {
				return fail(fmt.Errorf("host: 插件 %s 注册 provider %q 失败: %w", c.PluginID, pv.ID, err))
			}
		}
	}
	if len(c.Tools) > 0 {
		if t.Tools == nil {
			return fmt.Errorf("host: 插件 %s 注册了工具，但装配缺少工具目录（02 §5.4 描述符注册表）", c.PluginID)
		}
		undo = append(undo, func() { _ = t.Tools.Remove(c.PluginID) })
		for _, tl := range c.Tools {
			if err := t.Tools.Register(tl.Descriptor(c.PluginID)); err != nil {
				return fail(fmt.Errorf("host: 插件 %s 注册工具 %q 失败: %w", c.PluginID, tl.Name, err))
			}
		}
	}
	if len(c.Prompts) > 0 {
		if t.Prompts == nil {
			return fail(fmt.Errorf("host: 插件 %s 注册了提示词，但装配缺少提示词目录（02 §5.3）", c.PluginID))
		}
		if pr, ok := t.Prompts.(PluginRegistry); ok {
			undo = append(undo, rollbackAll(pr))
		}
		for _, item := range c.Prompts {
			if err := t.Prompts.RegisterPrompt(PromptRegistration{PluginID: c.PluginID, Def: item}); err != nil {
				return fail(fmt.Errorf("host: 插件 %s 注册提示词 %q 失败: %w", c.PluginID, item.ID, err))
			}
		}
	}
	if len(c.Actions) > 0 {
		if t.Actions == nil {
			return fail(fmt.Errorf("host: 插件 %s 注册了动作，但装配缺少动作注册表（7.6）", c.PluginID))
		}
		if pr, ok := t.Actions.(PluginRegistry); ok {
			undo = append(undo, rollbackAll(pr))
		}
		for _, item := range c.Actions {
			if err := t.Actions.RegisterAction(ActionRegistration{PluginID: c.PluginID, Def: item}); err != nil {
				return fail(fmt.Errorf("host: 插件 %s 注册动作 %q 失败: %w", c.PluginID, item.Name, err))
			}
		}
	}
	if len(c.Routes) > 0 {
		if t.Routes == nil {
			return fail(fmt.Errorf("host: 插件 %s 注册了路由，但装配缺少路由注册表（7.6）", c.PluginID))
		}
		if pr, ok := t.Routes.(PluginRegistry); ok {
			undo = append(undo, rollbackAll(pr))
		}
		for _, item := range c.Routes {
			if err := t.Routes.RegisterRoute(RouteRegistration{PluginID: c.PluginID, Def: item}); err != nil {
				return fail(fmt.Errorf("host: 插件 %s 注册路由 %s %s 失败: %w", c.PluginID, item.Method, item.Path, err))
			}
		}
	}
	if len(c.Hooks) > 0 {
		if t.Hooks == nil {
			return fail(fmt.Errorf("host: 插件 %s 注册了钩子，但装配缺少钩子链（06 §6.6）", c.PluginID))
		}
		undo = append(undo, func() { _ = t.Hooks.RemovePlugin(c.PluginID) })
		if hooks == nil {
			return fail(fmt.Errorf("host: 插件 %s 注册了钩子，但装配没有注入钩子工厂（06 §6.5）", c.PluginID))
		}
		for _, item := range c.Hooks {
			h, err := hooks(item)
			if err != nil {
				return fail(fmt.Errorf("host: 插件 %s 的钩子 %q 无法登记: %w", c.PluginID, item.Point, err))
			}
			if err := t.Hooks.Register(h); err != nil {
				return fail(fmt.Errorf("host: 插件 %s 注册钩子 %q 失败: %w", c.PluginID, item.Point, err))
			}
		}
	}
	f := c.Frontend
	if hasFrontend(f) {
		if t.Frontend == nil {
			return fail(fmt.Errorf("host: 插件 %s 有前端贡献，但装配缺少前端注册表（7.2）", c.PluginID))
		}
		if pr, ok := t.Frontend.(PluginRegistry); ok {
			undo = append(undo, rollbackAll(pr))
		}
		if err := t.Frontend.RegisterFrontend(FrontendRegistration{PluginID: c.PluginID, Contribution: f}); err != nil {
			return fail(fmt.Errorf("host: 插件 %s 注册前端贡献失败: %w", c.PluginID, err))
		}
	}
	return nil
}

func hasFrontend(f FrontendContribution) bool {
	return len(f.Slots)+len(f.Pages)+len(f.Nav)+len(f.Dashboard) > 0 ||
		len(f.Actions) > 0 || len(f.Styles) > 0 || len(f.Scripts) > 0 || f.Assets != ""
}

// rollback 把一个已提交插件的全部贡献从目录里摘掉（禁用/卸载路径，1.5.6）。
func (t Targets) rollback(pluginID string) {
	if t.Tools != nil {
		_ = t.Tools.Remove(pluginID)
	}
	if t.Hooks != nil {
		_ = t.Hooks.RemovePlugin(pluginID)
	}
	for _, reg := range []any{t.Providers, t.Prompts, t.Actions, t.Routes, t.Frontend} {
		if pr, ok := reg.(PluginRegistry); ok {
			_ = pr.RemovePlugin(pluginID)
		}
	}
	for _, reg := range t.Removers {
		if reg != nil {
			_ = reg.RemovePlugin(pluginID)
		}
	}
}

// commitStaging 做「校验 → 原子提交 → 运行期只读」。
func (p *Instance) commitStaging(st *Staging) error {
	c := st.contribution()
	if err := c.Validate(p.caps); err != nil {
		return fmt.Errorf("host: 插件 %s 的贡献校验失败: %w", p.id, err)
	}
	if err := p.opts.Targets.commit(c, p.caps, p.newHookHandler); err != nil {
		p.opts.Targets.rollback(p.id)
		return err
	}
	p.mu.Lock()
	p.contrib = c
	p.mu.Unlock()
	p.slots = st.keys
	return nil
}

func (p *Instance) scriptVersion() string {
	if p.script.Manifest == nil || p.script.Manifest.Version == "" {
		return "0.0.0"
	}
	return p.script.Manifest.Version
}

// frontendContribution 从清单 frontend 段结算前端贡献（模板路径已在清单校验里查过）。
func (p *Instance) frontendContribution() FrontendContribution {
	f := FrontendContribution{Actions: map[string]plugins.ActionBinding{}}
	m := p.script.Manifest
	if m == nil || m.Frontend == nil {
		return f
	}
	in := m.Frontend
	f.Slots = append([]plugins.SlotContribution(nil), in.Slots...)
	f.Pages = append([]plugins.PageContribution(nil), in.Pages...)
	f.Nav = append([]plugins.NavContribution(nil), in.Nav...)
	f.Dashboard = append([]plugins.DashboardContribution(nil), in.Dashboard...)
	f.Styles = append([]string(nil), in.Styles...)
	f.Scripts = append([]string(nil), in.Scripts...)
	f.Assets = in.Assets
	for name, binding := range in.Actions {
		f.Actions[name] = binding
	}
	return f
}

// 目录查询（预算档位与诊断）。

func (p *Instance) actionTimeoutOf(name string) (time.Duration, bool) {
	c := p.Contribution()
	if c == nil {
		return 0, false
	}
	for _, a := range c.Actions {
		if a.Name == name && a.TimeoutMS > 0 {
			return time.Duration(a.TimeoutMS) * time.Millisecond, true
		}
	}
	return 0, false
}

// toolsOf 返回已提交的工具登记。
func (p *Instance) toolsOf() []ToolDef {
	c := p.Contribution()
	if c == nil {
		return nil
	}
	return append([]ToolDef(nil), c.Tools...)
}

// committedSlots 复制当前登记表（重建时用来核对槽位）。
func (p *Instance) committedSlots() map[string]int {
	c := p.Contribution()
	if c == nil {
		return nil
	}
	out := map[string]int{}
	for _, t := range c.Tools {
		out[slotKey(slotKindTool, t.Name)] = t.Handler.Slot
	}
	for _, a := range c.Actions {
		out[slotKey(slotKindAction, a.Name)] = a.Handler.Slot
	}
	for _, r := range c.Routes {
		out[slotKey(slotKindRoute, routeIdent(r.Method, r.Path))] = r.Handler.Slot
	}
	for _, pr := range c.Providers {
		out[slotKey(slotKindProvider, pr.ID)] = pr.Stream.Slot
	}
	ord := map[string]int{}
	for _, h := range c.Hooks {
		ord[h.Point]++
		out[slotKey(slotKindHook, fmt.Sprintf("%s#%d", h.Point, ord[h.Point]))] = h.Handler.Slot
	}
	return out
}

// routeIdent 是路由的稳定标识（大写方法 + 路径）。
func routeIdent(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimSpace(path)
}
