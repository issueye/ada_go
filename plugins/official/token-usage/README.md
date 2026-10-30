# Token 用量（token-usage）

按工作区与当前会话展示 token 用量（输入 / 输出 / 缓存）与成本。纯前端插件：只做展示与换算，不自己统计 token，数据来自 Core 的 `usage.get`。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `token-usage` |
| 名称 | Token 用量 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 纯前端 |
| 前端能力 | `backend.read` |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 导航 / 页面 | 导航「Token 用量」→ 页面 `/ext/token-usage/usage`（`page.tsx`） |
| 仪表盘 | 概览页卡片 `usage`（`dashboard.tsx`，order 10） |
| 样式 | `usage.css`（`static/`） |

## 数据形状

数据来自只读投影 `props.data.usage`：

```ts
{
  thread:    { provider, model, inputTokens, outputTokens, cacheTokens, costMicros },
  workspace: { provider, model, inputTokens, outputTokens, cacheTokens, costMicros }
}
```

- 页面并排展示「工作区」（所有会话累计）与「当前会话」两张卡片。
- 数量按千分位格式化；成本由 `costMicros / 1_000_000` 转成美元，无成本时显示 `—`。
- 仪表盘只展示工作区汇总与当前会话明细；两者都没有数据时返回 `null`，概览页不留空卡。

## 边界

- 本插件没有后端与能力，不读写会话存储，也不发起网络请求。
- 数据缺失或字段为空时按 0 处理，不抛错、不猜测。

## 构建

`page.tsx` / `dashboard.tsx` 在渲染时由宿主编译成 VNode 再转 HTML；`static/usage.css` 随包原样嵌入。没有 `dist/`，因为这是纯前端插件。

## 相关文档

- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [会话与存储](../../../docs/04-sessions-storage.md)
