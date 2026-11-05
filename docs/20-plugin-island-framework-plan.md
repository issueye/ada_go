# 插件 SSR + 客户端渲染引擎框架开发计划

> 上级索引：[ada-go 设计文档](design.md)  
> 评估来源：[14-render-engine.md](14-render-engine.md)、[15-ui-components.md](15-ui-components.md)、[07-ssr-frontend.md](07-ssr-frontend.md)  
> 状态：开发计划（待评审）  
> 版本：0.1 · 2026-11-05  
> 分支：`feat/plugin-islands`（从本计划提交后创建）

## 1. 目标与范围

### 1.1 目标

在**不改变现有插件清单、12 个插槽、`/ext` 页面、动作桥与 VNode 契约**的前提下，为插件 UI 增加一层可选的同构运行时：

- 插件作者写一份 TSX/`render(h)` 组件；
- 服务端用同一份组件做 SSR，输出 VNode → Go 校验/转义 → HTML；
- 浏览器用同一份组件 hydrate，获得组件级局部刷新、状态、事件与动态 VNode 渲染；
- 没有 JS 时仍是完整 SSR 页面，降级行为不变；
- 现有插件模板（`export default function (props)`）继续可用，不强制迁移。

一句话：**做“插件 UI 的同构岛屿框架”，不做整站 SPA，也不做 React/Vue 兼容层。**

### 1.2 明确不做

| 不做 | 原因 |
|---|---|
| 整站 SPA / 客户端路由 | 与 SSR、无 JS 基线、单二进制和安全边界冲突 |
| 直接复用 React/Vue/Svelte 组件 | VNode、DOM 所有权、事件系统、CSP 均不兼容 |
| 服务端字符串模板 | moejs 没有 DOM，`ada-ui` 现有 `template:` 组件不能直接 SSR |
| 第三方插件在主世界执行客户端代码 | 同源脚本可读 DOM/CSRF；第三方只允许服务端驱动或 iframe |
| Suspense / Teleport / Transition / 细粒度 Signal | 非当前目标，先保证可预测的岛屿刷新 |
| 把 Markdown 动态块改成可 hydrate | 块契约是 display-only，交互走工具/页面 |
| 运行时 npm 解析 / 新构建链 | 仓库约束是单二进制、无 npm 依赖 |

### 1.3 成功标准

1. 一个现有官方插件页面（首选 `provider-admin`）用同构组件重写后，行为等价、无 JS 回退可用。
2. 一个现有插槽（首选 `tools-plan` 的 Todo 卡片）可 hydrate，首屏 DOM 不被重建。
3. 服务端与客户端对同一份组件输出的 DOM 逐节点一致；不一致有开发期诊断。
4. 12 个插槽全开时仍在现有渲染预算内（插槽 100ms、页面 300ms）。
5. 第三方插件声明 `hydrate` 时装配失败；CSP 无 `unsafe-eval`、无内联脚本。
6. 客户端 bundle 带内容哈希，源码与产物有 `--check` 一致性门禁。

## 2. 可行性结论（摘要）

现有基础已经具备框架的核心原语：

- 服务端：TSX → esbuild transform → moejs → VNode JSON → Go `ValidateNode`/`RenderHTML`（`internal/gateway/frontend/bridge.go`、`internal/plugins/host/render.go`、`internal/gateway/frontend/render.go`）。
- 客户端：`ada-render.js` 使用同一份 VNode 线格式，支持响应式、keyed diff、mount/patch/hydrate（`internal/gateway/web/static/ada-render.js`）。
- 组件层：`ada-ui.js` 提供浏览器组件；但大量使用字符串模板，**不能直接用于服务端**。
- 插槽/页面：12 个稳定插槽、`/ext/{id}/{slug}`、动作桥、SSE 均已存在。

