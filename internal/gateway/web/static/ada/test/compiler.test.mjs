// compiler 契约测试（node --test，零依赖）。
import { test } from "node:test";
import assert from "node:assert/strict";
import { compileTemplate, evaluate, toDisplayString, TemplateError } from "../compiler.mjs";

test("静态模板：文本、元素、void、self-closing", () => {
	const { plan } = compileTemplate("<div class=ext-x><p>你好</p><br/><hr></div>");
	assert.deepEqual(plan, [{
		k: "el", tag: "div", comp: false, void: false,
		props: [{ n: "class", s: "ext-x" }],
		kids: [
			{ k: "el", tag: "p", comp: false, void: false, props: [], kids: [{ k: "text", v: "你好" }] },
			{ k: "el", tag: "br", comp: false, void: true, props: [], kids: [] },
			{ k: "el", tag: "hr", comp: false, void: true, props: [], kids: [] },
		],
	}]);
});

test("空白折叠：换行缩进丢弃、文本内部折叠为单空格", () => {
	const { plan } = compileTemplate("<p>\n  a  b\t\n</p>\n<span>ok</span>");
	const div = { k: "el", tag: "p", comp: false, void: false, props: [], kids: [{ k: "text", v: "a b" }] };
	assert.deepEqual(plan, [div, { k: "el", tag: "span", comp: false, void: false, props: [], kids: [{ k: "text", v: "ok" }] }]);
});

test("注释被丢弃", () => {
	const { plan } = compileTemplate("<!-- 注释 --><p>x</p>");
	assert.equal(plan.length, 1);
});

test("插值拆分：静态与 dtext 相邻合并", () => {
	const { plan } = compileTemplate("<p>共 {{ total }} 项，{{ done }} 完成</p>");
	const kids = plan[0].kids;
	assert.equal(kids.length, 5);
	assert.deepEqual(kids[0], { k: "text", v: "共 " });
	assert.equal(kids[1].k, "dtext");
	assert.equal(kids[1].e.n, "total");
	assert.deepEqual(kids[2], { k: "text", v: " 项，" });
	assert.equal(kids[3].k, "dtext");
	assert.deepEqual(kids[4], { k: "text", v: " 完成" });
	assert.throws(() => compileTemplate("<p>{{ unclosed</p>"), /未闭合/);
});

test("ui- 前缀组件标记为 comp", () => {
	const { plan } = compileTemplate("<ui-badge tone='ok'>x</ui-badge>");
	assert.equal(plan[0].comp, true);
	assert.equal(plan[0].tag, "ui-badge");
});

test("v-if / v-else-if / v-else 折叠", () => {
	const { plan } = compileTemplate(
		"<p v-if='a === 1'>A</p><p v-else-if='b'>B</p><p v-else>C</p>");
	assert.equal(plan.length, 1);
	const node = plan[0];
	assert.equal(node.k, "if");
	assert.equal(node.branches.length, 2);
	assert.equal(node.branches[0].kids[0].kids[0].v, "A");
	// v-if 属性已被链逻辑消费，不再出现在元素 props 里
	assert.equal(node.branches[0].kids[0].props.length, 0);
	assert.equal(node.branches[1].e.n, "b");
	assert.deepEqual(node.fallback[0].kids[0].v, "C");
});

test("v-for：item / (item,index) / :key", () => {
	const a = compileTemplate("<li v-for='s in items' :key='s.id'>{{ s.text }}</li>");
	assert.equal(a.plan[0].k, "for");
	assert.equal(a.plan[0].item, "s");
	assert.equal(a.plan[0].index, null);
	assert.equal(a.plan[0].key.p, "id");
	const b = compileTemplate("<li v-for='(s, i) in items' :key='i'>{{ i }}.{{ s }}</li>");
	assert.equal(b.plan[0].item, "s");
	assert.equal(b.plan[0].index, "i");
});

test("v-show 折叠为 hidden 取反", () => {
	const { plan } = compileTemplate("<p v-show='open'>x</p>");
	assert.deepEqual(plan[0].props[0].n, "hidden");
	assert.equal(plan[0].props[0].e.t, "un");
});

