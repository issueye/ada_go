# browser-use（官方插件）

把 [browser-use](../../../mcp_projects/browser_use/README.md) 桌面浏览器接入 ada-go：模型可以用工具开标签页、按 ref 点按输入、跑多步脚本、抓页面接口、截图，并在需要时把控制权交还给用户。

配套技能：`skills/browser-use/SKILL.md`（工作流与排错手册，随插件注入技能目录）。

## 怎么连上的（没有新端口、没有新协议）

1. browser-use 在本机 `127.0.0.1` 上跑一个带 Bearer token 的 HTTP API，端口与 token 写在
   `<user-config>/browser-use/runtime.json`（Windows：`%APPDATA%\browser-use\runtime.json`）。
2. 插件用 `fs.read` 读这个文件，用 `net.fetch` 调 `POST /v1/call`；`X-Client-Id` 取
   `adag-<threadId>`，租约与审计日志里能对上是哪个线程在操作。
3. 应用没启动时，插件用 `exec` 执行 `browser-use cli status`：CLI 会以 `--serve` 在后台
   拉起应用并等到健康，之后复用同一条 HTTP 通道。启动失败会返回可操作的错误文本。
4. **慢调用走异步接口（旧版本退回 CLI）**：宿主出网桥把响应头超时固定在 15s，而
   `/v1/call` 是同步接口（响应头在动作跑完后才发）。所以脚本、长时间等待、慢导航走
   `POST /v1/calls` + `GET /v1/calls/{id}` 轮询——启动毫秒级返回、任务与请求解耦、
   `client` 归因仍是 `adag-<thread>`；browser-use 没有异步接口时退回
   `browser-use run` / `browser-use cli call`（CLI 自己 320s 总超时）。
   只读调用遇到 15s 超时也会自动改走这两条；点击/输入这类有副作用的动作**不会**
   自动重放，超时会明确要求先看状态再重试。

页面动作仍然全部经过 browser-use 自己的 URL 策略、租约（默认 120s 空闲释放）、遮罩与
审计日志；本插件只做「模型友好的工具形状 + 结果裁剪 + 启动/慢调用兜底」。

> 走异步接口时，审计日志里的 `client` 仍是 `adag-<thread>`；只有退回 CLI 兜底时才是
> `cli-<pid>`（CLI 不传 `--client` 时的默认值）。

## 安装与启用

插件随仓库发布在 `plugins/official/browser-use/`，发现即「可用的随包插件」。三种启用方式：

- 产品默认（推荐给长期使用）：把 `browser-use` 加进 `products/coding.agent.json` 的
  `plugins`。注意 `adag verify products` 与 `internal/conformance` 里冻结了 coding 产品的
  插件数（当前 13），改这里要同步改门禁与文档里的数字。
- 本工作区单独启用（不改产品声明）：在工作区 `.adago/plugins.json` 里写

  ```json
  { "version": 1, "plugins": { "browser-use": { "enabled": true } } }
  ```

- 全局启用（跨工作区）：在 `~/.adago/plugins.json` 里写同样的条目。

启用后重载插件（插件页「重载」或重启 ada-go），工具才会进入工具目录。

## 配置

| 方式 | 键 | 说明 |
| --- | --- | --- |
| 环境变量 | `BROWSER_USE_EXE` | `browser-use` 可执行文件的绝对路径；应用没运行时插件用它自动启动 |
| 环境变量 | `BROWSER_USE_APP_NAME` | 隔离实例（默认 `browser-use`）；指向 dev 实例时，`runtime.json` 目录也随之变化 |
| 环境变量 | `BROWSER_USE_START` | 启动模式：`http`（默认，后台 `--serve`，不动用户会话）/ `attach`（只连接、不自动启动） |
| 环境变量 | `BROWSER_USE_PORT` | 启动时指定的控制端口；0 或省略表示自动分配 |
| 插件设置 | `exe` / `path` | 与 `BROWSER_USE_EXE` 等价，经 `ctx.settings` 透传（宿主注入设置时才有） |
| 插件设置 | `start` / `port` | 与 `BROWSER_USE_START` / `BROWSER_USE_PORT` 等价 |

