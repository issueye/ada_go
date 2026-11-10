# 插件运行时与治理

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§6.4 moejs 运行时与沙箱、§6.5 装配期结算、§6.6 钩子、§6.7 技能、§6.8 系统插件、§6.9 信任与热重载、§6.10 内核 seam
> 版本：0.3 · 2026-11-10

## 6.4 moejs 运行时与沙箱

**所有权**：每个插件一个 Runtime，绑定一个专用 goroutine。所有调用通过带缓冲的 channel 串行进入；Runtime 的值不跨 goroutine。v1 固定单 Runtime；`pool` 字段保留给后续版本，若未来开放多实例，每个实例独立初始化，共享状态必须走 `ctx.state`/`ctx.config`，模块级变量不保证跨实例一致。

**初始化**（宿主封装层伪代码；真实 API 以 `github.com/MoeForge/moejs` 的 `Options`/`NewRuntime`/`Compile`/`Link` 为准）：

```go
opts := moejs.Options{
    MemoryLimit: 64 << 20,
    Locale:      "en-US",
    // 系统插件也需要代码生成；第三方默认关闭 eval/new Function。
    DisableDynamicCode: caps.Trust != TrustBuiltin,
    Importer:           &moejs.Importer{Resolve: moduleResolver(policy)},
}
rt := moejs.NewRuntime(opts)
defer rt.Close()

// 宿主把 ctx.* 桥与 node:* 模块都装上；能力位与模块白名单已删除，
// 不再按清单裁剪「装什么」（2026-11-10 决策）。
host := NewHostBridge(pluginID, scope, state, globalConfig, events, secrets)
// 宿主模块表 + 上下文成员由 host 封装；具体 API 见 12。
mod, err := moejs.Link(entry, moduleResolver(policy))
if err != nil { return err }
if err := rt.Load(mod); err != nil { return err }

// 入口工厂由宿主从模块导出中取出并调用；结果走 JSON 边界。
factory := host.EntryFactory(mod)
ret, err := factory.CallContext(ctx)
if err != nil { return err }
if _, err := ret.Await(ctx); err != nil { return err }
```

**调用**：

- 工具执行：`fn.CallContext(ctx, args)`；`args` 由宿主从 JSON 解码为 Go 值，结果只接受 JSON 可序列化值，最大 1MB。单次预算取 `ToolDef.timeoutMs`，默认 120s。
- Provider 流：长任务，预算为建连 15s、空闲 90s、总时长 30 分钟；不受 5s 脚本调用超时约束，但必须响应轮次取消。
- 异步：插件函数可能返回 Promise；宿主用 `Value.Await(ctx)` 结算。
- 钩子：`fn.CallContext(ctx, event)`；默认 200ms，最大 2s；超时记 `plugin_timeout`，按钩子失败方向处理。
- 路由/动作：`fn.CallContext(ctx, request)`；默认 5s。
- 宿主异步：插件里 `await` 的宿主函数返回 Promise；宿主在别的 goroutine 完成工作，结果回投到 Runtime 的 goroutine。

**回收策略**：

- 超时、超内存、引擎内部错误、脚本 panic → 标记该 Runtime 为脏，关闭并重建；当前调用返回 `plugin_crashed`，插件状态变为 `degraded`，下一次调用前重新初始化。
- 连续 3 次失败 → 熔断自动禁用插件，页面提示原因，用户仍可手动重载；禁用进入冷却（首次 5 分钟），冷却结束后半开探测：成功即恢复，失败则冷却翻倍（上限 10 分钟）。
- 取消不算插件的错（不计连续失败）。
- 关闭时取消它启动的所有异步工作，避免 goroutine 泄漏。

**Node 兼容模块**：默认安装纯计算模块；`fs`、`child_process` 与 `process.env` 的**功能边界**仍由宿主执行（工作区根、无 shell、白名单），但模块白名单与安装矩阵已删除（2026-11-10 决策）——宿主不再按清单能力位否决 `node:*`。`http`/`net`/`dns`/`worker_threads`/`vm` 仍不安装。模块表、成员包装、错误码与 Node 20 双跑语料见 [12 §6.12](12-node-compat.md)。

**为什么不在脚本里给 `node:fs` 全量**：`node:fs` 的写成员受 Scope 约束；但 `ctx.fs` 的读成员按 2026-10-31 决定不限制目标路径，因此 agent 的 `read_file`/`list_files`/`search_files` 可以读宿主绝对路径，包括插件目录、配置目录与 `secrets.toml` 所在目录。密钥的**值**仍只经 `secret` broker 按名提供；磁盘上的密钥文件是否可读由操作系统权限与部署方式决定。写成员现在不再按能力裁剪：越出工作区根时调用抛越界错误。

