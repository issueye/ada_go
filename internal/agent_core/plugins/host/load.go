package host

// 本文件负责虚拟模块与入口加载（06 §6.4 初始化、06 §6.5 装配、12 §6.12.3 解析）：
//
//   - 入口固定为 plugin://<id>/index.js 形状的名字；其余模块只在 Script.Bundle 内联表里找；
//   - 不访问磁盘、不解析 node_modules、不做 TypeScript 编译（05 §6.1、06 §6.10）；
//   - 宿主表里没有的模块在「链接期」失败，而不是运行期拿到 undefined（06 §6.5、12 §6.14.2）；
//     失败文案点名说明符与原因（`.node` 原生扩展 / npm 包）；
//   - import.meta 指向虚拟路径（plugin://<id>/…），宿主绝对路径不进脚本（12 §6.12.1）。

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/MoeForge/moejs"

	"github.com/adag/ada-go/internal/common/pluginapi"
)

// AgentAPIModule 是类型声明用的虚拟模块名（05 §6.3：后端插件从 agent:api 导入类型）。
// esbuild 会擦除纯类型导入，运行期这里只兜住「显式 import 它」的形状：导出为空。
const AgentAPIModule = "agent:api"

// moduleProvider 把一个模块名解析成宿主提供的模块（node:path、node:fs 之类）。
//
// 模块安装（caps.go）与模块表必须有同一份注册表：Runtime 构造期就要拿到 Importer，
// 而 InstallCapabilities 拿的是已经构造好的 Runtime —— 因此模块实现在这里以
// 注册表的形式提供，由 caps.go 在 init 里登记（见 RegisterModuleProvider）。
type moduleProvider func(caps *CapabilitySet) (*moejs.Module, error)

var (
	moduleProvidersMu sync.RWMutex
	moduleProviders   = map[string]moduleProvider{}
)

// RegisterModuleProvider 登记一个模块名（"path"、"node:fs"、"fs/promises" 都算同一模块）
// 的宿主实现。重复登记后者覆盖前者，便于测试替身。
func RegisterModuleProvider(name string, fn func(caps *CapabilitySet) (*moejs.Module, error)) {
	key := strings.TrimPrefix(name, "node:")
	moduleProvidersMu.Lock()
	defer moduleProvidersMu.Unlock()
	moduleProviders[key] = fn
}

func lookupModuleProvider(name string) (moduleProvider, bool) {
	key := strings.TrimPrefix(name, "node:")
	moduleProvidersMu.RLock()
	defer moduleProvidersMu.RUnlock()
	fn, ok := moduleProviders[key]
	return fn, ok
}

// BuiltinModules 返回宿主模块表里真的能装出来的 node 内建模块名（排序后、不含 agent:*）。
//
// 它是**诊断事实**而不是安装许可（安装矩阵已删除，宿主不再按清单裁剪模块）：
// node:module 的 builtinModules / isBuiltin 用它回答「什么叫内置」，
// 测试用它当完整性门禁。agent:host / agent:api 不是 node 内建，故排除。
func BuiltinModules() []string {
	moduleProvidersMu.RLock()
	names := make([]string, 0, len(moduleProviders))
	for name := range moduleProviders {
		if strings.Contains(name, ":") {
			continue
		}
		names = append(names, name)
	}
	moduleProvidersMu.RUnlock()
	sort.Strings(names)
	return names
}

// InstallContextMembers 是「把能力成员挂到每次调用的 ctx 对象上」的挂载点（可选）。
//
// 05 §6.3 的 ToolContext 里 fs/exec/fetch 这些成员由宿主统一注入，形状由宿主决定；
// agentapi.go 负责安装 ctx 的身份字段与 log/state/secret/ui/http（这些只依赖 host.go
// 里已经定义的桥），成员安装方通过本变量补上自己的成员。未赋值不视为错误。
var InstallContextMembers func(rt *mojsRuntime, put func(name string, v moejs.Value) error, kind callKind, caps *CapabilitySet, hc Context) error

// pluginapi.EntryScheme / entryName / validate 等形状检查已下沉到 internal/common/pluginapi：
// Gateway 的前端渲染桥只需要「清单 + 源码 + 静态资源」，不该依赖插件宿主。
// 这里保留一个薄封装，避免调用方到处写 pluginapi.EntryName。

func entryName(id string) string { return pluginapi.EntryName(id) }

// checkVirtualName 是 pluginapi.CheckVirtualName 的薄封装。
func checkVirtualName(id, name string) error { return pluginapi.CheckVirtualName(id, name) }

// moduleLoader 是一个运行时的虚拟模块表（每 Runtime 一份缓存，12 §6.11 模块系统）。
type moduleLoader struct {
	script Script
	caps   *CapabilitySet
	cache  map[string]*moejs.Module
	entry  *moejs.Module
}

func newModuleLoader(s Script, caps *CapabilitySet) *moduleLoader {
	return &moduleLoader{script: s, caps: caps, cache: map[string]*moejs.Module{}}
}

func (l *moduleLoader) importer() *moejs.Importer {
	return &moejs.Importer{Resolve: moejs.Resolver(l.resolve), Meta: l.meta}
}

