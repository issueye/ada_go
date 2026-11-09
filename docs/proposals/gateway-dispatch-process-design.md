# 网关调度层与进程管理设计（提案）

> 状态：提案，未进入 `docs/design.md` 基线  
> 基线：`480099f feat(plugins): 完成系统插件迁移与基座收敛`  
> 范围：Gateway 的 Dispatcher（消息调度层）、ProcessManager（进程管理）、插件进程注册，以及 cron 单进程迁移  
> 非范围：多用户、远程 Gateway、容器/沙箱、插件市场

## 0. 摘要

- Gateway 新增统一调度层 `Dispatcher`：普通消息和 cron 消息都按 `workspace` 路由到对应 agent worker。
- 发送者身份 `Sender` 是调度层一等字段，由 token/宿主桥盖章，插件不能伪造。
- Gateway 新增 `ProcessManager`：统一管理由 Gateway 拉起的进程，插件可注册 `on_enable` 或 `lazy` 进程。
- Core 新增基座内部 `ToolManager`：集中管理工具注册/移除、可见性、查找、快照与执行路由；它不是插件，不替代 engine 的审批链。
- cron 改为单例 scheduler 进程：插件启动时拉起，插件停止时回收；插件只负责 cron 语义与任务模型，到点调用 Dispatcher。
- cron 提供 CRUD / 立即执行 / 启停工具，但工具标记 `discovery: true`，不直接进入模型工具清单，只通过 `find_tool` / `exec_tool` 动态发现和执行。
- 基座只提供路由、进程生命周期、身份与审计，不理解 cron、任务或工作区业务规则。

## 1. 背景

当前架构中：

- Gateway 为每个工作区拉起一个 Core（agent worker），由 `internal/gateway/supervisor` 管理；
- 普通消息在 `internal/gateway/web` 中直接执行 `thread.create` + `turn.start`；
- 插件后端运行在 Core 内；
- `cron-manager` 使用机器级 `global.config` 保存任务，但每个 Core 都运行 `agent.every`，因此多工作区下同一任务会被多次触发；
- cron 任务的 `workspace` 字段没有参与触发，`ctx.turn.invoke` 只能在当前 Core 执行；
- 插件进程与 agent worker 的生命周期分散，缺少统一管理。

目标不是把 cron 逻辑搬进基座，而是给基座补两个通用能力：

1. **Dispatcher**：所有消息（用户、cron、未来其他插件）统一按工作区路由到 agent worker；
2. **ProcessManager**：Gateway 统一管理它拉起的进程，插件可动态注册、按启用或懒加载启动。

cron 仍然是系统插件，只负责任务模型、cron 解析和运行历史；到点后向 Dispatcher 发送消息。

## 2. 目标与非目标

### 2.1 目标

- 普通消息与 cron 消息走同一个发送入口；
- Dispatcher 按 `workspace` 选择目标 agent worker；
- 发送者身份是调度层一等字段，不可由插件伪造；
- cron 调度进程全局只有一份；
- 插件可注册由 Gateway 管理的进程，支持 `on_enable` 与 `lazy`；
- 进程崩溃可重启、网关退出可回收整棵进程树；
- Core 内工具管理集中到基座 `ToolManager`：注册、可见性、快照、发现式执行约束、路由与审计只有一个入口；
- 基座不出现 cron 表达式、任务模型、工作区业务规则。

### 2.2 非目标

- 不把 Gateway 的任意 HTTP API 暴露给插件；
- 不让插件直接持有 Gateway 内部令牌；
- 不在本阶段实现多用户/远程调度；
- 不把 cron 表达式、任务历史等业务语义放进 Dispatcher 或 ProcessManager。

## 3. 总体架构

