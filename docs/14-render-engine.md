# 前端渲染引擎（ada-render）

> 对应实现：`internal/gateway/web/static/ada-render.js`
> 组件层：[15-ui-components.md](15-ui-components.md)
> 页面接入：`static/islands.js` + `data-ada-island` / `data-ada-state`（概览、插件页已启用）
> 测试：`internal/gateway/web/testdata/ada-render.test.js`、`tools/render-demo/index.html`
> 版本：0.1 · 2026-10-30

## 0. 结论

不引入第二个 Vue，也不把整站改成 SPA。这里设计的是一层**可选的 VNode 运行时**：

```
数据 ──► VNode ──► Go 序列化成 HTML（首屏 / 无 JS）
                └► 浏览器 hydrate / patch（有 JS、实时更新）
```

VNode 是唯一的 DOM 结构真源。后端不再拼 HTML 字符串，只产出数据或 VNode JSON；
浏览器拿同一棵 VNode 树接管交互。首屏、表单、链接、`<details>` 在没有 JS 时仍是
完整的 HTML，引擎缺席只影响实时增量与局部交互。

实现是一个文件、零依赖、约 90 KB 未压缩 / 21 KB gzip，**不使用 `eval` 与
`new Function`**，因此满足现有 CSP：

```
default-src 'self'; img-src 'self' data:; style-src 'self';
script-src 'self'; connect-src 'self'; frame-ancestors 'none'
```

## 1. 约束与边界

### 1.1 硬约束

| 约束 | 来源 | 对设计的影响 |
|---|---|---|
| CSP 无 `unsafe-eval` | 09 §9.3 | 模板表达式用内置词法/语法/解释器，不用 `new Function` |
| 单文件、零依赖 | 本次要求 | 不引入 Vue/Preact/htmx 之外的运行时，不新增构建链 |
| 已有 VNode 契约 | `spec/vnode.schema.json`、`internal/gateway/frontend` | 引擎输出对齐现有 `{t,p,c}` / `{$C,n}` / `{#text,v}` |
| SSR 与无 JS 降级 | 07 §7.8 | 引擎不接管原生表单；首屏仍由 Go 输出 |
| 所有文本/属性由 Go 转义 | 07 §7.3 | 浏览器端只用 `textContent` / `setAttribute`，不拼 HTML |

### 1.2 明确不做

- 不做 SPA、客户端路由、整页 CSR。
- 不做客户端插件运行时；插件仍走服务端 VNode 渲染。
- 不替代表单提交：发送、审批、重试继续走原生 `POST` + `303`。
- 不做 `eval` 表达式、动态 `import`、异步组件、Suspense、Teleport。
- 不在服务端用字符串模板：字符串模板依赖 DOM 解析，只在浏览器可用；
  服务端要么用 `render(h)`，要么由 Go 直接构造 VNode。

## 2. 与现有 VNode 契约的关系

引擎的线格式与 `spec/vnode.schema.json` 完全一致：

```js
// 元素
{ t: 'div', p: { class: 'card' }, c: [] }
// 宿主组件
{ t: '$C', n: 'Icon', p: { name: 'i-list', size: 12 }, c: [] }
// 文本
{ t: '#text', v: 'hello' }
```

引擎内部的 `el`、`_e`、`_html`、`_ref`、`s`、`k` 都是运行时字段，`renderToJSON()`
会把它们剥掉。`k`（key）只用于浏览器 diff，不进线格式。

这样带来两个直接收益：

1. **Go 侧已有唯一的高安全出口**：`frontend.ValidateNode` + `frontend.RenderHTML`。
   VNode JSON 仍然经过白名单、深度/节点数/长度上限和 URL 校验，浏览器引擎不承担
   “把不可信字符串变成 HTML”的职责。
2. **同一棵树可以两处使用**：Go 用它做首屏，浏览器用它做 diff/hydrate，不会出现
   “服务端一套 class、客户端另一套 class”。

## 3. 引擎架构

```
                 ┌──────────────────────────────┐
模板字符串/元素 ─►│ 编译器：DOM → 描述树          │
                 │ - v-if / v-for / v-model      │
                 │ - 组件 / slot / 事件           │
                 └──────────────┬───────────────┘
                                │ 描述树（编译一次，缓存在组件定义上）
                                ▼
状态/Props ─────► 响应式渲染 ──► VNode 树 ──► DOM patch / hydrate
  (Proxy)         (effect)         │
                                   └──► renderToJSON（SSR 线格式）
```

四层职责：

