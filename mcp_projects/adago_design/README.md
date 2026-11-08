# adago_design — 设计体系 MCP（浏览器无限画布）

一个「设计体系 + 浏览器无限画布」的 MCP 服务器：AI Agent 通过 MCP 工具读取
多套设计体系的 Design Token 与组件规范，把组件预览（真实 HTML+CSS 渲染）以
卡片形式摆到浏览器里的无限画布上，做多体系并排对比、组合页面、截图回传。

内置八套体系（Token 基于各体系公开设计规范整理，为近似实现）：

| id | 体系 | 主色 | 主题 |
|---|---|---|---|
| `apple` | Apple HIG (iOS) | systemBlue `#007AFF` | light / dark |
| `volcano` | 火山引擎 Arco Design | arcoblue-6 `#165DFF` | light / dark |
| `douyin` | 抖音设计体系 | 抖音红 `#FE2C55` + 电光青 `#25F4EE` | light / dark |
| `xiaomi` | 小米 MIUI / HyperOS | 小米橙 `#FF6900` | light / dark |
| `element` | Element Plus | primary `#409EFF` | light / dark |
| `ant` | Ant Design 5 | daybreak `#1677FF` | light / dark |
| `tdesign` | 腾讯 TDesign | brand `#0052D9` | light / dark |
| `material` | Material Design 3 | 基线紫 `#6750A4` | light / dark |

组件（一份 HTML 结构 × 每体系一份皮肤 CSS）：button / input / select / switch /
checkbox / radio / slider / card / tag / badge / avatar / alert / progress /
navbar / tabbar / list / dialog / toast。

## 项目 → 规范 → 预览（设计稿必须归属项目）

一个**项目**对应一块画布作用域：画布只显示当前项目的节点，导出也按项目分。
设计稿**不允许**挂空项目 —— 这条约束拦在 `canvas.Store.Add` 里，HTTP 直连与 MCP 工具
两条写入路径都绕不过去；未建项目就新增会返回 `project_required`。

```
project_create（或 project_list 选已有）
      └─► design_preview / design_screen / canvas_add / design_audit …（都要带 project）
              └─► 画布只显示该项目；canvas_export 也只导该项目
```

> 升级说明：`canvas.json` 从 v2 升到 v3。旧数据（没有 `project_id` 的节点）在首次加载时
> 自动归入固定项目 `p-default`「默认项目（历史数据）」，不会丢稿，也不会给新稿开后门。

## 三层规范（先定尺寸 → 定体系 → 出预览）

| 规范 | 入口 | 说明 |
|---|---|---|
| 画板（尺寸） | `design_artboard` | 8 块画板：iPhone 15 / SE、Android 360、微信小程序、iPad、Web 1280/1440、桌面应用壳。给出内容区、安全区、栅格、导出倍率与「尺寸契约」文本 |
| 字体（授权） | `design_fonts` | 字体注册表带商用/嵌入/再分发三个标记；默认栈是 Inter + Noto Sans SC（OFL），全平台可复现 |
| 图标 | `design_icons` | 42 个 24 网格线性图标，`{{icon:name:size}}` 占位符在渲染时展开为内联精灵 |

三条硬规则：

1. **先定画板**。每个预览节点都带 `artboard` 字段，宽高由画板推导，不再有 420/390/780 这类魔法数。
2. **禁止 emoji 当图标**。预览文档内联 `<symbol>` 精灵，使用处写 `<svg class="ds-icon"><use href="#ds-ic-close"/></svg>`；
   `design_audit` 会扫描 emoji 与 `▾ › ✕ ◉` 等字符符号并判 fail。
3. **字体必须可商用**。系统字体只能作尾部兜底（`design_audit` 给 warn），
   `Redistributable=false` 的品牌字体（如 MiSans）不得随包分发字体文件。

图标库改动后跑一次 `GOWORK=off go generate ./internal/design`，
会从 Go 侧路径表生成画布 UI 用的 `internal/canvas/static/icons.js`，两侧永不漂移。

## 画布界面（设计系统）

画布工具蒙皮走**单色品牌 + 令牌单源**：纸面（浅色，缺省）× 墨纸（深色）两套主题，
颜色/字号/圆角/间距/层级全部来自 `internal/canvas/static/tokens.css` 的 `--ui-*` 令牌，
组件样式 `canvas.css` 里不写任何原始色值。完整规范见 [DESIGN-SYSTEM-UI.md](DESIGN-SYSTEM-UI.md)。

