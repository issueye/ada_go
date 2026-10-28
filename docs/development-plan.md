# ada-go 开发计划

> 上级索引：[ada-go 设计文档](design.md)  
> 输入：设计文档 00–13；里程碑与 [11 §14](11-structure-roadmap.md#14-里程碑) 保持一致  
> 版本：0.1 · 2026-10-28  
> 性质：实施计划，不新增设计决策；范围冲突时先改设计文档，再改本计划

本计划把现有设计展开成可执行任务、依赖关系、工作量与验收门禁。目标不是一次性写完所有模块，而是按“契约 → 无头内核 → 可用界面 → 完整插件平台 → 发布”逐段形成可运行、可回退的版本。

## 1. 范围与计划口径

### 1.1 v1.0 交付范围

- 单二进制 `adag`：Gateway 与 Core 两种进程角色；每工作区一个 Core。
- 唯一 `RunTurn`：无步数/工具调用次数上限；模型自然结束、错误、用户取消或显式资源上限终止。
- SQLite 会话 DAG：WAL、工作区独占锁、Core 单写者协程、只读 CLI、在线备份与启动迁移。
- 统一 TS/moejs 插件：官方与第三方共用 `AgentAPI`；官方预编译并嵌入，无平行 Go 插件 SDK。
- Node API 形状子集：按能力安装模块，E-1/E-4/E-5 上游缺口闭环，Node 20 双跑语料通过。
- 官方 provider：OpenAI Chat、Anthropic Messages、OpenAI Responses；OpenAI 兼容端点复用 Chat 插件。
- 官方工具与策略：`tools-fs`、`tools-shell`、`tools-git`、`tools-plan`、`tools-ask`、`tools-skill`、`policy-default`、`persona-coding`。
- Gateway：鉴权、工作区监督、JSON-RPC relay、SSE、SSR、前端动作与动态路由。
- SSR 前端：直接复用 a_da 的 token、CSS、DOM、组件与交互；12 个插槽；无 JS 可用；`ui-task-plan` 示例。
- 安全与运维：scope、能力位、交互失败关闭、审计、诊断、`doctor`、`verify`、备份恢复与升级。

### 1.2 不进入 v1.0 阻断路径

- 子智能体：按设计为 v1.5；M5 可做预研，不作为 v1.0 发布条件。
- 第三方 provider 插件、在线插件市场、运行时 npm、原生扩展、MCP、多用户、桌面壳。
- 插件 Runtime 多实例池、JIT、iframe 前端隔离、持久化“以后都允许”规则；均在 v1 后评估。

### 1.3 估算假设

- 工作量单位为**人日**，按 1 名熟悉 Go、TypeScript 与 SQLite 的全职工程师估算；不是承诺日期。
- 区间包含实现、单元/契约测试、文档同步与代码评审，不含上游 PR 的等待时间。
- M2/M3 可并行；两人以上按第 6 节分工压缩日历时间，但不能省略验收门禁。
- M1 完成后必须重新估算 M2–M5；moejs、SQLite 或 UI 复用的实际结果会显著影响后续工作量。

### 1.4 统一定义完成（DoD）

一个任务只有同时满足以下条件才算完成：

1. 实现与测试合并，目标平台的 CI 通过；生成物与源码同步。
2. 对应契约、Schema、示例或文档已更新；不存在“代码先行、文档后补”的漂移。
3. 新能力能被禁用：禁用插件后对应工具、页面、模型或插槽确实消失。
4. 错误、超时、取消、资源上限有测试；失败方向符合设计。
5. 涉及基座边界的改动通过 `adag verify boundary`；涉及 Node/UI 的改动分别通过 `verify nodecompat` / `verify ui`。

## 2. 实施策略与关键路径

### 2.1 四条实施原则

- **契约先行**：先冻结 `proto.json`、manifest/VNode Schema、产品声明与错误码，再写业务实现。
- **Walking skeleton 先行**：尽早打通“fake provider → RunTurn → 一个 TS 工具 → SQLite → CLI 回放”，再扩功能。
- **风险优先**：把 moejs 缺口、provider 长流、SQLite 多进程、TSX 渲染与 a_da UI 移植放在各自阶段最前面做尖刺验证。
- **垂直切片**：每个里程碑都交付可运行版本；不采用“所有内核写完再写所有 UI”的水平 Big Bang。

### 2.2 关键路径

```text
M0 契约冻结
   ↓
M1 无头内核 ───────────────┐
   ↓                       │
M2 Gateway + SSR           M3 插件平台（与 M2 并行）
   ↓                       │
   └───────────┬───────────┘
               ↓
          M4 前端插件
               ↓
          M5 加固与发布
```

M4 同时依赖 M2 的页面宿主与 M3 的完整装配/信任模型。若 M3 晚于 M2 完成，M4 顺延；不得用临时 Go 注册入口绕过依赖。

### 2.3 最早可用切片

1. **M0 末尾**：桩代码可启动，`adag verify` 能发现缺失消费者。
2. **M1 中段**：CLI 可用 fake provider 跑多轮工具调用、取消、压缩与 50 轮长循环。
3. **M1 末尾**：真实 OpenAI Chat 可完成一次编码任务；禁用工具插件后工具表为空。
4. **M2 末尾**：浏览器可发消息、看流式输出、批准/拒绝交互、断线恢复，无 JS 也能完成主流程。
5. **M3 末尾**：第三方目录插件与官方插件走同一入口；热重载不破坏在途调用。
6. **M4 末尾**：任务规划卡与示例 TSX 页面在真实界面生效，UI 与 a_da 基线一致。
7. **M5 末尾**：三平台安装包可升级、备份、恢复，完整 E2E 与性能门禁通过。

## 3. 里程碑与工作量总览

| 里程碑 | 主要交付 | 估算（人日） | 退出口径 |
|---|---|---:|---|
| M0 契约冻结 | 仓库骨架、协议/Schema、生成与 verify、CI、测试夹具骨架 | 8–12 | 桩代码通过 verify；设计中的方法、钩子、插槽、能力都有消费者 |
| M1 无头内核 | RunTurn、SQLite、moejs 宿主、Node 子集、OpenAI Chat、核心工具/策略、CLI | 40–55 | fake provider E2E、50 轮长循环、循环检测、取消、压缩、禁用插件、Node 双跑通过 |
| M2 网关与 SSR | 工作区监督、relay、鉴权、SSE、SSR 外壳、a_da UI、交互托盘、无 JS 降级 | 25–35 | 浏览器主流程 E2E；Gateway/Core 重启恢复；`verify ui` 与响应式验收通过 |
| M3 插件平台 | 安装/发现/信任、完整装配、15 钩子、技能、热重载、Node 矩阵、示例插件 | 30–40 | 沙箱与边界测试全绿；官方/第三方同入口；链接期能力失败；热重载双代正确 |
| M4 前端插件 | TSX 渲染、VNode、组件库、12 插槽、页面/动作/路由、任务规划卡 | 20–30 | golden HTML；示例插件真实生效；无 JS 与失败隔离通过 |
| M5 加固与发布 | 其余 provider、检查点回滚、性能/安全加固、打包、迁移、文档、RC | 25–35 | race + 全量 E2E + 性能 + 升级/备份/恢复 + `doctor` 通过，RC 无阻断问题 |
| **合计** | — | **148–207** | M2/M3 并行时，日历时间由关键路径决定 |

## 4. 分阶段任务分解

### 4.1 M0：契约冻结与工程骨架（8–12 人日）

| 编号 | 任务 | 产出 | 依赖 | 估算 |
|---|---|---|---|---:|
| M0.1 | 初始化仓库与依赖基线 | 单一 `go.mod`/`go.sum`、联合 `.` 与 `../moejs` 的 `go.work`/`go.work.sum`、目录骨架、模块路径、Go/工具链版本、CI 三平台模板 | 无 | 1–2 |
| M0.2 | 固化 Core RPC 契约 | `spec/proto.json`：34 方法、事件、错误码、握手与版本 | M0.1 | 2–3 |
| M0.3 | 固化插件与产品契约 | manifest/VNode Schema、`coding.agent.json`、`skeleton.agent.json` | M0.1 | 1–2 |
| M0.4 | 建立生成与校验骨架 | `genproto`、`adag verify` 桩、文档链接/数量检查、生成物提交规则 | M0.2–0.3 | 2–3 |
| M0.5 | 建立测试基础设施 | fake provider、脚本化模型、事件记录器、临时工作区、CI 门禁脚本 | M0.1 | 1–2 |
| M0.6 | 关闭启动前开放决策 | 模块路径、moejs 上游/fork 与 workspace 联调策略、a_da 固定 commit 策略 | 无 | 1 |

**退出标准**：

- workspace 与 `GOWORK=off` module 两种模式均能解析依赖并构建；`go.work` 只含相对路径，发布依赖仍由 `go.mod`/`go.sum` 锁定。
- `go generate ./... && git diff --exit-code` 通过；协议常量与 dispatch 覆盖率可校验。
- `products/skeleton.agent.json` 的 `plugins: []` 能驱动空基座桩启动。
- 所有清单字段都有消费者或明确删除；文档中的 34 方法、15 钩子、12 插槽与生成结果一致。
- 不写任何具体工具、provider、审批规则或业务页面。

### 4.2 M1：无头内核（40–55 人日）

#### A. 领域、端口与 RunTurn（7–10 人日）

- 实现消息/Part/工具描述符与回执/事件/错误模型；12 个端口都有生产实现与测试替身。
- 实现唯一 `RunTurn`：线程互斥、目录快照、模型循环、工具顺序执行、取消、恰好一个 `TurnFinished`。
- 实现终止原因、3/5 循环检测、可选墙钟/token 上限（默认 0）、上下文水位与压缩入口。
- 建立 INV-1～INV-12 的不变量测试；先跑 fake provider，再接真实 provider。

#### B. SQLite 会话与并发（6–8 人日）

- 实现 schema、事务迁移、`user_version`、append-only entries、runs、interactions、usage、audit、plugin state/meta。
- 实现工作区 OS 独占锁、Core 单写者协程、WAL 参数、短事务、busy 重试与只读查询连接。
- 实现 `export`、`backup`、崩溃后 WAL 恢复、网络盘检测；禁止 Gateway 打开 `state.db`。
- 覆盖第二个 Core 拒绝启动、并发只读导出、备份一致性与锁释放测试。

#### C. moejs 插件宿主（7–9 人日）

- 实现 manifest 最小子集、每插件一个 Runtime/goroutine、模块加载、入口工厂与贡献 staging。
- 实现 `agent:host` 能力桥：fs/exec/state/secret/ui/log；未声明属性不存在。
- 实现 JSON 编解码、Promise `Await`、宿主异步回投、64MB/深度/超时限制、脏 Runtime 重建。
- 工具默认 120s、钩子 200ms；宿主长调用暂停脚本计时；取消不可被脚本捕获。

#### D. Node 兼容子集（6–10 人日）

- **先做尖刺**：E-1 模块白名单、E-4 fs Node 错误码/静态裁剪、E-5 child_process shell 开关；确定上游提交或固定 fork。
- 安装纯计算模块与 Loop/timers；实现受限 fs、`process.env` 白名单、无 shell `execFile`。
- 建立 Node 20/moejs 双跑语料：返回值、错误 `code`、事件顺序与副作用。
- 未补齐能力保持“不可用/链接期失败”，不得用近似实现冒充兼容。

#### E. Provider 桥与 OpenAI Chat（5–7 人日）

- 实现宿主 `ctx.http.sse/json`：allowlist、超时、取消、响应大小与 TLS/代理单点执行。
- 实现 TS `provider-openai-chat`：统一 text/thinking/tool_call/usage/finish 增量、显式终止信号、参数片段组装。
- 实现基座重试包装：只在首个增量前对 429/5xx/网络错误重试。
- 录制 SSE 夹具，覆盖分片、乱序、缺终止、错误帧与长流取消。

#### F. 核心官方工具与策略（5–7 人日）

- 以 TS 实现 `tools-fs`、`tools-shell`、`tools-plan`、`policy-default`；构建期 esbuild 预编译并嵌入。
- 实现 Scope 真实落点校验、写文件变更队列、命令进程树取消、输出截断与回执。
- `policy-default` 实现 readonly/ask/auto/full；无 UI 时写/执行/网络拒绝；无策略插件时基座失败关闭。
- 验证官方插件与第三方共用同一注册路径，无 Go 注册 API。

#### G. CLI 与端到端（3–5 人日）

- 提供 CLI 对话/print、线程列表、事件导出、fake provider 开关与取消。
- 打通多轮工具、拒绝、取消、压缩、50 轮长循环、`finish` 终止、插件禁用与 Runtime 崩溃恢复。
- 建立性能基线：Core 拉起、默认产品装配、provider 桥接、工具调用与事件重放。
- M1 的可运行声明是 `products/headless.agent.json`（provider + fs/shell/plan + policy）；
  `coding` 中尚未交付的插件仍留在声明里，缺少时装配失败而不是静默降级。
  模型/端点用 `--provider/--model/--base-url` 或环境变量指定；`config.toml` 的完整优先级随 M2 接入。

**退出标准**：与 [11 §14 M1](11-structure-roadmap.md#14-里程碑) 一致；另要求 Windows/Unix 的工作区锁与进程树测试都通过。

### 4.3 M2：Gateway、SSR 与可用界面（25–35 人日）

| 工作包 | 内容 | 估算 |
|---|---|---:|
| 工作区监督 | 注册表、路径键归一化、spawn/握手、父进程心跳、重启、LRU/空闲回收 | 4–6 |
| RPC relay | WebSocket 客户端、请求多路复用、事件扇出、断线重连、快照/补丁窗口 | 4–5 |
| Web 安全 | token/cookie/CSRF/Origin、CSP、回环默认、请求体与连接限制 | 3–4 |
| SSE | 每线程 hub、delta/fragment/run/notice、40ms 合并、Last-Event-ID、断序快照 | 3–4 |
| SSR 外壳 | Go handlers/templates、静态资源 embed、a_da tokens/CSS/DOM/组件复用 | 6–8 |
| 对话主流程 | 会话列表、transcript、Composer、工具/思考卡、空状态、错误/离线页 | 3–4 |
| 交互与降级 | 审批/提问/排队托盘、真实表单 303、无 JS 轮询、键盘与焦点 | 3–4 |
| E2E 与验收 | chromedp 主流程、760px 抽屉、Light/Dark、reduced-motion、`verify ui` | 2–3 |

**先行尖刺**：用一个静态对话页验证 a_da 五层 CSS、Web Components 与 Go 模板能原样工作，再移植全部组件；不要先重写一套视觉样式。

**退出标准**：浏览器可发消息、流式更新、批准/拒绝、取消、排队、失败重试；Gateway 重启不丢线程/交互；Core 重启后轮次标记 interrupted；无 JS 主流程可读可操作。

### 4.4 M3：完整插件平台（30–40 人日，可与 M2 并行）

| 工作包 | 内容 | 估算 |
|---|---|---:|
| 安装与发现 | 工作区/全局目录、zip 安装、fingerprint、冲突优先级、启用状态落库 | 4–6 |
| 完整装配 | 两阶段 staging、重名/schema/能力/钩子等级校验、原子提交、运行期只读 | 4–5 |
| 15 个钩子 | 全部真实调用方、动作分级、成对点位、失败方向、递归深度与超时 | 4–5 |
| 技能 | SKILL.md 发现、frontmatter 校验、元数据注入、`tools-skill` 按需读取 | 2–3 |
| 热重载与降级 | 双代 Runtime/贡献、在途调用旧代、连续失败禁用、手动重载 | 4–5 |
| Node 完整矩阵 | 按能力安装/成员裁剪、错误码、资源边界、完整双跑与真实包冒烟 | 6–8 |
| 其余官方插件 | `tools-git`、`tools-ask` 后端部分、示例后端插件；persona 与产品装配校验 | 3–4 |
| 安全与边界 | 信任分级、签名/哈希、`.node` 拒绝、secret 隔离、boundary/conformance | 3–4 |
| 插件开发体验 | `sdk` 类型、最小示例、错误文案、`plugin-api.md` 生成入口 | 2–3 |

**退出标准**：第三方插件崩溃不影响 Core；未声明 Node 模块在链接期失败；官方包与第三方走同一入口；热重载不改变在途调用语义；`verify boundary` 与 Node 双跑全绿。

### 4.5 M4：前端插件与 UI 完整化（20–30 人日）

| 工作包 | 内容 | 估算 |
|---|---|---:|
| TSX 构建 | 官方预编译、第三方 esbuild、`ssr:components` external、产物缓存 | 3–4 |
| Render Runtime | 独立 moejs 池、无 stdlib/codegen、100/300ms 超时、失败重建 | 3–4 |
| VNode 渲染 | 标签/属性/组件白名单、转义、深度/节点/输出限制、错误隔离 | 3–4 |
| 组件与插槽 | Go 组件输出 a_da DOM；12 插槽、顺序、空贡献回退、状态恢复 | 4–5 |
| 页面与动作 | `/ext` 页面、nav/dashboard、只读预取、写动作 CSRF、动态路由/API | 4–5 |
| 示例插件 | `ui-task-plan`、`hello-tsx`、第三方 `task-plan`/`ask-user` | 2–3 |
| UI 验收 | golden HTML、截图基线、无 JS、响应式、对比度、插件失败回退矩阵 | 2–3 |

**退出标准**：示例插件在真实页面生效；插件 TSX 无 hooks/事件/副作用；失败插槽为空且页面其余正常；任务卡与 a_da 形态一致，数据仍来自通用只读动作。

### 4.6 M5：加固、兼容与发布（25–35 人日）

| 工作包 | 内容 | 估算 |
|---|---|---:|
| Provider 补齐 | `provider-anthropic-messages`、`provider-openai-responses` 与录制夹具 | 5–7 |
| 检查点与回滚 | 写工具检查点、change.list/revert、恢复/删除/跳过/失效清单 | 4–5 |
| 稳定性 | race、长流/长轮次、插件崩溃、进程监督、SSE 重连、SQLite 崩溃恢复 | 4–5 |
| 性能 | 达到 §12 指标；事件合并、模板/编译缓存优先；内存与启动门禁 | 3–4 |
| 安全审计 | 威胁模型逐项复核、CSP/CSRF、scope/symlink、secret、供应链与权限降级 | 3–4 |
| 运维与打包 | doctor、export/backup/restore、迁移、单二进制三平台构建、安装说明 | 3–4 |
| 文档与 RC | 用户/插件/运维文档、示例、升级说明、dogfood、RC 修复 | 3–6 |

子智能体可在本阶段做 v1.5 预研，但不得拖延 v1.0；若决定纳入 v1.0，必须先更新 [04 §5.8](04-sessions-storage.md#58-子智能体v15)、产品声明与测试范围。

**退出标准**：三平台全量 E2E/race 通过；备份恢复与升级演练通过；性能达到目标或有明确批准的偏差；RC 无 P0/P1 问题；文档与实现一致。

## 5. 并行分工与依赖管理

### 5.1 多人分工建议

| 角色 | 主责 | 可并行区间 | 交接物 |
|---|---|---|---|
| 内核/存储 | RunTurn、SQLite、压缩、取消、事件模型 | M0–M1，M5 稳定性 | 端口、fake provider、事件夹具、存储契约 |
| 插件/Provider | moejs、Node 兼容、装配、官方 TS 插件、provider | M1–M3，M5 provider | Runtime 宿主、SDK、双跑语料、官方 dist |
| Gateway/UI | 工作区监督、RPC relay、SSE、SSR、a_da UI、前端插件 | M2–M4 | 路由、组件、页面、golden HTML、浏览器 E2E |
| 安全/发布（可兼任） | verify、boundary、审计、性能、打包、升级/备份 | 全程，M5 为主 | CI 门禁、诊断、发布清单 |

### 5.2 硬依赖

- moejs 宿主最小能力未完成前，不写官方工具/provider；避免形成 Go 侧临时实现。
- `proto.json` 未冻结前，不写 Gateway relay 与前端事件处理。
- Scope/工作区锁未完成前，不开放写文件与命令工具。
- `policy-default` 与交互失败关闭未完成前，不宣称写/执行工具可用。
- a_da UI 静态页尖刺未通过前，不大规模移植模板。
- M3 装配/信任未完成前，不开放第三方前端写动作与脚本能力。

### 5.3 上游与第三方资产管理

- moejs 在 `go.mod` 中固定版本；E-1/E-4/E-5 改动优先进上游或固定 fork，禁止仓库内隐式补丁。
- 日常联调按 [11 §13.1](11-structure-roadmap.md#131-go-workspace) 使用 `go.work` 联合 `.` 与 `../moejs`；workspace 只覆盖本地模块解析，不改变发布依赖。未合并修复发布前必须落到已打 tag 的上游版本或 `go.mod` 中显式固定的 fork。
- esbuild、SQLite 驱动、Gin、HTMX 等版本在 M0 锁定，升级单独提 PR 并跑全量契约测试。
- a_da UI 固定 commit；tokens/CSS/JS/DOM 基线带 SHA-256，升级必须同步 `verify ui` 与 golden 截图。
- 所有官方 TS 插件的 `dist/` 由同一构建命令生成并提交/嵌入，禁止手工修改产物。

## 6. 工程节奏与分支规则

- 采用主干开发、小步 PR；一个 PR 只完成一个任务或一个可验收子任务。
- 每个 PR 必须带：实现、测试、文档/Schema 更新、必要的诊断码与验收说明。
- 生成文件与源码同 PR 提交；CI 执行 `go generate` 后要求工作区干净。
- 日常开发默认 Go workspace；修改 moejs 的 PR 同时附独立 module 模式结果。依赖整理用 `GOWORK=off go mod tidy`，发布候选与安装包始终以 `GOWORK=off` 构建。
- 设计变更先改对应设计文档，再改 `spec/` 与本计划；禁止用实现事实反向静默修改契约。
- M0/M1 以 fake provider 为默认 CI；真实 provider 测试使用本地密钥，不进公共 CI 日志。
- 里程碑结束打 tag：`v0.1-headless`、`v0.3-gateway`、`v0.5-plugins`、`v0.8-frontend`、`v1.0-rc.1`、`v1.0.0`。

## 7. 测试、性能与发布门禁

### 7.1 每次合并必跑

CI 对下列命令设置矩阵：默认 workspace 模式（相邻目录检出 moejs）与 `GOWORK=off` 独立 module 模式。两种模式都必须通过；依赖整理与校验额外执行 `GOWORK=off go mod tidy` 后检查 diff，并运行 `GOWORK=off go mod verify`。

```bash
go vet ./...
staticcheck ./...
go test -race ./...
go test ./internal/conformance -run TestInvariants
go test ./internal/conformance/nodecompat -run TestNodeCompat
go generate ./... && git diff --exit-code
adag verify
adag verify nodecompat
adag verify ui
adag verify boundary
adag doctor --fake
```

前端改动另跑 golden HTML 与 chromedp 主流程；存储改动另跑崩溃/WAL/锁测试；provider 改动另跑录制 SSE 夹具与长流取消。发布工作流只运行 `GOWORK=off` 矩阵，禁止用 workspace 产物制作安装包或执行发布漏洞扫描。

### 7.2 里程碑附加门禁

| 里程碑 | 附加门禁 |
|---|---|
| M0 | Schema/协议消费者完整；空基座桩启动；文档数量一致 |
| M1 | 50 轮长循环、3/5 循环检测、恰好一个 TurnFinished、Node 双跑、SQLite 多进程 |
| M2 | 浏览器 E2E、无 JS、760px 抽屉、Light/Dark、Gateway/Core 重启恢复 |
| M3 | 插件故障矩阵、热重载双代、能力链接期失败、官方/第三方同入口 |
| M4 | 12 插槽、页面/动作/路由、插件失败回退、任务卡、golden HTML |
| M5 | 三平台打包、升级、备份/恢复、性能、完整安全审计、RC dogfood |

### 7.3 性能验收

以 [10 §12](10-testing-performance.md#12-性能目标) 为准，重点看 Core 拉起 <500ms、默认插件装配 <1.2s、SSE 增量 p95 <100ms、插槽渲染 p95 <10ms、插件工具调用开销 p95 <5ms、事件重放 10k <150ms。优化顺序固定为事件合并与模板缓存优先，Runtime 池与编译缓存靠测量决定，JIT 不作为默认路径。

## 8. 风险、触发器与回退

| 风险 | 最早验证点 | 触发器 | 应对/回退 |
|---|---|---|---|
| moejs 缺口阻塞 M1 | M0.6 / M1-D 尖刺 | E-1/E-4/E-5 无法按矩阵实现 | 固定 fork 并提交上游；对应能力标记不可用，不做假兼容；重排 M1 任务 |
| 长任务被脚本超时误杀 | M1-C/E | provider/交互/exec 在 5s/200ms 被中断 | 宿主长调用暂停计时；独立期限；取消贯穿；补长流夹具 |
| SQLite 锁/备份在 Windows 不一致 | M1-B | 第二写者未被拒、备份包含旧 WAL、锁不释放 | 单写者协程 + OS 锁；只读/专用备份连接；失败则暂停发布，不开放多写者 |
| a_da UI 复用漂移 | M2 静态页尖刺 | token/class/DOM 与基线不一致 | 固定 commit；`verify ui`；先修基线再继续，不新建视觉语言 |
| TSX 渲染性能不足 | M4 尖刺 | 热渲染 p95 超 10ms/页面超 40ms | 预编译、缓存、模板优化；测量后再考虑 Runtime 池；不引入 hydration |
| Provider 协议差异扩大 | M1-E / M5 | 夹具无法归一化或 EOF 误判完成 | v1 仅官方 provider；补夹具；显式终止信号；第三方走 Chat base_url |
| 基座边界漂移 | 每次 PR | 出现 Go 工具/审批/provider 或官方 id 特判 | `verify boundary` 阻断；移回 TS 插件；空基座测试 |
| 真实 shell 与插件 JS 风险 | M1-F / M3 / M4 | 未询问执行、第三方弹窗或脚本注入 | 默认 ask、无策略失败关闭；ui.ask 受信；插件 JS 默认关闭；审计 |
| 估算偏差 | M1 结束 | M1 超上限 20% 或关键风险未关闭 | 重新估算 M2–M5；先砍非阻断范围，不砍门禁与安全边界 |

## 9. 决策清单与截止时间

| 决策 | 默认建议 | 最晚时间 |
|---|---|---|
| 模块路径与二进制命名 | `adag`；模块路径在仓库初始化时确定 | M0.1 |
| moejs 联调与上游/fork 策略 | `go.work` 联合本地源码；上游优先，固定 fork 兜底；发布切 `GOWORK=off` | M0.6 |
| a_da UI 基线 commit | 固定当前 Black Swan SSR 版本 | M2 开始前 |
| 产品名 | `ada-go` 为工作名，发布前统一 | M5 打包前 |
| 子智能体是否进首发 | 不进，保持 v1.5 | M5 开始前 |
| Runtime 池 | v1 固定每插件 1 个 | M4 性能验收前 |
| 第三方 provider | v1 不开放，兼容端点走官方 Chat | M5 冻结范围时 |
| 插件 JS 隔离 | v1 默认关闭；iframe 评估放 v1.1 | M4 结束前 |
| MCP/市场/多用户 | v2 再决策 | 不进入 v1 计划 |

## 10. 发布节奏与验收清单

### 10.1 版本节奏

| 版本 | 对应里程碑 | 可演示内容 |
|---|---|---|
| v0.1-headless | M1 | CLI 多轮编码任务、真实 OpenAI Chat、工具/策略、SQLite 恢复 |
| v0.3-gateway | M2 | 浏览器对话、流式、审批/提问/排队、无 JS、主题与响应式 |
| v0.5-plugins | M3 | 第三方安装/热重载、完整钩子/技能、Node 能力矩阵 |
| v0.8-frontend | M4 | TSX 页面/插槽/动作、任务规划卡、完整示例插件 |
| v1.0-rc.1 | M5 前段 | 三 provider、回滚、性能、安全、打包候选 |
| v1.0.0 | M5 | 三平台发布、升级/备份/恢复验证、文档齐全 |

### 10.2 v1.0 发布检查

- [ ] 三平台构建、安装、启动、升级、回退均演练通过；发布包全部由 `GOWORK=off` 构建，`go.mod`/`go.sum` 可独立解析全部依赖，`go mod verify` 与发布漏洞扫描通过。
- [ ] workspace 与独立 module CI 均通过；发布不依赖相邻目录中的未提交 moejs 源码。
- [ ] 空基座可启动；禁用任一官方插件后能力消失；不存在 Go 插件注册入口。
- [ ] fake provider、录制 provider、真实 provider 的 E2E 分别通过。
- [ ] 50 轮长循环、取消、循环检测、压缩、插件崩溃、Core/Gateway 重启通过。
- [ ] SQLite 第二写者拒绝、WAL 恢复、只读导出、在线备份与恢复通过。
- [ ] Node 双跑与偏差表同步；未支持模块链接期失败。
- [ ] UI token/class/DOM/golden/截图与 a_da 基线一致；无 JS、键盘、对比度、响应式通过。
- [ ] `doctor` 能诊断配置、锁、网络盘、数据库、插件与 provider；日志无密钥。
- [ ] 性能达到 §12；内存与启动门禁通过。
- [ ] 文档索引、协议、Schema、示例、升级说明与发布包一致。

## 11. 后续版本候选

- **v1.1**：策略插件持久化 allowlist、前端 iframe 隔离评估、更多插件示例与诊断能力。
- **v1.5**：`tools-subagent` 与隔离 profile；复用同一 `RunTurn`，不新增第二套循环。
- **v2**：MCP 适配、插件市场/分发、多用户与权限模型、桌面壳；均需先补设计与威胁模型。

## 12. 计划维护

- 任务编号（M0.1、M1-A…）用于 Issue/PR 关联；里程碑退出时逐项核对，不以“代码已写”代替验收。
- 每周记录：门禁状态、关键路径剩余量、性能基线、未关闭风险、开放决策。
- M1 结束必须重估；任一里程碑超出估算 20% 或关键风险连续两次未关闭时，重新排期并缩减非阻断范围。
- 设计变更先更新对应文档与本计划；本计划不引入新的协议、能力或产品范围。

---

**相关文档**：[设计索引](design.md) · [目录、里程碑与风险](11-structure-roadmap.md) · [测试与性能](10-testing-performance.md)
