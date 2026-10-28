// node:url —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 只用 ECMAScript 内建对象：没有 import，也没有 Buffer / process / console /
// TextEncoder / 宿主 URL 全局。fileURLToPath 与 pathToFileURL 只做字符串映射，
// 落在虚拟根 "/"（工作区）上；plugin:// 是宿主约定的虚拟 scheme，不碰文件系统。
//
// 有意省略（偏差都记在这里，便于与 Node 20 对账）：
//   1. 不做 IDNA / punycode：主机名只对 ASCII 小写化，非 ASCII 原样保留；
//      domainToASCII / domainToUnicode 是「toLowerCase + 透传」，永不抛错；
//   2. 百分号编码以 encodeURI 为底，再补 WHATWG 的 path / query / fragment 编码集，
//      已有的 %XX 原样保留，孤立代理项换成 U+FFFD；没有完整的 UTF-8 校验；
//   3. 简化状态机：protocol 设置只改 scheme 字段，不重排 path 形态；IPv4 规范化、
//      主机名 %XX 解码、非特殊 scheme 反斜杠处理均未实现；
//   4. file: 允许空 host；file://host/x 在 fileURLToPath 里退化为 "//host/x"
//      （不是 ERR_INVALID_FILE_URL_HOST）；pathToFileURL 按虚拟根解析相对路径；
//   5. URL.search 的空格是 %20，URLSearchParams.toString() 的是 "+"（同 Node）；
//   6. 错误一律是带 code 的 TypeError：ERR_INVALID_URL / ERR_INVALID_URL_SCHEME /
//      ERR_INVALID_ARG_TYPE / ERR_INVALID_TUPLE。

