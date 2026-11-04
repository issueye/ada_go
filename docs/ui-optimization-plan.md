# ada_go UI 优化方案：品牌设计语言 · 交互治理 · UED 规范

> 状态：评审稿
> 日期：2026-11-03
> 关联：[19-ued.md](19-ued.md)（本方案落地的规范层）、[07-ssr-frontend.md §7.7](07-ssr-frontend.md)（复用与令牌细则）、[15-ui-components.md](15-ui-components.md)
> 已确认决策：默认色板 = 黑天鹅；主题入口 = 侧栏弹出面板（不做独立设置页）；首发色板 = 黑天鹅 + 苔绿 Moss + 深海 Ocean + 暮色 Dusk。

## 0. 现状总评

这套 UI 的基本功明显高于平均水准：渐进增强（noscript 回退、`adag_js` cookie 门控 meta refresh）、modal 焦点陷阱与焦点归还、触屏适配（`hover:none` 全覆盖）、滚动纪律（`overscroll-behavior` 全覆盖）、`prefers-reduced-motion` 全覆盖。规范意识也已萌芽（components.css 头注释即口头规范、`adag verify ui` 把守底线）。

三大结构性债务：

1. **品牌割裂**：颜色令牌三源并存（`tokens.css` 上游真源 / `app.css` 内嵌副本 / `app-local.css` 的 M2 绿色残留），层叠结果是**浅色 = 黑天鹅、暗色 = 苔绿**，品牌词「Black Swan」只活在注释里。
2. **交互债**：长会话历史不可达（只取最近 100 条且无提示）、5 类 `<summary>` 触发器无键盘焦点环、ask-wizard 可被 Enter 提前提交等 20 项（§2.2）。
3. **规范缺位**：动效 6 种时长 4 种 easing、双通知系统、两套组件方言、术语混用——没有一份成文规范裁决「什么算对」。

对应解法即本方案三主线：**§1 品牌设计语言（含主题系统重构）、§2 交互治理（审计 + 修复批次）、§3 UED 规范（已落地为 19-ued.md）**，§4 路线图串行推进。

---

## 1. 品牌设计语言

### 1.1 资产盘点

| 资产 | 现状 | 评价 |
|---|---|---|
| 命名 | `ada-go`（产品）/ `adag`（二进制与命名空间）/ `a_da`（上游指代）/ `ada-`（CSS 前缀）四种写法并存，语义不同但无成文边界 | 词根健康，需钉规则 |
| Logo | 单一 512×512 `logo.png` 复用于 favicon、rail 24px、侧栏 28px、登录 32px、新建页 40px；无 favicon.ico / apple-touch-icon / OG | 极简可接受，favicon 需补 |
| 品牌主题 | Black Swan（宣纸暖白 × 墨黑，取自 logo 天鹅主体）只存在于 `tokens.css` 注释；实际显示被 `app-local.css` 绿色令牌覆盖成「浅黑天鹅 + 暗苔绿」 | **最大问题**，§1.3 解决 |
| 文案 | 人格已成型：克制、解释型长句、交代边界（「已加载的页面不会伪造数据。」）；但「会话/任务/对话」混用、「还没有/暂无」并存 | 收敛成术语表 |
| 动效 | 有 `--ada-transition: 120ms` 令牌但执行率不满：120/140/150/160/180/200ms 六种时长、ease/ease-out/linear/cubic-bezier 四种 easing 并存 | 收敛到令牌 |
| 字体/图标 | 系统字体栈（`--ui-font`/`--mono-font`）+ lucide 风格线性图标（stroke 1.75、sprite 通道） | 良好，条文化即可 |

### 1.2 品牌问题清单

| # | 问题 | 位置 | 归属 |
|---|---|---|---|
| B1 | 令牌三源冲突：浅色黑天鹅 / 暗色苔绿混搭；`--ph-brand-ink`（黑）压在绿色 `--ph-brand-soft` 上；`--ph-faint` 覆写后浅色 10px 小字对比度 ≈4.3:1（AA 线下） | `app-local.css:8-98` vs `tokens.css:35-131` | §1.3 步骤 1 |
| B2 | 命名四种写法无规则，`a_da` 出现在用户可见链路的风险无约束 | 全仓 | 19-ued §19.2.1 |
| B3 | 512px 原图当 favicon；无 touch icon | `shell.html.tmpl:9` | 批次 C |
| B4 | 动效时长/easing 不收敛 | 各 CSS | 批次 C |
| B5 | 术语混用（会话/任务/对话、还没有/暂无、返回对话/返回会话） | `ui.go:308-311,493,504` 等 | 批次 C |

