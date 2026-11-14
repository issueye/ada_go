// highlight.js —— 客户端轻量语法高亮（零依赖，纯正则 + 小分词）。
//
// 用途：插件详情页的源码预览。HEAD 是服务端 chroma 渲染；SSR 移除后由这里
// 在客户端做同一件事的最小版本——产出 token 数组，由宿主组件 ui-code 拼 VNode
//（不碰 innerHTML，CSP 不需要放宽，也不新增任何依赖）。
//
// 约定：
//   tokenize(text, lang) → [{ cls, text }]
//   cls 对齐 chroma 的 token class 子集（app-local.css 里 .chroma .k/.s/.c/…
//   的配色规则直接生效，渲染容器要带 class="chroma"）；cls === "" 是普通文本。
//   未知语言/空文本 → 单段纯文本，不猜语言（HEAD 的 lexers.Analyse 嗅探不移植）。
//   纯函数：不读全局、不抛错；未闭合的字符串/注释、二进制垃圾、超长行都只
//   退化为普通文本，且各段 text 首尾相接后等于原输入（round-trip 不变量）。
//
// 支持：js/ts/tsx/jsx、go、py、rs、json、sh、yaml、toml（含常见别名）。
// 刻意不做的事：正则字面量、模板字符串插值、HTML/JSX 标签高亮、跨语言通用
// 语法树——这些是 chroma 的能力，这里只保证「像代码」而不是「语义正确」。

// 扫描上限：超过就不再「伪高亮」，整段退化为纯文本（宁可不高亮也不卡住页面）。
//   MAX_CHARS —— 只扫描前 64KB（服务端预览本身也截断在 512KB 以内）；
//   MAX_TOKENS —— 一个 token 在宿主组件里是一个 span 节点。4000 覆盖插件包内
//   常见源码（几 KB 到十几 KB）；再大的文件退回纯文本，不制造上万节点。
//   （宿主组件走 render 函数，不受模板渲染计划的 MAX_NODES 预算约束，这里
//   的上限是为了浏览器侧的创建/patch 成本。）
const MAX_CHARS = 64 * 1024;
const MAX_TOKENS = 4000;

// 数字字面量（各语言取并集；sticky 正则从当前位置匹配）。
const NUM_RE_SRC =
	"0[xX][0-9a-fA-F_]+|0[bB][01_]+|0[oO][0-7_]+|\\d[\\d_]*(?:\\.[\\d_]+)?(?:[eE][+-]?[\\d_]+)?";

const IDENT_RE_SRC = "[A-Za-z_$][A-Za-z0-9_$]*";

const JS_KEYWORDS = new Set([
	"as", "async", "await", "break", "case", "catch", "class", "const", "continue",
	"debugger", "default", "delete", "do", "else", "export", "extends", "finally",
	"for", "from", "function", "get", "if", "import", "in", "instanceof", "let",
	"new", "of", "return", "set", "static", "super", "switch", "this", "throw",
	"try", "typeof", "var", "void", "while", "with", "yield",
	"true", "false", "null", "undefined",
	"NaN", "Infinity",
]);

const TS_KEYWORDS = new Set([
	...JS_KEYWORDS,
	"abstract", "declare", "enum", "implements", "infer", "interface", "is", "keyof",
	"module", "namespace", "never", "override", "private", "protected", "public",
	"readonly", "satisfies", "type", "unique", "unknown", "any", "asserts",
]);

const TS_TYPES = new Set([
	"string", "number", "boolean", "bigint", "symbol", "object", "void",
	"Array", "Object", "Promise", "Map", "Set", "Record", "Partial", "Readonly",
]);

const GO_KEYWORDS = new Set([
	"break", "case", "chan", "const", "continue", "default", "defer", "else",
	"fallthrough", "for", "func", "go", "goto", "if", "import", "interface", "map",
	"package", "range", "return", "select", "struct", "switch", "type", "var",
	"true", "false", "nil", "iota",
]);

const GO_TYPES = new Set([
	"bool", "byte", "complex64", "complex128", "error", "float32", "float64",
	"int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8",
	"uint16", "uint32", "uint64", "uintptr", "any",
]);

