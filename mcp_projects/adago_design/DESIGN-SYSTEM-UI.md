# 画布 UI 设计系统 · 优化改造文档

> 版本：v1（2026-11-08）
> 范围：`mcp_projects/adago_design` 的**浏览器画布界面**（工具条、左侧双浮窗、检查器、项目管理弹层、
> 空态/提示、检查标注层、截图输出）。八套设计体系预览文档（`ds-*`）不在本轮范围内。
> 定位：对齐仓库 `docs/19-ued.md`（Black Swan 纸墨、令牌单源、单色品牌、克制）的**工具型界面**落地版。

---

## 0. 一句话结论

现有画布 UI 的**信息架构是对的**（工具条 + 左双浮窗 + 右检查器 + 无限画布），但**视觉层没有设计系统**：
39 个十六进制色、450 个 px 字面值、13 种圆角、6 档字号（含 8px）、5 处原生 `prompt/confirm`、
0 处 `prefers-reduced-motion`、主操作在被禁用色上做白字（4.35:1 不合格）。
本轮把它收敛成**一套令牌驱动的纸墨界面**：纸面底 + 墨黑 chrome + 单一品牌色（墨）+ 语义状态色，
颜色/圆角/字号/间距全部走令牌，交互组件（弹层/菜单/toast/分段）统一形态，并加机器门禁防止回退。

---

## 1. 现状审计（带证据）

### 1.1 度量

| 指标 | 现状 | 证据位置 |
|---|---|---|
| `canvas.css` 十六进制色值 | **39** | `static/canvas.css` |
| `canvas.css` `rgba()` 字面值 | **16** | 同上 |
| `canvas.css` px 字面值 | **450** | 同上 |
| `border-radius` 不同取值 | **13** 种（4/5/6/7/8/9/10/12/14/18/50%/999/0-0-12-12） | 同上 |
| `font-size` 不同取值 | **6** 档（8 / 10 / 11 / 12 / 13 / 14px） | 同上 |
| `canvas.js` 十六进制色值 | **11** 处（连线箭头、截图底色、便签、标注） | `static/canvas.js` |
| `inspect.js` 十六进制色值 | **9** 处（A/B 标注色、关系文字色） | `static/inspect.js` |
| JS 模板内联 `style=` | **7** 处（体系详情面板） | `canvas.js:1324-1339` |
| 原生 `prompt()` / `confirm()` | **5** 处（重命名、删项目、画布尺寸、清空、便签） | `canvas.js:1215,1230,1556,1699,1713` |
| `prefers-reduced-motion` | **0** 处 | `canvas.css` |
| `:focus-visible` 规则 | **3** 条（只覆盖工具条按钮与两个输入框） | `canvas.css:154,381` |
| `z-index` 魔法数 | 10 个（5/20/32/34/40/50/80/81/9999/2147483000/2147482000） | `canvas.css` |
| 主题持久化 | **无**（`btn-theme` 只改属性，刷新丢失） | `canvas.js:1693` |
| `-theme` 启动参数 | **已解析但从未生效**（`Config.Theme` 无任何读取方） | `internal/config/config.go:67` |
| 禁用品牌色 | `--accent2:#25f4ee` 定义后**无人使用** | `canvas.css:4` |

### 1.2 对比度实测（WCAG 2.1 相对亮度公式）

| 前景 / 背景 | 比值 | 判定 |
|---|---|---|
| `#ffffff` on `--accent #7c5cff`（主按钮、`tb-active`、`.comp-item` hover） | **4.35** | ✗ 正文/12px 需 ≥ 4.5 |
| `--accent #7c5cff` on light `--bg #f2f3f7`（链接/当前项文字） | **3.92** | ✗ |
| `#ffffff` on `#f59e0b`（`group-label` 项目标签） | **2.15** | ✗✗ 严重 |
| `#ffffff` on `#d97706`（`group-label:hover`） | **3.19** | ✗ |
| `--bad #fe2c55` on light `#ffffff`（错误 toast 文字） | **3.68** | ✗ |
| `--fg2 #6b7080` on light `#f7f8fa`（次要文字） | 4.64 | ✓（勉强） |
| `--fg #e8e9ee` on dark `#0f1014` | 15.68 | ✓ |

