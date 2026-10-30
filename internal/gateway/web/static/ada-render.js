/*
 * ada-render.js · 单文件 VNode 渲染运行时（无依赖、CSP 安全）
 * ---------------------------------------------------------------------------
 * 定位：不是第二个 Vue。它是 ada-go 已有 VNode 契约的浏览器运行端：
 *
 *   Go SSR：数据 → VNode JSON → frontend.RenderHTML → HTML（唯一转义出口）
 *   浏览器：同一份 VNode JSON / 同一份组件定义 → DOM patch / hydrate
 *
 * 因此后端不再手写 DOM 字符串；VNode 是唯一结构真源，Go 只负责把已校验的
 * VNode 序列化成 HTML，浏览器只负责把同一棵树挂到 DOM 上。
 *
 * 三条硬约束（与仓库现状对齐）：
 *   1. CSP 是 `script-src 'self'`，没有 `unsafe-eval`：
 *      **不使用 eval / new Function**。模板表达式由内置词法+语法解释器执行。
 *   2. 依赖为零：整个运行时就是一个文件，不引入框架、构建链或 polyfill。
 *   3. 渐进增强：引擎只接管标记了 data-ada-app 的区域；表单、链接、
 *      <details> 在没有 JS 时仍然是完整的 HTML（SSR 输出不变）。
 *
 * VNode 线格式（与 spec/vnode.schema.json 对齐）：
 *   元素      { t: "div", p: {...}, c: [...] }
 *   组件      { t: "$C", n: "Button", p: {...}, c: [...] }
 *   文本      { t: "#text", v: "..." }
 * 引擎内部字段（el/_e/_html/_ref/s/k）不会进入线格式。
 *
 * 模板语法（Vue 子集，刻意保守）：
 *   {{ expr }}  v-if/v-else-if/v-else  v-for  v-show  v-text
 *   :attr / v-bind:attr  @event / v-on:event  v-model  ref  key
 *   <slot> / <template v-slot:name> / #name
 *   v-html 默认关闭；只有 createApp({ allowHTML: true }) 且内容已经由
 *   服务端 bluemonday 清洗时才允许开启（用于 Markdown 正文）。
 *   不支持：自定义指令、transition、异步组件、Suspense、render 级 eval。
 */
