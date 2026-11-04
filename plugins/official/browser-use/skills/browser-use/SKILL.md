---
name: browser-use
description: 用 browser-use 桌面浏览器完成网页任务：打开页面、按快照 ref 点击输入、填表登录、抓取列表/表格、抓接口 JSON、截图核对，以及多步流程脚本。任务涉及"打开某个网站""点按钮/填表""把页面数据抓下来""看看页面长什么样""复现一个网页 bug"时读它，按里面的黄金循环与错误码表操作。
---

# browser-use 浏览器

> 一句话：这是**用户桌面上真实的浏览器窗口**，不是无头沙箱。先用快照拿 ref，再按 ref 动作；
> 要数据先看接口，要判断外观先截图；任务结束把租约交还。

## 0. 什么时候用

**适合**：登录后抓数据、填表/提交、点菜单翻页、SPA 里找后端接口、截图核对页面、跑一段多步网页流程、
复现与排查网页问题、把网页结果整理成结构化数据。

**不适合**：纯 HTTP 抓取（用 `curl`/`ctx.fetch` 更快）、要求完全无头且不能弹窗的场景、
需要遍历嵌套 iframe 深层结构的场景（当前只支持直接 iframe）、
需要绕过网站自身权限/人机验证的操作。

## 1. 心智模型（先读这一段，能省一半试错）

- **租约**：agent 每次读/写页面都会拿到当前标签页的租约，默认 **120s 空闲自动释放**。
  租约在时工具栏与页面被遮罩覆盖（页面动作被拦截，`Take over` 与标签页仍可点）。
- **用户接管**：用户点 `Take over` 后状态变成 `user`：**读操作仍可用，写操作返回 `control_locked`**。
  这时不要硬试——请用户点状态栏的 `Resume agent`。
- **一个窗口多个标签页**：`browser_status` 能看到全部标签页；agent 新建的标签页可以直接关，
  用户自己的标签页要 `force: true` 才关得掉（`protected_tab` 就是提醒你这一点）。
- **ref 是页面内的坐标**：`e12` 只在当前文档有效；导航、重绘、局部刷新后可能失效，重新快照即可。
- **窗口会出现在用户眼前**：这是设计如此（用户能看见、能随时接手），除非用户明确要求，
  不要长时间占着遮罩不放。
- **启动与进程**：插件默认 `http` 模式——没在跑就用 `browser-use cli start` 起一个后台
  `--serve` 实例（不动用户会话：不恢复、不保存标签页）；`attach` 模式只连接、不启动。
  进程还活着但控制 API 无响应时（窗口卡住/`runtime.json` 过期），插件**不会**再启动一个，
  而是让你先处理那个窗口。想看明确结论就调 `browser_start`（显式检测并启动）。

## 2. 黄金循环

1. `browser_status` —— 确认启动模式、进程与端口、控制权在 agent、活动标签页是哪一个
   （默认 http 模式下，首次调用会顺带启动浏览器；需要显式启动用 `browser_start`）。
2. `browser_navigate {url}` —— 打开目标页面；已有标签页时按需 `browser_tab {action:"new", url}`（带 url 时会等加载完再返回）。
3. `browser_snapshot` —— 拿 ref 清单；页面长就 `filter:"button"` 或 `scope:"viewport"` 收窄。
4. `browser_act {action:"click", ref:"e12"}` / `{action:"type", ref:"e13", value:"…", submit:true}`。
5. 需要确认结果：再 `browser_snapshot`（结构变了）或 `browser_read`（要正文）或
   `browser_screenshot`（要看外观）。
6. 收尾：`browser_control {action:"release"}`（交还控制权），必要时关掉自己开的标签页。

> 不要凭记忆猜 ref，也不要跨快照复用 ref；快照很便宜，猜错一次更贵。

## 3. 工具速查

| 工具 | access | 关键参数 | 用途 |
| --- | --- | --- | --- |
| `browser_status` | read | – | 启动模式/进程、控制权、标签页、下载目录 |
| `browser_start` | exec | `mode`（http/attach）、`port` | 检测并启动浏览器（默认 HTTP 模式） |
| `browser_navigate` | write | `url`、`action`（goto/back/forward/reload/stop）、`wait_until` | 打开/前进/刷新 |
| `browser_tab` | write | `action`（new/select/close）、`url`、`tab`、`force` | 标签页管理 |
| `browser_snapshot` | read | `scope`、`filter`、`max_chars` | 拿 ref |
| `browser_read` | read | `selector`、`max_chars` | 读正文 |
| `browser_act` | write | `action`（click/dblclick/type/press/select/hover/scroll/wait） | 交互 |
| `browser_screenshot` | read | `annotate`、`max_width`、`inline` | 看外观 |
| `browser_eval` | exec | `expression` | 读一个值/简单 DOM |
| `browser_script` | exec | `source`、`args`、`timeout_ms` | 多步流程 |
| `browser_network` | read | `action`（list/get/clear/wait）、`url_contains`、`include_body` | 抓接口 |
| `browser_console` | read | `action`（list/get/clear/wait）、`level`、`text_contains`、`source` | 看页面控制台与报错 |
| `browser_control` | write | `action`（release/state） | 交还控制权 |
| `browser_call` | write | `tool`（cookies/files/frames/downloads/dialogs） | 长尾 |

