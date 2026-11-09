# Token 用量（token-usage）

按工作区与当前会话展示 token 用量（输入 / 输出 / 缓存）与成本。后端只从 `events.read` 读取每轮原始用量行，聚合口径、页面、卡片都在本插件内；禁用后基座不补位。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `token-usage` |
| 名称 | Token 用量 |
| 版本 | 0.3.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 route/只读动作 + 前端页面 |
| 后端能力 | `events.read` |
| 前端能力 | `backend.read`、`frontend.script` |

## 数据流

```text
SQLite usage 原始行 ──ctx.events.usage──> index.ts 聚合
                                           ├─ registerAction("usage") → props.data.backend["token-usage"].page_usage
                                           └─ GET /ext/token-usage/api/usage?threadId=…（浏览器刷新）
```

- 基座只持久化每轮 `provider/model/inputTokens/outputTokens/cacheTokens/costMicros/threadId`。
- `index.ts` 按线程/工作区汇总，并保留 `groups`（provider+model 分组）；单组时才回填顶层 `provider/model`。
- 前端从 `props.data.backend["token-usage"].page_usage` 取数；不再有 `props.data.usage` 投影。

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 导航 / 页面 | 导航「Token 用量」→ 页面 `/ext/token-usage/usage`（`page.tsx`，SSR + hydrate） |
| 仪表盘 | 概览页卡片 `usage`（`dashboard.tsx`，order 10） |
| Markdown 动态块 | `usage-card`（`blocks/usage-card.tsx` + `schemas/usage-card.json`） |
| 样式 | `usage.css`（`static/`） |
| 浏览器 bundle | `static/client.js`（由 `pluginbuild` 从 `client.tsx` 生成） |

页面用服务端 `renderToJSON` 输出首屏，浏览器用同一份 `defineComponent` hydrate：

- 头部 `ⓘ` tooltip 承载口径说明；操作是「复制汇总」「导出 CSV」（都在浏览器本地生成，不发请求）；一次性反馈走底部 toast。
- 「工作区 / 当前会话」分段切换四张 KPI 卡（累计 Token / 输入 / 输出 / 缓存命中率）；缓存命中率 = `cacheTokens / inputTokens`。
- 「用量构成」用三条计量条展示输入、输出、缓存占输入的比例；计量条宽度用 10% 一档的类名表达（VNode 白名单不含 `style`）。
- 「模型与成本」显示 `costMicros` 与 `groups`；多组时标注为多模型合并，不编造拆分。

## 构建

```sh
go run ./tools/pluginbuild -dir plugins/official/token-usage -entry client.tsx -out static/client.js
go run ./tools/pluginbuild -dir plugins/official/token-usage -entry client.tsx -out static/client.js -check
```

`page.tsx` / `dashboard.tsx` 在渲染时由宿主编译成 VNode 再转 HTML；`index.ts` 由 `tools/pluginbuild` 预编译到 `dist/index.js`。

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

`data` 由 `schemas/usage-card.json` 校验：`scope` 必填（`thread` | `workspace`），`title`、`usage` 可选；缺失字段按 0 / `—` 展示。块只做展示，交互走页面或工具。

## 边界

- 只读：不写会话存储、不发网络请求。
- 数据缺失或字段为空时按 0 处理，不抛错、不猜测。
- 页面/卡片/导航随插件启停；禁用后基座不再出现任何用量展示入口。

## 相关文档

- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [会话与存储](../../../docs/04-sessions-storage.md)