### 1.3 主题系统重构（核心工程）

**架构：两维正交。** 明暗维维持现状（`html.light`/`html.dark` 类，`theme.js` 负责）；新增调色板维 `html[data-theme="<id>"]`，由新文件负责。**不动任何上游镜像文件**（`tokens.css`、`theme.js`——07 §7.7.1 原样复制纪律），本地能力全部以新文件叠加（先例：`app.js` 窄屏补丁）。

| 步骤 | 内容 | 文件 |
|---|---|---|
| 1 | **清理双源**：删除 `app-local.css` 开头三块令牌（`:root` 绿色块 8-38、`@media prefers-color-scheme` 死代码 40-69、`html.dark` 覆盖块 71-98）；`:root` 缩减为仍在用的 `--ada-radius`/`--ada-gap` 两个旧别名 | `static/app-local.css` |
| 2 | **新建主题层**：`ui/themes.css` 定义 moss / ocean / dusk 三个色板，每板 `html.light[data-theme="…"]` + `html.dark[data-theme="…"]` 两块（特异性 0,2,1 恒胜基线 0,1,1），补齐 19-ued §19.3.2 全部 `--ph-*`；同文件内定义色板预览类 `.ada-swatch--<id> { --sw-canvas/brand/ink: … }`。moss 色值直接迁移现 `app-local.css` 绿色系，缺失令牌按绿调派生；ocean/dusk 新设计（纸面 × 单色品牌气质，对比度按 §19.3.3 验收） | `static/ui/themes.css`（新建） |
| 3 | **新建主题引导**：`themes.js`（阻塞式，紧随 `theme.js` 之后）：读 `a_da_theme_palette`（缺省 = black-swan = 移除 `data-theme` 属性）与 `a_da_theme_mode`（`light/dark/system`，缺省 system）；旧键 `a_da_theme` 迁移为 mode；mode=system 时监听 `matchMedia('(prefers-color-scheme: dark)')` change 动态翻转类。与 `theme.js` 的契约：rail 快切按钮仍走 `theme.js` 的 `[data-theme-toggle]` 委托切类写旧键，`themes.js` 的事件委托在其后注册、读取切类结果同步 `a_da_theme_mode` 与 `aria-pressed`——两者互不改代码 | `static/themes.js`（新建） |
| 4 | **侧栏面板**：`ui.go` `renderSidebar` 中 `.ada-sidebar__foot` 的主题按钮改为面板触发器（`data-theme-panel-toggle` + `aria-haspopup`），Go 生成面板 DOM `.ada-theme-pop`：上半亮/暗/跟随系统三态分段（`data-theme-mode`），下半 4 张色卡（`.ada-swatch--<id>` 类，**不用内联 style**——CSP `style-src 'self'` 与 verify 都会拦）；色板表（id、显示名、预览三色）定义在 `ui.go`，注释指向 `themes.css` 人工同步。rail 按钮（`ui.go:346-348`）保持快切不变 | `ui/go` `ui.go`、`ui/shell.css` |
| 5 | **装配**：`shell.html.tmpl` doc_head 在 tokens.css 后插入 `themes.css`、theme.js 后插入 `themes.js` | `templates/shell.html.tmpl` |
| 6 | **桌面壳对齐**：`backgroundColor` 改 `'#121110' : '#F6F3EC'`（与 tokens.css 画布逐字一致） | `desktop/main.cjs:608` |
| 7 | **文档先行**：07 §7.7.2 增补「主题 = 明暗 × 调色板」条款与镜像豁免说明；本方案与 19-ued.md 同步（已含） | `docs/` |

**不做的**：不改 `tokens.css`（上游镜像 + SHA-256）；不做独立设置页；插件 CSS 零改动（只引用令牌自动适配）。

### 1.4 其他品牌动作

- favicon.ico（32px）+ apple-touch-icon（180px）生成脚本 → 批次 C。
- 动效收敛：新增 `--ada-duration-fast: 160ms` / `--ada-duration-slow: 240ms`，存量 6 种时长逐个归位 → 批次 C。
- 术语收敛：`返回对话→返回会话`（`ui.go:308-311`）、`暂无会话→还没有会话`（`ui.go:504`）等 → 批次 C，全量按 19-ued §19.2.3 术语表执行。

