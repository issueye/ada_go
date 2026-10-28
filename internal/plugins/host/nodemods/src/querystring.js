// node:querystring —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 只用 ECMAScript 内建对象；与 URL 的 x-www-form-urlencoded 不同，querystring 的
// escape 把空格编码成 %20（不是 "+"），decode 也把 "+" 当普通字符——这里与 Node 一致。
//
// 有意简化：decodeURIComponent 自定义钩子只接受函数（不做参数校验的细节）；
// 嵌套对象按 Node 的扁平语义处理（不解析 "a[b]=1"）。

const maxKeysDefault = 1000;

function decodeComponent(s) {
  try {
    return decodeURIComponent(s);
  } catch (e) {
    return s; // 非法转义：保留原文，不抛错（Node 的 querystring 也不抛）
  }
}

export function escape(str) {
  return encodeURIComponent(String(str));
}

export function unescape(str) {
  return decodeComponent(String(str));
}

export const encode = escape;
export const decode = unescape;

function makeNullProtoObject() {
  return Object.create(null);
}

export function parse(str, sep, eq, options) {
  const separator = sep === undefined || sep === null ? "&" : String(sep);
  const equals = eq === undefined || eq === null ? "=" : String(eq);
  const opts = options || {};
  const decodeFn = typeof opts.decodeURIComponent === "function" ? opts.decodeURIComponent : decodeComponent;
  const maxKeys = opts.maxKeys === undefined ? maxKeysDefault : Number(opts.maxKeys);
  const out = makeNullProtoObject();
  if (str === undefined || str === null || str === "") return out;
  const source = String(str);
  const parts = separator === "" ? [source] : source.split(separator);
  let count = 0;
  for (const part of parts) {
    if (maxKeys > 0 && count >= maxKeys) break;
    if (part === "") continue;
    count++;
    const idx = part.indexOf(equals);
    let key;
    let value;
    if (idx < 0) {
      key = decodeFn(part);
      value = "";
    } else {
      key = decodeFn(part.slice(0, idx));
      value = decodeFn(part.slice(idx + equals.length));
    }
    if (Object.prototype.hasOwnProperty.call(out, key)) {
      const existing = out[key];
      // 重复键：第一次升级成数组（Node 的 maxKeys 只数键，不数项）。
      if (Array.isArray(existing)) existing.push(value);
      else out[key] = [existing, value];
    } else {
      out[key] = value;
    }
  }
  return out;
}

export function stringify(obj, sep, eq, options) {
  const separator = sep === undefined || sep === null ? "&" : String(sep);
  const equals = eq === undefined || eq === null ? "=" : String(eq);
  const opts = options || {};
  const encodeFn = typeof opts.encodeURIComponent === "function" ? opts.encodeURIComponent : escape;
  if (obj === null || typeof obj !== "object") return "";
  const parts = [];
  for (const key of Object.keys(obj)) {
    const value = obj[key];
    const encodedKey = encodeFn(key);
    if (Array.isArray(value)) {
      for (const item of value) {
        if (item === undefined) continue;
        parts.push(encodedKey + equals + encodeFn(item === null ? "" : String(item)));
      }
      continue;
    }
    if (value === undefined) continue;
    parts.push(encodedKey + equals + encodeFn(value === null ? "" : String(value)));
  }
  return parts.join(separator);
}

export default { parse, stringify, escape, unescape, encode, decode };
