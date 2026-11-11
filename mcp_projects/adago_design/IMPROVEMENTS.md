# adago_design 改进方案（六项）

> **实施状态：六项已全部落地（2026-11-06）**，实现与本文的偏差集中记录在文末「实施结果」。
>
> 基于对 `mcp_projects/adago_design` 现有实现的通读（约 9.4k 行，v0.1.0 / M2 完成态）整理。
> 每条按「现象 → 根因（含代码定位）→ 修改建议（含骨架）→ 涉及文件 → 验收」组织。
> 行号对应当前工作区版本，改动后请以符号名为准。

---

## 0. 现状梳理

### 0.1 分层与职责

```
main.go / internal/app       装配：config → design.LoadCustomSystems → canvas.OpenStore → Server
internal/design              设计数据层：Token / 组件 / 皮肤 CSS / 整页模板 / 派生 / 走查
  types.go                   数据模型（System/Tokens/TypeScale/Component）
  system_*.go                8 套体系数据（每套一份 Token + Family 字族字符串）
  style.go                   StyleSheet()：cssVars + cssBase + skinOverrides + 字号归一
  components.go              组件目录 + 18 个 HTML 片段 + Compose()/buildDoc()
  screens.go                 4 个整页模板 + ComposeScreen()
  spec.go / audit.go         字面值扫描 / WCAG 走查
internal/canvas              画布层：Store（内存+持久化）/ HTTP API / 截图任务队列 / 导出
  static/{index.html,css,js} 画布 UI（vanilla JS，870 行 canvas.js 混装 4 类职责）
internal/mcpserver           23 个 MCP 工具（stdio）
```

### 0.2 关键事实（改动的约束条件）

| 事实 | 影响 |
|---|---|
| 预览文档走 `iframe srcdoc`，且截图靠 `SVG foreignObject` 序列化 | **任何预览资源必须内联**：外链字体/外链 sprite 既不能保证加载，也会污染 canvas 光栅化 |
| `canvas.js` 无构建步骤（`//go:embed static`） | 图标/组件不得引入 bundler；只能用「内联 sprite + 小函数」 |
| 画布节点内容是服务端生成的完整 HTML | 图标系统必须由 Go 侧产出，不能只做 UI 侧 |
| 节点用 `sandbox="allow-same-origin"` | 父页面可以 `contentDocument`，检查模式才成立（问题 6 的前提） |
| `canvas.KindFrame` 已被「分组框」占用 | 问题 2 的「画板」**不能**叫 Frame，统一用 `Artboard` |

---

## 1. 缺少 SVG 图标库

### 1.1 现状

- 全仓无任何图标资源：`internal/design` 只有 18 个组件的 HTML 片段，`static/` 只有 UI 三件套。
- 组件里的「图标位」全部由文本符号占位：
  - `components.go:127,131` 下拉箭头 `▾`
  - `components.go:213` 返回 `‹`、`215` 更多 `⋯`、`229-231` 列表箭头 `›`
  - `components.go:220-224` tabbar `◉ ◎ ✚ ▢ ○`
  - `components.go:142,197,248` 复选框/通告/Toast 的 `✓ ✕ i !`
- 画布 UI 侧同样只有字符（见问题 5）。

### 1.2 根因

不是「忘了放图标」，而是**缺少一层图标抽象**：`ds-*` 前缀已经有完整的「结构 × 皮肤」体系，但没有对应的「图标 × 皮肤」体系。于是每个图标位只能退化成「能打出来的任意字符」，字体一变、字重一变、平台一变，图标就变形或丢字（Linux/CI 容器缺 emoji 字体时直接显示豆腐块）。

### 1.3 方案：`internal/design` 内建 24 网格图标库

#### (1) 数据模型与组织方式

```go
// internal/design/icons.go
package design

// Icon 是图标库中的一项。路径数据取 24×24 网格、2px 视觉描边、圆头圆角。
type Icon struct {
    Name   string   `json:"name"`   // 唯一名，kebab-case，如 "chevron-right"
    Group  string   `json:"group"`  // arrow | action | status | nav | media | data
    Tags   []string `json:"tags,omitempty"`
    Body   string   `json:"-"`      // <path/> / <circle/> 片段，不含 <svg> 外壳
    Filled bool     `json:"filled,omitempty"` // 实心态（star/heart-active）
}

var iconRegistry = map[string]Icon{}   // init 中由 icon_paths.go 注册

func RegisterIcon(ic Icon)             // 与 Register(System) 同风格，重复即 panic
func Icons() []Icon                     // 按 Group→Name 排序
func GetIcon(name string) (Icon, bool)
func IconNamesByGroup(g string) []string
```

`icon_paths.go` 单文件集中存路径（约 30 个，可读、可 diff、无外部依赖）：

```go
func init() {
    RegisterIcon(Icon{Name: "chevron-right", Group: "arrow", Body: `<path d="M9 6l6 6-6 6"/>`})
    RegisterIcon(Icon{Name: "close",         Group: "action", Tags: []string{"×","x","cancel"},
        Body: `<path d="M6 6l12 12M18 6L6 18"/>`})
    RegisterIcon(Icon{Name: "check",         Group: "status", Tags: []string{"✓","done"},
        Body: `<path d="M4 12.5l5 5L20 6.5"/>`})
    // ...
}
```

**组织规则（写进 `ICONS.md` 与 `design_icons` 工具描述）**

| 规则 | 说明 |
|---|---|
| 命名 | `<域>-<对象>[-<方向/状态>]`，kebab-case：`arrow-left`、`chevron-down`、`warning`、`home`、`heart` |
| 分组 | `arrow`（线性方向）/ `action`（动作）/ `status`（状态与反馈）/ `nav`（导航）/ `media`（内容互动）/ `data`（列表/表格） |
| 一格一义 | 同义只保留一个正名，别名进 `Tags`（`×`→`close`，`▾`→`chevron-down`），供 `design_icons?query=▾` 检索 |
| 几何 | 24×24 viewBox、视觉描边 1.8–2、`stroke-linecap/linejoin: round`、关键水平/垂直边对齐整像素 |
| 尺寸 | 只做 16 / 20 / 24 三档光学尺寸，通过 class 切换，不做任意缩放 |
| 品牌 | 单独 `brand-*` 分组、**单色剪影**，`NOTICE` 记录商标归属，仅用于设计演示、不得改形改色 |

路径来源三选一，都需在仓库根 `NOTICE` 登记：
- **自绘**（推荐，零许可负担）：按 24 网格重描，与现有 `ds-*` 组件风格一致；
- 引入 [Lucide]（ISC）/ [Tabler]（MIT）/ Material Symbols（Apache-2.0）——三者均可商业使用、可嵌入，保留 LICENSE 即可；
- 禁止引入来源不明或「免费下载」站的图标包。

#### (2) 渲染：单一真源 → 两条出口

一份 `<symbol>` 精灵，两种用法：

```go
// IconSprite 返回整份精灵，注入每个预览文档 <body> 首部。
func IconSprite() string {
    var b strings.Builder
    b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" aria-hidden="true" ` +
        `style="position:absolute;width:0;height:0;overflow:hidden">`)
    for _, ic := range Icons() {
        b.WriteString(`<symbol id="ds-ic-` + ic.Name + `" viewBox="0 0 24 24">` + ic.Body + `</symbol>`)
    }
    b.WriteString(`</svg>`)
    return b.String()
}

// Icon 是模板里用的写法：精灵引用（同文档内 <use> 有效）。
func Icon(name string, size int, cls string) string

// IconInline 返回独立 <svg>（给 MCP 片段 / 独立 HTML 文件用）。
func IconInline(name string, size int, cls string) string
```

```html
<svg class="ds-icon ds-icon--20"><use href="#ds-ic-chevron-right"/></svg>
```

**为什么必须内联精灵、不能用外链 `.svg` 文件或 icon font**：

1. 预览是 `srcdoc` 文档，相对路径无基准，外链取不到；
2. 截图走 `foreignObject` 序列化 → `<img>` 光栅化，外链资源会 404 或触发跨域污点，PNG 直接失败；
3. icon font 依赖字体加载时机，`rasterize()` 只等 2 帧（`canvas.js:417-420`），必然拍到空白；
4. 精灵只序列化一份路径数据，N 个图标不重复膨胀文档（现在单张预览已含 ~10KB CSS）。

#### (3) CSS（挂在 `cssBase`，随体系皮肤自动生效）

```css
.ds-icon{width:1em;height:1em;display:inline-block;vertical-align:-.125em;flex:none;
  fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round}
