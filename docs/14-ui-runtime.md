# 前端运行时、组件与 UED

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§14 渲染引擎、§15 组件层、§19 UED 规范  
> 版本：0.2 · 2026-11-09  
> 本文合并原 14/15/19 三份文档；章节编号保持不变，代码注释里的 §14/§15/§19 引用继续有效。

## 14. 前端渲染引擎（ada-render）

### 14.0 结论

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

### 14.1 约束与边界

#### 14.1.1 硬约束

| 约束 | 来源 | 对设计的影响 |
|---|---|---|
| CSP 无 `unsafe-eval` | 09 §9.3 | 模板表达式用内置词法/语法/解释器，不用 `new Function` |
| 单文件、零依赖 | 本次要求 | 不引入 Vue/Preact/htmx 之外的运行时，不新增构建链 |
| 已有 VNode 契约 | `spec/vnode.schema.json`、`internal/gateway/frontend` | 引擎输出对齐现有 `{t,p,c}` / `{$C,n}` / `{#text,v}` |
| SSR 与无 JS 降级 | 07 §7.8 | 引擎不接管原生表单；首屏仍由 Go 输出 |
| 所有文本/属性由 Go 转义 | 07 §7.3 | 浏览器端只用 `textContent` / `setAttribute`，不拼 HTML |

#### 14.1.2 明确不做

- 不做 SPA、客户端路由、整页 CSR。
- 不做客户端插件运行时；插件仍走服务端 VNode 渲染。
- 不替代表单提交：发送、审批、重试继续走原生 `POST` + `303`。
- 不做 `eval` 表达式、动态 `import`、异步组件、Suspense、Teleport。
- 不在服务端用字符串模板：字符串模板依赖 DOM 解析，只在浏览器可用；
  服务端要么用 `render(h)`，要么由 Go 直接构造 VNode。

### 14.2 与现有 VNode 契约的关系

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

### 14.3 引擎架构

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

#### 14.3.1 表达式解释器为什么手写

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

#### 14.3.2 响应式

- `reactive()` 用 Proxy 收集 `get`/`has`/`set`/`delete`。
- `ref()` 是带 `dep` 的 `RefImpl`；在 `reactive` 对象里读取会自动解包。
- `computed()` 惰性求值，依赖变化时只标记 dirty 并触发下游。
- 组件更新由 `effect` 驱动，`scheduler` 把同一组件的多次变化合并到一次微任务。
- `nextTick()` 返回当前刷新 Promise，测试与调用方可以等待 DOM 稳定。

### 14.4 API 速查

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
| `app.mount(el, { hydrate })` | `el` 可以是选择器或元素；普通 mount 会先清空容器再接管，`hydrate: true` 保留服务端 DOM |
| `app.mount(el, { hydrate: true, rootElement: true })` | `el` 是服务端已经输出的根元素本身（插件岛屿标记在根上）；hydrate 从该元素开始，卸载只移除子树，不清空父容器 |
| `app.unmount()` | 停止 effect、移除 DOM |
| `app.component(name, def)` | 全局注册组件；支持 `ui-my-card` / `my-card` / `myCard` / `MyCard` 解析 |
| `h(tagOrComponent, props, ...children)` | 元素或组件 VNode；`onClick` 自动转事件绑定 |
| `host(name, props, ...children)` | 显式构造宿主组件 `{t:'$C', n:name}`，用于 Icon 等 Go 组件 |
| `defineComponent(def)` | 标记并规范化组件定义 |
| `renderToJSON(defOrVNode, props?, {components})` | 完全展开 JS 组件，输出 spec 线格式 |
| `watch(source, cb, { immediate })` | 监听 getter/ref；浅比较，值不变不触发；返回 stop |
| `watchEffect(fn)` | 立即执行并收集依赖；组件 setup/data 中创建时随卸载自动停止 |
| `provide(key, value)` / `inject(key, default?)` | 组件级依赖注入；`app.provide(key, value)` 提供 app 级默认值 |
| `defineStore(id, { state, getters, actions })` | 轻量模块级 store；返回 `useStore(initialState?)`，同一 id 单例 |
| `store.$snapshot()` / `store.$hydrate(data)` | 显式导出/恢复 store 状态；岛屿渲染会自动收集快照进 `data-ada-props.stores`，客户端挂载前恢复 |
| `collectStoreSnapshots()` / `hydrateStores(data)` | 服务端收集本次渲染的 store 快照；客户端在 store 创建前暂存、首次实例化时恢复 |
| `keyed(vnode, key)` | 给 VNode 挂稳定 key；线格式没有 `k`，客户端按业务 key 重新挂 |
| `applyVNodePatches(items, patches)` | 应用 `{key, mode:"upsert"|"remove", vnode}` 列表，返回新 `[{key,vnode}]`，由响应式数组驱动 keyed patch |
| `AdaPatchStream.create({element, url, initial})` | 订阅 SSE `patch` 帧并驱动 keyed 列表；`source` 可注入测试；返回 `{app, proxy, close}` |
| `evaluate(expr, scope)` | 单独执行模板表达式，便于测试/调试 |

组件 `setup`/`data` 中创建的 `watch`/`watchEffect` 会登记到实例作用域，卸载时自动 `stop`；组件 `unmounted` 钩子现在也会被调用。`defineStore` 是同一 bundle 内的单例，跨插件不共享可变状态。

### 14.5 模板语法

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

Markdown 走数据而不是 HTML：Go goldmark + bluemonday 解析/清洗后转成 VNode AST，
浏览器 `ui-markdown` 组件渲染；流式入口是 `POST /markdown.ast`，`POST /markdown`
保留 HTML 兼容路径。

### 14.6 后端 SSR 怎么真正变简单

有三种落法，按“改动大小 / 收益”排序。

