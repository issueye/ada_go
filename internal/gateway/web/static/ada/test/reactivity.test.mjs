// reactivity 契约测试（node --test，零依赖）。
import { test } from "node:test";
import assert from "node:assert/strict";
import {
	ref, isRef, unref, reactive, isReactive, computed,
	effect, stop, effectScope, watch, watchEffect, nextTick, queueJob,
} from "../reactivity.mjs";

const tick = () => nextTick();

test("ref：读写与触发", async () => {
	const n = ref(1);
	let runs = 0;
	effect(() => { n.value; runs++; });
	assert.equal(runs, 1);
	n.value = 2;
	await tick();
	assert.equal(runs, 2);
	// 未变化的赋值不触发
	n.value = 2;
	await tick();
	assert.equal(runs, 2);
});

test("ref：effect 重跑会清理旧依赖（条件分支）", async () => {
	const a = ref(true);
	const x = ref("x");
	const y = ref("y");
	let seen = "";
	effect(() => { seen = a.value ? x.value : y.value; });
	assert.equal(seen, "x");
	x.value = "x2";
	await tick();
	assert.equal(seen, "x2");
	// 切到 y 后，x 的变更不应再触发
	a.value = false;
	await tick();
	assert.equal(seen, "y");
	x.value = "x3";
	await tick();
	assert.equal(seen, "y");
	y.value = "y2";
	await tick();
	assert.equal(seen, "y2");
});

test("reactive：属性触发、嵌套对象、ref 解包", async () => {
	const state = reactive({ count: 0, nested: { v: 1 }, r: ref(10) });
	let seen = 0;
	effect(() => { state.count; seen = state.count; });
	state.count = 5;
	await tick();
	assert.equal(seen, 5);
	// ref 解包读
	assert.equal(state.r, 10);
	let seenR = 0;
	effect(() => { seenR = state.r; });
	state.r = 20; // 对 ref 属性赋普通值 → 落到 ref.value
	await tick();
	assert.equal(seenR, 20);
	// 嵌套对象深路径触发
	let seenN = 0;
	effect(() => { seenN = state.nested.v; });
	state.nested.v = 7;
	await tick();
	assert.equal(seenN, 7);
	// 同一 raw 对象返回同一 proxy（身份稳定）
	assert.equal(reactive(state.nested), state.nested);
});

test("reactive：数组 push/length 迭代", async () => {
	const list = reactive([1, 2, 3]);
	let len = 0;
	effect(() => { len = list.length; });
	list.push(4);
	await tick();
	assert.equal(len, 4);
	// 迭代（ownKeys）也建立依赖
	let sum = 0;
	effect(() => { sum = [...list].reduce((a, b) => a + b, 0); });
	list[0] = 10;
	await tick();
	assert.equal(sum, 19);
});

test("reactive：删除键触发 ITERATE 与属性依赖", async () => {
	const s = reactive({ a: 1, b: 2 });
	let keys = [];
	effect(() => { keys = Object.keys(s); });
	delete s.b;
	await tick();
	assert.deepEqual(keys, ["a"]);
});

test("computed：惰性求值、缓存、链式失效", async () => {
	const n = ref(2);
	let getterRuns = 0;
	const double = computed(() => { getterRuns++; return n.value * 2; });
	assert.equal(double.value, 4);      // 第一次读取才计算
	assert.equal(getterRuns, 1);
	assert.equal(double.value, 4);
	assert.equal(getterRuns, 1);        // 缓存
	n.value = 3;
	await tick();
	assert.equal(getterRuns, 1);        // 惰性：标脏但不立即重算
	assert.equal(double.value, 6);
	assert.equal(getterRuns, 2);
	// 链式：computed 依赖 computed
	const quad = computed(() => double.value * 2);
	let downstream = 0;
	effect(() => { quad.value; downstream++; });
	assert.equal(downstream, 1);
	n.value = 4;
	await tick();
	assert.equal(downstream, 2);
	assert.equal(quad.value, 16);
});

test("批量调度：同一 tick 多次变更只触发一次", async () => {
	const a = ref(1), b = ref(1);
	let runs = 0;
	effect(() => { a.value; b.value; runs++; });
	a.value = 2; a.value = 3; b.value = 4; b.value = 5;
	await tick();
	assert.equal(runs, 2);
});