---

## 2. UI/UX 交互审计

### 2.1 保持的优势（动它之前先知道什么是好的）

| 优势 | 位置 |
|---|---|
| 无 JS 降级闭环：cookie 门控 meta refresh 轮询、noscript 原生控件 | `sidebar-boot.js`、`handlers.go:140-144` |
| modal 完整焦点管理：栈、Tab 循环、焦点归还、滚动锁 | `ada-modal.js` |
| 流式反馈三层：live-status、运行 dock、工具行呼吸条 + 秒级耗时 | `chat.html.tmpl:30,109`、`components.js:234-252` |
| 滚动意图识别：用户上翻即停跟随，「回到最新」常驻 | `chat-live.js:108-136` |
| 触屏适配：`hover:none` 下常驻复制/重命名/删除 | `shell.css:391-404` 等 |
| 滚动容器纪律：全部 `overscroll-behavior: contain`，注释说明意图 | `shell.css:1308-1315` |
| Tabs roving tabindex 完整 | `components.js:371-441` |

### 2.2 问题清单（20 项）

| # | 级 | 问题 | 位置 | 批次 |
|---|---|---|---|---|
| 1 | **P1** | 对话流只取最近 100 条且无「加载更早」入口或截断提示，长会话历史静默丢失（Core 的 `HasMore/NextCursor` 被丢弃） | `handlers.go:87,166` | A |
| 2 | **P1** | 侧栏 `Running` 恒 false：运行点永不显示，运行中会话仍可删除 | `server.go:385,369-389`、`ui.go:544-560` | A |
| 3 | **P1** | ask-wizard 文本框按 Enter 提前提交整表（含未作答步骤的默认值，可能直接 `decision=accept`） | `chat.html.tmpl:326,333`、`app.js:592-635` | A |
| 4 | **P1** | 5 类 `<summary>` 触发器（＋菜单/重命名/工作区组头/行菜单/编辑重发）无 `:focus-visible`，键盘焦点不可见 | `components.css:77-83,1286`、`shell.css:405,1592`、`layout.css:210`、`app-local.css:1145` | A |
| 5 | **P1** | 令牌三源冲突（= B1） | `app-local.css:8-98` | §1.3 |
| 6 | P2 | Ctrl+K 在 modal 打开时无让路，焦点被甩到 modal 背后；Ctrl+N 对输入中用户无让路 | `app.js:690-694,772-778` | B |
| 7 | P2 | 图片查看器有 `aria-modal` 但无 Tab 陷阱、不进模态栈 | `chat-images.js:116-163` | B |
| 8 | P2 | htmx.min.js（50KB）纯摆设：唯一 API 调用的目标元素全仓不存在 | `shell.html.tmpl:25`、`components.js:276-345` | C |
| 9 | P2 | SSE 断线 3 次后只提示「请刷新页面」，无重连按钮；局部刷新失败静默 | `chat-live.js:765-772,406-408` | B |
| 10 | P2 | 双通知系统：`AdaUI.toast` 完整实现但零调用；toast/inline 三形态规则不成文 | `ada-ui.js:1060-1110`、`app.js:43-69` | B |
| 11 | P2 | 拖拽图片到 composer 无放置反馈（`.is-drop-target` 样式存在但 JS 不打类） | `components.css:1001-1004`、`chat-images.js:88-97` | B |
| 12 | P2 | 死 UI 钩子：`#topbar-status` 永空、`data-auto-submit`/`data-modal-src` 无消费方、`.ada-remote*` 整套样式无渲染方、「其它工作区」分组死路径 | `view.go:168`、`app.js:180-186`、`widgets.css:217-343`、`ui.go:516-537` | C |
| 13 | P2 | ada-select 方向键高亮无 `aria-activedescendant`；级联菜单缺 `menuitem` 角色 | `ada-select.js:101-163`、`app.js:233,303-309` | B |
| 14 | P2 | 侧栏抽屉打开时 Tab 穿透被遮罩正文；关闭后焦点不归还 | `pages.css:226-272`、`app.js:476-529` | B |
| 15 | P2 | 两套组件方言：主 UI `ada-*` vs 二级页 Tailwind 基线类（`.card/.badge/.input`）混用 | `workspaces.html.tmpl:9,29`、`extensions.html.tmpl:30-44`、`overview.html.tmpl:32-40` | D |
| 16 | P2 | transition 六种时长并存（= B4） | 各 CSS | C |
| 17 | P2 | `100vh` 与外壳 `100dvh` 混用，移动端地址栏收展时高度跳变 | `app-local.css:1747-1752,1463,1595,1603` | B |
| 18 | P2 | 窄屏 flash 遮住 corner-dock 且 6-9s 不可穿透 | `shell.css:1252-1281`、`pages.css:6-14` | B |
| 19 | P2 | 禁用/按压态偏弱：`.ada-send:disabled` 用 `cursor:default`；select option 无 active 态 | `components.css:1367-1372` 等 | C |
| 20 | P2 | 概览岛 5s 轮询整岛重渲染，用户焦点在岛内时被打断 | `islands.js:94-98` | B |

