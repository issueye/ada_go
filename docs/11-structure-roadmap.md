# 目录、里程碑、决策与风险

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§13 目录结构、§14 里程碑与交付状态、§15 风险与开放问题、§16 系统插件迁移、§17 文档治理
> 版本：0.2 · 2026-11-09
> 本文吸收原 `11-structure-roadmap.md` 与 `11-structure-roadmap.md`：不再单独维护计划/决策文档。

## 13. 目录结构

```
ada-go/
  go.work                  本地 workspace：. 与 ../moejs
  go.mod                   ada-go 唯一 Go module；发布依赖版本在这里锁定
  cmd/adag/                main：gateway / core / plugin / doctor / verify
  app/                     组合根：产品声明 + 磁盘 plugins/official/ → coreapp
  internal/
    common/                跨端共享（无业务语义）：domain、ports、proto、diag、version
      domain/              消息、Part、工具描述符/回执、事件、错误、失败方向
      ports/               12 个端口接口（内核 seam）
      proto/               生成的方法常量、DTO、错误码
    gateway/               唯一对外 HTTP 进程的全部实现
      gateway.go           Gateway 装配、路由与生命周期
      contracts/ security/ supervisor/ relay/ sse/ dispatch/ processmgr/
      web/                 SSR handlers、模板、静态资源、投影缓存、ui/
      frontend/            esbuild、moejs render、VNode、插槽注册表
    agent_core/            Agent Core：唯一引擎、插件宿主、会话运行时
      engine/              RunTurn、终止条件、Compactor seam
      plugins/             manifest、loader、host(moejs)、contrib、hooks、skills、provider
      coreapp/ corecontracts/ coreproto/ corerpc/ cli/
      conformance/         端口契约、不变量、golden、nodecompat、ui 基线
    common/adapters/       sqlite、scope、clock、idgen、secret（零 provider 实现）
    common/toolkit/        描述符注册表、scope 校验、文件变更队列、检查点（零具体工具）
  plugins/
    official/              系统插件包（trust=builtin；构建期预编译 dist/）
      provider-openai-chat/ provider-anthropic-messages/ provider-openai-responses/
      tools-fs/ tools-shell/ tools-git/ tools-plan/ tools-ask/ tools-skill/ tools-subagent/
      tool-discovery/       find_tool/exec_tool（系统插件；宿主桥 tool.discovery）
      persona-coding/ provider-admin/ token-usage/
      env-manager/ cron-manager/ composer-telemetry/ plugin-helper/
      mcp-client/ browser-use/ ui-charts/ ui-hello-tsx/
    hello-tsx/ task-plan/ ask-user/   第三方示例（安装时编译）
  products/
    coding.agent.json
    skeleton.agent.json
    headless.agent.json
  spec/
    proto.json             协议唯一手写源
    manifest.schema.json   插件清单 Schema
    vnode.schema.json      VNode Schema
  docs/                    16 份设计正文 + design.md（见文档地图；不新增计划/评估文档）
  tools/
    genproto/              生成 methods_gen.go
    bundle-plugins/official/       预编译 plugins/official/* → dist/
    verify/                接线/能力/插槽/文档/边界门禁
```

约束：

- `internal/` 下不出现循环依赖；`domain` 不 import 其他内部包（除标准库）。
- `internal/` 不 import `plugins/official/` 下的代码或资源；`plugins/official/` 只使用 `agent:*` / `ssr:*` 虚拟模块与公开 TS 类型，不 import 基座 Go 包。基座源码不出现任何系统插件 id。
- 基座不提供平行的 Go 插件注册 API；系统与第三方必须走同一个 TS 入口、同一个 moejs 宿主与同一套贡献校验。
- Gateway 与 Core 不共享可变内存；共享的只有 `proto` DTO 与 `diag` 码。
- 网关模板/静态资源通过 `embed.FS`；系统插件从磁盘 `plugins/official` 读取（Electron resources/仓库），不进入二进制。

### 13.1 Go workspace

ada-go **保持单一 Go module**。`go.work` 只用于把本仓库与相邻的 moejs 源码联合起来做本地开发、上游缺口修复与双跑验证；不把 `internal/` 拆成多个 module。

```bash
# 在 ada-go/ 下执行；moejs 是相邻目录
go work init . ../moejs
```

规则：

- `go.work` 只写**相对路径**，提交 `go.work` 与 `go.work.sum`；不得提交个人机器上的绝对路径。
- 日常开发默认 workspace 模式；`go.mod` 始终保留真实 moejs 版本要求。
- CI 同时跑 workspace 与 `GOWORK=off` 两种模式；发布、安装包与漏洞扫描一律 `GOWORK=off`。
- 执行依赖整理与校验时使用 `GOWORK=off go mod tidy`、`GOWORK=off go mod verify`。

