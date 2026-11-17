# 前端运行时、组件与 UED

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§14 运行时、§15 组件层、§19 UED 规范  
> 版本：0.3 · 2026-11-17  
> 本文合并原 14/15/19 三份文档；章节编号保持不变，代码注释里的 §14/§15/§19 引用继续有效。

> **架构状态（2026-11-11 起）**：页面由自研运行时 `internal/gateway/web/static/ada/`
> 在浏览器渲染；Go 只输出 shell、登录页与错误页。没有服务端页面渲染、没有水合。
> 插件前端是运行时装载的 SFC 单文件组件。前端架构与插件契约见
> [07-frontend.md](07-frontend.md)。

## 14. 前端运行时（static/ada）

### 14.0 结论

浏览器是唯一渲染现场。一个 shell + 一组原生 ESM 模块完成布局、路由、组件、SFC 装载
与数据订阅：

```
Go shell（#app + bootstrap JSON + CSRF meta）
   └─ /static/ada/index.mjs（shell app：顶栏 / 侧栏 / 页面容器）
        ├─ reactivity.mjs    ref / reactive / computed / effect / effectScope / watch
        ├─ compiler.mjs      模板 → 渲染计划（闭包树），手写表达式解释器
        ├─ runtime.mjs       VNode、diff patch、defineComponent、provide/inject
        ├─ router.mjs        history 路由、参数、beforeLeave、滚动恢复
        ├─ sfc.mjs           .vue 解析 / 编译 / 缓存 / scoped CSS
        ├─ host.mjs          bootstrap、CSRF fetch、注册表、SlotOutlet、ada 服务面
        └─ whitelist.mjs     标签/属性/URL 白名单（与 spec 同步）
```

数据流：状态 → 响应式 → 组件 re-render → VNode diff patch。模型生成内容不走 SFC
编译器：服务端返回 VNode JSON，浏览器只做白名单渲染。

### 14.1 约束与边界

| 约束 | 来源 | 对设计的影响 |
|---|---|---|
| CSP `script-src 'self'`、无 `unsafe-eval` | 09 §9.3 | 模板不编译成代码字符串；表达式解释执行；无 `eval` / `new Function` |
| 无内联脚本 | CSP + 安装期审计 | SFC 的 `<script>` 必须 `src` 指向静态 ESM；内联 script 直接拒绝 |
| 无 `v-html`、无内联 `style="..."` | VNode 白名单 + UED | 文本一律 VNode 文本节点；样式走类名/令牌 |
| 无 npm 构建链 | 单二进制发布 | 运行时只用原生 ESM；插件 SFC 零构建 |
| 模型内容与插件内容分层 | 15 §16 | display profile 只接受白名单 VNode；SFC 只服务插件 UI |

明确不做：服务端页面渲染、水合、客户端框架替换、`v-html`、动态组件名
（`:is` 只接受注册表内名字）、`style` 字符串属性。

### 14.2 VNode 契约

VNode 是运行时唯一的 DOM 结构表达（与 `spec/vnode.schema.json` 一致）：

```
元素   { t: "div", p: { class: "x" }, c: [VNode|文本] }
组件   { t: "$C", n: "Todo", p: {…}, c: [...] }
文本   { t: "#text", v: "…" }
```

- 属性白名单：`class`、`id`、`href`、`src`、`alt`、`title`、`aria-*`、`data-*`、
  事件 `on:*`（仅插件 SFC；模型内容禁用）等；
- URL 属性过 `validateURL`：拒绝 `javascript:` / `vbscript:` / `data:text/html`；
- 限额：深度 64、节点 2000、单字符串 32KB、总输出 256KB（`internal/gateway/frontend/vnode.go`，
  `adag verify vnode` 逐条比对）；
- 服务端与客户端共用同一份词汇表：Go 校验模型内容，浏览器校验插件 SFC。

### 14.3 运行时模块

| 模块 | 职责与要点 |
|---|---|
| `reactivity.mjs` | Proxy 依赖收集；`computed` 惰性缓存；`effectScope` 收集子 effect，`stop()` 一刀切；调度队列微任务批量 flush |
| `compiler.mjs` | tokenize/parse → 模板 AST → 渲染计划；`v-if/v-else/v-show/v-for/:bind/@event/v-model/v-text`；表达式解释器禁 `constructor` / `__proto__` / `prototype`，全局白名单 `Math/JSON/Date/Number/String`；禁 `window/document/fetch` |
| `runtime.mjs` | `createVNode` / `h` / `patch`；`defineComponent`（`props` / `emits` / `setup` / `render`）；`provide/inject`；`onMounted/onUnmounted/onErrorCaptured`；`defineAsyncComponent`；`makeSfcComponent` |
| `router.mjs` | history API；路径模式与参数；`beforeLeave`；滚动位置；无 `window` 时可用于测试 |
| `sfc.mjs` | `parseSFC`（`template/style/script src`）；`compileScopedCss`；`loadSFC`（URL + 内容指纹缓存）；拒绝内联 script |
| `host.mjs` | `readBootstrap`；`makeFetcher`（CSRF/错误）；`createRegistryStore`；`SlotOutlet` / `PluginScope`；`makeAda` 服务面 |
| `index.mjs` | shell 布局、设置弹窗、图片查看器、页面装载、`ui-*` 注册 |
| `whitelist.mjs` | 客户端白名单数据源（`adag verify` 与 Go/schema 比对） |

