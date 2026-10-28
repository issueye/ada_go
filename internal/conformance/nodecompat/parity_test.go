// parity_test.go 是语料用例与真 Node 20 的关系归档（corpus/README.md 的 nodeParity 字段）。
//
// 这张表是 deviations.md 的机器侧索引：每条 deviation 行都要能落到至少一个语料用例上，
// TestNodeCompatParityArchive 会双向咬合（表里的用例必须存在、偏差表引用的用例必须有解释）。
package nodecompat

// parityNote：same = 与 Node 20 观察一致；deviation:… = 有差异，必须进偏差表。
var parityNote = map[string]string{
	"mod-01-fs-identity":     "same",
	"mod-02-cycle":           "same",
	"mod-03-dynamic-unknown": "deviation: Node 20 里 node:vm/node:net/events 都存在；moejs 由宿主 resolver 拒绝，且 rejection 是普通 Error（没有 ERR_MODULE_NOT_FOUND 码）（E-1）",
	"mod-04-import-meta":     "deviation: 引擎默认给 null 原型空对象，url/filename/dirname/resolve 全不存在，填充位是 Importer.Meta（宿主活）",
	"mod-05-cjs-surface":     "deviation: 两边 .mjs 都没有 require，但 Node 能直接跑 CJS 依赖，moejs 只能在构建期由 esbuild 包裹（E-3）",

	"lang-01-job-order":           "same",
	"lang-02-language":            "same",
	"lang-03-unhandled-rejection": "deviation: Node 打告警并可按 --unhandled-rejections 终止；moejs 只给 SetPromiseRejectionTracker 钩子",

	"tim-01-order":         "deviation: 上游三阶段循环（timers→tasks→immediates），immediate 与 0ms timer 的相对顺序与 Node 的 check 阶段不同",
	"tim-02-unref":         "same",
	"tim-03-clear-clamp":   "deviation: 定时器 id 是 Timeout 对象（要 Number() 才是数字）；>2^31 的延时 Node 额外告警，上游静默夹到 1ms",
	"tim-04-limit":         "deviation: Node 没有 256 上限也没有 ERR_TIMER_LIMIT；这条为 ada-go 宿主夹逼准备（E-2）",
	"tim-05-timers-module": "deviation: Node 有 node:timers 与 node:timers/promises；moejs 两者都没有，定时函数只是 eventloop 的全局（E-8）",
	"tim-06-signal-cancel": "deviation: Node 的 setTimeout 不接受 signal（那是 timers/promises 的 API）；moejs 把第三参当附加参数忽略，取消静默失效（E-8）",

	"bin-01-buffer":        "deviation: allocUnsafe 确实清零（上游无池）；readBigInt64LE 等大整数读写缺失",
	"bin-02-text-decoder":  "same",
	"bin-03-atob-clone":    "deviation: btoa/atob 按 WHATWG 严格校验，Node 对非 ASCII 输入的错误类型不同",
	"bin-04-shared-memory": "deviation: 设计说插件 Runtime 不提供 SharedArrayBuffer/Atomics，上游实际给了且没有禁用位（要宿主 MutableIntrinsics 删除或上游补开关）",
	"bin-05-huge-alloc":    "deviation: Node 是可 catch 的 RangeError；moejs 是不可 catch 的中断（Run 返回 *InterruptedError）",

	"fs-01-enoent":            "same",
	"fs-02-kind-mismatch":     "deviation: ENOTDIR 场景上游按「路径不存在优先」给 ENOENT；Node 20 给 ENOTDIR",
	"fs-03-escape":            "deviation: Node 20 没有 chroot，越界读会成功；moejs 用 os.Root 检查落点，越界 EACCES（errno 是负 libuv 值）",
	"fs-04-roundtrip":         "same",
	"fs-05-promises-callback": "deviation: 矩阵说不提供回调形式，上游同步/回调/promise 三种都有",
	"fs-06-readonly-write":    "deviation: 只读时写成员仍存在且可调用，失败码是 EROFS 而不是 ERR_ACCESS_DENIED（E-4 的裁剪退成「调用即抛」）",
	"fs-07-members-constants": "deviation: fs.constants 恒为 Linux 值（Windows 上 Node 给 libuv 的 Windows 值）；watch/fd/chmod 是抛 ERR_METHOD_NOT_IMPLEMENTED 的占位函数",
	"fs-08-symlink":           "deviation: Node 跟随越界符号链接；moejs 按真实落点 EACCES，lstat 不跟随",
	"fs-09-limits":            "deviation: Node 无 16MB/10k 上限；上游的拒绝阈值就是 Runtime 的 MemoryLimit，与堆耦合（E-6）",

	"proc-01-process": "deviation: 上游完全没有 process（全局与 node:process 模块都没有），整行是宿主工作量",
	"proc-02-os":      "deviation: 上游没有 node:os",
	"proc-03-globals": "deviation: 装上 webapi 后仍没有 Blob/File/FormData/console/process/crypto；SharedArrayBuffer/Atomics 反而存在",

	"cp-01-module":   "deviation: 上游没有 node:child_process——spawn/fork/exec 是「不存在」而不是「可绕过」，execFile 要 ada-go 自建（E-5）",
	"cp-02-execfile": "same",

	"cz-01-absent":  "deviation: 上游没有 node:crypto/node:zlib，也没有全局 crypto/subtle",
	"cz-02-vectors": "same",

	"err-01-shape-stack": "deviation: 栈帧指向虚拟模块名（plugin://…）但没有 source map 映射；Node 给真实文件路径",
	"err-02-arg-type":    "deviation: 参数类型/范围错误的 code 与 Node 不完全一致，按实测归档",
	"err-03-abort-shape": "deviation: 取消错误是 DOMException（name=AbortError），code 与设计写的 ABORT_ERR 不同（6.12.4 要宿主归一化）",
	"err-04-host-codes":  "deviation: ERR_ACCESS_DENIED/ERR_TIMER_LIMIT/ERR_FS_FILE_TOO_LARGE 都是宿主码，上游不产生",
}
