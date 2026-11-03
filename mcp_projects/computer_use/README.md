# Computer Use · 蒙板

给 AI 代理使用的 Windows 桌面操作程序。它把「代理正在看什么、要点哪里」画成一层
**蒙板（mask）** 覆盖在桌面上：变暗无关区域、框出目标元素并写出它的坐标与尺寸、
在鼠标位置实时显示坐标、按需铺开坐标标尺，并用点击涟漪与状态胶囊汇报动作。

代理通过 HTTP 或 MCP 调用它；蒙板本身**鼠标穿透、永远置顶、且不进入截图**——
代理截到的是干净桌面，人看到的是带标注的那一层。

![蒙板演示](docs/mask-demo.png)

---

## 目录

- [特性](#特性)
- [快速开始](#快速开始)
- [蒙板：它画什么](#蒙板它画什么)
- [坐标系统](#坐标系统)
- [HTTP API](#http-api)
- [MCP](#mcp)
- [JavaScript 与宏](#javascript-与宏)
- [给代理的使用流程](#给代理的使用流程)
- [安全与可控性](#安全与可控性)
- [架构](#架构)
- [依赖的 mygo](#依赖的-mygo)
- [已验证](#已验证)
- [限制与后续](#限制与后续)

## 特性

- **蒙板标注**：暗层 + 聚光洞、元素框、`x,y w×h` 坐标标签、指针坐标跟随、
  坐标标尺网格、点击涟漪、拖拽轨迹、底部状态胶囊；多显示器各自一层。
- **坐标优先**：所有接口与标注都用**物理像素**；截图自带 `region` 与 `scale`，
  一句话换算出可点的坐标。
- **完整输入**：移动、单击/双击/右键/中键、拖拽、滚轮、Unicode 打字、组合键；
  **批量动作**一次调用完成一串步骤，**`--fast`** 省掉给人看的停顿。
- **窗口感知**：列出可见窗口（标题、进程、矩形、前台标记）、聚焦窗口、
  在蒙板里框出窗口。
- **两种接口**：本地 HTTP JSON API（curl/脚本/任意语言）与 MCP stdio
  （Claude Desktop、IDE、代理框架）。
- **内置 JS 引擎（moejs）**：`cu.click/type/mask/...` 绑定进 JavaScript，
  脚本可直接操作电脑；**能把脚本或任意来源的动作录成宏**，再一键回放。
- **截图增强**：按显示器或区域截取、缩放、烧录坐标网格、可选指针标记。
- **可控与安全**：暂停开关（快捷键/托盘/API）、只读模式、令牌、动作审计日志、
  蒙板不进截图、不把自己的窗口列给代理。

![带坐标网格的截图](docs/screenshot-grid.png)

## 快速开始

要求：Windows 10 1809+、Go **1.27.3+**（`mygo` 的版本要求），无需 cgo、无需 Bun。

```powershell
cd E:\codes\go_projects\computer_use

# 依赖同机上的 mygo 仓库（go.mod 里用 replace 指向 ..\mygo）
go build -o bin\computer-use.exe .

# 1. 体检：显示器、截图、指针、组合键、剪贴板
.\bin\computer-use.exe doctor

# 2. 启动：蒙板 + HTTP API + 托盘图标 + 全局快捷键
.\bin\computer-use.exe run

# 3. 打开 http://127.0.0.1:7331 看状态页（屏幕 / 蒙板 / 暂停按钮）

# 4. 演示蒙板（示例标注，N 秒后退出，可另存预览图）
.\bin\computer-use.exe demo --seconds 20 --preview docs\mask-demo.png
```

运行中的快捷键（全局，任何程序在前台都有效）：

| 快捷键 | 作用 |
|---|---|
| `Ctrl+Alt+Shift+P` | 暂停 / 继续注入输入 |
| `Ctrl+Alt+Shift+X` | 清除全部标注 |
| `Ctrl+Alt+Shift+Q` | 退出程序 |

托盘图标提供同样的开关，以及「打开状态页」。

`run` 的常用参数：`--listen 127.0.0.1:7331`、`--token …`、`--no-mask`、
`--no-tray`、`--no-hotkeys`、`--read-only`（只读，不注入输入）、
`--fast`（动作前不给眼睛留停顿，蒙板照常画）、
`--show-mask-in-shots`（调试：让截图包含蒙板）、`--no-log`。

## 蒙板：它画什么

蒙板是每个显示器一个窗口：无边框、背景透明、总在最前、`WS_EX_TOOLWINDOW`
（不进 Alt+Tab 与任务栏）、`WS_EX_NOACTIVATE`（不抢焦点）、
`WS_EX_TRANSPARENT`（鼠标穿透），并用 `WDA_EXCLUDEFROMCAPTURE` 把自身排除在
屏幕截图之外。

| 元素 | 含义 |
|---|---|
| 暗层 | `dim`（0–0.85）把无关区域压暗，视线直接落到聚光洞里的元素 |
| 聚光洞 | 每个标注区域与标记点自动「挖洞」，洞里的画面保持原亮度 |
| 元素框 | 圆角描边 + 四角瞄准括号 + 柔和外发光，虚线表示「仅提示、不操作」 |
| 坐标标签 | 框左上角的胶囊：序号、元素名称、`x,y  w×h`（物理像素） |
| 指针坐标 | 光标处十字线 + 实时坐标胶囊；光标在哪块屏幕就画在哪块 |
| 标尺网格 | `grid=100` 时每 100 物理像素一条线，上/左边缘写出坐标值 |
| 点击涟漪 | 点击落点扩散的双环，左键蓝色、右键琥珀、中键绿色 |
| 拖拽轨迹 | 拖动路径淡出，终点高亮 |
| 状态胶囊 | 屏幕底部中央：运行/暂停指示点 + 最近一次动作（如 `click 960,540`） |

坐标标签的排版是刻意的：名称用无衬线粗体，坐标与尺寸用等宽字体（`Consolas`），
方便人和代理把「元素」与「可点的数字」直接对应起来。

预览给代理核对：`GET /v1/mask.png`（或 MCP 的 `mask_preview`）把蒙板窗口渲染出来，
叠在真实桌面截图之上，得到「用户所见」。截图本身（`/v1/screenshot`）不含蒙板。

## 坐标系统

**一切坐标都是物理像素，原点在主显示器左上角。** 多显示器时，主屏右侧为正、
左侧为负，虚拟桌面因此可能有负坐标。

- `GET /v1/state` 返回 `virtual`（虚拟桌面矩形）与每个显示器的 `bounds`。
- 截图返回 `region`（截到的桌面矩形）与 `scale`（图片像素 / 桌面像素）：

```
screen_x = region.x + image_x / scale
screen_y = region.y + image_y / scale
```

例：全桌面 3840×1080、`max_width=1600` → `region={0,0,3840,1080}`、`scale=0.4167`；
截图里 `(800, 225)` 对应桌面 `(1920, 540)`，直接 `click(1920, 540)`。

`grid=100` 会把坐标烧进图片（如顶部标尺上的 `1200`），配合上面的公式可以快速读点；
局部细节用 `region=x,y,w,h` 原分辨率截取（不缩放）来读小字。

## HTTP API

默认监听 `127.0.0.1:7331`，仅回环地址。设置了 `--token` 时，除状态页外都要带
`Authorization: Bearer <token>`（或 `?token=`）。

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/` | 状态页：屏幕、蒙板预览、暂停/清除按钮 |
| GET | `/v1/health` | 存活与版本 |
| GET | `/v1/state` | 显示器、虚拟桌面、指针、前台窗口、蒙板状态 |
| GET | `/v1/screenshot` | 截图（base64）+ 坐标映射，`format=png/jpeg` |
| GET | `/v1/screenshot.png` | 截图（二进制），映射写在 `X-Screen-*` 响应头 |
| GET | `/v1/windows` | 可见窗口列表 |
| POST | `/v1/actions` | 一次或一批动作 |
| POST | `/v1/mask` | 设置蒙板：区域、标记、文字、网格、暗度、HUD |
| POST | `/v1/mask/clear` | 清除标注 |
| GET | `/v1/mask.png[?display=N]` | 蒙板预览（用户所见） |
| POST | `/v1/pause` / `/v1/resume` | 暂停 / 继续注入 |
| GET/POST | `/v1/clipboard` | 读 / 写剪贴板文本 |

截图参数（`/v1/screenshot*`）：`display=N`、`region`（用 `x&y&w&h`）、
`max_width`、`scale`、`grid`、`cursor=1`、`format=png|jpeg`、`quality`。

### 截图

```powershell
# 带坐标网格、缩放到 1600 宽，返回 base64 与映射
$r = Invoke-RestMethod "http://127.0.0.1:7331/v1/screenshot?max_width=1600&grid=100"
$r.meta | ConvertTo-Json -Depth 4

# 直接拿 PNG（映射在响应头 X-Screen-Region / X-Screen-Scale）
Invoke-WebRequest "http://127.0.0.1:7331/v1/screenshot.png?display=0" -OutFile shot.png

# 读小字：把对话框原样截出来
Invoke-WebRequest "http://127.0.0.1:7331/v1/screenshot.png?x=200&y=160&w=720&h=420" -OutFile crop.png
```

### 动作

```powershell
# 单击 / 双击 / 右键
Invoke-RestMethod -Method Post "http://127.0.0.1:7331/v1/actions" -ContentType application/json `
  -Body '{"type":"click","x":960,"y":540}'
Invoke-RestMethod -Method Post "http://127.0.0.1:7331/v1/actions" -ContentType application/json `
  -Body '{"type":"click","x":960,"y":540,"button":"right"}'

# 拖拽、滚轮、打字、组合键
'{"type":"drag","x":400,"y":300,"x2":900,"y2":300}'
'{"type":"scroll","x":960,"y":540,"dy":3}'
'{"type":"type","text":"你好，蒙板","delay_ms":5}'
'{"type":"key","keys":"ctrl+shift+t"}'

# 一批动作，按顺序执行
'{"actions":[{"type":"click","x":100,"y":200},{"type":"type","text":"abc"},{"type":"key","keys":"enter"}]}'

# 聚焦窗口、在蒙板里框出窗口
'{"type":"focus","title":"记事本"}'
'{"type":"outline","handle":1234567,"append":true}'
```

动作类型：`click`（`button`、`clicks`）、`move`（`steps`）、`drag`、`scroll`
（`dx` 向右、`dy` 向下，单位为滚轮格数）、`type`（`delay_ms`）、`key`（`keys`、`repeat`）、
`wait`（`ms`）、`focus`（`handle`/`title`）、`outline`（`handle`/`title`/`append`）。

### 蒙板

```powershell
$mask = @{
  dim = 0.45; dark = $true; grid = 100; coords = $true; ttl_ms = 15000
  regions = @(
    @{ rect = @{x=200;y=160;w=720;h=420}; label = "搜索框"; index = 1; tone = "accent" }
    @{ rect = @{x=260;y=620;w=300;h=48};  label = "确定";   index = 2; tone = "green" }
  )
  markers = @( @{ at = @{x=410;y=644}; label = "点击这里" } )
  chips   = @( @{ at = @{x=200;y=600}; text = "坐标为物理像素" } )
  hud     = @{ detail = "等待确认"; busy = $true }
} | ConvertTo-Json -Depth 6
Invoke-RestMethod -Method Post "http://127.0.0.1:7331/v1/mask" -ContentType application/json -Body $mask
```

字段：`regions`（`rect`、`label`、`index`、`tone=accent|green|amber|red|white`、
`dashed`）、`markers`（`at`、`label`、`tone`）、`chips`（`at`、`text`）、
`dim`、`dark`、`grid`、`coords`、`visible`、`append`（追加而非替换）、
`clear`（先清空）、`ttl_ms`（过期时间）、`hud`。

## MCP

`computer-use mcp` 在 stdin/stdout 上说 MCP（JSON-RPC 2.0，按行分隔），
把工具调用转发给本机运行中的程序。没运行时加 `--spawn` 会自动拉起 `run`。

```json
{
  "mcpServers": {
    "computer-use": {
      "command": "E:\\codes\\go_projects\\computer_use\\bin\\computer-use.exe",
      "args": ["mcp", "--spawn"]
    }
  }
}
```

工具（26 个）：

| 工具 | 说明 |
|---|---|
| `screen_info` | 显示器、虚拟桌面、指针、前台窗口、蒙板状态 |
| `screenshot` | 截图（图片 + 坐标映射），支持 `display`/`region`/`grid`/`max_width` |
| `batch` | 一次调用按顺序执行多个动作（最快的填写表单方式） |
| `run_script` / `record_start` / `record_stop` | 运行 JS、开始/结束录制宏 |
| `list_macros` / `run_macro` / `macro_script` / `delete_macro` | 宏的列表、回放、导出 JS、删除 |
| `click` / `move` / `drag` / `scroll` | 鼠标动作（物理像素） |
| `type` / `key` | Unicode 打字与组合键 |
| `list_windows` / `focus_window` / `outline_window` | 窗口列表、聚焦、在蒙板中框出 |
| `clipboard` | 读 / 写剪贴板 |
| `mask` / `mask_preview` / `clear_mask` | 设置蒙板、预览蒙板、清除 |
| `pause` / `resume` / `wait` | 暂停、继续、等待 |

`tools/call` 返回内容里，截图是 MCP `image` 内容块 + 一段带映射公式的 `text`；
其余是 JSON 文本。示例（stdin 一行一条消息）：

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mask","arguments":{"regions":[{"rect":{"x":200,"y":160,"w":720,"h":420},"label":"搜索框"}],"grid":100}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"click","arguments":{"x":560,"y":370}}}
```

## JavaScript 与宏

程序内嵌 [moejs](https://github.com/MoeForge/moejs)（纯 Go 的 JS 运行时，无 cgo），
每个脚本拿到一个全局对象 `cu`，动作直接调用引擎（不经 HTTP）：

```js
// examples/mask-demo.js —— 在指针周围画带坐标的框，再清掉
const c = cu.cursor();
cu.mask({
  regions: [{ rect: { x: c.x - 150, y: c.y - 80, w: 300, h: 160 }, label: "脚本标注", index: 1 }],
  markers: [{ at: c, label: "指针" }],
  hud: { detail: "脚本运行中" }
});
cu.click(c.x, c.y);          // 也可以 cu.type / cu.key / cu.drag / cu.scroll
cu.wait(500);
cu.clearMask();
({ cursor: c, windows: cu.windows().length });
```

`cu` 的成员：

| 成员 | 说明 |
|---|---|
| `cu.click(x, y, {button, clicks})` / `cu.dblclick` | 点击（默认左键，clicks 2 为双击） |
| `cu.move(x, y, {steps})` / `cu.drag(x1,y1,x2,y2,{button})` / `cu.scroll(x,y,dx,dy)` | 指针动作 |
| `cu.type(text, {delay_ms})` / `cu.key(keys, {repeat})` | 键盘动作 |
| `cu.wait(ms)` / `cu.sleep(ms)` | 等待（会被记进宏） |
| `cu.focus({handle,title})` / `cu.outline({handle,title,append})` | 聚焦窗口 / 在蒙板上框出窗口 |
| `cu.screenshot({region,display,max_width,grid,fast,format,image})` | 截图，返回 `{width,height,region,scale}`；`image:true` 附带 base64 |
| `cu.state()` / `cu.cursor()` / `cu.windows()` | 读状态、指针、窗口列表 |
| `cu.mask(request)` / `cu.clearMask()` | 蒙板（与 `POST /v1/mask` 同一套字段） |
| `cu.pause()` / `cu.resume()` | 暂停/继续注入 |
| `cu.log(...)`、`console.log(...)` | 写进返回的 `output` |
| `cu.recordStart(name)` / `cu.recordStop()` | 在脚本内录制宏 |
| `cu.macroList()` / `cu.macroRun(name,{speed})` / `cu.macroScript(name)` | 列出、回放、导出宏 |
| `args` / `version` | 调用方传入的参数、程序版本 |

最后一个表达式的值就是脚本的返回值；抛出的 Go 错误会变成 JS 异常。

### 录制宏

三种入口都能录，录的是**动作**（不是屏幕像素）：

```powershell
# 1) 命令行：录制期间用 API / MCP / 脚本做的每个动作都会记下来
computer-use macro record start 登录流程
#    ... 这里用 curl / MCP / 脚本操作 ...
computer-use macro record stop

# 2) HTTP
curl -X POST localhost:7331/v1/record/start -d '{"name":"login"}'
curl -X POST localhost:7331/v1/record/stop

# 3) 脚本内
cu.recordStart("login"); cu.click(100,200); cu.type("ada"); cu.recordStop();
```

### 回放

```powershell
computer-use macro list                 # 列出
computer-use macro show 登录流程         # 看步骤 JSON
computer-use macro script 登录流程       # 导出成 JS（可编辑后再跑）
computer-use macro run 登录流程 --speed 2   # 回放，等待时间减半
curl -X POST localhost:7331/v1/macros/登录流程/run -d '{"speed":2}'
```

回放走同一套动作：蒙板会随步骤标注、暂停开关仍然生效、每一步进审计日志。
`speed` 只缩放宏里的 `wait`；`--fast` 启动的程序本身就不等。

脚本与宏的接口：

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | `/v1/script` | 运行 JS：`{source, name, timeout_ms, record, args}` |
| GET | `/v1/macros` | 宏列表 |
| GET/DELETE | `/v1/macros/{name}` | 取一个宏 / 删除 |
| PUT | `/v1/macros/{name}` | 保存外来宏：`{steps:[...]}` |
| POST | `/v1/macros/{name}/run` | 回放：`{speed}` |
| GET | `/v1/macros/{name}/script` | 导出为 JS |
| GET/POST | `/v1/record` `/v1/record/start` `/v1/record/stop` | 录制状态与开关 |

宏保存在 `%LOCALAPPDATA%\ComputerUse\macros.json`，重启后还在。状态页右侧
有「脚本 / 宏」面板：直接写 JS 运行，或点宏的「▶ 回放」。

## 给代理的使用流程

1. `screen_info`：拿到显示器与虚拟桌面，确认坐标基准。
2. `screenshot`（`grid=100`，`max_width≈1600`）：读界面，用网格或 `region` 局部放大
   确定目标点。
3. `mask` / `outline_window`：在动作前框出目标，让人看到代理的意图（坐标标签是给人
   和代理的共同参照）。
4. `click` / `type` / `key`：执行；蒙板会自动画出十字线、涟漪和状态胶囊。
5. 再截一张图确认结果；需要时 `mask_preview` 核对标注。

建议：不要连续盲点；每一步之后截图确认；大段文本用 `clipboard` + `key ctrl+v`；
等界面加载用 `wait`；不确定前后台窗口时先 `focus_window`。

## 安全与可控性

- **暂停**：`Ctrl+Alt+Shift+P`、托盘、`POST /v1/pause` 任一方式都能立刻停住注入；
  暂停期间所有输入动作返回错误，截图与读状态仍可用。
- **只读模式**：`--read-only` 只截屏、只看窗口，不注入任何输入。
- **仅本机**：默认只监听 `127.0.0.1`；可加 `--token`。
- **审计**：动作写入 `%LOCALAPPDATA%\ComputerUse\logs\actions.jsonl`（JSON Lines）。
- **不拍自己**：蒙板用 `WDA_EXCLUDEFROMCAPTURE` 排除在截图之外，代理的视觉输入永远
  是干净桌面；代理要核对标注时用 `/v1/mask.png`。
- **不列自己**：`list_windows` 过滤本程序的窗口。
- **不抢焦点**：蒙板窗口 `WS_EX_NOACTIVATE`，鼠标穿透，不进 Alt+Tab/任务栏。

已知风险：程序能注入任意键鼠（包括密码框），请只在你信任的代理上运行；不要把它
暴露到公网；`--token` 只是本机共享时的最小保护。

## 性能

本机实测（Windows 10、双 1920×1080、100% 缩放，客户端测得的端到端时间，取 3 次最好）：

| 操作 | 优化前 | 优化后 | 说明 |
|---|---|---|---|
| `GET /v1/state` | 87 ms | **1 ms** | 窗口列表缓存 400ms，进程名缓存 5s |
| `GET /v1/screenshot`（png 1600） | 141 ms | **108 ms** | 截图 50ms + 缩放/编码；PNG 用 BestSpeed |
| `screenshot`（png 1280 + `fast=1`） | — | **76 ms** | GDI 边拷边缩放，跳过 CAPTUREBLT |
| `screenshot`（区域 900×600） | 28 ms | **33 ms** | 只搬需要的像素 |
| `click` | 144 ms | **25 ms** | 去掉动作前的固定停顿（`--fast` 后更少） |
| `move` | 161 ms | **2 ms** | 指针到位的 1px 误差立即吸附，不再等 150ms |
| `type` 50 字 | 449 ms | **13 ms** | 一次 SendInput 批量送入，不再逐字等待 |
| `key` | — | **5 ms** | 只有 repeat > 1 才在按键之间等待 |
| `batch`（click+type+key+wait） | ≈ 三条命令之和 | **141 ms** | 一次 HTTP 往返，蒙板只在批次前后各刷一帧 |

再快的做法：

- **一次做一串**：能用 `batch` 就不要分三次调用（HTTP 往返、蒙板刷新、模型轮次都省）。
- **只截需要的**：`display=1` 或 `region=x,y,w,h`；全屏 4K 截图里 50ms 是 BitBlt 的搬运。
- **更小的图**：`max_width=1280` + `format=jpeg`（约 110KB，PNG 240KB），模型看得更快。
- **`--fast`**：启动参数，动作前不再停 40ms 给眼睛；蒙板仍然画，只是不等人。
- `state` 有 400ms 缓存；连续读不会重复枚举窗口。
- 蒙板的指针坐标刷新限制在 12.5 fps，HUD 在批次里只更新一次，避免每步整屏重绘。

如果要再压截图时间，下一步是 **DXGI Desktop Duplication / Windows.Graphics.Capture**：
一次抓取 5–15ms，而不是每个 BitBlt 50ms；代价是要管理 D3D11 设备。

## 架构

```
main.go            命令分发（run / mcp / shot / doctor / demo）
cmd_run.go         程序主体：蒙板、HTTP、托盘、快捷键、日志
cmd_mcp.go         MCP 代理：转发到运行中的程序，可选 --spawn
cmd_shot.go        无窗口截图
cmd_doctor.go      环境体检
cmd_demo.go        蒙板演示
internal/win32     Win32 封装：显示器、截图、SendInput、窗口、剪贴板
internal/screen    截图/缩放/坐标网格/与蒙板合成
internal/mask      蒙板窗口与绘制（state.go / draw.go / mask.go）
internal/engine    动作执行、坐标校验、暂停、录制、宏回放、审计
internal/macro     宏的数据结构、存储（macros.json）与导出为 JS
internal/script    moejs 运行时与 cu.* 绑定
internal/api       HTTP JSON API 与状态页
internal/mcp       MCP stdio 服务器（工具定义与转发）
patches/           mygo 的补丁
```

蒙板的窗口来自 `mygo.NewWindow`（`Transparent` + `Content: ui.View(...)`），
每帧用 `ui.Painter` 画：暗层是「全屏矩形减去洞」后的若干矩形，元素框是描边 +
角括号，文字用 `RichText` 排版，动画用 `p.AnimationFrame()` 局部重绘。

## 依赖的 mygo

Windows 上让**原生 UI**（`WindowOptions.Content`）透出桌面，需要窗口带
`WS_EX_NOREDIRECTIONBITMAP`，由 DirectComposition 合成带 alpha 的画面。这个能力
**已经合并进 mygo 上游**（`internal/windows/window.go` 里
`o.Flyout != nil || o.Transparent && o.Surface` → `w.noRedirect = b.composition() != nil`），
所以本仓库不再需要维护补丁：

```go
// mygo 的 window.go：窗口与内容都是透明的，alpha 透过 DirectComposition 合成
mygo.NewWindow(mygo.WindowOptions{
    Transparent: true,
    Content:     ui.View(view), // 视图里 c.Root().Background(ui.Transparent)
})
```

当前依赖：**mygo v0.7.4-36-g5b33c407（2026-11-03）**，Go **1.27.3+**，
`go.mod` 用 `replace github.com/egoist/mygo => ../mygo` 指向本机检出。
`patches/mygo-transparent-native-ui.patch` 只是早期未合并时的补丁，留作历史参考，
新检出不需要它。

## 已验证

在本机（Windows 10 21H2 19044、双屏 1920×1080、100% 缩放）实测：

- `doctor`：截图 3840×1080、SendInput、组合键、窗口枚举、剪贴板全部通过。
- 截图：全桌面/单屏/区域、缩放、`grid` 坐标网格、`cursor` 指针标记。
- 蒙板：两块屏各自的暗层、聚光洞、元素框 + 坐标标签、指针坐标、标尺网格、
  点击涟漪、状态胶囊；`/v1/mask.png` 与用户所见一致。
- 输入：在记事本中点击、输入中英文与 `—`、回车、`Ctrl+A`、拖拽选中、右键菜单、
  `Esc`；均按预期生效（蒙板开启时亦然，证明鼠标穿透）。
- 暂停：`POST /v1/pause` 后点击被拒绝，截图仍可用。
- MCP：`initialize`、`tools/list`、`screen_info`、`click`、`mask`、`batch`、
  `run_script`、`record_*`、`list_macros`、`run_macro` 往返正常（`internal/mcp`
  的测试会检查「列出的工具都真的能派发」）。
- JS / 宏：`POST /v1/script` 运行脚本（读状态、画蒙板、点击/输入）约 2–5 ms；
  `cu.recordStart/Stop` 在脚本内录制；HTTP/CLI/MCP 三种入口都能录；
  `macro run --speed` 回放、`macro script` 导出 JS、`macros.json` 重启后仍在。
  例：录制 `move + wait` 两步、导出、以 2× 回放、删除，全部通过。
- `go vet ./...` 干净，`go test ./...` 通过（几何、坐标换算、按键解析、缩放、
  网格、截图映射、moejs 绑定、宏存储）。
- 升级到 mygo v0.7.4-36（2026-11-03）与 Go 1.27.3 后复测：蒙板透明、聚光洞、
  截图不含蒙板、脚本（cu.*）、宏录制/回放全部正常。

## 限制与后续

- 仅 Windows（截屏与注入是 Win32 实现）；蒙板本身依赖 mygo 的原生 UI。
- 元素级定位目前来自「截图 + 网格 + 窗口矩形」；没有 OCR，也没有 UI Automation
  元素树——下一步可以加 `IUIAutomation`/`IAccessible` 的元素枚举，直接把每个控件的
  名称、角色、`x,y w×h` 交给代理（蒙板已能画这些矩形）。
- 高 DPI 多屏混合缩放的换算已按显示器实现，但只在 100% 缩放下实测。
- 没有操作录制/回放、多会话隔离与远程访问；这些都不在 v1 范围。
- 代理若要「看」动画过程，需要自己多次截图；蒙板动画只给人看。
