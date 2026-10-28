# Node 兼容子集偏差表（moejs v0.1.0-alpha.11 · M1-D 尖刺）

> 上级：[设计 12 §6.11/§6.12/§6.13/§6.14](../../../docs/12-node-compat.md) · 决策：[D-7](../../../docs/decisions.md) · 语料：[corpus/](corpus/README.md)
>
> **证据来源**：本表每一条的「实测」列都由 `go run ./tools/moejspike`（探针在 `tools/moejspike/`）
> 与 `go test ./internal/conformance/nodecompat -run TestNodeCompat`（语料在 `corpus/`）生成，
> **重跑即可复现**；引用的 case 名与语料 id 就是复现入口。基线快照：`corpus/**/*.expect.json`
> （由 `ADAG_NODECOMPAT_RECORD=1` 从真实运行生成，人工复核后提交）。
> Node 侧对照：`node internal/conformance/nodecompat/corpus/run-node20.mjs`（本机跑的是 Node 25.4.0，
> 正式对跑基线仍是 20 LTS；两者的差异在 `nodeParity` 里按实测标注）。
>
> 口径：**实测与 D-7 判定不一致时以实测为准**，本文件末尾「与 D-7 的差异」一节逐条列出。

## 1. 逐条偏差

列说明：
- **期望** = 设计 12 的小节口径；
- **实测** = alpha.11 的真实行为（case=探针用例 / 语料 id / 实际输出片段）；
- **归属** = 上游要补（含 issue 草案）· ada-go 宿主实现 · 标记不可用；
- **E-x** = 6.11.1 的缺口编号。

### 1.1 E-1 模块白名单 / 未知模块

| # | 模块或成员 | 期望（12 文档） | alpha.11 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-01 | 未知 `node:*`（静态 import） | §6.12.1(1)、§6.11 模块策略：链接期失败，错误码 `ERR_MODULE_NOT_FOUND` | `case=modules` / 语料 `mod-03-dynamic-unknown`：链接期确实失败，但错误是 Go 侧 `*moejs.ResolveError: cannot resolve module "node:crypto": No such built-in module: node:crypto`，**不是 JS 层的 `ERR_MODULE_NOT_FOUND`** | 宿主：把 resolver 错误映射成装配失败码（`internal/diag` + 插件安装期） | E-1 |
| D-02 | 未知 specifier（动态 import） | §6.11：必须 reject，不得返回 undefined 命名空间 | `mod-03-dynamic-unknown`：`{state:"absent", name:"Error", code:null}`，rejection 是普通 `Error`（`codes.* = null`），Node 20 给 `ERR_MODULE_NOT_FOUND` | 宿主：注入 JS 层错误码（或接受偏差并写进 6.12.4） | E-1 |
| D-03 | `fs` / `fs/promises` / `buffer` 裸名 | §6.12.3：`fs` 与 `node:fs` 同实例 | `case=modules` 的 `E-1/fs 与 node:fs 同一实例` → `{"defaultSame":true,"namedSame":true,"promisesSame":true}`；语料 `mod-01-fs-identity` 同结论 | 上游已具备 | E-1 |
| D-04 | 其余裸内置名（`events`、`timers`、`path`…） | §6.12.3「推荐写 node: 前缀」+ §6.11 模块策略 | `case=modules` 的 `E-1/裸名-events` → **LINK-FAIL**（上游只服务 fs/fs/promises/buffer 三个裸名）；语料 `mod-03-dynamic-unknown`（`events` absent）、`tim-05-timers-module`（`bareTimers` absent） | 宿主：`SetModuleLoader` 里把裸内置名归一到 `node:*` 再按能力判定（**D-7 说「裸名 events 也能解析」与实测相反，见 §3**） | E-1 |
| D-05 | 安装粒度（装了模块表但本 Runtime 没 Install） | §6.10「未安装的东西不存在」+ §6.12.1(1) 链接期失败 | `case=modules` 的 `E-1/安装粒度-未 Install` → Link 通过、`loadErr=*engine.Exception: Cannot import "node:fs": the host did not install the node package in this runtime`；`无 FS` → `this runtime has no file system`。**失败在求值期，不是链接期，也不是 `ERR_ACCESS_DENIED`** | 宿主：正确做法是不把该模块放进模块表（我们的 resolver 说了算）；文档措辞要更正 | E-1 |
| D-06 | 细粒度白名单开关 | §6.11.1 E-1：要细于 `NoWebAPIs` 的整组开关 | `case=modules` 的 `E-1/白名单就是宿主 resolver`：不套 `node.Resolver` 时 `node:fs` 直接 LINK-FAIL —— **模块表完全由宿主 Resolver 决定，上游无需新增开关** | 宿主（已定：落在 ada-go resolver） | E-1 |
| D-07 | 未 Link 直接 Load | §6.10：不允许静默降级 | `case=modules` 的 `E-1/未 Link 不能 Load` → `moejs: module … imports "node:fs": link it with moejs.Link and a resolver` | 上游已具备 | E-1 |
| D-08 | `import.meta` | §6.11 模块系统、§6.12.3：url/filename/dirname/resolve 指向 `plugin://` 虚拟路径 | 语料 `mod-04-import-meta` → `{"own":[],"protoNull":true,"url":"undefined","filename":"undefined","dirname":"undefined","resolve":"undefined"}`（Node 20：全是 string/function） | 宿主：`Importer.Meta` 钩子填（`case=modules` 的 `import.meta/宿主可填` 实测可填 `plugin://spike/index.js` + resolve） | E-1 |
| D-09 | 循环依赖 / 模块缓存同一性 | §6.11：不挂起；每 Runtime 一份缓存 | 语料 `mod-02-cycle`（`{"fromA":"A","valueB":"B-of-A"}`）、`mod-01-fs-identity`（静态=动态=同一实例） | 上游已具备 | — |