**关键技术验证**（临时探针，未提交）：把 `ada-render.js` 载入 moejs，用 `renderToJSON` 渲染一个带 `data`、`methods`、`onClick` 的组件定义，约 9.3ms 得到合法 VNode；事件函数被 `cleanProps` 自动丢弃，全程不需要 DOM。结论：**同一份组件定义可以双端运行**，这是本计划成立的前提。

三个硬约束：

1. moejs 没有 DOM，服务端只能用 `render(h)`/TSX，不能执行 `template:`。
2. hydration 是结构匹配，服务端与客户端 DOM 必须一致；当前 `Icon` 的 Go 内联 `<path>` 与 `ada-ui` 的 `<use>` 已经不一致，必须先统一。
3. 客户端插件代码同源执行，必须限定 builtin/verified；第三方不能默认开放。

## 3. 架构

### 3.1 渲染链路

```text
plugin.tsx（单一源码，render(h)/TSX）
  ├─ server: esbuild(TSX→JS) → moejs + ada-render → renderToJSON
  │            → Go ValidateNode → RenderHTML → HTML
  └─ client: esbuild(bundle) → /ext/{id}/static/client.js
               → plugin-islands.js → AdaRender.createApp(...).mount(el, { hydrate: true })
```

### 3.2 服务端运行时

- 在 `host.Renderer` 中增加一个“同构渲染”模式：
  - 虚拟模块 `ada:render` 暴露 `h`、`defineComponent`、`reactive`、`ref`、`computed`、`renderToJSON` 等；
  - 引擎源码 `ada-render.js` 作为宿主内置模块加载一次，按内容哈希缓存编译结果；
  - 每次渲染新建 Runtime，调用 `renderToJSON(export default, props)`；
  - 输出仍走 `frontend.RenderHTML`，Go 是唯一 HTML 转义出口。
- 保留现有“纯函数模板”快速路径：`export default function (props)` 不走引擎，避免所有插件都付 9ms 成本。
- 预算：沿用插槽 100ms / 页面 300ms / 块 300ms；引擎加载时间计入预算，但编译结果缓存。

### 3.3 浏览器运行时

- 新增 `plugin-islands.js`（或扩展 `islands.js`）：
  - 扫描 `[data-ada-plugin-island]`；
  - 按 `data-ada-module` 动态加载插件 bundle；
  - 读取 `data-ada-props`，创建 app，`mount(el, { hydrate: true })`；
  - 单岛失败只记录并保留 SSR DOM，不影响其他岛。
- 每个插件一个 bundle（MVP），同一 bundle 内多个岛共享模块状态；跨插件不共享可变状态。
- 事件在客户端 `h()` 中正常绑定；服务端 `renderToJSON` 已自动剥离函数属性。

### 3.4 组件契约

同构组件必须满足：

| 规则 | 说明 |
|---|---|
| 单根 | 组件/模板只能返回一个根节点；`v-for` 不能作为根 |
| `render(h)` / TSX | 不用 `template:` 字符串；服务端没有 DOM 编译器 |
| 确定性 | 不在 render/data 中读 `window`、`document`、`localStorage`、随机数、当前时间 |
| 显式 ID | 需要 id 的控件必须显式传 `id`，不依赖模块内自增计数器 |
| 只用 class/data-* | 不写 `style`（Go 拒绝，CSP 也拦截内联样式） |
| 数据来自 props | 首屏数据由服务端预取/投影进入；客户端刷新走 `adaAPI`/动作桥 |
| `$C` 需注册 | 宿主组件名在浏览器必须同名注册；未注册直接报错 |

### 3.5 岛屿标记与 props

服务端渲染贡献时，在根 VNode 上注入：

```html
data-ada-plugin-island="tools-plan:chat.corner"
data-ada-plugin="tools-plan"
data-ada-module="/ext/tools-plan/static/client.js?v=<hash>"
data-ada-props="<JSON>"
```

