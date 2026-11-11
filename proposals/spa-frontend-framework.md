# 提案：Ada 前端插件方案 —— 自研框架 · 单页面 · 无 SSR

- 日期：2026-11-10
- 分支：`design/spa-plugin-framework`（本提案在此分支上评审，未合入 main）
- 状态：**提案（未接受）**。若被接受，将改写 `docs/00-overview.md` §1.3/§1.5、`docs/07-ssr-frontend.md`（更名）、`docs/14-ui-runtime.md` 的相关章节。
- 一句话：把前端从「Go html/template 整页 SSR + 自研 VNode 交互岛」改为**单页面应用（SPA）**；插件前端从「TSX 服务端渲染 + 预编译 client.js 岛」改为**运行时装载的 SFC 单文件组件**；渲染框架**继续自研**（不引入 Vue 3 与 vue3-sfc-loader 依赖），但**借鉴 Vue 3 的运行时设计与 vue3-sfc-loader 的装载器形态**。

---

## 1. 现状评估

### 1.1 技术栈盘点

当前前端 = 「Go `html/template` 全页 SSR + 手写零依赖 JS 渐进增强」，发布物是单个 Go 二进制，无 npm/vite 构建链：

| 层 | 位置 | 规模 | 职责 |
|---|---|---|---|
| SSR 宿主 | `internal/gateway/web`（`server.go`/`handlers.go`/`view.go` + `templates/*.html.tmpl`） | — | 整页渲染、303 PRG、`meta refresh` 轮询、终态整页 reload |
| VNode 引擎 | `internal/gateway/web/static/ada-render.js` | 3031 行 | 自研 Vue 风格引擎：手写 tokenizer + Pratt 表达式解释器，VNode hydrate/patch，**不用 eval/new Function** |
| 组件层 | `static/ada-ui.js` / `ada-ui-iso.js` | 1120 行 | `ui-*` Web 组件与同构组件定义 |
| 外壳逻辑 | `static/app.js` / `chat-live.js` | 1531 + 808 行 | 布局交互、SSE 消费、主题、草稿 |
| 岛屿引导 | `static/islands.js` / `plugin-islands.js` | 769 + 199 行 | 扫描 `[data-ada-island]`、动态 `import()` 插件 ESM |
| 插件 SSR 渲染 | `internal/gateway/frontend`（`render.go`/`vnode.go`/`bridge.go`） | — | TSX → esbuild → moejs 执行 → VNode → 白名单校验 → Go 转义 HTML |
| 插件打包 | `tools/pluginbuild`（Go 版 esbuild） | — | client.tsx → 单文件 ESM `static/client.js`，内容指纹、CSP 扫描、512KB 上限 |
| 实时通道 | `internal/gateway/sse` + `static/chat-live.js` | — | `/events/{threadId}` SSE：seq 补发、resync 全量恢复、40ms 合并窗口 |

插件前端面：manifest `frontend` 段声明 `slots[]`（12 个稳定插槽）、`pages[]`、`blocks[]`、`nav[]`、`dashboard[]`、`actions{}`、`styles[]/scripts[]/assets/client`；能力位只有 `backend.read` 与 `frontend.script`（`internal/common/pluginapi/manifest.go` + `contract.go`，schema 冻结于 `spec/manifest.schema.json`）。

### 1.2 做得对的（新方案原样保留的资产）

1. **安全不变量扎实**：CSP `script-src 'self'` 全家桶（`internal/gateway/security/security.go:36`）、CSRF 双提交、VNode 白名单（标签/组件/属性/URL/深度/体积限额）、文本全转义、模型内容 display profile fail-closed。
2. **SSE 协议**：seq 单调、`Last-Event-ID` 补发、断序 → 快照重同步，与 RPC 方法集合一起被 `docs/08` 冻结。
3. **动作桥与 route 桥**：`POST /ext/{id}/action/{name}`（confirm + CSRF）、`ANY /ext/{id}/api/...`（`plugin.http`，1MB/64KB 限额），浏览器统一 `window.adaAPI` / `window.adaSSE`。
4. **信任模型与审计**：builtin / verified（Ed25519）/ third-party，来源决定信任上限；安装/启停/重载有审计事件。
5. **UED 令牌体系**：`--ph-*` / `--ada-*` CSS 变量、`ui-*` 组件前缀、明暗 × 调色板两维主题——纯 CSS 变量方案天然跨渲染框架存活。

### 1.3 痛点清单（驱动本次重构）

