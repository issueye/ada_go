# OpenAI Chat 供应商（provider-openai-chat）

OpenAI Chat Completions 的流式协议适配器。OpenAI 兼容端点（DeepSeek、Ollama、Moonshot、云知声等）通过产品配置的 `baseUrl` 复用同一适配器，插件不读全局配置。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `provider-openai-chat` |
| 名称 | OpenAI Chat 供应商 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `net.sse` |
| 密钥 | `openai_api_key` |
| 注册的 provider id | `openai` |

## 端点与请求

- 默认地址：`https://api.openai.com/v1/chat/completions`；`baseUrl` 已以 `/chat/completions` 结尾时直接使用。
- 请求头为 `Authorization: Bearer <key>`；`stream: true` 并带 `stream_options.include_usage`。
- 系统提示放进 messages 的第一条 `system`。
- 工具声明为 `{ type: "function", function: { name, description, parameters } }`；工具调用按 `tool_calls` 增量累积，工具回执用 `tool_call_id`。
- 图片以 `image_url` + base64 data URL 传入；`thinking` 增量不回灌给模型。

## 内置模型

| 模型 id | 说明 |
| --- | --- |
| `gpt-4o` / `gpt-4o-mini` | 文本 + 图片 |
| `gpt-4.1` / `gpt-4.1-mini` | 文本 + 图片 |
| `o3-mini` | 文本，支持 thinking |
| `deepseek-chat` | OpenAI 兼容端点 |
| `deepseek-reasoner` | OpenAI 兼容端点，支持 thinking |
| `u2-flash` | 云知声 MaaS，支持 thinking |
| `llama3.1` | Ollama 兼容端点 |

模型清单只是内置默认值；供应商管理页可按实际端点自行增删。

## 流式映射

- `delta.content` → `text.delta`；`delta.reasoning_content` 或 `delta.reasoning` → `thinking.delta`。
- `delta.tool_calls` 按 `index` 归并，`function.arguments` 逐段作为 `tool_call.delta`。
- `usage` 取 `prompt_tokens` / `completion_tokens` / `prompt_tokens_details.cached_tokens`。
- **终止信号必须显式**：只有 `data: [DONE]` 才算完成；EOF 前没收到就是协议错误。

## 失败处理

- 缺少密钥是配置错误，直接报 `ERR_PROVIDER`。
- 错误帧、非法 JSON、未收到 `[DONE]` 都抛 `ERR_PROVIDER`。
- 非零 HTTP 状态由宿主 SSE 层处理；插件不重试，重试由上层轮次决定。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [模型与审批](../../../docs/03-model-and-approval.md)
- [插件模型与后端 API](../../../docs/05-plugins.md)
