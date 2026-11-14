// 模板编译器 —— <template> 文本 → 渲染计划（plan）。
//
// 与 Vue 的分水岭（见 proposals/spa-frontend-framework.md §5.4）：
//   模板不编译成代码字符串再 new Function，而是编译成数据形态的渲染计划，
//   由 runtime.mjs 的解释器在渲染时遍历。表达式 AST 也按内容缓存。
//
// 计划节点形态（k = kind）：
//   { k:"text", v }                         静态文本
//   { k:"dtext", e }                        插值 {{ expr }}
//   { k:"el", tag, comp, void, props, kids }元素/组件；props 见下
//   { k:"if", branches:[{e,kids}], fallback }v-if / v-else-if / v-else
//   { k:"for", src, item, index, key, kids }v-for
// props 条目：
//   { n, s }                                静态属性（编译期已过白名单）
//   { n, e }                                动态绑定（:prop="expr"）
//   { n:"on:click", e, mods }               事件绑定（@click="expr"）
//
// v1 不支持（编译期报错，消息里给出替代方案）：v-html、v-pre、<slot>、<component :is>、
// 自增/自减、复合赋值（+= 等）、可选链赋值。支持 ?. 与 ??。

import { TAGS, isAllowedTag, isUiComponent, checkAttr, INLINE_SCRIPT_MSG } from "./whitelist.mjs";

const VOID_TAGS = new Set(["br", "hr", "input"]);

// 表达式白名单全局与禁止标识符（从 ada-render.js 的既有纪律继承）。
const GLOBALS = {
	Math, JSON, Date, Number, String, Boolean, Array, Object,
	parseInt, parseFloat, isNaN,
};
const BANNED = new Set([
	"constructor", "__proto__", "prototype", "eval", "Function",
	"window", "document", "fetch", "globalThis", "self", "top", "parent",
	"localStorage", "sessionStorage", "location", "navigator",
	"XMLHttpRequest", "import", "require", "process",
]);

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

export class TemplateError extends Error {
	constructor(message, pos) {
		if (pos !== undefined) {
			super(`${message}（位置 ${pos.line}:${pos.col}）`);
		} else {
			super(message);
		}
		this.name = "AdaTemplateError";
	}
}

// ---------------------------------------------------------------------------
// 词法：把模板拆成 text / tag 起止 / 注释
// ---------------------------------------------------------------------------

function tokenize(src) {
	const tokens = [];
	let i = 0;
	const n = src.length;
	const posAt = (idx) => {
		let line = 1, col = 1;
		for (let k = 0; k < idx && k < n; k++) {
			if (src[k] === "\n") { line++; col = 1; } else col++;
		}
		return { line, col };
	};
	const err = (msg, idx) => { throw new TemplateError(msg, posAt(idx)); };

	while (i < n) {
		const lt = src.indexOf("<", i);
		if (lt < 0) {
			pushText(src.slice(i));
			break;
		}
		if (lt > i) pushText(src.slice(i, lt));
		if (src.startsWith("<!--", lt)) {
			const end = src.indexOf("-->", lt + 4);
			if (end < 0) err("注释未闭合", lt);
			i = end + 3;
			continue;
		}
		const m = /^<\/?([a-zA-Z][a-zA-Z0-9-]*)/.exec(src.slice(lt));
		if (!m) err(`非法的 "<" 起始`, lt);
		const closing = src[lt + 1] === "/";
		const tag = m[1].toLowerCase();
		let j = lt + (closing ? 2 : 1) + m[1].length;
		const attrs = [];
		if (!closing) {
			for (;;) {
				while (j < n && /\s/.test(src[j])) j++;
				if (j >= n) err(`标签 <${tag}> 未闭合`, lt);
				if (src[j] === ">" || src.startsWith("/>", j)) break;
				const am = /^([a-zA-Z_@:][a-zA-Z0-9_.:@-]*)(\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+)))?/.exec(src.slice(j));
				if (!am) err(`标签 <${tag}> 中存在非法属性`, j);
				const name = am[1];
				const value = am[3] !== undefined ? (am[4] ?? am[5] ?? am[6] ?? "") : undefined;
				attrs.push({ name, value, pos: posAt(j) });
				j += am[0].length;
			}
		}
		const selfClosing = src.startsWith("/>", j);
		if (selfClosing) j += 2;
		else if (src[j] === ">") j += 1;
		else err(`标签 <${tag}> 闭合符缺失`, lt);
		tokens.push({ type: closing ? "end" : "start", tag, attrs, selfClosing: selfClosing || (!closing && VOID_TAGS.has(tag)), pos: posAt(lt) });
		i = j;
	}
	return tokens;

	function pushText(text) {
		if (text.length > 0) tokens.push({ type: "text", text });
	}
}

