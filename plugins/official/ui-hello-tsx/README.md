# Hello TSX（ui-hello-tsx）

前端插件最小示例：纯 SSR 页面、同构页面岛屿与插槽岛屿。用于验证「清单 + 模板」「SSR + hydrate」「一个 bundle 多 entry」三条路径。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `ui-hello-tsx` |
| 名称 | Hello TSX |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 纯前端 |
| 能力 | `frontend.script`（岛屿需要） |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 页面 | slug `hello`，标题「Hello TSX」，路由 `/ext/ui-hello-tsx/hello`（`hello.tsx`，纯 SSR） |
| 页面 | slug `counter`，标题「Island Counter」，路由 `/ext/ui-hello-tsx/counter`（`counter.tsx`，SSR + hydrate） |
| 插槽 | `sidebar.top`（`slot.tsx`，SSR + hydrate；所有页面可见） |
| 资源 | `static/client.js`：`client.tsx` 的浏览器 bundle，导出 `islands` 映射（page/slot entry） |

## 渲染方式

- `hello`：TSX 在 Gateway 渲染 Runtime 里执行成 VNode，再由 Go 校验并转义成 HTML；没有页面级客户端 JavaScript。
- `counter`：同一份 TSX 经 `ada-render.renderToJSON` 输出 VNode，Go 渲染首屏并注入 `data-ada-*` 标记；浏览器 `plugin-islands.js` 加载 `static/client.js`，用同一份 `defineComponent` hydrate。点击只更新本岛。
- `sidebar.top`：插槽级岛屿；服务端注入 `data-ada-entry="slot:sidebar.top"`，bundle 的 `islands` 映射选择 `Slot` 组件；布局标记直接落在根元素上，没有额外包裹层。
- 组件从 `ada:render` / `ada:ui` 导入；事件只在客户端绑定，服务端 VNode 不携带函数。

## 构建

`static/client.js` 由 `tools/pluginbuild` 从 `client.tsx` 生成（一个 bundle 同时包含 page 与 slot entry）：

```sh
go run ./tools/pluginbuild -dir plugins/official/ui-hello-tsx -entry client.tsx -out static/client.js
# CI/本地校验源码与产物同步
go run ./tools/pluginbuild -dir plugins/official/ui-hello-tsx -entry client.tsx -out static/client.js -check
```

## 用途

- `hello` 作为纯 SSR 前端插件的最小模板。
- `counter` 作为同构岛屿的最小参考：清单 `frontend.client` + `pages[].hydrate`，模板导出 `defineComponent`，客户端 bundle 由 `pluginbuild` 生成。
- 不属于任何产品默认启用的插件集合；需要时在 `products/*.agent.json` 里显式声明。

## 相关文档

- [SSR 前端](../../../docs/07-ssr-frontend.md)
- [渲染引擎](../../../docs/14-ui-runtime.md)
- [UI 组件](../../../docs/14-ui-runtime.md)
- [插件岛屿框架开发计划](../../../docs/07-ssr-frontend.md)
