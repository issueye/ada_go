# 插件系统分析：契约、运行时与边界执行

> 状态：实现分析（非设计草案）  
> 日期：2026-11-08  
> 分析范围：`internal/plugins/`、`app/`、`internal/cli/`、`internal/gateway/frontend/`、`plugins/official/`、`spec/manifest.schema.json`、`tools/verify/`  
> 方法：逐包阅读实现与测试，并与 [05-plugins.md](05-plugins.md)、[06-plugin-runtime.md](06-plugin-runtime.md)、[12-node-compat.md](12-node-compat.md) 以及 [插件 API SPEC](../plugins/official/plugin-helper/SPEC.md) 对照；不一致处在 §10 单列。  
> 定位：本文解释「代码现在实际怎么工作」，设计意图仍以设计文档为准；两者冲突时先修实现或先改文档，不要同时保留两种说法。

## 1. 摘要

ada-go 的插件系统不是「加载一段脚本」的运行时，而是一套**装配系统**：

1. **清单是契约**：`plugin.json` 声明身份、信任级别、能力、钩子、前端贡献；字段的每个校验点就是它的消费者，`adag verify` 再把 Go 结构、JSON Schema 与文档逐字比对。
2. **能力是安装出来的**：没有声明的能力在脚本里根本不存在；声明了但信任级别不够的，装配期直接失败。
3. **贡献两阶段提交**：入口工厂执行期只往 staging 里登记，校验通过后按固定顺序原子提交到工具/钩子/provider/动作/路由/前端目录，运行期只读。
4. **每个插件一个 Runtime、一条 goroutine**：脚本值不跨 goroutine，跨边界只有 JSON 字节；预算按调用类型分档，失败按「弄脏 → 重建 → 熔断 → 半开探测」回收。
5. **边界执行点集中**：文件、出网、命令、进程、交互、密钥、环境变量、定时任务、插件安装各自只有一个宿主实现，插件拿不到原始句柄。

### 1.1 关键数字（当前工作树）

| 维度 | 值 | 依据 |
|---|---|---|
| 官方插件 | 20 个目录 | `plugins/official/*/plugin.json` |
| 产品声明 | 3 份（coding / headless / skeleton） | `products/*.agent.json` |
| coding 产品启用插件 | 15 个 | `products/coding.agent.json` |
| 后端能力位 | 13 项 | `internal/plugins/contract.go` 的 `backendCapabilities` |
| 前端能力位 | 2 项 | 同上 `frontendCapabilities` |
| 钩子点位 | 15 个 | `internal/plugins/contract.go` 的 `hookPoints` |
| 前端插槽 | 12 个 | `internal/plugins/contract.go` 的 `slots` |
| 插件相关 RPC | 5 项（`plugin.list` / `reload` / `setEnabled` / `http` / `httpStream`） | `spec/proto.json` |
| 插件包生产代码 | 41 个文件、约 14.4k 行 | `internal/plugins/`（不含测试） |
| 插件包测试 | 29 个文件、约 5.4k 行、140 个测试函数 | `internal/plugins/**/*_test.go` |
| 全仓测试函数 | 904 个 | `*_test.go` 统计 |
| 官方插件 TS/TSX | 39 个文件、约 11.8k 行 | `plugins/official/`（不含 dist） |
| 前端渲染桥 | 21 个文件、约 5.2k 行 | `internal/gateway/frontend/` |

### 1.2 总体评价

**做得好的地方**

- **契约有单一真源**：能力位、钩子点位、插槽名只在 `internal/plugins/contract.go` 声明；`tools/verify` 的 `hooks` / `slots` / `capabilities` / `manifest` 四项检查把「声明—调用方—Schema—文档」串成闭环，孤儿点位与孤儿能力位会直接让门禁失败。
- **失败隔离贯穿装配与运行**：单个插件目录非法只记 warning；装配期任一贡献非法回滚整个插件而不影响其它插件；运行期连续失败只熔断该插件。`cli/session.go` 的「隔离重试」还会把第三方插件的装配错误归因后摘掉，避免一个坏插件让 Core 起不来。
- **能力安装不是运行期判断**：`node:fs`、`node:child_process` 未授权时在**链接期**失败，错误文本点名缺失能力，而不是跑起来拿到 `undefined`。
- **长任务与脚本预算分离**：`hostAsync` 在等待 UI/HTTP/state 时暂停脚本计时，避免 200ms 钩子预算误杀 300s 的交互或 30 分钟的 provider 流。
- **可观测面齐全**：`Summary`、`Warnings`、`plugin.check`、审计日志（密钥脱敏）、`plugin_meta.last_error` 让「为什么这个插件没起来」可回答。

**最需要关注的三件事**

1. **信任级别是自声明的，且没有验签/来源校验**（§10 P0-1）：磁盘插件写 `trust: "builtin"` 或 `verified` + 任意 `sig` 就能拿到 `exec`、`model.admin`、`plugin.admin` 与控制面钩子。这是实现与 `06 §6.9` 契约之间最大的缺口。
2. **安装与体检都会执行插件代码**（§10 P1-1）：`plugin.admin` 的安装预检、`check_plugin`（`access: "read"`）的诊断预检都会真实装配并执行入口工厂；后者等于一个「只读工具」能触发插件代码执行。
3. **文档与实现有多处漂移**（§10 P1-2）：第三方 TS 编译、Ed25519 验签、编译缓存、逐插件双代热重载、`adag plugin install ./dir` 等描述在代码里并不成立。

---

## 2. 分层与代码地图

### 2.1 包职责

| 位置 | 职责 | 关键文件 |
|---|---|---|
| `internal/plugins` | 契约层：清单结构、能力位、信任、钩子点位、插槽、钩子链 | `contract.go`、`manifest.go`、`hooks.go`、`sites.go`、`summary.go` |
| `internal/plugins/host` | moejs 宿主：Runtime 生命周期、模块加载、能力安装、贡献结算、渲染 | `host.go`、`runtime.go`、`load.go`、`caps.go`、`staging.go`、`agentapi.go`、`render.go` |
| `internal/plugins/host/node` | Node 兼容矩阵（数据表）：安装层级、能力、偏差、错误码、上游缺口 | `matrix.go` |
| `internal/plugins/host/nodemods` | 宿主自建的纯计算模块（path/util/events/crypto/zlib…） | `nodemods.go`、`crypto.go`、`zlib.go` |
| `internal/plugins/discovery` | 第三方发现、zip 安装、目录指纹、来源合并 | `discovery.go`、`install.go`、`hash.go` |
| `internal/plugins/provider` | 把 `registerProvider` 桥成 `ports.ModelClient`；供应商配置、重试 | `registry.go`、`config.go`、`retry.go` |
| `app` | 组合根：产品声明 × 插件包 × 宿主装配；官方插件唯一入口 | `app.go`、`registries.go`、`skills.go`、`routes.go` |
| `internal/cli` | 会话装配、插件发现/启停/热重载、`plugin.admin` 实现 | `session.go`、`plugin_state.go` |
| `internal/gateway/frontend` | 前端渲染桥：TSX→VNode→HTML、插槽/页面/块/岛屿、动作预取 | `bridge.go`、`render.go`、`load.go`、`vnode.go`、`schema.go` |
| `internal/toolkit` | 工具目录与执行器（策略门禁、审批后的实际调用） | `catalog.go`、`execute.go` |
| `internal/engine` | 轮次状态机；只认识 `ports`，不认识 moejs | `engine.go`、`run.go` |
| `tools/verify` | 开发门禁：14 项检查，其中 6 项直接约束插件契约 | `verify.go`、`checks.go`、`boundary.go` |
| `plugins/official` | 随包插件（TS/TSX 源码 + 构建产物 `dist/`） | `dir.go`、`build.mjs`、各插件目录 |

