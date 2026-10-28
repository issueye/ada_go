package host

// 本文件负责虚拟模块与入口加载（06 §6.4 初始化、06 §6.5 装配、12 §6.12.3 解析）：
//
//   - 入口固定为 plugin://<id>/index.js 形状的名字；其余模块只在 Script.Bundle 内联表里找；
//   - 不访问磁盘、不解析 node_modules、不做 TypeScript 编译（05 §6.1、06 §6.10）；
//   - 未安装的模块在「链接期」失败，而不是运行期拿到 undefined（06 §6.5、12 §6.14.2）；
//     失败文案点名缺失的能力位，让报错直接指向缺失能力；
//   - import.meta 指向虚拟路径（plugin://<id>/…），宿主绝对路径不进脚本（12 §6.12.1）。

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/MoeForge/moejs"

	"github.com/adag/ada-go/internal/plugins"
	nodemod "github.com/adag/ada-go/internal/plugins/host/node"
)

// AgentAPIModule 是类型声明用的虚拟模块名（05 §6.3：后端插件从 agent:api 导入类型）。
// esbuild 会擦除纯类型导入，运行期这里只兜住「显式 import 它」的形状：导出为空。
const AgentAPIModule = "agent:api"

// moduleProvider 把一个模块名解析成宿主提供的模块（node:path、node:fs 之类）。
//
// 能力安装（caps.go）与模块表必须共用一份白名单：Runtime 构造期就要拿到 Importer，
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

// InstallContextMembers 是「把能力成员挂到每次调用的 ctx 对象上」的挂载点（可选）。
//
// 05 §6.3 的 ToolContext 里 fs/exec/fetch 这些成员按能力安装，形状由宿主决定；
// agentapi.go 负责安装 ctx 的身份字段与 log/state/secret/ui/http（这些只依赖 host.go
// 里已经定义的桥），能力安装方通过本变量补上自己的成员。未赋值不视为错误。
var InstallContextMembers func(rt *mojsRuntime, put func(name string, v moejs.Value) error, kind callKind, caps *CapabilitySet, hc Context) error

// pluginID 是脚本对应的插件 id（清单优先，其次从入口名推）。
func (s Script) pluginID() string {
	if s.Manifest != nil && s.Manifest.ID != "" {
		return s.Manifest.ID
	}
	if rest, ok := strings.CutPrefix(s.EntryName, EntryScheme+"://"); ok {
		id, _, _ := strings.Cut(rest, "/")
		return id
	}
	return ""
}

// EntryScheme 是虚拟模块的名字空间（06 §6.4）。
const EntryScheme = "plugin"

// entryName 拼出规范入口名 plugin://<id>/index.js。
func entryName(id string) string { return EntryScheme + "://" + id + "/index.js" }

// validate 检查待加载脚本的形状：空基座不崩，缺件要点名（06 §6.5）。
func (s Script) validate() error {
	if s.Manifest == nil {
		return fmt.Errorf("host: 插件脚本缺少清单")
	}
	id := s.Manifest.ID
	if !plugins.ValidID(id) {
		return fmt.Errorf("host: 插件 id %q 非法（^[a-z][a-z0-9-]{1,63}$，06 §6.2）", id)
	}
	if s.Manifest.APIVersion != plugins.APIVersionMajor {
		return fmt.Errorf("host: 插件 %s 的 apiVersion %d 与宿主主版本 %d 不匹配（06 §6.2）",
			id, s.Manifest.APIVersion, plugins.APIVersionMajor)
	}
	if !s.Manifest.Trust.Valid() {
		return fmt.Errorf("host: 插件 %s 的 trust %q 未知（06 §6.9）", id, s.Manifest.Trust)
	}
	if strings.TrimSpace(s.Source) == "" {
		return fmt.Errorf("host: 插件 %s 没有入口源码（backend.entry 为空或在 Bundle 里缺失，06 §6.2）", id)
	}
	if s.EntryName == "" {
		s.EntryName = entryName(id)
	}
	if err := s.checkEntryName(id); err != nil {
		return err
	}
	for name := range s.Bundle {
		if err := checkVirtualName(id, name); err != nil {
			return fmt.Errorf("host: 插件 %s 的随包模块 %q 非法: %w", id, name, err)
		}
	}
	return nil
}

func (s Script) checkEntryName(id string) error {
	if err := checkVirtualName(id, s.EntryName); err != nil {
		return fmt.Errorf("host: 插件 %s 的入口名 %q 非法: %w", id, s.EntryName, err)
	}
	return nil
}

// checkVirtualName 限定模块名必须落在 plugin://<id>/ 之下（不接受绝对宿主路径）。
func checkVirtualName(id, name string) error {
	if name == AgentAPIModule {
		return nil
	}
	rest, ok := strings.CutPrefix(name, EntryScheme+"://")
	if !ok {
		return fmt.Errorf("必须是 %s://<id>/… 形状的虚拟名", EntryScheme)
	}
	owner, _, _ := strings.Cut(rest, "/")
	if owner != id {
		return fmt.Errorf("模块名属于插件 %q，与当前插件 %q 不符", owner, id)
	}
	if strings.Contains(name, "..") {
		return errors.New("模块名不允许包含 ..")
	}
	return nil
}