> 结论：凡「白字压在彩色底」的地方全部不合格——根因是**没有 `on-brand` 语义令牌**，
> 于是每个彩色底都写死 `color:#fff`，一旦底色换成亮色系就必然失败。

### 1.3 结构性问题（比配色更影响观感）

1. **色彩噪音**。界面同时出现紫（品牌）、青（`accent2`，未使用）、橙（项目标签）、绿（连接状态）、
   红（危险），再加八套体系卡片各自的品牌色 chip。工具 chrome 也在「炫技」，与预览内容抢注意力。
2. **几何没有网格**。圆角 13 种、字号 6 档、间距随手写（`padding:6px 8px` / `5px 9px` / `1px 7px` …），
   同一层级的控件高度不一（28/30/24/22/20/16）。
3. **层级没有体系**。`z-index` 全是魔法数，新增一层要翻整份文件确认「会不会压住 tooltip」。
4. **原生对话框打断设计语言**。`prompt()/confirm()` 由浏览器绘制，字体、圆角、语气全部脱离体系；
   且 `prompt` 在部分环境被静默禁用 → 删除项目、便签直接不可用。
5. **可访问性缺口**。全局只有工具条按钮有焦点环；左侧树行是 `div` 无 `role`/无 Tab 可达；
   树形展开按钮无 `aria-expanded`；toast 无 `aria-live`；弹层无 Tab 陷阱与焦点归还。
6. **JS 复制 CSS 常量**。`leftDockWidth()` 返回 292、`visibleRect()` 写 288/64/12、`fitView()` 写
   296/320/148 —— 这些全是 CSS 里的布局常量副本，改一处样式就要改三处 JS，`fitView` 居中会漂移。
7. **主题链断掉**。`data-theme` 硬编码在 `index.html`、`-theme` 参数无读取方、切换不持久化、
   截图导出把 `#7c5cff/#16171d` 写死 —— 浅色主题下导出的连线仍是深色主题色。

---

## 2. 设计决策

### 2.1 品牌：Black Swan 纸墨（单色品牌）

| 决策 | 取值 | 理由 |
|---|---|---|
| 界面底（画布工作区） | 浅 `#EDE8DC` / 深 `#14151A` | 与「稿纸」同源；节点卡片是白色预览，必须与背景拉开明度差 |
| 浮窗/工具条面 | 浅 `#FBF8F1` / 深 `#1C1E24` | 纸面层，比画布亮一档 |
| 主文字 | `#16171A` / `#F2EFE7` | 墨黑 × 宣纸，16:1 以上 |
| 品牌/交互强调 | **墨黑本身**（浅色用墨、深色用纸） | 单色品牌：选中、主按钮、激活态一律「墨底纸字 / 纸底墨字」 |
| 状态色 | success/warning/danger **成对 soft + ink** | 只表达状态，不做装饰；成对使用保证 4.5:1 |
| 检查标注 A/B | 浅 `#0B5FFF`/`#C81E48`，深 `#6E9BFF`/`#FF7D96` | A/B 是**数据编码色**（同语法高亮的定位），不算品牌色；必须与体系肤色无关 |
| 项目分组标签 | 由橙色块改为**纸墨中性胶囊** | 橙色 2.15:1 不可读，且是界面里唯一的第三种彩度 |

**为什么单色**：画布上同时存在 8 套体系 × 明暗 × 组件预览，它们自带品牌色。工具 chrome 一旦有彩色，
同屏出现两套「谁是主角」的竞争。chrome 退成纸墨后，预览卡成为唯一彩色源，对比更锋利；
这也与仓库 `19-ued.md` §19.1「克制：单色品牌、无渐变、无发光」一致。

### 2.2 补齐 UED 缺失的语义令牌

`19-ued.md` 的令牌清单（`--ph-*`）是**宿主页面**的；本模块是自包含 Go module（`GOWORK=off`），
不能 import 宿主的 `tokens.css`。因此本轮建立**同构的本地令牌层** `--ui-*`，
语义命名与 `--ph-*` 一一对齐（canvas/surface/elevated/subtle/line/ink/muted/faint/brand/on-brand/
success-\*/warn-\*/danger-\*/focus/scrollbar/motion），便于未来并库时改名即可。

