# MCP 客户端（mcp-client）

把 MCP server 的工具接入为普通工具。基座不知道 MCP：插件在装配期读取配置、调用 `initialize` + `tools/list`，把每个 MCP 工具注册成普通 `ToolDescriptor`；运行期用 `tools/call` 执行。

当前范围：**HTTP / Streamable HTTP** 与 **stdio**。stdio 走基座的通用 `ctx.process` 能力（长驻 stdin/stdout、进程树终止），插件只是 MCP JSON-RPC 按行编解码。

该插件随包发布但**默认不在 coding/headless 产品声明里启用**；在插件页启用（或产品声明加入 `mcp-client`）后生效。

## 配置

配置优先级：工作区 `.adago/mcp.json` > 用户级 `~/.adago/mcp.json` > `[plugins.settings.mcp-client].servers`；同 `id` 前者覆盖后者。旧工作区 `.adag/mcp.json` 只读兼容（新文件存在时忽略），页面保存写 `.adago/mcp.json`。

stdio 子进程默认继承宿主白名单里的 `PATH`/`HOME`/`USERPROFILE`/`SystemRoot`/`APPDATA`/`LOCALAPPDATA`/`XDG_CONFIG_HOME`，再叠加环境变量模块的用户变量；需要别的宿主变量时在配置的 `env` 里列出（仍在清单 `backend.env` 白名单内）。

```json
{
  "version": 1,
  "servers": [
    {
      "id": "demo",
      "transport": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer ${secret:mcp_token}" },
      "timeoutMs": 60000,
      "trustReadOnly": false,
      "toolPrefix": "mcp_demo"
    },
    {
      "id": "local-git",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-git", "/workspace"],
      "cwd": "/workspace",
      "env": ["PATH", "HOME", "USERPROFILE", "SystemRoot"],
      "timeoutMs": 30000
    }
  ]
}
```

- `id` 必须匹配 `^[a-z][a-z0-9_]{0,15}$`，用于工具名前缀；非法 id 或同一文件内重复的 id 只跳过该项并记警告（不会让插件/产品装配失败）。
- `url` 只允许 `https://` 或回环 `http://`；禁止 URL 内嵌凭据。状态路由只展示去掉 query/fragment 的地址。
- `timeoutMs` 同时约束握手/调用等待与宿主 HTTP 请求本身（超时会真正取消请求），并受 `backend.initTimeoutMs` 的总装配预算限制；单 server 挂起只降级该 server。
- `trustReadOnly` 默认 `false`：即使 MCP server 声明 `readOnlyHint`，也按 `write` 工具处理，交给 `policy-default` 询问。只有用户显式信任该 server 时才映射为 `read`。
- `toolPrefix` 缺省 `mcp_<id>`；工具名最终满足基座 `^[a-z][a-z0-9_]{1,63}$`。
- `${secret:name}` 只能引用清单 `backend.secrets` 声明的名字；本插件当前声明 `mcp_token`。
- `tools/list` 变化后需要 `adag plugin reload mcp-client`；运行期不会动态注册工具。

## 行为

- 单 server 失败只跳过该 server；所有 server 失败时插件仍加载、贡献 0 工具。
- 配置读取失败、JSON 损坏、配置项非法只记警告并降级，插件始终加载（官方插件装配失败会连累产品启动）。
- HTTP 会话过期（服务端回 404）会重新 `initialize` 一次并重试该请求；其它错误不重试，避免重放副作用。
- MCP 工具的 `inputSchema` 会转换成基座支持的 JSON Schema 子集；`oneOf/anyOf/$ref` 等无法无损表达时注册宽松兜底 Schema，并在 `details.schemaSimplified` 标记。
- MCP 返回 `isError: true` 时按工具结果回给模型，不抛异常。
- 传输/认证/超时错误按普通错误文本返回，避免一次 server 故障触发插件连续失败禁用。
- 路由 `GET /ext/mcp-client/api/servers` 返回 server 状态与工具摘要（地址与命令行中的凭据打码）。

## 管理页

启用插件后，侧栏「MCP 服务端」（或直接访问 `/ext/mcp-client/servers`，插件详情页也有入口）提供管理页：

- server 列表：id、transport、配置来源、状态（已连接 / 不可用 / 已停用）、工具数、最后错误；
- 工具摘要：映射后的工具名、原始 MCP 名、access、`Schema 已简化` 标记；
- 逐个 server 启用 / 停用（写回工作区 `.adago/mcp.json` 的 `enabled`，启用时删掉该键）；
- 编辑工作区配置原文并保存（用户级 `~/.adago/mcp.json` 只读，页面不编辑）；编辑器带 JSON 语法高亮，打开时按 2 空格自动美化，可在保存前点「格式化」，语法错误按行/列提示；
- 「重载插件」：重新装配并按新配置发现工具。保存或启停后都需要重载才会生效。

数据面是本插件自己的 route（`GET /servers`、`GET|POST /config`、`POST /servers/toggle`），页面脚本用 `window.adaAPI` 调用（自动带 CSRF）。写入只发生在工作区 `.adago/mcp.json`（`fs.write`，宿主工作区 scope 内），不接受用户传入路径；页面数据里的地址/命令行已脱敏，配置原文只在用户点开编辑器时返回。

## 随包技能

插件带技能 `mcp-tools`（`skills/mcp-tools/SKILL.md`）：启用插件后随系统提示注入
name/description 与 SKILL.md 的绝对路径，模型按路径读取正文。内容覆盖：

- 从工具目录挑 `mcp_<server>_<tool>`（前缀与 `[MCP <id>]` 来源标记、Schema 被简化时的注意事项）；
- 审批与两类失败的区分（`isError` 是 server 业务错误；`mcpError` 是传输/认证/超时）；
- 输出按不可信数据处理、截断与资源索引、不要把 token 复述出来；
- 新增/启停 server 走管理页，或按本 README 改 `.adago/mcp.json` 后重载插件。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成；不要手改 `dist/`。`page.tsx` 与 `static/` 由 Gateway 渲染桥在运行时加载，不进 `dist/`。






