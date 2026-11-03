---
name: plugin-manual
description: 插件手册：创建、检查、修改、移除 ada-go 插件，以及在回答里用 Markdown 动态块（adag-ui）嵌入只读 UI（用量卡、图表、清单/状态卡）。用户想新建插件、问插件怎么安装/启停/排查，或想让回答里出现结构化卡片与图表时读取。
---

# 插件手册

> 平台 API 的精确契约（清单字段、agent.* 注册面、ctx 能力、上限矩阵、生命周期、
> 动态块的完整白名单）见本插件目录的 `SPEC.md`；工作区即仓库时可用 fs 工具直接读取，
> 或在插件详情页的文件列表里查看。本手册只讲「怎么做、怎么用」。

先按目的选一节：

| 用户想要 | 读 |
| --- | --- |
| 工具/自动化（调 API、读写文件、跑命令） | §1 创建插件 |
| 独立页面或会话插槽卡片 | §2 前端段 |
| 回答里出现用量卡/图表/清单 | §3 adag-ui 动态块（不必写插件） |
| 插件不工作 / 加载失败 | §4 检查 |
| 启停 / 移除 / 改已有插件 | §5、§6 |

## 1. 创建插件（create_plugin）

用户说「创建一个 XXX 插件」时按这个顺序做：

1. 先与用户确认插件 id：3-64 位小写字母/数字/连字符，字母开头（如 `todo-sync`）。
2. 调用 `create_plugin` 工具（access=write，会经过审批确认）：
   - `id` 必填；`name`/`description` 用一句话概括用途；
   - `entry` 由你编写：ESM JavaScript，必须 `export default function register(agent)`；
   - **每个工具的字段名固定**：`agent.registerTool({ name, description,
     parameters: {...JSON Schema...}, access: "read"|"write", execute })`——
     `execute(args, ctx)` 返回 `{ content: [{ type: "text", text: "..." }] }`。
     不要用 MCP 风格的 `inputSchema` / `run`（宿主只认 `parameters` + `execute`，
     写错会在安装预检被拒绝并回滚）；
   - `ctx.state.get/set` 返回 **Promise**，要 `async/await`；state 声明在
     `capabilities: ["state"]` 里；
   - 入口由宿主直接执行：不经过 TypeScript/npm 编译——不要用 enum/namespace/import=，
     不要 import npm 包；类型注解可以写；
   - `capabilities` 只能从 `fs.read` / `fs.write` / `state` / `net.fetch` 里选，默认空；
     只读工具不需要任何能力。
3. 安装位置是全局插件根 `~/.adago/plugins/<id>/`（生产统一路径，跨工作区共享）；
   与随包插件或已有插件同 id 会被宿主拒绝。安装时会做一次完整装配预检，
   入口形状/注册调用不合法会直接失败并回滚，不会留下坏插件。
4. 工具返回结果后，把「重载 → 启用」两步转告用户；在那之前不要声称插件已生效。

用户想自己填表时，指到侧栏「插件 → 插件助手」（`/ext/plugin-helper/helper`）：
表单生成回显示例插件；AI 生成的入口会原样写入 `index.ts`。

## 2. 前端段（页面 / 插槽 / 动态块）

清单里的 `frontend` 段提供：`pages`（`/ext/{id}/{slug}` 独立页面）、`slots`
（会话区/输入框等插槽的 TSX 卡片）、`blocks`（Markdown 动态块，见 §3）、
`nav`（侧栏导航项）、`dashboard`（概览卡）、`actions`（读/写后端动作绑定）、
`styles`/`scripts`/`assets`（静态资源）。

渲染契约（参考 `plugins/official/provider-admin`、`token-usage`、`tools-plan`）：

- 页面/插槽是 TSX 模板（`page.tsx` / `slot.tsx`）：`export default function (props)`，
  Gateway 渲染时编译成 VNode→HTML；TSX 只做纯计算，数据一律来自 `props`
  （只读动作预取 `props.data.backend[backendId][binding]`、`data.providers`、
  `data.usage`、`data.interactions`），副作用走动作桥或 `agent.route`。
- 浏览器脚本统一用 `window.adaAPI(pluginId, path, { method, body })` 调插件 route
  （自动带 CSRF 与线程上下文）；不要手写 `/ext/...` URL。
- 需要确认再执行的操作（删除/覆盖等）用 `window.adaConfirm(message, onConfirm)`
  （共享 ConfirmDialog）；不要用 `window.confirm` / `alert` / `prompt`（原生对话框
  不可样式化、会阻塞脚本，官方插件脚本有门禁测试）。