### 2.2 装配与调用全景

```
products/*.agent.json                    plugins/official/            ~/.adago/plugins/
        │                                      │                          │
        └──────────────┬───────────────────────┴──────────┬───────────────┘
                       ▼                                  ▼
                 internal/coreapp（产品解析）        internal/plugins/discovery（第三方发现/指纹）
                       │                                  │
                       └──────────────┬───────────────────┘
                                      ▼
                                 app（组合根）
                     ┌────────────────┼─────────────────┐
                     ▼                ▼                 ▼
            host.Load(每插件)    toolkit.Catalog    plugins.HookChain
                     │                │                 │
      ┌──────────────┼──────────────┐ │                 │
      ▼              ▼              ▼ ▼                 ▼
  moejs Runtime   caps（fs/http/   staging → 原子提交到 7 类目录
  每插件 1 个      exec/process…）  （工具/provider/提示/动作/路由/钩子/前端）
      │
      ▼
  引擎调用：ports.ScriptHost → Instance.CallTool/CallHook/… → JSON 进 / JSON 出
      │
      ▼
  Gateway（HTTP/SSR/SSE）── 前端渲染桥（仅官方前端 + 第三方 blocks）
```

### 2.3 依赖与边界规则

- **引擎不知道 moejs**：`internal/engine` 只依赖 `ports`；mojs 类型只允许出现在 `internal/plugins/host`（`host.go` 的包内别名）。
- **官方插件只经组合根进入基座**：`internal/` 与 `cmd/` 不 import `plugins/official`；`app.LoadOfficial` / `AssembleWithExtras` 是唯一入口。`tools/verify` 的 `boundary` 检查会扫描源码中的官方插件 id 与 import。
- **没有平行 Go 注册 API**：工具/provider/动作/路由/钩子只能从 TS 的 `agent.register*` 注册；Go 侧目录（`toolkit.Catalog`、`provider.Registry` 等）只是这些贡献的消费者。
- **跨边界只有 JSON**：脚本值（`moejs.Value`）不跨 goroutine；入参与返回值都走 JSON 字节，单次结果上限 1MB、路由响应 64KB。
- **单一真源 + 门禁**：能力位/点位/插槽在 `contract.go` 声明，Schema 在 `spec/manifest.schema.json`，文档在 `docs/`；`adag verify` 负责三者对齐。

---

## 3. 契约面

### 3.1 清单（`plugin.json`）

加载路径：`discovery.LoadManifest`（JSON 解析 + `Validate`）→ `m.Validate(目录名)`（第二次校验，额外要求 id 等于目录名）。未知字段不报错（主版本一致时忽略并记录），`apiVersion` 主版本不匹配直接拒绝。

| 字段 | 形态 | 校验/消费者 |
|---|---|---|
| `id` | `^[a-z][a-z0-9-]{1,63}$` | 必须等于目录名；`plugin_meta`、注册表键、`ctx.state` 命名空间 |
| `name` / `version` / `description` | 字符串 | 页面展示；`version` 必须 semver |
| `apiVersion` | 整数 | 必须等于 `APIVersionMajor`（1） |
| `trust` | `builtin` / `verified` / `third-party` | 默认 third-party；`verified` 要求 `sig` 非空（**仅此一项检查**，见 §10） |
| `sig` | 字符串 | 当前不参与任何密码学校验 |
| `backend.entry` | 插件目录内相对路径 | 默认 `index.ts`；禁止绝对路径、反斜杠、盘符、`.`/`..` 段 |
| `backend.capabilities` | 能力白名单 | 已知、不重复、与信任级别相容 |
| `backend.env` | 环境变量名白名单 | 禁止 `ADAG_` 前缀；与 `secrets` 不允许重名 |
| `backend.secrets` | 密钥名 | 只有声明过的名字能经 `ctx.secret` 取值 |
| `backend.roles` | 角色 | v1 只有 `tool-policy` |
| `backend.hooks` | 点位 → 最高动作等级 | 点位已知、动作不超过该点位允许集合、信任级别允许 |
| `backend.pool` | 整数 | v1 只允许 ≤1（>1 装配失败） |
| `backend.initTimeoutMs` | 0..60000 | 入口工厂预算；>5000 仅 builtin/verified 可声明 |
| `frontend.capabilities` | 前端能力白名单 | `backend.read` / `frontend.script` |
| `frontend.client` | `.js` 相对路径 | 需 `frontend.script` 且 builtin/verified |
| `frontend.slots/pages/nav/dashboard/blocks` | 贡献数组 | 插槽名已知、模板存在、block 的 schema/template 扩展名与 profile 校验 |
| `frontend.actions` | binding → 目标动作 | 写动作必须有 `confirm`；`read` 预取需要 `backend.read`；最多 16 条 |
| `frontend.styles/scripts/assets` | 静态资源 | `scripts` 需 `frontend.script` 且 builtin/verified |

### 3.2 能力位与信任矩阵

后端 13 项、前端 2 项；`MinTrust` 决定最低信任级别（`contract.go:278-293`）。