```text
Web UI ─┐
        ├─▶ Dispatcher.Send(sender, req) ─▶ Supervisor.Acquire(workspace) ─▶ Core worker
Cron ───┘                                      │
   ▲                                           ├─ thread.create
   │                                           └─ turn.start / turn.steer
   │
   └── 由 Gateway ProcessManager 管理的单例 scheduler 进程

Core 内：
  插件 ──registerTool──▶ ToolManager ──▶ Catalog
  模型/插件 ──find_tool/exec_tool──▶ ToolManager ──▶ engine 审批链 ──▶ Runner

插件（Core 内）──ctx.processes.register──▶ Gateway ProcessManager
插件（Core 内）──ctx.dispatch.send───────▶ Gateway Dispatcher
```

四个组件职责：

| 组件 | 职责 | 不负责 |
|---|---|---|
| `Dispatcher` | 工作区解析、worker 获取、线程创建、轮次启动/排队、幂等、发送者审计 | cron 表达式、任务存储、进程生命周期 |
| `ProcessManager` | 进程 spawn/stop/restart、日志、进程树回收、引用计数、启动模式 | agent 轮次、工作区业务语义 |
| `Supervisor` | 工作区 ↔ Core 的 lease、RPC 句柄、空闲回收 | 插件进程 |
| `ToolManager` | 工具注册/移除、可见性、查找、快照、执行路由、审计 | 审批策略、工具实现、cron 业务 |

`Supervisor` 的 spawn/kill 后续可复用 `ProcessManager` 的底层实现，但保留自己的 lease/RPC 语义。

## 4. Dispatcher

### 4.1 发送者身份

```go
type SenderKind string

const (
    SenderUser   SenderKind = "user"
    SenderPlugin SenderKind = "plugin"
    SenderSystem SenderKind = "system"
)

type Sender struct {
    Kind     SenderKind `json:"kind"`
    ID       string     `json:"id"`                 // web / cron-manager / system
    PluginID string     `json:"pluginId,omitempty"` // plugin 发送者
    TaskID   string     `json:"taskId,omitempty"`   // cron 任务
}
```

身份由宿主盖章，不从请求体读取：

- Web：Gateway 构造 `{Kind: user, ID: "web"}`；
- Core 插件：宿主按 `CapabilitySet.PluginID` 填空，脚本不能传 `sender`；
- 内部 cron 进程：Bearer token 绑定 `plugin:cron-manager`，请求体不能覆盖；
- 未来多用户：`ID` 换成登录用户 id。

`sender` 只用于审计、限流、幂等命名空间与运行归属，**不能作为授权绕过**。工具审批、能力位、workspace 校验仍按原链路执行。

### 4.2 请求与回执

```go
type DeliveryMode string

const (
    DeliveryAuto  DeliveryMode = "auto"  // 空闲 start；运行中 steer
    DeliveryStart DeliveryMode = "start" // 只接受 start，busy 返回错误
    DeliverySteer DeliveryMode = "steer" // 只接受 steer
)

type SendRequest struct {
    Workspace      string
    ThreadID       string // 空 = 新建线程
    Title          string
    Text           string
    Images         []domain.Part
    Mode           DeliveryMode
    IdempotencyKey string
}

type Receipt struct {
    Workspace string
    ThreadID  string
    RunID     string
}

type Dispatcher interface {
    Send(ctx context.Context, sender Sender, req SendRequest) (Receipt, error)
    ListWorkspaces(ctx context.Context, sender Sender) ([]Workspace, error)
}
```

`Send` 流程：

1. 解析 `Workspace`；空表示默认工作区；
2. 校验工作区存在；
3. `Supervisor.Acquire(workspace)`；
4. `ThreadID` 为空时 `thread.create`；
5. 读取线程状态：
   - `auto`：空闲 `turn.start`，运行中 `turn.steer`；
   - `start`：运行中返回 `thread_busy`；
   - `steer`：空闲返回 `not_running`；
6. 返回 `{workspace, threadId, runId}`，审计发送者与结果。

### 4.3 普通消息迁移