test("job.id 稳定排序：先创建先执行", async () => {
	const src = ref(0);
	const order = [];
	effect(() => { src.value; order.push("e1"); }); // 初始执行一次
	effect(() => { src.value; order.push("e2"); }); // 初始执行一次
	assert.deepEqual(order, ["e1", "e2"]);
	src.value = 1;
	await tick();
	assert.deepEqual(order, ["e1", "e2", "e1", "e2"]); // 触发后仍保持创建序
});

test("effectScope：stop 回收全部子 effect", async () => {
	const n = ref(1);
	let runs = 0;
	const scope = effectScope();
	scope.run(() => {
		effect(() => { n.value; runs++; });
		effect(() => { n.value; runs++; });
	});
	assert.equal(runs, 2);
	scope.stop();
	n.value = 2;
	await tick();
	assert.equal(runs, 2); // 不再触发
	// scope 停止后新建 effect 立即失效
	let extra = 0;
	scope.run(() => effect(() => { n.value; extra++; }));
	assert.equal(extra, 0);
});

test("effectScope：嵌套 stop", async () => {
	const n = ref(1);
	let runs = 0;
	const outer = effectScope();
	outer.run(() => {
		const inner = effectScope();
		inner.run(() => effect(() => { n.value; runs++; }));
	});
	outer.stop();
	n.value = 9;
	await tick();
	assert.equal(runs, 1);
});

test("stop(effect) 手动停止", async () => {
	const n = ref(1);
	let runs = 0;
	const e = effect(() => { n.value; runs++; });
	stop(e);
	n.value = 5;
	await tick();
	assert.equal(runs, 1);
});

test("watch：ref / getter / reactive，新旧值", async () => {
	const n = ref(1);
	let calls = [];
	const w1 = watch(n, (nv, ov) => calls.push([nv, ov]));
	n.value = 2;
	await tick();
	assert.deepEqual(calls, [[2, 1]]);
	w1.stop();
	n.value = 3;
	await tick();
	assert.equal(calls.length, 1); // stop 后不再回调

	const s = reactive({ v: 1 });
	let deepCalls = [];
	const w2 = watch(() => s.v, (nv, ov) => deepCalls.push(nv));
	s.v = 9;
	await tick();
	assert.deepEqual(deepCalls, [9]);
	w2.stop();
});

test("watch：deep 遍历嵌套", async () => {
	const s = reactive({ a: { b: { c: 1 } } });
	let calls = [];
	const w = watch(s, (nv) => calls.push(nv.a.b.c), { deep: true });
	s.a.b.c = 42;
	await tick();
	assert.deepEqual(calls, [42]);
	w.stop();
});

test("watchEffect：立即执行并追踪", async () => {
	const n = ref(1);
	let seen = 0;
	const w = watchEffect(() => { seen = n.value; });
	assert.equal(seen, 1);
	n.value = 2;
	await tick();
	assert.equal(seen, 2);
	w.stop();
	n.value = 3;
	await tick();
	assert.equal(seen, 2);
});

test("循环赋值不会死循环（FLUSH_CAP 兜底或稳定收敛）", async () => {
	const n = ref(1);
	const d = computed(() => n.value * 2);
	// 下游写上游的病态模式：靠 cap 报错而非挂死
	let err = null;
	const e = effect(() => { d.value; if (n.value < 3) n.value = n.value + 1; });
	try {
		for (let i = 0; i < 20; i++) await tick();
	} catch (ex) { err = ex; }
	// 收敛即通过：n 最终 >= 3
	assert.ok(n.value >= 3, `n=${n.value} err=${err && err.message}`);
	stop(e);
});

test("nextTick：无变更时直接微任务", async () => {
	let ok = false;
	await nextTick(() => { ok = true; });
	assert.ok(ok);
});

test("queueJob：裸任务也能进队列", async () => {
	let ran = 0;
	const job = { id: 999999, queued: false, run: () => ran++ };
	queueJob(job);
	await tick();
	assert.equal(ran, 1);
});

test("unref/isRef/isReactive 基础", () => {
	assert.ok(isRef(ref(1)));
	assert.ok(!isRef({}));
	assert.equal(unref(ref(5)), 5);
	assert.equal(unref(5), 5);
	assert.ok(isReactive(reactive({})));
	assert.ok(!isReactive({}));
});
