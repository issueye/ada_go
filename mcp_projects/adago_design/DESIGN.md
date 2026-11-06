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
type Project struct {
    ID        string    `json:"id"`        // p-<ts>-<seq>
    Name      string    `json:"name"`      // 必填、同名去重
    Note      string    `json:"note,omitempty"`
    CreatedAt, UpdatedAt time.Time
}

type Node struct {
    ID        string   `json:"id"`        // n-<ts>-<seq>
    ProjectID string   `json:"project_id"` // 必填：必须指向已存在的项目
    Title     string   `json:"title"`
    System    string   `json:"system,omitempty"`
    Kind      string   `json:"kind"`      // screen|component|palette|note|frame
    HTML      string   `json:"html"`      // 完整预览文档（iframe srcdoc）
    Artboard  string   `json:"artboard,omitempty"`
    Width     int      `json:"width"`
    Height    int      `json:"height"`
    X, Y      float64  `json:"x"`         // 画布坐标（未缩放）
    Z         int      `json:"z"`
    Tags      []string `json:"tags,omitempty"`
    CreatedAt, UpdatedAt time.Time
}
```

`Store`：内存 map + 互斥锁；变更即写 `canvas.json`（原子替换，v3 格式
`{version:3, projects, nodes, edges}`）；启动时载入并完成迁移。
节点 HTML 由服务端（design 包）生成，画布 UI 不二次加工。

### 4.2 HTTP API（全部 `127.0.0.1`，POST 校验 Origin 同源）

| 方法/路径 | 说明 |
|---|---|
| `GET /` | 画布单页（embed.FS：index.html / canvas.css / canvas.js） |
| `GET /api/health` | `{ok, node_count, project_count, time}` |
| `GET /api/projects` | 项目列表（含 `node_count` / `frame_count`） |
| `POST /api/projects` | 建项目 `{name, note?}`；空名 400、同名 409 |
| `GET /api/projects/{id}` | 单个项目（404 `unknown_project`） |
| `PUT /api/projects/{id}` | 改名/改备注 |
| `DELETE /api/projects/{id}` | 删除空项目；非空 409 `project_not_empty` |
| `GET /api/nodes` | 节点列表；`?project=<id>` 过滤（未知项目 404），`?system=` 兼容过滤 |
| `POST /api/nodes` | 新建节点，`project` 必填（缺失 400 `project_required`） |
| `PUT /api/nodes/{id}` | 更新（位置/高度/标题等） |
| `DELETE /api/nodes/{id}` | 删除（级联删连线） |
| `POST /api/nodes/clear` | 清空**指定项目**，`project` 必填 |
| `POST /api/arrange` | `{group_by:"project"}` 按项目分行重排（全部视图用），返回 `{moved}` |
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
  点击选中 → 右侧检查器；Del 删除；`F` 适配全部节点。
- 侧栏：顶部是**项目树**（`全部` + 计数 + `⋯` / `＋ 新建项目 ⇧N` / 搜索 / 文件夹行 + 计数），
  参照设计稿工具的分组列表；`S.project === ""` 表示「全部」，否则为单项目作用域。
- **全部视图**：一次渲染所有项目的节点，按项目分行并在行首打橙色分组标签
  （`#group-labels`，世界坐标、随拖动更新、按 1/scale 反向缩放保证可读）。
  进入时若检测到项目包围盒相交，自动调 `POST /api/arrange {group_by:"project"}`
  排一次（行首留 220px 标签位），之后仍是自由画布，可拖可连。
- **单画布聚焦**（双击项目树子项 / 双击卡片标题栏）：`S.solo = nodeID`。
  不改数据、不改作用域，只给其它节点加 `.solo-hidden`（轮询新增节点同样处理），
  再让 `fitView()` 只算这一张稿（放大上限 1.6×、留白 48px、按「侧栏右侧 +
  工具条下方」的可见区居中）。`#edges` / `#rel-layer` / `#group-labels` / 检查器
  一并收起，顶部 `#solo-bar` 显示标题与所属项目，树里对应子项高亮。
  `Esc` / 双击空白 / 返回按钮退出；`resetCanvas()`（切项目、删项目）与被聚焦
  节点被删除时自动退出。**单击子项延后 220ms 执行**，双击到来即取消 ——
  否则两次 click 会先触发两次 `switchProject`，和双击的切作用域竞态。