| 层 | 作用 | 关键点 |
|---|---|---|
| 表达式解释器 | `{{ a.b + 1 }}`、`:class="{ active: ok }"`、`@click="pick(item)"` | 手写 tokenizer + Pratt parser；无 `eval` |
| 模板编译器 | `<template>`/字符串 → 描述树 | 编译一次；`v-for`/`v-if` 在描述树层展开 |
| 响应式 | `reactive` / `ref` / `computed` / `watchEffect` | Proxy 依赖收集；微任务批处理 |
| DOM 运行时 | mount / patch / unmount / hydrate | keyed diff；单根组件；事件委托到元素 |

### 3.1 表达式解释器为什么手写

Vue 的模板编译产物是 `new Function`，在 `script-src 'self'` 下会被浏览器直接拒绝。
本引擎把表达式解析成 AST 后解释执行：

- 支持：字面量、标识符、成员/下标、调用、一元/二元运算、三元、数组/对象字面量、
  `?.`、`??`。
- 解析结果在编译期缓存；运行时只做求值，不触碰代码生成。
- 禁读 `constructor`、`__proto__`、`prototype`、`caller`、`callee`、`arguments`，
  防止从对象原型链摸到 `Function`。
- 全局只暴露 `Math`、`JSON`、`Date`、`String`、`Number`、`Boolean`、`Array`、
  `Object`、`Intl`、`isNaN`、`isFinite`、`parseInt`、`parseFloat`、
  `encodeURIComponent`、`decodeURIComponent`；不暴露 `window`、`document`、`fetch`、
  `process`、`require`。

### 3.2 响应式

- `reactive()` 用 Proxy 收集 `get`/`has`/`set`/`delete`。
- `ref()` 是带 `dep` 的 `RefImpl`；在 `reactive` 对象里读取会自动解包。
- `computed()` 惰性求值，依赖变化时只标记 dirty 并触发下游。
- 组件更新由 `effect` 驱动，`scheduler` 把同一组件的多次变化合并到一次微任务。
- `nextTick()` 返回当前刷新 Promise，测试与调用方可以等待 DOM 稳定。

## 4. API 速查

```js
const {
  createApp, defineComponent, h, host, text,
  reactive, ref, computed, watchEffect, nextTick,
  renderToJSON, evaluate
} = window.AdaRender;
```

| API | 说明 |
|---|---|
| `createApp(options)` | `options` 即根组件：`data` / `props` / `computed` / `methods` / `template` / `render` / `setup` / `components` / `allowHTML` |
| `app.mount(el, { hydrate })` | `el` 可以是选择器或元素；`hydrate: true` 复用服务端 DOM |
| `app.unmount()` | 停止 effect、移除 DOM |
| `app.component(name, def)` | 全局注册组件；支持 `ui-my-card` / `my-card` / `myCard` / `MyCard` 解析 |
| `h(tagOrComponent, props, ...children)` | 元素或组件 VNode；`onClick` 自动转事件绑定 |
| `host(name, props, ...children)` | 显式构造宿主组件 `{t:'$C', n:name}`，用于 Icon 等 Go 组件 |
| `defineComponent(def)` | 标记并规范化组件定义 |
| `renderToJSON(defOrVNode, props?, {components})` | 完全展开 JS 组件，输出 spec 线格式 |
| `evaluate(expr, scope)` | 单独执行模板表达式，便于测试/调试 |

## 5. 模板语法

| 语法 | 支持 | 说明 |
|---|---|---|
| `{{ expr }}` | ✅ | 文本插值；`null/undefined` 渲染为空 |
| `v-if` / `v-else-if` / `v-else` | ✅ | 同级分支链 |
| `v-for="item in items"` | ✅ | 支持 `(item, index)`、数字、对象值 |
| `:key` / `key` | ✅ | keyed diff；循环变量作用域内求值 |
| `:attr` / `v-bind:attr` | ✅ | `class`/`style` 会合并 |
| `@event` / `v-on:event` | ✅ | 支持 `.prevent/.stop/.self/.once/.capture/.passive` 与常用键修饰 |
| `v-model` | ✅ | input/textarea/select/checkbox/radio；`.trim/.number/.lazy` |
| `v-show` | ✅ | 用 `hidden` 属性，不写内联 style（CSP） |
| `v-text` | ✅ | `textContent` |
| `v-html` | ⚠️ 默认关闭 | 只有 `createApp({ allowHTML: true })` 且内容是服务端 bluemonday 清洗过的 Markdown 时才开 |
| `v-pre` | ✅ | 跳过编译，保留静态 HTML |
| `ref` | ✅ | 组件实例或 DOM 元素写入 `this.$refs` |
| `<slot>` / `<template #name>` | ✅ | 默认/具名 slot；fallback 内容 |
| 自定义指令 | ❌ | 不开放 |
| 箭头函数/赋值表达式 | ❌ | 事件里请调用 method |

