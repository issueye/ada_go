# adago_design — 设计体系 MCP 设计文档

一个「**设计体系 + 浏览器无限画布**」的 MCP 服务器：AI Agent 通过 MCP 工具读取
多套设计体系的 Design Token 与组件规范，把组件预览（真实 HTML/CSS 渲染）
以卡片形式摆放到浏览器里的**无限画布**上，做多体系并排对比、组合成页面、
并支持画布截图回传给 Agent。

- 模块位置：`mcp_projects/adago_design`（自包含 Go module，`GOWORK=off` 构建）
- 参考实现：`mcp_projects/browser_use`（单二进制多形态 + 回环 HTTP 控制面）、
  `mcp_projects/mem_go`（官方 `modelcontextprotocol/go-sdk` 的工具注册模式）

## 1. 目标与非目标

### 目标

1. **多设计体系**：内置 Apple HIG（iOS）、火山引擎 Arco Design、抖音、
   小米 MIUI/HyperOS、Element Plus 五套体系的 Token 与组件数据，可扩展注册。
2. **真实渲染**：组件预览不是截图/图示，而是体系 Token 生成的真实 HTML+CSS，
   一份组件 HTML 结构 × 每体系一份皮肤 CSS，保证「同结构、异风格」可直接对比。
3. **无限画布**：浏览器打开 `http://127.0.0.1:<port>/`，画布支持平移/缩放/
   节点拖拽/框选/主题切换；节点是沙箱 iframe 渲染的预览卡片。
4. **MCP 工具**：Agent 通过 stdio MCP 读写画布、查询 Token、生成预览、截图回传。
5. **零外部依赖运行**：纯 Go 标准库 + 官方 MCP SDK；不依赖 chromedp/CDP；
   截图由已打开的画布页面自己完成（`foreignObject` 序列化渲染）。

### 非目标（有意不做）

- 不做 Figma/Sketch 文件解析，不做矢量编辑器（画布只管「摆放与查看」）。
- 不做设计体系数据的在线抓取/同步（Token 内置于代码，标注为公开资料整理）。
- 不做多用户/鉴权体系：服务只绑 `127.0.0.1`，与 browser-use 同级信任。
- 不做 SVG 白板/画笔。

## 2. 总体架构

```
┌────────────┐  stdio (MCP)   ┌───────────────────────────────────────┐
│  AI Agent  │◄──────────────►│  adago-design.exe                     │
└────────────┘                │  ┌─────────┐  ┌─────────────────────┐ │
                              │  │mcpserver│  │ canvas HTTP (loopback)│ │
                              │  └────┬────┘  └─────────┬───────────┘ │
                              │       │   ┌─────────┐   │             │
                              │       └──►│ design  │◄──┘             │
                              │           │ (tokens │  REST + 截图任务 │
                              │           │+preview)│                 │
                              │           └─────────┘                 │
                              └───────────────────────────────────────┘
                                                ▲ HTTP
                                     ┌──────────┴──────────┐
                                     │ 浏览器：无限画布 UI   │
                                     │ (vanilla JS + iframe)│
                                     └─────────────────────┘
```

- **一个二进制，两种形态**（同 browser-use）：
  - `adago-design.exe mcp`：stdio MCP + 内嵌回环画布服务（Agent 接入用）。
    首次调用画布类工具时按需启动 HTTP 服务；`--open`/`ADAGO_DESIGN_OPEN=1`
    时自动拉起系统默认浏览器。
  - `adago-design.exe serve`：只跑画布 Web 服务（人工查看用）。
- 端口：默认 `8788`，被占用则向上递增探测（最多 +20），实际地址通过
  `canvas_url` 工具与 `/api/health` 暴露。
- 数据目录：`%APPDATA%\adago-design\`（`--data`/`ADAGO_DESIGN_DATA` 覆盖），
  画布状态持久化为 `canvas.json`（原子写入：tmp+rename）。

## 3. 设计体系数据模型（internal/design）

```go
type System struct {
    ID, Name, Vendor, Description string   // id: "apple" | "volcano" | ...
    Family    string                       // "mobile" | "web" | "cross"
    Themes    []string                     // ["light","dark"]
    Style     StyleNotes                   // 风格要点（供 Agent 阅读的文字说明）
    Light     Tokens
    Dark      *Tokens
    Guideline []string                     // 使用守则（列表项）
}

