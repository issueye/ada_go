---
name: design-workflow
description: 用 adago_design MCP 做界面设计时的标准流程：先定画板尺寸，再查 Token/图标/字体，然后生成预览、走查并汇总台账。用户提到设计方案、组件预览、设计体系对比、画板尺寸、图标或字体规范时读取。
---

# 设计工作流（adago_design MCP + design-canvas 插件）

分工先说清楚，避免用错工具：

| 谁 | 提供什么 | 工具名 |
| --- | --- | --- |
| `adago_design` MCP（经 `mcp-client` 接入） | 设计规范、预览生成、走查、截图 | `mcp_<serverId>_<tool>`，例如 server id 为 `adago_design` 时是 `mcp_adago_design_design_preview` |
| `design-canvas` 插件（本包） | 画布台账与观测 | `design_canvas_sync`、`design_canvas_ledger` |
| `ui-charts` 插件 | 通用图表块 | `ui-charts/bar`、`ui-charts/column` 等 |

> 工具名前缀取决于 `.adago/mcp.json` 里的 server `id` 或 `toolPrefix`。
> 拿不准时先看工具目录里的实际名字，不要凭记忆拼前缀。

## 1. 六步流程（顺序不能换）

**⓪ 建项目**：`project_create`（已有项目用 `project_list` 取 ID）。
设计稿必须归属**已存在的项目**：`design_preview` / `design_screen` / `canvas_add` /
`design_audit` 等新增类工具都要传 `project`，不传会返回 `project_required`。
画布与导出都是按项目作用域走的，不同项目互不可见。

**① 定画板**：`design_artboard`。
不传 `artboard` 就挑一个 ID，会拿到「尺寸契约」：画板尺寸、状态栏/安全区/导航/标签栏、
内容区宽高、栅格、导出倍率。移动端用 `iphone-15`（393×852），Web 用 `web-1440`，
小程序用 `mp-wx`。**后续所有预览都要带同一个画板**，否则两张卡的尺寸不可比。

**② 查规范**：`design_systems` → `design_tokens` / `design_components` /
`design_icons` / `design_fonts`。
要图标就去 `design_icons` 取，不要自己画字符；要字体先看 `commercial` 字段。

**③ 出预览**：`design_preview`（组件组合）或 `design_screen`（login/settings/feed/profile）。
两者都接受 `artboard`，生成后会摆到浏览器画布上，并返回节点 ID 与画布地址。

**④ 走查**：`design_audit`。
除 WCAG 与阶梯检查外，还会报「字体授权」「图标与符号」「尺寸规范」三项；
出现 `fail` 就改 Token 或换图标，不要绕过。

**⑤ 汇总**：`design_canvas_sync`。
把画布现状沉淀成本线程台账，并拿到一段可直接贴进回答的动态块（`design-canvas/canvas-ledger`）。
之后要用就直接 `design_canvas_ledger`（读缓存），不要重复 sync。

## 2. 写预览时的硬规则

- 图标：`<svg class="ds-icon ds-icon--20"><use href="#ds-ic-close"/></svg>`，
  名字取自 `design_icons`。**不要用 emoji**（🔍 ✅ ⚠）或 `▾ › ✕ ◉ ○` 这类字符。
- 尺寸：宽高由画板决定，不要在 `canvas_add` 里随手写 420×320；
  给出 `artboard` 让它推导。
- 字体：只用 `design_fonts` 里 `commercial=true` 的字体；系统字体只作尾部兜底。
- 颜色/间距/圆角：引用 `var(--ds-color-*)` / `var(--ds-space-*)` / `var(--ds-radius-*)`，
  写死数值会被走查判为偏离阶梯。
- 预览是自包含文档：不要引外链字体、图片、CDN 样式（截图走 `foreignObject`，取不到外链）。

## 3. 用台账（本插件）

```
design_canvas_sync            # 第一次：读画布 → 缓存 → 返回可粘贴的动态块
design_canvas_ledger          # 之后：直接取缓存（refresh=true 才重新读）
```

返回内容里已经含一段 ```` ```adag-ui ```` 封套，**整块复制**即可，不要自己改
`renderer` 或编造字段（`additionalProperties: false`，多一个键整块降级）。
台账为空（刚清空画布）时块会整块跳过，这是正常现象，不要补一句「暂无数据」的假卡。

## 4. 失败怎么处理

| 现象 | 含义 | 处理 |
| --- | --- | --- |
| `无法读取画布：画布服务不可达` | adago_design 还没起 HTTP 服务 | 让用户先以 `serve` 或 `mcp` 形态启动；设计与预览仍可继续用 MCP 工具，台账稍后补 |
| 工具目录里没有 `mcp_*` 设计工具 | `mcp-client` 未启用，或 `.adago/mcp.json` 没登记 | 引导用户在插件页启用 `mcp-client`，登记后重载插件（工具列表是装配期结算的） |
| `design_audit` 报「图标与符号」fail | 有人用了 emoji/字符当图标 | 用 `design_icons` 换掉，不要把它当 warning 忽略 |

## 5. 不要做

- 不要用 `canvas_add` 手写整页 HTML 去替代 `design_screen`——会绕过画板与走查。
- 不要声称「已摆上画布」却没有实际调用工具；节点 ID 是唯一证据。
- 不要把 MCP 返回的 HTML 原文大段贴进回答（几十 KB），贴节点 ID + 画布地址即可。
