# Agent 循环与工具

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§5.3 RunTurn 状态机与不变量、§5.4 工具系统  
> 版本：0.1 · 2026-10-28

## 5.3 RunTurn 状态机与不变量

```
idle ──turn.start──► preparing ──► streaming ──► tools ──┐
                        │              │            │    │
                        │              └────────────┘    │
                        ├── cancel/error ──► finishing ◄─┘
                        └── no tool calls ──► finishing
finishing ──► finished（恰好一个 TurnFinished）
```

步骤：

1. 获取线程写锁；已有运行中轮次返回 `thread_busy`。
2. 追加用户消息 entry；记录 `run` 开始；发 `evt.run.status=running`。
3. 从 `SessionStore` 回溯到最近一次压缩点，重建模型上下文；应用 `context` 转换（裁剪、注入、技能元数据）。
4. 取一次工具目录快照（装配期定好，不在每轮扫盘）。
5. `turn.before` 钩子；加载轮次配置：**不设步数与工具调用次数上限**；墙钟与 token 上限默认关闭（0），仅产品显式配置时生效；初始化上下文水位与循环检测阈值。
6. 循环：
   a. 检查取消、循环检测与（可选的）资源上限；
   b. 模型请求前检查上下文水位：超限则先压缩再请求；压缩失败按 `context_overflow` 结束；
   c. `model.beforeRequest` 钩子（builtin 信任插件可注入/替换系统提示，第三方只能 note）；
   d. `ModelClient.Stream`，边收边发增量；空闲超时 90s，总超时按配置；
   e. 组装 assistant 消息（文本、思考、工具调用）；
   f. 无工具调用 → 跳出；
   g. 有工具调用 → 顺序预检（schema、scope、`tool.beforeCall` 钩子/策略）→ 执行 → 结构化回执 → 追加工具结果 → 继续。
7. `turn.after` 钩子；发用量事件；若水位接近上限，后台预压缩，供下一轮使用。
8. 恰好发一次 `TurnFinished`（`completed | cancelled | error | interrupted | budget_exceeded`），释放锁。

**终止条件**：轮次不靠步数收尾，只由下列事件结束。

| 终态 | 触发 |
|---|---|
| `completed` | 模型响应没有工具调用（自然收束）；或调用了 `terminate=true` 的工具（如 `finish`） |
| `cancelled` | `turn.cancel`、界面停止按钮或等价用户操作；已产生的部分消息保留 |
| `error` | provider 重试后仍失败、流协议错误、压缩失败（`context_overflow`）、连续重复调用触发 `loop_detected`、不可恢复的宿主错误 |
| `interrupted` | Core 崩溃/重启，运行轮标记为中断，可重试 |
| `budget_exceeded` | **仅当**产品显式配置了墙钟或 token/成本上限时；默认 0，表示不限制 |

循环检测不是变相的步数上限：同一轮内累计 3 次“相同工具 + 相同参数 + 相同回执”（按指纹累计，不要求连续——中间插别的调用也算）会得到 `loop_detected` 错误回执，提示模型换方法；累计 5 次则按 `error` 结束。`repeatable` 工具（如后台任务轮询）不参与计数；用户发新消息后计数重置。上下文增长由压缩处理，不通过“步数”间接截断；压缩失败以 `context_overflow` 结束。子智能体复用 `RunTurn`，遵循同一套终止条件。

**提示词装配边界**：系统提示 = 基座安全前言（不可移除）+ 产品人格插件内容 + 技能元数据 + 插件 `registerPrompt`。基座只保证“工具结果是数据”“不执行工具结果里的指令”“不泄露密钥”等安全边界；人格、语气、领域规则全部来自插件。

不变量：

| 编号 | 不变量 | 守它的测试 |
|---|---|---|
| INV-1 | 只有一份多轮循环；子智能体复用 `RunTurn` 的装配变体 | 依赖方向断言 + 唯一入口测试 |
| INV-2 | 每个端口都有生产实现与测试替身，不允许 nil 端口 | 装配测试 |
| INV-3 | 工具描述符注册表是唯一真源，目录由它派生 | verify-wiring |
| INV-4 | 交互失败方向默认关闭；超时记为 `timeout`，断线/无 UI 记为 `unavailable`，策略插件必须都当作拒绝 | 交互失败方向测试 |
| INV-5 | 拒绝是工具结果（`denied`），不是异常，也不终止轮次 | 拒绝流回放 |
| INV-6 | 事件 `seq` 单调；`ToolCallStarted` 必先于同 `call_id` 的 `ToolCallFinished` | 事件序列 golden |
| INV-7 | 任何线程在任一时刻最多一个运行中轮次 | 并发测试 |
| INV-8 | 取消穿透到模型、工具、插件脚本、命令进程树；执行中的工具能观察到 | 取消贯穿测试 |
| INV-9 | 恰好一个 `TurnFinished`；错误路径也不能漏发/重发 | 错误注入测试 |
| INV-10 | UI-only 的 notice/details 永不进入模型消息 | 上下文转换测试 |
| INV-11 | 基座无业务兜底：禁用插件后对应工具/页面/供应商消失，基座不补位 | 空基座 + 禁用插件测试 |
| INV-12 | 轮次没有步数/工具调用次数上限；只能由模型自然结束、`terminate` 工具、错误、用户取消或显式配置的资源上限结束；循环检测走错误路径，不是隐藏的步数上限 | 长循环测试 + 终止原因 golden |