**前端渲染运行时**：Gateway 侧独立一套 Runtime，只执行 TSX 产物，不安装任何 stdlib；每个页面/插槽渲染 100ms/300ms 超时，超时重建。详见 [14 §14](14-ui-runtime.md)。

## 6.5 装配期结算

加载流程（两阶段）：

```
扫描来源（系统插件目录 / 工作区 / 全局安装目录） → 解析/校验 manifest
   → 来源决定信任上限；系统插件取预编译 bundle，第三方编译 TS
   → 创建 Runtime → 执行入口
   → 工厂调用 register* 收集到 staging
   → 校验（重名、schema、钩子等级、插槽名、模板可编译）
   → 原子提交：provider 目录 / 工具目录 / 提示词 / 动作 / 路由 / 钩子链 / 前端注册表
   → 运行期只读
```

- 任何一个贡献非法，整个插件回滚，其他插件不受影响；错误记录到 `plugin_meta.last_error` 与页面。`.node` 原生扩展在静态检查期拒绝；`node:*` 模块不再按白名单在链接期否决（2026-11-10 决策）。
- 运行期再次 `registerTool` 等调用只记警告，不改变当前目录；要生效必须 `plugin.reload`。
- 重载采用“双代”：新 Runtime + 新贡献构建完成后，原子替换注册表；在途调用继续用旧代，结束后关闭旧代。系统插件单插件重载是原地重建，不重建全部装配；第三方变更触发装配级重载。
- 钩子链按 `priority` 排序；同级按插件 id 稳定排序，保证回放确定性。
- **缺目录不静默丢弃**：有贡献但装配没有对应目录 → 装配失败。
- **重建槽位一致**：脏运行时重建时，入口工厂必须注册出与已提交贡献相同的登记项集合；否则拒绝并降级。

## 6.6 钩子

14 个点位，全部有真实调用方；成对点位必须成对出现（`turn.before/after`、`model.beforeRequest/afterResponse`、`compaction.before/after`）。`tool.beforeCall` 点位已随审批策略删除（2026-11-10 决策），`tool.afterCall` 保留为纯观察点。

| 点位 | 时机 | 允许动作 | 失败方向 |
|---|---|---|---|
| `session.start` | Core 装配完成、会话加载后 | observe, note | 开 |
| `session.end` | Core 关闭前 | observe, note | 开 |
| `thread.created` | 新线程创建后 | observe, note | 开 |
| `user.message` | 用户消息追加后、进模型前 | observe, note | 开 |
| `turn.before` | 轮次开始、终止条件初始化前 | observe, note, abort（builtin） | 开 |
| `turn.after` | 轮次结束、TurnFinished 前 | observe, note | 开 |
| `model.beforeRequest` | 请求模型前 | observe, note, replace/abort（builtin） | 开 |
| `model.afterResponse` | 收到终止信号后 | observe, note | 开 |
| `tool.afterCall` | 工具回执生成后 | observe, note | 开 |
| `compaction.before` | 生成摘要前 | observe, note | 开 |
| `compaction.after` | 压缩 entry 写入后 | observe, note | 开 |
| `interaction.requested` | 交互请求发出后 | observe, note | 开 |
| `plugin.loaded` | 插件贡献提交后 | observe, note | 开 |
| `error` | 轮次内可恢复错误 | observe, note | 开 |

动作分级：

- `observe`：只读，任何插件。
- `note`：向界面/日志附加说明，不改变数据面。
- `replace`：仅 builtin 系统插件；`model.beforeRequest` 替换系统提示/注入上下文。
- `abort`：仅 builtin；`turn.before` / `model.beforeRequest`。

`deny` 动作等级已随审批策略删除：基座没有「执行前拒绝」的钩子路径，工具执行不受任何插件判决。

约束：

- 第三方插件不能进入控制面；系统插件进入控制面也必须有清单声明与门禁检查。
- 钩子超时默认 200ms；超时/异常不阻塞主循环。
- 钩子内不能再次触发同一钩子（防递归）；调用深度上限 2。
- 钩子收到的是 JSON 快照，不是可变的引擎对象；要改数据必须返回动作，由引擎裁决。
- **钩子不是内核替换机制**：要替换压缩、存储、模型等内核机制，走 §6.10 的 seam，不要给钩子加超级权限。

需要「执行前询问」的场景不再是钩子：由工具实现在自己的调用里用 `ctx.ui.confirm/select/input` 完成（`tools-ask` 就是这么做的），或者由工具返回 `denied` 回执。基座只负责把 `ctx.ui` 的请求送到 Gateway、等待响应并返回，它不知道这次询问的业务含义。

## 6.7 技能