| # | 痛点 | 证据 |
|---|---|---|
| P1 | **前端面装配期固定**，插件前端改动需重启 Gateway | `handleExtensionsReload` 注释、`docs/07 §7.7.6` |
| P2 | **浏览器无插件生命周期感知**：`evt.plugin.changed` 不进线程 SSE，靠 2s TTL + 刷新兜底 | `internal/gateway/sse/hub.go:279` |
| P3 | **停用不下线**：`Styles()/Scripts()/StaticAsset` 不检查启用状态，停用插件的 CSS/JS 仍被引用 | `frontend/bridge.go` |
| P4 | **自研引擎维护成本高、能力面窄**：3031 行手写引擎 + 手写表达式解释器，渲染运行时组件只实现 `Icon/TodoList/Select` 三个 | `docs/07 §7.5`、`docs/14` |
| P5 | **TSX 是纯函数式 SSR**：无状态、无事件、无副作用，复杂交互只能绕行——`scripts` 型插件（cron-manager）直接裸操作 DOM，完全绕过组件契约 | `docs/07 §7.3`、agent 探索报告 |
| P6 | **双轨渲染心智负担**：SSR HTML + 岛 hydrate + 按 key VNode patch + 终态整页 reload，四条同步路径并存 | `ada-patch-stream.js`、`chat-live.js` |
| P7 | **插件作者要懂三套东西**：TSX 服务端模板、client.js 岛 bundle（esbuild 预编译）、裸 scripts DOM 操作 | `tools/pluginbuild` |
| P8 | **渐进增强投入被实践边缘化**：`clientOnly` 岛（如 composer-telemetry）已无 SSR 回退，`ADAG_DISABLE_ISLANDS` 即空白 | `docs/07 §7.5` |

核心判断：**安全底盘与协议是资产，渲染架构是负债**。SPA + 运行时 SFC 把负债清掉，资产原样保留。

---

## 2. 目标与非目标

### 目标

1. **单页面应用**：网关只出一个 shell 页面 + 一份静态 runtime；页面切换零刷新，登录页/错误页除外。
2. **不再 SSR**：删除 `html/template` 页面渲染、TSX→VNode→HTML 服务端管线、moejs 前端 Render Runtime、`tools/pluginbuild` 前端打包。
3. **插件前端 = SFC**：插件作者只写 `.vue` 语法子集的单文件组件（template + style scoped + 逻辑在 ESM），**零构建步骤**，安装即用，改完刷新页面即生效。
4. **框架自研**：不引入 Vue 3 / vue3-sfc-loader / 任何 npm 运行时依赖；响应式内核、组件模型、模板编译器、SFC 装载器全部进 `static/`，与 Go 二进制一起发布。
5. **CSP 不变**：继续 `script-src 'self'`，不开放 `unsafe-eval`（这是自研相对 vue3-sfc-loader 的决定性优势，见 §5.8）。
6. **顺手修掉 P1/P2/P3**：运行时装载天然消灭 P1；工作区级 SSE + 响应式注册表消灭 P2/P3。
7. **模型生成内容不变**：Markdown `adag-ui` 块仍走「服务端校验的 VNode JSON + display profile」，绝不进入 SFC 编译器。

### 非目标

- 不做 SSR/水合（连保留兜底都不做，接受首屏依赖 JS）。
- 不做 TypeScript 编译（无构建 ⇒ v1 插件逻辑是纯 JS ESM；TS 见 §14 开放问题）。
- 不做 iframe/Worker 沙箱（维持 `docs/09` 「同源脚本是已接受残余风险」的立场，列为可选强化）。
- 不改 SSE 事件协议、RPC 方法集合、错误码、12 插槽语义。
- 不引入 npm/pinia/构建链。

---

## 3. 被有意推翻的既有决策（显式声明）

本提案推翻以下已记录的架构决策。评审通过后需同步改写对应文档，避免出现「文档说 A、代码是 B」的漂移（参照 2026-11-10 审批策略移除时的文档改写惯例）：

| 既有决策 | 出处 | 新决策 |
|---|---|---|
| 不做 SPA、不做客户端路由 | `docs/07 §7.1` | 整站 SPA + 自研客户端路由 |
| 渐进增强：无 JS 可完成全流程 | `docs/00 §1.3` | 放弃；仅登录/错误页保留无 JS 可用 |
| 不做客户端插件运行时 | `docs/14 §14.1.2` | 插件前端全部运行时装载 |
| 整页刷新语义（303 PRG、meta refresh、终态 reload） | `docs/07`、`chat-live.js` | 全部改为响应式状态更新 + SSE 驱动 |
| 服务端 VNode 白名单是唯一渲染出口 | `docs/07 §7.3` | 白名单词汇表不变，**执行点移到客户端编译/打补丁时**（见 §5.7）；服务端校验仅保留给模型内容 VNode |
| 插件前端变更需重启 Gateway | `docs/07 §7.7.6` | 运行时装载 + 内容指纹缓存，刷新即生效 |

12 插槽集合、SSE 协议、RPC 方法集、动作桥、信任模型、`--ph-*/--ada-*` UED 令牌：**全部不变**。

---

## 4. 总体架构

