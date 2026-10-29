# 模型适配、审批与取消

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§5.5 模型适配层、§5.6 审批（策略插件）与取消  
> 版本：0.1 · 2026-10-28

## 5.5 模型适配层

基座只拥有 `ModelClient` 契约与归一化流 DTO；**具体供应商是官方 TS provider 插件**，通过 `agent.registerProvider` 注册，由宿主桥接成 `ModelClient`。基座不知道 OpenAI/Anthropic 的 URL、鉴权头、SSE 细节与工具调用格式。

用户配置的供应商实例保存在数据目录 `providers.json`（0600）。Registry 按 `Protocol` 选择适配器插件，把该实例的 `baseUrl`/`apiKey`/模型清单注入一次流式请求；首次启动从已装配的适配器播种默认实例，之后以配置为准。API Key 不返回给页面，只显示是否配置与尾号。供应商端点的 host 会在配置加载/保存时加入宿主 HTTP 桥的 allowlist（空 BaseURL 按协议默认域名），改端点不需要重启 Core。

```go
type ModelClient interface {
    Stream(ctx context.Context, req Request) (Stream, error)
}

type Stream interface {
    Recv() (Delta, error) // text|thinking|tool_call|usage|finish
    Close() error
}

type Request struct {
    Model    ModelRef
    System   string
    Messages []Message // 已转换、已裁剪
    Tools    []ToolSchema
    Params   Params    // temperature、max tokens、thinking 等
}
```

provider 插件面（TS）：

```ts
interface ProviderDef {
  id: string;
  models: ModelInfo[];                      // 能力声明，产品配置只按 id 引用
  stream(req: ProviderRequest, ctx: ProviderContext): Promise<void>;
}

interface ModelInfo {
  id: string;                               // 产品配置中的模型名
  displayName?: string;
  contextWindow: number;                    // token
  maxOutput?: number;
  input: ("text" | "image")[];
  supportsTools: boolean;
  supportsThinking?: boolean;
  pricing?: { inputPerMTok: number; outputPerMTok: number };
}

interface ProviderRequest {
  model: string;
  system: string;
  messages: Message[];                      // 已转换、已裁剪
  tools: ToolSchema[];
  params: Params;
  baseUrl?: string;                         // 兼容端点覆盖；空时插件用官方默认地址
}

interface HttpInit {
  method?: "GET" | "POST";
  headers?: Record<string, string>;
  body?: string;
  signal?: AbortSignal;
  maxBytes?: number;                        // 由宿主再次夹紧
}

interface ProviderContext {
  signal: AbortSignal;
  secret(name: string): string | null;      // manifest 声明后才有
  http: {
    sse(url: string, init: HttpInit, onEvent: (e: { event?: string; data: string }) => void | Promise<void>):
      Promise<{ status: number; headers: Record<string, string> }>;
    json(url: string, init: HttpInit):
      Promise<{ status: number; headers: Record<string, string>; body: unknown }>;
  };
  emit(delta: Delta): void;                 // 归一化增量
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
}
```

`http.sse` 是受信能力 `net.sse`，只授予 builtin/verified；TLS、代理、重定向、连接/空闲/总时长、取消与响应大小都由宿主执行，插件只做“SSE 事件 → 归一化增量”的协议映射。provider 的 `stream` 是长任务，不受 5s 脚本调用超时约束，受建连 15s、空闲 90s、总时长 30 分钟与轮次取消约束。provider 清单声明 `backend.capabilities: ["net.sse"]` 与 `secrets`，模型名、`base_url` 与参数由产品配置传入，插件不读全局配置。

