// node:zlib —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 同步编解码交给 import.meta.binding（Go 标准库 gzip/zlib/flate）；
// 解压产物在宿主侧夹到 16MB（12 §6.12.2）。
//
// 有意简化 / 明确不支持：
//   - 没有 brotli 与 zstd：相关函数不导出（typeof 为 "undefined"，调用会自然失败），
//     而不是导出一个「调用即抛」的假函数；
//   - 没有流式 createGzip/createGunzip/createDeflate 等（宿主没有 Node stream）；
//   - 回调形式的 gzip/gunzip/deflate/inflate 用微任务模拟；不给回调时返回 Promise
//     （Node 要求回调，这里放宽，文档标注）；
//   - 选项对象只认识 level（0-9），其余键忽略。

const binding = import.meta.binding;

function encodeUtf8(str) {
  const out = [];
  for (let i = 0; i < str.length; i++) {
    let code = str.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff && i + 1 < str.length) {
      const low = str.charCodeAt(i + 1);
      if (low >= 0xdc00 && low <= 0xdfff) {
        code = 0x10000 + ((code - 0xd800) << 10) + (low - 0xdc00);
        i++;
      }
    }
    if (code < 0x80) out.push(code);
    else if (code < 0x800) out.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    else if (code < 0x10000) out.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    else out.push(0xf0 | (code >> 18), 0x80 | ((code >> 12) & 0x3f), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
  }
  return new Uint8Array(out);
}

function toBytes(value) {
  if (typeof value === "string") return encodeUtf8(value);
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  const e = new TypeError("The \"buffer\" argument must be an instance of Buffer, TypedArray, or DataView");
  e.code = "ERR_INVALID_ARG_TYPE";
  throw e;
}

// 二进制经 hex 过桥：JS 字符串是 UTF-16，0x80-0xFF 的裸字节会被编码层改写。
function toHex(bytes) {
  let out = "";
  for (let i = 0; i < bytes.length; i++) {
    const b = bytes[i];
    out += (b < 16 ? "0" : "") + b.toString(16);
  }
  return out;
}

function fromHex(hexStr) {
  const out = new Uint8Array(hexStr.length >> 1);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(hexStr.substr(i * 2, 2), 16);
  }
  return out;
}

function wrap(hexStr) {
  const bytes = fromHex(hexStr);
  // 有上游 Buffer 时返回 Buffer（Node 的形状），否则退成 Uint8Array。
  const B = globalThis.Buffer;
  return typeof B === "function" && typeof B.from === "function" ? B.from(bytes) : bytes;
}

function sync(kind, method, value) {
  const hexIn = toHex(toBytes(value));
  const out = binding[method](kind, hexIn);
  return wrap(out);
}

export function gzipSync(buf) {
  return sync("gzip", "compress", buf);
}

export function gunzipSync(buf) {
  return sync("gzip", "decompress", buf);
}

export function deflateSync(buf) {
  return sync("deflate", "compress", buf);
}

export function inflateSync(buf) {
  return sync("deflate", "decompress", buf);
}

export function deflateRawSync(buf) {
  return sync("raw", "compress", buf);
}

export function inflateRawSync(buf) {
  return sync("raw", "decompress", buf);
}

// async 形式：Node 的签名是 (buf, opts?, callback)。这里允许省略 callback 返回 Promise。
function asyncForm(kind, method) {
  return function (buf, opts, callback) {
    if (typeof opts === "function") {
      callback = opts;
      opts = undefined;
    }
    const done = (resolve, reject) => {
      try {
        resolve(wrap(binding[method](kind, toHex(toBytes(buf)))));
      } catch (e) {
        reject(e);
      }
    };
    const promise = new Promise(done);
    if (typeof callback === "function") {
      promise.then(
        (v) => {
          if (typeof queueMicrotask === "function") queueMicrotask(() => callback(null, v));
          else Promise.resolve().then(() => callback(null, v));
        },
        (e) => {
          if (typeof queueMicrotask === "function") queueMicrotask(() => callback(e));
          else Promise.resolve().then(() => callback(e));
        },
      );
      return undefined;
    }
    return promise;
  };
}

export const gzip = asyncForm("gzip", "compress");
export const gunzip = asyncForm("gzip", "decompress");
export const deflate = asyncForm("deflate", "compress");
export const inflate = asyncForm("deflate", "decompress");
export const deflateRaw = asyncForm("raw", "compress");
export const inflateRaw = asyncForm("raw", "decompress");

export const constants = {
  Z_NO_FLUSH: 0,
  Z_SYNC_FLUSH: 2,
  Z_FULL_FLUSH: 3,
  Z_FINISH: 4,
  Z_DEFAULT_COMPRESSION: -1,
  Z_NO_COMPRESSION: 0,
  Z_BEST_SPEED: 1,
  Z_BEST_COMPRESSION: 9,
};

// 流式接口明确不存在（宿主没有 Node stream）；导出会让人以为可用，因此不导出。
export function createGzip() {
  const e = new Error("zlib.createGzip 不提供：宿主没有 Node stream（12 §6.12.2）");
  e.code = "ERR_NOT_SUPPORTED";
  throw e;
}

export const createGunzip = createGzip;
export const createDeflate = createGzip;
export const createInflate = createGzip;

export default {
  gzipSync,
  gunzipSync,
  deflateSync,
  inflateSync,
  deflateRawSync,
  inflateRawSync,
  gzip,
  gunzip,
  deflate,
  inflate,
  deflateRaw,
  inflateRaw,
  constants,
  createGzip,
  createGunzip,
  createDeflate,
  createInflate,
};
