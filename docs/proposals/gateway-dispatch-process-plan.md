# 网关调度层与进程管理开发计划（提案）

> 状态：提案，未进入开发排期  
> 配套设计：[gateway-dispatch-process-design.md](gateway-dispatch-process-design.md)  
> 基线：`480099f feat(plugins): 完成系统插件迁移与基座收敛`  
> 原则：先立通用原语，再迁 cron；每一步保持 `go test ./...`、`go vet ./...`、`adag verify` 通过

## 1. 当前状态

已完成：

- Gateway `Supervisor` 按工作区管理 Core；
- Web 通过 `thread.create + turn.start/steer` 发送消息；
- 插件系统、能力位、审计、global.config、events.read、turn.invoke、agent.every 已落地；
- cron 已迁到系统插件，但仍是每 Core 一个调度器。

待解决：

- 多工作区下 cron 重复触发；
- cron 的 `workspace` 字段未生效；
- 普通消息与 cron 消息没有统一入口；
- 插件进程没有 Gateway 级生命周期管理；
- `turn.invoke` 无法跨工作区；
- 工具管理分散在 `toolkit.Catalog`、`app.toolInvoker`、engine 与 `tool-discovery` 之间，`exec_tool` 缺少 discovery 执行约束。

## 2. 阶段划分

| 阶段 | 目标 | 依赖 | 主要产物 |
|---|---|---|---|
| P0 | 决策与契约冻结 | 无 | 类型、错误码、能力位、审计字段 |
| P1 | 抽 Dispatcher | P0 | `internal/gateway/dispatch`，Web 走统一入口 |
| P2 | 内部发送 API + Sender | P1 | loopback 端点、token、sender 盖章、审计 |
| P3 | ProcessManager | P0 | 进程注册/启动/停止/重启/引用计数 |
| P4 | 插件进程桥 | P3 | `ctx.processes`、`process.supervise` |
| P5 | 基座 ToolManager | P0 | 工具注册/可见性/快照/路由集中化，修复 discovery 执行约束 |
| P6 | cron 单进程与工具 | P2/P4/P5 | scheduler 进程、任务发送、`discovery` 管理工具、删除每 Core 定时器 |
| P7 | 清理与文档 | 全部 | 删除旧 `turn.invoke` 依赖、测试、文档 |

## 3. P0：决策与契约

任务：

- 冻结 `Sender`、`SendRequest`、`Receipt`、`DeliveryMode`；
- 冻结 `ProcessSpec`、`StartMode`、`RestartPolicy`、`Scope`；
- 冻结 `ToolManager` 的 `Visibility`、`Snapshot`、`Register/RemoveOwner/Lookup/Route`；
- 定义错误码：
  - `workspace_not_found`
  - `thread_busy`
  - `dispatch_timeout`
  - `process_exists`
  - `process_not_found`
  - `process_limit`
  - `process_start_failed`
  - `tool_not_found`
  - `tool_not_discoverable`
  - `tool_conflict`
- 决定能力位：
  - `dispatch.send`
  - `process.supervise`
- 决定审计字段：
  - `senderKind/senderId/pluginId/taskId`
  - `workspace/threadId/runId`
  - `processId/pid/result`
  - `toolName/owner/visibility/access`
- 决定 v1 是否仅 builtin。

产物：

- `internal/gateway/contracts` 或新 `internal/gateway/dispatch` 类型；
- `spec/proto.json` 若走 RPC，补方法/错误码；
- 文档更新 `05/06/09`。

验收：

- 类型评审通过；
- 不引入 cron 字段；
- `adag verify` 通过。

## 4. P1：抽 Dispatcher

任务：

- 新建 `internal/gateway/dispatch`；
- 定义 `Dispatcher` 接口与 `Send` 实现；
- 把 `handleCreateThread`、`handleTurn`、编辑重发里的 `turn.start/steer` 收进去；
- 保留现有 steering、幂等、图片、分支语义；
- `/api/workspaces` 改为 `Dispatcher.ListWorkspaces`；
- 单元测试用 fake Supervisor。

