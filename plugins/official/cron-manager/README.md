# cron-manager — 定时任务（系统插件）

任务模型、调度、运行历史全部在本插件；基座不读 `cron.json`、不解析 cron 表达式、不调度、不暴露 `/api/cron/fire`。

## 数据流

```text
管理页 / 前端脚本
  └─ GET/POST /ext/cron-manager/api/* ──▶ index.ts（Core 内）
                                           ├─ ctx.config（global.config 命名空间 cron）读写 tasks
                                           ├─ ctx.dispatch.send（立即运行 / 单轮投递）
                                           └─ ctx.processes.register（装配期注册单例调度进程）

单例调度进程（网关 ProcessManager 拉起，scope=global，runtime=node）
  ├─ 直接读同一份 $ADAGO_HOME/config/cron.json（globalconfig 跨进程文件锁）
  ├─ 自己解析 5 段 cron、判断到期（tick 30s）
  ├─ POST /internal/dispatch/send（令牌只带发送范围；sender 由网关盖章为 plugin:cron-manager）
  └─ 把 lastFiredAt / threadId / runs 合并回配置
```

为什么要独立进程：Core 按工作区拉起、空闲回收，`agent.every` 在多工作区下会让同一任务被多次触发。scheduler 是全局单例，同一时刻只有一份到期判断；多工作区同时打开也只跑一个。

## 关键约束

- 任务键：`tasks`（数组），含 `id/name/enabled/cron/workspace/action/threadId/lastFiredAt/runs`。
- cron 表达式按宿主本地时区解释（与旧独立调度器一致）。
- `runs` 最多保留 20 条；`started` 表示调度层已受理，不表示轮次完成；`error` 记录上游业务码（如 `thread_busy`）。
- 同一任务同一分钟只触发一次：幂等键 `cron:<taskId>:<minute>` 交给网关调度层去重。
- 任务复用一个 `threadId`；线程被删除时由调度层决定重建策略并把新 id 回写。
- 调度进程崩溃按 `on_failure` 退避重启（0.5s→30s）；页面 `/state` 会显示 `scheduler.running/lastError`。
- 插件被禁用/重载时宿主按「本插件注册过哪些进程」注销引用，归零后调度进程停止；管理页/导航/历史一起消失。

## 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/state` | 任务列表（含 `nextRunAt` 预览）、警告与调度器状态 |
| POST | `/create` | 新建任务，返回 `{id}` |
| POST | `/update` | `{id, task}` 保存修改 |
| POST | `/remove` | `{id}` 删除 |
| POST | `/toggle` | `{id, enabled}` 启停 |
| POST | `/trigger` | 立即运行一次（不受启停影响） |
| GET | `/runs?id=` | 运行历史（最新在前） |

任务只有 **AI 任务** 一种：`action.text` 作为提示词交给调度层投递。旧 `cron.json` 里的 HTTP 动作已不再支持。

## 管理工具（全部 `discovery: true`）

不进模型默认工具清单，只经 `find_tool` / `exec_tool` 使用：

| 工具 | access | 作用 |
| --- | --- | --- |
| `cron_list` | read | 列出任务、下次运行时间与调度器状态 |
| `cron_get` | read | 单个任务详情与运行历史 |
| `cron_create` | write | 新建任务 |
| `cron_update` | write | 修改定义（未传 enabled 时保留原启停） |
| `cron_delete` | write | 删除任务 |
| `cron_set_enabled` | write | 启用/停用 |
| `cron_run` | exec | 立即执行一次（会触发一轮对话） |

## 迁移说明

- 旧版本任务存于 `~/.adago/cron.json`，由独立 cron 二进制调度；本版本改为 `global.config` 的 `cron` 命名空间，调度交给单例 scheduler 进程。
- 插件首次发现 `tasks` 为空时会尝试一次性导入旧 `~/.adago/cron.json`（`fs.read`），导入后不再读旧文件；任务定义字段基本一致。
- 需要 `process.supervise` 与 `dispatch.send` 两个 builtin-only 能力；没有网关控制面（CLI 直连）时调度进程注册不到，页面会显示调度器不可用。
