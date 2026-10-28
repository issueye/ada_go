# 会话、存储与子智能体

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§5.7 会话、分支与压缩（含 SQLite 并发）、§5.8 子智能体  
> 版本：0.1 · 2026-10-28

## 5.7 会话、分支与压缩

**存储**：每个工作区一个 SQLite（`~/.adago/workspaces/<hash>/state.db`），WAL 模式，单写者。选 SQLite 而不是 JSONL：需要按线程分页、按时间检索、原子追加、序号约束、插件 KV 与用量统计；同时保留 `adag export` 导出 JSONL，避免锁定。

**并发与多进程访问**：

- **写者唯一**：每个工作区同一时刻只允许一个 Core 持有写连接。Core 启动时对 `<dataDir>/locks/ws-<hash>.lock` 加 OS 独占锁（Unix `flock`，Windows `LockFileEx`）；锁目录在插件与工具的 scope 之外，插件无法删除或替换。拿不到锁返回 `workspace_locked`，不打开数据库。锁随进程退出自动释放；锁文件残留不代表仍被占用。
- **Gateway 不打开 `state.db`**：所有会话读写都经 Core RPC；Gateway 的 `ThreadView` 只是内存投影。这样多进程里只有 Core 是写者，Gateway 重启不影响数据库。
- **CLI 只读**：`export`、`doctor` 使用独立只读连接并设置 `PRAGMA query_only=ON`（能只读打开时加 `mode=ro`，避免 WAL 只读打开对 `-shm` 的依赖问题）；`backup` 用 `VACUUM INTO` 或 Online Backup API 的专用连接，只写目标文件、不改源库。`plugin install` 只写插件目录，不直接写 `plugin_meta`；插件的启用/禁用与热重载由 Gateway 发 RPC、Core 落库。`migrate` 只在 Core 启动、持锁后执行。v1 不提供“停 Core 之外”的写库命令。
- **SQLite 配置**：每个连接 `busy_timeout=5000`、`foreign_keys=ON`；库级 `journal_mode=WAL`、`synchronous=NORMAL`、`wal_autocheckpoint=1000`、`journal_size_limit=64MB`。`NORMAL` 保证崩溃一致性，但掉电可能丢最后一个提交窗口；需要更强持久性时设为 `FULL`。写事务一律 `BEGIN IMMEDIATE`，保持短事务（目标 <10ms）；遇 `SQLITE_BUSY`/`SQLITE_LOCKED` 指数退避重试，最多 3 次。
- **单写者协程**：Core 内所有写操作（entries、runs、interactions、usage、audit、plugin_state）进入有界队列，由唯一写协程串行提交；`seq` 也在这个协程内分配。业务层不得各自开写连接。队列满时 entry/interaction/audit 反压（宁可让轮次等待），usage 可按窗口合并，但绝不丢 entry 或交互。
- **读连接**：WAL 下读写不互斥；分页查询使用短事务，不跨请求持有读事务。导出用单个读事务拿一致快照；长读会阻止 checkpoint，导出结束立即 `wal_checkpoint(PASSIVE)`。
- **备份**：运行中备份用 `VACUUM INTO`（或驱动提供的 Online Backup API，实现前用集成测试确认）生成一致性数据库副本，禁止直接复制 `state.db`/`-wal`/`-shm`。插件目录按 fingerprint 复制并写入 manifest；需要数据库与插件严格一致时先停 Core（或至少暂停插件重载）。停 Core 后可 `wal_checkpoint(TRUNCATE)` 再复制；恢复时停 Core、校验备份、原子替换并删除旧 `-wal`/`-shm`。
- **迁移**：只由持锁的 Core 在启动时执行，事务内更新 `PRAGMA user_version`，失败回滚并拒绝启动；`doctor` 只报告版本，不迁移。
- **部署约束**：`state.db` 必须在本地文件系统；WAL 的共享内存与文件锁在 SMB/NFS 上不可靠，检测到网络盘时拒绝启动或由 `doctor` 报错。一个工作区只支持一个数据目录/一个 Core；跨数据目录双开不受支持，`doctor` 只能检查当前数据目录，不能保证发现另一个数据目录的 Core。
- **故障语义**：Core 崩溃后由 OS 释放锁，下一次启动自动做 WAL 恢复；不自动删除 `-wal`/`-shm`。第二个 Core 得到 `workspace_locked`（带持有者 pid），Gateway 显示“工作区已被另一个 Core 占用”。

这套模型下 SQLite 足够：WAL 只用来让只读进程与单写者并行，不用来支持多个写者。若未来出现多写者需求（多用户、多节点共享工作区），应换成服务型数据库，或把写集中到一个独立进程，而不是放开多进程写同一个 `state.db`。