### 14.4 API 速查

```js
import {
  ref, reactive, computed, watch, effect, effectScope, nextTick,
  defineComponent, h, provide, inject,
  onMounted, onUnmounted, onErrorCaptured,
  defineAsyncComponent,
} from "/static/ada/ada-runtime.mjs";
```

- `setup(props)` 返回普通对象/`ref`/`computed`，即为模板绑定作用域；
- `provide/inject` 沿组件树传递宿主服务（`ada`、路由、线程上下文）；
- 每个插件贡献挂在自己的 `effectScope`；卸载 = `scope.stop()` + 样式表摘除；
- 插件通过 `inject("ada")` 使用 `api` / `sse` / `action` / `thread` / `confirm` /
  `storage`，不直接摸 `window` / `document`。

### 14.5 模板语法

- 指令：`v-if` / `v-else` / `v-show` / `v-for` / `:bind` / `@event` / `v-model` /
  `v-text`；修饰符 `.prevent` / `.stop` / `.once` 编译期展开；
- 不支持：`v-html`、`v-pre`、动态组件名、`<slot>` 标签（插槽内容由宿主组件以
  children 传入）；
- 事件是插件 SFC 的一等公民；模型内容 VNode 仍然无事件；
- 表达式作用域 = setup 返回值 + props + 白名单全局；禁止 `constructor` /
  `__proto__` / `prototype` 与浏览器全局。

### 14.6 组件、插槽与错误边界

- `SlotOutlet`（模板名 `ui-slot`）从注册表按 `order` 取贡献，逐个
  `defineAsyncComponent(loadSFC)` 装载；
- 每个贡献一个独立错误边界：装载/编译/setup 失败 → 该位置标 `data-ada-error`
  并留空，其余页面不受影响；
- 插件停用或路由离开：`scope.stop()` + unmount + 样式表摘除；内容指纹变化时
  重新装载新版本，不需要重启 Gateway；
- 7 个会话内插槽（`chat.corner`、`composer.left/right/above/footer`、`thread.tray`、
  `transcript.after`）由路由级条件控制，只有会话上下文时渲染。

### 14.7 SFC 装载与 scoped 样式

```html
<!-- PlanCard.vue -->
<template>
  <div class="ext-plan">
    <span class="ext-plan__title">{{ plan.title }}</span>
    <button class="ada-btn" @click="refresh">刷新</button>
  </div>
</template>
<style scoped>
.ext-plan { padding: var(--ada-space-3); }
</style>
<script src="./PlanCard.js"></script>
```

- `<script>` 只能以 `src` 指向同目录 ESM（安装时落盘的静态文件）；内联脚本在
  `parseSFC` 阶段报错；
- `loadSFC` 缓存键 = URL + 文本内容指纹；`?v=` 内容变化触发重新装载；
- scoped 样式：编译期给选择器加 `[data-v-x]`，注入走 CSSOM
  （`new CSSStyleSheet().replaceSync()` + `document.adoptedStyleSheets`），不受
  `style-src 'self'` 影响；
- 清单 `styles[]` 仍支持插件级全局 CSS（运行时按内容指纹挂同一张 CSSStyleSheet）。

### 14.8 SSE 增量

- `GET /events/{id}`：Core 事件名 `evt.message.delta` / `evt.item.updated` /
  `evt.run.status` / `evt.interaction.requested|resolved` / `evt.error` / `evt.notice`；
  序号断裂时发 `resync`，客户端重拉 transcript/state 全量对齐；
- 40ms 窗口合并；心跳 15s；`Last-Event-ID` 连续补发、断裂发 `resync`；每线程一个 hub，
  单线程最多 64 个客户端；
- `GET /events/workspace`：`evt.plugin.changed` / `evt.ready` → 重拉
  `/frontend-registry`，插槽/页面/导航即时增减；
- 浏览器侧把事件写进响应式 store，由组件 re-render，不做 DOM 字符串替换。

### 14.9 安全

| 面 | 屏障 |
|---|---|
| 插件 SFC | 编译期标签/属性白名单 + 补丁期 URL/属性校验；内联 script、`v-html`、`eval` 直接拒绝 |
| 模型内容 | 服务端 goldmark/bluemonday + VNode display profile 校验；客户端只渲染校验后的 JSON |
| 数据面 | 动作桥/route 走会话、CSRF、Origin、插件状态校验；响应 `no-store`；route 单响应 ≤64KB |
| 脚本面 | `frontend.script` 能力 + 信任级别 + CSP；可执行代码只能是安装时落盘的静态文件 |
| 审计 | 插件启停、动作调用、SFC 装载失败均记录；事件序号/快照/错误码契约不变 |

### 14.10 性能

