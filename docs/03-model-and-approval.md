# 模型适配、审批（已移除）与取消

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§5.5 模型适配层、§5.6 审批（已移除）与取消  
> 版本：0.3 · 2026-11-10

## 5.5 模型适配层

基座只拥有 `ModelClient` 契约与归一化流 DTO；**具体供应商是官方 TS provider 插件**，通过 `agent.registerProvider` 注册，由宿主桥接成 `ModelClient`。基座不知道 OpenAI/Anthropic 的 URL、鉴权头、SSE 细节与工具调用格式。

供应商配置是**系统插件 `provider-admin` 拥有的数据**，保存在机器级命名空间（`global.config` 的 `provider-config` 命名空间，默认落地 `~/.adago/config/provider-config.json`，0600），切换工作区共享；显式传 `--data-dir` 时使用该数据目录下的 `config/` 做隔离。`provider.GlobalStore` 只在首次加载时读取并迁移 legacy `providers.json`，之后只写 `global.config`；插件 CRUD 收敛仍待完成。基座只读取当前模型与供应商配置视图，按 `Protocol` 选择适配器插件，把 `baseUrl`/`apiKey`/模型清单注入一次流式请求；**基座不做供应商/模型的 CRUD、不拉 `/models`、不渲染管理页**。首次启动只在已配置对应密钥（环境变量/secrets.toml）或显式 BaseURL 时播种默认实例；升级迁移只搬运用户确实改过的配置。API Key 不返回给页面，只显示是否配置与尾号。供应商端点由宿主 HTTP 桥直接访问，当前不做 host 黑白名单（后续以网络策略插件实现，见 `AGENTS.md`）；改端点不需要重启 Core。

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
  protocol?: string;                        // 供应商配置里的协议标识（如 openai_chat）
  defaultBaseUrl?: string;                  // 播种默认供应商时的端点
  secretName?: string;                      // 播种/密钥提示用的清单 secrets 名
  displayName?: string;                     // 供应商页显示名
  authHeader?: string;                      // 宿主代发 /models 时的鉴权头，如 Authorization / x-api-key
  authPrefix?: string;                      // 鉴权值前缀，如 "Bearer "
  apiVersionHeader?: string;                // 版本头，如 anthropic-version
  apiVersion?: string;                      // 版本头的值
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

`http.sse` 在历次设计里记为受信能力 `net.sse`；能力位已不再决定「装什么」（2026-11-10 决策），只剩清单层的声明校验：声明 `net.sse` 要求 builtin/verified，第三方声明即装配失败。TLS、代理、重定向、连接/空闲/总时长、取消与响应大小都由宿主执行，插件只做“SSE 事件 → 归一化增量”的协议映射。provider 的 `stream` 是长任务，不受 5s 脚本调用超时约束，受建连 15s、空闲 90s、总时长 30 分钟与轮次取消约束。provider 清单声明 `backend.capabilities: ["net.sse"]` 与 `secrets`，模型名、`base_url` 与参数由产品配置传入，插件不读全局配置。`ProviderDef` 的 `protocol` / `defaultBaseUrl` / `secretName` / `displayName` / `authHeader` / `authPrefix` / `apiVersionHeader` / `apiVersion` 用于系统 provider 插件声明播种默认供应商、密钥提示与代发 `/models` 的元数据；基座不写死具体供应商的端点、密钥名或鉴权头。`ModelClient` 是内核 seam：默认实现由 provider 插件桥接，替换实现必须在装配期注入并通过流式契约测试。