命名约束：

- 语义令牌 `--ui-*`：只允许出现这组名字 + 状态后缀，组件 CSS **不得**出现原始色值。
- 标尺令牌 `--ui-space-*` / `--ui-radius-*` / `--ui-text-*` / `--ui-z-*` / `--ui-dur-*`：尺寸的唯一定义处。
- 数据色 `--ui-ins-a` / `--ui-ins-b`：仅检查标注使用，允许与主题联动。

---

## 3. 令牌规格（实现即此表）

### 3.1 尺寸标尺

| 组 | 档位 | 用途 |
|---|---|---|
| 间距 `--ui-space-*` | 2 / 4 / 6 / 8 / 10 / 12 / 16 / 20 / 24 / 32 | 偶数网格（2 的倍数），奇数档不收 |
| 圆角 `--ui-radius-*` | `xs`2 / `sm`4 / `md`6 / `lg`8 / `xl`10 / `2xl`12 / `full`999 | 旧的 13 种收敛为 7 档 |
| 控件高度 `--ui-control-*` | `sm` 24 / `md` 28 / `lg` 32 | 工具条按钮=md，主按钮=lg，行内小按钮=sm |
| 字号 `--ui-text-*` | `xs` 11 / `sm` 12 / `md` 13 / `lg` 15 / `xl` 17 | **8px 与 10px 废止**，最小 11px |
| 行高 `--ui-lh-*` | `tight` 1.25 / `base` 1.5 / `relaxed` 1.7 | — |
| 描边 `--ui-border-w` | 1 / 2 | 2 只用于焦点环与选中框 |
| 层级 `--ui-z-*` | `paper`0 / `edge`1 / `label`6 / `panel`20 / `solo`32 / `toolbar`34 / `toast`40 / `popover`45 / `modal`50 / `rel`60 / `debug`9999 | 取代 10 个魔法数 |
| 动效 `--ui-dur-*` | `fast` 120ms / `base` 180ms / `slow` 240ms；`--ui-ease` = `cubic-bezier(.2,.7,.3,1)` | 只做状态反馈 |

布局常量（同时供 JS 读取，禁止 JS 复制数值）：

| 令牌 | 值 | 使用方 |
|---|---|---|
| `--ui-gutter` | 12px | 浮窗/工具条外边距，JS 计算可见区 |
| `--ui-dock-w` | 280px | 左侧浮窗宽度 |
| `--ui-inspector-w` | 288px | 检查器宽度 |
| `--ui-toolbar-h` | 44px | 工具条高度（含外距=56 → `--ui-top-safe`） |
| `--ui-node-head-h` | 32px | 节点标题栏高度（`HEAD_H` 的唯一定义处） |

几何常量（尺寸/模糊/图标档位；`canvas.css` 与 JS 内联样式只引用，不写裸值）：

| 组 | 令牌 | 值 | 用途 |
|---|---|---|---|
| 模糊 | `--ui-blur-toolbar` / `--ui-blur-panel` | 10 / 12px | `backdrop-filter` |
| 图标 | `--ui-icon-12` … `--ui-icon-24` | 12 / 14 / 16 / 20 / 24px | `.ds-icon--*` |
| 网格 | `--ui-grid-cell` / `--ui-grid-bleed` | 40 / 2px | 画布网点与纸张网格 |
| 菜单 | `--ui-menu-min-w`、`--ui-tb-menu-*`、`--ui-tip-max-w` | 156 / 268·360·440 / 240px | 更多菜单、工具条下拉、气泡 |
| 弹层 | `--ui-modal-w` / `--ui-ask-w` / `--ui-toast-max-w` / `--ui-empty-max-w` | 580 / 420 / 560 / 440px | 模态、询问、toast、空态 |
| 单画布条 | `--ui-solo-max-w` / `--ui-solo-title-max-w` / `--ui-solo-proj-max-w` | 760 / 230 / 160px | 聚焦条 |
| 控件 | `--ui-control-xl` / `--ui-zoom-min-w` / `--ui-field-min-w` / `--ui-textarea-min-h` | 40 / 44 / 150 / 76px | 面板头、缩放标签、输入框 |
| 其他 | `--ui-edge-label-stroke` / `--ui-focus-offset` | 4 / 2px | 连线标签描边、焦点环偏移 |
| iframe 内联 | `--ui-inspect-border-w` / `--ui-inspect-label-max-w` / `--ui-note-min-h` | 1.5 / 260 / 120px | 标注层与便签（srcdoc 启动时内联） |

