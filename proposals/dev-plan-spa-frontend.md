# 开发计划：SPA 前端重构 —— 移除 SSR · 自研框架 · 插件 SFC

- 日期：2026-11-11
- 分支：`design/spa-plugin-framework`
- 依据：`proposals/spa-frontend-framework.md`（已评审方向：单页面、无 SSR、框架自研、CSP 不变、插件前端 = 运行时装载 SFC）
- 本文档是**执行计划**：文件级任务分解 + 每阶段验收标准。执行顺序 R → F → S → C → M → V。

## 进度（2026-11-11）

| 阶段 | 状态 | 证据 |
|---|---|---|
| R 移除 SSR | ✅ 完成 | 139 文件 +1354/−31677 行；render.go/projections.go/pluginbuild/11 个静态 JS/8 个页面模板全删；moejs Render Runtime 收缩为 block 专用 |
| F 框架核心 | ✅ 完成 | `static/ada/`：reactivity/compiler/runtime/router/sfc/whitelist + ada-runtime 门面；node --test 83/83 |
| S shell SPA | ✅ 完成 | index.mjs/host.mjs/vnodejson.mjs/shell.css；/frontend-registry + /events/workspace 端点；Go 测试覆盖 |
| C 金丝雀 | ✅ ui-hello 完成（sidebar.top 插槽 + hello 页面）；C2/C3 由 M 覆盖 | plugins/official/ui-hello/{plugin.json,static/Hello.vue,Hello.js,HelloPage.vue,HelloPage.js} |
| M 迁移 | ✅ 完成：tools-plan / tools-ask / composer-telemetry / provider-admin / env-manager / mcp-client / cron-manager / token-usage 全部迁到 SFC；ui-charts 纯 blocks 无需迁移；新增门禁 test/plugins-sfc.test.mjs | 迁移后框架测试 90/90；顺带修复 3 个实缺口：读动作 ada.action 走 GET、dashboard 卡并入 dashboard.widgets 插槽流、v-if 链空白不再打断 |
| S4 基座界面完全重构 | ✅ 完成（按原界面 HEAD 形态全量重建，2026-11-11） | `static/ada/pages/`：Thread/Threads/Overview/Extensions/Workspaces/PluginDetail/Sidebar 七页 SFC + state.js/format.js；`ada/icons.css`（55 图标 CSS mask，verify 1.5.6 守卫禁止 svg 入白名单后的替代方案）+ ui-icon/ui-markdown/ui-slot 宿主组件；流式渲染（delta append 进 reactive 流式行 + item.updated 全量合并回收）；基座交互托盘（thread.tray 空时才渲染）；composer 全套（图片粘贴/拖拽 multipart、模型选择器带 thinking level、草稿、outbox、Ctrl+N/K）；新 JSON 端点 /partials/threads.json、/partials/model.json、state.interactions、transcript 游标分页、extensions?detail；框架真 bug 修复 2 处（方法调用接收者绑定、true/false/null/undefined 字面量）；门禁：Go 31 包、verify 11/11、staticcheck 零输出、node 94/94（含 pages-smoke 全页面 DOM 挂载冒烟）；2026-11-11 收尾补图片灯箱（UiImageViewer）、会话行「更多」菜单（复制 ID/归档）、303 flash → UiToast（node 96/96） |
| V 验收 | ✅ 门禁全绿 + curl 级冒烟（shell/#app/bootstrap/注册表/模型端点实证；IAB 环境不支持动态 import，Chromium 视觉走查受限，如实记录） | go build/vet/test 31 包、staticcheck、adag verify 11/11、node 94/94 |

### S4 已知降级（原始界面行为未/不可等价复刻，记录在案）
1. `<img>`/`input[type=file]` 不在标签白名单 → 图片用 CSS background-image、文件选择用 setup 内命令行创建 input；消息图片灯箱已用 background-image 重建（`UiImageViewer`，2026-11-11 收尾）
2. 模型级联浮层 → 菜单内嵌展开（HEAD 为 body 级浮层定位）
3. 会话行右键菜单 → 行内「更多」按钮 + ShellApp 根部的 `.ada-context-menu`（复制 ID/归档已移植，2026-11-11 收尾）；预览文件语法高亮降级为纯文本源码
4. run-status 逐柱动画 → 单帧均衡器 mask + 呼吸动画（HEAD 绑定 SVG rect）