- 目录：`plugins/<id>/skills/<name>/SKILL.md` 与 `<workspace>/.adago/skills/<name>/SKILL.md`。
- 遵循 Agent Skills 约定：YAML frontmatter 的 `name` 必须等于目录名（1–64，小写/数字/连字符），`description` 不超过 1024 字符。
- 装配期把 name/description/path（XML 片段）注入系统提示；正文不进入基座内存，模型需要时用 `read_file` 按 path 直接读取。
- 技能冲突（同名）由工作区优先；冲突记录警告，不静默覆盖。

## 6.8 系统插件

系统插件是 `trust=builtin`、随产品发布、可禁用的普通插件；它们与第三方使用同一个 moejs 宿主、同一份清单与同一套注册契约。区别只有信任级别（可用控制面钩子）与随包发布。`trust` 不再决定「装什么」：能力位与模块白名单已删除（2026-11-10 决策），宿主对系统插件与第三方插件安装同一套桥与模块。

| 类别 | 系统插件 | 贡献 / 拥有的逻辑 |
|---|---|---|
| Provider | `provider-openai-chat`、`provider-anthropic-messages`、`provider-openai-responses` | `registerProvider`；协议、流解析、鉴权头与模型能力 |
| 工具 | `tools-fs` | `read_file`/`write_file`/`edit_file`/`list_files`/`search_files` |
| 工具 | `tools-shell` | `run_command`/`run_background`/`check_task`/`wait_task`/`kill_task` |
| 工具 | `tools-git` | 只读 `git_status`/`git_diff`/`git_log`；写操作走 `run_command` |
| 工具 + UI | `tools-plan` | `update_plan`/`read_plan`/`reflect_plan` + `chat.corner` Todo 卡片 |
| 工具 + UI | `tools-ask` | `ask_user` + `thread.tray` 提问卡片 |
| 工具 | `tools-skill` | `skill_search`（正文由模型按 path 读取） |
| 工具发现 | `tool-discovery` | `find_tool`/`exec_tool`；目录只读投影 + 按名执行，内层执行沿用同一套超时与循环检测 |
| 外部工具 | `mcp-client` | 装配期发现 MCP `tools/list`，注册为普通工具；HTTP/stdio，见 [15 §18](15-extensions.md) |
| 外部工具 | `browser-use` | browser-use 回环 API 的 14 个工具 + 随包技能 |
| 人格 | `persona-coding` | `registerPrompt`：编码助手人格与领域规则 |
| 定时任务 | `cron-manager` | cron 表达式、任务模型、调度循环、运行历史、到期触发；`global.config` 存任务，不依赖基座 cron |
| 环境变量 | `env-manager` | env 命名空间、校验、管理页与覆盖语义；`global.config` 存值 |
| 供应商管理 | `provider-admin` | 供应商/端点/API Key/模型清单 CRUD 与页面；配置存 `global.config`，列表/切换用 `model.admin`，`/models` 走 `ctx.fetch` |
| 用量/遥测 | `token-usage` | 工作区/线程 Token 与成本页面、概览卡；`events.read` 聚合 |
| 用量/遥测 | `composer-telemetry` | 输入框下方辅助区（运行中/工作区/缓存/上下文环）；`clientOnly` |
| 插件管理 | `plugin-helper` | 创建/检查/启停插件；`plugin.admin` 安装桥 |
| 示例/图表 | `ui-charts`、`ui-hello-tsx` | 图表与示例页面 |

系统插件不是基座：基座源码不出现这些 id；产品声明选择启用哪些；禁用后能力必须消失。**禁止把系统插件的业务逻辑回落到基座**：

- `cron-manager` 禁用后，基座不得继续读 `cron.json`、解析 cron 表达式或调度任务；`/api/cron/fire` 与 `cronstore` 已删除，调度由插件用 `agent.every` + `ctx.turn.invoke` 完成。
- `env-manager` 禁用后，基座不得继续渲染 env 管理入口；基座只保留“向子进程注入已配置环境”的通用执行点。
- `provider-admin` 禁用后，基座不得继续提供供应商/模型 CRUD 页面；`model.list`/`model.set` 只作为只读目录与当前模型选择的通用 RPC。
- `token-usage`/`composer-telemetry` 禁用后，基座不得继续渲染对应页面/卡片；`events.read` 只提供事件缓冲与每轮原始用量行，展示口径聚合归插件。

`policy-default` 已删除（2026-11-10 决策）：写/执行工具现在直接执行，没有策略插件、没有「无策略即拒绝」的失败关闭。禁用任何系统插件都只让该插件自己的贡献消失，基座不补位也不改判定。

## 6.9 信任、签名与热重载

