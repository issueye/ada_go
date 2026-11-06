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

启用插件后，侧栏「MCP 服务端」（或直接访问 `/ext/mcp-client/servers`，插件详情页也有入口）提供管理页。0.5.0 起与 `provider-admin` / `env-manager` 一样是 **SSR + hydrate 岛屿组件**（`page.tsx` 是组件，`client.tsx` 是浏览器入口，`static/client.js` 由 `pluginbuild` 生成）：

- 头部：标题 + `ⓘ` tooltip（两层配置的覆盖规则、保存/启停后要重载）、「刷新状态 / 重载插件 / 编辑配置」；
- 汇总行：server 总数 + 状态筛选分段（全部 / 已连接 / 异常（有降级才出现）/ 已停用）+ 最后刷新时间；
- server 卡片：状态灯、等宽 ID、transport 与状态徽章、启停开关、脱敏后的命令行（可复制）、来源与工具前缀；降级卡额外显示错误条与「重新探测」（即重新装配）；
- 工具表：默认显示前 4 条，「展开全部 N 个」展开；工具名 / 访问（只读·写入）/ 备注（原始 MCP 名、Schema 已简化）；
- 空态：直接给「编辑配置」入口；所有一次性反馈走底部 toast，不再有常驻状态行；
- 「编辑配置」弹窗：`.adago/mcp.json` 原文、透明 textarea + 背后语法高亮层（滚动同步）、实时 JSON 校验（语法错误定位到行/列、结构要 `servers` 数组、统计 server 数与行数）、格式化、重新读取、保存；密钥提示用 `${secret:mcp_token}` 引用，不写明文。

数据面仍是本插件自己的 route（`GET /servers`、`GET|POST /config`、`POST /servers/toggle`），页面脚本用 `window.adaAPI` 调用（自动带 CSRF）。写入只发生在工作区 `.adago/mcp.json`（`fs.write`，宿主工作区 scope 内），不接受用户传入路径；页面数据里的地址/命令行已脱敏，配置原文只在用户点开编辑器时返回。

**SSR 预取**：后端 `index.ts` 额外注册了只读动作 `state`（与 `GET /servers` 同源），清单里绑定为 `page_servers`；结果落在 `props.data.backend["mcp-client"].page_servers`，首屏与无 JS 时也能看到 server 列表。预取失败时组件在 `mounted()` 里改走 `/servers` 补齐。

组件侧沿用两条约定：说明性文案一律 `ⓘ` + tooltip；一次性反馈走底部 toast（方法名 `showToast`，`data` 字段叫 `toast`，避免单 Proxy 里 data 遮蔽 methods）。SSR 的 VNode 白名单不含 `svg`，状态灯/开关全部用文字 + CSS 画。

`static/mcp-client.js` 是 0.4.0 的 legacy 脚本，清单已不再引用，只作回滚备份保留。

## 随包技能

插件带技能 `mcp-tools`（`skills/mcp-tools/SKILL.md`）：启用插件后随系统提示注入
name/description 与 SKILL.md 的绝对路径，模型按路径读取正文。内容覆盖：

- 从工具目录挑 `mcp_<server>_<tool>`（前缀与 `[MCP <id>]` 来源标记、Schema 被简化时的注意事项）；
- 审批与两类失败的区分（`isError` 是 server 业务错误；`mcpError` 是传输/认证/超时）；
- 输出按不可信数据处理、截断与资源索引、不要把 token 复述出来；
- 新增/启停 server 走管理页，或按本 README 改 `.adago/mcp.json` 后重载插件。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成；不要手改 `dist/`。`static/client.js` 由 `go run ./tools/pluginbuild -dir plugins/official/mcp-client` 生成（源是 `client.tsx` + `page.tsx`），发布前用 `-check` 校验漂移。`page.tsx` 与 `static/` 由 Gateway 渲染桥在运行时加载，不进 `dist/`。






