// 组件模型 + 渲染器（F2）—— VNode 工厂（内建白名单）、diff patch、组件实例、
// 模板计划解释器、异步组件、错误边界。
//
// 安全不变量（proposals/spa-frontend-framework.md §5.7）：
//   - 所有 VNode 出自 createVNode/h：标签白名单、属性白名单、URL 校验在此执行；
//   - 无 v-html / 无 innerHTML / 无内联 style 字符串；:style 走 CSSOM；
//   - 计划解释器对插件模板执行节点数/深度/字符串/输出预算（沿用现有数值）。

import { isAllowedTag, isUiComponent, isAllowedAttr, isAllowedInputType, isDangerousURL, URL_ATTRS } from "./whitelist.mjs";
import { evaluate, toDisplayString } from "./compiler.mjs";
import { ref, reactive, computed, effect, effectScope, queueJob, nextTick, isRef } from "./reactivity.mjs";

export const Fragment = Symbol("Fragment");

const MAX_DEPTH = 64;
const MAX_NODES = 2000;
const MAX_STRING_BYTES = 32 * 1024;
const MAX_OUTPUT_BYTES = 256 * 1024;

const BOOLEAN_ATTRS = new Set(["disabled", "checked", "required", "readonly", "open", "hidden"]);

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

export class RenderError extends Error {
	constructor(message) {
		super(message);
		this.name = "AdaRenderError";
	}
}

// ---------------------------------------------------------------------------
// VNode
// ---------------------------------------------------------------------------

export function createTextVNode(value) {
	return { tag: "#text", value: String(value), el: null, key: undefined };
}

export function createVNode(type, props, children) {
	if (type === Fragment) {
		return { tag: Fragment, props: props ?? null, kids: normalizeKids(children), el: null, key: props?.key, anchorContainer: null };
	}
	if (typeof type === "string") {
		if (!isAllowedTag(type) && !isUiComponent(type)) {
			throw new RenderError(`h(): 标签 <${type}> 不在白名单`);
		}
		const vnodeProps = validateProps(type, props ?? {});
		return { tag: type, props: vnodeProps, kids: normalizeKids(children), el: null, key: vnodeProps.key, compVnode: undefined, comp: null };
	}
	if (type !== null && typeof type === "object") {
		return { tag: type, props: props ?? {}, kids: normalizeKids(children), el: null, key: props?.key, comp: null };
	}
	throw new RenderError(`h(): 不支持的节点类型 ${typeof type}`);
}

export const h = createVNode;

function normalizeKids(children) {
	if (children === null || children === undefined || children === false || children === true) return [];
	if (typeof children === "string") return [createTextVNode(children)];
	if (typeof children === "number" || typeof children === "boolean") return [createTextVNode(toDisplayString(children))];
	if (Array.isArray(children)) {
		const out = [];
		for (const c of children) {
			if (c === null || c === undefined || c === false) continue;
			if (Array.isArray(c)) out.push(...normalizeKids(c));
			else if (typeof c === "string" || typeof c === "number" || typeof c === "boolean") out.push(createTextVNode(toDisplayString(c)));
			else if (c.tag !== undefined) out.push(c);
			else throw new RenderError("children 里出现了非法节点");
		}
		return out;
	}
	if (children.tag !== undefined) return [children];
	throw new RenderError("children 里出现了非法节点");
}

function validateProps(tag, props) {
	const out = {};
	for (const [name, value] of Object.entries(props)) {
		if (name === "key") { out.key = value; continue; }
		if (name.startsWith("on:")) {
			if (typeof value !== "function") throw new RenderError(`on:${name.slice(3)} 处理器必须是函数`);
			out[name] = value;
			continue;
		}
		if (name.startsWith("on")) {
			throw new RenderError(`事件绑定请使用 "on:事件名"（收到 "${name}"）`);
		}
		if (name === "innerHTML" || name === "innerText" || name === "outerHTML") {
			throw new RenderError(`"${name}" 被拒绝（无 v-html 语义，安全不变量）`);
		}
		if (name === "style") {
			if (typeof value === "string") throw new RenderError("style 仅接受对象/数组（CSSOM 注入），不接受字符串");
			out[name] = value;
			continue;
		}
		if (name === "class") { out[name] = value; continue; }
		if (isUiComponent(tag)) {
			// 组件 props 自由（宿主组件自行校验），仅禁 onX 静态写法（上面已拦）。
			out[name] = value;
			continue;
		}
		// 原生元素属性
		if (!isAllowedAttr(name)) throw new RenderError(`属性 "${name}" 不在白名单`);
		if (typeof value === "object") throw new RenderError(`属性 "${name}" 仅接受原始值绑定`);
		if (name === "type" && !isAllowedInputType(value)) throw new RenderError(`input type "${value}" 不在白名单`);
		if (URL_ATTRS.includes(name) && isDangerousURL(value)) throw new RenderError(`URL 属性 "${name}" 含危险协议`);
		out[name] = value;
	}
	return out;
}

