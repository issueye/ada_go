# 启动前决策与契约缺口

> 上级索引：[ada-go 设计文档](design.md)  
> 输入：[开发计划](development-plan.md) §4.1 M0.6、§9 决策清单；设计文档 08/11/12/13  
> 版本：0.1 · 2026-10-28  
> 性质：工程决策记录。不新增设计决策，只把散落在设计文档里的开放项**钉成可执行取值**，并登记 M0 期间发现的契约缺口。  
> 冲突口径：范围冲突先改对应设计文档，再改 [spec/](../spec) 与 [开发计划](development-plan.md)，最后回写本文件。

## 1. 已钉定的决策

### D-1 模块路径与二进制名（M0.1）

| 项 | 取值 | 依据/备注 |
|---|---|---|
| Go 模块路径 | `github.com/adag/ada-go` | 导入路径已写进全部源文件；改名要重扫全仓，视为单向门 |
| 二进制名 | `adag` | 设计 3.1；子命令 `gateway/core/plugin/doctor/verify/export/backup/version` |
| 工作名 | `ada-go`，产品 id `coding` / `skeleton` | 发布前若改名按 [11 §15.2](11-structure-roadmap.md#152-开放问题) 处理，属 M5 |
| `go` 指令 | 模块 `go 1.26.0`，`go.work` 同步 `1.26.0` | 由 `gin v1.12.0` 的最低要求决定；本地工具链 `go1.27.1` 实测通过 |
| 依赖基线 | 见 [11 §13.2](11-structure-roadmap.md#132-依赖)，由 `internal/builddeps/deps.go` 的空导入钉版本 | M1+ 各包被真实引用后把导入搬走，M5 删除该文件 |

### D-2 moejs 联调与上游策略（M0.6）

- `go.work` 只含相对路径 `.` 与 `../moejs`；提交 `go.work`，日常联调走 workspace 模式。
- 本地 `../moejs` 检出正好是 tag **`v0.1.0-alpha.11`**（`git describe` 一致、工作树干净），因此 `go.mod` 里 `require github.com/MoeForge/moejs v0.1.0-alpha.11` 与 workspace 覆盖指向同一份代码。
- 发布、安装包与漏洞扫描一律 `GOWORK=off`；依赖整理用 `GOWORK=off go mod tidy` + `go mod verify`。
- E-1/E-4/E-5（[12 §6.11.1](12-node-compat.md#6111-现状与待补)）优先提上游；上游未合并时允许固定 fork，但必须在 `go.mod` 显式 `require`/`replace`，**禁止仓库内隐式补丁**。未补齐的能力保持「不可用/链接期失败」，不做近似兼容。

实测偏差（需回改设计文档）：moejs 已无 `stdlib` 包，Node 兼容层实际在 `github.com/MoeForge/moejs/node`，Web API 在 `.../webapi`；[06 §6.4](06-plugin-runtime.md#64-moejs-运行时与沙箱) 与 [12 §6.12.1](12-node-compat.md#6121-安装) 的伪代码写作 `quickjs.Option` / `stdlib.Install` / `stdlib.Config`，与上游 API 名称不符。M1-D 开工前先把这两处伪代码按真实 API 更正。

### D-3 a_da UI 基线 commit（M2 开始前）

- 基线：`E:\codes\rust_projects\a_da` @ `ae2f8ffe1679ce25c490c7af0a788d6575669fa0`（2026-10-28，`refactor(plugins)!: 插件事件 API 收口，移除旧 hooks 兼容`）。
- 已确认存在的复用资产：`design/tokens.css`、`crates/agent-web/static/{app.css,components.css,layout.css,widgets.css,shell.css,pages.css,ada-select.js,ada-modal.js,components.js,chat.js,theme.js,icons? 见下}`。
- 注意：a_da 的 `static/` 里另有 `htmx.min.js`、`alpine.min.js`、`app.js`。设计 7.1 选型是 **HTMX + 原生 EventSource，不用 SPA/客户端框架**，因此 M2 导入时只取 `htmx.min.js`（并按需保留 `ada-select.js`/`ada-modal.js`/`components.js`/`chat.js`/`theme.js`），**不引入 Alpine**；`icons.svg` 不在 a_da `static/` 下，图标基线取 `ui/icons.rs` 的 path 集（7.7.1 已如此规定）。
- 落地方式不变：副本文件头记录来源与 SHA-256，`adag verify ui` 比对；该检查在 `web/static/tokens.css` 出现前显式 SKIP（不伪装成通过），资产一导入就自动生效。

### D-4 错误码与诊断码的真源分层

同一份错误码出现在四处，职责不同、由 `adag verify generated` 三方对齐：

| 位置 | 角色 | 生成/手写 |
|---|---|---|
| `spec/proto.json` `errorCodes` | 唯一手写源（码、名、area、retryable） | 手写 |
| `internal/proto/methods_gen.go` | 常量与元数据表 | `tools/genproto` 生成，禁手改 |
| `internal/domain/errors.go` | 引擎与适配器使用的强类型码（领域层不能 import 传输层） | 手写，verify 断言与源逐条一致 |
| `internal/diag/codes.go` | 诊断码 `ADG-<AREA>-<NNN>` 与「下一步」文案 | 手写，verify 断言覆盖全部码且 area 一致 |

M1 期间若领域层不再需要独立常量，可让 `domain` 改为引用 `diag`/`proto` 的别名并删掉重复表；届时先更新 [08 §8.5](08-protocol.md#85-错误与诊断码) 再改代码。

### D-5 官方插件 id 允许出现的位置

- 允许：`products/*.agent.json`（装配选择）、`tools/verify/boundary.go` 的边界清单、插件包自身目录、测试夹具。
- 禁止：`internal/**`、`app/**`、`cmd/**` 的非测试 Go 源码（含注释）。
- 由 `adag verify boundary` 扫描阻断；同一检查还禁止 `internal/` import `official/`、禁止任何 `func RegisterTool|RegisterProvider|RegisterAction|RegisterSlot|...` 形态的平行 Go 注册入口。

## 2. M0 期间发现的契约缺口

处理原则：**先改设计文档，再改 `spec/`，最后改实现**；未处理前按 Schema/现状执行，不静默发明。

| # | 缺口 | 当前处置 | 必须解决的里程碑 |
|---|---|---|---|
| G-1 | [08 §8.5](08-protocol.md#85-错误与诊断码) 没有「线程/技能/模型/交互不存在」类错误码 | 借用 `invalid_params`，字段 description 已标注 | M1-G（CLI 回放）前补码或确认借用 |
| G-2 | 握手/版本不符没有专用错误码；`spawn_failed`/`handshake_timeout` 只在 [01 §4.2](01-architecture.md#42-工作区注册表与监督) 作为 Gateway 侧结构化错误出现 | `version` 不匹配走 `internal_error` + `ADG-CORE-*` | M2（监督与握手）落地前定名 |
| G-3 | `Descriptor.Repeatable`（[02 §5.4](02-agent-loop.md#54-工具系统)）在 [08 §8.4](08-protocol.md#84-工具描述符与回执) 的描述符 JSON 里没有 | DTO 严格按 8.4 十字段；`repeatable` 暂不出现在 wire 上 | M1-C：要么补进 8.4，要么改为插件侧元数据 |
| G-4 | `terminatesTurn`（描述符）与 `ToolReturn.terminate`（返回值）两套命名 | 两者都保留：前者是目录声明，后者是单次提示 | M1-A 在 02/08 里统一措辞 |
| G-5 | `budget_exceeded` 文案含「成本上限」，但产品声明 `policies` 无成本字段 | v1 只支持 `turnTimeoutMs`/`maxTurnTokens` | M5 决定是否补成本上限字段 |
| G-6 | [08 §8.1](08-protocol.md#81-传输) 「所有事件可重放」与快照/ready 的补发语义冲突 | `evt.ready`/`evt.state.snapshot` 标 `replayable:false`，断序改走快照或 `thread.get` | M2-SSE 前把这条写进 8.3 |
| G-7 | `thread.branch` 语义未定（新线程还是移动 head） | 按 [04 §5.7](04-sessions-storage.md#57-会话分支与压缩)：移动 head，返回 Thread | M1-B 前补进 8.2 表格说明 |
| G-8 | `model.set` 作用层级未定 | Schema 允许可选 `threadId`，结果回 `scope` | M2 前定 workspace/thread 双级语义 |
| G-9 | [07 §7.5](07-ssr-frontend.md#75-前端插件化) 前端清单支持 `api`，但 [05 §6.2](05-plugins.md#62-清单) 无该字段 | Schema 未收；插件自有端点走后端 `route()` | M4 前补文档或删该词 |
| G-10 | `pool` 字段的挂载位置文档未指定 | 钉在 `backend.pool`，v1 `>1` 即装配失败 | M3 若开放多实例再改 |
| G-11 | `apiVersion` 主/次编码未定义 | 整数即主版本（当前 1），次版本仅宿主内部 | M3 插件开发体验前定名 |
| G-12 | 交互超时/无 UI 与 `expires_at` 的格式口径混用（RFC3339 vs Unix ms） | RPC 层统一 RFC3339Nano UTC；DB 层仍是 INTEGER | M1-A 前写进 8.2/04 |
| G-13 | 「同一线程最多一个待处理交互」时第二个请求的行为未定 | 暂按请求失败（不排队），因为 03 §5.6 只承诺「最多一个」 | M1-A 前补文档 |
| G-14 | [07 §7.6](07-ssr-frontend.md#76-动作桥与路由) 要求动态路由「路径参数原样传入」，`spec/proto.json` 的 `RouteRequest` 只有 `method/path/query/headers/body` | 插件从 `Path` 自行解析；`RouteRequest` 不含 `pathParams` | M3/M4 前补 `pathParams`（先改 07/08） |
| G-15 | 产品能力位（`capabilities.images`）在 12 端口里没有落点 | 引擎侧暂用 `Params.ImagesAllowed` 表达双门 | M1-E 前定：能力位由 coreapp 注入引擎配置 |
| G-16 | `evt.interaction.requested` 与 `error` 点位的载荷形状文档未给全 | 事件按 Schema；钩子事件以 JSON 快照透传 | M3（15 钩子真实调用方）前补 |

## 3. M0 退出状态

| 门禁 | 状态 |
|---|---|
| workspace 与 `GOWORK=off` 两种模式构建 | 通过（`sh scripts/ci-gate.sh` 两条腿全绿） |
| gofmt / `go vet` / `staticcheck` | 通过（`staticcheck.conf` 无点前缀，实测带点文件被忽略） |
| `go generate ./... && git diff --exit-code` | 生成物与源一致；**diff 比对需要首个提交**，本地无 HEAD 时该步显式 SKIP |
| `go test -race ./...` | 通过（`internal/conformance` 14 个测试、INV-1…INV-12 子用例；修掉一处并行子测试共写 `strings.Builder` 的真实竞争） |
| `adag verify`（11 项 + 1 项 M2 前 SKIP） | 通过；反向验证：在仓库副本注入 5 类漂移后 `manifest/vnode/products` 如期红掉 |
| `adag verify boundary` / `nodecompat` | 通过（基座源码零官方插件 id、无平行 Go 注册入口、domain 只用标准库、ports 只依赖 domain） |
| `adag doctor --fake` | 通过（M0 范围；SQLite/锁/端口检查显式待接入） |
| 空基座桩启动（`products/skeleton.agent.json`，`plugins: []`） | 通过：34 方法 dispatch、0 工具、0 供应商、带空态提示 |
| 未写任何具体工具、provider、审批规则与业务页面 | 是（`official/` 尚不存在，基座无兜底） |

集成期修掉的两处实现缺陷（都属于 M0 交付物自身，不是设计缺口）：

- `plugins.validateBackend` 原用 `strings.ContainsAny(entry, "..\\")`，第二参是字符集合 `{'.', '\'}`，导致 `index.ts` 这类合法入口被判越界；改为按路径段校验（禁绝对路径、反斜杠、盘符、`.`/`..` 段）。
- `conformance.TestPortsAreImplemented` 的并行子测试共写一个 `strings.Builder`，`-race` 报真实竞争；改为在父测试顺序拼装报告。

**相关文档**：[设计索引](design.md) · [开发计划](development-plan.md) · [目录、里程碑与风险](11-structure-roadmap.md)