- `builtin`：系统插件；预编译 bundle 随产品发布，只能启用/禁用，不能用磁盘同名插件覆盖；运行时仍走 moejs，与其他插件同一套能力与审计。
- `verified`：`manifest.sig` 为 Ed25519 签名，覆盖清单 + 文件哈希；公钥在配置里。签名失败按第三方处理并提示。
- `third-party`：默认；能力受限，但**前端贡献面与系统插件相同**（slots/pages/scripts/client/blocks 都可声明），由 `frontend.script` 能力、安装确认、CSP 与审计控制风险。
- **来源决定信任上限**：磁盘插件最高 third-party；`builtin` 只接受随包/官方目录；`verified` 必须验签。当前发现期已强制来源上限并对磁盘 `verified` 做 Ed25519 验签：签名覆盖“去 sig 的清单 JSON + 除 plugin.json 外的目录摘要”，公钥来自 `ADAGO_PLUGIN_PUBKEYS` 或 `~/.adago/plugin-keys.json`；失败降级 `third-party`。实现见 [09 §10.2](09-security-operations.md)。
- 安装：`adag plugin install <zip|dir>`，校验路径穿越、大小上限、文件类型；复制到目标目录后原子替换。安装/预检**不得执行插件代码**：当前预检只做清单、入口编译/链接与 `.node` 原生扩展的静态检查（模块白名单已删除）；`registerTool` 等贡献形状留给显式启用/重载。
- 热重载：`plugin.reload` → 系统插件原地重建，第三方触发装配级双代；新代构建成功后才原子替换，在途调用走旧代。
- 版本：插件 id + 版本 + 文件指纹写入 `plugin_meta`；`apiVersion` 主版本不兼容则禁用并提示，不阻止主程序启动。

## 6.10 内核实现与替换 seam

内核机制**默认有 Go 实现**，同时保留受控 seam。seam 是给装配层/适配器用的稳定接口，不是插件 hook 的超级权限。

| 内核机制 | 默认实现 | seam | 替换者 | 必须通过 |
|---|---|---|---|---|
| 模型流 | provider 插件桥接 `ModelClient` | `ModelClient` 接口 | provider 插件 / 适配器 | 流式契约、终止信号、取消、重试边界 |
| 压缩 | `ModelCompactor` | `Compactor` 接口 | 装配注入（系统插件或适配器） | 切点、结构化摘要、失败不丢历史、`context_overflow` |
| 工具执行 | `toolkit.Executor` | `ToolRunner` 接口 | 装配注入 | 参数校验、超时/截断/检查点 |
| 会话存储 | SQLite `SessionStore` | `ports.SessionStore` | 适配器 | DAG、游标、分支、压缩 entry、单写者并发 |
| 提示词 | 安全前言 + 插件片段 | `PromptSource` | 插件贡献 / 装配 | 安全前言不可移除、排序确定 |
| 交互桥 | `InteractionBroker` | `ports.InteractionBroker` | 适配器 | 失败关闭、幂等、持久化、超时 |
| 事件 | `EventSink` + 快照/补丁 | `ports.EventSink` | 适配器 | seq 单调、快照/补丁一致 |
| 工具发现 | `tool-discovery` 系统插件 | `tool.discovery`（`ctx.catalog`/`ctx.tool.invoke`） | builtin 系统插件 | 目录只读、内层执行不绕过超时与循环检测 |
| 消息投递 | 网关 `Dispatcher`（界面与插件共用一条路径） | `dispatch.send`（`ctx.dispatch.send`） | builtin 系统插件 | 令牌认进程、sender 由宿主盖章、`/internal/*` 不进用户鉴权 |
| 插件长驻进程 | 网关 `processmgr` + `/internal/processes/*`（注册/退避重启/引用计数/进程树） | `process.supervise`（`ctx.processes`） | builtin 系统插件 | owner 宿主盖章、entry 限插件目录、禁止 shell、环境剥宿主凭据 |
| 定时任务 | `cron-manager` 系统插件 | `global.config` + `process` + 通用轮次触发桥 | builtin 系统插件 | 任务模型/调度/历史自持，基座无 cron |
| 环境变量 | `env-manager` 系统插件 | `global.config` + 子进程注入点 | builtin 系统插件 | 键名/0600/不写日志，基座无 env 管理 |
| 供应商配置 | `provider-admin` 系统插件 | `global.config` + `model.admin` | builtin 系统插件 | 密钥不回显、CRUD 归插件 |

规则：

1. 替换实现只能在装配期注入，不能在运行期热替换。
2. 替换实现必须通过对应契约测试与安全不变量；安全边界（scope、交互失败方向、进程树终止、审计、VNode 白名单）不可替换。
3. 不为单一插件新增专用原语；能用通用原语组合的，优先组合。
4. seam 的 API 变更走版本与弃用周期；新增 seam 必须同时提交消费者与契约测试。

---

**相邻文档**：[← 插件模型与后端 API](05-plugins.md) · [索引](design.md) · [SSR 前端 →](07-ssr-frontend.md)