把 `internal/gateway/web` 中分散的 `thread.create + turn.start/steer` 收进 Dispatcher：

- `handleCreateThread`：`Send(Mode: auto)`；
- `handleTurn`：`Send(Mode: auto)`，保留现有 steering 语义；
- 编辑重发/分支：保留 `thread.branch` 等业务操作，最后一步仍走 Dispatcher；
- `/api/workspaces` 改为调用 `Dispatcher.ListWorkspaces`。

### 4.4 插件访问

Core 插件通过宿主桥访问：

```ts
ctx.dispatch.send({
  workspace: "/path/to/ws",
  threadId: "",
  title: "定时任务: 日报",
  text: "生成今天的日报",
  mode: "start",
  idempotencyKey: "cron:task-1:2026-11-09T09:00",
});
```

能力位 `dispatch.send`，v1 仅 builtin。宿主桥：

- 从 `CapabilitySet.PluginID` 盖章 sender；
- 通过 Gateway 内部 loopback 端点调用 Dispatcher；
- 插件看不到 token；
- 请求/响应有大小与超时限制；
- 审计 `pluginId/workspace/threadId/runId`。

## 5. ProcessManager

### 5.1 职责

- 是所有“由 Gateway 拉起的非 agent-worker 进程”的唯一父进程；
- 统一 spawn、日志、重启、停止、进程树回收；
- 支持插件注册/注销、引用计数、启用时启动或懒加载；
- 提供状态与健康检查，不解释进程业务。

### 5.2 规格

```go
type Scope string

const (
    ScopeGlobal    Scope = "global"    // 跨工作区单例
    ScopeWorkspace Scope = "workspace" // 每工作区一个
)

type StartMode string

const (
    StartOnEnable StartMode = "on_enable"
    StartLazy     StartMode = "lazy"
)

type RestartPolicy string

const (
    RestartNo        RestartPolicy = "no"
    RestartOnFailure RestartPolicy = "on_failure"
    RestartAlways    RestartPolicy = "always"
)

type ProcessSpec struct {
    ID          string         // pluginID/name
    Owner       string         // 插件 id，由宿主盖章
    Scope       Scope
    Workspace   string         // ScopeWorkspace 的 cwd
    Runtime     string         // node | binary
    Entry       string         // 插件目录内相对路径
    Args        []string
    Env         map[string]string
    StartMode   StartMode
    Restart     RestartPolicy
    StopTimeout time.Duration
}
```

### 5.3 生命周期

- `Register`：幂等注册；同一 `ID` 的多个注册者增加引用计数；
- `on_enable`：首次注册后立即启动；
- `lazy`：只登记，`Ensure` 时启动；
- `Unregister`：减少引用；最后一个引用释放时停止；
- Core 退出：Gateway 清理该 Core 的所有注册；
- 插件禁用/重载：Core 关闭时自动注销；
- 进程退出：按 `Restart` 退避重启；`no` 记录失败状态；
- Gateway 退出：停止全部进程并杀进程树；
- 注册者崩溃/断连：按 Core 会话清理，避免悬挂引用。

### 5.4 插件 API

```ts
ctx.processes.register({
  name: "scheduler",
  entry: "dist/scheduler.js",
  runtime: "node",
  scope: "global",
  start: "on_enable",
  restart: "on_failure",
  args: [],
  env: {},
});

ctx.processes.ensure("scheduler");
ctx.processes.stop("scheduler");
ctx.processes.status("scheduler");
ctx.processes.unregister("scheduler");
```

约束：

- `entry` 必须位于插件目录内；
- `runtime=node` 使用宿主 runtime；`binary` 仅 builtin/verified；
- 禁止 shell；argv 直接传给进程；
- `Env` 仅允许显式白名单；
- 每插件最多 8 个进程；全局最多 32 个；
- stdout/stderr 限长、脱敏、写 Gateway 日志；
- 停止先 graceful，超时杀进程树。

### 5.5 内部 API

