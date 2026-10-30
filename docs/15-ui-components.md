# ada-ui 组件层

> 实现：`internal/gateway/web/static/ada-ui.js`、`ada-ui.css`
> 依赖：[ada-render.js](14-render-engine.md)、`ada-select.js`、`ada-modal.js`
> 测试：`internal/gateway/web/testdata/ada-ui.test.js`、`tools/render-demo/ui.html`
> 版本：0.1 · 2026-10-30

## 0. 定位

`ada-ui.js` 是 `ada-render.js` 之上的一层通用组件，不引入新的视觉语言：

- class、DOM 与状态语义复用现有 `app.css` / `ui/components.css` / `ui/widgets.css` /
  `ui/layout.css` / `ui/shell.css`；
- 下拉与弹窗继续由现有 `<ada-select>` / `<ada-modal>` Web Components 提供行为；
- `ada-ui.css` 只补项目基线里还没有的控件：滑块、进度、开关、选择项、Toast、
  下拉面板对齐、表格修饰；
- 没有构建步骤、没有 npm 依赖，仍然是同源静态文件。

## 1. 加载顺序

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
`/overview` 与 `/extensions`；其它页面保持原有 SSR 资源，不额外下载引擎。

## 2. 用法

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

### 2.1 模板标签必须用 `ui-` 前缀

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

### 2.2 布尔 prop

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

## 3. 组件清单

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

## 4. Props / 事件 / Slots

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

## 5. 样式边界

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

## 6. 无障碍与 SSR

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

## 7. 测试

```bash
# 33 个 Node 用例：引擎 18 + 组件 15
mingw32-make test-js
# 或
node --test internal/gateway/web/testdata/ada-render.test.js internal/gateway/web/testdata/ada-ui.test.js
```

浏览器冒烟：直接打开 `tools/render-demo/ui.html`，标题应为
`ada-ui smoke: PASS`，`#log` 全为 PASS。该页是 `file://` 冒烟页，使用了内联脚本与
演示 CSS，不适用于网关 CSP。

概览/插件岛另有独立冒烟页：

- `tools/render-demo/overview.html` → `overview island smoke: PASS`
- `tools/render-demo/extensions.html` → `extensions island smoke: PASS`

Go 侧用例：

```bash
go test ./internal/gateway/web -run 'TestOverviewIslandAndDashboardJSON|TestExtensionsIslandAndJSON' -v
```

### 7.1 当前项目接入状态

| 页面 | 方式 | 服务端回退 | 数据接口 |
|---|---|---|---|
| `/overview` | `data-ada-island="overview"`，`islands.js` 挂载 | 统计卡/最近会话的 `overview_partial` SSR | `GET /partials/dashboard.json`，5s 轮询 |
| `/extensions` | `data-ada-island="extensions"` | 原有插件表格 SSR | `GET /partials/extensions.json` |
| `/threads/{id}`、`/workspaces` 等 | 保持原 SSR + 现有 JS | — | — |

- `shell.html.tmpl` 只在 `NeedsUI` 页面加载引擎/组件资源；目前由 `overviewData` 与
  `handleExtensions` 显式置位，插件详情页不会额外加载。
- 概览岛的插件贡献区仍在岛外，由服务端 `dashboard.widgets` 插槽渲染；客户端岛只接管
  统计卡与最近会话。
- 插件岛的启用/停用表单由客户端渲染，但仍是普通 `POST` 表单，带 CSRF 隐藏字段；
  没有 JS 时使用服务端回退表格。
- 概览页原来的 htmx `every 5s` 已移除，避免服务端片段替换掉客户端 DOM；轮询改由
  `islands.js` 调 JSON 接口。

## 8. 已知限制

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

---

**相邻文档**：[← 前端渲染引擎](14-render-engine.md) · [索引](design.md) · [SSR 前端](07-ssr-frontend.md)
