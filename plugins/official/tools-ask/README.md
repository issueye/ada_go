# 提问（tools-ask）

`ask_user`：用 `ctx.ui` 向用户提问（confirm / select / input，一次可问多个），并把待答项渲染成交互托盘里的提问卡片。没有可用界面或用户拒绝时失败关闭。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-ask` |
| 名称 | 提问 |
| 版本 | 0.2.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 前后端同包 |
| 后端能力 | `ui.ask` |
| 前端贡献 | 插槽 `thread.tray`（`slot.tsx`，order 10）+ `ask-user.css` |

## 工具：`ask_user`

| 形态 | 参数 | 结果 |
| --- | --- | --- |
| `confirm` | `question` | `yes` / `no` |
| `select` | `question` + `options`（2–8 个） | 选中的候选项 |
| `input` | `question` | 用户输入的一行文本 |

- 单问用 `question` / `kind` / `options`；多问用 `questions` 数组（最多 5 个）。
- 单问直接走对应的 `ctx.ui` 方法；多问走一张分步表单（`ctx.ui.form`），一次收集完再交给模型。
- 工具标 `access=read`：提问本身没有外部效果，能不能问由 `ui.ask` 能力位决定（只授予 builtin/verified）。

## 失败关闭

以下情况一律以 `ERR_ASK_DENIED` 失败，模型不应重复追问：

- `ctx.ui` 不存在，或 `available=false`（无头模式、连接断开）。
- 用户拒绝、取消或超时（宿主返回 `null`）。
- `select` 返回候选项之外的值，或 `input` 提交空字符串。
- 表单返回非法 JSON，或回答数量与问题数不一致。

拿到答案是普通工具结果：轮次继续，模型在同一次运行里接着处理回答。

## 前端提问卡片

- 数据来自 `props.data.interactions`（线程待答交互的只读投影）。
- 提交仍走基座交互路由 `/threads/{id}/interaction/{requestId}`，审批/提问的安全语义由基座负责，插件只决定怎么展示。
- 没有待答项时返回 `null`，由基座默认托盘接手。
- `kind=form`（多题分步表单）让位给基座托盘渲染，插件不重复实现翻页。

## 构建

- 后端：`index.ts` → `dist/index.js`，由 `node plugins/official/build.mjs` 生成。
- 前端：`slot.tsx` 在渲染时编译；`static/ask-user.css` 原样嵌入。
- 不要手改 `dist/`。

## 相关文档

- [模型与审批](../../../docs/03-model-and-approval.md)
- [插件模型与后端 API](../../../docs/05-plugins.md)
- [SSR 前端](../../../docs/07-ssr-frontend.md)