> 状态：P3 已只实现宿主侧 `processmgr`（Go API）；下面的 loopback 端点与插件桥随 P4 落地。

Gateway 暴露 loopback 控制端点（仅内部 token）：

```text
POST /internal/processes/register
POST /internal/processes/unregister
POST /internal/processes/ensure
POST /internal/processes/stop
GET  /internal/processes/status
```

Core 启动时注入：

```text
ADAGO_GATEWAY_CONTROL_URL=http://127.0.0.1:<port>
ADAGO_GATEWAY_CONTROL_TOKEN=<short-lived, per-core>
```

该环境变量只由宿主读取，不进入插件 `process.env`。

## 6. ToolManager（基座内部）

### 6.1 定位

ToolManager 是 Core 侧的基座内部组件，不是系统插件，不提供模型可见的管理工具。它集中管理：

- 工具注册与按 owner 移除；
- 描述符校验、保留名与命名冲突；
- 可见性（注入式 / 发现式 / system）；
- 查找、快照与执行路由；
- 审计与统计。

它不负责：

- `tool.beforeCall` 策略、审批与失败关闭——仍在 engine；
- 具体工具执行——仍由插件 Runner；
- 运行期任意代码加载；
- cron/任务业务语义。

### 6.2 当前问题

工具管理目前分散在：

| 层 | 现状 |
|---|---|
| `toolkit.Catalog` | 注册、去重、冻结、列举、查找、按插件移除 |
| `app.toolInvoker` | 工具名 → 插件实例的路由 |
| `engine.runTools` / `InvokeTool` | 审批链、循环检测、回执 |
| `tool-discovery` 插件 | `find_tool` / `exec_tool` 的模型接口 |

缺少统一入口回答：工具属于谁、是否 discovery、是否可用、执行走哪条链、某个 turn 用的是哪份快照。

### 6.3 接口

```go
type Visibility int

const (
    VisibilityInjected Visibility = iota // 注入模型清单
    VisibilityDiscovery                  // 只经 find_tool 发现
    VisibilitySystem                     // find_tool / exec_tool 等保留入口
    VisibilityAll                        // tool.list / 诊断
)

type Snapshot struct {
    Version uint64
    Tools   []domain.Descriptor
    byName  map[string]domain.Descriptor
}

type ToolManager interface {
    Register(owner string, def domain.Descriptor) error
    RemoveOwner(pluginID string) int

    List(ctx context.Context, v Visibility) []domain.Descriptor
    Lookup(ctx context.Context, name string) (domain.Descriptor, bool)
    Snapshot(ctx context.Context) Snapshot

    // 只做插件路由，不跑策略；审批仍由 engine 完成。
    Route(ctx context.Context, meta CallMeta, call domain.ToolCall) (domain.Result, error)
}
```

实现建议放在 `internal/toolkit`，由 `app.Assembly` 持有；`Catalog` 退化为纯注册表，`toolInvoker` 并入 Manager 的路由表。

### 6.4 可见性

| 视图 | 规则 | 使用方 |
|---|---|---|
| `Injected` | `Discovery=false`（包含 system 的 `find_tool`/`exec_tool`） | 模型工具声明 |
| `Discovery` | `Discovery=true` | `ctx.catalog.list` / `find_tool` |
| `System` | `System=true` 的 `find_tool`/`exec_tool` | 模型工具声明、保留名校验 |
| `All` | 全部描述符 | `tool.list`、诊断、统计 |

工具是否 discovery 是**工具自身声明**；ToolManager 不猜测、不改写。

### 6.5 执行链

```text
模型直呼工具
  engine.runTools
    → ToolManager.Lookup（Injected）
    → tool.beforeCall
    → policy 失败关闭
    → ToolManager.Route → 插件 Runner

find_tool / exec_tool
  ctx.catalog.list / ctx.tool.invoke
    → ToolManager.List(Discovery) / Lookup
    → engine.InvokeTool
    → tool.beforeCall
    → policy 失败关闭
    → ToolManager.Route → 插件 Runner
```

