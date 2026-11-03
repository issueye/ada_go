# cron-manager — 定时任务管理插件

管理全局定时任务的官方插件（bundled builtin）：提供侧栏「定时任务」管理页
（`/ext/cron-manager/cron`），任务的增删改查、启停、立即运行与运行历史。

## 架构

```
管理页（本插件，ctx.cron 能力）──读写──▶ ~/.adago/cron.json
                                            ▲ 读取/回写
adag gateway ──拉起守护──▶ mcp_projects/cron 的 cron.exe（到期执行）
   │                          │ http 动作：直接发请求
   │ Acquire(core)            │ agent 动作：POST /api/cron/fire（Bearer）
   ▼                          ▼
 adag core ◀── turn.start ── gateway
```

- 本插件只是**管理面**：任务的校验与持久化在宿主能力 `cron.admin`
  （`internal/cronstore`，0600 原子写 + 跨进程文件锁）；
- **调度与执行**在独立 cron 二进制（`mcp_projects/cron`），由网关自动拉起守护；
- agent 动作经网关送达 core：`thread.create` + `turn.start`，每个任务复用一个专用线程。

## 路由

| 路由 | 说明 |
|---|---|
| `GET /state` | 任务列表（含 nextRunAt 预览）+ 警告 |
| `POST /create` | 新建（name/cron/action） |
| `POST /update` | 整体替换定义字段（运行态 runs/threadId 不动） |
| `POST /remove` / `POST /toggle` | 删除 / 启停 |
| `POST /trigger` | 立即运行（cron 二进制下个 tick 消费，默认 15 秒内） |
| `GET /runs?id=&limit=` | 运行历史（最新在前，每任务最多 20 条） |

## 动作类型

- **HTTP 请求**：GET/POST、可选 headers/body/超时（1–120 秒，默认 15 秒），由 cron 二进制直接发送；
- **AI 任务**：提示词经网关 `POST /api/cron/fire` → `thread.create` + `turn.start`。
  无人值守轮次中，需要审批的工具调用按 Core 现有交互超时（默认 300 秒）自动过期，不会卡死。
