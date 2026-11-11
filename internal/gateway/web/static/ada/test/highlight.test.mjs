// highlight.js 门禁测试 —— 插件预览客户端高亮器的契约。
//
// 契约三条：
//  1. 每语言至少两个 token 断言（不是「有高亮」而是「这个位置是这类 token」）；
//  2. token 首尾相接等于原输入（round-trip 不变量：高亮不能吞字/造字）；
//  3. 恶意/病态输入不崩、不爆 token 数（未闭合字符串、二进制垃圾、巨长行）。
import { test } from "node:test";
import assert from "node:assert/strict";

import { tokenize, normalizeLang, LANGUAGES } from "../highlight.js";

const join = (tokens) => tokens.map((t) => t.text).join("");
const cls = (text, lang, want) => {
	const tokens = tokenize(text, lang);
	const hit = tokens.find((t) => t.text === text);
	assert.ok(hit !== undefined, `${lang}: 找不到 token "${text}"（得到 ${JSON.stringify(tokens)}）`);
	assert.equal(hit.cls, want, `${lang}: "${text}" 的 class 期望 ${want}，实得 "${hit.cls}"`);
};

test("语言归一化：别名折叠，未知返回空串", () => {
	assert.equal(normalizeLang("JavaScript"), "js");
	assert.equal(normalizeLang("typescript"), "ts");
	assert.equal(normalizeLang("tsx"), "tsx");
	assert.equal(normalizeLang("Golang"), "go");
	assert.equal(normalizeLang("python3"), "py");
	assert.equal(normalizeLang("bash"), "sh");
	assert.equal(normalizeLang("yml"), "yaml");
	assert.equal(normalizeLang("rs"), "rs");
	assert.equal(normalizeLang("brainfuck"), "");
	assert.equal(normalizeLang(""), "");
	assert.deepEqual(LANGUAGES.slice(0, 3), ["js", "ts", "tsx"]);
});

test("js：关键字/字符串/注释/数字/函数名", () => {
	cls("const", "js", "k");
	cls("return", "js", "k");
	cls("\"hi\"", "js", "s");
	cls("42", "js", "m");
	assert.equal(tokenize("// 注释", "js")[0].cls, "c");
	assert.equal(tokenize("function boot() {}", "js").find((t) => t.text === "boot").cls, "nf");
});

test("ts：关键字 + 类型标注（interface 后的名字是类型）", () => {
	cls("interface", "ts", "k");
	cls("readonly", "ts", "k");
	assert.equal(tokenize("interface Widget {}", "ts").find((t) => t.text === "Widget").cls, "nc");
	cls("\"utf8\"", "ts", "s");
	cls("string", "ts", "nb");
	assert.equal(tokenize("// 注释", "ts")[0].cls, "c");
});

test("tsx / jsx：标签内的代码仍按 TS/JS 分词", () => {
	cls("export", "tsx", "k");
	assert.equal(tokenize("useState(0)", "tsx").find((t) => t.text === "useState").cls, "nf");
	cls("true", "jsx", "k");
	assert.equal(tokenize("const el = null;", "jsx").find((t) => t.text === "null").cls, "k");
	assert.equal(tokenize("/* 块注释 */", "tsx")[0].cls, "c");
});

test("go：关键字/字符串/注释/数字/类型", () => {
	cls("func", "go", "k");
	cls("package", "go", "k");
	cls("\"fmt\"", "go", "s");
	cls("42", "go", "m");
	cls("string", "go", "nb");
	assert.equal(tokenize("// 注释", "go")[0].cls, "c");
	assert.equal(tokenize("type Row struct {}", "go").find((t) => t.text === "Row").cls, "nc");
});

test("py：关键字/字符串/注释/内建/函数名", () => {
	cls("def", "py", "k");
	cls("None", "py", "k");
	cls("\"hi\"", "py", "s");
	cls("print", "py", "nb");
	assert.equal(tokenize("# 注释", "py")[0].cls, "c");
	assert.equal(tokenize("def boot(x):", "py").find((t) => t.text === "boot").cls, "nf");
});

test("rs：关键字/字符串/注释/类型/函数名", () => {
	cls("fn", "rs", "k");
	cls("let", "rs", "k");
	cls("\"hi\"", "rs", "s");
	cls("u32", "rs", "nb");
	assert.equal(tokenize("// 注释", "rs")[0].cls, "c");
	assert.equal(tokenize("fn boot() {}", "rs").find((t) => t.text === "boot").cls, "nf");
});

test("json：键是字符串、字面量是关键字的颜色", () => {
	cls("\"name\"", "json", "s");
	cls("true", "json", "k");
	cls("null", "json", "k");
	cls("3", "json", "m");
	// JSON 没有注释语法：// 是普通文本
	assert.equal(tokenize("// nope", "json")[0].cls, "");
});