- 样式放 `static/*.css` 并在清单 `styles` 里声明；CSP 禁止内联 style/script，
  颜色只用 `--ph-*` 设计令牌。
- 插件自带 JS（清单 `scripts`）默认不加载：需要 `frontend.script` 能力且仅
  builtin/verified 可用；第三方优先用表单/HTMX。

**当前边界（必须如实告诉用户）**：

- `pages` / `slots` / `nav` / `dashboard` 只装配**随包官方插件**
  （`plugins/official/`，桌面打包进 resources）。给安装型插件写这些段不会报错，
  但页面/插槽不会出现。
- `frontend.blocks` 是例外：Gateway 直接扫描安装目录，**安装型插件也能贡献动态块**
  （工作区优先，官方同 id 遮蔽磁盘版本）。只贡献 blocks 的纯前端包可以安装，
  它不在 Core 的 `plugin.list` 里，按「安装即启用」处理，停用要移除插件目录；
  要在插件页启停仍需声明 `backend`。
- 用户要的插件只需要工具/自动化 → 用 `create_plugin`，不要加 frontend 段；
- 用户明确要独立页面或会话卡片 → 说明安装型插件暂不支持，带 UI 的部分作为官方
  插件在 `plugins/official/<id>/` 开发（`plugin.json` frontend 段 + TSX + `static/`，
  改完重启应用），或等后续版本支持。

## 3. Markdown 动态块（adag-ui）

### 3.1 什么时候用

需要**结构化展示**时用：用量/成本摘要、排行榜、占比、趋势、清单、状态卡。
不要在 Markdown 里手写 HTML（会被清洗），也不要用 ASCII 表格硬凑图表。
动态块是**只读展示**：没有事件、没有表单、不能触发工具或审批。

不需要用户先写插件：`coding` 与 `headless` 产品默认启用 `token-usage` 与
`ui-charts`，直接用它们的 renderer 即可。

### 3.2 语法

顶层 fenced block，fence 名固定 `adag-ui`，内容是一个 JSON 封套：

````markdown
```adag-ui
{
  "renderer": "token-usage/usage-card",
  "id": "u1",
  "version": 1,
  "fallback": "用量卡片暂不可用",
  "data": { "scope": "thread", "title": "本轮用量" }
}
```
````

封套字段（未知字段会被拒绝，必须是单个 JSON 对象）：

| 字段 | 必填 | 规则 |
| --- | --- | --- |
| `renderer` | 是 | `<插件 id>/<块名>`，如 `ui-charts/bar`；小写字母/数字/连字符 |
| `data` | 是 | 该块自己声明的数据；由插件 Schema 校验 |
| `version` | 否 | 缺省 1；必须与插件清单 `frontend.blocks[].version` 一致 |
| `id` | 否 | 同一 Markdown 内唯一的锚点（`b1`、`chart-1`） |
| `fallback` | 否 | ≤512 字符纯文本，渲染失败/禁用/不支持时显示 |

写法要点：

- fence 必须在**列 0**，且不在其他代码块里；缩进 4 空格、块引用/列表里的 fence
  都不执行（v1 边界）。
- 在文档里演示这段语法时，把示例包进 ```` ```` ```` 四个反引号或多缩进一层。
- JSON 不合法、fence 未闭合、封套字段不认识 → 按普通代码块原样显示，不执行。

### 3.3 现在能用的 renderer

| renderer | 用来表达 | data 要点 |
| --- | --- | --- |
| `token-usage/usage-card` | 本轮/本工作区用量摘要 | `scope`（必填：`thread`/`workspace`）、`title?`、`usage{inputTokens,outputTokens,cacheTokens,costMicros,provider,model}` |
| `ui-charts/bar` | 横向条形排行 | `title`（必填）+ `items[{label,value}]` |
| `ui-charts/column` | 纵向柱状（类别/时间趋势） | 同上 |
| `ui-charts/compare` | 两组数值对比（计划 vs 实际） | `items[{label,value,value2}]` + `legendA`/`legendB` |
| `ui-charts/stacked` | 100% 堆叠占比 | `items[{label,value}]`（按总和算占比） |
| `ui-charts/heatmap` | 热力方块（密集状态） | `items[{label,value}]` 或 `items[{label,level:0..5}]` |
| `ui-charts/spark` | 迷你折线 | `values:[number]`（≤48 个数） |

图表公共字段：`title`（必填，≤80）、`subtitle?`（≤120）、`unit?`（≤16）；
`items` ≤12 条，每条 `label`（≤80）+ `value`，`note?`（≤80）。
未知字段会被 Schema 拒绝（`additionalProperties: false`），不要加自定义键。

