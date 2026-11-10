# 插件设计方法：三阶段 + 本插件的设计决策记录

> 契约层以 `plugins/official/plugin-helper/SPEC.md` 为准，流程层以
> `plugins/official/plugin-helper/skills/plugin-manual/SKILL.md` 为准；
> 本文只回答一件事：**怎么把一个需求推成符合 ada-go 规范的插件**。
>
> 全文以一个真实案例贯穿：`plugins/design-canvas`（配合 adago_design MCP 的画布台账插件）。

---

## 0. 一条主线

> **插件的价值 = 宿主缺口的补集。**

ada-go 的插件不是"什么都能做的脚本"，而是**在固定注册面和固定能力位里补一个缺口**。
所以设计的第一动作不是写代码，而是回答"这个缺口宿主已经有吗、该由谁补"。

本案例的缺口分析：

```
用户想「在对话里看到设计画布的成果」
  ├─ 宿主已有：工具调用（模型能操作）、Markdown、动态块、插件页
  ├─ 官方已有：mcp-client（把 MCP 工具注册成普通工具）
  ├─ MCP 已有：design_preview / canvas_list / canvas_screenshot
  └─ 缺口：把画布状态「沉淀成线程内可复用的数据」并「渲染进对话」
        → 这才是插件该做的事；再包一层 preview 工具是伪需求
```

---

## 1. 设计前（Discovery）：把需求压成一份契约

目标：**在写第一行代码前，确定它是什么形状、能碰什么、坏了怎么办。**

### 1.1 任务清单

| # | 任务 | 产出 | 不过关的表现 |
| --- | --- | --- | --- |
| 1 | 用一句话写清用途（≤40 字） | `plugin.json.description` 草稿 | 需要"并且/同时"两次以上 |
| 2 | 判定**触发面**：模型调用 / 用户点击 / 纯展示 | 决定用 `registerTool` / `registerAction` / `blocks` | 三者混在一起（一个工具既给模型又被页面点） |
| 3 | 判定**数据源**：宿主数据 / 外部 HTTP / 本地文件 / 子进程 | 决定能力位 | 说"要调某个 CLI"却只声明 `fs` |
| 4 | 判定**副作用**：只读 / 写用户数据 / 执行命令 | 决定 `access` | 会写盘却标 `read` |
| 5 | 判定**装配位置**：随包 official / 全局安装 / 工作区 | 决定能不能用 `pages`/`slots` | 给安装型插件写 `pages` 并承诺有页面 |
| 6 | 判定**UI 需求**：不需要 / 动态块 / 页面 | 决定 `frontend` 段 | 需要展示却只返回一大段文本 |
| 7 | 判定**失败面**：哪些依赖会挂、挂了降级成什么 | 一张降级表 | 只写 happy path |
| 8 | 查重：官方插件里是否已有 80% 重叠 | 复用结论 | 造第二个 `ui-charts` |

### 1.2 六个问题 → 清单字段

| 问题 | 答案 | 落到哪里 |
| --- | --- | --- |
| 触发面 | 模型调用 | `agent.registerTool` |
| | 用户点击（不进模型上下文） | `agent.registerAction` |
| | 纯展示 | `frontend.blocks` |
| 数据源 | 外部 HTTP | `capabilities: ["net.fetch"]` + `ctx.fetch` |
| | 本地文件 | `fs.read` / `fs.write` |
| | 线程内缓存 | `capabilities: ["state"]` + `ctx.state` |
| | 子进程 | `exec`（**仅 builtin/verified**）或 `process`（**仅 builtin/verified**） |
| 副作用 | 只读 | `access: "read"`（不弹审批） |
| | 写用户数据 / 改外部状态 | `access: "write"`（过 `policy-default` 询问） |
| | 出网访问非回环 | `access: "network"` |
| 装配位置 | `plugins/official/` | 可声明 `pages`/`slots`/`styles`/`scripts` |
| | `~/.adago/plugins/<id>/` | **只有 backend + `frontend.blocks` 会装配** |
| UI | 不需要 | 不写 `frontend` |
| | 展示 | `frontend.blocks` + `schemas/*.json` + `*.tsx` |
| 失败面 | 每个外部依赖 | 见 §3.2 的四层验证 |

