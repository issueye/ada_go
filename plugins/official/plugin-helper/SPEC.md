# 插件 API SPEC（ada-go 插件平台 v1）

> 本文件是随包在 `plugin-helper` 里的平台 API 规范：写一个插件能用什么、
> 每个入口的确切形状、边界与生命周期。AI 生成插件代码前应按本规范自检；
> 与代码不一致时以宿主实现为准（`internal/plugins/host`）。

## 1. 插件包结构

```
<id>/
  plugin.json          # 清单（必填）
  index.ts             # 后端入口：内容是宿主可直接执行的 ESM JavaScript
  README.md            # 建议
  static/              # 前端静态资源（styles/scripts/assets 引用）
  skills/<name>/SKILL.md  # 随包技能（frontmatter: name=目录名 + description）
  dist/index.js        # 仅官方插件：构建期预编译产物（node plugins/official/build.mjs）
```

- 入口不做运行时 TypeScript/npm 编译：禁止运行时 TS 特性（enum/namespace/import=）
  与 npm import；依赖在构建期内联，或使用宿主提供的受限模块。
- 安装路径：生产统一到全局 `~/.adago/plugins/<id>/`（`plugin.admin` 安装桥的
  唯一落点，跨工作区共享）；工作区 `.adago/plugins/<id>/` 仅作为旧目录继续被发现，
  发现顺序为工作区优先、全局次之。目录名必须等于清单 `id`。
  纯前端插件（无 backend 入口）不会被 Core 加载。

## 2. plugin.json 清单

```json
{
  "id": "my-plugin",            // 目录名一致；3-64 位小写字母/数字/连字符
  "name": "我的插件",            // 显示名
  "version": "0.1.0",
  "apiVersion": 1,
  "description": "一句话用途",
  "trust": "third-party",       // builtin | verified | third-party
  "backend": {
    "entry": "index.ts",
    "capabilities": [],          // 见 §5 能力矩阵
    "secrets": ["some_key"]      // 声明后 ctx.secret(name) 才返回值
  },
  "frontend": {                  // 可选；当前仅随包官方插件被渲染桥装配（§8）
    "capabilities": ["backend.read"],
    "nav": [{ "label": "入口", "slug": "main" }],
    "pages": [{ "slug": "main", "title": "标题", "template": "page.tsx" }],
    "slots": [{ "slot": "chat.corner", "template": "slot.tsx", "order": 10 }],
    "actions": { "binding": { "action": "plan", "read": true, "backend": "tools-plan" } },
    "styles": ["main.css"],
    "scripts": ["main.js"],      // 默认不加载；需 frontend.script 且 builtin/verified
    "assets": "static"
  }
}
```

信任级别：`builtin`（随包发布，只能启用/禁用）、`verified`（签名有效）、
`third-party`（默认，受限）。

## 3. 注册面（入口默认导出 `register(agent)`）

### agent.registerTool(def) — 模型可调用的工具

```ts
{
  name: "my_tool",               // 全局唯一；进模型工具目录
  description: "做什么、什么时候用",
  parameters: { /* JSON Schema；additionalProperties: false */ },
  access: "read",                // read | write | exec | network
  timeoutMs?: number, parallelSafe?: true, repeatable?: true,
  rollback?: true, terminatesTurn?: false,
  async execute(args, ctx) {
    return { content: [{ type: "text", text: "给模型的文本" }], details: { ... } };
  },
}
```

- `access: write/exec/network` 的工具调用要过审批策略；产品没装配
  `role=tool-policy` 插件时这类工具一律 denied。
- 返回给模型的是 `content` 文本；结构化数据放 `details`（只给界面，INV-10）。
- **常见错误**：`inputSchema` / `run` 是 MCP 风格字段，宿主不认——字段名必须是
  `parameters` + `execute`（可调用成员），否则入口工厂阶段抛 TypeError、
  插件安装预检失败。
- `ctx.state.get/set/delete` 返回 Promise（异步 KV），要在 async execute 里 await。

### agent.route(method, path, handler) — 插件自己的 HTTP API

```ts
agent.route("GET", "/state", (req, ctx) => ({ ... }));       // req: {method,path,query,body?}
agent.route({ method: "POST", path: "/items", sse: false, handler(req, ctx) { ... } });
```

- 只允许 GET/POST；`path` 以 `/` 开头，不含 `..` 与查询串；同 method+path
  重复登记在加载期报错；`/*` 前缀匹配。
- 浏览器经 `/ext/{id}/api/*` 访问，统一用 `window.adaAPI(id, path, {method, body})`
  （自动带 CSRF 与线程上下文）；请求体 ≤1MB，响应 ≤64KB，错误统一
  `{"error":{"message":"…"}}`。