### 13.2 依赖

| 包 | 用途 | 说明 |
|---|---|---|
| `github.com/gin-gonic/gin` | HTTP 路由与中间件 | SSR、SSE、静态资源 |
| `github.com/MoeForge/moejs` | 纯 Go JS 运行时与沙箱 | 无 cgo；插件与 TSX 渲染；宿主只安装已声明能力 |
| `github.com/evanw/esbuild/pkg/api` | TS/TSX 去类型、JSX 转换、打包 | 构建期编译系统插件；第三方安装/加载期编译；TSX 服务端渲染 |
| `modernc.org/sqlite` | SQLite 驱动 | 纯 Go，保持交叉编译 |
| `github.com/coder/websocket` | WebSocket | context 友好，Gateway ↔ Core |
| `github.com/yuin/goldmark` | Markdown 渲染 | 关闭 unsafe |
| `github.com/microcosm-cc/bluemonday` | HTML 清洗 | Markdown 输出兜底 |
| `github.com/BurntSushi/toml` | 配置解析 | |
| `github.com/oklog/ulid/v2` | 可排序 id | 线程、entry、run |
| `github.com/gofrs/flock` | 单实例锁 / 全局配置锁 | 防止同一数据目录起两个 Gateway |
| `golang.org/x/sync` | errgroup / semaphore | 预取、并发工具 |

所有依赖锁定在 `go.mod`/`go.sum`；CI 校验 `go mod verify` 与许可证清单。不引入需要 cgo 的依赖。

---

## 14. 里程碑与交付状态

| 阶段 | 交付 | 状态 |
|---|---|---|
| M0 契约冻结 | 设计、`proto.json`、manifest/VNode Schema、工具描述符、事件模型、端口 | 完成 |
| M1 无头内核 | `RunTurn`、TS 插件宿主、Node 兼容子集、SQLite、系统工具/策略、CLI | 完成；provider/工具按系统插件装配 |
| M2 网关与 SSR | 工作区监督/回收、鉴权、SSE、对话页、交互托盘、无 JS 降级、a_da 复用 | 完成 |
| M3 插件平台 | 第三方安装、能力安装、装配期结算、15 钩子、技能、双代重载、Node 矩阵 | 完成 |
| M4 前端插件 | TSX→VNode、12 插槽、页面/动作/路由、组件库、岛屿 | 完成 |
| M5 加固发布 | 三家 provider、检查点回滚、子智能体、打包、审计、发布清单 | 完成（0.3.0） |
| M6 基座收敛 | cron/env/provider-admin/usage/discovery 迁移为系统插件；`global.config`/`events.read`；第三方前端同面；文档收敛 | 进行中，见 §16 |

### 14.1 工程决策（D-1…D-7）

| 编号 | 决策 |
|---|---|
| D-1 | 模块路径 `github.com/adag/ada-go`；二进制 `adag`；Go 1.26；依赖以 `go.mod` 为准 |
| D-2 | moejs 以 `go.work` 联调，发布 `GOWORK=off`；上游未合并时允许固定 fork，禁止隐式补丁 |
| D-3 | a_da UI 基线 commit `ae2f8ffe…`；只复用 token/CSS/DOM/组件，不引入 Alpine |
| D-4 | 错误码真源分层：`spec/proto.json` → `methods_gen.go` → `domain/errors.go` → `diag/codes.go`，verify 对齐 |
| D-5 | 系统插件 id 只允许出现在 `products/*.agent.json`、`tools/verify/boundary.go`、插件包自身与测试夹具；基座源码（含注释）禁止 |
| D-6 | 压缩 entry 的 payload = `CompactSummary` + `firstKeptEntryId`；循环检测的 `Repeatable` 只进 Core 内存，不上 wire |
| D-7 | moejs 上游缺口实测：模块白名单在宿主 resolver；`node:fs` 用上游 + 宿主包装；`node:child_process` 由 ada-go 自建；`http/net/dns/worker_threads/vm` 不装；`.node` 拒绝 |

### 14.2 契约缺口（G 系列）

已处理/仍开放的关键项：