```
┌───────────────────────────── 浏览器（唯一渲染现场） ─────────────────────────────┐
│                                                                                  │
│  index.html（Go 模板，仅 shell + importmap-free 引导，无业务模板）                 │
│      │                                                                           │
│      ▼                                                                           │
│  ada-runtime.mjs（自研框架，静态文件，无依赖）                                     │
│  ├── 响应式内核  ref / reactive / computed / effect / effectScope / watch        │
│  ├── 组件模型    defineComponent / setup / props / provide / inject / 生命周期     │
│  ├── VNode 渲染器 createVNode(白名单) + diff patch（ada-render.js 的演进）         │
│  ├── 模板编译器  SFC template → 渲染计划（闭包树，无 eval）                        │
│  ├── SFC 装载器  fetch .vue → parse → compile → compiledCache（借 vue3-sfc-loader）│
│  ├── scoped CSS  编译 + 每插件一张 ConstructableStyleSheet（CSSOM 注入）           │
│  ├── 路由器      history API，~200 行                                            │
│  └── 数据层      useAda() / useThread() / useSSE() composables                    │
│                                                                                  │
│  shell app：布局（sidebar/topbar/composer/transcript）+ 12 个 <SlotOutlet>        │
│  插件贡献：defineAsyncComponent(() => loadSFC('/ext/{id}/static/PlanCard.vue'))   │
│  逻辑代码：插件 ESM（原生 import()，CSP 'self' 下合法）                             │
│  模型内容：evt 流 VNode JSON → display profile 渲染组件（不经 SFC 编译器）          │
└──────────────────────────────────────────────────────────────────────────────────┘
             ▲ SSE：/events/{threadId}（不变） + /events/workspace（新增）
             ▲ fetch：window.adaAPI / 动作桥 / route 桥（不变）
┌───────────────────────────── Go 网关 ─────────────────────────────┐
│  保留：静态资源服务、动作桥、route 桥、SSE hub、CSRF/CSP 中间件、      │
│        vite-like 内容指纹 assetURL、handleExtStatic 512KB 限额       │
│  新增：/events/workspace 工作区事件通道、GET /frontend-registry      │
│  删除：页面模板（login/error 除外）、frontend/render.go SSR 管线、    │
│        moejs 前端 Render Runtime、pluginbuild 前端打包              │
└───────────────────────────────────────────────────────────────────┘
```

数据流变化：**VNode JSON 不再是页面 DOM 的真源**（只保留给模型内容块）；页面 DOM 真源变成「插件/基座组件的响应式状态」——这正是 Vue 3 的设计哲学：状态 → 响应式 → 组件 re-render → VNode diff patch。

---

## 5. 自研框架设计（ada-runtime）

### 5.1 借鉴清单：各借什么、不借什么

| 来源 | 借鉴 | 不借鉴 |
|---|---|---|
| **Vue 3** | 响应式内核形态（proxy-based `reactive` + 依赖收集 + 调度器批量 flush）；组件级 `effect` + VNode diff patch；`defineComponent/setup` 组合式 API；`provide/inject`；`effectScope`（插件卸载的关键）；`defineAsyncComponent`（插槽装载）；SFC 单文件格式与 scoped CSS 算法（`[data-v-x]` 后缀）；`onErrorCaptured` 错误边界 | 模板编译到代码字符串 + `new Function` 求值；runtime-dom 的 `patchProp` 全量语义（按白名单裁剪）；SSR/水合双端；TVX/TS 支持 |
| **vue3-sfc-loader** | 装载器解剖学：`parse → transform → compiledCache(key=内容指纹) → addStyle(text)`；`getFile` 资源契约（url → {type, content}）；`moduleCache` 模块代换思想；「网络、样式注入、缓存都交给宿主」的边界划分 | `new Function` 求值编译产物；babel 转译 TS/JSX；3 年未发版的外部依赖本身 |
| **现有 ada-render.js** | 手写 tokenizer + Pratt 表达式解释器（禁 `constructor/__proto__/prototype`，全局白名单）；VNode 线格式与 diff patch；`v-show` 走 `hidden`、禁 `v-html` 的既有纪律 | 「无状态表达式 + data-* 交互」的限制（SPA 中事件是第一公民） |

### 5.2 响应式内核（~400 行）

照搬 `@vue/reactivity` 的公开语义，实现收敛到本项目需要的子集：

```js
// static/ada/reactivity.mjs
let activeSub = null;          // 当前正在收集依赖的 effect
const pending = new Set();     // 调度队列（微任务批量 flush）
let batchDepth = 0;

export function ref(v) { return trackRef({ value: v }); }
export function reactive(obj) { return new Proxy(obj, handlers); }
export function computed(getter) { /* 惰性 + 缓存 + 依赖变更标脏 */ }
export function effect(fn, opts) { /* 运行 fn，收集依赖，变更时入调度队列 */ }
export function effectScope() { /* 收集子 effect/detach；scope.stop() 一刀切 */ }
export function watch(source, cb, opts) {}
export function watchEffect(fn) {}
```

- 依赖收集用 Proxy（get 时 `activeSub` 注册、set 时 trigger）；不支持 deep watch 的全部边角（`deep: 'array-only'` 足够）。
- **调度器**：组件 re-render 不是同步的，进 `pending` 集合，微任务统一 flush，按组件创建序稳定排序——避免同一 tick 内重复渲染与父子竞态（Vue 3 scheduler 的简化版）。
- **`effectScope` 是插件生命周期的基石**：每个插件贡献的所有 effect 挂在自己的 scope 下，禁用插件 = `scope.stop()`，一切监听/定时器/响应式副作用一键回收（对比现状：P2/P3 缺口靠 TTL 兜底）。
- Reactivity 内核从第一天起配契约测试（参照 `internal/common/pluginapi` 的做法）：trigger 顺序、computed 链式失效、scope.stop 后不再触发、循环依赖标脏不死锁。

### 5.3 组件模型（~500 行）