- 模块按需加载；SFC 编译结果按内容指纹缓存，重复挂载不重复编译；
- 路由切换只 re-render 目标页面；会话流增量只 patch 变化行；
- 插件停用立即摘除样式与 effect，不累积监听；
- 未做虚拟列表：长会话由 transcript 分页 + SSE 增量维持，超长列表的代价在
  可接受范围（后续可加窗口化，不需要改契约）。

### 14.11 测试与验收

```bash
# 运行时/组件/SFC/路由器/注册表/页面冒烟：12 个测试文件，134 个用例
node --test internal/gateway/web/static/ada/test/*.test.mjs

# 官方插件 dist 与 TS 源码同步（构建产物带内容指纹）
node plugins/official/build.mjs --check

# Go 侧静态资源/契约门禁
go test ./internal/gateway/web/ ./internal/gateway/frontend/
go run ./cmd/adag verify
```

render-demo 一类的旧浏览器冒烟页已删除：运行时行为由 `node --test` 的 DOM shim
覆盖，真实浏览器验收走网关启动后的页面走查。

### 14.12 已知限制

- 没有无 JS 回退：SPA 页面依赖 `index.mjs` 与浏览器能力；登录/错误页是仅有的
  无 JS 安全页；
- 浏览器需支持 `CSSStyleSheet` / `adoptedStyleSheets`（Chromium/Firefox 现行版本）；
- 模板表达式是解释执行，热点贡献可后续加闭包直连优化，但不得引入 `eval`；
- 插件可执行面只能是静态 ESM，禁止内联脚本与 `blob:`/`data:` 代码；
- `ui-*` 组件刻意保持小集合，复杂交互由插件用原生标签 + `ada-*` 类实现。

### 14.13 与 07 的关系

[07-frontend.md](07-frontend.md) 定义前端架构与插件契约（shell、路由、注册表、插槽、
动作桥/route/SSE）；本文定义运行时内部、组件层与 UED 规范。两者冲突时以 07 的
契约面与本文的安全/样式纪律共同为准。

## 15. 宿主 UI kit 与组件层

### 15.0 定位

`ui-*` 是宿主注册给所有 SFC 模板的组件前缀（`index.mjs`），插件模板可直接使用；
不在 kit 里的交互优先用原生标签 + `ada-*` 类，而不是再造一套组件。模型内容
（display profile）不使用 SFC 模板，只由 `ui-markdown` 按 VNode JSON 渲染。

### 15.1 样式加载顺序

shell 模板固定加载顺序：

```
tokens.css → ui/themes.css → app.css → ui/components.css → ui/layout.css
→ ui/widgets.css → ui/shell.css → ui/pages.css → ada/icons.css
→ ada/shell.css → app-local.css → ada/console.css
```

后加载的层只做外观覆盖；`adag verify ui` 校验 token/CSS 基线。插件 `styles[]` 与
SFC scoped 样式都在基座层之后挂载。

### 15.2 用法

```html
<ui-icon name="i-send" :size="16" />
<ui-badge tone="ok" label="已完成" />
<ui-panel title="详情"><p>正文</p></ui-panel>
<ui-button kind="primary" @click="save">保存</ui-button>
<ui-empty text="暂无内容" />
<ui-code :text="source" lang="ts" />
<ui-slot name="composer.footer" />
```

### 15.3 组件清单

| 组件 | Props | 说明 |
|---|---|---|
| `ui-icon` | `name`（`i-send` 或 `send`）、`size` | CSS mask 单色图标，跟随 `currentColor`；集合见 `ada/icons.css` |
| `ui-markdown` | `nodes`、`text`、`mdClass` | 模型正文 VNode 渲染；`text` 是流式未到 AST 前的纯文本占位 |
| `ui-badge` | `tone`、`label` | 状态徽标；tone 由页面派生，颜色仍走 `--ph-*` |
| `ui-button` | `kind`（`default`/`primary`/…）、`disabled` | 原生 `<button>` + `ada-btn` 类 |
| `ui-panel` | `title` | 折叠/详情面板的容器外壳（开合由 `<details>` 或页面控制） |
| `ui-empty` | `text` | 空态说明 |
| `ui-code` | `text`、`lang` | 客户端语法高亮，token class 对齐 chroma；不碰 `innerHTML` |
| `ui-slot` | `name` | 12 插槽 outlet；按注册表装载 SFC，失败留空 |

基座还提供确认对话框（`ConfirmDialog`）与文件选择器（`file-picker.js`），插件不得
用 `window.confirm` / `alert` / `prompt`。

### 15.4 DOM 与样式纪律

- 类名统一 `ada-` 前缀；状态用 `data-*`（`data-state="open"`），不拼条件 class 串；
- 颜色只引用 `--ph-*`，尺寸/圆角只引用 `--ada-*` 或 `console.css` 的 `--c-*`；
  不写十六进制色、奇数尺寸、`dark:` 变体、内联 `style` 布局；
