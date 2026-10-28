// engine_test.go 断言那些「JS 侧看不见」的运行时事实：中断、内存、关闭竞态、
// 宿主回调 panic。它们同样是设计 6.11 的验收项，但语料协议（返回值/错误 code/
// 事件顺序/副作用）表达不了 —— 脚本被中断时连 __report 都跑不到，所以放在 Go 侧。
//
// 这些断言全部是「当前真实行为」，包括行为不符合设计的那部分：那条差异写进
// deviations.md，不在测试里粉饰。
package nodecompat

import (
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moejs"
)

// 设计 6.11 资源边界 + 6.13：context 中断在解释器内轮询且脚本不可 catch。
func TestNodeCompatInterruptNotCatchable(t *testing.T) {
	st := runCase(t, `let caught = "no";
try { let x = 0; while (true) { x++ } } catch (e) { caught = "catch:" + String(e && e.message) }
__report(JSON.stringify({ returns: { caught }, codes: {}, order: [], effects: {} }));`,
		runtimeSpec{InterruptAfterMS: 120}, nil)

	if st.runErr == nil {
		t.Fatalf("死循环没有被中断（%s）", st.summary())
	}
	if !strings.Contains(st.runErr.Error(), "interrupted") {
		t.Errorf("中断的错误形状不对，期望 interrupted，实测 %q", st.runErr.Error())
	}
	if st.reported {
		t.Errorf("脚本 catch 到了中断并上报了（%s）—— 设计 6.11/6.13 要求取消不可 catch", st.report)
	}
}

// 设计 6.11 资源边界：内存上限计入脚本持有的对象/字符串/TypedArray/Buffer。
// 「怎么被拒」三种形态实测不同，逐个登记（interrupt = 不可 catch 的中断；
// catchable = 脚本能 catch 到 RangeError）：
//
//	字符串 / Buffer  → interrupt（与 Node 不同：Node 是可 catch 的 RangeError）
//	TypedArray       → catchable（D-7 说的「TypedArray 有偏差」就是这一条：走的是
//	                   分配上限检查而不是中断）
//	共同点：分配一律不可能成功 —— 这条是真断言。
func TestNodeCompatMemoryLimit(t *testing.T) {
	cases := []struct {
		name   string
		deny   string // interrupt | catchable
		buffer bool
		src    string
	}{
		{"字符串", "interrupt", false, `let s = "";
for (let i = 0; i < 24; i++) { s += "A".repeat(1024 * 1024) }
globalThis.keep = s;
__report(JSON.stringify({ returns: { len: s.length }, codes: {}, order: [], effects: {} }));`},
		{"Buffer", "interrupt", true, `let caught = "no";
try { globalThis.keep = Buffer.alloc(32 * 1024 * 1024) } catch (e) { caught = "catch:" + e.name }
__report(JSON.stringify({ returns: { caught, len: globalThis.keep ? globalThis.keep.length : 0 }, codes: {}, order: [], effects: {} }));`},
		{"TypedArray", "catchable", false, `let caught = "no";
try { globalThis.hold = [new Uint8Array(32 * 1024 * 1024)] } catch (e) { caught = "catch:" + e.name }
__report(JSON.stringify({ returns: { caught, held: globalThis.hold ? globalThis.hold[0].length : 0 }, codes: {}, order: [], effects: {} }));`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			st := runCase(t, c.src, runtimeSpec{MemLimit: 8 << 20, Buffer: c.buffer}, nil)
			switch c.deny {
			case "interrupt":
				if st.runErr == nil || !strings.Contains(st.runErr.Error(), "memory limit") {
					t.Fatalf("%s: 内存上限没有以中断终止执行（%s）", c.name, st.summary())
				}
				if st.reported {
					t.Errorf("%s: 中断被脚本 catch 住并上报了（%s）—— 设计 6.11 要求脚本不可 catch", c.name, st.report)
				}
			case "catchable":
				if !st.reported {
					t.Fatalf("%s: 期望脚本 catch 到 RangeError 并上报（%s）", c.name, st.summary())
				}
				if !strings.Contains(st.report, "catch:RangeError") {
					t.Errorf("%s: 期望 catch:RangeError，实测 %s", c.name, st.report)
				}
			}
			// 共同点：分配一律不成功（要么没跑成，要么长度 0）。
			if st.reported && (strings.Contains(st.report, `"held":33554432`) || strings.Contains(st.report, `"len":33554432`)) {
				t.Errorf("%s: 32MB 分配在 8MB 上限的 Runtime 里居然成功了：%s", c.name, st.report)
			}
		})
	}
}

