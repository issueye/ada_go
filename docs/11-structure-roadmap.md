# 目录、里程碑与风险

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§13 目录结构、§14 里程碑、§15 风险与开放问题  
> 版本：0.1 · 2026-10-28

## 13. 目录结构

```
ada-go/
  go.work                  本地 workspace：. 与 ../moejs
  go.work.sum              workspace 校验和
  go.mod                   ada-go 唯一 Go module；发布依赖版本仍在这里锁定
  go.sum
  cmd/adag/                main：gateway / core / plugin / doctor / verify
  app/                     组合根：产品声明 + 嵌入的 official/ → coreapp
  sdk/                     TS 插件类型：agent.d.ts、ssr.d.ts、node.d.ts（Node 兼容子集）、tsconfig
  internal/
    domain/                消息、Part、工具描述符/回执、事件、错误、失败方向
    engine/                RunTurn、终止条件、工具前钩子编排
    ports/                 12 个端口接口
    adapters/              sqlite、scope、clock、idgen、secret（零 provider 实现）
    toolkit/               描述符注册表、能力/scope 校验、文件变更队列、检查点（零具体工具）
    plugins/               manifest、loader、host(moejs)、contrib、hooks、skills
      host/node/           能力位 → stdlib.Config、env 白名单、Node 错误归一化
    coreproto/             JSON-RPC 服务端、事件发射、投影
    coreapp/               通用装配器：接收插件集合 → 目录/引擎（不 import official/）
    gateway/
      server/              Gin、中间件、路由
      registry/            工作区键、实例表、LRU、回收
      supervisor/          spawn、握手、重启、父进程心跳
      relay/               WS 客户端、请求多路复用、事件扇出
      auth/                token、配对码、cookie、CSRF、Origin
      web/                 SSR handlers、SSE hub、投影缓存、ui/（Go 组件、icons.go）
      frontend/            esbuild、moejs render、VNode、插槽注册表
    proto/                 生成的方法常量、DTO、错误码
    diag/                  诊断码词汇表
    conformance/           端口契约、不变量、golden、nodecompat、ui（token/class/DOM 基线）
  official/                官方 TS 插件包（trust=builtin，可禁用）
    <id>/
      plugin.json
      index.ts             后端入口（构建期编译）
      slot.tsx/page.tsx    前端模板（可选）
      dist/                esbuild 产物，go:embed 进二进制
    provider-openai-chat/ provider-anthropic-messages/ provider-openai-responses/
    tools-fs/ tools-shell/ tools-git/ tools-plan/ tools-ask/ tools-skill/ tools-subagent/
    policy-default/
    persona-coding/ ui-task-plan/ ui-hello-tsx/
  plugins/                 第三方示例插件（与 official/ 同一 TS 契约，装时编译）
    hello-tsx/ task-plan/ ask-user/
  web/
    templates/             layout、chat、workspaces、overview、extensions、plugin、error（DOM 与 a_da 同构）
    static/
      tokens.css           a_da token 原样副本；颜色唯一真源
      app.css              a_da 预编译产物（token + 工具类；不在本仓库跑 Tailwind）
      ui/                  components/layout/widgets/shell/pages.css 原样副本
      ada-select.js ada-modal.js components.js chat.js theme.js icons.svg
  products/
    coding.agent.json
    skeleton.agent.json
  spec/
    proto.json             协议唯一手写源
    manifest.schema.json   插件清单 Schema
    vnode.schema.json      VNode Schema
  docs/
    design.md              文档索引与阅读路径
    00-overview.md         摘要、目标与基座边界
    01-architecture.md     参考取舍、进程拓扑、Gateway、Core 分层
    02-agent-loop.md       RunTurn、终止条件、工具系统
    03-model-and-approval.md 模型适配、审批与取消
    04-sessions-storage.md 会话 DAG、压缩、SQLite 并发、子智能体
    05-plugins.md          插件包、清单、后端 API
    06-plugin-runtime.md   moejs 沙箱、装配、钩子、官方插件、信任
    07-ssr-frontend.md     SSR、插槽、组件、渐进增强
    08-protocol.md         RPC、事件、错误码、产品声明
    09-security-operations.md 安全模型、资源限制、可观测与运维
    10-testing-performance.md 测试门禁与性能目标
    11-structure-roadmap.md 目录、里程碑、风险
    12-node-compat.md      moejs 能力、Node 兼容模块、双跑语料
    13-appendix.md         插件与配置示例、参考差异、术语
    development-plan.md    M0–M5 开发计划、依赖、门禁与发布节奏
    plugin-api.md          从类型定义生成的插件 API 参考（后续）
  tools/
    genproto/              生成 methods_gen.go
    bundle-official/       esbuild 预编译 official/* → dist/，供 go:embed
    verify/                接线/能力/插槽/文档/边界门禁
```

约束：

