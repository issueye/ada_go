# 插件模型与后端 API

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§6.1 插件包形态、§6.2 清单、§6.3 后端插件 API  
> 版本：0.1 · 2026-10-28

## 6. 插件系统

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

- 后端入口导出默认工厂函数 `export default function (agent: AgentAPI) {}`。
- 前端模板导出 `export function Slot(props)` / `export function Page(props)`。
- 一个包可以只声明 backend、只声明 frontend，或两者都有；两者由不同宿主加载（Core / Gateway），但共享同一个插件 id 与版本。
- 官方插件与第三方同形：`plugin.json` + `index.ts` + 可选的 TSX；官方在构建期产出 `dist/`，桌面打包随 `resources/plugins/official` 发布，不存在平行的 Go 插件包或 Go 注册入口。
- 发现路径：`<workspace>/.adago/plugins/<id>/`（工作区，仅旧目录兼容）与 `~/.adago/plugins/<id>/`（全局，生产统一安装路径）。工作区优先；同 id 冲突时工作区覆盖全局并给出警告。新安装（`plugin.admin` 安装桥 / plugin-helper）一律写全局。
- 启用状态：全局 `~/.adago/plugins.json`（跨工作区共享）；工作区有单独启停时落到 `<workspace>/.adag/plugins.json` 覆盖全局；两者都无条目时回落产品声明。旧版按工作区写在 SQLite `plugin_meta` 的用户选择在打开会话时一次性迁移成工作区覆盖文件。
- v1 只从本地目录或 zip 安装；不做在线市场，不做运行时 npm 解析。插件依赖必须在构建时打进产物，或使用宿主提供的模块。宿主提供的 Node 内置模块是一个有明确边界的子集，见 [12](12-node-compat.md)。

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