Node 兼容层**不安装** `node:http`/`node:https`/`node:net`/`node:tls`/`node:dns`。provider 只能用 `ctx.http.sse`/`ctx.http.json`，不能用全局 `fetch` 或自建连接；这样代理、证书、重定向、DNS 与超时都只有一个执行点。完整模块矩阵见 [6.12](12-node-compat.md#612-node-兼容原生库)。

- 统一增量：`text.start/delta/end`、`thinking.start/delta/end`、`tool_call.start/delta/end`、`usage`、`finish`。
- 工具调用参数按 `index` 累积 JSON 片段；流结束时整体校验，失败作为工具错误回执。
- 系统 provider 插件：`provider-openai-chat`、`provider-anthropic-messages`、`provider-openai-responses`；任何 OpenAI 兼容端点（DeepSeek、Ollama、Moonshot 等）通过 `base_url` 复用 Chat 适配器。
- 第三方 provider 不在 v1 开放：只允许系统 provider 插件；第三方仍可通过官方 Chat 插件的 `base_url` 接入兼容端点，不向工作区开放任意鉴权与长连接代码。
- 终止信号必须显式：`[DONE]`、`message_stop`、`response.completed`。**EOF 不等于完成**；没有终止信号的 EOF 是错误。
- 超时分层：建连 15s、空闲 90s（可通过 `ADAG_STREAM_IDLE` 调整）、总时长 30 分钟；取消贯穿到 HTTP 请求。
- 重试：由基座的 `ModelClient` 包装层负责，provider 只实现单次流；只对 429/5xx/网络错误且在**首个增量之前**重试，指数退避 + 抖动，最多 3 次，尊重 `Retry-After`。已经开始输出后不自动重试，避免重复内容。
- 图片输入：模型能力位 + 产品能力位双门；任一说 false 就拒绝，而不是悄悄丢图。（这里的「能力位」是模型与产品声明的**功能开关**，与已移除的插件安装能力位不是一回事。）
- 用量：每次请求记录 input/output/cache token；有价的模型显示估算成本，没有价就只显示 token。

## 5.6 审批（已移除）与取消

> 状态：**审批策略已整体移除**（2026-11-10 决策，口径见 `AGENTS.md`）。本节保留通用交互桥与取消，不再描述任何审批规则。

**基座不内置审批，也不再失败关闭**：`tool.beforeCall` 钩子点位与 `policy-default` 插件都已删除，工具描述符只有 `Access` 效果声明（供 UI 徽标与审计），没有 `approval` 字段。写/执行工具**直接执行**，没有「没有策略插件就拒绝」的判定。

**执行前链路**：

```
schema/scope 校验 → 执行 → 结构化回执 → tool.afterCall 钩子（只观察）
```

- 钩子动作只剩 `observe` / `note` / `replace` / `abort`；`deny` 等级已删除，不存在「执行前拒绝」的钩子路径。
- `ctx.ui`（能力 `ui.ask`，仅 builtin/verified）仍存在：它是**通用交互原语**（confirm/select/input/form/notify），供 `tools-ask` 这类插件在工具内主动提问，不是审批开关。
- 工具可以自己返回 `denied` 回执（`ReceiptDenied`）：那是工具实现的业务决定，基座不做判定，轮次继续。

**通用交互桥（仍然生效）**：

- 交互请求经 `ctx.ui` → 宿主 → Gateway `InteractionTray`，持久化到 `interactions` 表；等待用户响应时占用该线程的调用队列，但不阻塞其他线程；同一线程最多一个待处理交互。
- 超时解析为 `timeout`；SSE 断线、无 UI 解析为 `unavailable`。基座不替调用方解释这两种结果的业务含义，调用方必须自己按拒绝方向处理。
- 一次交互恰好解析一次，状态为 `accepted | denied | cancelled | timeout | unavailable`；Gateway/Core 重启后可恢复。
- `interaction.respond` 必须同时匹配 `threadId` 与 `requestId`，且只接受 `pending`、未过期的请求；重复响应幂等返回首次结果，过期/伪造返回错误且不改变状态。

**与 pi 的对应关系**：

| pi | ada-go | 说明 |
|---|---|---|
| `pi.on("tool_call")` | 无对应点位（`tool.beforeCall` 已删除） | ada-go 执行前没有可阻塞的钩子；需要前置约束请在工具实现里做 |
| `ctx.ui.confirm/select/input` | `ctx.ui.*` | 同一组通用交互原语，不含审批语义 |
| `ctx.hasUI` | `ctx.ui.available` | 无 UI 时由调用方决定方向 |
| `{ block: true, reason }` | `ReceiptDenied`（工具自己返回） | 拒绝由工具实现决定，不是基座钩子 |
| 扩展自己实现 permission gate | 系统插件自己的工具实现 | 基座没有规则，也没有策略插件角色 |
| TUI 选择器（进程内） | Gateway `InteractionTray` + `interactions` 表 | 跨进程，因此交互必须持久化、可恢复、可幂等响应 |

差别只在进程边界：pi 的扩展与 TUI 同进程，可以直接阻塞；ada-go 的交互在 Core、界面在 Gateway，所以 `ctx.ui` 必须经过通用交互桥，并把待处理请求写入 `interactions` 表，才能做到断线/重启后恢复。

**取消**：

- 一个 `context.Context` 从 RPC 贯穿到 provider、工具、插件脚本、命令进程树，以及等待中的交互请求。
- `run_command`/`run_background` 在 Unix 用进程组、Windows 用 `taskkill /T` 杀整棵树；后台任务在插件 Runtime 关闭时同样终止；不是“等命令自己结束”。
- moejs 由宿主从任意 goroutine 调用 `Runtime.Interrupt` 中止脚本；中断不可被脚本捕获。
- 取消后仍必须发一次 `TurnFinished(cancelled)`，并保留已经产生的部分消息；未决交互标记 `cancelled`。

---

**相邻文档**：[← Agent 循环与工具](02-agent-loop.md) · [索引](design.md) · [会话、存储与子智能体 →](04-sessions-storage.md)