// granted 返回后端授予列表（矩阵比对口径）。
func (c *CapabilitySet) granted() []plugins.Capability {
	out := make([]plugins.Capability, 0, len(c.Backend))
	for cap := range c.Backend {
		out = append(out, cap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

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

// linkEntry 编译并链接入口模块：链接期就把未知 import 判成装配失败（06 §6.5）。
func (l *moduleLoader) linkEntry() (*moejs.Module, error) {
	if l.entry != nil {
		return l.entry, nil
	}
	name := l.script.EntryName
	if name == "" {
		name = entryName(l.script.pluginID())
	}
	m, err := moejs.Compile(name, l.script.Source)
	if err != nil {
		return nil, fmt.Errorf("host: 插件 %s 的入口编译失败: %w", l.script.pluginID(), err)
	}
	linked, lerr := moejs.Link(m, l.resolver())
	if lerr != nil {
		return nil, fmt.Errorf("host: 插件 %s 的入口链接失败: %w", l.script.pluginID(), describeLinkError(lerr))
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
	if rel, ok := strings.CutPrefix(name, EntryScheme+"://"+l.script.pluginID()+"/"); ok {
		if src, ok := l.script.Bundle[rel]; ok {
			return src, true
		}
	}
	return "", false
}

// resolveName 把 specifier 变成规范的虚拟模块名。
func (l *moduleLoader) resolveName(referrer moejs.Referrer, spec string) string {
	id := l.script.pluginID()
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
	if strings.HasPrefix(spec, EntryScheme+"://") {
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
		return nil, fmt.Errorf("host: 插件 %s 的模块 %s 编译失败: %w", l.script.pluginID(), name, err)
	}
	l.cache[name] = m
	return m, nil
}

// hostModule 找宿主提供的模块：必须同时过安装矩阵（能力位与信任级别）。
func (l *moduleLoader) hostModule(spec string) (*moejs.Module, error) {
	trimmed := strings.TrimPrefix(spec, "node:")
	if fn, ok := lookupModuleProvider(trimmed); ok {
		m, err := fn(l.caps)
		if err != nil {
			return nil, fmt.Errorf("host: 插件 %s 导入 %s 失败: %w", l.script.pluginID(), spec, err)
		}
		if m == nil {
			return nil, l.matrixError(spec)
		}
		return m, nil
	}
	return nil, l.matrixError(spec)
}

// matrixError 按安装矩阵解释「为什么这个模块在链接期不可用」，文案直接点名缺失能力
// （12 §6.14 要求报错指向缺失能力，而不是一个笼统的 Cannot find module）。
func (l *moduleLoader) matrixError(spec string) error {
	id := l.script.pluginID()
	trimmed := strings.TrimPrefix(spec, "node:")
	if !strings.HasPrefix(spec, "node:") && strings.Contains(trimmed, "/") && !knownBare(trimmed) {
		// 带斜杠又不是已知名：当作 npm 包路径处理。
		return npmSpecifierError(id, spec)
	}
	m, ok := nodemod.Lookup(spec)
	if !ok {
		return npmSpecifierError(id, spec)
	}
	switch m.Tier {
	case nodemod.TierNever:
		return fmt.Errorf("host: 插件 %s 导入 %s 被拒绝：该模块 v1 不安装（%s，12 §6.12.2）", id, spec, m.Deviation)
	case nodemod.TierCapability:
		if _, err := nodemod.Installable(spec, l.caps.Trust, l.caps.granted()); err != nil {
			return fmt.Errorf("host: 插件 %s 导入 %s 被拒绝：%v（清单 backend.capabilities 需要补 %q，06 §6.2）",
				id, spec, err, m.Capability)
		}
		return fmt.Errorf("host: 插件 %s 导入 %s：清单已授权 %q，但宿主尚未提供该模块实现（M1-D 安装点未接线，12 §6.11.1 %s）",
			id, spec, m.Capability, gapOr(m.Gap, "E-1"))
	default:
		return fmt.Errorf("host: 插件 %s 导入 %s：模块在矩阵里属于 %s 层，但宿主尚未提供实现（M1-D 待补，12 §6.11.1 E-1）",
			id, spec, m.Tier)
	}
}

// knownBare 判断裸名是否落在矩阵里（path、fs、fs/promises、timers/promises…）。
func knownBare(name string) bool {
	_, ok := nodemod.Lookup(name)
	return ok
}

func npmSpecifierError(id, spec string) error {
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return fmt.Errorf("host: 插件 %s 导入 %q：随包 Bundle 里没有这个模块（入口的相对依赖必须内联，06 §6.10）", id, spec)
	}
	return fmt.Errorf("host: 插件 %s 导入 %q：宿主不解析 node_modules，请把依赖在构建期打进 Bundle（05 §6.1、06 §6.10）", id, spec)
}

func gapOr(gap, fallback string) string {
	if gap != "" {
		return gap
	}
	return fallback
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
		name = entryName(l.script.pluginID())
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
	return set("path", strings.TrimPrefix(name, EntryScheme+"://"))
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

// installCapabilities 调用能力安装挂载点（由 caps.go 赋值）。
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