### 1.2 E-2 定时器

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-10 | `setTimeout().unref/ref/hasRef` | §6.11、§6.11.1 E-2：unref 后不阻止 Runtime 结束 | 语料 `tim-02-unref` → `{"hasUnref":"function","hasRef":"function","hasRefresh":"function","hasRefAfterUnref":false,"idIsNumber":true,"idType":"object","effects.log":[]}`（回调没跑，Run 直接结束）；`case=timers` 的 `E-2/unref` 同结论 | 上游已具备 | E-2 |
| D-11 | 每 Runtime 定时器数量上限 | §6.12.2 上限行、§6.12.4 `ERR_TIMER_LIMIT`：256 个，超出抛错 | `case=timers` 的 `E-2/定时器数量上限` → `{"fired":600,"err":"none"}`；语料 `tim-04-limit`（decision=skip-upstream）。**上游完全没有数量上限** | 宿主：计数夹 256 并抛 `ERR_TIMER_LIMIT`（M1-C）；同时提上游（per-runtime 上限） | E-2 |
| D-12 | 毫秒截断与 32 位上限 | §6.11：NaN/小数/超大值按规定处理 | 语料 `tim-03-clear-clamp` → `order=["nan","huge","negative","fraction"]`（全部夹到 1ms 且按序触发），与 Node 20 顺序一致；Node 额外打 `TimeoutNaNWarning`/`TimeoutOverflowWarning`/`TimeoutNegativeWarning`，上游静默 | 上游已具备（告警差异记档即可） | E-2 |
| D-13 | `setImmediate` 顺序 | §6.12.2：以 0ms 定时器实现，顺序与 Node 的 check 阶段不完全相同 | 语料 `tim-01-order` → `["microtask","promise","immediate","timeout0"]`；本机 Node 25.4.0 同一份语料跑出**相同顺序**（`run-node20.mjs` 标 SAME）。上游文档说的是三阶段循环 timers→tasks→immediates，与 Node 的先后关系不保证 | 上游已具备；顺序保证程度写进 6.12.2 的偏差列（文档里「以 0ms 定时器实现」这句与上游实现描述不符） | E-2 |
| D-14 | 清除与非法 id | §6.12.2：返回数字 id | 语料 `tim-03-clear-clamp` → `effects.cleared=[]`（`clearTimeout` 接受对象/数字/字符串，`clearImmediate(7)`、`clearInterval(undefined)` 都忽略）；但 **id 是 Timeout 对象**（`idType:"object"`，`intervalIdType:"object"`），`JSON.stringify` 得到 `{}`，与 Node 的 Timeout 对象字段不同 | 宿主不必做；文档「返回数字 id」要更正 | E-2 |
| D-15 | 关闭时全部取消定时器 | §6.11：Runtime 关闭时全部取消 | `TestNodeCompatTimersSurviveRunEnd` + `case=timers` 的 `6.11/关闭时全部取消` → `Run`/`Stop` 结束后未到期的 **ref'd 定时器仍在队列里，下一次 Run 会补跑**（实测 `hit="fired"`）；`Terminate()` 之后才真的不再跑 | 宿主：关闭走 `Terminate` 或换新 Runtime；上游 issue：Run 结束语义 | E-2 |

### 1.3 E-3 模块 CJS 表面

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-16 | `require`/`module`/`exports`/`__dirname` | §6.11：ESM 与 CJS 双向互操作；§6.12.2 `node:module` 的 `createRequire` | 语料 `mod-05-cjs-surface` → 全部 `undefined`，`require` 抛 `ReferenceError: require is not defined`（Node 的 .mjs 也是 undefined，但 Node 能直接跑 CJS 依赖）；`case=modules` 的 `E-3/CJS-require` 同结论 | 构建期 esbuild 包裹 + 宿主注入受限 `require`；上游 issue：`createRequire` | E-3 |
| D-17 | `node:module`（`createRequire`/`builtinModules`/`isBuiltin`） | §6.12.2：始终安装（受限） | `case=matrix` 与 `case=modules` 的 `E-3/node:module` → **LINK-FAIL**（上游根本没有该模块）；矩阵把它列 `TierAlways` 且 `Gap=E-3` | 宿主自建虚拟模块（只解析已安装 `node:*`）；标记不可用直到 M1-C | E-3 |
| D-18 | esbuild 的 CJS 垫片 | §6.12.3：`require` 只解析已安装 `node:*` 或包内联 | `case=modules` 的 `E-3/esbuild-CJS 垫片` → 产物含 esbuild 的 `__require` 运行期抛错垫片（`errors=0 warnings=0`） | 宿主：注入 `require` 垫片即可，不必上游实现完整 createRequire | E-3 |