.ds-icon--filled{fill:currentColor;stroke:none}
.ds-icon--16{width:16px;height:16px}
.ds-icon--20{width:20px;height:20px}
.ds-icon--24{width:24px;height:24px}
```

`currentColor` + `1em` 是刻意的：图标自动继承 `--ds-color-primary` / 状态色 / 文字色，天然满足 Token 规范，也让问题 3 的字体栈变化不再影响图标外观。

#### (4) 画布 UI 侧：同一份真源，`go generate` 生成

```go
//go:generate go run ./internal/design/gen_icons -out ../canvas/static/icons.js
```

产物 `static/icons.js`（无构建步骤，`<script src="icons.js">` 即可）：

```js
// 由 internal/design/icons.go 生成，勿手改
window.DSIcons = { sprite: "<svg …>…</svg>", paths: { "close": "<path d=\"M6 6l12 12…\"/>" } };
window.icon = (n, size, cls) => `<svg class="ds-icon ds-icon--${size||20} ${cls||''}"><use href="#ds-ic-${n}"/></svg>`;
```

`icons.js` 在 `index.html` 里把 `DSIcons.sprite` 注入 `document.body` 首部（`canvas.js` 启动时）。这样 Go 与 JS 不会漂移，也避免「两份图标各自长歪」。

#### (5) 让 Agent 也用图标

新增 MCP 工具 `design_icons`：

| 入参 | 出参 |
|---|---|
| `query?`（名/别名/分组模糊匹配）、`group?`、`format?=name\|svg` | `[{name, group, tags, svg}]` |

同时在 `mcpserver.NewServer` 的 `Instructions`（`server.go:39-44`）追加硬规则：

> 生成任何界面 HTML 时必须用 `design_icons` 取 SVG 图标，**不得使用 emoji 或 `▾ › ✕ ◉` 等字符符号**作为图标。

`design_audit` 增加「图标与符号一致性」检查（见问题 5.3），把这条规则变成可验收的门禁。

### 1.4 涉及文件

| 文件 | 动作 |
|---|---|
| `internal/design/icons.go` | 新增：类型、注册表、`IconSprite/Icon/IconInline` |
| `internal/design/icon_paths.go` | 新增：约 30 个图标路径 |
| `internal/design/gen_icons/main.go` | 新增：`go:generate` 生成器 |
| `internal/design/style.go` | `cssBase()` 追加 `.ds-icon` 段 |
| `internal/design/components.go` | `buildDoc()` 注入 `IconSprite()`；`renderComponent` 改用 `Icon()` |
| `internal/design/screens.go` | 同上 |
| `internal/canvas/static/icons.js` | 新增（生成物） |
| `internal/canvas/static/index.html` / `canvas.js` | 引入 icons.js、注入 sprite |
| `internal/mcpserver/server.go` | 注册 `design_icons`；Instructions 补规则 |
| `NOTICE` | 新增：路径来源与许可 |

### 1.5 验收

- `design_icons?group=status` 返回 5 个；`design_icons?query=▾` 命中 `chevron-down`。
- 任取一张 `design_preview` 产物，`<svg>` 内无 `<text>` 图标字符；文档内 `<symbol>` 数量 == 使用到的图标去重数。
- `canvas_screenshot` 截图里图标清晰（不是豆腐块/空白）。
- 新增单测：`icons_test.go` —— 每个图标 `Name` 唯一、`Body` 非空、`viewBox` 合法、无 `http` 引用、无 `<text>` 元素。

---

## 2. 设计流程缺「尺寸规范」前置

### 2.1 现状（尺寸全是硬编码的魔法数）

| 位置 | 代码 | 问题 |
|---|---|---|
| `components.go:312-320` | `width := 420; if sys.Family=="mobile" {430→390}`；`height := 220 + n*150`（上限 1600） | 宽度只有两档，高度是线性估算，与实际内容无关 |
| `screens.go:89-94` | `width := 420/390`；`height := map{"login":560,"settings":780,"feed":900,"profile":780}` | 四套模板各拍一个高度 |
| `components.go:164` | `.ds-page{max-width:560px;margin:0 auto;padding:…}` | 560 与移动端 390 冲突，「画板」概念不存在 |
| `store.go:197-210` | 默认 `420×320`，frame `600×400` | 又一个独立来源 |
| `server.go(canvas):434-463` | 预览宽度直接取自 `ComposePreview` 返回值 | 三处宽度来源互不知情 |
| `mcpserver/server.go:465-466` | `canvas_add` 默认 420×320 | 第四个来源 |

后果：① 节点高度普遍偏大/偏小 → iframe 内出现滚动条（`scrolling="auto"`），设计稿被裁切；② 同一个模板在 `design_screen` 与侧栏「添加预览」两条路径下尺寸不一致；③ 检查模式的「到画板四边距离」（`canvas.js:742-747`）量的是 `doc.body`，而 body 宽度 = 节点宽度 = 魔法数，量出来的边距没有设计含义。

### 2.2 根因

缺少**画板（Artboard）**这一等公民：尺寸被当成「渲染参数」散落在 4 个包、6 处默认值里，而不是「设计契约」。所以流程上也无法「先定尺寸」——根本没有可定的对象。

> 命名注意：`canvas.KindFrame` 已是「分组框」，勿复用 `Frame`。统一叫 `Artboard`。

### 2.3 方案

#### (1) 新增 `internal/design/artboard.go`

```go
type Artboard struct {
    ID     string `json:"id"`      // "iphone-15" / "web-1440" / "mp-wx"
    Name   string `json:"name"`
    Family string `json:"family"`  // mobile | tablet | web | cross
    Width  int    `json:"width"`   // 设计稿逻辑像素 @1x
    Height int    `json:"height"`
    StatusBar int `json:"status_bar"` // 状态栏高（0 = 无）
    SafeTop   int `json:"safe_top"`   // 安全区
    SafeBottom int `json:"safe_bottom"`
    NavBar  int   `json:"nav_bar"`
    TabBar  int   `json:"tab_bar"`
    Columns int   `json:"columns"`
    Margin  int   `json:"margin"`   // 内容左右边距
    Gutter  int   `json:"gutter"`   // 栅格间距
    DPR     []int `json:"dpr"`      // 导出倍率
}