| 能力 | 端 | 最低信任 | 脚本可见面 | 执行点 |
|---|---|---|---|---|
| `fs.read` | 后端 | third-party | `ctx.fs.readText/list/stat/exists`、`node:fs` 读成员 | `fsGuard` + 上游 node fs；读路径**不限宿主目录**（2026-10-31 决策） |
| `fs.write` | 后端 | third-party | `ctx.fs.writeText`、`node:fs` 写成员 | 工作区根 + 已安装插件目录（可写 `~/.adago/plugins/<id>/`），symlink 逐段校验 |
| `net.fetch` | 后端 | third-party | `ctx.fetch(url, init)` | 宿主 `HTTPBridge`：仅 http(s)、重定向 ≤5、响应 ≤16MB、超时/取消贯穿；**无 host 黑白名单** |
| `net.sse` | 后端 | verified | `ctx.http.json/sse` | 同桥；建连 15s / 空闲 90s / 总 30min |
| `exec` | 后端 | verified | `ctx.exec`、`node:child_process` 的 `execFile/execFileSync` | 无 shell；cwd 限工作区；输出 4MB；超时 120s；取消杀进程树；程序级 `ExecPolicy` 由装配层注入 |
| `process` | 后端 | verified | `ctx.process.start/write/read/kill` | 长驻 stdin/stdout；每 Runtime 8 个、单进程 30min |
| `env.admin` | 后端 | builtin | `ctx.env.list/set/remove` | `~/.adago/env.json` + 工作区 `.adago/env.json`；键名校验、0600 原子写 |
| `cron.admin` | 后端 | builtin | `ctx.cron.list/create/update/remove/setEnabled/trigger/runs` | `~/.adago/cron.json`；调度/执行在独立 cron 二进制 |
| `state` | 后端 | third-party | `ctx.state.get/set/delete` | SQLite `plugin_state`；按插件 + 线程命名空间 |
| `ui.ask` | 后端 | verified | `ctx.ui.confirm/select/input/form/notify` | 交互桥；无 UI 时按拒绝（失败关闭） |
| `subagent.invoke` | 后端 | builtin | `ctx.subagent.invoke` | v1.5；v1 不装配 |
| `model.admin` | 后端 | builtin | `ctx.providers.list/select/save/remove/fetchModels` | 目录不含密钥；切换复用 `model.set` 校验 |
| `plugin.admin` | 后端 | builtin | `ctx.pluginAdmin.install/check/remove` | 写全局插件根；宿主校验 id/相对路径/大小，拒绝覆盖 |
| `backend.read` | 前端 | third-party | SSR 预取 `props.data.backend.<id>.<binding>` | Gateway 渲染桥：≤16 个、并发 4、单个 300ms、缓存 8s |
| `frontend.script` | 前端 | verified | `/ext/{id}/static/*`、hydrate | CSP + nosniff；third-party 声明即装配失败 |

### 3.3 钩子点位

15 个点位全部有引擎调用方（`internal/engine/engine.go` 的调用方表 + `ports/hook.go` 常量）；只有 `tool.beforeCall` 是失败关闭点。

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
| `tool.beforeCall` | 参数校验后、执行前 | observe, note, deny, replace/abort（builtin） | **关（拒绝）** |
| `tool.afterCall` | 工具回执生成后 | observe, note | 开 |
| `compaction.before` | 生成摘要前 | observe, note | 开 |
| `compaction.after` | 压缩 entry 写入后 | observe, note | 开 |
| `interaction.requested` | 交互请求发出后 | observe, note | 开 |
| `plugin.loaded` | 插件贡献提交后 | observe, note | 开 |
| `error` | 轮次内可恢复错误 | observe, note | 开 |

执行语义（`hooks.go:145-236`）：

- 按 `priority` 升序、同级按 `pluginId` 稳定排序，保证回放确定性。
- 单处理器默认 200ms、上限 2s；超时/异常在安全点收敛为拒绝，其它点忽略并记 warning。
- 递归深度上限 2（`domain.HookRecursionDepthLimit`）。
- `deny` 只在 `tool.beforeCall` 立即返回；`replace`/`abort` 只接受 builtin；越权动作只记 warning 不生效。
- 处理器 panic 被 `recover` 收敛成错误，不会带崩轮次。

### 3.4 前端插槽与贡献

12 个插槽：`sidebar.top`、`sidebar.bottom`、`topbar.right`、`chat.corner`、`composer.left`、`composer.right`、`composer.footer`、`composer.above`、`thread.tray`、`transcript.after`、`extensions.list`、`dashboard.widgets`。

- `thread.tray` 是**基座拥有外壳**的插槽：托盘与安全操作由基座渲染，插件只能接管卡片内容（`Slot.BaseOwned`）。
- 插槽名是兼容承诺：新增即一次兼容承诺，删改需走主版本。
- 前端贡献还包含 page（`/ext/{id}/{slug}`）、nav、dashboard、block（Markdown `adag-ui` fence）、action binding、styles/scripts/assets。
- 统一插件包可同时声明 backend 与 frontend；两者由不同宿主加载，但共享同一个 id/version。

### 3.5 TS 侧 API

入口：`export default function (agent: AgentAPI, ctx?: AssemblyContext): void | Promise<void>`。

| 注册面 | 说明 | 约束 |
|---|---|---|
| `registerTool(def)` | 模型可调用工具 | 名字 `^[a-z][a-z0-9_]{1,63}$`；`parameters` 必须是合法 JSON Schema；`execute`/`handler` 至少一个可调用成员 |
| `registerProvider(def)` | 模型协议适配器 | 仅 builtin/verified；必须声明模型；注册后由 `provider.Registry.Bind` 绑定实例 |
| `registerPrompt(def)` | 系统提示片段 | 内容非空；按 priority 排序 |
| `registerAction(def)` | 界面动作 | 只给界面，不进模型上下文；写动作必须有 `confirm` |
| `route(method, path, handler)` | 插件 HTTP API | 只允许 GET/POST；`sse: true` 仅 GET；path 以 `/` 开头、禁止 `..` 与查询串 |
| `on(point, fn, opts)` | 钩子 | 点位必须已知；动作等级必须已被清单声明 |

`ctx` 按能力安装：身份字段（`workspace`/`threadId`/`runId`/`callId`/`now`/`signal`）恒有；`log`/`secret`/`state`/`ui`/`skills`/`providers`/`pluginAdmin`/`env`/`cron`/`fs`/`fetch`/`exec`/`process`/`sse`/`http`/`emit` 按能力与调用类型出现。没声明的属性**不存在**（不是 `undefined`），这是「能力先声明、后授予」的硬要求。

装配期上下文（入口工厂第二参数）是刻意收窄的子集：`workspace`/`settings`/`log`/`signal`/`fs`（只读）/`fetch`/`secret`/`process`，**没有** `state`/`ui`/`exec`/`fs.writeText`。

---

## 4. 生命周期

### 4.1 发现

```go
// internal/plugins/discovery/discovery.go
LoadDirDetailed(<workspace>/.adago/plugins, SourceWorkspace)
LoadDirDetailed(~/.adago/plugins,          SourceGlobal)
MergeByPriority(workspace, global)         // 同 id 工作区优先，记 warning
```

- 目录不存在视为空；散落文件与 `.` 开头的临时目录跳过。
- 单个插件目录失败（缺清单、清单非法、入口缺失、指纹失败）记 `Warning` 并跳过，其它插件不受影响。
- 指纹 = 目录内全部文件按相对路径排序后的 sha256（与 mtime 无关），用于 `plugin_meta` 与变更检测。
- `sideFiles` 把入口之外的 `.js/.mjs/.cjs` 收进 `Bundle`，其余收进 `StaticAssets`；跳过 `node_modules`。
- 与随包插件同 id 的磁盘插件被忽略并记 warning（随包不可被覆盖）。