### 3.2 颜色令牌与对比度

| 令牌 | 浅色（纸） | 深色（墨纸） | 对比度 |
|---|---|---|---|
| `--ui-canvas` | `#EDE8DC` | `#14151A` | — |
| `--ui-surface` | `#FBF8F1` | `#1C1E24` | — |
| `--ui-elevated` | `#FFFDF8` | `#23262E` | — |
| `--ui-subtle` | `#F2EDE2` | `#242730` | — |
| `--ui-line` | `#DCD5C6` | `#2E313A` | — |
| `--ui-line-strong` | `#C9C0AC` | `#3D414C` | — |
| `--ui-ink` | `#16171A` | `#F2EFE7` | 16.17 / 15.60 |
| `--ui-ink-2` | `#57544C` | `#B9B5AA` | 6.82 / 8.13 |
| `--ui-ink-3` | `#6E6A60` | `#9A9588` | 4.87 / 5.57 |
| `--ui-brand` / `--ui-on-brand` | `#16171A` / `#F6F3EC` | `#F2EFE7` / `#16171A` | 16.17 / 15.60 |
| `--ui-brand-soft` | `#E7E1D2` | `#2A2D35` | — |
| `--ui-success-soft/ink` | `#E7F3EA` / `#1F7A3D` | `#1B2A22` / `#63D68E` | 4.71 / 8.24 |
| `--ui-warn-soft/ink` | `#FBEEDA` / `#8A5300` | `#2B2517` / `#F2B84B` | 5.53 / 8.50 |
| `--ui-danger-soft/ink` | `#FBE9E7` / `#B3261E` | `#2E1B1D` / `#FF8A93` | 5.58 / 7.21 |
| `--ui-focus` | `#16171A` | `#F2EFE7` | 焦点环 2px + 2px offset |
| `--ui-ins-a` / `--ui-ins-b` | `#0B5FFF` / `#C81E48` | `#6E9BFF` / `#FF7D96` | 5.13 / 5.62（白底） |
| `--ui-scrim` | `rgba(22,23,26,.36)` | `rgba(8,9,12,.62)` | — |
| `--ui-shadow-1/2/3` | 1px/8px/24px 三级 | 同上加深度 | 3 级取代 6 个散装阴影 |

> 「对比度」列按 WCAG 2.1 公式实测（`scripts` 见第 8 节门禁）；**改动任一亮度必须重跑门禁**。

### 3.3 状态与密度

- 编辑器级密度：正文 13px、说明 12px、微标 11px；行高 1.5。
- 控件统一 `height` + `padding-inline` + `border-radius` 三元组，禁止用 `line-height` 撑高度。
- 悬停只改 `background`/`border-color`/`color`，不改尺寸（避免抖动）。
- 过渡只挂在 `background/border-color/color/transform/opacity`，时长 ≤ 240ms。

---

## 4. 组件规格