### 2.3 修复批次

- **批次 A（P1，随主题重构同阶段）**：#1 历史分页（用 Core 已有 `HasMore`，渲染「加载更早消息」或最低限度的截断提示）、#2 Running 透传（`threadSummaryDTO` 补字段；删除前 Core 校验）、#3 wizard 提前提交（非末步 Enter → next；未达步骤字段 `disabled`，即 19-ued §19.5.5）、#4 焦点环（summary 系进焦点清单 + `summary:focus-visible` 兜底）。
- **批次 B（P2 高价值）**：#6 键盘让路、#7 图片查看器并入 `<ada-modal>`、#9 SSE 重连按钮、#10 通知收敛（`AdaUI.toast` 删除或 `notify()` 迁入，规则按 19-ued §19.5.3）、#11 拖拽反馈、#13 aria 补齐、#14 抽屉陷阱与焦点归还、#17 dvh、#18 flash 避让、#20 岛轮询焦点跳过。
- **批次 C（清理与品牌小件）**：#8 htmx 移除、#12 死代码清理、#16 动效令牌归位（新增 --ada-duration-fast/slow）、#19 状态态补齐、B3 favicon/touch icon、B5 术语收敛、弹层互斥（ada-plus 与行菜单不可同开）。
- **批次 D（结构性，单独排期）**：#15 二级页组件方言统一到 `ada-*`——面广（三个页面模板 + `app.css` 依赖评估），单独成单，不夹带。

---

## 3. UED 规范

规范本体已落地 **[docs/19-ued.md](19-ued.md)**，七部分：设计原则（5 条）、品牌规范（命名体系 / 主题体系 / 术语文案 / 动效语言 / 视觉资产）、令牌体系（层次与真源 / 令牌清单 / **主题准入 checklist**）、组件与 DOM、交互模式（弹层四要素 / 快捷键表 / 通知三分 / 空态加载 / 分步表单 / 流式输出）、可访问性（WCAG 2.1 AA + 已知差距）、治理与门禁。

要点：

- **它裁决 §1/§2 里的每一项**：B1-B5、#4/#6/#7/#9/#10/#13/#14 等问题在规范里都有对应条款，修复即「向规范收敛」。
- **主题准入 checklist（§19.3.3）是新增色板的硬门禁**：40 令牌 × 明暗齐全、对比度抽查、缺省态零影响、色卡三色同步、桌面壳对齐、8 态截图走查。
- **镜像纪律成文（§19.7）**：上游镜像文件只读，本地能力新文件叠加——主题系统正是第一个执行样本。

---

## 4. 路线图

| 阶段 | 内容 | 交付判据 |
|---|---|---|
| P0 规范发布 | 19-ued.md 评审通过（本方案确认即视为发布） | 规范进 design.md 文档地图 |
| P1 主题工程 | §1.3 步骤 1-7：双源清理 + themes.css/themes.js + 面板 + 装配 + 桌面壳 + 07 §7.7.2 增补；顺带批次 A 的 #4 焦点环 | `adag verify` 13 项过；4 色板 × 明暗 8 态截图走查过 §19.3.3；旧键迁移不丢主题 |
| P2 P1 修复 | 批次 A 其余三项（#1/#2/#3） | 长会话可见「加载更早」；运行中会话不可删且显示运行点；wizard Enter 不提前提交 |
| P3 批次 B | §2.3 批次 B 全部 | 各项自查 + `go test ./...` |
| P4 批次 C | 清理与品牌小件；动效/术语收敛 | 死代码清零（grep 可验）；动效只剩令牌引用 |
| P5 批次 D | 二级页方言统一（已完成） | 二级页全部走 `ada-*` 方言；结论：**app.css 保留**——Tailwind preflight 是全站布局基线，语义类/工具类为 token 引用的冻结存量（新增使用被 UED §19.4 禁止），替换 preflight 的回归风险大于 ~8KB gzip 收益 |
| P6 批次 E | 二级页实测复核（§6，已完成） | 见 §6.3：无重复空态、窄屏表格可操作、单 h1、提交有 pending 态 |