关键约束：

- `ToolManager.Route` 不做审批，避免出现第二条绕过 engine 的执行链；
- `engine.InvokeTool` 必须检查 `desc.Discovery`，只允许执行发现式工具；
- `exec_tool` 自身仍是 read 工具，但内层工具按自己的 access 重跑审批；
- 工具结果只由外层调用落库，内层结果通过 `exec_tool` 上浮。

### 6.6 快照与生命周期

- 装配期：插件注册进入 staging，提交时按 owner 原子替换；
- 运行期：目录冻结，禁止新注册；
- 插件禁用/重载：`RemoveOwner(pluginID)`，不留下半截工具；
- 每个 turn 取一份 `Snapshot`，模型声明、执行查找、循环检测使用同一份；
- 中途禁用插件不影响本轮已发出的工具声明；未执行的调用得到 `tool_not_found` 回执；
- v1 不做“单工具实时启停”；如需要，必须显式失效快照并定义 `tool_not_found` 语义。

### 6.7 与 `tool-discovery` 的关系

- `tool-discovery` 仍是模型可见的发现入口；
- `ToolManager` 是它背后的目录、可见性与路由实现；
- `ctx.catalog` 只读投影、`ctx.tool.invoke` 内层执行都由 ToolManager 支撑；
- `tool-manager` 不应再作为普通插件重复实现一套管理面。

### 6.8 安全边界

- 只有装配期注册的工具进入目录；插件不能运行期注入；
- owner 由宿主按清单盖章，工具不能冒领；
- `find_tool` / `exec_tool` 保留名只允许 `tool.discovery` builtin；
- `Discovery + TerminatesTurn` 互斥；
- ToolManager 不做授权，所有执行仍走 engine 审批链；
- 审计记录 owner、tool、access、visibility、结果。

## 7. cron 单进程

### 7.1 模型

- `cron-manager` 的 Core 后端负责：任务 CRUD、页面 route、供模型调用的管理工具；
- 插件加载时注册全局进程 `cron-manager/scheduler`，`start: on_enable`：
  - 插件启动 → 注册并拉起 scheduler；
  - 插件禁用/重载/Core 退出 → 注销引用，最后一个引用释放时停止；
  - 同一 `pluginID/name` 去重，多工作区只启动一个 scheduler；
- scheduler 负责 cron 解析、到期判断、运行历史；
- scheduler 通过 Dispatcher 发送消息，不再使用 `ctx.turn.invoke`；
- Core 不再运行 `agent.every` 调度循环。

### 7.2 发送

```json
{
  "workspace": "/path/to/ws",
  "threadId": "th_...",
  "title": "定时任务: 日报",
  "text": "生成今天的日报",
  "mode": "start",
  "idempotencyKey": "cron:<taskId>:<minute>"
}
```

上例是 Dispatcher 收到的业务字段；`sender` 由内部 token 映射或宿主桥盖章，cron scheduler 进程不提交 `sender` 字段。

- `workspace` 为空表示默认工作区；
- `mode=start`，busy 记录失败，不静默丢；
- `idempotencyKey` 由 Dispatcher 按 `sender + key` 去重；
- 线程被删除时由 Dispatcher 决定重建策略；cron 只记录结果。

### 7.3 管理工具

`cron-manager` 注册以下工具，全部标记 `discovery: true`：

| 工具 | access | 作用 |
|---|---|---|
| `cron_list` | read | 列出任务（含启停、下次运行时间、最近一次运行） |
| `cron_get` | read | 读取单个任务详情与运行历史 |
| `cron_create` | write | 新建任务（name/cron/workspace/action.text） |
| `cron_update` | write | 修改任务定义；未传 `enabled` 时保留原启停状态 |
| `cron_delete` | write | 删除任务 |
| `cron_run` | exec | 立即执行一次（不受启停影响） |
| `cron_set_enabled` | write | 启用/停用任务 |