| 编号 | 状态 |
|---|---|
| G-1 线程/技能/模型/交互不存在类错误码 | 暂借 `invalid_params`，后续补码 |
| G-2 握手/版本不符错误码 | 暂用 `internal_error` + `ADG-CORE-*` |
| G-3 `Repeatable` 不上 wire | 已按 D-6 处理 |
| G-7 `thread.branch` 语义 | 移动 head，不新建线程 |
| G-9 前端 `api` 字段 | 未收；插件自有端点走后端 `route()` |
| G-10 `pool` 位置 | `backend.pool`，v1 >1 装配失败 |
| G-12 时间格式 | RPC 层 RFC3339Nano UTC；DB 层 INTEGER |
| G-14 动态路由 path 参数 | 插件从 `Path` 自行解析；`HTTPAPIRequest` 暂无 `pathParams` |
| G-15 产品能力位 `images` | 由 coreapp 注入引擎 `Params.ImagesAllowed` |

其余 G-4/5/6/8/11/13/16 已在 02/04/07/08 统一措辞或按现状冻结；新增缺口先改设计再改实现。

---

## 15. 风险与开放问题

### 15.1 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| 信任自声明 / 无验签 | 磁盘插件冒充 builtin，获得 `exec`/`plugin.admin` | 来源→信任上限、Ed25519 验签、`fs.write` 写保护；见 09 §10.4 |
| 第三方同源脚本 | XSS/读 DOM/同源请求 | 安装确认、`frontend.script` 最小化、CSP、审计；可选 iframe/Worker |
| 全局配置并发/损坏 | env/cron/provider 配置写坏 | 0600、原子写、跨进程锁、schema 校验、备份 |
| cron 迁移 | 调度/运行历史迁移期双写或漏触发 | 已完成：单写切换到 `global.config`，旧 `/api/cron/fire`/`cronstore` 已删除，旧 `cron.json` 由插件一次性导入 |
| 热路径插件化过度 | 每 token/每轮跨 JS 边界变慢 | 压缩/上下文/流式留在内核；插件只做冷路径编排 |
| SQLite 多进程锁 | 写入失败、第二个 Core 抢写 | 工作区独占锁、WAL、单写者协程、CLI 只读、Online Backup |
| 系统插件全部走 moejs | provider 流式开销、内存占用 | 预编译、每插件 Runtime、长调用独立期限、压测 |
| Node 兼容预期过高 | 插件用了未支持模块 | 模块矩阵、链接期失败、Node 20 双跑语料 |
| 轮次不设步数上限 | 模型原地打转 | 循环检测（3 次回执/5 次结束）、超时、压缩、可取消 |
| 插件 API 频繁变化 | 第三方失效 | `apiVersion` 主版本、弃用周期、契约测试、示例即文档 |
| 任意可执行程序 | 工作区外执行 | 默认策略 `ask`、readonly、审计、UI 明示；不宣称强沙箱 |
| Windows 进程监督 | 孤儿进程/端口占用 | 父进程心跳、`taskkill /T`、退出握手、doctor |
| 协议/文档漂移 | 前后端不兼容 | JSON 单源 + 生成 + `adag verify` + 文档地图门禁 |
| 基座边界漂移 | 基座逐渐内置业务 | `verify boundary` + 删除测试 + 空基座启动 + 评审清单 |
| a_da UI 漂移 | 两套观感 | 固定上游 commit、基线进仓库、`verify ui`、golden 截图 |

### 15.2 开放问题

1. **产品名**：`ada-go` 是工作名；发布前统一。
2. **插件 Runtime 池**：默认 1 是否足够？压测后再开放 `pool`。
3. **策略 allowlist 持久化**：由策略插件自行决定；基座不引入。
4. **MCP 流式/动态密钥前缀**：见 [15 §18](15-extensions.md)；仍待后续。
5. **多用户**：v1 单用户；多用户需重做工作区键、cookie、审计与密钥隔离。
6. **前端插件缓存**：只读插槽是否按数据哈希缓存？先测命中率。
7. **JIT**：暂不默认启用；SSR 规模变大再评估按平台开启。
8. **iframe 隔离**：第三方同源脚本的可选隔离方案；不作为 v1 前置。

---

## 16. 系统插件迁移路线

目标：基座零业务语义、零产品兜底。迁移按“先安全、再原语、后功能”的顺序；每一步都必须有删除测试。

