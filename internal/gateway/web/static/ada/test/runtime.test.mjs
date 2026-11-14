// runtime 契约测试（node --test + 最小 DOM shim）。
// 每个用例挂到独立 host 节点，避免用例间 DOM 串台。
import { test } from "node:test";
import assert from "node:assert/strict";
import { createDocument } from "./dom.mjs";

globalThis.document = createDocument();
globalThis.CSSStyleSheet = class {
	replaceSync() {}
};
globalThis.document.adoptedStyleSheets = [];

const { h, createApp, defineComponent, defineAsyncComponent, makeSfcComponent, ref, computed, reactive, nextTick, provide, inject, onMounted, onUnmounted, onErrorCaptured, RenderError } = await import("../runtime.mjs");
const { compileTemplate } = await import("../compiler.mjs");

const tick = () => nextTick();

// 挂载辅助：独立 host + 可选根级 provide。
function mountTest(def, provides) {
	const doc = globalThis.document;
	const host = doc.createElement("div");
	doc.body.appendChild(host);
	const app = createApp(def);
	if (provides !== undefined) {
		for (const [k, v] of Object.entries(provides)) app.provide(k, v);
	}
	app.mount(host);
	return { app, host };
}

test("h()：白名单校验", () => {
	assert.throws(() => h("my-evil"), /不在白名单/);
	assert.throws(() => h("iframe"), /不在白名单/);
	assert.throws(() => h("div", { onclick: () => {} }), /"on:事件名"/);
	assert.throws(() => h("div", { style: "color:red" }), /不接受字符串/);
	assert.throws(() => h("div", { innerHTML: "<b>" }), /被拒绝/);
	assert.throws(() => h("a", { href: "javascript:alert(1)" }), /危险协议/);
	assert.throws(() => h("input", { type: "file" }), /不在白名单/);
	assert.ok(h("div", { class: "x" }, "hi"));
	assert.ok(h("ui-badge", { tone: "ok" }, "hi")); // ui-* 组件 props 自由
});

test("挂载与静态渲染", () => {
	const { host } = mountTest(defineComponent({
		render: () => h("div", { class: "shell" }, [h("p", {}, "你好"), h("br")]),
	}));
	const div = host.childNodes[0];
	assert.equal(div.tagName, "DIV");
	assert.equal(div.attributes.get("class"), "shell");
	assert.equal(div.childNodes[0].tagName, "P");
	assert.equal(div.childNodes[0].childNodes[0].nodeValue, "你好");
	assert.equal(div.childNodes[1].tagName, "BR");
});

test("响应式更新：ref 变更触发重渲染与 patch", async () => {
	const count = ref(0);
	const { host } = mountTest(defineComponent({
		setup() { return { count }; },
		render: (s) => h("div", {}, `计数 ${s.count}`),
	}));
	assert.equal(host.childNodes[0].childNodes[0].nodeValue, "计数 0");
	count.value = 3;
	await tick();
	assert.equal(host.childNodes[0].childNodes[0].nodeValue, "计数 3");
});