func (l *moduleLoader) resolver() moejs.Resolver {
	return moejs.Resolver(l.resolve)
}

// ValidateScript 做**不执行插件代码**的静态检查：清单形状、入口语法与模块链接。
// 安装/体检的预检用它替代「运行入口工厂 + 完整装配」，
// 避免一个只读工具/安装动作触发任意插件代码（09 §10.2、11 §16 P0）。
//
// 它不注册工具、不执行 handler、不触发网络/子进程；真正的贡献校验留给显式重载。
func ValidateScript(s Script, caps *CapabilitySet) error {
	if s.Manifest == nil {
		return fmt.Errorf("host: 插件脚本缺少清单")
	}
	if caps == nil {
		return fmt.Errorf("host: 插件 %s 缺少能力集合", s.Manifest.ID)
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if err := validateStaticNodeImports(s, caps); err != nil {
		return err
	}
	loader := newModuleLoader(s, caps)
	_, err := loader.linkEntry()
	return err
}

// validateStaticNodeImports 只检查 `.node` 原生扩展（链接期一律拒绝）；模块白名单已移除。
var nativeAddonRe = regexp.MustCompile(`(?m)(?:from\s+|import\s*\(?\s*)["']([^"']+\.node)["']`)

func validateStaticNodeImports(s Script, _ *CapabilitySet) error {
	sources := make([]string, 0, len(s.Bundle)+1)
	sources = append(sources, s.Source)
	for _, src := range s.Bundle {
		sources = append(sources, src)
	}
	for _, src := range sources {
		if m := nativeAddonRe.FindStringSubmatch(src); m != nil {
			return fmt.Errorf("host: 插件 %s 静态检查导入 %s 被拒绝: 原生扩展不支持（12 §6.10）", s.PluginID(), m[1])
		}
	}
	return nil
}

// linkEntry 编译并链接入口模块：链接期就把未知 import 判成装配失败（06 §6.5）。
func (l *moduleLoader) linkEntry() (*moejs.Module, error) {
	if l.entry != nil {
		return l.entry, nil
	}
	name := l.script.EntryName
	if name == "" {
		name = entryName(l.script.PluginID())
	}
	m, err := moejs.Compile(name, l.script.Source)
	if err != nil {
		return nil, fmt.Errorf("host: 插件 %s 的入口编译失败: %w", l.script.PluginID(), err)
	}
	linked, lerr := moejs.Link(m, l.resolver())
	if lerr != nil {
		return nil, fmt.Errorf("host: 插件 %s 的入口链接失败: %w", l.script.PluginID(), describeLinkError(lerr))
	}
	l.entry = linked
	l.cache[name] = linked
	return linked, nil
}

// resolve 解析一个 specifier：先查随包表，再查宿主模块表，最后按矩阵给出点名错误。
func (l *moduleLoader) resolve(referrer moejs.Referrer, spec string) (*moejs.Module, error) {
	if spec == AgentAPIModule {
		return l.compileOnce(AgentAPIModule, "export {};")
	}
	if m, ok := l.cache[spec]; ok {
		return m, nil
	}
	if src, ok := l.bundleSource(referrer, spec); ok {
		name := l.resolveName(referrer, spec)
		return l.compileOnce(name, src)
	}
	if mod, err := l.hostModule(spec); err == nil {
		return mod, nil
	} else {
		return nil, err
	}
}

// bundleSource 在随包表里找源码；相对 specifier 以引用者所在目录为基准归一化。
func (l *moduleLoader) bundleSource(referrer moejs.Referrer, spec string) (string, bool) {
	if src, ok := l.script.Bundle[spec]; ok {
		return src, true
	}
	name := l.resolveName(referrer, spec)
	if src, ok := l.script.Bundle[name]; ok {
		return src, true
	}
	// 也接受「写 plugin://<id>/x.js、表里键是 ./x.js」这类归一化差异。
	if rel, ok := strings.CutPrefix(name, pluginapi.EntryScheme+"://"+l.script.PluginID()+"/"); ok {
		if src, ok := l.script.Bundle[rel]; ok {
			return src, true
		}
	}
	return "", false
}

// resolveName 把 specifier 变成规范的虚拟模块名。
func (l *moduleLoader) resolveName(referrer moejs.Referrer, spec string) string {
	id := l.script.PluginID()
	base := entryName(id)
	if referrer != nil {
		if n := referrer.Name(); n != "" {
			base = n
		}
	}
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == ".." {
		dir := path.Dir(base)
		return path.Join(dir, spec)
	}
	if strings.HasPrefix(spec, pluginapi.EntryScheme+"://") {
		return path.Clean(spec)
	}
	return spec
}

func (l *moduleLoader) compileOnce(name, src string) (*moejs.Module, error) {
	if m, ok := l.cache[name]; ok {
		return m, nil
	}
	m, err := moejs.Compile(name, src)
	if err != nil {
		return nil, fmt.Errorf("host: 插件 %s 的模块 %s 编译失败: %w", l.script.PluginID(), name, err)
	}
	l.cache[name] = m
	return m, nil
}

// hostModule 找宿主提供的模块；模块白名单已移除，宿主表里没有的模块按 npm/原生扩展报错。
func (l *moduleLoader) hostModule(spec string) (*moejs.Module, error) {
	trimmed := strings.TrimPrefix(spec, "node:")
	if fn, ok := lookupModuleProvider(trimmed); ok {
		m, err := fn(l.caps)
		if err != nil {
			return nil, fmt.Errorf("host: 插件 %s 导入 %s 失败: %w", l.script.PluginID(), spec, err)
		}
		if m == nil {
			return nil, l.missingModuleError(spec)
		}
		return m, nil
	}
	return nil, l.missingModuleError(spec)
}

// missingModuleError 解释「宿主表里没有这个模块」：不再按安装矩阵分层，
// 只剩「原生扩展 / npm 包」两种事实。
func (l *moduleLoader) missingModuleError(spec string) error {
	id := l.script.PluginID()
	if strings.HasSuffix(spec, ".node") {
		return fmt.Errorf("host: 插件 %s 导入 %s 被拒绝：原生扩展不支持（12 §6.10）", id, spec)
	}
	return npmSpecifierError(id, spec)
}

func npmSpecifierError(id, spec string) error {
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return fmt.Errorf("host: 插件 %s 导入 %q：随包 Bundle 里没有这个模块（入口的相对依赖必须内联，06 §6.10）", id, spec)
	}
	return fmt.Errorf("host: 插件 %s 导入 %q：宿主不解析 node_modules，请把依赖在构建期打进 Bundle（05 §6.1、06 §6.10）", id, spec)
}