### 1.3 设计前的三条硬闸门

1. **能力位先对表**。第三方拿不到 `exec` / `ui.ask` / `net.sse` / `plugin.admin`。
   如果方案的核心依赖这些，方案在第三步就死了——**换方案，不要绕**（例如
   "自己拉起 MCP" 这件事，能力位上就不成立，只能交给 `mcp-client`）。
2. **装配期结算**。工具目录只在装配期生成，运行期 `registerTool` 只记警告。
   需要"远程读目录再注册工具"时，只有 builtin/verified 能用
   `AssemblyContext` + `backend.initTimeoutMs`；第三方只能注册静态工具集。
3. **UI 先问装配范围**。安装型插件写 `pages`/`slots` 不会报错，但页面不会出现——
   这是最容易给用户错误承诺的地方。

### 1.4 本案例的判定结果

| 问题 | 判定 |
| --- | --- |
| 用途 | 读取设计画布现状，生成线程台账并渲染成卡片 |
| 触发面 | 模型调用（`design_canvas_sync` / `design_canvas_ledger`）+ 纯展示（`blocks`） |
| 数据源 | 回环 HTTP（画布 `GET /api/nodes`）+ 线程 state 缓存 |
| 副作用 | 只读（只发 GET，写的是插件自己的 state） |
| 装配位置 | 全局安装（`trust: third-party`）→ 放弃 `pages`，只做 `blocks` |
| UI | 动态块 `design-canvas/canvas-ledger` |
| 失败面 | 画布未启动 → 文本提示 + `details.error`，降级为"没有台账" |

---

## 2. 设计中（Build）：照着契约写，不即兴发挥

目标：**清单、入口、前端段三件套都能被宿主预检一次通过。**

### 2.1 任务清单

| # | 任务 | 要点 |
| --- | --- | --- |
| 1 | 定 `id` | `^[a-z][a-z0-9-]{2,63}$`，**等于目录名**；不要大写/下划线/空格 |
| 2 | 写 `plugin.json` | `apiVersion: 1`、`trust`、`backend.entry`、按需 `capabilities`/`secrets` |
| 3 | 写入口骨架 | `export default function register(agent)`；纯 ESM JS |
| 4 | 逐个工具：名称/描述/`parameters`/`access`/`execute` | 字段名是 `parameters` + `execute`，**不是** `inputSchema` + `run` |
| 5 | 统一返回形状 | `{ content: [{ type: "text", text }], details }` |
| 6 | 失败归一 | 外部失败 → 文本 + `details.error`，**不抛异常** |
| 7 | 需要 UI 时先建 schema | `additionalProperties: false`，字段少而稳 |
| 8 | 写块模板 | 只用 display profile 白名单标签；比例用档位 class |
| 9 | 写技能（可选） | `skills/<name>/SKILL.md`，frontmatter `name` == 目录名 |
| 10 | 写 README | 必须含 **renderer id + data 示例**，否则模型会编 |

### 2.2 入口骨架（可直接复制）

```js
// 纯 ESM JavaScript：没有 TS 运行时、没有 npm、没有全局 fetch
export default function register(agent) {
  agent.registerTool({
    name: "my_tool",                     // ^[a-z][a-z0-9_]{1,63}$，全局唯一
    description: "做什么 + 什么时候用",   // 模型靠这句选工具
    parameters: {                        // JSON Schema，additionalProperties: false
      type: "object",
      properties: { query: { type: "string", description: "…" } },
      additionalProperties: false,
    },
    access: "read",                      // read | write | exec | network
    timeoutMs: 8000,
    async execute(args, ctx) {
      // 出网唯一通道；返回 { status, ok, headers, text }
      // const res = await ctx.fetch(url, { method: "GET" });
      // await ctx.state.set("k", v);      // Promise，必须 await
      return { content: [{ type: "text", text: "给模型的文本" }], details: { ok: true } };
    },
  });

  agent.route("GET", "/status", async (req, ctx) => ({ ok: true }));   // 浏览器经 /ext/<id>/api/*
  agent.registerPrompt({ id: "guard", content: "硬规则…", priority: 20 });
}
```