- 下拉用原生 `<select>` + `ada-input` 类；需要自定义弹层时沿用 `.ada-select` / `.ada-modal` 形态（触发器 + 浮层 + 焦点圈 + Esc）；`adag:components` 只服务 Markdown 动态块，不是 SFC 组件库；
- 焦点态统一 2px 品牌描边 + 半透明光环；所有可点击元素有 `:focus-visible`；
- 图标 14–18px、stroke 1.75、`currentColor`；不用 emoji 当图标；
- 滚动条槽位 6px，`scrollbar-gutter: stable`，`overscroll-behavior: contain`；
- 主题 = 明暗 × 色板两维正交，`themes.js` 首帧落 `data-theme`；`--ph-glass` 只在既有
  位置使用，不做装饰玻璃。

### 15.5 无障碍

- 语义标签：`<main>`、`<nav>`、`<form>`、`<button>`；消息流 `aria-live="polite"`；
- 键盘：Tab 顺序自然；浮层打开焦点进入、Esc 关闭并回归触发元素；确认框默认聚焦
  “拒绝”这一安全选项；
- `prefers-reduced-motion` 下关闭过渡/自动滚动/呼吸动画；对比度正文 ≥4.5:1、
  大字/图标 ≥3:1。

### 15.6 测试

- `node --test static/ada/test/`：reactivity、compiler、runtime、router、sfc、
  host、页面冒烟、侧栏与插件 SFC 装载；
- `adag verify ui` 校验 token/CSS/DOM 基线；`adag verify vnode` 校验客户端与
  服务端白名单一致；
- 浏览器走查覆盖 Light/Dark 与 1600/1280/1024/860/760/500 宽度。

### 15.7 与 19 的关系

§15 定义组件与 DOM 的工程纪律；§19 定义视觉与交互规范。两者共同约束插件 SFC
与基座页面，冲突时以 §19 的品牌/无障碍标准为准。

## 19. UED 规范（用户体验设计系统）

### 19.1 设计原则

1. **浏览器渲染优先**。页面由 SPA 运行时渲染；只有登录/错误页是无 JS 安全页。新交互的验收顺序是键盘/焦点/ARIA/失败方向，不承诺 noscript 回退。
2. **令牌单源**。颜色只引用 `--ph-*`，尺寸/字号/圆角只引用 `--ada-*`，动效只引用动效令牌。业务 CSS 零字面值（十六进制、px 即兴值、裸秒数）。
3. **克制**。单色品牌、无渐变、无发光、无彩虹标签（沿 07 §7.7）；动效只做状态反馈，不做表演。
4. **文字先行**。一切文字（含 10px 小字）按对比度 ≥ 4.5:1 验收；界面以中文全角标点、解释型文案为常态。
5. **键盘与读屏一等公民**。所有可聚焦元素有可见焦点环；弹层遵守四要素（19.5.1）；图标 `aria-hidden`，动态区域有 live 语义。

### 19.2 品牌规范

#### 19.2.1 命名体系

ada 词根统一，四种写法各有边界，**不得混用**：

| 写法 | 用途 | 出现位置 | 禁止 |
|---|---|---|---|
| `ada-go` | 用户可见的产品/品牌名 | UI 品牌位、页面标题、产品声明 `identity.name`、文档 | 加后缀变体（ada-go-GO 等） |
| `adag` | 二进制与开发者命名空间 | CLI（`adag verify`）、cookie（`adag_js`/`adag_workspace`）、`adag-ui` 动态块、`data-adag-*` | 出现在用户可见文案 |
| `a_da` | 仅指代上游 Rust 项目 | 代码注释、NOTICE、来源头 | 用户可见 UI 与文档正文 |
| `ada-` | CSS 类名/文件前缀 | `.ada-btn`、`ui/*.css` | — |

大写 `Ada` / `Adag` 禁用（现有 UI 未出现，保持）。产品徽标（`Coding`/`Headless`/`Skeleton`）由 `productBadge()` 自动拆分，不手写。

#### 19.2.2 品牌主题与多主题体系

- **默认色板为「蓝海 azure」**（2026-11-16《AI Agent · 桌面客户端》重设计）：冷灰纸面 `#F2F3F5` × Element Plus 蓝 `#1D4ED8`，白面卡片 + 发丝线 + 四级文本灰；暗色为冷夜反转。`tokens.css` 仍是 Black Swan 基线（不设 `data-theme` 即黑天鹅），启动引导 `themes.js` 在首帧前把缺省落到 `data-theme="azure"`，两条路径结论一致。
- **主题 = 品牌衍生色板**。调色板维 `html[data-theme="<id>"]` × 明暗维 `html.light`/`html.dark`，两维正交；主题层（`ui/themes.css`）选择器用 `html.light[data-theme="…"]`（特异性恒胜基线），只覆盖 `--ph-*`，不新增选择器形态。
- 品牌底上的文字/图标一律 `--ph-on-brand`，禁止写死 `#fff`/`#000`；状态色成对使用「浅底 + 深字」（`*-soft` 配 `*-ink`），不能只换文字色。
- 主题（调色板）不是「皮肤商店」：新增主题必须走 19.3.3 准入清单，且风格仍须符合「纸面 × 单色品牌」的品牌气质。
- 色板：`azure`（默认，蓝海）、`black-swan`（黑天鹅，暖纸墨黑）、`moss`（苔绿，承接 M2 绿色身份）、`ocean`（深海）、`dusk`（暮色）。
- 控制台外观层为 `static/ada/console.css`（`c-*` 方言，最后加载）：48px 顶栏、288px 冷灰侧栏、800px 会话列、设置弹窗；只引用 `--ph-*`/`--ada-*`，不写十六进制。