- 优先给根节点注入属性，避免额外包裹层破坏插槽布局；根是 `$C` 时退化为 `display: contents` 容器。
- `data-ada-props` 只放客户端首帧需要的投影：
  - 排除 `app.csrf`（写表单继续用页面已有隐藏字段）；
  - 排除密钥、完整供应商配置等敏感字段；
  - 单属性上限 32KB（VNode `MaxStringBytes`）；超限时降级为 `GET /ext/{id}/island-props?...`，不硬塞。
- 属性值由 Go 侧 `html.EscapeString` 转义，不允许插件自己拼 HTML。

## 4. 兼容策略

### 4.1 保持不变

- `spec/vnode.schema.json` 线格式：`{t,p,c}` / `{$C,n}` / `{#text,v}`。
- 12 个插槽名、`/ext/{id}/{slug}` 路由、动作桥、SSE、CSRF、缓存策略。
- 现有插件模板 `export default function (props)` 和 `ssr:components` 导入。
- Go `ValidateNode` + `RenderHTML` 作为唯一安全出口。

### 4.2 清单扩展（推荐方案）

在 `spec/manifest.schema.json` 的 `frontend` 段增加**可选**字段，旧插件不受影响：

```json
{
  "frontend": {
    "capabilities": ["backend.read", "frontend.script"],
    "client": "client.js",
    "slots": [
      { "slot": "chat.corner", "template": "slot.tsx", "hydrate": true, "order": 10 }
    ],
    "pages": [
      { "slug": "providers", "template": "page.tsx", "hydrate": true, "title": "供应商" }
    ]
  }
}
```

- `frontend.client`：插件浏览器 bundle 的静态资源名，必须在 `assets` 目录中；MVP 每插件一个。
- `slots[].hydrate` / `pages[].hydrate`：该贡献是否 hydrate；缺省 false，完全向后兼容。
- `frontend.script` 能力仍作为加载 JS 的门禁；`hydrate` 是新消费者。P2 再评估是否拆出独立的 `frontend.hydrate` 能力位。
- `adag verify` 增加：字段消费者、client 资源存在、hash 一致、无 `eval`/`new Function`、第三方声明即失败。

**备选（零 schema 方案）**：不新增字段，复用 `frontend.scripts` + 模板输出约定（`data-ada-module`）。适合 P0 验证，但不可审计、容易被误用；仅作为过渡。

### 4.3 信任与第三方

- 仅 `builtin` / `verified` 可声明 `hydrate` 或 `frontend.client`；`third-party` 声明即装配失败（对齐 `x-trust-rule T3`）。
- 当前前端插槽/页面只装配随包官方插件（`cmd/adag/gateway.go`）；本计划不改变这一边界。
- 第三方如果将来需要客户端 UI：
  - 方案 B：服务端驱动 VNode/HTML patch，插件代码不进浏览器；
  - 或 iframe 隔离；不采用 Shadow DOM 作为 JS 隔离。

## 5. 工作分解

### P0 技术验证（0.5–1 人周）

| ID | 任务 | 产物 | 验收 |
|---|---|---|---|
| P0-1 | 服务端同构探针正式化 | `internal/plugins/host` 下 Go 测试：moejs 载入 `ada-render.js`，`renderToJSON` 渲染带状态/事件的组件 | 输出 VNode；事件函数不出现；无 DOM |
| P0-2 | 浏览器 hydrate 探针 | Node `dom-harness`：同一组件 mount + hydrate，断言不重建根节点 | 结构一致；事件可触发 |
| P0-3 | 一致性测试骨架 | `testdata/` 下 SSR golden ↔ 客户端 mount 结构比较 | 差异可定位到标签/属性/文本 |
| P0-4 | 决策门禁 | 记录性能、限制、是否进入 P1 | 评审通过 |

### P1 页面级岛屿（1.5–2 人周）

目标：`/ext/{id}/{slug}` 支持 hydrate；先用一个页面验证。

