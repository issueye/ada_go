package host

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	moejs "github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/node"

	"github.com/adag/ada-go/internal/plugins"
	hostnode "github.com/adag/ada-go/internal/plugins/host/node"
	"github.com/adag/ada-go/internal/plugins/host/nodemods"
)

// 本文件是 12 §6.11.1 E-1 的落点：模块白名单由宿主 resolver 执行（上游只有整组开关）。
//
// 三条规则（12 §6.12.1(1)、§6.12.3、D-7）：
//  1. `node:*` 与裸内置名一律先归一，再按「本次实际安装的集合」判定；裸名不会另开一条路。
//  2. 未声明能力的 `node:fs` / `node:child_process` 在链接期失败，错误里带
//     ERR_ACCESS_DENIED 与缺失的能力名；明确不安装的模块与未知模块报 ERR_MODULE_NOT_FOUND。
//  3. `.node` 原生扩展一律 ERR_NATIVE_ADDON_UNSUPPORTED（12 §6.10「明确不是」）。

// 模块名（归一后，不带 node: 前缀）。
const (
	moduleProcess       = "process"
	moduleChildProcess  = "child_process"
	moduleAgentHost     = "agent:host"
	moduleFS            = "fs"
	moduleFSPromises    = "fs/promises"
	moduleBuffer        = "buffer"
	moejsSpecifierAlias = "node:"
)

// nodeBuiltins 是 Node 20 的内置模块名清单（裸名形式）。归一时用它判断「这是内置名还是相对路径」。
//
// 名单里的名字一律按 node:<name> 处理，因此 import "events" 与 import "node:events"
// 得到同一个判定（D-7 E-1：上游让裸内置名也能解析，白名单必须自己覆盖这条路）。
var nodeBuiltins = map[string]bool{
	"assert": true, "async_hooks": true, "buffer": true, "child_process": true,
	"cluster": true, "console": true, "constants": true, "crypto": true,
	"dgram": true, "diagnostics_channel": true, "dns": true, "domain": true,
	"events": true, "fs": true, "fs/promises": true, "http": true, "http2": true,
	"https": true, "inspector": true, "inspector/promises": true, "module": true,
	"net": true, "os": true, "path": true, "path/posix": true, "path/win32": true,
	"perf_hooks": true, "process": true, "punycode": true, "querystring": true,
	"readline": true, "repl": true, "stream": true, "stream/consumers": true,
	"stream/promises": true, "stream/web": true, "string_decoder": true,
	"sys": true, "test": true, "test/reporters": true, "timers": true,
	"timers/promises": true, "tls": true, "trace_events": true, "url": true,
	"util": true, "util/types": true, "v8": true, "vm": true, "wasi": true,
	"webhooks": true, "worker_threads": true, "zlib": true,
	// Node 22 起新增、ada-go 明确不安装的成员（12 §6.12.2「不安装」表）。
	"sqlite": true, "sea": true, "test/mock": true,
}

// provider 说明一个已安装模块由谁实现（对账与偏差记录用）。
type provider string

const (
	// providerUpstream：moejs 的 node 包安装，宿主只加包装层。
	providerUpstream provider = "moejs/node"
	// providerHost：moejs 没有这个模块，由 ada-go 宿主提供（E-5 等）。
	providerHost provider = "ada-go/host"
)

// ModulePolicy 是「这个插件实际能 import 哪些模块」的判定器。
//
// 它由 CapabilitySet 唯一决定，因此 resolver（链接期）与 caps 安装（求值期、运行期）
// 各持一个实例也得到同一份答案；虚模块本身是进程级编译一次（与上游 node 包一致），
// 每 Runtime 的差别只在 import.meta 填的 binding。
type ModulePolicy struct {
	caps     *CapabilitySet
	installs map[string]provider
	denials  map[string]*policyError // 归一名 → 链接期错误（缓存，便于对账测试读取）
	// limits 是构造时的上限快照：判定器与安装层读同一份，避免同一次装配里两套阈值。
	limits Limits
}

// NewModulePolicy 按能力集合构造白名单。caps 为空时只安装「无外部效果」的面。
func NewModulePolicy(caps *CapabilitySet) *ModulePolicy {
	if caps == nil {
		caps = &CapabilitySet{Trust: plugins.TrustThirdParty, Backend: map[plugins.Capability]bool{}}
	}
	p := &ModulePolicy{caps: caps, limits: currentLimits(), installs: map[string]provider{}, denials: map[string]*policyError{}}
	p.build()
	return p
}

