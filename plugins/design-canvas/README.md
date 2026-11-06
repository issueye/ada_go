# design-canvas —— 设计画布台账插件

配合 [`adago_design`](../../mcp_projects/adago_design) 这个 **MCP** 使用：读取无限画布的
节点现状，生成线程级台账，并能在回答里渲染成只读卡片。

## 1. 它解决什么问题（以及为什么不是再包一层 MCP）

ada-go 里 **MCP 不是一个基座能力**，而是由官方 `mcp-client` 插件在**装配期**读取
`.adago/mcp.json`、连接 server、把每个 MCP 工具注册成**普通工具描述符**（见
[docs/18-mcp-plugin.md](../../docs/18-mcp-plugin.md)）。因此：

```
模型 ──► mcp_adago_design_design_preview ──► mcp-client 插件 ──► adago_design MCP (stdio)
                                                                    │
模型 ──► design_canvas_sync ──► 本插件 ──► ctx.fetch ──► 画布 HTTP (127.0.0.1:8788)
```

| 层 | 职责 | 谁提供 |
| --- | --- | --- |
| 生产 | Token/组件查询、预览生成、整页模板、走查、截图 | adago_design MCP（经 `mcp-client`） |
| 观测 | 画布现状 → 台账 → Markdown 动态块 | **本插件** |

**本插件刻意不注册 `design_preview` / `canvas_add` 之类的工具**：

1. MCP 工具已经是普通工具，再包一层会让模型面对「两个 design_preview 该调哪个」；
2. 重复封装要重新承担 Schema 转换、超时、审批语义，收益为零；
3. 第三方插件拿不到 `process` 能力（仅 builtin/verified），**本来就无法自己拉起
   stdio MCP**——绕开 `mcp-client` 去连 MCP 在能力位上就不成立。

它补的是 MCP 做不到的两件事：把画布状态**沉淀成线程级缓存**，以及把状态**渲染进对话**。

## 2. 接入 adago_design MCP（先做这步）

本插件只读画布，真正"生成设计"的是 MCP。MCP 走官方 `mcp-client` 插件接入，
**不需要、也不能**由本插件拉起：

1. **登记 server**（工作区 `.adago/mcp.json`，随项目走）：

   ```json
   {
     "version": 1,
     "servers": [
       {
         "id": "adago_design",
         "transport": "stdio",
         "command": "E:////codes////go_projects////ada_go////mcp_projects////adago_design////bin////adago-design.exe",
         "args": ["mcp"],
         "enabled": true,
         "timeoutMs": 60000
       }
     ]
   }
   ```

   `id` 必须是 `^[a-z][a-z0-9_]{0,15}$`（`adago_design` 是 12 位，合法）。
   stdio 由 `mcp-client` 用 `ctx.process` 拉起——该能力只对 builtin/verified 开放，
   所以只有官方 `mcp-client` 能做这件事。

2. **启用 `mcp-client`**（默认随包启用）并**重载插件**。工具列表是**装配期结算**的：
   改完 `mcp.json` 必须重载，运行期不会热加工具。

3. **确认工具名**。命名规则是 `mcp_<serverId>_<toolSlug>`，于是：

   | MCP 原始工具 | 模型看到的工具名 |
   | --- | --- |
   | `design_artboard` | `mcp_adago_design_design_artboard` |
   | `design_preview` | `mcp_adago_design_design_preview` |
   | `design_icons` | `mcp_adago_design_design_icons` |
   | `canvas_add` | `mcp_adago_design_canvas_add` |
   | `canvas_screenshot` | `mcp_adago_design_canvas_screenshot` |

   想缩短前缀可以在 server 上配 `toolPrefix`（如 `design`）→ `design_design_preview`；
   代价是可读性下降，且改前缀等于改模型看到的全部工具名。

4. **让画布活起来**。MCP 的 `mcp` 形态会在首次调用画布类工具时拉起内嵌 HTTP 服务，
   端口默认 8788（占用则 +1）。`design_canvas_sync` 读的正是这个地址，
   拿不准时先调 MCP 的 `canvas_url` 工具，再把真实端口写进 `[plugins.settings.design-canvas]`。

> 审批：MCP 工具默认按 `access=write/network` 走 `policy-default` 的询问；
> 本插件两个工具是 `read`，不弹审批。**这是刻意的分层**——高频的"看一眼状态"
> 不该每次打断用户，真正会改画布的动作留在 MCP 侧受审批约束。

## 3. 安装

插件根目录：全局 `~/.adago/plugins/<id>/`（跨工作区共享），目录名必须等于 `id`。

```powershell
# 方式一：打包后走 CLI（推荐分发用）
Compress-Archive -Path plugins\design-canvas\* -DestinationPath design-canvas.zip
.\bin\adag.exe plugin install --zip design-canvas.zip

# 方式二：本地开发，直接拷目录
Copy-Item -Recurse plugins\design-canvas "$env:USERPROFILE\.adago\plugins\design-canvas"
```

