---
name: mem-go-memory
description: 使用 mem-go MCP 长期记忆服务的操作规范。当 memory_store / memory_search / memory_forget 等 memory_* MCP 工具可用，且用户要求记住、忘记或回忆信息，陈述个人偏好、重要事实、项目约定，或需要恢复历史上下文时使用本技能。
---

# mem-go 记忆系统使用规范

mem-go 是挂载在 MCP 上的长期记忆库：数据持久化在本地向量数据库，跨会话可用。
7 个工具：`memory_store`、`memory_search`、`memory_list`、`memory_get`、`memory_delete`、`memory_forget`、`memory_stats`。

核心心智模型：**记忆是用户委托你保管的，写入要克制，删除必须确认。**

## 1. 何时写入（memory_store）

满足任一条件时写入：

- 用户明确要求记住："记住……"、"以后都……"、"帮我记一下"
- 用户陈述了跨会话有价值的信息：个人偏好、身份背景、项目约定、做过的重要决定
- 对话中形成了明确的结论或工作流，且用户表现出希望延续的意图

不要写入：一次性任务细节、闲聊、你自己的推理过程、以及**密码 / token / API Key 等机密**（除非用户明确点名要求保存，此时也要先提醒风险）。

写入前无需查重——同 scope 下语义重复的内容会自动合并为一条（action 返回 `updated`），
重复存储等价于"强化"该记忆。直接调用即可。

一次调用示例：

```json
{ "name": "memory_store", "arguments": {
  "content": "用户偏好用 SQLite 做本地原型，生产环境才上 PostgreSQL",
  "kind": "preference",
  "importance": 0.7,
  "tags": ["数据库"]
} }
```

### 参数选择

- **kind**（五选一）：`fact` 客观事实 ｜ `preference` 偏好习惯 ｜ `event` 发生的事 ｜ `knowledge` 领域知识/结论 ｜ `procedure` 步骤方法。拿不准就用 `fact`。
- **importance**（0~1）：用户核心身份与长期偏好 0.8~1.0；一般事实与约定 0.5~0.7；临时性信息 0.2~0.4。省略时默认 0.5。
- **tags**：短词、复用既有词汇（可先 `memory_list` 看已有标签风格），便于后续精确过滤。
- **scope**：单用户单项目场景一律留空（默认 `default`）。仅当用户明确要求按人/按项目隔离时才设置，并保持该 scope 下的后续读写一致——**不要自创 scope**，否则写入的记忆自己都检索不到。

### 写入后的反馈

用一句话确认即可，例如："已记住（更新了既有的偏好记忆）。" 不要粘贴工具返回的原始 JSON 给用户。

## 2. 何时检索（memory_search / memory_list）

- 回答的问题可能涉及用户的历史、偏好或既定约定时，**先 `memory_search` 再回答**，避免答非所忆
- 用户问"你还记得吗"、"我之前说过什么"时，用其原话或同义改写做 `query`
- 需要按时间线浏览（"最近记了什么"、"列出所有 procedure"）时用 `memory_list`；语义找内容用 `memory_search`

检索示例：

```json
{ "name": "memory_search", "arguments": { "query": "测试环境怎么部署", "limit": 5 } }
```

要点：

- 结果按 相似度×重要度×时间衰减 排序，`score` 高的排前；引用时给出原文，标注记忆 ID 方便后续删除
- **检索为空就直说"没有相关记忆"，绝不编造记忆内容**
- 检索是低成本的，但不要每轮对话都盲目检索——只在记忆确实可能改善回答时调用

## 3. 何时遗忘（memory_forget / memory_delete）

删除不可恢复，必须谨慎：

1. 用户要求忘记某事时，先 `memory_search` 用相同 query 预览会命中哪些记忆
2. 向用户展示将删除的内容，**得到确认后**再执行 `memory_forget`
3. 只有用户给出确切记忆 ID（比如刚从检索结果里选定的）时才用 `memory_delete`

`memory_forget` 是语义匹配删除，改述也能命中，但也可能误伤相近记忆：

- 命中范围比预期大 → 调高 `threshold`（默认 0.8，范围 0~1，越高越保守）
- 或用 `limit` 限制删除条数（默认 5），确认无误后再放宽

## 4. 工具速查

| 场景 | 工具 | 必填参数 |
|---|---|---|
| 记住一条信息 | `memory_store` | `content` |
| 语义找记忆 | `memory_search` | `query` |
| 按时间/标签浏览 | `memory_list` | — |
| 读取单条 | `memory_get` | `id` |
| 按 ID 删 | `memory_delete` | `ids` |
| 按语义删 | `memory_forget` | `query` |
| 看库存概况 | `memory_stats` | — |

可选过滤参数 `kind` / `tag` / `scope` 在 search、list、forget 中通用。

## 5. 排障

- 工具不可用（`mcp__*memory*` 不存在）→ 检查 MCP 客户端配置是否指向 `bin/memmcp.exe`，构建方式与客户端配置见项目 `README.md`
- 语义检索效果差、近义内容无法合并 → 大概率运行在离线哈希嵌入模式（未配置 API Key 时的回退），只有词面相似度；配置 `MEM_EMBEDDING_API_KEY` + `MEM_EMBEDDING_BASE_URL` 接入真实嵌入模型后重启服务
- `memory_stats` 可确认服务端实际的嵌入提供者与记忆总量，排障先看它