### 4.2 选择

优先级（`pluginSelection` / `assembleSet`）：

1. 产品声明的插件（`products/*.agent.json` 的 `plugins`）默认启用。
2. 用户启停状态：全局 `~/.adago/plugins.json`，工作区 `.adago/plugins.json` 覆盖全局；出现在文件里即视为用户显式选择。
3. 产品未声明、但用户显式启用的随包插件 → `optIn`，与产品插件同一条装配路径追加。
4. 第三方插件缺省启用，按 id 排序追加；与产品/随包同 id 的忽略。
5. 产品声明但用户禁用的 → `disabled`，本次装配容忍缺席；产品声明但完全缺失 → 装配失败（声明就是承诺）。

旧版按工作区写在 SQLite `plugin_meta` 的用户启停，会在打开会话时一次性迁移成 `<workspace>/.adago/plugins.json`。

### 4.3 装配

每个插件走同一条路径（`host.Load` + `staging` + `Targets.commit`）：

```
清单/身份校验
  → NewCapabilitySet（清单 ∩ 信任级别，算出实际授予集合）
  → NewRuntime（64MB、第三方关 codegen、Importer 挂 ModulePolicy）
  → InstallCapabilities（fs/http/exec/process/state/console/agent:host 模块 + 导入钩子）
  → linkEntry（链接期就拒绝未安装模块，错误点名缺失能力）
  → 执行入口工厂（agent API 只往 staging 写；运行期再 register 只记 warning）
  → Contribution.Validate（重名/命名/能力/钩子等级/插槽/模板）
  → Targets.commit（provider → 工具 → 提示词 → 动作 → 路由 → 钩子 → 前端）
  → frozen = true（运行期只读）
```

要点：

- **两阶段**：工厂执行期是 staging，提交是原子的；任一步失败调用 `rollback` 摘掉已提交部分，其它插件不受影响。
- **缺目录不静默丢弃**：有贡献但装配没有对应目录 → 装配失败（INV-2）。
- **重建槽位一致**：脏运行时重建时，入口工厂必须注册出与已提交贡献相同的登记项集合（`Staging.matchSlots`），否则拒绝并降级，避免已提交 `HandlerRef` 指向错误函数。
- **技能在插件加载前结算**：`settleSkills` 先扫描插件包内 `skills/<name>/SKILL.md` 与工作区技能，装配期只暴露 name/description/绝对 path（正文不进基座）。

### 4.4 调用与预算

| 调用类型 | 预算 | 结果上限 | 说明 |
|---|---|---|---|
| 工具 | 描述符 `timeoutMs`，缺省 120s | 1MB | 预算取 `ToolDef.timeoutMs` |
| 钩子 | 200ms，上限 2s | 1MB | 等待 `ctx.ui` 的时间不计入 |
| 动作 | 5s（可被 `timeoutMs` 覆盖） | 1MB | 只给界面 |
| 路由 | 5s | 64KB | GET/POST JSON |
| SSE 路由 | 空闲 90s / 总 30min | 单帧 64KB | 每帧重置空闲窗口 |
| provider 流 | 建连 15s / 空闲 90s / 总 30min | 增量 JSON | 首增量之后换空闲窗口 |
| 入口工厂 | 5s，可由 `initTimeoutMs` 放宽到 60s | — | 含 Promise 结算 |

调用链：`CallTool`/`CallHook`/… → `prepare`（熔断/脏检查）→ 入队 → owner goroutine → `UpdateCallContext` → `invoke` → `adopt`（thenable 用 then 结算）→ JSON 序列化 → 回执。

- 队列容量 64；`roundTrip` 在队列满时等待调用方 ctx，不主动 fail-fast。
- 取消：调用方 ctx 取消 → 释放宿主长调用 → `loop.Interrupt`（脚本不可 catch，INV-8）→ 宽限期等回执。
- 入参/返回值都是 JSON；`decodeToolResult` 只接受 `text`/`image` 部件，`details` 只给界面。
- Promise 永不结算 → `ErrUnsettled`，按 `plugin_crashed` 处理。

### 4.5 失败与回收

```
ready ──失败──► degraded（lastError，下一次调用前重建）
  │                 │
  │                 └─连续 3 次失败──► disabled（冷却 5min）
  │                                        │
  └──────────── 成功调用 ◄─────────────────┤
                                           ▼
                                   冷却到点：半开探测
                                   ├─成功：ready（计数/冷却清零）
                                   └─失败：disabled，冷却翻倍（≤10min）
```

- 弄脏 Runtime 的四类：超时、超内存、引擎内部错误、脚本 panic；下一次调用前重建。
- 重建失败同样进熔断簿记（冷却翻倍）。
- 取消不算插件的错（不计连续失败）。
- 关闭时 `teardown` 终止 loop、释放能力句柄（fs/worker/子进程）、清 handlers/roots，避免 goroutine 与句柄泄漏。

### 4.6 重载与启停

- `plugin.reload` 分两种粒度：
  - **随包插件**：`Instance.Reload` 原地重建同一代 Runtime（`runtime.go:1242`），不重建整个装配。
  - **第三方插件**：触发 `reloadAssembly`，重新发现、重新装配整个目录，等在途轮次排空（上限 30s）后原子替换，再关闭旧装配（上限 5s）。
- `plugin.setEnabled`：先写 `plugins.json`（全局或工作区覆盖），再整装配重载；重载失败回滚状态文件并保留旧代。
- 禁用唯一策略插件且目录里仍有写/执行工具 → 直接拒绝（失败关闭）。
- 事务边界：状态文件回滚、装配目录替换、旧代关闭三者不在同一把锁里，但顺序保证「失败保留旧代」。

### 4.7 关闭

`Assembly.Close` 逆序关闭每个 `Instance`：`quit` → owner goroutine `shutdown` → `teardown` → `CloseCapabilities`（fs 句柄、在跑子进程、后台任务）→ 清空脚本根。CLI 会话关闭时还会关闭 HTTP 桥与 SQLite。

---

## 5. 安全模型

### 5.1 能力安装，而不是运行期判断

`InstallCapabilities` 由 `caps.go` 在 `init` 里单点赋值（未赋值时 `host.Load` 拒绝加载，而不是跳过能力安装）。一次安装做三件事：

1. **链接期检查**：`moduleLoader.resolve` 先查随包 Bundle，再查宿主模块表；未安装的模块按 `ModulePolicy` 给出带错误码的拒绝。
2. **成员裁剪**：`node:fs`/`node:fs/promises` 按 `fs.read`/`fs.write` 包装成员；未授权写成员**调用即抛** `ERR_ACCESS_DENIED`（不是 `undefined`）。
3. **审计**：每次受控调用经 `Context.Log` 落结构化记录，密钥与 token 经正则脱敏。

