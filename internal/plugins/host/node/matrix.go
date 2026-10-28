// Package node 是插件 Node 兼容层的契约表：安装矩阵、错误码与上游缺口。
//
// 它是「API 形状的子集，不是 Node 运行时」（设计 6.10）。本包只携带数据与校验，
// 不注册工具、不提供 provider、不读产品配置、不出现任何官方插件 id（6.10 末尾）。
// 真正的 stdlib 安装在 M1-D/M3 由宿主按这张表映射到 moejs 的安装配置。
package node

import (
	"fmt"

	"github.com/adag/ada-go/internal/plugins"
)

// Tier 是模块的安装层级（6.12.2 的四张表）。
type Tier string

const (
	TierAlways     Tier = "always"     // 纯计算，无外部效果，始终安装
	TierLoop       Tier = "loop"       // 需要定时器 Loop
	TierCapability Tier = "capability" // 按能力位安装（受限 IO）
	TierNever      Tier = "never"      // 明确不安装
)

// ProvidedBy 说明一个模块由谁实现（M1-D 尖刺实测：见 internal/conformance/nodecompat/deviations.md）。
//
// 事实：mojs v0.1.0-alpha.11 的 node 包只提供 fs 与 buffer。设计 6.12.1 的 stdlib.Config
// 形状来自 go-quickjs 时期，其余「始终安装」的模块实际都要 ada-go 宿主自建 virtual module。
// 这不是措辞问题而是工作量归属：标 ProvidedHost 的每一个都是一份实现与一份语料。
type ProvidedBy string

const (
	// ProvidedUpstream：上游已提供，宿主只做配置、上限与审计包装。
	ProvidedUpstream ProvidedBy = "upstream"
	// ProvidedHost：上游没有该模块，宿主自建（仍按 node: 名字暴露，保持形状兼容）。
	ProvidedHost ProvidedBy = "host"
	// ProvidedNotInstalled：v1 不安装（TierNever，或缺口未落地前明确不可用）。
	ProvidedNotInstalled ProvidedBy = "not-installed"
)

// hostImplemented 是「上游没有、必须 ada-go 自己写」的模块集合。
//
// 依据是语料 proc-*/cz-*/tm-* 的 LINK-FAIL 实测；已提供上游实现的是 buffer 与 fs/fs/promises。
var hostImplemented = map[string]bool{
	"console": true, "path": true, "path/posix": true, "path/win32": true,
	"url": true, "querystring": true, "string_decoder": true, "events": true,
	"util": true, "assert": true, "crypto": true, "zlib": true,
	"stream/web": true, "process": true, "os": true, "module": true,
	"timers": true, "child_process": true,
}

// upstreamProvided 是上游 node 包确实装出来的模块。
var upstreamProvided = map[string]bool{"buffer": true, "fs": true, "fs/promises": true}

// ProvidedByOf 判定一个模块条目的实现归属。
func ProvidedByOf(m Module) ProvidedBy {
	if m.Tier == TierNever {
		return ProvidedNotInstalled
	}
	if upstreamProvided[m.Name] {
		return ProvidedUpstream
	}
	if hostImplemented[m.Name] || m.Tier == TierCapability {
		// 受限 IO：fs 系上游有、child_process 没有；child_process 已在 hostImplemented 里。
		return ProvidedHost
	}
	return ProvidedNotInstalled
}

// HostImplementationQueue 返回必须 ada-go 自己写的模块（M1-D 的交付清单）。
func HostImplementationQueue() []string {
	var out []string
	for _, m := range modules {
		if ProvidedByOf(m) == ProvidedHost {
			out = append(out, m.Name)
		}
	}
	return out
}

// Module 描述一个 node: 模块的安装契约。
type Module struct {
	Name string
	Tier Tier
	// Capability 是 TierCapability 所需能力位（其它层级为空）。
	Capability plugins.Capability
	// MinTrust 是该模块可用的最低信任级别（不限制时为空）。
	MinTrust plugins.Trust
	// Scope 说明真实边界由谁执行。
	Scope string
	// Deviation 是与 Node 20 的已知偏差（6.12.2「偏差与边界」列）。
	Deviation string
	// Gap 是依赖的上游待补项（6.11.1），可空。
	Gap string
}

