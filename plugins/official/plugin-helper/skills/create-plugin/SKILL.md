---
name: create-plugin
description: 用插件助手（plugin-helper）创建与检查插件的完整流程：对话里用 create_plugin 工具由 AI 生成代码，或指引向导页；含安装路径（全局 ~/.adago/plugins）、入口源码约束、能力清单白名单与前端（页面/插槽）的当前边界。用户想创建/新写一个插件，或问插件怎么创建、怎么给插件加界面时读取。
---

# 创建插件

> 平台 API 的精确契约（清单字段、agent.* 注册面、ctx 能力、上限矩阵、生命周期）
> 见本插件目录的 `SPEC.md`；工作区即仓库时可用 fs 工具直接读取，或在插件详情页
> 的文件列表里查看。本技能只讲操作流程。

用户说「创建一个 XXX 插件」时按这个顺序做：

## 1. 对话创建（优先）

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

## 2. 向导页面

用户想自己填表时，指到侧栏「插件 → 插件助手」（`/ext/plugin-helper/helper`）：
表单生成回显示例插件；AI 生成的入口会原样写入 `index.ts`。

## 3. 启用 / 停用状态

- 默认写到全局 `~/.adago/plugins.json`：一次启停，所有工作区生效。
- 某个工作区如果想单独启停（覆盖全局），该工作区会有
  `<workspace>/.adag/plugins.json`；存在这个文件时，本工作区的启停只写它。
- 用户问「为什么这个工作区不一样」时，检查工作区下有没有 `.adag/plugins.json`。

## 4. 检查插件（check_plugin）

用户说「插件不工作 / 加载失败 / 检查一下插件」时调用：

1. `id` 留空：返回全部插件摘要（启停/状态/错误），先定位是哪个插件的问题；
2. 带 `id`：返回单个插件的诊断报告——发现位置（随包/全局/工作区）、清单字段、
   启停决策来源（全局 plugins.json / 工作区 .adag/plugins.json / 产品声明）、
   装配状态与 last_error，并对未停用的落盘插件做入口预加载校验；
3. 把报告里的失败项原样转告用户；`入口加载` 失败通常是入口源码形状或
   能力声明问题，按反模式清单逐条核对后修复重装。

## 5. 移除插件（remove_plugin）

用户说「移除/卸载 XXX 插件」时调用 `remove_plugin`（access=write，过审批）：
删除发现优先级最高的插件目录并清理启停条目；随包插件拒绝移除。
移除后转告用户在插件页点「重载」生效。已加载的代服务到重载为止，不影响在途轮次。

## 5. 修改已有插件

全局插件在 `~/.adago/plugins/<id>/`（在工作区 fs 工具的边界之外）：直接在文件
系统/IDE 里改 `index.ts` / `plugin.json`，改完让用户在插件页点「重载」；改了
能力清单必须重载才生效。工作区即仓库时，也可以用 fs 工具改仓库内
`plugins/official/<id>/` 的随包插件源码（改完重启应用）。

## 6. 运行时环境（能用什么 / 没有什么）

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
  3. 出网**没有 host 黑白名单限制**（allowlist 已移除，黑白名单后续以
     插件形式实现）：任意 http(s) 域名都能访问，直接用即可；仍要遵守
     响应上限（16MB）与超时边界。
- 没有 `localStorage` / `indexedDB` / DOM（`document` / `window`）——
  持久化用 `state` 能力，UI 用前端段。
- 没有 `node:http` / `node:https` / `node:net` / `node:dns` /
  `node:worker_threads` / `node:vm`（宿主不安装这些模块）。
- 没有 npm 生态：不能 import 任何第三方包；只 import 宿主装了的模块。
- 没有 TypeScript 运行时：入口是纯 JavaScript（类型注解都不要写，
  交给 JSDoc 注释）。

## 7. 插件的前端部分（页面 / 插槽）

清单里的 `frontend` 段提供：`pages`（`/ext/{id}/{slug}` 独立页面）、`slots`
（会话区/输入框等插槽的 TSX 卡片）、`nav`（侧栏导航项）、`dashboard`（概览卡）、
`actions`（读/写后端动作绑定）、`styles`/`scripts`/`assets`（静态资源）。

渲染契约（参考 `plugins/official/provider-admin`、`token-usage`、`tools-plan`）：

- 页面/插槽是 TSX 模板（`page.tsx` / `slot.tsx`）：`export default function (props)`，
  Gateway 渲染时编译成 VNode→HTML；TSX 只做纯计算，数据一律来自 `props`
  （只读动作预取 `props.data.backend[backendId][binding]`、`data.providers`、
  `data.usage`、`data.interactions`），副作用走动作桥或 `agent.route`。
- 浏览器脚本统一用 `window.adaAPI(pluginId, path, { method, body })` 调插件 route
  （自动带 CSRF 与线程上下文）；不要手写 `/ext/...` URL。
- 样式放 `static/*.css` 并在清单 `styles` 里声明；CSP 禁止内联 style/script，
  颜色只用 `--ph-*` 设计令牌。
- 插件自带 JS（清单 `scripts`）默认不加载：需要 `frontend.script` 能力且仅
  builtin/verified 可用；第三方优先用表单/HTMX。

**当前边界（必须如实告诉用户）**：Gateway 渲染桥目前只装配**随包官方插件**
（`plugins/official/`，桌面打包进 resources）的前端段。全局安装的插件只加载
后端段——给它们写 `frontend` 段不会报错，但页面/插槽不会出现。因此：

- 用户要的插件只需要工具/自动化 → 用 `create_plugin`，不要加 frontend 段；
- 用户明确要独立页面或会话卡片 → 说明当前版本安装型插件暂不支持前端段，
  可以先创建后端与工具；带 UI 的部分作为官方插件在 `plugins/official/<id>/`
  开发（含 `plugin.json` frontend 段 + TSX + `static/`，改完重启应用），
  或等后续版本支持安装型插件的前端段。

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
- 给安装型插件写 frontend 段并声称页面/插槽会生效——当前渲染桥不装配它们。