#### 19.2.3 术语与文案

**术语表**（词频现状收敛后的钉定值）：

| 术语 | 含义 | 用法 |
|---|---|---|
| 会话 | thread | **唯一主名词**：列表、标题、删除、搜索、空态 |
| 任务 | 一轮工作的发起心智 | 仅限「新建任务」入口与其快捷键提示 |
| 工作区 | workspace | 稳定术语，不变 |
| 对话 | 仅动词性用法 | 「去对话页新建」这类引导句；不再作名词 |
| agent | 不译，小写 | 可直接入句 |

已知的存量混用（待收敛，见优化方案批次 C）：`返回对话/返回会话` → 统一「返回会话」；`还没有会话/暂无会话` → 统一「还没有会话」。

**语气**：
- 称呼「你」，无敬语、无感叹号；全角标点；省略号用 `…`；引用界面词用「」。
- 按钮 2–4 字；空态标题 6–10 字；说明句允许「解释型长句」，但必须交代边界或原因（范本：offline 页「已加载的页面不会伪造数据。」）。
- 错误文案必须给出下一步动作（做什么或点什么），不出现裸错误码当正文（错误码放详情区）。
- 确认弹窗必须说清后果与不可逆性（范本：「删除会话「%s」？删除后无法恢复。」）。

#### 19.2.4 动效语言

- 交互反馈（hover/active/focus/开关）一律 `var(--ada-transition)`：120ms `cubic-bezier(0.4, 0, 0.2, 1)`。
- 面层级出入（抽屉、弹层、flash）用新增时长令牌：`--ada-duration-fast: 160ms`、`--ada-duration-slow: 240ms`，easing 同上。禁止再出现 140/150/180/200ms 裸值（存量见优化方案批次 C）。
- 所有 keyframes 必须配 `prefers-reduced-motion` 降级（现状已全部做到，新增不得破例）。
- 允许的动效：状态变化反馈、出现/消失、进行中暗示（呼吸、旋转）。装饰性循环动画禁止。

#### 19.2.5 视觉资产

- Logo：单一 `logo.png`（512×512）为源；使用档位 24/28/32/40px，HTML 属性写死尺寸；暗色画布直接复用（logo 本身黑白）。
- 视觉资产（批次 C 已补）：`favicon.png`（32px，logo 面积平均下采样）与 `apple-touch-icon.png`（180px）；不再用 512px 原图当 icon。
- 图标：只经 `ui.go iconDefs` 的 sprite 通道（24 viewBox、stroke 1.75、`currentColor`、`fill:none`）；新图标先进 `iconDefs` 再用；`i-more` 的 stroke 3 为唯一既有例外。

### 19.3 设计令牌体系

#### 19.3.1 层次与真源

| 层 | 文件 | 职责 | 治理 |
|---|---|---|---|
| 颜色基线 | `static/tokens.css` | `--ph-*` 唯一真源，Black Swan 明暗两套 | **上游镜像**（a_da commit + SHA-256），本地只读 |
| 主题层 | `static/ui/themes.css` | 衍生色板：`html.light/dark[data-theme]` 块 + 色板预览 `--sw-*` | 本地拥有；新主题走 19.3.3 |
| 组件层 | `static/ui/*.css` | `--ada-*` 标尺（圆角/字号/图标/间距）+ 组件样式 | 尺寸标尺偶数网格（2 的倍数），奇数档不收 |
| 覆盖层 | `static/app-local.css` | SPA 本地细节 | **禁止重定义**任何既有 `--ph-*`/`--ada-*`（M2 双源教训）；本地**新增**令牌（上游标尺没有的，如 `--ada-duration-*`）集中在本文件 `:root` 定义并登记进 §19.3.2 |

#### 19.3.2 令牌清单

`--ph-*` 语义（完整值见 tokens.css / themes.css）：

- 画布与面：`canvas`（页底）、`surface`（卡）、`elevated`（浮层）、`subtle`（弱底）、`subtle-ink`
- 线：`line`、`line-strong`
- 文字：`ink`（正文）、`muted`（次要）、`faint`（辅助；10px 小字也按 4.5:1 验收）
- 品牌：`brand`、`brand-strong`、`brand-soft`、`brand-ink`、`on-brand`
- 状态：`success-soft/ink`、`warn-soft/ink`、`danger-soft/ink`
- 代码：`code-bg/ink`、`inline-code-bg/ink`、`syn-keyword/string/number/comment/function/type`
- 系统：`focus`、`glass`、`shadow`、`scrollbar`（+ `scrollbar-hover/active`、`scrollbar-size`）
- 动效时长（本地新增，定义在 app-local.css `:root`）：`--ada-duration-fast: 160ms`（面层级出入）、`--ada-duration-slow: 240ms`（抽屉/遮罩）；交互反馈直接用 `--ada-transition`，不新增时长档

