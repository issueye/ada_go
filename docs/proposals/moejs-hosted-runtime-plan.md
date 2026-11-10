# B 方案：把 cron 调度收进 moejs（不依赖外部 Node）

> 状态：**仅为方案，未改代码**。评审通过后再进入实现。
> 关系：替代 [gateway-dispatch-process-plan.md](gateway-dispatch-process-plan.md) §9 中「scheduler 作为
> `runtime=node` 外部长驻进程」的做法，P3/P4 的进程管理能力保留。

---

## 1. 结论先行

当前 cron 单例调度进程 `cron-manager/dist/scheduler.js` 必须以**宿主安装的外部 Node** 执行，
这与仓库既有口径冲突：

- [12 §6.10](../../docs/12-node-compat.md)：「**不是 Node 进程**：没有 libuv、没有 V8」（L31）
- `plugins/official/build.mjs:4`：「发布物只带一个 Go 二进制」
- [01 §4.5](../../docs/01-architecture.md)：桌面打包产物 `resources/bin/adag.exe` + `resources/plugins/official/`，
  **不含 node**

而 `internal/gateway/processmgr` 在 `RuntimeNode` 时直接 `spawn "node"`，且
`gateway.New` 创建 Manager 时未传 `NodeCommand`（默认就是 PATH 里的 `node`）。
于是「装了 Node 的机器才有时钟功能」，但文档没写、`doctor` 也没查。

**B 方案要解决的就是这件事：让 scheduler 跑在 moejs 里。**

B 有两个落点，本文件给出的建议是 **B1（网关进程内托管）**，理由见 §6。

---

## 2. 现状核对（实现前的事实基线）

### 2.1 scheduler 需要什么

| 依赖 | 用途 | moejs 现状 |
|---|---|---|
| `node:fs` `readFileSync/writeFileSync/renameSync/mkdirSync` | 直接读写 `$ADAGO_HOME/config/cron.json` | 需 `fs.read`/`fs.write` 能力，可用（但绕过了 `globalconfig` 的锁与限额） |
| `node:path` `dirname/join` | 拼路径 | 可用 |
| `node:os` `homedir()` | `ADAGO_HOME` 为空时的兜底 | **恒为空串**（`nodemods/src/os.js:44-48`，刻意不暴露宿主家目录） |
| `process.env.ADAGO_HOME` | 定位配置目录 | 只有清单 `backend.env` 白名单内的名字可见（`host.go:127-139`） |
| `process.env.ADAGO_GATEWAY_CONTROL_URL/TOKEN` | 调内部发送端点 | **拿不到**：这两个由 processmgr 注入到子进程环境，moejs 里没有等价物，也不该给脚本 |
| 全局 `fetch` | POST `/internal/dispatch/send` | **不存在**：宿主只装 `ctx.fetch`（`agentapi.go:679-681`） |
| `setTimeout` 循环 | 30s tick | 可用（有 256 定时器上限） |
| `process.on("SIGTERM")` / `process.exit` | 优雅退出 | **不存在**：process shim 没有 `on`；`exit` 直接抛 `ERR_ACCESS_DENIED`（`cap_env.go:135-138`） |
| `console.log` | 日志 | 可用，进宿主日志（已被进程日志环收集/脱敏） |

结论：scheduler **不能原样搬进 moejs**，必须改成用宿主 API 的形态（§5.5）。

### 2.2 宿主已有的可用原语

| 原语 | 现状 | 对 scheduler 的意义 |
|---|---|---|
| `ctx.config`（`global.config`） | 机器级命名空间 + 0600 原子写 + **跨进程 flock**（`globalconfig.go:mutate`） | 取代直接读写 `cron.json`，天然与插件共享同一份数据 |
| `ctx.dispatch.send`（`dispatch.send`） | 已落地，sender 由宿主盖章，可跨工作区 | 取代 `fetch` + 控制令牌，**不再需要令牌进进程** |
| `agent.every(ms, fn)` | 每插件 ≤8 条，`ms ∈ [1000, 3600000]`，每次调用注入新 Context + 10s 期限，`Close` 时取消（`runtime.go:383-407`） | 取代 `setTimeout` 循环与信号处理 |
| 每插件独立 moejs Runtime | 64MB 堆、专用 goroutine、Close 即杀 | 取代 `process.exit` 语义 |

### 2.3 当前 scheduler 与插件的共享方式（要改掉的隐患）