test("列表 patch：v-for 增删项", async () => {
	const items = ref(["a", "b", "c"]);
	const { plan } = compileTemplate("<ul><li v-for='x in items' :key='x'>{{ x }}</li></ul>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ items }), plan }));
	const ul = host.childNodes[0];
	assert.equal(ul.childNodes.length, 3);
	assert.equal(ul.childNodes[1].childNodes[0].nodeValue, "b");
	items.value = ["a", "d", "c", "e"];
	await tick();
	assert.equal(ul.childNodes.length, 4);
	assert.equal(ul.childNodes[1].childNodes[0].nodeValue, "d");
	assert.equal(ul.childNodes[3].childNodes[0].nodeValue, "e");
	items.value = ["a"];
	await tick();
	assert.equal(ul.childNodes.length, 1);
});

test("v-if 切换分支", async () => {
	const open = ref(true);
	const { plan } = compileTemplate("<p v-if='open'>开</p><p v-else>关</p>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ open }), plan }));
	assert.equal(host.childNodes[0].childNodes[0].nodeValue, "开");
	open.value = false;
	await tick();
	assert.equal(host.childNodes[0].childNodes[0].nodeValue, "关");
});

test("事件绑定与内联赋值（v-model）", async () => {
	const form = reactive({ name: "" });
	const { plan } = compileTemplate("<input v-model='form.name'/><span>{{ form.name }}</span>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ form }), plan }));
	const input = host.childNodes[0];
	const span = host.childNodes[1];
	assert.equal(input.tagName, "INPUT");
	input.dispatch("input", { target: { value: "ada" } });
	await tick();
	assert.equal(form.name, "ada");
	assert.equal(span.childNodes[0].nodeValue, "ada");
});

test("v-model 在 v-for 内写外层状态", async () => {
	const rows = ref([{ t: "a" }, { t: "b" }]);
	const { plan } = compileTemplate("<ul><li v-for='(r, i) in rows' :key='i'><input v-model='r.t'/></li></ul>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ rows }), plan }));
	const inputs = host.childNodes[0].childNodes;
	inputs[1].childNodes[0].dispatch("input", { target: { value: "B2" } });
	await tick();
	assert.equal(rows.value[1].t, "B2");
});

test("事件修饰符：.prevent", () => {
	const { plan } = compileTemplate("<button @click.prevent='go'>go</button>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ go() {} }), plan }));
	const prevented = host.childNodes[0].dispatch("click", {});
	assert.ok(prevented);
});

test("事件表达式求值后返回函数则再调用", () => {
	const { plan } = compileTemplate("<button @click='go'>go</button>");
	let called = 0;
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ go() { called++; } }), plan }));
	host.childNodes[0].dispatch("click", {});
	assert.equal(called, 1);
});

test("子组件：setup 里 computed 派生 props", () => {
	const Child = defineComponent({
		props: ["label"],
		setup(props) { return { label: computed(() => (props.label ?? "") + "!") }; },
		render: (s) => h("strong", {}, s.label),
	});
	const Parent = defineComponent({
		setup() { return { name: ref("ada") }; },
		render: (s) => h("div", {}, [h(Child, { label: s.name })]),
	});
	const { host } = mountTest(Parent);
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "ada!");
});

test("父组件响应式状态经 props 传给子组件", async () => {
	const store = ref("v1");
	const Child = defineComponent({
		props: ["v"],
		render: (s) => h("span", {}, String(s.v)),
	});
	const Parent = defineComponent({
		setup() { return { store }; },
		render: (s) => h("div", {}, [h(Child, { v: s.store })]),
	});
	const { host } = mountTest(Parent);
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "v1");
	store.value = "v2";
	await tick();
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "v2");
});

test("provide / inject 跨层", () => {
	const Grand = defineComponent({
		setup() { const v = inject("svc"); return { v }; },
		render: (s) => h("em", {}, s.v),
	});
	const Mid = defineComponent({ render: () => h("div", {}, [h(Grand)]) });
	const { host } = mountTest(Mid, { svc: "ada-svc" });
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "ada-svc");
});

test("inject 未提供且无默认值 → 报错被边界捕获", () => {
	const seen = [];
	const Bad = defineComponent({
		setup() { inject("missing"); return {}; },
		render: () => h("p", {}, "x"),
	});
	const Parent = defineComponent({
		setup() {
			onErrorCaptured((err) => { seen.push(err.message); return false; });
			return {};
		},
		render: () => h("div", {}, [h(Bad), h("p", {}, "after")]),
	});
	const { host } = mountTest(Parent);
	assert.equal(seen.length, 1);
	// Bad 挂载失败不产出 DOM；兄弟节点不受影响。
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "after");
});