// 推导值（契约的核心）
type ArtboardSpec struct {
    Artboard
    ContentWidth  int `json:"content_width"`   // Width - 2*Margin
    BodyHeight    int `json:"body_height"`     // Height - StatusBar - SafeTop - SafeBottom
    ContentHeight int `json:"content_height"`  // BodyHeight - NavBar - TabBar
    ExportSizes   []string `json:"export_sizes"` // ["@1x 390×844","@2x 780×1688",…]
}
```

内置 8 个（够覆盖现有 8 套体系的 Family）：

| id | 尺寸 | Family | 关键推导 |
|---|---|---|---|
| `iphone-15` | 393×852 | mobile | 状态栏 54 / 安全底 34 / nav 44 / tab 49 → 内容宽 361 |
| `iphone-se` | 375×667 | mobile | 无安全区，nav 44 / tab 49 → 内容宽 343 |
| `android-360` | 360×800 | mobile | 状态栏 24 / nav 48 / tab 56 → 内容宽 328 |
| `mp-wx` | 375×812 | mobile | 微信小程序：nav 44 + 胶囊 32 / tab 50 |
| `ipad-768` | 768×1024 | tablet | 双列 margin 24 / gutter 16 |
| `web-1280` | 1280×800 | web | 12 列 / gutter 24 / 最大内容宽 1200 |
| `web-1440` | 1440×900 | web | 12 列 / gutter 24 / 最大内容宽 1200 |
| `desktop-app` | 1440×900 | cross | 侧栏 240 + 内容区 |

#### (2) 新增 MCP 工具 `design_artboard`

```
入参: artboard? | family?（按体系 Family 取默认）
出参: ArtboardSpec（上表全部推导值）+ 一段「尺寸契约」文本
```

文本示例（直接给 Agent 读，也直接贴进设计交付文档）：

```
iphone-15 · 393×852 @1x（导出 @2x 786×1704 / @3x 1179×2556）
状态栏 54 / 安全区 上 0 下 34 / 导航 44 / 标签栏 49
内容区 361×715，边距 16，栅格 4
契约：所有间距取 4 的倍数；尺寸标注以本画板左上角为原点；不得输出非本画板尺寸的预览。
```

#### (3) 流程前置：改 `Instructions` 与工具签名

`server.go:39-44` 的推荐流程从 4 步扩为 5 步，**尺寸排第一**：

```
1 design_artboard（定画板尺寸规范）→
2 design_systems / design_tokens / design_components（定体系）→
3 design_preview | design_screen（必须带 artboard）→
4 design_audit（走查，含尺寸契约校验）→
5 canvas_arrange / canvas_screenshot / canvas_export
```

签名变更（保持向后兼容）：

- `design_preview` / `design_screen` / `canvas_add` 增加 `artboard?: string`；
- 缺省时**按体系 Family 推导**（mobile→`iphone-15`，web→`web-1440`，tablet→`ipad-768`，cross→`desktop-app`），不再用 420/390；
- `SceneInput` 内部把 `artboard` 一路带到 `Compose`/`ComposeScreen`；
- 节点新增 `Artboard string` 字段（`store.go:17-32`），并写入 `Tags: ["artboard:iphone-15", …]`，出参回显 `artboard` 与 `content_width`，让 Agent 明确当前契约。

#### (4) 删掉四处魔法数，改为单一推导

```go
// design/artboard.go
func (a Artboard) Spec() ArtboardSpec
func DefaultArtboardFor(family string) Artboard    // 唯一默认来源
func PageCSS(a Artboard) string                     // 生成 --ds-ab-* 变量
```

```css
:root{--ds-ab-w:393px;--ds-ab-content-w:361px;--ds-ab-margin:16px;--ds-ab-nav:44px;--ds-ab-tab:49px}
.ds-page{max-width:var(--ds-ab-content-w);margin:0 auto;padding:var(--ds-ab-pad-y) var(--ds-ab-margin)}
```

- `Compose` / `ComposeScreen` 宽度改 `a.Width`，删除 `height := 220 + n*150` 与 map；
- 高度交由客户端实测（见下）；
- `canvas.Store.Add` 的默认尺寸改为「按 `kind` + 默认画板」推导，不再写 420/320。

#### (5) 高度自适应（顺带修掉滚动条与截图裁切）

服务端没有浏览器，不可能精确算高。正确做法是**让渲染方量一次**：

```js
// canvas.js：iframe load 后测真实高度，差 > 4px 才回写一次
async function autoFitHeight(id, iframe) {
  if (S.drag) return;                       // 拖动中不打扰
  const n = S.nodes.get(id); if (!n || n.kind === "frame") return;
  const h = Math.ceil(iframe.contentDocument.documentElement.scrollHeight);
  if (Math.abs(h - n.height) <= 4) return;
  const { node } = await api("/api/nodes/" + id, {method:"PUT", headers:{…}, body: JSON.stringify({height: h})});
  S.nodes.set(id, node);                    // ← 关键：回写本地，避免下一轮轮询重载 iframe
}
```

配合 `Node.AutoHeight bool`（默认 true，用户手动改过尺寸则置 false），彻底消除「预览卡片内滚动条」与「截图被裁」。

#### (6) 走查联动

`design_audit` 新增「画板规范」检查（挂在 `audit.go` 第 7 项之后，复用 `add()`）：

- 节点/预览是否声明了 `artboard`（缺省 fail）；
- 内容宽度是否等于 `content_width`（`!=` → warn，给差值）；
- `margin` / `gutter` 是否为 4 或 8 的倍数。

检查模式的「到画板」也据此改写：`bodyRect` 换成 `artboard` 尺寸，并在面板补一行「宽度利用率 361/393 = 92%」。

### 2.4 涉及文件

| 文件 | 动作 |
|---|---|
| `internal/design/artboard.go` | 新增：8 个画板 + `Spec()` + `PageCSS()` |
| `internal/design/components.go` | `Compose` 用 Artboard；`buildDoc` 去掉 560、注入变量 |
| `internal/design/screens.go` | `ComposeScreen` 用 Artboard，删 height map |
| `internal/canvas/store.go` | `Node.Artboard` / `Node.AutoHeight`；默认尺寸改为推导 |
| `internal/canvas/server.go` | `addNodeIn` / `catalogPreviewIn` 接受 `artboard` |
| `internal/canvas/export.go` | 导出板标注画板尺寸，frame 宽取画板宽 |
| `internal/canvas/static/canvas.js` | `autoFitHeight`；检查面板「到画板」改口径 |
| `internal/mcpserver/server.go` | 新 `design_artboard`；三个工具加 `artboard` 入参 |
| `internal/design/audit.go` | 新检查项 |
| `README.md` / `DESIGN.md` | 流程与工具表同步 |

### 2.5 验收

- `design_artboard?artboard=iphone-15` 返回 `content_width=361`、`export_sizes` 含 @2x/@3x。
- `design_screen?system=apple&template=settings` 不传 artboard → 宽 393，节点 tag 含 `artboard:iphone-15`。
- 任意预览节点打开后 iframe 无竖向滚动条；`height` 与 `scrollHeight` 差 ≤4px。
- 全仓 `grep -n "420\|390\|560\|780\|900"` 在 `internal/` 里只剩画板表。

---

## 3. 字体默认不可商用 / 不可复现

### 3.1 现状

字族只有 `TypeScale.Family` 一个裸字符串（`types.go:30-33`），无许可元数据：

| 体系 | 当前字族（`system_*.go`） | 风险 |
|---|---|---|
| `apple` | `-apple-system, "SF Pro Text", "SF Pro Display", "PingFang SC", "Helvetica Neue", sans-serif` | SF Pro 仅限 Apple 平台；非 Apple 环境退化为 PingFang/Helvetica，字宽差异大 |
| `element` | `"Helvetica Neue", Helvetica, "PingFang SC", …, Arial` | Helvetica Neue 为 Monotype 专有，Windows/Linux 直接 fallback 到 Arial/DejaVu |
| `ant` / `tdesign` | `-apple-system / "Segoe UI" / PingFang SC / Microsoft YaHei` | 系统字体，**不可嵌入、不可分发**；Linux/CI 容器无这些字体 → 截图与设计稿不一致 |
| `douyin` | `"Douyin Sans", "DouyinSansBold", …` | 抖音美好体为 **OFL 1.1**，可商用可嵌入，但只在本地装了才能命中 |
| `xiaomi` | `"MiSans", "MiSans VF", "HarmonyOS Sans SC", …` | MiSans 免费商用、可嵌入，但许可**禁止再分发字体文件**；本机没装则退化 |
| `volcano` | `Inter, "PingFang SC", …` | `Inter` 是 OFL，方向正确，但只此一家 |
| `material` | `Roboto, "Noto Sans SC", system-ui, …` | Apache-2.0 + OFL，方向正确 |
| 画布 UI / 走查板 / 导出板 | `"Segoe UI", system-ui, "PingFang SC", …`（`canvas.css:13`、`audit.go:235`、`export.go:64`） | 同类问题，且三处各写一遍 |

### 3.2 根因

三层问题叠在一起：

1. **没有「授权」这一维度**。字体被当成纯样式字符串，无法回答「这套稿子能不能交付给客户」。MiSans 与 SF Pro 的许可天差地别，在代码里长得一样。
2. **把系统字体当资产用**。`-apple-system`/`Segoe UI`/`PingFang SC` 能渲染，但不能嵌入、不能随稿交付、在 Android/Linux/CI 上不存在 —— 于是「同一份 Token 在两台机器上渲染出两种排版」，截图不可复现，`design_audit` 的对比度/字号结论也就不可信。
3. **品牌字体无兜底**。抖音/小米体系把品牌字体放在首位，但没给出 OFL 替代；用户机器没装字体时，`PingFang SC` 之后的 `Microsoft YaHei` 在 Linux 也不存在，最终落到 `sans-serif`（DejaVu Sans），中文直接变方框。

### 3.3 方案：字体注册表 + 授权策略 + 栈生成

#### (1) 新增 `internal/design/fonts.go`

```go
type FontLicense string

const (
    LicenseOFL          FontLicense = "OFL-1.1"        // 可商用 / 可嵌入 / 可再分发（保留许可）
    LicenseApache       FontLicense = "Apache-2.0"     // 同上
    LicenseISC          FontLicense = "ISC"
    LicenseVendorFree   FontLicense = "vendor-free"    // 厂商免费商用：可嵌入，禁止单独再分发字体文件
    LicenseSystemOnly   FontLicense = "system-only"    // 纯系统引用：不可嵌入、不可分发
    LicenseProprietary  FontLicense = "proprietary"    // 需购买授权
)

type FontSpec struct {
    ID          string      `json:"id"`        // "inter"
    Family      string      `json:"family"`    // CSS family name
    Script      string      `json:"script"`    // latin | hans | mono
    License     FontLicense `json:"license"`
    Commercial  bool        `json:"commercial"`   // 可用于商业项目
    Embeddable  bool        `json:"embeddable"`   // 允许 @font-face 自托管
    Redistributable bool    `json:"redistributable"` // 允许随包分发字体文件
    Attribution bool        `json:"attribution"`  // 需保留版权声明/注明使用
    Weights     []int       `json:"weights"`
    Homepage    string      `json:"homepage"`
}

var fontRegistry = map[string]FontSpec{
  "inter":        {Family:"Inter",           Script:"latin", License:LicenseOFL,        Commercial:true, Embeddable:true, Redistributable:true,  Weights:[]int{400,500,600,700}},
  "noto-sans-sc": {Family:"Noto Sans SC",    Script:"hans",  License:LicenseOFL,        Commercial:true, Embeddable:true, Redistributable:true,  Weights:[]int{400,500,700}},
  "jetbrains-mono":{Family:"JetBrains Mono", Script:"mono",  License:LicenseOFL,        Commercial:true, Embeddable:true, Redistributable:true},
  "roboto":       {Family:"Roboto",          Script:"latin", License:LicenseApache,     Commercial:true, Embeddable:true, Redistributable:true},
  "douyin-sans":  {Family:"Douyin Sans",     Script:"hans",  License:LicenseOFL,        Commercial:true, Embeddable:true, Redistributable:true,  Homepage:"抖音美好体 · OFL 1.1（开源，禁止单独售卖字体文件）"},
  "misans":       {Family:"MiSans",          Script:"hans",  License:LicenseVendorFree, Commercial:true, Embeddable:true, Redistributable:false, Attribution:true, Homepage:"MiSans 知识产权许可协议：可嵌入，但禁止再分发字体文件，须注明使用"},
  "harmonyos-sans":{Family:"HarmonyOS Sans SC",Script:"hans",License:LicenseVendorFree, Commercial:true, Embeddable:true, Redistributable:false},
  "sf-pro":       {Family:"SF Pro Text",     Script:"latin", License:LicenseSystemOnly,Commercial:false,Embeddable:false,Redistributable:false},
  "pingfang":     {Family:"PingFang SC",     Script:"hans",  License:LicenseSystemOnly,Commercial:false,Embeddable:false,Redistributable:false},
  "segoe-ui":     {Family:"Segoe UI",        Script:"latin", License:LicenseSystemOnly,Commercial:false,Embeddable:false,Redistributable:false},
  "helvetica-neue":{Family:"Helvetica Neue", Script:"latin", License:LicenseProprietary,Commercial:false,Embeddable:false,Redistributable:false},
}

