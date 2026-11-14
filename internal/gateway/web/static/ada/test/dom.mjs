// 最小 DOM shim —— 仅供 node --test 使用（runtime/router 测试）。
// 只实现 ada-runtime 用到的子集：createElement/textNode/append/insert/remove/
// attributes/style(CSSOM)/listeners/childNodes/事件对象。
// ⚠️ 与浏览器语义的差距：不做 HTML 解析、不做事件冒泡、无实际布局。

export class DomNode {
	constructor(tag) {
		this.nodeType = 1;
		this.tagName = String(tag).toUpperCase();
		this.childNodes = [];
		this.parentNode = null;
		this.attributes = new Map();
		this.style = new Style();
		this._listeners = new Map();
		this.value = "";
		this.hidden = false;
		this.checked = false;
	}
	get className() {
		return this.attributes.get("class") ?? "";
	}
	// 兄弟节点导航（runtime 的 patch 锚点依赖 nextSibling 语义）。
	get nextSibling() {
		if (this.parentNode === null) return null;
		const idx = this.parentNode.childNodes.indexOf(this);
		return idx >= 0 && idx + 1 < this.parentNode.childNodes.length ? this.parentNode.childNodes[idx + 1] : null;
	}
	get previousSibling() {
		if (this.parentNode === null) return null;
		const idx = this.parentNode.childNodes.indexOf(this);
		return idx > 0 ? this.parentNode.childNodes[idx - 1] : null;
	}
	get textContent() {
		return this.childNodes.map((c) => (c.nodeType === 3 ? c._text : c.textContent)).join("");
	}
	set textContent(v) {
		for (const c of [...this.childNodes]) this.removeChild(c);
		if (v !== "") this.appendChild(new DomText(v));
	}
	appendChild(child) {
		if (child.parentNode !== null) child.parentNode.removeChild(child);
		child.parentNode = this;
		this.childNodes.push(child);
		return child;
	}
	insertBefore(child, ref) {
		if (ref === null || ref === undefined) return this.appendChild(child);
		if (child.parentNode !== null) child.parentNode.removeChild(child);
		const idx = this.childNodes.indexOf(ref);
		if (idx < 0) throw new Error("insertBefore: ref 不是子节点");
		child.parentNode = this;
		this.childNodes.splice(idx, 0, child);
		return child;
	}
	removeChild(child) {
		const idx = this.childNodes.indexOf(child);
		if (idx < 0) throw new Error("removeChild: 不是子节点");
		this.childNodes.splice(idx, 1);
		child.parentNode = null;
		return child;
	}
	setAttribute(name, value) {
		this.attributes.set(name, String(value));
	}
	getAttribute(name) {
		const v = this.attributes.get(name);
		return v === undefined ? null : v;
	}
	removeAttribute(name) {
		this.attributes.delete(name);
	}
	hasAttribute(name) {
		return this.attributes.has(name);
	}
	addEventListener(type, fn, opts) {
		const list = this._listeners.get(type) ?? [];
		list.push({ fn, once: opts === true || (opts && opts.once === true) });
		this._listeners.set(type, list);
	}
	removeEventListener(type, fn) {
		const list = this._listeners.get(type) ?? [];
		const idx = list.findIndex((x) => x.fn === fn);
		if (idx >= 0) list.splice(idx, 1);
	}
	// 测试辅助：派发事件（无冒泡）。
	dispatch(type, event) {
		const list = this._listeners.get(type) ?? [];
		let prevented = false;
		for (const entry of [...list]) {
			const e = { type, target: this, currentTarget: this, ...event, preventDefault() { prevented = true; }, stopPropagation() {} };
			entry.fn(e);
			if (entry.once) this.removeEventListener(type, entry.fn);
		}
		return prevented;
	}
}

export class DomText extends DomNode {
	constructor(text) {
		super("#text");
		this.nodeType = 3;
		this._text = String(text);
	}
	get nodeValue() {
		return this._text;
	}
	set nodeValue(v) {
		this._text = String(v);
	}
}

export class Style {
	constructor() {
		this._props = new Map();
	}
	setProperty(name, value) {
		if (value === null || value === undefined || value === "") this._props.delete(name);
		else this._props.set(name, String(value));
	}
	removeProperty(name) {
		this._props.delete(name);
	}
	getPropertyValue(name) {
		return this._props.get(name) ?? "";
	}
	toJSON() {
		return Object.fromEntries(this._props);
	}
}

export class DomComment extends DomNode {
	constructor(text) {
		super("#comment");
		this.nodeType = 8;
		this._text = String(text);
	}
	get nodeValue() {
		return this._text;
	}
}

export function createDocument() {
	const doc = new DomNode("#document");
	doc.createElement = (tag) => new DomNode(tag);
	doc.createTextNode = (t) => new DomText(t);
	doc.createComment = (t) => new DomComment(t);
	doc.documentElement = new DomNode("html");
	doc.appendChild(doc.documentElement);
	doc.body = new DomNode("body");
	doc.documentElement.appendChild(doc.body);
	doc.getElementById = (id) => {
		let found = null;
		const walk = (node) => {
			if (found !== null || node.attributes === undefined) return;
			if (node.attributes.get("id") === id) { found = node; return; }
			for (const c of node.childNodes) walk(c);
		};
		walk(doc);
		return found;
	};
	// 极简 querySelector：支持 "[name=x]" 与 "tag[name=x]" 形态（测试够用）。
	doc.querySelector = (sel) => {
		const m = /^(?:[a-zA-Z]+)?\[([a-zA-Z-]+)="([^"]*)"\]$/.exec(sel);
		if (m === null) return null;
		let found = null;
		const walk = (node) => {
			if (found !== null || node.attributes === undefined) return;
			if (node.attributes.get(m[1]) === m[2]) { found = node; return; }
			for (const c of node.childNodes) walk(c);
		};
		walk(doc);
		return found;
	};
	return doc;
}