- 后端部分（可选）：`backend.entry` 默认 `index.ts`，所有后端插件（官方与第三方）都必须提供入口。官方插件在构建期预编译成 `dist/`，桌面打包随 `resources/plugins/official` 发布，运行时与第三方一样由 moejs 宿主执行；第三方在安装或加载时编译。`backend.capabilities` 是可安装能力的白名单；`backend.hooks` 声明使用哪些点位与最高动作等级（未声明的点位在调用时拒绝）。不声明 `backend` 即纯前端插件。
- `backend.initTimeoutMs` 是入口工厂（含异步 Promise 结算）的装配预算，缺省 5000、上限 60000 毫秒；超过 5000 仅 builtin/verified 可声明。它只放宽装配期发现预算（如 MCP 的 `tools/list`），超时仍按插件装配失败处理，不影响 Core 启动。
- `backend.env` 是环境变量白名单：只有列出的名字出现在 `process.env`，值在装配期从宿主环境取值并冻结（组合根可显式注入覆盖，例如测试）；宿主环境里不存在的名字不注入（`undefined`），不伪造空串。密钥不得写入环境变量，仍走 `secret`。
- `backend.capabilities` 里的 `process` 只授予 builtin/verified，安装 `ctx.process`（长驻 stdin/stdout、可 kill 进程树）；它不是 `node:child_process` 的替代，也不装 `spawn`/`fork`。每 Runtime 最多 8 个、单进程生命周期 30m。
- Node 内置模块同样由能力位安装：`node:fs` 需要 `fs.read`/`fs.write`，`node:child_process` 需要 `exec`，`node:os` 始终受限安装；`node:path`/`node:crypto`/`node:buffer` 等纯计算模块无需能力。未安装的模块在链接期报 `ERR_ACCESS_DENIED`；`node:http(s)`/`node:net`/`node:dns`/`node:worker_threads`/`node:vm` 不安装。完整矩阵见 [6.12](12-node-compat.md#612-node-兼容原生库)。
- `backend.roles` 声明插件角色；v1 只定义 `tool-policy`（审批策略）。它只用于产品装配校验与“无策略时失败关闭”，基座不解释具体规则。
- `capabilities` 里的 `ui.ask` 只授予 builtin/verified：它允许插件发起通用用户交互（confirm/select/input），不授予任何工具或文件权限。`net.sse` 同样只授予 builtin/verified，供官方 provider 做流式模型协议；`subagent.invoke` 只授予 builtin，供 `tools-subagent` 使用；`model.admin` 只授予 builtin，供供应商管理插件读取目录并切换当前模型（不含密钥）；`plugin.admin` 只授予 builtin，供 `plugin-helper` 经宿主安装桥把插件骨架写入全局 `~/.adago/plugins`（宿主校验 id/路径/大小并拒绝覆盖，不授予任意文件写权限）。
- `apiVersion` 主版本不匹配直接拒绝加载；次版本兼容，宿主忽略未知字段并记录。
- `id` 必须匹配 `^[a-z][a-z0-9-]{1,63}$`，且等于目录名。
- `trust`：`builtin`（预编译后随 resources 发布，只能启用/禁用）、`verified`（签名有效）、`third-party`（默认，受限）。
- 后端 `capabilities` 是**可安装能力**的白名单；前端 `capabilities` 控制预取、脚本、静态资源等。
- 动作绑定的 `backend` 可以指向另一个插件的后端动作；目标插件/动作必须存在，跨插件只传 JSON 结果，不共享内存。
- 清单的每个字段都必须有真实消费者；`adag verify` 逐字段核对，否则构建失败。

### 6.3 后端插件 API

后端插件从虚拟模块 `agent:api` 导入类型；运行时能力由宿主注入，插件不直接持有任何 IO 句柄。仓库当前没有独立的 `sdk/` 类型包，官方插件在各自 `index.ts` 顶部本地声明所用类型形状；Node 兼容子集以 [6.12](12-node-compat.md#612-node-兼容原生库) 的安装矩阵为准：类型里存在不代表当前 Runtime 有能力，能力位仍以清单为准。

```ts
export default function (agent: AgentAPI, ctx?: AssemblyContext): void | Promise<void>;

interface AgentAPI {
  registerTool(def: ToolDef): void;
  registerProvider(def: ProviderDef): void; // 仅 builtin/verified，见 [5.5](03-model-and-approval.md#55-模型适配层)
  registerPrompt(def: { id: string; content: string; priority?: number }): void;
  registerAction(def: ActionDef): void;
  route(method: "GET" | "POST", path: string, handler: RouteHandler): void;
  route(def: RouteDef): void; // 对象形式，见 7.6.1
  on<K extends HookPoint>(point: K, fn: HookHandler<K>, opts?: HookOpts): void;
}

// 装配期受控上下文：用于在 registerTool 之前读配置/发现远程工具/启动 stdio server。
// 成员按能力安装；没有 state/ui/exec/fs.writeText；process 按能力安装。
interface AssemblyContext {
  workspace: string;
  settings: Record<string, unknown>;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  signal: AbortSignal;
  fs?: { readText(path: string, opts?: { maxBytes?: number }): Promise<string> }; // fs.read
  fetch?: (url: string, init?: { method?: string; headers?: object; body?: string; timeoutMs?: number }) => Promise<{ status: number; headers: object; text: string }>; // net.fetch；timeoutMs 会真正取消请求
  process?: { start(...): Promise<ProcessHandle> }; // process，仅 builtin/verified；形状同 ToolContext.process
  secret?(name: string): string | null; // 清单 backend.secrets
}

interface RouteDef {
  method: "GET" | "POST";
  path: string;                      // 精确路径，或以 /* 结尾的前缀
  handler: RouteHandler;
}

interface RouteRequest {
  method: "GET" | "POST";
  path: string;                      // 请求路径（前缀匹配时含剩余段）
  query: Record<string, string>;     // 同名参数取第一个
  body?: unknown;                    // POST 的 JSON 体
}

type RouteHandler = (req: RouteRequest, ctx: ToolContext) => unknown;

interface ToolDef {
  name: string;                 // ^[a-z][a-z0-9_]{1,63}$
  description: string;
  parameters: object;           // JSON Schema
  access: "read" | "write" | "exec" | "network"; // 外部效果分类；线程级 KV 不算 write；基座不据此审批
  timeoutMs?: number;
  parallelSafe?: boolean;
  repeatable?: boolean;         // 轮询/等待类工具（如 check_task、wait_task）允许连续相同调用，不参与循环检测
  rollback?: boolean;
  execute(args: any, ctx: ToolContext): Promise<ToolReturn> | ToolReturn;
}

interface ToolReturn {
  content: Array<{ type: "text"; text: string } | { type: "image"; mime: string; data: string }>;
  details?: Record<string, unknown>;  // 只给界面
  terminate?: boolean;                // 提示可以在本轮结束
}
```

`ToolContext`（按能力安装，没声明的属性不存在）：

```ts
interface ToolContext {
  workspace: string;
  threadId: string;                  // 当前线程；route 调用由 Gateway 透传，插件状态按它隔离
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
  fetch?: (url: string, init?: { method?: string; headers?: object; body?: string; timeoutMs?: number }) => Promise<{ status: number; headers: object; text(): Promise<string> }>; // capability: net.fetch；timeoutMs 会真正取消请求
  exec?: (cmd: string, args: string[], opts?: { cwd?: string; timeoutMs?: number }) => Promise<{ code: number; stdout: string; stderr: string }>; // capability: exec，仅 verified/builtin
  process?: {                        // capability: process，仅 verified/builtin
    start(cmd: string, args: string[], opts?: { cwd?: string; env?: string[]; timeoutMs?: number }): Promise<{
      pid: number; taskId: number;
      write(data: string): void;
      read(opts?: { maxBytes?: number; timeoutMs?: number }): Promise<{ stdout: string; stderr: string; exited: boolean; code?: number; truncated?: boolean; error?: string }>;
      kill(): void;
    }>;
  };
  execBackground?: (cmd: string, args: string[], opts?: { cwd?: string; timeoutMs?: number }) => Promise<{ taskId: number; pid: number }>; // capability: exec
  checkTask?: (taskId: number) => { taskId: number; pid: number; status: "running" | "exited" | "timeout" | "killed" | "failed"; code: number; stdout: string; stderr: string; truncated: boolean; stdoutBytes: number; stderrBytes: number; startedAt: string; endedAt?: string; error?: string }; // capability: exec
  waitTask?: (taskId: number, timeoutMs?: number) => Promise<{ taskId: number; pid: number; status: "running" | "exited" | "timeout" | "killed" | "failed"; code: number; stdout: string; stderr: string; truncated: boolean; stdoutBytes: number; stderrBytes: number; startedAt: string; endedAt?: string; error?: string; timedOut: boolean; waitedMs: number }>; // capability: exec；等到终态或等待上限（≤60s）
  killTask?: (taskId: number) => { taskId: number; found: boolean; killed: boolean }; // capability: exec
  state?: {                          // capability: state
    get<T>(key: string): Promise<T | null>;
    set(key: string, value: unknown): Promise<void>;
    delete(key: string): Promise<void>;
  };
  providers?: {                      // capability: model.admin，仅 builtin
    list(): Promise<{ providers: ProviderInfo[]; current: ModelRef }>;
    save(input: ProviderInput): Promise<ProviderInfo>;   // apiKey 留空=保持原值
    remove(providerId: string): Promise<{ removed: boolean }>;
    fetchModels(providerId: string): Promise<ProviderModel[]>; // 服务端拉 /models
    select(providerId: string, modelId: string): Promise<ModelRef>;
  };
  sse?: {                            // 仅 sse:true 的 route 处理器内存在
    send(data: unknown, event?: string): void;
  };
  secret?(name: string): string | null; // 仅在 manifest 声明 secrets 时存在
  ui?: UIContext;                    // capability: ui.ask，仅 builtin/verified
  settings?: Record<string, unknown>; // 插件自己的设置（[plugins.settings.<id>]，基座不解释）
}

interface UIContext {
  available: boolean;                // false 时 confirm/select/input 立即返回 null
  confirm(title: string, message: string, opts?: { timeoutMs?: number; signal?: AbortSignal }): Promise<boolean | null>;
  select(title: string, options: string[], opts?: { timeoutMs?: number; signal?: AbortSignal }): Promise<string | null>;
  input(title: string, placeholder?: string, opts?: { timeoutMs?: number; signal?: AbortSignal }): Promise<string | null>;
  notify(text: string, level?: "info" | "warn" | "error"): void;
}
```

动作（Action）与工具（Tool）是两条通道，不能混用：

- **工具**给模型调用，进入工具目录，受 `tool.beforeCall` 策略与 scope 约束。
- **动作**只给界面调用（`POST /ext/{id}/action/{name}`），不进入模型上下文。读动作可以在 SSR 时预取；写动作必须用户点击，并支持 `confirm` 文案。
- 动态路由（`route`）给插件自己的 HTTP API 用：`GET/POST /ext/{id}/api/*`，查询串与 JSON 请求体由宿主传入；返回 JSON，响应大小上限 64KB。`sse: true` 的路由只允许 GET，处理器用 `ctx.sse.send(data, event?)` 推帧（见 [7.6.2](07-ssr-frontend.md#762-sse-路由)）。
- `model.admin` 的供应商配置默认保存在机器级 `~/.adago/providers.json`（0600；显式 `--data-dir` 时为该数据目录下的 `providers.json`），切换工作区共享；`list()` 不含密钥，只给 `hasApiKey` 与尾号，`save()` 的 `apiKey` 留空表示保留原值。

```ts
interface ActionDef {
  name: string;                      // ^[a-z][a-z0-9_]{1,63}$
  label: string;                     // 界面上按钮/菜单的文案
  mode: "read" | "write";
  confirm?: string;                  // 写动作的确认文案；read 动作忽略
  timeoutMs?: number;                // 默认 5000
  execute(input: { threadId: string; [key: string]: unknown }, ctx: ActionContext): Promise<unknown> | unknown;
}

interface ActionContext {
  workspace: string;
  now: string;
  signal: AbortSignal;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  state?: { get<T>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>; delete(key: string): Promise<void> };
  secret?(name: string): string | null;
  ui?: UIContext;
}

interface HookContext {
  workspace: string;
  threadId: string;
  runId: string;
  now: string;
  signal: AbortSignal;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  ui?: UIContext;                    // capability: ui.ask；策略插件在这里询问用户
  state?: { get<T>(key: string): Promise<T | null>; set(key: string, value: unknown): Promise<void>; delete(key: string): Promise<void> };
  settings?: Record<string, unknown>; // 插件自己的设置（如策略模式），基座不解释
}
```

动作结果只接受 JSON 可序列化值，最大 1MB；`read` 动作的结果进入 `props.data.backend.<前端插件 id>.<绑定名>`，`write` 动作的结果不回填页面，靠 303 或后续刷新呈现。

**官方与第三方共用同一 TS 插件面**：`plugins/official/*` 不提供 Go 注册入口，也不再存在平行的 Go SDK。官方插件与第三方一样导出 `export default function (agent: AgentAPI)`，通过同一份 `AgentAPI` 注册工具、provider、提示词、动作、路由与钩子。差别只在构建与信任：

- 官方插件在构建期由 `build.mjs` 预编译成 `dist/index.js`；桌面打包把整个 `plugins/official/` 放进 Electron `resources`，Core 启动时从 `resources/bin/../plugins/official` 加载；目录不存在直接拒绝启动，二进制不再内嵌副本。仓库保留 `index.ts` 作为源码与类型检查入口。
- 第三方插件在安装/首次加载时编译，产物按内容指纹缓存。
- 两条路径最终都归约成同一份 `Contribution`，经过同一套重名、能力、钩子等级与插槽校验后原子提交；引擎不区分贡献来自官方还是第三方，只看信任级别授予的能力。

---

**相邻文档**：[← 会话、存储与子智能体](04-sessions-storage.md) · [索引](design.md) · [插件运行时与治理 →](06-plugin-runtime.md)