- `internal/` 下不出现循环依赖；`domain` 不 import 其他内部包（除标准库）。
- `internal/` 不 import `official/` 下的代码或资源；`official/` 只使用 `agent:*` / `ssr:*` 虚拟模块与公开 TS 类型，不 import 基座 Go 包。基座源码不出现任何官方插件 id。
- 基座不提供平行的 Go 插件注册 API；官方与第三方必须走同一个 TS 入口、同一个 moejs 宿主与同一套贡献校验。
- Gateway 与 Core 不共享可变内存；共享的只有 `proto` DTO 与 `diag` 码。
- 所有模板/静态资源通过 `embed.FS`；官方插件的 `dist/` 也在 `embed.FS` 中，开发模式可切换磁盘源码热编译。

### 13.1 Go workspace

ada-go **保持单一 Go module**。`go.work` 只用于把本仓库与相邻的 moejs 源码联合起来做本地开发、上游缺口修复与双跑验证；不把 `internal/` 拆成多个 module，也不改变 Gateway/Core 与插件边界。

```bash
# 在 ada-go/ 下执行；moejs 是相邻目录
go work init . ../moejs
```

生成的 `go.work` 形如（Go 版本以 M0 锁定值为准）：

```go
go <go-version>

use (
    .
    ../moejs
)
```

规则：

- `go.work` 只写**相对路径**，提交 `go.work` 与 `go.work.sum`；不得提交个人机器上的绝对路径。
- 日常开发默认 workspace 模式，moejs 的 E-1/E-4/E-5 修改可以与 ada-go 一起编译、测试；其它依赖仍按 `go.mod` 解析。
- `go.mod` 始终保留真实的 moejs 版本要求。`go.work` 是本地覆盖，不是发布依赖锁，也不替代 `go.mod`/`go.sum`。
- CI 同时跑两种模式：workspace 模式验证联合开发，`GOWORK=off` 的 module 模式验证独立构建。发布、安装包与漏洞扫描一律使用 `GOWORK=off`。
- 上游修复未合并时，本地可以继续用 workspace 联调；发布前必须改为已打 tag 的上游版本，或在 `go.mod` 中显式 `require`/`replace` 到固定 fork 版本。禁止发布依赖某台机器上的未提交源码。
- 执行依赖整理与校验时使用 `GOWORK=off go mod tidy`、`GOWORK=off go mod verify`，避免 workspace 的本地模块掩盖发布依赖问题。

### 13.2 依赖

| 包 | 用途 | 说明 |
|---|---|---|
| `github.com/gin-gonic/gin` | HTTP 路由与中间件 | SSR、SSE、静态资源 |
| `github.com/MoeForge/moejs` | 纯 Go JS 运行时与沙箱 | Go 1.25；无 cgo；插件与 TSX 渲染；宿主只安装已声明能力 |
| `github.com/evanw/esbuild/pkg/api` | TS/TSX 去类型、JSX 转换、打包 | 构建期编译官方插件；第三方安装/加载期编译；TSX 服务端渲染 |
| `modernc.org/sqlite` | SQLite 驱动 | 纯 Go，保持交叉编译 |
| `github.com/coder/websocket` | WebSocket | context 友好，Gateway ↔ Core |
| `github.com/yuin/goldmark` | Markdown 渲染 | 关闭 unsafe |
| `github.com/microcosm-cc/bluemonday` | HTML 清洗 | Markdown 输出兜底 |
| `github.com/BurntSushi/toml` | 配置解析 | |
| `github.com/oklog/ulid/v2` | 可排序 id | 线程、entry、run |
| `github.com/gofrs/flock` | 单实例锁 | 防止同一数据目录起两个 Gateway |
| `golang.org/x/sync` | errgroup / semaphore | 预取、并发工具 |

所有依赖锁定在 `go.mod`/`go.sum`；CI 校验 `go mod verify` 与许可证清单。不引入需要 cgo 的依赖（包括 SQLite 驱动）。

---

## 14. 里程碑

| 阶段 | 交付 | 退出标准 |
|---|---|---|
| M0 契约冻结 | 本设计、`proto.json`、manifest/VNode Schema、工具描述符、事件模型 | `adag verify` 与 `adag verify boundary` 在桩代码上通过；接口评审完成 |
| M1 无头内核 | `RunTurn`、TS 插件宿主、Node 兼容子集（path/util/events/buffer/crypto/fs 受限）、官方 OpenAI Chat provider 插件（TS）、SQLite 会话（WAL、单写者、工作区锁）、`tools-fs`/`tools-shell`/`tools-plan` 官方插件（TS）、`policy-default`（无 UI 时失败关闭）、CLI 交互 | 假 provider E2E：多轮、工具、拒绝、取消、压缩；50 轮长循环自然结束；循环检测与终止原因；禁用工具插件后工具表为空；Node 双跑语料通过 |
| M2 网关与 SSR | 工作区注册/监督/回收、鉴权、SSE、对话页、交互托盘、无 JS 降级、a_da token/组件/DOM 复用 | 浏览器 E2E：发消息、流式、响应交互、重启恢复；`adag verify ui` 与 760px 抽屉验收通过 |
| M3 插件平台 | 第三方插件安装、能力安装、装配期结算、15 个钩子、技能、示例插件、双代热重载、Node 兼容矩阵与能力裁剪 | 沙箱测试全绿；插件崩溃不影响 Core；官方包与第三方走同一入口；未声明能力的 Node 模块链接期失败 |
| M4 前端插件 | esbuild+moejs TSX、12 插槽、页面/动作/路由、组件库、任务规划卡 | golden HTML；示例插件在真实页面生效；插槽位置与 a_da 同名同位 |
| M5 加固与发布 | Anthropic/Responses、检查点回滚、子智能体、打包、审计、文档 | race + 全量 E2E + `adag doctor` + 安装包 |

