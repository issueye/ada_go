// node:crypto 的 JS 模块体：Node 形状、编码转换和 WebCrypto 外壳都在这里，
// 哈希/HMAC/KDF/随机数/常量时间比较落到 import.meta.binding。
// 过边界的字节统一用「每字符 0..255」的二进制字符串（toBin）。
// 不依赖 Buffer、process、TextEncoder、btoa 或任何 Node 内置模块。

const native = import.meta.binding;
const ALGOS = ["md5", "sha1", "sha256", "sha512", "sha3-256"];
const ALGO_MAP = { md5: "md5", sha1: "sha1", sha256: "sha256", sha512: "sha512", sha3256: "sha3-256" };
const B64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const HEX = "0123456789abcdef";
const KEY_BYTES = Symbol("crypto.keyBytes");

// ---------- 错误 ----------

function fail(Ctor, code, message, name) {
  const e = new Ctor(message);
  e.code = code;
  if (name) e.name = name;
  return e;
}

const typeError = (code, message) => fail(TypeError, code, message);
const rangeError = (code, message) => fail(RangeError, code, message);
const notSupported = (message) => fail(Error, "ERR_CRYPTO_NOT_SUPPORTED", message, "NotSupportedError");

function resolveAlgo(name) {
  const key = String(name == null ? "" : name).trim().toLowerCase().replace(/[^a-z0-9]/g, "");
  const algo = ALGO_MAP[key];
  if (!algo) throw fail(Error, "ERR_CRYPTO_INVALID_DIGEST", `Unsupported digest algorithm: ${String(name)}`);
  return algo;
}

// ---------- 字节与编码 ----------

// 手写 UTF-8：正确处理 BMP、代理对和孤立代理项（替换为 U+FFFD）。
function utf8Encode(value) {
  const s = String(value);
  const out = [];
  for (let i = 0; i < s.length; i++) {
    let c = s.charCodeAt(i);
    if (c < 0x80) {
      out.push(c);
    } else if (c < 0x800) {
      out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
    } else if (c >= 0xd800 && c <= 0xdbff) {
      const next = s.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        const cp = 0x10000 + ((c - 0xd800) << 10) + (next - 0xdc00);
        i++;
        out.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
      } else {
        out.push(0xef, 0xbf, 0xbd);
      }
    } else if (c >= 0xdc00 && c <= 0xdfff) {
      out.push(0xef, 0xbf, 0xbd);
    } else {
      out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
    }
  }
  return new Uint8Array(out);
}

function toBytes(value) {
  if (typeof value === "string") return utf8Encode(value);
  if (value instanceof Uint8Array) return value;
  if (value instanceof ArrayBuffer) return new Uint8Array(value);
  if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
  return utf8Encode(String(value)); // update 的兜底路径
}

const copyBytes = (value) => new Uint8Array(value);

function joinBytes(parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

function toBin(bytes) {
  let out = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    out += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
  }
  return out;
}

function fromHex(hex) {
  const s = String(hex).toLowerCase();
  if (s.length % 2 !== 0) throw typeError("ERR_INVALID_ARG_VALUE", "Invalid hex string length");
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < s.length; i += 2) {
    const hi = HEX.indexOf(s[i]);
    const lo = HEX.indexOf(s[i + 1]);
    if (hi < 0 || lo < 0) throw typeError("ERR_INVALID_ARG_VALUE", "Invalid hex string");
    out[i >> 1] = (hi << 4) | lo;
  }
  return out;
}

function toHex(bytes) {
  let out = "";
  for (const byte of bytes) out += HEX[byte >> 4] + HEX[byte & 15];
  return out;
}