| ID | 任务 | 产物 |
|---|---|---|
| P1-1 | 清单与加载 | `Frontend.Client`、`SlotContribution.Hydrate`、`PageContribution.Hydrate`；schema/verify/loader |
| P1-2 | 浏览器 bundle 构建 | Go esbuild 工具（`adag plugin build-client` 或 `tools/pluginbuild`）：TSX→IIFE/ESM、external `ada:render`/`ada:ui`、CSP 扫描、内容哈希 |
| P1-3 | 服务端同构渲染模式 | `host.Renderer` 增加 `ada:render` 虚拟模块；`renderToJSON`；编译缓存；预算 |
| P1-4 | 标记与 props 注入 | Bridge 在根 VNode 注入 `data-ada-*`；过滤 CSRF/敏感字段；超限降级 |
| P1-5 | 客户端引导 | `plugin-islands.js`：动态加载、hydrate、单岛错误隔离 |
| P1-6 | 资源门禁 | `/ext` 页面设置 `NeedsUI`；按需加载客户端 bundle，不再全局加载所有插件脚本 |
| P1-7 | 最小试点 | `ui-hello-tsx/counter`：同一份 TSX SSR + hydrate + 客户端 bundle；`provider-admin` 的真实迁移留到 P2-4 |
| P1-8 | 测试 | Go bridge/verify；Node hydrate；无 JS；CSP；动作桥 |

### P2 插槽级 + 状态/组件库（2–3 人周）

| ID | 任务 | 产物 |
|---|---|---|
| P2-1 | 同构组件库 | `ada-ui-iso`（或给 `ada-ui` 增加 `render(h)` 实现）：Button/Input/Select/Card/Tabs/Table/Modal/Icon/TodoList/Notice/Empty；逐组件 parity 测试 |
| P2-2 | 状态管理 | `defineStore`、`provide/inject`、effect scope、`watch`；SSR 状态快照 |
| P2-3 | 插槽岛屿 | 根属性注入/包裹层策略；`chat.corner`、`thread.tray`、`composer.*` 布局回归 |
| P2-4 | 真实页面迁移 | 先迁移只读真实页 `token-usage`（P2-4a）；`provider-admin` 交互页留 P2-4b |
| P2-5 | 服务端 VNode patch | key 信封（`{key,vnode,mode}`）、SSE 增量、客户端 patch 适配器 |
| P2-6 | 开发诊断 | hydrate 不一致警告；SSR/CSR 首帧差异日志；严格模式 |

### P3 加固与发布（1–2 人周）

| ID | 任务 | 产物 |
|---|---|---|
| P3-1 | 安全测试 | T3 拒绝、CSRF 不进入 props、CSP 无 eval、Electron 主世界风险复查 |
| P3-2 | 性能 | 12 插槽全开、页面 300ms/插槽 100ms；bundle 大小与首屏 |
| P3-3 | 文档 | 07/14/15/05、plugin-helper SPEC/manual、decisions；新增本计划索引 |
| P3-4 | 特性开关与回滚 | 产品/插件级开关；关闭后完全回到 SSR 路径 |
| P3-5 | 验收 | 无 JS、键盘/焦点、视觉回归、真实浏览器冒烟 |

## 6. 关键接口草案

### 6.1 插件组件

```tsx
import { defineIsland } from "ada:island";
import { Button, Card } from "ada:ui";

export default defineIsland({
  props: { plan: { default: null } },
  data() {
    return { open: false };
  },
  render(h) {
    return (
      <Card title="任务规划">
        <Button on-click={() => { this.open = !this.open; }}>展开</Button>
        {this.open ? <div class="tp-body">{/* ... */}</div> : null}
      </Card>
    );
  },
});
```

- `defineIsland(def)` 返回一个可调用函数，服务端调用它得到 VNode；浏览器 loader 读取其 `.def` 作为组件定义。
- `on-click`/`onClick` 只在客户端生效；服务端 `renderToJSON` 自动剥离。

### 6.2 服务端标记

```go
// Bridge 渲染贡献后，在根 VNode 上注入（伪代码）：
root.P["data-ada-plugin-island"] = pluginID + ":" + entry
root.P["data-ada-plugin"] = pluginID
root.P["data-ada-module"] = clientURL
root.P["data-ada-props"] = string(publicPropsJSON)
```