#### 14.6.1 静态 SSR：保持现状

侧栏、登录页、错误页等没有实时交互的区域继续用 Go 模板。引擎不加载或加载后
不接管。无 JS 行为完全不变。

#### 14.6.2 数据驱动 SSR：Go 产出 VNode，不再拼字符串

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

注意：`frontend.RenderHTML` 目前只实现了 `Icon`、`TodoList` 与 `Select` 三个宿主组件
（07 §7.3）。SSR 的组件要么落成原生标签 + `ada-*` class，要么先扩展
`renderComponent` 的白名单；不要在服务端输出一个浏览器端才认识的组件名。

#### 14.6.3 交互岛：同一份组件定义 hydrate

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

#### 14.6.4 如果希望服务端也执行同一份 JS

仓库已经有 moejs 与 `host.Renderer`（TSX → VNode）。可以把同一份组件定义交给
moejs 执行，得到 VNode JSON，再走 `frontend.RenderHTML`。这样服务端不再有第二套
DOM 代码。

代价与边界：

- 每次请求执行 JS，需要缓存编译结果；页面级组件可以接受，侧栏这种高频区域
  建议仍用 Go 直接构造 VNode。
- 服务端没有 DOM，字符串模板（`<template>`/HTML 字符串）不可用；组件需写成
  `render(h)`，或由构建期预编译成描述树。
- 渲染预算仍按 07 §7.3：插槽 100ms、页面 300ms。

### 14.7 Hydration 契约

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

### 14.8 SSE 增量

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

### 14.9 安全

| 面 | 处理 |
|---|---|
| HTML 注入 | 文本走 `createTextNode`；属性走 `setAttribute`；不拼接 HTML |
| `v-html` | 默认抛错；只有显式 `allowHTML` 且内容已由 Go bluemonday 清洗时可用 |
| SSR 线格式 | `renderToJSON` 遇到 `_html` 直接抛错，避免原始 HTML 绕过 Go 校验 |
| 表达式逃逸 | 禁 `constructor`/`__proto__`/`prototype` 等；不暴露 `window/document/fetch` |
| CSP | 无 `eval`/`new Function`；`v-show` 用 `hidden` 而不是内联 style |
| 不可信 VNode | 浏览器端组件定义是受信任代码；来自服务端的 VNode 仍应先过 `frontend.ValidateNode` |
| URL | 链接/表单仍由 Go 侧 `validateURL` 检查；浏览器端不新增 URL 协议面 |

### 14.10 性能与体积

- 未压缩约 90 KB，gzip 约 21 KB；无解析期代码生成。
- 模板在组件定义上缓存，首次渲染后不再编译。
- 更新按组件合并到微任务；同一次事件里的多次状态写入只渲染一次。
- keyed diff 复用 DOM；列表重排只移动节点。
- 静态 SSR 页面不加载引擎时零成本；岛屿按需加载。

### 14.11 测试与验收

```bash
# 18 个 Node 用例：VNode、表达式、响应式、mount/patch、组件、slots、hydration、SSR 线格式
node --test internal/gateway/web/testdata/ada-render.test.js
# 块 VNode：浏览器运行时挂载结果与 /markdown golden HTML 结构一致
node --test internal/gateway/web/testdata/ada-block-consistency.test.js
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

### 14.12 迁移路线

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0（已完成） | 引入引擎、组件层与测试 | Node 测试 + 浏览器冒烟通过 |
| P1（已完成） | `/overview`、`/extensions`、`/extensions/{id}`、`/workspaces` 改为 AdaUI 客户端岛：SSR 回退 + `islands.js` mount | 无 JS 行为不变；有 JS 时用组件渲染，概览/插件列表走 JSON，其余用初始状态 |
| P2（已完成基础版） | 对话流从“fetch 整页 + outerHTML 比较”切到 JSON 行 + VNode keyed patch；结构变化仍按 `item.updated` 拉取行 JSON | 不再整段重画；行按 key 复用；滚动/焦点稳定；流式增量仍走 live-stream |
| P3 | 把 `ui.go` 的字符串拼接改为 VNode 构造（先侧栏，后工具卡） | golden HTML 不变 |
| P4 | 评估 moejs 服务端执行同一份 JS 组件 | 页面渲染预算内；无重复 DOM 代码 |

每一步都可以独立回滚：引擎是增量层，Go SSR 始终是可用的基线。当前落地为
P0 + P1 + P2（基础版）：概览、插件列表、插件详情、工作区页已是“SSR 回退 + 客户端岛”；
`/threads/{id}` 的持久化对话行也已切到 JSON + keyed patch，流式文字仍在 live-stream，
终态/等待答复仍整页 reload。

### 14.13 已知限制

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

### 14.14 与 07 的关系

1. `docs/07-ssr-frontend.md` §7.1 的“明确不做”已限定：不做 SPA、客户端路由、通用客户端插件运行时；只对显式声明的 VNode hydrate 岛屿例外。
2. §7.3 的 VNode 契约保持不变；浏览器端运行时读取同一份 VNode JSON。
3. §7.4 的 `fragment` 事件可带 `vnode`（结构化增量），`html` 继续保留给非交互片段。
4. §7.7.3 的组件表标注了浏览器端已实现的组件；未实现的组件仍以 SSR 为准。

## 15. ada-ui 组件层

### 15.0 定位

`ada-ui.js` 是 `ada-render.js` 之上的一层通用组件，不引入新的视觉语言：

- class、DOM 与状态语义复用现有 `app.css` / `ui/components.css` / `ui/widgets.css` /
  `ui/layout.css` / `ui/shell.css`；
- 下拉与弹窗继续由现有 `<ada-select>` / `<ada-modal>` Web Components 提供行为；
- `ada-ui.css` 只补项目基线里还没有的控件：滑块、进度、开关、选择项、Toast、
  下拉面板对齐、表格修饰；
- 没有构建步骤、没有 npm 依赖，仍然是同源静态文件。

### 15.1 加载顺序

```html
<!-- 样式：先现有基线，最后 ada-ui.css -->
<link rel="stylesheet" href="{{asset "tokens.css"}}">
<link rel="stylesheet" href="{{asset "app.css"}}">
<link rel="stylesheet" href="{{asset "ui/components.css"}}">
<link rel="stylesheet" href="{{asset "ui/layout.css"}}">
<link rel="stylesheet" href="{{asset "ui/widgets.css"}}">
<link rel="stylesheet" href="{{asset "ui/shell.css"}}">
<link rel="stylesheet" href="{{asset "ui/pages.css"}}">
<link rel="stylesheet" href="{{asset "app-local.css"}}">
<link rel="stylesheet" href="{{asset "ada-ui.css"}}">