涉及文件：

- `internal/gateway/dispatch/dispatch.go`
- `internal/gateway/dispatch/dispatch_test.go`
- `internal/gateway/web/handlers.go`
- `internal/gateway/web/server.go`
- `internal/gateway/web/web_test.go`
- `internal/gateway/contracts/contracts.go`

验收：

- 普通发送仍通过现有 Web 测试；
- `thread_busy`、steering、图片、幂等行为不变；
- 无 cron 代码进入 Dispatcher。

## 5. P2：内部发送 API 与 Sender

任务：

- Gateway 增加内部 loopback 端点：
  - `POST /internal/dispatch/send`
  - `GET /internal/dispatch/workspaces`
- 生成 per-Core 短期 token；
- Core 启动时注入 `ADAGO_GATEWAY_CONTROL_URL/TOKEN`；
- 请求体不采信 `sender`；token 映射到 `Sender`；
- 审计 sender 与结果；
- 大小/超时限制。

涉及文件：

- `internal/gateway/web/internal_dispatch.go`
- `internal/gateway/web/server.go`
- `internal/gateway/security/*`
- `cmd/adag/core.go` / `cmd/adag/gateway.go`
- `internal/cli/session.go`（读取 control URL/token，交给宿主桥）

验收：

- 伪造 sender 被忽略；
- 非 token 请求 401；
- workspace 不存在明确失败；
- 普通 Web 发送不经过该端点也能工作（端点只给插件/进程）。

## 6. P3：ProcessManager

任务：

- 新建 `internal/gateway/processmgr`；
- 进程抽象（可测试的 `Process` 接口）；
- spawn/stop/restart/backoff/进程树；
- 引用计数与注册表；
- `on_enable` / `lazy`；
- 每插件/全局进程上限；
- 日志环形缓冲 + 限长/脱敏；
- Gateway Shutdown 回收。

涉及文件：

- `internal/gateway/processmgr/manager.go`
- `internal/gateway/processmgr/process.go`
- `internal/gateway/processmgr/manager_test.go`
- `internal/gateway/gateway.go`

验收：

- 单测覆盖启动/停止/崩溃重启/引用计数/lazy/上限；
- 进程树在停止后无残留；
- Gateway 退出时全部回收。

## 7. P4：插件进程桥

任务：

- 新能力位 `process.supervise`（v1 builtin-only）；
- Core 宿主 `ctx.processes.register/unregister/ensure/stop/status`；
- 宿主解析 `entry` 到插件目录，拒绝越界路径；
- 宿主盖章 `Owner`，插件不能注册他人进程；
- `ADAGO_GATEWAY_CONTROL_*` 只由宿主读取；
- Core 关闭/插件禁用自动 unregister；
- 审计。

涉及文件：

- `internal/plugins/contract.go`
- `internal/plugins/sites.go`
- `internal/plugins/host/host.go`
- `internal/plugins/host/cap_processes.go`
- `internal/plugins/host/cap_processes_test.go`
- `internal/cli/session.go`
- `spec/manifest.schema.json`
- `docs/05-plugins.md`

验收：

- 第三方声明能力被拒绝（v1）；
- 路径越界被拒绝；
- 插件禁用后进程停止；
- Core 断连后引用清理。

## 8. P5：基座 ToolManager

任务：

- 新建/抽取 `internal/toolkit` 的 `ToolManager`，包住现有 `Catalog` + `toolInvoker`；
- 统一注册入口：插件装配期 `Register(owner, descriptor)`，禁用/重载 `RemoveOwner(pluginID)`；
- 统一可见性：
  - `Injected`：模型工具声明；
  - `Discovery`：`find_tool` / `ctx.catalog.list`；
  - `System`：`find_tool` / `exec_tool` 保留入口；
  - `All`：`tool.list`、诊断；
