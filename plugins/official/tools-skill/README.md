# 技能搜索（tools-skill）

`skill_search`：在已装配技能的 `name` / `description` 里做子串匹配，返回候选列表。
它不读技能正文；装配期已把每个技能的 `path` 注入系统提示，模型需要正文时用
`read_file` 按 path 直接读取。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-skill` |
| 名称 | 技能搜索 |
| 版本 | 0.2.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | 无（技能元数据由宿主按产品能力位注入） |

## 工具：`skill_search`

| 参数 | 说明 |
| --- | --- |
| `query` | 必填，非空关键词；对技能名与简介做大小写不敏感的子串匹配（不分词） |

返回内容：

- 每个候选一行：`名称（来源[，来自插件 id]）：简介`。
- 最多显示前 50 个；超过时附总候选数与截断说明。
- 没有匹配时明确返回“没有匹配”和已装配技能总数。
- `details` 里带 `query`、`total`、`hits`、`truncated`、`paths`。

## 正文读取

- 系统提示里的技能 XML 带 `path`（SKILL.md 宿主绝对路径）；模型用 `read_file` 读取。
- 基座不保存技能正文，也不提供技能读取工具或 RPC；`skill.list` 只返回元数据。
- 工作区外路径的读取只对 builtin/verified 的 `ctx.fs.readText` 开放（官方
  `read_file` 是 builtin）。

## 依赖与失败模式

- 技能表来自宿主注入的 `ctx.skills`（name/description/path，只读）。
- 产品需启用 `capabilities.skills`（`coding` / `headless` 为 true，`skeleton` 为 false）。
- 宿主没有提供 `ctx.skills` 时，工具以 `ERR_ACCESS_DENIED` 失败，而不是悄悄返回空列表。
- `query` 为空时以 `ERR_INVALID_ARG_TYPE` 失败。

## 为什么正文按需读取

技能正文可能很长；装配期只注入名称、简介与路径，模型需要时再用 `read_file` 读取，
避免把多份 SKILL.md 一次性拉进上下文。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成，桌面打包随 `resources/plugins/official` 发布（不再进二进制）。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [Agent 循环与工具](../../../docs/02-agent-loop.md)