| 组件 | 规格要点 |
|---|---|
| 工具条按钮 `.tb-btn` | 32×32（`md` 控件高），图标 16px，`radius-md`；默认透明底；hover `subtle`；激活 = 墨底纸字；`aria-label` + `data-tip` 气泡；focus-visible 2px 焦点环 |
| 品牌位 `.tb-brand` | 22×22 墨底纸字方块（去紫），字重 700 13px；窄屏隐藏 |
| 工具条 `.tb` | 面 `surface` + 1px `line` + `shadow-2`，`radius-2xl`；拖动时关闭 `backdrop-filter`（性能，保留） |
| 浮窗 `.dock` | 面 `surface`，`radius-2xl`，1px line；标题条高 40，标题 13/700 + 图标 14 灰；收起只留标题条 |
| 树行 `.tree-row` | 高 30，`radius-md`，hover `subtle`，当前项 `brand-soft` 底 + 墨字；`role="treeitem"`、`aria-expanded`、Tab 可达 |
| 体系卡 `.sys-card` | 面 `elevated`，1px line，`radius-xl`；hover 只加 `line-strong`（不再紫边） |
| chip `.chip` | 11px，`radius-full`，`subtle` 底 + `ink-2` 字；画板 chip 用 `brand-soft` + `ink` |
| 菜单 `.menu` | `elevated` + `shadow-3` + `radius-xl`，`padding-1`；行高 30；当前项 `brand-soft`；键盘 ↑↓ + Enter |
| 弹层 `.modal` | 遮罩 `scrim` + 模糊 8px；盒 `elevated`，`radius-2xl`，宽 `min(560px, 92vw)`，`shadow-3`；四要素（点外/Esc/Tab 陷阱/焦点归还） |
| 输入 `.field` | 高 32（`lg`），`subtle` 底 + 1px line，`radius-md`；focus 换 `brand` 描边 + 焦点环；placeholder 用 `ink-3` |
| 分段 `.seg` | 外层 line 描边 `radius-full`，内段 28 高；激活 = 墨底纸字 |
| toast `.toast` | 底部居中胶囊，`elevated` + line2；普通 2.4s / 错误 4.2s（UED §19.5.3）；`role=status`/`aria-live=polite`，错误用 `role=alert`；进出 `translateY(8px)`，`reduced-motion` 下只切 `display` |
| 检查器 `.insp` | 宽 `--ui-inspector-w`，面 `surface`，左侧 1px line；分区标题 11px/700 letter-spacing .6；规格标签等宽 11px |
| 空态 `.empty` | 图标 32 + 标题 15/700 + 说明 12/1.7 + 行动按钮（有项目时给「打开设计体系」） |
| 检查标注 A/B | 框 1.5px、`ins-a`/`ins-b`；胶囊 11px 白字压在 `ins-a`/`ins-b`（实测 ≥5.1）；跨卡关系字号 11 |

---

## 5. 交互与可访问性（本轮修）

1. **废除原生对话框**：新增 `uiAsk()` / `uiConfirm()`，用体系弹层（点外/Esc/焦点陷阱/焦点归还），
   `prompt` 文本域 + `confirm` 危险按钮；`canvas.js` 里 5 处调用全部替换。
2. **toast 语义化**：容器 `role="status" aria-live="polite"`，错误切 `role="alert"`；
   时长 2.4s/4.2s；`prefers-reduced-motion` 时不动画。
3. **焦点环全覆盖**：`*:focus-visible` 兜底 + 组件级 2px `--ui-focus` + 2px offset；
   树行/菜单项/分段/图标按钮全部进入焦点清单。
4. **键盘**：菜单 ↑↓/Enter/Esc；树行 Enter 切换、Space 展开；弹层 Tab 陷阱；Esc 就近关闭（沿用现有优先级链）。
5. **`prefers-reduced-motion: reduce`**：一条全局规则把 `--ui-dur-*` 归零并关闭 `backdrop-filter` 动画。
6. **`@media (hover:none)`**：常驻 `node__del`、`tree__more` 等 hover 才出现的操作。
7. **触屏/窄屏**：`<980px` 隐藏品牌文字（已有），新增 `<760px` 浮窗整体收为图标条。
8. **主题**：`-theme` 参数生效（缺省改 **light 纸面**），`localStorage adago.theme` 持久化，
   `index.html` 内联 bootstrap 在 CSS 之前应用主题（无闪白）；`btn-theme` 写 localStorage。
9. **单一布局真源**：JS 用 `getComputedStyle(document.documentElement)` 读 `--ui-dock-w`、
   `--ui-inspector-w`、`--ui-gutter`、`--ui-node-head-h`，删掉 `292/288/296/320/148/64/12/HEAD_H` 副本。
10. **截图跟随主题**：连线/标签/背景色从令牌读（`--ui-brand`/`--ui-ink`/`--ui-surface`），
    不再写死 `#7c5cff`/`#16171d`。