test("v-model：text input → value + on:input 赋值", () => {
	const { plan } = compileTemplate("<input v-model='form.name'/>");
	const p = plan[0].props;
	assert.equal(p[0].n, "value");
	assert.equal(p[0].e.t, "mem");
	assert.equal(p[0].e.o.n, "form");
	assert.equal(p[1].n, "on:input");
	assert.equal(p[1].e.t, "assign");
});

test("v-model：checkbox → checked + on:change", () => {
	const { plan } = compileTemplate("<input type='checkbox' v-model='s.done'/>");
	const p = plan[0].props;
	assert.equal(p.find((x) => x.n === "checked").e.t, "mem");
	assert.equal(p.find((x) => x.n === "on:change").e.t, "assign");
});

test("事件绑定与修饰符", () => {
	const { plan } = compileTemplate("<button @click.prevent.stop='refresh()'>go</button>");
	const p = plan[0].props[0];
	assert.equal(p.n, "on:click");
	assert.deepEqual(p.mods, { prevent: true, stop: true });
	assert.equal(p.e.t, "call");
});

test("事件表达式内联赋值", () => {
	const { plan } = compileTemplate("<button @click='count = count + 1'>inc</button>");
	assert.equal(plan[0].props[0].e.t, "assign");
});

test("表达式：可选链、空值合并、数组/对象字面量、三元", () => {
	const { plan } = compileTemplate("<p :class='open ? [\"on\"] : {}' :data-x='user?.name ?? \"anon\"'>x</p>");
	const cls = plan[0].props.find((p) => p.n === "class");
	assert.equal(cls.e.t, "cond");
	const dx = plan[0].props.find((p) => p.n === "data-x");
	assert.equal(dx.e.op, "??");
});

test("evaluate：作用域与白名单全局", () => {
	const scope = { n: 3, items: ["a", "b"], user: { name: "ada" } };
	assert.equal(evaluate(compilePart("n + 1"), scope), 4);
	assert.equal(evaluate(compilePart("items.length"), scope), 2);
	assert.equal(evaluate(compilePart("user.name"), scope), "ada");
	assert.equal(evaluate(compilePart("Math.max(n, 9)"), scope), 9);
	assert.equal(evaluate(compilePart("JSON.stringify([n])"), scope), "[3]");
	assert.throws(() => evaluate(compilePart("window"), scope), TemplateError);
	assert.throws(() => evaluate(compilePart("missing"), scope), TemplateError);
	assert.throws(() => evaluate(compilePart("({}).constructor"), scope), TemplateError);
	assert.throws(() => evaluate(compilePart("obj.__proto__"), scope), TemplateError);
	assert.throws(() => evaluate(compilePart("Function('return 1')()"), scope), TemplateError);

	function compilePart(src) {
		const { plan } = compileTemplate(`<p>{{ ${src} }}</p>`);
		return plan[0].kids[0].e;
	}
});

test("evaluate：赋值与 $event", () => {
	const scope = { count: 1, form: { name: "" } };
	evaluate(compileEvent("count = count + 1"), scope, { event: null });
	assert.equal(scope.count, 2);
	evaluate(compileEvent("form.name = $event.target.value"), scope, { event: { target: { value: "ada" } } });
	assert.equal(scope.form.name, "ada");
	assert.throws(() => evaluate(compileEvent("Math.PI = 1"), scope, { event: null }), TemplateError);

	function compileEvent(src) {
		const { plan } = compileTemplate(`<button @click='${src}'>x</button>`);
		return plan[0].props[0].e;
	}
});