没配也能用：插件依次尝试设置 → `BROWSER_USE_EXE` → 仓库内开发构建
`mcp_projects/browser_use/bin/browser-use.exe` → `../browser_use/bin/browser-use.exe` →
`PATH` 上的 `browser-use`。**应用已经在跑时，只需要 runtime.json 可读，不需要可执行文件路径。**

### 进程检测与启动

1. **权威判据**是 `runtime.json` + `GET /v1/health`：API 能应答就算在运行，`browser_status`
   直接复用，不会起第二个窗口。
2. API 不通时再看进程表（Windows `tasklist` / Unix `ps`）：如果 **进程还活着但 API 无响应**
   （runtime.json 过期、窗口卡住），插件**不会**再启动一个——单实例锁会让新进程立刻退出，
   盲目重启只会白等 10s；它会返回「请关闭那个窗口后重试」。
3. 没有活着的进程时按启动模式启动：`http`（默认）执行 `browser-use cli start [--port N]`，
   以 `--serve` 起后台实例并等到健康；`attach` 只报错不启动。
4. `browser_start` 可显式触发（`mode`、`port` 可覆盖配置），并把 pid/端口/版本回给模型。

## 工具

| 工具 | access | 作用 |
| --- | --- | --- |
| `browser_status` | read | 启动模式/进程、控制权、活动标签页、全部标签页、下载目录；模式是 http 时顺带启动 |
| `browser_start` | exec | 检测进程并启动（默认 HTTP 模式）；已在运行则复用，不重复启动 |
| `browser_navigate` | write | 打开网址 / 前进 / 后退 / 刷新 / 停止；没有标签页时自动新建 |
| `browser_tab` | write | 新建、切换、关闭标签页（`force` 才关用户标签页） |
| `browser_snapshot` | read | 带 ref 的语义快照（点击/输入的定位依据） |
| `browser_read` | read | 页面或元素的可读正文 |
| `browser_act` | write | click / dblclick / type / press / select / hover / scroll / wait |
| `browser_screenshot` | read | 截图（默认叠加 ref 标注并把图片返回给模型） |
| `browser_eval` | exec | 在页面里执行 JS 表达式 |
| `browser_script` | exec | 一段 JS/TS 异步函数体跑多步流程（一个调用代替十几次动作） |
| `browser_network` | read | 查看 fetch/XHR 抓包（URL、状态、请求/响应体） |
| `browser_console` | read | 页面控制台：console.log/info/warn/error/debug、未捕获异常、未处理的 Promise 拒绝 |
| `browser_control` | write | 释放租约（收起遮罩）或查询控制状态 |
| `browser_call` | write | 长尾工具透传：`cookies` / `files` / `frames` / `downloads` / `dialogs` |

`browser_call` 故意只开放这五个工具：`eval` 与 `script` 必须走 `browser_eval` /
`browser_script`，否则可以用 `write` 级的透传绕过 `exec` 级的审批。

页面 dialog 是同步调用，不能事后逐条点；先用
`browser_call {tool:"dialogs", args:{action:"accept", text:"yes"}}` 设策略，再触发会弹框的动作。
`dismiss` 会让 `confirm()` 返回 false、`prompt()` 返回 null；记录里带实际 `answer`。

### 快照与交互

- 快照除了语义元素，也会收录 `cursor:pointer` 的 div/span 控件（role 显示为
  `clickable`），所以 React/Vue 里没有 role/tabindex 的卡片、菜单项也能按 ref 或
  `text=...` 定位；文本定位会优先精确匹配、可见、在视口内、层级更深的元素。
- `click` / `type` / `select` 默认最多等 5 秒可操作性（出现、可见、可用、不被遮挡），
  不再需要先 `wait` 再动作；`force:true` 仍会跳过遮挡检查。
- `browser_act {action:"wait", for:"tab"|"control"|"dialog"}` 可以等新标签页/弹窗、
  用户接管/释放、页面 dialog，而不是轮询 `browser_status`。
- `browser_snapshot {diff:true}` 只返回相对上一次快照的 `+` 新增、`~` 变化、`-` 消失节点，
  多步流程里能显著降低 token；第一次 diff 没有基线时会退化为全量。
- SPA 的 `pushState/replaceState/hash` 变化会更新页面运行时的 nav 计数，
  `wait {for:"url"}` 直接读页面 `location.href`，不再依赖 Go 侧缓存。

### 截图

