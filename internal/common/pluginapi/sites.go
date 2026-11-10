package pluginapi

import "fmt"

// InstallSite 说明一个能力位的语义：脚本面在哪、边界由谁执行。
//
// 注意：**能力位已不再决定宿主装什么**（2026-11-10 决策）。这张表是给清单词汇与
// 契约测试用的说明性对照，不是安装开关；`adag verify capabilities` 门禁已删除，
// 只剩 conformance 里 `CheckCapabilitySites()` 的「表与白名单自洽」断言。
// 宿主对系统插件与第三方安装同一套桥与 node:* 模块。
type InstallSite struct {
	Capability    Capability
	HostInstall   string // 该能力对应的桥/模块（始终安装）
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
		EnforcedBy:    "Scope + 文件变更队列 + 检查点（02 §5.4）；越界写入调用即抛错",
		Milestone:     "M1-C",
	},
	CapNetFetch: {
		Capability: CapNetFetch, HostInstall: "安装出网代理的 fetch（当前不限目标主机）",
		ScriptSurface: "ctx.fetch",
		EnforcedBy:    "宿主桥：协议 http(s)、逐跳重定向 ≤5、响应体 ≤16MB、超时与取消（09 §9.5；host 策略待插件化）",
		Milestone:     "M1-C",
	},
	CapNetSSE: {
		Capability: CapNetSSE, HostInstall: "provider 用的流式 HTTP 桥",
		ScriptSurface: "ctx.http.sse / ctx.http.json",
		EnforcedBy:    "宿主统一执行 TLS、代理、建连 15s/空闲 90s/总 30min、取消与响应大小（5.5）",
		Milestone:     "M1-E",
	},
	CapExec: {
		Capability: CapExec, HostInstall: "child_process 的 execFile/execFileSync（无 shell）",
		ScriptSurface: "ctx.exec 与 node:child_process",
		EnforcedBy:    "Run.Allow 按程序名与参数检查；120s 超时、4MB 捕获、取消杀整棵进程树（09 §9.2、12 §6.13）",
		Milestone:     "M1-C/D",
	},
	CapProcess: {
		Capability: CapProcess, HostInstall: "长驻 stdin/stdout 子进程（无 shell）",
		ScriptSurface: "ctx.process.start/write/read/kill",
		EnforcedBy:    "ExecPolicy + cwd scope + env 白名单；每 Runtime 进程数/生命周期/输出上限；关闭杀整棵进程树（09 §9.2、12 §6.13）",
		Milestone:     "v2 MCP stdio",
	},
	CapTurnInvoke: {
		Capability: CapTurnInvoke, HostInstall: "安装 ctx.turn.invoke：在指定线程或新线程触发一轮对话（清单声明仅 builtin）",
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
		Capability: CapToolDiscovery, HostInstall: "ctx.catalog（目录只读投影）与 ctx.tool.invoke（内层工具执行桥）",
		ScriptSurface: "ctx.catalog.list({query?}) / ctx.tool.invoke({tool,args})",
		EnforcedBy:    "宿主按内层描述符重跑 schema/scope 校验 → Runner.Execute；脚本不能绕过超时/截断/循环检测（05 §6.3、06 §6.6）",
		Milestone:     "v0.3 系统插件化 P1",
	},
	CapDispatchSend: {
		Capability: CapDispatchSend, HostInstall: "安装 ctx.dispatch：把消息投递到指定工作区的调度层（清单声明仅 builtin）",
		ScriptSurface: "ctx.dispatch.send({workspace?, threadId?, title?, text?, mode?, idempotencyKey?})",
		EnforcedBy:    "宿主经回环控制端点投递，令牌由网关签发给本 Core 进程且插件不可见；sender 由宿主盖章，脚本不能伪造；请求体/超时受限；仅 builtin（07 §7.9、05 §6.3）",
		Milestone:     "v0.3 系统插件化 P2",
	},
	CapProcessSupervise: {
		Capability: CapProcessSupervise, HostInstall: "安装 ctx.processes：把插件的长驻进程交给网关 ProcessManager（清单声明仅 builtin）",
		ScriptSurface: "ctx.processes.register / ensure / stop / status / unregister",
		EnforcedBy:    "宿主把 entry 解析到插件目录内并拒绝越界；owner 由能力集盖章；启动形态只有 node（宿主 runtime）与显式 binary；禁止 shell；环境剥掉宿主凭据；每 owner/全局进程数与日志环上限（09 §9.2、06 §6.10）",
		Milestone:     "v0.3 系统插件化 P4",
	},
	CapState: {
		Capability: CapState, HostInstall: "安装按插件 + 线程命名空间隔离的受限 KV",
		ScriptSurface: "ctx.state.get/set/delete",
		EnforcedBy:    "StateStore 端口（SQLite plugin_state 表）；插件拿不到数据库句柄",
		Milestone:     "M1-B/C",
	},
	CapUIAsk: {
		Capability: CapUIAsk, HostInstall: "通用交互请求桥",
		ScriptSurface: "ctx.ui.confirm/select/input/notify",
		EnforcedBy:    "InteractionBroker：失败关闭，无 UI 解析为 unavailable、超时解析为 timeout（INV-4）",
		Milestone:     "M1-A（桥）+ M2（托盘）",
	},
	CapSubagentInvoke: {
		Capability: CapSubagentInvoke, HostInstall: "安装子智能体委派入口（清单声明仅 builtin，v1.5）",
		ScriptSurface: "ctx.subagent.invoke",
		EnforcedBy:    "复用同一 RunTurn，只换装配；子表里永远没有 invoke_subagent（04 §5.8）",
		Milestone:     "v1.5（v1 不装配）",
	},
	CapModelAdmin: {
		Capability: CapModelAdmin, HostInstall: "安装供应商目录读取与当前模型切换（清单声明仅 builtin）",
		ScriptSurface: "ctx.providers.list() / ctx.providers.select(providerId, modelId)",
		EnforcedBy:    "只读目录不含密钥；切换复用 model.set 的解析与线程校验，插件不能指定目录外模型",
		Milestone:     "M5",
	},
	CapPluginAdmin: {
		Capability: CapPluginAdmin, HostInstall: "安装全局插件安装桥：把骨架写入 ~/.adago/plugins/<id>/（清单声明仅 builtin）",
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
			return fmt.Errorf("pluginapi: 能力位 %q 没有安装消费者", c)
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
			return fmt.Errorf("pluginapi: 消费点表引用了未知能力位 %q", c)
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