### 1.4 E-4 fs 错误码与只读裁剪

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-19 | 错误的 `code`/`errno`/`syscall`/`path` | §6.11.1 E-4、§6.12.4 | 语料 `fs-01-enoent` → `code:"ENOENT"`、`errno:-4058`（负 libuv 值）、`syscall:"open"`、`path` 等于脚本入参、`ownNames=[code,errno,message,path,syscall]`；`fs-02-kind-mismatch`、`fs-08-symlink`、`err-01-shape-stack` 同形状 | **上游已具备，不需要改**；12 文档 §6.11.1 把 E-4 写成「待补」要更正 | E-4 |
| D-20 | `EISDIR`/`ENOTDIR` | §6.12.4 | `fs-02-kind-mismatch` → `readFileSync(dir)` 给 `EISDIR`（errno -4068）；`readFileSync(file/x)` 给 **`ENOENT`** 而 Node 20 给 `ENOTDIR`；`readdirSync(file)` 给 `ENOENT` 而 Node 给 `ENOTDIR` | 上游 issue（ENOTDIR 归类）；或宿主按路径段预检后归一化 | E-4 |
| D-21 | 只读时写成员 | §6.12.1(2)、§6.13：静态裁剪，**访问即抛** `ERR_ACCESS_DENIED` | 语料 `fs-06-readonly-write` → `typeof fs.writeFile === "function"`、descriptor `{writable:true,configurable:true,enumerable:true}`，调用才抛 `EROFS`（errno -4034），**不是 `ERR_ACCESS_DENIED`** | 见 D-22/D-23 | E-4 |
| D-22 | 宿主裁剪：访问即抛（Proxy `get` 陷阱） | 同 §6.12.1(2) 字面要求 | `case=fs` 的 `E-4/宿主裁剪-访问即抛` → `loadErr=*engine.Exception: Error: ERR_ACCESS_DENIED: …（成员 appendFile 被宿主裁剪）`：**node:fs 模块体一次性读全部成员（`export const X = binding.X`），任一成员访问抛错就让整个模块求值失败**，只用 `readFile` 的插件也装不上 | **上游 issue**：binding 成员惰性化（getter/Proxy 惰性），否则「访问即抛」不可实现 | E-4 |
| D-23 | 宿主裁剪：退而求其次 | — | `case=fs` 的 `E-4/宿主裁剪-调用即抛` → 宿主在 `node.InstallLoop` 之后链上 `import.meta` 钩子改写 binding 成员，实测 `{"callDefault":{"code":"ERR_ACCESS_DENIED"},"callNamed":{…},"promisesCall":{code:"ERR_ACCESS_DENIED"},"readStillWorks":"hello moejs"}`；命名导出一起被换掉，模块仍可用 | 宿主：实现「**调用即抛**」；12 文档 §6.12.1(2) 的「访问即抛」按 D-22 改成「调用即抛 + 原因」 | E-4 |
| D-24 | symlink / `..` 越界 | §6.13：按真实落点拒绝 | 语料 `fs-08-symlink` → 越界链接 `readFileSync`/`realpathSync`/`statSync` 全部 `EACCES`（syscall 分别是 open/realpath/stat），`lstatSync().isSymbolicLink()` 为真，根内链接可读；`fs-03-escape` → `/workspace/../outside/secret.txt` 与 `/etc/passwd` 都是 `EACCES` | 上游已具备（Linux 还走 `openat2 RESOLVE_BENEATH`） | E-4 |
| D-25 | 宿主绝对路径 | §6.12.1、§6.13：不进脚本 | `case=fs` 的 `6.13/宿主绝对路径不进脚本` → `realpathSync("/workspace/hello.txt") = "/workspace/hello.txt"`；读宿主真实路径给 `EACCES`。注意 **`err.path` 会原样回显脚本传进去的宿主路径**（`fs-03-escape` 的 `returns.dotdotPath=true`）——脚本本来就有这个字符串，不构成泄露，但审计日志要按脱敏处理 | 上游已具备；宿主审计层负责脱敏 | E-4 |
| D-26 | `Allow` 钩子 | §6.12.1、§6.13：边界由 Root/ReadOnly/Allow 决定 | `case=fs` 的 `E-1/Allow 钩子` → `Allow(op, resolvedPath)` 看的是解析完链接之后的路径；`fs.ErrNotExist→ENOENT`、`syscall.Errno→同名码`、其它→`EACCES` | 上游已具备（宿主的密钥目录策略就挂这里） | E-4 |
| D-27 | `fs` 流与未实现成员 | §6.12.2：`createReadStream`/`createWriteStream`（Web Streams）；不开放 watch/fd/chmod | 语料 `fs-07-members-constants` → `typeof` 全是 `function`，调用给 `ERR_METHOD_NOT_IMPLEMENTED`（`codes.createReadStreamCall`/`openSyncCall`）；`v1ExcludedButPresent=[watch,open,close,read,write,chmod,chown,utimes]` | 上游 issue 或宿主裁剪：**「Web Stream 形式的读写流」这条不成立**，占位函数比「不存在」更危险 | E-4 |
| D-28 | 回调形式 | §6.12.2：不提供回调形式（只同步 + promise） | 语料 `fs-05-promises-callback` → `cbData:"hello moejs"`（回调形式真实可用），`promisesOnFs=true` | 宿主：按能力裁掉回调形式，或把 6.12.2 的这句改成事实 | E-4 |
| D-29 | `fs.constants` | §6.12.2：与 Node 一致 | `fs-07-members-constants` → `{F_OK:0,O_RDONLY:0,O_WRONLY:1,O_APPEND:1024,COPYFILE_EXCL:1}`；上游恒为 **Linux 值**，本机 Node 给 `O_WRONLY=8`（Windows libuv 值） | 上游已具备（固定视图反而是好事）；文档写明「常量恒为 Linux 值」 | E-4 |
| D-30 | `fs` 成员清单 / `Stats` | §6.12.2 兼容范围列了 13 个成员 | `fs-07-members-constants` → `memberCount=99`、`promisesMemberCount=32`、`extraMembers=[glob,globSync,cp,cpSync,access,exists,opendir]`；`Stats` 字段齐全（`typeof size/mtimeMs/ino` 都对）。矩阵的「兼容范围」列比真实能力窄 | 文档/矩阵按上游清单重写（我的活） | E-4 |
| D-31 | `API.Close()` 之后 | §6.13：关闭不留工作，调用要显式失败 | `case=fs` 的 `6.13/API.Close 之后` → `{"first":"hello moejs","after":"throw:EBADF","pendingWork":{"err":"EBADF"}}` | 上游已具备 | E-4 |

