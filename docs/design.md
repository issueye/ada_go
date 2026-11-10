# ada-go 设计文档

> 状态：设计基线（2026-11-09 重整）
> 版本：0.2
> 范围：以插件为核心的 AI Agent；Gateway + Agent Core；Go + Gin + moejs；SSR 前端与前端插件化  
> 工作名：`ada-go`（二进制 `adag`，模块路径 `github.com/adag/ada-go`）

这是一份**索引文档**。设计按主题收敛为 16 份正文（`00`–`15`）+ 本索引；原 `14/15/19` 合并为 [14-ui-runtime.md](14-ui-runtime.md)，原 `16/18` 合并为 [15-extensions.md](15-extensions.md)。计划、评估、交付说明不再单独成文，统一并入 [11-structure-roadmap.md](11-structure-roadmap.md) 与对应设计章节。

ada-go 是本地优先、单二进制、插件优先的 Agent 系统：Gateway 只负责 HTTP/SSR/SSE，Core 每个工作区一个进程跑唯一的 `RunTurn`；工具、provider、人格、策略、页面、定时任务、环境变量与用量展示全部由插件贡献。**基座零业务语义、零产品兜底**；随包发布、builtin 信任的是**系统插件**，不是基座。

核心数字：**2 个进程角色、1 条 RPC 契约、33 个 RPC 方法、14 个钩子点、12 个前端插槽、1 份协议源文件**。

## 文档地图

| 文档 | 对应章节 | 内容 |
|---|---|---|
| [00-overview.md](00-overview.md) | §0–1 | 摘要、目标、设计原则、成功标准、基座边界、系统插件层、内核 seam、空基座验收 |
| [01-architecture.md](01-architecture.md) | §2–5.2 | 参考取舍、进程拓扑、依赖方向、对话时序、Gateway、Core 分层、端口、桌面壳 |
| [02-agent-loop.md](02-agent-loop.md) | §5.3–5.4 | `RunTurn` 状态机、终止条件、不变量、工具系统、系统工具插件 |
| [03-model-and-approval.md](03-model-and-approval.md) | §5.5–5.6 | 模型适配层、provider TS 契约、审批（已移除）、交互桥与取消 |
| [04-sessions-storage.md](04-sessions-storage.md) | §5.7–5.8 | 会话 DAG、压缩与 `Compactor` seam、SQLite 并发、子智力体 |
| [05-plugins.md](05-plugins.md) | §6.1–6.3 | 插件包形态、清单、力力与信任、后端 `AgentAPI`、`global.config` / `events.read` |
| [06-plugin-runtime.md](06-plugin-runtime.md) | §6.4–6.10 | moejs 沙箱、装配期结算、钩子、技力、系统插件、信任与热重载、内核 seam |
| [07-ssr-frontend.md](07-ssr-frontend.md) | §7 | SSR、插槽、VNode、SSE、前端插件化（第三方同面）、动作桥、渐进增强 |
| [08-protocol.md](08-protocol.md) | §8 | 传输、RPC 方法、快照与补丁、工具 DTO、错误码、产品声明 |
| [09-security-operations.md](09-security-operations.md) | §9–10 | 威胁模型、路径/命令/Web 安全、第三方前端风险、资源限制、可观测、发布与恢复 |
| [10-testing-performance.md](10-testing-performance.md) | §11–12 | 分层测试、删除测试、门禁、关键用例、性力目标与实测基线 |
| [11-structure-roadmap.md](11-structure-roadmap.md) | §13–17 | 目录结构、依赖、里程碑与交付状态、风险、开放问题、工程决策、系统插件迁移路线、文档治理 |
| [12-node-compat.md](12-node-compat.md) | §6.10–6.14 | moejs 引擎力力、Node 兼容原生库、力力映射、安全边界与兼容性验证 |
| [13-appendix.md](13-appendix.md) | 附录 A–D | 插件示例、配置示例、与参考实现的差异、术语 |
| [14-ui-runtime.md](14-ui-runtime.md) | §14、§15、§19 | ada-render 运行时、ada-ui 组件层、UED 规范与主题/可访问性/治理 |
| [15-extensions.md](15-extensions.md) | §16、§18 | Markdown 动态块（`adag-ui`）与 MCP 插件化接入 |

## 阅读路径

- **评审设计**：00 → 01 → 02 → 03 → 05 → 06 → 12 → 08 → 09。
- **实现内核**：01 → 02 → 03 → 04 → 05 → 06 → 12 → 08 → 10。
- **写插件**：05 → 06 → 12（Node 兼容子集）→ 07 → 08 → 13（附录 A 是最小完整示例）。
- **做 UI**：14（运行时、组件、UED）→ 07（SSR 与插槽）→ 15 §16（Markdown 动态块）。
- **接 MCP**：05 → 06 → 15 §18。
- **运维与排障**：09 → 04（SQLite 并发与备份）→ 08（错误码）→ 10（门禁）。

## 文档约定

- **编号稳定**：章节号不随合并变化；代码注释里的 `02 §5.3`、`06 §6.6`、`19 §19.5` 等引用继续有效。合并后的文档保留原子章节号（14/15/19、16/18）。
- **单一事实源**：协议以 `spec/proto.json` 为准；能力位/钩子/插槽以 `internal/common/pluginapi/contract.go` 为准；清单以 `spec/manifest.schema.json` 为准。文档解释设计意图，冲突时先改源再改文档。
- **边界优先**：任何“基座要不要做”的争论回到 [00 §1.5](00-overview.md#15-基座边界scope-boundary)；默认答案是“不做，交给系统插件”。
- **不新增独立计划/评估文档**：开发计划、评估、交付说明、性力实测统一并入 11、10 或对应章节；一次性的分析不进入 `docs/`。
- **过期即缺陷**：`adag verify` 检查文档链接、文档地图与核心数字；文档未登记或成为孤岛即失败。