test("生命周期：onMounted / onUnmounted", async () => {
	const events = [];
	const show = ref(true);
	const Child = defineComponent({
		setup() {
			onMounted(() => events.push("mounted"));
			onUnmounted(() => events.push("unmounted"));
			return {};
		},
		render: () => h("span", {}, "x"),
	});
	const Parent = defineComponent({
		setup() { return { show }; },
		render: (s) => (s.show ? h("span", {}, [h(Child)]) : h("span", {}, "off")),
	});
	const { host } = mountTest(Parent);
	await tick();
	assert.deepEqual(events, ["mounted"]);
	show.value = false;
	await tick();
	assert.deepEqual(events, ["mounted", "unmounted"]);
	assert.equal(host.childNodes[0].childNodes[0].nodeValue, "off");
});

test("异步组件：loading → ok", async () => {
	let resolveLoad;
	const Inner = defineComponent({ render: () => h("strong", {}, "inner") });
	const Async = defineAsyncComponent(() => new Promise((res) => { resolveLoad = res; }));
	const { host } = mountTest(defineComponent({ render: () => h("div", {}, [h(Async)]) }));
	assert.equal(host.childNodes[0].childNodes[0].attributes.get("class"), "ada-slot-loading");
	resolveLoad(Inner);
	await tick();
	assert.equal(host.childNodes[0].childNodes[0].tagName, "STRONG");
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "inner");
});

test("异步组件：装载失败 → data-ada-error", async () => {
	let rejectLoad;
	const Async = defineAsyncComponent(() => new Promise((_, rej) => { rejectLoad = rej; }));
	const { host } = mountTest(defineComponent({ render: () => h("div", {}, [h(Async)]) }));
	rejectLoad(new Error("fetch fail"));
	await tick();
	assert.equal(host.childNodes[0].childNodes[0].attributes.get("data-ada-error"), "1");
});

test("样式经 CSSOM 注入（:style 对象绑定）", async () => {
	const color = ref("red");
	const { plan } = compileTemplate("<p :style='{ color: c, margin: \"4px\" }'>x</p>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ c: color }), plan }));
	const p = host.childNodes[0];
	assert.equal(p.style.getPropertyValue("color"), "red");
	assert.equal(p.style.getPropertyValue("margin"), "4px");
	color.value = "blue";
	await tick();
	assert.equal(p.style.getPropertyValue("color"), "blue");
});

test("class 静态与动态合并", () => {
	const { plan } = compileTemplate("<p class='base' :class=\"on ? 'on' : ''\">x</p>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ on: ref(true) }), plan }));
	assert.equal(host.childNodes[0].attributes.get("class"), "base on");
});

test("scoped 样式随卸载摘除", async () => {
	const show = ref(true);
	const styled = makeSfcComponent({ setupFn: () => ({}), plan: compileTemplate("<p>x</p>").plan, styles: "p{color:red}" });
	const Parent = defineComponent({
		setup() { return { show }; },
		render: (s) => (s.show ? h(styled) : h("span", {}, "off")),
	});
	const { host } = mountTest(Parent);
	await tick();
	assert.equal(globalThis.document.adoptedStyleSheets.length, 1);
	show.value = false;
	await tick();
	assert.equal(globalThis.document.adoptedStyleSheets.length, 0);
	assert.equal(host.childNodes[0].childNodes[0].nodeValue, "off");
});

test("ui-* 组件：未注册报错、注册后渲染并透传 props/kids", () => {
	const Badge = defineComponent({
		props: ["tone"],
		render: (s, ctx) => h("span", { class: `tone-${s.tone ?? ""}` }, ctx.kids),
	});
	const { host } = mountTest(defineComponent({
		render: () => h("div", {}, [h("ui-badge", { tone: "ok" }, "hello")]),
	}));
	// 未注册 → 渲染错误，挂载容器被标记（错误发生时 div 也未产出 DOM）
	assert.equal(host.getAttribute("data-ada-error"), "1");
	// 注册后正常渲染
	const app2 = createApp(defineComponent({
		render: () => h("div", {}, [h("ui-badge", { tone: "ok" }, "hello")]),
	}));
	app2.component("ui-badge", Badge);
	const host3 = globalThis.document.createElement("div");
	globalThis.document.body.appendChild(host3);
	app2.mount(host3);
	const span = host3.childNodes[0].childNodes[0];
	assert.equal(span.tagName, "SPAN");
	assert.equal(span.attributes.get("class"), "tone-ok");
	assert.equal(span.childNodes[0].nodeValue, "hello");
});

test("RenderError：输出上限保护可触发", () => {
	// 40KB 插值超过 32KB 单字符串上限：渲染错误按错误边界语义吞掉，
	// 表现为容器标记 data-ada-error 且不产出超限文本。
	const big = "x".repeat(40 * 1024);
	const { plan } = compileTemplate("<p>{{ v }}</p>");
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ v: ref(big) }), plan }));
	assert.equal(host.getAttribute("data-ada-error"), "1");
	assert.equal(host.childNodes.length, 0);
});

