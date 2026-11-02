# MCP 客户端（mcp-client）

把 MCP server 的工具接入为普通工具。基座不知道 MCP：插件在装配期读取配置、调用 `initialize` + `tools/list`，把每个 MCP 工具注册成普通 `ToolDescriptor`；运行期用 `tools/call` 执行。

当前范围：**HTTP / Streamable HTTP** 与 **stdio**。stdio 走基座的通用 `ctx.process` 能力（长驻 stdin/stdout、进程树终止），插件只是 MCP JSON-RPC 按行编解码。

该插件随包发布但**默认不在 coding/headless 产品声明里启用**；在插件页启用（或产品声明加入 `mcp-client`）后生效。

## 配置

配置优先级：工作区 `.adag/mcp.json` > 用户级 `~/.adago/mcp.json` > `[plugins.settings.mcp-client].servers`；同 `id` 前者覆盖后者。

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

- `id` 必须匹配 `^[a-z][a-z0-9_]{0,15}$`，用于工具名前缀。
- `url` 只允许 `https://` 或回环 `http://`；禁止 URL 内嵌凭据。
- `trustReadOnly` 默认 `false`：即使 MCP server 声明 `readOnlyHint`，也按 `write` 工具处理，交给 `policy-default` 询问。只有用户显式信任该 server 时才映射为 `read`。
- `toolPrefix` 缺省 `mcp_<id>`；工具名最终满足基座 `^[a-z][a-z0-9_]{1,63}$`。
- `${secret:name}` 只能引用清单 `backend.secrets` 声明的名字；本插件当前声明 `mcp_token`。
- `tools/list` 变化后需要 `adag plugin reload mcp-client`；运行期不会动态注册工具。

## 行为

- 单 server 失败只跳过该 server；所有 server 失败时插件仍加载、贡献 0 工具。
- MCP 工具的 `inputSchema` 会转换成基座支持的 JSON Schema 子集；`oneOf/anyOf/$ref` 等无法无损表达时注册宽松兜底 Schema，并在 `details.schemaSimplified` 标记。
- MCP 返回 `isError: true` 时按工具结果回给模型，不抛异常。
- 传输/认证/超时错误按普通错误文本返回，避免一次 server 故障触发插件连续失败禁用。
- 插件页/路由 `GET /ext/mcp-client/api/servers` 返回 server 状态与工具数。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成；不要手改 `dist/`。