### 5.2 模块矩阵三处一致

`ModulePolicy` 是唯一判定器，三处共用同一份答案：

- 静态 `import`：`moduleLoader.hostModule`；
- 动态 `import()` / `import.meta`：`chainImportHooks` 的 `Meta`/`Load` 钩子；
- 矩阵数据：`internal/plugins/host/node/matrix.go` 的 4 个层级（always / loop / capability / never）。

错误码与行为：

- `ERR_ACCESS_DENIED`：能力缺失或信任不够，错误带 `capability` 字段。
- `ERR_MODULE_NOT_FOUND`：明确不安装或宿主尚未实现。
- `ERR_NATIVE_ADDON_UNSUPPORTED`：`.node` 原生扩展一律拒绝。

### 5.3 失败关闭点

| 位置 | 行为 |
|---|---|
| `tool.beforeCall` 钩子超时/异常/无处理器 | 拒绝工具调用 |
| 无 `role=tool-policy` 插件 | `access != read` 的工具一律 denied（`toolkit.execute.go`） |
| 没有可用 UI | `ctx.ui.confirm/select/input` 返回 `null` → 策略插件按拒绝处理 |
| provider 注册但装配无 HTTP 桥 | 装配失败（不静默降级） |
| 策略插件被禁用且仍有写/执行工具 | `plugin.setEnabled` 拒绝 |
| `route` 未知 method/path、未知 action binding | 注册期 TypeError / 运行期 notFound |
| 前端渲染失败 | 单个贡献降级，不拖垮页面 |

### 5.4 已明确放宽的边界

来自 [AGENTS.md](../AGENTS.md) 与 [06-plugin-runtime.md](06-plugin-runtime.md)，不是 bug：

- **出网无 host 黑白名单**：任意 http(s) 域名可访问，边界只剩协议、重定向 5 跳、响应 16MB、超时与取消。后续计划以策略插件形式实现。
- **`ctx.fs` 读成员不限路径**：`readText`/`list`/`stat`/`exists` 可读宿主任意文件，因此 agent 的 `read_file` 可以读插件目录、配置目录、`secrets.toml` 所在目录；写与 `exec` cwd 仍限工作区。
- **审批演进暂缓**：`policy-default` 仍是现有插件依赖，不新增模式/策略 UI。
- **`node:fs` 写成员裁剪方式**：因上游模块体一次性读取成员，退成「调用即抛」而非「访问即抛」。

### 5.5 信任级别的实际强度

`trust` 在清单里声明，`NewCapabilitySet` 直接按它授予能力；**没有来源校验，也没有签名校验**：

- 任何磁盘插件写 `"trust": "builtin"` 即获得 builtin 全部能力（`exec`、`process`、`model.admin`、`plugin.admin`、`env.admin`、`cron.admin`、控制面钩子、前端脚本）。
- `"trust": "verified"` + 任意非空 `sig` 即获得 verified 能力（`exec`、`net.sse`、`ui.ask` 等）。
- 更糟的是 `fs.write` 允许写已安装插件目录（`cap_fs_pluginwrite.go`），一个 third-party 插件可以改自己的 `plugin.json` 或别的插件目录，重启/重载后完成提权。

这条链是当前实现最需要修的安全问题（§10 P0-1）。

### 5.6 审计与脱敏

- 审计只走 `Context.Log`，宿主不自己接文件/slog；字段固定含 `module/method/target/bytes/result/pluginId/runId/callId/threadId`。
- `target` 与字段值经 `secretKeyRe`/`bearerRe` 脱敏；宿主绝对路径在审计里只保留尾段（`baseForLog`），fs 审计记录的是脚本视图路径。
- 审计不入脚本、不入前端 props；`plugin_meta` 只存状态与错误。
- 前端渲染失败审计（`ui.block.*`）只含错误码与归属，不记录 `data` 原文。

### 5.7 资源上限

| 项 | 值 | 执行点 |
|---|---|---|
| Runtime 内存 | 64MB / 插件 | moejs `MemoryLimit` |
| 调用深度 | 2000 | moejs 构造期 |
| 单次结果 | 1MB（路由 64KB） | `replyValue` |
| fs 单次读写 | 16MB | 上游 + 宿主包装 |
| 目录列举 | 10k 条 / 路径 4096 字节 | 上游 + `Limits` |
| `ctx.fetch` 响应 | 16MB（SSE 32MB） | `HTTPBridge` |
| exec 输出 / 超时 | 4MB / 120s | `cap_exec.go` |
| 后台任务 | 每 Runtime 8 个、30min | `cap_exec_background.go` |
| 定时器 | 每 Runtime 256 | `capsState.timers` |
| `plugin.admin` 安装 | ≤32 文件、单文件 256KB、总量 1MB | `plugin_state.go` |
| zip 安装 | ≤20MB、≤500 条目、解压 ≤50MB | `discovery/install.go` |
| 前端渲染 | 插槽 100ms / 页面 300ms / 块 300ms / VNode 256KB | `host/render.go` |

---

## 6. 前端与渲染

### 6.1 两条加载路径

| 来源 | 可贡献面 | 加载器 |
|---|---|---|
| 随包官方插件 | 页面、插槽、nav、dashboard、动作、样式、脚本、blocks、islands | `plugins/official/dir.go` + `gateway/frontend/bridge.go` |
| 全局/工作区第三方插件 | **只有 `frontend.blocks`**（其余前端字段被剥掉） | `gateway/frontend/load.go` |

第三方插件要出现在插件页/可启停，必须声明 `backend`；纯 `blocks` 包按「安装即启用」处理。

### 6.2 渲染管线

```
TSX 源 → esbuild transform（jsx 工厂）→ moejs 一次性 Runtime
      → 执行模板默认导出 → VNode JSON → Go VNode 校验/白名单
      → HTML（唯一 template.HTML 出口）→ 插槽/页面/块
```

- 渲染 Runtime **不装能力**（无 fs/fetch/child_process/stdio），只有 `ssr:components` 虚拟模块。
- 渲染必须同步：返回 Promise 视为错误。
- VNode 白名单：标签、组件、属性、input type 都有固定集合；`on*`、`style`、原始 HTML、`javascript:`/`data:text/html` 一律拒绝。
- 编译缓存按源内容 sha256，最多 128 条，超出整体清空；每次渲染仍新建 Runtime。
- 岛屿（`hydrate: true`）：服务端输出 `data-ada-*` 标记，浏览器用插件 `frontend.client` 接管；`frontend.client` 仅 builtin/verified，第三方不能声明。

### 6.3 数据与动作