test("sh：关键字/变量/字符串/注释/命令", () => {
	cls("if", "sh", "k");
	cls("$HOME", "sh", "nv");
	cls("\"$x\"", "sh", "s");
	cls("echo", "sh", "nb");
	assert.equal(tokenize("# 注释", "sh")[0].cls, "c");
});

test("yaml：键是 nt、注释是 c、值里的字符串是 s", () => {
	const key = tokenize("name: ada", "yaml")[0];
	assert.deepEqual(key, { cls: "nt", text: "name" });
	assert.equal(tokenize("# 注释", "yaml")[0].cls, "c");
	assert.equal(tokenize('a: "ada"', "yaml").find((t) => t.text === "\"ada\"").cls, "s");
	assert.equal(tokenize("a: true", "yaml").find((t) => t.text === "true").cls, "k");
	// 列表项与缩进保持普通文本
	const list = tokenize("- key: value", "yaml");
	assert.equal(list[0].text, "- ");
	assert.equal(list[0].cls, "");
});

test("toml：表名是 nt、键是 na、字符串是 s", () => {
	const table = tokenize("[plugin]", "toml")[0];
	assert.deepEqual(table, { cls: "nt", text: "[plugin]" });
	const key = tokenize('id = "demo"', "toml")[0];
	assert.deepEqual(key, { cls: "na", text: "id" });
	assert.equal(tokenize('id = "demo"', "toml").find((t) => t.text === "\"demo\"").cls, "s");
	assert.equal(tokenize("# 注释", "toml")[0].cls, "c");
	// 带引号的版本号是字符串（不是数字）
	assert.equal(tokenize('v = "1.0.0"', "toml").find((t) => t.text === "\"1.0.0\"").cls, "s");
});

test("round-trip：每种语言 token 拼接后等于原输入", () => {
	const samples = {
		js: "// c\nconst x = f(\"s\", 42);\n/* b */",
		ts: "interface A { readonly x: string }\nexport const y = 1;",
		tsx: "<div class=\"a\">{x}</div>",
		jsx: "const el = null;",
		go: "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n",
		py: "def f(a):\n    return print(a)  # 注释\n",
		rs: "fn main() { let s = \"x\"; }",
		json: "{\"a\": [1, true, null]}",
		sh: "if [ \"$1\" = x ]; then echo \"$1\"; fi\n",
		yaml: "a: 1\nb:\n  - c: \"d\"\n# e\n",
		toml: "[t]\nid = \"demo\"\nn = 3 # c\n",
	};
	for (const [lang, text] of Object.entries(samples)) {
		const tokens = tokenize(text, lang);
		assert.equal(join(tokens), text, `${lang}: round-trip 失败`);
	}
});

test("未知语言与空输入：整段纯文本", () => {
	assert.deepEqual(tokenize("SELECT 1", "sql"), [{ cls: "", text: "SELECT 1" }]);
	assert.deepEqual(tokenize("", "js"), []);
	assert.deepEqual(tokenize(null, "js"), []);
	assert.deepEqual(tokenize(undefined, "js"), []);
	// 退化路径也必须 round-trip
	assert.equal(join(tokenize("some <b>text</b>", "")), "some <b>text</b>");
});

test("恶意输入不崩：未闭合字符串/注释、NUL、超长行、超长文本", () => {
	const nasty = [
		"\"unterminated",
		"/* never closed",
		"'x\n'y",
		"`template ${ not evaluated",
		"a\u0000b\u0000c",
		"<script>alert(1)</script>",
		"\\".repeat(2000),
		"0".repeat(5000),
		"x".repeat(20000),
		"\uFFFD\uD800",
	];
	for (const text of nasty) {
		for (const lang of LANGUAGES) {
			let tokens;
			assert.doesNotThrow(() => { tokens = tokenize(text, lang); }, `${lang}: 恶意输入抛错了`);
			assert.equal(join(tokens), text, `${lang}: 恶意输入 round-trip 失败`);
			assert.ok(tokens.length <= 8000, `${lang}: token 数爆炸（${tokens.length}）`);
		}
	}
});

test("超限文本：截断处不打乱 round-trip，token 数保持有界", () => {
	const big = "const a = 1;\n".repeat(8000); // ~78KB，超过 60KB 上限
	const tokens = tokenize(big, "js");
	assert.equal(join(tokens), big, "截断后的尾部必须原样保留");
	assert.ok(tokens.length <= 8000, `token 数应被截断保护压住，实得 ${tokens.length}`);
});
