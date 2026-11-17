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
                                           ├─ registerAction("usage") → GET /ext/token-usage/action/page_usage（read:true）
                                           └─ GET /ext/token-usage/api/usage?threadId=…（组件装载后经 ada.api 取数）
```

- 基座只持久化每轮 `provider/model/inputTokens/outputTokens/cacheTokens/costMicros/threadId`。
- `index.ts` 按线程/工作区汇总，并保留 `groups`（provider+model 分组）；单组时才回填顶层 `provider/model`。
- 页面与概览卡片在装载后自己调 route 取数；没有服务端预取，也没有服务端注入的页面数据。

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 导航 / 页面 | 导航「Token 用量」→ 页面 `/ext/token-usage/usage`（`static/UsagePage.vue`，SFC 运行时装载） |
| 仪表盘 | 概览页卡片 `usage`（`static/UsageWidget.vue`，order 10） |
| Markdown 动态块 | `usage-card`（`blocks/usage-card.tsx` + `schemas/usage-card.json`） |
| 样式 | `usage.css`（`static/`，清单 `styles` 声明） |
| 数据 | 页面/卡片装载后经 `ada.api("/usage")` 取数；没有服务端预取 |

页面与概览卡片都是运行时装载的 SFC，由浏览器渲染（服务端不产出页面 HTML）：

- 头部 `ⓘ` tooltip 承载口径说明；操作是「复制汇总」「导出 CSV」（都在浏览器本地生成，不发请求）；一次性反馈走底部 toast。
- 「工作区 / 当前会话」分段切换四张 KPI 卡（累计 Token / 输入 / 输出 / 缓存命中率）；缓存命中率 = `cacheTokens / inputTokens`。
- 「用量构成」用三条计量条展示输入、输出、缓存占输入的比例；计量条宽度用 10% 一档的类名表达（不写内联 style）。
- 「模型与成本」显示 `costMicros` 与 `groups`；多组时标注为多模型合并，不编造拆分。

## 构建

后端 `index.ts` 由 `node plugins/official/build.mjs` 预编译到 `dist/index.js`。前端没有
构建步骤：`static/UsagePage.vue` / `static/UsageWidget.vue` 与逻辑 ESM 由浏览器运行时
直接装载，改完重载插件（或重启网关）后刷新页面即可。

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

- [前端（SPA）](../../../docs/07-frontend.md)
- [会话与存储](../../../docs/04-sessions-storage.md)