- 只读动作可在 SSR 预取：最多 16 个、并发 4、单个 300ms、结果缓存 8s；结果放 `props.data.backend[<前端插件 id>][<binding>]`。
- 写动作必须用户点击 + CSRF + `confirm` 文案；结果不回填页面。
- 块 props 不含 CSRF；块只做纯计算，不能写库/出网/拿密钥。
- 渲染失败只降级当前贡献/当前块，其余 Markdown/页面不受影响。

---

## 7. Provider 与模型扩展

```
registerProvider(def)
  → staging → Targets.commit → provider.Registry.AddProvider（只登记）
  → 实例加载完成后 Registry.Bind(pluginID, inst)
  → ports.ModelClient.Stream:
      resolve（配置优先，其次静态登记）
      → 组装 providerRequest（含 baseUrl/apiKey/proxy/headers）
      → CallProviderStream（脚本）
      → ctx.emit(Delta) → 校验 kind → 转发引擎
```

- 只有 builtin/verified 能注册 provider；且必须声明 `net.sse` 能力。
- 供应商配置（`providers.json`）优先于插件静态登记；配置里 `protocol` 找不到适配器时返回 `core_unavailable`，不回退猜测。
- 建连窗口的语义：首个增量之前的错误可被重试；一旦交付过内容，重试会造成重复文本，所以不再重试。
- `apiKey` 会进 provider 脚本（适配器需要它），但 `model.admin` 的目录读取不暴露密钥，只给 `hasApiKey` 与尾号。
- 推理档位（`thinkingLevels`）在桥里按配置夹取，非法值忽略而不是透传。

---

## 8. 扩展点与改动成本

| 想加什么 | 必须改哪里 | 兼容影响 |
|---|---|---|
| 新工具 | 插件 `registerTool` + manifest `backend.capabilities`（如需 IO） | 不改基座；工具名全局唯一 |
| 新 provider | 新插件目录 + `registerProvider` + `net.sse` + builtin/verified | 不改基座；协议由插件声明 |
| 新钩子处理器 | 插件 `on(point)` + manifest `backend.hooks` 声明点位与等级 | 不改基座；同点位多插件按 priority |
| 新能力位 | `contract.go`（常量、白名单、MinTrust、`installSites`）+ 安装实现 + Schema + 文档 + verify | 需要主/次版本评审 |
| 新插槽 | `contract.go` 的 `slots` + 前端宿主位置 + Schema + 文档 + verify | **兼容承诺**；第三方可能依赖 |
| 新前端 block | 插件 `frontend.blocks` + schema + tsx | 不改基座；第三方可用 |
| 新官方插件 | `plugins/official/<id>` + `build.mjs` 产物 + 产品声明 + 测试 | 需要随包发布 |
| 新渲染组件 | `gateway/frontend/vnode.go` 白名单 + `host/render.go` 的 `ssr:components` | 需要两端同时改（VNode Schema 与 Go 校验器） |

判断标准：**只加插件、不改基座**是最低风险路径；一旦动 `contract.go` 或 VNode 白名单，就要同时动 Schema、文档与 `adag verify`，否则门禁会失败。

---

## 9. 质量与治理

### 9.1 测试

- 插件包 29 个测试文件、140 个测试函数，覆盖清单校验、能力矩阵、钩子链、staging 提交/回滚、Runtime 预算/取消/熔断、fs/exec/http 边界、渲染、发现与安装。
- 全仓 904 个测试函数；`internal/conformance` 还有清单非法样例、nodecompat 语料（Node 20 双跑）与不变量测试。
- 关键回归用例：`internal/corerpc/plugin_rpc_test.go`（list/reload/setEnabled）、`internal/cli/reload_test.go`（zip 安装 + 热重载）、`internal/gateway/frontend/bridge_*_test.go`（插槽/岛屿/块）。

### 9.2 门禁

`adag verify` 共 14 项，与插件直接相关的 6 项：

| 检查 | 作用 |
|---|---|
| `hooks` | 15 个点位都有引擎调用方，成对点位齐备 |
| `slots` | 12 个插槽都有宿主位置与空贡献回退，基座所有权一致 |
| `capabilities` | 每个能力位都有安装消费者与信任约束，无孤儿 |
| `manifest` | 清单 Schema 与 Go 结构逐字一致，每字段有消费者 |
| `nodecompat` | 模块矩阵、能力位、错误码、偏差表自洽 |
| `boundary` | 基座不 import official、源码无官方插件 id、无平行 Go 注册 API |

另：`adag doctor` 会调用 `plugins.CheckCapabilitySites()`；`go test ./...`、`go vet ./...`、staticcheck 是提交门禁。

### 9.3 可观测性

- `plugins.Summary`（ready/degraded/disabled/failed）、`Instance.Warnings()`、`HookChain.Warnings()`。
- `plugin.check` 单插件报告：来源、清单、启停决策、装配状态、入口预加载结果、文件清单。
- 审计事件：`plugin.installed` / `plugin.removed` / `plugin.reload` / `plugin.changed` / `plugin.state.migrated`；能力调用 `host.capability.<module>.<method>`。
- 日志字段限 32 个键，密钥脱敏；面向脚本的错误文本不带宿主绝对路径（只保留尾段）。诊断报告（`plugin.check`、安装结果）会给出本机路径，只应出现在受信界面或日志，不要回传进模型上下文。

---

## 10. 问题清单与建议

按「影响 × 可修复性」排序。P0/P1 建议在下一次发布前处理或至少在文档中显式承认。

### P0-1 信任级别自声明、无验签、无来源校验

**证据**

- `internal/plugins/manifest.go:157-166`：只检查 `trust` 是否已知；`verified` 只要求 `sig` 非空。
- 全仓没有 Ed25519/任何签名校验代码（搜索 `ed25519`/`Verify` 无实现命中）。
- `internal/plugins/discovery/discovery.go:146-195` 与 `app/app.go:236-251`：磁盘插件按 manifest 的 trust 原样装配。
- `internal/plugins/host/host.go:101-152`：`NewCapabilitySet` 直接按 trust 授予能力。
- `internal/plugins/host/cap_fs_pluginwrite.go`：`fs.write` 可写 `~/.adago/plugins/<id>/`，包括 `plugin.json`。

**影响**：本地投放一份带 `trust: "builtin"` 的插件即可获得 `exec`、`plugin.admin`、`env.admin`、控制面钩子等；third-party 还能通过 `fs.write` 修改自身/其它插件清单完成提权。

**建议**

1. 在 `discovery`/`app` 引入「来源 → 信任上限」：磁盘来源最高 `third-party`；`builtin` 只接受随包/官方目录；`verified` 必须经配置公钥验签，验签失败降级为 third-party 并记 warning。
2. `CapabilitySet` 增加 `Source`/`Verified` 字段，让执行点能区分「声明」与「认证」。
3. `fs.write` 对 `plugin.json`/`dist/` 等可执行文件加写保护，或要求插件 id 级审批。
4. 若短期不做验签，至少在 UI/日志中把 verified 显示为「未验证的声明」，并从 `verified` 能力集里摘掉 `exec`/`ui.ask`。

