// 响应式内核 —— 零依赖，语义对齐 @vue/reactivity 的公开子集（见开发计划 F1）。
//
// 设计要点：
//   - 依赖收集：Proxy get 时把当前 activeSub 注册进 dep；effect 重跑前先清旧依赖。
//   - 调度：trigger 默认进批量队列，微任务统一 flush，按创建序（job.id）稳定执行。
//   - effectScope：收集子 effect/子 scope，stop() 一刀切回收 —— 插件卸载的基石。
//   - ref 解包：reactive 对象的 ref 属性读取时解包并双向登记（ref 变更同时触发宿主属性）。
//   - 本模块不触碰 window/document，可在无 DOM 环境测试。

const ITERATE = Symbol("iterate");

let uid = 0;
let activeSub = null;
const subStack = [];

// ---------------------------------------------------------------------------
// 依赖图
// ---------------------------------------------------------------------------

const targetMap = new WeakMap(); // raw target -> Map<prop|ITERATE, Set<sub>>

function trackDep(target, key) {
	if (activeSub === null || activeSub.active === false) return;
	let depsMap = targetMap.get(target);
	if (depsMap === undefined) targetMap.set(target, (depsMap = new Map()));
	let dep = depsMap.get(key);
	if (dep === undefined) depsMap.set(key, (dep = new Set()));
	if (!dep.has(activeSub)) {
		dep.add(activeSub);
		activeSub.deps.push(dep);
	}
}

function triggerDep(target, key) {
	const depsMap = targetMap.get(target);
	if (depsMap === undefined) return;
	const dep = depsMap.get(key);
	if (dep === undefined) return;
	for (const sub of Array.from(dep)) schedule(sub);
}

// effect 重跑前清掉上一轮收集的依赖（条件分支导致的依赖漂移）。
function cleanupDeps(sub) {
	for (const dep of sub.deps) dep.delete(sub);
	sub.deps.length = 0;
}

function withTracking(sub, fn) {
	subStack.push(activeSub);
	activeSub = sub;
	try {
		return fn();
	} finally {
		activeSub = subStack.pop();
	}
}

// ---------------------------------------------------------------------------
// 批量调度队列
// ---------------------------------------------------------------------------

const queue = [];
let flushDepth = 0;
const FLUSH_CAP = 10000; // 单轮 flush 任务上限：超过视为病态级联，宁可报错不可死循环

function schedule(sub) {
	queueJob(sub);
}

export function queueJob(job) {
	if (job.queued === true) return;
	job.queued = true;
	queue.push(job);
	if (flushDepth === 0) {
		Promise.resolve().then(flushJobs);
	}
}

function flushJobs() {
	flushDepth++;
	try {
		let ran = 0;
		while (queue.length > 0) {
			if (++ran > FLUSH_CAP) {
				queue.length = 0;
				throw new Error("ada-reactivity: 单轮 flush 任务数超过上限，疑似响应式循环");
			}
			// 按 id 升序稳定执行；执行中新入队的任务继续在本轮处理。
			queue.sort((a, b) => a.id - b.id);
			const job = queue.shift();
			job.queued = false;
			if (job.scheduler !== undefined) job.scheduler(job);
			else job.run();
		}
	} finally {
		flushDepth--;
	}
}

export function nextTick(fn) {
	const p = flushDepth === 0 ? Promise.resolve().then(flushJobs) : Promise.resolve();
	return p.then(fn);
}

// ---------------------------------------------------------------------------
// effect
// ---------------------------------------------------------------------------

export function effect(fn, opts = {}) {
	const sub = {
		id: uid++,
		deps: [],
		queued: false,
		active: true,
		fn,
		last: undefined,
		scheduler: opts.scheduler,
		run() {
			if (sub.active === false) return;
			cleanupDeps(sub);
			sub.last = withTracking(sub, fn);
			return sub.last;
		},
	};
	const scope = opts.scope !== undefined ? opts.scope : currentScope;
	if (scope !== null) scope.add(sub);
	if (opts.lazy !== true) sub.run();
	return sub;
}