### 1.5 E-5 child_process

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-32 | `node:child_process` | §6.12.2、§6.13：只装 `execFile`/`execFileSync` | 语料 `cp-01-module` → `{"child_process":{"state":"absent","name":"Error","message":"No such built-in module: node:child_process"},"members":null}`；`case=exec` 的 `E-5/node:child_process` LINK-FAIL。**上游根本没有该模块，spawn/fork/exec 是「不存在」而不是「可绕过」** | ada-go 自建（不 fork、不推上游）；矩阵标「ada-go 实现」 | E-5 |
| D-33 | 第二条命令面 | §6.10、§6.13：不能有第二条路径 | `case=exec` 的 `6.13/命令面唯一性` → `{"Worker":"undefined","child_process":"undefined","process":"undefined","workerThreads":"No such built-in module: node:worker_threads","net":"No such built-in module: node:net"}`；webapi 只有 fetch | 上游已具备 | E-5 |
| D-34 | 宿主自建 execFile 的可行性 | §6.13：无 shell、allowlist、cwd 映射、maxBuffer、timeout、取消杀进程树 | `case=exec` 的 `E-5/宿主窄适配-execFile`（本轮修掉探针自身的两个 bug 后重测）→ `{"members":["execFile","execFileSync"],"noSpawn":"undefined","noExec":"undefined","sync":{"ok":{…}},"syncFail":{"code":7},"shellTrue":{"code":"ERR_ACCESS_DENIED","message":"ada-go 不开放 shell（Run.Shell=false）"},"badProgram":{"code":"ERR_ACCESS_DENIED"},"bigBuffer":{"code":"ERR_CHILD_PROCESS_STDIO_MAXBUFFER"},"cwdEscape":{"code":"ERR_ACCESS_DENIED"},"timeout":{"code":"ETIMEDOUT","killed":true},"cancel":{"name":"AbortError","code":"ABORT_ERR"},"afterKill":0}` | ada-go 宿主实现（M1-C）；语料 `cp-02-execfile` 现在 skip-host，落地后翻成 assert | E-5 |
| D-35 | 取消桥的实现坑（同 E-5，但值得记） | — | 修复前实测：JS 数组参数用 `rt.ToGo` 单类型断言 `[]any` 会静默丢参（cmd.exe 收到 0 个参数）；`signal` 在 `cmd.Start()` 之前 abort 会整个丢掉取消（实测 `timeout:{unexpected:0}`、`cancel:{unexpected:0}`） | 宿主桥：参数用定向解码；context/cancel 必须在把 Promise 交给脚本之前登记 | E-5 |

### 1.6 E-6 单次读写与目录条目上限

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-36 | 单次读 16MB 上限 | §6.13、§6.12.4 `ERR_FS_FILE_TOO_LARGE` | `case=fs` 的 `E-6/单次 16MB 上限` → `{"size":20971520,"read":20971520,"ctor":"Buffer"}`：20MB 一次读进 64MB 堆的 Runtime **完全成功** | 宿主包装层夹 16MB；上游 issue：可注入的单次字节上限 | E-6 |
| D-37 | 拒绝阈值与堆耦合 | §6.13：16MB 是独立边界 | `case=fs` 的 `E-6/拒绝阈值来自 MemoryLimit` → `{"tooLarge":"RangeError:ERR_FS_FILE_TOO_LARGE:File size (20971520) is greater than the memory limit (8388608)"}`：阈值就是 `MemoryLimit`（或 Node 的 2GiB） | 宿主：不要把 `MemoryLimit` 当 16MB 用；上游 issue 同上 | E-6 |
| D-38 | 目录 10k 条目上限 | §6.13：10k 条，超限报错 | `case=fs` 的 `E-6/目录 10k 条目` → `{"count":10500,…}`，宿主侧真实条目数=10500：**没有上限，全量返回** | 宿主：`readdir` 包装夹 10k；上游 issue：可注入条目上限 | E-6 |
| D-39 | 语料侧 | — | `fs-09-limits` decision=skip-upstream（reason 指 E-6） | — | E-6 |