组件标签在浏览器模板里必须带前缀（`<ui-button>`、`<ui-input>`）：HTML 解析器会把
`<Button>` 小写成原生 `<button>`，引擎不会把原生标签名解析成组件。非原生名字
（`<Card>`、`<Tabs>`、`<Icon>`）虽然仍能解析，但组件层统一使用 `ui-*` 以避免歧义。

## 6. 后端 SSR 怎么真正变简单

有三种落法，按“改动大小 / 收益”排序。

### 6.1 静态 SSR：保持现状

侧栏、登录页、错误页等没有实时交互的区域继续用 Go 模板。引擎不加载或加载后
不接管。无 JS 行为完全不变。

### 6.2 数据驱动 SSR：Go 产出 VNode，不再拼字符串

把 `ui.go` 里的 `fmt.Sprintf("<div class=...")` 换成 VNode 构造。现有
`frontend.Node` 就是可用的 Go 结构：

```go
node := frontend.Node{
    T: "div",
    P: map[string]any{"class": "ada-thread", "data-thread-row": t.ID},
    C: []frontend.Node{
        {T: "a", P: map[string]any{"class": "ada-thread__link", "href": "/threads/" + t.ID},
            C: []frontend.Node{
                {T: "span", P: map[string]any{"class": "ada-thread__title"},
                    C: []frontend.Node{frontend.TextNode(t.Title)}},
            }},
    },
}
raw, _ := json.Marshal(node)
html, err := frontend.RenderHTML(raw) // 校验 + 转义 + 长度上限
```

这一步已经把“后端 SSR DOM”从字符串拼接变成结构化数据：

- 不需要记住闭合标签和转义规则；
- class 与 `data-*` 是键值，不靠手写；
- 同一个 VNode 可以直接发给浏览器，不需要再写第二份 HTML。

注意：`frontend.RenderHTML` 目前只实现了 `Icon` 与 `TodoList` 两个宿主组件
（07 §7.3）。SSR 的组件要么落成原生标签 + `ada-*` class，要么先扩展
`renderComponent` 的白名单；不要在服务端输出一个浏览器端才认识的组件名。

### 6.3 交互岛：同一份组件定义 hydrate

首屏仍由 6.2 输出 HTML，但不能带事件处理函数。浏览器加载 `ada-render.js` 和
`islands.js`，用同一份组件模板 hydrate：

```html
<!-- shell.html.tmpl -->
<script src="{{asset "ada-render.js"}}" defer></script>
<script src="{{asset "islands.js"}}" defer></script>
```

```js
// islands.js
const ThreadRow = {
  props: ['thread'],
  template: `
    <div class="ada-thread" :class="{ 'is-active': thread.active }" :data-thread-row="thread.id">
      <a class="ada-thread__link" :href="'/threads/' + thread.id">
        <span class="ada-thread__title">{{ thread.title }}</span>
        <span class="ada-thread__meta">
          <span v-if="thread.running" class="ada-thread__dot"></span>
          <span class="ada-thread__time">{{ thread.updated }}</span>
        </span>
      </a>
    </div>`,
};

const ThreadTree = {
  components: { ThreadRow },
  props: ['threads'],
  template: `
    <div class="ada-thread-list" id="thread-list">
      <thread-row v-for="t in threads" :key="t.id" :thread="t"></thread-row>
    </div>`,
};

const el = document.getElementById('thread-list');
const threads = JSON.parse(el.dataset.adaState);
AdaRender.createApp({
  components: { ThreadRow, ThreadTree },
  template: `<thread-tree :threads="threads"></thread-tree>`,
  data: () => ({ threads }),
}).mount('#thread-island', { hydrate: true });
```

要点：

- 服务端输出的 DOM 必须和组件模板渲染出的结构一致（标签、class、层级、属性）。
  `key` 只用于客户端 diff，不参与 hydrate 匹配。
- 事件、`ref`、`v-model` 在 hydrate 时重新绑定；线格式里不传函数。
- 单根：组件和模板只能有一个根元素。多根请包一层容器。
- 可交互区域才需要岛屿；纯展示区域不需要加载 JS。

### 6.4 如果希望服务端也执行同一份 JS

