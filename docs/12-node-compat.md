# moejs 引擎能力与 Node 兼容层

> 上级索引：[ada-go 设计文档](design.md)  
> 对应章节：§6.10 定位与分层、§6.11 引擎能力契约、§6.12 Node 兼容原生库、§6.13 安全与资源边界、§6.14 兼容性验证  
> 版本：0.1 · 2026-10-28

## 6.10 定位：API 形状兼容，不是 Node 运行时

插件作者应当能用 Node 的习惯写后端插件：`import fs from "node:fs"`、`Buffer`、`EventEmitter`、`AbortSignal`、`async/await` 都按预期工作；但 ada-go 不嵌入 Node，也不把 Node 的运行时语义整体搬进来。

兼容分成三层，权限由宿主安装或配置：

| 层 | 提供方 | 内容 | 权限 |
|---|---|---|---|
| L1 引擎 | moejs | 语言、ESM/CJS、Promise/job、二进制、错误、内存与中断 | 无 IO；未安装的东西不存在 |
| L2 原生库 | moejs `stdlib` + ada-go 宿主配置 | 以 `node:*` 名字暴露的 Go 实现：`fs`、`path`、`crypto`、`child_process` 等 | 每个模块、每个能力位单独安装；边界由 Root/ReadOnly、Allow、Env 等宿主配置决定 |
| L3 宿主能力 | ada-go Core | `agent:host`、`ctx.fs`、`ctx.fetch`、`ctx.exec`、`ctx.state`、`ctx.secret`、`ctx.ui` | 插件 API 的出口；Scope、审计、超时、密钥在这里执行 |

`ctx.fs`/`ctx.fetch`/`ctx.exec` 是 ada-go 的显式宿主 API；`node:*` 是给现有 Node 代码与依赖的兼容形状。两者都由宿主安装，走同一套 Scope、能力位与审计策略，不是两套权限；新增模块不能绕开宿主配置直接访问系统。

**兼容承诺**：

- 目标是 **Node 20/22 LTS 的可观察行为**，不是内部实现。文档列出的子集之外，不保证兼容。
- 不支持的 API 必须**显式失败**（`ERR_ACCESS_DENIED`、`ERR_MODULE_NOT_FOUND` 或模块不存在），不允许静默降级或返回近似值。
- 同一份兼容性语料在 Node 20 与 moejs 下各跑一遍；差异要么修复，要么写进本文的“偏差”列。
- locale、时区、随机源、时钟由宿主固定；同一输入在同一配置下结果可复现。需要 Node 已知怪癖时由宿主显式开启 `WithNodeQuirks()`。

**明确不是**：

- 不是 Node 进程：没有 libuv、没有 V8、没有事件循环线程池、没有 `worker_threads`/`cluster`。
- 不支持原生扩展：`.node`、N-API、node-gyp、`process.dlopen` 一律不存在；加载时返回 `ERR_NATIVE_ADDON_UNSUPPORTED`。
- 不做运行时 npm 解析：依赖在构建/安装期由 esbuild 打进产物；运行期只认 `node:*` 与虚拟模块。
- 不提供服务器与套接字：`http`/`https`/`net`/`tls`/`dgram`/`dns` 不安装；provider 用 `ctx.http.sse`，插件出网用 `ctx.fetch`。
- 不提供 `vm`、`inspector`、`repl`、`wasi`；`eval`/`new Function` 对第三方关闭。
- 不承诺 Node 的版本号：`process.versions.node` 报兼容基线（如 `"20.0.0"`），`process.versions.ada` 与 `process.versions.moejs` 报真实值。

**与基座边界的关系**：Node 兼容层是运行时基础设施，不是插件贡献。它不注册工具、不提供 provider、不读产品配置、不出现任何官方插件 id；它只把“已授予的能力”翻译成插件熟悉的模块形状。Gateway 的 Render Runtime 不安装任何 Node 兼容模块。

---

## 6.11 引擎能力契约

以下能力是 ada-go 对 moejs 的硬性要求。“验收”列同时是兼容性测试的入口。交付形态优先在上游实现：ada-go 不维护私有 fork，也不在宿主里复制一套 Node 模块；宿主只做能力位到 `stdlib.Config` 的映射、错误归一化与审计包装。