// FontStack 生成合规字族串：主张 → 同类 OFL 兜底 → 系统兜底 → generic
func FontStack(primary ...string) string
// ResolveStack 解析既有字族串，逐项标注许可（供 audit 用）
func ResolveStack(family string) []FontSpec
// CommercialSafe 判断整条栈是否可商用（所有非 generic 项都 commercial）
func CommercialSafe(family string) (bool, []string)
```

#### (2) 默认策略（写进 `fonts.go` 顶部注释与 `DESIGN.md`）

```go
// DefaultStacks 是商业友好的默认栈：拉丁 Inter（OFL）+ 中文 Noto Sans SC（OFL）
// + system-ui 兜底。三者均可商用、可嵌入、可再分发，是全平台可复现的最小集。
var DefaultStacks = map[string]string{
    "latin": `Inter, "Noto Sans SC", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif`,
    "hans":  `"Noto Sans SC", Inter, system-ui, "PingFang SC", "Microsoft YaHei", sans-serif`,
    "mono":  `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`,
}
```

各体系改为「品牌主张 + 合规兜底」，而不是「品牌主张 + 一堆系统名」：

| 体系 | 改后 |
|---|---|
| `apple` | `-apple-system, BlinkMacSystemFont, "SF Pro Text", Inter, "Noto Sans SC", sans-serif`（Apple 平台优先，其他平台落到 OFL，不再掉到 Helvetica Neue） |
| `element` | 去掉 `"Helvetica Neue", Helvetica, Arial` → `Inter, "Noto Sans SC", system-ui, sans-serif` |
| `ant` / `tdesign` | 保留 `-apple-system/Segoe UI` 前置，尾部追加 `Inter, "Noto Sans SC"` |
| `douyin` | `"Douyin Sans", Inter, "Noto Sans SC", sans-serif`（OFL，可嵌入，兜底同源） |
| `xiaomi` | `"MiSans", "MiSans VF", "HarmonyOS Sans SC", Inter, "Noto Sans SC", sans-serif` |
| `volcano` / `material` | 已合规，补 `"Noto Sans SC"` 中文兜底 |
| 画布 UI / 走查板 / 导出板 | `canvas.css:13`、`audit.go:235`、`export.go:64` 统一引用 `DefaultStacks["latin"]`（三处归一，`canvas` 包通过 `canvas.Catalog` 或注入变量拿，避免循环依赖 → 建议在 `design` 暴露 `FontStack`，`canvas` 侧只用常量字符串） |

#### (3) 三条硬规则

1. **禁止 `@font-face` 外链**（Google Fonts/CDN）。理由同问题 1：`srcdoc` + `foreignObject` 截图取不到外链字体，且会把字体授权问题带进产物。需要品牌字体时，只按**本地 family 名**引用。
2. **品牌字体永不自托管**。MiSans 许可明确禁止再分发字体文件（可嵌入 App，但须注明；不得单独分发字体软件）；抖音美好体是 OFL，可自托管但要带 LICENSE。所以策略是：`Embeddable && Redistributable` 才允许进打包流程。
3. **交付级设计必须 `CommercialSafe`**。`design_tokens` 的 type 分类输出增加 `font_id / license / commercial / embeddable / attribution` 字段，让 Agent 能自检。

#### (4) 落点

- `cssVars()`（`style.go:78-123`）追加：
  ```css
  --ds-font: <FontStack 结果>;
  --ds-font-mono: <mono 栈>;
  /* font-license: OFL-1.1 (Inter, Noto Sans SC) */
  ```
  并在 `<html>` 上加 `data-font-license="OFL-1.1"`，便于产物侧核对。
- 新增 MCP 工具 `design_fonts`：`{fonts:[…], default_stack, policy}`。
- `design_audit` 新增「字体授权」检查：`ResolveStack(tok.Type.Family)` 逐项判定 ——
  - 全部 `commercial` → pass，标注 OFL/Apache 构成；
  - 含 `system-only` → warn：「跨平台不可复现，Android/Linux/CI 会退化，建议前置 OFL 字体」；
  - 含 `proprietary`（如 Helvetica Neue）→ fail，给替代项。
- `derive.go:55` 的 `Family: base.Family` → 派生品牌体系默认回到 `DefaultStacks`，避免「某茶饮品牌稿莫名用抖音字体」。

### 3.4 涉及文件

| 文件 | 动作 |
|---|---|
| `internal/design/fonts.go` | 新增：注册表、`FontStack`、`ResolveStack`、`CommercialSafe` |
| `internal/design/system_*.go` | 7 处 `Family:` 字族串按上表替换 |
| `internal/design/style.go` | `cssVars` 输出 `--ds-font/--ds-font-mono` + 许可注释 |
| `internal/design/audit.go` | 字体授权检查项 |
| `internal/design/derive.go` | 派生体系字族策略 |
| `internal/canvas/static/canvas.css` | `body{font-family}` 统一 |
| `internal/design/audit.go` / `canvas/export.go` | 板面字族统一 |
| `internal/mcpserver/server.go` | 新 `design_fonts`；`design_tokens` 出参补许可字段 |
| `NOTICE` | 字体许可汇总表（名称/协议/可商用/可嵌入/需署名） |

### 3.5 验收

- `design_tokens?system=element&category=type` 的 `family` 不含 `Helvetica`。
- `design_audit?system=apple` 的「字体授权」为 pass 或 warn（不得含 proprietary）。
- 新增 `fonts_test.go`：每个体系 `tok.Type.Family` 解析结果全部 `commercial==true`；`DefaultStacks` 三档均通过 `CommercialSafe`。
- 在无 PingFang/YaHei 的容器里打开同一预览，中文不出现方框（靠 Noto Sans SC 兜底）。

---

## 4. 拖动交互卡顿

### 4.1 现状代码路径

```
mousedown(.node__head) → startNodeDrag (canvas.js:197-205)
   S.dragging = {id, dx, dy, moved}；el.style.zIndex = 9999
mousemove(window)      → canvas.js:207-223
   写 el.style.left/top  ← 每事件
   renderEdges()        ← 每事件，全量重建 SVG
mouseup(window)        → canvas.js:225-238
   await PUT /api/nodes/{id}  ← 只提交坐标
```

### 4.2 根因（按影响排序）

**R1｜父页面根本收不到 mousemove（致命，表现为「粘住/冻结」而非「卡顿」）**

`.node__body iframe` 没有任何 `pointer-events` 约束（`canvas.css:42`）。节点高 320px，其中拖动把手 `.node__head` 只有 32px（`canvas.css:35-36`）。一旦光标向下移动离开头部进入 iframe 区域，事件被 iframe 文档吞掉——**iframe 内的事件不会冒泡到父文档**，`window.addEventListener("mousemove")` 直接静默。于是：往下拖 20px 就停住，回到头部又跳一段，观感就是严重卡顿。这是首要原因。

同时暴露两个衍生缺陷：
- 点卡片正文（iframe）无法选中节点 —— 只有 32px 头部可操作；
- `startNodeDrag` 里 `e.stopPropagation()`（`canvas.js:199`）阻断了节点自身的 `selectNode` 监听（`canvas.js:123`），所以**点头部也选不中**。选中基本只能靠工具条程序化调用。

**R2｜每次 mousemove 全量重建连线 SVG**

`renderEdges()`（`canvas.js:264-291`）先 `while (childNodes.length > 1) removeChild`，再为每条边 `createElementNS` + 4 次 `setAttribute`，标签还要建 `<text>`。画布上有 20 个节点、10 条边时，每次 mousemove 会产生 10 个 path + 10 个 text 的完整重建；即使 0 条边也要清一遍。而且它没有 rAF 合帧、没有脏边判断。

**R3｜用 `left/top` 布局而不是 `transform` 合成**

`el.style.left/top` 是布局属性，每帧触发 `#world` 下所有绝对定位子元素的 layout；节点内嵌 iframe 是独立文档，**offset 变化会让浏览器重新定位/重新光栅化 iframe 的表面**。改成 `translate3d` 后位移交给合成器，iframe 表面可直接平移复用。

**R4｜`.node.dragging{opacity:.92}`（`canvas.css:29`）**

`opacity < 1` 为整棵子树（含 iframe）创建离屏合成层，每帧都要把 iframe 表面重新混合。这是「拖起来发虚还掉帧」的直接来源。

**R5｜`backdrop-filter: blur` 与拖动打架**

`#toolbar`（`css:54`，blur 10px）、`#sidebar`（`css:81`，12px）、`#inspector`（`css:122`，12px）。当卡片从这些固定面板下方经过时，每帧都要重算背景模糊（GPU 回读 + 二次模糊）。拖动路径一旦经过工具条/侧栏，帧率立刻掉。

**R6｜无合帧、无指针捕获**

mousemove 里同步做 DOM 写 + `renderEdges`，没有 rAF 合帧；用 `mouse` 而非 `pointer`，没有 `setPointerCapture`，鼠标在窗口外释放会丢 `mouseup` → `S.dragging` 永久残留，后续移动全部走"拖动"分支。

**R7｜拖完一次，iframe 会整体重载一次（「松手闪一下」）**

`mouseup` 只发 `PUT {x,y}`，**丢弃了响应**（`canvas.js:233`），本地 `S.nodes` 里该节点的 `updated_at` 仍是旧值。2s 后的 `pollNodes` 发现 `local.updated_at !== n.updated_at` → 走 `updateNodeEl` → `iframe.dataset.rev !== n.updated_at` → `iframe.srcdoc = n.html`（`canvas.js:145-148`）→ **整个 iframe 文档重新加载**。每拖一次闪一次，叠在 R1/R3 上就是"拖动很卡"的完整体感。

**R8｜拖动期间仍在 2s 轮询全量同步**

`pollNodes` 每次两个请求（nodes + edges）+ 又一次 `renderEdges()`（`canvas.js:315`），与拖动抢主线程。

### 4.3 方案

#### (1) 事件不再被 iframe 吞掉（修 R1）

```css
/* 默认：卡片是「画布物件」，整块可拖可选中 */
.node__body iframe{pointer-events:none}
/* 检查模式：iframe 必须可命中才能取元素 */
body.mode-inspect .node__body iframe{pointer-events:auto}
/* 拖动中：双保险 */
body.is-dragging .node__body iframe{pointer-events:none}
```

`toggleInspect()`（`canvas.js:599-613`）里同步 `document.body.classList.toggle("mode-inspect", INS.on)`。