### 6.3 客户端 patch 信封

```json
{
  "key": "row:01J...",
  "mode": "upsert",
  "vnode": { "t": "div", "p": { "class": "row" }, "c": [] }
}
```

- 线格式没有 `k`；客户端按 `key` 重新挂 key 后再 `patch`，否则列表重排会重建节点。
- `mode: remove` 只带 key。

### 6.4 状态快照

```json
{
  "island": "tools-plan:chat.corner",
  "state": { "open": false }
}
```

- 初始状态与 SSR 必须一致；客户端先恢复状态再 hydrate。
- 不把 `localStorage` 内容作为 SSR 初始状态；持久化在 `mounted` 之后恢复。

## 7. 构建与门禁

- 构建工具用 Go esbuild（仓库已有依赖），不新增 npm 依赖。
- 产物：`/ext/{id}/static/client.js?v=<sha>`；单文件上限沿用 512KB；超限失败。
- 构建后扫描 `eval(`、`new Function`、内联 `<script>`；命中即失败。
- `adag verify` 新增：
  - `frontend.client` 指向的资源存在且 hash 与源码一致；
  - `hydrate` 贡献必须有 `client`；
  - `third-party` 声明 `hydrate`/`client` 失败；
  - 客户端 bundle 不含 `eval`/`new Function`；
  - 文档字段消费者齐全。
- CI 增加：`go test ./...`、`node --test`、`adag verify`、bundle `--check`。

## 8. 安全边界

| 面 | 处理 |
|---|---|
| 客户端执行 | 仅 builtin/verified；第三方 fail-closed |
| 数据 | props 只放首帧投影；CSRF/密钥不进 `data-ada-props` |
| 加载 | 按岛屿按需加载；不把全部插件脚本注入所有页面 |
| HTML | 仍由 Go 转义；浏览器不接收插件拼的 HTML |
| CSP | 无 `unsafe-eval`、无内联脚本；bundle 构建期扫描 |
| Electron | 主世界同源风险已知；不扩大第三方执行面 |
| 失败 | 单岛失败保留 SSR DOM；不拖垮页面 |

## 9. 测试策略

- **Go**：manifest/loader、标记注入、props 过滤、预算、失败隔离、`adag verify` 新增项。
- **Node**：`ada-render` 同构渲染、hydrate 不重建、keyed patch、store 快照、逐组件 SSR/客户端结构一致。
- **无 JS**：表单、链接、`<details>`、审批/提问托盘仍可用。
- **浏览器**：`tools/render-demo` 冒烟；真实 Electron/Chromium 的 CSP 与焦点回归。
- **安全**：第三方声明拒绝、CSRF 泄漏、eval 扫描、超限降级。

## 10. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| Hydration 不一致 | 节点被替换、闪烁、焦点丢失 | 组件契约 + parity 测试 + 开发期诊断；先页面后插槽 |
| `ada-ui` 模板不能 SSR | 组件库需重写 | 先做最小 iso 组件集；不追求一次覆盖全部 |
| 服务端引擎成本 | 12 插槽超预算 | 缓存编译、按需 hydrate、只对声明贡献启用 |
| bundle 与源码漂移 | 行为不一致 | 内容哈希 + `--check` + CI |
| 第三方执行 | 安全边界扩大 | 明确拒绝；后续走服务端驱动或 iframe |
| 布局被包裹层破坏 | 插槽视觉回归 | 优先根属性注入；wrapper 用 `display: contents` |
| 状态 SSR 不一致 | 首帧被 patch | 状态快照、确定性规则、禁止渲染期读浏览器存储 |
| 范围蔓延 | 做成 SPA/React 兼容层 | 非目标写死；里程碑门禁 |

## 11. 里程碑与提交拆分

建议在新分支 `feat/plugin-islands` 上按以下顺序提交：