> 2026-11-11 收尾：已消掉图片灯箱、会话行「复制 ID/归档」、POST 回执 303 flash 三项。
> flash 现由 `makeFetcher` 读取 `response.url` 的 `?notice=`/`?error=` 推入 `UiToast`
>（服务端 `flashRedirect` 文案原样展示，页面本体不解析）。


## 0. 总原则

1. **每个阶段结束门禁必须绿**：`go build ./...`、`go test ./...`、`go vet ./...`、staticcheck、`adag verify`（随阶段更新口径）。
2. **冻结项不碰**：SSE 协议（seq/快照/resync）、RPC 方法集合、错误码、12 插槽词汇表、CSP `script-src 'self'`、CSRF、动作桥 confirm、VNode 白名单词汇表（执行点移动，词汇不变）。
3. **JS 零 npm 依赖**：框架与测试只用浏览器原生 + `node --test`（本机 node 26）；测试文件与 DOM shim 进仓，作为开发期门禁（`node --test internal/gateway/web/static/ada/`）。
4. **导入路径约定**：插件逻辑 ESM 只允许 `import ... from "/static/ada/ada-runtime.mjs"` 与相对路径（`./x.js`）；`/static/ada/ada-runtime.mjs` 是稳定门面（`Cache-Control: max-age=300` + ETag），内部模块不对外承诺路径。
5. **失败方向**：任何插件贡献装载失败 → 该贡献位置标记 `data-ada-error`，页面其余部分不受影响；模型内容渲染失败 → 降级为普通代码块（现状语义不变）。

## 1. 目录与文件规划（新）

```
static/ada/
  ada-runtime.mjs        门面：re-export 公共 API（ref/reactive/computed/effect/effectScope/
                         watch/watchEffect/defineComponent/onMounted/onUnmounted/onErrorCaptured/
                         provide/inject/defineAsyncComponent）
  reactivity.mjs         响应式内核（F1）
  runtime.mjs            组件模型 + 渲染器 + createVNode 白名单 + diff patch（F2）
  compiler.mjs           模板 tokenizer/parser → 渲染计划（F3）
  sfc.mjs                SFC 装载器：fetch/parse/compile/compiledCache/scoped CSS（F4）
  router.mjs             history 路由器（F5）
  host.mjs               宿主服务：registry store、provide("ada")、SSE glue、bootstrap（F6）
  index.mjs              shell 入口：布局组件 + 路由表 + SlotOutlet（S2）
  whitelist.mjs          白名单词汇表（由 spec/vnode.json 生成，编译期/patch 期共用）
  test/
    dom.mjs              最小 DOM shim（node --test 用）
    reactivity.test.mjs / compiler.test.mjs / runtime.test.mjs / sfc.test.mjs / router.test.mjs
static/
  shell.css              shell 布局样式（改造自 app.css 的骨架部分）
  app-spa.mjs            （若与 index.mjs 合并则不单独建）
```

```
Go 新增/改造：
  internal/gateway/web/handlers.go        页面 handler → shell fallback + JSON 数据端点
  internal/gateway/web/server.go          路由：history fallback；/frontend-registry；模板收缩
  internal/gateway/web/registry.go        （新）GET /frontend-registry 装配
  internal/gateway/web/slot.go            保留词汇表与覆盖核对；删除 RenderSlot HTML
  internal/gateway/web/static.go          （新，若需）shell 数据装配
  internal/gateway/sse/hub.go             配置化第二实例（workspace 通道）
  internal/gateway/web/workspace_sse.go   （新）GET /events/workspace
  internal/gateway/frontend/bridge.go     收缩为「注册表数据源」（去掉 HTML 渲染）
  cmd/adag/verify*.go                     口径更新（V 阶段）
```

## 2. R 阶段 —— 移除 SSR 那一套（先行，独立验收）

> 验收标准：`go build/test/vet` 绿；`grep` 确认仓库内无整页 SSR 渲染路径残留；登录/错误页仍可渲染（这两个是无 JS 安全页）；浏览器请求任意页面路径得到 shell（此阶段 shell 可以先是最小占位）。

### R1 Go 侧删除

