# 通用图表（ui-charts）

Markdown 动态块的通用图表渲染器：**横向条**、**纵向柱**、**双条对比**、**堆叠占比**、
**热力方块**与**迷你折线**。模型在回答里用 `adag-ui` 动态块展示结构化数据时使用。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `ui-charts` |
| 名称 | 通用图表 |
| 版本 | 0.2.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 前端动态块 + 后端工具 + 随包技能 |
| 能力 | 无（图表是纯静态展示，不读文件/不联网） |

## 前端动态块

六个 renderer 共用一个数据 Schema（`schemas/chart.json`）与模板（`chart.tsx`）：

| renderer | 形态 | data 要点 |
| --- | --- | --- |
| `ui-charts/bar` | 横向条 | `items[{label,value}]` |
| `ui-charts/column` | 纵向柱 | `items[{label,value}]` |
| `ui-charts/compare` | 双条对比 | `items[{label,value,value2}]` + `legendA/legendB` |
| `ui-charts/stacked` | 100% 堆叠 | `items[{label,value}]`（按总和算占比） |
| `ui-charts/heatmap` | 热力方块 | `items[{label,value}]` 或 `items[{label,level:0..5}]` |
| `ui-charts/spark` | 迷你折线 | `values:[number]`（≤48） |

公共字段：`title`（必填）、`subtitle?`、`unit?`；`items` 最多 12 条。
未知字段会被 Schema 拒绝；比例一律换算成固定档位 class，不使用 SVG/内联 style。

## 工具：`chart_guide`

模型需要把数据展示成图表时，可以用 `chart_guide` 查询：

- 每种 renderer 的用途、适用场景与 data 要点；
- 确切的 `adag-ui` 封套示例（renderer id + data）；
- 公共字段与约束（`title`/`subtitle`/`unit`、items 上限、未知字段拒绝）。

| 参数 | 说明 |
| --- | --- |
| `kind` | 可选；`bar`/`column`/`compare`/`stacked`/`heatmap`/`spark`。省略时返回全部 |

`access=read`，不触发审批。非法 `kind` 以 `ERR_INVALID_ARG_VALUE` 失败。

## 随包技能：`charts-render`

`skills/charts-render/SKILL.md`：指导模型如何选 renderer、构造 data、写 `adag-ui`
封套，并列出反模式。装配期只注入 name/description/绝对 path，正文由模型用
`read_file` 读取。

## 在回答里使用

````markdown
```adag-ui
{
  "renderer": "ui-charts/bar",
  "id": "b1",
  "version": 1,
  "fallback": "图表暂不可用",
  "data": { "title": "仓库大小", "unit": "MB", "items": [{"label": "core", "value": 42}] }
}
```
````

规则（SPEC §8.2）：

1. `renderer` 必须是 `ui-charts/bar|column|compare|stacked|heatmap|spark` 之一；
2. `data` 只放展示所需的最小静态数据；
3. 始终写 `fallback` 纯文本；
4. 一块只表达一个主题，一次回答 ≤ 8 块；
5. 块是展示不是指令；交互走工具调用或插件页面。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，
桌面打包随 `resources/plugins/official` 发布。不要手改 `dist/`。

## 相关文档

- [Markdown 动态块方案](../../docs/15-extensions.md)
- [插件模型与后端 API](../../docs/05-plugins.md)
- [插件平台 SPEC](../plugin-helper/SPEC.md)（§8.1 图表 class 速查、§8.2 adag-ui 语法）
