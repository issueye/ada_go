# ada-go 设计文档

> 状态：设计草案（待评审）  
> 版本：0.1  
> 日期：2026-10-28  
> 范围：以插件为核心的 AI Agent；Gateway + Agent Core；Go + Gin + moejs；SSR 前端与前端插件化  
> 工作名：`ada-go`（二进制 `adag`，模块路径暂定 `github.com/<org>/ada-go`，可随时改名）

这是一份**索引文档**。原单文件设计已按主题拆分；各章保留原编号（`0`–`15` 与附录 A–D），因此跨文档的“见 5.5”“见 6.3”等引用仍然成立。

ada-go 是本地优先、单二进制、插件优先的 Agent 系统：Gateway 只负责 HTTP/SSR/SSE，Core 每个工作区一个进程跑唯一的 `RunTurn`；工具、provider、人格、策略、页面全部由插件贡献，官方插件也走同一 TS/moejs 契约。

核心数字：**2 个进程角色、1 条 RPC 契约、34 个 RPC 方法、15 个钩子点、12 个前端插槽、1 份协议源文件**。

## 文档地图

| 文档 | 对应章节 | 内容 |
|---|---|---|
| [00-overview.md](00-overview.md) | §0–1 | 摘要、产品目标、非目标、设计原则、成功标准、基座边界与空基座验收 |
| [01-architecture.md](01-architecture.md) | §2–5.2 | 参考项目取舍、进程拓扑、依赖方向、对话时序、Gateway、Core 分层与端口 |
| [02-agent-loop.md](02-agent-loop.md) | §5.3–5.4 | `RunTurn` 状态机、终止条件、不变量、工具系统与官方工具 |
| [03-model-and-approval.md](03-model-and-approval.md) | §5.5–5.6 | 模型适配层、provider TS 契约、审批策略插件、交互桥与取消 |
| [04-sessions-storage.md](04-sessions-storage.md) | §5.7–5.8 | 会话 DAG、压缩、SQLite 模式与并发/多进程访问、子智能体 |
| [05-plugins.md](05-plugins.md) | §6.1–6.3 | 插件包形态、`plugin.json`、能力与信任、后端 `AgentAPI` |
| [06-plugin-runtime.md](06-plugin-runtime.md) | §6.4–6.9 | moejs 沙箱、调用预算、装配期结算、钩子、技能、官方插件、签名与热重载 |
| [07-ssr-frontend.md](07-ssr-frontend.md) | §7 | SSR 技术选型、a_da UI/设计元语直接复用、页面布局、组件与 VNode、SSE、前端插件化、动作桥、渐进增强 |
| [08-protocol.md](08-protocol.md) | §8 | 传输、34 个 RPC 方法、快照与补丁、工具 DTO、错误码、产品声明 |
| [09-security-operations.md](09-security-operations.md) | §9–10 | 威胁模型、路径/命令/Web 安全、提示注入边界、资源限制、可观测性与运维 |
| [10-testing-performance.md](10-testing-performance.md) | §11–12 | 分层测试、门禁、关键用例、性能目标与优化顺序 |
| [11-structure-roadmap.md](11-structure-roadmap.md) | §13–15 | 目录结构、Go workspace、依赖约束、里程碑、风险与开放问题 |
| [12-node-compat.md](12-node-compat.md) | §6.10–6.14 | moejs 引擎能力、Node 兼容原生库、能力映射、安全边界与兼容性验证 |
| [13-appendix.md](13-appendix.md) | 附录 A–D | 插件示例、配置示例、与参考实现的差异、术语 |
| [development-plan.md](development-plan.md) | 实施计划 | M0–M5 任务分解、依赖与关键路径、工作量、质量门禁、发布节奏 |
| [decisions.md](decisions.md) | 工程决策 | 启动前开放项的钉定取值（模块路径、Go/moejs 基线、a_da commit、真源分层）与契约缺口台账 |

## 阅读路径

- **评审设计**：00 → 01 → 02 → 03 → 05 → 06 → 12 → 08 → 09。
- **制定与跟踪开发**：[development-plan.md](development-plan.md) → 11（里程碑/风险）→ 10（测试与性能）。
- **实现内核**：01 → 02 → 03 → 04 → 05 → 06 → 12 → 08 → 10。
- **写插件**：05 → 06 → 12（Node 兼容子集）→ 07 → 08 → 13（附录 A 是最小完整示例）。
- **运维与排障**：09 → 04（SQLite 并发与备份）→ 08（错误码）→ 10（门禁）。

## 文档约定

- **编号稳定**：章节号不随拆分变化；新增内容追加编号，不重排既有编号。
- **单一事实源**：协议以 `spec/proto.json` 为准，本组文档解释设计意图；两者冲突时先改协议再改文档。
- **边界优先**：任何“基座要不要做”的争论回到 [§1.5](00-overview.md#15-基座边界scope-boundary)；官方插件与第三方走同一 TS 契约。
- **不重复正文**：跨主题只放摘要与链接，细节留在对应文档；修改设计时同步更新受影响文档与本索引。
- **过期即缺陷**：`adag verify` 检查文档链接与方法/钩子/插槽数量；里程碑退出标准包含文档同步。