// backendCaps 把 CapabilitySet 摊平成 node 包需要的切片（顺序稳定，便于错误文案一致）。
func (c *CapabilitySet) backendCaps() []plugins.Capability {
	if c == nil {
		return nil
	}
	out := make([]plugins.Capability, 0, len(c.Backend))
	for cap, ok := range c.Backend {
		if ok {
			out = append(out, cap)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// build 走一遍安装矩阵：TierNever 与未授予能力的模块不装；
// 矩阵里「始终安装」但 moejs/宿主还没有实现的，也不装（宁缺毋滥，12 §6.13 失败方向）。
func (p *ModulePolicy) build() {
	caps := p.caps.backendCaps()
	for _, m := range hostnode.Modules() {
		switch m.Tier {
		case hostnode.TierNever:
			p.denials[m.Name] = notFound(m.Name,
				fmt.Sprintf("模块 node:%s 不安装（12 §6.12.2 不安装表：%s）", m.Name, m.Deviation))
			continue
		case hostnode.TierCapability:
			if !hostnode.CapsAllow(m, caps) {
				p.denials[m.Name] = accessDenied(m.Name, m.Capability,
					fmt.Sprintf("模块 node:%s 需要能力 %s，清单未声明（12 §6.12.1(1)：链接期失败，不等第一次调用）",
						m.Name, capsHint(m)))
				continue
			}
			if m.MinTrust != "" && !trustAtLeast(p.caps.Trust, m.MinTrust) {
				p.denials[m.Name] = accessDenied(m.Name, m.Capability,
					fmt.Sprintf("模块 node:%s 需要至少 %q 信任级别（当前 %q），能力 %q 不予安装（05 §6.2）",
						m.Name, m.MinTrust, p.caps.Trust, m.Capability))
				continue
			}
		}
		if prov, ok := p.provides(m.Name); ok {
			p.installs[m.Name] = prov
			continue
		}
		// 矩阵要求安装、但上游与宿主都还没有实现：不装，并把缺口写进错误文案。
		p.denials[m.Name] = notFound(m.Name,
			fmt.Sprintf("模块 node:%s 尚未提供（上游缺口 %s，宿主未实现前不冒充兼容）", m.Name, orNone(m.Gap)))
	}
	p.installs[moduleAgentHost] = providerHost
}

// provides 报告本包能否真的装出这个模块。
func (p *ModulePolicy) provides(name string) (provider, bool) {
	switch name {
	case moduleFS, moduleFSPromises, moduleBuffer:
		return providerUpstream, true
	case moduleProcess, moduleChildProcess:
		return providerHost, true
	}
	// 宿主自建模块（path/events/util/crypto）：实现了才算「能装」，没实现就落到
	// build() 的 denial 里，链接期点名「上游缺口 + 宿主未实现」，不冒充兼容。
	if _, ok := nodemods.Source(name); ok {
		return providerHost, true
	}
	return "", false
}

// Installed 返回实际安装的归一模块名（含 agent:host），排序后便于断言与对账。
func (p *ModulePolicy) Installed() []string {
	out := make([]string, 0, len(p.installs))
	for name := range p.installs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Provider 报告某个已安装模块由谁实现（未安装返回空）。
func (p *ModulePolicy) Provider(name string) provider { return p.installs[TrimModulePrefix(name)] }

// MissingFor 返回某模块未安装的原因（已安装返回空串）。对账测试用它区分
// 「能力缺失」「明确不装」「上游缺口」。
func (p *ModulePolicy) MissingFor(name string) string {
	if err, ok := p.denials[TrimModulePrefix(name)]; ok {
		return err.Error()
	}
	return ""
}

// Normalize 把 specifier 归一成矩阵用的名字：node:fs 与 fs 都得到 fs。
// 第二个返回值说明它是否是内置名（相对路径、plugin:// 与包内联模块为 false）。
func Normalize(spec string) (name string, builtin bool) {
	if spec == moduleAgentHost {
		return moduleAgentHost, true
	}
	name = TrimModulePrefix(spec)
	if name != spec {
		return name, true // 明确写了 node: 前缀
	}
	if nodeBuiltins[spec] {
		return spec, true
	}
	return spec, false
}

// TrimModulePrefix 去掉 node: 前缀（与 node.TrimPrefix 同义，这里避免到处引包名）。
func TrimModulePrefix(spec string) string {
	if strings.HasPrefix(spec, moejsSpecifierAlias) {
		return strings.TrimPrefix(spec, moejsSpecifierAlias)
	}
	return spec
}

// IsNativeAddon 报告 specifier 是否在尝试加载 .node 原生扩展。
func IsNativeAddon(spec string) bool {
	return strings.HasSuffix(strings.ToLower(spec), ".node")
}

// Allow 是链接期与动态 import 的唯一判定：返回 nil 表示放行。
func (p *ModulePolicy) Allow(spec string) error {
	if IsNativeAddon(spec) {
		return &policyError{
			Code:    codeNativeAddon,
			Message: "ada-go 不支持 .node 原生扩展（N-API/node-gyp/process.dlopen 一律不存在，12 §6.10「明确不是」）",
			Fields:  map[string]any{"path": baseForLog(spec)},
		}
	}
	name, builtin := Normalize(spec)
	if !builtin {
		return nil // 相对路径、plugin:// 与包内联模块交给宿主的模块表
	}
	if _, ok := p.installs[name]; ok {
		return nil
	}
	if err, ok := p.denials[name]; ok {
		return err
	}
	return &policyError{
		Code:    codeModuleNotFound,
		Message: fmt.Sprintf("未知内置模块 %q：不在 ada-go 的安装矩阵里（12 §6.12.2）", spec),
		Fields:  map[string]any{"specifier": spec},
	}
}

// Resolver 把策略挂在宿主解析器外面：先判白名单，再交给下一层。
//
// moejs 的 Link 与动态 import 都走这里，因此「未声明能力的 node:fs」在链接期就装配失败，
// 而不是等第一次调用（12 §6.12.1(1)）。
func (p *ModulePolicy) Resolver(next moejs.Resolver) moejs.Resolver {
	inner := node.Resolver(next) // 上游只服务 fs/fs/promises/buffer，其余原样转给宿主
	return func(referrer moejs.Referrer, spec string) (*moejs.Module, error) {
		if err := p.Allow(spec); err != nil {
			return nil, err
		}
		if src, ok := hostModuleSource(spec); ok {
			m, err := compileVirtualModule(TrimModulePrefix(spec), src)
			if err != nil {
				return nil, err
			}
			return m, nil
		}
		if inner != nil {
			return inner(referrer, spec)
		}
		return nil, &policyError{
			Code:    codeModuleNotFound,
			Message: fmt.Sprintf("宿主模块表没有 %q（依赖必须在构建期由 esbuild 内联，12 §6.12.3）", spec),
			Fields:  map[string]any{"specifier": spec},
		}
	}
}

// Importer 用策略包住宿主的 Importer：动态 import() 与 import.meta 都经过白名单。
//
// runtime.go 构造 Runtime 时应这样接线（能力安装只认 rt 句柄，拿不到 Importer）：
//
//	imp := NewModulePolicy(caps).Importer(hostImporter)
//	rt := moejs.NewRuntime(moejs.Options{Importer: imp, MemoryLimit: MemoryLimit, ...})
func (p *ModulePolicy) Importer(next *moejs.Importer) *moejs.Importer {
	imp := &moejs.Importer{}
	var resolve moejs.Resolver
	if next != nil {
		resolve, imp.Meta = next.Resolve, next.Meta
	}
	imp.Resolve = p.Resolver(resolve)
	return imp
}

// virtualModules 是宿主自建模块的进程级缓存（与上游 node 包同一口径：
// 一个模块一份 *moejs.Module，每 Runtime 用 import.meta 填自己的 binding）。
var virtualModules sync.Map // 归一名 → *moejs.Module

// compileVirtualModule 编译（一次）宿主自建的模块。
func compileVirtualModule(name, source string) (*moejs.Module, error) {
	if m, ok := virtualModules.Load(name); ok {
		return m.(*moejs.Module), nil
	}
	m, err := moejs.Compile(name, source)
	if err != nil {
		return nil, fmt.Errorf("host: 编译宿主模块 %s 失败: %w", name, err)
	}
	actual, _ := virtualModules.LoadOrStore(name, m)
	return actual.(*moejs.Module), nil
}

// hostModuleSource 按 specifier 找宿主自建的虚模块源码。
// 裸名与 node: 前缀归一到同一份，因此解析到的也是同一个 *moejs.Module。
func hostModuleSource(spec string) (string, bool) {
	name, builtin := Normalize(spec)
	if !builtin {
		return "", false
	}
	switch name {
	case moduleProcess:
		return processShim, true
	case moduleChildProcess:
		return childProcessShim, true
	case moduleAgentHost:
		return agentHostShim, true
	}
	if src, ok := nodemods.Source(name); ok {
		return src, true
	}
	return "", false
}

// trustAtLeast 是信任级别序（与 node.Installable 同一口径，本地实现避免反向依赖）。
func trustAtLeast(have, need plugins.Trust) bool {
	rank := map[plugins.Trust]int{plugins.TrustThirdParty: 0, plugins.TrustVerified: 1, plugins.TrustBuiltin: 2}
	h, ok1 := rank[have]
	n, ok2 := rank[need]
	if !ok1 || !ok2 {
		return false
	}
	return h >= n
}

func orNone(s string) string {
	if s == "" {
		return "无"
	}
	return s
}

// capsHint 说明模块需要的能力名：fs 是 fs.read / fs.write 的「或」关系（node.CapsAllow 同口径）。
func capsHint(m hostnode.Module) string {
	if m.Name == moduleFS || m.Name == moduleFSPromises {
		return `"fs.read"/"fs.write"`
	}
	return fmt.Sprintf("%q", string(m.Capability))
}

// accessDenied 构造「能力缺失」的链接期错误：code 是 ERR_ACCESS_DENIED，
// 字段带 capability（12 §6.12.4），消息里同时出现模块名与缺失的能力名。
func accessDenied(module string, cap plugins.Capability, message string) *policyError {
	return &policyError{
		Code:    codeAccessDenied,
		Message: message,
		Fields:  map[string]any{"capability": string(cap), "module": module},
	}
}

// notFound 构造「明确不安装 / 尚未提供」的链接期错误。
func notFound(module, message string) *policyError {
	return &policyError{
		Code:    codeModuleNotFound,
		Message: message,
		Fields:  map[string]any{"specifier": "node:" + module, "module": module},
	}
}

// Limits 是能力安装的字节与数量上限（09 §9.5、12 §6.13）。
//
// 超限一律报错而不是截断；进模型回执的 50KB/2000 行截断是另一层（02 §5.4），不在这里。
type Limits struct {
	// MaxFileBytes 是 fs 单次读写上限（16MB）。
	MaxFileBytes int
	// MaxDirEntries 是单次目录列举条目上限（10k）。
	MaxDirEntries int
	// MaxPathBytes 是路径字节上限（4096）。
	MaxPathBytes int
	// MaxFetchBytes 是 ctx.http 响应上限（16MB）。
	MaxFetchBytes int
	// MaxExecOutputBytes 是 child_process 捕获上限（4MB）。
	MaxExecOutputBytes int
	// MaxExecTimeout 是 child_process 的超时上限（120s）：脚本给更大值按本值夹住。
	MaxExecTimeout time.Duration
	// MaxTimers 是每 Runtime 未完成定时器上限（256）。
	MaxTimers int
}

// DefaultLimits 返回 09 §9.5 表格里的默认值。
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:       16 << 20,
		MaxDirEntries:      10000,
		MaxPathBytes:       4096,
		MaxFetchBytes:      16 << 20,
		MaxExecOutputBytes: 4 << 20,
		MaxExecTimeout:     120 * time.Second,
		MaxTimers:          256,
	}
}

// limitsMu 保护 limits：上限本身是宿主配置，测试用小阈值才能几毫秒跑完 16MB/10k 的边界。
var limitsMu sync.Mutex

var limits = DefaultLimits()

// SetLimits 覆盖全局上限（装配期一次；测试用小阈值）。零值字段回落默认值。
func SetLimits(l Limits) {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	limits = mergeLimits(l, DefaultLimits())
}

// currentLimits 读当前上限快照。
func currentLimits() Limits {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	return limits
}

func mergeLimits(have, fallback Limits) Limits {
	if have.MaxFileBytes <= 0 {
		have.MaxFileBytes = fallback.MaxFileBytes
	}
	if have.MaxDirEntries <= 0 {
		have.MaxDirEntries = fallback.MaxDirEntries
	}
	if have.MaxPathBytes <= 0 {
		have.MaxPathBytes = fallback.MaxPathBytes
	}
	if have.MaxFetchBytes <= 0 {
		have.MaxFetchBytes = fallback.MaxFetchBytes
	}
	if have.MaxExecOutputBytes <= 0 {
		have.MaxExecOutputBytes = fallback.MaxExecOutputBytes
	}
	if have.MaxExecTimeout <= 0 {
		have.MaxExecTimeout = fallback.MaxExecTimeout
	}
	if have.MaxTimers <= 0 {
		have.MaxTimers = fallback.MaxTimers
	}
	return have
}
