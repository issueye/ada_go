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
		Capability: CapNetFetch, HostInstall: "安装出网代理的 fetch（当前不限目标主机）",
		ScriptSurface: "ctx.fetch",
		EnforcedBy:    "宿主桥：协议 http(s)、逐跳重定向 ≤5、响应体 ≤16MB、超时与取消（09 §9.5；host 策略待插件化）",
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
	CapProcess: {
		Capability: CapProcess, HostInstall: "安装长驻 stdin/stdout 子进程（无 shell，仅 builtin/verified）",
		ScriptSurface: "ctx.process.start/write/read/kill",
		EnforcedBy:    "ExecPolicy + cwd scope + env 白名单；每 Runtime 进程数/生命周期/输出上限；关闭杀整棵进程树（09 §9.2、12 §6.13）",
		Milestone:     "v2 MCP stdio",
	},
	CapTurnInvoke: {
		Capability: CapTurnInvoke, HostInstall: "安装 ctx.turn.invoke：在指定线程或新线程触发一轮对话（仅 builtin）",
		ScriptSurface: "ctx.turn.invoke({threadId?, title?, prompt})",
		EnforcedBy:    "宿主建线程/生成 runId/执行轮次；插件只提交提示词，不等待轮次结果；不授予工具/文件/网络权限；仅 builtin",
		Milestone:     "v0.3 系统插件化 P2",
	},
	CapGlobalConfig: {
		Capability: CapGlobalConfig, HostInstall: "安装 ctx.config：机器级、按插件 id 命名空间隔离的 JSON 配置存储",
		ScriptSurface: "ctx.config.get/set/delete/list",
		EnforcedBy:    "固定路径（$ADAGO_HOME/config/<id>.json）、0600 原子写、跨进程文件锁、单值/总量/条目限额；仅 verified/builtin（09 §9.5）",
		Milestone:     "v0.3 系统插件化 P1",
	},
	CapEventsRead: {
		Capability: CapEventsRead, HostInstall: "安装 ctx.events.query：线程上下文内的只读事件/用量投影",
		ScriptSurface: "ctx.events.query({fromSeq, types, threadId, limit})",
		EnforcedBy:    "只读 Emitter 缓冲 + 线程上下文限制；不能写事件、不能跨线程/工作区；字段脱敏（09 §9.5）",
		Milestone:     "v0.3 系统插件化 P1",
	},
	CapToolDiscovery: {
		Capability: CapToolDiscovery, HostInstall: "安装 ctx.catalog（目录只读投影）与 ctx.tool.invoke（内层工具执行桥），仅 builtin",
		ScriptSurface: "ctx.catalog.list({query?}) / ctx.tool.invoke({tool,args})",
		EnforcedBy:    "宿主按内层描述符重跑 tool.beforeCall → policy 失败关闭 → Runner.Execute；脚本不能绕过审批/超时/截断；仅 builtin（05 §6.3、06 §6.6）",
		Milestone:     "v0.3 系统插件化 P1",
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
	CapModelAdmin: {
		Capability: CapModelAdmin, HostInstall: "安装供应商目录读取与当前模型切换（仅 builtin）",
		ScriptSurface: "ctx.providers.list() / ctx.providers.select(providerId, modelId)",
		EnforcedBy:    "只读目录不含密钥；切换复用 model.set 的解析与线程校验，插件不能指定目录外模型",
		Milestone:     "M5",
	},
	CapPluginAdmin: {
		Capability: CapPluginAdmin, HostInstall: "安装全局插件安装桥：把骨架写入 ~/.adago/plugins/<id>/（仅 builtin）",
		ScriptSurface: "ctx.pluginAdmin.install({ id, files: [{ path, content }] })",
		EnforcedBy:    "宿主校验 id/相对路径/大小上限，拒绝覆盖已有插件；不授予任意文件写权限",
		Milestone:     "M5",
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