// 手写 base64，标准字母表与 '=' 补齐。
function toBase64(bytes) {
  let out = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const a = bytes[i];
    const b1 = i + 1 < bytes.length ? bytes[i + 1] : 0;
    const b2 = i + 2 < bytes.length ? bytes[i + 2] : 0;
    out += B64[a >> 2] + B64[((a & 3) << 4) | (b1 >> 4)];
    out += i + 1 < bytes.length ? B64[((b1 & 15) << 2) | (b2 >> 6)] : "=";
    out += i + 2 < bytes.length ? B64[b2 & 63] : "=";
  }
  return out;
}

// update 的 encoding 只覆盖常见值；默认按 UTF-8。
function chunkBytes(chunk, encoding) {
  if (typeof chunk !== "string" || encoding == null) return toBytes(chunk);
  const enc = String(encoding).toLowerCase().replace(/-/g, "");
  if (enc === "hex") return fromHex(chunk);
  if (enc === "latin1" || enc === "binary") {
    const out = new Uint8Array(chunk.length);
    for (let i = 0; i < chunk.length; i++) out[i] = chunk.charCodeAt(i) & 0xff;
    return out;
  }
  return utf8Encode(chunk);
}

function digestOutput(hex, encoding) {
  if (encoding == null) return fromHex(hex);
  const enc = String(encoding).toLowerCase().replace(/-/g, "");
  if (enc === "hex") return toHex(fromHex(hex));
  if (enc === "base64") return toBase64(fromHex(hex));
  if (enc === "latin1" || enc === "binary") return toBin(fromHex(hex));
  throw typeError("ERR_INVALID_ARG_VALUE", `Unknown digest encoding: ${encoding}`);
}

// ---------- Hash / HMAC ----------

function finalizedError() {
  return fail(Error, "ERR_CRYPTO_HASH_FINALIZED", "Digest already called");
}

function hashLike(algo, finalize) {
  const parts = [];
  let done = false;
  const state = {
    algorithm: algo,
    update(chunk, encoding) {
      if (done) throw finalizedError();
      parts.push(copyBytes(chunkBytes(chunk, encoding)));
      return state;
    },
    digest(encoding) {
      if (done) throw finalizedError();
      done = true;
      return digestOutput(finalize(toBin(joinBytes(parts))), encoding);
    },
  };
  return state;
}

export function createHash(algorithm) {
  const algo = resolveAlgo(algorithm);
  return hashLike(algo, (data) => native.digestHex(algo, data));
}

export function createHmac(algorithm, key) {
  const algo = resolveAlgo(algorithm);
  const keyBin = toBin(toBytes(key));
  return hashLike(algo, (data) => native.hmacHex(algo, keyBin, data));
}

// ---------- 随机 ----------

function defer(fn) {
  if (typeof queueMicrotask === "function") queueMicrotask(fn);
  else Promise.resolve().then(fn);
}

export function randomBytes(size, callback) {
  if (!Number.isSafeInteger(size) || size < 0) throw rangeError("ERR_OUT_OF_RANGE", "size must be a non-negative integer");
  if (callback !== undefined && typeof callback !== "function") throw typeError("ERR_INVALID_ARG_TYPE", "callback must be a function");
  const bytes = fromHex(native.randomBytesHex(size));
  if (callback) {
    defer(() => callback(null, bytes));
    return undefined;
  }
  return bytes;
}

export function randomInt(min, max, callback) {
  if (typeof max === "function") {
    callback = max;
    max = undefined;
  } else if (callback !== undefined && typeof callback !== "function") {
    throw typeError("ERR_INVALID_ARG_TYPE", "callback must be a function");
  }

  const lo = max === undefined ? 0 : min;
  const hi = max === undefined ? min : max;
  if (typeof lo !== "number" || typeof hi !== "number") throw typeError("ERR_INVALID_ARG_TYPE", "min and max must be numbers");
  if (!Number.isSafeInteger(lo) || !Number.isSafeInteger(hi) || lo >= hi) {
    throw rangeError("ERR_OUT_OF_RANGE", `The range [${lo}, ${hi}) is invalid`);
  }

  const value = native.randomInt(lo, hi);
  if (callback) {
    defer(() => callback(null, value));
    return undefined;
  }
  return value;
}