// ---------------------------------------------------------------------------
// 表达式解析（Pratt）
// ---------------------------------------------------------------------------

const EXPR_TOKEN = /\s*(?:(\d+(?:\.\d+)?)|("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')|([A-Za-z_$][A-Za-z0-9_$]*)|(===|!==|==|!=|<=|>=|&&|\|\||\?\?|\?\.|[-+*/%!<>?:.,()\[\]{}=]))/y;

// 单引号字符串 → JSON 双引号形式解析；\' 先还原再转义内部双引号。
function unquote(raw) {
	if (raw[0] === "'") {
		const inner = raw.slice(1, -1).replace(/\\'/g, "'");
		return JSON.parse(`"${inner.replaceAll('"', '\\"')}"`);
	}
	return JSON.parse(raw);
}

function parseExpr(src, pos) {
	let i = 0;
	const n = src.length;
	const err = (msg) => { throw new TemplateError(`表达式 "${src}" 解析失败：${msg}`, pos); };

	function lex() {
		EXPR_TOKEN.lastIndex = i;
		const m = EXPR_TOKEN.exec(src);
		if (!m || m.index !== i) {
			// 注意：y 标志下 match 从 lastIndex 开始，空匹配不会前进
			if (i < n && /\s/.test(src[i])) { i++; return lex(); }
			return null;
		}
		i = EXPR_TOKEN.lastIndex;
		if (m[1] !== undefined) return { kind: "num", v: Number(m[1]) };
		if (m[2] !== undefined) return { kind: "str", v: unquote(m[2]) };
		if (m[3] !== undefined) return { kind: "name", v: m[3] };
		return { kind: "op", v: m[4] };
	}
	function peek() { const save = i; const t = lex(); i = save; return t; }
	function next() { return lex(); }

	function parseAssign() {
		const left = parseTernary();
		const t = peek();
		if (t && t.kind === "op" && t.v === "=") {
			next();
			if (!["id", "mem", "idx"].includes(left.t)) err("赋值目标必须是标识符或属性路径");
			return { t: "assign", target: left, v: parseAssign() };
		}
		return left;
	}
	function parseTernary() {
		const test = parseBinary(0);
		const t = peek();
		if (t && t.kind === "op" && t.v === "?") {
			next();
			const thenE = parseAssign();
			const c = next();
			if (!c || c.kind !== "op" || c.v !== ":") err("三元表达式缺少 :");
			const elseE = parseAssign();
			return { t: "cond", test, a: thenE, b: elseE };
		}
		return test;
	}
	const BIN_PREC = [
		["||"], ["&&"], ["??"],
		["===", "!==", "==", "!="],
		["<", ">", "<=", ">="],
		["+", "-"],
		["*", "/", "%"],
	];
	function parseBinary(minPrec) {
		let left = parseUnary();
		for (;;) {
			const t = peek();
			if (!t || t.kind !== "op") return left;
			let level = -1;
			for (let lv = 0; lv < BIN_PREC.length; lv++) if (BIN_PREC[lv].includes(t.v)) level = lv;
			if (level < 0 || level < minPrec) return left;
			next();
			const right = parseBinary(level + 1);
			left = { t: "bin", op: t.v, l: left, r: right };
		}
	}
	function parseUnary() {
		const t = peek();
		if (t && t.kind === "op" && (t.v === "!" || t.v === "-" || t.v === "+")) {
			next();
			return { t: "un", op: t.v, a: parseUnary() };
		}
		return parsePostfix(parsePrimary());
	}
	function parsePrimary() {
		const t = next();
		if (!t) err("表达式意外结束");
		if (t.kind === "num") return { t: "lit", v: t.v };
		if (t.kind === "str") return { t: "lit", v: t.v };
		if (t.kind === "name") {
			if (BANNED.has(t.v)) err(`禁止访问 "${t.v}"`);
			// 字面量关键字（Vue 模板同款支持；此前被当标识符导致作用域报错）
			if (t.v === "true") return { t: "lit", v: true };
			if (t.v === "false") return { t: "lit", v: false };
			if (t.v === "null") return { t: "lit", v: null };
			if (t.v === "undefined") return { t: "lit", v: undefined };
			return { t: "id", n: t.v };
		}
		if (t.kind === "op" && t.v === "(") {
			const e = parseAssign();
			const c = next();
			if (!c || c.kind !== "op" || c.v !== ")") err("缺少 )");
			return e;
		}
		if (t.kind === "op" && t.v === "[") {
			const items = [];
			for (;;) {
				const nt = peek();
				if (nt && nt.kind === "op" && nt.v === "]") { next(); break; }
				items.push(parseAssign());
				const sep = next();
				if (sep && sep.kind === "op" && sep.v === ",") continue;
				if (sep && sep.kind === "op" && sep.v === "]") break;
				err("数组字面量缺少 ]");
			}
			return { t: "arr", items };
		}
		if (t.kind === "op" && t.v === "{") {
			const props = [];
			for (;;) {
				const nt = peek();
				if (nt && nt.kind === "op" && nt.v === "}") { next(); break; }
				const key = next();
				if (!key || key.kind !== "name" && key.kind !== "str") err("对象字面量键必须是标识符或字符串");
				const colon = next();
				if (!colon || colon.kind !== "op" || colon.v !== ":") err("对象字面量缺少 :");
				props.push({ k: key.kind === "str" ? JSON.parse(key.v) : key.v, v: parseAssign() });
				const sep = next();
				if (sep && sep.kind === "op" && sep.v === ",") continue;
				if (sep && sep.kind === "op" && sep.v === "}") break;
				err("对象字面量缺少 }");
			}
			return { t: "obj", props };
		}
		err(`意外的记号 ${t.kind === "op" ? t.v : JSON.stringify(t.v)}`);
	}
	function parsePostfix(node) {
		for (;;) {
			const t = peek();
			if (!t || t.kind !== "op") return node;
			if (t.v === ".") {
				next();
				const p = next();
				if (!p || p.kind !== "name") err("属性访问缺少标识符");
				if (BANNED.has(p.v)) err(`禁止访问属性 "${p.v}"`);
				node = { t: "mem", o: node, p: p.v, opt: false };
			} else if (t.v === "?.") {
				next();
				const p = next();
				if (!p || p.kind !== "name") err("可选链缺少标识符");
				if (BANNED.has(p.v)) err(`禁止访问属性 "${p.v}"`);
				node = { t: "mem", o: node, p: p.v, opt: true };
			} else if (t.v === "[") {
				next();
				const e = parseAssign();
				const c = next();
				if (!c || c.kind !== "op" || c.v !== "]") err("缺少 ]");
				if (e.t === "lit" && typeof e.v === "string" && BANNED.has(e.v)) err(`禁止访问属性 "${e.v}"`);
				node = { t: "idx", o: node, e, opt: false };
			} else if (t.v === "(") {
				next();
				const args = [];
				for (;;) {
					const nt = peek();
					if (nt && nt.kind === "op" && nt.v === ")") { next(); break; }
					args.push(parseAssign());
					const sep = next();
					if (sep && sep.kind === "op" && sep.v === ",") continue;
					if (sep && sep.kind === "op" && sep.v === ")") break;
					err("调用缺少 )");
				}
				node = { t: "call", c: node, args, opt: false };
			} else {
				return node;
			}
		}
	}

	const ast = parseAssign();
	const rest = peek();
	if (rest) err(`存在无法解析的尾部记号 "${rest.kind === "op" ? rest.v : rest.v}"`);
	return ast;
}

const exprCache = new Map();
function parseExprCached(src, pos) {
	let ast = exprCache.get(src);
	if (ast === undefined) {
		ast = parseExpr(src, pos);
		exprCache.set(src, ast);
	}
	return ast;
}

// ---------------------------------------------------------------------------
// 表达式求值（runtime 渲染计划时调用）
// ---------------------------------------------------------------------------

export function evaluate(ast, scope, extra) {
	return evalNode(ast, scope, extra);
}

function evalNode(node, scope, extra) {
	switch (node.t) {
		case "lit": return node.v;
		case "id": {
			const n = node.n;
			if (n === "$event") {
				if (extra && "event" in extra) return extra.event;
				throw new TemplateError('$event 只能在事件表达式里使用');
			}
			if (scope !== null && n in scope) return scope[n];
			if (n in GLOBALS) return GLOBALS[n];
			throw new TemplateError(`表达式里的 "${n}" 不在作用域内（作用域 = setup 返回值 + props + 白名单全局）`);
		}
		case "mem": {
			const o = evalNode(node.o, scope, extra);
			if (o === null || o === undefined) {
				if (node.opt) return undefined;
				throw new TemplateError(`对 ${desc(node.o)} 的空值取属性 "${node.p}"（可用 ?. 可选链）`);
			}
			return o[node.p];
		}
		case "idx": {
			const o = evalNode(node.o, scope, extra);
			const k = evalNode(node.e, scope, extra);
			if (o === null || o === undefined) {
				if (node.opt) return undefined;
				throw new TemplateError(`对 ${desc(node.o)} 的空值取下标`);
			}
			return o[k];
		}
		case "call": {
			// 方法调用必须绑定接收者：items.slice() 若解绑 this，数组方法直接失效。
			if (node.c.t === "mem") {
				const o = evalNode(node.c.o, scope, extra);
				if (o === null || o === undefined) {
					if (node.c.opt) return undefined;
					throw new TemplateError(`对 ${desc(node.c.o)} 的空值调用方法 "${node.c.p}"`);
				}
				const fn = o[node.c.p];
				if (typeof fn !== "function") throw new TemplateError(`${desc(node.c)}.${node.c.p} 不是函数`);
				const args = node.args.map((a) => evalNode(a, scope, extra));
				return fn.apply(o, args);
			}
			const fn = evalNode(node.c, scope, extra);
			if (typeof fn !== "function") throw new TemplateError(`${desc(node.c)} 不是函数`);
			const args = node.args.map((a) => evalNode(a, scope, extra));
			return fn(...args);
		}
		case "bin": {
			const op = node.op;
			if (op === "&&") { const l = evalNode(node.l, scope, extra); return l ? evalNode(node.r, scope, extra) : l; }
			if (op === "||") { const l = evalNode(node.l, scope, extra); return l ? l : evalNode(node.r, scope, extra); }
			if (op === "??") { const l = evalNode(node.l, scope, extra); return (l === null || l === undefined) ? evalNode(node.r, scope, extra) : l; }
			const l = evalNode(node.l, scope, extra);
			const r = evalNode(node.r, scope, extra);
			switch (op) {
				case "+": return l + r;
				case "-": return l - r;
				case "*": return l * r;
				case "/": return l / r;
				case "%": return l % r;
				case "<": return l < r;
				case ">": return l > r;
				case "<=": return l <= r;
				case ">=": return l >= r;
				case "===": return l === r;
				case "!==": return l !== r;
				case "==": return l == r; // eslint-disable-line eqeqeq
				case "!=": return l != r; // eslint-disable-line eqeqeq
				default: throw new TemplateError(`不支持的运算符 ${op}`);
			}
		}
		case "un": {
			const v = evalNode(node.a, scope, extra);
			switch (node.op) {
				case "!": return !v;
				case "-": return -v;
				case "+": return +v;
				default: throw new TemplateError(`不支持的一元运算符 ${node.op}`);
			}
		}
		case "cond": return evalNode(node.test, scope, extra) ? evalNode(node.a, scope, extra) : evalNode(node.b, scope, extra);
		case "arr": return node.items.map((x) => evalNode(x, scope, extra));
		case "obj": {
			const o = {};
			for (const p of node.props) o[p.k] = evalNode(p.v, scope, extra);
			return o;
		}
		case "assign": return evalAssign(node, scope, extra);
		default: throw new TemplateError(`未知表达式节点 ${node.t}`);
	}
}

function evalAssign(node, scope, extra) {
	const v = evalNode(node.v, scope, extra);
	const t = node.target;
	if (t.t === "id") {
		const n = t.n;
		if (scope === null || !(n in scope)) {
			if (n in GLOBALS) throw new TemplateError(`不允许给全局 "${n}" 赋值`);
			throw new TemplateError(`"${n}" 不在作用域内，不能赋值`);
		}
		scope[n] = v;
		return v;
	}
	if (t.t === "mem") {
		const o = evalNode(t.o, scope, extra);
		if (o === null || o === undefined) {
			if (t.opt) return v;
			throw new TemplateError(`对空值赋属性 "${t.p}"`);
		}
		try {
			o[t.p] = v;
		} catch (e) {
			throw new TemplateError(`赋值失败：${e.message}`);
		}
		return v;
	}
	if (t.t === "idx") {
		const o = evalNode(t.o, scope, extra);
		if (o === null || o === undefined) throw new TemplateError("对空值赋下标");
		o[evalNode(t.e, scope, extra)] = v;
		return v;
	}
	throw new TemplateError("赋值目标必须是标识符或属性路径");
}

function desc(node) {
	if (node.t === "id") return `"${node.n}"`;
	if (node.t === "mem") return `${desc(node.o)}.${node.p}`;
	if (node.t === "idx") return `${desc(node.o)}[...]`;
	return "表达式";
}

// 模板插值的显示转换（与 Vue toDisplayString 语义一致）。
export function toDisplayString(v) {
	if (v === null || v === undefined) return "";
	if (typeof v === "string") return v;
	if (typeof v === "number" || typeof v === "boolean") return String(v);
	try { return JSON.stringify(v); } catch { return String(v); }
}

// ---------------------------------------------------------------------------
// 指令解析辅助
// ---------------------------------------------------------------------------

function parseVFor(expr, pos) {
	// 形态：item in list | (item, index) in list | item of list
	const m = /^\s*(?:\(\s*([A-Za-z_$][\w$]*)\s*,\s*([A-Za-z_$][\w$]*)\s*\)|([A-Za-z_$][\w$]*))\s+(?:in|of)\s+([\s\S]+)$/.exec(expr);
	if (!m) throw new TemplateError(`v-for 表达式不合法："${expr}"（应为 item in list 或 (item, index) in list）`, pos);
	return {
		item: m[3] ?? m[1],
		index: m[3] ? null : m[2],
		src: m[4].trim(),
	};
}

const EVENT_MODS = new Set(["prevent", "stop", "once", "self"]);
const RESERVED_TAGS = new Set(["template", "slot", "component", "keep-alive", "transition"]);

// ---------------------------------------------------------------------------
// 主编译：token 流 → 渲染计划
// ---------------------------------------------------------------------------

export function compileTemplate(src) {
	const tokens = tokenize(src);
	let i = 0;
	const plan = parseKids(null);
	if (i < tokens.length) throw new TemplateError(`存在未消费的模板记号（检查 </${tokens[i]?.tag}> 多余闭合）`, tokens[i]?.pos);
	return { plan };

	function parseKids(parentTag) {
		const kids = [];
		while (i < tokens.length) {
			const tk = tokens[i];
			if (tk.type === "text") {
				i++;
				for (const part of splitMustache(tk.text, tk.pos)) {
					if (part.type === "s") {
						const condensed = condense(part.v);
						if (condensed !== null) pushTextKids(kids, condensed);
					} else {
						kids.push({ k: "dtext", e: parseExprCached(part.expr, part.pos) });
					}
				}
				continue;
			}
			if (tk.type === "end") {
				if (parentTag === tk.tag) {
					i++;
					return kids;
				}
				throw new TemplateError(`</${tk.tag}> 与 <${parentTag ?? "?"}> 不匹配`, tk.pos);
			}
			kids.push(parseNode());
		}
		if (parentTag !== null) throw new TemplateError(`<${parentTag}> 未闭合`);
		return kids;
	}

	function parseNode() {
		const tk = tokens[i];
		// 收集本元素的指令（v-if 链按兄弟元素折叠）
		if (tk.type === "start") {
			const directives = tk.attrs.filter((a) => a.name === "v-if" || a.name === "v-else-if" || a.name === "v-else");
			if (directives.length > 0) return parseIfChain(directives);
			i++;
			const node = parseElement(tk);
			return node;
		}
		throw new TemplateError(`意外的记号类型 ${tk.type}`, tk.pos);
	}

	function parseIfChain(directives) {
		const branches = [];
		let fallback = null;
		let prevWasIf = false;
		while (i < tokens.length) {
			const tk = tokens[i];
			if (tk.type === "text") {
				// 兄弟元素之间的空白不打断 v-if 链（Vue 对空白-only 文本节点的同款处理）
				if (tk.text.trim() === "") { i++; continue; }
				break;
			}
			if (tk.type !== "start") break;
			const vif = tk.attrs.find((a) => a.name === "v-if");
			const velif = tk.attrs.find((a) => a.name === "v-else-if");
			const vel = tk.attrs.find((a) => a.name === "v-else");
			if (vif) {
				if (branches.length > 0 || fallback !== null) {
					// 空白不再分隔链之后，相邻的两个独立 v-if 链会走到这里：
					// 结束当前链（i 不动，由 parseKids 开新链），而不是报错。
					break;
				}
				branches.push({ e: parseExprCached(vif.value ?? "false", tk.pos), kids: openElement() });
				prevWasIf = true;
				continue;
			}
			if (velif) {
				if (!prevWasIf) throw new TemplateError("v-else-if 必须紧跟 v-if / v-else-if 分支", tk.pos);
				branches.push({ e: parseExprCached(velif.value ?? "false", tk.pos), kids: openElement() });
				continue;
			}
			if (vel) {
				if (!prevWasIf) throw new TemplateError("v-else 必须紧跟 v-if / v-else-if 分支", tk.pos);
				if (vel.value !== undefined && vel.value !== "") throw new TemplateError("v-else 不接受值", tk.pos);
				fallback = openElement();
				break;
			}
			break;
		}
		if (branches.length === 0) throw new TemplateError("v-if 链为空");
		return { k: "if", branches, fallback };
	}

	// 消费 start token 并解析到对应 end（供 v-if 链使用）。
	function openElement() {
		const tk = tokens[i];
		i++;
		const node = parseElement(tk);
		// 单根：if 分支内可能是单元素；包一层片段语义由解释器天然支持（数组）。
		return Array.isArray(node) ? node : [node];
	}

	function parseElement(tk) {
		const { tag, attrs, selfClosing, pos } = tk;
		if (RESERVED_TAGS.has(tag)) {
			throw new TemplateError(`<${tag}> 在 v1 模板中不支持：${tag === "slot" ? "插槽内容由宿主组件以 children 传入" : "请使用具体标签或 ui- 组件"}`, pos);
		}
		const comp = isUiComponent(tag);
		if (!comp && !isAllowedTag(tag)) {
			throw new TemplateError(
				tag === "script" ? INLINE_SCRIPT_MSG.replace("SFC 不允许内联 <script>", `模板不允许 <script>`) :
					`标签 <${tag}> 不在白名单（ui- 前缀为宿主组件）`, pos);
		}
		const props = [];
		let vfor = null;
		let keyExpr = null;
		for (const a of attrs) {
			const { name, value, pos: ap } = a;
			if (name === "v-if" || name === "v-else-if" || name === "v-else") {
				// 到达这里说明表达式已被 if 链逻辑消费（parseIfChain → openElement），跳过即可。
				continue;
			}
			if (name === "v-html") throw new TemplateError("v-html 被拒绝：渲染器不支持注入 HTML（安全不变量）", ap);
			if (name === "v-pre") throw new TemplateError("v-pre 在 v1 不支持", ap);
			if (name === "v-for") {
				vfor = parseVFor(value ?? "", ap);
				continue;
			}
			if (name === "key") { keyExpr = parseExprCached(value ?? "", ap); continue; }
			if (name === ":key" || name === "v-bind:key") { keyExpr = parseExprCached(value ?? "", ap); continue; }
			if (name === "v-text") {
				props.push({ n: "#text", e: parseExprCached(value ?? '""', ap) });
				continue;
			}
			if (name === "v-show") {
				props.push({ n: "hidden", e: { t: "un", op: "!", a: parseExprCached(value ?? "false", ap) } });
				continue;
			}
			if (name === "v-model") {
				pushVModel(props, tag, value ?? "", ap);
				continue;
			}
			if (name.startsWith("v-")) {
				throw new TemplateError(`不支持的指令 ${name}（v1 支持 v-if/else-if/else、v-for、v-show、v-model、v-text）`, ap);
			}
			if (name.startsWith("@") || name.startsWith("v-on:")) {
				const ev = name.startsWith("@") ? name.slice(1) : name.slice(6);
				if (ev === "") throw new TemplateError("事件绑定缺少事件名", ap);
				const parts = ev.split(".");
				const evt = parts[0];
				const mods = {};
				for (const m of parts.slice(1)) {
					if (!EVENT_MODS.has(m)) throw new TemplateError(`不支持的事件修饰符 .${m}`, ap);
					mods[m] = true;
				}
				props.push({ n: `on:${evt}`, e: parseExprCached(value ?? "", ap), mods });
				continue;
			}
			if (name.startsWith(":") || name.startsWith("v-bind:")) {
				const bn = name.startsWith(":") ? name.slice(1) : name.slice(7);
				if (bn === "") throw new TemplateError("动态绑定缺少属性名", ap);
				if (bn === "on" || bn.startsWith("on-")) throw new TemplateError("事件请用 @event 语法", ap);
				if (!comp && bn === "style") {
					// 原生元素：:style 仅接受对象/数组绑定，运行时走 CSSOM。
					props.push({ n: "#style", e: parseExprCached(value ?? "{}", ap) });
					continue;
				}
				if (!comp && bn === "class") {
					props.push({ n: "class", e: parseExprCached(value ?? '""', ap) });
					continue;
				}
				if (!comp) {
					const check = checkAttr(bn, undefined);
					if (check !== null) throw new TemplateError(`动态绑定 :${bn} 被拒绝：${check}`, ap);
				}
				props.push({ n: bn, e: parseExprCached(value ?? '""', ap) });
				continue;
			}
			// 静态属性：编译期过白名单（组件属性自由，但静态 on* 仍拒绝——事件必须走 @）。
			if (value !== undefined && value.includes("{{")) {
				throw new TemplateError(`静态属性 ${name} 不支持插值，请使用 :${name} 绑定`, ap);
			}
			if (comp) {
				if (/^on[a-zA-Z]/.test(name)) throw new TemplateError(`组件静态事件属性 ${name} 被拒绝：请使用 @event 语法`, ap);
				props.push({ n: name, s: value ?? "" });
				continue;
			}
			const check = checkAttr(name, value);
			if (check !== null) throw new TemplateError(`属性 ${name} 被拒绝：${check}`, ap);
			props.push({ n: name, s: value ?? "" });
		}
		const kids = selfClosing ? [] : parseKids(tag);
		const el = { k: "el", tag, comp, void: VOID_TAGS.has(tag), props, kids };
		if (vfor !== null) {
			return { k: "for", src: parseExprCached(vfor.src, pos), item: vfor.item, index: vfor.index, key: keyExpr, kids: [el] };
		}
		// key 不配 v-for 直接报错（无意义的静态 key）
		if (keyExpr !== null && vfor === null) throw new TemplateError(":key 只在 v-for 里有意义", pos);
		return el;
	}

	function pushVModel(props, tag, expr, pos) {
		const target = parseExprCached(expr, pos);
		if (!["id", "mem", "idx"].includes(target.t)) {
			throw new TemplateError("v-model 目标必须是可赋值路径（如 form.name）", pos);
		}
		if (tag === "input") {
			const type = findStaticAttr(props, "type") ?? "text";
			if (type === "checkbox") {
				props.push({ n: "checked", e: target });
				props.push({ n: "on:change", e: { t: "assign", target, v: { t: "mem", o: { t: "mem", o: { t: "id", n: "$event" }, p: "target", opt: false }, p: "checked", opt: false } } });
				return;
			}
			if (type === "radio") throw new TemplateError("v1 的 v-model 不支持 radio（请用 @change 显式处理）", pos);
			props.push({ n: "value", e: target });
			props.push({ n: "on:input", e: { t: "assign", target, v: { t: "mem", o: { t: "mem", o: { t: "id", n: "$event" }, p: "target", opt: false }, p: "value", opt: false } } });
			return;
		}
		if (tag === "textarea") {
			props.push({ n: "value", e: target });
			props.push({ n: "on:input", e: { t: "assign", target, v: { t: "mem", o: { t: "mem", o: { t: "id", n: "$event" }, p: "target", opt: false }, p: "value", opt: false } } });
			return;
		}
		if (tag === "select") {
			props.push({ n: "value", e: target });
			props.push({ n: "on:change", e: { t: "assign", target, v: { t: "mem", o: { t: "mem", o: { t: "id", n: "$event" }, p: "target", opt: false }, p: "value", opt: false } } });
			return;
		}
		throw new TemplateError(`v-model 只支持 input/textarea/select，收到 <${tag}>`, pos);
	}

	function findStaticAttr(props, name) {
		for (const p of props) if (p.n === name && p.s !== undefined) return p.s;
		return null;
	}
}

// 文本折叠（Vue condense 语义）：\s+ 折叠成单空格；含换行的全空白片段丢弃，
// 其余片段保留单个边缘空格（插值相邻的空格是有意义的）。
function condense(text) {
	const hasNL = /\n/.test(text);
	let s = text.replace(/\s+/g, " ");
	if (hasNL) s = s.trim();
	if (s.length === 0) return hasNL ? null : " ";
	return s;
}

// 按 {{ expr }} 切分文本；未闭合的 {{ 报错。
function splitMustache(text, pos) {
	const parts = [];
	let last = 0;
	const re = /\{\{([\s\S]+?)\}\}/g;
	let m;
	while ((m = re.exec(text)) !== null) {
		if (m.index > last) parts.push({ type: "s", v: text.slice(last, m.index) });
		const expr = m[1].trim();
		if (expr.length === 0) throw new TemplateError("插值表达式为空", pos);
		if (BANNED_HAS(expr)) throw new TemplateError(`插值包含禁止标识符`, pos);
		parts.push({ type: "e", expr, pos });
		last = re.lastIndex;
	}
	if (last < text.length) {
		const rest = text.slice(last);
		if (rest.includes("{{")) throw new TemplateError("插值 {{ 未闭合", pos);
		parts.push({ type: "s", v: rest });
	}
	return parts;
}

function BANNED_HAS(expr) {
	return /__(?:proto|defineGetter|defineSetter)__|\bconstructor\b|\bprototype\b|\beval\b|\bFunction\b|\bwindow\b|\bdocument\b|\bglobalThis\b|\bfetch\b/.test(expr);
}

// 相邻静态文本合并。
function pushTextKids(kids, text) {
	const last = kids[kids.length - 1];
	if (last !== undefined && last.k === "text") last.v += text;
	else kids.push({ k: "text", v: text });
}