### 1.7 E-7 内存/中断/异步桥

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-40 | 字符串计入内存上限 | §6.11 资源边界 | `case=resources` 的 `6.11/内存上限-字符串` + 本轮 `TestNodeCompatMemoryLimit/字符串` → `*engine.InterruptedError: interrupted: memory limit exceeded … limit 8388608`，脚本 **catch 不到**（没有 `__report`） | 上游已具备 | E-7 |
| D-41 | Buffer 计入内存上限 | §6.11.1 E-7 | `TestNodeCompatMemoryLimit/Buffer` → 同样以中断终止（`Buffer.alloc(32MB)` 在 8MB 堆里不成立）；node 包的 Buffer 就是 Uint8Array 子类，走引擎堆 | 上游已具备；Go 侧副本仍要宿主自查 | E-7 |
| D-42 | **TypedArray** 计入内存上限 | §6.11：计入 | `TestNodeCompatMemoryLimit/TypedArray` 与语料 `bin-05-huge-alloc` → `{"caught":"catch:RangeError","held":0}`：**计入了，但走的是可 catch 的 RangeError，不是不可 catch 的中断**（与字符串/Buffer 不同形） | 上游 issue（统一成中断语义）；宿主按「分配一律不成功」这一共同点即可，不必等上游 | E-7 |
| D-43 | 取消不可 catch | §6.11、§6.13 | `TestNodeCompatInterruptNotCatchable` + `case=resources` 的 `6.11/取消不可 catch` → `runErr=*engine.InterruptedError: interrupted: nodecompat deadline`，且 JS 没有上报 | 上游已具备 | E-7 |
| D-44 | 栈与调用深度可调 | §6.11：`WithStackSize`/`WithMaxCallDepth` | `TestNodeCompatCallDepthLimit` → `RangeError: Maximum call stack size exceeded`（depth≈508）；**上游没有这两个可调参数**，只有引擎默认值 | 文档更正（6.11 的接口清单里这两个名字不存在）；上游 issue（可选） | E-7 |
| D-45 | 正则回溯可中断 | §6.11 资源边界 | `case=resources` 的 `6.11/正则回溯可中断`（修掉 `performance` 引用错误后重测）→ `{"res":"null","ms":0}`：引擎正则不是回溯式（RE2 风格），`(a+)+$` 对 28 个 a 没有爆炸 | 该风险在 moejs 上不存在；文档保留说明，语料留一条防回归 | E-7 |
| D-46 | 宿主回调 panic | §6.11 宿主异步桥：变成 rejected Promise | `TestNodeCompatHostPanic` + `case=resources` 的 `6.11/宿主 panic 不上抛` → `*moejs.InternalError: moejs: internal error: …`：设了 `OnError` 就交给宿主、循环继续；没设就停循环。**不会崩进程，但也不会自动变成 rejected Promise** | 宿主：在 `NativeFunc` 里 recover 后返回错误 | E-7 |
| D-47 | `AsyncWork` 关闭竞态 | §6.11：未完成工作收到 `ErrClosed`，结果恰好投递一次 | `TestNodeCompatAsyncWorkCloseRace` + `case=resources` 的 `6.11/AsyncWork 关闭竞态` → Run 结束后 settle 被丢弃（`Post` 返回 false），脚本侧 `settled` 永远停在 `"pending"`，**没有 `ErrClosed` 的 rejection** | 宿主：跟踪未完成工作并在关闭时显式拒绝；上游 issue：关闭时投递 `ErrClosed` | E-7 |
| D-48 | 未处理 rejection | §6.11：交给宿主钩子而不是结束进程 | 语料 `lang-03-unhandled-rejection` → `{"alive":true}` + `observe.rejections=1`（`Runtime.SetPromiseRejectionTracker`）；Node 20 会打告警并可能按 `--unhandled-rejections` 终止 | 上游已具备 | E-7 |
| D-49 | 跨 Runtime 克隆 | §6.11：`Serialize`/`Deserialize` | `case=resources` 的 `6.11/跨 Runtime 克隆` → 上游没有这两个接口，跨 Runtime 只能走 `ToGo`/`FromGo` 或 JSON | 宿主桥按「只传 Go 值/JSON」实现；文档更正接口名 | E-7 |

### 1.8 E-8 timers/promises

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-50 | `node:timers` | §6.12.2：`setTimeout`/`setImmediate`/`setInterval` | 语料 `tim-05-timers-module` → `{"state":"absent","message":"No such built-in module: node:timers"}`：定时函数只是 eventloop 装的全局，没有模块 | 宿主可提供薄 shim；v1 矩阵保持「等 M1-C」 | E-8 |
| D-51 | `node:timers/promises` + `signal` | §6.12.2：promise 形式支持 `signal` | `tim-05-timers-module` → absent；`case=timers` 的 `E-8/node:timers 与 timers/promises` LINK-FAIL | 标记不可用（v1 不装，矩阵已如此） | E-8 |
| D-52 | `setTimeout(fn, ms, {signal})` | §6.11 取消：双向可取消 | `case=timers` 的 `E-8/signal 取消静默失效` → `{"fired":"YES-仍然触发"}`：**AbortSignal 被当成普通附加参数忽略，取消静默失效**（语料 `tim-06-signal-cancel` decision=skip-upstream）。比报错更糟 | 上游 issue（`timers/promises` 的 signal 取消）；宿主：第三方插件里检出这种写法要报错而不是放行 | E-8 |

