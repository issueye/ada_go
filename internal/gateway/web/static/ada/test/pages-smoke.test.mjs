// pages-smoke 门禁测试 —— 基座页面（static/ada/pages/*.vue）的运行期回归防线。
//
// 编译门禁（plugins-sfc.test.mjs）只能保证模板语法与白名单；这里补两道更深的关卡：
//  1. 作用域静态核对：模板表达式引用的标识符必须 ⊆ setup 返回键 + 白名单全局 +
//     v-for 循环变量。运行时 evaluate 对未声明标识符直接抛错（错误边界接住后
//     页面被标记 data-ada-error），这里把漏配提前拦下。已修过的真实问题：
//     null/true/false/undefined 是编译器眼里的"标识符"（模板里禁用布尔/空字面量）、
//     顶栏绑定漏 return。
//  2. 挂载冒烟：每个页面在 DOM shim 上真实挂载一遍（mock fetch/registry/router/
//     bootstrap）。已修过的真实问题：实例方法调用 data.recent.slice(...) 在
//     evalNode 里丢接收者（编译器冻结，改在 setup 里预计算）。
//
// ⚠️ 本文件会给 globalThis 挂 window/document 等桩：node --test 每个文件独立
// 进程，不会污染其它测试文件。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, writeFileSync, unlinkSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { parseSFC } from "../sfc.mjs";
import { compileTemplate, evaluate } from "../compiler.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const PAGES = resolve(HERE, "../pages");

// ---------------------------------------------------------------------------
// 1. 作用域静态核对
// ---------------------------------------------------------------------------

const GLOBALS = new Set(["Math", "JSON", "Date", "Number", "String", "Boolean", "Array", "Object", "parseInt", "parseFloat", "isNaN", "$event"]);

function collectExprs(node, out) {
	if (!node || typeof node !== "object") return;
	if (Array.isArray(node)) { node.forEach((n) => collectExprs(n, out)); return; }
	if (node.k === "dtext") out.push(node.e);
	if (node.k === "for") { out.push(node.src); if (node.key) out.push(node.key); }
	if (node.k === "if") {
		for (const b of node.branches) { out.push(b.e); collectExprs(b.kids, out); }
		collectExprs(node.fallback ?? [], out);
	}
	if (node.k === "el") {
		for (const p of node.props) if (p.e) out.push(p.e);
		collectExprs(node.kids, out);
	}
}

function collectIds(ast, out) {
	if (!ast || typeof ast !== "object") return;
	if (Array.isArray(ast)) { ast.forEach((a) => collectIds(a, out)); return; }
	switch (ast.t) {
		case "id": out.add(ast.n); return;
		case "mem": case "idx": collectIds(ast.o, out); if (ast.e) collectIds(ast.e, out); return;
		case "bin": collectIds(ast.l, out); collectIds(ast.r, out); return;
		case "un": collectIds(ast.a, out); return;
		case "cond": collectIds(ast.test, out); collectIds(ast.a, out); collectIds(ast.b, out); return;
		case "arr": collectIds(ast.items, out); return;
		case "obj": for (const p of ast.props) collectIds(p.v, out); return;
		case "call": collectIds(ast.c, out); collectIds(ast.args, out); return;
		case "assign": collectIds(ast.target, out); collectIds(ast.v, out); return;
	}
}

// 用不会抛错的 mock 作用域把每条表达式求值一遍（赋值/调用也走一遍求值路径）。
function tryEval(expr, scope) {
	try { evaluate(expr, scope, { event: {} }); } catch { /* mock 不完整，不影响标识符收集 */ }
}

