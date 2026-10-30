# Hello TSX（ui-hello-tsx）

M4 前端插件的最小示例：一个由 TSX 服务端渲染的页面。用于验证「前端插件只有清单 + 模板」这条装配路径。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `ui-hello-tsx` |
| 名称 | Hello TSX |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 纯前端 |
| 能力 | 无 |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 页面 | slug `hello`，标题「Hello TSX」，路由 `/ext/ui-hello-tsx/hello`（`hello.tsx`） |

## 渲染方式

- TSX 在 Gateway 的渲染 Runtime 里执行成 VNode，再由 Go 渲染器校验并转义成 HTML。
- 没有客户端 JavaScript、没有 hydration。
- 组件只从 `ssr:components` 导入；数据只来自 props（`app.product`、`app.route`、`plugin`），没有副作用。
- 页面展示产品名、插件 id/名称/版本与当前路由，用于确认投影数据是否正确。

## 用途

- 作为新前端插件的最小模板：复制目录、改 `plugin.json` 的 id/名称/页面 slug，再在产品的 `plugins` 列表中声明即可。
- 不属于任何产品默认启用的插件集合；需要时在 `products/*.agent.json` 里显式声明。

## 构建

不需要构建：`hello.tsx` 在渲染时由宿主编译；本插件没有 `index.ts`、`dist/` 或 `static/`。

## 相关文档

- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [渲染引擎](../../../docs/14-render-engine.md)
- [UI 组件](../../../docs/15-ui-components.md)