Node 兼容层**不安装** `node:http`/`node:https`/`node:net`/`node:tls`/`node:dns`。provider 只能用 `ctx.http.sse`/`ctx.http.json`，不能用全局 `fetch` 或自建连接；这样代理、证书、重定向、DNS 与超时都只有一个执行点。完整模块矩阵见 [6.12](12-node-compat.md#612-node-兼容原生库)。

- 统一增量：`text.start/delta/end`、`thinking.start/delta/end`、`tool_call.start/delta/end`、`usage`、`finish`。
- 工具调用参数按 `index` 累积 JSON 片段；流结束时整体校验，失败作为工具错误回执。
- 官方 provider 插件：`provider-openai-chat`、`provider-anthropic-messages`、`provider-openai-responses`；任何 OpenAI 兼容端点（DeepSeek、Ollama、Moonshot 等）通过 `base_url` 复用 Chat 适配器。
- 第三方 provider 不在 v1 开放：只允许官方 provider 插件；第三方仍可通过官方 Chat 插件的 `base_url` 接入兼容端点，不向工作区开放任意鉴权与长连接代码。
- 终止信号必须显式：`[DONE]`、`message_stop`、`response.completed`。**EOF 不等于完成**；没有终止信号的 EOF 是错误。
- 超时分层：建连 15s、空闲 90s（可通过 `ADAG_STREAM_IDLE` 调整）、总时长 30 分钟；取消贯穿到 HTTP 请求。
- 重试：由基座的 `ModelClient` 包装层负责，provider 只实现单次流；只对 429/5xx/网络错误且在**首个增量之前**重试，指数退避 + 抖动，最多 3 次，尊重 `Retry-After`。已经开始输出后不自动重试，避免重复内容。
- 图片输入：模型能力位 + 产品能力位双门；任一说 false 就拒绝，而不是悄悄丢图。
- 用量：每次请求记录 input/output/cache token；有价的模型显示估算成本，没有价就只显示 token。

## 5.6 审批（策略插件）与取消

审批参考 pi：**内核不内置审批策略**，只提供 `tool.beforeCall` 钩子与通用 `ctx.ui`；由策略插件决定何时询问、如何裁决。工具描述符只有 `Access` 效果声明，没有 `approval` 字段。

**执行前链路**：

```
schema/scope 校验 → tool.beforeCall 钩子链（按 priority）
  → 策略插件可调用 ctx.ui.confirm/select/input
  → 用户响应 / 超时 / 无 UI
  → 钩子返回 allow 或 deny
  → 执行 或 生成 denied 回执
```

- 任何插件都可以在 `tool.beforeCall` 返回 `deny`；`replace`（替换参数后重新校验）与 `abort` 仅 builtin。
- `ctx.ui` 是能力 `ui.ask`，只授予 builtin/verified 插件；第三方不能弹窗，避免钓鱼。
- 钩子等待用户响应时占用该线程的调用队列，但不阻塞其他线程；同一线程最多一个待处理交互。
- 超时解析为 `timeout`；SSE 断线、无 UI 解析为 `unavailable`。两者都必须被策略插件当作拒绝，基座不会替它放行。
- 一次交互恰好解析一次，状态为 `accepted | denied | cancelled | timeout | unavailable`；待处理交互持久化，Gateway/Core 重启后可恢复。
- `interaction.respond` 必须同时匹配 `threadId` 与 `requestId`，且只接受 `pending`、未过期的请求；重复响应幂等返回首次结果，过期/伪造返回错误且不改变状态。

**官方 `policy-default`（TS，builtin，`role=tool-policy`）**：

| 模式 | 读 | 写 | 执行/网络 |
|---|---|---|---|
| `readonly` | 允许 | 拒绝 | 拒绝 |
| `ask`（默认） | 允许 | 询问 | 询问 |
| `auto` | 允许 | 允许（scope 内） | 询问 |
| `full` | 允许 | 允许（scope 内） | 允许（仍记录审计） |

- 它订阅 `tool.beforeCall`，根据 `Access`、工具名、参数与线程/工作区 scope 决定是否 `ctx.ui.confirm`；询问时显示完整命令、cwd、原始参数（转义后）与风险说明。
- 模式是策略插件自己的设置，不是基座配置；换一个策略插件就换一套规则，引擎不感知。
- 非交互模式（CLI/print）没有 UI：`policy-default` 对写/执行/网络直接拒绝。
- 拒绝理由作为工具结果回给模型（`denied: ...`），界面卡片显示 `denied`，不显示成成功。

**没有策略插件时**：基座对 `write`/`exec`/`network` 工具返回 `denied: no policy`；只读工具不受影响。产品门禁 `adag verify` 要求启用这类工具时必须至少有一个 `role=tool-policy` 插件；运行期禁用策略插件后，写/执行工具立即变成拒绝，而不是裸奔。

**与 pi 的对应关系**：

| pi | ada-go | 说明 |
|---|---|---|
| `pi.on("tool_call")` | `agent.on("tool.beforeCall")` | 同为执行前钩子；ada-go 的 `replace` 仅 builtin，`deny` 对所有插件开放 |
| `ctx.ui.confirm/select/input` | `ctx.ui.*` | 同一组通用交互原语，不含审批语义 |
| `ctx.hasUI` | `ctx.ui.available` | 无 UI 时由策略插件决定失败方向 |
| `{ block: true, reason }` | `{ action: "deny", reason }` | 拒绝变成 `denied` 工具回执，轮次继续 |
| 扩展自己实现 permission gate | `policy-default` / 其他 `tool-policy` 插件 | 基座没有规则；换插件即换策略 |
| TUI 选择器（进程内） | Gateway `InteractionTray` + `interactions` 表 | 跨进程，因此交互必须持久化、可恢复、可幂等响应 |

差别只在进程边界：pi 的扩展与 TUI 同进程，可以直接阻塞；ada-go 的钩子在 Core、界面在 Gateway，所以 `ctx.ui` 必须经过通用交互桥，并把待处理请求写入 `interactions` 表，才能做到断线/重启后恢复。基座增加的只有这条桥，不是审批规则。

**取消**：

- 一个 `context.Context` 从 RPC 贯穿到 provider、工具、插件脚本、命令进程树，以及等待中的交互请求。
- `run_command` 在 Unix 用进程组、Windows 用 Job Object 杀整棵树；不是“等命令自己结束”。
- moejs 由宿主从任意 goroutine 调用 `Runtime.Interrupt` 中止脚本；中断不可被脚本捕获。
- 取消后仍必须发一次 `TurnFinished(cancelled)`，并保留已经产生的部分消息；未决交互标记 `cancelled`。

---

**相邻文档**：[← Agent 循环与工具](02-agent-loop.md) · [索引](design.md) · [会话、存储与子智能体 →](04-sessions-storage.md)