```js
import { ref, computed, inject } from "/static/ada/ada-runtime.mjs";

export const setup = (props) => {
  const ada = inject("ada");                  // 宿主服务（§8.4）
  const plan = ref(null);
  const refresh = async () => { plan.value = await ada.api("plan/state"); };
  onMounted(refresh); onUnmounted(() => clearTimeout(timer));
  return { plan, done: computed(() => ...), refresh };
};
```

- `defineComponent({ setup, props, emits, render? })`；setup 返回的普通对象/`ref`/`computed` 就是模板绑定作用域。
- props 是 shallow reactive（父传子单向）；`emits` 只做文档约束，不做运行时拦截。
- `provide/inject` 沿组件树传递宿主服务（线程上下文、API 客户端、确认对话框等），插件组件与基座组件同树。
- 生命周期：`onMounted / onUnmounted / onErrorCaptured`，全部注册到当前 `effectScope`。
- **每个插槽/页面贡献一个独立挂载点 + 独立错误边界**：贡献挂载失败标 `data-ada-error` 并留空位，不影响页面其它部分（延续现在「失败插槽留空」的失败方向）。

### 5.4 模板编译器（无 eval，这是与 Vue 的分水岭）

模板**不编译成代码字符串再 `new Function`**，而是编译成**渲染计划（render plan）**：一棵由闭包/描述子节点构成的树，`render()` 时遍历计划产出 VNode。

```
template 文本
  → tokenize + parse（复用 ada-render.js 的 tokenizer/Pratt 表达式解释器）
  → 模板 AST（元素/属性/指令/插值）
  → 静态提升 + 指令归一（v-if→条件组、v-for→列表组、v-model→value+onInput 对）
  → 渲染计划：{ tag, dynamicAttrs: [(ctx)=>…], children: [plan|text|slot], … }
  → render(ctx)：遍历计划 → 白名单校验 → createVNode
```

- 表达式仍是解释执行（每帧解析表达式 AST），但**缓存**：表达式 AST 与渲染计划都按模板内容指纹缓存，重复渲染只走解释器热路径。实测基线：现有 ada-render.js 已在 /overview 5s 轮询 + 会话流 patch 下工作，此模型的量级没有变化。
- 支持指令集（v1）：`v-if/v-else/v-show/v-for/:bind/@event/v-model/v-text`；**不支持** `v-html`（编译期直接报错，白名单不变量）、`v-pre`、动态组件名（`:is` 只接受宿主注册表内的组件名）。
- 事件（`@click` 等）是 v1 的一等公民：编译为 `on:click` VNode 属性，运行时绑定到 setup 作用域方法；修饰符 `.prevent/.stop/.once` 编译期展开，不做动态修饰符。**插件 SFC 允许事件**（§6 说明为什么安全模型允许）；display profile 的模型内容 VNode 仍然无事件。
- 表达式作用域 = setup 返回值 + 组件 props + 白名单全局（`Math/JSON/Date/Number/String`）；继续禁 `constructor/__proto__/prototype`、禁 `window/document/fetch`——插件要碰浏览器能力必须走宿主注入的服务面（§8.4），这一点从 ada-render.js 原样继承。
- 无 eval 的代价要诚实写明：解释器比编译型 render function 慢（估 2–5×），对插件 UI 的规模（表格/卡片/表单级）无感知；如果未来某贡献成为热点，可以给它加「编译到闭包树直连」的优化路径，依然不需要 eval。

### 5.5 SFC 装载器（借鉴 vue3-sfc-loader 的解剖学，~300 行）

```js
// static/ada/sfc.mjs
const compiled = new Map();                    // key = url + sha256(text) —— vue3-sfc-loader 的 compiledCache 思路

export function defineAsyncComponent(loader) { /* pending/error/success 三态包装 */ }

export async function loadSFC(url) {
  const text = await fetchText(url);           // getFile 契约：url → 文本
  const key = url + "@" + sha256(text);
  if (compiled.has(key)) return compiled.get(key);

  const sfc = parseSFC(text);                  // <template>/<style>/<script src>
  const logic = await import(sfc.scriptSrc);   // ★ 逻辑 = 原生 ESM，见下
  const plan  = compileTemplate(sfc.template, sfc.scriptBindings(logic));
  const css   = sfc.style && compileScoped(sfc.style, scopeId(url));

  const comp = { setup: logic.setup ?? logic.default?.setup, plan, css, scopeId };
  compiled.set(key, comp);
  return comp;
}
```

关键取舍——**SFC 的 `<script>` 块不接受内联代码**，必须 `src` 指向旁边的 ESM 文件：

- 合法：`<script src="./PlanCard.js"></script>`（原生 `import()` 执行，CSP `script-src 'self'` 完全兼容）。
- 非法：`<script>export const x = 1</script>`。原因：无 eval 的世界里内联脚本无法执行；而用 blob:/data: URL 绕行会把「任意字符串 → 可执行代码」变成可能（数据→代码升级），破坏「安装时静态审计」的前提——**所有可执行代码必须是安装时落盘的静态文件**。这条边界写进清单校验（`parseSFC` 遇内联 script 报装配错误）。
- 插件 JS 的 import 路径约定：`import { ref } from "/static/ada/ada-runtime.mjs"`（绝对路径 + 稳定 URL，`Cache-Control: max-age=300` + ETag）；相对路径 `./helper.js` 直接可用（浏览器原生解析）。不用 import maps（外部 importmap 的浏览器兼容面不整齐，不值得为省几个字符冒兼容风险）。
- 图片等资源：模板里直接写 `/ext/{id}/static/img.png` 绝对路径（`img-src 'self' data:` 已放行）；装载器不做资源打包。