1. `docs: 增加插件 SSR + 客户端渲染引擎框架开发计划`
2. `test(host): 增加 ada-render 在 moejs 中的同构渲染探针`
3. `feat(plugin): 增加 frontend.client 与 hydrate 清单字段`
4. `feat(pluginbuild): 增加浏览器 bundle 构建与 CSP 校验`
5. `feat(host): 增加同构渲染模式与 ada:render 虚拟模块`
6. `feat(web): 注入插件岛屿标记与首帧 props`
7. `feat(web): 按岛加载并 hydrate 插件组件`
8. `feat(provider-admin): 用同构组件重写模型设置页`
9. `feat(ui): 增加 render(h) 同构组件库`
10. `feat(state): 增加 defineStore/provide/effect scope`
11. `feat(sse): 增加 keyed VNode patch 适配器`
12. `docs/test: 补齐门禁、迁移说明与回滚开关`

## 12. 开放问题

1. `frontend.client` 每插件一个 bundle，还是按贡献拆分？MVP 建议一个。
2. `hydrate` 用现有 `frontend.script` 能力位，还是拆独立 `frontend.hydrate`？MVP 复用，P2 评估。
3. 同构组件库是新建 `ada-ui-iso`，还是把 `ada-ui` 逐步改成 `render(h)`？建议新建，避免破坏现有岛屿。
4. 插槽包裹层是否可接受？优先根属性注入；无法注入时用 `display: contents` 容器。
5. 服务端 VNode patch 是否进入 MVP？建议 P2，P1 只做首屏 hydrate。
6. 第三方客户端 UI 的长期方案：服务端驱动 patch，还是 iframe？先不开放，留到独立评审。
7. 是否需要在 `products` 增加 `capabilities.pluginIslands` 总开关？建议有，便于灰度/回滚。

## 13. 验收清单

- [ ] 同一份组件源码：SSR HTML 与客户端 hydrate 后 DOM 逐节点一致，根节点不被重建。
- [ ] 无 JS：页面可读、表单可提交、`<details>` 可展开、审批/提问可用。
- [ ] 单岛失败只影响该岛，其余页面正常。
- [ ] 12 个插槽全开仍在 100ms/300ms 预算内。
- [ ] 第三方插件声明 `hydrate`/`client` 被拒绝。
- [ ] CSP 控制台无 `unsafe-eval`/内联脚本告警。
- [ ] bundle 带 hash，源码/产物 `--check` 通过。
- [ ] `provider-admin` 页面与至少一个插槽完成迁移，行为等价。
- [ ] 文档、`adag verify`、Go/Node 测试全部通过。
- [ ] 关闭特性开关后完全回到现有 SSR 路径。

## 14. 当前进展（2026-11-05）