然后在插件页点「重载」，并把 `design-canvas` 启用（默认未启用时它不会进工具目录）。

## 4. 配置

非敏感默认值可放 `config.toml`：

```toml
[plugins.settings.design-canvas]
canvas_url = "http://127.0.0.1:8788"
```

`canvas_url` 缺省即 `http://127.0.0.1:8788`（adago_design 的默认端口；它被占用时会 +1 探测，
所以端口变了就要在这里改，或让 MCP 用 `canvas_url` 工具把真实地址告诉你）。

> 本插件没有 `secrets`：画布是回环无鉴权服务，不需要 token。

## 5. 工具

| 工具 | access | 入参 | 行为 |
| --- | --- | --- | --- |
| `design_canvas_sync` | `read` | `include_zero?`（兼容占位） | `GET /api/nodes` → 生成台账 → 写入线程 state → 返回可粘贴的动态块 |
| `design_canvas_ledger` | `read` | `refresh?` | 默认读缓存；`refresh=true` 重新读取画布 |

两者都是**只读**：只发 GET，不改画布、不删节点。写操作仍然交给 MCP 的
`canvas_*` 工具（它们有稳定错误码与审批语义）。

失败的返回值是**文本 + `details.error`**，不是异常：

| `details.error` | 含义 |
| --- | --- |
| `unreachable` | 画布 HTTP 未启动 / 端口不对 |
| `capability_missing` | 插件没拿到 `net.fetch`（清单被改坏） |
| `http_error` / `bad_json` | 画布返回异常，或不是 JSON |

不抛异常是刻意的：宿主对「同一插件连续 3 次调用 reject」会自动禁用插件，
一次"没开画布"不值得把插件停掉。

## 6. 动态块 `design-canvas/canvas-ledger`

`design_canvas_sync` / `design_canvas_ledger` 的返回文本里已经带了可直接粘贴的封套：

````markdown
```adag-ui
{
  "renderer": "design-canvas/canvas-ledger",
  "id": "canvas-ledger-1",
  "version": 1,
  "fallback": "设计画布台账：12 个节点（动态块不可用）",
  "data": {
    "title": "设计画布台账",
    "subtitle": "web-1440 ×2 · iphone-15 ×1",
    "total": 3,
    "canvasUrl": "http://127.0.0.1:8788",
    "groups": [{ "label": "element", "value": 1 }, { "label": "apple", "value": 1 }],
    "nodes": [{ "title": "Element 组件", "system": "element", "artboard": "web-1440", "size": "1440×1009" }]
  }
}
```
````

渲染规则：

- 数据契约见 [`schemas/canvas-ledger.json`](schemas/canvas-ledger.json)，
  `additionalProperties: false`，多一个键整块降级为 `fallback`。
- 模板 [`canvas-ledger.tsx`](canvas-ledger.tsx) 只用宿主提供的 `adag-chart__*` 令牌类，
  **不依赖本插件自己的 CSS**（安装型插件的 `styles` 不会被装配，只有
  `plugins/official/` 的页面/样式会进渲染桥）。
- 台账为空（刚清空画布）时模板返回 `null` → 整块跳过，不画空卡。

## 7. 能力与边界

```json
"backend": { "entry": "index.ts", "capabilities": ["net.fetch", "state"] }
```

| 能力 | 用途 | 边界 |
| --- | --- | --- |
| `net.fetch` | 读画布 `GET /api/health`、`GET /api/nodes` | 响应 ≤16MB；host 黑白名单当前未启用，但本插件只访问 `canvas_url`（回环） |
| `state` | 缓存台账（线程级 KV） | 值必须可 JSON 序列化；`get/set` 是 Promise |

没有声明的能力就用不了：本插件不需要 `fs`（不写用户文件）、不需要 `exec`
（第三方本来也没有）、不需要 `net.sse`（不订阅流）。

随包技能：`skills/design-workflow/SKILL.md`（五步流程、命名前缀、失败处理）。

## 8. 自检清单

- [ ] `plugin.json` 的 `id` == 目录名，`^[a-z][a-z0-9-]{2,63}$`
- [ ] 入口是纯 ESM JavaScript：`export default function register(agent)`，无 npm import、无 TS 运行时特性
- [ ] 工具用 `parameters` + `execute`，返回 `{ content: [{ type: "text", text }] }`
- [ ] `access` 与副作用一致：本插件两个工具都只发 GET → `read`
- [ ] 能力只声明实际用到的：`net.fetch` + `state`
- [ ] 所有外部失败都转成文本结果，不抛异常
- [ ] 块 schema `additionalProperties: false`，模板不含 `style`/`on*`/`svg`/`img`
- [ ] README 给出 **renderer id** 与一段 `data` 示例
