# mem_go — 基于 MCP 的 AI Agent 记忆系统

给 AI Agent（ZCode / Claude Desktop / Cursor 等 MCP 客户端）提供**长期记忆**的本地服务：
把用户陈述的事实、偏好、事件持久化到本地向量库，并支持语义检索、语义去重合并与语义遗忘。

底层存储使用同目录下的 [GoVector](../govector)（纯 Go 嵌入式向量数据库，BoltDB + HNSW），
**零外部服务依赖**——向量与元数据全部落在一个本地文件里。

```
MCP 客户端（ZCode / Claude / Cursor）
        │  stdio (JSON-RPC 2.0, MCP 协议)
        ▼
┌───────────────────────────── memmcp ─────────────────────────────┐
│  internal/mcpserver          7 个 MCP 工具（store/search/list/…） │
│  internal/memory                                                   │
│    ├─ Engine        语义去重 · 综合重排 · 过滤 · 统计               │
│    └─ Embedder      OpenAI 兼容嵌入 ｜ 离线哈希回退                 │
│         ▼                                                          │
│  GoVector core    BoltDB 持久化 + HNSW 余弦检索（按维度分集合）      │
└────────────────────────────────────────────────────────────────────┘
```

## 功能特性

- **7 个 MCP 工具**：`memory_store` / `memory_search` / `memory_list` / `memory_get` / `memory_delete` / `memory_forget` / `memory_stats`
- **语义去重合并**：同命名空间下相似内容自动合并为一条（内容更新、标签并集、重要度取大、强化计数 +1），阈值由嵌入提供者自适应
- **三维综合排序**：`得分 = 向量相似度 × 重要度权重 × 时间衰减（半衰期默认 30 天）`
- **多级过滤**：命名空间（scope）隔离多用户/多项目、类型（fact/preference/event/knowledge/procedure）、标签
- **维度安全**：集合按嵌入维度命名（`memories_1536`），切换嵌入模型不会污染旧数据
- **双嵌入提供者**：OpenAI 兼容 API（OpenAI/Ollama/vLLM/SiliconFlow 等）；无 Key 时自动回退到离线哈希嵌入，开箱即用
- **持久化**：每次写入即刷盘，进程重启后记忆完整保留

## 快速开始

### 构建

```bash
go build -o bin/memmcp.exe ./cmd/memmcp
```

### 直接运行（离线模式，无需任何 API Key）

```bash
./bin/memmcp.exe -db ./mem_go.db
```

未配置 API Key 时自动使用离线哈希嵌入器（词面相似度，适合体验与测试）。

### 接入真实嵌入模型（推荐）

任意 OpenAI 兼容的 `/embeddings` 接口均可：

```bash
export MEM_EMBEDDING_BASE_URL="https://api.openai.com/v1"   # 或 Ollama: http://localhost:11434/v1
export MEM_EMBEDDING_API_KEY="sk-..."
export MEM_EMBEDDING_MODEL="text-embedding-3-small"
./bin/memmcp.exe -db ./mem_go.db
```

## MCP 客户端配置

### ZCode（项目级 `.mcp.json`）

```json
{
  "mcpServers": {
    "mem-go": {
      "command": "E:\\codes\\go_projects\\mem_go\\bin\\memmcp.exe",
      "args": ["-db", "E:\\codes\\go_projects\\mem_go\\mem_go.db"],
      "env": {
        "MEM_EMBEDDING_API_KEY": "sk-...",
        "MEM_EMBEDDING_BASE_URL": "https://api.openai.com/v1",
        "MEM_EMBEDDING_MODEL": "text-embedding-3-small"
      }
    }
  }
}
```

### Claude Desktop / Cursor

配置文件结构相同（`claude_desktop_config.json` 或 `~/.cursor/mcp.json`）：

```json
{
  "mcpServers": {
    "mem-go": {
      "command": "E:\\codes\\go_projects\\mem_go\\bin\\memmcp.exe",
      "args": ["-db", "E:\\codes\\go_projects\\mem_go\\mem_go.db"]
    }
  }
}
```

## 工具一览

| 工具 | 作用 | 关键参数 |
|---|---|---|
| `memory_store` | 记住一条记忆（重复内容自动合并） | `content`、`kind`、`tags`、`importance`、`scope` |
| `memory_search` | 语义检索，综合排序 | `query`、`limit`、`kind`、`tag`、`scope` |
| `memory_list` | 按更新时间列出（不走向量检索） | `scope`、`kind`、`tag`、`limit` |
| `memory_get` | 按 ID 读取单条 | `id` |
| `memory_delete` | 按 ID 精确删除 | `ids` |
| `memory_forget` | 语义遗忘：删除与描述相似的内容 | `query`、`threshold`、`limit`、`scope` |
| `memory_stats` | 统计总数、类型/命名空间分布等 | — |

## 环境变量

| 变量 | 说明 | 默认值 |
|---|---|---|
| `MEM_DB_PATH` | 数据库文件路径（可用 `-db` 覆盖） | `./mem_go.db` |
| `MEM_EMBEDDING_PROVIDER` | `openai` / `hash`（留空自动判断） | 有 Key 用 openai，否则 hash |
| `MEM_EMBEDDING_BASE_URL` | OpenAI 兼容接口地址 | `https://api.openai.com/v1` |
| `MEM_EMBEDDING_API_KEY` | API Key（也接受 `OPENAI_API_KEY`） | — |
| `MEM_EMBEDDING_MODEL` | 嵌入模型名 | `text-embedding-3-small` |
| `MEM_HASH_DIM` | 离线哈希嵌入维度 | `512` |

## 设计说明

**记忆 → 向量点的映射**：每条记忆是 GoVector 中的一个点；
向量 = 记忆正文的嵌入，payload 携带 `content / kind / tags / importance / scope / created_at / updated_at / access_count`。

**去重策略**：`Store` 时先在同 `scope` 内做近邻检索，相似度达到阈值即视为同一记忆的"强化"——
以新内容覆盖、标签取并集、重要度取较大值、强化计数 +1。
阈值默认由嵌入提供者声明（密集嵌入模型 0.93，离线哈希 0.60），可用 `Config.DedupThreshold` 覆盖。

**检索重排**：候选取 `max(20, 4×limit)` 条，按
`相似度 × (0.7 + 0.3×重要度) × 0.5^(天数/半衰期)` 重排后截断，
兼顾"相关、重要、新鲜"三个维度。

**离线哈希嵌入器**：rune 1/2/3-gram + 整词的符号特征哈希，L2 归一化，完全确定性。
只刻画词面相似度（例如中英文混杂文本的近重复判断弱于真实模型），建议生产使用真实嵌入 API。

## 测试

```bash
go test ./...
```

覆盖：存储/检索、去重合并、scope 隔离、类型/标签过滤、时间排序、按 ID 读写删、语义遗忘、
重启持久化往返（含 payload 类型还原）、维度隔离、参数校验、
嵌入器（确定性/归一化/相似度序/OpenAI 请求形状）、以及 **MCP 全工具集成测试**（内存传输 + 真实协议会话）。

## 目录结构

```
mem_go/
├── cmd/memmcp/           # 入口：环境解析、嵌入器装配、stdio 服务
├── internal/memory/      # 记忆引擎（类型 / 嵌入器 / 引擎）
├── internal/mcpserver/   # MCP 工具注册与装配
└── go.mod                # 依赖 govector（本地 replace ../govector）与官方 MCP SDK
```
