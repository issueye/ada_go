# SSR 前端

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§7 SSR 前端  
> 版本：0.1 · 2026-10-28

## 7. SSR 前端

### 7.1 技术选型

| 关注点 | 选择 | 理由 |
|---|---|---|
| 模板 | Go `html/template` + 组件函数，结构照搬 a_da 的 Askama 模板 | 标准库、编译期检查、默认转义；DOM 与 class 保持同形 |
| 局部更新 | HTMX + 原生 `EventSource` | 少量 JS，表单/SSE 都能渐进增强 |
| 样式 | 直接复用 a_da 的 Black Swan token 与 `ada-*` 分层 CSS | 不重新配色、不另造视觉语言；不引入 Tailwind 构建链，只使用其预编译产物 |
| 插件模板 | TSX → esbuild → moejs → VNode → Go 渲染 | 插件作者体验接近 React，但服务端无 hydration、无客户端运行时 |
| 静态资源 | `go:embed` + 内容哈希 | 单二进制、可缓存、可校验 |
| 图标 | a_da 的线性 SVG 集（stroke 1.75） | 与组件库同源；不依赖图标字体，不用 emoji 当图标 |

明确不做：SPA、客户端路由、React hydration、客户端插件运行时、CSS-in-JS、运行时 Tailwind 扫描。