export function randomUUID() {
  return String(native.randomUUID()).toLowerCase();
}

// ---------- KDF / 常量时间比较 ----------

function integerArg(value, name, minimum) {
  if (typeof value !== "number") throw typeError("ERR_INVALID_ARG_TYPE", `${name} must be a number`);
  if (!Number.isSafeInteger(value) || value < minimum) throw rangeError("ERR_OUT_OF_RANGE", `${name} is out of range`);
  return value;
}

export function pbkdf2Sync(password, salt, iterations, keylen, digest) {
  const algo = resolveAlgo(digest);
  const rounds = integerArg(iterations, "iterations", 1);
  const length = integerArg(keylen, "keylen", 0);
  if (length === 0) return new Uint8Array(0); // Go 绑定要求 keylen > 0
  return fromHex(native.pbkdf2Hex(algo, toBin(toBytes(password)), toBin(toBytes(salt)), rounds, length));
}

export function hkdfSync(digest, ikm, salt, info, keylen) {
  const algo = resolveAlgo(digest);
  const length = integerArg(keylen, "keylen", 0);
  if (length === 0) return new Uint8Array(0);
  const saltBytes = salt == null ? new Uint8Array(0) : toBytes(salt);
  const infoBytes = info == null ? new Uint8Array(0) : toBytes(info);
  return fromHex(native.hkdfHex(algo, toBin(toBytes(ikm)), toBin(saltBytes), toBin(infoBytes), length));
}

export function timingSafeEqual(left, right) {
  const a = toBytes(left);
  const b = toBytes(right);
  if (a.length !== b.length) throw rangeError("ERR_CRYPTO_TIMING_SAFE_EQUAL_LENGTH", "Input buffers must have the same byte length");
  return !!native.timingSafeEqual(toBin(a), toBin(b));
}

export function getHashes() {
  return ALGOS.slice();
}

export function getRandomValues(array) {
  const typed = ArrayBuffer.isView(array) && !(array instanceof DataView) && typeof array.length === "number";
  if (!typed) throw typeError("ERR_INVALID_ARG_TYPE", "Expected an integer TypedArray");
  if (array.length > 65536) throw rangeError("ERR_OUT_OF_RANGE", "Array length exceeds 65536");
  const bytes = fromHex(native.randomBytesHex(array.byteLength));
  new Uint8Array(array.buffer, array.byteOffset, array.byteLength).set(bytes);
  return array;
}

// ---------- WebCrypto ----------

function algorithmName(algorithm) {
  const raw = typeof algorithm === "string" ? algorithm : algorithm && algorithm.name;
  if (typeof raw !== "string" || raw.trim() === "") throw typeError("ERR_INVALID_ARG_TYPE", "Algorithm name is required");
  return raw.trim().toUpperCase().replace(/[^A-Z0-9]/g, "");
}

const WEB_DIGESTS = {
  SHA1: { digest: "sha1", name: "SHA-1" },
  SHA256: { digest: "sha256", name: "SHA-256" },
  SHA512: { digest: "sha512", name: "SHA-512" },
  SHA3256: { digest: "sha3-256", name: "SHA3-256" },
};

function webDigest(algorithm) {
  const found = WEB_DIGESTS[algorithmName(algorithm)];
  if (!found) throw notSupported(`Unsupported digest algorithm: ${algorithmName(algorithm)}`);
  return found;
}

function toArrayBuffer(bytes) {
  return new Uint8Array(bytes).buffer;
}

function importedKeyBytes(key) {
  if (!key || typeof key !== "object" || !(key[KEY_BYTES] instanceof Uint8Array)) throw typeError("ERR_INVALID_ARG_TYPE", "Expected a CryptoKey");
  return key[KEY_BYTES];
}