test("异步组件就位：新子树插回占位符原位（不 append 到容器末尾）", async () => {
	// 复现 2026-11-11 走查布局错位：先渲染骨架块 + 异步占位，装载完成后
	// 异步内容若 append 到末尾，DOM 顺序与布局全错。
	let resolveLoad;
	const { defineAsyncComponent } = await import("../runtime.mjs");
	const Inner = defineComponent({ render: () => h("p", { class: "async-inner" }, "inner") });
	const Async = defineAsyncComponent(() => new Promise((res) => { resolveLoad = res; }));
	const { host } = mountTest(defineComponent({
		render: () => h("div", { class: "box" }, [
			h("p", { class: "first" }, "first"),
			h(Async),
			h("p", { class: "last" }, "last"),
		]),
	}));
	const box = host.childNodes[0];
	assert.deepEqual(box.childNodes.map((c) => c.className), ["first", "ada-slot-loading", "last"]);
	resolveLoad(Inner);
	await tick();
	assert.deepEqual(box.childNodes.map((c) => c.className), ["first", "async-inner", "last"]);
});

test("同组件换 key：卸载重建（setup 重新执行拿新参数）", async () => {
	// 路由切换依赖这个语义：/threads ↔ /threads/:id 共用同一页面组件。
	const mounted = [];
	const Page = defineComponent({
		props: ["id"],
		setup(p) { mounted.push(p.id ?? "~"); return { id: p.id ?? "~" }; },
		render: (s) => h("p", {}, String(s.id)),
	});
	const route = ref({ id: "a" });
	const Parent = defineComponent({
		setup() { return { route }; },
		render: (s) => h("div", {}, [h(Page, { key: s.route.id, id: s.route.id })]),
	});
	const { host } = mountTest(Parent);
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "a");
	route.value = { id: "b" };
	await tick();
	assert.equal(host.childNodes[0].childNodes[0].childNodes[0].nodeValue, "b");
	assert.deepEqual(mounted, ["a", "b"]); // setup 执行了两次，说明组件被重建
});

test("异步组件：包装组件把自己的 props 透传给内层组件", async () => {
	const seen = [];
	const Inner = defineComponent({
		props: ["params"],
		setup(p) { seen.push(p.params?.id); return {}; },
		render: () => h("p", {}, "inner"),
	});
	const Async = defineAsyncComponent(async () => Inner);
	const { host } = mountTest(defineComponent({ render: () => h("div", {}, [h(Async, { params: { id: "x" } })]) }));
	await tick();
	assert.deepEqual(seen, ["x"]);
	assert.equal(host.childNodes[0].childNodes[0].textContent, "inner");
});

