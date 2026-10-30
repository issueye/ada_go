# 默认审批策略（policy-default）

`tool.beforeCall` 的默认审批规则。基座本身不内置审批规则，只提供钩子与 `ctx.ui`；本插件就是产品装配时默认启用的那套策略。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `policy-default` |
| 名称 | 默认审批策略 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | `ui.ask` |
| 角色 | `tool-policy` |
| 钩子 | `tool.beforeCall`（`note` 级） |

## 审批模式

模式来自插件设置 `settings.mode`（配置项 `[plugins.settings.policy-default] mode`），缺省 `ask`。基座不解释这个字段，只透传给插件。

| 模式 | 读 | 写 | 执行 / 网络 |
| --- | --- | --- | --- |
| `readonly` | 允许 | 拒绝 | 拒绝 |
| `ask`（默认） | 允许 | 询问 | 询问 |
| `auto` | 允许 | 允许（工作区内） | 询问 |
| `full` | 允许 | 允许 | 允许 |

## 行为要点

- 只读工具（`access=read`）在任何模式下都直接放行。
- **越界防御**：无论什么模式，参数里出现工作区之外的绝对路径（`path`/`file`/`dest`/`cwd`/`dir`/`target`/`src` 等键，深度 3 以内）都拒绝。工作区之外只放行 `/tmp`。
- **失败关闭**：需要询问但没有可用 UI（CLI/print、SSE 断线、`ctx.ui` 缺失或 `available=false`）时，写/执行/网络一律拒绝，而不是默认允许。
- 询问通过 `ctx.ui.confirm` 发起，超时 300 秒；询问过程抛错同样按拒绝处理。
- 用户拒绝、取消或超时都返回 `deny`，并带可读原因。

## 适用产品

`coding` 与 `headless` 声明启用；`skeleton` 未声明。禁用本插件后没有默认审批规则，写/执行类调用将无人裁决（产品级失败关闭由装配校验负责）。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [模型与审批](../../../docs/03-model-and-approval.md)
- [插件模型与后端 API](../../../docs/05-plugins.md)