<!-- 脚本：引擎 → Web Components → 组件层 → 岛屿入口 -->
<script src="{{asset "ada-render.js"}}" defer></script>
<script src="{{asset "ada-select.js"}}" defer></script>
<script src="{{asset "ada-modal.js"}}" defer></script>
<script src="{{asset "ada-ui.js"}}" defer></script>
<script src="{{asset "islands.js"}}" defer></script>
```

`Select` 依赖 `ada-select.js`，`Modal` 依赖 `ada-modal.js`。没有这两个脚本时，
组件仍能渲染静态结构，但下拉/弹窗交互不可用；无 JS 页面请继续使用现有的 Go SSR
模板，不要用 `ui-*` 标签作为首屏唯一内容。

当前 `shell.html.tmpl` 只在 `baseData.NeedsUI` 为 true 时加载
`ada-ui.css` / `ada-render.js` / `ada-ui.js` / `islands.js`。目前启用的是
`/overview`、`/extensions`、`/extensions/{id}`、`/workspaces`，以及**有历史消息的**
`/threads/{id}`；其它页面保持原有 SSR 资源，不额外下载引擎。

### 15.2 用法

```js
const app = AdaUI.createApp({
  data() {
    return {
      name: 'Ada',
      model: 'b',
      options: [
        { value: 'a', label: '模型 A' },
        { value: 'b', label: '模型 B' }
      ],
      active: 'overview',
      tabs: [
        { key: 'overview', label: '概览', count: 2 },
        { key: 'details', label: '详情' }
      ]
    };
  },
  template: `
    <div>
      <ui-input v-model="name" label="名称" hint="用于侧栏显示"></ui-input>
      <ui-select v-model="model" :options="options" label="模型"></ui-select>
      <ui-tabs v-model="active" :tabs="tabs">
        <template #overview>概览内容</template>
        <template #details>详情内容</template>
      </ui-tabs>
    </div>`,
});

app.mount('#ui-island');
```

也可以先创建普通 app，再安装：

```js
const app = AdaRender.createApp({ /* ... */ });
AdaUI.install(app);
app.mount('#ui-island');
```

#### 15.2.1 模板标签必须用 `ui-` 前缀

浏览器解析 `<template>` 时会把 `<Button>` 小写成 `<button>`，与原生按钮撞名。
所以组件在模板里统一写成：

```html
<ui-button>保存</ui-button>
<ui-input v-model="name"></ui-input>
<ui-select v-model="model" :options="options"></ui-select>
```

JS 里的注册名仍是 `Button` / `Input` / `Select`：

```js
app.component('Button');       // 取到组件定义
AdaUI.components.Button;       // 同一份定义
```

原生标签（`button`、`input`、`select`、`table`、`progress`、`template`、`slot`…）
永远不会被解析成组件，避免 `<button>` 在 Button 模板里递归。

#### 15.2.2 布尔 prop

布尔 prop 在定义里带 `type: Boolean`。模板里裸写即 true：

```html
<ui-select native></ui-select>
<ui-progress :value="40" show-value></ui-progress>
<ui-message closable>...</ui-message>
```

要显式关闭用绑定：

```html
<ui-button :disabled="false">可用</ui-button>
<ui-card :padding="false">...</ui-card>
```

kebab-case 属性会映射到 camelCase prop：`show-value` → `showValue`，
`row-key` → `rowKey`，`cancel-text` → `cancelText`。

### 15.3 组件清单

| JS 名 | 模板标签 | 说明 |
|---|---|---|
| `Button` | `<ui-button>` | 变体、尺寸、图标、loading、禁用、链接 |
| `IconButton` | `<ui-icon-button>` | 纯图标按钮，强制 `label`/`title` |
| `Input` / `TextField` | `<ui-input>` / `<ui-text-field>` | 单行/多行输入、label/hint/error |
| `Textarea` | `<ui-textarea>` | `Input` 的多行别名 |
| `Select` | `<ui-select>` | 基于 `<ada-select>`；`native` 可退回原生 `<select>` |
| `Dropdown` / `DropdownButton` | `<ui-dropdown>` | `details` + `.ada-row-menu` 菜单 |
| `Tabs` | `<ui-tabs>` | 键盘方向键、具名 slot 面板 |
| `Icon` | `<ui-icon>` | `<use href="#i-…">`，依赖 Go 的 sprite |
| `Markdown` | `<ui-markdown>` | 接收后端 VNode AST，负责 Markdown 排版；不接受 HTML 字符串 |
| `Card` | `<ui-card>` | `.card` + 标题/header/footer slot |
| `Badge` | `<ui-badge>` | `.badge badge-*` |
| `Slider` / `Range` | `<ui-slider>` | 原生 range + `accent-color` |
| `Progress` / `ProgressBar` | `<ui-progress>` | 原生 progress + 状态色 |
| `Table` / `DataTable` | `<ui-table>` | 列定义、keyed 行、空态/loading |
| `Modal` | `<ui-modal>` | 基于 `<ada-modal>`，v-model 控开合 |
| `Message` / `Alert` | `<ui-message>` | `.alert alert-*` 内联消息 |
| `Notice` | `<ui-notice>` | `.ada-notice` 持久提示 |
| `Empty` | `<ui-empty>` | `.ada-empty` 空状态 |
| `Spinner` | `<ui-spinner>` | 复用 `.ada-run-status__spin` |
| `Switch` | `<ui-switch>` | 原生 checkbox + 自绘 track/thumb |
| `Checkbox` | `<ui-checkbox>` | 原生 checkbox + `accent-color` |
| `Radio` | `<ui-radio>` | 原生 radio + `accent-color` |
| `toast()` | — | 命令式 Toast，挂到 `document.body` |

### 15.4 Props / 事件 / Slots

### Button / IconButton

| 组件 | Props | 事件 | Slots |
|---|---|---|---|
| `Button` | `variant` (`primary`/`secondary`/`ghost`/`danger`)，`size` (`sm`/`md`/`lg`)，`type`，`href`，`disabled`，`loading`，`block`，`label`，`icon`，`iconRight` | `click` | default（覆盖 label） |
| `IconButton` | `icon`，`label`，`title`，`size`，`type`，`disabled` | `click` | — |

`href` 存在时渲染 `<a>`；`disabled`/`loading` 会移除 `href`、设置
`aria-disabled` 与 `tabindex="-1"`，并阻止点击。

### Input / Textarea / Select

| 组件 | Props | 事件 | 说明 |
|---|---|---|---|
| `Input` | `modelValue`，`type`，`name`，`label`，`hint`，`error`，`placeholder`，`disabled`，`readonly`，`required`，`autocomplete`，`multiline`，`rows`，`maxlength`，`minlength`，`id` | `update:modelValue`、`input` | `error` 优先于 `hint`，并设置 `aria-invalid` |
| `Textarea` | 同 `Input`，`multiline=true`，`rows=4` | 同上 | — |
| `Select` | `modelValue`，`options`，`name`，`label`，`hint`，`error`，`placeholder`，`disabled`，`native`，`id` | `update:modelValue`、`change` | `options` 可为字符串或 `{value,label,disabled}` |

`Select` 非 native 时输出：

```html
<ada-select class="ada-select">
  <button class="ada-select__trigger" data-select-trigger>…</button>
  <div class="ada-select__menu" data-select-menu>…</div>
  <input type="hidden" data-select-input>