test("v-for + v-if 分支：异步替换分组后界面刷新（嵌套数组扁平化回归）", async () => {
	// 复现 2026-11-11 走查：v-for 的 kids 里 v-if 返回数组未拍平 → patch 误配，
	// 异步数据到达后界面不刷新（无关重渲染却正常）。
	const groups = reactive([{ path: "a", loaded: true, threads: [] }, { path: "b", loaded: false, threads: [] }]);
	const { plan } = compileTemplate('<div v-for="(g, gi) in groups" :key="g.path"><div v-if="gi === 0" class="cur"><p>{{ g.threads.length }}</p></div><div v-else class="oth"><p v-if="!g.loaded">load</p><p v-else>{{ g.threads.length }}</p></div></div>');
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ groups }), plan }));
	assert.equal(host.childNodes[1].childNodes[0].childNodes[0].childNodes[0].nodeValue, "load");
	groups.splice(1, 1, { path: "b", loaded: true, threads: [{ id: "t" }] });
	await tick();
	assert.equal(host.childNodes[1].childNodes[0].childNodes[0].childNodes[0].nodeValue, "1");
});

test("v-for 异步替换：挂载时为空、先填充再等长索引替换，DOM 必须刷新", async () => {
	// 复现 2026-11-11 侧栏缺陷的真正根因（依赖收集过期）：
	//   sidebar 的 groups 初始为空 → 首轮渲染的 v-for 只登记了数组 length；
	//   load() 用 splice 填充（length 0→N，触发）→ 展开其它工作区时
	//   onToggle 用 groups.splice(i,1,{...}) 做**等长**索引替换（length 不变），
	//   依赖表里没有该索引的订阅者，effect 不重跑 —— 界面停在「展开以加载会话…」。
	// 对照实验：同一页面里改搜索词能刷新，因为那个 ref 是首轮就登记过的依赖。
	const groups = reactive([]);
	const { plan } = compileTemplate('\
<details v-for="(g, gi) in groups" :key="g.path" class="ws">\
<summary class="sum">{{ g.name }}</summary>\
<div v-if="!g.loaded" class="loading">展开以加载会话…</div>\
<div v-else-if="g.threads.length === 0" class="empty">还没有会话</div>\
<div v-else class="rows"><p v-for="t in g.threads" :key="t.id" class="row">{{ t.title }}</p></div>\
</details>');
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ groups }), plan }));
	assert.equal(host.childNodes.length, 0);
	// 异步取数第一跳：填充（length 0→2，这一跳原本就是好的）
	groups.splice(0, 0, { path: "/ws", name: "当前", loaded: true, threads: [] }, { path: "/other", name: "其它", loaded: false, threads: [] });
	await tick();
	assert.equal(host.childNodes.length, 2);
	assert.equal(host.childNodes[1].childNodes[1].textContent, "展开以加载会话…");
	// 异步取数第二跳：不可变替换 + 等长（这一步曾经不刷新）
	groups.splice(1, 1, { path: "/other", name: "其它", loaded: true, threads: [{ id: "t1", title: "会话一" }] });
	await tick();
	assert.equal(host.childNodes[1].childNodes.length, 2);
	assert.equal(host.childNodes[1].childNodes[1].attributes.get("class"), "rows");
	assert.deepEqual(host.childNodes[1].childNodes[1].childNodes.map((p) => p.textContent), ["会话一"]);
	// 再一跳：换成空列表（分支从 rows 切到 empty）
	groups.splice(1, 1, { path: "/other", name: "其它", loaded: true, threads: [] });
	await tick();
	assert.equal(host.childNodes[1].childNodes[1].textContent, "还没有会话");
});

test("v-for 异步替换：组件重新渲染时必须重新收集依赖（runUpdate 不得绕过 effect）", async () => {
	// 依赖重收集的直接断言：首轮渲染 arr 为空；填充后派生状态（布尔/字符串）
	// 在每轮渲染里被重新读取，后续只改这些派生值（length 与索引身份都不变）
	// —— 旧实现下 patch 会被调度但读不到变化，DOM 停在旧值。
	const rows = reactive([]);
	const marker = ref("A");
	const { plan } = compileTemplate('<ul><li v-for="(r, i) in rows" :key="r.id">{{ r.id }}-{{ marker }}</li></ul>');
	const { host } = mountTest(makeSfcComponent({ setupFn: () => ({ rows, marker }), plan }));
	rows.splice(0, 0, { id: "r1" }, { id: "r2" });
	await tick();
	const ul = host.childNodes[0];
	assert.deepEqual(ul.childNodes.map((li) => li.textContent), ["r1-A", "r2-A"]);
	// 只做等长索引替换（length 不变）：首轮登记的 length 依赖不会再报变化
	rows.splice(1, 1, { id: "r9" });
	await tick();
	assert.deepEqual(ul.childNodes.map((li) => li.textContent), ["r1-A", "r9-A"]);
});