`scheduler.ts` 的 `saveRuns` 注释写明：「读与 rename 之间若插件刚好写入，那次插件写入会被覆盖
（窗口在毫秒级）」——因为两边都整份改写同一份 `tasks` 数组。

用 `ctx.config` 之后仍然存在同样问题（`set` 是整键覆盖，没有 CAS）。
**§5.6 给出解法**：把运行历史换成**独立键**，不再回写 `tasks`。

### 2.4 关键约束：Core 会被空闲回收

`supervisor` 的 janitor 按 `IdleTimeout`（默认 15m）回收无 lease 的 Core（`janitor.go:reapIdle`）。
这直接决定 B2 的可行性，见 §6。

---

## 3. 两个落点

| | **B1 网关托管** | **B2 Core 托管 + 选主** |
|---|---|---|
| scheduler 住哪 | Gateway 进程内的一个 moejs Runtime | 某个 Core 的插件 Instance |
| 谁拉起 | `processmgr`（新增 `runtime=hosted`） | 插件装配期 `agent.every` |
| 工作区无关性 | 天然全局单例（网关只有一个） | 需要跨 Core 选主（N 个工作区 N 份循环） |
| 空闲回收影响 | 无（不随 Core 生命周期） | **致命**：唯一的 Core 被回收后任务不再触发 |
| 需要的 moejs 新能力 | 无（现有 API 够用） | 分布式租约/文件锁原语（新能力位或新原语） |
| 对外契约变化 | `processmgr` 多一个 runtime 值 + 归属语义 | 无（回到 `agent.every`），但要新增锁原语 |
| 改动量 | 中 | 中偏大（选主 + 回收豁免） |

---

## 4. 推荐：B1

三条理由：

1. **只有一份**：网关本来就是「一个进程管所有工作区」，把跨工作区单例放这里，不需要选主、
   不需要租约、不需要动回收策略。
2. **不需要放开任何 moejs 边界**：`homedir` 保持空、全局 `fetch` 保持不存在、
   `process.env` 保持白名单——scheduler 改用 `ctx.*` 即可，**安全口径零放宽**。
3. **`processmgr` 的能力全部复用**：注册、引用计数、每 owner 限额、日志环、退避重启
   本来就与「进程是不是 OS 进程」无关，只是 spawn/kill 的实现不同。

---

## 5. B1 详细设计

### 5.1 processmgr：把「被管对象」抽象扩一层

现状 `Process` 接口是 OS 进程语义（`PID()/Wait()/Kill()`，`process.go:221-225`）。
hosted 运行时无 pid、也没有进程树，但**有等价的生命周期**：跑着 → 结束。

设计：**保持接口不变**，让 hosted 句柄实现同一套方法。

| 方法 | OS 进程 | hosted 运行时 |
|---|---|---|
| `PID()` | 真实 pid | `0`（Status 增加 `Kind` 字段区分，不靠 pid==0 猜） |
| `Wait()` | `cmd.Wait()` | 等待 tick goroutine 返回（正常停止或致命错误） |
| `Kill()` | 杀进程树 | 取消 ctx + 关闭 Runtime（幂等） |

新增 `RuntimeHosted Runtime = "hosted"`；`resolveStartSpec` 的 `switch` 多一个分支：
不解析 `Command`，只把 `Entry` 作为「要加载的模块路径」。

`Options` 新增注入点 `HostedSpawn SpawnFunc`（nil 时 `runtime=hosted` 注册直接报
`invalid_spec`，即「本部署没有 hosted 运行时」）。

### 5.2 host：新增「守护模块」运行入口

需要一个新的窄 API（不新增能力位、不改现有 `Load` 语义）：

```
RunDaemon(ctx, script Script, caps *CapabilitySet, hc Context) (*Daemon, error)
  - 与插件同一条编译/链接/执行路径（moejs.NewRuntime + eventloop.New + capsState 安装）
  - 只接受 CapabilitySet 已授予的能力；hc 决定 ctx.* 成员
  - 顶层语句执行完即返回；Runtime 保持存活直到 Stop
Daemon.Close(ctx) error   // 取消定时器、关闭 Runtime
Daemon.Done() <-chan struct{}  // 意外终止（脚本抛出）时关闭
```

差异点（相对 `host.Load`）：

- 不结算 `Contribution`（daemon 不注册工具/钩子/页面）；
- 不需要 `agent:host`（不注册任何贡献）；
- 允许多个实例（网关可能需要多个 hosted daemon）；
- `EntryName` 用 `daemon://<owner>/<name>` 形式的虚拟名，源码来自磁盘绝对路径。