### 1.9 二进制、编码与语言面（6.11 其余行）

| # | 模块或成员 | 期望 | 实测 | 归属 | E-x |
|---|---|---|---|---|---|
| D-53 | `Buffer` 全局与 `node:buffer` | §6.12.2：`allocUnsafe` 仍清零 | 语料 `bin-01-buffer` → `{"isUint8":true,"allocUnsafeZeroed":true,"hex":"abc","latin1":"é","kMaxLength":2147483647,"byteLengthCJK":6,"globalSame":true,"noBigInt64Read":"undefined"}`（Node 20 的 `kMaxLength` 是 2^53-1，且 `readBigInt64LE` 存在） | 上游已具备；`readBigInt64LE` 等大整数读写缺失要提上游 | — |
| D-54 | `TextDecoder` 的 `fatal`/`ignoreBOM`/分片 | §6.11 二进制与编码 | 语料 `bin-02-text-decoder` → `fatal:"throw:TypeError"`、`ignoreBOM:"﻿A"`、`stripBom:"A"`、分片解码走通；与 Node 20 一致（`run-node20.mjs` 标 SAME） | 上游已具备 | — |
| D-55 | `atob`/`btoa` | §6.11 | 语料 `bin-03-atob-clone` → `btoa("中")` 给 `InvalidCharacterError`（WHATWG 严格），`structuredClone` 对 Proxy 给 `TypeError`（Node 给 `DataCloneError`） | 上游已具备；错误类型差异写进偏差列 | — |
| D-56 | `SharedArrayBuffer`/`Atomics` | §6.11：**插件 Runtime 不提供** | 语料 `bin-04-shared-memory` → `{"sharedArrayBuffer":"function","atomics":"object","worker":"undefined"}`；裸 Runtime 里也在（`case=matrix` 的 `6.11/SharedArrayBuffer 与 Atomics`）。**上游给了且没有禁用位** | 宿主：`MutableIntrinsics` 下删除；上游 issue：补禁用位（这是安全边界，优先上游） | — |
| D-57 | Web 数据 API（`Blob`/`File`/`FormData`） | §6.12.2：始终（Web 数据 API 行） | 语料 `proc-03-globals`（装齐 node+webapi）→ `Blob/File/FormData` 全 `undefined`（上游文档明确不提供）；`URL/URLSearchParams/fetch/Headers/Request/Response/Abort*/EventTarget/DOMException/performance/Readable-Writable-TransformStream` 都在 | 宿主：需要时自己装；矩阵的这行要按实测收紧 | — |
| D-58 | 裸 Runtime（L1 引擎）的全局面 | §6.10：L1 无 IO | 语料 `proc-03-globals`（bare）→ `Buffer/process/console/URL/fetch/setTimeout` 全 `undefined`，但 `TextEncoder/TextDecoder/structuredClone/queueMicrotask/atob/btoa/SharedArrayBuffer/Atomics` 由引擎自带 | 上游已具备；文档写明引擎自带哪些 | — |
| D-59 | 语言与内建覆盖 | §6.11 语言与内建行 | 语料 `lang-02-language` → 私有字段、class static 块、解构、可选链/空值合并、`Proxy`/`Reflect`、生成器与异步生成器、`Map/Set/WeakMap/WeakSet`、`BigInt(10n**20n*2n)`、`Symbol.asyncIterator`、模板 tag 全对；`lang-01-job-order` 的 job 顺序与 Node 20 一致（SAME） | 上游已具备 | — |
| D-60 | `Error.cause`/`AggregateError`/`DOMException` | §6.11 错误行 | 语料 `err-02-arg-type` → `cause` 可用、`AggregateError` 的 `errors.length=2`、`DOMException` 是 function | 上游已具备 | — |
| D-61 | 参数类型/范围错误码 | §6.12.4 | 语料 `err-02-arg-type` → 非字符串路径 `[TypeError, ERR_INVALID_ARG_TYPE]`、坏编码 `[TypeError, ERR_INVALID_ARG_VALUE]`、`Buffer.alloc(-1)` `[RangeError, ERR_OUT_OF_RANGE]`。**`ERR_INVALID_ARG_VALUE` 不在 6.12.4 的表里**（矩阵注册表也没有），文档要补 | 文档补码（我的活） | — |
| D-62 | 取消错误的形状 | §6.12.4 `ABORT_ERR` + `name=AbortError` | 语料 `err-03-abort-shape` → `name:"AbortError"`、`instanceof DOMException`、`code` 是 DOMException 的数字码而不是字符串 `ABORT_ERR`；`AbortSignal.timeout` 的 reason 是 `TimeoutError` | 宿主：边界归一化时补 `code:"ABORT_ERR"`；文档记录 DOMException 事实 | — |
| D-63 | 栈映射 | §6.11 错误行、§6.12.4 末段 | 语料 `err-01-shape-stack` → `stackMentionsVirtual=true`（帧是 `plugin://…`）、`stackMentionsHost=false`；**没有 source map 映射**（Node 给真实 file:line） | 宿主：esbuild source map 映射在 M1-C/M3 做 | — |
| D-64 | 代码生成关闭 | §6.10、§6.11 | 语料 `lang-04-codegen` → `eval` 与 `new Function` 都抛 `EvalError: code generation from strings is disabled for this realm (DisableDynamicCode)`，`WebAssembly` undefined；默认（不设该位）`eval("1+1")=2`（`case=resources`） | 上游已具备（选项名是 `DisableDynamicCode`，不是文档里的 `WithoutCodeGeneration`） | — |