### 5.6 scoped 样式与 CSSOM 注入

- scoped 算法照搬 Vue：编译期给选择器最后一个复合选择器追加 `[data-v-<hash>]`，给该 SFC 渲染出的所有根/子元素加同号属性。深度选择器 `:deep()` 按语义展开（插件要透到基座组件内部时用）。
- 注入**不走 `<style>` 标签**（会被 `style-src 'self'` 拦），走 **CSSOM**：每插件一张 `new CSSStyleSheet()`，`replaceSync(css)` 后挂到 `document.adoptedStyleSheets`——按 CSP 规范，CSSOM 注入不受 `style-src` 约束（Chromium/Firefox 行为一致，需在验收清单里对目标浏览器确认）。
- 这张样式表由插件 scope 持有：**禁用插件 = 从 `adoptedStyleSheets` 摘除**——直接修掉现状 P3「停用插件的 CSS 仍被引用」的缺口，且天然获得按插件的样式隔离（对比现在全局 `<link>` 注入）。
- UED 纪律不变：颜色只引 `--ph-*`、尺寸只引 `--ada-*`、`ui-*` 组件名（模板里写 `<ui-button>`，编译器对「未注册的 kebab 标签」按未知元素处理并在装配期告警）。

### 5.7 白名单的原生执行点

自研框架的独有优势：**每一条 VNode 都出自我们的编译器与渲染器**，白名单不再需要像 Vue 方案那样靠 module shim 间接拦截，而是内建在两道关卡里：

1. **编译期**（`compileTemplate`）：标签 ∉ `AllowedTags`、组件名 ∉ 注册表、`v-html`、内联 `style="..."` 字符串属性、`on*` 出现在模型内容上下文 → 装配失败，插件装不上（fail-closed，延续「前端插件输出 onClick → 校验拒绝」的既有测试语义，只是执行点从服务端移到装载器）。
2. **打补丁时**（`createVNode`/`patchProp`）：URL 属性（`href/src/action`）过 `validateURL`（拒 `javascript:/vbscript:/data:text/html`）；属性名白名单（`AllowedAttributes` + `aria-*`/`data-*`）；`:style` 动态绑定允许（Vue 同款：走 `style.setProperty` CSSOM，不触碰 CSP）；深度/节点数/字符串体积限额（64 / 2000 / 32KB / 256KB）沿用现有数值。
3. **词汇表单源化**：`AllowedTags/AllowedComponents/AllowedAttributes` 从 `internal/gateway/frontend/vnode.go` 下沉为共享 spec（`spec/vnode.json`），服务端（模型内容校验）与客户端（SFC 编译期）共用同一份，`adag verify` 继续做快照比对——白名单仍是 `docs/06 §6.10` 意义上的「不可替换安全不变量」，只是执行现场变了。

### 5.8 为什么不用 Vue 3 + vue3-sfc-loader（决策记录）

| 维度 | Vue 3 + vue3-sfc-loader | 自研 |
|---|---|---|
| CSP | loader 依赖 `new Function` 求值编译产物 ⇒ 必须 `unsafe-eval`；且依赖 3 年未发版，为 TS 还要加 `wasm-unsafe-eval` | 保持 `script-src 'self'`，零 CSP 放宽 |
| 白名单 | 只能靠 module shim 间接拦（生成的代码从 moduleCache 的 'vue' 导入 `createVNode` 等），`v-html`/`mergeProps` 等旁路需要逐个堵，且依赖 loader 内部实现细节 | 编译器+渲染器原生关卡，无旁路 |
| 体积 | Vue runtime ~34KB gzip + loader dist ~500KB+（含 babel） | 全自研预计 ~60–80KB gzip（现有 ada-render 21KB gzip 为基础） |
| 依赖治理 | 外部依赖锁死在 0.9.5，漏洞/兼容风险不可控 | 代码在本仓，受现有 `go test`/`adag verify` 门禁治理 |
| 工程量 | 接近零 | 响应式 + 组件 + 装载器 ≈ 1200–1500 行新 JS，在 3031 行既有引擎基础上增量可控（§14 风险） |

结论：本项目「无 npm 构建链 + 严格 CSP + 白名单不变量」的既有形态下，自研是阻力最小且安全姿态最强的路线；Vue 3 提供**设计蓝本**，vue3-sfc-loader 提供**装载器蓝本**。

---

## 6. CSP 与安全模型

CSP 保持：`default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'`（`docs/09 §9.3`）。

安全重心迁移：