test("白名单拒绝：未知标签 / script / v-html / 静态 onclick / 静态 style / 内联 script 属性", () => {
	assert.throws(() => compileTemplate("<my-evil/>"), /不在白名单/);
	assert.throws(() => compileTemplate("<script src='x.js'></script>"), /模板不允许/);
	assert.throws(() => compileTemplate("<iframe/>"), /不在白名单/);
	assert.throws(() => compileTemplate("<p v-html='x'/>"), /v-html/);
	assert.throws(() => compileTemplate("<p onclick='x'/>"), /静态事件属性/);
	assert.throws(() => compileTemplate("<p style='color:red'/>"), /style 属性/);
	assert.throws(() => compileTemplate("<a href='javascript:alert(1)'>x</a>"), /危险协议/);
	assert.throws(() => compileTemplate("<input type='file'/>"), /input type/);
	assert.throws(() => compileTemplate("<slot/>"), /v1 模板中不支持/);
	assert.throws(() => compileTemplate("<p v-foo='x'/>"), /不支持的指令/);
	assert.throws(() => compileTemplate("<img src='a.png'/>"), /不在白名单/);
});

test("错误信息带行号列号", () => {
	try {
		compileTemplate("<div>\n  <my-evil/>\n</div>");
		assert.fail("应抛出");
	} catch (e) {
		assert.ok(e instanceof TemplateError);
		assert.match(e.message, /2:/);
	}
});

test("v-for 不带 :key 允许（静态 key 才报错）", () => {
	const { plan } = compileTemplate("<li v-for='x in items'>{{ x }}</li>");
	assert.equal(plan[0].k, "for");
	assert.equal(plan[0].key, null);
	assert.throws(() => compileTemplate("<li :key='1'>x</li>"), /:key 只在 v-for/);
});

test("toDisplayString", () => {
	assert.equal(toDisplayString(null), "");
	assert.equal(toDisplayString(undefined), "");
	assert.equal(toDisplayString(0), "0");
	assert.equal(toDisplayString(true), "true");
	assert.equal(toDisplayString("x"), "x");
	assert.equal(toDisplayString({ a: 1 }), '{"a":1}');
});

test("v-if 链：兄弟元素间的空白不打断链", () => {
	const { plan } = compileTemplate(
		"<p v-if='a'>A</p>\n  <p v-else-if='b'>B</p>\n  <p v-else>C</p>");
	assert.equal(plan[0].k, "if");
	assert.equal(plan[0].branches.length, 2);
	assert.deepEqual(plan[0].fallback[0].kids[0].v, "C");
	const single = compileTemplate("<p v-if='a'>A</p> <p v-else>C</p>");
	assert.equal(single.plan[0].k, "if");
	assert.equal(single.plan[0].fallback[0].kids[0].v, "C");
});

test("v-if 链：相邻的两条独立链不合并", () => {
	const { plan } = compileTemplate(
		"<p v-if='x'>X</p>\n<p v-if='y'>Y</p>\n<p v-else>N</p>");
	assert.equal(plan.length, 2);
	assert.equal(plan[0].k, "if");
	assert.equal(plan[0].branches.length, 1);
	assert.equal(plan[0].fallback, null);
	assert.equal(plan[1].k, "if");
	assert.equal(plan[1].branches.length, 1);
	assert.deepEqual(plan[1].fallback[0].kids[0].v, "N");
});

test("字面量关键字与方法调用接收者绑定", () => {
	const scope = { items: ["b", "a", "c"], flag: true, empty: null };
	const part = (src) => compilePart(src);
	function compilePart(src) {
		const { plan } = compileTemplate(`<p>{{ ${src} }}</p>`);
		return plan[0].kids[0].e;
	}
	assert.equal(evaluate(part("true"), scope), true);
	assert.equal(evaluate(part("false"), scope), false);
	assert.equal(evaluate(part("null"), scope), null);
	assert.equal(evaluate(part("undefined"), scope), undefined);
	// 方法调用绑定接收者：slice/join/map 等数组方法在 this 解绑时全部失效
	assert.deepEqual(evaluate(part("items.slice(0, 2)"), scope), ["b", "a"]);
	assert.equal(evaluate(part("items.join('-')"), scope), "b-a-c");
	assert.equal(evaluate(part("flag ? items.length : 0"), scope), 3);
});
