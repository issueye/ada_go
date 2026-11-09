# composer-telemetry — 输入框下方辅助区的纯客户端岛屿

把会话页输入框下方的辅助区（**运行中 / 工作区 / 缓存命中 / 上下文占用环**）从基座
SSR 搬进一个插件岛。清单里 `composer.footer` 声明了 `clientOnly: true`：

- **服务端不渲染模板**：桥只输出一个 `<div class="ada-island ada-island--client">` 空壳，
  带 `data-ada-plugin-island` / `data-ada-entry` / `data-ada-module` / `data-ada-props` /
  `data-ada-client-only="1"`；
- **浏览器纯 mount**：`plugin-islands.js` 读到 `data-ada-client-only=1` 就用普通
  `app.mount()`（不是 hydrate），空壳内部挂上插件的 `.ada-aux`；
- **没有 SSR 回退**：插件停用、`ADAG_DISABLE_ISLANDS=1`、或 bundle 加载失败时，
  辅助区整块空白 —— 这是刻意的选择，不是缺陷。

## 数据来源

运行状态/上下文来自基座的**通用只读投影**；token/缓存/成本来自插件自己的后端：

```
GET /threads/{id}/state
→ { run: {status, terminal, startedAt, endedAt, errorCode, errorDetail},
    context: {tokens, messages, system, tools, window, percent} }

GET /ext/composer-telemetry/api/usage?threadId=…
→ { thread: {inputTokens, outputTokens, cacheTokens, costMicros},
    workspace: {inputTokens, outputTokens, cacheTokens, costMicros} }
```

用量 route 由 `index.ts` 用 `events.read` 读取 SQLite 每轮原始行后聚合；基座不再在
`/threads/{id}/state` 里返回 `usage`。挂载时拉一次，运行中（`running` / `waiting_input`）
每 2s 轮询，进入终态即停（终态后基座会整页 reload，那时再取新值）。`threadId` 来自
`props.data.thread.id`，工作区显示名来自 `props.app.workspaceName`。

## 口径与基座一致

| 指标 | 口径 |
| --- | --- |
| 缓存命中率 | `cacheTokens / inputTokens`（四舍五入，`input <= 0` 显示 `—`） |
| 上下文占比 | `percent`（窗口未声明时按 1M 估算并在提示里标注） |
| token 文案 | `compactTokenCount`：≥10 取整，否则 1 位小数去尾，K/M 压缩 |
| 环与横条 | 半径 15、周长 2πr；dash 值走 SVG 属性（CSP 禁内联 style） |

视觉全部复用基座 class（`.ada-aux*` / `.ada-token-ring*` / `.ada-context*`），
本插件只补两条布局规则（见 `static/telemetry.css`）。

## 构建

`static/client.js` 是 `client.tsx` 的构建产物，必须提交：

```sh
go run ./tools/pluginbuild -dir plugins/official/composer-telemetry -entry client.tsx -out static/client.js
go run ./tools/pluginbuild -dir plugins/official/composer-telemetry -entry client.tsx -out static/client.js -check
```

`app/client_bundles_test.go` 会在 `go test ./...` 时校验两者同步。

## 测试

```sh
node --test plugins/official/composer-telemetry/static/telemetry.test.js
```

三个用例：运行中（渲染 + 轮询 + 卸载停表）、终态（一次取数即静止）、取数失败
（保持占位、不重复注册定时器）。