`browser_screenshot` 支持 `ref` / `selector` 只截某个元素，以及 `format:"jpeg"` +
`quality` 减小图片体积；`inline:false` 只返回文件路径。元素截图会被裁剪到当前视口，
`details.clip` 给出实际区域；超出视口时会有 warning。

### 控制台（`browser_console`）

页面运行时在 document start 注入，先于页面脚本包住 `console.log/info/warn/error/debug`，
并监听 `error` 与 `unhandledrejection`——所以 SPA「点了没反应、数据没渲染」时，报错通常
已经在缓冲里。每条记录带 id、时间、级别、正文、来源（`console` / `exception` /
`rejection`）、文档 URL 与源码位置。

- `list`（缺省）/ `get` / `clear` / `wait`；`level`、`text_contains`、`source`、`since`、
  `limit` 过滤，`wait` 等到匹配行出现或等待上限（缺省 5s，最多 60s）。`wait` 在页面里
  等（一个挂起 promise，不轮询），页面定时器/用户点击产生的日志照样会命中；等待期间该
  标签页的动作槽被占用，所以先触发动作再 `wait`（5 秒内的日志直接命中）。
- 缓冲按文档隔离：整页导航会清空，hash 路由保留；上限 500 条 / 512KB 总量 / 单条 8KB
  （截断会标记）。
- 日志对象里的 `token` / `secret` / `password` / `cookie` / `session` 字段与
  `?token=` 这类查询串会脱敏。
- 与 `browser_script` 回执里的「控制台」不同：那是脚本自身的 `console.log`，这里读的是
  **网页自己**的控制台。`wait` 超时是正常空结果（`matched: false`），不是错误。

### 结果形状

给模型的是文本（快照正文、回执摘要、接口列表），结构化数据只进 `details`（界面用）：
每次都带当前 URL/标题，`browser_act` 的回执会说明点到了哪个 `role "name" [ref]`。
`browser_screenshot` 在 `inline=true`（默认）时额外返回一个 `image/png` 部件。

工具层对模型可见文本有 24000 字符的展示上限。超过时文本末尾会出现
`…（工具层截断：原始 N 字符，展示上限 24000 字符…）`，同时 `details.truncated=true`；
这时不要把结果当成完整内容，改用更小的 `filter`/`scope`/`limit` 或分批读取。
注意它和工具自己的 `max_chars` 截断是两回事：后者会在正文里写“已按 max_chars 截断”。

## 审批与边界

- `read` 工具（状态、快照、正文、截图、抓包、控制台）不受审批策略影响。
- `write` / `exec` 工具按 `policy-default` 的当前模式走：`ask` 每次确认、`auto` 自动放行写、
  `readonly` 全部拒绝。`parallelTools=1`，同一线程内浏览器动作是串行的。
- 浏览器租约 120s 空闲自动释放；用户点 `Take over` 后写操作返回 `control_locked`，
  读操作照常。任务结束或交接时调用 `browser_control {action:"release"}`。
- 单次工具预算 120s（产品 `policies.toolTimeoutMs`）：`browser_script` 默认 60s、
  上限 110s；超过 110s 的脚本会被工具预算先掐断。
- **15s 出网桥上限**：`ctx.fetch` 的响应头超时是 15s（宿主 `ResponseHeaderTimeout =
  ProviderConnectTimeout`）。插件据此把「注定慢」的调用交给 `/v1/calls` 异步接口（旧版本
  退回 CLI），并在只读调用超时时自动重放一次；有副作用的动作超时不会重放，避免双击/重复提交。
- **并发**：browser-use 的异步任务并发上限是 4，超出返回 429；插件会把「正在执行其它任务」
  作为可重试错误交给模型。

## 验证

```powershell
# 装配门禁：官方插件全部能从磁盘加载（含本插件的清单/入口/dist）
go test ./app ./internal/cli

# 发现层看到它（随包插件，未启用时状态是 unused）
go run ./cmd/adag plugin list | Select-String browser-use

# 真实调用（会拉起 browser-use 窗口）：启用插件后在会话里调 browser_status
```

`dist/index.js` 是 `build.mjs` 的产物（`index.ts` 不含类型注解，strip 后与原文逐字相同）。
改了 `index.ts` 后重新生成：

```powershell
node plugins/official/build.mjs
```