</ada-select>
```

`ada-select.js` 负责开合、键盘与点击外部收起；组件只同步 `modelValue`。

### Dropdown

```html
<ui-dropdown label="更多操作" :items="items" @select="onSelect"></ui-dropdown>
```

`items` 项：

```js
{ label: '重命名', value: 'rename', icon: 'i-edit' }
{ label: '删除', value: 'delete', danger: true }
{ separator: true }
{ label: '打开文档', value: 'docs', href: '/docs' }
```

| Props | 事件 | Slots |
|---|---|---|
| `label`，`icon`，`items`，`variant`，`size`，`align` (`left`/`right`)，`disabled`，`closeOnSelect` | `select(item, event)`、`command(value, item, event)` | default（追加自定义面板内容） |

### Tabs

```html
<ui-tabs v-model="active" :tabs="tabs">
  <template #overview>概览内容</template>
  <template #details>详情内容</template>
</ui-tabs>
```

| Props | 事件 | Slots |
|---|---|---|
| `tabs` (`{key,label,count?,icon?,disabled?}`)，`modelValue`，`id` | `update:modelValue`、`change` | 每个 `tab.key` 一个具名 slot |

面板全部挂载、非当前面板用 `hidden` 收起；方向键/Home/End 切换并移动焦点。

### Icon / Badge / Spinner / Empty

| 组件 | Props | 说明 |
|---|---|---|
| `Icon` | `name`，`size`，`title`，`label` | `name` 对应 Go sprite 的 `i-*`；需要页面包含 `{{.Icons}}` |
| `Markdown` | `nodes`（后端 VNode AST），`text`（AST 到达前的纯文本占位），`class` | 只渲染数据；Markdown 解析与清洗仍在 Go goldmark + bluemonday |
| `Badge` | `tone` (`neutral`/`brand`/`success`/`warn`/`danger`)，`mono`，`label` | `.badge badge-*` |
| `Spinner` | `size`，`label` | 复用 `.ada-run-status__spin` |
| `Empty` | `icon`，`title`，`description` | action slot |

### Card

| Props | Slots |
|---|---|
| `title`，`subtitle`，`padding`（默认 true），`class` | default、`header`、`footer` |

### Slider / Progress

| 组件 | Props | 事件 |
|---|---|---|
| `Slider` | `modelValue`，`min`，`max`，`step`，`disabled`，`label`，`showValue`，`suffix`，`format`，`id` | `update:modelValue`、`input` |
| `Progress` | `value`，`max`，`tone` (`brand`/`success`/`warn`/`danger`)，`size` (`sm`/`md`/`lg`)，`label`，`showValue` | — |

`Progress` 用原生 `<progress>` 的 `value`/`max`，不写内联宽度，CSP 安全。

### Table

```js
const columns = [
  { key: 'name', label: '名称' },
  { key: 'age', label: '年龄', render: (row) => row.age + ' 岁' },
  { label: '操作', render: (row, index, h) => h(AdaUI.components.Button, { size: 'sm' }, '编辑') },
];

