// node:util —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
// 只用 ECMAScript 内建对象：无 import，无 Buffer / process / console，也不依赖全局
// TextEncoder/TextDecoder。有意简化：inspect 恒为单行、永不上色，不展开自定义
// inspect symbol/getter/Error.stack；%o 与 %O 走同一 inspect；promisify 不认 .custom；
// isDeepStrictEqual 不比较 Symbol 键与不可枚举属性；TextDecoder 只支持 utf-8/utf8，
// fatal 与 stream 选项忽略（非法字节一律替换为 U+FFFD）。

const toStringTag = Object.prototype.toString;
const hasOwn = Object.prototype.hasOwnProperty;
const kMaxOutput = 10000; // 输出字符上限（宿主固定）
const kMaxArrayLength = 100; // 数组/Set/Map 最多展示元素数（Node 默认同值）

function tagOf(value) {
  return toStringTag.call(value).slice(8, -1);
}

function invalidArgType(name, expected, actual) {
  const got = actual === null ? 'null' : 'type ' + typeof actual;
  const err = new TypeError('The "' + name + '" argument must be of type ' + expected + '. Received ' + got);
  err.code = 'ERR_INVALID_ARG_TYPE';
  return err;
}

// ---------------------------------------------------------------- inspect

function inspect(value, opts) {
  const o = opts || {};
  let depth = o.depth === undefined ? 2 : o.depth;
  if (depth === null) depth = Infinity;
  if (typeof depth !== 'number') depth = 2;
  const out = formatValue(value, 0, depth, o.sorted === true, []);
  return out.length > kMaxOutput
    ? out.slice(0, kMaxOutput) + '... ' + (out.length - kMaxOutput) + ' more characters'
    : out;
}