参数直接复用任务模型：

```ts
agent.registerTool({
  name: "cron_create",
  description: "创建定时任务",
  parameters: { /* name, cron, workspace?, action.text, enabled? */ },
  access: "write",
  discovery: true,
  async execute(args, ctx) { /* 写 ctx.config，返回任务 id */ },
});
```

约束：

- **不进入默认工具清单**：`discovery: true` 的工具不注入模型上下文，模型只能先 `find_tool`，再用 `exec_tool` 执行；
- `find_tool` / `exec_tool` 由 `tool-discovery` 系统插件提供；该插件禁用时，cron 管理工具对模型不可达（管理页 route 不受影响）；
- `exec_tool` 执行内层工具时，宿主按该工具的 `access` 重跑审批/策略链，cron 工具不能绕过；
- `cron_run` 是 `exec`，会触发一轮对话；应在工具结果里明确返回 `{taskId, threadId, runId}` 或错误；
- 工具只做 CRUD/触发，不在工具内解析 cron 表达式；解析在 scheduler/任务校验共享模块中。

### 7.4 启动时机

两种可选：

- **A. Gateway 首次启动即预注册**：需要 Gateway 级插件元数据或持久化注册；
- **B. 首个工作区 Core 加载插件后启动**：实现简单，但没有任何工作区打开时 cron 不运行。

本提案默认 B；插件启动即拉起 scheduler（`on_enable`），不是 lazy。若产品要求无工作区也常驻，再补 A。

## 8. 安全边界

- 不向插件暴露 Gateway 任意 HTTP API；
- 插件不持有内部 token；
- `dispatch.send` / `process.supervise` v1 仅 builtin；
- 进程 `entry` 限定插件目录，禁止 shell；
- 进程环境、日志、响应大小有上限；
- 审计发送者、插件、进程、workspace、threadId、runId；
- 工具审批链、能力位、workspace 校验不被 sender 绕过；
- Dispatcher 只做路由，不做策略决策。

## 9. 失败与恢复

| 场景 | 行为 |
|---|---|
| 工作区不存在 | `workspace_not_found`，不静默回落 |
| Core 启动失败 | Dispatcher 返回错误；cron 记录失败 |
| 线程 busy（`mode=start`） | 返回 `thread_busy`；调用方决定重试/记录 |
| 线程已删除 | Dispatcher 按策略重建；cron 记录新 threadId |
| scheduler 崩溃 | ProcessManager 按 `on_failure` 重启 |
| Gateway 退出 | 停止所有进程；agent worker 由 Supervisor 回收 |
| Core 断连 | 清理该 Core 的进程注册；Dispatcher 不保留悬挂 lease |

## 10. 兼容与迁移

1. 先抽 ToolManager，包住 Catalog + toolInvoker，行为不变；
2. 修复 `InvokeTool` 的 discovery 执行约束，统一 `tool.list` / `ctx.catalog` / engine 视图；
3. 先抽 Dispatcher，Web 行为不变；
4. 加内部发送端点与 sender 审计；
5. 加 ProcessManager 与插件注册桥；
6. cron 改为单例 scheduler，删除每 Core `agent.every`；
7. 删除 `cron-manager` 对 `ctx.turn.invoke` 的依赖；
8. 清理旧 `turn.invoke` 或降级为兼容桥；
9. 补多工作区、禁停、崩溃重启、幂等测试。

## 11. 待确认

- `process.supervise` 是否只给 builtin；
- cron 在“无工作区打开”时是否必须运行（决定 A/B）；
- 全局进程注册是否允许第三方 verified 插件；
- 进程日志是否通过插件页面对用户展示；
- `mode=auto` 是否对 cron 开放，还是 cron 强制 `start`；
- ToolManager v1 是否支持单工具启停，还是只随插件生命周期变化。
