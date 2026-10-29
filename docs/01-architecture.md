# 架构、Gateway 与 Core 分层

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§2 参考项目分析、§3 总体架构、§4 Gateway、§5.1–5.2 Core 分层与端口  
> 版本：0.1 · 2026-10-28

## 2. 参考项目分析

### 2.1 pi（TypeScript Agent Harness）

值得借鉴：

- **双层消息模型**：`AgentMessage`（可含 UI/自定义消息）与发给模型的 `Message` 分离，转换只有一个边界。UI 提示永远不会污染模型上下文。
- **统一模型流**：按 `model.api` 注册 provider 适配器，把 OpenAI/Anthropic/Google 的 SSE 差异归一化成同一套增量事件（文本、思考、工具调用参数、结束、用量）。
- **append-only 树形会话**：每条 entry 有 `id/parentId`，分支只移动叶子，不改写历史；压缩也是一条 entry。
- **steering 队列**：用户中途插话在固定边界排空，不靠 abort 打断循环，语义可预测。
- **工具结果的 `content` / `details` 分离**：前者给模型，后者给界面；工具并行采用“顺序预检、并发执行、按源序回填”。
- **审批即扩展**：`tool_call` 钩子可在执行前阻止或改写工具调用，`ctx.ui` 提供通用的 confirm/select/input；内核不内置审批策略，非交互模式由扩展决定失败方向。

必须避免：

- 扩展与主进程同权限，零隔离；同名工具可静默覆盖内置工具。
- 三套重叠的运行时，主会话文件膨胀到数千行。
- 缺少内建的循环检测与资源上限；模型原地打转时只能人工中断；重试靠错误字符串正则。
- 热路径同步等待所有钩子，慢扩展拖住整个循环。
- 审批完全依赖用户自觉：不装 permission 扩展时危险命令直接执行；ada-go 用产品门禁要求“有写/执行工具就必须有策略插件”。

ada-go 的取舍：保留双层消息、统一流、树形会话、steering、content/details 分离；用**进程隔离 + moejs 沙箱 + 显式终止条件 + 循环检测 + 钩子超时**补上短板。

### 2.2 a_da（Rust 微内核 + 网关 + SSR）

值得借鉴：