- `sse: true` 的路由只允许 GET，处理器用 `ctx.sse.send(data, event?)` 推帧。

### agent.registerAction(def) — 只给界面调用的动作

```ts
{ name: "plan", label: "读取计划", mode: "read", confirm?: "文案",
  timeoutMs?: 2000, execute(input, ctx) { return {...}; } }
```

- 不进模型上下文、不受工具审批约束，但必须由用户点击；`mode: "write"` 走 CSRF+确认。
- `read: true` 的动作可在前端 SSR 预取（`props.data.backend[backendId][binding]`）。

### agent.registerPrompt(def) / agent.registerProvider(def) / agent.on(point, fn)

```ts
agent.registerPrompt({ id: "persona", content: "...", priority?: 10 });   // 系统提示片段
agent.registerProvider({ id: "openai", models: [ModelInfo], stream(req, ctx) {} }); // 仅 builtin/verified
agent.on("tool.beforeCall", fn, { priority?: 0, action?: "deny" });       // 策略钩子，见 docs 06 §6.6
```

## 4. 执行上下文 ctx

| 成员 | 能力 | 形状 |
| --- | --- | --- |
| `workspace` / `threadId` / `runId` / `callId` / `pluginId` / `now` | — | 身份；`workspace` 是挂载点 `/workspace`，不是本机路径 |
| `log(level, msg, fields?)` | — | `level: debug/info/warn/error`；密钥由宿主脱敏 |
| `settings` | — | 产品/用户配置透传的对象 |
| `state.get/set/delete(key[, value])` | `state` | 线程级命名空间 KV；值为可 JSON 序列化数据 |
| `secret(name)` | 清单 `secrets` | 未声明的名字返回 undefined |
| `fs.readText/writeText/list/stat/exists` | `fs.read`（写需 `fs.write`） | 以工作区为根；见 §6 上限；覆盖写入进检查点 |
| `exec.execFile/execFileSync` | `exec`（builtin/verified） | 无 shell；输出 4MB / 超时 120s |
| `ui.available / confirm / select / input` | `ui.ask`（builtin/verified） | 通用用户交互 |
| `skills()` | 产品 `skills: true` | 只读技能元数据；正文走基座 `read_skill` |
| `pluginAdmin.install({ id, files })` | `plugin.admin`（builtin） | 把骨架写入全局 `~/.adago/plugins/<id>/`；宿主校验 id/相对路径/大小并拒绝覆盖 |
| `http` / `emit` | provider 专用 | 出网桥与归一化增量（03 §5.5） |

## 5. 能力位 × 信任矩阵

| 能力 | third-party | verified | builtin |
| --- | --- | --- | --- |
| `fs.read` / `fs.write` / `state` / `net.fetch` | ✅ | ✅ | ✅ |
| `net.sse` / `exec` / `ui.ask` | ❌ | ✅ | ✅ |
| `model.admin` / `plugin.admin` | ❌ | ❌ | ✅ |
| 前端 `backend.read`（数据投影） | ✅ | ✅ | ✅ |
| 前端 `frontend.script`（自带 JS） | ❌ | ✅ | ✅ |

### 运行时环境（全局对象与出网）

**有**：`ctx.*`（能力入口，见 §4）、全局 `console`、受限 `process`
（`platform/arch`；`env` 仅含清单 `backend.env` 声明的名字；`exit` 不开放）、
`setTimeout`/`setInterval`（未完成上限 256）、按能力 import 的 Node 内置模块
（`node:fs` 需 fs 能力、`node:child_process` 需 exec、`node:path`/`crypto`/
`buffer` 纯计算、`node:os` 受限；完整矩阵见 docs 12）。

**没有**：全局 `fetch`/`XMLHttpRequest`/`WebSocket`、`localStorage`/DOM、
`node:http(s)`/`node:net`/`node:dns`/`node:worker_threads`/`node:vm`、
npm 生态（不能 import 第三方包）。

**出网**：唯一通道是声明 `net.fetch` 后的 `await ctx.fetch(url, init)`；
返回 `{ status, ok, headers, text }`——`text` 是响应体**字符串**（FromGo 桥不
支持 Go 函数值，历史上的 `text()` 函数形状会让整个 Promise 结算失败），
JSON 用 `JSON.parse(res.text)`；响应 ≤16MB。
**host 黑白名单暂未启用**（allowlist 已移除，后续以插件形式实现）：
任意 http(s) 域名都可出网；边界只剩协议、响应上限（16MB）与超时。

## 6. 资源上限（宿主强制）

| 项 | 值 |
| --- | --- |
| fs 单次读写 | 16MB（`readText` 可用 `maxBytes` 收窄） |
| fs 目录列举 | 10k 条目；路径 ≤4096 字节 |
| `net.fetch` 响应 | 16MB |
| exec 输出 / 超时 | 4MB / 120s |
| route 请求体 / 响应 | 1MB / 64KB |
| 技能正文（read_skill） | 16KB 截断 |
| Runtime 内存 / 调用深度 / 定时器 | 64MB / 2000 / 256 |

