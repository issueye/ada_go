/*
 * 最小 DOM 夹具：给 ada-render.js / ada-ui.js 的 Node 测试用。
 * 只实现引擎与组件实际用到的 API；HTML 解析是刻意保守的子集，
 * 用于把组件模板字符串变成可查询的节点树，不追求浏览器兼容性。
 */
'use strict';

const SVG_NS = 'http://www.w3.org/2000/svg';

const VOID_ELEMENTS = {
  area: true, base: true, br: true, col: true, embed: true, hr: true, img: true,
  input: true, link: true, meta: true, param: true, source: true, track: true, wbr: true
};

const ENTITIES = {
  amp: '&',
  lt: '<',
  gt: '>',
  quot: '"',
  apos: "'",
  nbsp: '\u00a0'
};

class DomNode {
  constructor(doc, nodeType) {
    this.ownerDocument = doc;
    this.nodeType = nodeType;
    this.parentNode = null;
    this.childNodes = [];
  }
  get firstChild() { return this.childNodes[0] || null; }
  get lastChild() { return this.childNodes[this.childNodes.length - 1] || null; }
  get nextSibling() {
    if (!this.parentNode) return null;
    const siblings = this.parentNode.childNodes;
    return siblings[siblings.indexOf(this) + 1] || null;
  }
  get previousSibling() {
    if (!this.parentNode) return null;
    const siblings = this.parentNode.childNodes;
    return siblings[siblings.indexOf(this) - 1] || null;
  }
  get children() {
    return this.childNodes.filter((node) => node.nodeType === 1);
  }
  appendChild(node) { return this.insertBefore(node, null); }
  insertBefore(node, ref) {
    if (node.parentNode) node.parentNode.removeChild(node);
    const index = ref ? this.childNodes.indexOf(ref) : this.childNodes.length;
    if (index === -1) throw new Error('insertBefore: 参照节点不在父节点内');
    this.childNodes.splice(index, 0, node);
    node.parentNode = this;
    return node;
  }
  removeChild(node) {
    const index = this.childNodes.indexOf(node);
    if (index === -1) throw new Error('removeChild: 节点不在父节点内');
    this.childNodes.splice(index, 1);
    node.parentNode = null;
    return node;
  }
  replaceChild(next, old) {
    const index = this.childNodes.indexOf(old);
    if (index === -1) throw new Error('replaceChild: 节点不在父节点内');
    if (next.parentNode) next.parentNode.removeChild(next);
    this.childNodes[index] = next;
    next.parentNode = this;
    old.parentNode = null;
    return old;
  }
  remove() {
    if (this.parentNode) this.parentNode.removeChild(this);
  }
  get textContent() {
    return this.childNodes.map((child) => child.textContent).join('');
  }
  set textContent(value) {
    while (this.childNodes.length) this.removeChild(this.childNodes[0]);
    if (value !== '' && value !== null && value !== undefined) {
      this.appendChild(this.ownerDocument.createTextNode(String(value)));
    }
  }
}

class DomText extends DomNode {
  constructor(doc, value) {
    super(doc, 3);
    this.nodeValue = String(value);
  }
  get textContent() { return this.nodeValue; }
  set textContent(value) { this.nodeValue = String(value); }
  get data() { return this.nodeValue; }
  set data(value) { this.nodeValue = String(value); }
}

class DomComment extends DomNode {
  constructor(doc, value) {
    super(doc, 8);
    this.nodeValue = String(value);
  }
  get textContent() { return ''; }
}