// 从页面 JS 抓 setup 的 return 顶层键（shorthand 与 name: 都认）。
// setup 的 return 在函数体一层缩进（\treturn {），优先匹配它，避免命中
// 嵌套 helper 里的 return {。
function returnKeys(jsPath) {
	const src = readFileSync(jsPath, "utf8");
	const m = /^\treturn \{/m.exec(src) ?? /return\s*\{/.exec(src);
	if (!m) return new Set();
	let depth = 0;
	let i = m.index + "return".length;
	const start = i;
	for (; i < src.length; i++) {
		if (src[i] === "{") depth++;
		else if (src[i] === "}") { depth--; if (depth === 0) break; }
	}
	const body = src.slice(start + 1, i).replace(/^\s*\{/, "").replace(/\}\s*$/, "");
	const keys = new Set();
	let retDepth = 0;
	let cur = "";
	const flush = () => {
		const t = cur.trim();
		cur = "";
		if (t === "") return;
		const short = /^([A-Za-z_$][\w$]*)$/.exec(t);
		if (short) { keys.add(short[1]); return; }
		const named = /^([A-Za-z_$][\w$]*)\s*:/.exec(t);
		if (named) keys.add(named[1]);
	};
	for (const ch of body) {
		if (ch === "(" || ch === "[" || ch === "{") { retDepth++; cur += ch; continue; }
		if (ch === ")" || ch === "]" || ch === "}") { retDepth--; cur += ch; continue; }
		if (ch === "," && retDepth === 0) { flush(); continue; }
		cur += ch;
	}
	flush();
	return keys;
}

test("基座页面：模板表达式只引用 setup 返回键 / 全局 / v-for 变量", () => {
	const problems = [];
	let count = 0;
	for (const f of readdirSync(PAGES).filter((n) => n.endsWith(".vue")).sort()) {
		count += 1;
		const vuePath = join(PAGES, f);
		const allowed = returnKeys(vuePath.replace(/\.vue$/, ".js"));
		const blocks = parseSFC(readFileSync(vuePath, "utf8"));
		const { plan } = compileTemplate(blocks.template);
		// v-for 的 item/index 在子作用域里，合法引用。
		const loopVars = new Set();
		(function walkFor(node) {
			if (!node || typeof node !== "object") return;
			if (Array.isArray(node)) { node.forEach(walkFor); return; }
			if (node.k === "for") { loopVars.add(node.item); if (node.index) loopVars.add(node.index); }
			if (node.kids) walkFor(node.kids);
			if (node.branches) for (const b of node.branches) walkFor(b.kids);
			if (node.fallback) walkFor(node.fallback);
		})(plan);
		const exprs = [];
		collectExprs(plan, exprs);
		// mock 作用域：has 恒真，get 返回再套一层 mock（mem/call 链不炸）。
		const mockScope = new Proxy({}, {
			has: () => true,
			get: (_t, k) => {
				if (typeof k === "symbol" || k === Symbol.unscopables) return undefined;
				return new Proxy(function () {}, { has: () => true, get: () => undefined, apply: () => undefined });
			},
		});
		const ids = new Set();
		for (const e of exprs) {
			collectIds(e, ids);
			tryEval(e, mockScope);
		}
		for (const id of ids) {
			if (GLOBALS.has(id) || loopVars.has(id)) continue;
			if (!allowed.has(id)) problems.push(`${f}: 模板引用 "${id}" 不在 setup 返回键里`);
		}
	}
	assert.ok(count >= 7, `基座页面数量异常：只找到 ${count} 个 .vue`);
	assert.deepEqual(problems, []);
});

// ---------------------------------------------------------------------------
// 2. 挂载冒烟（真实 mount，mock 数据面）
// ---------------------------------------------------------------------------

const storageData = new Map();
globalThis.window = globalThis;
globalThis.localStorage = {
	getItem: (k) => (storageData.has(k) ? storageData.get(k) : null),
	setItem: (k, v) => storageData.set(k, String(v)),
	removeItem: (k) => storageData.delete(k),
};
globalThis.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
globalThis.addEventListener = () => {};
globalThis.removeEventListener = () => {};
globalThis.location = { pathname: "/threads/th_1", href: "http://local/threads/th_1" };
globalThis.history = { pushState() {}, replaceState() {}, back() {} };
globalThis.EventSource = class {
	addEventListener() {}
	close() {}
};
globalThis.scrollTo = () => {};
if (typeof globalThis.navigator === "undefined" || !globalThis.navigator.clipboard) {
	Object.defineProperty(globalThis, "navigator", { value: { clipboard: { writeText: async () => {} } }, configurable: true });
}
const { createDocument, DomNode } = await import("./dom.mjs");
globalThis.document = createDocument();
document.querySelector = () => null; // shim 的 querySelector 只认 [x=y]；页面按可空处理

const fixtures = {
	"/partials/dashboard.json": { stats: [{ label: "会话", value: "2", hint: "本地会话总数", tone: "brand", numeric: true }], recent: [{ id: "th_1", title: "排查构建失败", workspace: "/tmp/ws", running: true, updatedLabel: "刚刚" }] },
	"/partials/threads.json": { workspace: "/tmp/ws", workspaceName: "ws", threads: [{ id: "th_1", title: "排查构建失败", updatedAt: new Date().toISOString(), running: true, archived: false }], hasMore: false, nextCursor: "" },
	"/api/workspaces": { default: "/tmp/ws", workspaces: [{ name: "ws", path: "/tmp/ws" }] },
	"/api/fs/list": { path: "", parent: "", roots: [{ label: "C:", path: "C:\\", kind: "disk" }], workspaces: [], entries: [{ name: "demo", path: "C:\\demo", href: "" }], selectable: false, truncated: false, omitted: 0 },
	"/partials/extensions.json": { plugins: [{ id: "demo", name: "演示", version: "1.0.0", status: "ready", statusLabel: "启用", statusTone: "success", enabled: true, frontend: true, backend: true, tools: 2, hooks: 0, skills: 1, readme: "# 演示", files: [{ path: "plugin.json", size: 12, sizeLabel: "12 B", depthClass: "" }], description: "", source: "bundled", error: "" }], count: 1, csrf: "t" },
	"/partials/model.json": { models: [{ value: "p|m", providerId: "p", modelId: "m", label: "模型 M", shortLabel: "M", current: true, thinkingLevels: ["low", "high"] }], currentLabel: "M", currentValue: "p|m", currentLevels: ["low"], currentLevel: "low" },
	"/partials/transcript/th_1": { threadId: "th_1", runStatus: "terminal", rows: [
		{ key: "e1", kind: "user", parts: [{ type: "text", text: "第一问" }, { type: "image", mime: "image/png", data: "aGk=" }] },
		{ key: "tool:call_1", kind: "tool", tool: { callId: "call_1", name: "read_file", status: "ok", statusLabel: "", durationMs: 12, args: "{}", argsHint: 'path="."', output: "body", details: "", reason: "" } },
		{ key: "e2", kind: "assistant", parts: [{ type: "thinking", text: "想想" }, { type: "image", mime: "image/png", data: "aGk=" }, { type: "text", text: "回答" }] },
		{ key: "n1", kind: "notice", level: "warn", text: "压缩了" },
	], hasMore: false, nextCursor: "" },
	"/threads/th_1/state": { run: { runId: "r1", status: "terminal", terminal: "completed", startedAt: new Date().toISOString() }, context: { tokens: 1, messages: 1, system: 0, tools: 0, window: 10, percent: 10 }, interactions: [{ id: "ix_1", kind: "confirm", title: "允许写文件", body: "", options: [], questions: [] }] },
};
async function mockFetch(path) {
	const key = String(path).split("?")[0];
	if (!(key in fixtures)) throw new Error("mock fetch 未配置: " + path);
	return JSON.parse(JSON.stringify(fixtures[key]));
}

const registryMock = {
	store: null, // 下面用 reactive 填
	slots: () => [],
	nav: () => [{ path: "/ext/demo/page", label: "演示页", order: 1 }],
	page: () => null,
	plugin: (id) => (id === "demo" ? { id, name: "演示" } : null),
	actionBinding: () => null,
	load: async () => {},
	subscribeWorkspaceEvents: () => {},
};

const routerMock = {
	current: { value: { path: "/threads/th_1", params: { id: "th_1" }, matched: {}, query: {} } },
	navigate: async () => {},
	replace: async () => {},
};
const bootstrapMock = {
	workspace: "/tmp/ws", workspaceName: "ws", productName: "测试产品", brandName: "ada",
	productBadge: "Headless", csrf: "token-1", gateway: "http://127.0.0.1:8090",
	workspaces: [{ name: "ws", path: "/tmp/ws" }], route: "/threads/th_1",
};

// 宿主 UI kit 的冒烟替身（真实实现注册在 index.mjs，其 import 链含浏览器绝对
// 路径，node 里拉不起来；这里按同一 props 形态给最小渲染）。
const { h, createApp, reactive, nextTick, defineComponent } = await import("../runtime.mjs");
registryMock.store = reactive({ plugins: [], loaded: true, loadError: null });

// shell 级服务面的最小替身（真实实现在 index.mjs 根部；页面 setup 只用到方法）。
const toastMock = { show: () => {}, dismiss: () => {}, state: reactive({ items: [] }) };
const imageViewerMock = {
	open: () => {}, close: () => {}, next: () => {}, prev: () => {},
	state: reactive({ open: false, images: [], index: 0 }),
};
const threadMenuMock = {
	open: () => {}, close: () => {},
	state: reactive({ open: false, id: "", title: "", running: false, x: 0, y: 0 }),
};
const smokeUi = {
	"ui-icon": { render: (_s, _ctx) => h("span", { class: "ada-icon ada-icon--mask", "aria-hidden": "true", style: { width: "14px", height: "14px" } }, null) },
	"ui-slot": { render: (_s, ctx) => h("div", { class: "ada-slot", "data-slot": String(ctx.props.name ?? "") }, ctx.kids) },
	"ui-markdown": { render: (_s, ctx) => h("div", { class: "part-text" }, String(ctx.props.text ?? "")) },
	"ui-badge": { render: (_s, ctx) => h("span", { class: "ada-badge" }, ctx.kids) },
	"ui-button": { render: (_s, ctx) => h("button", { class: "ada-btn" }, ctx.kids) },
	"ui-panel": { render: (_s, ctx) => h("section", { class: "ada-panel" }, ctx.kids) },
	"ui-empty": { render: (_s, ctx) => h("div", { class: "ada-empty" }, String(ctx.props.text ?? "")) },
	// 宿主 ui-code（源码高亮）冒烟替身：真实实现见 index.mjs（import 链在 node 里拉不起来）。
	"ui-code": { render: (_s, ctx) => h("code", null, String(ctx.props.text ?? "")) },
};
for (const [name, def] of Object.entries(smokeUi)) {
	smokeUi[name] = defineComponent({ name, render: def.render });
}

// loadSFC 的 io 注入：.vue 直接读盘；.js 复制为临时文件（"/static/ada/..." 改写成
// 相对路径）后用 file:// 导入。state.js 也按同法处理。
let stateCopyReady = false;
function ensureStateCopy() {
	if (stateCopyReady) return;
	const src = readFileSync(join(PAGES, "state.js"), "utf8")
		.replaceAll('"/static/ada/ada-runtime.mjs"', '"../ada-runtime.mjs"');
	writeFileSync(join(PAGES, "__smoke_state.mjs"), src);
	stateCopyReady = true;
}

async function importPageModule(jsPath, tag) {
	ensureStateCopy();
	const src = readFileSync(jsPath, "utf8")
		.replaceAll('"/static/ada/ada-runtime.mjs"', '"../ada-runtime.mjs"')
		.replaceAll('"./state.js"', '"./__smoke_state.mjs"');
	const tmp = join(PAGES, `__smoke_${tag}.mjs`);
	writeFileSync(tmp, src);
	try {
		return await import("file:///" + tmp.replaceAll("\\", "/"));
	} finally {
		unlinkSync(tmp);
	}
}

const { loadSFC } = await import("../sfc.mjs");

test("基座页面：每个页面都能在 DOM shim 上挂载（无渲染错误）", async () => {
	const problems = [];
	let count = 0;
	for (const f of readdirSync(PAGES).filter((n) => n.endsWith(".vue")).sort()) {
		count += 1;
		const tag = f.replace(/\.vue$/, "");
		const comp = await loadSFC("file:///" + join(PAGES, f).replaceAll("\\", "/"), {
			async fetchText() { return readFileSync(join(PAGES, f), "utf8"); },
			importModule: () => importPageModule(join(PAGES, tag + ".js"), tag),
		});
		const host = new DomNode("div");
		const app = createApp(comp);
		for (const [name, uiComp] of Object.entries(smokeUi)) app.component(name, uiComp);
		app.provide("fetch", mockFetch);
		app.provide("registry", registryMock);
		app.provide("router", routerMock);
		app.provide("confirmService", async () => true);
		app.provide("toast", toastMock);
		app.provide("imageViewer", imageViewerMock);
		app.provide("threadMenu", threadMenuMock);
		app.provide("bootstrap", bootstrapMock);
		const caught = [];
		app.config.errorHandler = (err) => { caught.push(`${err?.name}: ${err?.message}`); };
		app.mount(host);
		await nextTick();
		await new Promise((r) => setTimeout(r, 30));
		await nextTick();
		const marked = host.attributes.get("data-ada-error") !== undefined;
		app.unmount(); // 清掉定时器/SSE，避免进程悬挂
		if (caught.length > 0 || marked) {
			problems.push(`${f}: ${caught.join("; ") || "容器被标记 data-ada-error"}`);
		}
	}
	// 清理临时副本（页面模块在 finally 里已删，state 副本在这里删）。
	try { unlinkSync(join(PAGES, "__smoke_state.mjs")); } catch { /* 已不存在 */ }
	assert.ok(count >= 7, `基座页面数量异常：只找到 ${count} 个 .vue`);
	assert.deepEqual(problems, []);
});