### P1-1 安装与体检会执行插件代码，`check_plugin` 是 read 工具

**证据**

- `internal/cli/plugin_state.go:326-329`：`installPluginGlobally` 在写入后调用 `preflightPluginAssembly`，失败才回滚。
- `internal/cli/plugin_state.go:381-387`：预检走 `Assembler.Assemble`，即 `host.Load`，会执行入口工厂。
- `internal/cli/plugin_state.go:497-561`：`checkPlugin` 对已启用插件调用同一预检。
- `plugins/official/plugin-helper/index.ts:458-473`：`check_plugin` 声明 `access: "read"`。

**影响**：一个只读工具可以触发任意已安装插件的入口工厂（包括声明了 `exec`/`net.fetch` 的插件），绕过写/执行类工具的审批语义；安装路径则是「写文件即执行」。

**建议**

1. 预检改为「不执行用户代码」的静态检查（清单/入口存在/语法编译/模块白名单），把真实装配留给显式重载。
2. 若必须执行，把它建模为 `access: "exec"` 的动作并走审批；或让预检在无能力（空 CapabilitySet）的 Runtime 中运行。
3. zip 安装与 `plugin.admin` 安装的预检行为对齐（当前 zip 不做装配预检）。

### P1-2 文档与实现漂移

| 文档说法 | 实现现状 |
|---|---|
| 第三方插件「安装/首次加载时编译 TS」（05 §6.1、06 §6.4） | `discovery` 直接读入口文件；`host` 用 moejs 编译 ESM，不做 TS 编译。入口必须已是可执行 ESM JS（SPEC 已写明） |
| `verified` 的 Ed25519 签名覆盖清单 + 文件哈希（06 §6.9） | 只检查 `sig` 非空，无验签 |
| 编译缓存 `~/.adago/cache/plugins/<id>/<fingerprint>/`（06 §6.9） | 不存在；只有渲染器的内存编译缓存 |
| 逐插件双代热重载、在途调用走旧代（06 §6.5、SPEC §7） | 随包插件是原地重建（同代）；第三方是整装配重建；双代只发生在装配级 |
| `adag plugin install ./dir` 或 `.zip`（06 §6.9） | CLI 只有 `--zip`；目录安装只能手工放置 |
| 新安装统一写全局 `~/.adago/plugins`（05 §6.1） | `adag plugin install --zip` 写 `<workspace>/.adago/plugins`；`plugin.admin` 才写全局 |
| 06 §6.4 的 quickjs 伪代码 | 实际是 moejs；栈/调用深度选项不受支持（D-7） |

**建议**：以「代码为准」更新 05/06/SPEC，或补齐实现；不要保留两种说法。`adag verify docs` 只校验链接与核心数字，无法发现语义漂移，建议在关键段落加「实现状态」标注。

### P1-3 第三方前端能力与文档泛化描述不一致

`05 §6.1` 把 slots/pages 描述为插件通用能力；实际第三方只加载 `frontend.blocks`（`gateway/frontend/load.go:68-73`）。SPEC §8 已写明边界，但 05 未同步，插件作者容易误判。

**建议**：在 05 §6.1/§6.2 明确「页面/插槽/脚本仅随包官方插件」，第三方前端能力 = blocks；或在实现上放开受信任第三方（需要先解决 P0-1）。

### P1-4 失败归因依赖错误文本

`cli/session.go:2417-2458` 用 `strings.Contains(err.Error(), "插件 "+id)` 判断哪个第三方插件该被隔离。插件 id 互为前缀、错误文案本地化或错误来源变化都会误隔离/漏隔离。

**建议**：让装配/宿主错误携带结构化 `pluginId`（例如 `type PluginError struct { ID string; Err error }`），`blameThirdParty` 改读类型而不是文本。

### P2-1 出网无 host 黑白名单（已知暂缓）

按 AGENTS 决策，`net.fetch` 可访问任意 http(s) 域名；第三方插件可把工作区数据外发，也可探测内网。当前边界只有协议、重定向、大小与超时。

**建议**：按计划以策略插件实现（网络策略点位或独立策略插件），不在宿主桥里写死；在此之前，UI 与文档不要把 `net.fetch` 描述成「受控出网」。

### P2-2 读路径不限（已知决策）

`ctx.fs` 读成员可读宿主任意文件，agent 的 `read_file` 因此能读插件目录、配置目录、密钥文件所在目录。密钥**值**仍只经 `secret` broker，但磁盘上的明文密钥是否可读取决于 OS 权限。

**建议**：保持现状（已决策），但在插件页/安全文档中明确「读不构成隔离」；需要收紧时走策略/插件层。

### P2-3 环境变量白名单可声明敏感名

`backend.env` 只禁 `ADAG_` 前缀；插件可以声明 `OPENAI_API_KEY` 之类的名字，读取宿主进程环境里的密钥。`NewCapabilitySet` 在注入的 `HostEnv` 找不到时回退 `os.LookupEnv`（`host/host.go:121-133`）。

**建议**：对 `*_KEY`/`*_TOKEN`/`*_SECRET`/`*PASSWORD*` 等名字要求显式配置放行，或默认拒绝敏感名；至少在文档中说明 `backend.env` 不是安全边界。

### P2-4 重载粒度与成本

- 第三方插件任何变更都触发整装配重建：重新执行所有插件入口工厂、重新注册全部贡献、重建 provider 绑定；装配期有网络/子进程副作用的插件会被重复执行（MCP `tools/list`、stdio server）。
- 随包插件单插件重载是原地重建，不重建其它插件；但失败会进入熔断簿记。

**建议**：中期实现真正的逐插件双代（新实例构建完成 → 原子替换该插件贡献 → 旧实例排空后关闭）；短期在 UI 标明「重载 = 全部插件重建」。

### P2-5 发现阶段无大小上限，重复全量读盘

`sideFiles` 与 `FingerprintDir` 各遍历一次插件目录、把所有文件读进内存；zip/`plugin.admin` 的限额不覆盖手工放置的目录。大插件包会放大内存与启动时间。

**建议**：发现阶段加单文件/总量上限与 `stat` 预检；指纹与内容收集合并为一次遍历；大文件不进 `StaticAssets`。

### P2-6 队列背压没有默认期限

`Instance.roundTrip` 在队列满时等待调用方 ctx；产品 `turnTimeoutMs` 为 0 时没有轮次墙钟，极端情况下调用会长期排队。

**建议**：给队列等待一个显式预算（例如 `QueueWait`），超时返回 `plugin_timeout` 而不是无限等。

### P2-7 钩子缺少点位级预算与并发约束

