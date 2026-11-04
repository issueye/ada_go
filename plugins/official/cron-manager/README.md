# cron-manager — 定时任务管理插件

管理全局定时任务的官方插件（bundled builtin）：提供侧栏「定时任务」管理页
（`/ext/cron-manager/cron`），任务的增删改查、启停、立即运行与运行历史。

## 架构

```
管理页（本插件，ctx.cron 能力）──读写──▶ ~/.adago/cron.json
                                            ▲ 读取/回写
adag gateway ──拉起守护──▶ mcp_projects/cron 的 cron.exe（到期执行）
   │                          │ agent 动作（唯一的动作）：POST /api/cron/fire（Bearer）
   │ Acquire(core)            │
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
| `POST /trigger` | 立即运行（cron 二进制下个 tick 消费，默认 5 秒内） |
| `GET /runs?id=&limit=` | 运行历史（最新在前，每任务最多 20 条） |

## 动作类型

任务只有 **AI 任务** 一种：提示词经网关 `POST /api/cron/fire` → `thread.create` + `turn.start`。
无人值守轮次中，需要审批的工具调用按 Core 现有交互超时（默认 300 秒）自动过期，不会卡死。
旧的 HTTP 动作已移除：`cron.json` 里残留的 HTTP 任务会被跳过并给出警告。

## 工作区

每个任务可以选一个工作区（存为绝对路径 `workspace` 字段），到期后网关在对应工作区拉起
Core 并执行；任务线程按工作区隔离，切换工作区后线程会重建。

- 不选工作区（`workspace` 为空）时落到**默认工作区**（网关工作区列表第一个，即管理页里
  显示的「综合工作区」）；
- 任务专用线程被用户删除时，下一次触发会检测到并在同一次运行里自动重建线程，不会丢掉这一轮；
- 工作区列表由网关 `GET /api/workspaces`（只读 JSON）提供，与页面上的工作区选择器同源，
  包含 FilePicker 本次会话新打开的目录；
- 任务指定的工作区不在列表里时，触发会明确失败（运行历史里能看到原因），不会悄悄换到
  别的工作区执行。
