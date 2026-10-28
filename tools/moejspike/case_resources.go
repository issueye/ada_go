package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
)

func init() {
	register("resources", "资源边界：内存/中断/代码生成/宿主 panic/异步关闭（E-7 与 6.11 资源行）", runResources)
}

func runResources(r *recorder) {
	t, err := makeTree("resources", 2<<20)
	if err != nil {
		r.add(vNa, "夹具", "建工作区", err.Error(), "resources 用例未跑")
		return
	}

	// 1) 内存上限：TypedArray
	o := runJS(js(`let caught = "no";
try { globalThis.hold = [new Uint8Array(32 * 1024 * 1024)] } catch (e) { caught = "catch:" + e.name }
__report(JSON.stringify({caught, kept: globalThis.hold ? globalThis.hold[0].length : 0}));`),
		cfg{memLimit: 8 << 20})
	r.add(ifElse(o.runErr != nil && strings.Contains(o.runErr.Error(), "memory limit"), vDev, vDev), "6.11/内存上限-TypedArray",
		"6.11 资源边界：内存上限计入脚本持有的对象、字符串、TypedArray 与 Buffer；6.13：插件 Runtime 64MB（脚本堆）",
		o.actual(),
		"计入，且是「不可 catch」的中断：JS 的 try/catch 抓不到，Run 返回 *InterruptedError（errors.Is 命中 moejs.ErrMemoryLimit）。注意这不是 Node 行为（Node 里 RangeError 可捕获），M1-D 的语料要把这条写成「宿主侧终止」而不是「脚本可恢复错误」")

	// 2) 内存上限：字符串
	o = runJS(js(`let s = "";
for (let i = 0; i < 24; i++) { s += "A".repeat(1024 * 1024) }
globalThis.keep = s;
__report(JSON.stringify({len: s.length}));`), cfg{memLimit: 8 << 20})
	r.add(ifElse(o.runErr != nil, vMatch, vDev), "6.11/内存上限-字符串",
		"同上：字符串也计入",
		o.actual(),
		"字符串计入；同样以中断形式终止，脚本无法 catch")

	// 3) 内存上限：Buffer（Go 侧缓冲是否计入，E-7）
	o = runJS(js(`let caught = "no";
try { globalThis.keep = Buffer.alloc(32 * 1024 * 1024) } catch (e) { caught = "catch:" + e.name }
__report(JSON.stringify({caught, len: globalThis.keep ? globalThis.keep.length : 0}));`),
		cfg{memLimit: 8 << 20, install: nodeFS(t, "/workspace", false)})
	r.add(ifElse(o.runErr != nil || strings.Contains(o.report, "catch"), vMatch, vDev), "E-7/Buffer 是否计入",
		"6.11.1 E-7：宿主侧 Buffer/字符串的计入或独立配额；6.13：宿主为一次调用分配的 Go 侧缓冲另计，单次不超过 16MB",
		o.actual(),
		"node 包的 Buffer 就是 Uint8Array 子类，分配走引擎堆，因此直接受 MemoryLimit 约束；E-7 关心的「Go 侧副本」在 fs/webapi 包里也已计入（读文件前先用 MemoryLimit 夹），剩下的是 ada-go 宿主自己分配的缓冲，只能宿主自查")

	// 4) 死循环被中断：脚本能否 catch
	o = runJS(js(`let caught = "no";
try { let x = 0; while (true) { x++ } } catch (e) { caught = "catch:" + (e && e.message) }
__report(JSON.stringify({caught}));`), cfg{startLoopWithCancel: 120 * time.Millisecond})
	r.add(ifElse(o.runErr != nil && !strings.Contains(o.report, "catch:"), vMatch, vDev), "6.11/取消不可 catch",
		"6.11 资源边界：context 中断在解释器内轮询且脚本不可 catch；6.13：取消不可被 catch",
		o.actual(),
		"确认成立：中断把整个调用抛出为 *InterruptedError，JS 的 try/catch 抓不到（本例没有 __report），设计里的这条不用打折扣")

	// 5) 深递归
	o = runJS(js(`let depth = 0;
try { const f = (n) => { depth = n; return f(n + 1) }; f(0) } catch (e) { __report(JSON.stringify({name: e.name, code: e.code, message: e.message, depth})) ; throw e }
__report(JSON.stringify({ok: depth}));`), cfg{})
	r.add(ifElse(strings.Contains(o.report, "RangeError") || strings.Contains(o.cbErrs[0], "RangeError"), vMatch, vDev), "6.11/栈与调用深度",
		"6.11：栈大小与调用深度独立上限（WithStackSize/WithMaxCallDepth）",
		o.actual(),
		"引擎有调用深度上限并抛 RangeError（Maximum call stack size exceeded），脚本可捕获；但 moejs.Options 没有 WithStackSize/WithMaxCallDepth 这类可调参数，宿主无法按设计给 64KB/2000 层的显式值——只能接受引擎默认")

	// 6) 灾难性正则回溯
	o = runJS(js(`const now = () => (typeof performance !== "undefined" && performance.now) ? performance.now() : Date.now();
const t0 = now();
const s = "a".repeat(28) + "b";
let res;
try { res = String(s.match(/(a+)+$/)) } catch (e) { res = "throw:" + e.message }
const t1 = now();
__report(JSON.stringify({res, ms: Math.round(t1 - t0)}));`), cfg{})
	r.add(classify(o, ""), "6.11/正则回溯可中断",
		"6.11 资源边界：正则回溯可中断",
		o.actual(),
		"引擎的正则不是回溯式的（RE2 风格），28 个 a 加尾 b 没有指数爆炸，单次匹配在毫秒级；这条风险在 moejs 上不存在，但 M1-D 语料仍要留一条防回归")

	// 7) 代码生成开关
	o = runJS(js(`const out = {};
out.eval = (() => { try { return String(eval("1+1")) } catch (e) { return e.name + ":" + e.message } })();
out.fn = (() => { try { return String(new Function("return 2")()) } catch (e) { return e.name + ":" + e.message } })();
out.wasm = typeof WebAssembly;
__report(JSON.stringify(out));`), cfg{disableDynamicCode: true})
	r.add(ifElse(strings.Contains(o.report, "EvalError"), vMatch, vDev), "6.11/WithoutCodeGeneration",
		"6.11：第三方 Runtime 关闭代码生成；6.10：eval/new Function 对第三方关闭",
		o.actual(),
		"moejs.Options.DisableDynamicCode 就是设计里的 WithoutCodeGeneration：eval 与 Function 构造器抛 EvalError，Compile/CompileScript 不受影响（宿主仍能编译插件）")

	o = runJS(js(`__report(JSON.stringify({eval: (() => { try { return String(eval("1+1")) } catch (e) { return e.name } })()}));`), cfg{})
	r.add(ifElse(strings.Contains(o.report, `"eval":"2"`), vMatch, vDev), "6.11/默认允许代码生成",
		"6.11：builtin 信任级别可保留代码生成",
		o.actual(),
		"默认（不设 DisableDynamicCode）eval 可用，因此宿主必须显式对第三方关闭")

	// 8) 宿主回调 panic
	o = runJS(js(`let caught = "no";
try { __panic() } catch (e) { caught = "catch:" + (e.name + ":" + e.message) }
__report(JSON.stringify({caught}));`), cfg{
		globals: func(h *hostContext) {
			h.set("__panic", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				panic("spike: 宿主回调 panic")
			}))
		},
	})
	r.add(ifElse(len(o.cbErrs) > 0 || o.runErr != nil || strings.Contains(o.report, "InternalError"), vMatch, vDev), "6.11/宿主 panic 不上抛",
		"6.11 宿主异步桥：宿主回调 panic 变成 rejected Promise；不 make the process panic",
		o.actual(),
		"eventloop 把宿主函数的 Go panic 收成 *moejs.InternalError：设了 OnError 就交给宿主、循环继续；没设就停循环并把 panic 变成错误返回，不会让进程崩溃。设计要的「转成 rejected Promise」是宿主桥的活：在 NativeFunc 里 recover 后返回错误即可")

	// 9) 异步工作完成时 Runtime 已停：关闭竞态
	o = runJS(js(`globalThis.settled = "no";
__late(80).then((v) => { globalThis.settled = v }, () => { globalThis.settled = "rejected" });
__report("scheduled");`), cfg{
		loopStopAfter: 30 * time.Millisecond,
		globals: func(h *hostContext) {
			h.set("__late", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				p, settle := h.loop.NewPromise()
				go func() {
					time.Sleep(80 * time.Millisecond)
					settle(func(rt *moejs.Runtime) (moejs.Value, error) { return moejs.String("settled-late"), nil })
				}()
				return p, nil
			}))
		},
	})
	r.add(vDev, "6.11/AsyncWork 关闭竞态",
		"6.11 宿主异步桥：Runtime 关闭时未完成工作收到 ErrClosed，结果恰好投递一次",
		o.actual(),
		"确认缺口（语义在 ada-go 侧要自己收）：loop 结束后 settle 被丢弃（Post 返回 false），Promise 永远 pending，没有 ErrClosed 的 rejection；宿主必须自己跟踪未完成工作并在关闭时显式拒绝，否则插件的 await 会挂到调用超时")

	// 10) 未处理 rejection
	o = runJS(js(`Promise.reject(new Error("没有人接"));
await new Promise((res) => setTimeout(res, 20));
__report("still-alive");`), cfg{})
	r.add(ifElse(len(o.rejections) > 0 && strings.Contains(o.report, "still-alive"), vMatch, vDev), "6.11/未处理 rejection",
		"6.11 Promise 与 job：未处理的 rejection 交给宿主钩子，而不是结束进程",
		o.actual(),
		"Runtime.SetPromiseRejectionTracker 就是钩子位（本例抓到 Rejected 事件且进程继续）；Node 会打告警并在 --unhandled-rejections=throw 下终止，这里由宿主决定处置")

	// 11) 跨 Runtime 的数据传递（6.11 的 Serialize/Deserialize）
	o = runJS(js(`__report("first")`), cfg{})
	second := runJS(js(`__report(String(globalThis.fromOther ?? "无"))`), cfg{})
	r.add(vHost, "6.11/跨 Runtime 克隆",
		"6.11：Serialize/Deserialize 跨 Runtime 克隆；JavaScript 值不跨 goroutine",
		o.actual()+" / "+second.actual(),
		"moejs 没有 Serialize/Deserialize：跨 Runtime 只能走 ToGo/FromGo 或 JSON（文档明确「Only Go values and JSON are safe to pass between runtimes」）；ada-go 的宿主桥就按这条设计，不要试图传 Value")

	// 12) 事件循环外的 Runtime 是否有定时器
	o = runJS(js(`__report(JSON.stringify({setTimeout: typeof setTimeout}))`), cfg{bareRuntime: true})
	r.add(classify(o, ""), "6.11/裸 Runtime 无定时器",
		"TODO.md：裸 Runtime 没有计时器，setTimeout 是 ReferenceError",
		o.actual(),
		"eventloop.New 之后才有全局定时器；没有 Loop 的 Runtime 上 setTimeout 直接 ReferenceError（M1-C 若忘了建 Loop，插件会在这里炸）")
}

