// shell-sidebar.test.mjs —— 侧栏三条交互的门禁（2026-11-12 走查回归）。
//
// 走查报告的三件事都源于「DOM/CSS 已经换了一套，行为代码还留在上一套」：
//  1. 拖动没有效果：拖拽条改成 SPA 模板里的 [data-sidebar-resizer]，旧 app.js 的
//     bindSidebarResizer 随 SSR 一起删了，没人再给 --ada-sidebar-width 赋值；
//     另外 ui/pages.css 的断点直接写 width，会覆盖根变量。
//  2. 收缩没有效果：顶栏按钮调了不存在的 toggleSidebar()，.ada-shell 从没拿到
//     is-sidebar-collapsed。
//  3. 主题无法展开：themes.js 在 <head> 里抓 panel 引用（SPA 时还是 null），
//     且它与组件都写 hidden，重渲染后展开态被盖回去。
// 这里锁住「行为已接上 + 状态只有一处真源」，避免下次重构再丢。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { parseSFC } from "../sfc.mjs";
import { compileTemplate } from "../compiler.mjs";
import {
	SIDEBAR_WIDTH_KEY, SIDEBAR_WIDTH_MIN, SIDEBAR_WIDTH_MAX, SIDEBAR_WIDTH_DEFAULT,
	readSidebarWidth, clampSidebarWidth, applySidebarWidth, bindSidebarWidth,
} from "../pages/sidebar-width.js";
import { createDocument } from "./dom.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
// HERE = static/ada/test → 页面在 ../pages，静态根在 ../..
const ADA = resolve(HERE, "..");
const PAGES = join(ADA, "pages");
const STATIC = resolve(HERE, "../..");
const vue = readFileSync(join(PAGES, "Sidebar.vue"), "utf8");
const sidebarJs = readFileSync(join(PAGES, "Sidebar.js"), "utf8");
const stateJs = readFileSync(join(PAGES, "state.js"), "utf8");
const themesJs = readFileSync(join(STATIC, "themes.js"), "utf8");
const shellCss = readFileSync(join(ADA, "shell.css"), "utf8");
const indexMjs = readFileSync(join(ADA, "index.mjs"), "utf8");

const { plan } = compileTemplate(parseSFC(vue).template);

function walk(n, visit) {
	if (!n || typeof n !== "object") return;
	if (Array.isArray(n)) { n.forEach((x) => walk(x, visit)); return; }
	visit(n);
	if (n.kids) walk(n.kids, visit);
	if (n.branches) for (const b of n.branches) walk(b.kids, visit);
	if (n.fallback) walk(n.fallback, visit);
}

function els(tag) {
	const out = [];
	walk(plan, (n) => { if (n.k === "el" && n.tag === tag) out.push(n); });
	return out;
}

// ---------------------------------------------------------------------------
// 1. 侧栏宽度：拖动条真的接到 --ada-sidebar-width 上
// ---------------------------------------------------------------------------

function makeEnv(width = 1440) {
	const doc = createDocument();
	const style = new Map();
	doc.documentElement.style.setProperty = (k, v) => style.set(k, v);
	doc.documentElement.style.getPropertyValue = (k) => style.get(k) ?? "";
	const storage = new Map();
	const win = {
		innerWidth: width,
		innerHeight: 900,
		document: doc,
		localStorage: {
			getItem: (k) => (storage.has(k) ? storage.get(k) : null),
			setItem: (k, v) => storage.set(k, String(v)),
			removeItem: (k) => storage.delete(k),
		},
		addEventListener() {},
		removeEventListener() {},
	};
	return { doc, win, style, storage };
}

// 拖拽条 + 侧栏（含固定宽度）：用来验证 pointerdown/move/up 的整条链路。
function makeHandle(doc, startWidth) {
	const el = doc.createElement("div");
	el.setAttribute("data-sidebar-resizer", "");
	el.focus = () => {};
	el.setPointerCapture = () => {};
	el.releasePointerCapture = () => {};
	// DomNode 没有 classList：补一个最小实现（生产代码只 add/remove）。
	const classes = new Set();
	el.classList = {
		add: (c) => classes.add(c),
		remove: (c) => classes.delete(c),
		contains: (c) => classes.has(c),
	};
	el.classes = classes;
	const sidebar = doc.createElement("div");
	sidebar.setAttribute("id", "chat-sidebar");
	sidebar.getBoundingClientRect = () => ({ width: startWidth });
	doc.body.appendChild(sidebar);
	doc.body.appendChild(el);
	doc.querySelector = (sel) => (sel === "[data-sidebar-resizer]" ? el : null);
	return el;
}