test("确认对话框：Esc 关闭（render 返回 null 的组件必须卸载旧子树）", async () => {
	// 复现 2026-11-11 走查：ConfirmDialog 是 ShellApp 根部的常驻 vnode，关闭态
	// render 返回 null。旧 patchVNode 只在 n1 === null 时特判，n2 === null 会走到
	// `typeof n2.tag` 抛 TypeError（被错误边界接住）→ 面板留在 DOM 上、Esc 看似无效。
	// 这里同时验证：keydown 监听确实注册在 document 上、Esc 走 resolve(false) 链。
	const state = reactive({ open: false, text: "删除会话？", resolve: null });
	const Dialog = defineComponent({
		name: "ConfirmDialogLike",
		setup() {
			const onKey = (event) => {
				if (event.key !== "Escape" || !state.open) return;
				state.open = false;
				state.resolve?.(false);
				state.resolve = null;
			};
			document.addEventListener("keydown", onKey);
			onUnmounted(() => document.removeEventListener("keydown", onKey));
			return { state };
		},
		render(s) {
			if (!s.state.open) return null;
			return h("div", { class: "ada-modal" }, [
				h("p", { class: "ada-modal__message" }, s.state.text),
				h("button", { class: "ada-btn", type: "button", "on:click": () => { s.state.open = false; s.state.resolve?.(true); s.state.resolve = null; } }, "确认"),
			]);
		},
	});
	const { app, host } = mountTest(defineComponent({
		// 与 ShellApp 同形：常驻浮层组件挂在根子节点末尾（view 之后）。
		render: () => h("div", { class: "shell" }, [h("p", { class: "layout" }, "布局"), h("p", { class: "after" }, "尾部"), h(Dialog)]),
	}));
	const countModal = () => {
		let n = 0;
		const walk = (node) => {
			if (node.nodeType !== 1 && node.nodeType !== 3) return;
			if (node.attributes?.get("class") === "ada-modal") n += 1;
			for (const c of node.childNodes) walk(c);
		};
		walk(host);
		return n;
	};
	const errors = [];
	app.config.errorHandler = (err) => errors.push(err?.message ?? String(err));

	const result = new Promise((resolve) => { state.resolve = resolve; });
	state.open = true;
	await tick();
	assert.equal(countModal(), 1);
	assert.equal(host.childNodes[0].childNodes[2].attributes.get("class"), "ada-modal");

	// shim 的 dispatch 不冒泡，这里直接在 document 上派发（与真实 Esc 的
	// document 级监听路径一致：浏览器里 keydown 从焦点元素冒泡到 document）。
	document.dispatch("keydown", { key: "Escape" });
	await tick();
	assert.equal(state.open, false);
	assert.equal(await result, false);
	await tick();
	assert.equal(countModal(), 0);
	// 关闭后兄弟节点顺序不变、无错误边界触发
	assert.deepEqual(host.childNodes[0].childNodes.map((c) => c.attributes.get("class")), ["layout", "after"]);
	assert.deepEqual(errors, []);

	// 再次打开 → 关闭（点击「确认」走 resolve(true) 分支）→ DOM 同样复原
	state.open = true;
	await tick();
	assert.equal(countModal(), 1);
	host.childNodes[0].childNodes[2].childNodes[1].dispatch("click", {});
	await tick();
	assert.equal(countModal(), 0);
	assert.deepEqual(host.childNodes[0].childNodes.map((c) => c.attributes.get("class")), ["layout", "after"]);
});