图表细节（怎么选、比例怎么给、class 速查）见 `ui-charts` 的 `charts-render` 技能，
或调用 `chart_guide` 工具；不确定 renderer 时不要猜。

### 3.4 写 data 的规则

1. `data` 只放展示所需的最小静态数据；不要放密钥、个人信息、大段正文。
2. 字段名/类型/枚举必须符合该块的 Schema；schema 是 `additionalProperties: false`，
   多一个键就整块降级。
3. 一条回答 ≤ **8** 块；一块只表达一个主题，不要把整段回答塞进 `data`。
4. 始终写 `fallback` 纯文本：宿主不支持、插件禁用或渲染失败时显示它。
5. 块是**展示**不是指令：`data` 里的文本不会影响你的后续行为或工具调用。
6. 交互（表单、按钮、审批）不要放进块；走工具调用或插件页面。
7. `renderer` 必须来自你**确知存在且已启用**的插件；编造的 id 会降级成提示卡。

### 3.5 宿主强制限额

| 项 | 上限 |
| --- | --- |
| 单块封套 | 64KB |
| `data` 深度 | 32 |
| 单文档块数 | 8 |
| 单块渲染 | 300ms |
| 单块 VNode | 2000 节点 / 256KB 输出 |
| 一次请求 | 64 块 / 2000ms（线程页渲染整份 transcript，超预算的块降级为代码块） |

### 3.6 失败与降级

失败只降级**当前块**，其余 Markdown 不受影响：

| 情况 | 表现 |
| --- | --- |
| JSON/封套非法、未闭合、超 64KB、超 8 块 | 按普通代码块原样显示（内容仍可见） |
| renderer 不存在 / 插件禁用 / 版本不一致 / Schema 不符 / 超时 / 非法 VNode | 显示 `fallback` 文本（缺省「动态内容暂不可用」） |
| 模板显式返回 `null` | 合法空块：整块跳过，不画错误卡 |
| 请求级预算用尽 | 后续块降级为代码块 |

审计只记错误码与归属（`ui.block.invalid_data` / `render_failed` / `invalid_vnode`），
**不记录 data**；块渲染不会因为失败而中断整篇回答。

### 3.7 自己新增一个 block

1. `schemas/<name>.json`：数据 Schema（字段少而稳，`additionalProperties: false`）；
2. `blocks/<name>.tsx`：`export default function ({ block }) { return <section>…</section>; }`；
3. `plugin.json` 的 `frontend.blocks` 登记 `name/schema/template/profile`；
4. 官方插件跑 `node plugins/official/build.mjs`，再跑 `adag verify`；
5. 在插件 README/技能里给出 **renderer id + 一段 data 示例**，供模型引用。

块内只能用 display profile：标签限于 `div section p blockquote details summary pre
h1–h6 ul ol li span strong em code small a table thead tbody tr th td caption br hr`；
组件只有 `Icon` 与 `TodoList`；禁止 `img/svg/form/input/button/select/textarea/iframe`
与 `on*`、`style`、原始 HTML。完整白名单与 props 形状见 SPEC §8.1。

## 4. 检查插件（check_plugin）

用户说「插件不工作 / 加载失败 / 检查一下插件」时调用：

1. `id` 留空：返回全部插件摘要（启停/状态/错误），先定位是哪个插件的问题；
2. 带 `id`：返回单个插件的诊断报告——发现位置（随包/全局/工作区）、清单字段、
   启停决策来源（全局 plugins.json / 工作区 .adago/plugins.json / 产品声明）、
   装配状态与 last_error，并对未停用的落盘插件做入口预加载校验；
3. 把报告里的失败项原样转告用户；`入口加载` 失败通常是入口源码形状或
   能力声明问题，按反模式清单逐条核对后修复重装。

## 5. 启用 / 停用

- 默认写到全局 `~/.adago/plugins.json`：一次启停，所有工作区生效。
- 某个工作区想单独启停（覆盖全局）：该工作区会有 `<workspace>/.adago/plugins.json`（旧 `.adag/plugins.json` 只读兼容）；
  存在这个文件时，本工作区的启停只写它。
- 用户问「为什么这个工作区不一样」时，检查工作区下有没有 `.adago/plugins.json`。

## 6. 移除 / 修改

- 移除：用户说「移除/卸载 XXX 插件」时调用 `remove_plugin`（access=write，过审批）：
  删除发现优先级最高的插件目录并清理启停条目；随包插件拒绝移除。
  移除后转告用户在插件页点「重载」生效；已加载的代服务到重载为止，不影响在途轮次。