// describeLinkError 把 moejs 的链接错误翻成「缺什么」的一句话。
func describeLinkError(err error) error {
	var re *moejs.ResolveError
	if errors.As(err, &re) {
		return err
	}
	return err
}

// meta 填 import.meta：url/filename/dirname 都指向虚拟路径（12 §6.11 模块系统）。
func (l *moduleLoader) meta(r *moejs.Realm, m *moejs.Module, meta *moejs.Object) error {
	name := ""
	if m != nil {
		name = m.Name()
	}
	if name == "" {
		name = entryName(l.script.PluginID())
	}
	set := func(key, val string) error {
		return meta.SetProp(r, r.KeyFromGoString(key), moejs.String(val))
	}
	if err := set("url", name); err != nil {
		return err
	}
	if err := set("filename", name); err != nil {
		return err
	}
	if err := set("dirname", path.Dir(name)); err != nil {
		return err
	}
	return set("path", strings.TrimPrefix(name, pluginapi.EntryScheme+"://"))
}

// runInit 执行装配期：建运行时 → 加载入口 → 安装能力 → 跑入口工厂 → 结算贡献。
func (p *Instance) runInit(c *call) {
	defer func() {
		if rec := recover(); rec != nil {
			c.reply(outcome{err: fmt.Errorf("host: 插件 %s 装配期宿主侧 panic: %v", p.id, rec)})
		}
	}()
	p.cur = c
	defer func() { p.cur = nil }()

	if err := p.openRuntime(); err != nil {
		c.reply(outcome{err: err})
		return
	}
	if err := p.installCapabilities(); err != nil {
		c.reply(outcome{err: err})
		return
	}
	if err := p.loadEntry(); err != nil {
		c.reply(outcome{err: err})
		return
	}
	st := newStaging(p)
	p.staging = st
	if err := p.runFactory(st); err != nil {
		c.reply(outcome{err: err})
		return
	}
	if err := p.commitStaging(st); err != nil {
		c.reply(outcome{err: err})
		return
	}
	p.frozen = true
	p.generation.Add(1)
	c.reply(outcome{})
}

// loadEntry 在 loop 上加载入口模块（Load 只允许在 loop 回调里做，D-7 实测）。
func (p *Instance) loadEntry() error {
	linked, err := p.loader.linkEntry()
	if err != nil {
		return err
	}
	err = p.loop.Run(func(rt *mojsRuntime) error {
		if lerr := rt.Load(linked); lerr != nil && !errors.Is(lerr, moejs.ErrModulePending) {
			return lerr
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("host: 插件 %s 加载入口失败: %w", p.id, p.exportError(err))
	}
	return nil
}

// installCapabilities 调用成员与模块安装挂载点（由 caps.go 赋值）。
func (p *Instance) installCapabilities() error {
	fn := InstallCapabilities
	if fn == nil {
		return fmt.Errorf("host: InstallCapabilities 未赋值，插件 %s 拒绝加载（不跳过能力安装）", p.id)
	}
	hc := p.hc
	hc.PluginID = p.id
	if hc.Workspace == "" {
		hc.Workspace = p.caps.WorkspaceRoot
	}
	if p.cur != nil {
		hc.Signal = p.cur.signal
		hc.Now = p.cur.started.UTC()
	}
	if err := fn(p.rt, p.loop, p.caps, hc); err != nil {
		return fmt.Errorf("host: 插件 %s 的能力安装失败: %w", p.id, err)
	}
	return nil
}