`write`/`exec` 工具按产品审批策略执行（`ask` 每次确认、`readonly` 全拒）。被拒不是 bug，
要么换只读路径（快照/正文/抓包），要么请用户放行或调策略。

## 4. 读快照

```
snapshot v7  url=https://news.ycombinator.com/  title=Hacker News
viewport 1280x800  scroll=(0,0)  page=1280x2400  dpr=1  refs=42  nodes=512
[e1] link "Hacker News" href=https://news.ycombinator.com/
[e12] textbox "Search"
[e15] button "Search"
[e20] link "webview" href=https://github.com/…
```

- 一行 = 一个可交互元素：`[ref] role "可见名"`，可选 `value=` / `href=` / `checked` / `disabled` / `offscreen`。
- `role=clickable` 是 `cursor:pointer` 的非语义控件（React/Vue 卡片、菜单项）；它和 button/link 一样可以按 ref 或 `text=` 点。
- `offscreen` 表示在视口外；`browser_act` 会自己滚动到位，不用手动滚。
- `truncated` 为真时说明被 `max_chars` 截断：用 `filter`（role/名字/标签）或 `scope:"viewport"` 收窄。
- 文本末尾出现「工具层截断：原始 N 字符」是插件的 24000 字符展示上限到了，`details.truncated`
  也会为 true；不要把它当成完整页面，改用更小的 `filter`/`scope`/`max_chars` 或分批读取。
- 页面有局部刷新、或者只是想知道"刚才点击后变了什么"时用 `diff:true`：只返回
  `+` 新增 / `~` 变化 / `-` 消失的节点；第一次 diff 没有基线，会先给全量。
- 找不到目标时换定位方式：`selector:"#submit"`、`selector:"text=提交"`、`selector:"role=button[name=提交]"`，
  或 `click {text:"提交"}`（按可见文本）。

## 5. 交互配方

```jsonc
// 点击（默认会自动等待导航/加载）
{ "action": "click", "ref": "e15" }
{ "action": "click", "selector": "text=下一页" }
{ "action": "click", "text": "提交", "force": true }        // 被遮挡/动画中
{ "action": "click", "ref": "e9", "count": 2 }               // 双击等价于 action=dblclick

// 输入：value 是要写的文本；submit 相当于按 Enter
{ "action": "type", "ref": "e12", "value": "webview", "submit": true }
{ "action": "type", "selector": "#email", "value": "a@b.c", "clear": false } // 追加而不是覆盖

// 按键：组合键写 Control+A / Shift+Tab
{ "action": "press", "key": "Enter" }
{ "action": "press", "key": "Escape", "ref": "e20" }

// 下拉：值是 option 的 value 或可见文本
{ "action": "select", "ref": "e30", "values": ["2026-11"] }

// 滚动：direction + amount；也可以滚到某个元素
{ "action": "scroll", "direction": "down", "amount": 800 }
{ "action": "scroll", "ref": "e44" }

// 悬停：悬停后通常要重新快照，因为菜单是新出现的元素
{ "action": "hover", "text": "更多" }

// 等待：优先等具体信号，不要盲等
{ "action": "wait", "for": "url", "value": "/results" }
{ "action": "wait", "for": "selector", "value": "text=加载完成" }
{ "action": "wait", "for": "download" }        // 返回文件路径
{ "action": "wait", "for": "tab" }             // 等新标签页/弹窗
{ "action": "wait", "for": "control" }         // 等用户接管/释放
{ "action": "wait", "for": "dialog" }          // 等页面 alert/confirm/prompt
{ "action": "wait", "for": "load", "timeout_ms": 15000 }
```

`click` / `type` / `select` 默认最多等 5 秒元素可操作（出现、可见、可用、不被遮挡），
所以普通异步渲染不需要先手写 `wait`；等不到再检查快照或加 `force`。

