# AGENTS.md — 面向 AI 代理的仓库工作约定

本文件给在这个仓库里工作的 AI 代理（以及新加入的人类协作者）速查：
当前哪些能力是**有意简化/暂缓**的，动代码前先读这一节，不要把暂缓项当成 bug
修回来，也不要在相关路径上重新加回限制。

## 已移除：审批策略与 moejs 安全面（2026-11-10 决策）

以下能力**已经从基座删除**；恢复它们之前先与本文件和负责人确认。
不要在相关路径上重新加回限制，也不要按旧设计文档（`docs/02`、`docs/03`、`docs/06`
的审批章节）复活它们——那些章节已在 2026-11-10 改写为「已移除」。

### 1. 审批策略（policy-default）已整体移除

- 插件 `plugins/official/policy-default/` 已删除；`products/*.agent.json` 与
  `desktop/products/headless.agent.json` 都不再声明它。
- 钩子点位 `tool.beforeCall` 已删除（点位从 15 个降为 14 个；`tool.afterCall` 保留，
  唯一声明在 `internal/common/pluginapi/contract.go`），`deny` 动作等级也已删除：
  可返回的钩子动作只剩 `observe` / `note` / `replace` / `abort`。
- 基座不再有「没有策略插件就拒绝写/执行工具」的失败关闭：
  `coreapp.Runtime.Check()` 只校验产品声明，`toolkit.Executor` 不再有 `HasPolicy`，
  `engine.Deps` 不再有 `HasPolicy`，CLI 不再拦「禁用策略插件」。
- 工具描述符的 `Access` 仍是效果声明，只供 UI 徽标与审计；它不再是审批开关。
- 写/执行工具现在直接执行。**不要**重新引入 `role=tool-policy`、`plugins.backend.roles`
  枚举值、`denied: no policy` 文案或任何「执行前询问」钩子。

### 2. moejs 相关安全设计已移除

- **能力位不再决定「装什么」**：宿主不再按 `backend.capabilities` / `frontend.capabilities`
  裁剪模块、桥或成员，`InstallSite` 里「按能力安装」的口径与 `adag verify capabilities`
  门禁已经删除。**仍然保留的**只有清单层的声明校验：能力名必须是已知名字（封闭白名单）、
  不能重复、不能被低于 `MinTrust` 的信任级别声明（`internal/common/pluginapi/contract.go` 的 `ValidateCapabilityList`）——
  它是清单合法性检查，不是安装开关。写插件时不必为了「拿到 ctx.fs / node:fs」声明能力，
  但声明了不认识的名字仍会装配失败。
- **模块白名单与安装矩阵**：`internal/agent_core/plugins/host/module_policy.go`、
  `internal/agent_core/plugins/host/node/matrix.go` 与链接期 `ERR_ACCESS_DENIED` 判定已删除。
  插件可以 import `node:*` 内置模块，宿主不再做白名单；`internal/agent_core/conformance/nodecompat`
  与 `adag verify nodecompat` 相应收敛。
- **fs / exec / process 桥的边界**：`fsGuard` 的能力位开关、`ExecPolicy` 程序级校验、
  启动前的信任检查、`plugin.admin`/`model.admin` 等 builtin-only 限制都不再由基座执行；
  这些桥只保留功能（工作区挂载、无 shell 的 argv 传递、超时与输出截断）。
- **表现层仍保留的**：`adag.go` 不再有 `trust` 相关的拒绝路径（第三方/verified/builtin
  仍会出现在清单与页面上，但不再决定能否加载）。

### 3. 明确仍然生效的边界（不要顺手删）

- 事件序号、快照、错误码、RPC 方法集合与 12 个前端插槽。
- VNode 白名单、CSP/CSRF、HTTP 桥的协议/重定向/响应大小/超时限制。
- `ctx.fs` 的读成员不限制目标路径（下节），写入与 `exec` 的 cwd 仍限制在工作区根。
- 进程树终止、审计日志与脱敏。

### 4. agent 的读不限制路径（2026-10-31 决策）

- 背景：技能只在系统提示里注入 name/description/**绝对 path**，模型用 `read_file`
  直接读取正文，基座不保存技能正文、不提供技能读取工具或 RPC。
- 边界：`ctx.fs` 的读成员（`readText` / `list` / `stat` / `exists`）**不限制目标路径**，
  因此 agent 的 `read_file` / `list_files` / `search_files` 可以读宿主任意文件；
  写入与 `exec` 的 cwd 仍限制在工作区根。`node:fs` 模块仍按上游挂载点限制
  （agent 工具走 `ctx.fs`，不受影响）。
- 不要按旧「fs 根」规则给读加回限制。

## 其它速查

- 插件安装：全局 `~/.adago/plugins/<id>/`（`plugin.admin` 桥仍在，但不再是能力位）。
- 插件启停：全局 `~/.adago/plugins.json` + 工作区 `.adago/plugins.json` 覆盖。
- 插件平台 API 规范：`plugins/official/plugin-helper/SPEC.md`；
  插件手册技能：`plugins/official/plugin-helper/skills/plugin-manual/SKILL.md`。
- 验证门禁：`go test ./...`、`go vet ./...`、staticcheck、`adag verify`（11 项）。

## 基座边界与文档（2026-11-09 决策）

- **基座零业务语义、零产品兜底**：系统插件（builtin，随产品发布）实现工具、provider、
  人格、cron、env、供应商管理、用量展示；禁用后基座不补位。详细边界见
  `docs/00-overview.md` §1.5。
- **cron 是系统插件，不是基础设施**：基座不读 `cron.json`、不解析 cron 表达式、
  不调度任务、不提供 `/api/cron/fire`；调度由插件用 `ctx.processes` 注册单例进程完成。
- **第三方前端与系统插件同面**：slots/pages/scripts 都可声明，由安装确认、CSP 与审计
  控制风险。
- **内核默认实现 + 可替换 seam**：压缩、存储、模型、工具执行等默认用 Go 实现，
  同时保留受控接口；替换实现必须在装配期注入并通过契约测试。
- **文档收敛**：`docs/` 只保留 16 份设计正文（00–15）+ `design.md` 索引；
  不再有审批策略与能力位章节（2026-11-10 已改写为「已移除」）。
- **internal 目录结构**：`internal/common`（跨端共享的领域/端口/协议/适配器）、
  `internal/gateway`（网关侧进程与 Web）、`internal/agent_core`（Agent Core 内核与插件宿主）。