| 动作 | 目标 | 说明 |
|---|---|---|
| 删 | `internal/gateway/frontend/render.go` + `render_test.go` | TSX→VNode→HTML 管线 |
| 删 | `internal/gateway/frontend/` 岛屿/同构相关：`iso_*`、`island_bench`、`bridge_island`、`bridge_client` 等测试及其实现 | 岛屿 hydrate 体系随 SSR 退役 |
| 删/缩 | moejs 前端 Render Runtime：`internal/common/pluginapi/render.go`（`Renderer/NewRenderer`）——**删除 RenderTemplate/RenderPage/RenderIslandWithStores（页面/插槽/岛路径），保留 esbuild compile + RenderBlockTemplate（Markdown 动态块链路，`proposals` §9 不变量）**；预算常量 `frontend/vnode.go` 的 SlotTimeoutMS/PageTimeoutMS 退役、block 预算保留 | moejs 本体（插件后端宿主）不动 |
| 缩 | `internal/gateway/frontend/bridge.go`：删除 Slot/Page 的 HTML 渲染路径，保留：manifest 前端段解析、启停过滤（isEnabled + TTL 缓存）、静态资源 URL 指纹、动作/导航/仪表盘声明投影 | Bridge 变为 `/frontend-registry` 的数据源 |
| 删 | `internal/gateway/web/slot.go` 的 `RenderSlot`/`slotHosts` HTML 输出；**保留** 12 插槽词汇表与 `CheckSlotCoverage`（改为核对注册表声明 ⊆ 词汇表） | 词汇表是冻结契约 |
| 缩 | `internal/gateway/web/handlers.go`：删除页面渲染 handler 的模板逻辑；保留/新增 JSON 数据端点；表单 POST handler 保留（登录、动作、turn.start 等），成功响应从 303+页面改为 303 到来源路径（fetch 客户端跟随） | 数据端点清单见 R3 |
| 删 | 模板：`chat.html.tmpl`、`overview.html.tmpl`、`extensions.html.tmpl`、`ext.html.tmpl`、`workspaces.html.tmpl`、`plugin_detail.html.tmpl`、`not_found.html.tmpl`（并入 error）、`shell.html.tmpl`（改写为 SPA shell，见 S1） | 保留：`login.html.tmpl`、`error.html.tmpl`、`offline.html.tmpl`（离线提示，无 JS） |
| 删 | `web/pluginui.go` 中插件插槽 HTML 渲染（pluginSlots）；保留 `handleExtStatic`、动作桥、markdown.ast | |
| 删 | `web/markdown_vnode.go` 的 SSR 渲染出口（若只服务页面）；**保留** `POST /markdown.ast`（SPA 客户端要用） | 按盘点确认 |

### R2 静态 JS 删除

删除（SSR 时代产物，逻辑由 ada-runtime 重写）：`ada-render.js`、`ada-ui.js`、`ada-ui-iso.js`、`islands.js`、`plugin-islands.js`、`ada-patch-stream.js`、`chat-live.js`、`app.js`、`components.js`、`composer-draft.js`。

保留（登录页/shell 仍用或过渡期复用）：`theme.js`、`themes.js`、`sidebar-boot.js`、`api.js`（window.adaAPI/adaSSE——host.mjs 包装它）、`ada-select.js`、`ada-modal.js`、`file-picker.js`、`chat-images.js`、CSS（`app.css`→拆分出 `shell.css`，`ada-ui.css`/`tokens.css`/`themes` 保留）、`static/ui/`、图片。

### R3 数据端点（SPA 取数面，保持网关本地、不开通用 RPC）

盘点修正（2026-11-11）：`dashboard.json` / `extensions.json` / `transcript` / `/threads/{id}/state` / `/api/workspaces` 等端点**已存在**，直接保留；新增项只有 registry 与 workspace SSE。

| 端点 | 现状 | 用途 |
|---|---|---|
| `GET /partials/dashboard.json` | 已存在 | overview |
| `GET /partials/extensions.json` | 已存在 | 扩展页（含启停所需字段） |
| `GET /partials/transcript/{id}` | 已存在，**需改造**：助手行已是 VNode JSON，但用户/工具行由模板 `row` 渲染成 HTML（handlers.go L326-332）→ 改为纯 JSON 数据行，由客户端组件渲染 | 会话页 |
| `GET /threads/{id}/state` | 已存在 | 会话页状态 |
| `POST /markdown.ast` | 已存在 | markdown → VNode AST（display profile） |
| `GET /api/workspaces`、`/api/fs/list`、`/api/workspace/select`、`POST /model/select` | 已存在 | 工作区/模型 |
| `GET /extensions/{id}/file` | 已存在 | 插件文件预览 |
| `GET /ext/{id}/api/*`、`/ext/{id}/action/*`、`/events/{id}` | 已存在（不动） | 插件 route/动作/SSE |
| `GET /frontend-registry` | 新增 | 前端贡献注册表（schema 见 §6） |
| `GET /events/workspace` | 新增 | 工作区级 SSE（evt.plugin.changed 等） |

