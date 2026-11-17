# 前端（SPA）

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§7 前端架构  
> 版本：0.3 · 2026-11-17

> **架构状态（2026-11-11 起）**：前端是**单页面应用（SPA）**。Go 只渲染 shell
> （`#app` 容器 + bootstrap JSON + 登录/错误页），页面由自研运行时
> `internal/gateway/web/static/ada/` 在浏览器装载与渲染；插件前端是运行时装载的
> SFC 单文件组件（零构建）。服务端不产出业务页面 HTML，也没有水合/渐进增强回退。
> 运行时、组件与 UED 细节见 [14-ui-runtime.md](14-ui-runtime.md)；模型内容与
> Markdown 动态块见 [15-extensions.md](15-extensions.md)。

## 7. 前端架构（SPA）

### 7.1 技术选型

| 关注点 | 选择 | 理由 |
|---|---|---|
| 外壳 | Go `html/template` 渲染 `shell` / `login` / `error` 三个模板 | 登录与错误页是无 JS 安全页；shell 只带 bootstrap 数据，不渲染业务 DOM |
| 运行时 | 自研 ESM：`reactivity` / `compiler` / `runtime` / `router` / `sfc` / `host` | 无 npm 构建链、无 `eval`，与 CSP `script-src 'self'` 完全兼容 |
| 组件 | SFC（`<template>` + `<style scoped>` + `<script src>`） | 插件作者获得 Vue 式体验，逻辑仍是安装时落盘的静态 ESM |
| 样式 | a_da 预编译 CSS + `--ph-*` / `--ada-*` 令牌；SFC scoped CSS 走 CSSOM | 不引入 Tailwind 构建；不写内联 style |
| 数据 | JSON 端点 + SSE | 页面数据与结构分离，增量更新不走 HTML 片段 |
| 静态资源 | `go:embed` + 内容指纹 URL | 单二进制、可缓存、可校验 |

明确不做：服务端页面渲染、水合、`v-html`、内联 `<script>`、`new Function`/`eval`、
客户端 `style="..."` 字符串、用客户端框架替换自研运行时。

## 7.2 外壳与路由

`GET` 任意页面路径（`/`、`/threads`、`/threads/{id}`、`/extensions`、
`/ext/{id}/{slug}` …）都由 history fallback 返回同一份
[shell.html.tmpl](../internal/gateway/web/templates/shell.html.tmpl)：

- `#app` 是空容器；`<script type="application/json" id="ada-bootstrap">` 带品牌、
  工作区、路由、CSRF 与版本投影；
- `<script type="module" src="/static/ada/index.mjs">` 启动自研运行时；
- 登录页 `login.html.tmpl` 与错误页 `error.html.tmpl` 由 Go 模板直出（无 JS 安全页），
  二者无插件、无框架依赖；
- 其余页面路径只返回 JSON（`/partials/*.json`、`/threads/{id}/state`、
  `/markdown.ast`、`/frontend-registry`），不存在页面 HTML 端点。

客户端路由（[router.mjs](../internal/gateway/web/static/ada/router.mjs)，约 200 行）：

| 路由 | 页面 | 数据来源 |
|---|---|---|
| `/` | 概览（统计卡、最近会话、`dashboard.widgets` 插件卡） | `GET /partials/dashboard.json`（5s 轮询） |
| `/threads` | 会话列表 / 新建任务 | `GET /partials/threads.json` |
| `/threads/{id}` | 会话页（transcript、流式、交互托盘、输入区） | `/partials/transcript/{id}`、`/threads/{id}/state`、SSE |
| `/extensions`、`/extensions/{id}` | 插件列表 / 详情（启停、打开插件页） | `GET /partials/extensions.json` |
| `/workspaces` | 工作区选择 | `GET /api/workspaces`、`GET /api/fs/list` |
| `/ext/{id}`、`/ext/{id}/{slug}` | 插件页面（SFC） | `/frontend-registry` + 插件自身 route/动作 |

路由表 = 基座静态路由 + 注册表里的插件页面；`beforeLeave` 守卫负责未保存草稿提示，
滚动位置由运行时恢复。路径变更只触发组件 re-render，不整页刷新。

## 7.3 运行时模块

| 模块 | 职责 |
|---|---|
| `reactivity.mjs` | `ref` / `reactive` / `computed` / `effect` / `effectScope` / `watch` / `nextTick`；微任务批量 flush，`effectScope` 是插件卸载的一刀切 |
| `compiler.mjs` | 模板 tokenize/parse → 渲染计划（闭包树），表达式解释器禁 `constructor` / `__proto__` / `prototype`，全局白名单；**不生成代码字符串**，无 `eval` |
| `runtime.mjs` | `createVNode` / `h` / `defineComponent` / `provide` / `inject` / 生命周期 / `createApp` / `defineAsyncComponent`；按属性与 URL 白名单打补丁 |
| `router.mjs` | history API 路由、参数匹配、`beforeLeave`、滚动恢复 |
| `sfc.mjs` | `parseSFC` / `loadSFC` / `compileScopedCss`；SFC 缓存键 = URL + 内容指纹；拒绝内联 `<script>` |
| `host.mjs` | bootstrap / fetch（CSRF）/ 注册表 store / `SlotOutlet` / `PluginScope` / `makeAda` 服务面 |
| `index.mjs` | shell 布局（顶栏 + 侧栏 + 页面容器）、设置弹窗、图片查看器、页面装载 |
| `whitelist.mjs` | 客户端白名单词汇表（与 `spec/vnode.schema.json` 同步，`adag verify` 比对） |