const SPECIAL_SCHEMES = { 'http:': true, 'https:': true, 'ws:': true, 'wss:': true, 'ftp:': true, 'file:': true };
const DEFAULT_PORTS = { 'http:': '80', 'https:': '443', 'ws:': '80', 'wss:': '443', 'ftp:': '21' };
const SCHEME_RE = /^([a-zA-Z][a-zA-Z0-9+.\-]*):/;
const PORT_RE = /^[0-9]*$/;
const PATH_EXTRA = /[?#`{}]/g;
const QUERY_EXTRA = /[#`{}]/g;
const SPECIAL_QUERY_EXTRA = /[#`{}']/g;
const FRAGMENT_EXTRA = /[`]/g;
const USERINFO_EXTRA = /[\/:;=@\[\\\]^|]/g;

const isSpecial = (scheme) => SPECIAL_SCHEMES[scheme] === true;

function fail(ctor, code, message, input) {
  const e = new ctor(message);
  e.code = code;
  if (input !== undefined) e.input = input;
  return e;
}

const invalidURL = (input, why) => fail(TypeError, 'ERR_INVALID_URL', 'Invalid URL' + (why ? ': ' + why : ''), input);
const invalidArgType = (name, expected) => fail(TypeError, 'ERR_INVALID_ARG_TYPE', 'The "' + name + '" argument must be ' + expected);
const invalidTuple = (index) => fail(TypeError, 'ERR_INVALID_TUPLE', 'Each query pair must be an iterable [name, value] tuple; got index ' + index);

// ---------------------------------------------------------------- 编码工具

// 孤立代理项换成 U+FFFD，避免 encodeURI 抛 URIError（WHATWG 也是这个口径）。
function wellFormed(str) {
  const s = String(str);
  let out = '';
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    const d = c >= 0xd800 && c <= 0xdbff && i + 1 < s.length ? s.charCodeAt(i + 1) : 0;
    if (d >= 0xdc00 && d <= 0xdfff) {
      out += s[i] + s[i + 1];
      i++;
    } else {
      out += c >= 0xd800 && c <= 0xdfff ? '\ufffd' : s[i];
    }
  }
  return out;
}

const pct = (ch) => '%' + ch.charCodeAt(0).toString(16).toUpperCase();

// encodeURI 会转义 []^|，但 WHATWG 的 path / query 编码集允许它们字面出现。
const relax = (enc) => enc.replace(/%5B/g, '[').replace(/%5D/g, ']').replace(/%5E/g, '^').replace(/%7C/g, '|');

// 以 encodeURI 为底增量编码：已有 %XX 保留（不二次编码），extra 是额外要转义的字面量。
function encodePreserving(str, extra, relaxLiterals) {
  const chunks = wellFormed(str).split(/(%[0-9a-fA-F]{2})/);
  let out = '';
  for (let i = 0; i < chunks.length; i++) {
    if (i % 2 === 1) {
      out += chunks[i].toUpperCase();
      continue;
    }
    let enc = encodeURI(chunks[i]);
    if (relaxLiterals) enc = relax(enc);
    out += extra === null ? enc : enc.replace(extra, pct);
  }
  return out;
}

const encodePath = (str) => encodePreserving(str, PATH_EXTRA, true);
const encodeQuery = (str, special) => encodePreserving(str, special ? SPECIAL_QUERY_EXTRA : QUERY_EXTRA, true);
const encodeFragment = (str) => encodePreserving(str, FRAGMENT_EXTRA, true);
const encodeUserinfo = (str) => encodePreserving(str, USERINFO_EXTRA, false);

const decodeLoose = (str) => {
  try {
    return decodeURIComponent(str);
  } catch (e) {
    return str; // 残缺的 %XX / 非法 UTF-8 不抛错，回退原文
  }
};

// application/x-www-form-urlencoded（URLSearchParams 用）：空格写成 "+"。
const formEncode = (str) => encodeURIComponent(wellFormed(str)).replace(/%20/g, '+').replace(/[!'()~]/g, pct);
const formDecode = (str) => decodeLoose(str.replace(/\+/g, ' '));

const lowerAscii = (str) => str.replace(/[A-Z]/g, (c) => String.fromCharCode(c.charCodeAt(0) + 32));

// ---------------------------------------------------------------- 解析工具

// WHATWG 会去掉首尾的 C0 控制符与空格，并删掉中间所有的 tab / 换行。
const trimInput = (str) =>
  String(str).replace(/^[\u0000-\u0020]+/, '').replace(/[\u0000-\u0020]+$/, '').replace(/[\t\n\r]/g, '');

// 把 "/p?q#f" 拆成三段：query / fragment 为 null 表示「没出现」，'' 表示「出现但为空」。
function splitTail(str) {
  let path = str;
  let query = null;
  let fragment = null;
  const h = path.indexOf('#');
  if (h >= 0) {
    fragment = path.slice(h + 1);
    path = path.slice(0, h);
  }
  const q = path.indexOf('?');
  if (q >= 0) {
    query = path.slice(q + 1);
    path = path.slice(0, q);
  }
  return { path: path, query: query, fragment: fragment };
}

// 逐段消解 "." / ".."（含 %2e 形式），保留绝对路径的前导空段。
function normalizePath(rawPath, special) {
  const segs = (special ? String(rawPath).replace(/\\/g, '/') : String(rawPath)).split('/');
  const out = [];
  for (let i = 0; i < segs.length; i++) {
    const token = segs[i].toLowerCase();
    const last = i === segs.length - 1;
    if (token === '.' || token === '%2e') {
      if (last) out.push('');
    } else if (token === '..' || token === '.%2e' || token === '%2e.' || token === '%2e%2e') {
      if (out.length > 1) out.pop();
      if (last) out.push('');
    } else {
      out.push(segs[i]);
    }
  }
  return out.join('/');
}

function takeAuthority(str, allowBackslash) {
  for (let i = 0; i < str.length; i++) {
    const c = str[i];
    if (c === '/' || c === '?' || c === '#' || (allowBackslash && c === '\\')) {
      return { authority: str.slice(0, i), rest: str.slice(i) };
    }
  }
  return { authority: str, rest: '' };
}

function parsePort(rawPort, scheme, input) {
  if (rawPort === '') return '';
  if (!/^[0-9]+$/.test(rawPort)) throw invalidURL(input, 'invalid port');
  const n = parseInt(rawPort, 10);
  if (n > 65535) throw invalidURL(input, 'invalid port');
  const port = String(n);
  return DEFAULT_PORTS[scheme] === port ? '' : port; // 默认端口不写进 href
}

function splitHostPort(hostport, scheme, input) {
  if (hostport[0] === '[') {
    const close = hostport.indexOf(']');
    if (close < 0) throw invalidURL(input, 'unclosed IPv6 address');
    const tail = hostport.slice(close + 1);
    if (tail === '') return { host: hostport, port: '' };
    if (tail[0] !== ':') throw invalidURL(input, 'invalid host');
    return { host: hostport.slice(0, close + 1), port: parsePort(tail.slice(1), scheme, input) };
  }
  const idx = hostport.lastIndexOf(':');
  if (idx >= 0 && PORT_RE.test(hostport.slice(idx + 1))) {
    return { host: hostport.slice(0, idx), port: parsePort(hostport.slice(idx + 1), scheme, input) };
  }
  return { host: hostport, port: '' };
}

// 主机名：ASCII 小写化；非 ASCII 原样保留（不做 IDNA，见文件头第 1 条）。
const normalizeHost = (host, scheme) => (host === '' || host[0] === '[' || scheme === null || isSpecial(scheme) ? lowerAscii(host) : host);

function parseAuthority(authority, scheme, input) {
  let userinfo = '';
  let hostport = authority;
  const at = authority.lastIndexOf('@');
  if (at >= 0) {
    userinfo = authority.slice(0, at);
    hostport = authority.slice(at + 1);
  }
  let username = '';
  let password = '';
  if (userinfo !== '') {
    const colon = userinfo.indexOf(':');
    username = encodeUserinfo(colon < 0 ? userinfo : userinfo.slice(0, colon));
    if (colon >= 0) password = encodeUserinfo(userinfo.slice(colon + 1));
  }
  const hp = splitHostPort(hostport, scheme, input);
  return { username: username, password: password, host: normalizeHost(hp.host, scheme), port: hp.port };
}

function cloneParts(p) {
  return {
    scheme: p.scheme,
    username: p.username,
    password: p.password,
    host: p.host,
    port: p.port,
    path: p.path,
    query: p.query,
    fragment: p.fragment,
    opaque: p.opaque,
    hasAuthority: p.hasAuthority,
  };
}

function serializeAuthority(parts) {
  let out = '';
  if (parts.username !== '' || parts.password !== '') {
    out += parts.password === '' ? parts.username + '@' : parts.username + ':' + parts.password + '@';
  }
  out += parts.host;
  return parts.port === '' ? out : out + ':' + parts.port;
}

function serialize(parts) {
  let out = parts.scheme;
  if (!parts.opaque && parts.hasAuthority) out += '//' + serializeAuthority(parts);
  out += parts.path;
  if (parts.query !== null) out += '?' + parts.query;
  if (parts.fragment !== null) out += '#' + parts.fragment;
  return out;
}

function applyTail(parts, rest, special) {
  const tail = splitTail(rest);
  let path = special ? tail.path.replace(/\\/g, '/') : tail.path;
  if (parts.hasAuthority && path === '') path = '/';
  else if (parts.hasAuthority && path[0] !== '/') path = '/' + path;
  parts.path = encodePath(normalizePath(path, false));
  parts.query = tail.query === null ? null : encodeQuery(tail.query, special);
  parts.fragment = tail.fragment === null ? null : encodeFragment(tail.fragment);
}

function parseAbs(str, input) {
  const m = SCHEME_RE.exec(str);
  if (m === null) throw invalidURL(input, 'missing scheme');
  const scheme = m[1].toLowerCase() + ':';
  const special = isSpecial(scheme);
  let rest = str.slice(m[0].length);
  const parts = {
    scheme: scheme,
    username: '',
    password: '',
    host: '',
    port: '',
    path: '',
    query: null,
    fragment: null,
    opaque: false,
    hasAuthority: false,
  };

  // 不透明路径：about: / data: / mailto: / urn: ... 只认 ? 与 #。
  if (!special && rest.slice(0, 2) !== '//') {
    parts.opaque = true;
    const tail = splitTail(rest);
    parts.path = encodeFragment(tail.path);
    parts.query = tail.query === null ? null : encodeQuery(tail.query, false);
    parts.fragment = tail.fragment === null ? null : encodeFragment(tail.fragment);
    return parts;
  }

  parts.hasAuthority = true;
  if (scheme === 'file:') {
    // "file:///a" 剩下的 "/a" 表示「空 host」，不能当成 host 吃掉。
    if (rest.slice(0, 2) === '//') rest = rest.slice(2);
    else if (rest !== '' && rest[0] !== '/' && rest[0] !== '?' && rest[0] !== '#') rest = '/' + rest;
  } else if (special) {
    rest = rest.replace(/^[\/\\]+/, ''); // 特殊 scheme 允许 0..n 个 "/"（或 "\"）
  } else {
    rest = rest.slice(2); // 非特殊 scheme 只有精确的 "//" 才是 authority
  }

  const auth = takeAuthority(rest, special);
  const parsed = parseAuthority(auth.authority, scheme, input);
  parts.username = parsed.username;
  parts.password = parsed.password;
  parts.host = parsed.host;
  parts.port = scheme === 'file:' ? '' : parsed.port;
  if (special && parts.host === '' && scheme !== 'file:') throw invalidURL(input, 'empty host');
  applyTail(parts, auth.rest, special);
  return parts;
}

function parseURLString(input, base) {
  const original = String(input);
  const baseParts = base === undefined || base === null ? null : base instanceof URL ? base._parts : parseURLString(base, undefined);
  const s = trimInput(original);
  if (s === '') {
    if (baseParts === null) throw invalidURL(original, 'empty input');
    if (baseParts.opaque) throw invalidURL(original, 'relative to opaque base');
    const p = cloneParts(baseParts);
    p.fragment = null; // 空相对引用：只去掉 fragment
    return p;
  }
  if (SCHEME_RE.test(s)) return parseAbs(s, original);
  if (baseParts === null) throw invalidURL(original, 'no scheme');
  return parseRelative(baseParts, s, original);
}

function parseRelative(base, s, original) {
  if (s[0] === '#') {
    const p = cloneParts(base);
    p.fragment = encodeFragment(s.slice(1));
    return p;
  }
  if (base.opaque) throw invalidURL(original, 'relative to opaque base');
  const prefix = base.scheme + '//' + serializeAuthority(base);
  if (s.slice(0, 2) === '//') return parseAbs(base.scheme + s, original);
  if (s[0] === '/' || (isSpecial(base.scheme) && s[0] === '\\')) return parseAbs(prefix + s, original);
  if (s[0] === '?') return parseAbs(prefix + base.path + s, original);
  const cut = base.path.lastIndexOf('/');
  return parseAbs(prefix + (cut >= 0 ? base.path.slice(0, cut + 1) : '') + s, original);
}

// ---------------------------------------------------------------- URLSearchParams

class URLSearchParams {
  constructor(init) {
    this._list = [];
    this._owner = null;
    if (init === undefined || init === null) return;
    if (typeof init === 'string') {
      this._parseString(init);
    } else if (init instanceof URLSearchParams) {
      for (let i = 0; i < init._list.length; i++) this._list.push([init._list[i][0], init._list[i][1]]);
    } else if (typeof init === 'object' && typeof init[Symbol.iterator] === 'function') {
      let index = 0;
      for (const item of init) {
        if (item === null || typeof item !== 'object' || item.length !== 2) throw invalidTuple(index);
        this._list.push([String(item[0]), String(item[1])]);
        index++;
      }
    } else if (typeof init === 'object') {
      const keys = Object.keys(init);
      for (let i = 0; i < keys.length; i++) {
        const value = init[keys[i]];
        if (Array.isArray(value)) for (let j = 0; j < value.length; j++) this._list.push([keys[i], String(value[j])]);
        else this._list.push([keys[i], String(value)]);
      }
    } else {
      throw invalidArgType('init', 'of type string, object, or an iterable of [name, value] pairs');
    }
  }

  // 直接拿 URL 的 query 构造，并挂在 URL 上做单向回写。
  static fromQuery(query, owner) {
    const params = new URLSearchParams(query);
    params._owner = owner;
    return params;
  }

  _parseString(str) {
    let s = String(str);
    if (s[0] === '?') s = s.slice(1);
    if (s === '') return;
    const pairs = s.split('&');
    for (let i = 0; i < pairs.length; i++) {
      if (pairs[i] === '') continue;
      const eq = pairs[i].indexOf('=');
      if (eq < 0) this._list.push([formDecode(pairs[i]), '']);
      else this._list.push([formDecode(pairs[i].slice(0, eq)), formDecode(pairs[i].slice(eq + 1))]);
    }
  }

  _sync() {
    if (this._owner !== null) this._owner._updateSearch();
  }

  get size() {
    return this._list.length;
  }

  append(name, value) {
    this._list.push([String(name), String(value)]);
    this._sync();
  }

  delete(name, value) {
    const key = String(name);
    const wanted = arguments.length > 1 && value !== undefined ? String(value) : null;
    this._list = this._list.filter((p) => !(p[0] === key && (wanted === null || p[1] === wanted)));
    this._sync();
  }

  get(name) {
    const key = String(name);
    for (let i = 0; i < this._list.length; i++) if (this._list[i][0] === key) return this._list[i][1];
    return null;
  }

  getAll(name) {
    const key = String(name);
    return this._list.filter((p) => p[0] === key).map((p) => p[1]);
  }

  has(name, value) {
    const key = String(name);
    const wanted = arguments.length > 1 && value !== undefined ? String(value) : null;
    return this._list.some((p) => p[0] === key && (wanted === null || p[1] === wanted));
  }

  set(name, value) {
    const key = String(name);
    const wanted = String(value);
    const out = [];
    let found = false;
    for (let i = 0; i < this._list.length; i++) {
      const pair = this._list[i];
      if (pair[0] !== key) out.push(pair);
      else if (!found) {
        out.push([key, wanted]);
        found = true;
      }
    }
    if (!found) out.push([key, wanted]);
    this._list = out;
    this._sync();
  }

  sort() {
    // Array.prototype.sort 自 ES2019 起稳定；这里按 name 的 UTF-16 码元序比较。
    this._list.sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
    this._sync();
  }

  toString() {
    return this._list.map((p) => formEncode(p[0]) + '=' + formEncode(p[1])).join('&');
  }

  forEach(callback, thisArg) {
    if (typeof callback !== 'function') throw invalidArgType('callback', 'of type function');
    for (let i = 0; i < this._list.length; i++) callback.call(thisArg, this._list[i][1], this._list[i][0], this);
  }

  keys() {
    return this._list.map((p) => p[0])[Symbol.iterator]();
  }

  values() {
    return this._list.map((p) => p[1])[Symbol.iterator]();
  }

  entries() {
    return this._list.map((p) => [p[0], p[1]])[Symbol.iterator]();
  }

  [Symbol.iterator]() {
    return this.entries();
  }
}

// ---------------------------------------------------------------- URL

class URL {
  constructor(input, base) {
    this._parts = parseURLString(input, base);
    this._params = null;
  }

  _updateSearch() {
    const s = this._params === null ? '' : this._params.toString();
    this._parts.query = s === '' ? null : s;
  }

  get href() {
    return serialize(this._parts);
  }

  set href(value) {
    this._parts = parseURLString(value, undefined);
    this._params = null;
  }

  get protocol() {
    return this._parts.scheme;
  }

  set protocol(value) {
    let v = String(value);
    if (v === '') return;
    if (v[v.length - 1] !== ':') v += ':';
    if (!/^[a-zA-Z][a-zA-Z0-9+.\-]*:$/.test(v)) throw invalidURL(v, 'invalid protocol');
    this._parts.scheme = v.toLowerCase();
    if (isSpecial(this._parts.scheme) && !this._parts.opaque) {
      this._parts.hasAuthority = true;
      if (this._parts.path === '') this._parts.path = '/';
    }
    this._params = null;
  }

  get username() {
    return this._parts.username;
  }

  set username(value) {
    this._parts.username = encodeUserinfo(value);
  }

  get password() {
    return this._parts.password;
  }

  set password(value) {
    this._parts.password = encodeUserinfo(value);
  }

  get host() {
    return this._parts.port === '' ? this._parts.host : this._parts.host + ':' + this._parts.port;
  }

  set host(value) {
    const raw = String(value);
    const hp = splitHostPort(raw, this._parts.scheme, raw);
    this._parts.host = normalizeHost(hp.host, this._parts.scheme);
    this._parts.port = hp.port;
  }

  get hostname() {
    return this._parts.host;
  }

  set hostname(value) {
    this._parts.host = normalizeHost(String(value), this._parts.scheme);
  }

  get port() {
    return this._parts.port;
  }

  set port(value) {
    const raw = String(value);
    if (raw === '') {
      this._parts.port = '';
      return;
    }
    if (!/^[0-9]+$/.test(raw)) throw invalidURL(raw, 'invalid port');
    const n = parseInt(raw, 10);
    if (n > 65535) throw invalidURL(raw, 'invalid port');
    const port = String(n);
    this._parts.port = DEFAULT_PORTS[this._parts.scheme] === port ? '' : port;
  }

  get pathname() {
    return this._parts.path;
  }

  set pathname(value) {
    const p = this._parts;
    if (p.opaque) return; // 不透明路径不可写（与 WHATWG 一致）
    const special = isSpecial(p.scheme);
    let path = special ? String(value).replace(/\\/g, '/') : String(value);
    if (p.hasAuthority && path === '') path = '/';
    else if (p.hasAuthority && path[0] !== '/') path = '/' + path;
    p.path = encodePath(normalizePath(path, false));
  }

  get search() {
    return this._parts.query === null ? '' : '?' + this._parts.query;
  }

  set search(value) {
    const raw = String(value);
    if (raw === '') this._parts.query = null;
    else this._parts.query = encodeQuery(raw[0] === '?' ? raw.slice(1) : raw, isSpecial(this._parts.scheme));
    this._params = null;
  }

  get searchParams() {
    if (this._params === null) {
      this._params = URLSearchParams.fromQuery(this._parts.query === null ? '' : this._parts.query, this);
    }
    return this._params;
  }

  get hash() {
    return this._parts.fragment === null ? '' : '#' + this._parts.fragment;
  }

  set hash(value) {
    const raw = String(value);
    this._parts.fragment = raw === '' ? null : encodeFragment(raw[0] === '#' ? raw.slice(1) : raw);
  }

  get origin() {
    const p = this._parts;
    if (p.scheme !== 'http:' && p.scheme !== 'https:' && p.scheme !== 'ws:' && p.scheme !== 'wss:' && p.scheme !== 'ftp:') return 'null';
    return p.scheme + '//' + p.host + (p.port === '' ? '' : ':' + p.port);
  }

  toString() {
    return serialize(this._parts);
  }

  toJSON() {
    return serialize(this._parts);
  }
}

// ---------------------------------------------------------------- 遗留 API

const qsEscape = (value) => {
  const s = String(value);
  try {
    return encodeURIComponent(s);
  } catch (e) {
    return encodeURIComponent(wellFormed(s));
  }
};

function parseQueryObject(str) {
  const out = Object.create(null);
  if (str === '') return out;
  const pairs = str.split('&');
  for (let i = 0; i < pairs.length; i++) {
    if (pairs[i] === '') continue;
    const eq = pairs[i].indexOf('=');
    const key = decodeLoose(eq < 0 ? pairs[i] : pairs[i].slice(0, eq));
    const value = eq < 0 ? '' : decodeLoose(pairs[i].slice(eq + 1));
    if (out[key] === undefined) out[key] = value;
    else if (Array.isArray(out[key])) out[key].push(value);
    else out[key] = [out[key], value];
  }
  return out;
}

function stringifyQueryObject(obj) {
  const out = [];
  const keys = Object.keys(obj);
  for (let i = 0; i < keys.length; i++) {
    const value = obj[keys[i]];
    if (value === undefined) continue;
    const ek = qsEscape(keys[i]);
    if (Array.isArray(value)) for (let j = 0; j < value.length; j++) out.push(ek + '=' + qsEscape(value[j]));
    else out.push(ek + '=' + (value === null ? '' : qsEscape(value)));
  }
  return out.join('&');
}

function parse(urlString, parseQueryString, slashesDenoteHost) {
  const input = String(urlString);
  let rest = input;
  let hash = null;
  let search = null;

  const h = rest.indexOf('#');
  if (h >= 0) {
    hash = rest.slice(h);
    rest = rest.slice(0, h);
  }
  const q = rest.indexOf('?');
  if (q >= 0) {
    search = rest.slice(q);
    rest = rest.slice(0, q);
  }

  let protocol = null;
  const m = /^([a-zA-Z][a-zA-Z0-9+.\-]*:)/.exec(rest);
  if (m !== null) {
    protocol = m[1].toLowerCase();
    rest = rest.slice(m[1].length);
  }

  let slashes = null;
  let auth = null;
  let host = null;
  let hostname = null;
  let port = null;
  let pathname = rest === '' ? null : rest;

  // "//" 开头（不论 slashesDenoteHost）按 authority 处理，与 Node 的遗留解析一致。
  if (rest.slice(0, 2) === '//') {
    slashes = true;
    rest = rest.slice(2);
    const slash = rest.indexOf('/');
    const authority = slash < 0 ? rest : rest.slice(0, slash);
    rest = slash < 0 ? '' : rest.slice(slash);
    let hostport = authority;
    const at = authority.lastIndexOf('@');
    if (at >= 0) {
      auth = authority.slice(0, at);
      hostport = authority.slice(at + 1);
    }
    let hostPart = hostport;
    const colon = hostport.lastIndexOf(':');
    if (colon >= 0 && PORT_RE.test(hostport.slice(colon + 1))) {
      port = hostport.slice(colon + 1) === '' ? null : hostport.slice(colon + 1);
      hostPart = hostport.slice(0, colon);
    }
    hostname = lowerAscii(hostPart);
    host = port === null ? hostname : hostname + ':' + port;
    pathname = rest === '' ? '/' : rest;
  }

  let href = protocol === null ? '' : protocol;
  if (slashes) href += '//';
  if (auth) href += auth + '@';
  if (host) href += host;
  if (pathname !== null) href += pathname;
  if (search) href += search;
  if (hash) href += hash;

  let query = null;
  if (search !== null) query = parseQueryString ? parseQueryObject(search.slice(1)) : search.slice(1);
  const path = pathname === null && search === null ? null : (pathname === null ? '' : pathname) + (search === null ? '' : search);

  return {
    href: href,
    protocol: protocol,
    slashes: slashes,
    auth: auth,
    host: host,
    port: port,
    hostname: hostname,
    hash: hash,
    search: search,
    query: query,
    pathname: pathname,
    path: path,
  };
}

function format(urlObj) {
  const o = urlObj || {};
  const protocol = o.protocol ? String(o.protocol) : '';
  const auth = o.auth ? String(o.auth) : '';
  let host = o.host ? String(o.host) : '';
  if (host === '' && (o.hostname !== undefined || o.port !== undefined)) {
    host = (o.hostname ? String(o.hostname) : '') + (o.port ? ':' + String(o.port) : '');
  }
  const pathname = o.pathname ? String(o.pathname) : '';
  let search = o.search;
  if (typeof search !== 'string') {
    search = o.query !== undefined && o.query !== null && typeof o.query === 'object' ? '?' + stringifyQueryObject(o.query) : '';
  }
  const hash = o.hash ? (String(o.hash)[0] === '#' ? String(o.hash) : '#' + String(o.hash)) : '';
  const slashes = Boolean(o.slashes) || auth !== '' || host !== '';
  return protocol + (slashes ? '//' : '') + (auth === '' ? '' : auth + '@') + host + pathname + search + hash;
}

function resolve(from, to) {
  const base = parse(from, false, true);
  const target = String(to);
  if (base.protocol !== null) {
    try {
      return new URL(target, base.href).href;
    } catch (e) {
      // 落回下面的路径合并
    }
  }
  // 没有 scheme 的 from：按虚拟根 "/" 上的路径合并，再摘掉 file:// 前缀。
  const hadSlash = String(from)[0] === '/';
  try {
    const href = new URL(target, 'file:///' + String(from).replace(/^\/+/, '')).href.slice('file://'.length);
    return hadSlash ? href : href.replace(/^\//, '');
  } catch (e) {
    return target;
  }
}

// ---------------------------------------------------------------- 文件 URL 映射

function fileURLToPath(u) {
  let protocol;
  let host;
  let pathname;
  if (u instanceof URL) {
    protocol = u.protocol;
    host = u.host;
    pathname = u.pathname;
  } else if (typeof u === 'string') {
    const parsed = new URL(u);
    protocol = parsed.protocol;
    host = parsed.host;
    pathname = parsed.pathname;
  } else {
    throw invalidArgType('u', 'of type string or an instance of URL');
  }
  if (protocol === 'file:') {
    let p = decodeLoose(pathname);
    if (p === '') p = '/';
    // 虚拟 FS 没有 UNC，保留 "//host/x" 形状而不是抛 ERR_INVALID_FILE_URL_HOST。
    if (host !== '' && host !== 'localhost') p = '//' + decodeLoose(host) + p;
    return p;
  }
  if (protocol === 'plugin:') return pathname === '' ? '/' : decodeLoose(pathname); // plugin://<id>/x -> /x
  throw fail(TypeError, 'ERR_INVALID_URL_SCHEME', 'The URL must be of scheme file or plugin');
}

function encodeFilePath(path) {
  const segs = String(path).split('/');
  let out = '';
  for (let i = 0; i < segs.length; i++) {
    if (i > 0) out += '/';
    if (segs[i] !== '') out += encodeURIComponent(wellFormed(segs[i]));
  }
  return out;
}

function pathToFileURL(p) {
  if (typeof p !== 'string') throw invalidArgType('path', 'of type string');
  let abs = p === '' ? '/' : p;
  if (abs[0] !== '/') abs = '/' + abs; // 虚拟根：不查宿主 cwd
  return new URL('file://' + encodeFilePath(normalizePath(abs, false)));
}

// ---------------------------------------------------------------- 其它导出

// 不做 IDNA / punycode：ASCII 只小写化，非 ASCII 原样（返回小写形式），永不抛错。
const domainToASCII = (domain) => String(domain).toLowerCase();
const domainToUnicode = (domain) => String(domain).toLowerCase();

function urlToHttpOptions(u) {
  const url = u instanceof URL ? u : new URL(String(u));
  const hostname = url.hostname;
  const bracketed = hostname[0] === '[' && hostname[hostname.length - 1] === ']';
  const options = {
    protocol: url.protocol,
    hostname: bracketed ? hostname.slice(1, -1) : hostname,
    hash: url.hash,
    search: url.search,
    pathname: url.pathname,
    path: url.pathname + url.search,
    href: url.href,
    port: url.port === '' ? undefined : Number(url.port),
    auth: '',
  };
  if (url.username !== '' || url.password !== '') options.auth = decodeLoose(url.username) + ':' + decodeLoose(url.password);
  return options;
}

const url = {
  URL,
  URLSearchParams,
  parse,
  format,
  resolve,
  fileURLToPath,
  pathToFileURL,
  domainToASCII,
  domainToUnicode,
  urlToHttpOptions,
};

export {
  URL,
  URLSearchParams,
  parse,
  format,
  resolve,
  fileURLToPath,
  pathToFileURL,
  domainToASCII,
  domainToUnicode,
  urlToHttpOptions,
};
export default url;
