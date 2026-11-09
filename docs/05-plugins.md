# 插件模型与后端 API

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§6.1 插件包形态、§6.2 清单、§6.3 后端插件 API  
> 版本：0.2 · 2026-11-09

## 6. 插件系统

插件分两类，**共用同一份清单、同一个 moejs 宿主、同一套 `AgentAPI` 与同一套能力/审计**：

- **系统插件（system plugin）**：`trust=builtin`，随产品发布，由产品声明启用；实现产品功能（工具、provider、策略、人格、cron、env、供应商管理、用量展示、MCP、浏览器）。禁用后基座不补位。
- **第三方插件（third-party plugin）**：用户安装；默认 `trust=third-party`，可按签名升级为 `verified`。前端贡献面与系统插件相同（见 §6.2.4）。

基座只提供内核、通用原语与安全边界；**任何具体业务语义都不在基座里**。判断标准见 [00 §1.5](00-overview.md#15-基座边界scope-boundary)。

### 6.1 插件包形态

一个插件是一个目录，含 `plugin.json` 与一个后端入口/若干前端模板：

```
plugins/my-plugin/
  plugin.json
  index.ts              # 后端入口（可选）
  slot.tsx              # 前端插槽（可选）
  page.tsx              # 独立页面（可选）
  static/
    my-plugin.css
    my-plugin.js        # 可选，需声明 frontend.script 能力
  skills/
    weekly-review/
      SKILL.md
```

- 后端入口导出默认工厂函数 `export default function (agent: AgentAPI, ctx?: AssemblyContext) {}`。
- 前端模板导出 `export function Slot(props)` / `export function Page(props)`。
- 一个包可以只声明 backend、只声明 frontend，或两者都有；两者由不同宿主加载（Core / Gateway），但共享同一个插件 id 与版本。
- 系统插件与第三方同形：`plugin.json` + `index.ts` + 可选的 TSX；系统插件在构建期产出 `dist/`，桌面打包随 `resources/plugins/official` 发布；第三方在安装/加载时编译。不存在平行的 Go 插件包或 Go 注册入口。
- 发现路径：`<workspace>/.adago/plugins/<id>/`（工作区，仅旧目录兼容）与 `~/.adago/plugins/<id>/`（全局，生产统一安装路径）。工作区优先；同 id 冲突时工作区覆盖全局并给出警告。新安装一律写全局。
- 启用状态：全局 `~/.adago/plugins.json`；工作区 `.adago/plugins.json` 覆盖全局；两者都无条目时回落产品声明。旧工作区 `.adag/plugins.json` 与 SQLite `plugin_meta` 的旧选择只读兼容/一次性迁移。
- v1 只从本地目录或 zip 安装；不做在线市场，不做运行时 npm 解析。插件依赖必须在构建时打进产物，或使用宿主提供的模块（见 [12](12-node-compat.md)）。

### 6.2 清单

```json
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
```

后端字段（可与前端字段同包）：

```json
{
  "backend": {
    "entry": "index.ts",
    "capabilities": ["fs.read", "net.fetch"],
    "env": ["PATH"]
  }
}
```

规则：

- 后端部分（可选）：`backend.entry` 默认 `index.ts`，所有后端插件（系统与第三方）都必须提供入口。系统插件在构建期预编译成 `dist/`；第三方在安装/加载时编译。`backend.capabilities` 是可安装能力的白名单；`backend.hooks` 声明使用哪些点位与最高动作等级（未声明的点位在调用时拒绝）。不声明 `backend` 即纯前端插件。
- `backend.initTimeoutMs` 是入口工厂（含异步 Promise 结算）的装配预算，缺省 5000、上限 60000 毫秒；超过 5000 仅 builtin/verified 可声明。它只放宽装配期发现预算（如 MCP 的 `tools/list`），超时仍按插件装配失败处理，不影响 Core 启动。
- `backend.configNamespace` 是 `ctx.config` 的机器级命名空间，缺省等于插件 id；仅声明 `global.config` 时有效，用于多个系统插件共享同一份配置（如 `provider-config`）。显式 `--data-dir` 时命名空间落在该数据目录的 `config/` 下。
- `backend.env` 是环境变量白名单：只有列出的名字出现在 `process.env`，值在装配期从宿主环境取值并冻结（组合根可显式注入覆盖，例如测试）；宿主环境里不存在的名字不注入（`undefined`），不伪造空串。密钥不得写入环境变量，仍走 `secret`。
- 环境变量模块（系统插件 `env-manager` 经 `global.config` 管理的命名空间）会注入插件启动的**子进程**（`ctx.exec`、后台任务、`ctx.process.start`），同名覆盖宿主环境；它不进入插件 `process.env`（那里仍按 `backend.env` 白名单），值不写日志。变量名需匹配 `^[A-Za-z_][A-Za-z0-9_]*$`，`ADAG_` 前缀保留。
- `backend.capabilities` 里的 `process` 只授予 builtin/verified，安装 `ctx.process`（长驻 stdin/stdout、可 kill 进程树）；它不是 `node:child_process` 的替代，也不装 `spawn`/`fork`。每 Runtime 最多 8 个、单进程生命周期 30m。
- Node 内置模块同样由能力位安装：`node:fs` 需要 `fs.read`/`fs.write`，`node:child_process` 需要 `exec`，`node:os` 始终受限安装；`node:path`/`node:crypto`/`node:buffer` 等纯计算模块无需能力。未安装的模块在链接期报 `ERR_ACCESS_DENIED`；`node:http(s)`/`node:net`/`node:dns`/`node:worker_threads`/`node:vm` 不安装。完整矩阵见 [12 §6.12](12-node-compat.md)。
- `backend.roles` 声明插件角色；v1 只定义 `tool-policy`（审批策略）。它只用于产品装配校验与“无策略时失败关闭”，基座不解释具体规则。
- `apiVersion` 主版本不匹配直接拒绝加载；次版本兼容，宿主忽略未知字段并记录。
- `id` 必须匹配 `^[a-z][a-z0-9-]{1,63}$`，且等于目录名。
- `trust`：`builtin`（系统插件，随 resources 发布，只能启用/禁用）、`verified`（签名有效）、`third-party`（默认）。**来源决定信任上限**：磁盘插件最高 `third-party`；`builtin` 只接受随包/官方目录；`verified` 必须经配置公钥验签，失败降级为 `third-party` 并记 warning。来源上限与 Ed25519 验签已落地：磁盘 `verified` 必须用已配置公钥验签，失败降级 `third-party`；`builtin` 仍只接受随包/官方目录。
- 前端可选 `client` 与贡献级 `hydrate`：`client` 是 `assets` 目录内的浏览器 bundle，`slots[].hydrate` / `pages[].hydrate` 声明该贡献在浏览器接管；缺省不启用，旧插件行为不变。声明 `client`/`hydrate`/`clientOnly` 需要 `frontend.script` 能力；**第三方与系统插件同面**，不再按 trust 拒绝（风险控制见 §6.2.4 与 [09](09-security-operations.md)）。
- 动作绑定的 `backend` 可以指向另一个插件的后端动作；目标插件/动作必须存在，跨插件只传 JSON 结果，不共享内存。
- 清单的每个字段都必须有真实消费者；`adag verify` 逐字段核对，否则构建失败。

#### 6.2.1 能力位

后端 16 项目标能力、前端 2 项；`MinTrust` 决定最低信任级别。**能力位是通用原语，不是产品功能**；没有一项叫 `cron`、`env` 或 `usage`。

| 能力 | 端 | 最低信任 | 脚本可见面 | 执行点 |
|---|---|---|---|---|
| `fs.read` | 后端 | third-party | `ctx.fs.readText/list/stat/exists`、`node:fs` 读成员 | `fsGuard` + 上游 node fs；读路径**不限宿主目录**（2026-10-31 决策） |
| `fs.write` | 后端 | third-party | `ctx.fs.writeText`、`node:fs` 写成员 | 工作区根 + 已安装插件目录（可写 `~/.adago/plugins/<id>/`），symlink 逐段校验 |
| `net.fetch` | 后端 | third-party | `ctx.fetch(url, init)` | 宿主 `HTTPBridge`：仅 http(s)、重定向 ≤5、响应 ≤16MB、超时/取消贯穿；无 host 黑白名单 |
| `net.sse` | 后端 | verified | `ctx.http.json/sse` | 同桥；建连 15s / 空闲 90s / 总 30min |
| `exec` | 后端 | verified | `ctx.exec`、`node:child_process` 的 `execFile/execFileSync` | 无 shell；cwd 限工作区；输出 4MB；超时 120s；取消杀进程树；程序级 `ExecPolicy` 由装配层注入 |
| `process` | 后端 | verified | `ctx.process.start/write/read/kill` | 长驻 stdin/stdout；每 Runtime 8 个、单进程 30min |
| `state` | 后端 | third-party | `ctx.state.get/set/delete` | SQLite `plugin_state`；按插件 + 线程命名空间 |
| `global.config` | 后端 | verified | `ctx.config.get/set/delete/list`、`ctx.config.workspace.*` | 机器级（`$ADAGO_HOME/config/<id>.json` 或 `~/.adago/config/<id>.json`）与工作区级（`<workspace>/.adago/config/<id>.json`）命名空间；0600、原子写、跨进程锁、限额 |
| `events.read` | 后端 | third-party | `ctx.events.query({fromSeq, types, threadId, limit})`、`ctx.events.usage({threadId?, limit?})` | 只读事件/用量投影：Emitter 缓冲（线程内）+ 每轮原始用量行（工作区范围）；不能写事件、不能跨工作区；展示口径聚合归插件 |
| `tool.discovery` | 后端 | builtin | `ctx.catalog.list({query?})`、`ctx.tool.invoke({tool,args})` | 发现式工具宿主桥：目录只读投影 + 内层工具执行；宿主按内层描述符重跑 beforeCall/policy/Runner，仅 builtin |
| `turn.invoke` | 后端 | builtin | `ctx.turn.invoke({threadId?, title?, prompt})` | 通用轮次触发桥：宿主建线程/生成 runId/后台执行，插件只拿受理回执；不授予工具/文件/网络权限，仅 builtin；只在本 Core 内开一轮 |
| `dispatch.send` | 后端 | builtin | `ctx.dispatch.send({workspace?, threadId?, title?, text?, mode?, idempotencyKey?, taskId?})` | 内部发送桥：交给网关调度层，可跨工作区，与界面发送共用同一份投递规则（busy/steer/waiting_input 只有一处判定）；令牌由网关签发给本 Core 进程且插件不可见，sender 由宿主盖章；仅 builtin |
| `ui.ask` | 后端 | verified | `ctx.ui.confirm/select/input/form/notify` | 交互桥；无 UI 时按拒绝（失败关闭） |
| `subagent.invoke` | 后端 | builtin | `ctx.subagent.invoke` | v1.5；v1 不装配 |
| `model.admin` | 后端 | builtin | `ctx.model.list/select` | **只读模型目录 + 切换当前模型**；供应商配置 CRUD 走 `global.config`，不在本能力里 |
| `plugin.admin` | 后端 | builtin | `ctx.pluginAdmin.install/check/remove` | 写全局插件根；宿主校验 id/相对路径/大小，拒绝覆盖 |
| `backend.read` | 前端 | third-party | SSR 预取 `props.data.backend.<id>.<binding>` | Gateway 渲染桥：≤16 个、并发 4、单个 300ms、缓存 8s |
| `frontend.script` | 前端 | third-party | `/ext/{id}/static/*`、hydrate、client bundle | CSP + nosniff + 安装确认；同源脚本风险见 [09](09-security-operations.md) |

发现式工具宿主桥作为 builtin-only 能力 `tool.discovery` 暴露：`ctx.catalog`（目录只读投影）与 `ctx.tool.invoke`（按名执行内层工具，宿主重跑审批链）。通用轮次触发桥 `turn.invoke`（`ctx.turn.invoke`）已落地：宿主建线程/生成 runId/后台执行，插件只提交提示词并拿受理回执；新增桥必须同时提交消费者、审计与契约测试。内部发送桥 `dispatch.send`（`ctx.dispatch.send`）同样已落地：它把消息交给**网关调度层**（[07 §7.9](07-ssr-frontend.md#79-消息发送路径调度层)），可跨工作区，且与界面发送共用同一份投递规则；令牌由网关签给本 Core 进程并只经环境变量传递，插件既拿不到地址也拿不到凭据，sender 由宿主按能力集盖章。
`env.admin` 已不存在：env 的变量读写/校验/管理页由 `env-manager` 系统插件经 `global.config` 实现，基座只保留“向子进程注入已配置环境”的通用执行点。`cron.admin` 已删除：cron 的任务模型/调度/历史由 `cron-manager` 经 `global.config` + `agent.every` 通用定时器 + `turn.invoke` 实现；基座不再读 `cron.json`、不调度、不暴露 `/api/cron/fire`。

> 实现状态：`global.config` 已落地（`ctx.config` + `internal/globalconfig`）；`events.read` 已落地（`ctx.events.query` + Emitter 可回放缓冲，`ctx.events.usage` + SQLite 原始用量行）；`token-usage`/`composer-telemetry` 已改为插件内聚合，基座已删除展示用 `usage.get` 投影，只保留 `events.read` 的原始用量行；`turn.invoke` 轮次触发桥与 `agent.every` 通用定时器已落地；cron 已完成迁移。`dispatch.send` 已落地：网关侧 `internal/gateway/dispatch` 同时服务界面发送与 `ctx.dispatch.send`，内部端点只认回环来源 + 控制令牌，且摘掉了宿主环境里的 `ADAG_`/`ADAGO_` 变量（插件子进程拿不到宿主凭据）。路线见 [11 §16](11-structure-roadmap.md)。

#### 6.2.2 信任级别

- `builtin`：系统插件；只能启用/禁用，不能被磁盘同名插件覆盖；运行时仍走 moejs。
- `verified`：`manifest.sig` 为 Ed25519 签名，覆盖清单 + 文件哈希；公钥在配置里。签名失败按第三方处理并提示。
- `third-party`：默认；能力受 §6.2.1 约束，前端贡献面与系统插件相同，但安装时必须展示能力与脚本风险并取得确认。

信任不是自声明：`discovery`/`app` 必须执行“来源 → 信任上限”；没有验签就不得把磁盘插件当 `verified`/`builtin`。

#### 6.2.3 前端贡献

系统插件与第三方插件都可以声明：

- `slots`：插槽 + TSX 模板 + 排序；
- `pages`：`/ext/{id}/{slug}` 独立页面；
- `nav`：侧栏导航项；
- `dashboard`：概览页卡片；
- `blocks`：Markdown `adag-ui` 动态块；
- `actions`：读/写后端动作绑定；写动作必须有 `confirm`；
- `client` / `hydrate` / `clientOnly`：客户端 bundle 与服务端/客户端接管；
- `styles` / `scripts` / `assets`：静态资源；
- `backend.read`：SSR 只读预取。

规则：

- 前端能力只由 `frontend.capabilities` 决定，不再按 `trust` 剥掉页面/插槽/脚本；`third-party` 不再被特殊裁剪为“只有 blocks”。
- `frontend.script` 是脚本/客户端 bundle 的唯一门禁；安装时展示“该插件可在浏览器同源执行 JS”，用户确认后才装配。
- 第三方前端仍受 CSP、VNode 白名单、CSRF、动作确认与审计约束；详见 [07 §7.5](07-ssr-frontend.md#75-前端插件化) 与 [09 §9.3](09-security-operations.md)。

#### 6.2.4 系统插件不是基座

系统插件与第三方使用同一契约；它们不是“基座内置代码”：

- 基座源码不出现系统插件 id；
- 禁用系统插件后，工具/页面/导航/后台行为消失；
- 系统插件的业务状态与逻辑由插件自己拥有（例如 cron 任务模型、env 校验、供应商 CRUD、用量聚合）；
- 基座只提供通用原语与失败关闭门禁。

### 6.3 后端插件 API

后端插件从虚拟模块 `agent:api` 导入类型；运行时能力由宿主注入，插件不直接持有任何 IO 句柄。

```ts
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
```

`agent.every` 只是通用定时器，不解释 cron 表达式、不存任务、不记录运行历史；这些语义都属于使用它的插件（当前是 `cron-manager`）。

装配期上下文（入口工厂第二参数）是刻意收窄的子集：`workspace`/`settings`/`log`/`signal`/`fs`（只读）/`fetch`/`secret`/`process`，**没有** `state`/`ui`/`exec`/`fs.writeText`/`config`/`events`。

#### 6.3.1 ToolContext

```ts
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
```

`ToolContext`（按能力安装，没声明的属性不存在）：

```ts
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
  process?: {                        // capability: process，仅 verified/builtin
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
  sse?: { send(data: unknown, event?: string): void }; // 仅 sse:true 的 route 处理器内存在
  secret?(name: string): string | null; // 仅在 manifest 声明 secrets 时存在
  ui?: UIContext;                    // capability: ui.ask，仅 builtin/verified
  settings?: Record<string, unknown>; // 插件自己的设置（[plugins.settings.<id>]，基座不解释）
}
```

`config` 是**机器级、插件命名空间隔离**的持久化：key 只在本插件命名空间可读写；宿主负责 0600、原子写与跨进程锁。`events` 是只读投影，字段脱敏，不能跨工作区。两者都不得用于绕过 `state`/`fs` 的 scope。

#### 6.3.2 动作、路由与钩子

动作（Action）与工具（Tool）是两条通道，不能混用：

- **工具**给模型调用，进入工具目录，受 `tool.beforeCall` 策略与 scope 约束。
- **动作**只给界面调用（`POST /ext/{id}/action/{name}`），不进入模型上下文。读动作可以在 SSR 时预取；写动作必须用户点击，并支持 `confirm` 文案。
- **动态路由**（`route`）给插件自己的 HTTP API 用：`GET/POST /ext/{id}/api/*`，查询串与 JSON 请求体由宿主传入；返回 JSON，响应大小上限 64KB。`sse: true` 的路由只允许 GET，处理器用 `ctx.sse.send(data, event?)` 推帧。
- **钩子**只用于生命周期观察与 `tool.beforeCall` 策略；控制面动作（`replace`/`abort`）仅 builtin 系统插件可用。需要替换内核机制的，不走钩子，走 [06 §6.10](06-plugin-runtime.md#610-内核实现与替换-seam) 的 seam。

#### 6.3.3 系统插件如何实现业务功能

| 系统插件 | 使用的通用原语 | 自己拥有的逻辑 |
|---|---|---|
| `cron-manager` | `global.config`、`process`/`exec`、通用轮次触发桥（builtin） | cron 表达式解析、任务模型、调度循环、运行历史、到期触发 |
| `env-manager` | `global.config`、`page`/`action`、`process` 注入点 | env 命名空间格式、校验、管理页、作用域覆盖 |
| `provider-admin` | `global.config`、`model.admin`、`net.fetch` | 供应商 CRUD（`ctx.config`）、`/models` 拉取（`ctx.fetch`）、密钥尾号展示 |
| `token-usage` / `composer-telemetry` | `events.read`、`backend.read`、`clientOnly` | 聚合口径、页面/卡片、轮询节奏、上下文环 |
| `tool-discovery` | `tool.discovery`（builtin） | 目录只读投影、按名执行、结果上浮 |
| `mcp-client` | `net.fetch`、`process`、`state` | MCP transport、工具映射、连接生命周期 |
| `policy-default` | `tool.beforeCall`、`ctx.ui` | 询问/放行规则与模式 |

这些插件由产品声明启用；基座不读取它们的配置格式，也不提供同名兜底。

#### 6.3.4 工具管理集中化（ToolManager）

工具不是各层各管一段，而是由 **Core 侧的基座内部组件 `ToolManager`**（`internal/toolkit`）统一负责：

| 职责 | 说明 |
|---|---|
| 注册与摘除 | `RegisterOwner(owner, descriptor)` / `RemoveOwner(pluginID)`；owner 由宿主按清单盖章，声明与 owner 不一致直接拒绝 |
| 描述符校验 | 名字规则、保留名（`find_tool`/`exec_tool`）、Schema、access、`discovery` 与 `terminatesTurn` 互斥 |
| 可见性视图 | `Injected`（模型声明）/ `Discovery`（`find_tool`）/ `System`（保留入口）/ `All`（`tool.list`、诊断） |
| 快照 | 每个 turn 取一份不可变快照，模型声明、执行查找与循环检测同源；中途禁用不让已发出的声明与执行目录错位 |
| 执行路由 | 归属以目录为准：`pluginID` 与描述符不一致时拒绝，不把调用投给另一个插件 |

可见性规则只有一处实现（`domain.ToolVisibility`），模型声明、`ctx.catalog.list` 与 `exec_tool` 的内层校验都问它，避免出现「`find_tool` 列出的工具」与「`exec_tool` 能执行的工具」两套口径。

`ToolManager` 刻意**不做**审批：`tool.beforeCall`、策略失败关闭与回执仍只在引擎一份；它也不加载插件、不理解 cron/任务等业务语义。`tool-discovery` 仍是模型可见的入口，背后由 `ToolManager` 提供目录与路由。

---

**相邻文档**：[← 会话、存储与子智能体](04-sessions-storage.md) · [索引](design.md) · [插件运行时与治理 →](06-plugin-runtime.md)
