// sfc 装载器契约测试（node --test + 最小 DOM shim）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { createDocument } from "./dom.mjs";

globalThis.document = createDocument();
globalThis.CSSStyleSheet = class {
	constructor() { this._css = ""; }
	replaceSync(css) { this._css = css; }
	get cssText() { return this._css; }
};
globalThis.document.adoptedStyleSheets = [];

const { parseSFC, compileScopedCss, loadSFC, clearSfcCache, SfcError } = await import("../sfc.mjs");
const { nextTick } = await import("../reactivity.mjs");

const SFC = [
	"<template>",
	"  <div class=\"ext-hello\">",
	"    <p>{{ greeting }}</p>",
	"    <button @click='bump()'>{{ n }}</button>",
	"  </div>",
	"</template>",
	"<style scoped>",
	".ext-hello { padding: 4px; }",
	".ext-hello:hover { color: red; }",
	".ext-hello :deep(strong) { font-weight: bold; }",
	"@media (min-width: 600px) { .ext-hello { padding: 8px; } }",
	"@keyframes spin { from { transform: rotate(0); } }",
	"</style>",
	"<script src=\"./Hello.js\"></script>",
].join("\n");

test("parseSFC：块提取与内联 script 拒绝", () => {
	const blocks = parseSFC(SFC);
	assert.equal(blocks.scriptSrc, "./Hello.js");
	assert.match(blocks.template, /ext-hello/);
	assert.equal(blocks.styles.length, 1);
	assert.throws(() => parseSFC("<template><p>x</p></template><script>inline()</script>"), SfcError);
	assert.throws(() => parseSFC("<template><p>x</p></template>"), /缺少/);
	assert.throws(() => parseSFC("<template><p>x</p></template><style>p{}</style><script src='./a.js'></script>"), /scoped/);
	assert.throws(() => parseSFC("<template>a</template><template>b</template><script src='./a.js'></script>"), /一个/);
});

test("compileScopedCss：选择器加作用域、伪类、:deep、@media、@keyframes", () => {
	const css = compileScopedCss(
		".ext-hello { padding: 4px; }\n.ext-hello:hover { color: red; }\n.ext-hello :deep(strong) { font-weight: bold; }\n@media (min-width: 600px) { .ext-hello { padding: 8px; } }\n@keyframes spin { from { transform: rotate(0); } }",
		"abc12345",
	);
	assert.match(css, /\.ext-hello\[data-v-abc12345\]\{padding: 4px;\}/);
	assert.match(css, /\.ext-hello\[data-v-abc12345\]:hover\{color: red;\}/);
	assert.match(css, /\.ext-hello\[data-v-abc12345\] strong\{font-weight: bold;\}/);
	assert.match(css, /@media \(min-width: 600px\)\{\.ext-hello\[data-v-abc12345\]\{padding: 8px;\}\}/);
	assert.match(css, /@keyframes spin \{ from \{ transform: rotate\(0\); \} \}/);
});

test("loadSFC：解析+编译+缓存命中（注入 io）", async () => {
	clearSfcCache();
	let fetches = 0;
	let imports = 0;
	const io = {
		async fetchText() {
			fetches++;
			return SFC;
		},
		async importModule() {
			imports++;
			return {
				setup() {
					return { greeting: "你好", n: 0, bump() {} };
				},
			};
		},
	};
	const def = await loadSFC("https://ext.test/static/Hello.vue", io);
	assert.equal(fetches, 1);
	assert.equal(imports, 1);
	assert.ok(def.plan !== undefined);
	assert.ok(def.scopeId.length === 8);

	// 二次装载同 URL：fetch 一次即可（内容没变 → 缓存命中，不重复 import）
	const def2 = await loadSFC("https://ext.test/static/Hello.vue", io);
	assert.equal(fetches, 2); // 仍需 fetch 校验内容
	assert.equal(imports, 1);
	assert.equal(def2, def);
});

test("loadSFC：内容变化 → 指纹变化 → 重新编译", async () => {
	clearSfcCache();
	let content = SFC;
	const io = {
		async fetchText() { return content; },
		async importModule() { return { setup: () => ({}) }; },
	};
	const a = await loadSFC("https://ext.test/static/X.vue", io);
	content = SFC.replace("greeting", "hi");
	const b = await loadSFC("https://ext.test/static/X.vue", io);
	assert.notEqual(a, b);
});

test("loadSFC 全链路：装载即渲染（挂载验证）", async () => {
	clearSfcCache();
	const { h, createApp, ref } = await import("../runtime.mjs");
	const io = {
		async fetchText() { return SFC; },
		async importModule() {
			return {
				setup() {
					const n = ref(0);
					return { greeting: "你好", n, bump() { n.value++; } };
				},
			};
		},
	};
	const def = await loadSFC("https://ext.test/static/Hello.vue", io);
	const host = globalThis.document.createElement("div");
	globalThis.document.body.appendChild(host);
	const app = createApp(def);
	app.mount(host);
	await nextTick();
	const root = host.childNodes[0];
	// scope 属性由渲染器按组件 scopeId 盖章（scoped CSS 选择器依赖它）
	assert.ok(root.attributes.has("data-v-" + def.scopeId));
	assert.ok(globalThis.document.adoptedStyleSheets.length >= 1);
	assert.equal(root.childNodes[0].childNodes[0].nodeValue, "你好");
});

test("loadSFC：模块缺 setup 导出 → 报错", async () => {
	clearSfcCache();
	const io = {
		async fetchText() { return SFC; },
		async importModule() { return { notSetup: true }; },
	};
	await assert.rejects(() => loadSFC("https://ext.test/static/Y.vue", io), /setup/);
});
