// plugins-sfc 门禁测试 —— 官方插件 SFC 前端面与基座页面的编译回归防线（M 阶段起）。
//
//  1. 遍历 plugins/official/*/static/*.vue（含金丝雀 ui-hello）与基座页面
//     static/ada/pages/*.vue：每个 template 都要过 parseSFC + compileTemplate。
//     标签/属性/指令/表达式语法的任何白名单违规在这里立即失败——这是防迁移
//     回滚的核心关卡（loadSFC 的浏览器路径走同一对函数）。
//  2. 每个 .vue 的 <script src> 必须真实存在，且逻辑 ESM 的 import 目标只允许
//     "/static/ada/ada-runtime.mjs" 与相对路径（dev-plan §0.4 的导入约定）。
//  3. 清单（plugin.json）里每个 slot/page/dashboard 的 component 都必须存在于
//     assets 目录——Go 装配（bridge.newUnit）做同一件事，这里在 node 侧提前拦截。
import { test } from "node:test";
import assert from "node:assert/strict";
import { readdirSync, readFileSync, existsSync } from "node:fs";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { parseSFC } from "../sfc.mjs";
import { compileTemplate } from "../compiler.mjs";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../../../../../..");
const OFFICIAL_DIR = join(ROOT, "plugins", "official");
const PAGES_DIR = join(ROOT, "internal", "gateway", "web", "static", "ada", "pages");
const RUNTIME_MODULE = "/static/ada/ada-runtime.mjs";

// import/export-from 的模块说明符扫描（含 import "x" 裸导入与 export { a } from "x"）。
// 覆盖静态 ESM 的全部模块加载入口；动态 import() 在插件逻辑里本身就是被禁的表达式，
// 不在本扫描的承诺范围内（浏览器 CSP script-src 'self' 兜底）。
const MODULE_SPEC_RE = /(?:^|[\s;])(?:import\s+(?:[\w$*,{}\s]+\s+from\s+)?|export\s+(?:\*\s+(?:as\s+[\w$]+\s+)?|\{[^}]*\}\s+)from\s+)["']([^"']+)["']/g;

function isAllowedImport(spec) {
	return spec === RUNTIME_MODULE || spec.startsWith("./") || spec.startsWith("../");
}

function pluginIDs() {
	return readdirSync(OFFICIAL_DIR, { withFileTypes: true })
		.filter((d) => d.isDirectory())
		.map((d) => d.name)
		.sort();
}

function vueFiles(pluginID, assets) {
	const dir = join(OFFICIAL_DIR, pluginID, assets || "static");
	try {
		return readdirSync(dir, { withFileTypes: true })
			.filter((d) => d.isFile() && d.name.endsWith(".vue"))
			.map((d) => join(dir, d.name));
	} catch {
		return [];
	}
}

function basePageFiles() {
	try {
		return readdirSync(PAGES_DIR, { withFileTypes: true })
			.filter((d) => d.isFile() && d.name.endsWith(".vue"))
			.map((d) => join(PAGES_DIR, d.name));
	} catch {
		return [];
	}
}

test("官方插件 SFC：每个 .vue 都能过 parseSFC + compileTemplate", () => {
	const problems = [];
	let count = 0;
	for (const id of pluginIDs()) {
		for (const file of vueFiles(id, "static")) {
			count += 1;
			const text = readFileSync(file, "utf8");
			let blocks;
			try {
				blocks = parseSFC(text);
			} catch (err) {
				problems.push(`${file}: parseSFC — ${err.message}`);
				continue;
			}
			try {
				compileTemplate(blocks.template);
			} catch (err) {
				problems.push(`${file}: compileTemplate — ${err.message}`);
			}
		}
	}
	assert.ok(count > 0, "plugins/official 下没有找到任何 .vue（迁移丢失？）");
	assert.deepEqual(problems, []);
});

test("基座页面 SFC：static/ada/pages/*.vue 都能过 parseSFC + compileTemplate", () => {
	const files = basePageFiles();
	assert.ok(files.length >= 7, `基座页面数量异常：只找到 ${files.length} 个 .vue（Shell 需要至少 7 页）`);
	const problems = [];
	for (const file of files) {
		let blocks;
		try {
			blocks = parseSFC(readFileSync(file, "utf8"));
		} catch (err) {
			problems.push(`${file}: parseSFC — ${err.message}`);
			continue;
		}
		try {
			compileTemplate(blocks.template);
		} catch (err) {
			problems.push(`${file}: compileTemplate — ${err.message}`);
		}
	}
	assert.deepEqual(problems, []);
});

test("SFC 逻辑模块（插件 + 基座页面）：<script src> 存在，只 import 运行时门面或相对路径", () => {
	const problems = [];
	const checkOne = (file) => {
		const text = readFileSync(file, "utf8");
		let blocks;
		try {
			blocks = parseSFC(text);
		} catch {
			return; // 上一条用例已报告 parse 失败
		}
		const scriptPath = join(dirname(file), blocks.scriptSrc);
		if (!existsSync(scriptPath)) {
			problems.push(`${file}: <script src="${blocks.scriptSrc}"> 指向的文件不存在`);
			return;
		}
		const src = readFileSync(scriptPath, "utf8");
		for (const m of src.matchAll(MODULE_SPEC_RE)) {
			const spec = m[1];
			if (!isAllowedImport(spec)) {
				problems.push(`${scriptPath}: 不允许的 import 目标 "${spec}"（只允许 ${RUNTIME_MODULE} 与相对路径）`);
			}
		}
	};
	for (const id of pluginIDs()) {
		for (const file of vueFiles(id, "static")) checkOne(file);
	}
	for (const file of basePageFiles()) checkOne(file);
	assert.deepEqual(problems, []);
});

test("官方插件清单：slot/page/dashboard 的 component 都存在于 assets 目录", () => {
	const problems = [];
	for (const id of pluginIDs()) {
		const manifestPath = join(OFFICIAL_DIR, id, "plugin.json");
		if (!existsSync(manifestPath)) continue;
		const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
		const fe = manifest.frontend;
		if (!fe) continue;
		const assetsDir = join(OFFICIAL_DIR, id, fe.assets || "static");
		const refs = [];
		for (const s of fe.slots ?? []) refs.push(["slot:" + s.slot, s.component]);
		for (const p of fe.pages ?? []) refs.push(["page:" + p.slug, p.component]);
		for (const d of fe.dashboard ?? []) refs.push(["dashboard", d.component]);
		for (const [kind, component] of refs) {
			if (!component || !component.endsWith(".vue")) {
				problems.push(`${manifestPath}: ${kind} 的 component "${component}" 不是 .vue 相对路径`);
				continue;
			}
			if (!existsSync(join(assetsDir, component))) {
				problems.push(`${manifestPath}: ${kind} 引用的组件 "${component}" 不在 ${fe.assets || "static"}/ 里`);
			}
		}
	}
	assert.deepEqual(problems, []);
});