- 每 turn 生成 `Snapshot`，模型声明、执行查找、循环检测使用同一份；
- 统一执行路由：`ToolManager.Route` 只路由到插件 Runner，不执行策略；
- `engine.InvokeTool` 增加 `desc.Discovery` 校验，修复“exec_tool 可执行非发现式工具”；
- `tool.list` / `ctx.catalog` / `ctx.tool.invoke` / engine 使用同一份视图；
- 统计与审计：owner、tool、access、visibility、结果。

涉及文件：

- `internal/toolkit/manager.go`（新）
- `internal/toolkit/manager_test.go`
- `internal/toolkit/catalog.go`
- `app/app.go`（Assembly 持有 Manager）
- `internal/engine/toolinvoke.go`
- `internal/engine/toolinvoke_test.go`
- `internal/plugins/host/cap_tooldiscovery.go`
- `internal/corerpc/server.go`（tool.list）
- `docs/05-plugins.md`（若提升为正式设计）

验收：

- 现有工具测试通过，行为不变；
- 插件禁用/重载后工具原子移除，无残留；
- `exec_tool` 不能执行非 discovery 工具；
- 模型声明、find_tool、exec_tool 使用同一份可见性规则；
- 每 turn 快照稳定，中途禁用不改变已发出的声明。

## 9. P6：cron 单进程与工具

任务：

- `cron-manager` 注册 `cron-manager/scheduler` 全局进程，`on_enable`；
- 新增 `dist/scheduler.js`（或等价入口）：
  - 读 `global.config` 的 `cron` 命名空间；
  - 解析 cron；
  - 到期向 Dispatcher 发送消息；
  - 写运行历史；
- 删除 Core 内 `agent.every` 调度循环；
- 删除 `ctx.turn.invoke` 调用；
- 任务 `workspace` 真正参与发送；
- scheduler 使用 `sender: plugin:cron-manager + taskId`；
- 多工作区去重；
- 注册管理工具，全部 `discovery: true`：
  - `cron_list` / `cron_get`（read）；
  - `cron_create` / `cron_update` / `cron_delete`（write）；
  - `cron_run`（exec，立即触发）；
  - `cron_set_enabled`（write，启停）；
- 工具与 route 共享同一份任务校验/存储逻辑，避免两套行为；
- 确认 `tool-discovery` 禁用时工具不可达、页面仍可用。

涉及文件：

- `plugins/official/cron-manager/plugin.json`
- `plugins/official/cron-manager/index.ts`
- `plugins/official/cron-manager/dist/index.js`
- `plugins/official/cron-manager/scheduler.ts`（新）
- `plugins/official/cron-manager/dist/scheduler.js`
- `plugins/official/cron-manager/tools.ts`（可选，工具定义与 route 共享）
- `app/cron_manager_test.go`
- `app/tool_discovery_test.go`
- `plugins/official/dir_test.go`

验收：

- 打开多个工作区只启动一个 scheduler；
- 同一任务同一分钟只触发一次；
- `workspace` 指向正确 worker；
- 删除/禁用插件后 scheduler 停止；
- 线程被删/busy/错误有运行记录；
- `find_tool` 能发现 `cron_*` 工具，`exec_tool` 可执行；
- 模型默认工具清单里没有 `cron_*`；
- 未启用 `tool-discovery` 时 `find_tool` 不存在，cron 管理页仍可用。

## 10. P7：清理与文档

任务：

- 评估 `turn.invoke` 是否保留为兼容桥；
- 删除 cron 对 `turn.invoke` 的依赖；
- 如果 Dispatcher 覆盖普通消息，删除 Web 中重复的 `thread.create + turn.start` 逻辑；
- 更新 `docs/05/06/07/09/11`、AGENTS；
- 把 ToolManager 的可见性/快照/执行约束写回正式插件与运行时文档；
- 更新协议/错误码/能力位；
- 全量测试与 verify。

验收：

