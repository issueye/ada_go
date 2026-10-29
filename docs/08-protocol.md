# 协议契约

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§8 协议契约  
> 版本：0.1 · 2026-10-28

## 8. 协议契约

### 8.1 传输

- Gateway ↔ Core：JSON-RPC 2.0 over WebSocket，Gateway 作为客户端，Core 作为服务端。
- 请求/响应带递增 id；事件是 JSON-RPC notification（无 id），方法与 `evt.` 前缀。
- 单条消息上限 2MB；文本增量合并后发送；心跳 ping/pong 15s；30s 无 pong 判定断线。
- Core 断线：Gateway 标记实例 `failed`，在途 RPC 返回 `core_unavailable`；自动重连一次，失败则重启 Core。
- 事件序号：线程内 `seq` 单调递增；工作区级事件用独立 `workspaceSeq`。所有事件可重放，至少保留到对应 run 结束。

### 8.2 方法表

方法名、方向、入参/出参 JSON Schema 写在 `spec/proto.json`，是唯一手写源；`go generate` 生成 `internal/proto/methods_gen.go`（常量 + dispatch 覆盖表）。DTO 手写在 `dto.go`，由契约测试逐字段对照 Schema。

| 分组 | 方法 | 说明 |
|---|---|---|
| system | `system.hello` | 握手：版本、产品、能力、插件摘要 |
| system | `system.ping` | 健康检查 |
| system | `system.shutdown` | 优雅退出（刷盘、关运行时） |
| workspace | `workspace.info` | 路径、产品、配置、数据目录 |
| workspace | `workspace.updatePolicy` | 资源上限、循环检测、模型等运行参数（审批等策略设置由插件自己管理） |
| thread | `thread.create` | 创建线程 |
| thread | `thread.list` | 列表（分页、归档过滤） |
| thread | `thread.get` | 快照（items 窗口 + 运行状态） |
| thread | `thread.entries` | 历史分页（游标） |
| thread | `thread.rename` | 重命名 |
| thread | `thread.archive` | 归档/恢复 |
| thread | `thread.branch` | 从某 entry 分支 |
| thread | `thread.delete` | 删除（软删 + 清理任务） |
| turn | `turn.start` | 开始一轮（幂等键防重复提交） |
| turn | `turn.cancel` | 取消当前轮 |
| turn | `turn.steer` | 排队一条中途消息 |
| turn | `turn.get` | 当前轮状态 |
| interaction | `interaction.list` | 待处理交互 |
| interaction | `interaction.respond` | 接受/拒绝/取消（带 `requestId`，幂等） |
| model | `model.list` | 可用模型与能力 |
| model | `model.set` | 切换当前模型 |
| provider | `provider.list` | 供应商配置（不含密钥） |
| tool | `tool.list` | 当前工具目录 |
| plugin | `plugin.list` | 插件、版本、状态、错误 |
| plugin | `plugin.reload` | 重载一个或全部 |
| plugin | `plugin.setEnabled` | 启用/禁用 |
| plugin | `plugin.http` | 调用插件 `route` 注册的动态接口 |
| skill | `skill.list` | 技能元数据 |
| skill | `skill.read` | 读取正文（模型工具也走同一实现） |
| action | `action.invoke` | 前端动作 → 后端动作 |
| change | `change.list` | 检查点与变更 |
| change | `change.revert` | 回滚卡片/文件/全部 |
| usage | `usage.get` | 线程/工作区用量 |
| debug | `debug.state` | 本机调试快照（默认关闭） |

事件：

| 事件 | 载荷 |
|---|---|
| `evt.ready` | Core 就绪、产品、工具数、插件数 |
| `evt.state.snapshot` | 线程全量快照（items 窗口、seq、run、interactions） |
| `evt.state.patch` | `baseSeq` + upsert/remove ops + 指纹 |
| `evt.message.delta` | 消息 id、文本/思考增量、seq |
| `evt.item.updated` | 单条 item 结构变化（工具卡状态等） |
| `evt.run.status` | running / waiting_input / finishing / terminal |
| `evt.interaction.requested` | 交互 id、类型、标题、正文、选项、截止时间 |
| `evt.interaction.resolved` | 交互结果：accepted / denied / cancelled / timeout / unavailable |
| `evt.notice` | 只给人的提示（永不进模型） |
| `evt.usage` | token 与成本增量 |
| `evt.plugin.changed` | 插件状态/贡献变化（工作区级） |
| `evt.error` | 可恢复错误 + 诊断码 |

### 8.3 快照与补丁

- `ItemKey` 稳定：`msg:<id>`、`tool:<callId>`、`interaction:<id>`、`notice:<id>`、`compaction:<id>`。
- item 带 `at`（开始时间）与 `fingerprint`（内容哈希）；投影按 `at` 排序，避免流式导致的顺序抖动。
- 补丁只发变化字段；服务端对单字段超过 32KB 的文本做截断并标 `truncated`，完整内容用 `thread.entries` 或工具详情接口按需取。
- 客户端 `seq` 不连续 → 请求 `thread.get` 全量重同步；不尝试猜测缺失。
- 事件总线保留最近 N 条（默认 4096）用于重连补发；超出则只发快照。

### 8.4 工具描述符与回执

`tool.list` 与模型请求共用同一份描述符：