**经验**：提交表单后先用 `wait {for:"url"}` 或 `wait {for:"selector"}`，再快照；
SPA 的局部刷新经常不改 URL，要等文本或 `browser_network {action:"wait"}`。

## 6. 多步流程：`browser_script`

一个脚本代替十几次工具调用，中间不用来回传快照。脚本是 **async 函数体**：

```js
const page = await browser.active();
await page.goto("https://news.ycombinator.com");
const snap = await page.snapshot();
const search = snap.match(/\[(e\d+)\] textbox "Search"/)?.[1];
await page.fill(search, args.q, { submit: true });
await page.waitForURL("/from");
return await page.evaluate(() =>
  [...document.querySelectorAll("tr.athing")].slice(0, 5).map((tr) => ({
    title: tr.querySelector(".titleline")?.textContent?.trim(),
    href: tr.querySelector("a")?.href,
  }))
);
```

可用面（省略参数的默认都合理）：

- `browser.active()` / `browser.page(id)` / `browser.newPage(url)` / `browser.tabs()` / `browser.status()`
- `page.goto/back/forward/reload/stop`、`page.snapshot(opts)`（返回快照文本）、`page.read()`
- `page.click(t, opts)` / `dblclick` / `fill(t, text)`（覆盖）/ `type(t, text)`（追加）/
  `press(key)` / `select(t, values)` / `hover(t)` / `scroll(o)`
- `page.waitForSelector(sel)` / `waitForURL(url)` / `waitForText(text)` / `waitForLoad()` / `waitForDownload()`
- `page.evaluate(fn, arg)`（在页面里跑，返回 JSON）、`page.requests(opts)` / `request(id)` /
  `waitForRequest(opts)` / `clearRequests()`
- `page.screenshot({inline:true})`、`page.cookies(opts)`、`page.frames()`、`page.useFrame(id)` / `mainFrame()`、
  `page.upload(t, paths)`、`page.info()/url()/title()`、`page.close()`
- 全局 `console.log`（回传控制台输出）、`sleep(ms)`、`args`（工具参数里的 `args` 对象）

`t` 可以是 `e12` / `ref=e12` / CSS 选择器 / `text=…` / `role=button[name=…]`。

**限制**：默认 60s（`timeout_ms` 可调，工具层上限 110s）、200 次浏览器动作、64MB 堆、
32KB 控制台、64KB 返回值、256KB 源码；**没有** `fetch`/`fs`/`import`/`eval`/`Function`；
同一时刻只能跑一个脚本（`script_busy`）。脚本的每个动作照样走租约、遮罩与审计日志。

**长脚本不用担心 15s**：脚本、长等待、慢导航由插件交给 browser-use 的异步任务接口
（`/v1/calls`，启动立即返回、后台跑完再取结果），可以用满 `timeout_ms`。但**不要写没有
上限的轮询**：每次 `evaluate` 都有开销，轮询间隔 ≥300ms、总时长 <20s 最稳；等页面就绪
优先用 `waitForURL` / `waitForText`，而不是自己 `while` 空转。

> 背景：ada-go 的出网桥把响应头超时固定在 15s，而 browser-use 的 `/v1/call` 要等动作跑完
> 才发响应头。所以脚本/长等待/慢导航走异步接口（旧版本 browser-use 退回 CLI）；
> `click`/`type` 这类有副作用的动作超时后**不会**自动重放，遇到 15s 超时要先
> `browser_status` 看状态再决定是否重试。异步任务并发上限 4，撞上了会提示"正在执行其它任务"，
> 等前一个跑完再重试即可。

**什么时候别用脚本**：只有一两步时直接调 `browser_act` 更容易排错（脚本里的错误只给行号与
`script_error`）。

## 7. 抓接口（SPA 优先走这里）

页面数据 90% 来自某次 `fetch`/`XHR`，抓接口比解析 DOM 稳得多。

1. `browser_network {action:"clear"}` 清空旧记录。
2. 点触发按钮（或 `browser_act {action:"click"}`），再
   `browser_network {action:"wait", url_contains:"/api/", timeout_ms:8000}`。
3. `browser_network {action:"get", id:"n16", max_body:20000}` 拿请求/响应体，
   或先 `list` 看 URL/状态定位目标。
4. 需要的话在 `browser_script` 里 `page.request("n16")` 取结构化 JSON。

**边界**：抓包按文档隔离（整页导航会清空，hash 路由保留）；只记录页面脚本发起的
fetch/XHR（不记录文档本身、WebSocket、SSE 体）；请求体 ≤16KB、响应体 ≤128KB、
最多 300 条 / 4MB；`password/token/secret/cookie/session` 字段与头已脱敏。
接口需要登录态时，先在页面里登录（或复用用户已登录的会话），抓包会带上 cookie。