const PY_KEYWORDS = new Set([
	"and", "as", "assert", "async", "await", "break", "class", "continue", "def",
	"del", "elif", "else", "except", "finally", "for", "from", "global", "if",
	"import", "in", "is", "lambda", "match", "case", "nonlocal", "not", "or",
	"pass", "raise", "return", "try", "while", "with", "yield",
	"True", "False", "None",
]);

const PY_BUILTINS = new Set([
	"print", "len", "range", "str", "int", "float", "list", "dict", "set", "tuple",
	"bool", "bytes", "open", "super", "isinstance", "issubclass", "type", "enumerate",
	"zip", "sum", "min", "max", "sorted", "abs", "input", "format", "repr", "map",
	"filter", "Exception", "BaseException", "ValueError", "KeyError", "TypeError",
]);

const RS_KEYWORDS = new Set([
	"as", "async", "await", "break", "const", "continue", "crate", "dyn", "else",
	"enum", "extern", "fn", "for", "if", "impl", "in", "let", "loop", "match",
	"mod", "move", "mut", "pub", "ref", "return", "self", "Self", "static",
	"struct", "super", "trait", "type", "union", "unsafe", "use", "where",
	"while", "true", "false",
]);

const RS_TYPES = new Set([
	"i8", "i16", "i32", "i64", "i128", "isize", "u8", "u16", "u32", "u64", "u128",
	"usize", "f32", "f64", "bool", "char", "str", "String", "Vec", "Option",
	"Result", "Box", "Rc", "Arc", "HashMap", "HashSet", "Some", "None", "Ok", "Err",
]);

const SH_KEYWORDS = new Set([
	"if", "then", "elif", "else", "fi", "for", "while", "until", "do", "done",
	"case", "esac", "in", "function", "return", "local", "export", "unset",
	"shift", "source", "alias", "set", "select", "time", "declare",
]);

const SH_COMMANDS = new Set([
	"echo", "cd", "pwd", "cat", "grep", "sed", "awk", "git", "ls", "cp", "mv",
	"rm", "mkdir", "rmdir", "touch", "curl", "wget", "npm", "node", "go", "make",
	"docker", "kubectl", "test", "printf", "read", "exec", "env", "sleep",
]);

const JSON_KEYWORDS = new Set(["true", "false", "null"]);

const JS_STRINGS = [
	{ open: '"', close: '"' },
	{ open: "'", close: "'" },
	{ open: "`", close: "`", multiline: true },
];

const PY_STRINGS = [
	{ open: '"""', close: '"""', multiline: true },
	{ open: "'''", close: "'''", multiline: true },
	{ open: '"', close: '"' },
	{ open: "'", close: "'" },
];

const RS_STRINGS = [
	{ open: '"', close: '"', esc: true },
];

// 每种语言的扫描配置。strings 按 open 长度降序匹配（三引号优先）。
const LANGS = {
	js: {
		ident: IDENT_RE_SRC,
		line: ["//"],
		block: [["/*", "*/"]],
		strings: JS_STRINGS,
		keywords: JS_KEYWORDS,
		typeLeads: new Set(["class"]),
		funcCall: true,
	},
	ts: {
		ident: IDENT_RE_SRC,
		line: ["//"],
		block: [["/*", "*/"]],
		strings: JS_STRINGS,
		keywords: TS_KEYWORDS,
		builtins: TS_TYPES,
		typeLeads: new Set(["class", "interface", "type", "enum", "namespace"]),
		funcCall: true,
	},
	jsx: {
		ident: IDENT_RE_SRC,
		line: ["//"],
		block: [["/*", "*/"]],
		strings: JS_STRINGS,
		keywords: JS_KEYWORDS,
		typeLeads: new Set(["class"]),
		funcCall: true,
	},
	tsx: {
		ident: IDENT_RE_SRC,
		line: ["//"],
		block: [["/*", "*/"]],
		strings: JS_STRINGS,
		keywords: TS_KEYWORDS,
		builtins: TS_TYPES,
		typeLeads: new Set(["class", "interface", "type", "enum", "namespace"]),
		funcCall: true,
	},
	go: {
		ident: IDENT_RE_SRC,
		line: ["//"],
		block: [["/*", "*/"]],
		strings: [
			{ open: "`", close: "`", multiline: true },
			{ open: '"', close: '"', esc: true },
			{ open: "'", close: "'", esc: true },
		],
		keywords: GO_KEYWORDS,
		builtins: GO_TYPES,
		typeLeads: new Set(["type"]),
		funcCall: true,
	},
	py: {
		ident: IDENT_RE_SRC,
		line: ["#"],
		strings: PY_STRINGS,
		keywords: PY_KEYWORDS,
		builtins: PY_BUILTINS,
		typeLeads: new Set(["class"]),
		funcCall: true,
	},
	rs: {
		ident: IDENT_RE_SRC,
		line: ["//"],
		block: [["/*", "*/"]],
		strings: RS_STRINGS,
		keywords: RS_KEYWORDS,
		builtins: RS_TYPES,
		typeLeads: new Set(["struct", "enum", "trait", "impl", "type", "union"]),
		funcCall: true,
	},
	json: {
		ident: IDENT_RE_SRC,
		strings: [{ open: '"', close: '"', esc: true }],
		keywords: JSON_KEYWORDS,
	},
	sh: {
		ident: IDENT_RE_SRC,
		line: ["#"],
		strings: [
			{ open: '"', close: '"', esc: true },
			{ open: "'", close: "'" },
		],
		keywords: SH_KEYWORDS,
		builtins: SH_COMMANDS,
		varPrefix: "$",
	},
	yaml: { kind: "yaml" },
	toml: { kind: "toml" },
};

