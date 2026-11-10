# 插件模型与后端 API

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§6.1 插件包形态、§6.2 清单、§6.3 后端插件 API  
> 版本：0.2 · 2026-11-09

## 6. 插件系统

插件分两类，**共用同一份清单、同一个 moejs 宿主、同一套 iAgentAPIi 与同一套宿主桥与审计**：

- **系统插件（system plugin）**：itrust=builtini，随产品发布，由产品声明启用；实现产品功能（工具、provider、策略、人格、cron、env、供应商管理、用量展示、MCP、浏览器）。禁用后基座不补位。
- **第三方插件（third-party plugin）**：用户安装；默认 itrust=third-partyi，可按签名升级为 iverifiedi。前端贡献面与系统插件相同（见 §6.2.4）。

基座只提供内核、通用原语与安全边界；**任何具体业务语义都不在基座里**。判断标准见 [00 §1.5](00-overview.md#15-基座边界scope-boundary)。

### 6.1 插件包形态

一个插件是一个目录，含 iplugin.jsoni 与一个后端入口/若干前端模板：

iii
plugins/my-plugin/
  plugin.json
  index.ts              # 后端入口（可选）
  slot.tsx              # 前端插槽（可选）
  page.tsx              # 独立页面（可选）
  static/
    my-plugin.css
    my-plugin.js        # 可选，需在清单声明 frontend.script
  skills/
    weekly-review/
      SKILL.md
iii

- 后端入口导出默认工厂函数 iexport default function (agent: AgentAPI, ctx?: AssemblyContext) {}i。
- 前端模板导出 iexport function Slot(props)i / iexport function Page(props)i。
- 一个包可以只声明 backend、只声明 frontend，或两者都有；两者由不同宿主加载（Core / Gateway），但共享同一个插件 id 与版本。
- 系统插件与第三方同形：iplugin.jsoni + iindex.tsi + 可选的 TSX；系统插件在构建期产出 idist/i，桌面打包随 iresources/plugins/officiali 发布；第三方在安装/加载时编译。不存在平行的 Go 插件包或 Go 注册入口。
- 发现路径：i<workspace>/.adago/plugins/<id>/i（工作区，仅旧目录兼容）与 i~/.adago/plugins/<id>/i（全局，生产统一安装路径）。工作区优先；同 id 冲突时工作区覆盖全局并给出警告。新安装一律写全局。
- 启用状态：全局 i~/.adago/plugins.jsoni；工作区 i.adago/plugins.jsoni 覆盖全局；两者都无条目时回落产品声明。旧工作区 i.adag/plugins.jsoni 与 SQLite iplugin_metai 的旧选择只读兼容/一次性迁移。
- v1 只从本地目录或 zip 安装；不做在线市场，不做运行时 npm 解析。插件依赖必须在构建时打进产物，或使用宿主提供的模块（见 [12](12-node-compat.md)）。

### 6.2 清单

iiijson
{
  "id": "task-plan",
  "name": "任务卡片",
  "version": "0.1.0",
  "apiVersion": 1,
  "description": "把 tools-plan 的计划渲染成会话区右上角的悬浮卡",
  "trust": "third-party",
  "frontend": {
    "capabilities": ["backend.read"],
    "slots": [
      { "slot": "chat.corner", "template": "slot.tsx", "order": 10 }
    ],
    "styles": ["task-plan.css"],
    "assets": "static",
    "actions": {
      "plan": { "action": "plan", "read": true, "backend": "tools-plan" }
    }
  }
}
iii

后端字段（可与前端字段同包）：

iiijson
{
  "backend": {
    "entry": "index.ts",
    "capabilities": ["fs.read", "net.fetch"],
    "env": ["PATH"]
  }
}
iii

规则：

- 后端部分（可选）：ibackend.entryi 默认 iindex.tsi，所有后端插件（系统与第三方）都必须提供入口。系统插件在构建期预编译成 idist/i；第三方在安装/加载时编译。ibackend.capabilitiesi 只做清单合法性校验（名字必须已知、不能重复、不能被低于 iMinTrusti 的信任级别声明）；它**不再决定装什么**（2026-11-10 决策）。ibackend.hooksi 声明使用哪些点位与最高动作等级（未声明的点位在调用时拒绝）。不声明 ibackendi 即纯前端插件。
- ibackend.initTimeoutMsi 是入口工厂（含异步 Promise 结算）的装配预算，缺省 5000、上限 60000 毫秒；超过 5000 仅 builtin/verified 可声明。它只放宽装配期发现预算（如 MCP 的 itools/listi），超时仍按插件装配失败处理，不影响 Core 启动。
- ibackend.configNamespacei 是 ictx.configi 的机器级命名空间，缺省等于插件 id；仅声明 iglobal.configi 时有效，用于多个系统插件共享同一份配置（如 iprovider-configi）。显式 i--data-diri 时命名空间落在该数据目录的 iconfig/i 下。
- ibackend.envi 是环境变量白名单：只有列出的名字出现在 iprocess.envi，值在装配期从宿主环境取值并冻结（组合根可显式注入覆盖，例如测试）；宿主环境里不存在的名字不注入（iundefinedi），不伪造空串。密钥不得写入环境变量，仍走 isecreti。
- 环境变量模块（系统插件 ienv-manageri 经 iglobal.configi 管理的命名空间）会注入插件启动的**子进程**（ictx.execi、后台任务、ictx.process.starti），同名覆盖宿主环境；它不进入插件 iprocess.envi（那里仍按 ibackend.envi 白名单），值不写日志。变量名需匹配 i^[A-Za-z_][A-Za-z0-9_]*$i，iADAG_i 前缀保留。
- ictx.processi（长驻 stdin/stdout、可 kill 进程树）始终装上；它不是 inode:child_processi 的替代，也不装 ispawni/iforki。每 Runtime 最多 8 个、单进程生命周期 30m。
- Node 内置模块不再按能力裁剪：宿主对系统插件与第三方装同一套模块。inode:fsi 的**写成员仍固定工作区根**，inode:child_processi 只有 iexecFilei（无 shell），inode:osi/inode:processi 是受限子集；inode:http(s)i/inode:neti/inode:dnsi/inode:worker_threadsi/inode:vmi 不安装。完整矩阵见 [12 §6.12](12-node-compat.md)。
- ibackend.rolesi 字段保留只为清单兼容：**角色集合已为空**（审批策略移除后没有内置角色语义），声明任何非空角色都会装配失败。
- iapiVersioni 主版本不匹配直接拒绝加载；次版本兼容，宿主忽略未知字段并记录。
- iidi 必须匹配 i^[a-z][a-z0-9-]{1,63}$i，且等于目录名。
- itrusti：ibuiltini（系统插件，随 resources 发布，只能启用/禁用）、iverifiedi（签名有效）、ithird-partyi（默认）。**来源决定信任上限**：磁盘插件最高 ithird-partyi；ibuiltini 只接受随包/官方目录；iverifiedi 必须经配置公钥验签，失败降级为 ithird-partyi 并记 warning。来源上限与 Ed25519 验签已落地：磁盘 iverifiedi 必须用已配置公钥验签，失败降级 ithird-partyi；ibuiltini 仍只接受随包/官方目录。
- 前端可选 iclienti 与贡献级 ihydratei：iclienti 是 iassetsi 目录内的浏览器 bundle，islots[].hydratei / ipages[].hydratei 声明该贡献在浏览器接管；缺省不启用，旧插件行为不变。声明 iclienti/ihydratei/iclientOnlyi 需要 ifrontend.scripti 能力；**第三方与系统插件同面**，不再按 trust 拒绝（风险控制见 §6.2.4 与 [09](09-security-operations.md)）。
- 动作绑定的 ibackendi 可以指向另一个插件的后端动作；目标插件/动作必须存在，跨插件只传 JSON 结果，不共享内存。
- 清单的每个字段都必须有真实消费者；iadag verifyi 逐字段核对，否则构建失败。

#### 6.2.1 能力位（只剩清单校验）

> 2026-11-10 决策后：能力位**不再决定「装什么」**。宿主对系统插件与第三方安装同一套 `ctx.*` 桥与 `node:*` 模块，不再按清单裁剪模块、桥或成员；链接期也不再因缺能力而报 `ERR_ACCESS_DENIED`。仍然生效的只有清单层的声明校验：名字必须在这张表里、不能重复、不能被低于「最低信任」的信任级别声明（`internal/common/pluginapi/contract.go` 的 `ValidateCapabilityList`）。

后端 17 项、前端 2 项。**能力位是清单词汇，不是产品功能**；没有一项叫 icroni、ienvi 或 iusagei。

| 能力 | 端 | 最低信任 | 历史脚本可见面（现在始终可见） | 仍生效的边界 |
|---|---|---|---|---|
| ifs.readi | 后端 | third-party | ictx.fs.readText/list/stat/existsi、inode:fsi 读成员 | 读路径**不限宿主目录**（2026-10-31 决策） |
| ifs.writei | 后端 | third-party | ictx.fs.writeTexti、inode:fsi 写成员 | 工作区根 + 已安装插件目录（可写 i~/.adago/plugins/<id>/i），symlink 逐段校验 |
| inet.fetchi | 后端 | third-party | ictx.fetch(url, init)i | 宿主 iHTTPBridgei：仅 http(s)、重定向 ≤5、响应 ≤16MB、超时/取消贯穿；无 host 黑白名单 |
| inet.ssei | 后端 | verified | ictx.http.json/ssei | 同桥；建连 15s / 空闲 90s / 总 30min |
| iexeci | 后端 | verified | ictx.execi、inode:child_processi 的 iexecFile/execFileSynci | 无 shell；cwd 限工作区；输出 4MB；超时 120s；取消杀进程树；程序级 iExecPolicyi 由装配层注入 |
| iprocessi | 后端 | verified | ictx.process.start/write/read/killi | 长驻 stdin/stdout；每 Runtime 8 个、单进程 30min |
| istatei | 后端 | third-party | ictx.state.get/set/deletei | SQLite iplugin_statei；按插件 + 线程命名空间 |
| iglobal.configi | 后端 | verified | ictx.config.get/set/delete/listi、ictx.config.workspace.*i | 机器级（i$ADAGO_HOME/config/<id>.jsoni 或 i~/.adago/config/<id>.jsoni）与工作区级（i<workspace>/.adago/config/<id>.jsoni）命名空间；0600、原子写、跨进程锁、限额 |
| ievents.readi | 后端 | third-party | ictx.events.query({fromSeq, types, threadId, limit})i、ictx.events.usage({threadId?, limit?})i | 只读事件/用量投影：Emitter 缓冲（线程内）+ 每轮原始用量行（工作区范围）；不能写事件、不能跨工作区；展示口径聚合归插件 |
| itool.discoveryi | 后端 | builtin | ictx.catalog.list({query?})i、ictx.tool.invoke({tool,args})i | 发现式工具宿主桥：目录只读投影 + 内层工具执行；宿主按内层描述符沿用同一套超时与循环检测 |
| iturn.invokei | 后端 | builtin | ictx.turn.invoke({threadId?, title?, prompt})i | 通用轮次触发桥：宿主建线程/生成 runId/后台执行，插件只拿受理回执；不授予工具/文件/网络权限；只在本 Core 内开一轮 |
| idispatch.sendi | 后端 | builtin | ictx.dispatch.send({workspace?, threadId?, title?, text?, mode?, idempotencyKey?, taskId?})i | 内部发送桥：交给网关调度层，可跨工作区，与界面发送共用同一份投递规则（busy/steer/waiting_input 只有一处判定）；令牌由网关签发给本 Core 进程且插件不可见，sender 由宿主盖章 |
| iprocess.supervisei | 后端 | builtin | ictx.processes.register/ensure/stop/status/unregister/listi | 进程监督桥：把插件长驻进程交给网关 ProcessManager 拉起、退避重启与回收；entry 由宿主解析到插件目录内（越界/绝对路径拒绝），owner 由宿主盖章，无 stdin/stdout 句柄，环境剥掉宿主凭据 |
| iui.aski | 后端 | verified | ictx.ui.confirm/select/input/form/notifyi | 通用交互桥；无 UI 时解析为 iunavailablei，由调用方决定方向 |
| isubagent.invokei | 后端 | builtin | ictx.subagent.invokei | v1.5；v1 不装配 |
| imodel.admini | 后端 | builtin | ictx.model.list/selecti | **只读模型目录 + 切换当前模型**；供应商配置 CRUD 走 iglobal.configi，不在本能力里 |
| iplugin.admini | 后端 | builtin | ictx.pluginAdmin.install/check/removei | 写全局插件根；宿主校验 id/相对路径/大小，拒绝覆盖 |
| ibackend.readi | 前端 | third-party | SSR 预取 iprops.data.backend.<id>.<binding>i | Gateway 渲染桥：≤16 个、并发 4、单个 300ms、缓存 8s |
| ifrontend.scripti | 前端 | third-party | i/ext/{id}/static/*i、hydrate、client bundle | CSP + nosniff + 安装确认；同源脚本风险见 [09](09-security-operations.md) |

发现式工具宿主桥是 itool.discoveryi（ictx.catalogi 目录只读投影 + ictx.tool.invokei 按名执行内层工具，沿用同一套超时与循环检测）。通用轮次触发桥 iturn.invokei（ictx.turn.invokei）已落地：宿主建线程/生成 runId/后台执行，插件只提交提示词并拿受理回执；新增桥必须同时提交消费者、审计与契约测试。内部发送桥 idispatch.sendi（ictx.dispatch.sendi）同样已落地：它把消息交给**网关调度层**（[07 §7.9](07-ssr-frontend.md#79-消息发送路径调度层)），可跨工作区，且与界面发送共用同一份投递规则；令牌由网关签给本 Core 进程并只经环境变量传递，插件既拿不到地址也拿不到凭据，sender 由宿主盖章。进程监督桥 iprocess.supervisei（ictx.processesi）也已落地：它把插件的长驻进程交给**网关 ProcessManager**（[06 §6.10](06-plugin-runtime.md)），跨会话存活、崩溃按策略退避重启、按引用计数回收；entry 由宿主解析到插件目录内并拒绝越界，owner 由宿主盖章，没有 stdin/stdout 句柄。
定时任务的调度已迁到单例进程：icron-manageri 装配期用 ictx.processesi 注册 ischeduleri（global、node、on_enable），由它解析 cron 并经 ictx.dispatch.sendi 同一条调度层投递；iagent.everyi 循环已从插件删除。
ienv.admini 已不存在：env 的变量读写/校验/管理页由 ienv-manageri 系统插件经 iglobal.configi 实现，基座只保留“向子进程注入已配置环境”的通用执行点。icron.admini 已删除：cron 的任务模型/调度/历史由 icron-manageri 经 iglobal.configi + iagent.everyi 通用定时器 + iturn.invokei 实现；基座不再读 icron.jsoni、不调度、不暴露 i/api/cron/firei。

> 实现状态：iglobal.configi 已落地（ictx.configi + iinternal/common/globalconfigi）；ievents.readi 已落地（ictx.events.queryi + Emitter 可回放缓冲，ictx.events.usagei + SQLite 原始用量行）；itoken-usagei/icomposer-telemetryi 已改为插件内聚合，基座已删除展示用 iusage.geti 投影，只保留 ievents.readi 的原始用量行；iturn.invokei 轮次触发桥与 iagent.everyi 通用定时器已落地；cron 已完成迁移。idispatch.sendi 已落地：网关侧 iinternal/gateway/dispatchi 同时服务界面发送与 ictx.dispatch.sendi，内部端点只认回环来源 + 控制令牌，且摘掉了宿主环境里的 iADAG_i/iADAGO_i 变量（插件子进程拿不到宿主凭据）。路线见 [11 §16](11-structure-roadmap.md)。

#### 6.2.2 信任级别

- ibuiltini：系统插件；只能启用/禁用，不能被磁盘同名插件覆盖；运行时仍走 moejs。
- iverifiedi：imanifest.sigi 为 Ed25519 签名，覆盖清单 + 文件哈希；公钥在配置里。签名失败按第三方处理并提示。
- ithird-partyi：默认；能力声明受 §6.2.1 的名字/最低信任校验，前端贡献面与系统插件相同，安装时必须展示声明的能力与脚本风险并取得确认。宿主装什么已不再由信任级别决定。

信任不是自声明：idiscoveryi/iappi 必须执行“来源 → 信任上限”；没有验签就不得把磁盘插件当 iverifiedi/ibuiltini。

#### 6.2.3 前端贡献

系统插件与第三方插件都可以声明：

- islotsi：插槽 + TSX 模板 + 排序；
- ipagesi：i/ext/{id}/{slug}i 独立页面；
- inavi：侧栏导航项；
- idashboardi：概览页卡片；
- iblocksi：Markdown iadag-uii 动态块；
- iactionsi：读/写后端动作绑定；写动作必须有 iconfirmi；
- iclienti / ihydratei / iclientOnlyi：客户端 bundle 与服务端/客户端接管；
- istylesi / iscriptsi / iassetsi：静态资源；
- ibackend.readi：SSR 只读预取。

规则：

- 前端能力只由 ifrontend.capabilitiesi 声明，不按 itrusti 剥掉页面/插槽/脚本；ithird-partyi 不被特殊裁剪为“只有 blocks”。
- ifrontend.scripti 是脚本/客户端 bundle 的声明门禁；安装时展示“该插件可在浏览器同源执行 JS”，用户确认后才装配。
- 第三方前端仍受 CSP、VNode 白名单、CSRF、动作确认与审计约束；详见 [07 §7.5](07-ssr-frontend.md#75-前端插件化) 与 [09 §9.3](09-security-operations.md)。

#### 6.2.4 系统插件不是基座

系统插件与第三方使用同一契约；它们不是“基座内置代码”：

- 基座源码不出现系统插件 id；
- 禁用系统插件后，工具/页面/导航/后台行为消失；
- 系统插件的业务状态与逻辑由插件自己拥有（例如 cron 任务模型、env 校验、供应商 CRUD、用量聚合）；
- 基座只提供通用原语与失败关闭门禁。

### 6.3 后端插件 API

后端插件从虚拟模块 iagent:apii 导入类型；运行时能力由宿主注入，插件不直接持有任何 IO 句柄。

iiits
export default function (agent: AgentAPI, ctx?: AssemblyContext): void | Promise<void>;

interface AgentAPI {
  registerTool(def: ToolDef): void;
  registerProvider(def: ProviderDef): void; // 仅 builtin/verified，见 5.5
  registerPrompt(def: { id: string; content: string; priority?: number }): void;
  registerAction(def: ActionDef): void;
  route(method: "GET" | "POST", path: string, handler: RouteHandler): void;
  route(def: RouteDef): void;
  // 通用周期任务：1000…3600000ms，每个插件最多 8 条；handler 每次收到新的 ctx。
  every(ms: number, handler: (req: unknown, ctx: RouteContext) => unknown): void;
  on<K extends HookPoint>(point: K, fn: HookHandler<K>, opts?: HookOpts): void;
}
iii

iagent.everyi 只是通用定时器，不解释 cron 表达式、不存任务、不记录运行历史；这些语义都属于使用它的插件（当前是 icron-manageri）。

装配期上下文（入口工厂第二参数）是刻意收窄的子集：iworkspacei/isettingsi/ilogi/isignali/ifsi（只读）/ifetchi/isecreti/iprocessi，**没有** istatei/iuii/iexeci/ifs.writeTexti/iconfigi/ieventsi。

#### 6.3.1 ToolContext

iiits
type TaskSnapshot = {
  taskId: number; pid: number;
  status: "running" | "exited" | "timeout" | "killed" | "failed";
  code: number; stdout: string; stderr: string;
  truncated: boolean; stdoutBytes: number; stderrBytes: number;
  startedAt: string; endedAt?: string; error?: string;
};

type ProviderInfo = {
  id: string; displayName: string; baseUrl?: string;
  active: boolean; hasApiKey: boolean; keyTail?: string;
  models: ProviderModel[];
};
type ProviderModel = {
  id: string; displayName?: string; contextWindow: number;
  maxOutput?: number; supportsTools: boolean; supportsThinking?: boolean;
};
type ModelRef = { providerId: string; modelId: string };

interface UIContext {
  available: boolean;
  confirm(title: string, message: string, opts?: { timeoutMs?: number; signal?: AbortSignal }): Promise<boolean | null>;
  select(title: string, options: string[], opts?: { timeoutMs?: number; signal?: AbortSignal }): Promise<string | null>;
  input(title: string, placeholder?: string, opts?: { timeoutMs?: number; signal?: AbortSignal }): Promise<string | null>;
  notify(text: string, level?: "info" | "warn" | "error"): void;
}

interface ActionDef {
  name: string; label: string; mode: "read" | "write";
  confirm?: string; timeoutMs?: number;
  execute(input: { threadId: string; [key: string]: unknown }, ctx: ActionContext): Promise<unknown> | unknown;
}
interface ActionContext {
  workspace: string; now: string; signal: AbortSignal;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  state?: { get<T>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>; delete(key: string): Promise<void> };
  config?: { get<T>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>; delete(key: string): Promise<void>; list(prefix?: string): Promise<Array<{ key: string; value: unknown }>> };
  secret?(name: string): string | null;
  ui?: UIContext;
}

interface HookContext {
  workspace: string; threadId: string; runId: string; now: string; signal: AbortSignal;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  ui?: UIContext;
  state?: { get<T>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>; delete(key: string): Promise<void> };
  config?: { get<T>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>; delete(key: string): Promise<void>; list(prefix?: string): Promise<Array<{ key: string; value: unknown }>> };
  settings?: Record<string, unknown>;
}

interface RouteDef { method: "GET" | "POST"; path: string; sse?: boolean; handler(req: RouteRequest, ctx: ToolContext): unknown; }
interface RouteRequest { method: "GET" | "POST"; path: string; query: Record<string, string>; body?: unknown; }
iii

iToolContexti（宿主始终注入下列成员；i// capability:i 注释是清单词汇的历史标注，不再决定「有没有」）：

iiits
interface ToolContext {
  workspace: string;
  threadId: string;                  // 当前线程；route 调用由 Gateway 透传
  now: string;                       // RFC3339
  signal: AbortSignal;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  fs?: {                             // capability: fs.read / fs.write
    readText(path: string, opts?: { maxBytes?: number }): Promise<string>;
    writeText(path: string, text: string): Promise<{ bytes: number }>;
    list(path: string): Promise<Array<{ name: string; dir: boolean; size: number }>>;
    stat(path: string): Promise<{ size: number; mtime: string; dir: boolean }>;
    exists(path: string): Promise<boolean>;
  };
  fetch?: (url: string, init?: { method?: string; headers?: object; body?: string; timeoutMs?: number }) => Promise<{ status: number; headers: object; text(): Promise<string> }>; // capability: net.fetch
  exec?: (cmd: string, args: string[], opts?: { cwd?: string; timeoutMs?: number }) => Promise<{ code: number; stdout: string; stderr: string }>; // capability: exec，仅 verified/builtin
  process?: {                        // capability: process
    start(cmd: string, args: string[], opts?: { cwd?: string; env?: string[]; timeoutMs?: number }): Promise<{
      pid: number; taskId: number;
      write(data: string): void;
      read(opts?: { maxBytes?: number; timeoutMs?: number }): Promise<TaskSnapshot>;
      kill(): void;
    }>;
  };
  execBackground?: (cmd: string, args: string[], opts?: { cwd?: string; timeoutMs?: number }) => Promise<{ taskId: number; pid: number }>; // capability: exec
  checkTask?: (taskId: number) => TaskSnapshot; // capability: exec
  waitTask?: (taskId: number, timeoutMs?: number) => Promise<TaskSnapshot & { timedOut: boolean; waitedMs: number }>; // capability: exec
  killTask?: (taskId: number) => { taskId: number; found: boolean; killed: boolean }; // capability: exec
  state?: {                          // capability: state
    get<T>(key: string): Promise<T | null>;
    set(key: string, value: unknown): Promise<void>;
    delete(key: string): Promise<void>;
  };
  config?: {                         // capability: global.config
    path(): string;
    get<T>(key: string): Promise<T | null>;
    set(key: string, value: unknown): Promise<void>;
    delete(key: string): Promise<void>;
    list(prefix?: string): Promise<Array<{ key: string; value: unknown }>>;
    workspace?: {
      path(): string;
      get<T>(key: string): Promise<T | null>;
      set(key: string, value: unknown): Promise<void>;
      delete(key: string): Promise<void>;
      list(prefix?: string): Promise<Array<{ key: string; value: unknown }>>;
    };
  };
  events?: {                         // capability: events.read
    query(q?: { fromSeq?: number; types?: string[]; threadId?: string; limit?: number }): Promise<Array<{
      seq: number; type: string; threadId?: string; runId?: string; at: string; data: unknown;
    }>>;
    // 每轮原始用量行；threadId 省略时是当前工作区全部，基座不做展示口径聚合。
    usage(q?: { threadId?: string; limit?: number }): Promise<Array<{
      threadId: string; provider: string; model: string;
      inputTokens: number; outputTokens: number; cacheTokens: number; costMicros: number; at: string;
    }>>;
  };
  model?: {                          // capability: model.admin，仅 builtin
    list(): Promise<{ providers: ProviderInfo[]; current: ModelRef }>;
    select(providerId: string, modelId: string): Promise<ModelRef>;
  };
  turn?: {                           // capability: turn.invoke，仅 builtin
    invoke(req: { threadId?: string; title?: string; prompt: string }): Promise<{ threadId: string; runId: string }>;
  };
  dispatch?: {                       // capability: dispatch.send，仅 builtin
    send(req: {
      workspace?: string; threadId?: string; title?: string; text: string;
      mode?: "auto" | "start" | "steer"; idempotencyKey?: string; taskId?: string;
    }): Promise<{ workspace: string; threadId: string; runId?: string; mode: string }>;
  };
  processes?: {                      // capability: process.supervise，仅 builtin
    register(spec: {
      name: string; scope?: "global" | "workspace"; runtime?: "node" | "binary";
      entry: string; args?: string[]; env?: Record<string, string>;
      start?: "on_enable" | "lazy"; restart?: "no" | "on_failure" | "always";
      stopTimeoutMs?: number;
    }): Promise<ProcessStatus>;
    ensure(name: string, scope?: string): Promise<ProcessStatus>;
    stop(name: string, scope?: string): Promise<ProcessStatus>;
    status(name: string, scope?: string): Promise<ProcessStatus | undefined>;
    unregister(name: string, scope?: string): Promise<void>;
    list(): Promise<ProcessStatus[]>;
  };
  sse?: { send(data: unknown, event?: string): void }; // 仅 sse:true 的 route 处理器内存在
  secret?(name: string): string | null; // 仅在 manifest 声明 secrets 时存在
  ui?: UIContext;                    // capability: ui.ask，仅 builtin/verified
  settings?: Record<string, unknown>; // 插件自己的设置（[plugins.settings.<id>]，基座不解释）
}
iii

iconfigi 是**机器级、插件命名空间隔离**的持久化：key 只在本插件命名空间可读写；宿主负责 0600、原子写与跨进程锁。ieventsi 是只读投影，字段脱敏，不能跨工作区。两者都不得用于绕过 istatei/ifsi 的 scope。

#### 6.3.2 动作、路由与钩子

动作（Action）与工具（Tool）是两条通道，不能混用：

- **工具**给模型调用，进入工具目录，受 scope 与超时约束（执行前没有审批钩子）。
- **动作**只给界面调用（iPOST /ext/{id}/action/{name}i），不进入模型上下文。读动作可以在 SSR 时预取；写动作必须用户点击，并支持 iconfirmi 文案。
- **动态路由**（iroutei）给插件自己的 HTTP API 用：iGET/POST /ext/{id}/api/*i，查询串与 JSON 请求体由宿主传入；返回 JSON，响应大小上限 64KB。isse: truei 的路由只允许 GET，处理器用 ictx.sse.send(data, event?)i 推帧。
- **钩子**只用于生命周期观察；控制面动作（ireplacei/iaborti）仅 builtin 系统插件可用。itool.beforeCalli 点位已随审批策略删除（2026-11-10 决策），执行前没有可阻塞的钩子。需要替换内核机制的，不走钩子，走 [06 §6.10](06-plugin-runtime.md#610-内核实现与替换-seam) 的 seam。

#### 6.3.3 系统插件如何实现业务功能

| 系统插件 | 使用的通用原语 | 自己拥有的逻辑 |
|---|---|---|
| icron-manageri | iglobal.configi、iprocessi/iexeci、通用轮次触发桥（builtin） | cron 表达式解析、任务模型、调度循环、运行历史、到期触发 |
| ienv-manageri | iglobal.configi、ipagei/iactioni、iprocessi 注入点 | env 命名空间格式、校验、管理页、作用域覆盖 |
| iprovider-admini | iglobal.configi、imodel.admini、inet.fetchi | 供应商 CRUD（ictx.configi）、i/modelsi 拉取（ictx.fetchi）、密钥尾号展示 |
| itoken-usagei / icomposer-telemetryi | ievents.readi、ibackend.readi、iclientOnlyi | 聚合口径、页面/卡片、轮询节奏、上下文环 |
| itool-discoveryi | itool.discoveryi（builtin） | 目录只读投影、按名执行、结果上浮 |
| imcp-clienti | inet.fetchi、iprocessi、istatei | MCP transport、工具映射、连接生命周期 |

（ipolicy-defaulti 已删除：审批策略不再由插件提供，基座也没有对等替代。）

这些插件由产品声明启用；基座不读取它们的配置格式，也不提供同名兜底。

#### 6.3.4 工具管理集中化（ToolManager）

工具不是各层各管一段，而是由 **Core 侧的基座内部组件 iToolManageri**（iinternal/common/toolkiti）统一负责：

| 职责 | 说明 |
|---|---|
| 注册与摘除 | iRegisterOwner(owner, descriptor)i / iRemoveOwner(pluginID)i；owner 由宿主按清单盖章，声明与 owner 不一致直接拒绝 |
| 描述符校验 | 名字规则、保留名（ifind_tooli/iexec_tooli）、Schema、access、idiscoveryi 与 iterminatesTurni 互斥 |
| 可见性视图 | iInjectedi（模型声明）/ iDiscoveryi（ifind_tooli）/ iSystemi（保留入口）/ iAlli（itool.listi、诊断） |
| 快照 | 每个 turn 取一份不可变快照，模型声明、执行查找与循环检测同源；中途禁用不让已发出的声明与执行目录错位 |
| 执行路由 | 归属以目录为准：ipluginIDi 与描述符不一致时拒绝，不把调用投给另一个插件 |

可见性规则只有一处实现（idomain.ToolVisibilityi），模型声明、ictx.catalog.listi 与 iexec_tooli 的内层校验都问它，避免出现「ifind_tooli 列出的工具」与「iexec_tooli 能执行的工具」两套口径。

iToolManageri 刻意**不做**授权：执行前的 scope 校验、超时、回执与检查点仍只在引擎一份（审批策略已移除，没有执行前拒绝）；它也不加载插件、不理解 cron/任务等业务语义。itool-discoveryi 仍是模型可见的入口，背后由 iToolManageri 提供目录与路由。

---

**相邻文档**：[← 会话、存储与子智能体](04-sessions-storage.md) · [索引](design.md) · [插件运行时与治理 →](06-plugin-runtime.md)