export function stop(sub) {
	if (sub.active === false) return;
	sub.active = false;
	cleanupDeps(sub);
}

// ---------------------------------------------------------------------------
// effectScope
// ---------------------------------------------------------------------------

export function effectScope() {
	const parent = currentScope;
	const scope = {
		active: true,
		_subs: [],
		_scopes: [],
		add(sub) {
			if (scope.active === false) {
				// scope 已停止后再创建的 effect 立即处于停止态（不追踪不触发）。
				sub.active = false;
				return;
			}
			scope._subs.push(sub);
		},
		run(fn) {
			const prev = currentScope;
			currentScope = scope;
			try {
				return fn();
			} finally {
				currentScope = prev;
			}
		},
		stop() {
			if (scope.active === false) return;
			scope.active = false;
			for (const sub of scope._subs) stop(sub);
			scope._subs.length = 0;
			for (const child of scope._scopes) child.stop();
			scope._scopes.length = 0;
		},
	};
	if (parent !== null) parent._scopes.push(scope);
	return scope;
}

let currentScope = null;
export function activeEffectScope() {
	return currentScope;
}

// ---------------------------------------------------------------------------
// ref
// ---------------------------------------------------------------------------

export function ref(value) {
	const dep = new Set();
	const r = {
		__isRef: true,
		_v: value,
		_host: undefined, // 若作为 reactive 属性存在，回填宿主 target/prop
		_key: undefined,
		get value() {
			if (activeSub !== null && activeSub.active && !dep.has(activeSub)) {
				dep.add(activeSub);
				activeSub.deps.push(dep);
			}
			return r._v;
		},
		set value(nv) {
			if (Object.is(nv, r._v)) return;
			r._v = nv;
			for (const sub of Array.from(dep)) schedule(sub);
			if (r._host !== undefined) triggerDep(r._host, r._key);
		},
	};
	return r;
}

export function isRef(v) {
	return v !== null && typeof v === "object" && v.__isRef === true;
}

export function unref(v) {
	return isRef(v) ? v.value : v;
}

// ---------------------------------------------------------------------------
// reactive
// ---------------------------------------------------------------------------

const reactiveMap = new WeakMap(); // raw -> proxy（身份稳定：同对象同 proxy）

export function reactive(target) {
	if (target === null || typeof target !== "object") return target;
	if (isRef(target)) return target;
	if (target.__raw !== undefined) return target; // 已是本模块产出的 proxy，防重复包装
	const existing = reactiveMap.get(target);
	if (existing !== undefined) return existing;
	const proxy = new Proxy(target, handlers);
	reactiveMap.set(target, proxy);
	return proxy;
}

export function isReactive(v) {
	return v !== null && typeof v === "object" && v.__raw !== undefined;
}

const handlers = {
	get(target, prop, receiver) {
		if (prop === "__raw") return target;
		const v = Reflect.get(target, prop, receiver);
		// ref 解包：订阅 ref 自身并登记宿主，读取解包值。
		if (isRef(v)) {
			if (v._host === undefined) {
				v._host = target;
				v._key = prop;
			}
			if (activeSub !== null && activeSub.active) v.value; // 触发 ref 的 getter 完成收集
			return v.value;
		}
		trackDep(target, prop);
		if (v !== null && typeof v === "object") return reactive(v);
		return v;
	},
	set(target, prop, value, receiver) {
		const old = Reflect.get(target, prop, receiver);
		// 对 ref 属性赋普通值：落到 ref.value（ref setter 负责两级触发）。
		if (isRef(old) && !isRef(value)) {
			old.value = value;
			return true;
		}
		const ok = Reflect.set(target, prop, value, receiver);
		if (!ok) return false;
		if (Object.is(old, value)) return true;
		triggerDep(target, prop);
		if (old === undefined && value !== undefined) {
			// 新增键：触发迭代依赖；数组越界写会把 target.length 经 define 路径
			// 悄悄改掉（绕过本 set trap），因此数组新增索引必须显式补触发 length，
			// 否则 push 之类的操作不会让跟踪 length 的 effect 重跑。
			triggerDep(target, ITERATE);
			if (Array.isArray(target)) triggerDep(target, "length");
		}
		return true;
	},
	deleteProperty(target, prop) {
		const had = Reflect.has(target, prop);
		const ok = Reflect.deleteProperty(target, prop);
		if (ok && had) {
			triggerDep(target, prop);
			triggerDep(target, ITERATE);
		}
		return ok;
	},
	has(target, prop) {
		trackDep(target, prop);
		return Reflect.has(target, prop);
	},
	ownKeys(target) {
		trackDep(target, ITERATE);
		return Reflect.ownKeys(target);
	},
};