渲染完全由组件状态驱动：状态 → 响应式 → 组件 re-render → VNode diff patch。
模型生成内容不走 SFC 编译器：服务端 `POST /markdown.ast` 返回 VNode JSON，
浏览器只做 display profile 的白名单渲染；Markdown `adag-ui` 动态块由服务端
渲染成 VNode JSON（见 §7.4）。

## 7.4 数据、SSE 与增量

- 持久化片段是 JSON（`/partials/transcript/{id}` 等）；浏览器拿数据自己渲染，
  **没有 HTML 片段替换**。
- 会话实时流：`GET /events/{id}`（SSE）。事件名直接沿用 Core 事件：
  `evt.message.delta`（文本增量）、`evt.item.updated`（落库行）、`evt.run.status`（轮次状态）、
  `evt.interaction.requested` / `evt.interaction.resolved`、`evt.error`、`evt.notice`；
  序号断裂时网关发 `resync`，客户端重拉 transcript/state 做全量对齐。
- 工作区级事件：`GET /events/workspace`（`evt.plugin.changed` / `evt.ready`），
  注册表代变更时浏览器重拉 `/frontend-registry`，插槽/页面/导航即时增减。
- 合并与重连：40ms 窗口合并增量；心跳每 15s；浏览器带 `Last-Event-ID`，连续则补发、
  断裂则发 `resync`；每个线程一个 hub，单线程最多 64 个客户端。
- 运行状态与上下文占用：`GET /threads/{id}/state`，插件在运行中轮询（基座不注入业务用量）。
- Markdown：`POST /markdown.ast`（服务端 goldmark + bluemonday → VNode JSON）；
  `adag-ui`  fence 由网关编译插件 TSX（esbuild + moejs，模块名 `adag:components`）
  → VNode JSON → 客户端 `ui-markdown` 渲染；失败块降级，不影响其余正文。

## 7.5 插件前端

清单的 `frontend` 段（字段与校验见 [spec/manifest.schema.json](../spec/manifest.schema.json)）：

```jsonc
{
  "frontend": {
    "capabilities": ["frontend.script", "backend.read"],
    "slots":     [{ "slot": "chat.corner", "component": "PlanCard.vue", "order": 90 }],
    "pages":     [{ "slug": "plan", "component": "PlanPage.vue", "title": "计划面板" }],
    "nav":       [{ "slug": "plan", "label": "计划", "icon": "i-list" }],
    "dashboard": [{ "component": "Dashboard.vue", "order": 10 }],
    "blocks":    [{ "name": "chart", "schema": "…", "template": "chart.tsx" }],
    "actions":   { "plan": { "action": "plan", "read": true } },
    "styles":    ["plan.css"],
    "assets":    "static"
  }
}
```

- `slots` / `pages` / `dashboard` 的 `component` 必须是 `assets` 目录内的 `.vue`；
  装载失败（404、编译错误、setup 抛错）由错误边界捕获，贡献位置标
  `data-ada-error` 并留空，不影响页面其余部分。
- SFC 的 `<script>` **必须**是 `<script src="./X.js">`（原生 ESM）；内联 script 在
  `parseSFC` 阶段报错。逻辑 ESM 从 `/static/ada/ada-runtime.mjs` 导入运行时 API。
- 每个贡献一个独立 `effectScope`：插件停用 / 路由离开 = `scope.stop()` + 卸载 +
  样式表摘除；生产上不需要重启网关即可随内容指纹更新。
- 7 个会话内插槽（`chat.corner`、`composer.*`、`thread.tray`、`transcript.after`）
  只在有 `threadId` 时渲染（路由级条件），与旧插槽语义一致。
- 12 个插槽名是冻结契约（`internal/common/pluginapi/contract.go`），`adag verify slots`
  守护词汇表；插件 SFC 的下拉直接用原生 `<select>` + `ada-input` 类即可（运行时没有
  SFC 版 Select 组件）；`adag:components` 只服务 Markdown 动态块，且当前只实现
  `Icon` / `TodoList` / `Markdown` 三个展示组件。
- `frontend.script` 能力 = 「本插件有浏览器端可执行面」；third-party 声明即装配失败。
  `backend.read` 只影响「动作绑定能否标为只读」，不再有服务端预取。

## 7.6 动作桥、route 与 SSE

```
浏览器 GET  /ext/{id}/action/{binding}   # read:true，直接返回 JSON
浏览器 POST /ext/{id}/action/{binding}   # write，CSRF + 用户点击确认
  → Gateway 校验会话 / CSRF / Origin / 插件状态
  → Core action.invoke { plugin, action, input, threadId }
  → 插件 execute(input, ctx) → JSON
```