顺带修选中：
```js
function startNodeDrag(e, id){
  if (e.button !== 0) return;
  selectNode(id);          // ← 先选中，再拖
  e.preventDefault();      // 不再 stopPropagation（自身监听已手动调用）
  ...
}
```

#### (2) 拖动主循环：pointer capture + rAF 合帧 + transform（修 R3/R6）

```js
function startNodeDrag(e, id){
  if (e.button !== 0) return;
  selectNode(id); e.preventDefault();
  const n = S.nodes.get(id), el = S.els.get(id);
  S.drag = {id, el, sx:e.clientX, sy:e.clientY, ox:n.x, oy:n.y, nx:n.x, ny:n.y, raf:0, moved:false};
  el.setPointerCapture(e.pointerId);
  el.classList.add("dragging");
  document.body.classList.add("is-dragging");
}

function onPointerMove(e){                    // window，passive
  const d = S.drag; if (!d) return;
  d.nx = d.ox + (e.clientX - d.sx) / S.scale;
  d.ny = d.oy + (e.clientY - d.sy) / S.scale;
  d.moved ||= Math.abs(d.nx - d.ox) > 1 || Math.abs(d.ny - d.oy) > 1;
  if (d.raf) return;                          // 一帧一次
  d.raf = requestAnimationFrame(() => {
    d.raf = 0;
    d.el.style.transform = `translate3d(${d.nx}px,${d.ny}px,0)`;
    scheduleEdges(d.id);                      // 只排脏边
  });
}
```

节点定位基准从 `left/top` 改为 `transform`：`nodeEl()` / `updateNodeEl()` 里写
`el.style.transform = translate3d(n.x, n.y, 0)`，`left/top` 固定为 0。落盘时仍写 `x/y`，DOM 与状态只有一个来源。

配套 CSS：
```css
.node{will-change:transform;contain:layout paint}
.node.dragging{opacity:1}          /* 去掉 0.92：避免 iframe 每帧重新混合 */
body.is-dragging .node{transition:none}
```

#### (3) 连线增量渲染 + 合帧（修 R2/R8）

```js
const edgeEls = new Map();   // edgeId -> {path, label}
let edgeRaf = 0;
function scheduleEdges(dirtyId){             // 无参 = 全量
  edgeDirty.add(dirtyId || "*");
  if (edgeRaf) return;
  edgeRaf = requestAnimationFrame(() => { edgeRaf = 0; flushEdges(); });
}
function flushEdges(){
  if (!S.edges.size) { if (edgeEls.size) clearEdgeEls(); return; }  // 0 边时零开销
  // 只重算 dirty 涉及的边，复用已有 <path>，仅 setAttribute("d", …)
}
```

要点：元素池化（同 id 复用节点）、只改 `d`/`x`/`y` 属性、空画布早退。渲染成本从 O(E) DOM 创建降为 O(脏边) 属性写。

#### (4) 面板模糊让路（修 R5）

```css
body.is-dragging #toolbar,
body.is-dragging #sidebar,
body.is-dragging #inspector{backdrop-filter:none;background:var(--bg2)}
```
更彻底的选择是把三处 `backdrop-filter` 去掉、用不透明 `--bg2` + 1px 边框（当前视觉差异极小，收益是拖动全程无 GPU 回读）。

#### (5) 同步回写，消除 iframe 重载（修 R7）

```js
const { node } = await api("/api/nodes/" + id, {method:"PUT", …});
S.nodes.set(id, node);        // 关键：把服务端新 updated_at / html 同步回本地
```
或服务端 `handleUpdateNode` 对「只改坐标」的请求**不 bump `updated_at`**（加 `if in.X/in.Y only { keep UpdatedAt }` 分支）。两者取一，推荐前者（更通用），后者可作为额外优化。

#### (6) 拖动期间暂停后台同步

```js
setInterval(() => { if (!S.drag) pollNodes(); }, 2000);
```
`autoFitHeight`（问题 2）同样在 `S.drag` 时跳过。

#### (7) 松手回滚与清理

```js
function onPointerUp(e){
  const d = S.drag; if (!d) return;
  cancelAnimationFrame(d.raf);
  d.el.releasePointerCapture?.(e.pointerId);
  d.el.classList.remove("dragging");
  document.body.classList.remove("is-dragging");
  S.drag = null;
  if (!d.moved) return;
  savePosition(d.id, d.nx, d.ny).catch(err => {
    const n = S.nodes.get(d.id);
    n.x = d.ox; n.y = d.oy;                       // 失败回滚
    d.el.style.transform = `translate3d(${d.ox}px,${d.oy}px,0)`;
    toast("位置保存失败: " + err.message, true);
  });
}
```

### 4.4 涉及文件

| 文件 | 改动 |
|---|---|
| `internal/canvas/static/canvas.js` | `startNodeDrag` / `onPointerMove` / `onPointerUp` / `renderEdges→scheduleEdges+flushEdges` / `pollNodes` 门控 / `nodeEl`+`updateNodeEl` 改 transform / `toggleInspect` 加 body class |
| `internal/canvas/static/canvas.css` | `.node__body iframe{pointer-events:none}`、`body.mode-inspect` 反向开关、`.node{will-change;contain}`、去掉 `.dragging{opacity}`、`body.is-dragging` 关 blur |
| `internal/canvas/server.go` | （可选）纯坐标更新不 bump `updated_at` |

### 4.5 验收

- DevTools Performance 录 3 秒连续拖动：**60fps、无 >50ms 长任务**；`renderEdges`/`flushEdges` 调用次数 ≤ 帧数且与边数无强相关。
- 拖动全程（含光标扫过卡片正文、工具条、侧栏）节点跟手，不回跳。
- 松手后 iframe **不再重载**（Network 面板无 srcdoc 重设、无闪烁）。
- 鼠标拖出窗口释放，回来仍可正常点击（无残留拖动态）。
- 点卡片任意位置都能选中该节点。

---

## 5. 界面 emoji 替换为 SVG 图标

### 5.1 现状清单（全量）

**A. 画布 UI（`static/index.html` + `canvas.js`）**

| 位置 | 现状 | 替换为 |
|---|---|---|
| `index.html:19` / `:21` | `−` / `+` | `minus` / `plus` |
| `index.html:25` | `＋便签` | `plus` + 文本「便签」 |
| `index.html:26` | `🔍 检查` | `inspect`（取景框） |
| `index.html:30` / `canvas.js:59` | `● 已连接 / ● 连接断开` | 纯 CSS 圆点 `<i class="dot">`（**不是图标**，去掉字符即可） |
| `index.html:31` | `🌗` | `theme`（半明半暗圆） |
| `index.html:37,41` | `‹` | `chevron-left` |
| `index.html:47` | `✕` | `close` |
| `canvas.js:114,118` | 节点删除 `✕` | `close` |
| `canvas.js:369` | 组件 `＋` 按钮 | `plus` |
| `canvas.js:820` | `🔍 元素规格` | `inspect` |
| `canvas.js:840-842` | `✓` / `✓≈` / `⚠` | `check` / `approx` / `warning`（保留 `.mark.ok/.warn` 配色） |
| `canvas.js:544` | `‹ / ›` 侧栏收起 | `chevron-left` / `chevron-right` |
| `index.html:17` | `D` logo | 保留（字母标，非 emoji） |

**B. 组件预览（`components.go`，会进截图与导出产物）**

| 位置 | 现状 | 替换为 |
|---|---|---|
| `127,131` | `▾` | `chevron-down` |
| `142,197,248` | `✓` | `check` |
| `196,198,199,249` | `i` `!` `✕` | `info` `warning` `close` |
| `213` | `‹ 返回` | `chevron-left` + 文本 |
| `215` | `⋯` | `more` |
| `220-224` | `◉ ◎ ✚ ▢ ○` | `home` `compass` `plus` `message` `user` |
| `229-231` | `›` | `chevron-right` |

**C. 整页模板（`screens.go`）**

| 位置 | 现状 | 替换为 |
|---|---|---|
| `136-139` / `156-161` / `183-187` | tabbar `◉ ◎ ✚ ▢ ○` | 同 B |
| `149` | `♥ 2.1k` `💬 486` `↗ 分享` | `heart` `comment` `share` |
| `153` | `🔍` | `search` |

**D. MCP 文本输出（`mcpserver/server.go:190,429`）**

`❌ [code] msg`、`⚠️ 无效组件已跳过` —— 这是给 LLM 读的文本，不是界面。保留亦可，但建议统一为 `[ERROR]` / `[WARN]` 前缀，避免模型把 emoji 学进它生成的 HTML 里（已有实践表明会传染）。

### 5.2 根因

1. **没有任何图标资源**（问题 1 的直接后果），只能用键盘上能打出来的字符顶替。
2. **字符图标不可控**：`◆ ◉ ▢ ✚` 属于几何图形/dingbat 区，字体覆盖极不稳定（Windows Segoe UI 有、Linux DejaVu 部分有、CI 容器可能全无 → 豆腐块）；emoji（`🔍 🌗 💬`）还带彩色彩色度、基线随平台变化、行高被撑大。
3. **不可着色/不可继承 Token**：`♥` 的颜色由文字色决定但形状无法用 `stroke-width` 统一；`✓` 的字重随字体变化，18px 与 20px 视觉粗细不一致。
4. **交付物不可控**：预览会进 `canvas_screenshot` PNG 与 `canvas_export` HTML，字符图标让「同一份设计稿在不同机器上截图不一致」，直接破坏设计走查的可比性。

### 5.3 方案

1. 先落地问题 1 的图标库（依赖关系必须如此）。
2. 按 5.1 表格逐条替换，统一写法：
   ```html
   <svg class="ds-icon ds-icon--20"><use href="#ds-ic-chevron-right"/></svg>
   ```
   画布 UI 侧用 `icon("chevron-right", 20)`（来自生成物 `icons.js`）。
