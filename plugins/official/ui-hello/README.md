# Hello SFC（ui-hello）

前端插件最小示例：一个插槽卡片 + 一个独立页面，全部是运行时装载的 SFC 单文件组件。用于演示「清单 + SFC」「零构建、浏览器运行时编译」「插槽与页面共用一套装载路径」三条契约。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `ui-hello` |
| 名称 | Hello SFC |
| 版本 | 0.2.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 纯前端 |
| 能力 | `frontend.script`（SFC 逻辑 ESM 的前端可执行面） |

## 前端贡献

| 类型 | 内容 |
| --- | --- |
| 页面 | slug `hello`，标题「Hello SFC」，路由 `/ext/ui-hello/hello`（`static/HelloPage.vue`） |
| 插槽 | `sidebar.top`（`static/Hello.vue`，order 90；所有页面可见） |
| 导航 | 侧栏「Hello」→ `/ext/ui-hello/hello`（图标 `i-sparkles`，order 90） |
| 资源 | `static/`：两组 `.vue` + 同名逻辑 ESM（`Hello.js` / `HelloPage.js`） |

## 渲染方式

SFC 由浏览器运行时装载，页面只由浏览器运行时渲染：

- `Hello.vue` / `HelloPage.vue` 各有一个 `<template>`、一个 `<style scoped>` 和一个
  `<script src="./*.js">`；逻辑是同目录的 ESM 静态文件，没有内联 script、没有 bundle。
- `Hello.js` 用 `ref` 维护点击计数，`HelloPage.js` 在 `onMounted` 时记录装载时刻；
  两者都从 `/static/ada/ada-runtime.mjs` 导入运行时 API。
- 浏览器拉取 `/frontend-registry` 拿到带内容指纹的组件 URL，`static/ada/` 的
  `loadSFC` 负责拉取、解析、编译模板、注入 scoped 样式（CSSOM）并挂载。
- `HelloPage.vue` 的 `<ui-panel>` 是宿主 UI 组件；插槽装载失败只影响该贡献，
  该位置会留 `data-ada-error` 占位。

## 构建

没有前端构建步骤：改 `static/*.vue` 或 `static/*.js` 后重载插件（或重启网关），
刷新页面即生效；如果改的是 `plugin.json`，同样在插件页点「重载」。`adag verify`
负责校验清单字段与资源存在。

## 用途

- 作为纯前端 SFC 插件的最小模板（清单 `frontend` 段 + `assets` 目录）。
- 作为「插槽 + 页面 + 导航」共用一套 SFC 装载路径的最小参考。
- 不属于任何产品默认启用的插件集合；需要时在 `products/*.agent.json` 里显式声明。

## 相关文档

- [前端（SPA）](../../../docs/07-frontend.md)
- [渲染引擎与 UI 组件](../../../docs/14-ui-runtime.md)
