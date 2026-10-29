# 插件运行时与治理

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§6.4 moejs 运行时与沙箱、§6.5 装配期结算、§6.6 钩子、§6.7 技能、§6.8 官方插件、§6.9 信任与热重载  
> 版本：0.1 · 2026-10-28

## 6.4 moejs 运行时与沙箱

**所有权**：每个插件一个 Runtime，绑定一个专用 goroutine。所有调用通过带缓冲的 channel 串行进入；Runtime 的值不跨 goroutine。v1 固定单 Runtime；`pool` 字段保留给后续版本，若未来开放多实例，每个实例独立初始化，共享状态必须走 `ctx.state`，模块级变量不保证跨实例一致。

**初始化**（伪代码）：

```go
opts := []quickjs.Option{
    quickjs.WithMemoryLimit(64 << 20),
    quickjs.WithStackSize(1 << 16),
    quickjs.WithMaxCallDepth(2000),
    quickjs.WithLocale("en-US"),
}
if caps.Trust != TrustBuiltin {
    // 所有插件拿到的都是已编译 JS（官方预编译进 dist/，第三方在安装期编译），
    // 运行期不需要代码生成；第三方额外禁用 codegen 作为纵深防御。
    opts = append(opts, quickjs.WithoutCodeGeneration())
}
rt := quickjs.New(opts...)
defer rt.Close()

// 只安装声明过的能力；没有安装的能力在脚本里不存在。
host := NewHostBridge(pluginID, capabilities, scope, state, secrets)
rt.SetModule("agent:host", host.Module())

// 受限 stdlib：只装能力位允许的 Node 模块；path/util/events/buffer 等纯计算模块
// 始终可用，fs/fetch/child_process/process.env 按能力安装。见 6.12。
// console 也由 stdlib 安装，Stdout/Stderr 接到结构化日志 writer。
if err := stdlib.Install(rt, nodeConfig(pluginID, caps, workspace)); err != nil {
    return err
}

loader := newBundleLoader(compiledBundle) // esbuild 产物，虚拟文件系统
rt.SetModuleLoader(loader.Load)
ns, err := rt.EvalModule(entryName, source)
factory, _ := ns.Get("default")           // export default function (agent) {}
ret, err := factory.CallContext(ctx)
if err != nil {
    return err
}
if _, err := ret.Await(ctx); err != nil { // Await 对非 thenable 原样返回，对 Promise 结算
    return err
}
```

**调用**：

- 工具执行：`fn.CallContext(ctx, args)`；`args` 由宿主从 JSON 解码为 Go 值，结果只接受 JSON 可序列化值，最大 1MB。单次预算取 `ToolDef.timeoutMs`，默认 120s，而不是 5s 的脚本默认值。
- Provider 流：`stream` 是长任务，预算为建连 15s、空闲 90s、总时长 30 分钟；不受 5s 调用超时约束，但必须响应轮次取消。
- 异步：插件函数可能返回 Promise；宿主用 `Value.Await(ctx)` 结算，`Await` 会运行 Runtime 的 job 与宿主异步工作。
- 钩子：`fn.CallContext(ctx, event)`；默认 200ms，最大 2s；超时记 `plugin_timeout`，按钩子失败方向处理。
- 交互与宿主长调用：脚本等待 `ctx.ui`、`ctx.exec`、`ctx.http.sse`、`subagent.invoke` 时暂停脚本调用超时计时，由各自的期限（交互 300s、命令 120s、provider 空闲 90s/总 30 分钟、子智能体跟随父轮次取消）负责；否则 5s/200ms 的脚本上限会误杀正常的长任务。
- 路由/动作：`fn.CallContext(ctx, request)`；默认 5s。
- 宿主异步：插件里 `await` 的宿主函数返回 Promise；宿主用 `rt.Go`/`AsyncWork` 在别的 goroutine 完成工作，结果回投到 Runtime 的 goroutine。

**回收策略**：

- 超时、超内存、引擎内部错误、脚本 panic → 标记该 Runtime 为脏，关闭并重建；当前调用返回 `plugin_crashed`，插件状态变为 `degraded`，下一次调用前重新初始化。
- 连续 3 次失败 → 自动禁用插件，页面提示原因；用户可以手动重载。
- 运行时关闭时取消它启动的所有异步工作，避免 goroutine 泄漏。

