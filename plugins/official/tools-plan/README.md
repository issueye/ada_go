# 任务计划（tools-plan）

按会话维护任务计划，并把同一份计划渲染成会话区右上角的 Todo 卡片。工具与卡片同属一个插件：禁用即整块能力消失。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-plan` |
| 名称 | 任务计划 |
| 版本 | 0.3.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 前后端同包 |
| 后端能力 | `fs.read`、`fs.write`、`state` |
| 前端贡献 | 插槽 `chat.corner`（`slot.tsx`，order 10）+ `todo.css` |

## 工具与动作

| 名称 | 类型 | access | 作用 |
| --- | --- | --- | --- |
| `update_plan` | 工具 | `read` | 用完整步骤列表整体替换当前会话的计划 |
| `read_plan` | 工具 | `read` | 读取当前会话的计划 |
| `plan` | 只读动作 | — | 给 Todo 卡片取数，返回 `{ plan: { done, total, steps } }` |

`update_plan` 标 `access=read` 是刻意的：它只写插件自己的会话状态文件 `.adag/todo/TODO_<threadId>.json`，不是用户文件编辑，不应触发写/执行审批。

## 存储

- 每个会话一份计划，文件位于工作区 `.adag/todo/` 下：`.adag/todo/TODO_<threadId>.json`。
- 文件内容：`{ threadId, steps: [{ step, status }], updatedAt }`；`status` 为 `pending` / `in_progress` / `done`。
- 会话 ID 中的非法文件名字符会替换为 `_`。
- 旧版本曾把计划放在线程级 state KV；首次读文件时迁移一次，之后文件是唯一真源。
- 读取时对脏数据宽容：空步骤或坏行直接跳过，不让一条坏数据拖垮整张卡片。

## 前端 Todo 卡片

- 数据只来自 `props.data.backend["tools-plan"].slot_chat_corner.plan`。
- `total=0`（没有计划）或动作失败时返回 `null`，不画空卡，也不回退到示例数据。
- 没有会话上下文（首页/概览页的插槽预取）时，`plan` 动作直接返回空计划，避免因“没有会话”把插件标记成降级。
- 卡片可折叠；全部完成时加 `is-done` 状态。

## 失败模式

- 缺少 `ctx.threadId` 时，工具以 `ERR_INVALID_ARG_VALUE` 失败（无法定位按会话隔离的文件）。
- 清单未声明 `fs.read`/`fs.write` 时以 `ERR_ACCESS_DENIED` 失败。
- `steps` 不是数组、步骤为空会报参数错误。

## 构建

- 后端：`index.ts` → `dist/index.js`，由 `node plugins/official/build.mjs` 生成。
- 前端：`slot.tsx` 在渲染时编译；`static/todo.css` 原样嵌入。
- 不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [SSR 前端](../../../docs/07-ssr-frontend.md)
