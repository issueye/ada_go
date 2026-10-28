package plugins

import "fmt"

// InstallSite 说明一个能力位被谁消费：宿主装什么、脚本里出现什么、边界由谁执行。
//
// 「能力先声明、后授予」的落地方式（设计 0 摘要第 3 条、6.4）：没声明的能力在脚本里根本不存在，
// 不是运行时再判断。adag verify 用这张表确认每个能力位都有真实消费者，
// 也确认没有「声明了却没人安装」的孤儿能力。
type InstallSite struct {
	Capability    Capability
	HostInstall   string // 宿主在安装阶段做的事
	ScriptSurface string // 脚本里可见的入口（agent:host 属性或 node: 模块）
	EnforcedBy    string // 真实边界的执行点
	Milestone     string // 生产实现落地的里程碑
}

var installSites = map[Capability]InstallSite{
	CapFSRead: {
		Capability: CapFSRead, HostInstall: "以工作区为根、只读地安装受限 fs",
		ScriptSurface: "ctx.fs 与 node:fs / node:fs/promises",
		EnforcedBy:    "Scope：先字符串检查再 EvalSymlinks，按真实落点拒绝越界（09 §9.2）",
		Milestone:     "M1-C",
	},
	CapFSWrite: {
		Capability: CapFSWrite, HostInstall: "在同一根上放开写成员，并把写路径纳入变更队列",
		ScriptSurface: "ctx.fs.writeText 与 node:fs 的写成员",
		EnforcedBy:    "Scope + 文件变更队列 + 检查点（02 §5.4）；未声明时写成员访问即抛 ERR_ACCESS_DENIED",
		Milestone:     "M1-C",
	},
	CapNetFetch: {
		Capability: CapNetFetch, HostInstall: "安装 allowlist 代理的 fetch",
		ScriptSurface: "ctx.fetch",
		EnforcedBy:    "宿主 allowlist：协议、主机、端口、逐跳重定向与响应体上限（09 §9.5）",
		Milestone:     "M1-C",
	},
	CapNetSSE: {
		Capability: CapNetSSE, HostInstall: "安装 provider 用的流式 HTTP 桥（仅 builtin/verified）",
		ScriptSurface: "ctx.http.sse / ctx.http.json",
		EnforcedBy:    "宿主统一执行 TLS、代理、建连 15s/空闲 90s/总 30min、取消与响应大小（5.5）",
		Milestone:     "M1-E",
	},
	CapExec: {
		Capability: CapExec, HostInstall: "安装 child_process 的 execFile/execFileSync（无 shell，仅 builtin/verified）",
		ScriptSurface: "ctx.exec 与 node:child_process",
		EnforcedBy:    "Run.Allow 按程序名与参数检查；120s 超时、4MB 捕获、取消杀整棵进程树（09 §9.2、12 §6.13）",
		Milestone:     "M1-C/D",
	},
	CapState: {
		Capability: CapState, HostInstall: "安装按插件 + 线程命名空间隔离的受限 KV",
		ScriptSurface: "ctx.state.get/set/delete",
		EnforcedBy:    "StateStore 端口（SQLite plugin_state 表）；插件拿不到数据库句柄",
		Milestone:     "M1-B/C",
	},
	CapUIAsk: {
		Capability: CapUIAsk, HostInstall: "安装通用交互请求桥（仅 builtin/verified）",
		ScriptSurface: "ctx.ui.confirm/select/input/notify",
		EnforcedBy:    "InteractionBroker：失败关闭，无 UI 解析为 unavailable、超时解析为 timeout（INV-4）",
		Milestone:     "M1-A（桥）+ M2（托盘）",
	},
	CapSubagentInvoke: {
		Capability: CapSubagentInvoke, HostInstall: "安装子智能体委派入口（仅 builtin，v1.5）",
		ScriptSurface: "ctx.subagent.invoke",
		EnforcedBy:    "复用同一 RunTurn，只换装配；子表里永远没有 invoke_subagent（04 §5.8）",
		Milestone:     "v1.5（v1 不装配）",
	},
	CapBackendRead: {
		Capability: CapBackendRead, HostInstall: "Gateway 侧预取只读后端动作结果（最多 16 个、并发 4、单个 300ms、缓存 8s）",
		ScriptSurface: "props.data.backend.<插件 id>.<binding>",
		EnforcedBy:    "只读动作白名单 + CSRF/Origin + 结果上限（7.5）",
		Milestone:     "M4",
	},
	CapFrontendScript: {
		Capability: CapFrontendScript, HostInstall: "Gateway 侧允许加载插件自带 JS（默认关闭，仍受 CSP 限制）",
		ScriptSurface: "/ext/{id}/static/* 与 /static/*",
		EnforcedBy:    "CSP 无内联脚本 + 信任级别 builtin/verified + nosniff（09 §9.3、7.5）",
		Milestone:     "M4（v1 默认不加载第三方 JS）",
	},
}

// InstallSiteFor 返回能力位的消费点。
func InstallSiteFor(c Capability) (InstallSite, bool) {
	s, ok := installSites[c]
	return s, ok
}

// InstallSites 返回全部消费点（按能力名升序，便于 diff 与文档生成）。
func InstallSites() []InstallSite {
	out := make([]InstallSite, 0, len(installSites))
	for _, s := range installSites {
		out = append(out, s)
	}
	sortSites(out)
	return out
}

// CheckCapabilitySites 确认能力白名单与消费点表完全对齐（无孤儿、无多余）。
func CheckCapabilitySites() error {
	for _, c := range append(BackendCapabilities(), FrontendCapabilities()...) {
		if _, ok := installSites[c]; !ok {
			return fmt.Errorf("plugins: 能力位 %q 没有安装消费者", c)
		}
	}
	for c := range installSites {
		known := false
		for _, x := range append(BackendCapabilities(), FrontendCapabilities()...) {
			if x == c {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("plugins: 消费点表引用了未知能力位 %q", c)
		}
	}
	return nil
}

func sortSites(sites []InstallSite) {
	for i := 1; i < len(sites); i++ {
		for j := i; j > 0 && sites[j-1].Capability > sites[j].Capability; j-- {
			sites[j-1], sites[j] = sites[j], sites[j-1]
		}
	}
}