删除的 HTML 端点：`/partials/dashboard`（HTML 片段）、`/partials/slot/{name}`、`/partials/workspace-threads`（HTML 行）、`POST /markdown`（HTML 版，ast 为主路径）。

### R4 官方插件前端资产（盘点后逐个处置）

- 删除各官方插件 `slot.tsx/page.tsx/client.tsx/static/client.js` 与 manifest 中 `client/scripts/hydrate/clientOnly/template` 字段（M 阶段用 SFC 重写后再声明 `component`）。
- `blocks/nav/dashboard/actions/styles` 保留（与渲染管线无关，styles 为全局 CSS 文件继续有效）。
- **先删资产、后迁 SFC**：中间态下这些插件在前端不可见（注册表无贡献），后端能力（route/actions/blocks）不受影响；`adag verify` 的插槽覆盖核对按「注册表为空即过」更新。

### R5 verify/测试改写

- golden HTML 测试（模板渲染）删除；保留 vnode 校验、转义、白名单、SSE、CSRF 等纯逻辑测试。
- `CheckSlotCoverage` 改为注册表核对；`adag verify` 相应项更新（详见 V 阶段清单）。

## 3. F 阶段 —— ada-runtime 核心（与 R 并行开发，不依赖 Go 改动）

> 验收标准：`node --test internal/gateway/web/static/ada/` 全绿；无 `new Function`/`eval`（源码扫描脚本进 verify）；公开 API 与 §1 门面一致。

### F1 reactivity.mjs（响应式内核，~400 行）

- API：`ref/isRef/unref/reactive/isReactive/computed/effect/effectScope/watch/watchEffect/nextTick`。
- 语义：Proxy 依赖收集（get 追踪 / set 触发）；调度队列微任务批量 flush、组件按创建序稳定更新；`computed` 惰性+缓存+链式失效；`effectScope().stop()` 回收全部子 effect（插件卸载基石）。
- 测试用例（reactivity.test.mjs）：依赖收集与触发、computed 链式失效顺序、数组/嵌套对象、scope.stop 后不再触发、watch flush 时机（pre/post）、批量去重。

### F2 runtime.mjs（组件模型 + 渲染器，~600 行）

- `defineComponent({ props, emits, setup, render? })`；setup 上下文：`{ props, slots, emit, expose }`；生命周期 `onMounted/onUnmounted/onErrorCaptured` 注册到当前 scope。
- `createApp(root)`：挂载、全局 provide、每 app 一个 error handler；`createVNode(tag|comp, props, children)` 是**唯一** VNode 构造入口，内建白名单校验（§5.7 的 patch 期关卡）：标签 ∈ whitelist.mjs、属性白名单 + `aria-*/data-*`、URL 属性过 validateURL、禁 `innerHTML`（v-html 无编译路径）、`:style` 仅对象/数组（走 CSSOM）、深度 ≤64 / 节点 ≤2000 / 字符串 ≤32KB / 输出 ≤256KB（沿用现值）。
- 渲染循环：组件 render 包在 effect 里 → 状态变更 → 调度器入队 → flush 时重渲染该组件子树 → diff patch（复用 ada-render.js 的对齐思路：同 tag 同 key 就地 patch，否则重建）。
- 事件绑定 `on:{event}` 修饰符编译期展开（.prevent/.stop/.once/.self）。
- `defineAsyncComponent(loader)`：pending/error/success 三态；error 态渲染 `<div class="ada-slot-error" data-ada-error>`。
- 测试用例：挂载/更新/卸载、props 单向、事件触发、白名单拒绝（未知标签/onclick 属性/javascript: href/innerHTML）、error boundary 隔离、scope 卸载清理。

### F3 compiler.mjs（模板编译器，~700 行）