---

## 6. 实施批次与文件

| 批次 | 内容 | 文件 |
|---|---|---|
| A | 新增令牌层（明暗双主题 + 标尺 + 布局常量） | **新增** `internal/canvas/static/tokens.css` |
| B | `canvas.css` 全量改写为令牌驱动（零色值、零字号字面、圆角/字号全部走 var） | `static/canvas.css` |
| C | 结构：`tokens.css` 先于 `canvas.css`；主题 bootstrap；`role`/`aria`；弹层骨架 | `static/index.html` |
| D | 行为：主题持久化、`uiAsk/uiConfirm`、toast 语义与时长、布局度量读令牌、截图读令牌、详情面板去内联 style | `static/canvas.js`、`static/inspect.js` |
| E | 配置：`-theme` 真正生效（服务端注入 `data-theme`），默认改 light | `internal/config/config.go`、`internal/canvas/server.go`、`internal/app/app.go`、`main.go` |
| F | 门禁：令牌完整性 + 对比度 + 禁字面值 + 结构断言 | **新增** `internal/canvas/ui_tokens_test.go` |
| G | 文档：`DESIGN.md` §4.4 增「界面设计系统」、`README.md` 增令牌一节 | `DESIGN.md`、`README.md` |

不改动：`internal/design/*`（8 套体系与预览皮肤）、MCP 工具契约、HTTP API、数据结构。

---

## 7. 验收

### 7.1 视觉验收（真机截图）

| 用例 | 通过条件 |
|---|---|
| 浅色 1600×1000 / 1366×768 | 纸面底、墨字、单色 chrome；无紫/青/橙残留；主按钮墨底纸字 |
| 深色同上 | 墨纸底；浮窗与画布明度分层可见；连线/标签可读 |
| 项目标签（全部视图） | 中性胶囊，白字对比 ≥4.5（实测目标 ≥7） |
| 检查模式 A/B | 蓝/红编码色与 8 套体系肤色互不干扰；胶囊文字 ≥4.5 |
| 空态 | 图标 + 标题 + 说明 + 行动点；不再只有两行灰字 |
| 弹层 | 遮罩/圆角/阴影与体系一致；Esc 与点外可关；焦点可见 |
| 截图导出 | 浅色主题导出浅色连线/标签，深色导出深色（不再混色） |

### 7.2 机器门禁（`ui_tokens_test.go`）

```bash
GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...
```

断言清单：

1. `tokens.css` 同时定义明/暗两套，且第 3.2 节全部语义令牌齐全（缺一即 fail）。
2. 对比度：`ink/canvas`、`ink-2/surface`、`ink-3/surface`、`on-brand/brand`、
   `success-ink/success-soft`、`warn-ink/warn-soft`、`danger-ink/danger-soft` ≥ 4.5（Go 侧按 WCAG 公式验算）。
3. `canvas.css` 内不得出现 `#hex` / `rgb(` / `rgba(`；`font-size` 与 `border-radius` 不得出现 px 字面值。
4. `static/**/*.js`（含 `js/` 下 12 个职责模块与 `inspect.js`）不得出现 `#hex`
   （`icons.js` 为生成物，路径数据无颜色，一并纳入扫描）。
5. 画布 JS（聚合扫描）不得出现 `prompt(` / `confirm(`，且必须包含
   `uiAsk` / `uiConfirm` / `isAskOpen` 的定义。
6. `index.html` 中 `tokens.css` 必须先于 `canvas.css`；含 `data-theme` 初始值。
7. `canvas.css` 必须有 `prefers-reduced-motion` 与全局 `:focus-visible` 规则。

### 7.3 兼容性

- 类名与 DOM 结构、元素 id 全部保留（`canvas.js`/`inspect.js` 的行为不变）；
  新增只有：`tokens.css`、弹层 DOM（`#ask-modal`）、若干 `aria` 属性。
- `canvas.json` 数据结构不变。已生成节点 HTML 固化在内，不受界面主题影响。
- 默认主题由 dark 改为 light 属**有意的观感变更**，记录在本文与 `DESIGN.md`；
  既有用户的一次性切换可通过 `-theme dark` 或界面按钮恢复，并持久化。