| 维度 | 规则 |
|---|---|
| 品牌 | 单色：浅色墨底纸字、深色纸底墨字；彩色只用于状态（成功/警告/危险）与检查标注 A/B |
| 主题 | `data-theme=light`（缺省纸面）/ `dark`（墨纸）；`-theme` 只是缺省值，界面切换写浏览器 localStorage |
| 字号 | 11 / 12 / 13 / 15 / 17（8px、10px 已废止） |
| 圆角 | 2 / 4 / 6 / 8 / 10 / 12 / full（7 档取代原 13 种） |
| 弹层 | `uiAsk()` / `uiConfirm()` 替代原生 prompt/confirm：点外关闭、Esc、Tab 陷阱、焦点归还 |
| 门禁 | `ui_tokens_test.go`：令牌完整性、明暗对比度 ≥4.5、零字面值、零原生对话框 |

## 构建

```powershell
# 推荐走仓库根脚本（GOWORK=off）
.\scripts\build-mcp.cmd

# 或手动
cd mcp_projects/adago_design
GOWORK=off go build -trimpath -o bin/adago-design.exe .
```

## 运行形态（一个二进制，两种模式）

```powershell
# 人工查看：只跑画布服务并自动打开浏览器
bin\adago-design.exe serve            # http://127.0.0.1:8788/

# Agent 接入：stdio MCP + 内嵌画布服务（不自动开浏览器）
bin\adago-design.exe mcp
```

公共参数：`-port 8788`（被占用自动 +1 探测）、`-data <目录>`（默认
`%APPDATA%\adago-design`，可被 `ADAGO_DESIGN_DATA` 覆盖）、`-open`、
`-theme dark|light`。

## MCP 工具

| 工具 | 用途 |
|---|---|
| `project_create` | **创建设计项目**（设计稿的归属容器）；同名拒绝，重复调用请改用 `project_list` |
| `project_list` | 列出项目与各自的设计稿数量；不确定 `project` 填什么时先调它 |
| `project_remove` | 删除**空**项目（`confirm=true`）；项目下仍有设计稿会被拒绝 |
| `project_canvas` | **画布版式**：按比例预设或自定义宽高设置当前画布，`free=true` 恢复自由画布；读取用 `project_list` |
| `design_artboard` | **画板（尺寸规范）**：列表 / 按 ID 取推导值；返回内容区、安全区、栅格、导出倍率与尺寸契约 |
| `design_fonts` | **字体授权**：可商用 / 可嵌入 / 可再分发标记，各脚本默认字族栈，授权策略 |
| `design_icons` | **图标库**：按名称/分组/别名检索，返回 `<use>` 引用或独立 `<svg>` |
| `design_systems` | 列出全部设计体系（含默认画板） |
| `design_tokens` | 查询 Token（system/theme/category：color/type/spacing/radius/shadow/motion） |
| `design_components` | 组件清单；指定 component 返回详情与 HTML 片段 |
| `design_search` | 跨体系搜索体系/组件/Token |
| `design_derive` | 从品牌主色派生自定义体系（10 阶色板 + 语义色，结构继承基底），持久化注册 |
| `design_register` | 注册自定义体系（Token JSON，结构缺省从基底兜底） |
| `design_audit` | 设计走查：WCAG 对比度/字号/阶梯/状态色 + 字体授权 + 图标一致性 + 尺寸规范，评分 + 报告节点 |
| `design_preview` | 用某体系把组件渲染成真实预览页（`artboard` 决定宽高、`project` 决定归属），默认自动摆上画布 |
| `design_screen` | 按模板（login/settings/feed/profile）生成整页真实预览，高度取画板 body 高 |
| `design_compare` | 两体系 Token 对比（结构化 diff）；可选生成并排对比板节点 |
| `canvas_add` | 添加任意 HTML 预览卡片（可带 `artboard` 取画板尺寸） |
| `canvas_list` / `canvas_move` / `canvas_remove` / `canvas_clear` | 画布节点管理 |
| `canvas_arrange` | 节点自动网格排版（体系分组优先） |
| `canvas_restore` | 恢复最近一次清空（内存快照） |
| `canvas_export` | 导出整块画布为独立 HTML 文件 |
| `canvas_connect` | 节点间连线（带标签的贝塞尔曲线，持久化） |
| `canvas_group` | 分组框（自动包住成员节点，垫底虚线框） |
| `canvas_screenshot` | 截图画布或单个节点，返回 PNG（需画布已在浏览器打开） |
| `canvas_url` | 获取画布访问地址 |

