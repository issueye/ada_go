package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/MoeForge/moejs/webapi"
)

func init() {
	register("timers", "定时器：unref/ref/上限/setImmediate/timers-promises（E-2/E-8）", runTimers)
}

func runTimers(r *recorder) {
	// 1) 顺序：microtask 先于 timer，setImmediate 与 0ms 定时器的相对顺序
	o := runJS(js(`const log = [];
setTimeout(() => log.push("timeout0"), 0);
setImmediate(() => log.push("immediate"));
queueMicrotask(() => log.push("microtask"));
Promise.resolve().then(() => log.push("promise"));
await new Promise((res) => setTimeout(res, 40));
__report(JSON.stringify({order: log}));`), cfg{})
	r.add(classify(o, ""), "6.11/定时器与循环-顺序",
		"6.11：microtask 先于 timer；6.12.2：setImmediate 以 0ms 定时器实现，与 Node 的 check 阶段顺序不完全相同",
		o.actual(),
		"上游是三阶段循环（timers→tasks→immediates），setImmediate 与 setTimeout(0) 的顺序和 Node 不同；偏差表要把「同一 tick 内 immediate 晚于 0ms timer」写清")

	// 2) unref / ref / hasRef
	o = runJS(js(`const t = setTimeout(() => __noteFired(), 30);
t.unref();
__report(JSON.stringify({hasRef: t.hasRef(), id: Number(t), type: typeof t}));`),
		cfg{globals: func(h *hostContext) {
			h.set("__noteFired", moejs.NativeFunc(func(_ *moejs.Realm, _ moejs.Value, _ []moejs.Value) (moejs.Value, error) {
				h.out.cbErrs = append(h.out.cbErrs, "unref 定时器仍然回调了")
				return moejs.Undefined(), nil
			}))
		}})
	r.add(ifElse(o.reported && strings.Contains(o.report, `"hasRef":false`) && !strings.Contains(strings.Join(o.cbErrs, " "), "仍然回调"), vMatch, vDev), "E-2/unref",
		"6.11：unref/ref；6.11.1 E-2：unref 后不阻止 Runtime 结束",
		o.actual(),
		"上游 Timeout 对象有 ref/unref/hasRef/refresh/close，且 Symbol.toPrimitive 给出数字 id；unref 的定时器不阻止 Run 结束（本例 Run 直接结束，回调没跑）")

	// 3) 每 Runtime 定时器上限（E-2）
	o = runJS(js(`let n = 0, err = "none";
try {
  for (let i = 0; i < 600; i++) { setTimeout(() => { n++ }, 1) }
} catch (e) { err = e.code || e.name }
await new Promise((res) => setTimeout(res, 120));
__report(JSON.stringify({fired: n, err}));`), cfg{})
	r.add(ifElse(strings.Contains(o.report, `"fired":600`), vGap, vDev), "E-2/定时器数量上限",
		"6.12.2、6.12.4：每 Runtime 256 个未完成定时器，超出抛 ERR_TIMER_LIMIT",
		o.actual(),
		"引擎没有任何定时器数量上限：600 个全部排入并全部触发，ERR_TIMER_LIMIT 不存在（6.12.4 的这一行目前只能由宿主自己数）")

	// 4) 毫秒截断与 32 位上限
	o = runJS(js(`const seen = [];
setTimeout(() => seen.push("nan->1"), NaN);
setTimeout(() => seen.push("big->1"), 2147483648);
setTimeout(() => seen.push("neg->1"), -5);
setTimeout(() => seen.push("1.7 truncated"), 1.7);
await new Promise((res) => setTimeout(res, 30));
__report(JSON.stringify(seen));`), cfg{})
	r.add(ifElse(strings.Contains(o.report, "4") || strings.Contains(o.report, "nan->1"), vMatch, vDev), "6.11/毫秒截断与 32 位上限",
		"6.11：毫秒截断与 32 位上限",
		o.actual(),
		"NaN/<1/>2147483647 一律夹到 1ms，小数截断；与 Node 行为一致（Node 对 >2^31 会额外告警，上游不告警）")

	// 5) node:timers 与 node:timers/promises 是否存在
	o = runJS(js(`import { setTimeout as st } from "node:timers/promises";
__report(JSON.stringify({type: typeof st}));`), cfg{})
	r.add(ifElse(o.linkErr != nil, vGap, vMatch), "E-8/node:timers 与 timers/promises",
		"6.12.2：timers、timers/promises（promise 形式支持 signal）",
		o.actual(),
		"上游没有 timers 模块：定时函数只是 eventloop 装的全局；timers/promises 完全不存在，signal 取消自然也没有")

	// 6) setTimeout 的 signal 选项被忽略（危险：静默不生效）
	o = runJS(js(`const ac = new AbortController();
ac.abort(new Error("轮次取消"));
let fired = "no";
setTimeout(() => { fired = "YES-仍然触发" }, 20, {signal: ac.signal});
await new Promise((res) => setTimeout(res, 60));
__report(JSON.stringify({fired}));`), cfg{web: &webapi.Options{}})
	r.add(ifElse(strings.Contains(o.report, "YES"), vGap, vMatch), "E-8/signal 取消静默失效",
		"6.11 取消：宿主 ctx.signal 与脚本 signal 双向可取消；6.12.2：timers/promises 支持 signal",
		o.actual(),
		"确认缺口：AbortSignal 传进 setTimeout 的 options 被当成普通附加参数忽略，定时器照旧触发——插件按 Node 习惯写的取消代码在这里是静默失效，比报错更糟")

	// 7) 清除与 id 形状
	o = runJS(js(`const log = [];
const a = setTimeout(() => log.push("a"), 10);
const b = setTimeout(() => log.push("b"), 10);
clearTimeout(a);
clearTimeout(Number(b));
clearTimeout(" nonsense ");
clearImmediate(7);
await new Promise((res) => setTimeout(res, 40));
__report(JSON.stringify({log, idType: typeof a}));`), cfg{})
	r.add(ifElse(strings.Contains(o.report, `"log":[]`), vMatch, vDev), "6.11/清除定时器",
		"6.11：清除；6.12.2：返回数字 id",
		o.actual(),
		"clearTimeout 接受 Timeout 对象、它的数字 id、甚至字符串 id（Node 也忽略非法值）；返回的是 Timeout 对象而不是纯数字（JSON.stringify 得到 {}），这是与 Node 的真实偏差")

	// 8) 关闭时是否取消所有定时器
	o = runJS(js(`setTimeout(() => { globalThis.__hit = "fired" }, 200);
__report("scheduled");`), cfg{loopStopAfter: 40 * time.Millisecond})
	after := afterRunEval(`String(globalThis.__hit ?? "not-fired")`)
	r.add(ifElse(after == "fired", vDev, vMatch), "6.11/关闭时全部取消",
		"6.11：Runtime 关闭时全部取消定时器",
		o.actual()+("\n         Stop 之后再 Run 一次并读全局=")+fmt.Sprint(after),
		"Run/Stop 结束只是「本轮没有 ref 的工作」：未到期的 ref'd 定时器留在队列里，下一次 Run（或复用同一 Runtime）会把它们补跑。宿主重建脏 Runtime 必须换新 Runtime，或在关闭前 Terminate（它会丢弃全部队列并中断）——否则「关闭时全部取消」不成立")
}

// resumePending 复用上一次的 Runtime，看未到期定时器是否还在。
func resumePending() any {
	if lastLoop == nil {
		return "无（探针没建过 Loop）"
	}
	s, err := moejs.CompileScript("resume", `String(typeof globalThis.__hit !== "undefined" ? "fired" : "not-fired")`)
	if err != nil {
		return "编译失败: " + err.Error()
	}
	var got any = "无"
	if err := lastLoop.Run(func(rt *moejs.Runtime) error {
		v, rerr := rt.RunScript(s)
		if rerr != nil {
			got = "runErr: " + rerr.Error()
			return nil
		}
		got, _ = rt.ToGo(v)
		return nil
	}); err != nil {
		return "runErr: " + err.Error()
	}
	return got
}