3. **四个必须注意的细节**（否则替换后会引入新 bug）：
   - 复选框：`.ds-checkbox__box` 现在靠 `color:transparent` 隐藏 `✓`（`style.go:204`），换成 SVG 后 `currentColor` 也会透明，需改成 `.ds-checkbox__box .ds-icon{opacity:0}` + `.ds-checkbox--checked .ds-icon{opacity:1}`，否则选中态看不见勾。
   - Tabbar：`.ds-tabbar__icon{font-size:18px}`（`style.go:270`）要删掉 font-size，改用 `.ds-icon--20`，否则行高从 18px 变 20px 会顶偏标签。
   - 通告：`.ds-alert__icon` 是「18px 圆底 + 白色字母」（`style.go:245-253`），替换后要保留 `border-radius:50%;background:状态色` 且图标 `stroke:#fff`，否则状态语义丢失。
   - 基线：`.ds-icon{vertical-align:-.125em}` 必须保留，否则 `‹ 返回` 这类「图标 + 文本」的混排会上下错位 1–2px —— 截图对比时会被放大成"布局变了"。
4. **防回归门禁**：`design_audit` 增加「图标与符号一致性」检查（`audit.go` 第 8 项）：
   ```go
   var symbolRe = regexp.MustCompile(`[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}` +
       `\x{25A0}-\x{25FF}\x{2190}-\x{21FF}\x{2713}\x{2717}\x{25C9}\x{25CE}\x{25A2}\x{2726}\x{25CB}]`)
   // 扫描 StyleSheet + 全部组件 HTML + 屏幕模板；命中即 fail，输出 (组件, 字符, 建议图标名)
   ```
   放在 `appendSpecCheck` 旁边，与「组件规范」并列，让 emoji 回归在 `go test` 阶段就暴露（`design_test.go` 里加一条对全组件的扫描断言）。

### 5.4 涉及文件

| 文件 | 动作 |
|---|---|
| `internal/canvas/static/index.html` | 11 处字符/emoji → `icon()` |
| `internal/canvas/static/canvas.js` | 6 处（删除、规格标题、Token 标记、组件加号、侧栏箭头） |
| `internal/canvas/static/canvas.css` | `.dot` 连接状态；`.mark` 内图标尺寸 |
| `internal/design/components.go` | 17 处（`renderComponent` 全部模板） |
| `internal/design/screens.go` | 9 处（3 个 tabbar + feed 操作行 + 搜索） |
| `internal/design/style.go` | `.ds-icon` 段；修 `.ds-checkbox__box` / `.ds-tabbar__icon` / `.ds-alert__icon` |
| `internal/design/audit.go` + `spec.go` | 「图标与符号一致性」检查 |
| `internal/mcpserver/server.go` | 错误/警告前缀改 ASCII（可选） |
| `README.md` / `DESIGN.md` | `🔍 检查` → `inspect 检查`（文档层同步） |

### 5.5 验收

- `grep -P '[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}]'` 在 `internal/` 下为 0 命中。
- 全部 18 个组件预览 + 4 个整页模板截图里图标清晰、同尺寸图标视觉粗细一致。
- 复选框选中/未选中、tabbar 选中态颜色由 `--ds-color-primary` 正确驱动。
- `design_audit` 的「图标与符号一致性」为 pass。

---

## 6. 检查模式的关联操作缺陷

### 6.1 现象

开启检查模式后：选中元素 A（面板出规格），此时把光标移到同卡片/其他卡片的元素 B —— **B 既不高亮也不出标注**；点 B 则 A 的标注与规格全部被替换，无法做「A 与 B 的间距/对齐」这类关联检查。

### 6.2 根因（9 处，逐条对应代码）

| # | 位置 | 缺陷 |
|---|---|---|
| 1 | `canvas.js:589-595` | **状态模型只有单目标**：`INS {node, target, hover}`，结构上没有 anchor/secondary 概念，从数据层面就无法表达"两个元素" |
| 2 | `canvas.js:715-719` | **pinned 优先覆盖悬停**：`if (INS.target && INS.node === nodeId) el = INS.target` —— 只要选中过，`drawInspect` 永远画已选元素，悬停目标被丢弃。这是"选了一个就看不了第二个"的直接原因 |
| 3 | `canvas.js:699-700` | **跨卡片早退**：`setHover` 一进门 `if (INS.node && INS.node !== nodeId) return` —— 在 A 卡选过元素后，B 卡的悬停被完全屏蔽 |
| 4 | `canvas.js:686-696` | **`clearHover` 全局清空**：遍历所有节点并 `layer.innerHTML = ""`，跨卡对照必然丢一半；`state.leave`（`632-634`）一触发就抹掉另一张卡的标注 |
| 5 | `canvas.js:721-747` | **坐标系割裂**：所有绘制都在各自 iframe 的 `position:fixed` 层里，`getBoundingClientRect` 是 iframe 视口坐标；`drawInspect` 只拿 `doc.body` 当"画板"。两个元素在两张 iframe 里 → 没有共同坐标系，画不出跨卡连线 |
| 6 | `canvas.js:632-634` | **`INS.hover` 语义混乱**：`leave` 里用 `INS.hover === iframe` 判断，但 `INS.hover` 会被后续 `setHover` 覆盖 → 有时不清（残留标注）、有时误清（把已选目标的层也清了） |
| 7 | `canvas.js:748-764` | **关系类型单一**：只有「到画板四边」+「同父级最近兄弟」。没有 A↔B 的水平/垂直间距、边线对齐偏差、包含/重叠、尺寸差 |
| 8 | `canvas.js:635-641` | **点击总是重置**：`state.click` 无条件覆盖 `INS.node/target`，没有"追加/锁定"语义，也没有修饰键（Shift/Alt）区分主次选择 |
| 9 | `canvas.js:616-648` | **监听器泄漏**：`attachInspect` 每次 toggle 都 `iframe.addEventListener("load", …)`（647），无去重；`iframe.dataset.inspectAttached`（661）写了从不读，是死代码 |

补充：`drawInspect` 每次 `pointermove` 都 `layer.innerHTML=""` + 新建 20–40 个 DOM 节点 + 遍历所有兄弟 `getBoundingClientRect()`，没有 rAF 合帧 —— 所以就算能选，检查模式本身也是卡的。

### 6.3 方案：双选中模型 + 跨帧坐标归一 + 父级关系层

#### (1) 状态模型（替换 `INS`）

```js
const INS = {
  on: false,
  anchor: null,   // 主选：{nodeId, el, tag, rect}
  target: null,   // 次选：同上
  hover:  null,   // {nodeId, el}
  mode: "idle",   // idle | anchor | pair
  detail: {},     // systemId -> catalog 详情（原 catalog 缓存）
};
```

交互契约：

| 操作 | 结果 |
|---|---|
| 单击元素 | 无 anchor → 设 anchor；有 anchor 无 target → 设 target（进入 `pair`）；已有 pair → anchor 前移为当前 target，新元素作 target（rolling，符合"逐项对比"的连续性） |
| `Shift` + 单击 | 强制设为 target（不移动 anchor） |
| `Alt` 悬停 | 与 anchor 做**临时对照**（虚线），不改变选择 |
| `Esc` | target → 清；无 target → anchor → 清；无选择 → 退出检查模式 |
| `C` | 清空 pair，保留检查模式 |
| 点空白（iframe body / 画布） | 清 pair |

#### (2) 跨帧坐标归一（关键，解决根因 5）

不要用 `getBoundingClientRect()` 做缩放换算（会被 `world` 的 `translate+scale` 污染）。直接用「节点世界坐标 + 元素文档坐标」：

```js
const HEAD_H = 32;   // .node__head 高度

function worldRect(nodeId, el) {
  const n = S.nodes.get(nodeId);
  const ifr = S.els.get(nodeId).querySelector("iframe");
  const r = el.getBoundingClientRect();
  const win = ifr.contentWindow;
  return {
    x: n.x + r.left + win.scrollX,
    y: n.y + HEAD_H + r.top + win.scrollY,
    w: r.width, h: r.height,
  };
}
```

节点内元素 → 世界坐标是一步加法，与画布缩放/平移完全解耦，`scale` 变化时重算一次即可。

#### (3) 关系层放在父页面（解决根因 4/5）

```html
<div id="world">
  <svg id="edges">…</svg>
  <svg id="rel-layer"><g id="rel"></g></svg>   <!-- world 坐标系，pointer-events:none -->
</div>
```

- 卡片**内部**的锚定高亮与四边标注仍留在 iframe 内的 `.dsx-layer`（复用现有 `drawInspect` 的 mask/hl 部分，改名为 `drawBox`）；
- 元素**之间**的间距/对齐/尺寸差，全部由 `#rel` 在 world 坐标绘制 —— 一条线可以横跨两张卡片，这是原实现结构上做不到的。

#### (4) 关系计算（`rel.js`，纯函数，可单测）

```js
// 返回可直接绘制的标注描述，便于测试与复用（MCP 侧也能用）
function relation(a, b) {          // a/b: worldRect
  const dx = b.x - (a.x + a.w);    // 水平间距（b 在右）
  const dy = b.y - (a.y + a.h);    // 垂直间距（b 在下）
  return {
    gapX: dx, gapY: dy,
    dw: b.w - a.w, dh: b.h - a.h,
    align: {                         // 四边 + 两轴中线对齐偏差
      left: a.x - b.x, right: (a.x + a.w) - (b.x + b.w),
      top:  a.y - b.y, bottom: (a.y + a.h) - (b.y + b.h),
      cx: (a.x + a.w/2) - (b.x + b.w/2), cy: (a.y + a.h/2) - (b.y + b.h/2),
    },
    overlap: dx < 0 && dy < 0,
    contains: a.x <= b.x && a.y <= b.y && a.x+a.w >= b.x+b.w && a.y+a.h >= b.y+b.h,
  };
}
```