- 输入：SFC `<template>` 文本 → tokenize（复用 ada-render.js tokenizer 思路重写为 ESM）→ 模板 AST → 归一指令 → 渲染计划（静态节点提升，动态段闭包化）。
- 指令 v1：`v-if/v-else-if/v-else`、`v-show`（hidden 属性）、`v-for`（含 :key）、`v-bind`（:）、`v-on`（@）、`v-model`（input/textarea/select → value + onInput 组合）、`v-text`；**不支持**：`v-html`（报错）、`v-pre`、`:is` 动态组件（只允许 `<component :is="注册表组件名">` 由 SlotOutlet 使用，编译器特例）、过滤器、插槽语法（v1 插槽由宿主组件手写 render 函数提供）。
- 表达式：Pratt 解析器；作用域 = setup 返回值 + props + 白名单全局（Math/JSON/Date/Number/String/Boolean/Array）；禁 `constructor/__proto__/prototype/window/document/fetch/globalThis`；编译期静态检查未声明标识符（默认拒绝，白名单例外：`$event`、`$emit`）。
- 输出缓存：`compileTemplate(src, scopeId)` 按内容指纹 memoize。
- 测试用例：全部指令展开正确性、白名单拒绝（未知标签/`<script>`/内联 style 属性/on* 于静态属性）、表达式作用域越界拒绝、static hoisting。

### F4 sfc.mjs（SFC 装载器，~300 行）

- `parseSFC(text)`：`<template>`/`<style scoped|module?>`/`<script src>` 块提取；**内联 `<script>` 直接抛错**（含提示信息指向 src 约定）。
- `loadSFC(url)`：fetch 文本 → sha256 指纹（SubtleCrypto）→ compiledCache 命中即返回 → `import(scriptSrc)`（原生 ESM）→ `compileTemplate` → scoped CSS 编译（`[data-v-<hash>]` 后缀 + `:deep()` 展开）→ 组装 component def。
- `addStyle`：每插件一张 `new CSSStyleSheet()` + `replaceSync` + 挂 `document.adoptedStyleSheets`；返回 detach 句柄（scope 持有）。
- `defineAsyncComponent(() => loadSFC(url))` 是插件贡献的标准装载形态。
- 测试用例（DOM shim）：parse 块提取、内联 script 拒绝、scoped 属性注入与选择器改写、缓存命中、导入失败错误态。

### F5 router.mjs（~200 行）

- `createRouter({ routes })`：history API、路径模式（`:param`）、`navigate/replace/back`、`beforeLeave` 守卫、popstate、滚动恢复、`<RouterOutlet>`、当前路由为 reactive（组件可读）。
- 401/403 → 跳转登录页（整页导航，登录页是 SSR）；404 → shell 内 error 视图。
- 测试用例（shim history）：匹配、参数、守卫短路、popstate。

### F6 host.mjs（宿主服务）

- `provide("ada")` 服务面：`api(path, opts)`（包装 window.adaAPI）、`sse(path, handlers)`、`action(name, input)`（自动 confirm 文案 → ada-modal）、`thread`（reactive 当前线程上下文）、`confirm(text)`、`storage`（localStorage `<pluginId>:` 前缀隔离）。
- registry store：拉 `/frontend-registry` → reactive Map；订阅 `/events/workspace` → `evt.plugin.changed` 增量更新（启用/禁用即时生效：插槽移除 + scope.stop + 样式表摘除 + 路由注销）。
- thread store：`/events/{threadId}` SSE → `evt.message.delta/item.updated/run.status` 驱动 reactive items；resync → 重拉 transcript partial。
- 静态预取：shell bootstrap 内联 props（app/workspace/route 投影）由 Go 模板注入 JSON（`<script type="application/json">`，CSP 允许非 script 标签）。

## 4. S 阶段 —— Shell SPA（Go 端点 + 前端装配）

> 验收标准：浏览器打开 `/` 得到 shell；overview/extensions/workspaces/plugin_detail 四页组件化可用（无 JS 不再支持——已声明放弃）；12 插槽 outlet 全部就位；登录/错误页无 JS 可用。

| 任务 | 内容 |
|---|---|
| S1 | `shell.html.tmpl` 改写：仅 head（CSRF meta、bootstrap JSON、theme.js、`<script type="module" src="/static/ada/index.mjs?v=…">`）+ `<div id="app">`；`server.go` 模板集收缩为 shell/login/error/offline |
| S2 | `index.mjs`：shell 布局组件（sidebar/topbar/composer/transcript 骨架）+ 12 个 `<SlotOutlet name>` + 路由表（`/`=overview、`/extensions`、`/workspaces`、`/ext/:id`、`/ext/:id/:slug`、`/thread/:id`）+ `router.addRoute` 注册插件页面 |
| S3 | Go：`GET /frontend-registry`（bridge 投影，schema 见 §6）、`GET /events/workspace`（hub 第二实例：seq 独立、Last-Event-ID 补发、evt.plugin.changed/evt.ready）、history fallback（非 API/静态路径回 shell） |
| S4 | 基座页面组件：Overview（dashboard.json）、Extensions（extensions.json + 启停表单 fetch POST）、Workspaces、PluginDetail、Thread（transcript.json + SSE store + composer 表单 + 交互托盘——托盘外壳是基座组件，插件不可替换，`docs/00 §1.5.7`） |
| S5 | 会话终态语义改写：`chat-live.js` 的 reloadOnce 触发点改为 store 状态机（逐条对照移植，golden 用例随测试改写） |