(function (global, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory();
  } else {
    global.AdaRender = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = '0.1.0';
  var TEXT = '#text';
  var COMPONENT = '$C';
  var SVG_NS = 'http://www.w3.org/2000/svg';
  var DOC = typeof document !== 'undefined' ? document : null;
  var EMPTY_COMPONENTS = Object.create(null);

  /* ═══════════════════════════════════════════════════════════════════
   * 1. 响应式：Proxy 依赖收集 + 微任务调度
   * ═══════════════════════════════════════════════════════════════════ */

  var activeEffect = null;
  var effectStack = [];
  var targetMap = new WeakMap();
  var effectUid = 0;
  var RAW = typeof Symbol === 'function' ? Symbol('raw') : '__ada_raw__';

  function isObject(value) {
    return value !== null && typeof value === 'object';
  }

  function isPlainObject(value) {
    if (!isObject(value)) return false;
    var proto = Object.getPrototypeOf(value);
    return proto === Object.prototype || proto === null;
  }

  function toRaw(value) {
    return value && value[RAW] ? value[RAW] : value;
  }

  function trackDep(dep) {
    if (!activeEffect) return;
    dep.add(activeEffect);
    activeEffect.deps.push(dep);
  }

  function triggerDep(dep) {
    if (!dep) return;
    var effects = Array.from(dep);
    for (var i = 0; i < effects.length; i += 1) {
      var runner = effects[i];
      if (runner === activeEffect || !runner.active) continue;
      if (runner.scheduler) runner.scheduler();
      else runner();
    }
  }

  function track(target, key) {
    if (!activeEffect) return;
    var depsMap = targetMap.get(target);
    if (!depsMap) {
      depsMap = new Map();
      targetMap.set(target, depsMap);
    }
    var dep = depsMap.get(key);
    if (!dep) {
      dep = new Set();
      depsMap.set(key, dep);
    }
    trackDep(dep);
  }

  function trigger(target, key) {
    var depsMap = targetMap.get(target);
    if (!depsMap) return;
    var dep = depsMap.get(key);
    if (dep) triggerDep(dep);
  }

  function cleanupEffect(runner) {
    for (var i = 0; i < runner.deps.length; i += 1) {
      runner.deps[i].delete(runner);
    }
    runner.deps.length = 0;
  }

  function effect(fn, options) {
    options = options || {};
    var runner = function () {
      if (!runner.active) return undefined;
      cleanupEffect(runner);
      effectStack.push(runner);
      activeEffect = runner;
      try {
        return fn();
      } finally {
        effectStack.pop();
        activeEffect = effectStack.length ? effectStack[effectStack.length - 1] : null;
      }
    };
    runner.uid = ++effectUid;
    runner.active = true;
    runner.deps = [];
    runner.scheduler = options.scheduler || null;
    if (!options.lazy) runner();
    return runner;
  }

  function stop(runner) {
    if (runner) {
      runner.active = false;
      cleanupEffect(runner);
    }
  }

  var proxyCache = new WeakMap();

  function isObservable(value) {
    return Array.isArray(value) || isPlainObject(value);
  }

  function reactive(target) {
    if (!isObject(target) || !isObservable(target)) return target;
    if (target[RAW]) return target;
    var cached = proxyCache.get(target);
    if (cached) return cached;

    var proxy = new Proxy(target, {
      get: function (obj, key, receiver) {
        if (key === RAW) return obj;
        var value = Reflect.get(obj, key, receiver);
        track(obj, key);
        if (isRef(value) && !Array.isArray(obj)) {
          trackDep(value.dep);
          return value.value;
        }
        if (isObservable(value)) {
          return reactive(value);
        }
        return value;
      },
      set: function (obj, key, value, receiver) {
        var old = obj[key];
        var next = toRaw(value);
        if (isRef(old) && !isRef(value)) {
          old.value = next;
          trigger(obj, key);
          return true;
        }
        var had = Object.prototype.hasOwnProperty.call(obj, key);
        var ok = Reflect.set(obj, key, next, receiver);
        if (!had || old !== next) trigger(obj, key);
        return ok;
      },
      deleteProperty: function (obj, key) {
        var had = Object.prototype.hasOwnProperty.call(obj, key);
        var ok = Reflect.deleteProperty(obj, key);
        if (had) trigger(obj, key);
        return ok;
      },
      has: function (obj, key) {
        track(obj, key);
        return Reflect.has(obj, key);
      }
    });
    proxyCache.set(target, proxy);
    return proxy;
  }

  function RefImpl(value) {
    this._rawValue = value;
    this._value = isObject(value) && !isRef(value) ? reactive(value) : value;
    this.dep = new Set();
  }

  Object.defineProperty(RefImpl.prototype, 'value', {
    get: function () {
      trackDep(this.dep);
      return this._value;
    },
    set: function (next) {
      var raw = toRaw(next);
      if (raw === this._value) return;
      this._value = isObject(raw) && !isRef(raw) ? reactive(raw) : raw;
      triggerDep(this.dep);
    }
  });

  function isRef(value) {
    return !!(value && value.__ada_ref === true);
  }

  function ref(value) {
    if (isRef(value)) return value;
    var instance = new RefImpl(value);
    Object.defineProperty(instance, '__ada_ref', { value: true });
    return instance;
  }

  function unref(value) {
    return isRef(value) ? value.value : value;
  }

  function ComputedRef(getter) {
    this._getter = getter;
    this._value = undefined;
    this._dirty = true;
    this.dep = new Set();
    var self = this;
    this._runner = effect(getter, {
      lazy: true,
      scheduler: function () {
        if (!self._dirty) {
          self._dirty = true;
          triggerDep(self.dep);
        }
      }
    });
  }

  Object.defineProperty(ComputedRef.prototype, 'value', {
    get: function () {
      if (this._dirty) {
        this._value = this._runner();
        this._dirty = false;
      }
      trackDep(this.dep);
      return this._value;
    }
  });

  function computed(getter) {
    var instance = new ComputedRef(getter);
    Object.defineProperty(instance, '__ada_ref', { value: true });
    Object.defineProperty(instance, '__ada_computed', { value: true });
    return instance;
  }

  function watchEffect(fn) {
    var runner;
    runner = effect(fn, {
      lazy: true,
      scheduler: function () { queueJob(runner); }
    });
    runner();
    return runner;
  }

  /* ---- 调度：一次事件循环内合并同一组件的重复更新 ---- */

  var queue = new Set();
  var flushing = false;
  var flushPromise = null;

  function queueJob(job) {
    queue.add(job);
    if (flushing) return;
    if (!flushPromise) {
      flushPromise = Promise.resolve().then(flushJobs);
    }
  }

  function flushJobs() {
    flushing = true;
    try {
      var jobs = Array.from(queue).sort(function (a, b) {
        return (a.uid || 0) - (b.uid || 0);
      });
      queue.clear();
      for (var i = 0; i < jobs.length; i += 1) jobs[i]();
    } finally {
      flushing = false;
      flushPromise = null;
      if (queue.size) flushPromise = Promise.resolve().then(flushJobs);
    }
  }

  function nextTick(fn) {
    var pending = flushPromise || Promise.resolve();
    return typeof fn === 'function' ? pending.then(fn) : pending;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 2. VNode：线格式与内部节点共用一套形状
   * ═══════════════════════════════════════════════════════════════════ */

  function text(value) {
    return { t: TEXT, v: value === null || value === undefined ? '' : String(value) };
  }

  function normalizeChildren(input, out) {
    out = out || [];
    for (var i = 0; i < input.length; i += 1) {
      var child = input[i];
      if (child === null || child === undefined || typeof child === 'boolean') continue;
      if (Array.isArray(child)) {
        normalizeChildren(child, out);
      } else if (isVNode(child)) {
        out.push(child);
      } else {
        out.push(text(child));
      }
    }
    return out;
  }

  function isVNode(value) {
    return isObject(value) && typeof value.t === 'string';
  }

  function isRenderable(value) {
    return value !== null && value !== undefined && typeof value !== 'boolean';
  }

  function isComponentDef(value) {
    if (typeof value === 'function') return true;
    return isObject(value) && (typeof value.render === 'function' || typeof value.setup === 'function' ||
      typeof value.template === 'string' || isObject(value.template));
  }

  function h(type, props) {
    var children = [];
    var rest = Array.prototype.slice.call(arguments, 2);
    if (arguments.length === 2 && isRenderable(props) && !isPlainObject(props)) {
      rest = [props];
      props = {};
    }
    normalizeChildren(rest, children);

    if (typeof type === 'string') {
      if (type === TEXT) return text(props && props.v);
      var staticProps = props ? shallowCopy(props) : {};
      var key = staticProps.key;
      if (key !== undefined) delete staticProps.key;
      var events = [];
      for (var propName in staticProps) {
        if (!Object.prototype.hasOwnProperty.call(staticProps, propName)) continue;
        if (typeof staticProps[propName] !== 'function') continue;
        var eventName = null;
        if (/^on[A-Z]/.test(propName)) eventName = propName.slice(2).toLowerCase();
        else if (propName.indexOf('on:') === 0) eventName = propName.slice(3).toLowerCase();
        if (eventName) {
          events.push({ name: eventName, handler: staticProps[propName], modifiers: [] });
          delete staticProps[propName];
        }
      }
      var vnode = { t: type, p: staticProps, c: children, k: key };
      if (events.length) vnode._e = events;
      return vnode;
    }
    if (isComponentDef(type)) {
      var componentProps = props ? shallowCopy(props) : {};
      var componentKey = componentProps.key;
      if (componentKey !== undefined) delete componentProps.key;
      var listeners = {};
      for (var listenerName in componentProps) {
        if (!Object.prototype.hasOwnProperty.call(componentProps, listenerName)) continue;
        if (typeof componentProps[listenerName] !== 'function') continue;
        var emitted = null;
        if (/^on[A-Z]/.test(listenerName)) {
          emitted = listenerName.slice(2);
          emitted = emitted.charAt(0).toLowerCase() + emitted.slice(1);
        } else if (listenerName.indexOf('on:') === 0) {
          emitted = listenerName.slice(3);
        }
        if (emitted) {
          if (!listeners[emitted]) listeners[emitted] = [];
          listeners[emitted].push(componentProps[listenerName]);
          delete componentProps[listenerName];
        }
      }
      if (Object.keys(listeners).length) {
        var existing = componentProps.$on || {};
        for (var eventKey in listeners) {
          if (Object.prototype.hasOwnProperty.call(listeners, eventKey)) {
            existing[eventKey] = (existing[eventKey] || []).concat(listeners[eventKey]);
          }
        }
        componentProps.$on = existing;
      }
      return { t: COMPONENT, n: type, p: componentProps, c: children, k: componentKey };
    }
    if (Array.isArray(type)) {
      return normalizeChildren([type].concat(children), []);
    }
    throw new TypeError('[ada-render] h(): 非法节点类型 ' + typeof type);
  }

  function shallowCopy(source) {
    var out = {};
    for (var key in source) {
      if (Object.prototype.hasOwnProperty.call(source, key)) out[key] = source[key];
    }
    return out;
  }

  /* 显式构造宿主组件节点：{ t:"$C", n:"Button", p, c }。
     字符串标签一律是 HTML 元素，避免把自定义元素误判成组件。 */
  function host(name, props) {
    var children = normalizeChildren(Array.prototype.slice.call(arguments, 2), []);
    var out = { t: COMPONENT, n: String(name), p: props ? shallowCopy(props) : {}, c: children };
    if (props && props.key !== undefined) {
      out.k = props.key;
      delete out.p.key;
    }
    return out;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 3. 模板表达式：词法 + 语法 + 求值（不经过 Function/eval）
   * ═══════════════════════════════════════════════════════════════════ */

  var BANNED_PROPERTIES = {
    __proto__: true,
    prototype: true,
    constructor: true,
    caller: true,
    callee: true,
    arguments: true
  };

  var GLOBAL_ALLOWLIST = {
    Math: Math,
    JSON: JSON,
    Date: Date,
    String: String,
    Number: Number,
    Boolean: Boolean,
    Array: Array,
    Object: Object,
    Intl: typeof Intl !== 'undefined' ? Intl : undefined,
    isNaN: isNaN,
    isFinite: isFinite,
    parseInt: parseInt,
    parseFloat: parseFloat,
    encodeURIComponent: encodeURIComponent,
    decodeURIComponent: decodeURIComponent
  };

  var PUNCTUATIONS = [
    '===', '!==', '>>>', '**=',
    '==', '!=', '>=', '<=', '&&', '||', '??', '?.', '=>',
    '+', '-', '*', '/', '%', '!', '?', ':', '.', ',', '(', ')', '[', ']', '{', '}'
  ];

  function syntaxError(message, source, position) {
    var detail = source ? '：' + String(source).slice(0, 120) : '';
    return new Error('[ada-render] 模板表达式错误' + detail + (position !== undefined ? ' @' + position : '') + '：' + message);
  }

  function tokenize(source) {
    var tokens = [];
    var i = 0;
    while (i < source.length) {
      var ch = source[i];
      if (/\s/.test(ch)) {
        i += 1;
        continue;
      }
      if (ch === '"' || ch === "'") {
        var quote = ch;
        var value = '';
        i += 1;
        var closed = false;
        while (i < source.length) {
          var c = source[i];
          if (c === '\\') {
            var esc = source[i + 1];
            var map = { n: '\n', r: '\r', t: '\t', b: '\b', f: '\f', v: '\v', '0': '\0' };
            if (esc === 'u') {
              value += String.fromCharCode(parseInt(source.slice(i + 2, i + 6), 16));
              i += 6;
            } else {
              value += map[esc] !== undefined ? map[esc] : esc;
              i += 2;
            }
            continue;
          }
          if (c === quote) {
            i += 1;
            closed = true;
            break;
          }
          value += c;
          i += 1;
        }
        if (!closed) throw syntaxError('字符串没有闭合', source, i);
        tokens.push({ type: 'str', value: value });
        continue;
      }
      if (/[0-9]/.test(ch) || (ch === '.' && /[0-9]/.test(source[i + 1]))) {
        var start = i;
        if (ch === '0' && (source[i + 1] === 'x' || source[i + 1] === 'X')) {
          i += 2;
          while (i < source.length && /[0-9a-fA-F]/.test(source[i])) i += 1;
          tokens.push({ type: 'num', value: parseInt(source.slice(start, i), 16) });
          continue;
        }
        while (i < source.length && /[0-9]/.test(source[i])) i += 1;
        if (source[i] === '.') {
          i += 1;
          while (i < source.length && /[0-9]/.test(source[i])) i += 1;
        }
        if (source[i] === 'e' || source[i] === 'E') {
          i += 1;
          if (source[i] === '+' || source[i] === '-') i += 1;
          while (i < source.length && /[0-9]/.test(source[i])) i += 1;
        }
        tokens.push({ type: 'num', value: parseFloat(source.slice(start, i)) });
        continue;
      }
      if (/[A-Za-z_$]/.test(ch)) {
        var idStart = i;
        i += 1;
        while (i < source.length && /[A-Za-z0-9_$]/.test(source[i])) i += 1;
        tokens.push({ type: 'id', value: source.slice(idStart, i) });
        continue;
      }
      var matched = null;
      for (var p = 0; p < PUNCTUATIONS.length; p += 1) {
        if (source.slice(i, i + PUNCTUATIONS[p].length) === PUNCTUATIONS[p]) {
          matched = PUNCTUATIONS[p];
          break;
        }
      }
      if (!matched) throw syntaxError('无法识别的字符 ' + JSON.stringify(ch), source, i);
      if (matched === '=>') throw syntaxError('模板不支持箭头函数', source, i);
      tokens.push({ type: 'punc', value: matched });
      i += matched.length;
    }
    tokens.push({ type: 'eof', value: '' });
    return tokens;
  }

  var BINARY_PRECEDENCE = {
    '??': 1,
    '||': 2,
    '&&': 3,
    '==': 4, '!=': 4, '===': 4, '!==': 4,
    '<': 5, '>': 5, '<=': 5, '>=': 5,
    '+': 6, '-': 6,
    '*': 7, '/': 7, '%': 7
  };

  function parseExpression(source) {
    var tokens = tokenize(source);
    var pos = 0;

    function peek() { return tokens[pos]; }
    function next() { return tokens[pos++]; }
    function accept(value) {
      if (peek().value === value) {
        pos += 1;
        return true;
      }
      return false;
    }
    function expect(value) {
      if (!accept(value)) throw syntaxError('缺少 ' + value, source, pos);
    }

    function parsePrimary() {
      var token = next();
      if (token.type === 'num' || token.type === 'str') return { type: 'literal', value: token.value };
      if (token.type === 'id') {
        if (token.value === 'true') return { type: 'literal', value: true };
        if (token.value === 'false') return { type: 'literal', value: false };
        if (token.value === 'null') return { type: 'literal', value: null };
        if (token.value === 'undefined') return { type: 'literal', value: undefined };
        return { type: 'id', name: token.value };
      }
      if (token.value === '(') {
        var inner = parseTernary();
        expect(')');
        return inner;
      }
      if (token.value === '[') {
        var items = [];
        if (!accept(']')) {
          do {
            items.push(parseTernary());
          } while (accept(','));
          expect(']');
        }
        return { type: 'array', items: items };
      }
      if (token.value === '{') {
        var props = [];
        if (!accept('}')) {
          do {
            var keyToken = next();
            var key;
            if (keyToken.type === 'id' || keyToken.type === 'str' || keyToken.type === 'num') {
              key = String(keyToken.value);
            } else {
              throw syntaxError('对象字面量的键必须是标识符或字符串', source, pos);
            }
            expect(':');
            props.push([key, parseTernary()]);
          } while (accept(','));
          expect('}');
        }
        return { type: 'object', props: props };
      }
      throw syntaxError('意外的记号 ' + JSON.stringify(token.value), source, pos);
    }

    function parsePostfix() {
      var node = parsePrimary();
      while (true) {
        if (accept('.')) {
          var prop = next();
          if (prop.type !== 'id') throw syntaxError('. 后面必须是属性名', source, pos);
          node = { type: 'member', object: node, property: prop.value, optional: false };
          continue;
        }
        if (accept('?.')) {
          var opt = next();
          if (opt.type !== 'id') throw syntaxError('?. 后面必须是属性名', source, pos);
          node = { type: 'member', object: node, property: opt.value, optional: true };
          continue;
        }
        if (accept('[')) {
          var index = parseTernary();
          expect(']');
          node = { type: 'index', object: node, index: index };
          continue;
        }
        if (accept('(')) {
          var args = [];
          if (!accept(')')) {
            do {
              args.push(parseTernary());
            } while (accept(','));
            expect(')');
          }
          node = { type: 'call', callee: node, args: args };
          continue;
        }
        break;
      }
      return node;
    }

    function parseUnary() {
      if (accept('!') || accept('-') || accept('+')) {
        var op = tokens[pos - 1].value;
        return { type: 'unary', op: op, arg: parseUnary() };
      }
      return parsePostfix();
    }

    function parseBinary(minPrecedence) {
      var left = parseUnary();
      while (true) {
        var op = peek().value;
        var precedence = BINARY_PRECEDENCE[op];
        if (!precedence || precedence < minPrecedence) break;
        pos += 1;
        var right = parseBinary(precedence + 1);
        left = { type: 'binary', op: op, left: left, right: right };
      }
      return left;
    }

    function parseTernary() {
      var test = parseBinary(1);
      if (accept('?')) {
        var consequent = parseTernary();
        expect(':');
        var alternate = parseTernary();
        return { type: 'conditional', test: test, consequent: consequent, alternate: alternate };
      }
      return test;
    }

    var ast = parseTernary();
    if (peek().type !== 'eof') throw syntaxError('表达式末尾有多余内容', source, pos);
    return ast;
  }

  function checkProperty(property, source) {
    if (BANNED_PROPERTIES[String(property)]) {
      throw new Error('[ada-render] 模板禁止访问属性 ' + property + (source ? '（' + source + '）' : ''));
    }
  }

  function evalAst(node, scope) {
    switch (node.type) {
      case 'literal':
        return node.value;
      case 'id': {
        var scoped = scope ? scope[node.name] : undefined;
        if (scoped === undefined && scope && !(node.name in scope)) return GLOBAL_ALLOWLIST[node.name];
        return scoped;
      }
      case 'array':
        return node.items.map(function (item) { return evalAst(item, scope); });
      case 'object': {
        var object = {};
        for (var i = 0; i < node.props.length; i += 1) {
          object[node.props[i][0]] = evalAst(node.props[i][1], scope);
        }
        return object;
      }
      case 'unary': {
        var arg = evalAst(node.arg, scope);
        if (node.op === '!') return !arg;
        if (node.op === '-') return -arg;
        return +arg;
      }
      case 'binary':
        return evalBinary(node, scope);
      case 'conditional':
        return evalAst(node.test, scope) ? evalAst(node.consequent, scope) : evalAst(node.alternate, scope);
      case 'member': {
        var base = evalAst(node.object, scope);
        if (base === null || base === undefined) {
          if (node.optional) return undefined;
          throw new Error('[ada-render] 读取 ' + node.property + ' 时对象为空');
        }
        checkProperty(node.property);
        return base[node.property];
      }
      case 'index': {
        var target = evalAst(node.object, scope);
        var key = evalAst(node.index, scope);
        if (target === null || target === undefined) return undefined;
        checkProperty(key);
        return target[key];
      }
      case 'call':
        return evalCall(node, scope);
      default:
        throw new Error('[ada-render] 未知表达式节点 ' + node.type);
    }
  }

  function evalBinary(node, scope) {
    var op = node.op;
    if (op === '&&') {
      var leftAnd = evalAst(node.left, scope);
      return leftAnd ? evalAst(node.right, scope) : leftAnd;
    }
    if (op === '||') {
      var leftOr = evalAst(node.left, scope);
      return leftOr ? leftOr : evalAst(node.right, scope);
    }
    if (op === '??') {
      var leftNull = evalAst(node.left, scope);
      return leftNull === null || leftNull === undefined ? evalAst(node.right, scope) : leftNull;
    }
    var left = evalAst(node.left, scope);
    var right = evalAst(node.right, scope);
    switch (op) {
      case '===': return left === right;
      case '!==': return left !== right;
      case '==': return left == right; // eslint-disable-line eqeqeq
      case '!=': return left != right; // eslint-disable-line eqeqeq
      case '<': return left < right;
      case '>': return left > right;
      case '<=': return left <= right;
      case '>=': return left >= right;
      case '+': return left + right;
      case '-': return left - right;
      case '*': return left * right;
      case '/': return left / right;
      case '%': return left % right;
      default: throw new Error('[ada-render] 未知运算符 ' + op);
    }
  }

  function evalCall(node, scope) {
    var args = node.args.map(function (arg) { return evalAst(arg, scope); });
    var callee = node.callee;
    if (callee.type === 'member' || callee.type === 'index') {
      var base = evalAst(callee.object, scope);
      var prop = callee.type === 'member' ? callee.property : evalAst(callee.index, scope);
      checkProperty(prop);
      if (base === null || base === undefined) {
        throw new Error('[ada-render] 调用 ' + prop + ' 时对象为空');
      }
      var method = base[prop];
      if (typeof method !== 'function') {
        throw new Error('[ada-render] ' + prop + ' 不是函数');
      }
      return method.apply(base, args);
    }
    var fn = evalAst(callee, scope);
    if (typeof fn !== 'function') throw new Error('[ada-render] 模板调用的值不是函数');
    return fn.apply(null, args);
  }

  function assignExpression(node, scope, value) {
    if (node.type === 'id') {
      scope[node.name] = value;
      return;
    }
    if (node.type === 'member') {
      var base = evalAst(node.object, scope);
      checkProperty(node.property);
      if (base === null || base === undefined) throw new Error('[ada-render] v-model 目标为空');
      base[node.property] = value;
      return;
    }
    if (node.type === 'index') {
      var target = evalAst(node.object, scope);
      var key = evalAst(node.index, scope);
      checkProperty(key);
      if (target === null || target === undefined) throw new Error('[ada-render] v-model 目标为空');
      target[key] = value;
      return;
    }
    throw new Error('[ada-render] v-model 需要可赋值表达式');
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 4. 作用域：模板标识符 → 组件状态 / props / 方法 / 循环变量
   * ═══════════════════════════════════════════════════════════════════ */

  function getFromInstance(instance, key) {
    if (!instance) {
      return GLOBAL_ALLOWLIST[key];
    }
    if (key === '$event') return undefined;
    if (key === '$slots') return instance.slots;
    if (key === '$refs') return instance.refs;
    if (key === '$props') return instance.props;
    if (key === '$attrs') return instance.vnode.p;
    if (key === '$emit') return instance.emit;
    if (key === '$app') return instance.app;
    if (instance.data && key in instance.data) return instance.data[key];
    if (instance.setupState && key in instance.setupState) {
      return unref(instance.setupState[key]);
    }
    if (instance.computed && Object.prototype.hasOwnProperty.call(instance.computed, key)) {
      return instance.computed[key].value;
    }
    if (instance.methods && Object.prototype.hasOwnProperty.call(instance.methods, key)) {
      return instance.methods[key];
    }
    if (instance.props && key in instance.props) {
      return instance.props[key];
    }
    return GLOBAL_ALLOWLIST[key];
  }

  function setInInstance(instance, key, value) {
    if (instance.data && key in instance.data) {
      instance.data[key] = value;
      return;
    }
    if (instance.setupState && key in instance.setupState) {
      if (isRef(instance.setupState[key])) instance.setupState[key].value = value;
      else instance.setupState[key] = value;
      return;
    }
    if (instance.props && key in instance.props) {
      throw new Error('[ada-render] 不能直接修改 prop "' + key + '"');
    }
    if (instance.data) instance.data[key] = value;
  }

  function createScope(instance) {
    var own = Object.create(null);
    return new Proxy(own, {
      get: function (target, key) {
        if (key in target) return target[key];
        return getFromInstance(instance, key);
      },
      set: function (target, key, value) {
        if (key in target) {
          target[key] = value;
          return true;
        }
        setInInstance(instance, key, value);
        return true;
      },
      has: function () {
        return true;
      }
    });
  }

  function childScope(parent, aliases) {
    var own = Object.create(null);
    if (aliases) {
      for (var key in aliases) {
        if (Object.prototype.hasOwnProperty.call(aliases, key)) own[key] = aliases[key];
      }
    }
    return new Proxy(own, {
      get: function (target, key) {
        if (key in target) return target[key];
        return parent[key];
      },
      set: function (target, key, value) {
        if (key in target) {
          target[key] = value;
          return true;
        }
        return Reflect.set(parent, key, value);
      },
      has: function (target, key) {
        return key in target || key in parent;
      }
    });
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 5. 模板编译：DOM → 描述树（DOM 只在浏览器/测试环境使用）
   * ═══════════════════════════════════════════════════════════════════ */

  function resolveTemplate(template, doc) {
    if (typeof template === 'string') {
      var trimmed = template.trim();
      if (trimmed.charAt(0) === '#' && doc) {
        var target = doc.querySelector(trimmed);
        if (!target) throw new Error('[ada-render] 找不到模板 ' + trimmed);
        return target.content || target;
      }
      if (!doc) {
        throw new Error('[ada-render] 字符串模板需要 DOM；服务端请使用 render(h) 函数');
      }
      var holder = doc.createElement('template');
      holder.innerHTML = String(template).trim();
      return holder.content;
    }
    if (template && template.nodeType === 1 && String(template.tagName).toLowerCase() === 'template') {
      return template.content || template;
    }
    if (template && (template.nodeType === 11 || template.nodeType === 1)) return template;
    throw new Error('[ada-render] template 必须是字符串或 <template>');
  }

  function compileChildren(parent, ctx) {
    var nodes = [];
    for (var i = 0; i < parent.childNodes.length; i += 1) nodes.push(parent.childNodes[i]);
    var out = [];
    for (var index = 0; index < nodes.length; index += 1) {
      var node = nodes[index];
      if (node.nodeType === 8) continue; // comment
      if (node.nodeType === 3) {
        var compiledText = compileText(node.nodeValue, ctx);
        if (compiledText) out.push(compiledText);
        continue;
      }
      if (node.nodeType !== 1) continue;
      if (node.hasAttribute('v-else') || node.hasAttribute('v-else-if')) {
        throw new Error('[ada-render] v-else / v-else-if 前面缺少 v-if');
      }
      if (node.hasAttribute('v-if')) {
        var group = compileIfGroup(nodes, index, ctx);
        out.push(group.node);
        index = group.next - 1;
        continue;
      }
      out.push(compileNode(node, ctx));
    }
    return out;
  }

  function compileIfGroup(nodes, start, ctx) {
    var first = nodes[start];
    var branches = [{ expr: parseExpression(first.getAttribute('v-if')), node: compileNode(first, ctx) }];
    var index = start + 1;
    while (index < nodes.length) {
      var candidate = nodes[index];
      if (candidate.nodeType === 3 && /^\s*$/.test(candidate.nodeValue)) {
        index += 1;
        continue;
      }
      if (candidate.nodeType === 8) {
        index += 1;
        continue;
      }
      if (candidate.nodeType !== 1) break;
      if (candidate.hasAttribute('v-else-if')) {
        branches.push({
          expr: parseExpression(candidate.getAttribute('v-else-if')),
          node: compileNode(candidate, ctx)
        });
        index += 1;
        continue;
      }
      if (candidate.hasAttribute('v-else')) {
        branches.push({ expr: null, node: compileNode(candidate, ctx) });
        index += 1;
      }
      break;
    }
    return { node: { type: 'if', branches: branches }, next: index };
  }

  function compileText(raw, ctx) {
    if (!raw) return null;
    var parts = [];
    var last = 0;
    var open = raw.indexOf('{{');
    while (open !== -1) {
      var close = raw.indexOf('}}', open + 2);
      if (close === -1) break;
      if (open > last) parts.push({ type: 'text', value: raw.slice(last, open) });
      parts.push({ type: 'expr', expr: parseExpression(raw.slice(open + 2, close)) });
      last = close + 2;
      open = raw.indexOf('{{', last);
    }
    if (!parts.length) {
      if (/^\s*$/.test(raw) && raw.indexOf('\n') !== -1) return null;
      return { type: 'text', value: raw };
    }
    if (last < raw.length) parts.push({ type: 'text', value: raw.slice(last) });
    if (parts.length === 1 && parts[0].type === 'text') return { type: 'text', value: parts[0].value };
    if (parts.length === 1 && parts[0].type === 'expr') return { type: 'interp', expr: parts[0].expr };
    return { type: 'interp-parts', parts: parts };
  }

  function parseFor(expression) {
    var match = /^\s*(?:\(\s*([A-Za-z_$][\w$]*)\s*(?:,\s*([A-Za-z_$][\w$]*)\s*)?\)|([A-Za-z_$][\w$]*))\s+(?:in|of)\s+([\s\S]+)$/.exec(expression);
    if (!match) throw new Error('[ada-render] v-for 语法应为 "item in items" 或 "(item, index) in items"');
    return {
      item: match[1] || match[3],
      index: match[2] || null,
      source: match[4]
    };
  }

  function splitDirective(raw) {
    var dot = raw.indexOf('.');
    if (dot === -1) return { body: raw, modifiers: [] };
    return { body: raw.slice(0, dot), modifiers: raw.slice(dot + 1).split('.') };
  }

  function directiveOf(name) {
    if (name.charAt(0) === ':') {
      var bind = splitDirective(name.slice(1));
      return { kind: 'bind', arg: bind.body, modifiers: bind.modifiers };
    }
    if (name.charAt(0) === '@') {
      var on = splitDirective(name.slice(1));
      return { kind: 'on', arg: on.body, modifiers: on.modifiers };
    }
    if (name.charAt(0) === '#') {
      var slot = splitDirective(name.slice(1));
      return { kind: 'slot', arg: slot.body, modifiers: slot.modifiers };
    }
    if (name.indexOf('v-') !== 0) return null;
    var body = name.slice(2);
    var dot = body.indexOf('.');
    var modifiers = [];
    if (dot !== -1) {
      modifiers = body.slice(dot + 1).split('.');
      body = body.slice(0, dot);
    }
    var colon = body.indexOf(':');
    if (colon !== -1) {
      return { kind: body.slice(0, colon), arg: body.slice(colon + 1), modifiers: modifiers };
    }
    return { kind: body, arg: null, modifiers: modifiers };
  }

  function compileNode(el, ctx) {
    var tag = String(el.tagName || '').toLowerCase();

    if (tag === 'slot') return compileSlot(el, ctx);

    if (tag === 'template') {
      var forExpression = el.getAttribute('v-for');
      var slotDirective = null;
      for (var i = 0; i < el.attributes.length; i += 1) {
        var d = directiveOf(el.attributes[i].name);
        if (d && d.kind === 'slot') slotDirective = d;
      }
      var children = compileChildren(el.content || el, ctx);
      if (forExpression) {
        var parsed = parseFor(forExpression);
        return {
          type: 'for',
          source: parseExpression(parsed.source),
          item: parsed.item,
          index: parsed.index,
          node: { type: 'fragment', children: children }
        };
      }
      // <template v-slot:x> 的内容由组件子节点收集器处理；走到这里时
      // 它只是一个透明的片段容器，不能生成真实的 <template> DOM 节点。
      if (slotDirective) return { type: 'fragment', children: children };
      return { type: 'fragment', children: children };
    }
    return compileElement(el, ctx, false);
  }

  function compileSlot(el, ctx) {
    var name = el.getAttribute('name') || 'default';
    var nameExpr = null;
    var props = [];
    for (var i = 0; i < el.attributes.length; i += 1) {
      var attr = el.attributes[i];
      var directive = directiveOf(attr.name);
      if (!directive) continue;
      if (directive.kind === 'bind' && directive.arg) {
        if (directive.arg === 'name') {
          nameExpr = parseExpression(attr.value);
        } else {
          props.push({ name: directive.arg, expr: parseExpression(attr.value) });
        }
      }
    }
    return {
      type: 'slot',
      name: name,
      nameExpr: nameExpr,
      props: props,
      children: compileChildren(el, ctx)
    };
  }

  function compileElement(el, ctx) {
    var tag = String(el.tagName || '').toLowerCase();
    var component = resolveComponent(ctx, tag);
    var dirs = {
      forExpr: null,
      show: null,
      html: null,
      text: null,
      model: null,
      pre: false,
      key: null,
      ref: null
    };
    var staticProps = {};
    var bindings = [];
    var events = [];
    var slotTemplates = [];

    for (var i = 0; i < el.attributes.length; i += 1) {
      var attr = el.attributes[i];
      var name = attr.name;
      var value = attr.value;
      if (name === 'key') {
        dirs.key = parseExpression(value);
        continue;
      }
      if (name === 'ref') {
        dirs.ref = value;
        continue;
      }
      var directive = directiveOf(name);
      if (!directive) {
        // HTML 布尔属性没有值：`<input disabled>` 必须当成 true，而不是空字符串。
        staticProps[name] = value === '' && BOOLEAN_ATTRS[name] ? true : value;
        continue;
      }
      switch (directive.kind) {
        case 'if':
          // 分支链由 compileIfGroup 统一消费；这里不重复解析。
          break;
        case 'else-if':
        case 'else':
          // 由 compileIfGroup 消费；这里保留节点本身，不重复解析。
          break;
        case 'for':
          dirs.forExpr = parseFor(value);
          break;
        case 'show':
          dirs.show = parseExpression(value);
          break;
        case 'html':
          if (!ctx.allowHTML) {
            throw new Error('[ada-render] v-html 默认关闭；仅允许服务端清洗过的 Markdown，并在 createApp({ allowHTML: true }) 中显式开启');
          }
          dirs.html = parseExpression(value);
          break;
        case 'text':
          dirs.text = parseExpression(value);
          break;
        case 'model':
          dirs.model = { expr: parseExpression(value), modifiers: directive.modifiers };
          break;
        case 'pre':
          dirs.pre = true;
          break;
        case 'bind':
          if (!directive.arg) throw new Error('[ada-render] v-bind 需要属性名');
          if (directive.arg === 'key') dirs.key = parseExpression(value);
          else if (directive.arg === 'ref') dirs.ref = value;
          else bindings.push({ name: directive.arg, expr: parseExpression(value) });
          break;
        case 'on':
          if (!directive.arg) throw new Error('[ada-render] v-on 需要事件名');
          events.push({
            name: directive.arg,
            expr: parseExpression(value),
            modifiers: directive.modifiers
          });
          break;
        case 'slot':
          // 组件子节点里的 <template v-slot> 由 collectComponentChildren 处理。
          break;
        case 'cloak':
          break;
        case 'key':
          dirs.key = parseExpression(value);
          break;
        case 'ref':
          dirs.ref = value;
          break;
        default:
          throw new Error('[ada-render] 不支持的指令 v-' + directive.kind);
      }
    }

    if (dirs.pre) {
      var rawProps = {};
      for (var rawIndex = 0; rawIndex < el.attributes.length; rawIndex += 1) {
        var rawAttr = el.attributes[rawIndex];
        if (rawAttr.name !== 'v-pre') rawProps[rawAttr.name] = rawAttr.value;
      }
      return {
        type: 'element',
        tag: tag,
        staticProps: rawProps,
        bindings: [],
        events: [],
        model: null,
        show: null,
        html: null,
        text: null,
        key: null,
        ref: null,
        raw: el.innerHTML || '',
        children: []
      };
    }

    if (component) {
      var componentChildren = collectComponentChildren(el, ctx, slotTemplates);
      var descriptor = {
        type: 'component',
        def: component,
        staticProps: staticProps,
        bindings: bindings,
        events: events,
        model: dirs.model,
        key: dirs.key,
        ref: dirs.ref,
        children: componentChildren.children,
        slots: componentChildren.slots
      };
      if (dirs.forExpr) return { type: 'for', source: parseExpression(dirs.forExpr.source), item: dirs.forExpr.item, index: dirs.forExpr.index, node: descriptor };
      return descriptor;
    }

    var children = [];
    if (dirs.html || dirs.text) {
      children = [];
    } else {
      children = compileChildren(el, ctx);
    }

    var descriptor = {
      type: 'element',
      tag: tag,
      staticProps: staticProps,
      bindings: bindings,
      events: events,
      model: dirs.model,
      show: dirs.show,
      html: dirs.html,
      text: dirs.text,
      key: dirs.key,
      ref: dirs.ref,
      raw: null,
      children: children
    };
    if (dirs.forExpr) {
      return { type: 'for', source: parseExpression(dirs.forExpr.source), item: dirs.forExpr.item, index: dirs.forExpr.index, node: descriptor };
    }
    return descriptor;
  }

  function collectComponentChildren(el, ctx, slotTemplates) {
    var children = [];
    var named = [];
    for (var i = 0; i < el.childNodes.length; i += 1) {
      var child = el.childNodes[i];
      if (child.nodeType === 3) {
        var compiled = compileText(child.nodeValue, ctx);
        if (compiled) children.push(compiled);
        continue;
      }
      if (child.nodeType === 8) continue;
      if (child.nodeType !== 1) continue;
      if (String(child.tagName).toLowerCase() === 'template') {
        var slotName = null;
        for (var a = 0; a < child.attributes.length; a += 1) {
          var d = directiveOf(child.attributes[a].name);
          if (d && d.kind === 'slot') {
            slotName = d.arg || 'default';
            break;
          }
        }
        if (slotName) {
          named.push({
            name: slotName,
            children: compileChildren(child.content || child, ctx)
          });
          continue;
        }
      }
      children.push(compileNode(child, ctx));
    }
    return { children: children, slots: named };
  }

  var NATIVE_TAGS = {
    a: true, abbr: true, address: true, area: true, article: true, aside: true,
    audio: true, b: true, base: true, bdi: true, bdo: true, blockquote: true,
    body: true, br: true, button: true, canvas: true, caption: true, cite: true,
    code: true, col: true, colgroup: true, data: true, datalist: true, dd: true,
    del: true, details: true, dfn: true, dialog: true, div: true, dl: true,
    dt: true, em: true, embed: true, fieldset: true, figcaption: true, figure: true,
    footer: true, form: true, h1: true, h2: true, h3: true, h4: true, h5: true,
    h6: true, head: true, header: true, hgroup: true, hr: true, html: true, i: true,
    iframe: true, img: true, input: true, ins: true, kbd: true, label: true,
    legend: true, li: true, link: true, main: true, map: true, mark: true, menu: true,
    meta: true, meter: true, nav: true, noscript: true, object: true, ol: true,
    optgroup: true, option: true, output: true, p: true, picture: true, pre: true,
    progress: true, q: true, rp: true, rt: true, ruby: true, s: true, samp: true,
    script: true, search: true, section: true, select: true, slot: true, small: true,
    source: true, span: true, strong: true, style: true, sub: true, summary: true,
    sup: true, table: true, tbody: true, td: true, template: true, textarea: true,
    tfoot: true, th: true, thead: true, time: true, title: true, tr: true, track: true,
    u: true, ul: true, var: true, video: true, wbr: true,
    svg: true, path: true, circle: true, rect: true, line: true, polyline: true,
    polygon: true, g: true, use: true, defs: true, text: true, tspan: true,
    symbol: true, foreignobject: true, lineargradient: true, radialgradient: true,
    stop: true, clippath: true, mask: true, pattern: true
  };

  function pascalCase(tag) {
    return String(tag).replace(/(^|-)([a-z0-9])/g, function (_, dash, ch) {
      return ch.toUpperCase();
    });
  }

  function resolveComponent(ctx, tag) {
    if (!ctx || !ctx.components) return null;
    var registry = ctx.components;
    // 浏览器解析模板时会把 <Button> 小写成 <button>；为了不和原生标签撞名，
    // UI 组件统一用 <ui-button> 这类带前缀的标签。
    if (tag.indexOf('ui-') === 0) {
      if (registry[tag]) return registry[tag];
      return registry[pascalCase(tag.slice(3))] || null;
    }
    if (NATIVE_TAGS[tag]) return null;
    if (registry[tag]) return registry[tag];
    var camel = tag.replace(/-([a-z0-9])/g, function (_, c) { return c.toUpperCase(); });
    if (registry[camel]) return registry[camel];
    var pascal = pascalCase(tag);
    if (registry[pascal]) return registry[pascal];
    return null;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 6. 描述树渲染：每次组件更新重新执行，依赖收集自然发生
   * ═══════════════════════════════════════════════════════════════════ */

  function renderChildren(descriptors, scope, instance) {
    var out = [];
    for (var i = 0; i < descriptors.length; i += 1) {
      var rendered = renderDescriptor(descriptors[i], scope, instance);
      if (rendered === null || rendered === undefined) continue;
      if (Array.isArray(rendered)) normalizeChildren(rendered, out);
      else out.push(rendered);
    }
    return out;
  }

  function renderDescriptor(descriptor, scope, instance) {
    if (!descriptor) return null;
    switch (descriptor.type) {
      case 'text':
        return { t: TEXT, v: descriptor.value };
      case 'interp': {
        var value = evalAst(descriptor.expr, scope);
        return value === null || value === undefined ? null : { t: TEXT, v: String(value) };
      }
      case 'interp-parts': {
        var textValue = '';
        for (var i = 0; i < descriptor.parts.length; i += 1) {
          var part = descriptor.parts[i];
          if (part.type === 'text') textValue += part.value;
          else {
            var v = evalAst(part.expr, scope);
            if (v !== null && v !== undefined) textValue += String(v);
          }
        }
        return textValue ? { t: TEXT, v: textValue } : null;
      }
      case 'fragment':
        return renderChildren(descriptor.children, scope, instance);
      case 'if':
        for (var b = 0; b < descriptor.branches.length; b += 1) {
          var branch = descriptor.branches[b];
          if (branch.expr === null || evalAst(branch.expr, scope)) {
            return renderDescriptor(branch.node, scope, instance);
          }
        }
        return null;
      case 'for':
        return renderFor(descriptor, scope, instance);
      case 'element':
        return renderElement(descriptor, scope, instance);
      case 'component':
        return renderComponentDescriptor(descriptor, scope, instance);
      case 'slot':
        return renderSlot(descriptor, scope, instance);
      default:
        throw new Error('[ada-render] 未知描述节点 ' + descriptor.type);
    }
  }

  function renderFor(descriptor, scope, instance) {
    var source = evalAst(descriptor.source, scope);
    var list;
    if (typeof source === 'number') {
      list = [];
      for (var n = 0; n < source; n += 1) list.push(n + 1);
    } else if (Array.isArray(source)) {
      list = source;
    } else if (source && typeof source === 'object') {
      list = Object.keys(source).map(function (key) { return source[key]; });
    } else {
      return null;
    }
    var out = [];
    for (var i = 0; i < list.length; i += 1) {
      var aliases = {};
      aliases[descriptor.item] = list[i];
      if (descriptor.index) aliases[descriptor.index] = i;
      var rendered = renderDescriptor(descriptor.node, childScope(scope, aliases), instance);
      if (rendered === null || rendered === undefined) continue;
      if (Array.isArray(rendered)) normalizeChildren(rendered, out);
      else out.push(rendered);
    }
    return out;
  }

  function normalizeClass(value) {
    if (!value) return '';
    if (typeof value === 'string') return value;
    if (Array.isArray(value)) {
      return value.map(normalizeClass).filter(Boolean).join(' ');
    }
    if (typeof value === 'object') {
      var out = [];
      for (var key in value) {
        if (value[key]) out.push(key);
      }
      return out.join(' ');
    }
    return String(value);
  }

  function mergeClass(a, b) {
    var left = normalizeClass(a);
    var right = normalizeClass(b);
    return [left, right].filter(Boolean).join(' ');
  }

  function normalizeStyle(value) {
    if (!value) return '';
    if (typeof value === 'string') return value;
    if (Array.isArray(value)) return value.map(normalizeStyle).filter(Boolean).join(';');
    var out = [];
    for (var key in value) {
      if (value[key] !== null && value[key] !== undefined && value[key] !== false) {
        var name = key.charAt(0) === '-' ? key : key.replace(/[A-Z]/g, function (c) {
          return '-' + c.toLowerCase();
        });
        out.push(name + ':' + value[key]);
      }
    }
    return out.join(';');
  }

  function mergeStyle(a, b) {
    var left = normalizeStyle(a);
    var right = normalizeStyle(b);
    return [left, right].filter(Boolean).join(';');
  }

  function applyModelToElement(descriptor, props, events, scope) {
    var model = descriptor.model;
    if (!model) return;
    var tag = descriptor.tag;
    var type = String(props.type || (tag === 'input' ? 'text' : '')).toLowerCase();
    var current = evalAst(model.expr, scope);
    var isCheckbox = tag === 'input' && type === 'checkbox';
    var isRadio = tag === 'input' && type === 'radio';
    var isSelect = tag === 'select';
    var eventName = model.modifiers.indexOf('lazy') !== -1 ? 'change' : 'input';
    if (isCheckbox || isRadio || isSelect) eventName = 'change';

    if (isCheckbox) {
      if (Array.isArray(current)) props.checked = current.indexOf(props.value) !== -1;
      else props.checked = !!current;
    } else if (isRadio) {
      props.checked = current === props.value;
    } else {
      props.value = current === null || current === undefined ? '' : current;
    }

    events.push({
      name: eventName,
      model: model,
      modifiers: [],
      handler: function (event) {
        var target = event.target;
        var next;
        if (isCheckbox) {
          if (Array.isArray(evalAst(model.expr, scope))) {
            var list = evalAst(model.expr, scope).slice();
            var at = list.indexOf(props.value);
            if (target.checked && at === -1) list.push(props.value);
            if (!target.checked && at !== -1) list.splice(at, 1);
            next = list;
          } else {
            next = target.checked;
          }
        } else if (isRadio) {
          next = props.value;
        } else if (isSelect && props.multiple) {
          next = Array.prototype.slice.call(target.selectedOptions).map(function (option) { return option.value; });
        } else {
          next = target.value;
        }
        if (model.modifiers.indexOf('trim') !== -1 && typeof next === 'string') next = next.trim();
        if (model.modifiers.indexOf('number') !== -1 && typeof next === 'string' && next !== '') next = Number(next);
        assignExpression(model.expr, scope, next);
      }
    });
  }

  function buildElementProps(descriptor, scope) {
    var props = {};
    var key;
    for (key in descriptor.staticProps) {
      if (Object.prototype.hasOwnProperty.call(descriptor.staticProps, key)) {
        props[key] = descriptor.staticProps[key];
      }
    }
    for (var i = 0; i < descriptor.bindings.length; i += 1) {
      var binding = descriptor.bindings[i];
      var value = evalAst(binding.expr, scope);
      if (binding.name === 'class') props.class = mergeClass(props.class, value);
      else if (binding.name === 'style') props.style = mergeStyle(props.style, value);
      else props[binding.name] = value;
    }
    return props;
  }

  function renderElement(descriptor, scope, instance) {
    var props = buildElementProps(descriptor, scope);
    var events = [];
    for (var i = 0; i < descriptor.events.length; i += 1) {
      events.push(makeEventBinding(descriptor.events[i], scope, instance));
    }
    applyModelToElement(descriptor, props, events, scope);
    if (descriptor.show) props.hidden = !evalAst(descriptor.show, scope);

    var vnode = {
      t: descriptor.tag,
      p: props,
      c: [],
      k: descriptor.key ? evalAst(descriptor.key, scope) : undefined,
      _e: events,
      _ref: descriptor.ref || null
    };

    if (descriptor.html) {
      vnode._html = String(evalAst(descriptor.html, scope) || '');
    } else if (descriptor.text) {
      var textValue = evalAst(descriptor.text, scope);
      vnode.c = textValue === null || textValue === undefined ? [] : [{ t: TEXT, v: String(textValue) }];
    } else if (descriptor.raw !== null && descriptor.raw !== undefined) {
      vnode._html = descriptor.raw;
    } else {
      vnode.c = renderChildren(descriptor.children, scope, instance);
    }
    return vnode;
  }

  function renderComponentDescriptor(descriptor, scope, instance) {
    var props = buildElementProps(descriptor, scope);
    var events = [];
    for (var i = 0; i < descriptor.events.length; i += 1) {
      events.push(makeEventBinding(descriptor.events[i], scope, instance, true));
    }
    if (descriptor.model) {
      props.modelValue = evalAst(descriptor.model.expr, scope);
      events.push({
        name: 'update:modelValue',
        component: true,
        handler: (function (expr) {
          return function (value) { assignExpression(expr, scope, value); };
        })(descriptor.model.expr),
        modifiers: descriptor.model.modifiers
      });
    }
    if (events.length) {
      props.$on = {};
      for (var e = 0; e < events.length; e += 1) {
        var ev = events[e];
        if (!props.$on[ev.name]) props.$on[ev.name] = [];
        props.$on[ev.name].push(ev.handler);
      }
    }

    var children = renderChildren(descriptor.children, scope, instance);
    var slots = {};
    for (var s = 0; s < descriptor.slots.length; s += 1) {
      slots[descriptor.slots[s].name] = (function (slot) {
        return function (slotProps) {
          var slotScope = slotProps ? childScope(scope, slotProps) : scope;
          return renderChildren(slot.children, slotScope, instance);
        };
      })(descriptor.slots[s]);
    }

    return {
      t: COMPONENT,
      n: descriptor.def,
      p: props,
      c: children,
      s: slots,
      k: descriptor.key ? evalAst(descriptor.key, scope) : undefined,
      _ref: descriptor.ref || null
    };
  }

  function renderSlot(descriptor, scope, instance) {
    var name = descriptor.nameExpr
      ? evalAst(descriptor.nameExpr, scope)
      : (descriptor.name || 'default');
    var slotProps = {};
    for (var i = 0; i < descriptor.props.length; i += 1) {
      slotProps[descriptor.props[i].name] = evalAst(descriptor.props[i].expr, scope);
    }
    var slot = instance && instance.slots && instance.slots[name];
    if (slot) {
      var rendered = slot(slotProps);
      // 显式写了 <ui-badge ...></ui-badge> 时 default slot 存在但为空；
      // 这种情况必须回退到组件模板里的 fallback（例如 {{ label }}）。
      if (rendered && rendered.length) return rendered;
    }
    return renderChildren(descriptor.children, scope, instance);
  }

  function makeEventBinding(binding, scope, instance, component) {
    return {
      name: binding.name,
      component: !!component,
      modifiers: binding.modifiers || [],
      handler: function (event) {
        var mods = binding.modifiers || [];
        if (!component && !eventAllowed(mods, event)) return undefined;
        if (!component) {
          if (mods.indexOf('prevent') !== -1) event.preventDefault();
          if (mods.indexOf('stop') !== -1) event.stopPropagation();
          if (mods.indexOf('self') !== -1 && event.target !== event.currentTarget) return undefined;
        }
        var eventScope = childScope(scope, { $event: event });
        var value = evalAst(binding.expr, eventScope);
        if (typeof value === 'function') {
          return value.call(instance ? instance.proxy : null, event);
        }
        return value;
      }
    };
  }

  var KEY_ALIASES = {
    enter: 'Enter',
    esc: 'Escape',
    space: ' ',
    tab: 'Tab',
    delete: 'Delete',
    backspace: 'Backspace',
    up: 'ArrowUp',
    down: 'ArrowDown',
    left: 'ArrowLeft',
    right: 'ArrowRight'
  };

  var SYSTEM_MODIFIERS = {
    ctrl: 'ctrlKey',
    shift: 'shiftKey',
    alt: 'altKey',
    meta: 'metaKey'
  };

  function eventAllowed(modifiers, event) {
    for (var i = 0; i < modifiers.length; i += 1) {
      var modifier = modifiers[i];
      if (KEY_ALIASES[modifier]) {
        if (event.key !== KEY_ALIASES[modifier]) return false;
        continue;
      }
      if (SYSTEM_MODIFIERS[modifier]) {
        if (!event[SYSTEM_MODIFIERS[modifier]]) return false;
        continue;
      }
      if (modifier === 'exact') {
        var used = false;
        for (var key in SYSTEM_MODIFIERS) {
          if (event[SYSTEM_MODIFIERS[key]]) used = true;
        }
        if (used) return false;
      }
    }
    return true;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 7. 组件实例：选项、props、slots、computed、生命周期
   * ═══════════════════════════════════════════════════════════════════ */

  function normalizeComponent(def) {
    if (typeof def === 'function') return { render: def, __ada_defined: true };
    if (!isObject(def)) throw new TypeError('[ada-render] 组件必须是对象或 render 函数');
    if (def.__ada_defined) return def;
    var normalized = shallowCopy(def);
    normalized.__ada_defined = true;
    if (!normalized.name) normalized.name = def.name || 'Anonymous';
    return normalized;
  }

  function defineComponent(def) {
    return normalizeComponent(def);
  }

  function normalizePropDefs(props) {
    if (!props) return {};
    if (Array.isArray(props)) {
      var map = {};
      for (var i = 0; i < props.length; i += 1) map[props[i]] = {};
      return map;
    }
    return props;
  }

  function defaultPropValue(definition) {
    if (!definition || !Object.prototype.hasOwnProperty.call(definition, 'default')) return undefined;
    return typeof definition.default === 'function' ? definition.default() : definition.default;
  }

  function kebabCase(name) {
    return String(name).replace(/[A-Z]/g, function (ch) {
      return '-' + ch.toLowerCase();
    });
  }

  function resolveProps(def, raw) {
    var defs = normalizePropDefs(def.props);
    var out = {};
    for (var key in defs) {
      if (Object.prototype.hasOwnProperty.call(defs, key)) {
        var definition = defs[key];
        var value = raw ? raw[key] : undefined;
        if (value === undefined && raw) value = raw[kebabCase(key)];
        if (value === undefined) {
          value = defaultPropValue(definition);
        } else if (definition && definition.type === Boolean) {
          value = value === '' || value === kebabCase(key) || value === key
            ? true
            : value === 'false' || value === 0
              ? false
              : !!value;
        }
        out[key] = value;
      }
    }
    return out;
  }

  function makeSlots(vnode) {
    var slots = {};
    slots.default = function () {
      return vnode.c ? vnode.c.slice() : [];
    };
    if (vnode.s) {
      for (var name in vnode.s) {
        if (Object.prototype.hasOwnProperty.call(vnode.s, name)) slots[name] = vnode.s[name];
      }
    }
    return slots;
  }

  function callHook(instance, name) {
    var hook = instance.def && instance.def[name];
    if (typeof hook === 'function') hook.call(instance.proxy);
  }

  function createInstance(vnode, parent, app) {
    var def = vnode.n;
    if (typeof def === 'string') {
      var owner = parent || null;
      var registry = (owner && owner.components) ||
        (app && app._components) ||
        (owner && owner.app && owner.app._components) ||
        EMPTY_COMPONENTS;
      def = registry[def];
      if (!def) throw new Error('[ada-render] 未注册组件 "' + vnode.n + '"（浏览器端需要 app.component 同名注册）');
    }
    def = normalizeComponent(def);
    var ownerApp = app || (parent && parent.app) || null;
    var instance = {
      uid: ++effectUid,
      def: def,
      vnode: vnode,
      parent: parent || null,
      app: ownerApp,
      props: null,
      slots: null,
      refs: {},
      data: null,
      setupState: null,
      methods: {},
      computed: {},
      proxy: null,
      subTree: null,
      effect: null,
      isMounted: false,
      isUnmounted: false
    };

    instance.proxy = createPublicProxy(instance);
    instance.props = reactive(resolveProps(def, vnode.p));
    instance.slots = makeSlots(vnode);
    if (def.components) {
      instance.components = Object.create(null);
      if (ownerApp && ownerApp._components) {
        for (var appComponent in ownerApp._components) {
          if (Object.prototype.hasOwnProperty.call(ownerApp._components, appComponent)) {
            instance.components[appComponent] = ownerApp._components[appComponent];
          }
        }
      }
      for (var localComponent in def.components) {
        if (Object.prototype.hasOwnProperty.call(def.components, localComponent)) {
          instance.components[localComponent] = normalizeComponent(def.components[localComponent]);
        }
      }
    } else {
      instance.components = (ownerApp && ownerApp._components) || EMPTY_COMPONENTS;
    }
    instance.emit = function (event) {
      emit(instance, event, Array.prototype.slice.call(arguments, 1));
    };

    if (def.methods) {
      for (var methodName in def.methods) {
        if (Object.prototype.hasOwnProperty.call(def.methods, methodName)) {
          instance.methods[methodName] = def.methods[methodName].bind(instance.proxy);
        }
      }
    }

    var setupRender = null;
    if (typeof def.setup === 'function') {
      var setupContext = {
        emit: function (event) {
          var args = Array.prototype.slice.call(arguments, 1);
          emit(instance, event, args);
        },
        slots: instance.slots,
        attrs: vnode.p
      };
      var setupResult = def.setup(instance.props, setupContext);
      if (typeof setupResult === 'function') {
        setupRender = setupResult;
      } else if (isObject(setupResult)) {
        instance.setupState = reactive(setupResult);
      }
    }

    var initialData = typeof def.data === 'function' ? def.data.call(instance.proxy) : (def.data || {});
    instance.data = reactive(initialData);

    if (def.computed) {
      for (var computedName in def.computed) {
        if (Object.prototype.hasOwnProperty.call(def.computed, computedName)) {
          var definition = def.computed[computedName];
          instance.computed[computedName] = computed(
            typeof definition === 'function' ? definition.bind(instance.proxy) : definition.get.bind(instance.proxy)
          );
        }
      }
    }

    instance.render = function () {};
    if (setupRender) {
      instance.render = function () { return setupRender.call(instance.proxy, h); };
    } else if (typeof def.render === 'function') {
      instance.render = function () { return def.render.call(instance.proxy, h); };
    } else if (def.template) {
      instance.render = makeTemplateRender(def, instance);
    } else {
      throw new Error('[ada-render] 组件 "' + (def.name || 'Anonymous') + '" 缺少 template 或 render');
    }

    return instance;
  }

  function createPublicProxy(instance) {
    var target = Object.create(null);
    return new Proxy(target, {
      get: function (_, key) {
        if (key === RAW) return instance;
        if (instance.data && key in instance.data) return instance.data[key];
        if (instance.setupState && key in instance.setupState) {
          return unref(instance.setupState[key]);
        }
        if (instance.computed && Object.prototype.hasOwnProperty.call(instance.computed, key)) {
          return instance.computed[key].value;
        }
        if (instance.methods && Object.prototype.hasOwnProperty.call(instance.methods, key)) {
          return instance.methods[key];
        }
        if (instance.props && key in instance.props) return instance.props[key];
        if (key === '$props') return instance.props;
        if (key === '$slots') return instance.slots;
        if (key === '$refs') return instance.refs;
        if (key === '$attrs') return instance.vnode.p;
        if (key === '$emit') return instance.emit;
        if (key === '$app') return instance.app;
        return undefined;
      },
      set: function (_, key, value) {
        setInInstance(instance, key, value);
        return true;
      },
      has: function (_, key) {
        if (instance.data && key in instance.data) return true;
        if (instance.setupState && key in instance.setupState) return true;
        if (instance.computed && Object.prototype.hasOwnProperty.call(instance.computed, key)) return true;
        if (instance.methods && Object.prototype.hasOwnProperty.call(instance.methods, key)) return true;
        if (instance.props && key in instance.props) return true;
        return key.charAt(0) === '$';
      }
    });
  }

  function emit(instance, event, args) {
    var rawProps = instance.vnode && instance.vnode.p;
    var handlers = rawProps && rawProps.$on && rawProps.$on[event];
    if (!handlers) return;
    if (!Array.isArray(handlers)) handlers = [handlers];
    for (var i = 0; i < handlers.length; i += 1) handlers[i].apply(null, args);
  }

  var templateCache = new WeakMap();

  function makeTemplateRender(def, instance) {
    var doc = (instance.app && instance.app._doc) || DOC;
    var cached = templateCache.get(def);
    if (!cached || cached.doc !== doc || cached.components !== instance.components) {
      cached = {
        doc: doc,
        components: instance.components,
        root: compileTemplateRoot(def.template, doc, instance.app, instance.components)
      };
      templateCache.set(def, cached);
    }
    return function () {
      var scope = createScope(instance);
      var rendered = renderDescriptor(cached.root, scope, instance);
      if (Array.isArray(rendered)) {
        if (rendered.length !== 1) {
          throw new Error('[ada-render] 组件 "' + (def.name || 'Anonymous') + '" 的模板必须只有一个根节点');
        }
        return rendered[0];
      }
      return rendered;
    };
  }

  function compileTemplateRoot(template, doc, app, components) {
    var ctx = {
      doc: doc,
      allowHTML: !!(app && app._options && app._options.allowHTML),
      components: components || (app && app._components) || {}
    };
    var fragment = resolveTemplate(template, doc);
    var children = compileChildren(fragment, ctx);
    if (children.length !== 1) {
      throw new Error('[ada-render] 模板必须只有一个根节点（当前 ' + children.length + ' 个）');
    }
    return children[0];
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 8. DOM 运行时：mount / patch / unmount / hydrate
   * ═══════════════════════════════════════════════════════════════════ */

  function docFor(instance) {
    return (instance && instance.app && instance.app._doc) || DOC;
  }

  function insert(el, container, anchor) {
    if (!container) throw new Error('[ada-render] 缺少挂载容器');
    container.insertBefore(el, anchor || null);
  }

  function createElement(tag, isSVG, doc) {
    if (isSVG || tag === 'svg') return doc.createElementNS(SVG_NS, tag);
    return doc.createElement(tag);
  }

  var BOOLEAN_ATTRS = {
    required: true, disabled: true, readonly: true, checked: true, open: true,
    hidden: true, selected: true, multiple: true, autofocus: true, novalidate: true,
    formnovalidate: true, inert: true
  };

  var DOM_PROPS = {
    value: true, checked: true, selected: true, disabled: true, hidden: true, open: true,
    multiple: true, readonly: true, required: true
  };

  var DOM_PROP_NAMES = {
    readonly: 'readOnly',
    novalidate: 'noValidate',
    formnovalidate: 'formNoValidate',
    autofocus: 'autofocus'
  };

  function patchProp(el, key, previous, next) {
    if (key === 'key' || key === '__proto__' || key === 'constructor') return;
    if (key === 'class') {
      var className = normalizeClass(next);
      if (className) el.setAttribute('class', className);
      else el.removeAttribute('class');
      return;
    }
    if (key === 'style') {
      var style = normalizeStyle(next);
      if (style) el.setAttribute('style', style);
      else el.removeAttribute('style');
      return;
    }
    if (key.charAt(0) === 'o' && key.charAt(1) === 'n' && typeof next === 'function') return;
    if (DOM_PROPS[key]) {
      var propName = DOM_PROP_NAMES[key] || key;
      if (key === 'value' && el.tagName === 'TEXTAREA' && next !== undefined) {
        el.value = next === null ? '' : String(next);
        return;
      }
      if (key === 'value' && el.value === (next === null || next === undefined ? '' : String(next))) return;
      if (key === 'checked' && el.checked === !!next) return;
      el[propName] = next === undefined || next === null ? (typeof next === 'boolean' ? false : '') : next;
      if (!BOOLEAN_ATTRS[key] && next !== undefined && next !== null) el.setAttribute(key, String(next));
      return;
    }
    if (BOOLEAN_ATTRS[key]) {
      if (next) el.setAttribute(key, '');
      else el.removeAttribute(key);
      return;
    }
    if (next === null || next === undefined || next === false) {
      el.removeAttribute(key);
      return;
    }
    if (next === true) {
      el.setAttribute(key, '');
      return;
    }
    el.setAttribute(key, String(next));
  }

  function mountProps(vnode, el) {
    var props = vnode.p || {};
    for (var key in props) {
      if (Object.prototype.hasOwnProperty.call(props, key)) {
        if (key === 'key' || key.charAt(0) === '$') continue;
        patchProp(el, key, null, props[key]);
      }
    }
    if (vnode._html !== undefined) {
      el.innerHTML = vnode._html;
      return;
    }
    mountEvents(vnode, el);
  }

  function patchProps(oldVNode, newVNode, el) {
    var oldProps = (oldVNode && oldVNode.p) || {};
    var newProps = newVNode.p || {};
    for (var key in oldProps) {
      if (Object.prototype.hasOwnProperty.call(oldProps, key) && !(key in newProps)) {
        if (key === 'key' || key.charAt(0) === '$') continue;
        patchProp(el, key, oldProps[key], null);
      }
    }
    for (var nextKey in newProps) {
      if (Object.prototype.hasOwnProperty.call(newProps, nextKey)) {
        if (nextKey === 'key' || nextKey.charAt(0) === '$') continue;
        if (oldProps[nextKey] !== newProps[nextKey]) {
          patchProp(el, nextKey, oldProps[nextKey], newProps[nextKey]);
        }
      }
    }
    if (oldVNode && oldVNode._html !== undefined && newVNode._html === undefined) {
      el.innerHTML = '';
    }
    if (newVNode._html !== undefined) {
      if (!oldVNode || oldVNode._html !== newVNode._html) el.innerHTML = newVNode._html;
      detachEvents(oldVNode, el);
      return;
    }
    detachEvents(oldVNode, el);
    mountEvents(newVNode, el);
  }

  function detachEvents(vnode, el) {
    if (!vnode || !vnode._e || !el) return;
    for (var i = 0; i < vnode._e.length; i += 1) {
      var binding = vnode._e[i];
      if (binding._fn) el.removeEventListener(binding.name, binding._fn, binding._options || false);
    }
  }

  function mountEvents(vnode, el) {
    if (!vnode._e) return;
    for (var i = 0; i < vnode._e.length; i += 1) {
      var binding = vnode._e[i];
      if (binding.component) continue;
      var modifiers = binding.modifiers || [];
      var options = {
        once: modifiers.indexOf('once') !== -1,
        capture: modifiers.indexOf('capture') !== -1,
        passive: modifiers.indexOf('passive') !== -1
      };
      binding._fn = binding.handler;
      binding._options = options;
      el.addEventListener(binding.name, binding._fn, options);
    }
  }

  function mount(vnode, container, anchor, parentInstance, isSVG) {
    if (vnode === null || vnode === undefined) return null;
    if (Array.isArray(vnode)) {
      var last = null;
      for (var i = 0; i < vnode.length; i += 1) last = mount(vnode[i], container, anchor, parentInstance, isSVG);
      return last;
    }
    var doc = docFor(parentInstance);

    if (vnode.t === TEXT) {
      var textNode = doc.createTextNode(vnode.v);
      vnode.el = textNode;
      insert(textNode, container, anchor);
      return textNode;
    }

    if (vnode.t === COMPONENT) {
      return mountComponent(vnode, container, anchor, parentInstance);
    }

    if (vnode.t === '#fragment') {
      throw new Error('[ada-render] 根层片段需要单个容器；请给模板/组件加一个根元素');
    }

    var svg = isSVG || vnode.t === 'svg';
    var el = createElement(vnode.t, svg, doc);
    vnode.el = el;
    mountProps(vnode, el);
    insert(el, container, anchor);

    if (vnode._html === undefined) {
      var children = vnode.c || [];
      for (var c = 0; c < children.length; c += 1) {
        mount(children[c], el, null, parentInstance, svg);
      }
      syncSelectValue(vnode, el);
    }
    applyRef(vnode, parentInstance);
    return el;
  }

  function patch(oldVNode, newVNode, container, anchor, parentInstance, isSVG) {
    if (oldVNode === newVNode) return;
    if (!oldVNode) {
      mount(newVNode, container, anchor, parentInstance, isSVG);
      return;
    }
    if (!newVNode) {
      unmount(oldVNode);
      return;
    }
    if (!sameVNode(oldVNode, newVNode)) {
      var nextAnchor = oldVNode.el ? oldVNode.el.nextSibling : anchor;
      unmount(oldVNode);
      mount(newVNode, container, nextAnchor, parentInstance, isSVG);
      return;
    }

    if (newVNode.t === TEXT) {
      newVNode.el = oldVNode.el;
      if (newVNode.v !== oldVNode.v) oldVNode.el.nodeValue = newVNode.v;
      return;
    }

    if (newVNode.t === COMPONENT) {
      patchComponent(oldVNode, newVNode, container, anchor, parentInstance);
      return;
    }

    var el = oldVNode.el;
    newVNode.el = el;
    patchProps(oldVNode, newVNode, el);
    if (newVNode._html === undefined) {
      patchChildren(oldVNode, newVNode, el, parentInstance, isSVG || newVNode.t === 'svg');
      syncSelectValue(newVNode, el);
    }
    clearRef(oldVNode);
    applyRef(newVNode, parentInstance);
  }

  function sameVNode(a, b) {
    if (!a || !b) return false;
    if (a.t !== b.t) return false;
    if (a.k !== b.k) return false;
    if (a.t === COMPONENT) return a.n === b.n;
    return true;
  }

  function patchChildren(oldVNode, newVNode, el, parentInstance, isSVG) {
    var oldChildren = (oldVNode && oldVNode.c) || [];
    var newChildren = (newVNode && newVNode.c) || [];
    var oldByKey = new Map();
    var matched = new Set();

    for (var i = 0; i < oldChildren.length; i += 1) {
      var child = oldChildren[i];
      if (child && child.k !== undefined && child.k !== null) oldByKey.set(child.k, child);
    }

    var usedOld = new Set();
    for (var n = 0; n < newChildren.length; n += 1) {
      var next = newChildren[n];
      var previous = null;
      if (next && next.k !== undefined && next.k !== null) {
        previous = oldByKey.get(next.k) || null;
      } else if (oldChildren[n] && (oldChildren[n].k === undefined || oldChildren[n].k === null)) {
        previous = oldChildren[n];
      }
      if (previous) {
        usedOld.add(previous);
        patch(previous, next, el, null, parentInstance, isSVG);
      } else {
        mount(next, el, null, parentInstance, isSVG);
      }
    }

    for (var o = 0; o < oldChildren.length; o += 1) {
      var oldChild = oldChildren[o];
      if (oldChild && !usedOld.has(oldChild)) unmount(oldChild);
    }

    var anchor = null;
    for (var r = newChildren.length - 1; r >= 0; r -= 1) {
      var node = newChildren[r] && newChildren[r].el;
      if (!node) continue;
      if (node.parentNode !== el || node.nextSibling !== anchor) {
        el.insertBefore(node, anchor);
      }
      anchor = node;
    }
  }

  function singleRoot(vnode, name) {
    if (vnode === null || vnode === undefined) {
      throw new Error('[ada-render] 组件 "' + (name || 'Anonymous') + '" 渲染为 null');
    }
    if (Array.isArray(vnode)) {
      throw new Error('[ada-render] 组件 "' + (name || 'Anonymous') + '" 必须只有一个根节点');
    }
    return vnode;
  }

  function mountComponent(vnode, container, anchor, parentInstance) {
    var instance = createInstance(vnode, parentInstance);
    vnode._component = instance;
    applyRef(vnode, parentInstance);

    var firstRun = true;
    var runner = effect(function () {
      var next = singleRoot(instance.render.call(instance.proxy, h), instance.def.name);
      if (firstRun) {
        firstRun = false;
        instance.subTree = next;
        mount(next, container, anchor, instance);
        vnode.el = next.el;
        instance.isMounted = true;
        callHook(instance, 'mounted');
        return;
      }
      patch(instance.subTree, next, container, anchor, instance);
      instance.subTree = next;
      vnode.el = next.el;
      callHook(instance, 'updated');
    }, {
      lazy: true,
      scheduler: function () { queueJob(runner); }
    });
    instance.effect = runner;
    runner();
    return vnode.el;
  }

  function patchComponent(oldVNode, newVNode, container, anchor, parentInstance) {
    var instance = oldVNode._component;
    newVNode._component = instance;
    instance.vnode = newVNode;

    var nextProps = resolveProps(instance.def, newVNode.p);
    for (var key in nextProps) {
      if (Object.prototype.hasOwnProperty.call(nextProps, key)) instance.props[key] = nextProps[key];
    }
    for (var oldKey in instance.props) {
      if (Object.prototype.hasOwnProperty.call(instance.props, oldKey) && !(oldKey in nextProps)) {
        delete instance.props[oldKey];
      }
    }
    instance.slots = makeSlots(newVNode);
    clearRef(oldVNode);
    applyRef(newVNode, parentInstance);
    instance.effect();
    newVNode.el = instance.subTree ? instance.subTree.el : oldVNode.el;
  }

  function unmount(vnode) {
    if (!vnode) return;
    clearRef(vnode);
    if (vnode.t === COMPONENT) {
      unmountComponent(vnode._component);
      return;
    }
    if (vnode.t === TEXT) {
      if (vnode.el && vnode.el.parentNode) vnode.el.parentNode.removeChild(vnode.el);
      return;
    }
    if (vnode.c) {
      for (var i = 0; i < vnode.c.length; i += 1) unmount(vnode.c[i]);
    }
    if (vnode.el && vnode.el.parentNode) vnode.el.parentNode.removeChild(vnode.el);
  }

  function unmountComponent(instance) {
    if (!instance || instance.isUnmounted) return;
    instance.isUnmounted = true;
    stop(instance.effect);
    unmount(instance.subTree);
  }

  function syncSelectValue(vnode, el) {
    if (!vnode || vnode.t !== 'select' || !vnode.p) return;
    var value = vnode.p.value;
    if (value === undefined) return;
    if (vnode.p.multiple) {
      var values = Array.isArray(value) ? value.map(String) : [String(value)];
      var options = el.options || [];
      for (var i = 0; i < options.length; i += 1) {
        options[i].selected = values.indexOf(String(options[i].value)) !== -1;
      }
      return;
    }
    el.value = value === null ? '' : String(value);
  }

  function applyRef(vnode, owner) {    if (!vnode || !vnode._ref || !owner) return;
    vnode._refOwner = owner;
    owner.refs[vnode._ref] = vnode.t === COMPONENT && vnode._component ? vnode._component.proxy : vnode.el;
  }

  function clearRef(vnode) {
    if (!vnode || !vnode._ref) return;
    if (vnode._refOwner) delete vnode._refOwner.refs[vnode._ref];
  }

  /* ---- hydrate：服务端 SSR 出来的同一棵树，只接管不重建 ---- */

  function hydrateNode(vnode, dom, container, parentInstance) {
    if (!vnode) return;
    var doc = docFor(parentInstance);

    if (vnode.t === TEXT) {
      if (dom && dom.nodeType === 3) {
        if (dom.nodeValue !== vnode.v) dom.nodeValue = vnode.v;
        vnode.el = dom;
        return;
      }
      var textNode = doc.createTextNode(vnode.v);
      replaceNode(dom, textNode, container);
      vnode.el = textNode;
      return;
    }

    if (vnode.t === COMPONENT) {
      var instance = createInstance(vnode, parentInstance);
      vnode._component = instance;
      var subTree = instance.render.call(instance.proxy, h);
      subTree = singleRoot(subTree, instance.def.name);
      instance.subTree = subTree;
      hydrateNode(subTree, dom, container, instance);
      vnode.el = subTree.el;
      applyRef(vnode, parentInstance);
      instance.isMounted = true;

      var firstRun = true;
      var runner = effect(function () {
        var next = singleRoot(instance.render.call(instance.proxy, h), instance.def.name);
        if (firstRun) {
          firstRun = false;
          instance.subTree = next;
          return;
        }
        patch(instance.subTree, next, container, null, instance);
        instance.subTree = next;
        callHook(instance, 'updated');
      }, {
        lazy: true,
        scheduler: function () { queueJob(runner); }
      });
      instance.effect = runner;
      runner();
      callHook(instance, 'mounted');
      return;
    }

    var el = dom;
    if (!el || el.nodeType !== 1 || String(el.tagName).toLowerCase() !== vnode.t.toLowerCase()) {
      el = createElement(vnode.t, vnode.t === 'svg', doc);
      replaceNode(dom, el, container);
    }
    vnode.el = el;
    mountProps(vnode, el);
    applyRef(vnode, parentInstance);
    if (vnode._html !== undefined) {
      el.innerHTML = vnode._html;
      return;
    }

    var children = vnode.c || [];
    var cursor = el.firstChild;
    for (var i = 0; i < children.length; i += 1) {
      cursor = skipHydrationNoise(cursor);
      var nextCursor = cursor ? cursor.nextSibling : null;
      hydrateNode(children[i], cursor, el, parentInstance);
      cursor = nextCursor || (children[i].el ? children[i].el.nextSibling : null);
    }
    cursor = skipHydrationNoise(cursor);
    while (cursor) {
      var remove = cursor;
      cursor = cursor.nextSibling;
      el.removeChild(remove);
    }
    syncSelectValue(vnode, el);
  }

  function skipHydrationNoise(node) {
    while (node && (node.nodeType === 8 || (node.nodeType === 3 && /^\s*$/.test(node.nodeValue)))) {
      node = node.nextSibling;
    }
    return node;
  }

  function replaceNode(oldNode, newNode, container) {
    if (oldNode && oldNode.parentNode === container) container.replaceChild(newNode, oldNode);
    else container.appendChild(newNode);
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 9. SSR 线格式：完全展开组件，只留下 Go 能渲染的 VNode JSON
   * ═══════════════════════════════════════════════════════════════════ */

  function cleanProps(props) {
    var out = {};
    if (!props) return out;
    for (var key in props) {
      if (!Object.prototype.hasOwnProperty.call(props, key)) continue;
      if (key === 'key' || key.charAt(0) === '$' || key.charAt(0) === '_') continue;
      if (key === 'on' || (key.charAt(0) === 'o' && key.charAt(1) === 'n' && typeof props[key] === 'function')) continue;
      var value = props[key];
      if (typeof value === 'function' || value === undefined) continue;
      if (key === 'class') {
        var cls = normalizeClass(value);
        if (cls) out[key] = cls;
        continue;
      }
      if (key === 'style') {
        var style = normalizeStyle(value);
        if (style) out[key] = style;
        continue;
      }
      if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
        out[key] = value;
      }
    }
    return out;
  }

  function serializeNode(node, ctx, depth) {
    if (node === null || node === undefined || typeof node === 'boolean') return null;
    if (typeof node === 'string' || typeof node === 'number') return { t: TEXT, v: String(node) };
    if (Array.isArray(node)) {
      var list = [];
      for (var i = 0; i < node.length; i += 1) {
        var item = serializeNode(node[i], ctx, depth + 1);
        if (item) list.push(item);
      }
      return list;
    }
    if (!isVNode(node)) return null;
    if (depth > 64) throw new Error('[ada-render] VNode 深度超过 64');

    if (node.t === TEXT) return { t: TEXT, v: String(node.v) };

    if (node.t === COMPONENT) {
      var def = node.n;
      var resolvedNode = node;
      if (typeof def === 'string') {
        var registered = ctx && ctx.components ? ctx.components[def] : null;
        if (!registered) {
          return {
            t: COMPONENT,
            n: def,
            p: cleanProps(node.p),
            c: serializeChildren(node.c, ctx, depth)
          };
        }
        def = registered;
        resolvedNode = { t: COMPONENT, n: def, p: node.p, c: node.c, s: node.s, k: node.k };
      }
      var instance = createInstance(resolvedNode, null, {
        _doc: ctx && ctx.app && ctx.app._doc,
        _options: ctx && ctx.app && ctx.app._options,
        _components: (ctx && ctx.components) || EMPTY_COMPONENTS
      });
      var rendered = instance.render.call(instance.proxy, h);
      if (Array.isArray(rendered)) {
        if (rendered.length !== 1) throw new Error('[ada-render] SSR 组件必须只有一个根节点');
        rendered = rendered[0];
      }
      return serializeNode(rendered, ctx, depth + 1);
    }

    if (node._html !== undefined) {
      throw new Error('[ada-render] v-html 内容不能进入 SSR 线格式；请把清洗后的 HTML 拆成 VNode，或在浏览器端渲染');
    }

    var out = { t: node.t, p: cleanProps(node.p) };
    var children = serializeChildren(node.c, ctx, depth);
    if (children.length) out.c = children;
    return out;
  }

  function serializeChildren(children, ctx, depth) {
    var out = [];
    if (!children) return out;
    for (var i = 0; i < children.length; i += 1) {
      var item = serializeNode(children[i], ctx, depth + 1);
      if (item === null) continue;
      if (Array.isArray(item)) {
        for (var j = 0; j < item.length; j += 1) out.push(item[j]);
      } else {
        out.push(item);
      }
    }
    return out;
  }

  function renderToJSON(componentOrVNode, props, options) {
    var app = options && options.app;
    var ctx = {
      components: (options && options.components) || (app && app._components) || EMPTY_COMPONENTS,
      app: app || null
    };
    var vnode = isVNode(componentOrVNode)
      ? componentOrVNode
      : h(componentOrVNode, props || {});
    var output = serializeNode(vnode, ctx, 0);
    if (Array.isArray(output)) {
      if (output.length !== 1) throw new Error('[ada-render] renderToJSON 需要单个根节点');
      return output[0];
    }
    return output;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 10. createApp：注册组件、挂载/水合
   * ═══════════════════════════════════════════════════════════════════ */

  function createApp(options) {
    options = options || {};
    var app = {
      _options: options,
      _components: Object.create(null),
      _doc: options.document || DOC,
      _instance: null,
      _container: null,
      component: function (name, def) {
        if (def === undefined) return this._components[name];
        this._components[name] = normalizeComponent(def);
        return this;
      },
      mount: function (selector, mountOptions) {
        mountOptions = mountOptions || {};
        var container = typeof selector === 'string'
          ? this._doc.querySelector(selector)
          : selector;
        if (!container) throw new Error('[ada-render] 找不到挂载容器 ' + selector);
        if (this._instance) this.unmount();

        var rootVNode = {
          t: COMPONENT,
          n: normalizeComponent(options),
          p: options.props || {},
          c: []
        };
        this._container = container;
        if (mountOptions.hydrate) {
          this._instance = hydrateRoot(rootVNode, container, this);
        } else {
          this._instance = mountRoot(rootVNode, container, this);
        }
        return this._instance.proxy;
      },
      unmount: function () {
        if (this._instance) unmountComponent(this._instance);
        this._instance = null;
        if (this._container) this._container.innerHTML = '';
      }
    };

    if (options.components) {
      for (var name in options.components) {
        if (Object.prototype.hasOwnProperty.call(options.components, name)) {
          app.component(name, options.components[name]);
        }
      }
    }
    return app;
  }

  function mountRoot(vnode, container, app) {
    var instance = createInstance(vnode, null, app);
    vnode._component = instance;
    var firstRun = true;
    var runner = effect(function () {
      var next = singleRoot(instance.render.call(instance.proxy, h), instance.def.name);
      if (firstRun) {
        firstRun = false;
        instance.subTree = next;
        mount(next, container, null, instance);
        instance.isMounted = true;
        callHook(instance, 'mounted');
        return;
      }
      patch(instance.subTree, next, container, null, instance);
      instance.subTree = next;
      callHook(instance, 'updated');
    }, {
      lazy: true,
      scheduler: function () { queueJob(runner); }
    });
    instance.effect = runner;
    runner();
    return instance;
  }

  function hydrateRoot(vnode, container, app) {
    var instance = createInstance(vnode, null, app);
    vnode._component = instance;
    var firstRun = true;
    var runner = effect(function () {
      var next = singleRoot(instance.render.call(instance.proxy, h), instance.def.name);
      if (firstRun) {
        firstRun = false;
        instance.subTree = next;
        return;
      }
      patch(instance.subTree, next, container, null, instance);
      instance.subTree = next;
      callHook(instance, 'updated');
    }, {
      lazy: true,
      scheduler: function () { queueJob(runner); }
    });
    instance.effect = runner;
    runner();
    hydrateNode(instance.subTree, skipHydrationNoise(container.firstChild), container, instance);
    instance.isMounted = true;
    callHook(instance, 'mounted');
    return instance;
  }

  /* ═══════════════════════════════════════════════════════════════════
   * 11. 导出
   * ═══════════════════════════════════════════════════════════════════ */

  return {
    version: VERSION,
    h: h,
    host: host,
    text: text,
    defineComponent: defineComponent,
    createApp: createApp,
    reactive: reactive,
    ref: ref,
    computed: computed,
    watchEffect: watchEffect,
    nextTick: nextTick,
    renderToJSON: renderToJSON,
    toJSON: renderToJSON,
    evaluate: function (source, scope) {
      return evalAst(parseExpression(source), scope || {});
    },
    compile: function (template, options) {
      var opts = options || {};
      var doc = opts.document || DOC;
      return compileTemplateRoot(template, doc, {
        _options: { allowHTML: !!opts.allowHTML },
        _components: opts.components || {}
      }, opts.components || {});
    }
  };
});