**关键**：`host.Load` 当前要求 `Script.Manifest` 且校验 id/apiVersion（`load.go:85-115`）。
daemon 不是插件清单里的条目，需要一条独立、更窄的校验（绝对路径、后缀 `.js/.mjs`、
大小 ≤1MB、只读一次）。

### 5.3 归属语义（本方案最需要拍板的一点）

现有 `ctx.processes.register` 的语义是「**本插件注册 → 本 Runtime 关闭时注销**」
（`cap_processsupervise.go` 的 `releaseManagedProcesses`）。
这个语义对 hosted 的 cron scheduler **是错的**：Core 被空闲回收、某工作区关掉，
scheduler 不该跟着停。

设计：

| 概念 | 含义 | 谁负责停止 |
|---|---|---|
| `core-owned` | 注册来自 Core 内插件，随该 Core 的 Runtime 关闭注销（现状不变） | Core |
| `gateway-owned` | 注册来自系统插件的「全局常驻声明」，Core 注销**不**停止它 | Gateway（Shutdown 或插件被禁用） |

判定依据**不能**由脚本自选（否则插件能把自己提成常驻）。建议：
`ProcessSpec.Scope == global` **且** owner 是 builtin 系统插件 **且** 部署声明了 hosted 运行时
→ 提升为 gateway-owned；其余一律 core-owned。

同时提供显式清理路径：

- 插件被禁用/卸载：Gateway 侧按 owner 清理（`Endpoint.CleanupKey` 已有类似能力）；
- `Gateway.Shutdown`：先停 hosted，再停 OS 进程。

> 待确认项 A：是否接受「builtin + scope=global」自动提升，还是要求清单显式声明
> （如 `backend.daemons[]`，走清单 Schema 而不是运行期 API）。

### 5.4 网关侧如何装配 hosted 运行时

网关已经允许 import `internal/plugins/host`（`tools/verify/boundary_helpers.go` 的
gateway allowlist 含 `internal/plugins`，且 `gateway.go:34` 与
`internal/gateway/frontend/bridge.go:35` 已经在用），所以不需要动依赖方向门禁。

需要新增的装配件：

1. **config 目录对齐**：`hc.ConfigDir` 必须与 Core 侧一致
   （`cli/session.go:319` 是 `dir(ProvidersPath)/config`；网关要算出同一个）。
   scheduler 与插件必须读写**同一份** `cron.json`，否则页面看到的是旧数据。
2. **`hc.Dispatch`**：网关直接指向自己的 `Dispatcher`（比 Core 绕控制端点更短，
   且不需要给 hosted 运行时发任何令牌）。
3. **能力集**：`host.NewCapabilitySet(manifest, workspaceRoot, hostEnv, secrets)`。
   注意 hosted daemon 的 `workspaceRoot` 语义——scheduler 是全局的，
   建议给一个只读的「插件包目录」作为 root，避免它拿到某个工作区的 fs 根。

### 5.5 scheduler 改写对照

| 现在的做法 | 改成 | 说明 |
|---|---|---|
| `readFileSync($ADAGO_HOME/config/cron.json)` | `await ctx.config.get("tasks")` | 走 `globalconfig`（0600/原子写/flock/限额） |
| `writeEnvelope` + `renameSync` | `await ctx.config.set(...)` | 不再自己实现原子写 |
| `fetch /internal/dispatch/send` + Bearer 令牌 | `await ctx.dispatch.send({...})` | **进程里不再有令牌** |
| `setTimeout(loop, 30000)` + `SIGTERM/exit` | `agent.every(30000, ...)`；停止由 Runtime 关闭 | 不再需要信号处理 |
| `homedir()` 兜底路径 | 删除 | 路径由宿主决定，脚本看不到也不需要 |
| `console.log("[cron-scheduler] ...")` | `ctx.log("info", ...)` 或保留 `console.log` | 两者都进宿主日志 |
| `sameMinute` + `lastFiredAt` 去重 | 保留 | 幂等键仍用 `cron:<taskId>:<minute>` |

### 5.6 运行历史改独立键（修掉覆盖窗口）

现状两边都改 `tasks` 数组。改为：

| 键 | 内容 | 写入方 |
|---|---|---|
| `tasks` | 任务定义（cron/enabled/action/workspace/name） | **只有插件**（页面与本体的 CRUD） |
| `runs` | `{ [taskId]: { lastFiredAt, threadId, runs: [...] } }` | **只有 scheduler** |

好处：