## 7.5 看控制台（点了没反应、数据没渲染）

接口返回 200 但页面不动，多半是前端报错。控制台工具比猜 DOM 快：

1. `browser_console {action:"clear"}` 清掉旧日志。
2. 触发动作（`browser_act`），再 `browser_console {action:"wait", level:"error", timeout_ms:5000}`
   等报错出现；不想等就直接 `list`。
3. `browser_console {level:"error"}` / `{source:"exception"}` 过滤未捕获异常，
   `{text_contains:"xxx"}` 按关键字找。
4. 拿到 URL:行号后，`browser_eval` 看对应 DOM/状态，或用 `browser_screenshot` 核对渲染。

**边界**：只记录**当前文档、运行时注入之后**的日志（整页刷新清空，hash 路由保留）；
500 条 / 512KB / 单条 8KB；对象里的 `token/secret/password/cookie/session` 字段与
`?token=` 查询串已脱敏。`wait` 超时返回 `matched:false` 的空结果，不是错误。
`wait` 在页面里挂起，页面定时器/用户点击产生的日志会命中，但等待期间该标签页的动作槽
被占用——**先触发动作，再 wait**（5 秒内的日志直接命中），不要在 wait 里等另一个工具调用。
这里的"控制台"是**网页自己**的 console，不是 `browser_script` 回执里那段脚本 console。

## 8. 截图与视觉确认

- `browser_screenshot {}` 默认叠加 ref 标注并把图片直接返回给你；需要看清细节时加
  `max_width: 1600`，只要文件路径时 `inline: false`。
- 只截某个元素用 `ref` 或 `selector`（图片会裁剪到视口内的部分）；图片太大时用
  `format:"jpeg"` + `quality:80` 可明显减小体积。
- 截图里的 ref 标签与 `browser_snapshot` 的 ref 一致，可以"看图找元素 → 按 ref 点击"。
- 截图文件也保存在 `%APPDATA%\browser-use\screenshots\`（状态里能看到下载目录）。
- 判断"布局是否错位/样式是否生效/给用户看结果"用截图；判断"功能是否可用"用动作 + 快照。

## 9. 长尾工具（`browser_call`）

```jsonc
// 登录态：列 / 设 / 删 / 导入导出（值是敏感信息，审计日志里已脱敏）
{ "tool": "cookies", "args": { "action": "list", "url": "https://example.com/" } }
{ "tool": "cookies", "args": { "action": "set", "name": "session", "value": "…", "domain": "example.com", "path": "/" } }
{ "tool": "cookies", "args": { "action": "import", "cookies": [ … ] } }

// 上传：目标可以是 file input 的 ref，也可以是旁边的上传按钮
{ "tool": "files", "args": { "action": "upload", "ref": "e31", "paths": ["%USERPROFILE%\\Desktop\\report.pdf"] } }

// iframe：先 list 再 use，动作作用在 frame 内；处理完回 main
{ "tool": "frames", "args": { "action": "list" } }
{ "tool": "frames", "args": { "action": "use", "frame": "f2" } }
{ "tool": "frames", "args": { "action": "main" } }