const ALIASES = {
	javascript: "js", js: "js", mjs: "js", cjs: "js", jsx: "jsx",
	typescript: "ts", ts: "ts", mts: "ts", cts: "ts", tsx: "tsx",
	golang: "go", go: "go",
	python: "py", python3: "py", py: "py",
	rust: "rs", rs: "rs",
	json: "json", jsonc: "json", json5: "json",
	bash: "sh", shell: "sh", zsh: "sh", sh: "sh",
	yaml: "yaml", yml: "yaml",
	toml: "toml", ini: "toml",
};

// 常见渲染目标语言名（供 UI 展示；不含未知语言）。
export const LANGUAGES = ["js", "ts", "tsx", "jsx", "go", "py", "rs", "json", "sh", "yaml", "toml"];

// 归一化语言名；不认识的返回 ""。
export function normalizeLang(lang) {
	const key = String(lang ?? "").trim().toLowerCase();
	return ALIASES[key] ?? "";
}

// tokenize(text, lang) → [{ cls, text }]（纯函数，见文件头的不变量）。
export function tokenize(text, lang) {
	const src = String(text ?? "");
	if (src === "") return [];
	const id = normalizeLang(lang);
	const cfg = id !== "" ? LANGS[id] : undefined;
	if (cfg === undefined) return [{ cls: "", text: src }];
	const body = src.length > MAX_CHARS ? src.slice(0, MAX_CHARS) : src;
	const tail = src.length > MAX_CHARS ? src.slice(MAX_CHARS) : "";
	const out = cfg.kind === "yaml" ? scanYaml(body)
		: cfg.kind === "toml" ? scanToml(body)
			: scanCode(body, cfg);
	if (out.length > MAX_TOKENS) {
		// token 太多说明是病态输入：整段退化为纯文本，避免渲染出上万节点。
		return [{ cls: "", text: src }];
	}
	if (tail !== "") appendPlain(out, tail);
	return out;
}

// ---------------------------------------------------------------------------
// 通用扫描
// ---------------------------------------------------------------------------