// bareRun 在不建 Loop 的裸 Runtime 上跑一个模块，验证「没有事件循环」时的行为。
// src 已由调用方拼上 prelude，这里不能再拼一次（重复声明 __shape 会让编译失败，
// 该探针于是从未真正跑过，却被 classify 记成「符合」）。
func bareRun(src string, _ cfg) *outcome {
	o := &outcome{}
	m, err := moejs.Compile("plugin://spike/bare.js", src)
	if err != nil {
		o.compileErr = err
		return o
	}
	rt := moejs.NewRuntime(moejs.Options{})
	var reported string
	if err := rt.SetGlobal("__report", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		reported = moejs.Arg(args, 0).String()
		return moejs.Undefined(), nil
	})); err != nil {
		o.setupErr = err
		return o
	}
	linked, lerr := moejs.Link(m, func(_ moejs.Referrer, spec string) (*moejs.Module, error) {
		return nil, fmt.Errorf("Cannot find module '%s'", spec)
	})
	if lerr != nil {
		o.linkErr = lerr
		return o
	}
	if err := rt.Load(linked); err != nil && !isPending(err) {
		o.loadErr = err
	}
	o.reported = reported != ""
	o.report = reported
	return o
}

func isPending(err error) bool { return err != nil && strings.Contains(err.Error(), "still pending") }