| 能力 | 硬性要求 | 验收 |
|---|---|---|
| 语言与内建 | 覆盖 Node 20 常用语法：类与私有字段、解构、生成器/异步生成器、顶层 `await`、`Proxy`/`Reflect`、`Symbol`、`Map`/`Set`/`WeakMap`/`WeakSet`、`BigInt`、可选链与空值合并 | test262 子集 + 真实 npm 包冒烟 |
| 模块系统 | ESM 与 CJS 双向互操作；默认导出与命名导出按 Node 规则；动态 `import()`；每 Runtime 一份模块缓存；循环依赖不挂起；`import.meta.url/filename/dirname/resolve` 指向虚拟路径 | ESM/CJS 夹具、循环依赖、缓存同一性 |
| Promise 与 job | microtask 按序排空；thenable 同化；`await` 不阻塞宿主；未处理的 rejection 交给宿主钩子，而不是结束进程 | job 顺序、未处理 rejection、嵌套 `await` |
| 宿主异步桥 | `rt.Go`/`AsyncWork` 的结果恰好投递一次；`Post`/`Done`/`Unref` 语义完整；Runtime 关闭时未完成工作收到 `ErrClosed`；宿主回调 panic 变成 rejected Promise；JavaScript 值不跨 goroutine | 关闭竞态、取消、panic 注入 |
| 定时器与循环 | `setTimeout`/`setInterval`/`queueMicrotask`/`setImmediate`；毫秒截断与 32 位上限；microtask 先于 timer；`unref`/`ref`；每 Runtime 定时器数量上限；关闭时全部取消 | 顺序、取消、上限、`unref` 后不阻止结束 |
| 取消 | `AbortController`/`AbortSignal`：`reason`、`throwIfAborted`、`AbortSignal.timeout`、`AbortSignal.any`、`abort` 事件；宿主 `ctx.signal` 与脚本 signal 双向可取消；监听器可清理 | 超时、父取消、监听器泄漏 |
| 二进制与编码 | `ArrayBuffer`、TypedArray、`DataView`、`Buffer`（Uint8Array 子类）、`TextEncoder`/`TextDecoder`（UTF-8、`fatal`、`ignoreBOM`）、`atob`/`btoa`、`structuredClone` 与 transfer；插件 Runtime 不提供 `SharedArrayBuffer`/`Atomics` | 编解码、克隆、transfer、越界 |
| 错误 | `Error.cause`、`AggregateError`、`DOMException`；跨宿主边界保留 `name`/`message`/`code`/`stack`；栈帧映射到 `plugin://<id>/...` 与 esbuild source map；不泄露宿主绝对路径与密钥 | 错误形状、栈映射、脱敏 |
| 资源边界 | 内存上限计入脚本持有的对象、字符串、TypedArray 与 Buffer；栈大小与调用深度独立上限；context 中断在解释器内轮询且脚本不可 `catch`；正则回溯可中断；Runtime 可关闭且不留 goroutine | 内存、死循环、深递归、灾难性回溯 |
| 模块策略 | 能按模块名安装/拒绝；未知模块在链接期失败；同一模块的 `fs` 与 `node:fs` 是同一实例；第三方 Runtime 关闭代码生成 | 模块白名单、别名同一性、链接期错误 |
| 可观测 | 结构化 `console`；未捕获异常与未处理 rejection 有宿主钩子；可读取当前脚本名与行号；关闭原因可区分超时、内存、取消、内部错误 | 日志字段、关闭原因 |

实现上依赖这些接口（已存在或按 E-x 补齐）：`SetModule`/`SetModuleValues`/`SetModuleLoader`/`SetAsyncModuleLoader` 控制模块表；`rt.Go`/`StartAsyncWork`/`Post`/`Done`/`Unref`/`AbortOn` 管异步；`CallContext`/`Await`/`RunJobsContext` 执行与排空；`WithMemoryLimit`/`WithStackSize`/`WithMaxCallDepth` 设边界；`WithoutCodeGeneration` 关代码生成；`SetTimeZone`/`WithLocale`/`WithNodeQuirks` 固定可复现行为；`Serialize`/`Deserialize` 跨 Runtime 克隆；`OnUnhandledRejection`/`OnClose` 接生命周期。

