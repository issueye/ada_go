# 技能搜索（tools-skill）

`skill_search`：在已装配技能的 `name` / `description` 里做子串匹配，返回候选列表。它是薄搜索层——不读技能正文、不做安装；正文始终走基座的 `read_skill`。

## 插件信息

| 项 | 值 |
| --- | --- |
| id | `tools-skill` |
| 名称 | 技能搜索 |
| 版本 | 0.1.0 |
| 信任级别 | `builtin`：随二进制发布，只能启用/禁用 |
| 形态 | 后端 |
| 能力 | 无 |

## 工具：`skill_search`

| 参数 | 说明 |
| --- | --- |
| `query` | 必填，非空关键词；对技能名与简介做大小写不敏感的子串匹配（不分词） |

返回内容：

- 每个候选一行：`名称（来源[，来自插件 id]）：简介`。
- 最多显示前 50 个；超过时附总候选数与截断说明。
- 没有匹配时明确返回“没有匹配”和已装配技能总数。
- `details` 里带 `query`、`total`、`hits`、`truncated`。

## 依赖与失败模式

- 技能表来自宿主注入的 `ctx.skills`（只读元数据，不含正文）。产品需启用 `capabilities.skills`（`coding` 为 true，`headless` 为 false）。
- 宿主没有提供 `ctx.skills`（老宿主或技能能力未启用）时，工具以 `ERR_ACCESS_DENIED` 失败，而不是悄悄返回空列表。
- `query` 为空时以 `ERR_INVALID_ARG_TYPE` 失败。

## 为什么不做正文搜索

技能正文可能很长；按名读正文由基座 `read_skill` 负责，避免把多份 SKILL.md 拉进上下文。`tools-skill` 只解决“技能多、模型记不全名字”的检索问题。

## 构建

`index.ts` 是源码，`dist/index.js` 由 `node plugins/official/build.mjs` 生成并被 `go:embed` 进二进制。不要手改 `dist/`。

## 相关文档

- [插件模型与后端 API](../../../docs/05-plugins.md)
- [Agent 循环与工具](../../../docs/02-agent-loop.md)