const rows = [{ id: 'a', name: 'Ada', age: 36 }];
```

| Props | 列字段 |
|---|---|
| `columns`，`rows`，`rowKey`，`empty`，`loading`，`striped`，`hover`，`caption` | `key`、`label`、`class`、`headerClass`、`value(row)`、`render(row,index,h)` |

`col.render` 可以返回字符串、VNode 或 VNode 数组；表头/单元格的 class 由列定义提供，
不写内联样式。

### Modal

```html
<ui-modal v-model="open" id="confirm" title="确认操作" @confirm="onConfirm">
  <p>确定继续吗？</p>
</ui-modal>
```

| Props | 事件 | Slots |
|---|---|---|
| `modelValue`（`null` 表示非受控），`id`，`title`，`wide`，`confirmText`，`cancelText`，`confirmVariant`，`closeOnConfirm` | `update:modelValue`、`open`、`close`、`confirm`、`cancel` | default、`actions` |

`modelValue` 为布尔时是受控模式；为 `null`/不传时可用组件的 `open()` / `close()`
方法（通过 `ref` 获取）。点击遮罩/Esc 由 `<ada-modal>` 关闭，并同步回
`update:modelValue`。`data-modal-open="<id>"` 也能打开。

### Message / Notice

| 组件 | Props | 事件 |
|---|---|---|
| `Message` | `tone` (`info`/`success`/`warn`/`error`)，`title`，`closable` | `close` |
| `Notice` | `tone` (`neutral`/`brand`/`success`/`warn`/`danger`)，`icon` | — |

`Message` 用 `.alert alert-*`，`Notice` 用 `.ada-notice ada-notice--*`。

### Switch / Checkbox / Radio

| 组件 | Props | 事件 |
|---|---|---|
| `Switch` | `modelValue`，`disabled`，`label`，`id` | `update:modelValue`、`change` |
| `Checkbox` | `modelValue`，`disabled`，`label`，`id` | 同上 |
| `Radio` | `modelValue`，`value`，`name`，`disabled`，`label`，`id` | 同上 |

`Radio` 的 `modelValue` 与 `value` 用严格相等比较；一组 Radio 共享 `name`。

### Toast

```js
const handle = AdaUI.toast('已保存', { tone: 'success', title: '成功', duration: 3000 });
handle.close();
AdaUI.toast.error('保存失败');
AdaUI.toast.warn('网络较慢');
```

`toast()` 返回 `{ element, close }`；`duration` 为 0 时不自动关闭。
颜色复用 `.alert alert-*`，位置由 `ada-ui.css` 的 `.ada-toast-stack` 控制。

### 15.5 样式边界

`ada-ui.css` 只新增以下类：

| 类 | 对应组件 |
|---|---|
| `.ada-btn--block` | Button `block` |
| `.ada-field__hint--error` | Input/Select error |
| `.ada-choice*` | Checkbox / Radio |
| `.ada-switch*` | Switch |
| `.ada-slider*` | Slider |
| `.ada-progress*` | Progress |
| `.ada-dropdown*` | Dropdown 对齐/分隔线 |
| `.ada-tabs__panel` | Tabs 面板间距 |
| `.ada-table--striped` / `--hover` / `.ada-table__empty` | Table |
| `.ada-toast-stack` / `.ada-toast*` | Toast |

所有颜色走 `--ph-*`，尺寸走 `--ada-*`；不写内联 style、不使用 `dark:` 变体。

### 15.6 无障碍与 SSR

- 原生控件优先：`input`/`select`/`textarea`/`progress`/`input[type=range]` 保留浏览器
  表单语义；`Switch` 用 `role="switch"` 的原生 checkbox。
- `IconButton` 要求 `label`/`title`；`Icon` 在没有 `label` 时 `aria-hidden="true"`。
- `Tabs` 使用 `role="tablist"/"tab"/"tabpanel"` 与 `aria-selected`/`aria-controls`。
- `Modal` 的焦点圈、Esc、遮罩、滚动锁定沿用 `ada-modal.js`。
- 这一层是**浏览器端组件**：没有 JS 时 `ui-*` 标签不会被渲染。无 JS 首屏继续用
  Go SSR（现有模板），把 `ui-*` 只放在标记过的交互岛里。
- 需要 SSR 同一份组件时，用 `render(h)` 或 Go VNode 渲染成原生 HTML；字符串模板
  依赖 DOM，只在浏览器可用。服务端输出的 DOM 必须与组件模板一致，hydrate 才能复用。
- 组件在未显式传 `id` 时会生成模块内自增 id；SSR/hydrate 场景请显式传 `id`，
  避免服务端与客户端不一致。

### 15.7 测试

```bash
# 38 个 Node 用例：引擎 20 + 组件 17 + 块渲染一致性 1
mingw32-make test-js
# 或
node --test internal/gateway/web/testdata/ada-render.test.js internal/gateway/web/testdata/ada-ui.test.js internal/gateway/web/testdata/ada-block-consistency.test.js
```

浏览器冒烟：直接打开 `tools/render-demo/ui.html`，标题应为
`ada-ui smoke: PASS`，`#log` 全为 PASS。该页是 `file://` 冒烟页，使用了内联脚本与
演示 CSS，不适用于网关 CSP。

整页岛另有独立冒烟页：

- `tools/render-demo/overview.html` → `overview island smoke: PASS`
- `tools/render-demo/extensions.html` → `extensions island smoke: PASS`
- `tools/render-demo/pages.html` → `pages island smoke: PASS`（工作区 + 插件详情）
- `tools/render-demo/transcript.html` → `transcript island smoke: PASS`（keyed 行 + live-stream 复用）
- `tools/render-demo/chat-live.html` → `chat live island smoke: PASS`（假 EventSource/fetch 驱动 JSON patch）

Go 侧用例：