新令牌准入：必须同时补 light + dark、登记进本清单、`themes.css` 全部色板同步补齐，否则该令牌不许被组件引用。

#### 19.3.3 主题（色板）准入清单

新增一个色板必须全部满足：

- [ ] 19.3.2 清单中全部 `--ph-*` × 明暗两套齐全，`color-scheme` 正确；
- [ ] 对比度抽查达标：`faint/canvas`、`muted/surface`、`on-brand/brand`、各 `syn-*/code-bg` ≥ 4.5:1（大字/图标 ≥ 3:1）；
- [ ] 缺省态零影响：不设 `data-theme` 时渲染与黑天鹅逐像素一致；
- [ ] 色卡预览三色 `--sw-canvas/--sw-brand/--sw-ink` 与主题值同步（同文件内定义）；
- [ ] 桌面壳 `desktop/main.cjs` 的 `backgroundColor` 若画布值变化则同步；
- [ ] 8 态截图走查（每色板 × 明暗）：主按钮、代码块语法高亮、滚动条、弹窗、交互托盘；
- [ ] 登记进 19.2.2 色板清单与主题面板（`ui.go` 色板表）。

### 19.4 组件与 DOM 规范

- 类名 `ada-` 前缀；状态用 `data-*` 属性或 `is-*` 类表达，不拼条件 class 串；Web Component 刻意不挂 shadow root（样式与 a_da 同树）。
- **标题层级**：一篇页面只有顶栏一个 `<h1>`（页面标识）；正文页面标题用 `<h2 class="ada-page-title">`，节标题 `<h2 class="ada-section-title">`。二级页历史上有过「顶栏 h1 + 正文 h1」并列，读屏按标题跳读会听到两遍同一页名，已统一。
- 二级页（overview/extensions/workspaces/login/plugin_detail）已统一到 `ada-*` 组件方言（批次 D 完成）：页面骨架用 `ada-page-title/ada-section-head/ada-panel/ada-chip/ada-table/ada-empty-card/ada-link`（app-local.css）。`app.css` 里的基线类与工具类是**冻结的存量**：不许新增使用；新页面一律走 ada-* 方言。
- **重设计批次的例外**：Token 用量 / 插件 / 概览 / 插件详情四页在 `static/ui/pages.css` 用页面专属前缀（`tu-* / ex-* / ov-* / pd-*`）。原因是 `app-local.css` 里存量的页面级规则（`ada-plugin-*` 等）加载在 `pages.css` **之后**、特异性更高，沿用旧类名会被静默覆盖（搜索框「框中框」就是一个实例）。新前缀只用于页面局部结构，颜色仍只取 `--ph-*` 令牌、尺寸/圆角仍只取 `--ada-*` 令牌；宿主内置页的 SFC 与插件 SFC 必须共用同一套类，避免出现两套 DOM 结构。
- **插件页共用一套页面语言**：插件自带的管理页同样用自己的页面前缀（`pm-*` 供应商 / `em-*` 环境变量 / `mc-*` MCP 服务端 / `cm-*` 定时任务 / `tu-*` Token 用量），但结构一律照同一套模板：页头（标题 + ⓘ tooltip + 操作）→ 汇总条（数量 + 状态筛选分段 + 最后刷新）→ 卡片化列表（表头与数据行共用一套网格）→ 底注。一次性反馈走底部 toast，长说明收进 ⓘ，警告用左侧色标提示条而不是满宽告警边框；新增插件页照这套来，不另起控件形态。
- **浮层卡片的玻璃态**：`chat.corner` 的 Todo 卡片默认不透明；仅在浏览器支持 `backdrop-filter` 时降到半透明底 + 背景模糊（收起态更透、展开态稍实），`prefers-contrast: more` 下回到不透明。「半透明但没模糊」会让文字与底下正文叠在一起，不能作为降级方案。
- **焦点环**：2px 品牌描边 + 3px 半透明光环（`.ada-focusable` 语义）；`button/a/[tabindex]` 有兜底，但**每个新增可聚焦元素（尤其 `<summary>` 触发器）必须显式进入焦点清单**——键盘 Tab 可达且可见。
- CSP `style-src 'self'`：模板与 JS 禁止内联 style 属性（`adag verify` 把守）；动态预览色一律走 CSS 变量类（如 `.ada-swatch--moss`）。
- 表单：Enter 提交必须处理 IME（`isComposing` 必拦）；破坏性操作必须走 `data-confirm`，后果句见 19.2.3。
- **提交态**：整页 POST 的表单在提交后必须有可见反馈与防重复——表单打 `data-submitting` + `aria-busy="true"`，提交按钮降透明度、`cursor:progress`、禁二次点击（`pointer-events:none`），**不用 `disabled`**（`disabled` 的 submitter 不参与表单数据，`name="decision"` 这类按钮的值会丢）。`data-confirm` 拦下的提交不进入提交态，等用户确认后 `requestSubmit()` 再来一次；bfcache 恢复时（`pageshow.persisted`）必须复位。
- **输入草稿**：主 composer（新建任务与会话页）的未发送文本按 `data-draft-key` 存 `localStorage`（`a_da_draft:<key>`），整页刷新/热更新后恢复，提交成功或清空输入时删除；图片是 `File` 对象，不进文本草稿。新增 composer 必须带唯一的 `data-draft-key`。
- **会话行右键**：侧栏会话行提供「复制会话 ID / 归档会话」；运行中的会话禁用归档。归档只从列表隐藏，不软删历史；当前会话归档后回首页。模型/工作区选择随 `state.db` 持久化，刷新或 Core 重启后保持。
- **待发送队列**：运行中发送的消息先进入输入框上方的 outbox 浮窗，不立即 `turn.steer`；每条提供编辑 / 立即发送 / 删除（纯图标按钮），队列按会话存 `localStorage`，刷新不丢；当前轮正常 `completed` 时自动发队首，其余继续排队。带图片的消息仍走原生立即发送（`File` 不能跨刷新保存）。
- **输入区指标**：输入框下方只放只读信息（缓存命中率、上下文占用环），不再重复放「中止」；中止只在顶栏。这块整体由 `composer-telemetry` 插件的 SFC 贡献（`composer.footer`）渲染：基座只保留 `#composer-aux` 外壳，不画内容——插件停用或装载失败时该区整块空白（贡献失败留空，刻意的取舍）。运行/上下文来自基座通用投影 `GET /threads/{id}/state`（`run` / `context`），token/缓存/成本来自插件自己的 `GET /ext/composer-telemetry/api/usage`（`events.read` 原始行聚合）；组件挂载时拉一次，运行中每 2s 轮询、进入终态即停。上下文占用按 head 路径 entry token 估算，带「约」标注；缓存命中率 = cacheTokens / inputTokens。上下文环本身是 `<details>` 触发器：点开/键盘展开后给出三段分项（系统提示词 / 工具定义 / 对话消息）与合计占比，三段都用引擎同一套估算（引擎水位与界面占比不许各算一套），并在地到压缩水位时给出提示；分项只读，不放任何可点操作。