绘制规则：

| 情形 | 画法 |
|---|---|
| 水平分离且垂直投影重叠 | 两条竖向边界 + 中间双箭头 + 标签 `24px`（自动取 `--ds-space-*` 最近档，复用 `tokenMark`） |
| 垂直分离且水平投影重叠 | 同上，竖向 |
| 任一方向边线偏差 ≤1px | 贯穿两元素的虚线 + 标签「左对齐」/「顶对齐」/「居中对齐」 |
| 包含 | 外层元素四角内缩标记 + 标签「A ⊃ B」 |
| 重叠 | 交集矩形浅填充 + 「重叠 12×0」 |
| 尺寸差 | 面板行 `W 361 → 328（-33）` |

#### (5) 面板：新增「关联」段

`showSpecPanel`（`canvas.js:794-823`）在 `pair` 状态下改为双栏：

```
🔍{inspect} 元素关联
A  div.ds-list__item   361×52   @16,120
B  div.ds-btn--primary 120×40   @16,196
─────────────────────────────
水平间距  A右→B左  0px      ⚠ 规范 16px
垂直间距  A下→B上  24px     ✓ 24px
对齐      左对齐（偏差 0px）
尺寸差    W -241 / H -12
Token     间距 24 ✓ / 圆角 4 ✓≈6
```

跨卡片时补一行「建立卡片连线」按钮 → `POST /api/edges`，把元素级关系升级为画布级叙事（打通 `canvas_connect`）。

#### (6) hover 性能与作用域（顺带修 6.1 的"检查模式也卡"）

- `.dsx-layer` 内部分区：`[data-role="hover"]` / `[data-role="anchor"]` / `[data-role="target"]`，`clearHover` 只删 `hover` 分区，锚定标注永不被误清（修根因 4/6）。
- `drawInspect` 走 rAF 合帧；缓存「元素 → rect」在同一帧内只算一次；目标未变（`el === INS.hover.el && scrollY 相同`）则跳过重绘。
- 事件从 `mousemove` 改 `pointermove`，并去掉跨节点早退（修根因 3）：每张卡各自持有 hover，互不影响。
- 修监听器泄漏（根因 9）：用 `iframe.dataset.inspectBound` 作唯一守卫，`load` 监听用 `{once:true}` 或在 `detachInspect` 时显式移除。

#### (7) 顺手修的一个真 bug

`drawInspect` 的遮罩用 `bodyRect.height` 算底部遮罩（`canvas.js:727`），但层是 `position:fixed`（视口坐标）。预览内容高于视口且已滚动时，`rect.bottom` 可小于 0 或大于视口，遮罩会出现负高度/反向覆盖。改用 `viewport` 尺寸：

```js
const vw = doc.documentElement.clientWidth, vh = doc.documentElement.clientHeight;
```

### 6.4 涉及文件

| 文件 | 动作 |
|---|---|
| `internal/canvas/static/canvas.js` | 拆分：检查模式整体迁出 → 见下 |
| `internal/canvas/static/inspect.js` | **新增**：`INS` 状态机、`attachInspect/detachInspect`、`worldRect`、`drawBox`、`drawRelation`、`relation()`、面板渲染 |
| `internal/canvas/static/index.html` | 引入 `inspect.js`；`#world` 内加 `#rel-layer` |
| `internal/canvas/static/canvas.css` | `.rel-*` 系列；`.dsx-layer` 分区；`body.mode-inspect` 的 iframe 指针开关 |
| `internal/canvas/static/canvas_test.go`（或新增 `inspect_test.go`） | `relation()` 纯函数用例：分离/重叠/包含/对齐/同尺寸 |

> 拆分理由：`canvas.js` 已 870 行，混装「视图变换 / 节点渲染 / 拖动 / 轮询 / 侧栏 / 截图 / 检查模式」七类职责。检查模式本身就有 ~285 行（586-870），独立成 `inspect.js` 后，双选中状态机才有地方写清楚。

### 6.5 验收

- 选中 A 后悬停 B：B 高亮、实时显示 A↔B 间距（不丢 A 的标注）。
- 跨卡片（A 在左卡、B 在右卡）：能画出横跨两卡的间距线与对齐虚线。
- `Shift+单击` 只改 B 不改 A；`Alt+悬停` 只是预览不改变选择；`Esc` 逐级退出。
- 两张卡同时保留锚定标注（切换卡片不清空）。
- 检查模式连续移动 3 秒：60fps，无 `innerHTML=""` 抖动（Performance 里 Layout 次数明显下降）。

---

## 7. 实施顺序与整体验收

### 7.1 依赖关系

```
问题1 图标库 ──► 问题5 emoji 替换（强依赖：没有图标库无从替换）
问题2 画板尺寸 ──► 问题6 关联检查的「到画板」口径（弱依赖）
问题4 拖动优化 ──► 问题6 检查模式（共用 pointer-events 开关与 rAF 合帧基建）
问题3 字体 ──► 独立，可与任何一项并行
```

建议 4 个 PR：

| PR | 内容 | 风险 |
|---|---|---|
| PR-1 | 拖动与渲染性能（问题 4）—— 纯前端，无 API 变更，收益最快 | 低 |
| PR-2 | 图标库 + emoji 替换（问题 1+5）—— 含 `design_icons` 与 gate 检查 | 中（触及全部组件模板，需逐张截图比对） |
| PR-3 | 画板尺寸（问题 2）—— 含 `design_artboard`、高度自适应、审计项 | 中（改 `Compose/ComposeScreen` 出参，注意旧节点兼容） |
| PR-4 | 字体授权（问题 3）+ 检查模式双选中（问题 6） | 中高（6 是结构性重写，建议独立 `inspect.js`） |

### 7.2 门禁

```bash
GOWORK=off go test ./...
GOWORK=off go vet ./...
staticcheck ./...
# 新增断言
#  - icons_test.go    图标唯一/路径合法/无外链/无 <text>
#  - fonts_test.go    8 体系字族全部 commercial
#  - artboard_test.go 8 画板推导值（content_width 等）快照
#  - symbol_test.go   全部组件 HTML + 模板无 emoji/dingbat
#  - relation_test    （前端）纯函数用例
```

### 7.3 兼容性注意

1. `canvas.json` 新增 `artboard` / `auto_height` 字段 → v2 读取需容忍缺省（`loadNodes` 里补默认值，勿因空值 panic）。
2. 已存在的旧节点宽度仍是 420/390 → 打开时按 `artboard` 缺失视为 `legacy`，**不要**强制重排，避免打乱用户现有画布。
3. `design_preview` / `design_screen` 出参新增 `artboard`，属**只增不改**，对 Agent 兼容。
4. 字体栈变更会改变已生成节点的视觉 → 旧节点 HTML 已固化在内，不受影响；新生成的才生效。

### 7.4 一句话总结

这六项里，**4（拖动）与 6（关联检查）是 bug**，根因都在 `canvas.js` 的「事件被 iframe 吞掉 + 单目标状态模型 + 无合帧」；**1（图标）、2（尺寸）、3（字体）是缺失的规范层**，需要在 `internal/design` 各补一个注册表（`icons.go` / `artboard.go` / `fonts.go`），并且必须由 Go 侧产出、随 `buildDoc` 内联注入 —— 因为预览走 `srcdoc` + `foreignObject` 截图，任何外链资源都不可靠。**5（emoji）不是独立需求，是 1 的验收项**，应当用 `design_audit` 的检查项固化，而不是靠人工 grep。

---

## 8. 实施结果（2026-11-06）

六项全部落地。`GOWORK=off go build ./... && go vet ./... && go test ./...` 全绿。

### 8.1 新增文件

| 文件 | 作用 |
|---|---|
| `internal/design/icons.go` | 图标注册表、精灵渲染、`{{icon:...}}` 展开、检索 |
| `internal/design/icon_paths.go` | 38 个 24 网格图标路径 |
| `internal/design/gen_icons/main.go` | `go:generate` 生成画布侧 `static/icons.js` |
| `internal/design/fonts.go` | 字体授权注册表、默认栈、商用/可复现判定 |
| `internal/design/artboard.go` | 8 块画板、推导值、`PageCSS` |
| `internal/design/checks.go` | 走查新增：字体授权 / 图标与符号 / 尺寸规范 |
| `internal/canvas/static/inspect.js` | 检查模式（双选中 + 跨帧关系层）独立成文件 |
| `internal/canvas/static/icons.js` | 生成物（勿手改） |
| 单测 `icons_test.go` / `artboard_test.go` / `fonts_test.go` / `symbol_test.go` / `catalog_tools_test.go` | 规范层不变量与工具契约 |

### 8.2 与原方案的偏差（刻意为之）

| 项 | 原方案 | 实际实现 | 原因 |
|---|---|---|---|
| 图标命名 | `<域>-<对象>`（`nav-home`） | 扁平语义（`home`）+ `Group` 字段 | 名字短、`use` 引用不冗长；域信息本来就有字段承载 |
| 尺寸工具名 | `design_frame` / `design_canvas` | `design_artboard` | `canvas.KindFrame` 已被「分组框」占用，避免概念撞车 |
| 组件预览默认宽度 | 420/390 | 按体系 Family 取画板（web 1440 / mobile 393） | 尺寸只有一个来源；Web 体系本来就该按桌面宽度出稿 |
| 高度自适应度量 | `documentElement.scrollHeight` | `.ds-page` 的 `getBoundingClientRect().height` | 文档有 `min-height:100vh`，scrollHeight 会被视口撑满，量不到真实内容 |
| 关系标注绘制 | 只说"父级关系层" | 父页面第 3 个 SVG（`#rel-layer`，z-index 最高、`pointer-events:none`） | 必须盖在卡片之上才能看清跨卡连线 |
| Alt 悬停 | 可选 | 已实现（`tempTarget`，不写入选择） | 连续对比时不想每次都改选择 |