每阶段门禁：`go test ./...`、`go vet ./...`、staticcheck、`adag verify`；UI 变更按 19-ued.md 对应条款自查。

## 5. 风险与边界

- **上游同步**：`themes.js`/`themes.css` 为纯新增文件，不碰镜像，上游 re-sync 无冲突；`app-local.css` 令牌块删除后若上游再同步该文件需人工比对（该文件本就是本地层，风险低）。
- **暗色回归**：删除 `app-local.css` 暗色块后，暗色从苔绿切回黑天鹅墨纸反转——这是已确认决策，但需要 8 态走查确认无令牌漏网（特别是 `--ph-brand-ink`/`--ph-subtle` 系从未被绿色块覆盖的变量）。
- **verify 基线**：`checkUI` 只扫 `tokens.css` 的硬编码色与模板内联 style；`themes.css` 中的色值属令牌定义、色卡走 CSS 变量类，均合规；若后续 verify 收紧扫描范围，按 19-ued §19.7「文档先行」更新基线。
- **不做的事**：独立设置页、骨架屏、自定义 webfont、主题商店化——均与「克制」原则冲突，规范已明文。

---

## 6. 批次 E：二级页复核（2026-11-04）

批次 A–D 完成后，对 SSR 二级页做了一轮**实测复核**：本地 `adag gateway --fake` 起真实网关，
按 390 / 768 / 1024 / 1440 四档量 `scrollWidth`、`getBoundingClientRect`、`getComputedStyle`，
并逐页截图比对。样本：`/`、`/overview`、`/extensions`、`/extensions/{id}`、`/workspaces`。

### 6.1 发现（按严重度）

| # | 级 | 问题 | 证据 | 影响 |
|---|---|---|---|---|
| E1 | **P1** | 概览岛与 SSR 片段**两套 DOM**：岛屿用 `ui-card/ui-table/ui-empty`（渲染成 `.card/.badge/.table` 存量类），模板用 `ada-panel` 列表。挂载即整块替换 | 挂载后 `#dashboard-live` 内 `.card` 出现、`.ada-panel` 为 0 | 首屏 CLS；批次 D 的方言统一每次访问被回滚；设计改动「改模板不生效」 |
| E2 | **P1** | 概览「最近会话」空态**出现两遍**：表格内「暂无数据」+ 空态标题「还没有会话」同时渲染 | `td.ada-table__empty` 与 `h2.ada-empty__title` 并存 | 用户被告知两次「没有数据」，且面板被撑到 ~350px 空白 |
| E3 | **P1** | 窄屏（≤760px）插件表五列挤压：状态 chip 断成「启/用」竖排、操作列按钮被切一半 | 390px 实测表宽 409 > 视口 390，`scrollWidth` 溢出 | 手机上「启用/停用」不可点全，状态不可读 |
| E4 | P2 | 概览统计卡在 <640px 竖排 4 列，单卡 110px 高 | 390px 实测：首屏 440px 只有统计 | 「最近会话」被推到折叠线以下，首屏无有效信息 |
| E5 | P2 | `.ada-section-head` 不换行：页头标题与右侧动作组互挤 | 插件详情 390px：页头高 112px、标题被压成 2 字一行 | 窄屏页头忽高忽低，标题读不全 |
| E6 | P2 | 页面内出现**两个 `<h1>`**（顶栏 + 正文），插件详情页却用 `<div>` 冒充标题 | `document.querySelectorAll('h1').length === 2` | 读屏按标题跳读会重复同一页名；页面间语义不一致 |
| E7 | P2 | 整页 POST 表单无提交态：按钮点击后毫无变化 | 启用/停用、重载插件、新建目录 | 慢网下重复点击 → 重复启停、重复建目录 |
| E8 | P2 | 侧栏分组标签「插件」与同名导航项「插件」并列 | `/overview` 侧栏：`插件页面` 组内含 `插件` 项（修复前组名也是「插件」） | 分不清哪一行进插件清单 |
| E9 | P3 | 「新建文件夹」输入框只有 placeholder，无标签 | `workspaces.html.tmpl` | 读屏读不出字段用途；placeholder 输入即消失 |