- `go test ./...` 通过；
- `go vet ./...` 通过；
- `adag verify` 通过；
- 文档无孤立/过期链接；
- 多工作区 cron 删除测试通过。

## 11. 测试矩阵

| 用例 | 预期 |
|---|---|
| Web 普通消息 | 走 Dispatcher，行为与现有测试一致 |
| 运行中发送 | `auto` 转 steer；`start` 返回 busy |
| workspace 空 | 默认工作区 |
| workspace 不存在 | 明确错误，不静默回落 |
| sender 伪造 | 被 token/宿主盖章覆盖或拒绝 |
| 幂等键重复 | 只启动一次 |
| cron 多工作区 | 只启动一个 scheduler |
| cron 同分钟 | 只触发一次 |
| cron 禁用 | scheduler 停止，不再触发 |
| cron 工具发现 | 默认工具清单无 `cron_*`；`find_tool` 能列出 |
| cron 工具执行 | `exec_tool` 可执行；审批链按内层工具重跑 |
| tool-discovery 禁用 | `find_tool` 不存在；cron 管理页仍可用 |
| ToolManager 可见性 | 模型声明 / find_tool / exec_tool 使用同一规则 |
| 非 discovery 工具经 exec_tool | 返回 `tool_not_discoverable`，Runner 不执行 |
| 插件禁用/重载 | 该插件工具原子移除，无残留 |
| turn 快照 | 本轮内工具声明与查找稳定，中途禁用不影响已发出声明 |
| scheduler 崩溃 | 按策略重启 |
| 进程路径越界 | 拒绝注册 |
| 进程数超限 | 拒绝并给出限额 |
| Gateway shutdown | 全部进程回收，无孤儿 |
| Core 断连 | 引用清理，进程在零引用时停止 |

## 12. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| Core→Gateway 反向调用 | 新增攻击面 | loopback + per-core token + 白名单 + 审计 |
| 插件注册任意进程 | 任意代码执行 | v1 仅 builtin；entry 限插件目录；禁止 shell |
| 全局进程引用计数错误 | 进程提前停止/泄漏 | Core 会话绑定；断连清理；单测覆盖 |
| cron 无工作区时不运行 | 后台任务漏触发 | 明确接受 B，或补 Gateway 预注册 A |
| Dispatcher 抽取改变 Web 语义 | 回归 | 保留现有 Web 测试；逐步替换 |
| 进程日志含密钥 | 泄漏 | 限长/脱敏；不默认暴露给浏览器 |
| 多进程注册竞争 | 重复启动 | 单飞 + 引用计数 + 注册表锁 |
| ToolManager 与 Catalog/Invoker 双入口 | 规则漂移、旁路 | Manager 成为唯一门面；Catalog 纯注册表；engine 只拿快照 |
| 快照与运行中禁用竞态 | 模型声明与执行不一致 | 每 turn 快照；未执行调用回 `tool_not_found` |
| 单工具实时启停 | 模型上下文漂移 | v1 只随插件生命周期变化；后续显式失效快照 |

## 13. 回滚策略

- Dispatcher：保留旧 Web 调用路径开关，按工作区灰度；
- ProcessManager：新增组件可整体停用；cron 回退到旧 `turn.invoke`（临时）；
- cron scheduler：保留 feature flag，异常时可回退；
- 内部端点：仅 loopback，关闭后插件桥返回不可用，不影响普通 Web。

## 14. 待确认

1. `process.supervise` 是否只给 builtin？
2. cron 是否必须在“无工作区打开”时运行？
3. 是否允许 verified 第三方注册全局进程？
4. `Dispatcher.Send` 是否暴露给所有 builtin 插件，还是只 cron？
5. 进程日志是否需要在插件页展示？
6. ToolManager 是否提供单工具启停，还是只随插件生命周期变化？
7. `tool.list` 是否继续返回 discovery 工具，还是按调用方视图过滤？
6. 是否保留 `turn.invoke` 作为兼容桥，还是直接删除？