```sql
CREATE TABLE threads (
  id            TEXT PRIMARY KEY,
  workspace     TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  head_entry_id TEXT,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  archived      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE entries (
  id         TEXT PRIMARY KEY,
  thread_id  TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  parent_id  TEXT,
  seq        INTEGER NOT NULL,
  kind       TEXT NOT NULL, -- message|compaction|branch_summary|model_change|plugin_state|custom
  role       TEXT,          -- user|assistant|tool|system
  payload    BLOB NOT NULL, -- JSON
  tokens     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX entries_seq ON entries(thread_id, seq);
CREATE INDEX entries_parent ON entries(thread_id, parent_id);

CREATE TABLE runs (
  id TEXT PRIMARY KEY, thread_id TEXT NOT NULL, status TEXT NOT NULL,
  started_at INTEGER NOT NULL, ended_at INTEGER,
  error_code TEXT, usage_json TEXT
);

CREATE TABLE interactions (
  id TEXT PRIMARY KEY, thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  run_id TEXT NOT NULL, call_id TEXT, plugin_id TEXT NOT NULL,
  kind TEXT NOT NULL, -- confirm|select|input
  title TEXT NOT NULL, body TEXT NOT NULL, options_json TEXT,
  status TEXT NOT NULL, -- pending|accepted|denied|cancelled|timeout|unavailable
  response_json TEXT, created_at INTEGER NOT NULL, resolved_at INTEGER, expires_at INTEGER
);
CREATE INDEX interactions_pending ON interactions(thread_id, status);

CREATE TABLE plugin_state (
  plugin_id TEXT NOT NULL, key TEXT NOT NULL, value BLOB NOT NULL,
  updated_at INTEGER NOT NULL, PRIMARY KEY(plugin_id, key)
);

CREATE TABLE plugin_meta (
  id TEXT PRIMARY KEY, version TEXT NOT NULL, enabled INTEGER NOT NULL,
  fingerprint TEXT NOT NULL, installed_at INTEGER NOT NULL, last_error TEXT
);

CREATE TABLE usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT, thread_id TEXT NOT NULL,
  provider TEXT NOT NULL, model TEXT NOT NULL,
  input_tokens INTEGER, output_tokens INTEGER, cache_tokens INTEGER,
  cost_micros INTEGER, created_at INTEGER NOT NULL
);

CREATE TABLE audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL,
  kind TEXT NOT NULL, thread_id TEXT, plugin_id TEXT, detail_json TEXT
);
```

**树形流程**：

- 线程 id 为 `th_<workspaceHash>_<ulid>`，entry id 为 ULID；Gateway 可由线程 id 前缀定位工作区。
- entry 通过 `parent_id` 形成 DAG；线程只有 `head_entry_id` 一个指针。
- “编辑并重发”追加一个同级的新用户 entry，并把 head 移到新分支；旧分支不删除。
- 恢复会话：从 head 回溯到最近 `compaction` entry，放入摘要，再从 `first_kept_entry_id` 重放。
- entry 一旦写入不可修改；分支切换只改指针，保证审计与回放。

**压缩**：

- 触发：每次模型请求前与轮次结束后检查；估算 token 超过 `contextWindow - reserve(16k)`，或达到上下文 80%。长轮次因此可以持续运行，不靠步数上限截断。
- 保留最近约 20k token；切点只允许落在 user/assistant 边界，绝不在工具调用与回执之间。
- 摘要为结构化 Markdown：目标、已完成、未完成、改动文件、关键决策、风险；追加一条 `compaction` entry。
- 单个超长轮次无法切分时，用“拆分轮次 + 两段摘要”；摘要失败不丢历史，只标记 `compaction_failed` 并继续，用裁剪兜底；若压缩 + 裁剪后仍超出窗口（例如单条工具结果不可拆），以 `context_overflow` 结束该轮。
- 压缩是 Core 行为，不依赖插件；插件可以观察 `compaction.before/after`，但不能替代摘要（builtin 信任插件除外）。

## 5.8 子智能体（v1.5）

`invoke_subagent` 是一等工具，用于把只读调研、独立小任务委派出去：

| 隔离维度 | 做法 |
|---|---|
| 上下文 | 只给 profile 系统提示 + 委派任务，一次性 |
| 会话 | 临时 SessionStore，不污染主线程 |
| 工具 | 按 profile 裁切；子智能体默认装“静默策略”：无 UI 时写/执行直接拒绝 |
| 取消/终止 | 父 `context` 贯通，父取消即子取消；子轮次与主轮次同一套终止条件，不另设步数上限 |
| 递归 | 子智能体的工具表里永远没有 `invoke_subagent` |
| 续跑 | v1.5 不支持，上下文刻意临时 |

装配而非“另写循环”：`RunTurn` 只换 `ToolCatalog`、`PromptSource`、`SessionStore` 与策略插件集合。父会话只把最终文本 + 摘要 + 用量作为一条工具结果拿回。

---

**相邻文档**：[← 模型适配、审批与取消](03-model-and-approval.md) · [索引](design.md) · [插件模型与后端 API →](05-plugins.md)
