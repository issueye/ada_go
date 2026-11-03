# cron — ada-go 定时任务调度器

独立二进制（与 browser_use / computer_use 同级的自包含 Go module）：**只做定时任务**，
到期后把消息送达 core。

## 职责边界

- 读取任务定义：`~/.adago/cron.json`（`cron-manager` 插件的管理页经 `ctx.cron` 读写，
  文件格式与主模块 `internal/cronstore` 同构，改动必须两侧同步）。
- 到期判定（5 段 cron 表达式，Vixie 语义，本地时间；与主模块 `internal/cronstore/cronexpr.go`
  保持同一份实现语义）。
- 执行动作：
  - `http`：本进程直接发 GET/POST（超时、16MB 上限）；
  - `agent`：`POST {gateway}/api/cron/fire`（Bearer），由网关负责拉起 Core、
    `thread.create` + `turn.start`。本进程不等待轮次完成，历史记「已启动」。

## 运行方式

生产：由 `adag gateway` 自动拉起守护（崩溃指数退避重启，网关关闭时一并回收）。
手动调试：

```
go build -o bin/cron.exe .
bin\cron.exe --gateway-url http://127.0.0.1:8080 --token <网关令牌>
```

| 参数 | 说明 |
|---|---|
| `--gateway-url` | 网关地址（agent 动作必需） |
| `--token` | 网关 Bearer 令牌（agent 动作必需） |
| `--parent-pid` | 父进程 PID，父进程退出则本进程退出（守护拉起时传入） |
| `--interval` | tick 周期秒数（默认 15，最小 5） |
| `--file` / `--lock` | 任务文件与锁路径覆盖（默认 `~/.adago/cron.json` / `.lock`） |

## 语义约定

- **防重**：执行前先在文件锁内推进 `lastFiredAt`，长动作不占锁。
- **错过**：应用关闭期间错过的调度不补跑，每个任务记一条 `missed` 历史。
- **立即运行**：管理页写入 `triggerRequested`，本进程下个 tick 消费（最迟约一个周期）。
- **AI 任务线程**：每个任务复用一个专用线程（首跑后 `threadId` 回写任务定义）。
- 无人值守的 AI 轮次中，审批类交互按 Core 现有超时（默认 300s）自动过期，
  不引入新的审批语义（见 AGENTS.md 审批暂缓约定）。