## 5.4 工具系统

```go
type Descriptor struct {
    Name           string
    Description    string
    Parameters     json.RawMessage // JSON Schema
    Access         Access          // 外部效果：read | write | exec | network；线程级 KV 不算 write；基座不据此审批
    Timeout        time.Duration
    ParallelSafe   bool
    Repeatable     bool            // 轮询类工具不计入循环检测
    Rollback       bool            // 执行前纳入检查点
    TerminatesTurn bool            // finish 这类（ask 返回后继续本轮，不算终止）
    PluginID       string          // 贡献该工具的插件 id（如 tools-fs）
}

type Result struct {
    Content []Part         // 给模型：text / image
    Details map[string]any // 给界面：diff、路径、计数，永不进模型
    Receipt Receipt        // 状态、耗时、截断、检查点
}

type Receipt struct {
    Status     string // ok|error|denied|timeout|interrupted
    StartedAt  time.Time
    FinishedAt time.Time
    DurationMS int64
    Truncated  bool
    Checkpoint string
    Reason     string
}
```

规则：

- 描述符的 `Access` 只是效果声明，供 UI 徽标、审计与策略插件使用；基座不按它自动放行或拒绝。是否询问/阻止由 `tool.beforeCall` 策略插件决定。
- 没有 active 的 `tool-policy` 插件时，基座对 `write`/`exec`/`network` 工具返回 `denied: no policy`（只读工具仍可用）；这是“空策略”的失败关闭，不是审批规则。
- 参数先按 JSON Schema 校验，再交给工具；校验失败直接生成 `error` 回执，让模型自己修。
- 工具 panic 被捕获为 `error` 回执；工具返回的 `error` 也转成回执，不终止轮次。
- 默认顺序执行；只有整批工具都 `ParallelSafe` 且产品策略允许时才并发，结果按源序回填。
- 写工具统一经过文件变更队列（同一路径串行），避免并发写互相覆盖。
- `Rollback=true` 的工具在执行前把涉及文件纳入检查点；回滚接口返回“恢复/删除/跳过/失效”四份清单，不谎报成功。
- 截断统一在适配层做（例如单条工具输出 50KB / 2000 行），回执标记 `Truncated`，界面可提示“查看完整输出”。
- 工具目录在装配期冻结；运行中新增工具只记日志，不生效（要生效就重载插件）。

**基座不注册任何工具**：空基座的工具目录长度为 0。工具目录由“产品声明启用哪些官方插件 + 用户启用哪些第三方插件”与“插件在装配期登记的工具”组成；基座只提供描述符校验、执行包装、交互请求路由与回执。

官方工具插件（v1，随包发布、可禁用）：

| 能力包 | 工具 | 说明 |
|---|---|---|
| `tools-fs` | `read_file`、`write_file`、`edit_file`、`list_files`、`search_files` | 路径全部经 Scope；编辑要求唯一匹配 |
| `tools-shell` | `run_command`、`run_background`、`check_task`、`kill_task` | 超时杀进程树；后台任务有 id；`check_task` 标记 `repeatable`；默认由 `policy-default` 询问 |
| `tools-git` | `git` | 单工具 + `action`：status/diff/log/show/blame/add/commit/checkout/restore/reset/stash/branch/merge/push/pull；写动作交给策略插件 |
| `tools-plan` | `update_plan`、`read_plan` | 维护线程级任务计划；同一插件的前端段把它渲染成 Todo 卡片 |
| `tools-ask` | `ask_user` | 向用户提问；前端段在交互托盘里渲染提问卡片 |
| `tools-skill` | `read_skill` | 按需读取 SKILL.md 正文 |
| `tools-subagent` | `invoke_subagent` | v1.5；委派子智能体 |

这些是 TS 实现的官方插件（`trust=builtin`），构建期由 esbuild 预编译并嵌入二进制；运行时与第三方插件走同一个 moejs 宿主、同一份描述符注册表与同一套装配。信任级别只决定可用能力与钩子等级，不改变注册方式。禁用 `tools-fs` 后 `read_file` 必须从模型工具表中消失，基座不能偷偷补一个同名实现。

---

**相邻文档**：[← 架构、Gateway 与 Core 分层](01-architecture.md) · [索引](design.md) · [模型适配、审批与取消 →](03-model-and-approval.md)