// modules 是安装矩阵的唯一声明。
var modules = []Module{
	// 始终安装
	{Name: "console", Tier: TierAlways, Scope: "映射到结构化日志；不写进程 stdout", Deviation: "长参数按 util.inspect 截断，避免日志放大"},
	{Name: "path", Tier: TierAlways, Deviation: "path.resolve 以虚拟根 /（工作区）为基准"},
	{Name: "path/posix", Tier: TierAlways},
	{Name: "path/win32", Tier: TierAlways},
	{Name: "url", Tier: TierAlways, Deviation: "fileURLToPath 只接受 plugin:// 与 file:// 虚拟路径"},
	{Name: "querystring", Tier: TierAlways},
	{Name: "string_decoder", Tier: TierAlways, Deviation: "仅 UTF-8；其它编码抛 ERR_ENCODING_NOT_SUPPORTED"},
	{Name: "events", Tier: TierAlways, Deviation: "error 事件无监听时抛出，符合 Node"},
	{Name: "util", Tier: TierAlways, Deviation: "inspect 深度与颜色由宿主固定，不读取终端能力"},
	{Name: "assert", Tier: TierAlways, Deviation: "错误消息不保证逐字一致；AssertionError 字段一致"},
	{Name: "buffer", Tier: TierAlways, Deviation: "allocUnsafe 仍清零，不暴露未初始化内存"},
	{Name: "crypto", Tier: TierAlways, Deviation: "无对称加解密、无非对称 sign/verify、无 X.509/KeyObject；随机源由宿主注入"},
	{Name: "zlib", Tier: TierAlways, Deviation: "无 brotli/zstd；解压产物夹到 16MB"},
	{Name: "stream/web", Tier: TierAlways, Deviation: "没有 Node 的 stream 模块；pipe 用 pipeTo/pipeThrough"},
	{Name: "process", Tier: TierAlways, Scope: "cwd 是虚拟根 /；env 只含 backend.env 白名单", Deviation: "exit 抛异常；没有 kill/binding/dlopen/setuid/umask"},
	{Name: "os", Tier: TierAlways, Deviation: "hostname/homedir 为空；没有 networkInterfaces/userInfo/loadavg"},
	{Name: "module", Tier: TierAlways, Gap: "E-3", Deviation: "createRequire 只解析已安装的 node:*；不读磁盘、没有 require.cache"},

	// 需要 Loop
	{Name: "timers", Tier: TierLoop, Deviation: "setImmediate 以 0ms 定时器实现，与 Node 的 check 阶段顺序不完全相同"},
	{Name: "timers/promises", Tier: TierLoop, Gap: "E-8", Deviation: "signal 取消与 setInterval 清理保证待补"},

	// 按能力安装
	{
		Name: "fs", Tier: TierCapability, Capability: plugins.CapFSRead,
		Scope:     "根固定为工作区，脚本视图 /；symlink 越界按真实落点拒绝",
		Deviation: "单次读写 16MB、目录 10k 条；不提供回调形式；v1 不开放 watch、文件描述符、chmod/chown/utimes",
		Gap:       "E-4",
	},
	{
		Name: "fs/promises", Tier: TierCapability, Capability: plugins.CapFSRead,
		Scope: "与 fs 同一实例、同一根", Deviation: "同 fs；写成员在未声明 fs.write 时访问即抛 ERR_ACCESS_DENIED", Gap: "E-4",
	},
	{
		Name: "child_process", Tier: TierCapability, Capability: plugins.CapExec, MinTrust: plugins.TrustVerified,
		Scope:     "只有 execFile/execFileSync，不经过 shell；Run.Allow 再按程序名与参数检查",
		Deviation: "spawn/fork/exec/execSync 不存在；捕获上限 4MB、超时 120s、取消杀整棵进程树", Gap: "E-5",
	},

	// 不安装
	{Name: "http", Tier: TierNever, Deviation: "出网走 ctx.fetch（net.fetch）或 ctx.http（net.sse）"},
	{Name: "https", Tier: TierNever, Deviation: "同 http"},
	{Name: "net", Tier: TierNever, Deviation: "没有监听与原始 socket"},
	{Name: "tls", Tier: TierNever, Deviation: "TLS 由宿主单点执行"},
	{Name: "dgram", Tier: TierNever, Deviation: "没有 UDP 面；出网只能经 ctx.fetch / ctx.http"},
	{Name: "dns", Tier: TierNever, Deviation: "DNS 由宿主执行，插件不可控"},
	{Name: "worker_threads", Tier: TierNever, Deviation: "并行工具由 Core 调度；委派走 subagent.invoke（v1.5）"},
	{Name: "cluster", Tier: TierNever, Deviation: "进程编排属于 Gateway 监督层，不是插件能力"},
	{Name: "vm", Tier: TierNever, Deviation: "不开放；第三方 Runtime 关闭代码生成"},
	{Name: "inspector", Tier: TierNever, Deviation: "调试走宿主日志与 adag doctor"},
	{Name: "repl", Tier: TierNever, Deviation: "不开放"},
	{Name: "wasi", Tier: TierNever, Deviation: "不开放；沙箱由能力位与 Scope 定义"},
	{Name: "async_hooks", Tier: TierNever, Deviation: "需要请求上下文时用 ctx 显式传递；AsyncLocalStorage 到 v1.1 再评估"},
	{Name: "perf_hooks", Tier: TierNever, Deviation: "只用全局 performance.now"},
	{Name: "sqlite", Tier: TierNever, Deviation: "持久化用 ctx.state；会话库只属于 Core"},
	{Name: "test", Tier: TierNever, Deviation: "插件测试在构建期用 Node 跑；assert 仍可用"},
}