function formatValue(v, depth, maxDepth, sorted, seen) {
  const t = typeof v;
  if (t === 'string') return quoteString(v);
  if (t === 'number') return Object.is(v, -0) ? '-0' : String(v);
  if (t === 'boolean' || t === 'undefined') return String(v);
  if (t === 'bigint') return String(v) + 'n';
  if (t === 'symbol') return v.toString();
  if (t === 'function') {
    const name = v.name;
    // 类声明按 Node 形状输出 [class X]，其余按具名/匿名函数。
    return /^class[\s{]/.test(Function.prototype.toString.call(v))
      ? '[class ' + (name || '(anonymous)') + ']'
      : name ? '[Function: ' + name + ']' : '[Function (anonymous)]';
  }
  if (v === null) return 'null';
  if (seen.indexOf(v) !== -1) return '[Circular]'; // 循环引用只留标记（简化）
  const tag = tagOf(v);
  if (depth > maxDepth) { // 深度用尽：给 [Object] / [Foo] 形状的占位
    return '[' + (tag === 'Object' ? 'Object' : (v.constructor && v.constructor.name) || tag) + ']';
  }
  if (tag === 'Date') return Number.isNaN(v.getTime()) ? 'Invalid Date' : v.toISOString();
  if (tag === 'RegExp') return String(v);
  if (tag === 'Error') return formatError(v);
  if (tag === 'Promise') return 'Promise { <pending> }';
  if (tag === 'Map') return formatCollection(v, true, depth, maxDepth, sorted, seen);
  if (tag === 'Set') return formatCollection(v, false, depth, maxDepth, sorted, seen);
  if (tag === 'ArrayBuffer') return 'ArrayBuffer { byteLength: ' + v.byteLength + ' }';
  if (ArrayBuffer.isView(v)) {
    const parts = [];
    const limit = Math.min(v.length, kMaxArrayLength);
    for (let i = 0; i < limit; i += 1) parts.push(String(v[i]));
    pushMore(parts, v.length);
    return tag + '(' + v.length + ') [ ' + parts.join(', ') + ' ]';
  }
  if (Array.isArray(v)) return formatArray(v, depth, maxDepth, sorted, seen);
  return formatObject(v, depth, maxDepth, sorted, seen);
}

function formatError(err) {
  const name = err.name || 'Error';
  const message = err.message === undefined ? '' : String(err.message);
  return (message ? name + ': ' + message : name) +
    (err.code === undefined ? '' : " { code: '" + String(err.code) + "' }");
}

function formatArray(arr, depth, maxDepth, sorted, seen) {
  seen.push(arr);
  const parts = [];
  const limit = Math.min(arr.length, kMaxArrayLength);
  for (let i = 0; i < limit; i += 1) {
    // 稀疏洞按 Node 形状标注（连续多个洞也逐个标 <1 empty item>，简化）。
    parts.push(i in arr ? formatValue(arr[i], depth + 1, maxDepth, sorted, seen) : '<1 empty item>');
  }
  pushMore(parts, arr.length);
  seen.pop();
  return parts.length === 0 ? '[]' : '[ ' + parts.join(', ') + ' ]';
}

function formatObject(obj, depth, maxDepth, sorted, seen) {
  seen.push(obj);
  const keys = Object.keys(obj);
  if (sorted) keys.sort();
  const parts = [];
  const limit = Math.min(keys.length, kMaxArrayLength);
  for (let i = 0; i < limit; i += 1) {
    parts.push(formatKey(keys[i]) + ': ' + formatValue(obj[keys[i]], depth + 1, maxDepth, sorted, seen));
  }
  pushMore(parts, keys.length);
  seen.pop();
  const body = parts.length === 0 ? '{}' : '{ ' + parts.join(', ') + ' }';
  const ctor = (obj.constructor && obj.constructor.name) || '';
  return ctor && ctor !== 'Object' ? ctor + ' ' + body : body;
}

function formatCollection(coll, isMap, depth, maxDepth, sorted, seen) {
  seen.push(coll);
  const parts = [];
  let count = 0;
  for (const item of coll) {
    if (count >= kMaxArrayLength) { parts.push('... ' + (coll.size - count) + ' more items'); break; }
    parts.push(isMap
      ? formatValue(item[0], depth + 1, maxDepth, sorted, seen) + ' => ' +
        formatValue(item[1], depth + 1, maxDepth, sorted, seen)
      : formatValue(item, depth + 1, maxDepth, sorted, seen));
    count += 1;
  }
  seen.pop();
  const head = (isMap ? 'Map(' : 'Set(') + coll.size + ') ';
  return head + (parts.length === 0 ? '{}' : '{ ' + parts.join(', ') + ' }');
}

function pushMore(parts, total) {
  if (total > kMaxArrayLength) parts.push('... ' + (total - kMaxArrayLength) + ' more items');
}

function formatKey(key) {
  return /^[A-Za-z_$][A-Za-z0-9_$]*$/.test(key) ? key : quoteString(key);
}

function quoteString(str) {
  let out = "'";
  for (let i = 0; i < str.length; i += 1) {
    const ch = str[i];
    const code = str.charCodeAt(i);
    if (ch === "'") out += "\\'";
    else if (ch === '\\') out += '\\\\';
    else if (ch === '\n') out += '\\n';
    else if (ch === '\r') out += '\\r';
    else if (ch === '\t') out += '\\t';
    else if (code < 0x20 || code === 0x7f) out += '\\x' + code.toString(16).padStart(2, '0');
    else out += ch;
  }
  return out + "'";
}

// ---------------------------------------------------------------- format

function format(...args) {
  return args.length === 0 ? '' : formatWithOptions({}, ...args);
}

function formatWithOptions(inspectOptions, fmt, ...args) {
  const opts = inspectOptions || {};
  if (typeof fmt !== 'string') { // 首参非字符串：逐个 inspect 后空格连接
    if (args.length === 0) return inspect(fmt, opts);
    return [fmt, ...args].map((v) => (typeof v === 'string' ? v : inspect(v, opts))).join(' ');
  }
  if (args.length === 0) return fmt;
  let out = '';
  let index = 0;
  for (let i = 0; i < fmt.length; i += 1) {
    const ch = fmt[i];
    if (ch !== '%' || i + 1 >= fmt.length) { out += ch; continue; }
    const spec = fmt[i + 1];
    if (spec === '%') { out += '%'; i += 1; continue; }
    // 未知占位符或缺参数：保留原样，交给后面的「多余参数追加」逻辑。
    if ('sdifjoOc'.indexOf(spec) === -1 || index >= args.length) { out += ch; continue; }
    const arg = args[index];
    index += 1;
    i += 1;
    if (spec === 's') {
      out += typeof arg === 'bigint' ? String(arg) + 'n'
        : typeof arg === 'object' && arg !== null && !hasOwn.call(arg, 'toString')
          ? inspect(arg, { depth: 0 }) : String(arg);
    } else if (spec === 'd') {
      out += typeof arg === 'bigint' ? String(arg) + 'n' : String(Number(arg));
    } else if (spec === 'i') {
      out += typeof arg === 'bigint' ? String(arg) + 'n' : String(parseInt(arg, 10));
    } else if (spec === 'f') {
      out += typeof arg === 'bigint' ? String(arg) + 'n' : String(parseFloat(arg));
    } else if (spec === 'j') {
      try { out += JSON.stringify(arg); } catch (_) { out += '[Circular]'; }
    } else if (spec === 'o' || spec === 'O') {
      out += inspect(arg, opts); // %o 本应含隐藏属性，这里与 %O 同形（简化）
    } // 'c'：CSS 占位符，吞掉参数、无输出（同 Node）
  }
  for (; index < args.length; index += 1) {
    out += ' ' + (typeof args[index] === 'string' ? args[index] : inspect(args[index], opts));
  }
  return out;
}

// ------------------------------------------------------------ 函数工具

function promisify(fn) {
  if (typeof fn !== 'function') throw invalidArgType('original', 'function', fn);
  return function promisified(...args) {
    return new Promise((resolve, reject) => {
      fn.call(this, ...args, (err, value) => { if (err) reject(err); else resolve(value); });
    });
  };
}

function callbackify(fn) {
  if (typeof fn !== 'function') throw invalidArgType('original', 'function', fn);
  return function callbackified(...args) {
    const callback = args[args.length - 1];
    if (typeof callback !== 'function') throw invalidArgType('callback', 'function', callback);
    // 回调始终异步（Node 用 process.nextTick，这里用微任务），且只回调一次。
    const settle = (err, value) => Promise.resolve().then(
      () => (err ? callback(err) : callback(null, value))
    );
    try {
      fn.apply(this, args.slice(0, -1)).then(
        (value) => settle(null, value), (err) => settle(err)
      );
    } catch (err) {
      settle(err); // 同步抛错也走回调，不让异常逃逸
    }
  };
}

function inherits(ctor, superCtor) {
  if (typeof ctor !== 'function') throw invalidArgType('ctor', 'function', ctor);
  if (typeof superCtor !== 'function') throw invalidArgType('superCtor', 'function', superCtor);
  Object.defineProperty(ctor, 'super_', { value: superCtor, writable: true, configurable: true });
  Object.setPrototypeOf(ctor.prototype, superCtor.prototype);
}

function deprecate(fn, msg) {
  if (typeof fn !== 'function') throw invalidArgType('fn', 'function', fn);
  void msg; // 不做告警（无 stderr），仅保留「原样调用」的可观察行为
  return function deprecated(...args) {
    return fn.apply(this, args);
  };
}

const types = {
  isPromise(value) { return value instanceof Promise || tagOf(value) === 'Promise'; },
  isDate(value) { return tagOf(value) === 'Date'; },
  isRegExp(value) { return tagOf(value) === 'RegExp'; },
  isMap(value) { return tagOf(value) === 'Map'; },
  isSet(value) { return tagOf(value) === 'Set'; },
  isArrayBuffer(value) { return tagOf(value) === 'ArrayBuffer'; },
  isTypedArray(value) { return ArrayBuffer.isView(value) && tagOf(value) !== 'DataView'; },
  isUint8Array(value) { return tagOf(value) === 'Uint8Array'; },
  isNativeError(value) { return value instanceof Error; },
  isAsyncFunction(value) { return tagOf(value) === 'AsyncFunction'; },
  isGeneratorFunction(value) { return tagOf(value) === 'GeneratorFunction'; },
  isProxy() { return false; }, // 纯 JS 无法探测 Proxy 内部槽（Node 用 V8 的 IsProxy）
};

// ------------------------------------------------------- isDeepStrictEqual

function isDeepStrictEqual(a, b) {
  return deepEqual(a, b, new Map());
}

function deepEqual(a, b, seen) {
  if (a === b) return a !== 0 || 1 / a === 1 / b; // +0 与 -0 不等
  if (typeof a === 'number' && typeof b === 'number' && Number.isNaN(a) && Number.isNaN(b)) return true;
  if (a === null || b === null || typeof a !== 'object' || typeof b !== 'object') return false;
  if (tagOf(a) !== tagOf(b)) return false;
  if (Object.getPrototypeOf(a) !== Object.getPrototypeOf(b)) return false;
  const mapped = seen.get(a);
  if (mapped !== undefined) return mapped === b; // 环上已比较过
  seen.set(a, b);
  const tag = tagOf(a);
  if (tag === 'Date') return Object.is(a.getTime(), b.getTime());
  if (tag === 'RegExp') return a.source === b.source && a.flags === b.flags;
  if (tag === 'ArrayBuffer') return a.byteLength === b.byteLength; // 未逐字节比较（简化）
  if (tag === 'Map') {
    if (a.size !== b.size) return false;
    const rest = Array.from(b);
    outerMap: for (const entry of a) {
      for (let i = 0; i < rest.length; i += 1) {
        // 用 seen 副本试配，失败不污染主比较路径；顺序无关（同 Node）。
        if (deepEqual(entry[0], rest[i][0], new Map(seen)) &&
            deepEqual(entry[1], rest[i][1], new Map(seen))) {
          rest.splice(i, 1);
          continue outerMap;
        }
      }
      return false;
    }
    return true;
  }
  if (tag === 'Set') {
    if (a.size !== b.size) return false;
    const rest = Array.from(b);
    outerSet: for (const value of a) {
      for (let i = 0; i < rest.length; i += 1) {
        if (deepEqual(value, rest[i], new Map(seen))) { rest.splice(i, 1); continue outerSet; }
      }
      return false;
    }
    return true;
  }
  const keysA = Object.keys(a);
  const keysB = Object.keys(b);
  if (keysA.length !== keysB.length) return false;
  for (let i = 0; i < keysA.length; i += 1) {
    if (!hasOwn.call(b, keysA[i])) return false;
    if (!deepEqual(a[keysA[i]], b[keysA[i]], seen)) return false;
  }
  return true;
}

// ------------------------------------------------------- TextEncoder/Decoder

class TextEncoder {
  constructor() { this.encoding = 'utf-8'; }

  encode(input) {
    const str = input === undefined ? '' : String(input);
    let length = 0; // 先按码点估算字节数（孤立代理按 U+FFFD 计 3 字节）
    for (const ch of str) {
      const cp = surrogateSafe(ch.codePointAt(0));
      length += cp < 0x80 ? 1 : cp < 0x800 ? 2 : cp < 0x10000 ? 3 : 4;
    }
    const out = new Uint8Array(length);
    let i = 0;
    for (const ch of str) {
      const cp = surrogateSafe(ch.codePointAt(0));
      if (cp < 0x80) out[i++] = cp;
      else if (cp < 0x800) { out[i++] = 0xc0 | (cp >> 6); out[i++] = 0x80 | (cp & 0x3f); }
      else if (cp < 0x10000) {
        out[i++] = 0xe0 | (cp >> 12);
        out[i++] = 0x80 | ((cp >> 6) & 0x3f);
        out[i++] = 0x80 | (cp & 0x3f);
      } else {
        out[i++] = 0xf0 | (cp >> 18);
        out[i++] = 0x80 | ((cp >> 12) & 0x3f);
        out[i++] = 0x80 | ((cp >> 6) & 0x3f);
        out[i++] = 0x80 | (cp & 0x3f);
      }
    }
    return out;
  }
}

function surrogateSafe(code) {
  return code >= 0xd800 && code <= 0xdfff ? 0xfffd : code;
}

class TextDecoder {
  constructor(encoding, options) {
    const label = encoding === undefined ? 'utf-8' : String(encoding).toLowerCase();
    if (label !== 'utf-8' && label !== 'utf8') {
      const err = new RangeError("The encoding label provided ('" + encoding + "') is invalid.");
      err.code = 'ERR_ENCODING_NOT_SUPPORTED';
      throw err;
    }
    this.encoding = 'utf-8';
    this.fatal = !!(options && options.fatal); // 接受但忽略：非法字节一律替换
    this.ignoreBOM = !!(options && options.ignoreBOM);
  }

  decode(input) {
    if (input === undefined) return '';
    let bytes;
    if (input instanceof Uint8Array) bytes = input;
    else if (input instanceof ArrayBuffer) bytes = new Uint8Array(input);
    else if (ArrayBuffer.isView(input)) bytes = new Uint8Array(input.buffer, input.byteOffset, input.byteLength);
    else throw invalidArgType('input', 'BufferSource', input);
    let i = 0;
    let out = '';
    // 默认丢弃 UTF-8 BOM（ignoreBOM: true 时才保留）。
    if (!this.ignoreBOM && bytes.length >= 3 && bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) i = 3;
    while (i < bytes.length) {
      const b0 = bytes[i];
      if (b0 < 0x80) { out += String.fromCharCode(b0); i += 1; continue; }
      const need = b0 >= 0xc2 && b0 <= 0xdf ? 1 : b0 >= 0xe0 && b0 <= 0xef ? 2 : b0 >= 0xf0 && b0 <= 0xf4 ? 3 : -1;
      let cp = need === 1 ? b0 & 0x1f : need === 2 ? b0 & 0x0f : b0 & 0x07;
      let ok = need > 0 && i + need < bytes.length;
      for (let k = 1; ok && k <= need; k += 1) {
        const b = bytes[i + k];
        // 首续字节的合法区间：排除过长编码、代理区与超出 U+10FFFF 的码点。
        const min = k === 1 && b0 === 0xe0 ? 0xa0 : k === 1 && b0 === 0xf0 ? 0x90 : 0x80;
        const max = k === 1 && b0 === 0xed ? 0x9f : k === 1 && b0 === 0xf4 ? 0x8f : 0xbf;
        if (b < min || b > max) { ok = false; break; }
        cp = (cp << 6) | (b & 0x3f);
      }
      if (!ok) { out += '\uFFFD'; i += 1; continue; } // 非法或截断序列：逐字节替换
      out += String.fromCodePoint(cp);
      i += need + 1;
    }
    return out;
  }
}

// ---------------------------------------------------------------- 默认导出

const util = {
  format, formatWithOptions, inspect, promisify, callbackify, inherits, deprecate,
  types, isDeepStrictEqual, TextEncoder, TextDecoder,
};

export {
  format, formatWithOptions, inspect, promisify, callbackify, inherits, deprecate,
  types, isDeepStrictEqual, TextEncoder, TextDecoder,
};

export default util;