- 侧栏原设计：顶部是项目选择器（`<select>` + 新建/管理按钮）——切项目 = 换一块画布：
  先清掉当前渲染的节点，再按 `?project=` 重新拉取；当前项目记在 localStorage。
  「项目管理」面板（`#proj-modal`）承担新建 / 重命名 / 删除 / 切换四件事，
  行内展示节点数、分组框数、更新时间与 ID；`#proj-select` 只做快速切换（只显示名称，
  计数在面板里，避免 220px 的窄栏被长名字挤爆）。切项目后必须同步 `select.value`，
  否则从面板切换会出现「画布换了、下拉没换」的状态分叉。
  下方是设计体系清单（点开看 Token 摘要与组件列表，可一键「添加预览卡」）。
- 侧栏收起：面板整体 `translateX(-296px)` 移出屏幕，但**必须留一个屏内入口** ——
  左侧常驻「› 体系」把手 `#sb-expand`，由 `body.sb-collapsed` 控制显隐；
  否则收起来就再也点不到展开按钮（原来的 `#sb-toggle` 也一起被移出了屏幕）。
  收起状态写 localStorage，刷新后保持。
- 渲染：每节点一个 `sandbox="allow-same-origin"` 的 iframe（srcdoc），
  按 `Scale` 用 CSS transform 缩放。
- 同步：本地操作即时生效 + 每 2s 增量合并（按 UpdatedAt）。

#### 拖动与渲染性能（对齐问题 4）

踩过的坑与对策：

| 现象 | 根因 | 对策 |
|---|---|---|
| 拖动时节点「粘住」 | 光标进入 iframe 后，父页面收不到 `mousemove`（iframe 内事件不冒泡）| `.node__body iframe{pointer-events:none}`；检查模式反向放开 |
| 掉帧 | 每次 mousemove 全量重建连线 SVG | 元素池化 + 脏边增量 + rAF 合帧 |
| 掉帧 | `left/top` 每帧触发 layout，iframe 表面重新光栅化 | `translate3d` + `will-change` + `contain:layout paint` |
| 发虚 | `.node.dragging{opacity:.92}` 让 iframe 每帧重新混合 | 去掉 opacity，用阴影/`z-index` 表达拖动态 |
| 经过面板时骤降 | 固定面板的 `backdrop-filter: blur` 每帧重算 | `body.is-dragging` 时关闭 blur |
| 松手闪一下 | PUT 丢弃回包 → 本地 `updated_at` 陈旧 → 轮询误判 → 重设 srcdoc | 用回包覆盖本地节点；重载判定改为比对 `html` |

#### 检查模式（对齐问题 6）

两层职责分离：

```
卡片内（各自 iframe 的 .dsx-layer，视口坐标）  →  元素边框 / A B 角标 / 到画板距离
卡片间（父页面 #rel-layer，世界坐标）          →  间距线 / 对齐虚线 / 包含重叠 / 尺寸差
```

- 状态机：`idle → anchor → (hover 临时 B | 点击固定 B)`；第三个元素使锚点前移（连续对比），
  `Shift` 强制替换 B，`Esc` 逐级退出。原来的「`Alt` 悬停临时对照」已并入
  **点选 + hover**：定 A 后移入谁就和谁比，不需要按键或第二次点击；
  固定 B 后 hover 只刷新信息，不再改对比对象。
- 悬停标注：`nameChip` 出元素名胶囊；`hoverGaps` 取最近两个兄弟的间距画
  `dsx-line` + `dsx-label`（参考图的 `35px` 语义）。有锚点时让位给 A↔B 标注，
  避免两套线在同一张卡里打架。
- 世界坐标：`节点 x/y + 元素在 iframe 视口内的坐标`（`getBoundingClientRect` 已含滚动），
  与画布缩放/平移解耦，因此跨卡片也能连线。
- 标注层按 `data-role`（hover / sel）分区，清悬停只清那一张卡的 hover 分区，
  绝不误清已选中的标注。
- **坐标字段别混用**：`localRect()` 返回 `{x,y,w,h}`，`getBoundingClientRect()`
  返回 `{left,top,right,bottom,width,height}`。`hoverGaps` 早期按后者取值，
  所有比较都变成 `undefined < undefined` → 间距标注**静默消失、不抛异常**。
  现在函数入口显式拆成 `L/T/R/B`，只有兄弟节点才读 DOMRect。
- 检查器面板带 `panelGen` 代际号：`ensureCatalog` 是异步的，hover 又高频，
  旧请求回来时若代际已变就丢弃结果，避免面板显示过期元素。
- 代码在独立的 `static/inspect.js`（`canvas.js` 只管视图、拖动、同步、截图）。

#### 滚动条（视觉一致性）

面板与预览文档各有一套规则，但视觉统一为「透明轨道 + 6px 悬浮滑块」：