---

## 8. 风险与取舍

| 风险 | 取舍 |
|---|---|
| 单色 chrome 可能显得「不够工具感」 | 用 2px 焦点环、墨底激活态、三级阴影补足层次；颜色留给预览内容 |
| 浅色为默认会让老用户觉得「变了」 | 与 UED 缺省态一致；一次切换即持久化；`-theme dark` 可固定 |
| 450 个 px 字面值无法一次全部令牌化 | 只强制「颜色/圆角/字号/层级」走令牌，间距归到偶数网格并在文档登记 |
| 门禁禁止 hex 可能误伤渐变/描边需要 | 允许 `color-mix(in srgb, var(--ui-*) …)` 与令牌组合，仍属令牌驱动 |

---

## 9. 完成定义（DoD）

- [x] 审计度量与对比度证据（第 1 节）
- [x] 令牌表（第 3 节，含实测对比度）
- [x] 组件与交互规格（第 4、5 节）
- [x] 批次 A–E 落地，`go build/vet/test` 全绿
- [x] 门禁 `ui_tokens_test.go` 通过（8 组断言）
- [x] 明暗 × 两种宽度真机截图通过（第 7.1 节）
- [x] `DESIGN.md` / `README.md` 同步，`19-ued.md` 追加变更记录

---

## 10. 实施结果（2026-11-09）

### 10.1 改动清单

| 文件 | 变更 |
|---|---|
| **新增** `internal/canvas/static/tokens.css` | 明暗两套语义令牌 + 排版/间距/圆角/控件/布局/层级/动效标尺 |
| `internal/canvas/static/canvas.css` | 全量重写为令牌驱动：**0 个十六进制色值、0 个 rgb 函数、0 个字号/圆角 px 字面值、0 个层级魔法数**（原 39 / 16 / 6 档 / 13 档 / 10 个）；`var(--ui-*)` 引用 864 处，裸 px 字面从 450 降到 48（余下全是 `1px` 描边与少数固定布局宽度） |
| `internal/canvas/static/index.html` | `tokens.css` 先于 `canvas.css`；主题 bootstrap 脚本；`data-theme` 占位；`role`/`aria` 补全；新增 `#ask-modal` 骨架 |
| `internal/canvas/static/canvas.js` | 新增 `tokenPx/tokenColor/UI` 令牌读取、`uiAsk/uiConfirm/isAskOpen`、`focusables`；5 处原生对话框全部替换；toast 语义与时长；`fitView/leftDockWidth/visibleRect` 改读令牌；截图/便签取色改读令牌；空态三件套；树行 `role/aria/tabindex` + 键盘导航 |
| `internal/canvas/static/inspect.js` | A/B 标注色改读令牌（含 `--ui-on-ins` 深色反白）；标注层样式去写死色值；换主题重挂（`adago:theme-change`） |
| `internal/canvas/server.go` | 新增 `Theme` 字段、`ServeIndex` 注入主题（占位符替换）、`NormalizeTheme` |
| `internal/app/app.go` | 装配时把 `cfg.Theme` 传给画布服务 |
| `internal/config/config.go` | `-theme` 缺省改 `light`（白名单收敛） |
| `main.go` | `-theme` 帮助文案更新 |
| **新增** `internal/canvas/ui_tokens_test.go` | 8 组机器门禁 |
| `DESIGN.md` / `README.md` / `19-ued.md` | 规范与变更记录同步 |

### 10.2 门禁实测

```bash
GOWORK=off go build ./...   # ok
GOWORK=off go vet ./...     # ok
GOWORK=off go test ./...    # ok（canvas 3.8s / design 0.4s / mcpserver 18.6s）
```

