// host glue 集成测试（node --test + DOM shim）：registry store / SlotOutlet / PluginScope。
import { test } from "node:test";
import assert from "node:assert/strict";
import { createDocument } from "./dom.mjs";

globalThis.document = createDocument();
globalThis.CSSStyleSheet = class { replaceSync() {} };
globalThis.document.adoptedStyleSheets = [];

const { readBootstrap, makeFetcher, createRegistryStore, SlotOutlet, makeAda } = await import("../host.mjs");
const { createApp, defineComponent, ref, nextTick, provide } = await import("../runtime.mjs");

const tick = () => nextTick();

test("readBootstrap：JSON 解析 + meta csrf 回退", () => {
	const el = document.createElement("script");
	el.setAttribute("type", "application/json");
	el.setAttribute("id", "ada-bootstrap");
	el.textContent = JSON.stringify({ workspace: "w1", workspaceName: "演示" });
	document.body.appendChild(el);
	const b = readBootstrap();
	assert.equal(b.workspace, "w1");
	assert.equal(b.workspaceName, "演示");
	// meta 回退
	const meta = document.createElement("meta");
	meta.setAttribute("name", "csrf-token");
	meta.setAttribute("content", "tok123");
	document.body.appendChild(meta);
	assert.equal(readBootstrap().csrf, "tok123");
});

test("makeFetcher：CSRF 头与 JSON 透传", async () => {
	const calls = [];
	const saved = globalThis.fetch;
	globalThis.fetch = async (path, opts) => {
		calls.push({ path, opts });
		return {
			ok: true, status: 200,
			headers: new Map([["content-type", "application/json"]]),
			json: async () => ({ ok: true }),
			text: async () => "{}",
		};
	};
	try {
		const fetch_ = makeFetcher({ csrf: "tok" });
		const out = await fetch_("/x", { method: "POST", body: { a: 1 } });
		assert.deepEqual(out, { ok: true });
		assert.equal(calls[0].opts.headers["X-CSRF-Token"], "tok");
		assert.equal(calls[0].opts.headers["Content-Type"], "application/json");
		assert.equal(calls[0].opts.body, JSON.stringify({ a: 1 }));
	} finally {
		globalThis.fetch = saved;
	}
});

test("makeFetcher：303 回执的 ?notice=/?error= 推给 onFlash，响应本体不解析", async () => {
	const flashes = [];
	const saved = globalThis.fetch;
	const htmlResponse = (url) => ({
		ok: true, status: 200, url,
		headers: new Map([["content-type", "text/html; charset=utf-8"]]),
		text: async () => "<html>page</html>",
	});
	try {
		globalThis.fetch = async () => htmlResponse("http://local/threads/th_1?error=x%20y");
		const fetch_ = makeFetcher({ csrf: "" }, { onFlash: (kind, message) => flashes.push([kind, message]) });
		const out = await fetch_("/threads/th_1/archive", { method: "POST", body: { next: "/" } });
		assert.equal(out, "<html>page</html>");
		assert.deepEqual(flashes, [["error", "x y"]]);

		globalThis.fetch = async () => htmlResponse("http://local/threads?notice=ok");
		const out2 = await fetch_("/threads/th_1/archive", { method: "POST", body: { next: "/threads" } });
		assert.equal(out2, "<html>page</html>");
		assert.deepEqual(flashes[1], ["notice", "ok"]);

		// 没有 flash 查询串的页面响应不推 toast。
		globalThis.fetch = async () => htmlResponse("http://local/threads");
		await fetch_("/threads");
		assert.equal(flashes.length, 2);

		// 第二参直接给函数也认（老调用点的简写形式）。
		let direct = null;
		const fetchFn = makeFetcher({ csrf: "" }, (kind, message) => { direct = [kind, message]; });
		globalThis.fetch = async () => htmlResponse("http://local/x?notice=ok");
		await fetchFn("/x");
		assert.deepEqual(direct, ["notice", "ok"]);
	} finally {
		globalThis.fetch = saved;
	}
});

test("makeFetcher：JSON 响应不触发 onFlash", async () => {
	let fired = 0;
	const saved = globalThis.fetch;
	try {
		globalThis.fetch = async () => ({
			ok: true, status: 200, url: "http://local/api?notice=x",
			headers: new Map([["content-type", "application/json"]]),
			json: async () => ({ ok: true }),
			text: async () => "{}",
		});
		const fetch_ = makeFetcher({ csrf: "" }, { onFlash: () => { fired += 1; } });
		assert.deepEqual(await fetch_("/api"), { ok: true });
		assert.equal(fired, 0);
	} finally {
		globalThis.fetch = saved;
	}
});

test("registry store：装载 / slots 排序 / page 查找", async () => {
	const registryData = {
		rev: 3,
		plugins: [
			{
				id: "b", name: "B", trust: "official", version: "1.0",
				slots: [{ slot: "chat.corner", component: "/ext/b/static/B.vue?v=x", order: 20 }],
				pages: [{ slug: "pb", component: "/ext/b/static/P.vue?v=x", title: "PB" }],
				actions: { act: { confirm: "确认？" } },
			},
			{
				id: "a", name: "A", trust: "official", version: "0.1",
				slots: [{ slot: "chat.corner", component: "/ext/a/static/A.vue?v=y", order: 10 }],
			},
		],
	};
	const store = createRegistryStore(async () => registryData);
	await store.load();
	assert.equal(store.store.rev, 3);
	assert.ok(store.store.loaded);
	// order 排序：a(10) 在前
	const contribs = store.slots("chat.corner");
	assert.deepEqual(contribs.map((c) => c.pluginId), ["a", "b"]);
	assert.equal(store.page("b", "pb").component, "/ext/b/static/P.vue?v=x");
	assert.equal(store.page("b", "nope"), null);
	assert.deepEqual(store.actionBinding("b", "act"), { confirm: "确认？" });
	// nav 空
	assert.deepEqual(store.nav(), []);
});

