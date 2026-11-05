# 19 · UED 规范（用户体验设计系统）

> 状态：v1（随《UI 优化方案》落地演进）
> 日期：2026-11-03
> 范围：ada-go 全部用户可见界面——SSR 外壳与页面、插件 UI、桌面壳观感、错误页
> 分工：[07-ssr-frontend.md §7.7](07-ssr-frontend.md) 管「从 a_da 复用什么、怎么接」；[15-ui-components.md](15-ui-components.md) 管「组件怎么用」；**本篇管「什么算对」**。评审 UI 变更以本篇为准绳，偏离即缺陷；改本篇前先读 [design.md 文档约定](design.md#文档约定)。

---

## 19.1 设计原则

1. **渐进增强优先**。无 JS 时核心流程可用（表单提交、会话阅读、meta refresh 轮询）；JS 是增强而不是依赖。新交互必须先回答「noscript 下怎样」。
2. **令牌单源**。颜色只引用 `--ph-*`，尺寸/字号/圆角只引用 `--ada-*`，动效只引用动效令牌。业务 CSS 零字面值（十六进制、px 即兴值、裸秒数）。
3. **克制**。单色品牌、无渐变、无发光、无彩虹标签（沿 07 §7.7.2）；动效只做状态反馈，不做表演。
4. **文字先行**。一切文字（含 10px 小字）按对比度 ≥ 4.5:1 验收；界面以中文全角标点、解释型文案为常态。
5. **键盘与读屏一等公民**。所有可聚焦元素有可见焦点环；弹层遵守四要素（19.5.1）；图标 `aria-hidden`，动态区域有 live 语义。

## 19.2 品牌规范

### 19.2.1 命名体系

ada 词根统一，四种写法各有边界，**不得混用**：

| 写法 | 用途 | 出现位置 | 禁止 |
|---|---|---|---|
| `ada-go` | 用户可见的产品/品牌名 | UI 品牌位、页面标题、产品声明 `identity.name`、文档 | 加后缀变体（ada-go-GO 等） |
| `adag` | 二进制与开发者命名空间 | CLI（`adag verify`）、cookie（`adag_js`/`adag_workspace`）、`adag-ui` 动态块、`data-adag-*` | 出现在用户可见文案 |
| `a_da` | 仅指代上游 Rust 项目 | 代码注释、NOTICE、来源头 | 用户可见 UI 与文档正文 |
| `ada-` | CSS 类名/文件前缀 | `.ada-btn`、`ui/*.css` | — |

大写 `Ada` / `Adag` 禁用（现有 UI 未出现，保持）。产品徽标（`Coding`/`Headless`/`Skeleton`）由 `productBadge()` 自动拆分，不手写。

### 19.2.2 品牌主题与多主题体系

- 品牌主题为 **Black Swan（黑天鹅）**：宣纸暖白画布 `#F6F3EC` × 墨黑 `#16171A`（取自 logo 天鹅主体）；暗色为墨纸反转。它是 `tokens.css` 的默认值——**不设 `data-theme` 属性即黑天鹅**，任何主题层不得在缺省态改变它。
- **主题 = 品牌衍生色板**。调色板维 `html[data-theme="<id>"]` × 明暗维 `html.light`/`html.dark`，两维正交；主题层（`ui/themes.css`）选择器用 `html.light[data-theme="…"]`（特异性恒胜基线），只覆盖 `--ph-*`，不新增选择器形态。
- 品牌底上的文字/图标一律 `--ph-on-brand`，禁止写死 `#fff`/`#000`；状态色成对使用「浅底 + 深字」（`*-soft` 配 `*-ink`），不能只换文字色。
- 主题（调色板）不是「皮肤商店」：新增主题必须走 19.3.3 准入清单，且风格仍须符合「纸面 × 单色品牌」的品牌气质。
- 首发色板：`black-swan`（默认）、`moss`（苔绿，承接 M2 绿色身份）、`ocean`（深海）、`dusk`（暮色）。

### 19.2.3 术语与文案

**术语表**（词频现状收敛后的钉定值）：

| 术语 | 含义 | 用法 |
|---|---|---|
| 会话 | thread | **唯一主名词**：列表、标题、删除、搜索、空态 |
| 任务 | 一轮工作的发起心智 | 仅限「新建任务」入口与其快捷键提示 |
| 工作区 | workspace | 稳定术语，不变 |
| 对话 | 仅动词性用法 | 「去对话页新建」这类引导句；不再作名词 |
| agent | 不译，小写 | 可直接入句 |

已知的存量混用（待收敛，见优化方案批次 C）：`返回对话/返回会话` → 统一「返回会话」；`还没有会话/暂无会话` → 统一「还没有会话」。

**语气**：
- 称呼「你」，无敬语、无感叹号；全角标点；省略号用 `…`；引用界面词用「」。
- 按钮 2–4 字；空态标题 6–10 字；说明句允许「解释型长句」，但必须交代边界或原因（范本：offline 页「已加载的页面不会伪造数据。」）。
- 错误文案必须给出下一步动作（做什么或点什么），不出现裸错误码当正文（错误码放详情区）。
- 确认弹窗必须说清后果与不可逆性（范本：「删除会话「%s」？删除后无法恢复。」）。

### 19.2.4 动效语言

- 交互反馈（hover/active/focus/开关）一律 `var(--ada-transition)`：120ms `cubic-bezier(0.4, 0, 0.2, 1)`。
- 面层级出入（抽屉、弹层、flash）用新增时长令牌：`--ada-duration-fast: 160ms`、`--ada-duration-slow: 240ms`，easing 同上。禁止再出现 140/150/180/200ms 裸值（存量见优化方案批次 C）。
- 所有 keyframes 必须配 `prefers-reduced-motion` 降级（现状已全部做到，新增不得破例）。
- 允许的动效：状态变化反馈、出现/消失、进行中暗示（呼吸、旋转）。装饰性循环动画禁止。

### 19.2.5 视觉资产

- Logo：单一 `logo.png`（512×512）为源；使用档位 24/28/32/40px，HTML 属性写死尺寸；暗色画布直接复用（logo 本身黑白）。
- 视觉资产（批次 C 已补）：`favicon.png`（32px，logo 面积平均下采样）与 `apple-touch-icon.png`（180px）；不再用 512px 原图当 icon。
- 图标：只经 `ui.go iconDefs` 的 sprite 通道（24 viewBox、stroke 1.75、`currentColor`、`fill:none`）；新图标先进 `iconDefs` 再用；`i-more` 的 stroke 3 为唯一既有例外。

## 19.3 设计令牌体系

### 19.3.1 层次与真源

| 层 | 文件 | 职责 | 治理 |
|---|---|---|---|
| 颜色基线 | `static/tokens.css` | `--ph-*` 唯一真源，Black Swan 明暗两套 | **上游镜像**（a_da commit + SHA-256），本地只读 |
| 主题层 | `static/ui/themes.css` | 衍生色板：`html.light/dark[data-theme]` 块 + 色板预览 `--sw-*` | 本地拥有；新主题走 19.3.3 |
| 组件层 | `static/ui/*.css` | `--ada-*` 标尺（圆角/字号/图标/间距）+ 组件样式 | 尺寸标尺偶数网格（2 的倍数），奇数档不收 |
| 覆盖层 | `static/app-local.css` | SSR 本地细节 | **禁止重定义**任何既有 `--ph-*`/`--ada-*`（M2 双源教训）；本地**新增**令牌（上游标尺没有的，如 `--ada-duration-*`）集中在本文件 `:root` 定义并登记进 §19.3.2 |

### 19.3.2 令牌清单

`--ph-*` 语义（完整值见 tokens.css / themes.css）：

- 画布与面：`canvas`（页底）、`surface`（卡）、`elevated`（浮层）、`subtle`（弱底）、`subtle-ink`
- 线：`line`、`line-strong`
- 文字：`ink`（正文）、`muted`（次要）、`faint`（辅助；10px 小字也按 4.5:1 验收）
- 品牌：`brand`、`brand-strong`、`brand-soft`、`brand-ink`、`on-brand`
- 状态：`success-soft/ink`、`warn-soft/ink`、`danger-soft/ink`
- 代码：`code-bg/ink`、`inline-code-bg/ink`、`syn-keyword/string/number/comment/function/type`
- 系统：`focus`、`glass`、`shadow`、`scrollbar`（+ `scrollbar-hover/active`、`scrollbar-size`）
- 动效时长（本地新增，定义在 app-local.css `:root`）：`--ada-duration-fast: 160ms`（面层级出入）、`--ada-duration-slow: 240ms`（抽屉/遮罩）；交互反馈直接用 `--ada-transition`，不新增时长档

新令牌准入：必须同时补 light + dark、登记进本清单、`themes.css` 全部色板同步补齐，否则该令牌不许被组件引用。

### 19.3.3 主题（色板）准入清单

新增一个色板必须全部满足：

- [ ] 19.3.2 清单中全部 `--ph-*` × 明暗两套齐全，`color-scheme` 正确；
- [ ] 对比度抽查达标：`faint/canvas`、`muted/surface`、`on-brand/brand`、各 `syn-*/code-bg` ≥ 4.5:1（大字/图标 ≥ 3:1）；
- [ ] 缺省态零影响：不设 `data-theme` 时渲染与黑天鹅逐像素一致；
- [ ] 色卡预览三色 `--sw-canvas/--sw-brand/--sw-ink` 与主题值同步（同文件内定义）；
- [ ] 桌面壳 `desktop/main.cjs` 的 `backgroundColor` 若画布值变化则同步；
- [ ] 8 态截图走查（每色板 × 明暗）：主按钮、代码块语法高亮、滚动条、弹窗、审批托盘；
- [ ] 登记进 19.2.2 色板清单与主题面板（`ui.go` 色板表）。

## 19.4 组件与 DOM 规范

- 类名 `ada-` 前缀；状态用 `data-*` 属性或 `is-*` 类表达，不拼条件 class 串；Web Component 刻意不挂 shadow root（SSR 同树样式）。
- **标题层级**：一篇页面只有顶栏一个 `<h1>`（页面标识）；正文页面标题用 `<h2 class="ada-page-title">`，节标题 `<h2 class="ada-section-title">`。二级页历史上有过「顶栏 h1 + 正文 h1」并列，读屏按标题跳读会听到两遍同一页名，已统一。
- 二级页（overview/extensions/workspaces/login/plugin_detail）已统一到 `ada-*` 组件方言（批次 D 完成）：页面骨架用 `ada-page-title/ada-section-head/ada-panel/ada-chip/ada-table/ada-empty-card/ada-link`（app-local.css）。`app.css` 里的基线类与工具类是**冻结的存量**：不许新增使用；新页面一律走 ada-* 方言。
- **焦点环**：2px 品牌描边 + 3px 半透明光环（`.ada-focusable` 语义）；`button/a/[tabindex]` 有兜底，但**每个新增可聚焦元素（尤其 `<summary>` 触发器）必须显式进入焦点清单**——键盘 Tab 可达且可见。
- CSP `style-src 'self'`：模板与 JS 禁止内联 style 属性（`adag verify` 把守）；动态预览色一律走 CSS 变量类（如 `.ada-swatch--moss`）。
- 表单：Enter 提交必须处理 IME（`isComposing` 必拦）；破坏性操作必须走 `data-confirm`，后果句见 19.2.3。
- **提交态**：整页 POST 的表单在提交后必须有可见反馈与防重复——表单打 `data-submitting` + `aria-busy="true"`，提交按钮降透明度、`cursor:progress`、禁二次点击（`pointer-events:none`），**不用 `disabled`**（`disabled` 的 submitter 不参与表单数据，`name="decision"` 这类按钮的值会丢）。`data-confirm` 拦下的提交不进入提交态，等用户确认后 `requestSubmit()` 再来一次；bfcache 恢复时（`pageshow.persisted`）必须复位。
- **输入草稿**：主 composer（新建任务与会话页）的未发送文本按 `data-draft-key` 存 `localStorage`（`a_da_draft:<key>`），整页刷新/热更新后恢复，提交成功或清空输入时删除；图片是 `File` 对象，不进文本草稿。新增 composer 必须带唯一的 `data-draft-key`。
- **会话行右键**：侧栏会话行提供「复制会话 ID / 归档会话」；运行中的会话禁用归档。归档只从列表隐藏，不软删历史；当前会话归档后回首页。模型/工作区选择随 `state.db` 持久化，刷新或 Core 重启后保持。
- **待发送队列**：运行中发送的消息先进入输入框上方的 outbox 浮窗，不立即 `turn.steer`；每条提供编辑 / 立即发送 / 删除（纯图标按钮），队列按会话存 `localStorage`，刷新不丢；当前轮正常 `completed` 时自动发队首，其余继续排队。带图片的消息仍走原生立即发送（`File` 不能跨刷新保存）。
- **输入区指标**：输入框下方只放只读信息（缓存命中率、上下文占用环），不再重复放「中止」；中止只在顶栏。上下文占用按 head 路径 entry token 估算，带「约」标注；缓存命中率 = cacheTokens / inputTokens。

## 19.5 交互模式规范

### 19.5.1 弹层四要素

一切弹层（modal、下拉、菜单、抽屉、面板）必须同时具备：

1. **点外关闭**（遮罩点击或 document 委托）；
2. **Esc 关闭**，就近原则且 modal 优先——低层弹层的 Esc 处理必须先探测 `AdaComponents.modalOpen()` 让路；
3. **焦点管理**：modal = Tab 陷阱 + 关闭后焦点归还触发器；非模态弹层 = 关闭后触发器保持可见焦点或合理落点；
4. **互斥**：同屏至多一个非模态弹层（打开 A 时先关 B）；modal 可与非模态弹层共存但位于其上。

存量偏差已收敛：图片查看器补 Tab 陷阱、侧栏抽屉补 inert 与焦点归还（批次 B 完成）；`ada-plus` 与行菜单的同开互斥在批次 C 清理时统一处理。

### 19.5.2 快捷键

| 键 | 行为 | 让路规则 |
|---|---|---|
| `Ctrl/Cmd K` | 聚焦会话搜索 | modal 打开时不抢焦点 |
| `Ctrl/Cmd N` | 新建任务 | 输入中不拦截（composer 有焦点时不触发）；浏览器保留键仅在 Electron 壳可靠，Web 端提示不承诺 |
| `Esc` | 就近关闭最上层浮层 | 见 19.5.1 |

新增快捷键必须登记进本表并遵守让路规则；不建快捷键帮助面板（快捷键总量控制在个位数）。

### 19.5.3 通知与错误

- 三种形态的分工：**toast/flash** = 页面级操作结果（自动消失，错误 9s/普通 6s）；**inline** = 弹窗/表单内部错误（不消失，带修复动作）；**流内错误卡**（`.ada-run-error`）= 轮次终态失败（含错误码详情）。
- 全局通知只有一条实现路径：app.js `notify()`（经 window.AdaApp.notify 供模块复用）+ 服务端 flash；`AdaUI.toast` 已移除（批次 B）。
- 连接类提示必须带动作按钮（「重新连接」），不得只说「请刷新页面」。

### 19.5.4 加载与空态

- 空态三件套：图标 + 标题（6–10 字）+ 说明句；可行动的空态加行动点（范本：chat 空态）。
- 加载：首屏靠 SSR（无骨架屏）；局部刷新用行内 spinner 或文本占位；岛内轮询在用户焦点/悬停于岛内时跳过该轮。
- 局部刷新失败不得静默：至少 notify 一句；会话历史截断必须显式提示或提供「加载更早消息」。

### 19.5.5 分步表单（ask-wizard 模式）

一题一步；非末步 Enter = 下一步而非提交；未到达步骤的字段必须 `disabled`（不参与提交）；步进后自动聚焦当前字段；无 JS 时全展开直接提交。末步真正提交前，脚本先把全部步骤重新启用——`disabled` 控件不随表单提交，只带末题会让服务端按 `q0`、`q1…` 拼出的 `answers` 缺项，`ask_user` 会以「回答数量与问题不一致」拒绝。

### 19.5.6 流式输出

- live 语义：生成中 `role="status"`，错误 `role="alert"`；新增动态区域照此二分。
- 滚动意图识别：用户上翻即停止强制跟随，「回到最新」出现；此逻辑收敛在 chat-live.js，不重复实现。
- 运行中反馈 = 输入框上方一行品牌色「深度求索中，用时 N 秒 …」+ 工具行呼吸条 + 秒级耗时刷新；状态文案与计时由同一处运行态驱动，不再用带边框的 warning 药丸。新运行态 UI 复用同一套。

## 19.6 可访问性标准

基线 = WCAG 2.1 AA：

- 对比度：正文与小字 ≥ 4.5:1，大字/图标 ≥ 3:1；**已知差距**：存量 `--ph-faint` 浅色 4.3:1（随主题重构修复）。
- 键盘：Tab 序 = 视觉序；modal 陷阱与归还；skip-link 保留。
- aria：图标 `aria-hidden`；动态区 `role=status/alert`；下拉高亮同步 `aria-activedescendant`；级联菜单项补 `role=menuitem`；抽屉/面板开合同步 `aria-expanded`。
- 触屏：`@media (hover:none)` 下常驻原本 hover 才出现的操作（现状已覆盖，新增交互不得破例）。
- `prefers-reduced-motion` 全覆盖（19.2.4）。

## 19.7 治理与门禁

- **门禁**：`go test ./...`、`go vet ./...`、staticcheck、`adag verify`（13 项，其中 ui 项校验 tokens 来源头、a_da 副本清单、模板无内联 style）；UI 变更加走 19.3.3 主题清单 / 本篇对应条款的自查。
- **文档先行**（沿 design.md 约定）：改令牌、组件、交互模式前先改本篇与 07 §7.7，再改实现；`adag verify ui` 的基线断言同步更新。
- **镜像纪律**：`tokens.css`、`app.css`、`theme.js` 等上游镜像文件不做本地修改；本地能力一律以新文件叠加（先例：`app.js` 的窄屏补丁、本次的 `themes.js`/`themes.css`）。
- 本篇变更历史记录在文末；大版本随《UI 优化方案》里程碑推进。

---

*变更：v1（2026-11-03）——初版：原则、品牌（命名/主题/文案/动效/资产）、令牌体系与主题准入、组件与 DOM、交互模式、可访问性、治理。*
*变更：v1.1（2026-11-04）——补「标题层级」（一篇页面一个 h1）与「提交态」（整页 POST 的 pending 与防重复）两条，来源见优化方案批次 E。*