`descriptor.hookTimeoutOf` 恒为 false，点位级超时没有契约；同一插件的钩子串行进入自己的 Runtime，不同插件并行由引擎顺序驱动。没有每插件钩子调用速率限制。

**建议**：若将来需要 `backend.hooks` 支持点位级 timeout，在清单与 `HookHandler` 同步加字段；否则在文档里明确「只有全局 200ms/2s」。

### P2-8 staging 重建要求登记项完全一致

重建时入口工厂必须注册出与已提交贡献相同的登记项；依赖网络/配置条件注册的插件在重建后会失败并触发熔断。这是「运行期只读」的严格实现，但对合法插件不友好。

**建议**：明确契约（入口工厂必须确定性注册）；或在重建时允许少注册（多出来的拒绝、少的保留旧句柄），把差异记 warning。

### P2-9 每个插件一条 goroutine + 一个 Runtime 的成本

15 个插件即 15 条常驻 goroutine 与 15 个 Runtime；64MB 是单插件上限而非预分配，但峰值叠加仍可能到 GB 级。没有全局插件内存/CPU 配额。

**建议**：增加全局预算与诊断指标（每插件 RSS/调用耗时），在 `adag doctor`/性能文档中给出上限模型。

### P3 其他

- `domain.Trust` 与 `plugins.Trust` 两套同义类型，靠 `DomainTrust()` 转换；`domain.Access` 与 `plugins.Access` 同样重复。
- `host.Renderer` 每次渲染新建 Runtime，只有编译缓存；高频页面渲染有固定成本（已有 bench）。
- `virtualModules` 进程级缓存永不清理（模块名有限，可接受）。
- `plugin.check` 的报告包含宿主绝对路径 `Dir`，只应出现在受信界面/日志，不要进模型上下文。
- `scripts`/`hydrate` 的校验在清单层，但第三方前端整体不装配，配置项对第三方实际无意义。

---

## 11. 建议路线

**短期（下一次发布前）**

1. 修 P0-1 的最小闭环：磁盘来源强制 third-party；`builtin` 只认官方目录；`verified` 没有验签就降级并记 warning。
2. 把 `check_plugin` 的预检改成静态检查，或改 `access` 为 `exec` 并走审批。
3. 更新 05/06/SPEC 的漂移段落，标注「实现状态」。
4. 修 P1-4 的结构化错误归因（成本低、收益直接）。

**中期**

5. 实现 Ed25519 验签（公钥配置 + 清单/文件哈希覆盖），把 `verified` 变成真实信任级。
6. 逐插件双代重载；provider/钩子注册的原子替换。
7. 网络策略插件化（host allowlist 的既定路线），先做策略点位与配置格式。
8. 发现阶段限额与一次遍历；全局插件资源配额与指标。

**长期**

9. 第三方前端能力（页面/插槽/脚本）在 verified 基础上放开，或明确永久只支持 blocks。
10. 插件市场/在线安装（当前明确非目标，不要提前做）。

---

## 12. 附录

### 12.1 关键文件索引

| 关注点 | 文件 |
|---|---|
| 清单结构与校验 | `internal/plugins/manifest.go` |
| 能力位/钩子/插槽/信任 | `internal/plugins/contract.go` |
| 能力消费点表 | `internal/plugins/sites.go` |
| 钩子链实现 | `internal/plugins/hooks.go` |
| 宿主契约与预算 | `internal/plugins/host/host.go` |
| Runtime 生命周期/回收 | `internal/plugins/host/runtime.go` |
| 模块加载与链接期拒绝 | `internal/plugins/host/load.go`、`module_policy.go` |
| 能力安装 | `internal/plugins/host/caps.go` |
| fs/exec/http 边界 | `internal/plugins/host/cap_fs.go`、`cap_exec.go`、`httpbridge.go` |
| 贡献 staging/提交 | `internal/plugins/host/staging.go`、`contrib.go` |
| agent API 与 ctx | `internal/plugins/host/agentapi.go` |
| TSX 渲染 | `internal/plugins/host/render.go` |
| 发现/安装/指纹 | `internal/plugins/discovery/discovery.go`、`install.go` |
| 组合根装配 | `app/app.go`、`app/registries.go` |
| 会话启停/热重载 | `internal/cli/session.go`、`plugin_state.go` |
| provider 桥 | `internal/plugins/provider/registry.go` |
| 前端渲染桥 | `internal/gateway/frontend/bridge.go`、`load.go`、`vnode.go` |
| 门禁 | `tools/verify/verify.go`、`checks.go`、`boundary.go` |

### 12.2 最小插件骨架

```
my-plugin/
  plugin.json
  index.ts          # 实际是可执行 ESM JavaScript
```

```json
{
  "id": "my-plugin",
  "name": "示例",
  "version": "0.1.0",
  "apiVersion": 1,
  "trust": "third-party",
  "backend": {
    "entry": "index.ts",
    "capabilities": ["fs.read"]
  }
}
```

```ts
export default function (agent: any) {
  agent.registerTool({
    name: "my_tool",
    description: "读取工作区文件并返回长度",
    parameters: {
      type: "object",
      additionalProperties: false,
      properties: { path: { type: "string" } },
      required: ["path"],
    },
    access: "read",
    async execute(args: any, ctx: any) {
      const text = await ctx.fs.readText(args.path);
      return { content: [{ type: "text", text: `长度 ${text.length}` }] };
    },
  });
}
```

安装：`plugin.admin`（`create_plugin`）或手工放到 `~/.adago/plugins/my-plugin/`；重载后 `plugin.setEnabled` 启用。

### 12.3 术语

| 术语 | 含义 |
|---|---|
| Contribution | 插件在装配期登记的全部贡献（工具/provider/提示/动作/路由/钩子/前端） |
| Staging | 装配期收集贡献的临时容器；校验通过后原子提交 |
| CapabilitySet | 清单能力与信任级别求交后的「实际授予集合」 |
| ModulePolicy | 模块白名单判定器；链接期、求值期、动态 import 三处共用 |
| Host bridge | 宿主执行的受控 IO（HTTP/fs/exec/state/ui/secret/env/cron） |
| Fail-closed | 超时/异常/无 UI 时按拒绝处理（唯一安全点是 `tool.beforeCall`） |
| Dirty runtime | 超时/超内存/内部错误/panic 后需重建的 Runtime |
| Half-open probe | 熔断冷却到点后放行一次试探调用，成功恢复、失败翻倍冷却 |
| Island | 服务端 SSR + 浏览器 hydrate 的插件前端贡献（仅 builtin/verified 的 client） |

---

**相邻文档**：[插件模型与后端 API](05-plugins.md) · [插件运行时与治理](06-plugin-runtime.md) · [Node 兼容子集](12-node-compat.md) · [插件 API SPEC](../plugins/official/plugin-helper/SPEC.md) · [设计索引](design.md)