| 威胁 | 旧屏障 | 新屏障 |
|---|---|---|
| 插件输出任意 HTML/脚本 | 服务端 VNode 白名单 + Go 转义 | 编译期白名单 + patch 期白名单 + 无 eval + 无 v-html + 无内联 style；逻辑代码必须是安装时落盘的静态 ESM |
| 数据→代码注入 | 无 eval（页面上根本无法求值字符串） | 同左（自研框架的最大安全收益：vue3-sfc-loader 路线做不到） |
| 同源脚本全权 | 已接受残余风险（安装确认 + 信任 + 审计） | 同左；插件 ESM 与基座同源，仍靠安装确认兜底 |
| 模型生成内容 | display profile + 服务端校验 | **不变**：块渲染走 VNode JSON + 独立渲染组件，不进 SFC 编译器、无事件、无表单 |
| 停用插件残留面 | （缺口 P3） | effectScope.stop + 样式表摘除 + 注册表反应式下线 |

新增的审查面：SFC 与 ESM 都是纯静态文本，`adag plugin install` 的 preflight 增加「SFC 内联 script 拒绝 / 标签白名单预扫描 / ESM import 目标限定白名单（`/static/ada/*` 与相对路径）」三条静态检查。

---

## 7. 插件契约变更

### 7.1 manifest（`frontend` 段）

```jsonc
{
  "frontend": {
    "capabilities": ["frontend.script"],       // 不变
    "slots": [
      { "slot": "chat.corner", "component": "PlanCard.vue", "order": 90 }
    ],
    "pages": [
      { "slug": "plan", "component": "PlanPage.vue", "title": "计划面板" }
    ],
    "blocks": [ /* 不变：display profile VNode JSON */ ],
    "nav": [ /* 不变 */ ],
    "actions": { /* 不变：动作桥 + confirm */ },
    "assets": "static"                          // 不变：/ext/{id}/static/
  }
}
```

变更点与退役点：

- `template` → `component`：指向 `.vue` 文件（`hydrate/clientOnly/client/scripts` 字段整体退役——没有 SSR 就没有水合；`frontend.script` 继续作为脚本面的声明门禁，SFC + ESM 都算脚本面）。
- `styles[]` 保留一个过渡期（迁移期旧插件仍用），目标态废弃（scoped CSS 取代）。
- 校验：`component` 必须存在于 `assets` 目录；SFC 解析（含内联 script 拒绝）在安装 preflight 做。
- `spec/manifest.schema.json` 同步改写，`adag verify` 比对口径更新。

### 7.2 插件目录与示例

```
plugins/official/tools-plan/
├── plugin.json
└── static/
    ├── PlanCard.vue      ← 模板 + scoped 样式 + <script src>
    ├── PlanCard.js       ← 逻辑：setup / 状态 / 调 adaAPI
    └── icon.svg
```

```html
<!-- PlanCard.vue -->
<template>
  <div class="ext-plan">
    <header class="ext-plan__head">
      <span class="ext-plan__title">{{ plan.title }}</span>
      <ui-badge :tone="allDone ? 'ok' : 'info'">{{ done }}/{{ plan.steps.length }}</ui-badge>
    </header>
    <label v-for="s in plan.steps" :key="s.id" class="ext-plan__step">
      <input type="checkbox" :checked="s.done" @change="toggle(s)" />
      {{ s.text }}
    </label>
    <button class="ada-btn" @click="refresh">刷新</button>
  </div>
</template>
<style scoped>
.ext-plan { padding: var(--ada-space-3); }
.ext-plan__title { color: var(--ph-text-primary); }
</style>
<script src="./PlanCard.js"></script>
```

```js
// PlanCard.js —— 唯一可执行体，安装时落盘的静态 ESM
import { ref, computed, onMounted, inject } from "/static/ada/ada-runtime.mjs";

export const setup = (props) => {
  const ada = inject("ada");
  const plan = ref(props.data?.plan ?? null);       // 初始 props 投影，细节 §8.4
  const refresh = async () => { plan.value = await ada.api("/state"); };
  const toggle = (s) => ada.action("toggleStep", { id: s.id });   // 动作桥，confirm 文案由 manifest 声明
  onMounted(refresh);
  return { plan, done: computed(() => plan.value?.steps.filter(s => s.done).length ?? 0),
           allDone: computed(() => ...), refresh, toggle };
};
```

对比现状：`slot.tsx`（TSX/moejs/esbuild 三件套）+ `client.js`（预编译岛）+ props 只读投影三条规则收敛为「一个 SFC + 一个 ESM」。

### 7.3 能力与信任

- `frontend.script` 语义不变：声明「本插件有浏览器端可执行面」。SFC 与 ESM 均计入此门禁。
- third-party 与系统插件继续**同面**（`docs/00 §1.5.5` 不变）：第三方插件同样可声明 slots/pages，风险由安装确认 + 静态 preflight + 审计控制。
- 安装确认 UI 补齐（现状缺口⑤）：确认对话框展示「将注入 UI 插槽 ×N、页面 ×N、可执行脚本文件清单 + 字节数」，作为本方案的一部分一并落地。

---

## 8. 宿主装配

### 8.1 Shell 与路由

- Go 只渲染一个 `index.html`（shell 骨架 + CSRF meta + bootstrap script）与 `login.html` / `error.html`（这两个保留服务端渲染，无插件、无框架依赖）。
- 网关对全部页面路径返回 shell（history fallback），由客户端路由接管；`/static/`、`/ext/{id}/static/`、`/events/`、`/ext/{id}/api/`、动作桥仍由现行 handler 服务。
- 自研路由器（~200 行）：路径模式匹配 + 参数、`beforeLeave` 守卫（草稿未保存提示）、滚动恢复；路由表 = 基座静态路由 + 插件页面动态注册。

