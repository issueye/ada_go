# 测试与性能

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§11 测试与门禁、§12 性能目标  
> 版本：0.3 · 2026-11-10

## 11. 测试与门禁

### 11.1 分层测试

- **领域/引擎**：固定时钟、录制事件、脚本化模型；覆盖工具循环、拒绝、超时、取消、终止条件、压缩、分支重放。
- **Provider 契约**：录制三家 SSE 夹具（含分片、乱序、缺失终止、错误帧、tool call 片段），TS provider 在 moejs 里跑同一组断言；另测长流（>5s）不被脚本超时误杀、取消能穿透到 `http.sse`。
- **协议**：JSON Schema 校验请求/响应；方法常量与 dispatch 覆盖率 100%；事件 golden 回放。
- **存储**：SQLite migration、单写者队列、并发追加、断序恢复、分支回溯、崩溃后 WAL 恢复；多进程：第二个 Core 被工作区锁拒绝、只读 export 与写入并行、`VACUUM INTO` 备份与源库一致、`SQLITE_BUSY` 重试、迁移期间拒绝第二写者。
- **插件**：清单校验、贡献冲突、热重载双代、超时重建、内存上限、FS 越界、symlink 逃逸、动态注册不生效；系统插件 `dist/` 与 `index.ts` 构建一致；系统与第三方走同一入口。（能力位与模块白名单已删除，「未声明能力不可达」不再适用。）
- **系统插件/删除测试**：禁用 `tools-fs`/`tools-shell`/`cron-manager`/`env-manager`/`provider-admin`/`token-usage`/`tool-discovery` 后，工具表、页面、导航、调度、env 注入、供应商 CRUD、用量聚合与 find/exec 入口必须一起消失；基座不补位。
- **内核 seam**：`Compactor`、`SessionStore`、`ToolRunner`、`ModelClient`、`PromptSource` 的替换实现必须通过与默认实现相同的契约测试；安全不变量不可替换。
- **通用原语**：`global.config` 的命名空间隔离、0600、原子写、跨进程锁、限额；`events.read` 的只读、游标、线程/工作区范围与字段脱敏。
- **Node 兼容**：同一语料在 Node 20 LTS 与 moejs 下双跑；模块 `node:`/裸名同一实例、ESM/CJS 互操作；`fs` 根与写成员越界、错误码、16MB/10k 上限；`process.env` 白名单；`child_process` 无 shell、超时、输出上限与进程树；定时器顺序/取消/256 上限；Buffer/TextDecoder/structuredClone；`.node` 拒绝；真实包冒烟。清单与偏差见 [6.14](12-node-compat.md#614-兼容性验证)。
- **前端**：VNode 校验、转义、标签/属性白名单、golden HTML、SSE 合并与断序快照、无 JS 表单链路。
- **UI 设计系统**：token 名/值与 a_da 基线逐项一致（`adag verify ui`）；CSS 无硬编码十六进制色、奇数尺寸与 `dark:` 变体；组件 class/DOM 基线快照；Light/Dark 对比度 ≥4.5:1（大字/图标 ≥3:1）；1600/1280/1024/860/760/500 宽度无横向溢出；≤760px 抽屉与汉堡；`prefers-reduced-motion`、键盘焦点回归、`<details>`/真实链接无 JS 可操作；对话流、工具卡、三类托盘、任务卡、空状态、错误页的 golden HTML 与截图对照 a_da 基线。详见 [7.7.7](07-ssr-frontend.md#777-验收)。
- **E2E**：`adag gateway --fake-provider` + httptest/浏览器（chromedp）：新建线程、流式、交互请求与响应、插件页面、重载插件、Gateway 重启恢复。
- **边界**：零插件启动、禁用系统插件后对应能力消失、`internal/` 不依赖 `plugins/official/`、基座源码无系统插件 id、不存在 Go 插件注册 API。
- **模糊测试**：协议 JSON、VNode、路径 scope、Markdown 清洗。

### 11.2 门禁

```bash
go vet ./...
staticcheck ./...
go test -race ./...
go test ./internal/agent_core/conformance -run TestInvariants
go test ./internal/agent_core/conformance/nodecompat -run TestNodeCompat
go generate ./... && git diff --exit-code   # 生成物必须同步
adag verify                                 # 协议/接线/插槽/清单/文档/UI/边界（11 项）
adag verify ui                              # token/class/DOM 基线与 a_da 一致；无硬编码色与奇数尺寸
adag verify boundary                        # 基座边界：不 import plugins/official/*、源码无系统插件 id、无 Go 插件注册 API
adag doctor --fake                          # 无模型依赖的启动自检
```

`adag verify nodecompat`（模块表/能力位/错误码/偏差表）与 `adag verify capabilities` 已随能力位、模块白名单一起删除（2026-11-10 决策）；Node 兼容语料仍由 `go test ./internal/agent_core/conformance/nodecompat` 覆盖。

测试必须可并行；唯一需要串行的用例是“进程级单例/端口占用”，放在独立包并显式标记。

### 11.3 关键测试用例

| 用例 | 期望 |
|---|---|
| 不装任何插件启动 | 页面可打开、可建线程；工具表为空，显示“未配置供应商/无工具”空态 |
| 禁用 `tools-fs` | `read_file` 等从模型工具表消失，基座不补位 |
| 插件 `while(true){}` | 5s 后 `plugin_timeout`，Runtime 重建，Core 存活 |
| 禁用 `cron-manager` | 定时任务不再调度；基座不读 `cron.json`、不暴露 `/api/cron/fire` |
| 禁用 `env-manager` | env 管理入口与注入策略消失；基座不保留管理语义 |
| 禁用 `provider-admin` | 供应商/模型 CRUD 页面消失；基座只保留只读 `model.list` 与切换 |
| 禁用 `token-usage`/`composer-telemetry` | 对应用量页面/卡片消失；基座只保留 `events.read` 的原始用量行，页面/卡片由插件聚合 |
| 禁用 `tool-discovery` | `find_tool`/`exec_tool` 从模型与工具表消失；注入式工具不受影响 |
| 替换 `Compactor` seam | 通过切点/摘要/失败不丢历史测试；`compaction.*` 仍可见 |
| `global.config` 越权 | 只能读写自己的命名空间；跨命名空间/路径穿越被拒绝 |
| 插件分配 1GB | `ErrMemoryLimit`，插件 `degraded`，其余工具可用 |
| 插件读 `../../secret` | 拒绝，回执 `error`，审计记录 |
| 插件 `import "node:fs"`（未声明能力） | 正常装配并可用：能力位不再决定装什么（2026-11-10 决策） |
| 插件的 `fs.writeFile` 越出工作区 | 调用即抛越界错误；工作区内读写正常 |
| 插件 `process.env.SECRET` | 不在 `backend.env` 白名单时为 `undefined`；密钥只能经 `secret()` 取得 |
| 插件 `child_process.exec("rm -rf /")` | 拒绝：只允许 `execFile`、不经过 shell；审计记录参数 |
| 插件加载 `.node` 原生扩展 | 安装/装载失败，`ERR_NATIVE_ADDON_UNSUPPORTED` |
| 插件使用 `ctx.fetch` | 走宿主 HTTP 桥：仅 http(s)、重定向 ≤5、响应 ≤16MB、超时与取消贯穿 |
| 工具实现返回 `denied` | 模型收到 `denied` 文本（含原因），轮次继续 |
| 工具实现调 `ctx.ui` 但无 UI | 解析为 `unavailable`；工具按自己的逻辑返回 `denied` 或降级 |
| 交互请求 300s 未响应 | 记为 `timeout`，按拒绝处理；线程可继续 |
| 待处理交互期间 Gateway 重启 | 交互记录保留，页面恢复托盘；客户端无法伪造已响应 |
| 取消执行中的命令 | 进程树全部退出，`TurnFinished(cancelled)` |
| 模型连续 50 轮工具调用后给出文本 | 不因步数上限终止，最终 `TurnFinished(completed)` |
| 模型调用 `finish` | 立即收尾，恰好一个 `TurnFinished(completed)` |
| 连续 3 次相同工具 + 相同参数 + 相同回执 | 返回 `loop_detected` 错误回执，轮次继续；第 5 次以 `error` 结束；`repeatable` 工具不受影响 |
| 产品未配置墙钟/token 上限 | 长轮次不被“预算”杀掉，只有模型、错误或用户能结束 |
| 运行中用户取消 | 在模型、工具、插件脚本任一阶段都得到 `TurnFinished(cancelled)`，部分消息保留 |
| SSE 断线 10s 后重连 | 连续则补发，断裂则快照，无重复消息 |
| Gateway 重启 | 线程/交互/插件状态保留，运行轮标记 interrupted |
| 前端插件输出 `onClick` | 校验拒绝，该插槽为空，页面其余正常 |
| UI 复用了旧 token 或多写一个十六进制色 | `adag verify ui` 失败并给出文件与行号 |
| 760px 宽度打开对话页 | 侧栏收为抽屉；汉堡唤出、遮罩/Esc 关闭；主区无横向溢出 |
| 禁用 JS 打开对话页与任务卡 | 正文、交互记录、`<details>` 任务卡与页签链接仍可读可操作 |
| 无 JS 发消息 | 303 回页面，轮询后看到结果 |

---

## 12. 性能目标

> 实测采样（Windows 11 x64、SSD、`-count=1`）：gateway startup 39–47ms、
> first page 42–44ms、fake turn 3ms、core 自检 43–66ms，全部低于下表目标。
> 采样方法：`go test ./internal/gateway -run TestGatewayE2E -v` 与
> `go run ./cmd/adag core --workspace . --self-check`；超标先 `-count=5` 取最小值排除页缓存，
> 再用 pprof 定位。本表是回归雷达，不进入 CI 硬断言。

| 指标 | 目标 |
|---|---|
| Gateway 冷启动 | < 200ms（不含 Core）；实测 39–47ms |
| Core 拉起 + 握手 | < 500ms（SSD，空工作区）；首页含拉起实测 42–44ms |
| 默认产品插件装配 | < 1.2s（系统插件预编译 bundle）；core 自检实测 43–66ms |
| Provider 流式桥接 | p95 < 5ms/事件批（不含网络） |
| 首 token 额外开销 | < 30ms（不含 provider 网络） |
| SSE 增量端到端 | p95 < 100ms |
| 插槽渲染 | p95 < 10ms（热运行时） |
| 插件页面渲染 | p95 < 40ms |
| 插件工具调用开销 | p95 < 5ms（不含实际 IO） |
| Node 兼容层模块导入 | p95 < 10ms（已编译产物，单插件） |
| Node 受控宿主调用 | p95 < 2ms（不含实际 IO） |
| 插件 `fs` 4KB 读 | p95 < 5ms（SSD） |
| 事件重放 10k 条 | < 150ms |
| 空闲内存 | Gateway ~40MB；Core ~60MB + 每插件 8–20MB（默认产品约 150–260MB） |
| 并发 | 单工作区 64 SSE、8 线程同时浏览；全局 8 Core |

优化顺序：先保证事件合并与模板缓存，再考虑 Runtime 池与编译缓存；JIT 不作为默认路径。

---

**相邻文档**：[← 安全与运维](09-security-operations.md) · [索引](design.md) · [目录、里程碑与风险 →](11-structure-roadmap.md)