错误以 `isError` 结果返回并携带稳定错误码：`project_required` / `unknown_project` /
`project_duplicate` / `project_not_empty` / `edge_cross_project` / `unknown_ratio` /
`unknown_system` /
`unknown_artboard` / `unknown_component` / `capture_timeout` / `confirm_required` /
`bad_request` / `screen_failed` / `compare_failed` / `empty_canvas`。

## 接入 ada-go（stdio）

`.adago/mcp.json`（或 `~/.adago/mcp.json`）：

```json
{
  "id": "adago_design",
  "transport": "stdio",
  "command": "E:\\codes\\go_projects\\ada_go\\mcp_projects\\adago_design\\bin\\adago-design.exe",
  "args": ["mcp"],
  "enabled": true
}
```

写入后重载 mcp-client 插件即可。也可以用根脚本注册：

```powershell
.\scripts\build-mcp.cmd -Register        # 构建 + 注册 computer-use（原有行为）
.\scripts\build-mcp.cmd -Register -IncludeAdagoDesignMcp
```

## 检查模式（蓝湖式元素标注 + 双元素关联）

工具条「检查」开启元素检查模式（`body.mode-inspect` 会把预览 iframe 的
`pointer-events` 从 `none` 放开，否则父页面收不到元素事件）：

- **悬停即出信息**：虚线高亮框 + 红色元素名胶囊（`标签.类 · 宽×高`）、
  到最近兄弟元素的间距线（最多两条，如 `12px`），右侧检查器同步显示该元素规格
- **点选 + hover 对比**：单击定锚点 A 后，**移入**任意其它元素即实时得到 A↔B
  （临时 B 随鼠标走，不需要第二次点击，可跨卡片）；再点一次才把 B 固定
- 固定 B 后 hover 只更新信息、不改对比对象；再点第三个元素则锚点前移（连续对比）
- `Shift`+单击：强制替换 B；`Esc` 逐级退出（B → A → 关闭检查），`C` 清空配对

**两层标注**：

| 层 | 位置 | 内容 |
|---|---|---|
| 卡片内 | 各自 iframe 的 `.dsx-layer` | 元素边框、A/B 角标、悬停名称胶囊、就近间距、到画板四边距离 |
| 卡片间 | 父页面 `#rel-layer`（世界坐标） | A↔B 水平/垂直间距、边线对齐虚线、包含/重叠、尺寸差 |

世界坐标 = `节点 x/y + 元素在 iframe 视口内的坐标`，与画布缩放/平移完全解耦，
所以两张卡片上的元素也能连起来量。

规格面板：尺寸 / 到画板（含宽度利用率）/ 内边距 / 边框 / 圆角 / 阴影 / 背景 / 字体，
数值带 **Token 合规标记**（✓ 落在阶梯、✓≈ 2px 内近似、⚠ 偏离并给最近规范值）；
配对时切换为关联面板（间距 / 对齐 / 尺寸差 / 位移 / 包含）。面板渲染带 `panelGen`
代际号：hover 高频切换 + `ensureCatalog` 异步，旧请求回来时若代际已变就丢弃，
避免「鼠标停在 C、面板还显示 B」。

## 交互性能

- 节点位置走 `transform: translate3d`（`left/top` 恒为 0），位移交给合成器，
  避免每帧 layout 与 iframe 表面重新光栅化
- 拖动用 `pointerdown` + `setPointerCapture` + rAF 合帧；指针离开窗口也不丢
- 预览 iframe 默认 `pointer-events:none`，卡片整块可拖可选中
- 连线渲染按「脏边增量」更新（元素池化，只改 `d` 属性），空画布零开销
- 拖动期间关闭面板 `backdrop-filter`，松开后用服务端回包同步 `updated_at`
  （避免轮询误判为远端修改而重载 iframe）
- 预览高度自适应：组件预览加载后实测 `.ds-page` 高度回写（`auto_height`），
  消除卡片内滚动条与截图裁切
- 左侧拆成两块独立浮窗（`#left-dock`）：项目面板在上、设计体系面板在下，可分别收起；
  收起后只留标题条，状态分别记在 `localStorage`（`adago.projCollapsed` / `adago.sysCollapsed`）
- 滚动条统一成「透明轨道 + 6px 悬浮滑块」（面板与预览文档各一套规则）：
  默认滚动条在深色主题下是一整条灰带，还会挤占卡片版心宽度

## 项目管理（左侧项目浮窗）

左侧「项目」浮窗里是项目树，参照设计稿工具的分组列表：