## 5. C 阶段 —— 金丝雀插件迁移（验证全链路）

| 任务 | 内容 | 验收 |
|---|---|---|
| C1 | `ui-hello-tsx` → `ui-hello`：`Hello.vue`（sidebar.top）+ `HelloPage.vue`（slug hello）+ ESM 逻辑；删除旧 TSX/client.js | 装载、渲染、卸载、禁用即时消失 |
| C2 | `composer-telemetry` → SFC（composer.footer）：验证 clientOnly 场景在 SPA 下退化为普通组件 | 指标随会话更新 |
| C3 | `tools-plan` → SFC（chat.corner + route 数据 + 动作桥 confirm） | 走通 ada.api/action/sse 全服务面 |

## 6. /frontend-registry 契约（Go ↔ JS 唯一接口）

```jsonc
{
  "rev": 7,                     // 递增序号，evt.plugin.changed 携带
  "app": { "workspace": "...", "workspaceName": "...", "product": "...", "route": "/" },
  "plugins": [                  // 仅 enabled；排序稳定
    {
      "id": "tools-plan", "name": "计划", "trust": "official", "version": "1.2.0",
      "slots":   [ { "slot": "chat.corner", "component": "/ext/tools-plan/static/PlanCard.vue?v=a1b2c3d4", "order": 90 } ],
      "pages":   [ { "slug": "plan", "component": "/ext/tools-plan/static/PlanPage.vue?v=a1b2c3d4", "title": "计划面板" } ],
      "nav":     [ { "path": "/ext/tools-plan/plan", "label": "计划", "order": 10 } ],
      "dashboard": [ { "component": "/ext/tools-plan/static/PlanWidget.vue?v=…", "order": 10 } ],
      "actions": { "toggleStep": { "confirm": "将修改计划步骤", "kind": "write" } }
    }
  ]
}
```

- `?v=` 内容指纹沿用 `assetURL` 机制；component URL 必须在 `assets` 目录内（服务端校验）。
- `evt.plugin.changed` 载荷：`{ "id": "...", "enabled": true, "rev": 8 }` → 客户端重拉 registry（简单正确优先，不做增量 diff）。

## 7. M 阶段 —— 其余官方插件迁移 + 契约固化

provider-admin、env-manager、mcp-client、cron-manager（scripts 型收编进组件契约）、token-usage、ui-charts（blocks 不动）；`spec/manifest.schema.json` 改写（`component` 字段、删 `client/scripts/hydrate/clientOnly/template`）；`internal/common/pluginapi/manifest.go` 校验同步。

## 8. V 阶段 —— 验收门禁

| 门禁 | 内容 |
|---|---|
| go build/test/vet/staticcheck | 全绿 |
| `node --test static/ada/` | 框架契约测试全绿（进仓，README 说明） |
| `adag verify` 口径更新 | ①slot 覆盖：注册表声明 ⊆ 12 词汇表 ②CSP/无 eval 扫描扩到 `static/ada/**` 与插件 SFC/ESM preflight（内联 script、import 白名单、未知标签）③manifest schema 逐字比对更新 ④golden 快照从 HTML golden 改为「编译计划快照」（compiler.mjs 输出 fixture，node 生成 + Go verify 比对，或纯 node 侧断言） |
| 手工冒烟 | `adag serve` + 浏览器：登录 → shell → 各页 → 插件插槽 → 启停即时生效 |

## 9. 排期与依赖关系

```
R（移除）─────────────┐
F1→F2→F3→F4→F5→F6 ───┼──→ S（shell+端点）──→ C（金丝雀）──→ M（迁移）──→ V（验收）
   （F 与 R 并行）      │
                       └─ R 完成是 S 的前提（Go 路由面已清空）
```