async function subtleDigest(algorithm, data) {
  const hash = webDigest(algorithm);
  return toArrayBuffer(fromHex(native.digestHex(hash.digest, toBin(toBytes(data)))));
}

async function importKey(format, keyData, algorithm, extractable, usages) {
  if (String(format || "").toLowerCase() !== "raw") throw notSupported("Only raw keys are supported");
  const name = algorithmName(algorithm);
  if (name !== "HMAC" && name !== "PBKDF2" && name !== "HKDF") throw notSupported(`Unsupported key algorithm: ${name}`);
  if (!Array.isArray(usages)) throw typeError("ERR_INVALID_ARG_TYPE", "usages must be an array");

  const params = algorithm && typeof algorithm === "object" ? algorithm : {};
  let hash;
  if (params.hash != null) hash = webDigest(params.hash);
  else if (name === "HMAC") throw typeError("ERR_INVALID_ARG_TYPE", "HMAC requires a hash");
  else hash = webDigest("SHA-1");

  const key = { type: "secret", algorithm: { name, hash: { name: hash.name } }, usages: usages.slice() };
  Object.defineProperty(key, KEY_BYTES, { value: copyBytes(toBytes(keyData)), enumerable: false });
  return key;
}

async function subtleSign(algorithm, key, data) {
  if (algorithmName(algorithm) !== "HMAC") throw notSupported("Only HMAC signing is supported");
  if (!key || key.type !== "secret" || !key.algorithm || algorithmName(key.algorithm) !== "HMAC") {
    throw typeError("ERR_INVALID_ARG_TYPE", "Expected an HMAC CryptoKey");
  }
  const hash = webDigest(key.algorithm.hash);
  return toArrayBuffer(fromHex(native.hmacHex(hash.digest, toBin(importedKeyBytes(key)), toBin(toBytes(data)))));
}

async function subtleVerify(algorithm, key, signature, data) {
  const expected = new Uint8Array(await subtleSign(algorithm, key, data));
  const actual = toBytes(signature);
  if (actual.length !== expected.length) return false;
  return native.timingSafeEqual(toBin(expected), toBin(actual));
}

async function subtleDeriveBits(algorithm, baseKey, length) {
  const name = algorithmName(algorithm);
  if (name !== "PBKDF2" && name !== "HKDF") throw notSupported(`Unsupported derivation algorithm: ${name}`);
  if (!Number.isSafeInteger(length) || length < 0 || length % 8 !== 0) {
    throw rangeError("ERR_OUT_OF_RANGE", "length must be a multiple of 8");
  }
  if (length === 0) return new ArrayBuffer(0);

  const params = algorithm && typeof algorithm === "object" ? algorithm : {};
  const hash = webDigest(params.hash);
  const raw = importedKeyBytes(baseKey);
  const salt = params.salt == null ? new Uint8Array(0) : toBytes(params.salt);
  const keylen = length / 8;
  let hex;
  if (name === "PBKDF2") {
    const iterations = integerArg(params.iterations, "iterations", 1);
    hex = native.pbkdf2Hex(hash.digest, toBin(raw), toBin(salt), iterations, keylen);
  } else {
    const info = params.info == null ? new Uint8Array(0) : toBytes(params.info);
    hex = native.hkdfHex(hash.digest, toBin(raw), toBin(salt), toBin(info), keylen);
  }
  return toArrayBuffer(fromHex(hex));
}

export const subtle = {
  digest: subtleDigest,
  importKey,
  sign: subtleSign,
  verify: subtleVerify,
  deriveBits: subtleDeriveBits,
};

export const webcrypto = { subtle, getRandomValues, randomUUID };

export default {
  createHash,
  createHmac,
  randomBytes,
  randomInt,
  randomUUID,
  pbkdf2Sync,
  hkdfSync,
  timingSafeEqual,
  getHashes,
  getRandomValues,
  webcrypto,
  subtle,
};