### 6.11.1 现状与待补

moejs 已经提供 ESM/CJS 互操作、顶层 `await`、`AsyncWork`、`AbortController`、`Buffer`、`structuredClone`、`TextEncoder`、source map、内存/栈/深度上限、context 中断、模块加载器与按能力拆分的 `stdlib`。ada-go 还需要以下接口或行为，按优先级排列：

| 编号 | 待补能力 | 用途 |
|---|---|---|
| E-1 | 模块白名单/拒绝列表（细于当前 `NoWebAPIs` 的整组开关） | 只装纯计算模块，排除 `vm`、`worker_threads`、`async_hooks`；避免“为了 `util` 顺带装上 `vm`” |
| E-2 | 定时器 `unref`/`ref`、每 Runtime 数量上限、全局 `setImmediate` | 插件可等待但不必阻止 Runtime 结束；防止定时器耗尽 |
| E-3 | `node:module` 的 `createRequire`（仅解析已安装的 `node:*`） | 兼容 CJS 依赖里的 `require("node:path")`；禁止磁盘解析 |
| E-4 | `fs` 的 Node 错误码与字段（`code`、`errno`、`syscall`、`path`）；只读时静态裁剪写成员 | 插件按 `err.code === "ENOENT"` 分支；能力缺失在调用前就暴露 |
| E-5 | `child_process` 的 shell 开关与按函数安装（只装 `execFile`/`execFileSync`） | ada-go 不允许 shell；`spawn`/`fork` 不进入 v1 |
| E-6 | 每次调用的文件大小、目录条目数上限（或宿主可注入的包装） | 在引擎层再兜一层 16MB/10k，而不只靠插件自觉 |
| E-7 | 宿主侧 Buffer/字符串的计入或独立配额 | 内存上限之外，Go 侧副本不能无界增长 |
| E-8 | `timers/promises` 的 `signal` 取消与 `setInterval` 清理保证 | 长等待可被轮次取消 |