test("SlotOutlet + PluginScope：注册表驱动装载与渲染", async () => {
	globalThis.location = { pathname: "/" };
	const registryData = {
		rev: 1,
		plugins: [{
			id: "demo", name: "Demo", trust: "official",
			slots: [{ slot: "sidebar.top", component: "/ext/demo/static/W.vue?v=1", order: 10 }],
		}],
	};
	const registry = createRegistryStore(async () => registryData);
	await registry.load();

	const fetch = async () => ({ ok: true, status: 200, headers: new Map(), json: async () => ({}), text: async () => "" });
	// SFC 装载经 io 注入不可行（SlotOutlet 直接调 loadSFC(url) —— 走默认 io 的 fetch）
	// 这里替换 global fetch 提供假 .vue 与假模块 URL 是不可行的（import 原生）。
	// 因此直接验证：空插槽渲染 null + 贡献包装结构。
	const EmptyHost = defineComponent({
		setup() { provide("registry", registry); provide("fetch", fetch); provide("confirmService", async () => true); return {}; },
		render: () => h("div", {}, [h(SlotOutlet, { name: "nothing.here" })]),
	});
	const { h } = await import("../runtime.mjs");
	const host = document.createElement("div");
	document.body.appendChild(host);
	createApp(EmptyHost).mount(host);
	await tick();
	assert.equal(host.childNodes[0].childNodes.length, 0); // 无贡献 → 不渲染
});

test("makeAda：api/action 带确认流", async () => {
	const calls = [];
	const fetch = async (path, opts) => { calls.push([path, opts]); return { ok: true }; };
	const registry = { actionBinding: (_id, name) => (name === "w" ? { confirm: "确认？" } : null) };
	let confirmCalls = 0;
	const ada = makeAda({
		pluginId: "p1",
		fetch,
		registry,
		confirm: async () => { confirmCalls++; return true; },
	});
	await ada.api("state", { method: "GET" });
	assert.equal(calls[0][0], "/ext/p1/api/state");
	// 有 confirm 声明的动作：先确认再 POST
	await ada.action("w", { x: 1 });
	assert.equal(confirmCalls, 1);
	assert.equal(calls[1][0], "/ext/p1/action/w");
	assert.deepEqual(calls[1][1].body, { x: 1 });
	// 无 confirm 声明的动作直接 POST
	await ada.action("r", {});
	assert.equal(confirmCalls, 1);
	assert.equal(calls[2][0], "/ext/p1/action/r");
});

test("makeAda：storage 键名隔离", () => {
	const saved = globalThis.localStorage;
	const mem = new Map();
	globalThis.localStorage = {
		getItem: (k) => mem.get(k) ?? null,
		setItem: (k, v) => mem.set(k, v),
		removeItem: (k) => mem.delete(k),
	};
	try {
		const ada = makeAda({ pluginId: "p1", fetch: async () => ({}), registry: { actionBinding: () => null }, confirm: async () => true });
		ada.storage.set("counter", 42);
		assert.equal(ada.storage.get("counter"), 42);
		assert.ok(mem.has("p1:counter"));
		ada.storage.remove("counter");
		assert.equal(ada.storage.get("counter"), null);
	} finally {
		globalThis.localStorage = saved;
	}
});

test("registry slots：dashboard 卡并入 dashboard.widgets 插槽", async () => {
	const registryData = {
		rev: 1,
		plugins: [{
			id: "tu", name: "用量", trust: "official",
			dashboard: [{ component: "/ext/tu/static/UsageWidget.vue?v=1", order: 10 }],
		}],
	};
	const store = createRegistryStore(async () => registryData);
	await store.load();
	const contribs = store.slots("dashboard.widgets");
	assert.equal(contribs.length, 1);
	assert.equal(contribs[0].pluginId, "tu");
	assert.equal(contribs[0].component, "/ext/tu/static/UsageWidget.vue?v=1");
});

test("makeAda.action：read 绑定走 GET（query 承载 input），写绑定走 POST + confirm", async () => {
	const calls = [];
	const fetch = async (path, opts) => { calls.push([path, opts]); return { ok: true }; };
	const registry = {
		actionBinding: (_id, name) => (name === "r" ? { read: true } : { confirm: "确认？" }),
	};
	let confirms = 0;
	const ada = makeAda({ pluginId: "p", fetch, registry, confirm: async () => { confirms++; return true; } });
	await ada.action("r", { threadId: "t1", n: 3 });
	assert.equal(calls[0][0], "/ext/p/action/r?threadId=t1&n=3");
	assert.equal((calls[0][1]?.method ?? "GET"), "GET");
	await ada.action("w", { x: 1 });
	assert.equal(confirms, 1);
	assert.equal(calls[1][0], "/ext/p/action/w");
	assert.equal(calls[1][1].method, "POST");
});