class DomElement extends DomNode {
  constructor(doc, tagName, namespaceURI) {
    super(doc, 1);
    this.tagName = String(tagName).toUpperCase();
    this.namespaceURI = namespaceURI || null;
    this._attributes = [];
    this._listeners = [];
    this._innerHTML = null;
    this.value = '';
    this.checked = false;
    this.selected = false;
    this.disabled = false;
    this.hidden = false;
    this.open = false;
    this.multiple = false;
    this.readOnly = false;
    this.required = false;
    this.content = this.tagName === 'TEMPLATE' ? new DomFragment(doc) : null;
  }
  get attributes() { return this._attributes; }
  get id() { return this.getAttribute('id') || ''; }
  set id(value) { this.setAttribute('id', value); }
  get className() { return this.getAttribute('class') || ''; }
  set className(value) { this.setAttribute('class', value); }
  setAttribute(name, value) {
    const key = String(name);
    const text = String(value);
    const found = this._attributes.find((attr) => attr.name === key);
    if (found) found.value = text;
    else this._attributes.push({ name: key, value: text });
  }
  getAttribute(name) {
    const found = this._attributes.find((attr) => attr.name === String(name));
    return found ? found.value : null;
  }
  removeAttribute(name) {
    const index = this._attributes.findIndex((attr) => attr.name === String(name));
    if (index !== -1) this._attributes.splice(index, 1);
  }
  hasAttribute(name) {
    return this._attributes.some((attr) => attr.name === String(name));
  }
  addEventListener(name, fn, options) {
    this._listeners.push({ name, fn, options: options || false });
  }
  removeEventListener(name, fn) {
    this._listeners = this._listeners.filter((item) => !(item.name === name && item.fn === fn));
  }
  dispatchEvent(event) {
    if (!event || !event.type) throw new Error('dispatchEvent: 事件缺少 type');
    if (!event.target) event.target = this;
    if (!event.preventDefault) event.preventDefault = function () { event.defaultPrevented = true; };
    if (!event.stopPropagation) event.stopPropagation = function () { event.propagationStopped = true; };
    let node = this;
    while (node) {
      event.currentTarget = node;
      const listeners = node._listeners ? node._listeners.slice() : [];
      for (const listener of listeners) {
        if (listener.name !== event.type) continue;
        listener.fn.call(node, event);
        if (listener.options && listener.options.once) node.removeEventListener(listener.name, listener.fn);
        if (event.propagationStopped) break;
      }
      if (event.propagationStopped) break;
      node = node.parentNode;
    }
    return !event.defaultPrevented;
  }
  click() {
    return this.dispatchEvent({ type: 'click', button: 0 });
  }
  focus() {
    this.ownerDocument.activeElement = this;
  }
  blur() {
    if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null;
  }
  get innerHTML() {
    if (this._innerHTML !== null) return this._innerHTML;
    const target = this.content || this;
    return target.childNodes.map(serialize).join('');
  }
  set innerHTML(value) {
    this._innerHTML = String(value);
    const target = this.content || this;
    while (target.childNodes.length) target.removeChild(target.childNodes[0]);
    parseInto(String(value), target, this.ownerDocument);
  }
  get options() {
    return this.children.filter((child) => child.tagName === 'OPTION');
  }
  get selectedOptions() {
    return this.options.filter((option) => option.selected);
  }
  matches(selector) {
    return matchesSelector(this, selector);
  }
  closest(selector) {
    let node = this;
    while (node && node.nodeType === 1) {
      if (matchesSelector(node, selector)) return node;
      node = node.parentNode;
    }
    return null;
  }
  querySelector(selector) {
    return this.querySelectorAll(selector)[0] || null;
  }
  querySelectorAll(selector) {
    const out = [];
    const walk = (node) => {
      for (const child of node.childNodes) {
        if (child.nodeType === 1 && matchesSelector(child, selector)) out.push(child);
        walk(child);
      }
    };
    walk(this);
    return out;
  }
}

class DomFragment extends DomNode {
  constructor(doc) { super(doc, 11); }
}

