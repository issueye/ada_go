# browser-use 开发计划(优化与功能强化)

> 状态:v1 · 2026-11-04  
> 范围:`mcp_projects/browser_use/`(应用)+ `plugins/official/browser-use/`(插件)  
> 依据:2026-11-04 全量代码走查(应用 ~9.6k 行 Go + 1.9k 行注入 JS;插件 1.9k 行 TS)  
> 配套文档:[README.md](README.md) · [DESIGN.md](DESIGN.md) · [插件 README](../../plugins/official/browser-use/README.md)

---

## 目录

1. [背景与目标](#1-背景与目标)
2. [现状摘要](#2-现状摘要)
3. [问题清单](#3-问题清单)
4. [开发批次](#4-开发批次)
5. [里程碑总览](#5-里程碑总览)
6. [验证门禁与工作流](#6-验证门禁与工作流)
7. [风险与依赖](#7-风险与依赖)
8. [明确不做](#8-明确不做)

---

## 1. 背景与目标

browser-use 0.3.0 已经跑通「桌面浏览器 + AI agent 控制层」的核心闭环:标签页、语义快照
(含 ref 定位与 diff)、动作、moejs 脚本引擎、HTTP/MCP/CLI 三通道、租约与遮罩、审计。
当前的问题集中在四类:

1. **正确性**:少量并发竞态与生命周期脱节,长会话下会放大;
2. **资源**:页内运行时的几处无界增长,长驻 SPA 会话内存与 CPU 缓慢劣化;
3. **能力断层**:应用已实现但插件未透传(events 长轮询、read 格式、定位高级参数),
   模型实际能力低于应用真实能力;
4. **结构债**:服务端巨型 switch、插件 14 个同构样板、双端错误码无单一事实来源。

**目标**:批次 1-2 消除正确性/资源问题并补齐关键测试;批次 3-4 以最小成本把模型的
实际能力抬到应用上限;批次 5 降低后续所有功能的边际成本。

---

## 2. 现状摘要

三层结构(均为同仓代码,无外部服务依赖):

| 层 | 位置 | 职责 |
|---|---|---|
| 应用 | `mcp_projects/browser_use/` | WebView 多标签、快照/ref、动作、脚本引擎、`/v1/*` HTTP、MCP(stdio + Streamable HTTP)、CLI |
| 插件 | `plugins/official/browser-use/` | 工具形状(browse\_* 14 个)、结果裁剪、启动检测、15s 超时规避(异步 + CLI 双兜底) |
| 手册 | `plugins/official/browser-use/skills/browser-use/SKILL.md` | 模型工作流指引(与实现基本同步) |

值得保留、继续沿用的设计决策:

- 动作单漏斗 `begin()`(`internal/browser/actions.go:310`)— 同 tab 串行、遮罩/租约集成点;
- 错误信封 `{code,message,hint,details}` 跨端对齐(`internal/browser/errors.go`);
- console wait 用页内 pending Promise 免轮询(`runtime.js:537-568`);
- dialog 策略制(agent 预设下一轮策略,不事后逐条应答);
- ref 指纹恢复(WeakRef 死后按 role+name 重绑);
- 异步 `/v1/calls` 与「副作用不重放」的区分(`plugins/.../index.ts:448-468`)。

---

## 3. 问题清单

> 文件:行号以 2026-11-04 代码为准。修复时若行号漂移,以语义定位为准。

### 3.1 P0 — 正确性

| # | 问题 | 位置 | 说明 |
|---|---|---|---|
| A1 | NewTab 超限竞态 | `internal/browser/manager.go:146-150` | MaxTabs 检查后放锁再创建 WebView,并发新建可超限 1 个 |
| A2 | 租约过期不打断在途动作 | `manager.go:99-120` + `actions.go:1021` | expireLoop 可在长动作(如 120s wait network)执行中清租约、解锁输入;租约不随动作续期 |
| A3 | CloseTab 不清 `t.Page` | `manager.go:197-235` + `actions.go:381-383` | destroy 后在途动作报 `CodeInternal` 而非语义化 `tab_closed` |
| A4 | URL 白名单不对称 | `manager.go:502` vs `manager.go:532` | agent goto 拒 `blob:`,页内导航放 `blob:`,两份名单应收敛 |
| A5 | MCP 5min 上限撞脚本 MaxTimeout | `internal/mcp/server.go:36` + `config.go:104` | 硬编码 5min == Script.MaxTimeoutMS=300s,且 MCP 路径无 +5s slack,最长脚本双 deadline 同时到期 |
| A6 | screenshot 强制 inline | `internal/mcp/mcp.go:89-97` | 无条件 `m["inline"]=true`,覆盖调用方显式 `inline:false` |
| A7 | wire 解包错误被吞 | `actions.go:482, 529, 573, 600, 621, 641` | `_ = json.Unmarshal`,畸形返回静默变零值 Receipt |
| A8 | 配置吞错 | `config.go:140` + `config.go:85-87` | config.json 解析错误被忽略;UIConfig 无 json tag,`ui` 节永不生效 |
| A9 | 错误码无单一事实来源 | `runtime.js:1234, 1381, 1175, 1815, 1853, 1918` | JS 产出 `not_editable`/`no_frame`/`frame_timeout` 等码不在 Go 常量表(`errors.go:28-62`) |

### 3.2 P1 — 性能与资源

| # | 问题 | 位置 | 说明 |
|---|---|---|---|
| B1 | `state.refs` 无界增长 | `runtime.js:849-860` | 指纹字符串不可 WeakRef 化,每次快照最多 +400 条,永不清理 |
| B2 | `text=` 定位全 DOM O(N) 样式扫描 | `runtime.js:907-928` | `deepQueryAll("*")` + 逐元素 `getComputedStyle`;`Wait for=text` 另每 100ms 全文 includes(`actions.go:1364-1376`) |
| B3 | network wait 100ms 轮询 | `actions.go:1110-1114` | 与 console wait 的页内 Promise 方案不对称 |
| B4 | 截图 4 次编解码 | `screenshot.go:126-133, 232-238` | capture→decode→缩放→encode,inline 再读盘 base64 |
| B5 | 插件每调用一次健康检查 | `plugins/.../index.ts:149-158` | 缓存 runtime 也要 GET /v1/health(2.5s 超时),每次工具调用多一次往返 |
| B6 | jobs sweep 只在 create 时触发 | `internal/control/jobs.go:95` | 已结束任务(含内联截图 result)可驻留内存到进程退出 |
| B7 | KeepSources 无保留期 | `internal/script/engine.go:258-268` | 磁盘无界增长 |
| B8 | args 反复反序列化(最多 3 次) | `server.go:278` / `tools.go:19` / `audit.go:111` | 对 4MB body 有感知;可与批次 5 重构合并 |

### 3.3 P2 — 安全边界(均为应用自身边界,不涉及宿主策略)

| # | 问题 | 位置 | 说明 |
|---|---|---|---|
| C1 | token 无轮换/吊销,X-Client-Id 可伪造 | `server.go:56, 391-394` | 本地进程可冒充他人 release 租约、污染审计归因 |
| C2 | MCP HTTP 默认无鉴权 | `internal/mcp/run.go:21` | 无 `--token` 时任意本地进程可驱动浏览器 |
| C3 | Bind 可配非 loopback 无守卫 | `config.go:66` + `server.go:48-52` | 配错即暴露局域网 |
| C4 | `/v1/health` 无鉴权泄露信息 | `server.go:108-118` | pid/port/feature 泄露;页面可 goto 控制端口读到 |
| C5 | 审计静默失败/丢弃无计数 | `audit.go:44-58, 96-106` | 写失败与队列满丢弃均无计数器、无日志 |

### 3.4 P3 — 能力缺口

**应用侧**(DESIGN.md 已规划未做):全页截图(DESIGN §7.6)、拖拽、嵌套 iframe 跨域 relay
(DESIGN §6.6)、下载进度/取消、请求 mock、localStorage/sessionStorage 工具、popup opener、
人类活跃检测(DESIGN §10)。

**插件侧**(应用已有、模型用不到 — 价值最高):

| # | 缺口 | 位置 |
|---|---|---|
| D1 | events 长轮询无封装,`browser_call` 白名单不含 `events` | `index.ts:1900-1916` |
| D2 | cookies/files/frames/downloads/dialogs 裸 JSON 透传无 schema/formatter | `index.ts:1115-1118` |
| D3 | 应用定位/读取高级参数未透传:read `format: markdown/links/tables`、snapshot `root/roles/include_frames`、type `slowly`、click `button/modifiers` | `index.ts:1390-1399, 1430-1438, 1132-1144` |

### 3.5 P4 — 工程质量

| # | 问题 | 位置 |
|---|---|---|
| E1 | audit 包零测试(RedactArgs 为安全敏感逻辑) | `internal/audit/audit.go:111-168` |
| E2 | config 包零测试;control 同步主路径 /v1/call 无测试 | `config.go:141-177` / `server.go:210-222, 387-396` |
| E3 | 租约/动作槽并发零测试;截图纯函数(downscale/cropRGBA/annotate)无测试 | `tab.go` / `screenshot.go` |
| E4 | 服务端 schema 不做校验,args struct 与 schema 双份维护 | `tools.go:40-624` vs `spec.go:16-279` |
| E5 | 插件 14 个 registerTool 同构样板(可泛化一半代码) | `index.ts` |
| E6 | 审计条目构造两份、tabID 回退三份 | `server.go:338-363` / `tools.go:652-664`;`tools.go:33, 634` / `server.go:345-351` |
| E7 | 文档口径漂移:DESIGN 脚本超时 120s/300s vs 实现 60s/110s;SKILL 可操作等待 5s vs DESIGN 30s;截图目录口径不一 | `DESIGN.md` / `SKILL.md:130, 170, 229` |
| E8 | 插件规避层脆弱:超时靠宿主中文文案匹配、清洗靠正则剥 `(5001 provider_error)` | `index.ts:448-451, 476-481` |

---

## 4. 开发批次

> 每项任务给出:改动点、验收标准。行号是编写时快照,以语义为准。
> 工作量估算按单人专注开发。

### 批次 1 — 正确性修复(估 2 天)

| 任务 | 改动点 | 验收标准 |
|---|---|---|
| T1.1 MaxTabs 竞态 | `manager.go` NewTab:检查移入创建后的加锁段,或持锁预留名额 | 并发 NewTab(MaxTabs=2,发起 5 个)后 `len(tabs) <= 2`;新增单测 |
| T1.2 动作续租 | `manager.go` expireLoop 跳过 actionMu 持有中的 tab;`actions.go` begin/done 之间加心跳(30s 续租) | 130s 的 wait network 期间遮罩不消失、`browser_status` 控制权保持;新增单测(模拟 expireLoop 与长动作并发) |
| T1.3 CloseTab 语义错误 | `manager.go` CloseTab 置 `t.Page=nil`;`actions.go:381` invoke 判 nil 返回 `tab_closed` | 关闭后 in-flight 动作返回 `tab_closed`(复用 `human_nav_test.go` 的模式) |
| T1.4 URL 白名单收敛 | `manager.go:492-536` 两份名单合并为一个函数(参数:来源是 goto 还是页内导航) | 新增表驱动单测覆盖 javascript:/data:/file:/blob:/自定义 scheme 两来源 |
| T1.5 MCP 超时对齐 | `mcp/server.go:36` 改读 `Script.MaxTimeoutMS` 并加 5s slack | MCP 路径 300s 脚本不再双 deadline 竞态;timeout\_test 补 MCP 分支 |
| T1.6 screenshot 尊重 inline | `mcp.go:89-97` 仅在调用方未传时默认 inline=true | MCP 调 screenshot 显式 `inline:false` 不再返回 image content;`mcp_test.go` 补用例 |
| T1.7 wire 解包告警 | `actions.go` 6 处 `_ = json.Unmarshal` 改为:失败时 Receipt 标记 warning(或返回错误) | 单测构造畸形 wire 返回,断言 warning/错误 |
| T1.8 配置显错 | `config.go:140` 解析失败打日志+返回明确错误或使用完整默认;`UIConfig` 补 json tag | 坏 config.json 启动时有日志;`ui` 节生效;config 包补加载测试 |
| T1.9 错误码单一来源 | `errors.go` 建 JS 侧码清单(或生成表);`runtime_contract_test.go` 扩展为双端契约测试 | `errors_test.go` 断言 runtime.js 产出的每个码都在 Go 表中 |

**批次 1 附带测试补齐**(与修复同 PR 或紧随):audit.RedactArgs 表驱动测试(E1)、
config 加载测试(E2 部分)。

### 批次 2 — 性能与资源(估 2 天)

| 任务 | 改动点 | 验收标准 |
|---|---|---|
| T2.1 refs 清理 | `runtime.js:849-860`:每次快照时清扫「上次快照后未命中且 WeakRef 已死」的条目;或设上限(如 2000)LRU 淘汰 | 模拟 50 轮快照的 SPA 会话,refs 数量有界 |
| T2.2 text= 定位前置过滤 | `runtime.js:907-928`:先按 textContent 粗筛再做样式计算;加超时降级 | 3000+ 节点页面 text= 定位耗时可测地下降(基准脚本);语义不变(既有契约测试仍过) |
| T2.3 network wait 页内化 | `runtime.js` installNetwork 增加 pending promise 机制,移植 console wait(`runtime.js:537-568`)方案;删 Go 侧 100ms 轮询 | `network wait` 与 `console wait` 行为一致(wait 期间不轮询);人类点击触发的请求照样命中 |
| T2.4 插件健康检查短路 | `index.ts:149-158`:健康成功的 runtime 在 N 秒(建议 10s)内直接复用,失败才清缓存;顺带合并 `browser_tab new` 的 3 次串行 RPC | 单次工具调用(应用健康时)不再产生 /v1/health 请求;应用死后首次调用仍能正确恢复 |
| T2.5 jobs 定时 sweep | `jobs.go` 增加后台定时器(60s);逐出改为可多个 | 已结束任务 TTL 后即使无新 create 也被清理;jobs\_test 补用例 |
| T2.6 KeepSources 保留期 | `config.go` 加 `script.keep_sources_days`(默认 7);启动时清理过期目录 | 过期目录被清理;`keep_sources=false` 行为不变 |
| T2.7 截图管线合并 | `screenshot.go`:capture→decode→缩放→encode 中 decode/缩放仅在需要(标注/缩放/JPEG)时执行 | inline PNG 不缩放时零中间编解码;输出字节等价 |

### 批次 3 — 功能强化 I:模型能力对齐(估 3-5 天)

> 本批主题:**应用已实现,把能力透传给模型**。性价比最高的批次。

**T3.1 `browser_wait_event` 事件工具(最高价值)**

- 应用侧:无改动(`/v1/events` 已实现:`server.go:294-327`)。
- 插件侧:新增工具 `browser_wait_event {kinds?, after?, timeout?}`;`browser_call`
  白名单加 `events` 兜底。
- SKILL.md:更新 §「等待」节——等弹窗/接管/下载/导航完成优先用事件而非轮询 status。
- 验收:模型可 `browser_wait_event {kinds:"control.takeover,download.finished"}`
  挂起等待;timeout 返回空事件非错误;审计归因 `adag-<thread>` 正确。

**T3.2 透传工具 schema 化**

- 插件侧:cookies/files/frames/downloads/dialogs 从裸 JSON 透传升级为专用参数 schema
  + formatter(schema 以应用 `spec.go` 为唯一来源,插件照抄并加一致性注释);formatter
  按工具定制(替代 genericText 的 JSON dump)。
- 验收:`browser_call {tool:"cookies"}` 等在 MCP `tools/list` 中有完整参数 schema;
  返回文本人类可读。

**T3.3 高级定位/读取参数透传**

- 插件侧放行(应用已实现):read `format: markdown/links/tables`、snapshot
  `root/roles/include_frames`、type `slowly`、click `button/modifiers`。
- 验收:markdown 格式读表格页、`click {button:"right"}` 弹上下文菜单均可用;
  SKILL.md 同步更新参数表。

**T3.4 全页截图**(DESIGN M2 已规划,本批实现)

- 应用侧:`screenshot.go` 增加 `full_page` 选项——注入 JS 分段 scroll + CapturePage +
  按滚动偏移拼合;处理 fixed 元素(首段截图后隐藏)与懒加载(每段落定等待);复用
  现有 downscale/annotate/JPEG 管线。
- 插件侧:`browser_screenshot` 透传 `full_page`。
- 验收:fixture 站点 3 屏长页全页截图内容完整、fixed 头部只出现一次;smoke 脚本加用例。

### 批次 4 — 能力扩展 II:浏览器动作补全(估 1-2 周,按需排期)

| 任务 | 内容 | 关键改动点 |
|---|---|---|
| T4.1 拖拽 | `invoke` 加 `drag` op:HTML5 DnD(dragstart/dragover/drop)与 pointer 序列两套;复用 setInputLock bypass;插件 `browser_act` 加 `drag` action | `runtime.js` invoke 表(`runtime.js:1897-1914`);spec.go click/drag schema |
| T4.2 嵌套 iframe | relay 改递归:子 frame 收 cmd 后向自身子 frame 转发(深度上限 5、防环);`frames use f2/f3` 路径语义;帧树扁平展示 | `runtime.js:1777-1878`;spec.go frames |
| T4.3 下载增强 | OnWillDownload 补 progress 事件(kind: `download.progress`)、`downloads cancel`;`wait for=download` 带进度 | `tab.go:295-307`;events.go kinds |
| T4.4 人类活跃检测 | preload 监听 pointerdown/keydown(遮罩解除期间),emit `control.human_active`;agent 写操作自动暂停 | `runtime.js`;manager.go Pause 语义 |
| T4.5 storage 工具 | `browser_call` 加 `storage`:localStorage/sessionStorage list/get/set/clear + 统计;走 URL 策略与审计 | runtime.js invoke 表;spec.go;tools.go |
| T4.6 请求 mock(评估) | 拦截/改写页面 fetch(需动 mygo 层与 runtime)——**先评估可行性再排期**,不强入本批 | DESIGN M3 |

### 批次 5 — 结构重构与文档同步(估 3-4 天,可与批次 3-4 交错)

| 任务 | 内容 | 验收标准 |
|---|---|---|
| T5.1 schema 驱动校验 | 服务端用 spec.go schema 校验 args(enum/required/additionalProperties),替代/兜底各工具匿名 struct | 未知字段报 `bad_request` 而非静默忽略;spec\_test 扩展「schema ↔ struct 一致性」 |
| T5.2 插件样板泛化 | 14 个 registerTool 收敛为「声明式 spec + 通用 rpc + formatter 注册表」;启动拒绝逻辑两份合并(`index.ts:367-382` vs `:1232-1247`) | dist 行为逐字回归(用 SKILL.md 用例对照);`node plugins/official/build.mjs` 重建 |
| T5.3 服务端去重 | 审计条目构造合并(`server.go:338-363` ← `tools.go:652-664`);tabID 回退三份收敛;删死字段 `audit.Entry.Script` | 编译+既有测试全过;审计 JSONL 字段不变(兼容) |
| T5.4 并发测试补齐 | 租约 Acquire/Release/expireLoop、动作槽、takeover 竞态的表驱动测试 | `manager_test.go` + `tab_test.go` 落地 |
| T5.5 文档口径同步 | DESIGN.md 脚本超时 120s/300s→60s/110s(工具层)、可操作等待 30s→5s、截图目录;SKILL.md 对应节 | README/DESIGN/SKILL 三份口径一致;无「5 秒 vs 30 秒」类矛盾 |

### 可选批次 6 — 安全加固(视需要单独立项)

- C1 per-client token 或至少 token 吊销通道;C2 MCP HTTP 默认要求 token(loopback
  裸奔时打醒目日志);C3 Bind 非 loopback 时要求显式确认标志;C4 /v1/health 最小化
  信息(去 pid/feature);C5 审计丢弃计数 + 定期 flush 落日志。
- T2.8(可选):args 单次反序列化后传递(与 T5.3 合并做)。

### 宿主侧(单独立项,不阻塞本计划)

15s `ResponseHeaderTimeout`(`internal/plugins/host/httpbridge.go:107`)使插件带约
260 行规避层(`index.ts:408-665`)。若宿主为插件 fetch 提供按调用 opt-in 的更长响应头
超时(纯超时通道,与 host 黑白名单/审批无关),E8 的脆弱重放层可退役。不动宿主则维持
现状——规避层本身可用。

---

## 5. 里程碑总览

| 里程碑 | 内容 | 估时 | 退出标准 |
|---|---|---|---|
| M-R1 | 批次 1 全部 + E1/E2 测试 | ~2 天 | P0 清零;`go test ./...`(GOWORK=off)过 |
| M-R2 | 批次 2 全部 | ~2 天 | 长会话内存有界;network wait 无轮询;插件无冗余健康检查 |
| M-R3 | 批次 3 全部 | ~5 天 | browser_wait_event/透传/全页截图可用;SKILL.md 同步 |
| M-R4 | 批次 4 按需 | 1-2 周 | 拖拽/嵌套 iframe/下载增强落地(mock 视评估) |
| M-R5 | 批次 5 全部 | ~4 天 | schema 校验、样板泛化、文档口径一致 |

依赖关系:M-R1 → M-R2(续租机制是 T2.3 页内化的前提语义);M-R3 独立于 M-R2 可并行;
M-R4 依赖 M-R3 的事件通道(T4.3 下载进度靠 events);M-R5 的 T5.1 依赖批次 1 的
T1.9(错误码/码表先稳)。

---

## 6. 验证门禁与工作流

每批次退出前,按仓库既有门禁执行:

```powershell
# 应用(本模块不在 go.work 中)
cd mcp_projects\browser_use
$env:GOWORK = "off"
go test ./...
go vet ./...
staticcheck ./...

# 装配门禁(官方插件加载)
cd <repo root>
go test ./app ./internal/cli

# 插件改了 index.ts 后重建 dist
node plugins/official/build.mjs

# 真实 WebView 冒烟(导航/快照/wait/diff/截图)
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/smoke-browser-use.ps1
```

约定:

- 行为契约优先钉在测试里(沿用 `runtime_contract_test.go` 的源码契约模式与
  `timeout_test.go` 的预算钉法);
- 插件侧改动必须同步 `SKILL.md`,应用侧行为改动必须同步 `README.md`「What works now」
  与 `DESIGN.md` 对应节(避免再次出现 E7 漂移);
- 批次 3 起每个新工具/参数,spec.go 是唯一 schema 源头,MCP/HTTP/CLI 不允许各自派生。

---

## 7. 风险与依赖

| 风险 | 影响 | 缓解 |
|---|---|---|
| T1.2 续租改变租约语义 | 依赖 120s 空闲释放的既有脚本行为 | 心跳仅动作执行期内生效;空闲释放语义不变;测试钉住 |
| T2.3 network wait 页内化 | SPA 场景请求时序边界 | 保留「最近 5s 宽限窗」语义(`actions.go:1063`);fixture 站点回归 |
| T3.4 全页截图 | WebView2 分段截屏的滚动误差/懒加载竞态 | 落定等待 + 首尾段去重;失败时明确报错回退视口截图 |
| T4.2 嵌套 iframe 跨域 relay | 跨域 postMessage 安全面扩大 | 帧白名单仅限同源/已知 origin 或显式 `include_frames`;深度上限 |
| 插件 dist 与 index.ts 双源 | 忘记重建导致宿主跑旧逻辑 | T5.2 重构时在 build.mjs 输出指纹校验(仓库已有 dist 指纹惯例,见 commit fe21ef4) |
| mygo 上游能力限制(T4.1/T4.6) | 拖拽/mock 需要 WebView 层支持 | T4.6 先评估;T4.1 走合成事件不依赖上游 |

---

## 8. 明确不做

与仓库 `AGENTS.md`(2026-10-31 决策)对齐,本计划**不包含**:

- 宿主出网 host 黑白名单(已移除,不再加回;后续黑白名单走插件形式,不在本计划范围);
- 宿主审批策略(policy-default)演进——browser-use 应用自身的遮罩/租约是协作层不是
  审批层,维持现状;DESIGN M2 的「敏感动作审批」如启动属应用内部功能,单独立项,
  不依赖宿主策略演进;
- 给 agent 的读路径加限制(`ctx.fs` 读不设防是有意决策);
- 在宿主桥里写死任何浏览器相关策略。