**Node 兼容模块**：默认只安装纯计算模块；`fs`、`fetch`、`child_process` 与 `process.env` 按能力安装，`http`/`net`/`dns`/`worker_threads`/`vm` 不安装。模块矩阵、成员裁剪、错误码与 Node 20 双跑语料见 [6.12](12-node-compat.md#612-node-兼容原生库)。

**为什么不在脚本里给 `node:fs` 全量**：`stdlib.FS` 已经按根目录与只读限制，但“能读整个工作区”和“能读整个磁盘”是不同风险。ada-go 默认只授予工作区根；读插件目录、配置目录、密钥目录需要单独能力位，且 `secret` 只按名字取值。只读时写成员不是返回 `undefined`，而是访问即抛 `ERR_ACCESS_DENIED`，避免能力差异被误判成版本差异。

**前端渲染运行时**：Gateway 侧独立一套 Runtime 池，只执行 TSX 产物，不安装任何 stdlib；`WithoutCodeGeneration()` 关闭 `eval/new Function`（TSX 在构建期已转换，不需要运行时代码生成）。每个页面/插槽渲染 100ms/300ms 超时，超时重建。

## 6.5 装配期结算

加载流程（两阶段）：

```
扫描来源（嵌入的 plugins/official/ 或磁盘插件目录） → 解析/校验 manifest
   → 官方取预编译 bundle；第三方编译 TS
   → 创建 Runtime → 执行入口
   → 工厂调用 register* 收集到 staging
   → 校验（重名、schema、能力、钩子等级、插槽名、模板可编译）
   → 原子提交：provider 目录 / 工具目录 / 提示词 / 动作 / 路由 / 钩子链 / 前端注册表
   → 运行期只读
```

- 任何一个贡献非法，整个插件回滚，其他插件不受影响；错误记录到 `plugin_meta.last_error` 与页面。Node 模块导入在链接期校验：没装 `fs` 却 `import "node:fs"` 属于装配失败，不是运行期空值。
- 运行期再次 `registerTool` 等调用只记警告，不改变当前目录；要生效必须 `plugin.reload`。
- 重载采用“双代”：新 Runtime + 新贡献构建完成后，原子替换注册表；在途调用继续用旧代，结束后关闭旧代。
- 钩子链按 `priority` 排序；同级按插件 id 稳定排序，保证回放确定性。

## 6.6 钩子

15 个点位，全部有真实调用方；成对点位必须成对出现（`turn.before/after`、`model.beforeRequest/afterResponse`、`tool.beforeCall/afterCall`、`compaction.before/after`）。

| 点位 | 时机 | 允许动作 | 失败方向 |
|---|---|---|---|
| `session.start` | Core 装配完成、会话加载后 | observe, note | 开 |
| `session.end` | Core 关闭前 | observe, note | 开 |
| `thread.created` | 新线程创建后 | observe, note | 开 |
| `user.message` | 用户消息追加后、进模型前 | observe, note | 开 |
| `turn.before` | 轮次开始、终止条件初始化前 | observe, note, abort（内置） | 开 |
| `turn.after` | 轮次结束、TurnFinished 前 | observe, note | 开 |
| `model.beforeRequest` | 请求模型前 | observe, note, replace（内置）, abort（内置） | 开 |
| `model.afterResponse` | 收到终止信号后 | observe, note | 开 |
| `tool.beforeCall` | 参数校验后、执行前 | observe, note, deny, replace（内置）, abort（内置） | **关（拒绝）** |
| `tool.afterCall` | 工具回执生成后 | observe, note | 开 |
| `compaction.before` | 生成摘要前 | observe, note | 开 |
| `compaction.after` | 压缩 entry 写入后 | observe, note | 开 |
| `interaction.requested` | 交互请求发出后 | observe, note | 开 |
| `plugin.loaded` | 插件贡献提交后 | observe, note | 开 |
| `error` | 轮次内可恢复错误 | observe, note | 开 |

动作分级：

- `observe`：只读，任何插件。
- `note`：向界面/日志附加说明，不改变数据面。
- `deny`：仅 `tool.beforeCall`；拒绝理由作为工具结果。
- `replace`：仅 builtin 信任插件、仅 `tool.beforeCall`（替换参数，替换后重新校验）与 `model.beforeRequest`（替换系统提示/注入上下文）。
- `abort`：仅 builtin 信任插件、仅 `turn.before` / `model.beforeRequest` / `tool.beforeCall`。

约束：

- 第三方插件不能进入控制面（不能改轮次、不能改模型请求、不能 abort）；官方插件进入控制面也必须有清单声明与门禁检查。
- 钩子超时默认 200ms；超时/异常不阻塞主循环（安全点除外，默认拒绝）。
- `tool.beforeCall` 中的 `ctx.ui` 等待不计入 200ms 钩子预算，而受交互超时（默认 300s）与轮次墙钟上限（若配置）约束；交互返回后剩余逻辑仍受 2s 上限。
- 钩子内不能再次触发同一钩子（防递归）；调用深度上限 2。
- 钩子收到的上下文是 JSON 快照，不是可变的引擎对象；要改数据必须返回动作，由引擎裁决。

`tool.beforeCall` 事件是 `{ callId, tool, access, input, threadId }`；`input` 是已校验的参数快照。策略插件的典型写法（与 pi 的 permission gate 同构）：

```ts
agent.on("tool.beforeCall", async (event, ctx) => {
  if (event.access === "read") return;                 // 只读直接放行
  if (!ctx.ui?.available) {
    return { action: "deny", reason: "没有可用交互界面，按失败关闭处理" };
  }
  const ok = await ctx.ui.confirm(
    "允许执行这个工具？",
    event.tool + "\n\n" + JSON.stringify(event.input, null, 2),
    { timeoutMs: 300_000, signal: ctx.signal }
  );
  if (ok !== true) {
    return { action: "deny", reason: "用户拒绝、取消或超时" };
  }
});
```

基座只负责把 `ctx.ui` 的请求送到 Gateway、等待响应并返回；它不知道这次询问叫“审批”，也不知道什么工具该问。

## 6.7 技能

- 目录：`plugins/<id>/skills/<name>/SKILL.md` 与 `<workspace>/.adago/skills/<name>/SKILL.md`。
- 遵循 Agent Skills 约定：YAML frontmatter 的 `name` 必须等于目录名（1–64，小写/数字/连字符），`description` 不超过 1024 字符。
- 装配期只注入 name/description（XML 片段）到系统提示；模型需要时调用 `read_skill` 读取正文，渐进披露，避免把全部技能塞进上下文。
- 技能冲突（同名）由工作区优先；冲突记录警告，不静默覆盖。

## 6.8 官方插件（随包发布，可禁用）

| 类别 | 插件 | 形态 | 贡献 |
|---|---|---|---|
| Provider | `provider-openai-chat`、`provider-anthropic-messages`、`provider-openai-responses` | TS | `registerProvider`；桥接成 `ModelClient` 适配器 |
| 工具 | `tools-fs` | TS | `read_file` / `write_file` / `edit_file` / `list_files` / `search_files` |
| 工具 | `tools-shell` | TS | 命令执行与后台任务；进程树管理由宿主能力完成 |
| 工具 | `tools-git` | TS | 单工具 `git`，按 action 分派 |
| 工具 + UI | `tools-plan` | TS + TSX | `update_plan`/`read_plan` 工具，以及 `chat.corner` 的 Todo 卡片（同一插件的前端段，取自己的 `plan` 只读动作） |
| 工具 + UI | `tools-ask` | TS + TSX | `ask_user` 工具，以及 `thread.tray` 的提问卡片（同一插件的前端段，数据来自线程交互投影） |
| 工具 | `tools-skill` | TS | `read_skill` |
| 工具 | `tools-subagent` | TS | `invoke_subagent`（v1.5） |
| 策略 | `policy-default` | TS | `tool.beforeCall` + `ctx.ui`；readonly/ask/auto/full；角色 `tool-policy` |
| 提示词 | `persona-coding` | TS | `registerPrompt`：编码助手人格与领域规则 |
| UI | `provider-admin` | TS + TSX + JS | 模型设置：Master-Detail 管理供应商/端点/API 格式/密钥/模型清单，拉取 `/models`，切换当前模型；页面经插件 route 调用，密钥只显示尾号；`composer.left` 插槽贡献模型设置入口（基座不写死插件 id/路由） |
| UI | `token-usage` | TSX | 工作区/线程 Token 用量页面与概览卡片（`usage.get`） |
| UI | `ui-hello-tsx` | TSX | 示例：页面、仪表盘、API、插槽 |

官方插件不是基座：它们与第三方插件使用同一个 moejs 宿主、同一份清单与同一套注册契约；审批规则由 `policy-default`（或其他 `tool-policy` 插件）提供。区别只有信任级别（可用控制面钩子与 `exec`/`ui.ask`/`net.sse`/`model.admin` 等受信能力）与随包发布。基座源码不出现这些 id；产品声明选择启用哪些，禁用后能力必须消失；禁用策略插件后写/执行工具立即被基座拒绝。

## 6.9 信任、签名与热重载

- `builtin`：预编译 bundle 随二进制发布，只能启用/禁用，不能用磁盘同名插件覆盖；运行时仍走 moejs，与其他插件同一套能力与审计。
- `verified`：`manifest.sig` 为 Ed25519 签名，覆盖清单 + 文件哈希；公钥在配置里。签名失败按第三方处理并提示。
- `third-party`：加载前在插件页显示能力清单；默认禁止 `exec`、禁止控制面钩子、禁止前端脚本。
- 安装：`adag plugin install ./dir` 或 `.zip`，校验路径穿越、大小上限（50MB）、文件类型；复制到目标目录后原子替换。
- 热重载：`POST /plugins/{id}/reload` → Gateway 通知 Core → Core 重载后端 → Gateway 重载前端；两侧都成功后状态变为 `ready`；任一侧失败保留旧代并显示错误。
- 版本：插件 id + 版本 + 文件指纹写入 `plugin_meta`；官方包指纹取嵌入 bundle 的哈希，第三方指纹变化才重新编译，编译缓存在 `~/.adago/cache/plugins/<id>/<fingerprint>/`。

---

**相邻文档**：[← 插件模型与后端 API](05-plugins.md) · [索引](design.md) · [SSR 前端 →](07-ssr-frontend.md)