仓库已经有 moejs 与 `host.Renderer`（TSX → VNode）。可以把同一份组件定义交给
moejs 执行，得到 VNode JSON，再走 `frontend.RenderHTML`。这样服务端不再有第二套
DOM 代码。

代价与边界：

- 每次请求执行 JS，需要缓存编译结果；页面级组件可以接受，侧栏这种高频区域
  建议仍用 Go 直接构造 VNode。
- 服务端没有 DOM，字符串模板（`<template>`/HTML 字符串）不可用；组件需写成
  `render(h)`，或由构建期预编译成描述树。
- 渲染预算仍按 07 §7.3：插槽 100ms、页面 300ms。

## 7. Hydration 契约

服务端 HTML 与客户端组件树必须逐节点对应。引擎的 hydrate 策略是“能复用就复用，
不匹配就替换”：

1. 文本：节点类型是文本 → 更新 `nodeValue`；否则新建。
2. 元素：`tagName` 一致 → 复用，补属性/事件；否则替换。
3. 子节点：按顺序游标前进；跳过注释与纯空白文本；多余的旧节点删除。
4. 组件：先渲染子树，再对子树做 hydrate；首帧不重建 DOM。

建议的服务端标记：

```html
<div id="thread-island" data-ada-island="thread-tree"
     data-ada-state='{"threads":[...]}'>
  <!-- 服务端 VNode 渲染出的 thread-list -->
</div>
```

`islands.js` 只扫描 `[data-ada-island]`，按名字挂载；没有匹配的岛屿就跳过。
不要 hydrate 整页，也不要 hydrate 插件插槽（插件仍由服务端渲染）。

## 8. SSE 增量

现有 `chat-live.js` 在 `evt.item.updated` 时整段 fetch 页面再用 `DOMParser` 对行做
outerHTML 比较。引入引擎后可以改为：

- SSE 帧携带 VNode/结构化 patch；
- 组件把 VNode 放进响应式数组，`render(h)` 直接输出 VNode 子节点；
- 引擎按 `key` 复用行，不再重新解析整页 HTML。

示例（组件用 `render` 而不是字符串模板，因为子节点本身就是 VNode）：

```js
const Transcript = {
  props: ['items'],
  render(h) {
    return h('div', { id: 'transcript', class: 'ada-transcript' }, this.items);
  },
};

const transcript = {
  items: AdaRender.reactive(existingVNodes),
};
```

收到增量：

```js
// 线格式不带 key（spec 没有 k 字段）；客户端按条目 id 重新挂 key，
// 否则引擎只能按位置复用，重排时会重建节点。
const vnode = AdaRender.h('div', { class: 'ada-msg' }, item.text);
vnode.k = item.key;

const index = transcript.items.findIndex((v) => v.k === item.key);
if (index === -1) transcript.items.push(vnode);
else transcript.items.splice(index, 1, vnode);
await AdaRender.nextTick();
```

更稳的做法是 SSE 只传 **数据条目**（id、角色、parts），由客户端组件渲染 VNode；
客户端天然知道 key，也不需要把 VNode JSON 当作跨语言协议的一部分。

这替代了 `chat-live.js` 里“fetch 整页 + DOMParser + sameRow 字符串比较”的路径，
也避免了“整段重画”带来的闪烁。

## 9. 安全

| 面 | 处理 |
|---|---|
| HTML 注入 | 文本走 `createTextNode`；属性走 `setAttribute`；不拼接 HTML |
| `v-html` | 默认抛错；只有显式 `allowHTML` 且内容已由 Go bluemonday 清洗时可用 |
| SSR 线格式 | `renderToJSON` 遇到 `_html` 直接抛错，避免原始 HTML 绕过 Go 校验 |
| 表达式逃逸 | 禁 `constructor`/`__proto__`/`prototype` 等；不暴露 `window/document/fetch` |
| CSP | 无 `eval`/`new Function`；`v-show` 用 `hidden` 而不是内联 style |
| 不可信 VNode | 浏览器端组件定义是受信任代码；来自服务端的 VNode 仍应先过 `frontend.ValidateNode` |
| URL | 链接/表单仍由 Go 侧 `validateURL` 检查；浏览器端不新增 URL 协议面 |

## 10. 性能与体积

- 未压缩约 90 KB，gzip 约 21 KB；无解析期代码生成。
- 模板在组件定义上缓存，首次渲染后不再编译。
- 更新按组件合并到微任务；同一次事件里的多次状态写入只渲染一次。
- keyed diff 复用 DOM；列表重排只移动节点。
- 静态 SSR 页面不加载引擎时零成本；岛屿按需加载。