function scanCode(src, cfg) {
	const out = [];
	const identRe = new RegExp(cfg.ident ?? IDENT_RE_SRC, "y");
	const numRe = new RegExp(NUM_RE_SRC, "y");
	let plain = "";
	const flush = () => {
		if (plain !== "") {
			out.push({ cls: "", text: plain });
			plain = "";
		}
	};
	let pendingType = false;
	let i = 0;
	while (i < src.length) {
		if (out.length > MAX_TOKENS) break;
		const ch = src[i];

		// 行注释
		if (cfg.line !== undefined) {
			let hit = false;
			for (const marker of cfg.line) {
				if (src.startsWith(marker, i)) {
					const stop = lineEnd(src, i);
					flush();
					out.push({ cls: "c", text: src.slice(i, stop) });
					i = stop;
					hit = true;
					break;
				}
			}
			// 行首注释要求前置空白（YAML/TOML 语义），其余语言任意位置都算。
			if (hit) {
				pendingType = false;
				continue;
			}
		}

		// 块注释
		if (cfg.block !== undefined) {
			let hit = false;
			for (const [open, close] of cfg.block) {
				if (src.startsWith(open, i)) {
					const at = src.indexOf(close, i + open.length);
					const stop = at < 0 ? src.length : at + close.length;
					flush();
					out.push({ cls: "c", text: src.slice(i, stop) });
					i = stop;
					hit = true;
					break;
				}
			}
			if (hit) {
				pendingType = false;
				continue;
			}
		}

		// 字符串（open 长的先试：三引号不会被单引号抢先吃掉）
		{
			let hit = false;
			for (const spec of cfg.strings) {
				if (src.startsWith(spec.open, i)) {
					const stop = stringEnd(src, i, spec);
					flush();
					out.push({ cls: "s", text: src.slice(i, stop) });
					i = stop;
					hit = true;
					break;
				}
			}
			if (hit) {
				pendingType = false;
				continue;
			}
		}

		// shell 变量：$NAME / ${NAME}
		if (cfg.varPrefix === "$" && ch === "$") {
			identRe.lastIndex = i + 1;
			const m = identRe.exec(src);
			if (m !== null && m.index === i + 1) {
				flush();
				out.push({ cls: "nv", text: src.slice(i, i + 1 + m[0].length) });
				i = i + 1 + m[0].length;
				pendingType = false;
				continue;
			}
		}

		// 数字
		if (ch >= "0" && ch <= "9") {
			numRe.lastIndex = i;
			const m = numRe.exec(src);
			if (m !== null && m.index === i && m[0].length > 0) {
				flush();
				out.push({ cls: "m", text: m[0] });
				i += m[0].length;
				pendingType = false;
				continue;
			}
		}

		// 标识符：关键字 / 类型名 / 内建 / 函数名 / 普通
		if (isIdentStart(ch, cfg)) {
			identRe.lastIndex = i;
			const m = identRe.exec(src);
			if (m !== null && m.index === i && m[0].length > 0) {
				const word = m[0];
				const end = i + word.length;
				let cls = "";
				if (cfg.keywords !== undefined && cfg.keywords.has(word)) {
					cls = "k";
					pendingType = cfg.typeLeads !== undefined && cfg.typeLeads.has(word);
				} else if (pendingType) {
					cls = "nc";
					pendingType = false;
				} else if (cfg.builtins !== undefined && cfg.builtins.has(word)) {
					cls = "nb";
				} else if (cfg.funcCall === true && nextNonSpace(src, end) === "(") {
					cls = "nf";
				}
				if (cls === "") plain += word;
				else {
					flush();
					out.push({ cls, text: word });
				}
				i = end;
				continue;
			}
		}

		plain += ch;
		i += 1;
	}
	if (i < src.length) plain += src.slice(i);
	flush();
	return out;
}

function isIdentStart(ch, cfg) {
	if ((ch >= "A" && ch <= "Z") || (ch >= "a" && ch <= "z") || ch === "_") return true;
	return cfg.ident !== undefined && ch === "$";
}

function nextNonSpace(src, at) {
	let i = at;
	while (i < src.length && (src[i] === " " || src[i] === "\t")) i += 1;
	return src[i] ?? "";
}

function lineEnd(src, at) {
	const nl = src.indexOf("\n", at);
	return nl < 0 ? src.length : nl;
}

function stringEnd(src, start, spec) {
	let i = start + spec.open.length;
	while (i < src.length) {
		const ch = src[i];
		if (spec.esc === true && ch === "\\") {
			i += 2;
			continue;
		}
		if (src.startsWith(spec.close, i)) return i + spec.close.length;
		if (spec.multiline !== true && ch === "\n") return i;
		i += 1;
	}
	return src.length;
}

function appendPlain(out, text) {
	if (text === "") return;
	const last = out[out.length - 1];
	if (last !== undefined && last.cls === "") last.text += text;
	else out.push({ cls: "", text });
}

// ---------------------------------------------------------------------------
// YAML / TOML：按行处理（key 的位置信息只有行首才能可靠判断）
// ---------------------------------------------------------------------------

const YAML_KEY_RE = /^([A-Za-z_][\w.-]*|"[^"]*"|'[^']*')\s*:(?=\s|$)/;