// 设计 6.11 资源边界：栈与调用深度有独立上限。上游给了上限但没给可调参数，
// 所以这里只断言「有上限、可被脚本捕获为 RangeError」，不断言具体层数。
func TestNodeCompatCallDepthLimit(t *testing.T) {
	st := runCase(t, `let depth = 0;
let shape = "no-throw";
try { const f = (n) => { depth = n; return f(n + 1) }; f(0) } catch (e) { shape = e.name + ":" + String(e.message).slice(0, 20) }
__report(JSON.stringify({ returns: { shape, deep: depth > 100 }, codes: {}, order: [], effects: {} }));`,
		runtimeSpec{}, nil)
	if !st.reported {
		t.Fatalf("深递归探针没有上报（%s）", st.summary())
	}
	if !strings.Contains(st.report, "RangeError") || !strings.Contains(st.report, `"deep":true`) {
		t.Errorf("深递归应当以 RangeError 结束且确实递归了很深，实测 %s", st.report)
	}
}

// 设计 6.11 宿主异步桥：Runtime 关闭时未完成工作要收到 ErrClosed，结果恰好投递一次。
// 实测两件事，都是缺口（详见 deviations.md D-47）：
//   - 一轮 Run 结束（含 Stop）后，未完成的 settle 不会收到任何拒绝：脚本侧永远 pending，
//     插件的 await 会挂到调用超时；
//   - 但结果也没有被丢弃 —— 同一个 Loop 再 Run 一次，上一轮的迟到结果就投递进来了，
//     于是「上一轮的状态」漏进下一轮。宿主绝不能复用脏 Runtime，也不能指望 ErrClosed。
func TestNodeCompatAsyncWorkCloseRace(t *testing.T) {
	spec := runtimeSpec{
		StopAfterMS: 30,
		Setup: func(h *hostProbe) {
			h.setNative("__late", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				p, settle := h.loop.NewPromise()
				go func() {
					time.Sleep(120 * time.Millisecond)
					settle(func(*moejs.Runtime) (moejs.Value, error) { return moejs.String("settled-late"), nil })
				}()
				return p, nil
			}))
		},
	}
	st := runCase(t, `globalThis.settled = "pending";
__late(120).then((v) => { globalThis.settled = v }, () => { globalThis.settled = "rejected" });
__report(JSON.stringify({ returns: { scheduled: true }, codes: {}, order: [], effects: {} }));`,
		spec, nil)
	if !st.reported {
		t.Fatalf("调度阶段就没跑起来（%s）", st.summary())
	}
	if st.runErr != nil {
		t.Fatalf("Run 不该报错（%s）", st.summary())
	}
	time.Sleep(200 * time.Millisecond) // 后台工作一定已经尝试过投递
	if got := st.peek(t, `String(globalThis.settled)`); got != "pending" {
		t.Errorf("期望关闭后没有 ErrClosed 拒绝、Promise 永远 pending，实测 settled=%s —— 上游若已实现，请同步撤销 deviations.md 的对应行", got)
	}
	if got := st.afterRunEval(t, `String(globalThis.settled)`); got != "settled-late" {
		t.Errorf("期望「再 Run 一次就把上一轮的迟到结果投递进来」（状态跨轮泄漏），实测 settled=%s", got)
	}
}