// ---------------------------------------------------------------------------
// DOM 应用层（属性/样式/事件）
// ---------------------------------------------------------------------------

function hyphenate(s) {
	return s.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`);
}

function applyStyle(el, value) {
	const next = styleToObject(value);
	const prev = el.__adaStyle ?? {};
	for (const k of Object.keys(prev)) {
		if (!(k in next)) el.style.removeProperty(k);
	}
	for (const [k, v] of Object.entries(next)) {
		if (v === null || v === undefined || v === false || v === "") el.style.removeProperty(k);
		else el.style.setProperty(hyphenate(k), String(v));
	}
	el.__adaStyle = next;
}

function styleToObject(value) {
	if (value === null || value === undefined) return {};
	if (Array.isArray(value)) return Object.assign({}, ...value.map(styleToObject));
	if (typeof value !== "object") throw new RenderError(":style 仅接受对象/数组");
	const out = {};
	for (const [k, v] of Object.entries(value)) {
		if (v === null || v === undefined || v === false) continue;
		if (typeof v === "object") throw new RenderError(":style 嵌套层级过深");
		out[k] = v;
	}
	return out;
}

function mergeClass(value) {
	if (value === null || value === undefined || value === false) return "";
	if (typeof value === "string") return value;
	if (Array.isArray(value)) return value.map(mergeClass).filter(Boolean).join(" ");
	if (typeof value === "object") return Object.entries(value).filter(([, v]) => v).map(([k]) => k).join(" ");
	return String(value);
}

function setProp(el, name, value, old) {
	if (name.startsWith("on:")) {
		const evt = name.slice(3);
		if (old !== undefined) el.removeEventListener(evt, old);
		if (value !== undefined && value !== null) {
			el.addEventListener(evt, value, value.__once === true ? { once: true } : undefined);
		}
		return;
	}
	if (name === "class") {
		const cls = mergeClass(value);
		const oldCls = old !== undefined ? mergeClass(old) : "";
		if (cls !== oldCls) el.setAttribute("class", cls);
		return;
	}
	if (name === "style") {
		applyStyle(el, value);
		return;
	}
	if (name === "key") return;
	if (name === "value") {
		const v = value === null || value === undefined ? "" : String(value);
		if (el.value !== v) el.value = v;
		return;
	}
	if (name === "checked") {
		el.checked = Boolean(value);
		if (value) el.setAttribute("checked", "");
		else el.removeAttribute("checked");
		return;
	}
	if (name === "hidden") {
		el.hidden = Boolean(value);
		return;
	}
	if (BOOLEAN_ATTRS.has(name)) {
		if (value) el.setAttribute(name, "");
		else el.removeAttribute(name);
		return;
	}
	if (value === null || value === undefined || value === false) {
		el.removeAttribute(name);
		return;
	}
	el.setAttribute(name, String(value));
}

// ---------------------------------------------------------------------------
// 组件模型
// ---------------------------------------------------------------------------

let currentInstance = null;
let jobUid = 0;

export function getCurrentInstance() {
	return currentInstance;
}

export function defineComponent(def) {
	if (typeof def === "function") def = { setup: def };
	if (def.setup === undefined && def.render === undefined && def.plan === undefined) {
		throw new RenderError("defineComponent：需要 setup / render / plan 之一");
	}
	return {
		name: def.name ?? "anonymous",
		props: def.props ?? [],
		emits: def.emits ?? [],
		setup: def.setup ?? null,
		render: def.render ?? null,
		plan: def.plan ?? null,
	};
}

export function provide(key, value) {
	const inst = currentInstance;
	if (inst === null) throw new RenderError("provide 必须在 setup 里调用");
	inst.provides[key] = value;
}

export function inject(key, defaultValue) {
	const inst = currentInstance;
	if (inst === null) throw new RenderError("inject 必须在 setup 里调用");
	if (key in inst.provides) return inst.provides[key];
	if (arguments.length > 1) return defaultValue;
	throw new RenderError(`inject("${String(key)}") 未找到提供者`);
}

export function onMounted(fn) {
	const inst = currentInstance;
	if (inst === null) throw new RenderError("onMounted 必须在 setup 里调用");
	inst.mountedCbs.push(fn);
}

export function onUnmounted(fn) {
	const inst = currentInstance;
	if (inst === null) throw new RenderError("onUnmounted 必须在 setup 里调用");
	inst.unmountedCbs.push(fn);
}

export function onErrorCaptured(fn) {
	const inst = currentInstance;
	if (inst === null) throw new RenderError("onErrorCaptured 必须在 setup 里调用");
	inst.errorCbs.push(fn);
	return false;
}

function capitalize(s) {
	return s.length === 0 ? s : s[0].toUpperCase() + s.slice(1);
}

// setup 返回值 → 模板作用域：get 时解包 ref，props 从 reactive 实例属性实时读取。
function makeRenderScope(state, getProps) {
	return new Proxy(state, {
		get(target, k) {
			if (k in target) {
				const v = target[k];
				return isRef(v) ? v.value : v;
			}
			const p = getProps();
			if (p !== null && k in p) return p[k];
			return undefined;
		},
		set(target, k, v) {
			if (isRef(target[k])) {
				target[k].value = v;
				return true;
			}
			const p = getProps();
			if (p !== null && k in p) {
				p[k] = v;
				return true;
			}
			target[k] = v;
			return true;
		},
		has(target, k) {
			if (k in target) return true;
			const p = getProps();
			return p !== null && k in p;
		},
	});
}

// v-for 子作用域：额外变量挂 own，未命中读写都委托父作用域（外层状态可被 v-model 修改）。
function childScope(parent, extra) {
	return new Proxy(extra, {
		get(t, k) {
			if (k in t) {
				const v = t[k];
				return isRef(v) ? v.value : v;
			}
			return parent[k];
		},
		set(t, k, v) {
			if (k in t) {
				const cur = t[k];
				if (isRef(cur)) cur.value = v;
				else t[k] = v;
				return true;
			}
			parent[k] = v;
			return true;
		},
		has(t, k) {
			return k in t || k in parent;
		},
	});
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

export function createApp(rootComponent) {
	const app = {
		_components: new Map(),
		_provides: {},
		config: { errorHandler: null },
		mounted: false,
	};
	app.provide = (key, value) => { app._provides[key] = value; return app; };
	app.component = (name, def) => { app._components.set(name, def); return app; };
	app.mount = (el, opts = {}) => {
		const scope = opts.scope ?? effectScope();
		const rootVnode = h(rootComponent, opts.props ?? {});
		mountVNode(rootVnode, el, app, null, scope);
		app.mounted = true;
		app._scope = scope;
		app._rootInstance = rootVnode.comp;
		return app;
	};
	app.unmount = () => {
		if (app._rootInstance) unmountInstance(app._rootInstance);
	};
	return app;
}

// replaceAnchor：给被替换的旧节点找一个插入锚点。
// 单元素取该元素；组件/Fragment 取子树里第一个真实元素；取不到则退回
// 组件实例的挂载容器（异步占位符属于同容器的兄弟节点，插入位置仍然正确）。
function replaceAnchor(vnode) {
	const els = vnodeElements(vnode);
	if (els.length === 0) return null;
	// 锚点必须是「整个旧子树之后」的那个兄弟：取最后一个元素的 nextSibling
	// （null 表示旧子树在末尾，插入到末尾即可）。必须在 unmount 之前计算。
	const last = els[els.length - 1];
	if (last.parentNode === null) return null;
	return last.nextSibling;
}

// vnodeElements：按渲染顺序收集 vnode 子树里的真实 DOM 元素（组件/Fragment 递归）。
function vnodeElements(vnode) {
	const out = [];
	collect(vnode);
	return out;
	function collect(v) {
		if (v === null || v === undefined) return;
		if (v.tag === Fragment) {
			for (const k of v.kids) collect(k);
			return;
		}
		if (typeof v.tag === "object") {
			const sub = v.comp !== null && v.comp !== undefined ? v.comp.subTree : v.compVnode;
			if (sub !== null && sub !== undefined) collect(sub);
			else if (v.el !== null && v.el !== undefined) out.push(v.el);
			return;
		}
		if (v.el !== null && v.el !== undefined) out.push(v.el);
	}
}

function appendNode(container, node) {
	if (insertAnchor !== null && insertAnchor !== undefined && insertAnchor.parentNode === container) {
		container.insertBefore(node, insertAnchor);
		return;
	}
	container.appendChild(node);
}

// insertAnchor：patch 替换节点时的插入锚点。异步组件装载完成时，新子树必须
// 插回**原占位符的位置**，而不是 append 到容器末尾——否则先渲染的骨架块
// （侧栏/顶栏）会在异步页面就位后跑到 DOM 最后，布局整体错位
// （2026-11-11 走查：侧栏「左侧完全空白」就是这个原因）。
let insertAnchor = null;

function mountVNode(vnode, container, app, parent, scope) {
	if (vnode.tag === "#text") {
		vnode.el = document.createTextNode(vnode.value);
		appendNode(container, vnode.el);
		return;
	}
	if (vnode.tag === Fragment) {
		vnode.anchorContainer = container;
		for (const k of vnode.kids) mountVNode(k, container, app, parent, scope);
		return;
	}
	if (typeof vnode.tag === "object") {
		mountComponent(vnode, container, app, parent, scope);
		return;
	}
	if (isUiComponent(vnode.tag)) {
		const def = app._components.get(vnode.tag);
		if (def === undefined) throw new RenderError(`宿主组件 <${vnode.tag}> 未注册`);
		const child = { tag: def, props: vnode.props, kids: vnode.kids, el: null, key: vnode.key, comp: null };
		vnode.compVnode = child;
		mountVNode(child, container, app, parent, scope);
		vnode.el = child.el;
		return;
	}
	const el = document.createElement(vnode.tag);
	vnode.el = el;
	for (const [name, value] of Object.entries(vnode.props)) setProp(el, name, value);
	for (const k of vnode.kids) mountVNode(k, el, app, parent, scope);
	appendNode(container, el);
}

function unmountVNode(vnode, container) {
	if (vnode.tag === "#text") {
		vnode.el?.parentNode?.removeChild(vnode.el);
		vnode.el = null;
		return;
	}
	if (vnode.tag === Fragment) {
		for (const k of vnode.kids) unmountVNode(k, container);
		return;
	}
	if (typeof vnode.tag === "object") {
		if (vnode.comp) unmountInstance(vnode.comp);
		return;
	}
	if (vnode.compVnode !== undefined) {
		if (vnode.compVnode.comp) unmountInstance(vnode.compVnode.comp);
		else unmountVNode(vnode.compVnode, container);
		return;
	}
	vnode.el?.parentNode?.removeChild(vnode.el);
	vnode.el = null;
}

function mountComponent(vnode, container, app, parent, scope) {
	const def = vnode.tag;
	const inst = {
		def,
		app,
		parent: parent ?? null,
		provides: null,
		props: reactive({}),
		scope: effectScope(),
		subTree: null,
		el: null,
		anchorContainer: container,
		mountedCbs: [],
		unmountedCbs: [],
		errorCbs: [],
		mountedDone: false,
		unmounted: false,
		updateJob: null,
		renderEffect: null,
		renderScope: null,
	};
	vnode.comp = inst;
	inst.provides = parent !== null ? Object.create(parent.provides) : Object.create(app._provides);

	for (const [k, v] of Object.entries(vnode.props ?? {})) {
		if (k === "key") continue;
		inst.props[k] = v;
	}
	if (vnode.kids.length > 0) inst.props.kids = vnode.kids;

	const ctx = {
		props: inst.props,
		kids: vnode.kids,
		emit: (event, ...args) => {
			const handler = inst.props[`on:${event}`] ?? inst.props[`on${capitalize(event)}`];
			if (typeof handler === "function") handler(...args);
		},
		instance: inst,
	};

	let bindings = null;
	const prev = currentInstance;
	currentInstance = inst;
	try {
		inst.scope.run(() => {
			if (def.setup !== null) bindings = def.setup(inst.props, ctx) ?? {};
		});
	} catch (err) {
		currentInstance = prev;
		handleError(err, inst, container);
		return;
	}
	currentInstance = prev;

	inst.renderScope = makeRenderScope(bindings ?? {}, () => inst.props);

	const renderTree = () => {
		if (def.plan !== null) {
			const budget = { nodes: 0, bytes: 0 };
			return { tag: Fragment, props: null, kids: normalizeKids(renderPlan(def.plan, inst.renderScope, inst, budget, 0)), el: null, key: undefined, anchorContainer: inst.anchorContainer };
		}
		if (def.render !== null) return def.render(inst.renderScope, ctx);
		throw new RenderError(`组件 ${def.name} 既无 plan 也无 render`);
	};

	// 更新 job 必须交回 renderEffect.run()，不能直接 runUpdate：effect.run 会先
	// cleanupDeps 再在 withTracking 里重跑渲染，从而按「本次」读到的状态重收集依赖。
	// 直接 runUpdate 会让 effect 的依赖停留在首轮渲染那一刻：首轮 v-for 的数组是空
	// 的，只登记了 length，后续等长索引替换（groups.splice(i,1,{...})）没有依赖者，
	// 界面就停在旧 DOM（2026-11-11 走查：其它工作区展开后仍显示「展开以加载会话…」；
	// 搜索词变化却会刷新——那个 ref 是首轮就登记过的依赖）。
	const updateJob = {
		id: jobUid++,
		queued: false,
		run() {
			if (inst.renderEffect !== null) inst.renderEffect.run();
			else runUpdate(inst, renderTree);
		},
	};
	inst.updateJob = updateJob;

	inst.renderEffect = effect(() => {
		runUpdate(inst, renderTree);
	}, { scope: inst.scope, scheduler: () => queueJob(updateJob) });
}

function runUpdate(inst, renderTree) {
	if (inst.unmounted === true) return;
	try {
		const next = renderTree();
		const prev = inst.subTree;
		inst.subTree = next;
		patchVNode(prev, next, inst.anchorContainer, inst.app, inst);
		if (!inst.mountedDone) {
			inst.mountedDone = true;
			for (const cb of inst.mountedCbs) {
				try { cb(); } catch (err) { handleError(err, inst); }
			}
		}
	} catch (err) {
		handleError(err, inst, inst.anchorContainer);
	}
}

function patchVNode(n1, n2, container, app, owner) {
	if (n1 === n2) return;
	// render() 允许返回 null（空占位）：对话框/浮层关闭态就是「本组件当前不产出
	// 任何 DOM」。旧实现只在 n1 === null（首次挂载）时特判，n2 === null 会一路走到
	// `typeof n2.tag` 并在 null 上取属性（错误边界接住后旧 DOM 原地不动）——
	// 表现为确认框按 Esc 后仍在（2026-11-11 走查）。关闭时必须卸载旧子树。
	if (n2 === null || n2 === undefined) {
		if (n1 === null || n1 === undefined) return;
		unmountVNode(n1, container);
		return;
	}
	if (n1 === null || n1 === undefined) {
		mountVNode(n2, container, app, owner, owner?.scope);
		return;
	}
	// 替换类分支要记住旧节点的锚点：异步组件从「装载中」切到「就位」时，
	// 新子树必须插回占位符原位（见 insertAnchor 注释）。
	const anchor = replaceAnchor(n1);
	const prevAnchor = insertAnchor;
	if (typeof n2.tag === "object") {
		if (typeof n1.tag === "object" && n1.tag === n2.tag && n1.key === n2.key) {
			patchComponent(n1, n2, app);
			return;
		}
		// 同组件换了 key（典型：路由切换时给页面 vnode 带 path 作为 key）：
		// 必须卸载重建，让 setup 重新执行——否则 setup 里捕获的路由参数是旧值
		//（2026-11-11 走查：会话列表点进会话后主区仍渲染「新建任务」）。
		insertAnchor = anchor;
		unmountVNode(n1, container);
		mountVNode(n2, container, app, owner, owner?.scope);
		insertAnchor = prevAnchor;
		return;
	}
	if (n2.tag === Fragment) {
		if (n1.tag === Fragment) {
			n2.anchorContainer = n1.anchorContainer;
			patchChildren(n1.anchorContainer, n1.kids, n2.kids, app, owner);
			return;
		}
		insertAnchor = anchor;
		unmountVNode(n1, container);
		mountVNode(n2, container, app, owner, owner?.scope);
		insertAnchor = prevAnchor;
		return;
	}
	if (n2.tag === "#text") {
		if (n1.tag === "#text") {
			if (n1.value !== n2.value) {
				n1.value = n2.value;
				n1.el.nodeValue = n2.value;
			}
			n2.el = n1.el;
			return;
		}
		insertAnchor = anchor;
		unmountVNode(n1, container);
		mountVNode(n2, container, app, owner, owner?.scope);
		insertAnchor = prevAnchor;
		return;
	}
	if (n1.tag === n2.tag && n1.key === n2.key) {
		n2.el = n1.el;
		if (n1.compVnode !== undefined) {
			// ui-* 委托：新 props/kids 经组件实例增量更新
			const child = { tag: n1.compVnode.tag, props: n2.props, kids: n2.kids, el: n1.compVnode.el, key: n1.compVnode.key, comp: n1.compVnode.comp };
			patchComponent(n1.compVnode, child, app);
			n2.compVnode = n1.compVnode;
			return;
		}
		patchProps(n1, n2);
		patchChildren(n2.el, n1.kids, n2.kids, app, owner);
		return;
	}
	insertAnchor = anchor;
	unmountVNode(n1, container);
	mountVNode(n2, container, app, owner, owner?.scope);
	insertAnchor = prevAnchor;
}

function patchComponent(oldV, newV, app) {
	const inst = oldV.comp;
	newV.comp = inst;
	const oldProps = oldV.props ?? {};
	const newProps = newV.props ?? {};
	for (const k of Object.keys(oldProps)) {
		if (!(k in newProps) && k !== "kids") delete inst.props[k];
	}
	for (const [k, v] of Object.entries(newProps)) {
		if (k === "key") continue;
		inst.props[k] = v;
	}
	if (newV.kids.length > 0) inst.props.kids = newV.kids;
	else delete inst.props.kids;
	newV.el = oldV.el;
}

function patchProps(n1, n2) {
	const el = n2.el;
	const oldProps = n1.props ?? {};
	const newProps = n2.props ?? {};
	for (const k of Object.keys(oldProps)) {
		if (!(k in newProps)) setProp(el, k, undefined, oldProps[k]);
	}
	for (const [k, v] of Object.entries(newProps)) {
		setProp(el, k, v, oldProps[k]);
	}
}

function patchChildren(parentEl, oldKids, newKids, app, owner) {
	const common = Math.min(oldKids.length, newKids.length);
	for (let i = 0; i < common; i++) patchVNode(oldKids[i], newKids[i], parentEl, app, owner);
	if (newKids.length > oldKids.length) {
		for (let i = oldKids.length; i < newKids.length; i++) mountVNode(newKids[i], parentEl, app, owner, owner?.scope);
	} else if (oldKids.length > newKids.length) {
		for (let i = newKids.length; i < oldKids.length; i++) unmountVNode(oldKids[i], parentEl);
	}
}

function unmountInstance(inst) {
	if (inst.unmounted === true) return;
	inst.unmounted = true;
	for (const cb of inst.unmountedCbs) {
		try { cb(); } catch { /* 卸载钩子失败不再传播 */ }
	}
	if (inst.subTree !== null) unmountVNode(inst.subTree, inst.anchorContainer);
	inst.scope.stop();
}

// ---------------------------------------------------------------------------
// 错误边界
// ---------------------------------------------------------------------------

function handleError(err, inst, container) {
	let cur = inst;
	while (cur !== null) {
		for (const cb of cur.errorCbs) {
			try {
				const handled = cb(err, cur);
				if (handled === false) return;
			} catch (cbErr) {
				err = cbErr;
			}
		}
		cur = cur.parent;
	}
	const handler = inst?.app?.config?.errorHandler;
	if (handler !== null && handler !== undefined) {
		try { handler(err, inst); return; } catch { /* fallthrough */ }
	}
	if (typeof console !== "undefined") console.error("[ada-render]", err);
	if (container !== undefined && container !== null && typeof container.setAttribute === "function") {
		container.setAttribute("data-ada-error", "1");
	}
}

// ---------------------------------------------------------------------------
// 模板计划解释器（插件 SFC 渲染路径；预算在此强制执行）
// ---------------------------------------------------------------------------

function renderPlan(plan, scope, inst, budget, depth) {
	return renderKids(plan, scope, inst, budget, depth);
}

function renderKids(nodes, scope, inst, budget, depth) {
	const out = [];
	for (const node of nodes) {
		const v = renderNode(node, scope, inst, budget, depth);
		if (v !== null) out.push(v);
	}
	return out;
}

function renderNode(node, scope, inst, budget, depth) {
	if (++budget.nodes > MAX_NODES) throw new RenderError("模板渲染超出节点数上限（2000）");
	if (depth > MAX_DEPTH) throw new RenderError("模板渲染超出深度上限（64）");
	switch (node.k) {
		case "text":
			budget.bytes += node.v.length;
			return createTextVNode(node.v);
		case "dtext": {
			const v = toDisplayString(evaluate(node.e, scope));
			if (v.length > MAX_STRING_BYTES) throw new RenderError("模板渲染超出单字符串上限（32KB）");
			budget.bytes += v.length;
			if (budget.bytes > MAX_OUTPUT_BYTES) throw new RenderError("模板渲染超出输出上限");
			return createTextVNode(v);
		}
		case "el":
			return renderElement(node, scope, inst, budget, depth);
		case "if": {
			for (const br of node.branches) {
				if (evaluate(br.e, scope)) return renderKids(br.kids, scope, inst, budget, depth + 1);
			}
			if (node.fallback !== null) return renderKids(node.fallback, scope, inst, budget, depth + 1);
			return null;
		}
		case "for": {
			const list = evaluate(node.src, scope);
			const items = Array.isArray(list) ? list : list === null || list === undefined ? [] : Object.values(list);
			const out = [];
			for (let i = 0; i < items.length; i++) {
				const extra = { [node.item]: items[i] };
				if (node.index !== null) extra[node.index] = i;
				const itemScope = childScope(scope, extra);
				const key = node.key !== null ? evaluate(node.key, itemScope) : i;
				// 必须扁平化：v-if 分支返回的是 vnode 数组，不拍平会让
				// Fragment.kids 里出现嵌套数组，patch 时按「同 tag」误配，
				// 异步数据更新后界面不刷新（2026-11-11 走查：展开工作区后
				// 会话列表不出现；无关重渲染却能刷新——同一 patch 坏路径）。
				const kids = normalizeKids(renderKids(node.kids, itemScope, inst, budget, depth + 1));
				out.push({ tag: Fragment, props: null, kids, el: null, key, anchorContainer: null });
			}
			return out;
		}
		default:
			throw new RenderError(`未知计划节点 ${node?.k}`);
	}
}

function renderElement(node, scope, inst, budget, depth) {
	const props = {};
	let textOverride = undefined;
	for (const p of node.props) {
		if (p.s !== undefined) {
			if (BOOLEAN_ATTRS.has(p.n) && p.s === "") props[p.n] = true;
			else props[p.n] = p.s;
			continue;
		}
		if (p.n === "#text") {
			textOverride = toDisplayString(evaluate(p.e, scope));
			continue;
		}
		if (p.n.startsWith("on:")) {
			// 事件表达式惰性求值：渲染期只创建 handler，不触发表达式。
			props[p.n] = makeEventHandler(p, scope);
			continue;
		}
		const v = evaluate(p.e, scope);
		if (p.n === "#style") {
			props.style = props.style === undefined ? v : [props.style, v];
			continue;
		}
		if (p.n === "class") {
			props.class = props.class === undefined ? v : [props.class, v];
			continue;
		}
		props[p.n] = v;
	}
	// SFC scoped 样式：本组件模板的元素盖 scope 戳（data-* 在属性白名单内）。
	if (inst !== null && typeof inst.def?.scopeId === "string") {
		props[`data-v-${inst.def.scopeId}`] = "";
	}
	const vnode = createVNode(node.tag, props, textOverride !== undefined ? textOverride : null);
	const kids = renderKids(node.kids, scope, inst, budget, depth + 1);
	vnode.kids = normalizeKids(kids);
	return vnode;
}

function makeEventHandler(p, scope) {
	const mods = p.mods ?? {};
	const fn = (event) => {
		if (mods.self && event.target !== event.currentTarget) return;
		if (mods.stop && typeof event.stopPropagation === "function") event.stopPropagation();
		if (mods.prevent && typeof event.preventDefault === "function") event.preventDefault();
		const r = evaluate(p.e, scope, { event });
		if (typeof r === "function") r(event);
	};
	if (mods.once) fn.__once = true;
	return fn;
}

// ---------------------------------------------------------------------------
// 异步组件
// ---------------------------------------------------------------------------

export function defineAsyncComponent(loader) {
	return defineComponent({
		name: "AsyncComponent",
		setup(props, ctx) {
			const status = ref("loading");
			const comp = ref(null);
			const error = ref(null);
			Promise.resolve(loader())
				.then((resolved) => {
					comp.value = resolved;
					status.value = "ok";
				})
				.catch((err) => {
					error.value = err;
					status.value = "error";
				});
			return { status, comp, error, kids: ctx.kids, ownProps: ctx.props };
		},
		// render 接到的是解包后的作用域（ref 已展开），读取本身即建立依赖。
			render(scope) {
				if (scope.status === "ok") {
					// 透传的是异步包装组件自己的 props（scope.ownProps 经 ctx.props 拿到；
					// 渲染作用域里没有 "props" 这个键，scope.props 会读到 undefined——
					// 2026-11-11 走查：页面组件因此拿不到 params）。
					return h(scope.comp, filterAsyncProps(scope.ownProps ?? {}), scope.kids);
				}
				if (scope.status === "error") {
					const msg = scope.error?.message ?? String(scope.error ?? "装载失败");
					return h("div", { class: "ada-slot-error", "data-ada-error": "1" }, `组件装载失败：${msg}`);
				}
				return h("div", { class: "ada-slot-loading", "aria-busy": "true" }, "");
			},
		});
}

function filterAsyncProps(props) {
	const out = {};
	for (const [k, v] of Object.entries(props ?? {})) {
		if (k === "kids" || k === "key") continue;
		out[k] = v;
	}
	return out;
}

// 供 sfc.mjs 使用：把 SFC 组装结果变成组件定义（scoped 样式随实例挂摘）。
export function makeSfcComponent({ setupFn, plan, styles }) {
	return defineComponent({
		name: "SfcComponent",
		setup(props, ctx) {
			if (styles != null) attachScopedStyles(styles);
			const bindings = setupFn !== null ? setupFn(props, ctx) ?? {} : {};
			return bindings;
		},
		plan,
	});
}

function attachScopedStyles(cssText) {
	if (cssText === null || typeof CSSStyleSheet === "undefined" || typeof document === "undefined") return;
	try {
		const sheet = new CSSStyleSheet();
		sheet.replaceSync(cssText);
		document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
		onUnmounted(() => {
			const sheets = [...document.adoptedStyleSheets];
			const i = sheets.indexOf(sheet);
			if (i >= 0) sheets.splice(i, 1);
			document.adoptedStyleSheets = sheets;
		});
	} catch { /* 样式表失败不阻断渲染 */ }
}

export { ref, reactive, computed, effect, effectScope, queueJob, nextTick, isRef, evaluate, toDisplayString };
