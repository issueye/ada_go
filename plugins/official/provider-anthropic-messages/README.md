# Anthropic Messages 供应商（provider-anthropic-messages）

Anthropic Messages API 的流式协议适配器：只做「SSE 事件 → 归一化 Delta」的映射。TLS、代理、重定向、超时、取消、allowlist 与响应大小都由宿主的 `ctx.http.sse` 执行。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `provider-anthropic-messages` |
| 名称 | Anthropic Messages 供应商 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `net.sse` |
| 密钥 | `anthropic_api_key` |
| 注册的 provider id | `anthropic` |

## 端点与请求

- 默认地址：`https://api.anthropic.com/v1/messages`；`baseUrl` 可由产品配置覆盖（网关类兼容端点）。
- 请求头固定 `x-api-key` 与 `anthropic-version: 2023-06-01`。
- 系统提示走顶层 `system`；`max_tokens` 必填：优先请求参数，其次模型声明的 `maxOutput`，最后回退 `8192`。
- 工具声明为 `{ name, description, input_schema }`；工具调用用 `tool_use`，工具回执用 `tool_result`，相邻回执合并进同一条 user 消息。

## 内置模型

| 模型 id | 上下文 | 最大输出 | 多模态 | thinking |
| --- | --- | --- | --- | --- |
| `claude-sonnet-4.5` | 200k | 64k | 文本 + 图片 | 是 |
| `claude-haiku-4.5` | 200k | 64k | 文本 | 否 |
| `claude-opus-4.1` | 200k | 32k | 文本 + 图片 | 否 |

## 流式映射

- 按 content block index 区分 text / thinking / tool 槽位，`content_block_stop` 关闭对应增量。
- `input_json_delta.partial_json` 累积为工具参数片段。
- `usage` 由 `message_start`（输入与缓存）与 `message_delta`（输出）合成一条。
- **终止信号必须显式**：只有 `message_stop`（事件名或 `data.type`）才算完成；EOF 前没收到就是协议错误。

## 失败处理

- 缺少密钥是配置错误，直接报 `ERR_PROVIDER`，不按可重试网络错误处理。
- `error` 帧、非法 JSON、未收到 `message_stop` 都抛 `ERR_PROVIDER`。
- 被轮次取消时由宿主中断 SSE，插件不做重试。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [模型与审批](../../../docs/03-model-and-approval.md)
- [插件模型与后端 API](../../../docs/05-plugins.md)