- 动作不是工具：不进模型上下文、不受执行前审批约束，但写动作必须用户确认，且受
  scope / 宿主限制。
- 插件自定义数据接口：`GET/POST /ext/{id}/api/{path}` → Core `plugin.http` →
  插件登记的路由处理器；只接受 JSON，单响应 ≤64KB，路由按插件隔离。
- 插件 SSE：route 声明 `sse: true`（只允许 GET）；`/ext/{id}/api/{path}` 升级为
  `text/event-stream`，帧 ≤64KB，空闲 90s、总时长 30min，`done` / `error` 是保留事件名。
- 浏览器侧统一服务面（`host.mjs` 的 `makeAda`）：`ada.api(path, opts)`（自动 CSRF）、
  `ada.sse(path, handlers)`、`ada.action(name, input)`（写动作自动走确认对话框）、
  `ada.thread`、`ada.storage`（按插件前缀隔离）。
- 数据获取在组件 `setup()` 里主动调用动作/route；首屏不依赖服务端预取。

## 7.7 UI 与设计元语：复用 a_da

ada-go 直接使用 a_da 的 UI 与设计元语：同一套 `--ph-*` token、`ada-*` 组件样式、
DOM/class 约定、12 个插槽位置与交互规则；只换渲染现场，不产生第二套视觉语言。

| a_da 资产 | ada-go 落点 | 复用方式 |
|---|---|---|
| `design/tokens.css` | `static/tokens.css` | 原样复制；`--ph-*` 颜色的唯一真源 |
| `app.css` | `static/app.css` | 预编译产物；不运行 Tailwind，只固定并校验 |
| `components/layout/widgets/shell/pages.css` | `static/ui/` | 原样复制；引用顺序 components → layout → widgets → shell → pages |
| 主题扩展 | `static/ui/themes.css` + `themes.js` | 明暗 × 色板两维正交；默认 `azure` 冷灰纸面 × 蓝 `#1D4ED8`，暗色冷夜反转 |
| 图标 | `static/ada/icons.css` + `<ui-icon>` | 同一组线性 SVG（stroke 1.75）与尺寸档位 |
| 12 插槽 | `internal/common/pluginapi/contract.go` | 冻结契约，宿主与插件共用 |
| 控制台外观 | `static/ada/console.css` | 2026-11-16 设计稿的 `c-*` 方言：48px 顶栏、288px 侧栏、800px 会话列、设置弹窗 |

纪律：类名统一 `ada-` 前缀；颜色只引用 `--ph-*`，尺寸只引用 `--ada-*`；不写内联
`style`；主题/对比度/焦点/滚动条细节见 [14 §19](14-ui-runtime.md)。SFC 的
`<style scoped>` 由运行时编译并注入 CSSOM（`adoptedStyleSheets`），不受
`style-src 'self'` 影响。

## 7.8 无 JS 与可访问性

- 没有 JS：只有登录页与错误页可用；SPA 页面是空容器 + bootstrap，**不提供无 JS
  回退**（刻意的取舍）。登录/错误页的表单与链接必须是原生可用的。
- 语义结构：`<main>`、`<nav>`、`<form>`、`<button>`；消息流 `aria-live="polite"`，
  流式期间不逐 token 播报。
- 键盘：Tab 顺序自然；弹层打开时焦点进入弹层、Esc 关闭并回归触发元素；确认对话
  框默认聚焦安全选项。
- `prefers-reduced-motion` 下关闭过渡、自动滚动与呼吸动画；错误提示写「发生了什么
  + 下一步」，不显示堆栈。

## 7.9 消息发送路径（调度层）

页面不自己拼 `thread.create` + `turn.start`：新建会话、发消息、编辑重发都调用
网关调度层 `Dispatch.Send(ctx, sender, req)`（`internal/gateway/dispatch`）：

1. 解析工作区（空 = 默认；显式路径必须已在工作区列表里）；
2. 按工作区取 Core 客户端，请求结束即释放；
3. `threadId` 为空时 `thread.create`；
4. 读一次 `turn.get`，按投递模式决定动作：
   - `auto`（界面默认）：空闲 `turn.start`，运行中 `turn.steer`；steer 撞上「轮次
     刚好结束」时退回 `turn.start`；
   - `start`（定时任务等）：运行中直接返回 `thread_busy`，不偷偷排队；
   - `steer`：空闲返回 `not_running`；
5. `waiting_input` / `finishing` 不打断，也不排队，直接给出原因。

边界：`sender` 由宿主盖章（界面固定 `user:web`，插件按清单 id 填 `pluginId`），
只用于审计、限流与幂等命名空间，**不能作为授权绕过**；幂等键由调用方给（表单每次
提交生成一个，定时任务用「任务 + 时刻」推导），调度层不猜「文本 + 线程」。

---

**相邻文档**：[← 插件运行时与治理](06-plugin-runtime.md) · [索引](design.md) · [协议契约 →](08-protocol.md)