### 1.10 安装矩阵被探针证伪的行（6.12.2）

`case=matrix` 的 `6.12.2/模块矩阵逐条` 与 `TestMatrixAgainstUpstream` 逐条实测（同一份 Runtime 夹具）。
**只有 `buffer`（`{"keys":10}`）、`fs`（`{"keys":100}`）、`fs/promises`（`{"keys":33}`）三行链接成功。**

| 矩阵行（Tier） | 模块 | 实测 | 归属 |
|---|---|---|---|
| TierAlways | `console` | LINK-FAIL：`No such built-in module: node:console`；裸全局也没有（`proc-03-globals`） | 宿主：`SetGlobal` + 结构化日志 writer（`case=misc` 已验证可行） |
| TierAlways | `path`、`path/posix`、`path/win32` | LINK-FAIL ×3 | 宿主自建（Go `path/filepath` 的虚拟视图，或引入上游实现） |
| TierAlways | `url` | LINK-FAIL；全局 `URL` 由 webapi 提供（`proc-03-globals`） | 宿主：虚拟模块薄封装全局 `URL`/`URLSearchParams` |
| TierAlways | `querystring` | LINK-FAIL | 宿主自建 |
| TierAlways | `string_decoder` | LINK-FAIL | 宿主自建（`TextDecoder` 已在引擎里） |
| TierAlways | `events` | LINK-FAIL（裸名 `events` 也 LINK-FAIL，见 D-04） | 宿主自建 `EventEmitter` |
| TierAlways | `util` | LINK-FAIL；`util.inspect` 完全没有 | 宿主自建（`format`/`inspect`/`types`/`promisify`） |
| TierAlways | `assert` | LINK-FAIL | 宿主自建 |
| TierAlways | `crypto` | LINK-FAIL；全局 `crypto`/`crypto.subtle` 也不存在（语料 `cz-01-absent`） | 宿主自建（`case=misc` 用 Go 标准库 + 虚拟模块验证，sha256("abc") 向量对上） |
| TierAlways | `zlib` | LINK-FAIL（语料 `cz-01-absent`）；webapi 也没有 `CompressionStream`/`DecompressionStream` | 宿主自建 + 自己夹 16MB |
| TierAlways | `stream/web` | LINK-FAIL；但全局 `ReadableStream`/`WritableStream`/`TransformStream` 由 webapi 提供 | 宿主：虚拟模块指向全局即可 |
| TierAlways | `process` | LINK-FAIL；全局 `process` 也不存在（语料 `proc-03-globals`） | 宿主自建（`case=misc` 验证：env 白名单、cwd 虚拟根、`exit` 抛 `ERR_ACCESS_DENIED` 都装得出来） |
| TierAlways | `os` | LINK-FAIL（语料 `proc-02-os` skip-host） | 宿主自建受限 `node:os` |
| TierAlways | `module` | LINK-FAIL（见 D-17） | 宿主自建（E-3） |
| TierLoop | `timers` | LINK-FAIL（见 D-50） | 宿主 shim 或文档改为「只有全局」 |
| TierLoop | `timers/promises` | LINK-FAIL（见 D-51） | 不可用（v1 不装，与矩阵一致） |
| TierCapability | `fs` / `fs/promises` | 可导入，但 16MB/10k/回调/流/裁剪全部要靠宿主（D-27…D-39） | 上游 + 宿主包装 |
| TierCapability | `child_process` | LINK-FAIL（D-32） | ada-go 自建（E-5） |
| TierNever | 全部 17 行 | 全部 LINK-FAIL（`TestMatrixTierNeverIsAbsent` 逐条硬断言） | 与矩阵一致，无需改 |

## 2. 汇总

| 判定 | 条数 | 说明 |
|---|---|---|
| 上游已具备（宿主直接用） | 24 行 | E-1 的 resolver 事实、E-4 的错误码与越界、E-2 的 unref/截断、取消、内存中断、代码生成、结构化克隆、语言面、矩阵 TierNever 全部 |
| 必须 ada-go 宿主实现 | 18 行 | 14 个 TierAlways 模块 + `fs` 的 16MB/10k/回调与流裁剪/成员裁剪调用即抛 + `child_process` + 256 定时器计数 + panic→rejection + 关闭时投递 ErrClosed |
| 标记不可用（v1 不装） | 4 行 | `timers/promises`、`module`（未落地前）、`zlib`（未落地前）、`.node` 原生扩展（`process.dlopen` 不存在，实测已确认） |
| 需要上游补（issue 草案见汇报） | 8 项 | binding 成员惰性化（D-22）、per-runtime 定时器上限（D-11）、单次 IO 字节/目录条目上限可注入（D-36/D-37/D-38）、`timers/promises` 的 signal 取消（D-52）、`createRequire`（D-17）、TypedArray 超限语义统一（D-42）、正则/回溯可中断不需要（D-45 已排除）、`AsyncWork` 关闭投递 `ErrClosed`（D-47）、`SharedArrayBuffer` 禁用位（D-56）、Run 结束的定时器队列语义（D-15）、`ENOTDIR` 归类（D-20）、大整数 Buffer 读写（D-53） |

