# MCP 插件化接入设计（v2）

> 上级索引：[ada-go 设计文档](design.md)
> 对应决策：[00 §1.2](00-overview.md#12-非目标v1-明确不做) 的 MCP 延后、[11 §15.2](11-structure-roadmap.md#152-开放问题) 的 MCP 开放问题
> 状态：设计提案（v2）。MCP-1 HTTP 与 MCP-2 stdio 已落地：`AssemblyContext`、`backend.initTimeoutMs`、`ctx.process` 与 `plugins/official/mcp-client`；管理页 `/ext/mcp-client/servers` 已落地（状态/工具/启停/配置编辑/重载）。`secretsPrefix` 与流式读取仍待后续。
> 版本：0.1 · 2026-11-02

## 0. 结论

**MCP 是一个（或一组）普通后端插件，不是基座能力。**

- 基座不新增 MCP RPC、MCP 钩子、MCP 插槽、MCP 专有配置字段。
- `mcp-client` 插件在**装配期**读取配置、连接 MCP server、调用 `tools/list`，把每个 MCP 工具注册成普通 `ToolDescriptor`；模型看到的仍是普通工具。
- 工具列表变化**不动态注册**：按现有「贡献在装配期结算」不变量，刷新工具列表 = `adag plugin reload <id>`；插件重载期间旧代继续服务。
- HTTP/Streamable HTTP 与 stdio 均已落地；stdio 依赖**通用**的 `ctx.process` 能力（长驻 stdin/stdout），插件只做 MCP JSON-RPC 编解码。基座仍然不知道 MCP 协议。
- MCP server 的工具描述与输出一律视为不可信数据；默认按非只读工具处理，交给 `policy-default` / 其他 `tool-policy` 插件询问。

这个设计的核心不是“写一个 MCP 客户端脚本”，而是解决两个现有契约缺口：

1. **装配期没有可用的上下文**：当前入口工厂只有 `agent`，没有 `ctx`，插件在注册工具前无法读配置、发网络请求或取密钥。
2. **没有长驻子进程能力**：当前 `ctx.exec` 是 `execFile` 一次性等待，不能给 stdio MCP server 持续写 stdin / 读 stdout。

---

## 1. 目标与非目标

### 1.1 目标

- 用户在工作区配置若干 MCP server（HTTP 或 stdio），插件把它们暴露为模型可调用的工具。
- 工具带来源（server id / 原始 tool 名）、描述、JSON Schema；参数校验、超时、截断、审计仍由基座工具机制统一执行。
- MCP server 不可用、工具列表为空、单个工具 schema 不支持时，插件**降级**而不是拖垮 Core。
- 工具调用经过现有 `tool.beforeCall` 策略链；默认非只读 MCP 工具必须询问。
- 插件可被禁用/重载；禁用后 MCP 工具从目录消失，基座无兜底。

### 1.2 非目标

- 不在基座内实现 MCP 协议、MCP 类型、MCP 连接池或 MCP UI。
- 不新增 Core RPC 方法；MCP 管理走插件页面/动作或 `plugin.reload`。
- v1 不支持 MCP resources / prompts / sampling / elicitation；只做 tools。
- v1 不做 OAuth 授权码流程；只支持显式 header / bearer token。
- v1 不保证长任务的流式进度；受工具超时与传输能力限制。
- 不把 MCP server 当成“可信插件”；它没有能力位，只有工具输入/输出。

---

## 2. 总体架构

```text
Browser
  │ SSR / SSE / 插件页面
  ▼
Gateway ── JSON-RPC ──► Core
                          │
                          ├─ engine.RunTurn
                          ├─ toolkit.Catalog（普通工具描述符）
                          └─ mcp-client 插件（moejs）
                               ├─ HTTP/Streamable HTTP ──► MCP server A
                               └─ stdio（ctx.process）──► MCP server B
```

约束：

- 模型请求里只出现普通工具；`tool.list` 与 MCP 无关。
- `mcp-client` 插件贡献 `registerTool`、可选 `route`/`registerAction`/前端页面；不贡献新协议。
- 插件 id、server id、原始 tool 名都出现在工具描述/详情里，便于审计与排障。
- 基座源码不出现 `mcp-*` 插件 id；产品声明决定是否启用。

---

## 3. 配置

### 3.1 位置

按优先级从高到低：

1. 工作区 `.adag/mcp.json`（推荐；随项目走，可被插件页面编辑）。
2. 用户级 `~/.adago/mcp.json`（只读配置；需要用户手工编辑）。
3. `config.toml` 的 `[plugins.settings.mcp-client]`（仅放非敏感默认值；嵌套数组在 TOML 里可读性差，不作为首选）。

工作区配置由插件通过 `ctx.fs.readText`/`writeText` 管理；用户级配置只读，因为 `ctx.fs.writeText` 只允许工作区与已安装插件目录，不允许写 `~/.adago` 根。

### 3.2 形状

```json
{
  "version": 1,
  "servers": [
    {
      "id": "filesystem",
      "enabled": true,
      "transport": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${secret:mcp_token}"
      },
      "timeoutMs": 60000,
      "trustReadOnly": false,
      "toolPrefix": "fs"
    },
    {
      "id": "local-git",
      "enabled": false,
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-git", "/workspace"],
      "cwd": "/workspace",
      "env": ["PATH", "HOME"],
      "timeoutMs": 30000
    }
  ]
}
```

规则：

- `id` 必须匹配 `^[a-z][a-z0-9_]{0,15}$`，用于工具名前缀；非法 id 或同一文件内重复的 id 只跳过该项并记警告（配置错误降级，不拖垮插件装配）。
- `url` 只允许 `https://`，或 `http://127.0.0.1` / `http://localhost`；禁止 URL 内嵌用户名密码。
- `${secret:name}` 只能引用清单 `backend.secrets` 允许的名字；缺失时该 server 跳过并记警告，不把空 token 发出去。示例使用单 token `mcp_token`；每个 server 独立 token 需要 `backend.secretsPrefix`（见 8.3）。
- `trustReadOnly` 默认 `false`：即使 MCP server 声明 `readOnlyHint`，也按非只读工具处理，交给策略插件询问。只有用户显式信任该 server 时才映射为 `read`。
- `timeoutMs` 默认 60000，夹在 1000–600000；调用超时由基座工具预算与 MCP 请求两层共同约束。
- `enabled: false` 的 server 不连接、不注册工具。
- `stdio` 已支持，需要 `process` 能力；每个 server 一个长驻进程，受每 Runtime 8 个、30 分钟生命周期与进程树终止约束。`command` 必须是显式程序，不经过 shell。

---

## 4. 装配期发现与注册

### 4.1 设计时缺口（已在 P0 补上）

设计时入口工厂签名只有：

```ts
export default function (agent: AgentAPI): void | Promise<void>;
```

`agent` 只有 `registerTool/registerProvider/registerPrompt/registerAction/route/on`，工厂里没有 `ctx.fetch`、`ctx.secret`、`ctx.settings`，因此：

- 工厂无法直接连接 MCP server 做 `tools/list`；
- 工厂无法读取密钥；
- 插件默认工厂预算只有 5s，多 server 发现容易超时。

现在入口工厂已支持可选的第二个只读参数 `(agent, ctx)`，并可通过 `backend.initTimeoutMs` 放宽预算；详见 4.2。

### 4.2 通用扩展（已实现）

给入口工厂增加第二个参数，保持旧插件兼容：

```ts
export default async function (agent: AgentAPI, ctx: AssemblyContext): Promise<void>;
```

`AssemblyContext` 是**装配期受控上下文**，成员按能力安装，语义与 `ToolContext` 对齐：

```ts
interface AssemblyContext {
  workspace: string;
  settings: Record<string, unknown>; // [plugins.settings.<id>]
  log(level: string, msg: string, fields?: object): void;
  signal: AbortSignal;
  fs?: { readText(path: string, opts?: { maxBytes?: number }): Promise<string> };
  fetch?: (url: string, init?: object) => Promise<{ status: number; headers: object; text: string }>; // net.fetch
  process?: { start(cmd: string, args: string[], opts?: object): Promise<ProcessHandle> }; // process，仅 builtin/verified
  secret?(name: string): string | null; // 仅清单声明/前缀允许的名字
}
```

关键点：

- 受控：装配期不允许 `fs.writeText`、`state.set`、`ui.*`；唯一允许的装配期副作用是 `ctx.process.start`（stdio MCP），由 `process` 能力、程序策略与进程管理器约束。
- `fetch` 只在有 `net.fetch` 时存在；`secret` 只在清单声明时存在；没声明就是属性不存在。
- 工厂的 Promise 仍要结算；新增 manifest 字段 `backend.initTimeoutMs`（默认 5000，上限 60000，仅 builtin/verified）用于给 MCP 这类需要网络发现的插件放宽预算。超时按插件失败处理，不拖垮整个 Core。
- 装配期 `fetch` 的响应仍受宿主 16MB/超时/重定向限制；插件自己再做 MCP 协议超时。

> 这是通用能力，不是 MCP 专有：任何需要装配期读取远程目录的插件都可以用。基座仍不知道 MCP。

### 4.3 发现流程

```ts
export default async function (agent, ctx) {
  const cfg = await loadConfig(ctx); // ctx.fs 或 ctx.settings
  const clients = [];

  for (const server of cfg.servers.filter((s) => s.enabled)) {
    try {
      const client = new MCPClient(server, ctx);
      await client.initialize();          // initialize + notifications/initialized
      const tools = await client.listTools(); // 处理 nextCursor 分页
      clients.push(client);
      for (const tool of tools) {
        const def = toToolDef(server, tool, client);
        if (def) agent.registerTool(def);
      }
    } catch (err) {
      ctx.log("warn", `MCP server ${server.id} 不可用，已跳过`, { err: String(err) });
    }
  }

  agent.route("GET", "/servers", () => statusView(clients, cfg));
  // 可选前端页面：只读展示 server 状态与工具数
}
```

规则：

- 单 server 失败只跳过该 server；配置里非法或重复的项**只跳过该项并记警告**（配置是用户可编辑的，装配失败会让整个产品起不来，代价过高）。
- 每个 server 的 `initialize` + `tools/list` 有硬期限：`min(timeoutMs, 剩余发现预算)`，整个发现阶段共用 `DISCOVERY_BUDGET_MS`（25s，为 `initTimeoutMs=30000` 留收尾余量）。挂起的 server 只降级自己，不会耗尽装配预算。
- 所有 server 都失败时插件仍算加载成功，只是贡献 0 个工具；插件页显示错误，用户修复后 `plugin.reload`。
- 发现阶段不注册任何“占位工具”；工具目录里不出现假能力。单个工具注册失败（与其它插件同名等）只丢该工具，已注册的保留。
- 发现完成后不能再注册；运行期 `registerTool` 仍按现有规则只记警告。

---

## 5. 工具命名与 Schema 映射

### 5.1 命名

工具名必须满足基座规则 `^[a-z][a-z0-9_]{1,63}$`，且全目录唯一。

```text
mcp_<serverId>_<toolSlug>
或
<toolPrefix>_<toolSlug>   // 配置了 toolPrefix 时
```

- `toolSlug`：小写，非 `[a-z0-9_]` 替换为 `_`，折叠连续 `_`，去掉首尾 `_`。
- 超过 63 字节时截断到 54 字节，追加 `_` + 原始名字的短哈希（djb2 前 8 位十六进制，只用于去重，不参与安全判定）。
- 仍冲突时追加 `_2`、`_3`；插件保留 `toolName -> {serverId, originalName}` 映射。
- 原始 tool 名只出现在描述/详情与 MCP 请求里，不直接作为基座工具名。

描述格式：

```text
[MCP filesystem] read_file
读取文件。来源：MCP server filesystem / tool read_file。
```

描述截断到 2000 字符；MCP server 提供的描述视为不可信文本，不做 Markdown 渲染，不放进系统提示的指令区。

### 5.2 Schema

MCP `inputSchema` 是 JSON Schema，基座只支持封闭子集（`type/properties/required/additionalProperties/items/enum/minimum/maximum/minLength/maxLength/pattern/minItems/maxItems/uniqueItems/description/default/format/title/nullable/$comment`）。

转换规则：

1. 优先原样使用，交给 `toolkit.compileSchema` 校验。
2. 删除 `$schema`、`examples`、`$id`、`$defs`（这些不在基座白名单；`$defs` 需先内联 `$ref`）。
3. `const: x` → `enum: [x]`。
4. `oneOf/anyOf/allOf`、`$ref` 无法无损表达时：
   - 不能猜测语义，也不应静默放宽；
   - 注册为**宽松兜底 Schema**：
     ```json
     { "type": "object", "additionalProperties": true,
       "description": "原始 MCP inputSchema 含基座不支持的组合关键字，参数由 MCP server 自行校验" }
     ```
   - 在 `details.schemaSimplified = true` 与插件页标记该工具“Schema 已简化”。
   - 原始 Schema 不放进模型上下文，避免上下文膨胀与注入。
5. `inputSchema` 不是对象 / 没有 `properties` 时，用 `{type:"object"}`；仍可调用。
6. `outputSchema` 不映射到基座；只用于插件页展示。

如果工具名或 Schema 转换失败，跳过该工具并记警告，不影响同 server 的其他工具。

---

## 6. 调用与结果映射

### 6.1 ToolDef

```ts
agent.registerTool({
  name: mappedName,
  description: prefixedDescription,
  parameters: translatedSchema,
  access: mapAccess(server, tool),
  timeoutMs: server.timeoutMs ?? 60000,
  parallelSafe: false,
  repeatable: false,
  rollback: false,
  execute: async (args, ctx) => callMCP(client, tool, args, ctx),
});
```

`access` 映射：

| MCP annotations / 配置 | access |
|---|---|
| `readOnlyHint === true` 且 `server.trustReadOnly === true` | `read` |
| `openWorldHint === true` | `network` |
| 其他（含 `destructiveHint`、无注解、readOnly 但未信任） | `write` |

`trustReadOnly` 默认 false 的原因：`readOnlyHint` 是 server 自己声明的，不能作为安全依据。未信任时按写工具处理，`policy-default` 在 `ask` 模式下会询问；`readonly` 模式直接拒绝。

### 6.2 MCP 调用

```ts
async function callMCP(client, tool, args, ctx) {
  const res = await client.callTool(tool.name, args); // tools/call
  return toToolReturn(res);
}
```

- 只调用配置里的 server；模型不能传 URL/server 地址。
- `tools/call` **不自动重试**：远端工具可能有副作用；重试只允许发生在 `initialize`/`tools/list` 这类只读握手。
- 传输错误、401/403、协议错误：返回普通 error 文本（`MCP server unavailable: ...`）并带 `details.mcpError`，不抛异常。这样不会因为一次 server 故障触发插件连续失败禁用，模型也能看到原因。
- 参数错误（本地映射/JSON 序列化失败）才抛错，让模型修参数。
- `ctx.signal` 取消贯穿到 HTTP 请求；stdio 的请求等待受调用取消影响，进程本身由插件关闭/宿主 Runtime 关闭统一 kill。

### 6.3 结果

MCP `CallToolResult.content[]` 映射到基座 `ToolReturn.content`：

| MCP content | 基座 |
|---|---|
| `text` | `{type:"text", text}` |
| `image` | `{type:"image", mime, data}`（产品/模型不支持图片时由基座既有双门拒绝） |
| `audio` | 文本占位 `[audio omitted]` + details |
| `resource`（text） | 文本 + `details.resources` |
| `resource`（blob） | 文本占位 + `details.resources`，不把任意二进制塞进上下文 |
| `resource_link` | Markdown 链接文本 `[name]` 指向 `uri`；URI 按 Markdown 安全规则清洗 |

补充：

- `structuredContent` 放 `details.structuredContent`；若 `content` 为空，用 JSON 文本作为模型可见内容。
- `isError: true`：文本前加 `MCP tool error: `，`details.isError = true`；不抛异常，按工具结果回给模型。
- 单个 text 截断到 50KB（基座再按 50KB/2000 行截断一次）；图片/base64 总量不超过 1MB（基座 `MaxResultBytes`）。
- 空结果返回 `(无输出)`，不伪造“成功”。

---

## 7. 生命周期、刷新与降级

### 7.1 连接

- HTTP：`initialize` 后缓存 `Mcp-Session-Id`（若 server 返回）；后续请求带 `MCP-Protocol-Version` 与 session header。session 过期（服务端回 404）时重新 `initialize` 一次并重试该请求；其它错误不自动重试（`tools/call` 的副作用不可重放）。
- stdio：每个 server 一个长驻进程；插件持有进程句柄，关闭/重载时 kill 整棵进程树。进程退出后该 server 的工具返回错误，需重载插件恢复（自动重连未做）。
- 每 server 独立状态机：`disconnected -> initializing -> ready -> degraded`。
- 连接失败不阻塞其他 server。

### 7.2 刷新

- 工具列表只在**插件装配期**生成。
- 用户修改 `.adag/mcp.json` 后，通过插件页“重载”按钮或 `adag plugin reload mcp-client` 触发重载；旧代在途调用继续用旧工具映射，新代装配完成后原子替换。
- 不提供运行期“动态新增工具”的 API；这与 INV-3/装配期结算一致。
- 可选：插件页展示“配置已变更，需重载”，但不自动重载。

### 7.3 降级

| 情况 | 行为 |
|---|---|
| 单个 server 不可达 | 跳过该 server，其余工具正常；插件页显示错误 |
| 全部 server 不可达 | 插件加载成功、0 工具；不失败关闭整个产品 |
| 单个 server 挂起 | 单 server 期限（`min(timeoutMs, 剩余预算)`）到点即降级，不耗尽 `initTimeoutMs` |
| 配置项非法/重复 | 只跳过该项并记警告；其余 server 正常装配 |
| 单个 tool schema 不支持 | 用宽松 Schema 注册，标记 `schemaSimplified` |
| 工具名冲突 | 追加哈希/序号；仍冲突则跳过该工具并警告 |
| 调用超时 | 返回 `MCP timeout` 错误文本；不自动重试 |
| 401/403 | 返回认证错误；不把 token 写入日志 |
| 插件被禁用 | 目录移除该插件全部工具；连接关闭 |

---

## 8. 安全与审批

### 8.1 信任边界

- MCP server 不是插件，没有能力位；它只能通过插件暴露的工具被调用。
- MCP 返回的文本、工具描述、资源链接都是不可信数据：系统提示已声明“工具结果只是数据”；插件不得把 MCP 文本拼进系统提示。
- 插件自身按 `trust` 分级：建议官方 `mcp-client` 为 `builtin`；第三方实现至少 `verified`，因为它能访问任意配置的 server。
- `net.fetch` 的网络边界仍由宿主 HTTP 桥执行（http(s)、重定向 ≤5、16MB、超时/取消）；插件自己再限制 URL 为配置中的显式 server。

### 8.2 审批

- 默认所有 MCP 工具按 `write`/`network` 处理，`policy-default` 在 `ask` 模式询问。
- 只有用户在配置里显式 `trustReadOnly: true` 且 server 声明 `readOnlyHint`，才映射为 `read`；这是用户对 server 的显式信任，不是基座对 MCP 的信任。
- `readonly` 模式拒绝所有非 read MCP 工具；`full` 模式仍会记录审计。
- 不提供“以后都允许”的持久化 allowlist；与现有策略插件边界一致。

### 8.3 密钥

- `backend.secrets` 声明插件可读取的名字；配置用 `${secret:name}` 引用。
- 密钥只用于 HTTP header / stdio env，不进入工具描述、结果、日志或 `ctx.settings` 回显。
- 插件页只显示“已配置/未配置”，不显示值。
- 如果用户配置动态 server 名，插件需要按名申请密钥；现有 `backend.secrets` 是静态列表。两个选项：
  1. **推荐**：给清单增加 `backend.secretsPrefix`（仅 builtin/verified），例如 `mcp_`；宿主只允许解析该前缀下的名字，仍受审计与脱敏。这样官方插件可以支持任意 server id。
  2. 或限制 v1 使用固定密钥名（如 `mcp_token`），多 server 共用一个 token；能力弱但无需改 Schema。
- 在 `secretsPrefix` 落地前，`mcp-client` 采用方案 2，并在插件页提示限制。

### 8.4 资源限制

| 项 | 建议值 |
|---|---|
| 每工作区 MCP server 数 | 32 |
| 每 server 工具数 | 200 |
| 单次 `tools/list` 分页 | 32 页 |
| `tools/call` 超时 | 默认 60s，最大 600s（还受轮次/工具预算约束） |
| HTTP 响应 | 宿主 16MB；插件只接受 JSON-RPC 单条，超过 1MB 的 content 截断 |
| 工具结果 | 基座 1MB / 50KB / 2000 行 |
| 装配期发现总预算 | `backend.initTimeoutMs`，默认 5s，MCP 建议 30s |
| stdio 进程数 | 每 Runtime 8，单进程生命周期 30m |

---

## 9. 前端与运维

### 9.1 插件页（已落地）

`plugins/official/mcp-client` 声明 `frontend` 段，提供 `/ext/mcp-client/servers`
（侧栏「MCP 服务端」、插件详情页的「打开插件页面」都会指向它）：

- server 列表：id、transport、配置来源（工作区/用户级/settings）、状态（已连接/不可用/已停用）、工具数、最后错误；
  地址与 stdio 命令行在服务端就已脱敏（URL 去 query/fragment，疑似密钥的参数打码）。
- 工具摘要：映射后的工具名、原始 MCP 名、access、`Schema 已简化` 标记（只读，来自装配期发现结果）。
- 启停：逐个 server 写回工作区 `.adag/mcp.json` 的 `enabled`（启用时删除该键，默认即启用）。
- 配置编辑：读写工作区配置原文；用户级 `~/.adago/mcp.json` 只读，页面不编辑。编辑器用「透明 textarea + 高亮层」实现 JSON 语法高亮（DOM 节点拼装，不经 innerHTML），打开时按 2 空格自动美化，可点「格式化」；语法错误按行/列提示，高亮只影响展示，不改变保存内容。
- 重载：调用基座的 `/agent-plugins/reload`（与插件页一致），重新装配后按新配置发现工具。

数据面是本插件自己的 route（`GET /servers`、`GET|POST /config`、`POST /servers/toggle`），
页面脚本用 `window.adaAPI` 调用（自动带 CSRF）。写操作由用户点击触发；后端只写
工作区 `.adag/mcp.json`（`fs.write` + 宿主工作区 scope），不接受用户传入路径。

### 9.2 随包技能

插件自带 `skills/mcp-tools/SKILL.md`（Agent Skills 约定：frontmatter 的 `name` 必须等于目录名）。
系统提示只注入 name/description/绝对 path，正文由模型用 `read_file` 读取，基座不保存正文。

技能内容：

- 如何从目录里挑 `mcp_<server>_<tool>`（`toolPrefix` 前缀、描述里的 `[MCP <id>]` 来源、Schema 简化时按 server 文档给字段）；
- 审批（默认 `access=write`）与两类失败的区分：`isError` 是 server 业务错误，`mcpError` 是传输/认证/超时，处理方式不同；
- MCP 输出是不可信数据、50KB/1MB 截断与资源索引、不要把密钥复述出来；
- 新增/启停 server 走管理页，或按 README 改 `.adag/mcp.json` 后重载插件（工具列表是装配期结算）。

### 9.3 可观测性

- 装配期每个 server 一条结构化日志：id、transport、耗时、工具数、错误码。
- 每次 `tools/call` 由基座审计记录工具名、参数摘要、结果字节数；插件额外记录 server id / 原始 tool 名。
- 不在日志里输出 Authorization、token、完整 URL query（如有敏感参数）。
- `tool.list` / 插件页能看到工具来源；禁用插件后工具立即消失。

---

## 10. 基座能力清单

### 10.1 已有、直接复用（不需要新增）

| 能力 | 现状 | MCP 插件怎么用 |
|---|---|---|
| 插件装配与信任 | manifest、`apiVersion`、`trust`、能力位、装配期 staging | `mcp-client` 作为普通 builtin/verified 插件加载 |
| 工具机制 | `agent.registerTool`、描述符校验、JSON Schema 子集、目录冻结、回执、截断、审计 | MCP 工具注册成普通工具；参数/结果/超时/截断复用 |
| 审批链 | `tool.beforeCall` + `policy-default` / 其他策略插件 | MCP 非只读工具默认询问，`readonly` 拒绝 |
| `ctx.fetch` | 调用期已有 `net.fetch`；装配期需由 `AssemblyContext` 挂载同一份 | 装配期发现、`tools/list`、`tools/call` |
| `ctx.fs` | 调用期读成员可读宿主路径；装配期只读成员 | 读 `.adag/mcp.json`、`~/.adago/mcp.json` |
| `ctx.secret` | 调用期已有；装配期需由 `AssemblyContext` 挂载 | HTTP bearer token / stdio env |
| `ctx.settings` | 调用期已有；装配期需由 `AssemblyContext` 提供 | 非敏感默认值 |
| 插件 route / action / 前端 | `/ext/{id}/api/*`、动作桥、Vue/TSX 页面 | server 状态页、重载按钮、工具列表 |
| 热重载 | 双代 Runtime、在途调用旧代、`plugin.reload` | 配置变更后重载并重新发现工具 |
| 审计与日志 | `ctx.log`、工具审计、`audit` 表 | 记录 server/tool、耗时、错误；不记 token |
| 插件状态 | `plugin.list`、degraded、连续失败禁用 | 插件页展示 server 状态；单 server 失败不拖垮插件 |

### 10.2 需要新增的通用宿主能力

| 能力 | 用途 | 是否 MCP 专有 | 优先级 |
|---|---|---|---|
| 工厂第二参数 `AssemblyContext` | 装配期读配置、发 `tools/list`、取密钥 | 否 | P0 |
| `backend.initTimeoutMs` | 放宽装配期发现预算 | 否 | P0 |
| `ctx.process`（长驻 stdin/stdout + kill） | stdio MCP transport | 否 | P1（已实现） |
| `backend.secretsPrefix` | 动态 server 的按名密钥 | 否 | P1 |
| `ctx.http` 流式读取（可选） | MCP SSE 进度/长流 | 否 | P2 |
| `agent.onClose(fn)`（可选） | 插件关闭时发 MCP `shutdown`/刷状态 | 否 | P2 |

**P0 是 v1 的前置条件**。没有工厂上下文，只能在装配期注册一个泛化的 `mcp_call` 工具，无法把每个 MCP 工具作为独立 schema 暴露给模型；这不是本设计推荐路线。`ctx.process` 不是 HTTP MVP 的前置条件，但 stdio 必须依赖它。

当前实现状态：P0/P1 的 `ctx.process` 与管理页均已落地（`AssemblyContext`、`backend.initTimeoutMs`、`plugins/official/mcp-client` 的 HTTP + stdio + `/ext/mcp-client/servers`）；`secretsPrefix` 与流式读取仍待后续。

**P0 是 v1 的前置条件**。没有工厂上下文，只能在装配期注册一个泛化的 `mcp_call` 工具，无法把每个 MCP 工具作为独立 schema 暴露给模型；这不是本设计推荐路线。

`ctx.process` 建议形状（仅概念，具体以实现评审为准）：

```ts
interface ProcessHandle {
  pid: number;
  write(data: string): void;                       // stdin
  read(opts?: { maxBytes?: number; timeoutMs?: number }): Promise<{
    stdout: string; stderr: string; exited: boolean; code?: number;
  }>;                                              // 自上次 read 以来的增量
  kill(): void;                                    // 杀整棵进程树
}
ctx.process?: {
  start(cmd: string, args: string[], opts?: {
    cwd?: string; env?: string[]; maxBuffer?: number; timeoutMs?: number;
  }): Promise<ProcessHandle>;                      // capability: process
};
```

它和 `ctx.exec` 一样受程序策略、cwd scope、env 白名单、输出上限、审计与进程树终止约束；只是把一次性等待换成可读写句柄。基座仍然不认识 MCP。

---

## 11. 分阶段交付

### 11.1 MCP-1（v2.0，HTTP）—— 已落地（MVP）

- 补 `AssemblyContext` + `backend.initTimeoutMs`（已实现）。
- 实现 `mcp-client` 插件：配置、HTTP 发现、工具命名/Schema 转换、`tools/call`、结果映射、`GET /ext/mcp-client/api/servers` 状态路由，以及 `/ext/mcp-client/servers` 管理页（状态/工具/启停/配置编辑/重载，已实现）。
- 测试：假 MCP HTTP server（初始化/分页/成功/错误/401/超时/超大结果/取消）、工具名冲突、Schema 简化、禁用后工具消失、重载后工具变化。
- 验收：`adag verify` 不变；空基座仍 0 工具；MCP server 全挂时 Core 正常启动；非只读工具默认触发审批。

### 11.2 MCP-2（stdio）—— 已落地（MVP）

- 补 `ctx.process` 通用能力与安全边界（进程数、输出、超时、进程树、审计）—— 已实现。
- `mcp-client` 增加 stdio transport：JSON-RPC 按行读写、`initialize`/`tools/list`/`tools/call`、进程退出/重连 —— 已实现基础版；自动重连未做，进程退出后该 server 的工具返回错误，需重载插件恢复。
- 测试：假 stdio server（用测试二进制）、stdin/stdout 分帧、进程崩溃、取消、插件关闭杀进程树。

### 11.3 MCP-3（可选）

- `resources/list`/`resources/read` 映射为只读工具或资源查看器。
- `prompts/list`/`prompts/get` 映射为技能/提示模板（需先定义权限）。
- progress notifications → 插件页/工具详情；仍不进入模型上下文。
- OAuth / PKCE；token 存储与刷新。

---

## 12. 验收清单

- [ ] 基座无新增 RPC 方法、钩子点位、插槽、官方插件 id。
- [ ] 空基座工具目录仍为 0；不启用 `mcp-client` 时无 MCP 能力。
- [ ] HTTP MCP server 的成功/失败/超时/401/超大结果/取消都有测试。
- [ ] 工具名符合基座规则，冲突有确定性回退。
- [ ] 不支持的 Schema 不静默放宽：用宽松 Schema 并显式标记。
- [ ] 默认非只读工具经过 `tool.beforeCall` 策略；`readOnlyHint` 不被默认信任。
- [ ] server 不可用只降级该 server；所有 server 失败时插件仍加载。
- [ ] 运行期不动态注册；重载后工具目录原子替换。
- [ ] 插件禁用/重载后连接关闭、工具消失、无残留 goroutine/子进程。
- [ ] 密钥不出现在日志、工具结果、前端 props 与 `process.env`。

---

## 13. 开放问题

1. **工厂上下文是否只读**：装配期是否允许 `fs.writeText`/`state.set`？本设计建议只读，避免装配副作用；MCP 发现不需要写。
2. **发现预算**：`backend.initTimeoutMs` 放在清单还是宿主配置？清单更贴近插件，但需要 Schema/verify 同步。
3. **`secretsPrefix`**：前缀授权会扩大 builtin/verified 插件的密钥面；是否只允许 builtin，或要求每次安装时确认？
4. **readOnlyHint 信任**：是否提供 UI 的“信任此 server 的只读声明”开关，而不是只靠配置文件？
5. **长任务/进度**：`ctx.fetch` 目前缓冲完整响应；MCP 长工具是否必须等 `ctx.http.stream`/`ctx.fetch` 流式能力？
6. **stdio 进程安全**：`ctx.process` 是否需要独立于 `exec` 的能力位与更严格的程序 allowlist？
7. **resources/prompts**：是否进入 v2 范围，还是保持“只做 tools”的最小面？
8. **多工作区配置**：已钉定为工作区 `.adag/mcp.json` > 用户级 `~/.adago/mcp.json` > `settings`；同 `id` 前者覆盖后者。
9. **stdio 写入阻塞**：`process.write` 目前是在脚本线程上同步写 stdin；参数很大且 server 暂时不读时会阻塞该插件的 Runtime（无期限）。后续可改成带期限的异步写队列，或给单条消息加大小上限（已有 4MB 分帧上限兜底）。

---

**相邻文档**：[插件模型](05-plugins.md) · [插件运行时](06-plugin-runtime.md) · [SSR 前端](07-ssr-frontend.md) · [安全与运维](09-security-operations.md) · [Node 兼容](12-node-compat.md)




