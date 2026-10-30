# OpenAI Responses 供应商（provider-openai-responses）

OpenAI Responses API 的流式协议适配器。与 Chat Completions 的关键差别：系统提示走 `instructions`、工具声明是扁平形状、历史用 `function_call` / `function_call_output` 独立项、终止信号是 `response.completed`。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `provider-openai-responses` |
| 名称 | OpenAI Responses 供应商 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `net.sse` |
| 密钥 | `openai_api_key` |
| 注册的 provider id | `openai-responses` |

## 端点与请求

- 默认地址：`https://api.openai.com/v1/responses`；`baseUrl` 已以 `/responses` 结尾时直接使用。
- 请求头为 `Authorization: Bearer <key>`，`stream: true`。
- 系统提示走顶层 `instructions`；`maxTokens` 映射为 `max_output_tokens`。
- 工具声明为扁平的 `{ type: "function", name, description, parameters }`。
- 用户/助手消息用 `input_text` / `output_text`；工具调用与回执用 `function_call` / `function_call_output`（`call_id` 对应 Chat 的 `tool_call_id`）。

## 内置模型

| 模型 id | 上下文 | 最大输出 | 多模态 | thinking |
| --- | --- | --- | --- | --- |
| `gpt-5.1` | 400k | 128k | 文本 + 图片 | 是 |
| `gpt-5.1-mini` | 400k | 128k | 文本 + 图片 | 是 |
| `o4-mini` | 200k | 100k | 文本 | 是 |

## 流式映射

- `response.output_text.delta` → `text.delta`。
- `response.reasoning_summary_text.delta` → `thinking.delta`。
- `response.output_item.added`（`function_call`）与 `response.function_call_arguments.delta` → 工具调用开始与参数增量。
- `response.completed` 携带最终 `usage`；`status=incomplete` 时把 `incomplete_details.reason` 作为 finishReason。
- `response.created` / `response.in_progress` 等元数据事件不产生增量。

## 失败处理

- **终止信号必须显式**：只有 `response.completed` 才算完成；EOF 前没收到就是协议错误。
- `response.failed` / `response.error` / `error` 帧、非法 JSON 都抛 `ERR_PROVIDER`。
- 缺少密钥是配置错误，直接报错，不按可重试网络错误处理。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [模型与审批](../../../docs/03-model-and-approval.md)
- [插件模型与后端 API](../../../docs/05-plugins.md)
