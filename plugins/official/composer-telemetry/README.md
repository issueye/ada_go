# composer-telemetry — 输入框下方辅助区的 SFC 插槽

把会话页输入框下方的辅助区（**运行中 / 工作区 / 缓存命中 / 上下文占用**）做成一个
插件插槽组件。清单在 `composer.footer` 声明 `Telemetry.vue`：

- **浏览器运行时装载**：组件随插槽由 `/frontend-registry` 登记、`static/ada/`
  的 `loadSFC` 解析编译后挂载；服务端只给注册表 JSON 与静态资源，不产出这段 HTML。
- **没有页面直出回退**：插件停用、组件装载失败或取数失败时，该插槽整块空白——这是
  刻意的选择，不是缺陷。
- **没有构建步骤**：`Telemetry.vue` 与同目录 `Telemetry.js` 直接生效，改完重载插件
  （或重启网关）后刷新页面即可。

## 数据来源

运行状态/上下文来自基座的**通用只读投影**；token/缓存/成本来自插件自己的后端：

```
GET /threads/{id}/state
→ { run: {status, terminal, startedAt, endedAt, errorCode, errorDetail, usage},
    context: {tokens, messages, system, tools, window, percent} }

GET /ext/composer-telemetry/api/usage?threadId=…
→ { thread: {inputTokens, outputTokens, cacheTokens, costMicros},
    workspace: {inputTokens, outputTokens, cacheTokens, costMicros} }
```

用量 route 由 `index.ts` 用 `events.read` 读取 SQLite 每轮原始行后聚合；基座不在
`/threads/{id}/state` 里返回 `usage` 业务投影。组件装载时拉一次，运行中
（`running` / `waiting_input` / `finishing`）每 2s 轮询，进入终态即停。`threadId`
来自 `ada.thread.id`，工作区显示名来自 `inject("bootstrap")` 的
`bootstrap.workspaceName`。

## 口径与基座一致

| 指标 | 口径 |
| --- | --- |
| 缓存命中率 | `cacheTokens / inputTokens`（四舍五入，`input <= 0` 显示 `—`） |
| 上下文占比 | `percent`（窗口未声明时按 1M 估算并在提示里标注） |
| token 文案 | `compactTokenCount`：≥10 取整，否则 1 位小数去尾，K/M 压缩 |
| 上下文计量条 | 文本百分比 + `div` 填充条；宽度按百分比绑定，颜色走 `--ph-*` 令牌 |

视觉优先复用基座 class（`.ada-aux*` / `.ada-context*`），组件自己的 scoped 样式只补
计量条与占位（`static/telemetry.css` 另补占位根不占版面等布局规则）。

## 构建

后端 `index.ts` 由 `node plugins/official/build.mjs` 生成 `dist/index.js`。前端没有
构建步骤：`static/Telemetry.vue` / `static/Telemetry.js` 与 `static/telemetry.css`
由浏览器运行时直接装载，没有需要提交的 bundle。