```css
*{scrollbar-width:thin;scrollbar-color:color-mix(in srgb,var(--fg2) 38%,transparent) transparent}
::-webkit-scrollbar{width:10px;height:10px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:color-mix(in srgb,var(--fg2) 32%,transparent);
  border-radius:999px;border:2px solid transparent;background-clip:padding-box}
```

- 10px 是**命中区**，2px 透明边框内缩后视觉宽度约 6px，既细又好抓；
- 轨道透明，避免深色主题下出现一条高对比灰带；
- 预览文档（iframe 内部）单独注入一份，用 `--ds-color-text-3` 着色，
  与所在体系主题一致，且不会挤占版心宽度。

### 4.5 项目作用域与不变量

「先建项目、再加设计稿」不是 UI 约定，而是**数据层不变量**：

| 不变量 | 落点 | 违反时的错误 |
|---|---|---|
| 节点必须有 `project_id`，且项目已存在 | `Store.Add`（唯一写入入口） | `project_required` / `unknown_project` |
| 清空只能按项目清 | `Store.Clear(projectID)` | `project_required` |
| 连线两端必须同项目 | `Store.AddEdge` | `edge_cross_project` |
| 项目非空不可删 | `Store.RemoveProject` | `project_not_empty` |
| 自动排版只在项目内计算 | `autoPositionLocked(projectID, …)` | —（各项目从 (80,80) 起） |
| 恢复快照不跨已删项目 | `RestoreLastClear` | 跳过该项目节点 |

为什么拦在 `Store` 而不是 handler：节点有两条写入路径（HTTP 直连 / MCP 工具），
只有数据层能同时覆盖；handler 层只负责把哨兵错误翻译成稳定错误码 + 提示。

**迁移**：v1（裸数组）与 v2（无 projects）在 `loadAll` 里统一处理 ——
无 `project_id` 的节点归入固定项目 `p-default`「默认项目（历史数据）」；
项目已被删但节点残留的，也一并归入，避免出现无主设计稿。
这是唯一的自动建项目场景，只为不丢历史数据，新写入仍然严格校验。

## 5. MCP 工具（internal/mcpserver）

SDK：`github.com/modelcontextprotocol/go-sdk v1.8.0`（与 mem-go 一致）。
服务器名 `adago-design`；Instructions 指引：先 `design_systems` 再
`design_tokens`/`design_components`，产出用 `design_preview`（`addToCanvas`）
或 `canvas_add` 摆放，`canvas_screenshot` 回看效果。

| 工具 | 入参要点 | 出参 |
|---|---|---|
| `project_create` | `name`（必填、同名拒绝）、`note?` | 项目（含 ID）+ 「下一步带 project」提示 |
| `project_list` | — | 项目列表（含节点计数、更新时间） |
| `project_remove` | `id`, `confirm=true` | 删除结果；非空项目拒绝 |
| `design_artboard` | `artboard?`, `family?` | 画板清单或单个 `ArtboardSpec`（含 `contract` 契约文本） |
| `design_fonts` | `script?=latin/hans/mono` | 字体清单（含授权标记）+ 默认栈 + 授权策略 |
| `design_icons` | `query?`, `group?`, `format?=sprite/inline` | 图标清单（名字/分组/别名/HTML） |
| `design_systems` | — | 体系清单（id/名称/风格要点/主题/组件数/默认画板） |
| `design_tokens` | `system`, `theme?=light`, `category?=all/color/type/spacing/radius/shadow/motion` | Token 全量或分类 |
| `design_components` | `system`, `component?`（单个则含 HTML 片段） | 组件清单/详情/用法守则 |
| `design_search` | `query` | 跨体系命中清单（体系/组件/Token） |
| `design_preview` | `system`, `components[]`, `title?`, `theme?=light`, `artboard?`, `addToCanvas?=true`, `tags?` | HTML 文档（路径+节选）、节点 ID、画板 ID、画布 URL |
| `design_screen` | `system`, `template?=login/settings/feed/profile`, `theme?`, `app_name?`, `artboard?`, `addToCanvas?=true` | 整页预览 HTML、节点 ID、用到组件 |
| `design_compare` | `system_a`, `system_b`, `theme?`, `addToCanvas?=false` | 双体系 Token diff（颜色/字型/间距/圆角/阴影/动效）+ 可选对比板节点 |
| `canvas_add` | `title`, `html`, `artboard?`, `width?`, `height?`, `x?`, `y?`, `kind?=note`, `tags?` | 节点 ID + 画布 URL（给出画板时宽高取画板） |
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

### 3.4 规范层：画板 / 字体 / 图标