```bash
go test ./internal/gateway/web -run 'TestOverviewIslandAndDashboardJSON|TestExtensionsIslandAndJSON|TestWorkspacesAndPluginDetailUseAdaUI|TestThreadTranscriptIslandAndJSON' -v
```

#### 15.7.1 当前项目接入状态

| 页面 | 方式 | 服务端回退 | 数据接口 |
|---|---|---|---|
| `/overview` | `data-ada-island="overview"`，`islands.js` 挂载 | 统计卡/最近会话的 `overview_partial` SSR | `GET /partials/dashboard.json`，5s 轮询 |
| `/extensions` | `data-ada-island="extensions"` | 原有插件表格 SSR | `GET /partials/extensions.json` |
| `/extensions/{id}` | `data-ada-island="plugin-detail"` | 原有详情卡/操作 SSR | 初始 `data-ada-state`，无轮询 |
| `/workspaces` | `data-ada-island="workspaces"` | 原有工作区选择器 SSR | 初始 `data-ada-state`，无轮询 |
| `/threads/{id}` | `data-ada-island="transcript"`（有历史消息时） | 原有 `#transcript` 行 SSR | `GET /partials/transcript/{id}`，结构变化时刷新 |

- `shell.html.tmpl` 只在 `NeedsUI` 页面加载引擎/组件资源；目前由
  `overviewData`、`handleExtensions`、`handleWorkspaces`、`handlePluginDetail`
  以及有历史消息的 `handleThread` 显式置位。
- 概览岛的插件贡献区仍在岛外，由服务端 `dashboard.widgets` 插槽渲染；客户端岛只接管
  统计卡与最近会话。
- 插件/工作区/详情岛的启用、停用、选择、新建目录仍是普通 `POST` 表单，带 CSRF
  隐藏字段；没有 JS 时使用服务端回退 HTML。
- 概览页原来的 htmx `every 5s` 已移除，避免服务端片段替换掉客户端 DOM；轮询改由
  `islands.js` 调 JSON 接口。
- 对话流岛只接管持久化行：助手行由 `parts[]` 数据 + `ui-markdown` 渲染；用户/工具/
  Notice 行仍用服务端 row HTML。结构变化时拉取 `/partials/transcript/{id}` 并用 key
  做 VNode patch；流式文字走 `#live-stream`，实时 Markdown 由 `/markdown.ast` 返回
  VNode AST，不再回传 HTML。终态/等待答复仍整页 reload 以刷新交互托盘与输入区。
- 工具行在岛内通过 `.ada-transcript__item > .ada-disclosure[data-kind="tool"]`
  缩进 40px，与助手正文（头像 28 + 间距 12）对齐；工具状态变化按 800ms 合并窗口
  刷新，连续工具调用不会逐条刷屏。

### 15.8 已知限制

- **`ui-` 前缀是硬约定**：`<Button>` 在浏览器里会变成原生 `<button>`；组件统一用
  `<ui-button>`，原生标签不会被解析成组件。
- `Select` 依赖 `ada-select.js`，`Modal` 依赖 `ada-modal.js`；缺脚本时只有静态结构。
- `Table` 不做排序/分页/虚拟滚动；需要时用 `col.render` 或虚拟列表。
- `Tabs` 的所有面板都会挂载（非当前面板 `hidden`），大面板需要自己延迟渲染。
- 组件根节点不能是一个会渲染为 `null` 的 `v-if`；需要条件根时用 `v-show` 或外层包一层
  容器（引擎要求组件子树始终有根）。
- `Toast` 是命令式服务，必须能访问 `document.body`；它不参与组件树 diff。
- `Icon` 依赖 Go 的 sprite；没有 sprite 时 `<use>` 为空。
- `Button` 目前不透传任意 `class`；自定义 class 请用 `Card` 的 `class` 或在外层包裹。
- `Modal` 的 `open()/close()` 方法需要通过 `ref` 拿到组件实例后调用；模板里用
  `v-model` 更直接。
- 对话流岛的助手行是 `ui-markdown` 组件（`parts[].nodes`）；用户/工具/Notice 行仍是
  服务端 row 模板 HTML。Markdown 只在 Go goldmark + bluemonday 解析/清洗，前端不
  解析原始 Markdown，也不接收 HTML 字符串；`/markdown` HTML 端点保留给无组件回退。
  结构变化时会拉取完整行 JSON（不是增量 patch），极大对话下仍有带宽成本；流式文字
  仍走 live-stream，终态/等待答复仍 reload。

## 19. UED 规范（用户体验设计系统）

### 19.1 设计原则

1. **渐进增强优先**。无 JS 时核心流程可用（表单提交、会话阅读、meta refresh 轮询）；JS 是增强而不是依赖。新交互必须先回答「noscript 下怎样」。
2. **令牌单源**。颜色只引用 `--ph-*`，尺寸/字号/圆角只引用 `--ada-*`，动效只引用动效令牌。业务 CSS 零字面值（十六进制、px 即兴值、裸秒数）。
3. **克制**。单色品牌、无渐变、无发光、无彩虹标签（沿 07 §7.7.2）；动效只做状态反馈，不做表演。
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