- **P0-1 已完成**：`internal/plugins/host/iso_render_test.go` 证明 `ada-render.js` 可在 moejs 中无 DOM 运行，`renderToJSON` 输出 VNode，事件函数不进入线格式。
- **P0-2 已完成**：`internal/gateway/web/testdata/iso-hydrate.test.js` 覆盖 `renderToJSON → 服务端 HTML → hydrate → 点击更新`；同时发现并修复嵌套组件 hydrate 后首次更新丢失 DOM 引用的 bug（`ada-render.js` 的 `hydrateNode` 首跑不再替换已接管的 `subTree`）。
- **P0-3 已完成**：`iso-parity.js` 归一化比较工具 + `internal/gateway/frontend/testdata/iso/` 共享 fixture；Go `TestIsoParityGolden` 与 Node `iso-parity.test.js` 对同一份 VNode 比较 SSR golden 与客户端 mount 结构。
- **P1-1 已完成**：`frontend.client`、`slots[].hydrate`、`pages[].hydrate` 进入清单 Schema 与 Go 结构；装配期校验 client 路径、`frontend.script` 能力、builtin/verified 信任与静态资源存在性（`newUnit`），旧插件缺省行为不变。测试：`manifest_islands_test.go`、`bridge_client_test.go`。
- **P1-2 已完成**：`tools/pluginbuild` 用 Go esbuild 把 `client.tsx` 打成单文件 ESM；`ada:render`/`ada:ui` 由 shim 从 `globalThis` 取，产物带内容指纹、512KB 上限、CSP 扫描与 `--check`。测试：`tools/pluginbuild/build_test.go`。
- **P1-3 已完成**：`host.Renderer.RenderIsland` 走正式 API：`ada:render` 虚拟模块 + 同一份 `ada-render.js` + `renderToJSON`，复用编译缓存与预算；引擎源由 `web.StaticAsset` 提供，Bridge 装配时注入。测试：`island_render_test.go`、`static_asset_test.go`。
- **P1-4 已完成**：Bridge 对 `hydrate:true` 的插槽/页面在根 VNode 注入 `data-ada-plugin-island` / `data-ada-plugin` / `data-ada-module`（带内容哈希）/ `data-ada-props`；首帧 props 排除 CSRF，超 32KB 或根为 `$C`/文本时降级纯 SSR。非 hydrate 贡献完全保持原路径。测试：`bridge_island_test.go`。
- **P1-5 已完成**：`plugin-islands.js` 扫描岛屿、按 URL 动态 import、读 `data-ada-props`、从页面 `csrf_token` 补 CSRF，并用 `mount(el, {hydrate:true, rootElement:true})` 接管根元素；单岛失败保留 SSR DOM。引擎新增 `rootElement` hydrate 入口。测试：`plugin-islands.test.js`、`iso-hydrate.test.js`。
- **P1-6 已完成**：新增 `web.IslandDetector`，Bridge 按页面实际渲染的插槽/页面判断是否有 hydrate 贡献（`HasIslands`）；`base()` 据此设置 `NeedsUI`，空会话也不会覆盖该结果。没有岛屿的页面不加载 `ada-render`/`ada-ui`/`plugin-islands`；legacy `frontend.scripts` 行为暂不变。测试：`TestIslandDetectorGatesPluginIslandAssets`、`TestHasIslandsForChatSlot`。
- **P1-7 已完成（最小试点）**：`ui-hello-tsx` 新增 `counter` 页面：`counter.tsx` 导出 `defineComponent`，服务端注入岛屿标记，`pluginbuild` 生成 `static/client.js`，浏览器用同一份组件 hydrate；`hello` 保持纯 SSR。端到端测试见 `app/ui_pipeline_test.go` 的 `TestUIExtRoutes`。`provider-admin` 的真实迁移留到 P2-4。
- **P1-8 已完成**：`TestIslandAssetsHonorCSP` 守住 ada-render/ada-ui/plugin-islands 的 CSP；`TestClientBundlesHonorCSP` 守住随包 client bundle；无 JS 首屏由 `TestUIExtRoutes` 覆盖；动作桥/插槽回归沿用既有测试。
- **P1 完成**。
- **P2-1 基础版完成**：新增 `ada-ui-iso.js`（`render(h)`，无字符串模板）首批 Badge/Card/Button/Input/Textarea/TextField/Empty；服务端 `ada:render` 增加 `ada:ui` 虚拟模块，浏览器 `AdaUIIso` + pluginbuild shim 优先使用它；SSR 展开为原生标签，不残留 `$C`。测试：`TestRenderIslandWithIsoComponents`、`TestIsoComponentsRenderThroughGo`、`iso-components.test.js`。
- **P2-1 图标桥接**：`Icon` 在服务端保留 `$C`（Go `renderIcon` 输出内联 SVG），浏览器用 `IconAdapter` 输出同一套 path；`plugin-islands.js` 在 app 级覆盖 `Icon`。`TodoList`/`Notice` 复用该桥。测试：`TestIsoIconTodoNoticeRenderThroughGo`、`iso-components.test.js`。
- **P2-1 表格**：`Table`（别名 `DataTable`）用原生 table/thead/tbody/tr/th/td 输出，支持 columns/rows/rowKey/empty/loading/caption/striped/hover；服务端与浏览器同构。测试：`TestIsoTableRenderThroughGo`、`iso-components.test.js`。
- **P2-1 Select**：`Select` 服务端保留 `$C Select`（Go `renderSelect` 输出 `ada-select` DOM），浏览器 `SelectAdapter` 输出同一结构并复用 `ada-select.js`；`plugin-islands` 在 app 级覆盖 `Select`。同时修复 `renderToJSON` 丢失 `$C` 数组/对象 props 的问题（`cleanComponentProps`）。测试：`TestIsoSelectRenderThroughGo`、`iso-components.test.js`。
- **P2-1 Tabs**：`Tabs` 输出 roving tabindex、`role=tab/tablist/tabpanel`、`hidden` 面板与具名 slot；VNode 白名单补充 `hidden`/`tabindex`（Go + Schema 同步）。测试：`TestIsoTabsRenderThroughGo`、`iso-components.test.js`。剩余 Modal。
- **P2-2 基础版完成**：`provide`/`inject`（组件级 + `app.provide`）、`watch`、`watchEffect` 实例作用域与卸载自动停止、`unmounted` 钩子、`defineStore`（state/getters/actions/单例）与 `$snapshot`/`$hydrate`；`renderToJSON` 保留组件 parent 链，SSR 的 provide/inject 与浏览器一致。**自动快照**：`collectStoreSnapshots` → `data-ada-props.stores` → 客户端 `hydrateStores` 在 store 创建前暂存。测试：`ada-state.test.js`、`TestRenderIslandStateAndProvide`、`TestRenderIslandWithStoreSnapshots`、`TestHydrateSlotCarriesStoreSnapshots`。
- **P2-3 基础版完成**：岛屿标记新增 `data-ada-entry`，`plugin-islands.js` 支持 bundle 的 `islands` 映射（一个 bundle 多 page/slot entry，default 兜底）；插槽标记仍落在根元素上、不额外包裹，`TestHydrateSlotKeepsRootsUnwrapped` 守住布局。`ui-hello-tsx` 增加 `sidebar.top` 插槽岛与 `client.tsx` 多 entry bundle，`TestUIExtRoutes` 端到端覆盖。
- **P2-4a 完成**：`token-usage` 页面迁移为 `defineComponent`，服务端 SSR + 浏览器 hydrate；「显示缓存命中率」是本地状态；`pluginbuild` 生成 `static/client.js`。`TestUIExtRoutes` 覆盖页面标记、bundle 与 NeedsUI。`provider-admin` 等交互页留 P2-4b。
- **P2-5 客户端适配完成**：`keyed(vnode,key)` 与 `applyVNodePatches(items, patches)` 支持 `{key,mode:"upsert"|"remove",vnode}` 信封；`ada-patch.test.js` 验证按 key 复用 DOM、文本就地更新、删除不重建其它行。Gateway SSE 的 VNode 帧接入留 P2-5b。
- **P2-5b 客户端流适配完成**：新增 `ada-patch-stream.js`，订阅 SSE `patch` 帧（`{patches:[...]}`）并驱动 keyed 列表；`source` 可注入，便于测试与自定义 EventSource。测试：`ada-patch-stream.test.js`。服务端具体 route 的帧格式仍由插件自行发送。
- **P2-4b-1 完成**：`provider-admin` 首屏骨架改为同构组件（SSR + hydrate），交互仍由 legacy `provider-admin.js` 在 `DOMContentLoaded` 后接管；`pluginbuild` 生成 `static/client.js`。`TestUIExtRoutes` 覆盖页面标记、legacy 脚本与 island 引导共存。列表/详情/弹窗的组件化留 P2-4b-2。
- **下一步**：P2-4b-2 `provider-admin` 交互组件化，或 P2-1 剩余 Select/Tabs/Modal。