## 7. 生命周期

1. **发现**：扫描两个插件根（工作区优先、全局次之）；单插件非法记 warning 跳过，
   不拖垮其它插件。
2. **装配**：产品声明选择默认启用集合；未声明的随包插件呈 `unused`，可手动启用。
3. **启停状态**：真源是 JSON——全局 `~/.adago/plugins.json`（跨工作区共享）+
   工作区 `<workspace>/.adag/plugins.json`（仅该工作区有单独启停时存在，条目覆盖
   全局）；两者都没有条目时回落产品声明。旧版按工作区写在 SQLite `plugin_meta`
   的用户选择会在打开会话时一次性迁移成工作区覆盖文件。
4. **工具目录**：启用插件的工具进入同一目录，`access=write/exec/network` 受审批策略约束。
5. **热重载**：插件页「重载」（`plugin.reload`）或修改后重启；双代切换，在途轮次不中断。
6. **失败保护**：同一插件连续 3 次调用 reject 会被自动禁用，需手动重载恢复。

## 8. 前端段：契约与当前边界

- 页面/插槽是 TSX 模板（`export default function (props)`），Gateway 渲染时编译成
  VNode→HTML；TSX 只做纯计算，数据来自 `props`（只读动作预取 / `data.providers` /
  `data.usage` / `data.interactions`），副作用走动作桥或 route。
- 页面地址 `/ext/{id}/{slug}`；`nav` 进侧栏；`dashboard` 进概览页。
- 浏览器脚本统一 `window.adaAPI(id, path, {method, body})`；样式放 `static/*.css`
  并在清单 `styles` 声明；CSP 禁止内联 style/script，颜色只用 `--ph-*` 设计令牌。
- **当前边界**：渲染桥只装配随包官方插件（`plugins/official/`）的前端段；
  全局安装的插件（`~/.adago/plugins/`）只加载后端段——`frontend` 段不会报错，
  但页面/插槽不会出现。

## 9. 安全边界摘要

- 密钥只存在于 Core 内存与配置：不进提示词、工具结果、日志、前端 props，
  也不进插件子进程环境；`ctx.secret` 只对清单声明过的名字开放。
- 插件出网只经宿主 HTTP 桥（host 黑白名单暂未启用）；fs 只在workspace 根内；无 shell 的
  exec 仅 builtin/verified。
- 工具与动作的写/执行都要用户确认；动作不进模型上下文。
- 所有插件 HTTP 响应 `Cache-Control: no-store`（静态资源除外）。

## 10. 本插件（plugin-helper）自身 API — 完整示例

Route（向导页面用）：

- `GET /info` → `{ workspace, pluginRoot, files }`
- `POST /create`，body `{ id, name?, description?, toolName? }` →
  `{ id, name, toolName, path, files, reloadRequired: true }`；
  校验失败返回 `{"error":{"message":"…"}}`
- `POST /remove-plugin`，body `{ id }` → `{ id, dir, source }`；随包插件拒绝
- `POST /check-plugin`，body `{ id? }` → id 非空返回单插件诊断报告 `report`
  （found/name/source/dir/entry/capabilities/enabled/status/lastError/tools/
  files/checks），id 省略返回全部插件摘要 `plugins`

工具（AI 用）：

- `create_plugin`（access=write，过审批）
- 参数：`id`（必填）、`name?`、`description?`、`entry?`（AI 生成的入口源码，
  必须含 `export default function register(agent)` 并注册 `tool_name` 工具，
  ≤256KB）、`tool_name?`（默认 `<id>_hello`）、`capabilities?`（白名单见 §5）
- 行为：校验 → 经 `ctx.pluginAdmin.install` 把骨架写入全局
  `~/.adago/plugins/<id>/`（不覆盖已有目录，拒绝与随包插件同 id）→
  返回文件清单与「重载 → 启用」指引；任何校验失败不落盘
- `check_plugin`（access=read）：等价于 `POST /check-plugin`，诊断报告文本化；
  入口预加载失败的插件会带出宿主精确错误（形状/能力/语法），便于 AI 自修
- `remove_plugin`（access=write，过审批）：删除发现优先级最高的插件目录并
  清理启停条目；随包插件拒绝；移除后需重载生效

启停状态：全局 `~/.adago/plugins.json`（跨工作区共享）+ 工作区
`<workspace>/.adag/plugins.json`（单独启停的覆盖，见 §7.3）。

随包技能：`skills/create-plugin/SKILL.md` — 创建流程的操作指引（工作流层），
本文件是契约层；两者配合使用。