这些是 **moejs 的演进项**，不是当前已实现清单；E-1/E-4/E-5 是 M1 阻断项。未补齐前，ada-go 只能用更窄的安装（例如不装 `fs`/`child_process`）或宿主包装规避，不能宣称 6.12 的矩阵已经满足。本地联合修复与双跑使用 [11 §13.1](11-structure-roadmap.md#131-go-workspace) 的 Go workspace；发布验证必须切回 `GOWORK=off`，以版本化依赖复验。

---

## 6.12 Node 兼容原生库

“原生库”指 Go 实现、以 Node 模块名暴露的内置模块。它们不向脚本暴露原生句柄；文件、网络、命令、环境变量的范围由宿主的配置与回调决定（Root/ReadOnly、Fetch.Allow、Run.Allow、Env），脚本不能改。

### 6.12.1 安装

宿主按清单能力构造 `stdlib.Config`；没声明的模块根本不安装。目标形态：

```go
// 目标形态；模块白名单与 Run.Shell 是 6.11 的待补项。
cfg := stdlib.Config{
    Stdout: logWriter, Stderr: logWriter,
    Loop:   loop, // 无 Loop 则没有计时器，promise 立即结算
    Process: &stdlib.Process{
        Args:    []string{"adag-plugin", id},
        Env:     envAllowlist(manifest.Backend.Env), // 只含清单列出的名字
        Cwd:     "/", // 工作区在脚本里挂到 /，不暴露宿主绝对路径
        Version: "20.0.0",
        Exit:    nil, // process.exit 抛异常，不能结束 Core
    },
    FS:          nodeFS(caps, workspace), // 无 fs.read/fs.write 时为 nil
    OS:          &stdlib.OSInfo{Tmpdir: "/tmp"},
    Fetch:       nodeFetch(caps),         // 无 net.fetch 时为 nil
    Run:         nodeRun(caps),           // 无 exec 或非 builtin/verified 时为 nil
    NoWebAPIs:   false,
    WindowsPaths: false, // 虚拟 FS 统一 POSIX 视图，宿主路径不进入脚本
}
```

安装后还要做三件事：

1. **链接期检查**：静态 `import "node:fs"` 在没有 `fs.read`/`fs.write` 时直接让插件装配失败，错误码 `ERR_ACCESS_DENIED`；不要等到第一次调用才报错。
2. **成员裁剪**：模块存在但部分成员无权限时，被裁剪的成员访问即抛 `ERR_ACCESS_DENIED`（用 Proxy 的 `get` 陷阱），避免 `typeof fs.writeFile === "undefined"` 这种可被误判为“版本差异”的静默行为。
3. **审计**：所有受控调用记录模块、方法、作用域内路径/主机/命令、字节数与结果；参数中的密钥与 token 脱敏。

### 6.12.2 模块矩阵

#### 始终安装（纯计算，无外部效果）

| 模块 | 兼容范围 | 偏差与边界 |
|---|---|---|
| `console` | `log`/`info`/`warn`/`error`/`debug` 映射到结构化日志 | 不写进程 stdout；长参数按 `util.inspect` 截断，避免日志放大 |
| `path`、`path/posix`、`path/win32` | 与 Node 一致：`join`/`resolve`/`normalize`/`parse`/`format`/`relative`/`isAbsolute`；两种风格都在 | `path.resolve` 以 `process.cwd()` 为基准，即虚拟根 `/`（工作区） |
| `url` | `URL`、`URLSearchParams`、`fileURLToPath`、`pathToFileURL`、`format`、`parse`、`resolve`、`domainToASCII/Unicode` | `fileURLToPath` 仅接受 `plugin://` 与 `file://` 虚拟路径 |
| `querystring`、`string_decoder` | Node 形状；`StringDecoder` 仅 UTF-8 | 其他编码抛 `ERR_ENCODING_NOT_SUPPORTED` |
| `events` | `EventEmitter`：`on`/`once`/`off`/`emit`/`listenerCount`/`removeAllListeners` | `error` 事件无监听时抛出，符合 Node |
| `util` | `format`、`inspect`、`promisify`、`callbackify`、`types`、`TextEncoder`/`TextDecoder` | `inspect` 的深度与颜色由宿主固定；不读取终端能力 |
| `assert` | `ok`、`equal`、`strictEqual`、`deepEqual`、`deepStrictEqual`、`throws`、`rejects`、`match` | 错误消息不保证逐字一致；`AssertionError` 字段一致 |
| `buffer` | `Buffer.from`/`alloc`/`allocUnsafe`/`concat`/`isBuffer`/`byteLength`；`Buffer` 是全局 | `allocUnsafe` 仍清零；不暴露未初始化内存 |
| `crypto` | `createHash`（md5/sha1/sha2/sha3）、`createHmac`、`pbkdf2`、`hkdf`、`randomBytes`/`randomInt`/`randomUUID`、`timingSafeEqual`；`crypto.subtle` 的 digest/HMAC/PBKDF2/HKDF | 无对称加解密、无 `sign`/`verify` 非对称、无 X.509/KeyObject；随机源由宿主注入 |
| `zlib` | `gzip`/`gunzip`/`deflate`/`inflate`/`deflateRaw`/`inflateRaw` 及同步形式；`CompressionStream`/`DecompressionStream` | 无 brotli/zstd；单次解压产物由宿主夹到 16MB（引擎默认上限 1GB）；超出报错 |
| `stream/web` | Web Streams：`ReadableStream`/`WritableStream`/`TransformStream`/`TextEncoderStream` | 没有 Node 的 `stream` 模块；`pipe` 用 `pipeTo`/`pipeThrough` |
| Web 数据 API | `URL`、`TextEncoder`/`TextDecoder`、`atob`/`btoa`、`structuredClone`、`Blob`、`File`、`FormData`、`DOMException`、`performance`、`AbortController` | 无 DOM、无 `window`、无 `localStorage`；`performance` 只提供 `now`/`timeOrigin` |

#### 需要 Loop（定时器）

| 模块 | 兼容范围 | 偏差与边界 |
|---|---|---|
| 全局 `setTimeout`/`setInterval`/`setImmediate`/`clearTimeout`/`clearInterval`/`clearImmediate`/`queueMicrotask` | 毫秒、参数透传、返回数字 id | 没有独立事件循环线程；回调在 Runtime 的 goroutine 上执行；`setImmediate` 以 0ms 定时器实现，与 Node 的 check 阶段顺序不完全相同 |
| `timers`、`timers/promises` | `setTimeout`/`setImmediate`/`setInterval`；promise 形式支持 `signal` | `setImmediate` 以 0ms 定时器实现，顺序与 Node 的 check 阶段不完全相同；定时器不打满 CPU，长等待应让出 |
| 上限 | 每 Runtime 256 个未完成定时器 | 超出抛 `ERR_TIMER_LIMIT`；Runtime 关闭时全部取消 |

#### 按能力安装（受限 IO）

| 模块 | 能力位 | 兼容范围 | 偏差与边界 |
|---|---|---|---|
| `fs`、`fs/promises` | `fs.read` / `fs.write` | `readFile`/`writeFile`/`appendFile`/`readdir`/`stat`/`lstat`/`mkdir`/`rm`/`rename`/`copyFile`/`realpath`/`readlink`/`existsSync`；同步与 promise 两种形状；`createReadStream`/`createWriteStream`（Web Streams） | 根固定为工作区，脚本视图 `/`；symlink 越界拒绝；单次读写 16MB、目录 10k 条；不提供回调形式；v1 不开放 `watch`、文件描述符、`chmod`/`chown`/`utimes` |
| `process` | 始终（只读子集） | `argv`、`env`（白名单）、`cwd`、`platform`、`arch`、`version`/`versions`、`pid`、`hrtime`、`nextTick`、`stdout`/`stderr` | `cwd` 是虚拟根 `/`；`env` 只含 `backend.env`；`exit` 抛异常；没有 `kill`、`binding`、`dlopen`、`setuid`、`umask` |
| `os` | 始终（受限） | `platform`、`arch`、`EOL`、`cpus`、`tmpdir` | `hostname`/`homedir` 为空；没有 `networkInterfaces`、`userInfo`、`loadavg` |
| `child_process` | `exec`，仅 builtin/verified | `execFile`/`execFileSync`；参数数组、`cwd`、`env`、`timeout`、`maxBuffer` | 不安装 `spawn`/`fork`；`exec`/`execSync` 因走 shell 而被拒绝（`Run.Shell=false`）；`cwd` 使用虚拟根并由宿主映射到工作区，越界拒绝；捕获上限 4MB，超时 120s，杀整棵进程树；进入模型回执仍按 50KB/2000 行截断 |
| `module` | 始终（受限，待补 E-3） | `createRequire` 只解析已安装的 `node:*`；`builtinModules` 列已安装模块；`isBuiltin` | 不做磁盘解析、不读 `package.json`、没有 `require.cache`/`require.main` |
| `node:test` | 否 | 不安装 | 插件测试在构建期用 Node 跑，不在 Runtime 内跑；`assert` 仍可用 |

#### 不安装

| 模块 | 替代 |
|---|---|
| `http`、`https`、`net`、`tls`、`dgram`、`dns` | 出网用 `ctx.fetch`（`net.fetch`）或 `ctx.http`（provider 的 `net.sse`）；监听端口不是插件能力 |
| `worker_threads`、`cluster`、`child_process.spawn/fork` | 子智能体用 `subagent.invoke`；并行工具由 Core 调度 |
| `vm`、`inspector`、`repl`、`wasi` | 不开放；第三方 Runtime 关闭代码生成 |
| `async_hooks` | v1 不安装；需要请求上下文时用 `ctx` 显式传递，v1.1 再评估 `AsyncLocalStorage` |
| `perf_hooks` | 只用全局 `performance.now`；不提供 histogram/observer |
| `node:sqlite`（Node 22） | 持久化用 `ctx.state`；会话库只属于 Core |
| `.node` 原生扩展 | 安装期拒绝，`ERR_NATIVE_ADDON_UNSUPPORTED` |

### 6.12.3 解析与打包

- 官方插件在构建期由 esbuild 预编译；第三方在安装/首次加载时编译。两者都以 `platform=node`、`format=esm` 打包，`node:*` 与裸内置名标记为 external，交给 Runtime 的模块表。
- CJS 依赖由 esbuild 转成 ESM 包裹，保留 `module.exports`/`exports`/`__dirname`/`__filename`；`__dirname` 是 `plugin://<id>/` 下的虚拟目录，不是宿主路径。`require` 只解析已安装的 `node:*` 或同包内联模块。
- 推荐写 `node:` 前缀；`fs` 与 `node:fs` 解析到同一个模块实例，`require` 与 `import` 看到同一份导出。
- 依赖在构建期全部内联；运行期**没有** npm 目录扫描、没有 `node_modules`、没有 `package.json` 的 `exports` 条件解析。缺失依赖在安装期报 `ERR_MODULE_NOT_FOUND`。
- `import.meta.url` 形如 `plugin://<id>/index.js`；`import.meta.resolve` 只解析虚拟模块与已安装的 `node:*`。
- 插件包内的静态资源用 `plugin://<id>/...` 只读访问；写操作只能落到工作区。
- 第三方插件不能通过字符串拼接绕过模块策略：所有解析都经过宿主 loader，未知 specifier 直接失败。

### 6.12.4 错误映射

Node 代码大量依赖 `err.code` 分支，因此错误形状是兼容契约的一部分：

| `code` | 触发 | 附加字段 |
|---|---|---|
| `ENOENT` | 路径不存在 | `syscall`、`path` |
| `EACCES` / `EPERM` | 权限不足、只读文件系统 | `syscall`、`path` |
| `EISDIR` / `ENOTDIR` | 路径类型不符 | `syscall`、`path` |
| `EEXIST` / `ENOTEMPTY` | 已存在、目录非空 | `syscall`、`path`、`dest` |
| `ENOSPC` / `EMFILE` | 磁盘满、句柄耗尽 | `syscall`、`path` |
| `ETIMEDOUT` | 命令或宿主调用超时 | `syscall` |
| `ABORT_ERR` | `AbortSignal` 取消 | `name = "AbortError"` |
| `ERR_INVALID_ARG_TYPE` / `ERR_OUT_OF_RANGE` | 参数类型或范围错误 | `name = "TypeError"` / `"RangeError"` |
| `ERR_ACCESS_DENIED` | 能力缺失、作用域越界、成员被裁剪 | `capability` |
| `ERR_MODULE_NOT_FOUND` | 模块不存在或未安装 | `specifier` |
| `ERR_NATIVE_ADDON_UNSUPPORTED` | 尝试加载 `.node` | `path` |
| `ERR_FS_FILE_TOO_LARGE` | 超过单次读写上限 | `size`、`limit` |
| `ERR_CHILD_PROCESS_STDIO_MAXBUFFER` | 命令输出超过 `maxBuffer` | `limit` |
| `ERR_TIMER_LIMIT` | 定时器超过每 Runtime 上限 | `limit` |

错误对象至少含 `name`、`message`、`code`；文件与命令错误再带 `errno`、`syscall`、`path`。栈里只出现 `plugin://` 路径。

---

## 6.13 安全与资源边界

- **无环境权限**：L2 模块不向脚本暴露原生句柄；文件、网络、命令都在宿主构造的边界内（Root/ReadOnly、Fetch.Allow、Run.Allow、Env），并统一审计。插件不能通过 Node 模块拿到第二条访问路径。
- **文件**：根固定为工作区；`..` 与 symlink 按真实落点检查；插件目录只读；密钥目录永不授予。单次读写 16MB、目录 10k 条、路径 4096 字节；超限报错而不是截断。
- **环境变量**：`backend.env` 是白名单，只把列出的名字注入 `process.env`；值在装配期冻结。`ADAG_*` 与密钥名是保留名，清单声明也会被拒绝。密钥不走环境变量，走 `ctx.secret(name)`；`process.env` 中永远不出现密钥。
- **网络**：默认没有任何网络模块。`ctx.fetch` 受 `net.fetch` 与宿主 allowlist 约束（协议、主机、端口、重定向逐跳检查、响应体上限）；provider 的 `ctx.http.sse` 受 `net.sse` 约束，只有 builtin/verified 可用。没有 `listen`、没有原始 socket。
- **命令**：`child_process` 只有 `execFile`，仅 `exec` 能力且仅 builtin/verified；`Run.Allow` 再按程序名与参数检查；`cwd` 使用虚拟根并由宿主映射到工作区，越界拒绝；环境变量白名单；120s 超时；4MB 输出；取消时杀整棵进程树。`exec`/`execSync` 因走 shell 而禁用。
- **随机与时间**：`crypto` 的随机源由宿主注入；时间、locale、时区由宿主固定，避免插件用时间侧信道或不可复现行为。
- **内存**：插件 Runtime 64MB（脚本堆）。宿主为一次调用分配的 Go 侧缓冲另计，单次不超过 16MB；所有 Buffer 在调用结束释放。Runtime 超内存即重建，插件标记 `degraded`。
- **CPU**：脚本调用 5s、工具 120s、钩子 200ms（最大 2s）、provider 15s/90s/30min；宿主长调用暂停脚本计时；取消不可被 `catch`。
- **模块策略失败方向**：没有能力就不安装；安装了但成员无权限就抛 `ERR_ACCESS_DENIED`；未知模块在链接期失败。任何情况下都不回退到“全量 Node”。
- **前端**：Gateway 的 Render Runtime 不安装 `stdlib`，没有 `process`、`Buffer`、`fs`、`fetch`；TSX 只做纯渲染（见 [7](07-ssr-frontend.md)）。

---

## 6.14 兼容性验证

### 6.14.1 双跑语料

同一组用例在 Node 20 LTS 与 moejs 插件 Runtime 下运行，比较返回值、错误 `code`、事件顺序与副作用。允许的差异必须写进 6.12 的“偏差”列；新增差异先改文档再改实现。

### 6.14.2 分层用例

| 层 | 用例 |
|---|---|
| 模块解析 | `node:fs` 与 `fs` 同一实例；`require`/`import` 看到同一导出；循环依赖；动态 `import()`；未安装模块链接期失败；缺失依赖安装期失败；`plugin://` 路径 |
| 语言与异步 | 顶层 `await`；microtask/timer 顺序；未处理 rejection；`AsyncWork` 关闭竞态；宿主 panic 转 rejection；跨 goroutine 无 JS 值 |
| 定时器 | `setTimeout`/`setInterval`/`setImmediate`；清除；`unref`；256 上限；Runtime 关闭取消；`timers/promises` 的 `signal` |
| 二进制 | Buffer 与 Uint8Array 互转；`TextDecoder` 分片；`atob`/`btoa`；`structuredClone` 循环引用与 transfer；超大分配报错 |
| `fs` | 读写/追加/列目录/stat；只读时写成员拒绝；`..` 与 symlink 越界；16MB 上限；10k 条目；`ENOENT`/`EACCES`/`EISDIR`；Web Stream 分块读写 |
| `process`/`os` | `env` 只含白名单；`cwd` 为 `/`；`exit` 抛异常；`hrtime`；`hostname`/`homedir` 为空 |
| `child_process` | `execFile` 成功/失败/超时/输出超限；shell 被拒绝；`cwd` 越界；取消杀进程树；环境变量白名单 |
| `crypto`/`zlib` | 已知向量；HMAC/PBKDF2/HKDF；`timingSafeEqual`；gzip/deflate 往返；解压炸弹上限 |
| 错误 | 每个 `code` 的 `name`/`message`/`code`/`syscall`/`path`；栈映射到 `plugin://`；不泄露宿主路径 |

### 6.14.3 真实包冒烟

选只依赖受支持子集的包，在安装期打包、在插件 Runtime 内执行：`semver`、`yaml`、`zod`、`picomatch`、`nanoid`（纯计算）；再加一个使用 `fs`/`path` 的目录扫描小包，验证作用域与流式读。任何需要原生扩展、`http`、`worker_threads` 或 Node 流的包，明确列为“不支持”，不进入冒烟集。

### 6.14.4 门禁

```bash
go test ./internal/conformance/nodecompat -run TestNodeCompat
go test ./internal/plugins/host/node -run TestCapabilityMatrix
adag verify nodecompat          # 模块表、能力位、错误码、文档偏差表
adag verify boundary            # Node 兼容层不注册工具/不出现插件 id
```

性能基线：模块导入 p95 < 10ms（单插件、已编译产物）；受控宿主调用 p95 < 2ms（不含实际 IO）；`fs` 4KB 读 p95 < 5ms（SSD）；Node 兼容层常驻内存 < 2MB/插件（不含 Buffer）。

---

**相邻文档**：[← 目录、里程碑与风险](11-structure-roadmap.md) · [索引](design.md) · [附录 →](13-appendix.md)