- 品牌主题为 **Black Swan（黑天鹅）**：宣纸暖白画布 `#F6F3EC` × 墨黑 `#16171A`（取自 logo 天鹅主体）；暗色为墨纸反转。它是 `tokens.css` 的默认值——**不设 `data-theme` 属性即黑天鹅**，任何主题层不得在缺省态改变它。
- **主题 = 品牌衍生色板**。调色板维 `html[data-theme="<id>"]` × 明暗维 `html.light`/`html.dark`，两维正交；主题层（`ui/themes.css`）选择器用 `html.light[data-theme="…"]`（特异性恒胜基线），只覆盖 `--ph-*`，不新增选择器形态。
- 品牌底上的文字/图标一律 `--ph-on-brand`，禁止写死 `#fff`/`#000`；状态色成对使用「浅底 + 深字」（`*-soft` 配 `*-ink`），不能只换文字色。
- 主题（调色板）不是「皮肤商店」：新增主题必须走 19.3.3 准入清单，且风格仍须符合「纸面 × 单色品牌」的品牌气质。
- 首发色板：`black-swan`（默认）、`moss`（苔绿，承接 M2 绿色身份）、`ocean`（深海）、`dusk`（暮色）。

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
| 覆盖层 | `static/app-local.css` | SSR 本地细节 | **禁止重定义**任何既有 `--ph-*`/`--ada-*`（M2 双源教训）；本地**新增**令牌（上游标尺没有的，如 `--ada-duration-*`）集中在本文件 `:root` 定义并登记进 §19.3.2 |

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
- [ ] 8 态截图走查（每色板 × 明暗）：主按钮、代码块语法高亮、滚动条、弹窗、审批托盘；
- [ ] 登记进 19.2.2 色板清单与主题面板（`ui.go` 色板表）。

### 19.4 组件与 DOM 规范

- 类名 `ada-` 前缀；状态用 `data-*` 属性或 `is-*` 类表达，不拼条件 class 串；Web Component 刻意不挂 shadow root（SSR 同树样式）。
- **标题层级**：一篇页面只有顶栏一个 `<h1>`（页面标识）；正文页面标题用 `<h2 class="ada-page-title">`，节标题 `<h2 class="ada-section-title">`。二级页历史上有过「顶栏 h1 + 正文 h1」并列，读屏按标题跳读会听到两遍同一页名，已统一。
- 二级页（overview/extensions/workspaces/login/plugin_detail）已统一到 `ada-*` 组件方言（批次 D 完成）：页面骨架用 `ada-page-title/ada-section-head/ada-panel/ada-chip/ada-table/ada-empty-card/ada-link`（app-local.css）。`app.css` 里的基线类与工具类是**冻结的存量**：不许新增使用；新页面一律走 ada-* 方言。
- **重设计批次的例外**：Token 用量 / 插件 / 概览 / 插件详情四页在 `static/ui/pages.css` 用页面专属前缀（`tu-* / ex-* / ov-* / pd-*`）。原因是 `app-local.css` 里存量的页面级规则（`ada-plugin-*` 等）加载在 `pages.css` **之后**、特异性更高，沿用旧类名会被静默覆盖（搜索框「框中框」就是一个实例）。新前缀只用于页面局部结构，颜色仍只取 `--ph-*` 令牌、尺寸/圆角仍只取 `--ada-*` 令牌；宿主内置页的 Go 模板与 `static/islands.js` 客户端岛必须共用同一套类，保证 SSR 与 hydrate 前后是同一棵 DOM。
- **插件页共用一套页面语言**：插件自带的管理页同样用自己的页面前缀（`pm-*` 供应商 / `em-*` 环境变量 / `mc-*` MCP 服务端 / `cm-*` 定时任务 / `tu-*` Token 用量），但结构一律照同一套模板：页头（标题 + ⓘ tooltip + 操作）→ 汇总条（数量 + 状态筛选分段 + 最后刷新）→ 卡片化列表（表头与数据行共用一套网格）→ 底注。一次性反馈走底部 toast，长说明收进 ⓘ，警告用左侧色标提示条而不是满宽告警边框；新增插件页照这套来，不另起控件形态。
- **浮层卡片的玻璃态**：`chat.corner` 的 Todo 卡片默认不透明；仅在浏览器支持 `backdrop-filter` 时降到半透明底 + 背景模糊（收起态更透、展开态稍实），`prefers-contrast: more` 下回到不透明。「半透明但没模糊」会让文字与底下正文叠在一起，不能作为降级方案。
- **焦点环**：2px 品牌描边 + 3px 半透明光环（`.ada-focusable` 语义）；`button/a/[tabindex]` 有兜底，但**每个新增可聚焦元素（尤其 `<summary>` 触发器）必须显式进入焦点清单**——键盘 Tab 可达且可见。
- CSP `style-src 'self'`：模板与 JS 禁止内联 style 属性（`adag verify` 把守）；动态预览色一律走 CSS 变量类（如 `.ada-swatch--moss`）。
- 表单：Enter 提交必须处理 IME（`isComposing` 必拦）；破坏性操作必须走 `data-confirm`，后果句见 19.2.3。
- **提交态**：整页 POST 的表单在提交后必须有可见反馈与防重复——表单打 `data-submitting` + `aria-busy="true"`，提交按钮降透明度、`cursor:progress`、禁二次点击（`pointer-events:none`），**不用 `disabled`**（`disabled` 的 submitter 不参与表单数据，`name="decision"` 这类按钮的值会丢）。`data-confirm` 拦下的提交不进入提交态，等用户确认后 `requestSubmit()` 再来一次；bfcache 恢复时（`pageshow.persisted`）必须复位。
- **输入草稿**：主 composer（新建任务与会话页）的未发送文本按 `data-draft-key` 存 `localStorage`（`a_da_draft:<key>`），整页刷新/热更新后恢复，提交成功或清空输入时删除；图片是 `File` 对象，不进文本草稿。新增 composer 必须带唯一的 `data-draft-key`。
- **会话行右键**：侧栏会话行提供「复制会话 ID / 归档会话」；运行中的会话禁用归档。归档只从列表隐藏，不软删历史；当前会话归档后回首页。模型/工作区选择随 `state.db` 持久化，刷新或 Core 重启后保持。
- **待发送队列**：运行中发送的消息先进入输入框上方的 outbox 浮窗，不立即 `turn.steer`；每条提供编辑 / 立即发送 / 删除（纯图标按钮），队列按会话存 `localStorage`，刷新不丢；当前轮正常 `completed` 时自动发队首，其余继续排队。带图片的消息仍走原生立即发送（`File` 不能跨刷新保存）。
- **输入区指标**：输入框下方只放只读信息（缓存命中率、上下文占用环），不再重复放「中止」；中止只在顶栏。这块整体由 `composer-telemetry` 插件的 **纯客户端岛屿** 渲染（`composer.footer` + `clientOnly`）：基座只保留 `#composer-aux` 外壳，不画内容——插件停用或 `ADAG_DISABLE_ISLANDS` 时该区整块空白（没有 SSR 回退，刻意的取舍）。运行/上下文来自基座通用投影 `GET /threads/{id}/state`（`run` / `context`），token/缓存/成本来自插件自己的 `GET /ext/composer-telemetry/api/usage`（`events.read` 原始行聚合）；岛挂载时拉一次，运行中每 2s 轮询、进入终态即停。上下文占用按 head 路径 entry token 估算，带「约」标注；缓存命中率 = cacheTokens / inputTokens。上下文环本身是 `<details>` 触发器：点开/键盘展开后给出三段分项（系统提示词 / 工具定义 / 对话消息）与合计占比，三段都用引擎同一套估算（引擎水位与界面占比不许各算一套），并在地到压缩水位时给出提示；分项只读，不放任何可点操作。

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
- 全局通知只有一条实现路径：app.js `notify()`（经 window.AdaApp.notify 供模块复用）+ 服务端 flash；`AdaUI.toast` 已移除（批次 B）。
- **反馈观感统一**：宿主 flash（`.ada-flash[data-flash]`）与各插件页 toast（`tu-/mc-/pm-/em-/pd-toast`）是同一套「底部居中深色胶囊」——`bottom:28px`、`--ph-ink` 底 / `--ph-canvas` 字、错误态 `--ph-danger-ink`、`--ada-radius-md` 圆角、`min(460px, 100vw-40px)` 宽度、160ms 由下往上。服务端 flash HTML 与 `data-flash` 属性不变（无 JS 时仍可见），只由 CSS 决定观感；`app.js` 退出动画必须保留 `translateX(-50%)`，否则淡出瞬间会水平跳位。关闭按钮视觉收起但保留 DOM，`:focus-visible` 时重新显现，保证键盘可达。
- 连接类提示必须带动作按钮（「重新连接」），不得只说「请刷新页面」。