type Tokens struct {
    Colors  map[string]Color        // 语义色 -> {value, usage}
    Type    TypeScale               // 字族 + 字号阶梯（size/weight/lineHeight）
    Spacing map[string]int          // 语义间距 -> px
    Radius  map[string]int          // 语义圆角 -> px
    Shadows map[string]string       // 语义阴影 -> CSS box-shadow
    Motion  map[string]string       // 语义动效 -> CSS（时长/缓动）
    Grid    int                     // 栅格基数（通常 4 或 8）
}
```

### 3.1 八套体系（公开资料整理，近似值）

| id | 名称 | 来源 | 主色 | 关键风格 |
|---|---|---|---|---|
| `apple` | Apple HIG (iOS) | Apple Human Interface Guidelines | systemBlue `#007AFF` | 8pt 栅格、连续圆角、SF Pro、毛玻璃层级 |
| `volcano` | 火山引擎 Arco Design | 字节跳动 Arco | arcoblue-6 `#165DFF` | 中性灰阶 text-1/2/3、轻描边、2/4/8 圆角阶梯 |
| `douyin` | 抖音设计体系 | 抖音公开视觉规范 | 抖音红 `#FE2C55` + 青 `#25F4EE` | 高饱和撞色、大圆角、沉浸式黑色 `#161823` |
| `xiaomi` | 小米 MIUI/HyperOS | 小米公开视觉规范 | 橙 `#FF6900` | MiSans、大圆角（16/24）、轻阴影、暖白 |
| `element` | Element Plus | Element Plus 主题变量 | primary `#409EFF` | 4px 圆角、清晰描边、`--el-*` 语义变量 |
| `ant` | Ant Design 5 | 蚂蚁 Ant Design 变量 | daybreak `#1677FF` | 6/8px 圆角、透明度文本四级、三段式阴影 |
| `tdesign` | 腾讯 TDesign | TDesign 变量 | brand `#0052D9` | 3/6/9 圆角阶梯、gray-1~4 分层、稳重描边 |
| `material` | Material Design 3 | Google Material You 基线 | `#6750A4` | 色对（on-color）、胶囊按钮、12-28 大圆角、surface 阶梯 |

每套体系含：`Colors`（语义色，light/dark 双主题，如可用）、`Type`
（字族+字号阶梯）、`Spacing`、`Radius`、`Shadows`、`Motion`、`Grid`，
外加 `Components` 组件清单（见 3.2）与 `Guideline` 使用守则。

### 3.2 组件：一份结构，五份皮肤

组件 HTML 结构全体系统一（class 前缀 `ds-`），差异全部落在每体系生成的
CSS 变量与组件样式里：

```
ds-btn（primary/secondary/danger/text × small/normal/large）
ds-input、ds-switch、ds-checkbox、ds-radio、ds-select、ds-slider
ds-card、ds-tag、ds-badge、ds-avatar、ds-alert（info/success/warning/danger）
ds-progress、ds-navbar、ds-tabbar、ds-list、ds-dialog、ds-toast
```

- `design.StyleSheet(sys, theme) string`：由 Token 生成该体系的完整 CSS
  （`:root` 变量段 + 组件段）。
- `design.ComponentHTML(sys, compID, variant...) string`：渲染单个组件 HTML 片段。
- `design.Compose(sys, ids, opts)`：把若干组件拼成一张预览页
  （完整 HTML 文档，内嵌 StyleSheet + 局部排版样式），即画布节点内容。

## 4. 无限画布（internal/canvas + static/）

### 4.1 数据模型

```go
type Node struct {
    ID     string   `json:"id"`       // n-<ts>-<rand>
    Title  string   `json:"title"`
    System string   `json:"system,omitempty"`
    Kind   string   `json:"kind"`     // screen|component|palette|note
    HTML   string   `json:"html"`     // 完整预览文档（iframe srcdoc）
    Width  int      `json:"width"`    // 渲染宽度（手机 390 / 桌面 420）
    Height int      `json:"height"`
    X, Y   float64  `json:"x"`        // 画布坐标（未缩放）
    Z      int      `json:"z"`
    Tags   []string `json:"tags,omitempty"`
    CreatedAt, UpdatedAt time.Time
}
```

`Store`：内存 map + 互斥锁；变更即写 `canvas.json`（原子替换）；
启动时载入。节点 HTML 由服务端（design 包）生成，画布 UI 不二次加工。

### 4.2 HTTP API（全部 `127.0.0.1`，POST 校验 Origin 同源）