// 设计 6.11：Runtime 关闭时全部取消定时器。实测 Run/Stop 结束只是「本轮没有 ref 的工作」，
// 未到期的 ref'd 定时器留在队列里，下一次 Run 会补跑 —— 宿主重建脏 Runtime 必须换新的
// 或先 Terminate。差异见 deviations.md。
func TestNodeCompatTimersSurviveRunEnd(t *testing.T) {
	st := runCase(t, `globalThis.hit = "not-fired";
setTimeout(() => { globalThis.hit = "fired" }, 200);
__report(JSON.stringify({ returns: { scheduled: true }, codes: {}, order: [], effects: {} }));`,
		runtimeSpec{StopAfterMS: 40}, nil)
	if !st.reported {
		t.Fatalf("定时器探针没跑起来（%s）", st.summary())
	}
	if got := st.afterRunEval(t, `String(globalThis.hit)`); got != "fired" {
		t.Errorf("登记事实是「未到期定时器会在下一次 Run 补跑」（实测 fired）；本次得到 %s —— 上游行为变了要同步改偏差表", got)
	}
	// 反过来：Terminate 之后必须真的不再跑。
	st2 := runCase(t, `globalThis.hit2 = "not-fired";
setTimeout(() => { globalThis.hit2 = "fired" }, 200);
__report(JSON.stringify({ returns: { scheduled: true }, codes: {}, order: [], effects: {} }));`,
		runtimeSpec{StopAfterMS: 20}, nil)
	if st2.loop == nil {
		t.Fatal("没有 Loop，无法验证 Terminate")
	}
	st2.loop.Terminate()
	time.Sleep(250 * time.Millisecond)
	if got := st2.afterRunEval(t, `String(globalThis.hit2 ?? "gone")`); got == "fired" {
		t.Errorf("Terminate 之后定时器仍补跑（%s）—— 与上游文档不符，说明有回归", got)
	}
}

// 设计 6.11 宿主异步桥：宿主回调 panic 不能带崩进程。
// 实测 eventloop 把 panic 收成 *moejs.InternalError 并从 Run 返回 —— 进程活着，
// 但「变成 rejected Promise」是宿主桥的活（在 NativeFunc 里 recover 后返回错误）。
func TestNodeCompatHostPanic(t *testing.T) {
	spec := runtimeSpec{Setup: func(h *hostProbe) {
		h.setNative("__panic", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
			panic("nodecompat: 宿主回调 panic")
		}))
	}}
	st := runCase(t, `let caught = "no";
try { __panic() } catch (e) { caught = "catch:" + String(e && e.name) }
__report(JSON.stringify({ returns: { caught }, codes: {}, order: [], effects: {} }));`,
		spec, nil)
	text := st.summary()
	if !strings.Contains(text, "internal error") {
		t.Errorf("宿主 panic 没有收成 *moejs.InternalError 交给宿主（%s）", text)
	}
	// 关键事实：panic 不会带崩进程（测试还活着），但脚本侧也 catch 不到 ——
	// 「变成 rejected Promise」是宿主桥的活（在 NativeFunc 里 recover 后返回错误）。
	if st.reported && strings.Contains(st.report, `"caught":"no"`) {
		t.Errorf("期望脚本没有 catch 到宿主 panic（当前实测确实 catch 不到）；实测 %s", st.report)
	}
}

// 设计 6.11 取消：AbortSignal.timeout / AbortSignal.any / reason / throwIfAborted
// 由 webapi 提供。这里补一条 Go 侧也看得见的断言：监听器清理不残留。
func TestNodeCompatAbortShape(t *testing.T) {
	st := runCase(t, `const ac = new AbortController();
ac.abort(new Error("自定义 reason"));
const out = { aborted: ac.signal.aborted, reason: String(ac.signal.reason.message) };
try { ac.signal.throwIfAborted(); out.threw = "no" } catch (e) { out.threw = e.name }
out.timeout = typeof AbortSignal.timeout;
out.any = typeof AbortSignal.any;
const a = new AbortController(), b = new AbortController();
const comp = AbortSignal.any([a.signal, b.signal]);
b.abort(new Error("第二个"));
out.composed = String(comp.aborted) + "/" + String(comp.reason.message);
let ran = 0;
const hs = [];
for (let i = 0; i < 10; i++) { const f = () => { ran++ }; hs.push(f); ac.signal.addEventListener("abort", f) }
ac.signal.dispatchEvent(new Event("abort"));
for (const f of hs) ac.signal.removeEventListener("abort", f)
ac.signal.dispatchEvent(new Event("abort"));
out.afterRemove = ran;
__report(JSON.stringify({ returns: out, codes: {}, order: [], effects: {} }));`,
		runtimeSpec{Web: true, Buffer: true}, nil)
	if !st.reported {
		t.Fatalf("AbortSignal 探针没跑起来（%s）", st.summary())
	}
	for _, want := range []string{`"aborted":true`, `"threw":"Error"`, `"timeout":"function"`, `"any":"function"`, `"afterRemove":10`} {
		if !strings.Contains(st.report, want) {
			t.Errorf("取消面缺少 %s，实测 %s", want, st.report)
		}
	}
}