这三者是「设计前置约束」，各一个注册表 + 一个 MCP 查询工具。
共同设计原则：**数据在 `internal/design`，由 `buildDoc` 内联进每份文档**——
因为预览是 `srcdoc` 文档、截图走 `foreignObject` 序列化，任何外链资源都不可靠。

#### (1) 画板 `artboard.go`（对齐问题 2）

```go
type Artboard struct {
    ID, Name, Family        string
    Width, Height           int  // @1x 逻辑像素
    StatusBar, SafeTop, SafeBottom, NavBar, TabBar int
    Columns, Margin, Gutter int
    MaxContent              int  // 内容区上限（Web 用）
    DPR                     []int
}
type ArtboardSpec struct {  // 推导值 = 尺寸契约
    Artboard
    ContentWidth, BodyHeight, ContentHeight int
    ExportSizes []string  // ["@2x 786×1704", …]
    Contract    string    // 给人/Agent 读的一段话
}
```

- 8 块：`iphone-15`(393×852) / `iphone-se`(375×667) / `android-360` / `mp-wx`(375×812) /
  `ipad-768` / `web-1280` / `web-1440` / `desktop-app`。
- **命名**：`canvas.KindFrame` 已是「画布分组框」，画板一律叫 `Artboard`，不要复用 Frame。
- `ResolveArtboard(id, family)`：显式 ID 优先，否则按 family 取默认（全仓唯一默认来源）。
- `PageCSS(a)` 输出 `--ds-ab-*` 变量，`.ds-page` 用它取宽度与内边距（不再写死 560px）。
- 高度策略：组件预览由客户端实测 `.ds-page` 高度回写（`Node.AutoHeight`）；
  整页模板直接取 `BodyHeight`，一张卡 = 一块画板。

#### (2) 字体 `fonts.go`（对齐问题 3）

```go
type FontSpec struct {
    ID, Family, Script string
    License            FontLicense // OFL / Apache / ISC / vendor-free / system-only / proprietary / unknown
    Commercial, Embeddable, Redistributable, Attribution bool
}
```

- `DefaultStacks`：`latin = Inter + Noto Sans SC + system-ui`，全部 OFL、可嵌入可再分发，
  保证在无 PingFang/雅黑的 Android/Linux/CI 上排版一致。
- 品牌字体：抖音美好体是 OFL（可再分发）；MiSans 免费商用、可嵌入但**禁止再分发字体文件**
  （`Redistributable=false` + `Attribution=true`）。
- `StackCommercial` / `StackReproducible` 给走查用：专有字体 fail，仅系统字体 warn。
- 禁止 `@font-face` 外链（截图取不到），品牌字体只按本地 family 名引用。

#### (3) 图标 `icons.go` + `icon_paths.go`（对齐问题 1、5）

```go
type Icon struct { Name, Group string; Tags []string; Body string; Filled bool }
func IconSprite() string                 // 全部 <symbol>，注入文档 <body> 首部
func IconRef(name string, size int) string
func ExpandIcons(s string) string        // 展开模板里的 {{icon:name:size}}
```

- 38 个图标、24 网格、1.8 描边、`currentColor`（自动继承 Token 色）。
- 分组：`arrow / action / status / nav / media / data`；名字扁平语义（`close` / `check` /
  `chevron-right`），分组放 `Group` 字段，历史字符符号放 `Tags`（`design_icons?query=✕`）。
- 组件模板写 `{{icon:check:12}}`，渲染时展开 —— 保持模板可读、可 diff，且不会被
  字符串拼接淹没。
- 画布 UI 侧的 `static/icons.js` 由 `go generate ./internal/design` 从同一份路径表生成，
  两侧不漂移。
- 走查「图标与符号」扫描 emoji/dingbat 与未解析的图标名，命中即 fail。

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

- `internal/design`：注册表完备性（8 体系、Token 非空、颜色合法 hex）、
  StyleSheet/Compose 生成物包含关键 token 值、组件 HTML 结构快照断言；
  新增规范层不变量：图标命名/路径/精灵覆盖、画板推导值、字体全体系可商用、
  **渲染产物无 emoji 与字符图标**。
- `internal/canvas`：Store CRUD/持久化/并发、HTTP API 全路径（httptest）、
  截图任务闭环（模拟 UI 回传）/超时路径。
- `internal/mcpserver`：内存 transport 走 `initialize` + `tools/list` +
  `tools/call`（design_tokens、design_preview、canvas_add、canvas_screenshot
  超时路径），断言结果结构；目录类工具与画板透传单测。
- 前端：`go generate` 同步 icons.js；用无头 Chromium 做真实输入事件回归
  （拖动跨越 iframe 区域不丢事件、检查模式 A↔B 跨卡片配对、截图链路出图）。
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