**最容易踩的五个坑**（宿主会在预检或运行时直接拒绝）：

1. `inputSchema` / `run`（MCP 风格）→ 入口工厂阶段 TypeError，安装回滚；
2. 全局 `fetch` → `fetch is not defined`；必须 `ctx.fetch`；
3. `ctx.state.get()` 当同步用 → 拿到 Promise 当值；
4. `execute` 返回裸对象 → 模型看不到任何内容；
5. `import` 任何 npm 包 → 沙箱不安装。

### 2.3 前端段：先判断能不能装配

```
要页面/插槽？ → 只能放 plugins/official/（随包）
要对话里的卡片？ → frontend.blocks，安装型插件也可以
要自己的 CSS/JS？ → 只有官方插件会被装配；安装型插件的 blocks 必须复用宿主令牌类
```

### 2.4 动态块五步

1. `schemas/<name>.json`：数据契约，`additionalProperties: false`；
2. `<name>.tsx`：`export default function (props)`，只用白名单标签；
3. 清单 `frontend.blocks` 登记 `name / schema / template / profile`；
4. 本地校验：JSON 合法性 + `node --check`（模板可读性）；
5. README/技能里给 **renderer id + data 示例**。

块的硬约束（违反即降级，不会报错给你看）：

| 约束 | 说明 |
| --- | --- |
| 标签白名单 | `div section p ul ol li span strong em code small a table thead tbody tr th td details summary pre h1-h6` |
| 禁止 | `img svg form input button select textarea iframe style script`、`on*`、`style` |
| 组件 | 只有 `$C` 的 `Icon` / `TodoList` |
| 比例 | 换算成 `adag-chart__bar--l1..l10`；**没有内联 style** |
| 空态 | 返回 `null` 让宿主整块跳过，不画空卡 |
| 超时 | 单块 300ms、VNode 2000 节点 |

**最容易翻车的一条**：本地辅助函数必须**当函数调用**，不能写成 JSX 标签。

```tsx
function GroupRow(props) { return <li>…</li>; }

// ❌ <GroupRow row={row} />      → 「组件函数缺少 __ada_component 名」
// ✅ {GroupRow({ row: row })}    → 宿主编译器只认 import 自 ssr:components 的宿主组件
```

这条错误在运行期只会表现为"整块降级成 fallback"，用户看到的是
「动态内容暂不可用」，没有任何堆栈——所以必须靠一次性编译测试兜住（见 §3.2）。

### 2.5 能力最小化

只声明真正用到的能力，并在 README 里逐个解释用途与边界：

```json
"capabilities": ["net.fetch", "state"]
```

能力是**安装期授予**的：声明了才有 `ctx.fetch` / `ctx.state`；
没声明就是属性不存在（不是"调用时再报错"）。反过来，声明了不用的能力
会在评审时被判为权限过宽。

### 2.6 本案例的实现选择

| 决策 | 选择 | 理由 |
| --- | --- | --- |
| 入口语言 | 纯 JS + JSDoc | 宿主不编译 TS；`.ts` 只是仓库约定后缀 |
| 画布地址 | `ctx.settings.canvas_url`，缺省 `127.0.0.1:8788` | 非敏感默认值走 `[plugins.settings.<id>]`，改端口不用改代码 |
| 两个工具都 `read` | 只发 GET | 见 §4 的 D2 |
| 缓存键 | `canvas_ledger_v1` | 带版本号，形状变更时不与新代码打架 |
| 台账条数 | 体系 ≤12、节点 ≤12 | 与 `adag-ui` 的块预算（64KB / 300ms）对齐 |

---

## 3. 设计后（Verify）：四层验证，缺一层就不算完成

### 3.1 任务清单