```
全部        4   ⋯        ← 点「全部」回到跨项目视图；⋯ 里是管理/重排/刷新
＋ 新建项目   ⇧N
⌕ 搜索项目
› 📁 默认项目（历史数据） 2   ← 点行 = 只看该项目；点 › = 展开该项目的设计稿
   · 旧设计稿A                ← 单击定位；双击 = 单独只展示这一张
› 📁 移动端改版          2
› 📁 运营活动页          0
```

- **全部视图**：一次看所有项目的稿子，按项目分行排布，每行左侧一个橙色标签
  （`项目名 + 数量`，点它只看该项目）。标签做反向缩放，画布缩到 20% 也看得清。
- **单项目视图**：点项目行即过滤到该项目，标签隐藏；再点「全部」回来。
- **双击单独展示**：双击项目下的设计稿子项（或双击画布卡片标题栏）进入
  **单画布聚焦** —— 只留这一张稿，隐藏其它卡片、连线和分组标签并放大居中；
  顶部出现「← 返回画布 · 标题 · 项目名」条，树里对应子项高亮。
  `Esc` / 双击空白处 / 点「返回画布」退出；切换项目、清空画布、删除该稿时自动退出。
- **新建**：`＋ 新建项目` 或 `⇧N` 打开项目管理面板（名称必填、同名拒绝、可填备注），
  建完自动切到新项目。
- **管理**：`⋯ → 项目管理…` 打开面板，支持重命名、删除（仅空项目）、切换。
- **重排**：`⋯ → 按项目重新排列`，把各项目排成行（行首留 200px 标签位）。
  进入「全部」时若检测到项目包围盒重叠，会自动排一次，之后仍可自由拖动。

## 画布版式（选择 / 比例 / 居中）

工具栏靠左是画布控制组。**画布 = 项目作用域 + 一份版式**，三件事都收在这里：

> 工具条**只显示图标**：画布名、比例、适配/检查/清空等文字与当前状态都放在悬停
> tooltip 里（键盘聚焦同样可见），避免长项目名把工具条撑成多行。

- **选择画布**：文件夹图标 →「全部项目 / 项目名」下拉，每行带该项目的版式与稿数；选中即切换作用域。
  与左侧项目树共用同一状态（`localStorage` 记住上次选择），两边互相同步。
- **修改比例**：比例图标 → 自由画布 / 16:9 / 16:10 / 4:3 / 3:2 / 1:1 / 3:4 / 2:3 / 9:16 / 自定义宽高。
  比例落成具体宽高（预设长边 1080–1920，自定义 320–8192），存进项目的
  `canvas` 字段（`canvas.json`）；Agent 侧对应 `project_canvas` 工具。
- **居中展示**：准星图标；把画布矩形对齐到可视区中心（自动扣掉左侧浮窗、检查器、工具条），
  放不下时缩放到刚好放下；自由画布按全部节点包围盒居中。快捷键 `0`。

画布纸张（`#canvas-paper`）只作版式参考：**不裁剪、不拦截事件**，节点仍可拖到边界外
当草稿区。单画布聚焦（双击卡片/项目树子项）时纸张自动隐藏，居中改为对准该节点。
Agent 修改画布版式后，画布 UI 约 30s 内低频率刷新同步（无需手动刷新页面）。

## 截图原理（无 CDP）

`canvas_screenshot` 创建截图任务 → 已打开的画布页面长轮询领取 → 把目标
预览 HTML 放进离屏 iframe → `SVG foreignObject` 序列化 → `<canvas>` 光栅化
→ PNG 回传给 MCP。8 秒内没有页面响应则返回 `capture_timeout`，提示先打开
画布地址。预览内容是纯 CSS（无外链资源、图标为内联 `<symbol>` 精灵），
渲染安全不失真。

## 测试与门禁

```powershell
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go generate ./internal/design   # 改了图标后同步 icons.js
```

- `internal/design`：8 体系 Token 完备性、全部组件可渲染、图标库不变量、
  字体授权（全部体系可商用）、画板推导值、**渲染产物无 emoji/字符图标**。
- `internal/canvas`：Store CRUD/持久化/并发、HTTP API 全路径、截图任务闭环。
- `internal/mcpserver`：内存 MCP 会话走 `tools/list` + `tools/call` 契约，
  含 `design_icons` / `design_fonts` / `design_artboard` 与画板透传。

设计细节见 [DESIGN.md](DESIGN.md)；六项历史问题的分析与改造记录见
[IMPROVEMENTS.md](IMPROVEMENTS.md)。
