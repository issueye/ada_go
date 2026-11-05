# Hello TSX（ui-hello-tsx）

前端插件最小示例：一个由 TSX 服务端渲染的页面，外加一个 P1-7 同构岛屿试点。用于验证「清单 + 模板」与「同一份 TSX SSR + hydrate」两条路径。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `ui-hello-tsx` |
| 名称 | Hello TSX |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 纯前端 |
| 能力 | `frontend.script`（仅 `counter` 岛屿需要） |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 页面 | slug `hello`，标题「Hello TSX」，路由 `/ext/ui-hello-tsx/hello`（`hello.tsx`，纯 SSR） |
| 页面 | slug `counter`，标题「Island Counter」，路由 `/ext/ui-hello-tsx/counter`（`counter.tsx`，SSR + hydrate） |
| 资源 | `static/client.js`：`counter.tsx` 的浏览器 bundle |

## 渲染方式

- `hello`：TSX 在 Gateway 渲染 Runtime 里执行成 VNode，再由 Go 校验并转义成 HTML；没有客户端 JavaScript。
- `counter`：同一份 TSX 经 `ada-render.renderToJSON` 输出 VNode，Go 渲染首屏并注入 `data-ada-*` 标记；浏览器 `plugin-islands.js` 加载 `static/client.js`，用同一份 `defineComponent` hydrate。点击只更新本岛。
- 组件只从 `ada:render` 导入；事件只在客户端绑定，服务端 VNode 不携带函数。

## 构建

`static/client.js` 由 `tools/pluginbuild` 生成：

```sh
go run ./tools/pluginbuild -dir plugins/official/ui-hello-tsx -entry counter.tsx -out static/client.js
# CI/本地校验源码与产物同步
go run ./tools/pluginbuild -dir plugins/official/ui-hello-tsx -entry counter.tsx -out static/client.js -check
```

## 用途

- `hello` 作为纯 SSR 前端插件的最小模板。
- `counter` 作为同构岛屿的最小参考：清单 `frontend.client` + `pages[].hydrate`，模板导出 `defineComponent`，客户端 bundle 由 `pluginbuild` 生成。
- 不属于任何产品默认启用的插件集合；需要时在 `products/*.agent.json` 里显式声明。

## 相关文档

- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [渲染引擎](../../../docs/14-render-engine.md)
- [UI 组件](../../../docs/15-ui-components.md)
- [插件岛屿框架开发计划](../../../docs/20-plugin-island-framework-plan.md)
