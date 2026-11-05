# 第三方插件岛屿隔离方案评估

> 状态：评估完成，v1 决策为「不开放第三方客户端 bundle」。
> 关联：[20-plugin-island-framework-plan.md](20-plugin-island-framework-plan.md)（P3-4）、
> [07-ssr-frontend.md](07-ssr-frontend.md)、[09-security-operations.md](09-security-operations.md)。

## 1. 背景

内置/已验证插件可以在浏览器里运行 `frontend.client` bundle，并 hydrate 同构组件。
第三方插件当前被清单校验拒绝：

- `trust=third-party` + `frontend.client` → 装配期报错；
- 只有 `builtin` / `verified`（带 sig）允许客户端 bundle；
- 所有随包 bundle 仍要过 CSP 扫描（无 `eval` / `new Function`）。

问题：第三方插件如果需要交互式 UI，是否要开一条隔离通道？

## 2. 可选路径

### A. 服务端驱动 + 传统脚本（当前可用）

第三方插件可以：

- 用 `backend.read` 只读动作 + `frontend.actions` 做 SSR 预取；
- 用 `frontend.script` 加载自己的普通脚本，操作服务端渲染的 DOM；
- 用插件 `route` 提供 JSON API，脚本通过 `window.adaAPI` 调用。

限制：

- 没有 VNode/组件运行时；
- 交互需要自己维护 DOM，容易和 SSR 漂移；
- 仍受 CSP、CSRF、props 上限与信任矩阵约束。

这是 v1 对第三方推荐的路径：**只读 SSR + 传统脚本**，不执行第三方组件代码。

### B. 签名/verified bundle（当前门禁）

给第三方做代码签名，验签后按 `verified` 处理，允许 `frontend.client`。

- 优点：复用同构岛屿，体验与官方插件一致；
- 缺点：签名基础设施、密钥管理、撤销列表都未建设；
- 结论：v1 不开放，避免把「信任」简化成一次签名检查。

### C. iframe 沙箱（未来选项）

把第三方交互 UI 放进 `sandbox` iframe：

```html
<iframe sandbox="allow-scripts" src="/ext/{id}/isolated?..." />
```

约定：

- iframe 使用独立 origin（或至少 `sandbox` 无 `allow-same-origin`）；
- 宿主只通过 `postMessage` 传只读数据与用户动作；
- 消息必须有 `type`、`version`、`requestId` 与 schema 校验；
- 不把 CSRF token、cookie、`window.adaAPI` 暴露给 iframe；
- iframe 内不加载宿主 `ada-render`，避免共享全局；
- 高度/超时/消息大小限额；键盘焦点进出可预期。

代价：

- 需要新的宿主页面、消息协议、CSP 与 origin 配置；
- 主题/字体/令牌需要跨 iframe 同步；
- 可访问性与键盘焦点更复杂；
- 不是当前需求的最小实现。

## 3. 决策

**v1 保持第三方客户端 bundle 拒绝。**

- 第三方交互 UI 走 A：服务端 SSR + `frontend.script` + 插件 route；
- `backend.read` 只读预取可让第三方页面在无 JS 时也有内容；
- iframe 方案记录为 C，等有真实第三方交互需求再立项；
- 不因为「第三方也能写组件」而放宽 T3 信任矩阵。

## 4. 后续验收（若启动 iframe）

- origin/sandbox 属性有自动化断言；
- 消息协议有 schema 与越界/超限测试；
- iframe 页面不能读宿主 cookie/CSRF；
- 键盘焦点可进出，读屏可识别标题与区域；
- 失败时宿主页面保留可用的 SSR 降级。

## 5. 现状证据

- `TestManifestIslandValidation`：third-party + client 拒绝；verified + sig 允许；
- `TestClientBundlesHonorCSP`：随包 bundle 无 eval/new Function；
- `TestHydrateSlotInjectsIslandMarkers`：CSRF 不进入 `data-ada-props`；
- `TestUIExtRoutes`：provider 页 SSR 预取在无 JS 时输出供应商目录。