### 8.3 真机验证（无头 Chromium + CDP 真实输入事件）

| 用例 | 结果 |
|---|---|
| 图标精灵 | 38 个 `<symbol>`；工具条按钮图标 16×16 正常渲染 |
| 拖动跨越 iframe 区域 | 节点连续跟随（transform 从 `306,219` → `435,708`），未冻结 |
| 拖动后 iframe | 打标记存活，未重载 |
| 从卡片正文选中节点 | `S.selected` 命中，检查器弹出 |
| 高度自适应 | `element` 760 → 1009，与 `.ds-page` 实测一致 |
| 检查模式同卡配对 | anchor→target 成立，关系层 17 个节点，面板含"间距/对齐" |
| 检查模式跨卡配对 | A 在 element 卡、B 在 apple 卡，两卡同时保留 A/B 框 |
| 截图链路 | 87ms 出 90KB PNG，图标在 `foreignObject` 光栅化后完整保留 |
| 控制台 | 无异常、无 warning |

> 验证脚本在工作区外（`.workbuddy/tmp/verify*.mjs`），依赖 Node 22 内置 `WebSocket`
> 直连 CDP，不引入 puppeteer/playwright 依赖。

### 8.4 已知遗留

- `staticcheck` 本机二进制为旧版（导出数据 v2 vs 工具链 v5），无法运行；`go vet` 已通过。
- 检查模式的关系计算目前覆盖：水平/垂直间距、四边与中线对齐、包含、重叠、尺寸差；
  旋转元素、跨滚动容器的边界情况未处理。
- 画板切换不会自动迁移已有节点宽度（刻意：避免打乱用户现有画布）。

---

## 9. 迭代 1：P0 数据正确性与性能加固（2026-11-12）

基于全仓分析定下的三个迭代计划，本轮落地迭代 1（P0）。分析结论与计划见
对话记录/仓库提案；四项改动 + 一个新增探测层，全部带机器门禁
（`internal/canvas/hardening_test.go`）。

### 9.1 落地内容

| # | 问题 | 改动 | 落点 |
|---|---|---|---|
| 1 | `persistLocked` 静默吞掉全部写盘错误 + `MarshalIndent` 膨胀 | 紧凑序列化；写盘三步（tmp → 旧文件轮换进 `.bak`/`.bak2` → 就位）；失败记入 `Store.lastPersistErr` + 日志 + `/api/health` 的 `persist_error` 字段；`OpenStore` 主文件缺失/损坏时从备份降级恢复 | `store.go` |
| 2 | 两进程同开一份数据目录 → 各自内存态全量覆盖写，后写覆盖先写 | 绑定端口前 `ProbeInstance` 探测 `port..port+20`（`/api/health` 回显 `server`/`data_dir`/`rev`）；同数据目录命中时 `serve` 委派浏览器打开已有实例（exit 0）、`mcp` 快速失败并给处置提示；不同数据目录共存 | `canvas/instance.go`、`app.go`、`main.go` |
| 3 | 节点部分更新用零值判断（`Title != ""`），空值永远写不进去 | `updateNodeIn` 全指针字段：区分「未传」与「显式置空」；`Store.Update` 只在 HTML 真变时推进 `HTMLRev` | `server.go`、`store.go` |
| 4 | 列表接口全量回传 HTML（~10KB/节点）× 2s 轮询 × 每标签页 | 列表默认剥 `html`（`nodeMeta` 空指针遮蔽嵌入字段）；新增 `GET /api/nodes/{id}` 按需取正文、`?include=html` 全量、`GET /api/events` SSE 推送；UI 改为 SSE 触发（150ms 合帧）+ 2s 轮询兜底 + 按 `html_rev` 复用缓存 | `server.go`、`canvas.js` |

顺带修掉一个休眠 bug：`updateNodeEl` 原先先 `S.nodes.set` 再取 `prev`，
`prev === n` 使「正文变了重载 iframe」的比较永远相等——现在由调用方显式传入更新前的本地节点。

### 9.2 与原方案的偏差（刻意为之）

| 项 | 原方案 | 实际实现 | 原因 |
|---|---|---|---|
| 写盘 debounce（500ms 合并） | 计划有 | **未做**，保持同步写 | 紧凑序列化后单次写毫秒级；debounce 引入崩溃丢失窗口与退出 flush 复杂度，收益不成比例 |
| 多实例「客户端模式」 | 计划首选（第二实例把 Store 操作代理给已有实例） | 改为 serve 委派 + mcp 快速失败 | 客户端模式需要为整个 Store 面板维护第二套 HTTP 客户端实现（含 Update(mut) 这类无法直接代理的签名），长期维护成本高；快速失败同样消除数据丢失，且消息可行动。后续若两 mcp 会话并发成为常态再上客户端模式 |
| 列表 `?since=` 增量 | 计划有 | **未做** | 元数据列表本身已降到 KB 级，SSE 已消灭轮询滞后；增量协议徒增对账复杂度 |

### 9.3 验证

- `GOWORK=off go test ./... && go vet ./...` 全绿（新增 `hardening_test.go` 8 项：
  备份轮换与降级恢复、写盘失败可见、rev/订阅、列表剥 HTML、SSE 流、指针语义、探测命中/不命中）。
- 真机冒烟：serve 起服务 → health 含新字段；建项目/节点 → 列表无 `html` 键；
  SSE 首条 rev 与变更推送到位；备份三件套生成；同数据目录 serve 委派（exit 0）、
  mcp 快速失败（exit 1）、不同数据目录共存（+1 端口）。
- 浏览器端到端（IAB 真实渲染）：预览卡完整渲染（按需取正文成立）；页面开着时
  服务端新增节点 → **不刷新即出现**（`.node` 1→2，iframe 正文与标题逐字核对通过）。

---

## 10. 迭代 2：Agent 体验（2026-11-12）

四个工具面改动，全部带单测；与迭代 1 的区别：这次动的是「Agent 拿到的东西」
（更干净的截图、可自愈的截图链路、面向节点的走查、更省 token 的规范输出）。

### 10.1 落地内容

| # | 问题 | 改动 | 落点 |
|---|---|---|---|
| 1 | `canvas_add` 任意 HTML 无消毒：外链 `<img>`/样式表真实加载 → 跨域资源污染 `foreignObject` 光栅化 → **整张截图失败** | 入库统一注入 CSP（`default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:`）：外链被浏览器阻断（画不出，不再 taint），内联样式不受影响；`Store.Add/Update/loadAll` 三处接线，旧快照装载自愈。**不做**字符串手术剥 `<script>`/改写 `src` —— script 本被 sandbox 挡住，外链被 CSP 挡住，正则改写 HTML 易碎 | `canvas/sanitize.go`（新） |
| 2 | 截图强依赖「画布已在浏览器打开」：Agent 无人值守时 `capture_timeout` 高频 | 超时自动 `openbrowser` 拉起画布 + 再等一轮；每进程至多自动开一次（避免连环弹窗）；`Deps.AutoOpen` 开关，`mcp` 子命令开启、测试/CI 关闭（go test 不弹窗） | `mcpserver/server.go`、`main.go` |
| 3 | `design_audit` 只审计体系 StyleSheet，Agent 要的是「我刚摆的这张稿有什么问题」 | 新增 `AuditNodeHTML`：节点级走查（外链资源 fail / emoji 与字符图标 fail / 画板契约 warn / 节点内 font-family 商用判定 warn）；`design_audit` 增加 `node_id` 入参（优先于 system；与 system 均缺报 bad_request） | `design/nodeaudit.go`（新）、`mcpserver/server.go` |
| 4 | `design_tokens` 全量输出体量大（usage 说明 + hover/container 变体 + 阴影动效） | `brief=true`（仅 category=all）：关键语义色的值、字阶 size/weight、间距/圆角阶梯；省略 usage、变体色、阴影、动效 —— 概览后再按类目取全量 | `mcpserver/server.go` `briefTokens` |

### 10.2 验证

- `GOWORK=off go vet ./... && go test ./...` 全模块绿；新增
  `canvas/sanitize_test.go`（注入位置/幂等/Add·Update·旧快照自愈）、
  `design/nodeaudit_test.go`（干净满分 / 外链与 emoji fail / 画板三态 / 字族判定）、
  `mcpserver/enhance3_test.go`（节点级走查契约含报错路径、brief/全量双形态）。
- 真机冒烟：外链资源节点入库带 CSP；设计预览入库带 CSP 且结构完好
  （`ds-page`/`ds-btn` 全数在位）。
- 浏览器实测（IAB）：外链图卡片 `img` 未加载（被 CSP 阻断），
  Element 预览按钮 `backgroundColor = rgb(64,158,255)`（#409EFF，内联样式无损）。

### 10.3 遗留与后续

- 迭代 3（结构与生态）待启动：canvas.js 模块化、节点 HTML 分文件存储 + undo 栈、
  体系数据外置 embed JSON、组件扩充（tabs/table/pagination 优先）、e2e 回归收敛。
- `staticcheck` 本机二进制版本不匹配（导出数据 v2 vs 工具链 v5）仍未跑，`go vet` 已过。