### 6.2 修复与判据

| # | 改动 | 范围 | 预期效果 |
|---|---|---|---|
| E1/E2 | `islands.js` 概览模板改为与 `overview_partial` 逐类同构（`ada-panel`/`ada-chip`/`ada-section-head`/链接行/一句空态）；纯展示逻辑下沉 `recentRows`、`chipClass` 两个 computed/method；`refresh()` 先比较序列化结果，**数据没变不赋值** | `static/islands.js` | 挂载不再换形态、空态只剩一句、5s 轮询不再整岛重排；改模板即改页面 |
| E3 | 窄屏给表格兜 `min-width: 560px` 走整体横滚；状态/能力 chip `nowrap`；**操作列 `position: sticky; right: 0`** 常驻可视 | `static/app-local.css` | 390px 下状态单行、启用/停用始终可点，不必先横滑到底 |
| E4 | 新增 `.ada-stat-grid`（<1024px 两列，≥1024px 四列）替换 `grid-cols-1 sm:grid-cols-2 lg:grid-cols-4` | `templates/overview.html.tmpl`、`islands.js`、`app-local.css` | 390px 下统计区 2×2，首屏即可见「最近会话」 |
| E5 | `.ada-section-head` 加 `flex-wrap`，`__actions` 加 `flex-wrap` + `margin-left:auto` | `app-local.css` | 窄屏页头标题独占一行、按钮整齐换行（详情页 112px → 97px 且标题满宽） |
| E6 | 二级页正文标题统一 `<h2 class="ada-page-title">`；插件详情从 `<div>` 改为 `<h2>` | 5 个模板 | 每页一个 h1（顶栏），标题跳读不再重复 |
| E7 | `app.js` 提交态：`submit` 冒泡阶段打 `data-submitting`/`aria-busy`，按钮 `aria-disabled` + 禁二次点击；`data-confirm` 拦下的提交跳过；`pageshow.persisted` 复位。CSS 给 `.ada-btn` 加转圈（含 reduced-motion 降级） | `static/app.js`、`app-local.css` | 提交后有可见反馈、重复提交被挡；`name=decision` 等按钮值不丢 |
| E8 | 侧栏分组标签「插件」→「插件页面」 | `ui.go` | 分组与条目不再同名 |
| E9 | 输入框补 `aria-label` | `templates/workspaces.html.tmpl` | 读屏可读出字段用途 |

### 6.3 验证方式

- **门禁**：`go test ./...`（`internal/cronstore` 的 `TestUpdateConcurrent` 在 Windows 上因 `cron.lock` 被占用失败，已在基线提交 `976104f` 复现，与本批无关）、`go vet ./...`、`gofmt -l` 干净、`node --test`（`AdaUI.toast` 那条为批次 B 移除 API 后遗留的用例，同样先于本批存在）、`adag verify` 13/13 通过。
- **实测**：修复前后各跑一遍四档宽度探针（`scrollWidth`、元素 `right`、`grid-template-columns`、`white-space`、`h1` 计数）+ 截图：
  - 概览 390：`.ada-panel` 5 个、`.card` 0 个、空态文案 1 条、统计两列（170/170）；
  - 插件表 390：表宽 560、横滚 210px、chip 单行（h=22）、操作列吸右；
  - 插件详情 390：页头 97px、标题满宽 352、`h1` 计数 1；
  - 提交态：合成 `submit` 后 `data-submitting=1`／`aria-busy=true`／按钮 `opacity .6`，第二次提交被 `preventDefault`。
- **未做**：插件列表岛（`mountExtensions`）仍是 `ui-card/ui-table` 方言——它没有轮询也不做局部更新，属于「挂载即重绘同一份数据」的冗余路径，建议后续整段下线或改写成与 SSR 同构后再合并进 E1 的做法（单列一项，不夹带）。