| 方法/路径 | 说明 |
|---|---|
| `GET /` | 画布单页（embed.FS：index.html / canvas.css / canvas.js） |
| `GET /api/health` | `{ok, version, nodeCount, captureReady}` |
| `GET /api/nodes` | 全量节点（轮询同步，`?since=` 可增量） |
| `POST /api/nodes` | 新建节点（服务端生成 ID/时间戳） |
| `PUT /api/nodes/{id}` | 更新（位置/标题等；`if-unmodified-since` 防覆盖） |
| `DELETE /api/nodes/{id}` | 删除 |
| `POST /api/nodes/clear` | 清空 |
| `POST /api/capture` | 创建截图任务（tool 调用）→ `{taskID}` |
| `GET /api/tasks` | UI 长轮询领取任务（10s 内返回或超时空列表） |
| `POST /api/tasks/{id}/result` | UI 回传 PNG（base64 dataURL） |
| `GET /api/capture/{id}` | tool 取结果（长轮询，8s 超时 → 409 提示打开画布） |

### 4.3 截图链路（无 CDP 方案）

```
MCP canvas_screenshot ─► POST /api/capture ─► 任务入队
   UI 轮询领任务 ─► 把目标节点 HTML 注入离屏 <iframe>
   ─► SVG <foreignObject> 序列化 ─► <canvas> 绘制 ─► PNG dataURL
   ─► POST /api/tasks/{id}/result ─► tool 长轮询取回 ─► ImageContent
```

预览内容是纯 CSS（无外链图片/字体），`foreignObject` 渲染安全且不失真。
8 秒内没有已打开的画布领取任务 → 返回错误并提示先打开画布 URL。

### 4.4 画布 UI（vanilla JS，无构建步骤）

- 交互：空白处拖拽平移；滚轮/触控板缩放（以光标为中心）；节点头部拖拽移动；
  点击选中 → 右侧检查器（显示体系/Token 摘要/HTML 尺寸）；Del 删除；
  `F` 适配全部节点。
- 侧栏：设计体系清单（点开看 Token 摘要与组件列表，可一键「添加预览卡」）。
- 渲染：每节点一个 `sandbox="allow-same-origin"` 的 iframe（srcdoc），
  按 `Scale` 用 CSS transform 缩放。
- 同步：本地操作即时生效 + 每 2s `GET /api/nodes?since=` 增量合并（按 UpdatedAt）。
- 视觉：点阵网格背景、暗/亮主题切换、极简工具条（缩放百分比、适配、清空、主题）。

## 5. MCP 工具（internal/mcpserver）

SDK：`github.com/modelcontextprotocol/go-sdk v1.8.0`（与 mem-go 一致）。
服务器名 `adago-design`；Instructions 指引：先 `design_systems` 再
`design_tokens`/`design_components`，产出用 `design_preview`（`addToCanvas`）
或 `canvas_add` 摆放，`canvas_screenshot` 回看效果。