// ---------------------------------------------------------------------------
// computed
// ---------------------------------------------------------------------------

export function computed(getter) {
	let value;
	let dirty = true;
	const dep = new Set(); // 下游订阅者（读 c.value 的 effect）

	const sub = {
		id: uid++,
		deps: [],
		queued: false,
		active: true,
		// 上游变更：标脏 + 级联调度下游（下游重读 .value 时才真正重算 —— 惰性）。
		scheduler() {
			dirty = true;
			for (const s of Array.from(dep)) schedule(s);
		},
		run() {
			cleanupDeps(sub);
			value = withTracking(sub, getter);
			dirty = false;
		},
	};

	if (currentScope !== null) currentScope.add(sub);

	return {
		__isRef: true,
		get value() {
			if (activeSub !== null && activeSub.active && !dep.has(activeSub)) {
				dep.add(activeSub);
				activeSub.deps.push(dep);
			}
			if (dirty) sub.run();
			return value;
		},
	};
}

// ---------------------------------------------------------------------------
// watch / watchEffect
// ---------------------------------------------------------------------------

// watchEffect(fn, { scope? }) → { stop() }：立即执行并追踪，依赖变更后批量重跑。
export function watchEffect(fn, opts = {}) {
	const sub = effect((onCleanup) => fn(onCleanup), { scope: opts.scope });
	return { stop: () => stop(sub) };
}

// watch(source, cb, { deep?, scope? }) → { stop() }
// source: ref | reactive 对象 | getter 函数；cb(newVal, oldVal)。
// reactive 源强制触发（forceTrigger）：deep 遍历后返回值身份不变，
// 若仅按 Object.is 比较将永远不触发，因此 reactive 源每次 trigger 都回调。
export function watch(source, cb, opts = {}) {
	const { getter, forceTrigger } = makeGetter(source, opts.deep === true);
	let oldValue;
	let inited = false;

	const sub = effect(getter, { lazy: true, scope: opts.scope });
	const job = {
		id: uid++,
		queued: false,
		run() {
			const newValue = sub.run();
			if (inited && (forceTrigger || !Object.is(newValue, oldValue))) {
				const o = oldValue;
				oldValue = newValue;
				cb(newValue, o);
				return;
			}
			oldValue = newValue;
			inited = true;
		},
	};
	sub.scheduler = () => queueJob(job);

	sub.run(); // 初始收集依赖（不触发回调）
	oldValue = sub.last;
	inited = true;

	return { stop: () => stop(sub) };
}

function makeGetter(source, deep) {
	if (typeof source === "function") return { getter: source, forceTrigger: false };
	if (isRef(source)) return { getter: () => source.value, forceTrigger: false };
	if (source !== null && typeof source === "object") {
		const r = reactive(source);
		if (deep) {
			return {
				getter: () => {
					traverse(r, new Set());
					return r;
				},
				forceTrigger: true,
			};
		}
		return { getter: () => r, forceTrigger: true };
	}
	throw new Error("ada-reactivity: watch 源必须是 ref/reactive/getter");
}

function traverse(obj, seen) {
	if (obj === null || typeof obj !== "object") return;
	if (seen.has(obj)) return;
	seen.add(obj);
	for (const key of Reflect.ownKeys(obj)) {
		if (key === "__raw" || key === "__isRef") continue;
		let v;
		try {
			v = obj[key];
		} catch {
			continue;
		}
		if (isRef(v)) v = v.value;
		traverse(v, seen);
	}
}