function scanYaml(src) {
	const out = [];
	const lines = src.split("\n");
	for (let n = 0; n < lines.length; n += 1) {
		if (n > 0) appendPlain(out, "\n");
		const line = lines[n];
		const indent = /^\s*/.exec(line)[0];
		let rest = line.slice(indent.length);
		appendPlain(out, indent);
		if (rest === "") continue;
		if (rest.startsWith("#")) {
			out.push({ cls: "c", text: rest });
			continue;
		}
		// 列表项：整块 "- " 当普通文本，后面继续按 key/值规则
		while (rest.startsWith("- ")) {
			appendPlain(out, "- ");
			rest = rest.slice(2);
			const more = /^\s*/.exec(rest)[0];
			appendPlain(out, more);
			rest = rest.slice(more.length);
		}
		const key = YAML_KEY_RE.exec(rest);
		if (key !== null) {
			const sep = rest.slice(key[1].length).match(/^\s*:/)[0];
			out.push({ cls: "nt", text: key[1] });
			appendPlain(out, sep);
			scanValue(out, rest.slice(key[1].length + sep.length));
			continue;
		}
		scanValue(out, rest);
	}
	return out;
}

const TOML_TABLE_RE = /^\[\[?[^\]]*\]\]?/;
const TOML_KEY_RE = /^([A-Za-z_][\w.-]*|"[^"]*")(\s*=\s*)/;

function scanToml(src) {
	const out = [];
	const lines = src.split("\n");
	for (let n = 0; n < lines.length; n += 1) {
		if (n > 0) appendPlain(out, "\n");
		const line = lines[n];
		const indent = /^\s*/.exec(line)[0];
		let rest = line.slice(indent.length);
		appendPlain(out, indent);
		if (rest === "") continue;
		if (rest.startsWith("#")) {
			out.push({ cls: "c", text: rest });
			continue;
		}
		const table = TOML_TABLE_RE.exec(rest);
		if (table !== null) {
			out.push({ cls: "nt", text: table[0] });
			scanValue(out, rest.slice(table[0].length));
			continue;
		}
		const key = TOML_KEY_RE.exec(rest);
		if (key !== null) {
			out.push({ cls: "na", text: key[1] });
			appendPlain(out, key[2]);
			scanValue(out, rest.slice(key[0].length));
			continue;
		}
		scanValue(out, rest);
	}
	return out;
}

// 值部分：字符串 / 数字 / 布尔 / 注释；其它字符保持普通文本。
function scanValue(out, text) {
	const numRe = new RegExp(NUM_RE_SRC, "y");
	let plain = "";
	const flush = () => {
		if (plain !== "") {
			out.push({ cls: "", text: plain });
			plain = "";
		}
	};
	let i = 0;
	while (i < text.length) {
		const ch = text[i];
		if (ch === "#") {
			// 值注释只认行内 " #"：`a#b` 是普通文本。
			const prev = text[i - 1] ?? "";
			if (i === 0 || prev === " " || prev === "\t") {
				flush();
				out.push({ cls: "c", text: text.slice(i) });
				return flush();
			}
		}
		if (ch === '"' || ch === "'") {
			const spec = { open: ch, close: ch, esc: ch === '"' };
			const stop = stringEnd(text, i, spec);
			flush();
			out.push({ cls: "s", text: text.slice(i, stop) });
			i = stop;
			if (i === text.length && !text.startsWith(spec.close, text.length - spec.close.length)) {
				// 未闭合：已消费到行尾，直接结束
			}
			continue;
		}
		if (ch >= "0" && ch <= "9") {
			numRe.lastIndex = i;
			const m = numRe.exec(text);
			if (m !== null && m.index === i && m[0].length > 0) {
				flush();
				out.push({ cls: "m", text: m[0] });
				i += m[0].length;
				continue;
			}
		}
		if ((ch >= "A" && ch <= "Z") || (ch >= "a" && ch <= "z") || ch === "_") {
			const m = /^[A-Za-z_][A-Za-z0-9_]*/.exec(text.slice(i));
			if (m !== null) {
				const word = m[0];
				if (word === "true" || word === "false" || word === "null") {
					flush();
					out.push({ cls: "k", text: word });
				} else {
					plain += word;
				}
				i += word.length;
				continue;
			}
		}
		plain += ch;
		i += 1;
	}
	flush();
}