// Modules 返回安装矩阵（只读副本）。
func Modules() []Module { return append([]Module(nil), modules...) }

// Lookup 按模块名找条目（接受 "fs" 与 "node:fs" 两种写法）。
func Lookup(name string) (Module, bool) {
	clean := TrimPrefix(name)
	for _, m := range modules {
		if m.Name == clean {
			return m, true
		}
	}
	return Module{}, false
}

// TrimPrefix 去掉 node: 前缀。
func TrimPrefix(name string) string {
	const p = "node:"
	if len(name) >= len(p) && name[:len(p)] == p {
		return name[len(p):]
	}
	return name
}

// Installable 报告在某信任级别与能力集合下，该模块是否可安装。
func Installable(name string, trust plugins.Trust, caps []plugins.Capability) (Module, error) {
	m, ok := Lookup(name)
	if !ok {
		return Module{}, fmt.Errorf("node: 未知模块 %q，链接期失败（ERR_MODULE_NOT_FOUND）", name)
	}
	if m.Tier == TierNever {
		return m, fmt.Errorf("node: 模块 %q 不安装；替代品见 6.12.2（%s）", name, m.Deviation)
	}
	if m.Capability != "" && !hasCap(caps, m.Capability) {
		return m, fmt.Errorf("node: 模块 %q 需要能力 %q，清单未声明（ERR_ACCESS_DENIED）", name, m.Capability)
	}
	if m.MinTrust != "" && !trustAtLeast(trust, m.MinTrust) {
		return m, fmt.Errorf("node: 模块 %q 需要至少 %q 信任级别，当前 %q", name, m.MinTrust, trust)
	}
	return m, nil
}

// fs.write 也让 fs 可用：能力是「或」关系，所以单独判断。
func hasCap(caps []plugins.Capability, want plugins.Capability) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func trustAtLeast(have, need plugins.Trust) bool {
	order := map[plugins.Trust]int{plugins.TrustThirdParty: 0, plugins.TrustVerified: 1, plugins.TrustBuiltin: 2}
	return order[have] >= order[need]
}

// CapsAllow 报告清单能力集合能否安装该模块（fs 接受 fs.read 或 fs.write）。
func CapsAllow(m Module, caps []plugins.Capability) bool {
	if m.Capability == "" {
		return true
	}
	if m.Name == "fs" || m.Name == "fs/promises" {
		return hasCap(caps, plugins.CapFSRead) || hasCap(caps, plugins.CapFSWrite)
	}
	return hasCap(caps, m.Capability)
}

// ErrorCode 是 Node 兼容层的错误码契约（6.12.4）。
type ErrorCode struct {
	Code     string
	Trigger  string
	Fields   []string
	Category string // syscall-shaped | ada-shaped
}

