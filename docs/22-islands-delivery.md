# 插件岛屿框架交付说明

> 状态：框架实现与自动化门禁已交付；少量发布前手工验收项见 §4。
> 计划与任务分解：[20-plugin-island-framework-plan.md](20-plugin-island-framework-plan.md)。

## 1. 已交付

- **运行时**：`ada-render.js` 单文件、CSP 安全，支持 `h/host/defineComponent/createApp/mount/patch/hydrate/renderToJSON`。
- **同构岛屿**：同一份 TSX/`render(h)` 在 moejs 服务端 `renderToJSON`，浏览器 `hydrate`。
- **清单**：`frontend.client`、`slots[].hydrate`、`pages[].hydrate`；third-party 客户端 bundle 拒绝。
- **构建**：`tools/pluginbuild` 用 Go esbuild 生成 ESM bundle，注入 `ada:render`/`ada:ui` shim，构建期 CSP 扫描与 512KB 限制。
- **组件集**：Badge/Card/Button/Input/Textarea/Table/Select/Tabs/Modal/Icon/TodoList/Notice/Empty。
- **状态**：`provide/inject`、`watch`、effect scope、`defineStore`、store 快照注入。
- **增量更新**：`keyed` + `applyVNodePatches` + `AdaPatchStream`。
- **真实页面**：
  - `token-usage` SSR + hydrate + 客户端状态；
  - `provider-admin` 由 legacy 页迁移为组件页，覆盖新增/编辑/启停/删除/模型拉取与字段编辑；
  - `ui-hello-tsx` 插槽级岛屿试点。
- **SSR 预取**：`backend.read` 动作 + `frontend.actions`，provider 页无 JS 也能看到目录。
- **回滚**：产品级 `capabilities.pluginIslands` 与运维级 `ADAG_DISABLE_ISLANDS`。
- **诊断**：`window.AdaIslandsDiagnostics` 暴露岛屿总数/成功/失败/耗时。

## 2. 使用方式

清单片段：

```json
{
  "frontend": {
    "capabilities": ["frontend.script"],
    "client": "client.js",
    "pages": [
      { "slug": "providers", "title": "供应商", "template": "page.tsx", "hydrate": true }
    ]
  }
}
```

构建：

```sh
go run ./tools/pluginbuild -dir plugins/official/provider-admin -entry client.tsx -out static/client.js
go run ./tools/pluginbuild -dir plugins/official/provider-admin -entry client.tsx -out static/client.js -check
```

关闭客户端岛屿：

```sh
ADAG_DISABLE_ISLANDS=1
```

或在产品中设置 `"pluginIslands": false`。

## 3. 验证指标

| 项 | 结果 |
|---|---|
| `adag verify` | 14/14 |
| Node 前端/桌面测试 | 14/14 |
| 单个岛屿 SSR | 7.5ms（原生）/ 9.6ms（`ada:ui`） |
| 12 插槽 SSR | 45.9ms |
| 真实浏览器 hydration | 11.6ms（token）/ 15.1ms（provider） |
| 随包 client bundle | 2–32KB，≤512KB 上限 |
| provider 页交互测试 | `/state`、`/save`、`/remove`、`/select`、`/fetch-models` 全覆盖 |

## 4. 未完成 / 手工验收

- 读屏、缩放、截图基线视觉回归；
- 真实 Electron 运行时（静态安全配置已收紧，见 `desktop/security.test.js`）；
- CI/多设备/低端机性能；
- 第三方客户端岛屿隔离（v1 明确不开放，见 [21-third-party-islands.md](21-third-party-islands.md)）；
- legacy `page.tsx` / `provider-admin.js` 仍保留作历史参考，未再加载。

## 5. 关键文档

- [20-plugin-island-framework-plan.md](20-plugin-island-framework-plan.md)：计划、进度、验收与性能；
- [21-third-party-islands.md](21-third-party-islands.md)：第三方隔离评估；
- [14-render-engine.md](14-render-engine.md)：VNode 运行时；
- [15-ui-components.md](15-ui-components.md)：组件层；
- [07-ssr-frontend.md](07-ssr-frontend.md)：插件前端契约。