| 工具 | 入参要点 | 出参 |
|---|---|---|
| `design_systems` | — | 体系清单（id/名称/风格要点/主题/组件数） |
| `design_tokens` | `system`, `theme?=light`, `category?=all/color/type/spacing/radius/shadow/motion` | Token 全量或分类 |
| `design_components` | `system`, `component?`（单个则含 HTML 片段） | 组件清单/详情/用法守则 |
| `design_search` | `query` | 跨体系命中清单（体系/组件/Token） |
| `design_preview` | `system`, `components[]`, `title?`, `theme?=light`, `addToCanvas?=true`, `tags?` | HTML 文档（路径+节选）、节点 ID、画布 URL |
| `design_screen` | `system`, `template?=login/settings/feed/profile`, `theme?`, `app_name?`, `addToCanvas?=true` | 整页预览 HTML、节点 ID、用到组件 |
| `design_compare` | `system_a`, `system_b`, `theme?`, `addToCanvas?=false` | 双体系 Token diff（颜色/字型/间距/圆角/阴影/动效）+ 可选对比板节点 |
| `canvas_add` | `title`, `html`, `width?`, `height?`, `x?`, `y?`, `kind?=note`, `tags?` | 节点 ID + 画布 URL（不指定 x/y 自动布局） |
| `canvas_list` | `system?`, `tag?` | 节点摘要列表（含 x/y/w/h） |
| `canvas_move` | `id`, `x`, `y` | 更新后节点 |
| `canvas_remove` | `id` | 删除确认 |
| `canvas_arrange` | `columns?`（默认 sqrt 取整，1-8） | 按「体系分组→网格」自动排版结果 |
| `canvas_clear` | `confirm=true` | 清空数量（保留内存快照） |
| `canvas_restore` | — | 恢复最近一次清空的节点 |
| `canvas_export` | `title?` | 全画布独立 HTML 板文件路径 |
| `canvas_screenshot` | `nodeID?`（空=全画布）, `scale?=1` | `ImageContent`（PNG） |
| `canvas_url` | — | 画布访问地址与打开方式 |
| `design_derive` | `primary`(#RRGGBB), `name`, `base_system`, `addToCanvas?=true` | 10 阶品牌色板 + 派生体系（持久化注册）+ 色板节点 |
| `design_register` | `name`, `tokens_json`, `dark_json?`, `base_system?` | 自定义体系注册（结构兜底 + 持久化） |
| `design_audit` | `system`, `theme?`, `addToCanvas?=true` | WCAG 对比度/字号/阶梯走查评分 + 报告节点 |
| `canvas_connect` | `from`, `to`, `label?` | 节点间贝塞尔连线（SVG，持久化） |
| `canvas_group` | `title`, `node_ids[]`, `padding?` | 分组框（垫底虚线框，自动包住成员） |

### 3.3 屏幕模板（design_screen）

| 模板 | 结构 |
|---|---|
| `login` | 居中卡片：品牌块 + 账号/密码 + 记住我 + 主操作 + 注册链接 |
| `settings` | 导航栏 + 开关分组 ×3 + 列表分组 ×4 + 底部标签栏 |
| `feed` | 导航栏 + 媒体卡片 ×2（渐变占位图/标题/操作行） + 底部标签栏 |
| `profile` | 头像头部 + 统计行 + 列表 ×3 + 底部标签栏 |

约定（沿用 browser-use）：JSON 键 snake_case；错误不抛 JSON-RPC error，
而是 `isError` 结果 + 稳定错误码（`unknown_system` / `unknown_component` /
`capture_timeout` / `canvas_unavailable`）；文本输出首行给人看的摘要，
结构化输出供 Agent 解析。

## 6. 错误与日志

- 错误码常量集中在 `canvas` / `mcpserver` 包，`canvas.Error{Code, Message, Hint}`。
- 日志：stdlib `log`，前缀 `[adago-design] `；stdio 模式下日志只走 stderr，
  不污染 JSON-RPC 管道。
- panic 兜底：MCP 服务器开 `WithRecovery`（SDK 选项），HTTP handler recover → 500。

## 7. 构建与接入

```powershell
# 构建（GOWORK=off，与其它 mcp_projects 一致）
cd mcp_projects/adago_design
GOWORK=off go build -trimpath -o bin/adago-design.exe .

# ada-go 接入（stdio）
# .adago/mcp.json:
{"id":"adago_design","transport":"stdio",
 "command":"E:\\codes\\go_projects\\ada_go\\mcp_projects\\adago_design\\bin\\adago-design.exe",
 "args":["mcp"],"enabled":true}
```

`scripts/build-mcp.ps1` 增补 `adago-design` 目标（console 二进制）。

## 8. 测试与验收

- `internal/design`：注册表完备性（5 体系、Token 非空、颜色合法 hex）、
  StyleSheet/Compose 生成物包含关键 token 值、组件 HTML 结构快照断言。
- `internal/canvas`：Store CRUD/持久化/并发、HTTP API 全路径（httptest）、
  截图任务闭环（模拟 UI 回传）/超时路径。
- `internal/mcpserver`：内存 transport 走 `initialize` + `tools/list` +
  `tools/call`（design_tokens、design_preview、canvas_add、canvas_screenshot
  超时路径），断言结果结构。
- 门禁：`GOWORK=off go test ./... && go vet ./...`。
- 冒烟：`serve` 起服务 → 浏览器打开画布 → MCP stdio 会话调用工具链。

## 9. 里程碑

- M0：数据模型 + 设计体系 Token/组件/样式生成 + 画布（摆放/预览/截图）+ MCP 工具 + 测试 + 文档。✅
- M1：体系扩到 8 套（+Ant Design/TDesign/Material 3）、design_compare（Token diff + 对比板）、
  design_screen（4 种整页模板）、design_search、canvas_arrange/restore/export。✅
- M2：design_derive（品牌色派生体系，持久化注册）、design_register（自定义体系）、
  design_audit（WCAG 走查）、canvas_connect/canvas_group（连线与分组框，canvas.json v2）。✅
- M3（候选）：minimap、框选批量操作、画布内编辑、演示模式、Markdown 设计文档导出、
  ZIP 导出、Web 字体打包、ada-go 插件化、组件级对比。