### 8.2 插槽 outlet

```html
<!-- shell 布局中，12 处 -->
<SlotOutlet name="composer.footer" />
```

```js
// SlotOutlet：读注册表 → 按 order 排序 → defineAsyncComponent(loadSFC) 逐个挂载
const registry = inject("registry");            // reactive Map（§8.3）
const contribs = computed(() => registry.forSlot(props.name));
```

- 插件贡献挂载失败（fetch 404 / 编译错误 / setup 抛异常）→ 错误边界捕获 → 该贡献位置标记 `data-ada-error`，页面其余部分不受影响。
- 每贡献一个独立 effectScope：卸载 = `scope.stop()` + unmount + 样式表摘除。
- 7 个会话内插槽的「无 threadId 不渲染」语义（`threadContextSlots`）改为路由级条件渲染，行为不变。

### 8.3 注册表与生命周期（修掉 P1/P2/P3）

```
GET /frontend-registry        → 当前工作区全部前端贡献（SFC URL 带 ?v= 内容指纹）
GET /events/workspace         → 工作区级 SSE：evt.plugin.changed / evt.ready（新增端点）
```

- 注册表是一次性拉取的响应式 `Map`；`evt.plugin.changed` 推送变更 → 注册表更新 → Vue 式响应链自动完成「插槽消失/页面 404/样式表摘除/路由注销」。
- **运行时装载消灭 P1**：插件前端文件内容变化 ⇒ URL 指纹变化 ⇒ 重新 `loadSFC` 即装载新版本，不需要重启 Gateway；扩展页提供「重载前端」按钮 = 清 `compiled` 缓存 + 重新拉注册表。
- 协议边界声明：`evt.plugin.changed` 事件本体已存在于协议（`domain/event.go`），只是从未推给浏览器；`/events/workspace` 是**网关路由的新端点**，不新增 Core RPC 方法、不改事件序号/快照协议（`docs/08` 冻结项不受影响）。工作区通道的 seq 独立计数、同样支持 Last-Event-ID 补发（复用 `sse/hub.go` 现有机制，配置化第二实例）。

### 8.4 数据层 composables

`provide("ada")` 注入的服务面（插件只经此触达宿主，不直接摸 window/document）：

```js
const ada = {
  api(path, opts)          // = window.adaAPI(pluginId, path, opts)：route 桥，自动 CSRF
  sse(path, handlers)      // = window.adaSSE(...)：插件 SSE route
  action(name, input)      // 动作桥；写动作自动走 adaConfirm（manifest 的 confirm 文案）
  thread                   // 当前线程上下文（threadId/标题；会话内插槽才有）
  confirm(text)            // 统一确认对话框（禁 window.confirm 的既有约定不变）
  storage                  // 插件隔离 localStorage：key 自动前缀 "<pluginId>:"（顺手补齐现状缺口④）
}
```

- 基座会话数据（transcript/流式 delta）改为响应式 store（`useThread()`：`items = ref([])` 由 `evt.item.updated` 驱动），SSE 协议原样，只是消费者从「DOM patch 脚本」变成「响应式 store → 组件 re-render」。
- 初始 props 投影（`app{workspace,product,route}` 等）继续随 shell bootstrap 下发，避免首屏闪烁；`backend.read` 服务端预取绑定退役——数据获取统一由插件 setup 里的 `ada.api` 完成（装配期预取的复杂度不再值得，route 桥本地回环延迟足够低）。

---

## 9. 模型生成内容（不变量）

Markdown `adag-ui` 动态块的整条链路**不动**（`docs/15 §16`）：

```
模型输出 → goldmark + bluemonday → 插件 route 返回块数据
        → 服务端 display profile + VNode 白名单校验（vnode.go，保留）
        → VNode JSON 下发 → 基座「块渲染组件」渲染（无事件、无表单、无 SFC 编译）
```

唯一的实现差异：渲染现场从 Go SSR 变成客户端组件——校验仍在服务端（校验与渲染分离是本来就该有的形态）。display profile 的词汇表与 §5.7 共享同一份 spec。模型内容**永远不会**经过 SFC 编译器，也就永远没有 eval 面。

---

## 10. Go 侧删留清单

**删除**（净删约 4.5k 行 Go + 5.5k 行旧 JS）：

- `internal/gateway/web/templates/` 页面模板（chat/overview/extensions/ext/plugin_detail/workspaces…；保留 shell/login/error）
- `internal/gateway/frontend/render.go`（TSX→VNode→HTML）与 moejs 前端 Render Runtime（64MB/100ms/300ms 预算随之退役）
- `tools/pluginbuild` 的前端打包路径（Go esbuild 仅保留给后端插件 TSX？→ 后端插件无前端 TSX 后，若 backend.tsx 也退役则整目录评估）
- `static/`：`ada-render.js`（3031 行，其中 tokenizer/Pratt/patch 迁入 ada-runtime 后删除）、`ada-ui.js`/`ada-ui-iso.js`、`islands.js`、`plugin-islands.js`、`ada-patch-stream.js`、`composer-draft.js`（并入 store）
- `meta refresh` / 303 PRG 页面流（表单仍 POST，但成功后走客户端导航）