// DomNode.removeEventListener 需要同一个函数引用；这里包一层记录，方便断言解绑。
// fire(type, event) 直接派发给 window 上的监听（shim 不做冒泡，拖动期间的事件
// 本身就是挂在 window 上收的）。
function trackWindow(win, listeners) {
	win.addEventListener = (type, fn) => { listeners.push({ type, fn, on: true }); };
	win.removeEventListener = (type, fn) => {
		const i = listeners.findIndex((l) => l.type === type && l.fn === fn);
		if (i >= 0) listeners.splice(i, 1);
	};
	win.fire = (type, event) => {
		for (const l of [...listeners]) if (l.type === type) l.fn(event);
	};
}

test("侧栏宽度：默认值跟随 a_da 断点，存过值则用存储值", () => {
	const env = makeEnv(1440);
	assert.equal(readSidebarWidth(env.win), SIDEBAR_WIDTH_DEFAULT);
	env.win.innerWidth = 1000;
	assert.equal(readSidebarWidth(env.win), 236, "≤1100px 的断点档是 236");
	env.win.innerWidth = 1440;
	env.storage.set(SIDEBAR_WIDTH_KEY, "320");
	assert.equal(readSidebarWidth(env.win), 320, "存过宽度后按存储值");
	env.storage.set(SIDEBAR_WIDTH_KEY, "999");
	assert.equal(readSidebarWidth(env.win), SIDEBAR_WIDTH_DEFAULT, "越界值回落默认");
});

test("侧栏宽度：clamp 同时受上下限与视口约束", () => {
	assert.equal(clampSidebarWidth(100, 1400), SIDEBAR_WIDTH_MIN);
	assert.equal(clampSidebarWidth(900, 1400), SIDEBAR_WIDTH_MAX);
	// 视口只剩 480 → max(200, 480-360=120) = 200，主区至少留 360
	assert.equal(clampSidebarWidth(900, 480), SIDEBAR_WIDTH_MIN);
	assert.equal(clampSidebarWidth(300.4, 1400), 300);
});

test("侧栏宽度：拖动改变量、松手才落存储；清理函数解掉 window 监听", () => {
	const env = makeEnv(1440);
	const listeners = [];
	trackWindow(env.win, listeners);
	const handle = makeHandle(env.doc, 264);
	const cleanup = bindSidebarWidth(handle, { env });
	assert.equal(env.style.get("--ada-sidebar-width"), "264px", "挂载时先按存储/默认落一次变量");

	handle.dispatch("pointerdown", { button: 0, pointerId: 7, clientX: 300, clientY: 10 });
	assert.equal(listeners.length, 4, "拖动开始挂上 pointermove/up/cancel（另有一个常驻 resize）");
	env.win.fire("pointermove", { pointerId: 7, clientX: 360 });
	assert.equal(env.style.get("--ada-sidebar-width"), "324px", "移动中只改 CSS 变量");
	assert.equal(env.storage.has(SIDEBAR_WIDTH_KEY), false, "移动中不写存储");
	env.win.fire("pointerup", { pointerId: 7, clientX: 360 });
	assert.equal(env.storage.get(SIDEBAR_WIDTH_KEY), "324", "松手落存储");
	assert.equal(listeners.length, 1, "松手后只留 resize 监听");

	cleanup();
	assert.equal(listeners.length, 0, "卸载时连 resize 一并解绑");
});

test("侧栏宽度：窄屏/折叠时拖动与键盘都不生效", () => {
	const env = makeEnv(1440);
	const handle = makeHandle(env.doc, 264);
	const cleanup = bindSidebarWidth(handle, { env, isDisabled: () => true });
	handle.dispatch("pointerdown", { button: 0, pointerId: 1, clientX: 300 });
	handle.dispatch("keydown", { key: "ArrowRight", preventDefault() {} });
	assert.equal(env.style.get("--ada-sidebar-width"), "264px");
	cleanup();
});

test("侧栏宽度：双击恢复默认，键盘键有边界", () => {
	const env = makeEnv(1440);
	const handle = makeHandle(env.doc, 380);
	const cleanup = bindSidebarWidth(handle, { env });
	handle.dispatch("dblclick", {});
	assert.equal(env.storage.get(SIDEBAR_WIDTH_KEY), String(SIDEBAR_WIDTH_DEFAULT));
	handle.dispatch("keydown", { key: "Home", preventDefault() {} });
	assert.equal(env.style.get("--ada-sidebar-width"), SIDEBAR_WIDTH_MIN + "px");
	handle.dispatch("keydown", { key: "End", preventDefault() {} });
	assert.equal(env.style.get("--ada-sidebar-width"), SIDEBAR_WIDTH_MAX + "px");
	cleanup();
});

// ---------------------------------------------------------------------------
// 2. 收缩：菜单按钮 -> shellState.collapsed -> .ada-shell 的类
// ---------------------------------------------------------------------------