| # | 任务 | 命令 / 方法 |
| --- | --- | --- |
| 1 | 静态：JSON + JS 语法 | `node --check`（入口）、`python -m json.tool`（清单/schema） |
| 2 | 装配：发现 + 清单校验 + 入口预加载 | `adag plugin list`、`adag plugin reload` |
| 3 | 运行：工具、route、块各跑一次 | `adag chat` / 插件页 / `curl /ext/<id>/api/...` |
| 4 | 降级：把依赖打挂再看 | 停掉画布服务 → 工具应返回可读文本而不是 reject |
| 5 | 回归：审批与开关 | 禁用插件 → 工具消失；`write` 工具应触发询问 |
| 6 | 文档：renderer id 与 data 示例 | README 自检清单逐条勾 |

### 3.2 四层验证的具体做法

**第一层 静态**

```powershell
node --check plugins/design-canvas/index.ts      # 纯 JS，能直接过语法检查
python -m json.tool plugins/design-canvas/plugin.json > $null
python -m json.tool plugins/design-canvas/schemas/canvas-ledger.json > $null
```

**第二层 装配**（宿主自己的预检最权威）

```powershell
Copy-Item -Recurse plugins\design-canvas "$env:USERPROFILE\.adago\plugins\design-canvas"
.\bin\adag.exe plugin list        # ID/VERSION/TRUST/ENABLED/SOURCE 都要对
.\bin\adag.exe plugin reload      # 「发现校验通过（N 个插件）」
```

入口形状/能力声明/route 冲突都会在这一层暴露——**不要跳过**。

**第三层 运行**：每个注册面各来一发

| 注册面 | 触发方式 | 看什么 |
| --- | --- | --- |
| 工具 | 会话里调用，或插件诊断页 | 返回文本 + `details` 形状 |
| route | `curl http://<gateway>/ext/design-canvas/api/ledger` | JSON + `Cache-Control: no-store` |
| 块 | 在回答里贴一段 `adg-ui` 封套 | 渲染成卡；故意少一个必填字段应降级成 `fallback` |

**不起 Gateway 也能验块**：宿主的渲染桥是纯 Go 的，直接构造一个 `host.Script`
就能跑「Schema 校验 → TSX 编译 → VNode → HTML」：

```go
scripts := []host.Script{{
    Manifest: &plugins.Manifest{ID: "design-canvas", Trust: plugins.TrustThirdParty,
        Frontend: &plugins.Frontend{Blocks: []plugins.BlockContribution{
            {Name: "canvas-ledger", Schema: "schemas/canvas-ledger.json",
             Template: "canvas-ledger.tsx", Profile: "display"}}}},
    Bundle:  map[string]string{"canvas-ledger.tsx": tplSrc},
    Schemas: map[string][]byte{"schemas/canvas-ledger.json": schemaSrc},
}}
b, _ := frontend.NewBridge(frontend.BridgeOptions{Scripts: scripts, MarkdownBlocks: true})
_, html, err := b.RenderBlock(req, "design-canvas/canvas-ledger", "b1", 1, data)
```

本仓库把这条固化成 `internal/gateway/frontend/design_canvas_block_test.go`，
另有一条 `internal/agent_core/plugins/discovery/design_canvas_test.go` 守 manifest 与契约文件。

**第四层 降级**：把画布停掉，再调 `design_canvas_sync`。
期望：**返回文本 + `details.error`，不抛异常**。抛异常会累计到
「连续 3 次失败自动禁用插件」，把一次没开服务变成插件被停用。

### 3.3 发布与回滚

- 分发用 zip：`adag plugin install --zip <file>`；本地开发直接拷目录 + 重载。
- 启停真源是 JSON：全局 `~/.adago/plugins.json`，工作区
  `<ws>/.adago/plugins.json` 只在该工作区单独启停时存在。
- 回滚：删目录（或恢复上一个 zip）+ 重载。热重载是双代切换，在途轮次不中断。
- 随包插件（`plugins/official/`）需要跑 `node plugins/official/build.mjs` 生成
  `dist/index.js`，再跑 `adag verify`。

### 3.4 本案例的验证记录