// 下载与弹框
{ "tool": "downloads", "args": {} }
{ "tool": "dialogs", "args": {} }
{ "tool": "dialogs", "args": { "action": "accept", "text": "yes" } }
{ "tool": "dialogs", "args": { "action": "dismiss" } }
```

- 上传单次 ≤20MiB；`files` 只支持 `upload`。默认只允许 `security.upload_dirs`
  （Desktop/Documents/Downloads）下的路径，越界会报 `blocked_action`。
- 弹框（`alert`/`confirm`/`prompt`）由浏览器拦截、非阻塞。页面里的 dialog 是同步调用，
  不能事后逐条点：先在**触发动作之前**用 `dialogs {action:"accept"|"dismiss"}` 设策略
  （`accept` 可带 `text` 作为 prompt 的返回值），再用 `dialogs {action:"list"}` 看
  `answer`；`wait {for:"dialog"}` 可以等弹框发生。
- 只支持**直接 iframe**（任意来源），嵌套 iframe 不遍历；`frames use` 之后记得 `main`。

## 10. 错误码 → 下一步

| code | 含义 | 怎么办 |
| --- | --- | --- |
| `stale_ref` | ref 已不在页面里 | 重新 `browser_snapshot` 取新 ref |
| `not_found` | 选择器/文本/角色没匹配到 | 换 selector、`text=`、`role=…[name=…]`；确认元素在 right frame |
| `ambiguous` | 匹配到多个 | 用 ref，或更具体的选择器 |
| `not_visible` / `covered` | 元素不可见/被遮挡 | 先滚动到它、`hover` 展开、必要时 `force:true` |
| `not_enabled` | 元素被禁用 | 先满足前置条件（勾选、填完必填项） |
| `timeout` | 动作/等待超时 | 加 `timeout_ms`；换更可靠的等待信号；确认网络请求真的发出 |
| `nav_failed` / `blocked_url` | 导航失败/被 URL 策略拦 | 检查地址与协议（只允许 http/https） |
| `control_locked` | 用户已接管 | 请用户点 `Resume agent`，不要重试 |
| `protected_tab` | 想关用户标签页 | 加 `force:true`（先想清楚是否该关） |
| `too_many_tabs` / `no_tab` / `invalid_tab` | 标签页数量或 id 问题 | `browser_status` 看现状，`browser_tab` 调整 |
| `page_unresponsive` | 页面卡死 | `browser_navigate {action:"reload"}`，或换标签页重开 |
| `bad_request` | 参数形状不对 | 看错误里的 hint；ref 与 selector 不能同时给 |
| `script_syntax` / `script_error` / `script_timeout` | 脚本自身问题 | 修脚本；先缩小到最小可复现片段 |
| `script_busy` | 已有脚本在跑 | 等它结束，别并发 |
| `script_output_limit` / `script_result_too_large` | 输出/返回值超限 | 只 `return` 需要的字段；用 `console.log` 会占控制台预算 |

## 11. 收尾与交接

- 任务完成：`browser_control {action:"release"}` —— 收起遮罩，把浏览器还给用户。
- 自己开的标签页用完就关；**不要**关用户的标签页（除非用户要求）。
- 需要用户登录/扫码/接验证码时：先 `release`，请用户操作，再继续。
- 用户在操作浏览器时不要抢：`control_locked` 就等，不要循环重试。

## 12. 排错

- **工具报"15s 内没有返回"**：动作已经发出但 ada-go 出网桥的响应头超时（15s）到了。
  **先别重试**（点击/输入可能已经生效），用 `browser_status` + `browser_snapshot` 看当前状态，
  再决定下一步。脚本、`wait`、慢导航已经自动走 `/v1/calls` 异步接口（旧版本退回 CLI），
  不会撞这个上限。
- **工具报"正在执行其它任务"**：browser-use 的异步任务并发上限是 4，等前面的任务结束再重试。
- **工具报"无法使用 browser-use"**：应用没起来且自动启动失败。让用户手动打开 browser-use，
  或在启动 ada-go 前设置 `BROWSER_USE_EXE=<browser-use.exe 绝对路径>`。
  应用已经在跑时不需要这个变量（插件只读 `runtime.json`）。
- **工具报"启动模式 attach：只连接、不自动启动"**：插件被配置成 attach。让用户打开
  browser-use，或调 `browser_start {mode:"http"}` 显式启动（也可以用 `BROWSER_USE_START=http`
  重启 ada-go 改成默认自动启动）。
- **工具报"检测到 browser-use 进程（pid …），但控制 API 无响应"**：窗口卡住或
  `runtime.json` 过期。请用户关闭那个窗口后重试；**不要**反复调 `browser_start`
  （单实例锁会让新进程立刻退出）。
- **状态里全是 `idle` 但没有标签页**：`browser_navigate {url}` 会自动新建一个。
- **`browser_screenshot` 不返回图片**：`inline` 被设成 false，或模型/产品不支持图片
  （`capabilities.images=false`）。
- **看应用侧到底发生了什么**：`%APPDATA%\browser-use\audit\` 每个工具调用一行 JSONL，
  状态栏 `Activity` 面板是同一份数据的最近 100 条。
- **想用 dev/隔离实例**：设 `BROWSER_USE_APP_NAME=browser-use-dev` 再启动应用，
  插件读该名字下的 `runtime.json`，不会碰用户的会话。

## 13. 反模式

- 不看快照就猜 ref；跨页面复用 ref。
- 用 `browser_read` 抓结构化数据（用 `browser_network` 或 `browser_eval`）。
- 用 `browser_script` 只做一次点击（排错更难，审批照旧）。
- 在 `ask` 模式下一次点击一次确认地硬跑长流程——改用脚本，或先和用户讨论策略。
- 用户接管后反复重试写操作。
- 任务结束不 `release`，让遮罩一直挡着用户的界面。
- 在脚本里 `console.log` 整个响应体（撞 32KB 控制台上限），或 `return` 整页 HTML。