### 19.5 交互模式规范

#### 19.5.1 弹层四要素

一切弹层（modal、下拉、菜单、抽屉、面板）必须同时具备：

1. **点外关闭**（遮罩点击或 document 委托）；
2. **Esc 关闭**，就近原则且 modal 优先——低层弹层的 Esc 处理必须先探测 `AdaComponents.modalOpen()` 让路；
3. **焦点管理**：modal = Tab 陷阱 + 关闭后焦点归还触发器；非模态弹层 = 关闭后触发器保持可见焦点或合理落点；
4. **互斥**：同屏至多一个非模态弹层（打开 A 时先关 B）；modal 可与非模态弹层共存但位于其上。

存量偏差已收敛：图片查看器补 Tab 陷阱、侧栏抽屉补 inert 与焦点归还（批次 B 完成）；`ada-plus` 与行菜单的同开互斥在批次 C 清理时统一处理。

#### 19.5.2 快捷键

| 键 | 行为 | 让路规则 |
|---|---|---|
| `Ctrl/Cmd K` | 聚焦会话搜索 | modal 打开时不抢焦点 |
| `Ctrl/Cmd N` | 新建任务 | 输入中不拦截（composer 有焦点时不触发）；浏览器保留键仅在 Electron 壳可靠，Web 端提示不承诺 |
| `Esc` | 就近关闭最上层浮层 | 见 19.5.1 |

新增快捷键必须登记进本表并遵守让路规则；不建快捷键帮助面板（快捷键总量控制在个位数）。

#### 19.5.3 通知与错误

- 三种形态的分工：**toast/flash** = 页面级操作结果（自动消失，错误 4.2s / 普通 2.4s）；**inline** = 弹窗/表单内部错误（不消失，带修复动作）；**流内错误卡**（`.ada-run-error`）= 轮次终态失败（含错误码详情）。
- 全局通知只有一条实现路径：SPA 的 toast 服务（`index.mjs` 的 `toastService`，fetch/onFlash 与插件共用）+ 登录/错误页的服务端 flash。
- **反馈观感统一**：宿主 flash（`.ada-flash[data-flash]`）与各插件页 toast（`tu-/mc-/pm-/em-/pd-toast`）是同一套「底部居中深色胶囊」——`bottom:28px`、`--ph-ink` 底 / `--ph-canvas` 字、错误态 `--ph-danger-ink`、`--ada-radius-md` 圆角、`min(460px, 100vw-40px)` 宽度、160ms 由下往上。flash 的 `data-flash` 属性与样式保持不变；退出动画必须保留 `translateX(-50%)`，否则淡出瞬间会水平跳位。关闭按钮视觉收起但保留 DOM，`:focus-visible` 时重新显现，保证键盘可达。
- 连接类提示必须带动作按钮（「重新连接」），不得只说「请刷新页面」。