- **微内核 + 端口**：领域层零 IO、零产品名词，唯一多轮循环；产品只是声明（`agent.spec.json`）。
- **网关进程模型**：一工作区一进程；身份键归一化；空闲回收、LRU 上限、有连接不回收；工作区不存在拒绝拉起。
- **一个进程一个端口 = 整站**：同一端口托管 SSR 页面、SSE、静态资源、`/api`、`/rpc`。
- **SSR + HTMX + 表单 303**：核心动作不依赖前端框架；SSE 只做增量；`hx-swap-oob` 做局部同步。
- **前端 TSX 插件**：插件清单声明插槽/页面/动作/路由；TSX 在服务端执行成 VNode，再交给宿主组件库；没有 hydration。
- **一套成熟的设计系统**：Black Swan `--ph-*` token、`ada-*` 组件样式、DOM 结构、插槽位置与交互规则已经落地；ada-go 直接复用，只换服务端实现（见 [7.7](07-ssr-frontend.md#77-ui-与设计元语直接复用-a_da)）。
- **装配期结算**：插件贡献一次性并入目录；运行期动态注册只收集不生效。
- **前后端动作分离**：动作是“界面触发的后端调用”，不是模型工具；读动作可预取，写动作点击才执行。

必须避免：

- 文档与实现漂移（方法数、钩子数、审批章节都出现过不一致）；ada-go 用生成与门禁把漂移变成构建失败。
- 协议在 JSON、Rust 常量、TS 客户端、dispatch 四处手写；ada-go 只保留一份 JSON 源 + 生成常量，DTO 由契约测试盯住。
- 插件没有内存上限；ada-go 每个运行时都设内存/时间/调用深度上限。
- 过度耦合的 OOB DOM id 协议；ada-go 的 SSE 事件带显式目标与序号，前端只做“替换目标 + 断序重同步”。
- 审批层被整体删除后，危险工具缺少可替换的安全策略；ada-go 采用 pi 式解耦——交互桥在基座，审批规则放在 `policy-default` 插件，并用产品门禁保证“有写/执行工具就必须有策略插件”。

### 2.3 moejs

它是本项目的插件运行时基础，而不是“顺手选个 JS 引擎”：

- 纯 Go，无 cgo，随主程序交叉编译；Windows/macOS/Linux 一致。
- 默认运行时**没有任何 IO**：没有 `require`、`process`、`fetch`、文件系统；装什么由宿主决定。
- 构造期可设内存上限、栈大小、最大调用深度；`EvalContext`/`CallContext` 用 context 中断死循环；中断不可被脚本 `catch`。
- 脚本超内存返回 `ErrMemoryLimit`（不可捕获）；引擎内部 panic 在 Eval 边界转成 `ErrInternal`，不会带崩宿主。
- `SetModule`/`SetModuleLoader` 精确控制模块解析；`Set` 把 Go 函数暴露给脚本，Go 错误自动变成 JS 异常。
- `rt.Go`/`AsyncWork` 支持跨 goroutine 的异步结果；`Compile`/`Program` 可复用编译结果。
- 官方 `stdlib` 是**按能力拆分**的：FS 可限定根目录与只读，Fetch/Run/Serve/Sockets 等都要显式安装。这与“能力先声明”完全同构。
- 面向插件的 Node 兼容层是 **API 形状的子集**，不是 Node 运行时：无 libuv、无原生扩展、无运行时 npm 解析；`fs`/`crypto`/`child_process` 等由 Go 实现并按能力安装。模块矩阵、引擎要求与偏差见 [6.12](12-node-compat.md#612-node-兼容原生库)。

限制与对策：

- Runtime 不是并发安全的，必须一个 goroutine 拥有一个 Runtime。ada-go 用“每插件一条调用队列 + 独立 goroutine”封装。
- 它不是 TypeScript 编译器，也不处理 JSX。ada-go 用 esbuild 的 Go API 做去类型/JSX 转换与打包。
- 内存上限按运行时计，不等于进程 RSS；宿主还要用 `GOMEMLIMIT` 与进程级上限兜底。
- JIT 是实验特性且只在部分平台可用；ada-go 默认关闭，插件脚本以短任务为主，解释器足够。

### 2.4 取舍总表

| 维度 | pi | a_da | ada-go |
|---|---|---|---|
| 进程模型 | 单 Node 进程 | 网关 + 一工作区一节点 | 网关 + 一工作区一 Core，单二进制两角色 |
| 扩展安全 | 无隔离 | Boa + 能力开关，无内存上限 | moejs + 能力安装 + 内存/时间/深度上限 |
| 工具来源 | 内置 + TS 扩展 | 内置 Rust + TS 插件 | 基座零工具；官方与第三方都是 TS 插件，走同一 moejs 引擎与目录契约 |
| 会话存储 | JSONL 树 | 会话文件 | SQLite append-only DAG + 快照投影 |
| 协议 | SDK/内部事件 | 83 个方法，多处手写 | 34 个方法，JSON 单源 + 生成常量 |
| 审批 | 扩展钩子 | 当前实现已移除 | 策略插件（`tool.beforeCall` + `ctx.ui`）+ 通用交互桥；基座无审批规则 |
| 前端 | TUI | Askama + HTMX + TSX（Black Swan 设计系统） | html/template + HTMX + TSX（moejs），直接复用同一套 token、`ada-*` 组件与 DOM |
| 产品装配 | 代码装配 | 声明式 spec | 声明式 spec + 逐字段消费者门禁 |
| 子智能体 | 有 | 有，隔离靠装配 | v1.5，复用同一引擎与装配 |

---

## 3. 总体架构

### 3.1 进程拓扑

```
┌──────────────────────────── 浏览器 / Electron 壳 ────────────────────────────┐
│  HTML + CSS + 少量原生 JS（HTMX / EventSource）                               │
└───────────────────────────────────┬──────────────────────────────────────────┘
                                    │ HTTP / SSE（一个端口）
┌───────────────────────────────────▼──────────────────────────────────────────┐
│ Gateway (adag gateway)                                                       │
│  auth · workspace registry · supervisor · relay · SSR · frontend plugins     │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────┐  ┌────────────────┐  │
│  │ Gin router   │  │ SSE hub      │  │ template/comp  │  │ moejs render │  │
│  │ / /threads   │  │ per thread   │  │ 12 slots       │  │ (TSX → VNode)  │  │
│  └──────────────┘  └──────────────┘  └────────────────┘  └────────────────┘  │
└───────────────────────────────────┬──────────────────────────────────────────┘
                                    │ JSON-RPC 2.0 over WebSocket（回环 + 一次性 token）
              ┌─────────────────────┼─────────────────────┐
              ▼                     ▼                     ▼
┌───────────────────────┐ ┌───────────────────────┐ ┌───────────────────────┐
│ Agent Core · 工作区 A │ │ Agent Core · 工作区 B │ │ Agent Core · 工作区 C │
│ engine.RunTurn        │ │                       │ │                       │
│ provider plugins      │ │                       │ │                       │
│ tool catalog          │ │                       │ │                       │
│ interaction broker    │ │                       │ │                       │
│ session store (SQLite)│ │                       │ │                       │
│ backend plugin host   │ │                       │ │                       │
│  └ moejs runtimes   │ │                       │ │                       │
└───────────────────────┘ └───────────────────────┘ └───────────────────────┘
```

- 单二进制 `adag`，子命令 `gateway`、`core`、`plugin`、`doctor`、`verify`、`export`、`backup`。
- Gateway 是唯一的公开监听者；Core 只监听 `127.0.0.1` 的临时端口，握手时用一次性 token。
- 一个 Gateway 默认绑定一个工作区，也可以注册多个工作区；线程 id 形如 `th_<workspaceHash>_<ulid>`，自带工作区前缀，URL 不需要额外前缀。
- 侧栏线程列表按**当前工作区**展示；跨工作区通过切换器重建，不维护全局线程索引。
- 打包时模板、静态资源、官方插件通过 `go:embed` 进二进制；第三方插件在工作区/用户目录。官方插件仍按插件装配，可禁用。

### 3.2 为什么 Gateway 与 Core 分进程

- **崩溃隔离**：模型流解析、工具执行、第三方脚本都可能出问题；Core 崩溃不应让整站 502。
- **资源边界**：Core 的 SQLite 写入、moejs 内存、命令进程树可以按工作区限制与回收。
- **生命周期匹配**：工作区是天然的隔离单位；同一工作区的多个线程共享工具目录、插件状态、命令环境。
- **未来可远程**：契约是 JSON-RPC，Core 可以跑在容器或远端，Gateway 不需要改。
- **SSR 与 Agent 解耦**：Gateway 可以独立重启、独立扩缩；SSR 不直接触碰模型密钥与工具权限。

不选“每会话一进程”：进程太多，插件初始化与 SQLite 连接成本高，且同一工作区的文件变更需要共享锁。

不选“全在一个进程”：moejs 内存上限只约束脚本堆，不约束 Go 进程；一个失控插件或命令仍可能拖垮站点。Go 的 goroutine 不是安全边界。

### 3.3 依赖方向

```
domain（消息/工具/事件/错误）
   ▲
   ├── engine（RunTurn + 钩子编排 + 工具前策略）
   ├── ports（接口）
   ├── adapters（sqlite / scope / clock / secret）
   ├── toolkit（工具描述符、执行包装、文件变更队列；不含具体工具）
   └── plugins（moejs 宿主 + 贡献结算）
             ▲
             ├── coreapp（通用装配器：插件集合 → 目录/引擎）
             │      ▲
             │      └── app（组合根：产品声明 + 嵌入的 plugins/official/*）
             ├── coreproto（JSON-RPC 服务端）
             └── cmd/adag core

plugins/official/*（官方 TS 插件包：provider、fs、shell、git、plan…；只用 agent:* 虚拟模块）
   ▲
   └── app（唯一嵌入与装配点）

gateway
   ├── registry / supervisor / relay
   ├── auth
   └── web（SSR + 前端插件 + SSE）
             └── cmd/adag gateway
```

规则：

- `domain` 不 import 任何适配器，不出现产品名。
- `engine` 只依赖 `ports` 与 `domain`。
- Gateway 不 import Core 的内部包，只 import `coreproto` 的 DTO 与生成的方法常量。
- 插件运行时只通过 `ports.ScriptHost` 进入引擎；引擎不知道 moejs 的存在。

### 3.4 一次对话的时序

1. 浏览器 `POST /threads/{id}/messages`（无 JS 表单）或 JS `fetch`。
2. Gateway 校验会话 cookie、CSRF、Origin；把请求转成 `turn.start` 发给对应 Core。
3. Core 获取线程写锁，追加用户消息 entry，开始 `RunTurn`。
4. Core 流式调用模型，边收边发事件：`evt.message.delta`、`evt.item.updated`、`evt.run.status`。
5. Gateway 把事件投影进内存中的 `ThreadView`，向该线程的 SSE 客户端广播；结构变化时用 Go 模板渲染 HTML 片段一并推送。
6. 模型产生工具调用：Core 过 `tool.beforeCall` 钩子 → 执行 → 结构化回执 → 作为工具结果回给模型。策略插件可以在钩子里调用 `ctx.ui.confirm` 决定是否放行。
7. 需要用户确认时，`ctx.ui` 经交互桥发 `evt.interaction.requested`；Gateway 渲染输入框上方的通用交互托盘；用户 `POST /interactions/{id}`；Gateway 转 `interaction.respond`；Core 唤醒策略插件，钩子返回 allow/deny。
8. 本轮结束：Core 恰好发一次 `TurnFinished`（`evt.run.status` 的终态），Gateway 重新渲染 transcript 尾部与状态条。
9. 用户关闭页面不影响轮次；Core 继续跑，Gateway 无客户端时仍保留投影，空闲回收由 Registry 负责。

---

## 4. Gateway 设计

### 4.1 职责边界

做：

- HTTP 路由、SSR、SSE、静态资源、前端插件加载与渲染。
- 鉴权、CSRF、Origin/Host 检查、CSP、安全响应头。
- 工作区注册表、Core 监督、空闲回收、崩溃重启、请求路由。
- 把 Core 事件投影成页面需要的视图，缓存并广播。
- 预取前端插件的只读动作，代理写动作与插件路由。
- 记录访问日志、SSE 连接数、Core 重启次数等指标。

不做：

- 不调用模型，不持有模型密钥。
- 不执行工具、不读写工作区文件（前端插件的静态资源除外）。
- 不实现会话真相；只缓存投影，可随时从 Core 重建。
- 不解析插件的后端脚本；后端插件只在 Core 里跑。

### 4.2 工作区注册表与监督

**身份键**：`workspaceKey(path)`：

1. `filepath.Abs`；
2. `filepath.EvalSymlinks`（解析 junction/symlink 后的真实落点）；
3. Windows 下折叠盘符与路径大小写，统一分隔符；
4. `sha256(realPath)[:12]` 作为目录名与实例 id 的一部分。

`E:\x`、`E:/x/`、`e:/X` 必须落到同一个键。工作区目录不存在时拒绝拉起（为空气目录起进程只会白占资源）。

**注册表**：

```go
type Instance struct {
    ID         string        // product::hash
    Product    string
    Workspace  string        // 规范化后的真实路径
    Endpoint   string        // ws://127.0.0.1:port/rpc
    PID        int
    Status     Status        // starting|ready|busy|reaping|failed|stopped
    LastActive time.Time
    Restarts   int
}
```

- `ensure(workspace)` 双检锁：已有 ready 实例直接返回；starting 等待握手；没有则 spawn。
- 线程 id 只由 Core 生成，格式 `th_<workspaceHash>_<ulid>`；Gateway 解析前缀即可路由到实例，未知前缀返回 404。
- spawn 命令：`ADAG_CORE_TOKEN=<random> adag core --workspace <path> --listen 127.0.0.1:0 --parent-pid <pid>`；token 走环境变量，不出现在进程命令行。
- Core 在 stdout 打印一行握手 JSON（endpoint、pid、product、version），Gateway 5 秒内连上并 `system.hello`。
- 启动失败返回结构化错误（`workspace_missing`、`spawn_failed`、`handshake_timeout`），页面显示原因，不白屏。
- 崩溃：同一工作区 5 分钟内最多重启 3 次；超过则熔断，页面提示“内核反复退出”，可在设置里手动重启。
- Core 在打开 `state.db` 前取得工作区独占锁；同一工作区第二个 Core 启动失败并返回 `workspace_locked`（含持有者 pid）。Gateway 不打开 `state.db`，因此不参与数据库锁竞争。
- 空闲回收：默认 15 分钟无请求、无 SSE、无运行中轮次；上限 8 个 Core；每分钟扫描；LRU 驱逐。**有 SSE 连接、运行中、等待交互的实例永不回收**。
- Gateway 退出：先停止接受新请求，通知 Core `system.shutdown`，10 秒后强杀；Core 若发现父进程消失或 30 秒无 Gateway 心跳，自行退出，避免孤儿进程。

**投影缓存**：每个线程一份 `ThreadView`，包含有序 items、运行状态、待处理交互、用量。Gateway 重启后缓存为空，首个请求用 `thread.get` + `thread.entries` 重建；不持久化投影，避免第二份真相。

### 4.3 鉴权

默认配置：

- 只监听 `127.0.0.1`；校验 `Host` 只允许 `localhost`/`127.0.0.1`/`[::1]`，防 DNS rebinding。
- 首次访问签发 HttpOnly、SameSite=Lax 的会话 cookie；POST 需要 CSRF token（表单隐藏字段 + 同值 cookie，双提交）。
- 所有 POST 与 WebSocket 校验 `Origin`；无 Origin 的本地工具请求放行，但必须有 token。

非回环监听：

- 必须显式 `--token`，且必须 `--tls-cert/--tls-key` 或显式 `--allow-plaintext`；两者缺一拒绝启动，而不是打印警告。
- 启动日志打印一次性配对码；`GET/POST /__auth` 接受 token 或配对码，换发会话 cookie。
- Token 可带工作区白名单（`token=wsA|wsB`），越权返回 403。
- 登录失败按 IP 限速；token 比较用常量时间。

Core 的 WebSocket 使用独立的一次性 token（环境变量传入，不出现在命令行），只在本机回环可达；Core 同时校验父进程 PID 与心跳，防止被同机其他进程接管。

### 4.4 路由

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 最近线程或空态 |
| GET | `/threads` | 线程列表 |
| POST | `/threads` | 新建线程（表单 303） |
| GET | `/threads/{id}` | 对话页（SSR） |
| GET | `/threads/{id}/entries` | 历史分页（JSON，滚动加载） |
| POST | `/threads/{id}/messages` | 发消息（表单或 fetch） |
| GET | `/threads/{id}/stream` | SSE 增量流 |
| POST | `/threads/{id}/cancel` | 取消当前轮次 |
| POST | `/interactions/{id}` | 回应用户交互（确认/选择/输入） |
| GET | `/settings` / `POST /settings` | 供应商、模型、主题与插件设置 |
| GET | `/plugins` / `POST /plugins/{id}/reload` | 插件列表与重载 |
| GET | `/d/{slug}` | 概览页（插件 widget） |
| GET | `/ext/{id}/{slug}` | 插件独立页面 |
| GET | `/ext/{id}/static/*` | 插件静态资源（只读、内容类型白名单） |
| POST | `/ext/{id}/action/{name}` | 前端动作 → Core 后端动作 |
| ANY | `/ext/{id}/api/*` | 插件动态接口（`registerHTTPAPI`，GET/POST） |
| GET | `/static/*` | 内嵌静态资源（哈希文件名 + 长缓存） |
| GET/POST | `/__auth` | 换取会话 cookie |
| GET | `/healthz` | 存活与版本（不含敏感信息） |
| GET | `/rpc` | Gateway 自身的管理 WebSocket（可选，v1 仅本机） |

约定：

- 所有修改型表单在无 JS 时 `POST → 303` 回原页面；有 JS 时拦截为 `fetch`，不整页刷新。
- 插件页面与动作都走 Gateway 鉴权；插件拿不到 token，也拿不到 Core 地址。
- 静态资源用 `go:embed`，按内容哈希命名；插件静态资源单独目录、`ETag`、`X-Content-Type-Options: nosniff`。
- 路由表集中声明，SSR 的导航与 `adag doctor` 共用同一份定义。

---

## 5. Agent Core 设计

### 5.1 分层

```
app             组合根：产品声明 + 嵌入的官方插件包 → coreapp；唯一知道 plugins/official/ 的包
coreapp         通用装配器：接收插件集合 → 工具目录 / 引擎 / 存储（不 import plugins/official/）
  ├── coreproto JSON-RPC 服务端、事件发射、状态投影
  ├── engine    RunTurn（唯一多轮循环）、钩子编排、工具前策略、终止条件
  ├── toolkit   工具描述符注册表、能力/scope 校验、文件变更队列、检查点（零具体工具）
  ├── plugins   moejs 宿主、清单校验、贡献结算、运行时池
  ├── adapters  SQLite、Scope、Clock、Secret（零 provider 实现）
  └── domain    消息、Part、工具描述符/回执、事件、错误、失败方向
```

依赖只能向下。`domain` 不知道 Gin，`engine` 不知道 moejs，`coreproto` 不知道模板。`adapters` 只放基础设施；具体 provider 与具体工具都在 `plugins/official/`，和第三方插件一样通过 `agent:*` 虚拟模块注册，由同一个 moejs 宿主执行。

### 5.2 端口

v1 保留 12 个端口，每个都有真实消费者；新增端口必须同时提交消费者与契约测试。

| 端口 | 职责 | 主要实现 |
|---|---|---|
| `Clock` | 时间与超时 | 系统时钟 / 测试时钟 |
| `IDGen` | ULID、调用 id | crypto/rand |
| `EventSink` | 发出领域事件 | coreproto 发射器 |
| `SessionStore` | 线程、entry、游标、分支 | SQLite |
| `ModelClient` | 流式请求统一模型 | 由 provider 插件注册的实现 |
| `ToolCatalog` | 当前可用工具描述符 | 装配期快照（来源：插件） |
| `InteractionBroker` | 通用用户交互：请求、等待、解析 | coreproto 事件 + Gateway 托盘 |
| `HookRunner` | 钩子调用、超时、动作裁决 | plugins（钩子链） |
| `Scope` | 路径解析与越界拒绝 | realpath + 平台规则 |
| `ScriptHost` | 插件脚本生命周期 | moejs 宿主 |
| `StateStore` | 插件 KV、线程级状态 | SQLite |
| `SecretBroker` | 按名字取密钥，日志脱敏 | 环境/文件 |

`CancelToken` 直接用 `context.Context`，不单独造类型；`PromptSource` 合并系统提示词与技能元数据，作为 `engine` 的内部函数；`BlobStore` 等有真实需求再加，不预先抽象。

---

**相邻文档**：[← 摘要与基座边界](00-overview.md) · [索引](design.md) · [Agent 循环与工具 →](02-agent-loop.md)