- 两个写入方不再碰同一键，**毫秒级覆盖窗口消失**；
- scheduler 写历史不需要「先读全量再按 id 打补丁」；
- 页面渲染时按 task id join 两键（插件侧一次 `ctx.config.get` 两次即可）。

代价：`cron-manager/index.ts` 的 `loadTasks/saveTasks` 与页面 `/state`、`/runs`
要改成读两键；`cron_get`/`cron_list` 工具同步调整。

### 5.7 删除项

- `plugins/official/cron-manager/scheduler.ts` 的 Node 专属实现 → 改写为 `hosted` 入口（文件名建议 `daemon.ts` → `dist/daemon.js`）
- `processmgr` 的 `RuntimeNode` **保留**（第三方插件仍可声明自己的 node 进程），但
  **官方 cron 不再使用它**，因此发布物不再隐式要求宿主安装 node。
- `internal/gateway/processmgr/scheduler_e2e_test.go` 里对 `exec.LookPath("node")` 的依赖 →
  改为 hosted 运行时的端到端（不再需要系统 node）。

---

## 6. 为什么不选 B2

B2 看起来「更符合插件归属」，但撞上两个硬约束：

1. **空闲回收**：`reapIdle` 在 `leases==0 && 无在途` 且超过 `IdleTimeout` 时回收 Core。
   要让 scheduler 不被打断，必须要么把那个 Core 钉住（等于关掉回收，
   `MaxCores`/内存策略全部失效），要么引入「租约续期」——而租约续期本身又要一个
   leader 选举写者。
2. **选主需要新原语**：`globalconfig` 的 flock 是 Go 侧 API，脚本面上只有 `ctx.config`
   的 get/set/delete/list（无 CAS、无锁）。要么新开一个通用 `ctx.lock` 原语，
   要么用「写时间戳 + 读回判定」的弱租约——两者都要新增契约、测试与文档，
   且弱租约在时钟回拨/进程暂停下有重复触发的风险。

也就是说 B2 的代价不是「改几行代码」，而是**新增一个跨进程协调原语**；
B1 的代价只是「给 processmgr 多一个 runtime 分支」。
按「不为单一插件新增专用能力位」（06 §6.10 规则 3）的口径，B1 明显更轻。

---

## 7. 改动清单（文件级）

### 7.1 新增

| 文件 | 作用 |
|---|---|
| `internal/plugins/host/daemon.go` | `RunDaemon` / `Daemon`：编译、链接、执行顶层模块并保持 Runtime |
| `internal/plugins/host/daemon_test.go` | 可用/不可用能力、停止、异常终止、超时 |
| `internal/gateway/processmgr/hosted.go` | hosted 的 `Process` 实现（goroutine + cancel → Wait/Kill） |
| `internal/gateway/processmgr/hosted_test.go` | 启停、重启策略、日志环、归属 |
| `plugins/official/cron-manager/daemon.ts` + `dist/daemon.js` | 改写后的 scheduler |

### 7.2 修改

| 文件 | 改动 |
|---|---|
| `internal/gateway/processmgr/process.go` | 新增 `RuntimeHosted`；`resolveStartSpec` 分支；Status 增 `Kind` |
| `internal/gateway/processmgr/manager.go` | `Options.HostedSpawn`；归属提升判定 |
| `internal/gateway/processmgr/endpoint.go` | 接受 `runtime=hosted`；owner/scope 校验 |
| `internal/gateway/processmgr/control.go` | hosted **不注入**控制令牌（它直接用 `hc.Dispatch`） |
| `internal/plugins/host/cap_processsupervise.go` | `runtime` 允许 `hosted`；`gateway-owned` 不在 Runtime 关闭时注销 |
| `internal/gateway/gateway.go` | 装配 hosted spawn、config 目录对齐、`hc.Dispatch` 直连 Dispatcher |
| `plugins/official/cron-manager/index.ts` + `scheduler.ts`（删除） | 注册 `runtime: hosted`；页面读 `tasks`+`runs` 两键 |
| `plugins/official/cron-manager/plugin.json` | 能力去掉 `process.supervise` 之外的多余项；如走清单声明则加 `backend.daemons` |
| `plugins/official/build.mjs` | 多入口产出保持不变（`daemon.ts → dist/daemon.js`） |

### 7.3 不改

- `internal/plugins/host/nodemods/src/os.js`（`homedir` 保持空）
- 全局 `fetch`（保持不存在）
- `process.env` 白名单语义
- `internal/gateway/dispatch`（`ctx.dispatch.send` 已就绪）
- `internal/globalconfig`（flock/限额已就绪）