#### 19.5.4 加载与空态

- 空态三件套：图标 + 标题（6–10 字）+ 说明句；可行动的空态加行动点（范本：chat 空态）。
- 加载：首屏由 shell 与运行时按需取数（无骨架屏）；局部刷新用行内 spinner 或文本占位；插槽内轮询在用户焦点/悬停于该区域时跳过该轮。
- 局部刷新失败不得静默：至少 notify 一句；会话历史截断必须显式提示或提供「加载更早消息」。

#### 19.5.5 分步表单（ask-wizard 模式）

一题一步；非末步 Enter = 下一步而非提交；未到达步骤的字段必须 `disabled`（不参与提交）；步进后自动聚焦当前字段。末步真正提交前，客户端先把全部步骤重新启用——`disabled` 控件不随表单提交，只带末题会让服务端按 `q0`、`q1…` 拼出的 `answers` 缺项，`ask_user` 会以「回答数量与问题不一致」拒绝。

#### 19.5.6 流式输出

- live 语义：生成中 `role="status"`，错误 `role="alert"`；新增动态区域照此二分。
- 滚动意图识别：用户上翻即停止强制跟随，「回到最新」出现；此逻辑收敛在会话页组件，不重复实现。
- 运行中反馈 = 输入框上方一行品牌色「深度求索中，用时 N 秒 …」+ 工具行呼吸条 + 秒级耗时刷新；状态文案与计时由同一处运行态驱动，不再用带边框的 warning 药丸。新运行态 UI 复用同一套。

### 19.6 可访问性标准

基线 = WCAG 2.1 AA：

- 对比度：正文与小字 ≥ 4.5:1，大字/图标 ≥ 3:1；**已知差距**：存量 `--ph-faint` 浅色 4.3:1（随主题重构修复）。
- 键盘：Tab 序 = 视觉序；modal 陷阱与归还；skip-link 保留。
- aria：图标 `aria-hidden`；动态区 `role=status/alert`；下拉高亮同步 `aria-activedescendant`；级联菜单项补 `role=menuitem`；抽屉/面板开合同步 `aria-expanded`。
- 触屏：`@media (hover:none)` 下常驻原本 hover 才出现的操作（现状已覆盖，新增交互不得破例）。
- `prefers-reduced-motion` 全覆盖（19.2.4）。

### 19.7 治理与门禁

- **门禁**：`go test ./...`、`go vet ./...`、staticcheck、`adag verify`（11 项，其中 ui 项校验 tokens 来源头、a_da 副本清单、模板无内联 style）；UI 变更加走 19.3.3 主题清单 / 本篇对应条款的自查。
- **文档先行**（沿 design.md 约定）：改令牌、组件、交互模式前先改本篇与 07 §7.7，再改实现；`adag verify ui` 的基线断言同步更新。
- **镜像纪律**：`tokens.css`、`app.css`、`theme.js` 等上游镜像文件不做本地修改；本地能力一律以新文件叠加（先例：`app-local.css` 的本地覆盖、本次的 `themes.js`/`themes.css`）。
- **自成一体模块的界面同样遵守本篇**：`adago_design`（原 `mcp_projects/adago_design`，已移出本仓库）是独立 Go module（`GOWORK=off`），
  不能 import 宿主 `tokens.css`，因此它建了一份**同构的本地令牌层** `internal/canvas/static/tokens.css`
  （`--ui-*` 语义与 `--ph-*` 一一对齐：canvas/surface/elevated/subtle/line/ink/muted/faint/
  brand/on-brand/状态成对/focus/scrollbar/动效）。规则不变：单色品牌、缺省纸面（Black Swan）、
  彩色只给状态与数据编码、令牌单源、禁用原生 `prompt/confirm`、焦点环与减少动效全覆盖；
  机器门禁落在 `internal/canvas/ui_tokens_test.go`，设计系统说明见该模块 `DESIGN-SYSTEM-UI.md`。
  **新增自成一体模块的界面时照此办理**：本地令牌层 + 同名语义 + 同样的门禁清单，而不是另起一套配色。
- 本篇变更历史记录在文末；大版本随《UI 优化方案》里程碑推进。

---

*变更：v1（2026-11-03）——初版：原则、品牌（命名/主题/文案/动效/资产）、令牌体系与主题准入、组件与 DOM、交互模式、可访问性、治理。*
*变更：v1.1（2026-11-04）——补「标题层级」（一篇页面一个 h1）与「提交态」（整页 POST 的 pending 与防重复）两条，来源见优化方案批次 E。*
*变更：v1.2（2026-11-08）——补「插件页共用一套页面语言」（cron 管理页按供应商/环境变量/MCP 同一模板重写）与「浮层卡片的玻璃态」（Todo 卡片半透明 + 背景模糊的降级条件）。*
*变更：v1.3（2026-11-09）——补「自成一体模块的界面同样遵守本篇」：adago_design 画布界面建立同构 `--ui-*` 本地令牌层、缺省纸面、单色品牌与机器门禁（见该模块 `DESIGN-SYSTEM-UI.md`）。*