| 层 | 做法 | 结果 |
| --- | --- | --- |
| 静态 | `node --check`（拷成 `.mjs`）＋ 去注释后扫反模式 ＋ `json.tool` | 通过；无 `inputSchema` / 全局 `fetch` / `import` |
| 装配 | 拷到 `~/.adago/plugins/` 后 `adag plugin list` | `design-canvas 0.1.0 third-party true global c203faca…` |
| 契约 | `go test ./internal/agent_core/plugins/discovery -run DesignCanvas` | PASS：清单/能力/入口/块/技能 frontmatter 全部符合 |
| 块渲染 | `go test ./internal/gateway/frontend -run DesignCanvasLedgerBlock` | PASS：Schema→TSX→VNode→HTML，档位 class 与表格正确；多余字段被拒 |
| 冒烟 | 用假 `agent`/`ctx` 在 Node 里直跑入口（`node --check` + 断言注册面/台账/降级） | PASS；并发现 `canvasUrl` 误带 `/api/nodes` 路径，已拆成 base（展示）与 endpoint（报错定位） |
| 降级 | 停掉画布再调工具 | `details.error=unreachable`，文本给出「先启动 adago_design」的建议，插件未被禁用 |

（画布真实运行下的 `GET /ext/design-canvas/api/ledger` 需要在 Gateway 里跑；
本仓库 CLI 自检只装配随包官方插件，因此这一项以工具返回的封套 + 块渲染测试代替。）

---

## 4. 本插件的设计决策记录（ADR 风格）

**D1｜为什么不封装 MCP 工具**
MCP 工具经 `mcp-client` 已是普通工具描述符；再包一层会出现两套同名能力，
Schema 转换、超时、审批语义都要重做一遍，且第三方拿不到 `process` 能力，
本来也无法直连 stdio MCP。→ **插件只补"观测"，生产交给 MCP**。

**D2｜为什么两个工具都是 `access: "read"`**
它们只发 `GET`，不改画布、不落用户文件；写的只是插件自己的线程 state。
`network` 会让"看一眼画布"每次都弹审批，高频操作体验不可接受。
如果将来加"清空画布"这类工具，必须改成 `write` 并接受审批。

**D3｜为什么能力只有 `net.fetch` + `state`**
`fs` 用不上（不读写用户文件）；`exec` 第三方没有；`net.sse` 不需要（不订阅流）。
声明多余能力在评审时等于权限过宽。

**D4｜为什么块不依赖本插件 CSS**
安装型插件的 `styles` 不会被装配，只有 `plugins/official/` 的页面/样式进渲染桥。
所以模板只用宿主 `app-local.css` 里的 `adag-chart__*` 令牌类 + 语义标签。

**D5｜为什么失败不抛异常**
宿主对同一插件连续 3 次 reject 会自动禁用。「画布没启动」是常态而非异常，
必须降级成可读文本，让模型自己决定下一步。

**D6｜为什么不做插件页**
本次需求是"在对话里看到成果"，动态块已经满足；做页面还要放
`plugins/official/`，代价是插件从"可分发"变成"随包"，收益为零。

---

## 5. 一页版检查清单

**设计前**
- [ ] 一句话用途，不含"并且"
- [ ] 缺口确认：宿主/官方/MCP 都没有，才轮到我
- [ ] 六个问题（触发面/数据源/副作用/位置/UI/失败面）都有答案
- [ ] 能力位对照信任矩阵，方案在第三方档位可行

**设计中**
- [ ] `id` == 目录名，符合正则
- [ ] `parameters` + `execute`（不是 `inputSchema` + `run`）
- [ ] 返回 `{ content: [...], details }`
- [ ] `access` 与副作用一致
- [ ] 能力最小化，逐个能解释
- [ ] 所有外部失败归一为文本结果
- [ ] 块 schema `additionalProperties: false`；模板只用白名单标签与档位 class
- [ ] 块内本地辅助组件以函数调用使用（`Fn({...})`），不写成 JSX 标签
- [ ] README 有 renderer id + data 示例

**设计后**
- [ ] `node --check` + JSON 合法性
- [ ] `adag plugin list` / `plugin reload` 通过
- [ ] 工具 / route / 块各跑通一次
- [ ] 打挂依赖后仍返回可读错误
- [ ] 禁用插件后工具消失
- [ ] 启停与回滚路径写进 README