// errorCodes 逐条照 6.12.4 表格。
var errorCodes = []ErrorCode{
	{"ENOENT", "路径不存在", []string{"syscall", "path"}, "syscall-shaped"},
	{"EACCES", "权限不足", []string{"syscall", "path"}, "syscall-shaped"},
	{"EPERM", "只读文件系统或操作不被允许", []string{"syscall", "path"}, "syscall-shaped"},
	{"EISDIR", "路径是目录而非文件", []string{"syscall", "path"}, "syscall-shaped"},
	{"ENOTDIR", "路径不是目录", []string{"syscall", "path"}, "syscall-shaped"},
	{"EEXIST", "目标已存在", []string{"syscall", "path", "dest"}, "syscall-shaped"},
	{"ENOTEMPTY", "目录非空", []string{"syscall", "path"}, "syscall-shaped"},
	{"ENOSPC", "磁盘满", []string{"syscall", "path"}, "syscall-shaped"},
	{"EMFILE", "句柄耗尽", []string{"syscall", "path"}, "syscall-shaped"},
	{"ETIMEDOUT", "命令或宿主调用超时", []string{"syscall"}, "syscall-shaped"},
	{"ABORT_ERR", "AbortSignal 取消", []string{"name=AbortError"}, "ada-shaped"},
	{"ERR_INVALID_ARG_TYPE", "参数类型错误", []string{"name=TypeError"}, "ada-shaped"},
	{"ERR_OUT_OF_RANGE", "参数范围错误", []string{"name=RangeError"}, "ada-shaped"},
	{"ERR_ACCESS_DENIED", "能力缺失、作用域越界、成员被裁剪", []string{"capability"}, "ada-shaped"},
	{"ERR_MODULE_NOT_FOUND", "模块不存在或未安装", []string{"specifier"}, "ada-shaped"},
	{"ERR_NATIVE_ADDON_UNSUPPORTED", "尝试加载 .node 原生扩展", []string{"path"}, "ada-shaped"},
	{"ERR_FS_FILE_TOO_LARGE", "超过单次读写上限", []string{"size", "limit"}, "ada-shaped"},
	{"ERR_CHILD_PROCESS_STDIO_MAXBUFFER", "命令输出超过 maxBuffer", []string{"limit"}, "ada-shaped"},
	{"ERR_TIMER_LIMIT", "定时器超过每 Runtime 上限", []string{"limit"}, "ada-shaped"},
	{"ERR_ENCODING_NOT_SUPPORTED", "非 UTF-8 编码", []string{"encoding"}, "ada-shaped"},
}

// ErrorCodes 返回错误码表。
func ErrorCodes() []ErrorCode { return append([]ErrorCode(nil), errorCodes...) }

// Gap 是 moejs 上游待补项（6.11.1）。
type Gap struct {
	ID          string
	Capability  string
	BlocksM1    bool
	Disposition string // 当前应对：上游优先 / 固定 fork 兜底 / 能力标记不可用
}

// Gaps 返回 E-1…E-8；E-1/E-4/E-5 是 M1 阻断项（6.11.1、开发计划 §8）。
func Gaps() []Gap {
	return []Gap{
		{"E-1", "模块白名单/拒绝列表（细于整组 NoWebAPIs 开关）", true, "上游优先；未补齐前只装更窄的模块集合，不冒充兼容"},
		{"E-2", "定时器 unref/ref、每 Runtime 数量上限、全局 setImmediate", false, "上游优先；宿主用 256 上限包装"},
		{"E-3", "node:module 的 createRequire（只解析已安装 node:*）", false, "上游优先；未补齐前 CJS 依赖只允许包内联"},
		{"E-4", "fs 的 Node 错误码与字段、只读时静态裁剪写成员", true, "上游优先；未补齐前不开放 fs 能力位给第三方"},
		{"E-5", "child_process 的 shell 开关与按函数安装", true, "上游优先；未补齐前 exec 能力只走 ctx.exec，不装 node:child_process"},
		{"E-6", "每次调用的文件大小、目录条目数上限", false, "宿主包装兜底 16MB/10k"},
		{"E-7", "宿主侧 Buffer/字符串计入或独立配额", false, "宿主分配上限 + 进程级 GOMEMLIMIT 兜底"},
		{"E-8", "timers/promises 的 signal 取消与 setInterval 清理保证", false, "上游优先；长等待改由宿主侧期限负责"},
	}
}

// BlockingGaps 返回 M1 阻断项清单，供开发计划 §8 的触发器跟踪。
func BlockingGaps() []Gap {
	var out []Gap
	for _, g := range Gaps() {
		if g.BlocksM1 {
			out = append(out, g)
		}
	}
	return out
}

// CheckMatrix 自检：能力位必须是已声明的后端能力；TierNever 不得带能力；
// 阻断缺口必须落在真实模块上。这是 adag verify nodecompat 的一部分。
func CheckMatrix() error {
	for _, m := range modules {
		if m.Tier == TierNever && m.Capability != "" {
			return fmt.Errorf("node: 模块 %q 标记为不安装却声明了能力 %q", m.Name, m.Capability)
		}
		if m.Tier == TierCapability {
			if m.Capability == "" {
				return fmt.Errorf("node: 模块 %q 是 capability 层级却没有能力位", m.Name)
			}
			known := false
			for _, c := range plugins.BackendCapabilities() {
				if c == m.Capability {
					known = true
				}
			}
			if !known && m.Capability != plugins.CapFSWrite {
				return fmt.Errorf("node: 模块 %q 引用了未知能力 %q", m.Name, m.Capability)
			}
		}
		if m.Gap != "" {
			found := false
			for _, g := range Gaps() {
				if g.ID == m.Gap {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("node: 模块 %q 引用了未知上游缺口 %q", m.Name, m.Gap)
			}
		}
	}
	return nil
}
