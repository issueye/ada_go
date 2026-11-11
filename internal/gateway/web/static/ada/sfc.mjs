// SFC 装载器（F4）—— 运行时解析/编译单文件组件（借鉴 vue3-sfc-loader 的解剖学，
// 但不走 new Function 求值：模板进自研编译器，逻辑走原生 ESM import）。
//
// SFC 约定：
//   <template> ... </template>            必填，一份
//   <style scoped> ... </style>           可选（scoped 是唯一支持的模式）
//   <script src="./Xxx.js"></script>      必填，src 指向旁边的 ESM；内联 script 直接拒绝
//
// 缓存：compiledCache key = url + "@" + sha256(text)（内容指纹；文件变化自动失效）。

import { compileTemplate } from "./compiler.mjs";
import { makeSfcComponent } from "./runtime.mjs";
import { INLINE_SCRIPT_MSG } from "./whitelist.mjs";

const compiled = new Map();

// ---------------------------------------------------------------------------
// SFC 文本解析
// ---------------------------------------------------------------------------

export class SfcError extends Error {
	constructor(message) {
		super(message);
		this.name = "AdaSfcError";
	}
}

export function parseSFC(text) {
	const blocks = { template: null, styles: [], scriptSrc: null };
	const re = /<(template|style|script)((?:\s+[a-zA-Z-]+(?:=(?:"[^"]*"|'[^']*'))?)*)\s*>([\s\S]*?)<\/\1>/g;
	let m;
	while ((m = re.exec(text)) !== null) {
		const [, kind, attrsRaw, body] = m;
		const attrs = parseAttrs(attrsRaw);
		if (kind === "template") {
			if (blocks.template !== null) throw new SfcError("SFC 只允许一个 <template> 块");
			blocks.template = body;
		} else if (kind === "style") {
			if (attrs.scoped === undefined) {
				throw new SfcError("<style> 必须声明 scoped（非 scoped 全局样式请走 manifest styles）");
			}
			blocks.styles.push(body);
		} else if (kind === "script") {
			if (attrs.src === undefined) {
				throw new SfcError(INLINE_SCRIPT_MSG);
			}
			if (blocks.scriptSrc !== null) throw new SfcError("SFC 只允许一个 <script> 块");
			blocks.scriptSrc = attrs.src;
		}
	}
	// template/script 之外的孤立 <script>（regex 不会匹配自闭合或属性异常形态）兜底检查
	if (/<script(?![^>]*\ssrc=)[^>]*>/i.test(text.replace(re, ""))) {
		throw new SfcError(INLINE_SCRIPT_MSG);
	}
	if (blocks.template === null) throw new SfcError("SFC 缺少 <template> 块");
	if (blocks.scriptSrc === null) throw new SfcError(`SFC 缺少 <script src="./Xxx.js"> 块`);
	return blocks;
}

function parseAttrs(raw) {
	const out = {};
	const re = /([a-zA-Z-]+)(?:=(?:"([^"]*)"|'([^']*)'))?/g;
	let m;
	while ((m = re.exec(raw)) !== null) {
		out[m[1]] = m[2] ?? m[3] ?? "";
	}
	return out;
}

// ---------------------------------------------------------------------------
// scoped CSS 编译（Vue 算法子集）：每个选择器末尾追加 [data-v-<id>]；
// :deep(X) 使其后续部分脱离作用域；@media 内层规则同样处理；@keyframes 原样保留。
// ---------------------------------------------------------------------------

export function compileScopedCss(css, scopeId) {
	const attr = `[data-v-${scopeId}]`;
	const out = [];
	let i = 0;
	while (i < css.length) {
		// 收集一段到 { 或 } 的前奏
		let brace = indexOfTopLevel(css, i);
		if (brace < 0) {
			const rest = css.slice(i).trim();
			if (rest.length > 0) out.push(rest);
			break;
		}
		const prelude = css.slice(i, brace).trim();
		if (prelude.startsWith("@keyframes") || prelude.startsWith("@font-face")) {
			// 原样保留整个块
			const end = matchBrace(css, brace);
			out.push(css.slice(i, end + 1));
			i = end + 1;
			continue;
		}
		if (prelude.startsWith("@")) {
			// @media / @supports：递归处理内层
			const inner = css.slice(brace + 1, matchBrace(css, brace));
			out.push(`${prelude}{${compileScopedCss(inner, scopeId)}}`);
			i = matchBrace(css, brace) + 1;
			continue;
		}
		const end = matchBrace(css, brace);
		const body = css.slice(brace + 1, end);
		const selectors = prelude.split(",").map((s) => scopeSelector(s.trim(), attr)).filter((s) => s.length > 0);
		if (selectors.length > 0 && body.trim().length > 0) {
			out.push(`${selectors.join(",")}{${body.trim()}}`);
		}
		i = end + 1;
	}
	return out.join("\n");
}

function scopeSelector(sel, attr) {
	const deepIdx = sel.indexOf(":deep(");
	if (deepIdx >= 0) {
		const open = sel.indexOf("(", deepIdx);
		const close = sel.indexOf(")", deepIdx);
		const inner = open >= 0 && close > open ? sel.slice(open + 1, close).trim() : "";
		const before = sel.slice(0, deepIdx).trim();
		const after = close >= 0 && close + 1 < sel.length ? sel.slice(close + 1).trim() : "";
		const head = before.length > 0 ? `${before}${attr}` : attr;
		return [head, inner, after].filter((s) => s.length > 0).join(" ");
	}
	// 伪类/伪元素放在 scope 属性之后：p:hover → p[attr]:hover
	const pseudoRe = /::?[a-zA-Z-]+(\([^)]*\))?$/;
	const m = pseudoRe.exec(sel);
	if (m !== null && m.index > 0) {
		return `${sel.slice(0, m.index)}${attr}${sel.slice(m.index)}`;
	}
	return `${sel}${attr}`;
}

// 从 from 起找下一个顶层 { 或 }（忽略字符串/注释的场景在 CSS 里罕见，按朴素处理）。
function indexOfTopLevel(css, from) {
	for (let i = from; i < css.length; i++) {
		const c = css[i];
		if (c === "{" || c === "}") return i;
	}
	return -1;
}

function matchBrace(css, openIdx) {
	let depth = 0;
	for (let i = openIdx; i < css.length; i++) {
		if (css[i] === "{") depth++;
		else if (css[i] === "}") {
			depth--;
			if (depth === 0) return i;
		}
	}
	return css.length - 1;
}

// ---------------------------------------------------------------------------
// 装载（io 可注入以便测试）
// ---------------------------------------------------------------------------

export async function loadSFC(url, io = defaultIO()) {
	const text = await io.fetchText(url);
	let hash;
	try {
		hash = await sha256Hex(text);
	} catch {
		hash = String(text.length); // 无 crypto.subtle 环境退化：长度指纹
	}
	const key = `${url}@${hash}`;
	const hit = compiled.get(key);
	if (hit !== undefined) return hit;

	const blocks = parseSFC(text);
	// 相对 url（如 "/static/ada/pages/Thread.vue"）不能直接作为 new URL 的 base：
	// 用文档 baseURI（或无 DOM 环境下的占位 origin）补全后再解析 <script src>。
	const base = typeof document !== "undefined" && typeof document.baseURI === "string"
		? document.baseURI
		: typeof location !== "undefined" && typeof location.href === "string"
			? location.href
			: "http://ada.invalid/";
	const scriptUrl = new URL(blocks.scriptSrc, new URL(url, base)).toString();
	const mod = await io.importModule(scriptUrl);
	const setupFn = mod.setup ?? mod.default?.setup ?? null;
	if (setupFn === null) {
		throw new SfcError(`SFC 逻辑模块缺少 setup 导出：${scriptUrl}`);
	}

	const scopeId = hash.slice(0, 8);
	const { plan } = compileTemplate(blocks.template);
	const css = blocks.styles.length > 0
		? compileScopedCss(blocks.styles.join("\n"), scopeId)
		: null;

	const def = makeSfcComponent({ setupFn, plan, styles: css, scopeId });
	def.scopeId = scopeId;
	compiled.set(key, def);
	return def;
}

export function clearSfcCache() {
	compiled.clear();
}

function defaultIO() {
	return {
		async fetchText(url) {
			const res = await fetch(url);
			if (!res.ok) throw new SfcError(`SFC 获取失败 ${res.status}: ${url}`);
			return res.text();
		},
		importModule(url) {
			return import(/* @vite-ignore */ url);
		},
	};
}

async function sha256Hex(text) {
	const data = new TextEncoder().encode(text);
	const digest = await crypto.subtle.digest("SHA-256", data);
	return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

export { compileTemplate };