class DomDocument {
  constructor() {
    this.ownerDocument = null;
    this.nodeType = 9;
    this.childNodes = [];
    this._listeners = [];
    this.activeElement = null;
    this.body = this.createElement('body');
    this.appendChild(this.body);
  }
  createElement(tag) { return new DomElement(this, tag); }
  createElementNS(namespaceURI, tag) { return new DomElement(this, tag, namespaceURI); }
  createTextNode(value) { return new DomText(this, value); }
  createComment(value) { return new DomComment(this, value); }
  createDocumentFragment() { return new DomFragment(this); }
  appendChild(node) { return DomNode.prototype.appendChild.call(this, node); }
  insertBefore(node, ref) { return DomNode.prototype.insertBefore.call(this, node, ref); }
  replaceChild(next, old) { return DomNode.prototype.replaceChild.call(this, next, old); }
  removeChild(node) { return DomNode.prototype.removeChild.call(this, node); }
  get firstChild() { return this.childNodes[0] || null; }
  querySelectorAll(selector) {
    const out = [];
    const walk = (node) => {
      for (const child of node.childNodes) {
        if (child.nodeType === 1 && matchesSelector(child, selector)) out.push(child);
        walk(child);
      }
    };
    walk(this);
    return out;
  }
  querySelector(selector) {
    return this.querySelectorAll(selector)[0] || null;
  }
  getElementById(id) {
    return this.querySelector('#' + id);
  }
  addEventListener(name, fn) {
    this._listeners.push({ name, fn });
  }
  removeEventListener(name, fn) {
    this._listeners = this._listeners.filter((item) => !(item.name === name && item.fn === fn));
  }
  dispatchEvent(event) {
    if (!event.target) event.target = this;
    if (!event.preventDefault) event.preventDefault = function () { event.defaultPrevented = true; };
    for (const listener of this._listeners.slice()) {
      if (listener.name === event.type) listener.fn.call(this, event);
    }
    return !event.defaultPrevented;
  }
}

function createDocument() {
  return new DomDocument();
}

function decodeEntities(text) {
  return String(text).replace(/&(#x?[0-9a-fA-F]+|[a-zA-Z]+);/g, (match, entity) => {
    if (entity.charAt(0) === '#') {
      const hex = entity.charAt(1) === 'x' || entity.charAt(1) === 'X';
      const code = parseInt(entity.slice(hex ? 2 : 1), hex ? 16 : 10);
      return Number.isFinite(code) ? String.fromCharCode(code) : match;
    }
    return Object.prototype.hasOwnProperty.call(ENTITIES, entity) ? ENTITIES[entity] : match;
  });
}

function findTagEnd(html, start) {
  let quote = null;
  for (let i = start + 1; i < html.length; i += 1) {
    const ch = html[i];
    if (quote) {
      if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") {
      quote = ch;
      continue;
    }
    if (ch === '>') return i;
  }
  return html.length;
}

