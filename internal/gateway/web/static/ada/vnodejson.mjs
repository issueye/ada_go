// vnodejson.mjs —— 模型生成内容（display profile VNode JSON）的客户端渲染器。
//
// 线格式（与服务端 vnode.go / spec/vnode.schema.json 对齐）：
//   元素   { "t": "<tag>", "p": {...}, "c": [...] }
//   组件   { "t": "$C",    "n": "<Name>", "p": {...}, "c": [...] }
//   文本   { "t": "#text", "v": "..." }
//
// 安全边界（proposals/spa-frontend-framework.md §9，不变量）：
//   - 服务端已完成 display profile 校验（无事件、无表单、白名单标签）；
//   - 客户端这里仍按白名单复查标签、拒绝事件属性绑定、拒绝 style 字符串；
//   - 模型内容永不进入 SFC 编译器。

import { h, defineComponent, ref, inject } from "./runtime.mjs";
import { TAGS } from "./whitelist.mjs";

const tagSet = new Set(TAGS);

// display profile 允许的宿主组件（docs/15 §16.10；其余名称渲染为占位）。
const displayComponents = new Map();

export function registerDisplayComponent(name, def) {
	displayComponents.set(name, def);
}

function renderNodeJson(node, ctx) {
	if (node === null || node === undefined) return null;
	if (typeof node === "string") return node; // 文本简写形态
	if (Array.isArray(node)) return node.map((n) => renderNodeJson(n, ctx));
	const t = node.t;
	if (t === "#text") return String(node.v ?? "");
	if (t === "$C") {
		const name = node.n;
		const def = displayComponents.get(name);
		const props = { ...(node.p ?? {}) };
		if (node.c !== undefined) props.kids = node.c;
		if (def === undefined) {
			return h("em", { class: "ada-vn-unknown" }, `[组件 ${name} 未实现]`);
		}
		return h(def, props);
	}
	// 元素
	if (typeof t !== "string" || !tagSet.has(t)) {
		return h("em", { class: "ada-vn-unknown" }, `[标签 ${String(t)} 不在白名单]`);
	}
	const props = sanitizeProps(t, node.p ?? {});
	const kids = node.c !== undefined ? renderNodeJson(node.c, ctx) : null;
	return h(t, props, kids);
}

function sanitizeProps(tag, p) {
	const out = {};
	for (const [k, v] of Object.entries(p)) {
		// display profile 无事件（服务端已保证）；这里把 on* 一律丢弃。
		if (k.startsWith("on")) continue;
		if (k === "style") continue; // 模型内容不允许 style（服务端已保证，防御性再丢一次）
		if (k === "class") { out.class = v; continue; }
		if (typeof v === "object") continue; // 属性只接受原始值
		out[k] = v;
	}
	return out;
}

// Markdown 组件：文本 → /markdown.ast → VNode JSON 递归渲染。
const MarkdownComponent = defineComponent({
	name: "VnMarkdown",
	props: ["text"],
	setup(props) {
		const fetch = inject("fetch");
		const ast = ref(null);
		const failed = ref(null);
		async function load() {
			try {
				ast.value = await fetch("/markdown.ast", { method: "POST", body: { text: props.text ?? "" } });
				failed.value = null;
			} catch (err) {
				failed.value = err?.message ?? String(err);
			}
		}
		load();
		return { ast, failed, text: props.text };
	},
	render(s) {
		if (s.failed !== null) return h("code", {}, `[markdown 渲染失败]`);
		if (s.ast === null) return h("p", { class: "ada-vn-loading" }, "…");
		const nodes = renderNodeJson(s.ast.nodes ?? s.ast, { depth: 0 });
		return h("div", { class: "ada-vn-markdown" }, nodes);
	},
});
registerDisplayComponent("Markdown", MarkdownComponent);

// Icon 组件：v1 以文字符号占位（沿用 a_da 图标语义的子集）。
const IconComponent = defineComponent({
	name: "VnIcon",
	props: ["name"],
	setup(props) { return { name: props.name ?? "" }; },
	render: (s) => h("span", { class: "ada-vn-icon", "data-icon": s.name, "aria-hidden": "true" }, "•"),
});
registerDisplayComponent("Icon", IconComponent);

// TodoList 组件：计划步骤展示（无交互）。
const TodoListComponent = defineComponent({
	name: "VnTodoList",
	props: ["items", "title"],
	setup(props) { return { items: Array.isArray(props.items) ? props.items : [], title: props.title ?? "" }; },
	render: (s) => h("div", { class: "ada-vn-todo" }, [
		s.title !== "" ? h("div", { class: "ada-vn-todo-title" }, s.title) : null,
		h("ul", {}, s.items.map((it) => {
			const text = typeof it === "string" ? it : (it?.text ?? "");
			const done = typeof it === "object" && it !== null && it.done === true;
			return h("li", { class: done ? "is-done" : "" }, text);
		})),
	]),
});
registerDisplayComponent("TodoList", TodoListComponent);

// VNodeJson 组件：会话行 / 块渲染入口。
export const VNodeJson = defineComponent({
	name: "VNodeJson",
	props: ["node"],
	setup(props) {
		return { node: props.node };
	},
	render: (s) => h("div", { class: "ada-vn" }, renderNodeJson(s.node, { depth: 0 })),
});

// renderVNodeJsonChildren 把 display profile 节点渲染成 VNode 数组（不带外层
// 包装）：ui-markdown 宿主组件用它直接把助手正文落在 .part-text 里，与 HEAD
// 的 ui-markdown DOM 同形（无 .ada-vn 包裹层）。
export function renderVNodeJsonChildren(node) {
	return renderNodeJson(node, { depth: 0 });
}