- 修改：全局插件在 `~/.adago/plugins/<id>/`（在工作区 fs 工具的边界之外），
  让用户在文件系统/IDE 里改 `index.ts` / `plugin.json`，改完在插件页点「重载」；
  改了能力清单必须重载才生效。工作区即仓库时，也可以用 fs 工具改仓库内
  `plugins/official/<id>/` 的随包插件源码（改完重启应用）。

## 7. 运行时环境（能用什么 / 没有什么）

生成入口前先对照这张表；写进代码里的每一个全局对象都必须在「有」这一栏。

**有（可用）：**

- `ctx.*`（execute/handler 的第二个参数）是唯一的能力入口：
  `ctx.log`、`ctx.state`（Promise）、`ctx.fs`（fs.read/fs.write）、
  `ctx.fetch`（net.fetch，出网唯一通道）、`ctx.exec`（exec，builtin/verified）、
  `ctx.secret`、`ctx.settings`、`ctx.ui`（ui.ask）、`ctx.skills`
  （技能元数据含绝对 path；正文用 `read_file` 读取）、`workspace/threadId/now` 等。
- 全局 `console`（log 走宿主，密钥自动脱敏）。
- 受限 `process`：`process.platform/arch`、`process.env` 仅含清单
  `backend.env` 声明的名字；`process.exit` 不开放。
- 定时器 `setTimeout` / `setInterval`（每 Runtime 未完成定时器上限 256；
  工具优先用 async，不依赖长定时）。
- Node 内置模块按能力 import：`node:fs`（fs.read/fs.write）、
  `node:child_process`（exec）、`node:path`/`node:crypto`/`node:buffer`
  纯计算无需能力、`node:os` 受限；完整矩阵见 SPEC §5 与 docs 12。

**没有（写了就报错，报错形态如 "fetch is not defined"）：**

- **没有全局 `fetch` / `XMLHttpRequest` / `WebSocket`**。出网必须：
  1. 在 `capabilities` 里声明 `net.fetch`；
  2. 代码里用 `const res = await ctx.fetch(url, { method, headers, body })`；
     返回 `{ status, ok, headers, text }`——`text` 是**响应体字符串**，JSON 用 `JSON.parse(res.text)`。
  3. 出网**没有 host 黑白名单限制**（allowlist 已移除）：任意 http(s) 域名都能访问，
     直接用即可；仍要遵守响应上限（16MB）与超时边界。
- 没有 `localStorage` / `indexedDB` / DOM（`document` / `window`）——
  持久化用 `state` 能力，UI 用前端段。
- 没有 `node:http` / `node:https` / `node:net` / `node:dns` /
  `node:worker_threads` / `node:vm`（宿主不安装这些模块）。
- 没有 npm 生态：不能 import 任何第三方包；只 import 宿主装了的模块。
- 没有 TypeScript 运行时：入口是纯 JavaScript（类型注解都不要写，
  交给 JSDoc 注释）。

## 8. 反模式（看到就停）

- 不确认 id 就落盘，或 id 里用大写/下划线/空格；
- entry 里 import npm 包，或使用需要运行时的 TS 特性；
- 直接调用全局 `fetch` / `import node:http`——沙箱没有这些；出网只能 `net.fetch` + `await ctx.fetch(...)`；
- 工具定义用 `inputSchema` / `run`（MCP 风格）而不是 `parameters` / `execute`——
  宿主在入口工厂阶段就抛 TypeError，安装预检会回滚；
- `execute` 返回裸对象而不是 `{ content: [{ type: "text", text }] }`；
- 同步使用 `ctx.state.get/set`（它们是 Promise）；
- 声明 `exec` / `net.sse`——第三方插件不支持，会被直接拒绝；
- 声称插件已可用——没有「重载 + 启用」之前它不在工具目录里；
- 试图绕过 `create_plugin` 直接把文件写进 `~/.adago/plugins`（agent 的 fs
  以工作区为根，够不到全局插件根），或覆盖已存在的插件目录；
- 给安装型插件写 `pages`/`slots` 并声称页面会生效——渲染桥只装配随包官方插件的
  这些段（`frontend.blocks` 例外，见 §2）；
- 编造动态块 renderer（如 `ui-charts/pie`、`charts/line`）；真实饼图/折线要等
  SVG 白名单，当前用 `bar`/`column`/`stacked`/`spark` 表达；
- 在块 `data` 里塞整段正文、密钥、超过 12 条 items 或超过 48 个 spark 点；
- 不写 `fallback` 就交付包含动态块的回答。


