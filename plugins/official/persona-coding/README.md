# 编码人格（persona-coding）

向系统提示注入一段编码助手人格与工程纪律。它不注册工具、不申请任何能力，只贡献提示词。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `persona-coding` |
| 名称 | 编码人格 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端（仅提示词） |
| 能力 | 无 |

## 行为

- 通过 `registerPrompt` 注册 id 为 `persona` 的提示片段，`priority: 10`。
- 基座始终先注入不可移除的安全前言；本片段排在其后，与其它插件的提示按优先级排序。
- 内容保持克制：先读后改、小步前进、最小改动、提交卫生、诚实汇报、安全边界。项目级约定交给工作区技能（`.adago/skills`），不写进人格。
- 禁用本插件后，系统提示只剩基座的安全前言，不保留任何编码纪律。

## 适用产品

`coding` 声明启用；`headless` 与 `skeleton` 未声明。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成并被 `go:embed` 进二进制。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [Agent 循环与系统提示](../../../docs/02-agent-loop.md)