| 断言 | 结果 |
|---|---|
| `TestUITokensComplete` | 24 个语义令牌 × 明暗两套齐全 + 5 个布局常量 |
| `TestUITokenContrast` | 11 组前景/背景 × 2 主题全部 ≥ 4.5（含 `--ui-on-ins` 对 A/B） |
| `TestCanvasCSSUsesTokensOnly` | `canvas.css` 零色值/零字号字面/零圆角字面/零层级魔法数 |
| `TestCanvasCSSAccessibilityRules` | `:focus-visible`、`prefers-reduced-motion`、`.empty-hint` 在位 |
| `TestCanvasJSNoLiteralColors` | `canvas.js`/`inspect.js` 零色值（`icons.js` 生成物豁免） |
| `TestCanvasJSNoNativeDialogs` | 代码行零 `prompt(`/`confirm(`，且 `uiAsk/uiConfirm/isAskOpen` 存在 |
| `TestIndexLoadsTokensFirst` | `tokens.css` 先于 `canvas.css`、`data-theme` 占位、`#ask-modal` 在位 |
| `TestServeIndexInjectsTheme` | `NormalizeTheme` 白名单收敛（light/dark/空/DARK） |

> `staticcheck` 本机二进制仍是旧版（导出数据 v2 vs 工具链 v5），无法运行；`go vet` 通过。
> 与 `IMPROVEMENTS.md §8.4` 记录的遗留一致。

### 10.3 真机验证（无头 Chromium + CDP 真实点击）

| 用例 | 结果 |
|---|---|
| 浅色 1600×1000 / 1366×768 | 纸面底 + 墨字 + 单色 chrome；主按钮墨底纸字；无紫/青/橙残留 |
| 深色 1600×1000 | 墨纸底；浮窗/画布/卡片三级明度可辨；文字 15.6:1 |
| 画布下拉菜单 | 面 `elevated` + `shadow-3`；当前项 `brand-soft`；行高 28 |
| 项目管理弹层 | 遮罩 + 6px 模糊；`radius-2xl`；Tab 陷阱；Esc/点外可关 |
| 重命名（ask 弹层） | 单行输入 32 高；Inline 校验；焦点自动落在输入框并全选 |
| 清空（confirm 弹层） | 危险按钮 `danger-soft` 底 + `danger-ink` 字；`role=dialog` |
| 检查模式 A/B（深色） | A `#6E9BFF` / B `#FF7D96`，胶囊字 `#14151A`（6.77 / 7.49）；胶囊与八套体系肤色无关 |
| 项目分组标签 | 中性胶囊 + `ink` 字，取代原橙色块（2.15:1 → 16:1） |
| 体系详情 | 色卡改为「色点 + 名字 + 值」列表（白字压白块事故消除）；字阶显示真实字号 |
| 空态 | 图标 + 标题 + 说明 + 行动按钮（新建项目/查看设计体系） |
| toast | `role=status`→`role=alert`；2.4s/4.2s；`toast-in` 入场 |
| 截图导出（深色） | 节点光栅化正常；画布底色/连线/标签色读令牌（浅色导浅色、深色导深色） |
| 控制台 | 全程无异常、无 warning |
| `-theme dark` 启动 | `/` 返回 `data-theme="dark"`；localStorage 覆盖生效 |

### 10.4 已知偏差与遗留

| 项 | 说明 |
|---|---|
| 检查标注字号 10px → 11px | 胶囊更可读，但更长；已在 A/B 与间距标注上验证不遮挡 |
| 项目分组标签由橙改中性 | 观感变化最大的单点；理由是原配色 2.15:1 不可读且是界面第三彩度 |
| 缺省主题 dark → light | 与 `19-ued.md` 缺省态一致；`-theme dark` 或界面按钮可固定 |
| `staticcheck` 不可用 | 工具链版本问题，非代码问题（见 10.2） |
| 间距/尺寸已令牌化 | `TestCanvasCSSGeometryUsesTokens` 强制 `padding/margin/gap/width/height/min-*/max-*/inset/outline-offset/background-size/backdrop-filter` 不得写裸值；`TestUITokenGeometryComplete` 锁住几何令牌清单。`canvas.css` 仅剩 `1px` 发丝线、`translate` 光学微调与 `@media` 断点三类白名单字面量 |

---

*变更：v1（2026-11-08）—— 初版：审计、决策、令牌表、组件规格、交互与可访问性、批次、验收、风险。*
*变更：v1.1（2026-11-09）—— 落地：补充第 10 节实施结果（改动清单、门禁实测、真机验证、偏差与遗留）。*