| 阶段 | 工作 | 前置 | 验收 |
|---|---|---|---|
| P0 安全前置 | 来源→信任上限、Ed25519 验签、安装/预检不执行代码、`fs.write` 对插件清单/`dist/` 写保护 | 无 | 磁盘插件不能声明 builtin/verified；`check_plugin` 不再触发装配 |
| P0 文档/门禁 | 文档地图收敛；删除测试；`verify boundary` 增“基座无产品数据文件/端点”断言 | 无 | 新增文档必须登记；禁用系统插件测试全绿 |
| P1 通用原语 | `global.config`（命名空间/0600/原子写/跨进程锁）、`events.read`（只读/游标/脱敏） | 无 | 契约测试；限额；审计 |
| P1 工具发现 | `tool-discovery` 系统插件；builtin-only `tool.discovery`（`ctx.catalog`/`ctx.tool.invoke`） | P1 原语 | 禁用后 find/exec 消失；内层审批链不绕过 |
| P1 用量/遥测 | `token-usage`/`composer-telemetry` 改用 `events.read` 自聚合；基座只留每轮原始计数 | `events.read` | 禁用后页面/卡片消失；基座不再聚合展示 |
| P1 工具管理 | 基座内部 `ToolManager` 集中工具注册/按 owner 摘除/可见性视图/turn 快照/执行路由；可见性规则收敛到 `domain.ToolVisibility` | P1 工具发现 | 模型声明、`find_tool`、`exec_tool` 与 `tool.list` 用同一份规则；`exec_tool` 不能执行注入式工具 |
| P2 调度层 | 网关唯一发送路径 `Dispatcher`（界面与插件共用）；builtin-only `dispatch.send` 宿主桥 + 回环控制端点（token 认进程、sender 由宿主盖章） | P1 工具管理 | 伪造 sender 无效；非令牌 401；`/internal/*` 不走用户鉴权；插件子进程拿不到宿主凭据 |
| P2 env | `env-manager` 用 `global.config` 管理命名空间；基座只保留子进程注入点 | `global.config` | 禁用后管理页/注入策略消失 |
| P2 provider 管理 | `provider-admin` 用 `global.config` 管理供应商；基座只读配置视图 + `model.list/set` | `global.config` | 禁用后 CRUD 页面消失；`model.set` 仍可用 |
| P2 cron | `cron-manager` 用 `global.config` + `agent.every` 通用定时器 + `turn.invoke` 轮次触发桥；基座删除 `cronstore`/`cronproc`/`/api/cron/fire` | 已完成 | 禁用后调度消失；`cron.json` 不再被基座读取 |
| P3 第三方前端 | 与系统插件同面；安装确认、CSP、审计、可选 iframe | P0 信任 | 第三方页面/插槽/脚本装配成功；删除后无残留 |
| P3 文档收尾 | 05/06/07/09 与代码一致；删除旧文档；`verify docs` 通过 | 全部 | 文档地图 16 份，无孤立文档 |

**当前进度（2026-11-09）**：

- P0 安全前置已完成：来源→信任上限、Ed25519 验签/降级、静态预检（不执行插件代码）、插件目录写保护。
- P1 通用原语：`global.config`（`ctx.config` + `internal/common/globalconfig`）与 `events.read`（`ctx.events.query` + Emitter 可回放缓冲，`ctx.events.usage` + SQLite 原始用量行）已落地；`tool-discovery` 系统插件已落地，引擎内联 `find_tool/exec_tool` 已删除；provider 配置存储已迁到 `global.config` 的 `provider-config` 命名空间（legacy `providers.json` 仅首次读取迁移，不再双写），`provider-admin` 已接管供应商 CRUD 与 `/models` 拉取（`ctx.fetch`）；基座 `model.admin` 只剩目录读取与当前模型切换，CRUD/预检桥已删除。`env-manager` 已完成迁移：变量读写/校验走 `ctx.config`（`env` 命名空间），bins 由 `ctx.fs` 投影，基座 `env.admin` 已删除；旧 `env.json` 仅迁移兜底读取。`token-usage`/`composer-telemetry` 已改为插件内聚合：页面动作/route 经 `events.read` 读取原始行，Gateway 不再注入 `data.usage`，`/threads/{id}/state` 只返回 `run`/`context`。下一阶段只剩 cron 迁移。
- cron 已完成迁移：`cron-manager` 用 `global.config` 存任务、`agent.every` 每 30s 调度、`ctx.turn.invoke` 触发轮次；基座 `cron.admin`/`cronstore`/`cronproc`/`/api/cron/fire` 与 `cron-sync` 门禁已删除。
- 工具管理已集中化：`internal/common/toolkit` 的 `ToolManager` 包住目录与插件路由，提供 `Injected`/`Discovery`/`System`/`All` 四种视图与 turn 级快照；可见性规则收敛到 `domain.ToolVisibility`，模型声明、`find_tool` 目录投影与 `exec_tool` 内层校验同源；`exec_tool` 现在拒绝注入式工具（`tool_not_discoverable`），`owner` 由宿主盖章且与目录不一致时拒绝路由。
- 调度层已落地：网关 `internal/gateway/dispatch` 是唯一发送路径（新建会话、发消息、编辑重发与 `/api/workspaces` 都走它），`auto`/`start`/`steer` 三种投递模式共用一份 busy/waiting/finishing 判定；builtin-only 能力 `dispatch.send` 让插件经 `ctx.dispatch.send` 交给同一调度层，网关用回环控制端点 + 每 Core 一次性令牌认证，`/internal/*` 不进用户鉴权链。
- P3 进程管理已落地：`internal/gateway/processmgr` 是网关名下非 agent-worker 进程（插件注册的长驻进程）的唯一父进程——注册/`on_enable`/`lazy` 启动、`no`/`on_failure`/`always` 指数退避重启、引用计数、每 owner 8 / 全局 32 限额、日志环（200 行、单行 2KB、凭据脱敏）、停止杀整棵进程树；`Gateway.Shutdown` 先停被管进程再关 Core，`Gateway.Processes()` 供插件桥接入。P4 进程桥已落地：builtin-only 能力 `process.supervise`（`ctx.processes.register/ensure/stop/status/unregister/list`）经 `/internal/processes/*`（回环 + 控制令牌）交给同一份 ProcessManager；entry 由 Core 宿主解析到插件目录内并拒绝越界，网关再校验可读性，owner 由能力集盖章；Core 重签令牌时按签发键回收旧代注册。 P6 cron 单进程已落地：`cron-manager` 在装配期注册 `scheduler`（scope=global，runtime=node，on_enable/on_failure），scheduler 自己解析 cron、经 `/internal/dispatch/send` 投递、按 task id 把运行历史合并回 `$ADAGO_HOME/config/cron.json`；每 Core 的 `agent.every` 循环已删除，多工作区不再重复触发。调度进程的凭据由网关按进程签发（只带 `dispatch:send` 范围，sender 盖章为 `plugin:cron-manager`），`ADAGO_HOME`/`ADAG_OFFICIAL_PLUGINS_DIR` 作为路径指针不再被环境剥离。
### 16.1 剩余系统插件迁移的先决条件