---

## 8. 测试计划

| 场景 | 断言 |
|---|---|
| hosted daemon 启动 | 顶层代码执行；`Done` 未关闭 |
| daemon 调用 `ctx.config` | 与 Core 侧插件读到同一份 `cron.json`（跨进程 flock 生效） |
| daemon 调用 `ctx.dispatch.send` | 投递落到 Dispatcher；sender = `plugin:cron-manager` |
| tick 到期 | 同一任务同一分钟只投递一次（幂等键） |
| 非法能力 | 未授予 `global.config` 时 `ctx.config` 不存在（`ERR_ACCESS_DENIED` 或 undefined） |
| daemon 抛出 | 视为崩溃 → 按 `restart=on_failure` 退避重启，日志环可见 |
| 停止 | `Close` 后 Runtime 释放、不再投递 |
| 归属 | Core 被回收后 scheduler 仍在跑（gateway-owned 语义） |
| 无 hosted 运行的部署 | 注册返回 `invalid_spec`，不静默降级 |
| 回归 | 删掉「必须装 node」后，`go test ./...` 与本机无 node 环境一致通过 |

`adag verify` 不新增检查项；但建议在 `boundary` 或新检查里加一条：
**官方插件不得声明 `runtime: node` 的进程**（防止再退回外部 Node 依赖）。

---

## 9. 文档改动

| 文档 | 改动 |
|---|---|
| `docs/05-plugins.md` §6.2.1 | `process.supervise` 行的 runtime 说明加 `hosted`；补归属（core-owned / gateway-owned） |
| `docs/06-plugin-runtime.md` §6.10 | 进程表：插件长驻进程分「OS 进程」与「宿主托管模块」两类 |
| `docs/09-security-operations.md` §9.5 | hosted daemon 的资源上限（同插件 Runtime：64MB / 单次 5s / 定时器 256） |
| `docs/11-structure-roadmap.md` §16 | 进度补一条：cron 调度收进 moejs，发布物不再要求宿主安装 node |
| `docs/12-node-compat.md` | 保持「不嵌入 Node」，补一句「官方插件不使用外部 node 进程」 |
| `plugins/official/cron-manager/README.md` | 数据流图：删掉 spawn node，改成网关托管；说明 `tasks`/`runs` 两键 |

---

## 10. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| 网关进程内跑插件 JS | 网关内存/CPU 被占用，理论上影响 SSR | 独立 Runtime + 64MB 上限 + tick 内只做读配置/判定/投递；`agent.every` 有 10s 期限 |
| `ctx.config` 的 `set` 是整键覆盖 | 若仍用同一键会覆盖 | §5.6：历史独立成 `runs` 键，写入方分离 |
| 归属提升被滥用 | 第三方插件试图把自己变成常驻 | 只对 builtin 生效；非 builtin 一律 core-owned |
| hosted 与 OS 进程语义混淆 | 运维看不出差异 | `Status` 增 `Kind`；日志前缀区分；`list` 输出带 kind |
| 迁移期旧数据 | 老 `cron.json` 的运行历史在 `tasks` 里 | 首次加载时把 `tasks[].runs` 搬到 `runs` 键，之后只读新键（一次性迁移，双写禁止） |
| 回滚 | 若 hosted 不可用 | 保留 `RuntimeNode` 实现；插件侧切换只需改 `runtime` 一个字段；文档标注「回滚路径」 |

---

## 11. 待确认（实现前必须拍板）

**A. 归属提升的声明方式**
- A1：`scope=global` + builtin + 部署声明 hosted → 自动提升（运行期推导）
- A2：清单显式声明（`backend.daemons[]`，进 Schema）→ 更明确，但要改 Schema 与校验

**B. `hosted` 的入口形态**
- B1a：新 `daemon.ts` 单文件入口（与 `index.ts` 并列，`dist/daemon.js`）
- B1b：仍叫 `scheduler.ts`，只是内容改成 `ctx.*` 形态（少改一个文件名，但名字会误导「这是外部进程」）

**C. 运行历史键名**
- `runs`（推荐）还是 `taskRuns`？需要与 `token-usage` 等既有命名习惯一致。

**D. 是否需要 `AbortSignal` 语义**
`agent.every` 的 handler 拿的是新 Context，但长 tick（例如投递超时 60s）可能跨越下一个 tick；
建议 daemon 内部用「上一拍未完则跳过本拍」的自旋保护（不引入新原语）。