## 3. 与 D-7 判定表的差异（以实测为准）

D-7 的判定整体成立（E-1 落宿主 resolver、E-4 上游已具备、E-5 由 ada-go 自建、E-6/E-2 宿主夹上限、
E-3/E-8 不可用）。以下是本轮重跑发现的**不一致**，请回写 `docs/decisions.md`：

1. **D-7 的 E-1 行说「裸名 `events` 也能解析」—— 实测不成立。**
   `case=modules` 的 `E-1/裸名-events` 与语料 `mod-03-dynamic-unknown` 都是 LINK-FAIL /
   `Cannot find module 'events'`：上游只把 `fs`、`fs/promises`、`buffer` 三个裸名映射进模块表。
   结论方向不变（白名单落宿主 resolver），但「裸名会绕过」这个风险目前不存在，宿主归一化是**补齐**
   而不是**收紧**。
2. **D-7 的 E-5 行「实测无 shell、allowlist、cwd 越界拒绝、maxBuffer 夹逼、超时与取消都能杀进程树」在上一轮并没有全部实测出来。**
   探针的 `exec_adapter.go` 有两个 bug：JS 数组参数被 `rt.ToGo` 的 `[]any` 单类型断言静默丢弃
   （于是 cmd.exe 收到 0 个参数，`syncFail`/`bigBuffer`/`timeout`/`cancel` 四项全部测不到，
   探针自己把该条标成了「偏差」）；`signal` 在 `cmd.Start()` 之前 abort 时取消整个丢失。
   修掉后重跑，九项检查全部达成（见 D-34），D-7 的结论这才真正成立。两处 bug 也是 M1-C
   宿主桥要避开的坑，记在 D-35。
3. **E-7 行「TypedArray 有偏差」需要具体化。**
   实测：TypedArray 确实计入上限，但超限时给的是**可 catch 的 `RangeError`**（`bin-05-huge-alloc`、
   `TestNodeCompatMemoryLimit/TypedArray`），而字符串/Buffer 是**不可 catch 的中断**
   （`TestNodeCompatMemoryLimit/字符串`）。两者都不是 Node 行为。上一轮探针该条的判定文字
   写的是「JS 的 try/catch 抓不到」，与它自己贴出的实测输出（`caught:"catch:RangeError"`）矛盾，
   本次按实测纠正。
4. **`6.11/正则回溯可中断` 上一轮的「偏差」是探针 bug。**
   脚本写了 `performance.now ? … : Date.now()`，而裸 Runtime 里 `performance` 未声明 →
   `ReferenceError`，压根没跑到正则。改成 `typeof performance` 守卫后实测
   `{"res":"null","ms":0}`：引擎正则是 RE2 风格，没有灾难性回溯，该风险不存在（D-45），
   判定由「偏差」变「符合」。
5. **`6.11/裸 Runtime 无定时器` 上一轮的「符合」是假象。**
   `bareRun` 把 prelude 拼了两次，模块编译就失败（`Identifier '__shape' has already been declared`），
   而 `classify` 不看 `compileErr`，于是被判成「符合」。修掉后实测
   `{"setTimeout":"undefined"}`（D-58），结论没变，但之前那条证据无效。
6. **新增：D-20（`ENOTDIR` 实际给 `ENOENT`）、D-27（`createReadStream` 是抛
   `ERR_METHOD_NOT_IMPLEMENTED` 的占位函数）、D-28（回调形式真实存在）、D-29（`fs.constants`
   恒为 Linux 值）、D-30（`fs` 有 99 个成员，矩阵的兼容范围列过窄）、D-62（取消错误是
   DOMException，`code` 不是字符串 `ABORT_ERR`）、D-61（`ERR_INVALID_ARG_VALUE` 没进 6.12.4
   的码表）、D-15（Run 结束后未到期定时器会补跑）** 这几条 D-7 没覆盖，需要进文档。
7. **`ERR_TIMER_LIMIT` / `ERR_FS_FILE_TOO_LARGE` / `ERR_ACCESS_DENIED` / `ERR_CHILD_PROCESS_STDIO_MAXBUFFER`
   这四个 6.12.4 的码上游一律不产生**（语料 `err-04-host-codes`、`tim-04-limit`、`fs-09-limits`
   全在 skip 列里，理由各指 E-2/E-6/E-4）。6.12.4 要在码表旁注明「由 ada-go 宿主产生」，
   否则读者会以为引擎已经具备。

## 4. 复现

```bash
cd /e/codes/go_projects/ada_go
go run ./tools/moejspike                          # 本表「实测」列的 case 级证据（7 个 case）
go test -count=1 ./internal/conformance/nodecompat -run TestNodeCompat -v   # 语料四组结果 + 基线
go test -count=1 ./internal/conformance/nodecompat                          # 再加矩阵对账与运行时事实
node internal/conformance/nodecompat/corpus/run-node20.mjs                   # Node 侧对照（本机 25.4.0）
```