| 系统插件 | 当前基座依赖 | 先决条件 | 迁移后验收 |
|---|---|---|---|
| `token-usage` / `composer-telemetry` | 已完成：页面动作/route 经 `events.read` 读原始行并自聚合；Gateway 不再注入 `data.usage`，`/threads/{id}/state` 只留 `run`/`context` | `events.read` 的 `ctx.events.usage`（SQLite 原始行） | 禁用后页面/卡片消失；基座不再聚合展示 |
| `provider-admin` | `model.admin` 的 `/models` 拉取（CRUD 已迁到 `ctx.config`） | 把 `/models` 拉取移到 `ctx.fetch`（需要 ProviderInfo 带协议/鉴权元数据）；`model.admin` 只留 list/select | 基座只读配置视图 + 切换当前模型；拉取归插件 |
| `cron-manager` | 已完成：`global.config` 任务模型 + `agent.every` 通用定时器 + `ctx.turn.invoke`；基座已删除 `cron.admin`/`cronstore`/`cronproc`/`/api/cron/fire` | `agent.every` 与 `turn.invoke` | 基座不读 `cron.json`、不调度、不暴露 cron 端点；任务模型/历史归插件 |

这些不是“再写一个 UI”就能完成的迁移：每一项都要求先补对应的通用原语或调整配置作用域，否则会退化成把基座代码换个文件放。
**迁移原则**：

- 先补原语再搬功能；没有原语的迁移会退化成“把基座代码换个文件放”。
- 每个系统插件必须拥有自己的数据 schema、迁移/导出/卸载策略；基座不成为孤儿数据的兜底。
- 迁移期保留兼容桥时必须在文档与 UI 标注“迁移中”，不得静默双写。
- 系统插件禁用/删除后，基座回到空态；不能留下半截依赖。

---

## 17. 文档治理

- **正文 16 份**：00–15；`design.md` 只做索引与阅读路径。
- **不新增计划/评估/交付文档**：开发计划、风险、决策、性能实测、发布清单分别并入 11、10、09；一次性分析不进 `docs/`。
- **合并保留章节号**：14/15/19 合并后仍用 §14/§15/§19；16/18 合并后仍用 §16/§18；代码注释引用继续有效。
- **文档先行**：改能力位、钩子、插槽、协议、基座边界时，先改文档与 `spec/`，再改实现；`adag verify` 负责链接、地图与核心数字。
- **过期即缺陷**：文档未登记、成为孤岛、核心数字不符、链接失效，均按门禁失败处理。

---

**相邻文档**：[← 测试与性能](10-testing-performance.md) · [索引](design.md) · [moejs 与 Node 兼容 →](12-node-compat.md)