**UI 来源**：视觉语言、设计令牌、组件、DOM 结构、class 名、插槽位置与交互规则全部以 `E:\codes\rust_projects\a_da` 的 `design/tokens.css` 与 `crates/agent-web` 为准；ada-go 只替换实现技术（Askama → `html/template`，Rust 组件 → Go 组件，OXC+Boa → esbuild+moejs），不重新设计界面。细节见 [7.7](#77-ui-与设计元语直接复用-a_da)。

### 7.2 页面与布局

```
┌──────────────┬──────────────────────────────────────────────────────┐
│ Sidebar 264  │ Topbar 54：会话标题 / 模式与审批档位 / 运行中 / 刷新 / 停止 │
│              ├──────────────────────────────────────────────────────┤
│ 品牌          │ Transcript（SSE 增量推送，阅读宽度 840–880）          │
│ 新会话        │  用户消息 / 助手 Markdown / 思考与工具折叠            │
│ 主导航        │  Notice / Diff / 空状态 / 回到最新                    │
│ 工作区树      │                                                      │
│ 会话列表      │                                                      │
│ 插件导航      ├──────────────────────────────────────────────────────┤
│ 侧栏底部插槽  │ 交互托盘（审批 / 提问 / 排队，贴在 Composer 上方）    │
│              │ Composer：输入卡 + 工具行（左/右插槽）                │
│              │ ComposerAux：状态 / 遥测 / 快捷操作 / composer.footer │
└──────────────┴──────────────────────────────────────────────────────┘
```

外壳尺寸、滚动归属与 a_da 完全一致：设计基准视口 1600×1000，视口锁定 `100dvh`（`100vh` 回退），页面本身不滚动，只有对话流、侧栏列表与二级内容滚动；侧栏 264px（≤960px 为 232px，≤760px 收为抽屉），主区最小 420px，顶栏 54px，Composer 固定底部，内容内边距 24–28px（窄屏 16–18px）。外壳 class 与 a_da 当前基线一致：`ada-shell` / `ada-sidebar` / `ada-main` / `ada-topbar` / `ada-scroll` / `ada-composer` / `ada-rail`（折叠窄轨），由 `internal/gateway/web/ui.go` 的侧栏组件与 `templates/shell.html.tmpl` 输出。

品牌位：侧栏始终显示 logo + 品牌名（产品 `identity.name`，默认 `ada-go`）+ 产品徽标（产品名去掉品牌前缀，如 `Headless`）；页面 `<title>` 用品牌名，顶栏 chip 用产品徽标。品牌名与产品名都由 Gateway 从产品声明注入，模板不写死；登录页也带同一套品牌位。

12 个稳定插槽（其中 11 个与 a_da **同名同位**；`thread.tray` 是 ada-go 为通用交互新增的第 12 个）：

| 插槽 | 位置 |
|---|---|
| `sidebar.top` | 侧栏品牌区下方 |
| `sidebar.bottom` | 侧栏导航之后 |
| `topbar.right` | 顶栏右侧操作区 |
| `chat.corner` | 会话区右上角悬浮 |
| `composer.left` | 输入框工具行左侧 |
| `composer.right` | 输入框工具行右侧 |
| `composer.footer` | 输入框下方辅助区 |
| `composer.above` | 输入框上方悬浮区（sticky，浮在对话流底部） |
| `thread.tray` | 交互托盘内的插件位：接管提问卡等内容渲染；托盘外壳与提交动作仍由基座 |
| `transcript.after` | 对话流末尾 |
| `extensions.list` | 插件列表页 |
| `dashboard.widgets` | 概览页卡片区 |

插槽是不可轻易增加的兼容承诺；新增插槽需要同时更新清单校验、组件文档、示例插件与 golden 测试。

**UI 边界**：基座拥有外壳、通用消息/工具/交互渲染与安全提示；插件只能填充这 12 个槽与 `/ext` 页面，不能替换交互托盘外壳、安全操作按钮、登录页、错误页。插件也不能要求基座提供业务字段（例如 `todo`）；业务数据通过通用 `backend` 只读动作结果传入。

### 7.3 组件与渲染

宿主组件（Go 实现，既能在 `html/template` 里用，也能被 VNode 渲染器按名字调用；名字、DOM 与 class 以 a_da 组件库为准，全部是通用组件，不含业务语义）：

`Button`、`IconButton`、`SendButton`、`Chip`、`TextField`、`Select`、`Tabs`、`Modal`、`ConfirmDialog`、`Disclosure`、`Panel`、`UserMessage`、`AssistantMessage`、`Notice`、`EmptyState`、`Composer`、`ComposerAux`、`ModelPicker`、`ModelTable`、`QueueTray`、`Form`、`ToolCall`、`TodoList`（TSX 名 `Todo`）、`Icon`、`Slot`；另有 `InteractionTray` 把审批/提问/排队归一化为同一托盘，`Markdown` 负责已清洗正文的最终排版。逐项映射见 [7.7.3](#773-组件与-dom)。

TSX 侧通过虚拟模块引用：

```tsx
import { Button, Chip, Panel, Disclosure, Icon, Todo } from "ssr:components";

export function Slot(props: SlotProps) {
  const plan = props.data.backend["task-plan"]?.plan as any;
  if (!plan || !plan.total) return null;

  return (
    <details class="tp-card" data-state="collapsed">
      <summary class="tp-card__summary">
        <Icon name="i-list" size={12} />
        <span>任务规划</span>
        <span>{plan.done + "/" + plan.total}</span>
      </summary>
      <div class="tp-card__body">
        <Todo steps={plan.steps} notes={plan.notes} />
      </div>
    </details>
  );
}
```

限制（刻意）：没有 hooks、没有状态、没有副作用、没有事件处理函数；`onClick` 等属性直接拒绝。交互通过 HTML 原生元素（`<details>`、`<form>`、`<a>`）或 `data-*` + 宿主 JS 完成。这让 SSR 结果确定、可缓存、可测试。

VNode JSON 形态：

```json
{ "t": "div", "p": { "class": "card" }, "c": [
  { "t": "$C", "n": "Button", "p": { "label": "打开", "href": "/" }, "c": [] }
] }
```

校验：

- 允许的 HTML 标签白名单；组件名白名单；
- 属性白名单；禁止 `on*`、`style` 属性（一律走类名）、`dangerouslySetInnerHTML`、`javascript:` URL；
- 最大深度 64、最大节点 2000、单字符串 32KB、总输出 256KB；
- 所有文本与属性值由 Go 侧转义；插件永远拿不到 `template.HTML`；
- 失败时该插槽渲染为空并记录错误，不影响页面其余部分。

### 7.4 流式渲染与 SSE

`GET /threads/{id}/stream`：

- 响应头：`Content-Type: text/event-stream`、`Cache-Control: no-store`、`X-Accel-Buffering: no`。
- Gateway 把 Core 的 `evt.*` 投影成浏览器事件：`snapshot`、`patch`、`delta`、`fragment`、`run`、`notice`。
- 事件 envelope：

```
id: 128
event: patch
data: {"threadId":"th_...","seq":128,"baseSeq":120,"ops":[
  {"op":"upsert","key":"msg:01J...","fingerprint":"...","item":{...}},
  {"op":"remove","key":"tool:call_1"}
]}
```

- 文本增量单独走 `event: delta`，只带 `messageId` 与 `text`，避免每次都渲染整段 Markdown。
- 结构变化（消息开始/结束、工具卡状态、交互请求、运行状态）由 Gateway 用模板渲染 HTML 片段，`event: fragment` 带 `target` 选择器与 `html`，前端只做 `outerHTML` 替换。
- 合并：40ms 窗口合并增量，最大延迟 100ms；单帧超过 256KB 时降频。
- 心跳：每 15s 发注释行 `: ping`；完成本轮后 200ms 内做一次全量 tail 同步，防止丢帧。
- 断线重连：浏览器带 `Last-Event-ID`；Gateway 比较 `seq`，连续则补发，断裂则发 `event: snapshot` 全量快照。
- 单线程最多 64 个 SSE 客户端；超限返回 429，页面降级为轮询/刷新。
- 服务端每线程一个 SSE hub，向所有客户端扇出；客户端断开只减计数，不取消轮次。

为什么不用“全量 HTML 每帧替换”：长对话下会闪烁、丢焦点、丢滚动位置；增量 JSON + 结构片段是更稳的折中。

### 7.5 前端插件化

**清单**见 [6.2](05-plugins.md#62-清单)。前端部分支持：

- `slots`：插槽 + TSX 模板 + 排序；
- `pages`：`/ext/{id}/{slug}` 独立页面；
- `nav`：侧栏导航项；
- `dashboard`：概览页卡片；
- `actions`：读/写后端动作绑定；
- `api`：插件自己的 JSON 端点（GET/POST，模板渲染或直接 JSON）；
- `styles` / `scripts` / `assets`：静态资源。

**渲染链路**：

1. 官方插件取构建期预编译的 `dist/*.js`；第三方在加载时用 esbuild 打包（bundle、ESM、external `ssr:components`），得到单个 JS 模块；
2. 每个 Render Runtime 初始化时注册虚拟组件模块，执行页面/插槽模块取得导出函数；
3. 请求时构造 `props`（服务端数据 + 预取动作结果），`CallContext` 执行 `Slot/Page`；
4. 返回值 `Decode` 成 Go `any`，校验 VNode，渲染 HTML；
5. 失败/超时：插槽留空并记录；页面显示错误面板（开发模式带堆栈）。

Render Runtime **不安装 Node 兼容层**：没有 `process`、`Buffer`、`fs`、`fetch`、`crypto` 模块，也不加载后端 stdlib。TSX 只做纯计算与 VNode 组装；需要的数据一律通过 `props`（含只读动作预取）进入，副作用走动作桥。

**只读数据投影**（`backend.read` 能力位，与只读动作预取同一个门禁）：

- `data.providers`：`provider.list` + `model.list` 的供应商/模型清单与当前模型（不含 API Key）；
- `data.usage`：`usage.get` 的工作区与当前线程聚合（输入/输出/缓存 token、成本）；
- `data.interactions`：`thread.get` 返回的当前线程待答交互（审批/选择/输入）；
- `app.csrf`：写表单的双提交 token（与基座模板里的隐藏字段同源）。

写操作仍走基座路由（如 `POST /model/select` 切换当前模型）或插件动作；投影只读，不改 Core 状态。当前渲染运行时的组件实现面是 `Icon` / `TodoList`，其它组件名虽在白名单里，执行会返回「尚未实现」——官方插件优先用原生标签 + `ada-*` class。

**Props**：

```ts
type SlotProps = {
  plugin: { id: string; name: string; version: string };
  app: { workspace: string; product: string; route: string; theme: "light" | "dark" };
  slot: string;
  data: {
    thread?: ThreadView;      // 标题、状态、条目计数
    run?: RunView;            // 当前轮次状态、耗时、模型
    interactions?: InteractionView[];
    usage?: UsageView;
    backend: Record<string, Record<string, unknown>>; // 只读动作预取结果
  };
};
```

- 数据是**投影**，不是任意查询；插件不能借 props 读别的线程或文件。
- 后端只读动作最多 16 个、并发 4、单个 300ms、结果缓存 8s；失败只影响对应 `backend.<id>.<name>`。
- 写动作必须用户点击，走 CSRF + 确认；`read: false` 的动作不会在 SSR 时执行。
- 插件 JS（`scripts`）默认不加载；需要 `frontend.script` 能力且信任级别为 builtin/verified。第三方 JS 同源运行，仍是风险点；v1.1 评估 iframe 隔离，v1 建议优先用 HTMX/表单。

### 7.6 动作桥与动态接口

```
浏览器 POST /ext/{id}/action/{binding}
  → Gateway 校验会话、CSRF、Origin、插件状态
  → 查绑定的后端动作名
  → Core action.invoke { plugin, action, input, threadId }
  → 插件 execute(input, ctx) → JSON
  → 写动作 303 回页面；读动作返回 JSON
```

- 动作不是工具：不进模型上下文，不受 `tool.beforeCall` 策略约束，但必须在界面上由用户点击确认，且受插件能力与 scope 约束；`exec` 能力不对第三方开放。
- 所有插件 HTTP 响应都带 `Cache-Control: no-store`（静态资源除外），避免跨用户缓存。

#### 7.6.1 插件动态 HTTP API（route）

后端插件在加载期登记自己的数据接口，供自己的前端页面/脚本调用：

```ts
agent.route("GET", "/items/*", (req, ctx) => ({ items: [] }));
agent.route({ method: "POST", path: "/items", handler(req, ctx) { return { ok: true }; } });
```

```
浏览器 GET/POST /ext/{id}/api/{path}
  → Gateway 校验会话、CSRF、Origin、插件状态（请求体上限 1MB）
  → Core plugin.http { pluginId, method, path, query, body }
  → 匹配插件登记的精确/前缀路由 → handler(req, ctx) → JSON
  → 未命中 404；方法不符 405；插件失败 502；错误统一 {"error":{"message":"…"}}
```

- 只允许 GET/POST，只接受 JSON 请求体与 JSON 返回值；`body` 只对 POST 透传，单次响应上限 64KB（宿主强制）。
- 路由按插件隔离；同一方法+路径重复登记在加载期报错；`/*` 是前缀匹配，`Path` 保留剩余段由插件自行解析（[G-14](decisions.md)）。
- 接口不是工具、不进模型上下文；能力位与 scope 仍由插件清单决定，`exec` 等能力不因此放开。
- 浏览器侧统一用 `window.adaAPI(pluginId, path, { method, body })`（`api.js`，自动带 CSRF），不要手写 `/ext/...` URL。

### 7.7 UI 与设计元语：直接复用 a_da

ada-go 的界面不是“参考 a_da 风格”，而是**直接使用 a_da 的 UI 与设计元语**：同一套 token、同一套 `ada-*` 组件样式、同一套 DOM 结构、class 名、插槽位置与交互规则。只替换服务端实现技术，不产生第二套视觉语言。

#### 7.7.1 复用清单

| a_da 资产 | ada-go 落点 | 复用方式 |
|---|---|---|
| `design/tokens.css` | `web/static/tokens.css` | 原样复制；所有 `--ph-*` 颜色的唯一真源 |
| `crates/agent-web/static/app.css` | `web/static/app.css` | 直接使用 a_da 的**预编译产物**（含 token 段与工具类）；ada-go 不运行 Tailwind，只固定并校验该产物 |
| `components.css` / `layout.css` / `widgets.css` / `shell.css` / `pages.css` | `web/static/ui/` | 原样复制；引用顺序保持 components → layout → widgets → shell → pages |
| `ada-select.js` / `ada-modal.js` | `web/static/` | 原样复制；`<ada-select>` / `<ada-modal>` 的 Web Components 行为不变 |
| `components.js` / `app.js` / `chat-live.js` / `theme.js` | `web/static/` | `components.js`（组件行为）、`app.js`（外壳：提示条/确认弹窗/侧栏）、`theme.js` 原样复制；`chat-live.js` 由 ada-go 维护，消费 `/events/{id}` 的 SSE 增量 |
| `htmx.min.js` / `alpine.min.js` / `logo.png` | `web/static/` | 原样复制；概览页每 5 秒 `hx-get="/partials/dashboard"` 换新 |
| `ui/icons.rs` | `internal/gateway/web/ui.go` | 同一组线性 SVG path、同一 stroke 1.75 与尺寸档位；模板经 `{{icon "…"}}` 或 sprite `<use>` 引用 |
| `ui/components.rs` / `rows.rs` | `internal/gateway/web/ui.go` + `templates/` | Go 组件函数与模板输出 a_da **相同的 `ada-*` 标签、class 与 `data-*` 状态** |
| `templates/*.html`、`partials/transcript.html` | `web/templates/` | 手写 Go 模板，DOM 层级与 class 不变；Askama 语法换成 `html/template` |
| `plugins/task-plan/` | `plugins/official/ui-todo/` | 同一张 Todo 卡片（真实计划来自只读动作；无计划时不占位） |

上游固定到 a_da 的一个 commit。`web/static/` 中的副本在文件头记录来源与 SHA-256；`adag verify ui` 比对 token 名/值、CSS 层顺序、组件 class 基线与图标集，任何漂移先改文档与基线再改实现。运行时链接顺序为 `app.css` → `tokens.css` → `components/layout/widgets/shell/pages.css`：后加载的 `tokens.css` 覆盖 `app.css` 内联的编译副本，保证颜色仍只有一个可读真源。模板引用静态资源统一走 `{{asset "name"}}`，输出带内容指纹的 `/static/name?v=<hash>`；静态资源本身仍可长缓存，改 CSS/JS 后新 URL 会自动失效旧缓存（桌面壳的 Electron HTTP 缓存也在内）。

#### 7.7.2 设计令牌

- **颜色只引用 `--ph-*`**：`canvas`、`surface`、`elevated`、`line`、`line-strong`、`ink`、`muted`、`faint`、`brand`、`brand-strong`、`brand-soft`、`brand-ink`、`on-brand`、`subtle`、`subtle-ink`、`success-soft/ink`、`warn-soft/ink`、`danger-soft/ink`、`code-bg/ink`、`inline-code-bg/ink`、`focus`、`glass`、`shadow`、`scrollbar*`。业务 CSS 不写十六进制色。
- **Black Swan 主题**：宣纸暖白画布 `#F6F3EC` + 墨黑 `#16171A`；暗色反转为暖黑画布 + 暖白文字，品牌色随之反转。主按钮、选中、焦点都用品牌色，但不大面积铺底；单色品牌下链接必须带下划线。
- **品牌底上的文字一律用 `--ph-on-brand`**，不写死 `#fff`。状态色成对使用“浅底 + 深字”，不能只换文字色。
- **对比度**：正文 ≥ 4.5:1，大字/图标 ≥ 3:1；当前色板最低组合 4.87:1（faint/canvas）。浅色与深色都要逐项验收，不能只看浅色。
- **主题只切 `html.dark`**：`theme.js` 原样复用，`localStorage` 键沿用 `a_da_theme`，无存储时跟随系统；业务 CSS 禁止 `dark:` 变体。
- `--ph-glass` 只出现在 a_da 已经使用它的位置（如 `.app-header` 顶栏），不作为装饰性玻璃效果；不使用渐变、发光边框、彩虹标签。
- 尺寸走 `components.css` 的 `--ada-*` 标尺：字号 10/12/14/16/20/24，圆角 6/10/14/20 + 胶囊，图标 12/14/18，间距 4/8/12/16/20/24。所有值都是 2 的倍数，不写 9px、11.5px 这类即兴值。

#### 7.7.3 组件与 DOM

Go 组件库按 a_da 的组件清单实现，名字与输出对齐：

| a_da 组件 | ada-go Go 组件 | 说明 |
|---|---|---|
| `Button` / `IconButton` / `SendButton` | 同名 | 变体、尺寸、`kind` 与焦点态一致；图标按钮强制 `aria-label` |
| `Chip` | `Chip` | 模式、审批档位、模型；tone 与 caret 行为一致 |
| `TextField` / `Select` | 同名 | `Select` 仍是组件化下拉，不是原生 `<select>` |
| `Tabs` / `TabItem` | `Tabs` | 导航条归组件库，面板归页面；无 JS 时是真实链接 `?tab=` |
| `Modal` / `ConfirmDialog` | 同名 | 必须用 `<ada-modal>`；不用原生 `<dialog>`、`window.confirm` |
| `Disclosure` / `Panel` | 同名 | 工具、思考、压缩的单行折叠与展开面板 |
| `UserMessage` / `AssistantMessage` | 同名 | 用户右对齐气泡；助手 26px 头像 + Markdown 正文（`--ada-text-base`） |
| `Notice` / `EmptyState` | 同名 | 错误、重试、空状态；不显示假统计 |
| `ToolCall` | `ToolCall` | 单行日志式工具卡：28px 行高、12px 正文、左侧 2px 状态条、结果尾标、输出在前参数在后、单层折叠 |
| `Composer` + `ComposerAux` | 同名 | 18px 输入卡、32px 圆形发送键、左/右工具行；辅助区状态 → 遥测 → 快捷操作 → 插槽 |
| `ModelPicker` | `ModelPicker` | 模型药丸 + 向上弹层（模型分组 / 推理等级） |
| `ModelTable` | `ModelTable` | 供应商模型表：固定列宽、省略号 + `title`、行级操作；沿用 a_da 的 `.ada-table` 样式，作为 TSX 组件导出 |
| `Form` | `Form` | TSX 表单容器；提交按钮用 `<Button kind="submit">` |
| `QueueTray` | `QueueTray` | 排队消息：提升、删除、清空 |
| `Slot` | `Slot` | 插槽容器；贡献为空时回退 |
| `TodoList`（TSX 名 `Todo`） | `TodoList`（TSX 名 `Todo`） | 计划步骤清单，复用 `.ada-todo__list*` 样式 |
| —（a_da 分三个托盘） | `InteractionTray` | ada-go 唯一的基座新增壳组件：把审批/提问/排队归一化为同一托盘；审批与排队 in-flow，提问浮动，视觉与 a_da 一致 |

规则：

- 类名统一 `ada-` 前缀；状态一律 `data-*`（`data-state="open"`），不拼 class。
- **布局类落在 `components.css` / `layout.css`，模板不写任意值 utility**：`app.css` 是预编译产物，模板里现写的 Tailwind 任意值类不会生成样式，只会静默失效。
- 弹层类 UI 一律走 `ada-select` / `ada-modal`：开合、焦点圈、Esc、遮罩、滚动锁定都在元素内部；弹窗必须放在页签容器之外。
- 焦点态统一 2px 品牌描边 + 3px 半透明光环；组合式输入卡用 `:focus-within`；所有可点击元素有 `:focus-visible`。
- 图标 14–16px（操作强调可到 18px），stroke 1.75、圆角线帽、`currentColor`；品牌 mark 用产品 LOGO，不加装饰性大插画。
- 滚动条槽位 6px、可见 4px 圆条，三态走 `--ph-scrollbar*`；`thumb` 必须用 `background-color`；滚动容器加 `overscroll-behavior: contain`，对话流与会话列表加 `scrollbar-gutter: stable`。

#### 7.7.4 交互与状态

沿用 a_da 的八条设计原则：操作面唯一、状态必须真实、阅读优先、布局稳定、渐进披露、可恢复、暗色等价、失败安全。

- **状态颜色**：运行中用 warn（顶栏徽标）或品牌色（左侧呼吸条）；等待批准用 warn；待答复用 brand；失败/停止用 danger。完成态不加绿标；颜色不是唯一信号，同时有文字或图标。
- **布局稳定**：审批/排队托盘位于 transcript 与 Composer 之间，出现时不覆盖输入框；提问卡浮在托盘区顶部，不占布局；浮层出现/消失不顶动对话流。
- **Composer**：Enter 发送、Shift+Enter 换行、IME 组合输入中不发送；空内容禁用发送；运行中允许继续输入并明确显示“已排队 N 条”；图片 PNG/JPEG/WebP/GIF，单张 ≤8MB、最多 4 张。
- **工具卡**：执行中的耗时是活的（服务端 `startedAt` + 客户端每秒续走）；长输出只有一层折叠（10 行预览 + “展开全部”），展开后 420px 内滚；JSON 走等宽原文，其余按 Markdown。
- **回到最新**：离开底部时出现底部中央胶囊；点击回到底部并恢复自动跟随；与 `composer.above` 插件卡分开，互不遮挡。
- **通信**：`flash` 横幅 error 9s、其它 6s，可手动关闭；失败 Notice 带“重试这一轮”真实表单；离线保留外壳并显示“未连接/重新连接”，不显示假统计。
- **键盘与焦点**：Esc 关闭浮层并回到触发元素；浮层打开时焦点进入浮层；交互托盘默认聚焦安全选项；`prefers-reduced-motion` 下关闭过渡、自动滚动与呼吸动画。
- **文案**：统一“新会话 / 会话 / 插件 / 供应商 / 概览 / 运行中 / 等待批准 / 待答复 / 排队中 / 完成 / 失败 / 已停止”；工具 id 保留英文并配中文标签；错误写“发生了什么 + 下一步”。

#### 7.7.5 任务规划卡片

Todo 卡片是前端插件，不是基座组件：`plugins/official/ui-todo` 默认挂 `chat.corner`（会话区右上角），数据来自只读动作 `tools-plan` 的 `plan`（没有计划时不渲染）；提问卡片 `ui-ask-user` 挂 `thread.tray`，待答项来自 `data.interactions` 投影，插件让位时基座托盘自动接手。供应商管理（`provider-admin`）与 Token 用量（`token-usage`）分别提供页面与侧栏导航。

- 收起态：胶囊 = 列表图标 + “任务规划” + `3/12` + 当前步骤标题。
- 展开态：320px 卡片，头部（标题 + `3 / 12 已完成` + 复制 + 收起）+ 备注 + 步骤清单（最大 256px 内滚）。
- 步骤状态：完成 = 勾 + 删除线；进行中 = 箭头 + 脉冲 + 品牌浅底；待办 = 空心圆。
- 展开/收起用 `<details>/<summary>`，无 JS 也能用；插件 JS 只记展开偏好并在一帧更新后恢复；复制输出 `- [x] 标题` 形式的 Markdown。
- 数据来自 `props.data.backend["<plan-plugin>"].plan`（见 [附录 A](13-appendix.md#附录-a插件示例)）；基座仍不知道 `plan/steps/total` 这些业务字段。这是相对 a_da 唯一有意的数据差异：a_da 的 `props.data.todo` 由核心投影，ada-go 由工具插件的只读动作提供。

#### 7.7.6 页面范围

基座提供与 a_da 同构的外壳页面（`/`、`/threads/{id}` 是同一个对话页组件，空会话时渲染新会话空态）：

| 路由 | 页面 | 数据来源 |
|---|---|---|
| `GET /`、`GET /threads`、`GET /threads/{id}` | 对话页 / 会话页 | `thread.list`、`thread.get`、`thread.entries` |
| `GET /overview`、`GET /partials/dashboard` | 概览（统计卡、最近会话、插件卡片；htmx 每 5 秒换新） | `thread.list`、`plugin.list`、`dashboard.widgets` 插槽 |
| `GET /workspaces` | 工作区选择（目录浏览限制在当前工作区子树内） | Gateway 配置 + `os.ReadDir` |
| `GET /extensions` | 插件列表（全部随包/已安装插件合并成一张表，不分区；产品未声明的随包插件显示为「未启用 / unused」，不误报 failed） | `PluginLister`、`plugin.list`、`extensions.list` 插槽 |
| `GET /extensions/{id}` | 插件详情（前端/后端归属、能力计数、启停、打开插件页面） | 同上 |
| `POST /model/select` | 切换当前模型（工作区级；供应商管理插件页面调用） | `model.set` |
| `GET /ext/{id}/{slug}` | 插件页 | `PluginUI.Page` |
| `GET /login`、离线页、错误页 | 外壳页面 | — |

二级页与聊天页共用侧栏，内容最大宽度 1040px，表格横向滚动；供应商管理保持 a_da 的 Master-Detail 与两个真实链接页签，但它是**官方前端插件页面**，不是基座业务页——基座只提供 `/ext` 宿主、插槽与动作桥。API Key 只显示“已配置 + 尾号”，不回显。

与 a_da 的已知差异（如实声明）：工作区集合在 Gateway 启动时固定，目录选择器只能选用已配置的工作区（浏览子目录用于查看，不新增工作区）；前端插件在装配期加载，扩展页只提供 agent 插件的启用/停用与重载，前端插件变更需重启 Gateway。

#### 7.7.7 验收

- 宽度 1600 / 1280 / 1024 / 860 / 760 / 500 下无横向溢出；≤760px 侧栏为抽屉，汉堡唤出、遮罩/Esc 关闭。
- Light/Dark 两套主题逐组件验收；正文 ≥4.5:1、大字/图标 ≥3:1；token 名与值与 a_da 基线逐项一致。
- CSS 中不出现硬编码十六进制色、奇数尺寸、`dark:` 变体；模板不写内联 `style` 布局。
- golden HTML 覆盖：对话流、工具卡、审批/提问/排队托盘、任务卡、空状态、错误页；`<details>`、真实链接与表单在禁用 JS 时仍可读可操作。
- `prefers-reduced-motion`、键盘 Tab 顺序、焦点回归、`aria-live` 完成播报各有自动化或人工验收项。
- 视觉回归以 a_da 的 `preview-p0.png` 等截图为对照基线；ada-go 不维护第二套截图口径。

### 7.8 渐进增强与可访问性

- 没有 JS：发消息、响应交互、重载插件、切换线程全部是原生表单 + 303；运行中页面用 `<meta http-equiv="refresh" content="2">` 轮询，完成后停止。
- 有 JS：表单拦截为 `fetch`，SSE 增量更新；提交后不整页刷新。
- 语义结构：`<main>`、`<nav>`、`<form>`、`<button>`；消息列表 `aria-live="polite"`，流式期间不逐 token 播报，只在完成时播报。
- 键盘：Tab 顺序自然；交互托盘打开时聚焦安全选项（确认框默认“拒绝”），Esc 关闭；发送快捷键可配置。
- 错误：页面级错误显示原因与下一步（重试/打开日志/重启 Core），不显示堆栈；开发模式可展开。

---

**相邻文档**：[← 插件运行时与治理](06-plugin-runtime.md) · [索引](design.md) · [协议契约 →](08-protocol.md)