#### 19.5.4 加载与空态

- 空态三件套：图标 + 标题（6–10 字）+ 说明句；可行动的空态加行动点（范本：chat 空态）。
- 加载：首屏靠 SSR（无骨架屏）；局部刷新用行内 spinner 或文本占位；岛内轮询在用户焦点/悬停于岛内时跳过该轮。
- 局部刷新失败不得静默：至少 notify 一句；会话历史截断必须显式提示或提供「加载更早消息」。

#### 19.5.5 分步表单（ask-wizard 模式）

一题一步；非末步 Enter = 下一步而非提交；未到达步骤的字段必须 `disabled`（不参与提交）；步进后自动聚焦当前字段；无 JS 时全展开直接提交。末步真正提交前，脚本先把全部步骤重新启用——`disabled` 控件不随表单提交，只带末题会让服务端按 `q0`、`q1…` 拼出的 `answers` 缺项，`ask_user` 会以「回答数量与问题不一致」拒绝。

#### 19.5.6 流式输出

- live 语义：生成中 `role="status"`，错误 `role="alert"`；新增动态区域照此二分。
- 滚动意图识别：用户上翻即停止强制跟随，「回到最新」出现；此逻辑收敛在 chat-live.js，不重复实现。
- 运行中反馈 = 输入框上方一行品牌色「深度求索中，用时 N 秒 …」+ 工具行呼吸条 + 秒级耗时刷新；状态文案与计时由同一处运行态驱动，不再用带边框的 warning 药丸。新运行态 UI 复用同一套。

### 19.6 可访问性标准

基线 = WCAG 2.1 AA：

- 对比度：正文与小字 ≥ 4.5:1，大字/图标 ≥ 3:1；**已知差距**：存量 `--ph-faint` 浅色 4.3:1（随主题重构修复）。
- 键盘：Tab 序 = 视觉序；modal 陷阱与归还；skip-link 保留。
- aria：图标 `aria-hidden`；动态区 `role=status/alert`；下拉高亮同步 `aria-activedescendant`；级联菜单项补 `role=menuitem`；抽屉/面板开合同步 `aria-expanded`。
- 触屏：`@media (hover:none)` 下常驻原本 hover 才出现的操作（现状已覆盖，新增交互不得破例）。
- `prefers-reduced-motion` 全覆盖（19.2.4）。

### 19.7 治理与门禁

- **门禁**：`go test ./...`、`go vet ./...`、staticcheck、`adag verify`（13 项，其中 ui 项校验 tokens 来源头、a_da 副本清单、模板无内联 style）；UI 变更加走 19.3.3 主题清单 / 本篇对应条款的自查。
- **文档先行**（沿 design.md 约定）：改令牌、组件、交互模式前先改本篇与 07 §7.7，再改实现；`adag verify ui` 的基线断言同步更新。
- **镜像纪律**：`tokens.css`、`app.css`、`theme.js` 等上游镜像文件不做本地修改；本地能力一律以新文件叠加（先例：`app.js` 的窄屏补丁、本次的 `themes.js`/`themes.css`）。
- **自成一体模块的界面同样遵守本篇**：`mcp_projects/adago_design` 是独立 Go module（`GOWORK=off`），
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