## 11. 测试与验收

```bash
# 18 个 Node 用例：VNode、表达式、响应式、mount/patch、组件、slots、hydration、SSR 线格式
node --test internal/gateway/web/testdata/ada-render.test.js
# 或
make test-js

# 浏览器冒烟：模板编译、v-for/v-if/v-model、具名 slot、组件 emit、hydrate
# 打开 tools/render-demo/index.html，页面标题应为
# "ada-render smoke: PASS"，#log 全为 PASS
```

验收项：

- 无 JS：页面仍可读、表单仍可提交、`<details>` 仍可展开。
- 有 JS：岛屿 hydrate 不重建首屏节点；更新只 patch 变化行。
- CSP：控制台无 `unsafe-eval` 拒绝；不出现内联 `<script>`。
- 键盘/焦点：hydrate 后焦点不丢；Esc/遮罩行为由现有组件控制器负责。
- 视觉：class 与 DOM 与 a_da 基线一致；不出现内联 style。

## 12. 迁移路线

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0（已完成） | 引入引擎、组件层与测试 | Node 测试 + 浏览器冒烟通过 |
| P1（已完成） | `/overview`、`/extensions`、`/extensions/{id}`、`/workspaces` 改为 AdaUI 客户端岛：SSR 回退 + `islands.js` mount | 无 JS 行为不变；有 JS 时用组件渲染，概览/插件列表走 JSON，其余用初始状态 |
| P2 | 把实时对话流从“fetch 整页 + outerHTML 比较”切到 VNode patch | 不再整段重画；滚动/焦点稳定 |
| P3 | 把 `ui.go` 的字符串拼接改为 VNode 构造（先侧栏，后工具卡） | golden HTML 不变 |
| P4 | 评估 moejs 服务端执行同一份 JS 组件 | 页面渲染预算内；无重复 DOM 代码 |

每一步都可以独立回滚：引擎是增量层，Go SSR 始终是可用的基线。当前落地为
P0 + P1：概览、插件列表、插件详情、工作区页已是“SSR 回退 + 客户端岛”，
`/threads/{id}` 仍走原 SSR + SSE。

## 13. 已知限制

- **单根**：组件与模板只能有一个根节点；`v-for` 不能直接作为根。
- **组件标签前缀**：浏览器模板里用 `<ui-button>`；`button` / `input` / `select` /
  `table` / `progress` 等原生标签名永远不会被解析成组件。
- **表达式子集**：不支持箭头函数、赋值、模板字符串、`typeof`、`in`；
  事件里请调用 method。`?.` 只对当前成员短路，链式访问请写 `a?.b?.c`。
- **字符串模板仅浏览器可用**：服务端执行同一份 JS 时要用 `render(h)`。
- **无过渡/异步组件/Suspense/Teleport/provide-inject**。
- **`v-html` 不能进 SSR 线格式**；Markdown 正文仍应由服务端渲染为 VNode 或保留现有
  HTML 片段路径。
- **`$C` 字符串组件**：浏览器端必须先用 `app.component('Icon', …)` 同名注册；
  未注册会直接抛错，不会猜一个 DOM。服务端与客户端的组件名要一一对应。
- **hydrate 是结构匹配**：服务端与客户端结构不一致时会替换节点；空白文本节点会被
  跳过，模板不要依赖有意义的纯空白。
- **内联 style 受 CSP 限制**：`style-src 'self'` 会拦截；样式一律走 class/data-*。
- **具名 slot 的 slot props** 只支持把对象键展开为作用域变量，不支持复杂解构；
  默认 slot 不接收 slot props（内容在父组件渲染时已经展开）。

## 14. 对现有设计文档的改动

如果接受这条路线，需要同步修正：

1. `docs/07-ssr-frontend.md` §7.1 的“明确不做”增加限定：
   - 仍然不做 SPA、客户端路由、客户端插件运行时；
   - 新增“基础页面的 VNode hydrate 岛屿”作为例外。
2. §7.3 的 VNode 契约保持不变；补充“浏览器端运行时读取同一份 VNode JSON”。
3. §7.4 的 `fragment` 事件增加一种可选载荷：`vnode`（结构化增量），
   `html` 继续保留给非交互片段。
4. §7.7.3 的组件表增加“JS 运行时实现”一列，标注哪些组件已能在浏览器端渲染。

---

**相邻文档**：[← 插件运行时与治理](06-plugin-runtime.md) · [索引](design.md) · [协议契约 →](08-protocol.md)
