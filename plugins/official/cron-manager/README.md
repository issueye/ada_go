# cron-manager — 定时任务（系统插件）

任务模型、调度、运行历史全部在本插件；基座不读 `cron.json`、不解析 cron 表达式、不调度、不暴露 `/api/cron/fire`。

## 数据流

```text
管理页 / 前端脚本
  └─ GET/POST /ext/cron-manager/api/* ──▶ index.ts
                                           ├─ ctx.config（global.config 命名空间 cron）读写 tasks
                                           ├─ agent.every(30s) 解析 5 段 cron 并判断到期
                                           └─ ctx.turn.invoke({threadId?, title, prompt}) → Core 轮次
```

- 任务键：`tasks`（数组），含 `id/name/enabled/cron/workspace/action/threadId/lastFiredAt/runs`。
- cron 表达式按宿主本地时区解释（与旧独立调度器一致）。
- `runs` 最多保留 20 条；`started` 表示宿主已受理，不表示轮次完成。
- 同一任务同一分钟只触发一次；tick 间隔 30 秒。
- 任务复用一个 `threadId`；线程被删除时 `turn.invoke` 会重建线程并回写新 id。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/state` | 任务列表（含 `nextRunAt` 预览）与警告 |
| POST | `/create` | 新建任务，返回 `{id}` |
| POST | `/update` | `{id, task}` 保存修改 |
| POST | `/remove` | `{id}` 删除 |
| POST | `/toggle` | `{id, enabled}` 启停 |
| POST | `/trigger` | 立即运行一次（不受启停影响） |
| GET | `/runs?id=` | 运行历史（最新在前） |

任务只有 **AI 任务** 一种：`action.text` 作为提示词交给 `ctx.turn.invoke`。旧 `cron.json` 里的 HTTP 动作已不再支持。

## 迁移说明

- 旧版本任务存于 `~/.adago/cron.json`，由独立 cron 二进制调度；本版本改为 `global.config` 的 `cron` 命名空间，调度在插件内。
- 插件首次发现 `tasks` 为空时会尝试一次性导入旧 `~/.adago/cron.json`（`fs.read`），导入后不再读旧文件；任务定义字段基本一致。
- 禁用本插件后，管理页/导航/调度/历史一起消失，基座不补位。