```json
{
  "name": "read_file",
  "description": "读取工作区内文件的一段行",
  "parameters": {
    "type": "object",
    "properties": {
      "path": { "type": "string" },
      "offset": { "type": "integer", "minimum": 1 },
      "limit": { "type": "integer", "minimum": 1, "maximum": 2000 }
    },
    "required": ["path"],
    "additionalProperties": false
  },
  "access": "read",
  "timeoutMs": 15000,
  "parallelSafe": true,
  "rollback": false,
  "terminatesTurn": false,
  "pluginId": "tools-fs"
}
```

回执（工具结果 entry 的 payload）：

```json
{
  "callId": "call_01J...",
  "tool": "write_file",
  "status": "ok",
  "content": [{ "type": "text", "text": "wrote 42 bytes to src/main.go" }],
  "details": { "path": "src/main.go", "bytes": 42, "diff": "..." },
  "receipt": {
    "status": "ok",
    "startedAt": "2026-10-28T10:00:00Z",
    "finishedAt": "2026-10-28T10:00:00.012Z",
    "durationMs": 12,
    "truncated": false,
    "checkpoint": "chk_01J..."
  }
}
```

`content` 给模型；`details` 只给界面。拒绝：

```json
{
  "status": "denied",
  "content": [{ "type": "text", "text": "denied by user: 不允许修改生产配置" }],
  "receipt": { "status": "denied", "durationMs": 0, "reason": "user" }
}
```

### 8.5 错误与诊断码

JSON-RPC 标准错误码用于传输层；业务错误用 4 位应用码：

| 码 | 名称 | 典型场景 |
|---|---|---|
| 1001 | `workspace_missing` | 工作区目录不存在 |
| 1002 | `core_unavailable` | Core 未就绪/崩溃 |
| 1003 | `core_busy` | 实例正在回收/启动 |
| 1004 | `workspace_locked` | 另一个 Core 持有工作区锁 |
| 2001 | `thread_busy` | 线程已有运行中轮次 |
| 2002 | `turn_aborted` | 用户取消 |
| 2003 | `budget_exceeded` | 显式配置的墙钟/token/成本上限超限（默认不启用；步数不设限） |
| 2004 | `context_overflow` | 压缩失败且无法继续 |
| 2005 | `loop_detected` | 连续重复相同的工具调用，模型未改道 |
| 3001 | `tool_not_found` | 工具已被禁用 |
| 3002 | `tool_invalid_args` | Schema 校验失败 |
| 3003 | `plugin_timeout` | 插件调用超时 |
| 3004 | `plugin_crashed` | 插件运行时重建 |
| 3005 | `plugin_denied` | 能力/信任不允许 |
| 4001 | `auth_required` | 未登录/ cookie 过期 |
| 4002 | `csrf_invalid` | 表单 token 不匹配 |
| 4003 | `origin_denied` | Origin/Host 校验失败 |
| 5001 | `provider_error` | 模型返回错误 |
| 5002 | `protocol_error` | 适配器流解析失败 |
| 5003 | `storage_busy` | SQLite 写锁等待超时 |
| 5004 | `storage_corrupt` | WAL/数据库损坏，需要 `doctor` 或备份恢复 |

诊断码用于日志与页面提示：`ADG-<AREA>-<NNN>`（例如 `ADG-MODEL-002`、`ADG-PLUGIN-004`）。词汇表在 `internal/diag/codes.go` 一处维护，页面只显示码 + 人话 + 下一步，不显示堆栈。

### 8.6 产品声明

```json
{
  "id": "coding",
  "name": "ada-go Coding",
  "plugins": [
    "provider-openai-chat",
    "tools-fs",
    "tools-shell",
    "tools-git",
    "tools-plan",
    "tools-ask",
    "tools-skill",
    "policy-default",
    "persona-coding",
    "ui-todo",
    "ui-ask-user",
    "provider-admin",
    "token-usage"
  ],
  "capabilities": {
    "skills": true,
    "images": true,
    "subagents": false
  },
  "identity": {
    "name": "ada-go",
    "locale": "zh-CN"
  },
  "policies": {
    "toolTimeoutMs": 120000,
    "turnTimeoutMs": 0,
    "maxTurnTokens": 0,
    "loopRepeatLimit": 3,
    "loopAbortLimit": 5,
    "parallelTools": 1
  }
}
```

声明里每个字段都必须有消费者：

| 字段 | 消费者 |
|---|---|
| `plugins` | 随包官方插件的启用清单；第三方插件由发现目录与用户启用状态决定，装配时合并 |
| `capabilities.skills/images/subagents` | 技能装配、图片输入门、子智能体装配 |
| `identity.name/locale` | 界面品牌与语言；**不含人格**，人格来自 `persona-*` 插件 |
| `policies` | 引擎并发、单工具超时、可选的墙钟/token 上限、循环检测阈值；**不含审批规则**，审批属于 `policy-*` 插件自己的设置 |

`adag verify` 逐字段查找消费者，找不到就失败；`products/skeleton.agent.json` 只声明最小字段且 `plugins: []`，用来证明“产品只是一份声明、空基座可运行”。

---

**相邻文档**：[← SSR 前端](07-ssr-frontend.md) · [索引](design.md) · [安全与运维 →](09-security-operations.md)