**保留**：`security/`（CSP/CSRF）、`sse/`、动作桥与 route 桥 handlers、`handleExtStatic`（512KB 限额继续约束每个插件文件）、`frontend/vnode.go` 校验器（服务模型内容）、信任/审计/插件启停 RPC、`assetURL` 内容指纹机制。

**新增**：`GET /frontend-registry`、`GET /events/workspace`、shell 装配（`/` history fallback）、SFC preflight 静态检查。

---

## 11. 验证门禁与测试（`adag verify` 更新）

| 门禁 | 变化 |
|---|---|
| slot 覆盖核对 | `CheckSlotCoverage` 从「插槽名 ↔ 宿主模板位置」改为「插槽名 ↔ shell 布局组件位置」 |
| vnode schema 快照 | 保留；词汇表下沉 spec 后两端比对同一份 |
| CSP/无 eval 扫描 | 范围扩大：新增扫描 `static/ada/**`（编译器/渲染器不得含 `new Function/eval/innerHTML` 赋值）与插件 SFC/ESM preflight |
| golden 测试 | HTML golden 退役；改为 (a) 响应式内核契约测试（JSDOM 级纯逻辑，无需浏览器） (b) 模板编译快照（template → 渲染计划 JSON） (c) 少量端到端冒烟（shell 挂载 + 插件插槽 + SSE patch） |
| manifest schema | `component` 字段 + 内联 script 拒绝 + import 白名单 |

---

## 12. 迁移路线

| 阶段 | 内容 | 退出判据 |
|---|---|---|
| **P0** | ada-runtime 内核：响应式 + 组件模型 + 模板编译器 + 渲染器（在独立目录开发，契约测试先行） | 内核契约测试全绿；用内核重写 1 个基座组件（如 `ui-select`）作为试点 |
| **P1** | Shell SPA：index.html + 路由器 + 12 SlotOutlet + 注册表 + `/frontend-registry` + `/events/workspace`；基座页面（overview/extensions/workspaces/会话）迁入组件 | 基座功能无 JS 退化移除后全部可用；SSE 会话流经 store 工作 |
| **P2** | 官方插件迁 SFC：ui-hello-tsx → ui-hello、tools-plan、composer-telemetry、provider-admin、env-manager、mcp-client、cron-manager（scripts 型收编进组件契约，消掉 P5）、token-usage、ui-charts（blocks 不动） | 官方插件零 TSX、零 client.js；旧渲染管线仅剩模型内容块在用 |
| **P3** | 模型内容块客户端渲染 + 退役 Go SSR 管线与 moejs Render Runtime；删除页面模板与旧静态 JS | `internal/gateway/frontend` 只剩 vnode.go 校验；文档改写（00/07/14） |
| **P4** | 第三方开放：SFC preflight、安装确认 UI、`adag plugin install` 静态扫描、`storage` 隔离、可选强化评估（iframe 沙箱） | 第三方插件全流程走通；`adag verify` 新门禁全绿 |

P2 期间新旧并存（旧插件继续走桥渲染、新插件走 SFC），以插件为单位灰度；P3 收口后删旧管线。

---

## 13. 风险与开放问题

| # | 风险/问题 | 处置 |
|---|---|---|
| R1 | **自研框架的工程量与质量**（响应式边界 case、patch 对齐、指令语义）——最大风险 | P0 契约测试先行；内核收敛最小子集（明确不支持的指令/能力列表写进文档）；现有 ada-render.js 的 tokenizer/parser/patch 是已验证的底子，增量集中在响应式与组件层 |
| R2 | 首屏依赖 JS，无网络/禁 JS 全盲 | 接受（§2 非目标）；login/error 保留无 JS 页面；Electron 场景无真实退化 |
| R3 | 表达式解释器性能 | 渲染计划缓存 + 静态提升；热点路径留「闭包直连」优化后路；性能预算沿用 `docs/10` 精神（插槽交互 p95 目标不变） |
| R4 | scoped CSS + adoptedStyleSheets 的浏览器兼容 | 目标浏览器 = Chromium（Electron）+ 现代桌面浏览器，均支持；验收清单显式验证 |
| R5 | 插件 ESM 与基座同源的残余风险（不变项） | 维持 `docs/09` 立场；安装确认 UI（本提案补齐）；iframe 沙箱作为可选强化单独立项 |
| Q1 | 后端插件 TSX（`slot.tsx` 等）是否也退役，`tools/pluginbuild` 整体去留 | P2 时评估；倾向后端模板收敛为 Go 端已知组件描述（或 JSON 数据 + 前端组件），后端不再跑 TSX |
| Q2 | 插件逻辑是否需要 TS | v1 纯 JS ESM；若要 TS，唯一无 eval 路线是安装期用 Go esbuild 转译（有构建但只在 install 一次），单独立项 |
| Q3 | `/events/workspace` 的补发窗口与心跳参数 | 复用 hub 配置化第二实例，默认值与线程流一致 |
| Q4 | 会话页整页 reload 语义（终态/等待输入）改为状态驱动后的行为差异 | P1 中逐条对照 `chat-live.js` 的 reload 触发点，改为 store 状态机；交互托盘外壳不可替换的硬规则（`docs/00 §1.5.7`）不变 |