function parseAttributes(source) {
  const out = [];
  const pattern = /([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>`]+)))?/g;
  let match;
  while ((match = pattern.exec(source))) {
    const name = match[1];
    if (name === '/') continue;
    const value = match[2] !== undefined ? match[2] : match[3] !== undefined ? match[3] : match[4] !== undefined ? match[4] : '';
    out.push([name, decodeEntities(value)]);
  }
  return out;
}

function parseInto(html, target, doc) {
  const stack = [{ node: target, tag: null, svg: false }];
  let i = 0;
  while (i < html.length) {
    if (html[i] === '<') {
      if (html.startsWith('<!--', i)) {
        const end = html.indexOf('-->', i + 4);
        if (end === -1) break;
        i = end + 3;
        continue;
      }
      if (html.startsWith('<!', i)) {
        const end = html.indexOf('>', i);
        if (end === -1) break;
        i = end + 1;
        continue;
      }
      if (html[i + 1] === '/') {
        const end = html.indexOf('>', i);
        if (end === -1) break;
        const tag = html.slice(i + 2, end).trim().toLowerCase();
        let depth = stack.length - 1;
        while (depth > 0 && stack[depth].tag !== tag) depth -= 1;
        if (depth > 0) stack.length = depth;
        i = end + 1;
        continue;
      }
      const end = findTagEnd(html, i);
      let raw = html.slice(i + 1, end);
      const selfClosing = /\/\s*$/.test(raw);
      if (selfClosing) raw = raw.replace(/\/\s*$/, '');
      const space = raw.search(/\s/);
      const tag = (space === -1 ? raw : raw.slice(0, space)).toLowerCase();
      const attrSource = space === -1 ? '' : raw.slice(space);
      const parent = stack[stack.length - 1];
      const inSvg = parent.svg || tag === 'svg';
      const el = inSvg ? doc.createElementNS(SVG_NS, tag) : doc.createElement(tag);
      for (const [name, value] of parseAttributes(attrSource)) el.setAttribute(name, value);
      parent.node.appendChild(el);
      if (!selfClosing && !VOID_ELEMENTS[tag]) {
        stack.push({
          node: tag === 'template' ? el.content : el,
          tag: tag === 'template' ? 'template' : tag,
          svg: inSvg
        });
      }
      i = end + 1;
      continue;
    }
    const next = html.indexOf('<', i);
    const text = html.slice(i, next === -1 ? html.length : next);
    if (text) stack[stack.length - 1].node.appendChild(doc.createTextNode(decodeEntities(text)));
    i = next === -1 ? html.length : next;
  }
}

function matchesSimple(el, selector) {
  if (!el || el.nodeType !== 1) return false;
  let rest = selector.trim();
  if (!rest) return false;
  const tagMatch = /^[A-Za-z][\w-]*/.exec(rest);
  if (tagMatch) {
    if (el.tagName !== tagMatch[0].toUpperCase()) return false;
    rest = rest.slice(tagMatch[0].length);
  }
  while (rest) {
    if (rest.charAt(0) === '.') {
      const match = /^\.([\w-]+)/.exec(rest);
      if (!match) return false;
      const classes = (el.getAttribute('class') || '').split(/\s+/);
      if (classes.indexOf(match[1]) === -1) return false;
      rest = rest.slice(match[0].length);
      continue;
    }
    if (rest.charAt(0) === '#') {
      const match = /^#([\w-]+)/.exec(rest);
      if (!match || el.getAttribute('id') !== match[1]) return false;
      rest = rest.slice(match[0].length);
      continue;
    }
    if (rest.charAt(0) === '[') {
      const end = rest.indexOf(']');
      if (end === -1) return false;
      const expr = rest.slice(1, end);
      const eq = expr.indexOf('=');
      if (eq === -1) {
        if (!el.hasAttribute(expr)) return false;
      } else {
        const name = expr.slice(0, eq);
        let value = expr.slice(eq + 1);
        if ((value.charAt(0) === '"' && value.charAt(value.length - 1) === '"') ||
            (value.charAt(0) === "'" && value.charAt(value.length - 1) === "'")) {
          value = value.slice(1, -1);
        }
        if (el.getAttribute(name) !== value) return false;
      }
      rest = rest.slice(end + 1);
      continue;
    }
    return false;
  }
  return true;
}

function matchesSelector(el, selector) {
  const parts = String(selector).trim().split(/\s+/);
  if (parts.length === 1) return matchesSimple(el, parts[0]);
  let node = el;
  if (!matchesSimple(node, parts[parts.length - 1])) return false;
  for (let i = parts.length - 2; i >= 0; i -= 1) {
    node = node.parentNode;
    while (node && node.nodeType === 1 && !matchesSimple(node, parts[i])) node = node.parentNode;
    if (!node || node.nodeType !== 1) return false;
  }
  return true;
}

function serialize(node) {
  if (node.nodeType === 3) return node.nodeValue;
  if (node.nodeType === 8) return '<!--' + node.nodeValue + '-->';
  if (node.nodeType === 11) return node.childNodes.map(serialize).join('');
  if (node.nodeType === 9) return node.childNodes.map(serialize).join('');
  const attrs = (node.attributes || [])
    .map((attr) => ' ' + attr.name + '="' + attr.value + '"')
    .join('');
  const tag = node.tagName.toLowerCase();
  return '<' + tag + attrs + '>' + node.childNodes.map(serialize).join('') + '</' + tag + '>';
}

module.exports = {
  createDocument,
  DomDocument,
  DomElement,
  DomText,
  DomFragment,
  DomComment,
  matchesSimple,
  matchesSelector
};