test("折叠状态由 shellState 统一持有，且持久化到 a_da_sidebar_collapsed", () => {
	assert.match(stateJs, /COLLAPSE_KEY\s*=\s*"a_da_sidebar_collapsed"/, "缺少折叠偏好的存储键");
	assert.match(stateJs, /toggleAppearance\s*\(\)/, "菜单按钮应走 toggleAppearance（窄屏抽屉 / 宽屏折叠）");
	assert.match(stateJs, /applyBreakpoint\s*\(\)/, "缺少跨断点清理（否则抽屉与折叠可同时成立）");
	assert.match(stateJs, /max-width:\s*760px/, "窄屏断点必须与 pages.css 的抽屉断点同值");
});

test("ShellApp：外壳类、遮罩与菜单按钮都跟着 shellState", () => {
	assert.match(indexMjs, /is-sidebar-collapsed/, "ShellApp 必须把 .ada-shell 渲染成折叠态");
	assert.match(indexMjs, /toggleAppearance/, "菜单按钮必须调用存在的 toggleAppearance()");
	assert.doesNotMatch(indexMjs, /toggleSidebar\(\)/, "旧的（已不存在的）toggleSidebar() 调用必须清掉");
	assert.match(indexMjs, /ada-backdrop is-open|backdropClass/, "遮罩的 is-open 要跟 sidebarOpen 绑定");
	// 断点变化要回写状态，否则 resize 之后按钮行为与 CSS 形态对不上。
	assert.match(indexMjs, /matchMedia\("\(max-width: 760px\)"\)/, "缺少 760px 断点的 matchMedia 监听");
	assert.match(indexMjs, /applyBreakpoint/, "断点 change 后必须调用 applyBreakpoint");
});

test(".ada-sidebar 的宽度在宽屏由变量决定（否则拖动写入会被断点覆盖）", () => {
	// 必须在媒体查询里重建 width：特异性要压过 ui/pages.css 的 .ada-sidebar 断点规则。
	assert.match(shellCss, /@media\s*\(min-width:\s*761px\)\s*\{[\s\S]*\.ada-shell .ada-sidebar\s*\{\s*width:\s*var\(--ada-sidebar-width, 264px\)/, "缺少宽屏宽度接管规则");
	assert.match(shellCss, /\.ada-shell\.is-sidebar-collapsed \.ada-sidebar\s*\{\s*width:\s*56px/, "折叠窄轨 56px 必须继续生效");
});

// ---------------------------------------------------------------------------
// 3. 主题面板：开合只为组件状态，themes.js 不再抢着写 hidden
// ---------------------------------------------------------------------------

test("主题面板的展开态绑定 themeOpen，而不是静态 hidden", () => {
	assert.doesNotMatch(vue, /<div class="ada-theme-pop"[^>]*\shidden[^>]>/i, "面板不能再渲染静态 hidden（重渲染会把展开态盖回去）");
	assert.match(vue, /:hidden="!themeOpen"/, "面板的 hidden 必须绑定 themeOpen");
	assert.match(vue, /@click="toggleThemePanel"/, "触发器必须调用 toggleThemePanel");
	assert.match(vue, /:aria-expanded="themeOpen \? 'true' : 'false'"/, "触发器 aria-expanded 要跟展开态");
	assert.match(sidebarJs, /const themeOpen = ref\(false\)/, "Sidebar.js 缺少 themeOpen 状态");
	assert.match(sidebarJs, /function toggleThemePanel\(\)/, "Sidebar.js 缺少 toggleThemePanel");
	assert.match(sidebarJs, /themeOpen, toggleThemePanel/, "themeOpen/toggleThemePanel 必须导出给模板");
	assert.match(sidebarJs, /onDocClickTheme/, "缺少点面板外关闭");
});

test("themes.js 只应用主题（不碰面板开合）", () => {
	// 它阻塞加载于 <head>，SPA 渲染时面板还不存在；而且改 DOM 会被重渲染覆盖。
	assert.doesNotMatch(themesJs, /data-theme-panel/, "themes.js 不应再查询/操作主题面板");
	assert.doesNotMatch(themesJs, /setPanel/, "themes.js 的 setPanel 应随组件化一起删除");
	assert.match(themesJs, /data-theme-mode/, "仍要负责模式按钮的选中态镜像");
	assert.match(themesJs, /data-theme-pick/, "仍要负责色板按钮的选中态镜像");
});

test("主题面板按钮：模式/色板都在模板里（插件页与任意路由都可用）", () => {
	const modeBtns = els("button").filter((b) => b.props.some((p) => p.n === "data-theme-mode"));
	const pickBtns = els("button").filter((b) => b.props.some((p) => p.n === "data-theme-pick"));
	assert.equal(modeBtns.length, 1, "应有 1 个 v-for 输出三态模式按钮");
	assert.equal(pickBtns.length, 1, "应有 1 个 v-for 输出色板按钮");
});