每个阶段都必须同步更新 `spec/` 与文档；文档过期视为缺陷。

---

## 15. 风险与开放问题

### 15.1 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| TSX 在 moejs 中渲染的稳定性/性能 | 页面慢或白块 | 预编译、Runtime 池、100ms 超时、失败隔离、只读插槽缓存 |
| esbuild 依赖体积/行为变化 | 构建复杂 | 固定在 go.mod；只使用 transform/build 的稳定子集；保留“纯 JS 插件”路径 |
| SQLite 在 Windows/多进程下的锁竞争 | 写入失败、第二个 Core 抢写、备份不一致 | 工作区独占锁保证单写者；WAL + busy_timeout + 单写者协程；CLI 只读；备份走 Online Backup/`VACUUM INTO`；`doctor` 检查网络盘与锁持有者 |
| 官方插件全部走 moejs | provider 流式开销、内存占用、长任务被脚本超时误杀 | 官方预编译并嵌入；每插件独立 Runtime；长调用由宿主异步与独立期限；provider 增量批量回投；真实 SSE 夹具压测；内存与启动门禁 |
| Node 兼容预期过高 | 插件用了未支持的模块或语义，安装后才失败；作者以为“就是 Node” | 模块矩阵与偏差表；链接期失败；`adag verify nodecompat`；Node 20 双跑语料；文档与报错直接指向缺失能力 |
| moejs 的 Node 兼容缺口 | 模块白名单、fs 错误码、shell 开关等未补齐，M1 无法按矩阵交付 | E-1/E-4/E-5 作为阻断项先提交上游；adapter 层做窄包装；未补齐的能力在文档与清单中保持“不可用”，不以近似实现冒充 |
| 轮次不设步数上限 | 模型原地打转、成本失控、长尾轮次占住线程 | 连续重复调用检测（3 次回执、5 次结束）；provider 空闲/总时长与单工具超时；压缩控制上下文；可选墙钟/token 上限；全程可取消且事件实时可见 |
| 插件生态 API 频繁变化 | 第三方插件失效 | `apiVersion` 主版本、弃用周期、契约测试、示例即文档 |
| 同源插件 JS 的安全边界 | XSS/数据外传 | 默认禁用；仅 verified/builtin；CSP；后续 iframe 隔离 |
| 真实 shell 的逃逸 | 工作区外执行 | 默认策略 `ask`（`policy-default`）、readonly 禁用、审计、UI 明示；不宣称强沙箱 |
| 进程监督在 Windows 的边界 | 孤儿进程/端口占用 | 父进程心跳、Job Object、退出握手、`adag doctor` 修复 |
| 协议与实现漂移 | 前端/核心不兼容 | JSON 单源 + 生成 + `adag verify` + 版本握手 |
| 基座边界漂移 | 基座逐渐内置业务、工具或 provider | `adag verify boundary` + 空基座启动测试 + 依赖方向检查 + 评审清单 |
| a_da UI 上游漂移 | token/class/DOM 与 a_da 分叉，出现两套观感 | 固定上游 commit；tokens/CSS/DOM 基线进仓库；`adag verify ui` + golden 截图；差异先改文档 |

### 15.2 开放问题

1. **产品名**：`ada-go` 是工作名；若要与 a_da 明确区分，发布前统一改名。
2. **插件 Runtime 池**：默认 1 是否足够？高并发工具调用是否需要按插件声明 `pool`？建议 v1 固定 1，压测后再开放。
3. **策略 allowlist 持久化**：`policy-default` v1 不提供“以后都允许”；是否在 v1.1 增加按工具/路径的持久化规则，由策略插件自行决定并承担风险，基座不引入。
4. **MCP**：是否作为“外部工具插件”接入，还是保持只支持 TS 插件？建议 v2 决策时再引入适配器，避免 v1 扩大攻击面。
5. **多用户**：v1 单用户；若未来多用户，需要把工作区键、cookie、审计与密钥隔离重新设计。
6. **前端插件缓存**：只读插槽是否按数据哈希缓存？需要先测命中率，避免缓存导致状态滞后。
7. **JIT**：暂不默认启用；若 SSR 插件规模变大，再评估按平台开启。

---

**相邻文档**：[← 测试与性能](10-testing-performance.md) · [索引](design.md) · [moejs 与 Node 兼容 →](12-node-compat.md)
