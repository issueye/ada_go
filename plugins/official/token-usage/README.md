# Token 用量（token-usage）

按工作区与当前会话展示 token 用量（输入 / 输出 / 缓存）与成本。纯前端插件：只做展示与换算，不自己统计 token，数据来自 Core 的 `usage.get`。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `token-usage` |
| 名称 | Token 用量 |
| 版本 | 0.2.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 纯前端 |
| 前端能力 | `backend.read`、`frontend.script` |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 导航 / 页面 | 导航「Token 用量」→ 页面 `/ext/token-usage/usage`（`page.tsx`，SSR + hydrate） |
| 仪表盘 | 概览页卡片 `usage`（`dashboard.tsx`，order 10） |
| Markdown 动态块 | `usage-card`（`blocks/usage-card.tsx` + `schemas/usage-card.json`） |
| 样式 | `usage.css`（`static/`） |
| 浏览器 bundle | `static/client.js`（由 `pluginbuild` 从 `page.tsx` 生成） |

页面是 P2-4 首个真实业务迁移：服务端 `renderToJSON` 输出首屏，浏览器用同一份 `defineComponent` hydrate。重设计后：

- 头部 `ⓘ` tooltip 承载口径说明，操作是「复制汇总」「导出 CSV」（都在浏览器本地生成，不发请求）；一次性反馈走底部 toast（`showToast` 方法名与 `toast` 数据字段不同名，避免单 Proxy 遮蔽）。
- 「工作区 / 当前会话」分段切换四张 KPI 卡的数据源（累计 Token / 输入 / 输出 / 缓存命中率）。
- 「用量构成」用三条计量条展示 输入、输出、缓存占输入的比例；计量条宽度用 10% 一档的类名表达（SSR VNode 白名单不含 `style`）。
- 「模型与成本」显示 `costMicros` 与单组 `provider/model`；多分组被后端折叠时如实标注，不编造拆分。
- **不做**近 7 天趋势、按模型拆分、环比：`usage.get` 只返回两个标量聚合，没有日期分桶、分组明细或上期值。

`dashboard.tsx` 仍是概览页的 SSR 插槽片段，样式复用 `usage.css` 的 `tu-widget__*`。

## 构建

```sh
go run ./tools/pluginbuild -dir plugins/official/token-usage -entry client.tsx -out static/client.js
go run ./tools/pluginbuild -dir plugins/official/token-usage -entry client.tsx -out static/client.js -check
```

## 数据形状

数据来自只读投影 `props.data.usage`：

```ts
{
  thread:    { provider, model, inputTokens, outputTokens, cacheTokens, costMicros },
  workspace: { provider, model, inputTokens, outputTokens, cacheTokens, costMicros }
}
```

- 四张 KPI 卡显示选中范围（工作区或当前会话）的累计 / 输入 / 输出 / 缓存命中率；缓存命中率 = `cacheTokens / inputTokens`。
- 数量按千分位格式化；成本由 `costMicros / 1_000_000` 转成美元，无成本时显示 `—`。
- 累计 = 输入 + 输出；缓存是输入的一部分，**不重复计入**。
- 仪表盘只展示工作区汇总与当前会话明细；两者都没有数据时返回 `null`，概览页不留空卡。

## Markdown 用法

renderer id 是 `token-usage/usage-card`。在助手回答里用 `adag-ui` fence 引用：

````markdown
```adag-ui
{
  "renderer": "token-usage/usage-card",
  "id": "b1",
  "fallback": "用量卡片暂不可用",
  "data": {
    "scope": "thread",
    "title": "本轮用量",
    "usage": { "inputTokens": 1234, "outputTokens": 567, "cacheTokens": 89, "costMicros": 1200 }
  }
}
```
````

`data` 由 `schemas/usage-card.json` 校验：`scope` 必填（`thread` | `workspace`），
`title`、`usage` 可选；缺失字段按 0 / `—` 展示。块只做展示，交互走页面或工具。

## 边界

- 本插件没有后端与能力，不读写会话存储，也不发起网络请求。
- 数据缺失或字段为空时按 0 处理，不抛错、不猜测。

## 构建

`page.tsx` / `dashboard.tsx` 在渲染时由宿主编译成 VNode 再转 HTML；`static/usage.css` 随包原样嵌入。没有 `dist/`，因为这是纯前端插件。

## 相关文档

- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [会话与存储](../../../docs/04-sessions-storage.md)
