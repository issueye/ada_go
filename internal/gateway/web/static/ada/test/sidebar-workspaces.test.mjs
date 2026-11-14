// sidebar-workspaces.test.mjs —— 侧栏「工作区列表」结构与行为的门禁（2026-11-11 调整）。
//
// 锁住三件在这次重构里容易回归的事：
//  1. 模板里不再有「其它工作区」分区与两块各自滚动的结构（data-ws-active /
//     data-ws-others / ada-thread-list--split 都该消失），所有工作区是同一列表。
//  2. 每个工作区组都是可折叠的 button（details/summary 的原生开合已换成点击）。
//  3. 搜索直接过滤 v-for 的源（hitThreads），而不是渲染后 hidden——后者会在
//     「搜索命中 0 条」时留下一整片空行。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { parseSFC } from "../sfc.mjs";
import { compileTemplate } from "../compiler.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const PAGES = resolve(HERE, "../pages");

const vue = readFileSync(join(PAGES, "Sidebar.vue"), "utf8");
const js = readFileSync(join(PAGES, "Sidebar.js"), "utf8");
const { plan } = compileTemplate(parseSFC(vue).template);

// 遍历渲染计划的所有节点（el / if 分支 / for 子体都要走）。
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

const staticClasses = (n) => n.props.filter((p) => p.s !== undefined).map((p) => p.s);

test("侧栏不再分「当前 / 其它」两块各自滚动", () => {
	for (const gone of ["data-ws-active", "data-ws-others", "ada-thread-list--split", "ada-ws-scroll--active", "ada-ws-scroll--others", "ada-ws-scroll--workArea"]) {
		assert.equal(vue.includes(gone), false, `Sidebar.vue 仍残留 ${gone}（工作区应统一成一个列表）`);
	}
	assert.ok(vue.includes("data-ws-list"), "Sidebar.vue 缺少统一的工作区列表容器 data-ws-list");
});

test("侧栏工作区组：可折叠 button + 展开时才加载", () => {
	const summary = els("button").filter((b) => staticClasses(b).includes("ada-ws__summary"));
	assert.equal(summary.length, 1, "工作区组头应恰好一个 .ada-ws__summary button");
	const props = summary[0].props;
	assert.ok(
		props.some((p) => p.n === "on:click" && JSON.stringify(p.e ?? null).includes("toggleGroup")),
		"组头点击必须走 toggleGroup（展开/折叠 + 切换当前工作区）",
	);
	assert.ok(props.some((p) => p.n === "aria-expanded"), "组头要有 aria-expanded（折叠态对读屏可见）");

	// 折叠态：会话容器挂 v-if="g.open"。计划里表达式是结构化的
	// {t:"mem", o:{t:"id",n:"g"}, p:"open"}，不是源码文本，按结构比对。
	const ifNodes = [];
	walk(plan, (n) => { if (n.k === "if") ifNodes.push(n); });
	const openCond = ifNodes.some((n) =>
		n.branches.some((b) => b.e && b.e.t === "mem" && b.e.p === "open" && b.e.o && b.e.o.n === "g"),
	);
	assert.ok(openCond, "缺少 v-if=g.open 条件（折叠态应完全不渲染会话行）");

	// 会话行绑定的 v-for 源必须是 hitThreads(g)：过滤发生在渲染前。
	const forSrcs = [];
	walk(plan, (n) => { if (n.k === "for") forSrcs.push(n.src); });
	assert.ok(
		forSrcs.some((s) => s && s.t === "call" && s.c && s.c.n === "hitThreads"),
		"会话行的 v-for 源应为 hitThreads(g)",
	);
	assert.ok(js.includes("if (!g.loaded) await loadGroup"), "展开未加载的分组时必须触发 loadGroup");
});

test("侧栏搜索过滤 v-for 源，而不是渲染后 hidden", () => {
	assert.equal(vue.includes(':hidden="!threadHit(t)"'), false, "搜索不应再用渲染后 hidden");
	assert.ok(vue.includes("hitThreads(g)"), "会话行应绑定 hitThreads(g)（过滤后的列表）");
	assert.ok(js.includes("function hitThreads"), "Sidebar.js 缺少 hitThreads");
});

test("侧栏「全部展开/收起」按钮与搜索按钮存在", () => {
	assert.ok(vue.includes("toggleAll"), "缺少全部展开/收起按钮");
	assert.ok(vue.includes("focusSearch"), "工作区头/窄轨缺少聚焦搜索入口");
});
