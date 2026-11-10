// node:string_decoder —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// StringDecoder 存在的理由只有一个：UTF-8 的多字节序列可能被切成两个 chunk，
// 解码器必须把不完整的前缀留到下一次 write（这里用 pending 字节缓冲实现）。
//
// 有意简化：
//   - 只做「字节 → 字符串」；不做 Node 的 base64/hex 内部 3 字节分组语义，
//     这两种编码返回的是原始字节的直通表示（文档标注）；
//   - 非法序列按 U+FFFD 处理（与 Node 一致），但不逐字节报告几个替换字符。

const REPLACEMENT = "\uFFFD";

function normalizeEncoding(encoding) {
  if (encoding === undefined || encoding === null) return "utf8";
  const name = String(encoding).toLowerCase();
  switch (name) {
    case "utf8":
    case "utf-8":
      return "utf8";
    case "utf16le":
    case "utf-16le":
    case "ucs2":
    case "ucs-2":
      return "utf16le";
    case "latin1":
    case "binary":
      return "latin1";
    case "ascii":
      return "ascii";
    case "base64":
      return "base64";
    case "hex":
      return "hex";
    default:
      return null;
  }
}

function unknownEncoding(encoding) {
  const e = new TypeError("Unknown encoding: " + String(encoding));
  e.code = "ERR_UNKNOWN_ENCODING";
  return e;
}

// toBytes 把 chunk 规整成 Uint8Array：Uint8Array 直接用，ArrayBuffer/视图取视图，
// 字符串按 UTF-8 编码（这是 Node 里 write(string) 的语义）。
function toBytes(chunk) {
  if (chunk === undefined || chunk === null) return new Uint8Array(0);
  if (chunk instanceof Uint8Array) return chunk;
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
  if (ArrayBuffer.isView(chunk)) return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
  if (typeof chunk === "string") return encodeUtf8(chunk);
  const e = new TypeError("The \"buf\" argument must be an instance of Buffer, TypedArray, or DataView");
  e.code = "ERR_INVALID_ARG_TYPE";
  throw e;
}

function concat(a, b) {
  if (a.length === 0) return b;
  if (b.length === 0) return a;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

export function encodeUtf8(str) {
  const out = [];
  for (let i = 0; i < str.length; i++) {
    let code = str.charCodeAt(i);
    // 代理对 → 一个码位
    if (code >= 0xd800 && code <= 0xdbff && i + 1 < str.length) {
      const low = str.charCodeAt(i + 1);
      if (low >= 0xdc00 && low <= 0xdfff) {
        code = 0x10000 + ((code - 0xd800) << 10) + (low - 0xdc00);
        i++;
      }
    }
    if (code < 0x80) {
      out.push(code);
    } else if (code < 0x800) {
      out.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else if (code < 0x10000) {
      out.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    } else {
      out.push(
        0xf0 | (code >> 18),
        0x80 | ((code >> 12) & 0x3f),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f),
      );
    }
  }
  return new Uint8Array(out);
}

// utf8Length 给出以 b 开头的 UTF-8 序列应有几个字节（非法首字节返回 1）。
function utf8Length(b) {
  if (b < 0x80) return 1;
  if (b >= 0xc2 && b <= 0xdf) return 2;
  if (b >= 0xe0 && b <= 0xef) return 3;
  if (b >= 0xf0 && b <= 0xf4) return 4;
  return 1;
}

// decodeUtf8 解一段字节。flush 为 false 时把结尾不完整的序列留在 rest 里。
function decodeUtf8(bytes, flush) {
  let out = "";
  let i = 0;
  while (i < bytes.length) {
    const b = bytes[i];
    const need = utf8Length(b);
    if (i + need > bytes.length) {
      if (!flush) {
        return { text: out, rest: bytes.slice(i) };
      }
      out += REPLACEMENT;
      i++;
      continue;
    }
    if (need === 1) {
      out += String.fromCharCode(b);
      i++;
      continue;
    }
    let code = b & (0xff >> (need + 1));
    let ok = true;
    for (let k = 1; k < need; k++) {
      const cont = bytes[i + k];
      if ((cont & 0xc0) !== 0x80) {
        ok = false;
        break;
      }
      code = (code << 6) | (cont & 0x3f);
    }
    if (!ok) {
      out += REPLACEMENT;
      i++;
      continue;
    }
    // 解出来的是码位，直接转 UTF-16（不是再编码回字节）。
    if (code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff)) {
      out += REPLACEMENT;
    } else if (code > 0xffff) {
      const c = code - 0x10000;
      out += String.fromCharCode(0xd800 + (c >> 10), 0xdc00 + (c & 0x3ff));
    } else {
      out += String.fromCharCode(code);
    }
    i += need;
  }
  return { text: out, rest: new Uint8Array(0) };
}

function decodeUtf16le(bytes, flush) {
  let out = "";
  let i = 0;
  for (; i + 1 < bytes.length; i += 2) {
    out += String.fromCharCode(bytes[i] | (bytes[i + 1] << 8));
  }
  const rest = i < bytes.length ? bytes.slice(i) : new Uint8Array(0);
  if (rest.length > 0 && flush) {
    out += REPLACEMENT;
    return { text: out, rest: new Uint8Array(0) };
  }
  return { text: out, rest };
}

function decodeLatin1(bytes) {
  let out = "";
  for (let i = 0; i < bytes.length; i++) out += String.fromCharCode(bytes[i]);
  return out;
}

export class StringDecoder {
  constructor(encoding) {
    const norm = normalizeEncoding(encoding);
    if (norm === null) throw unknownEncoding(encoding);
    this.encoding = norm;
    this.pending = new Uint8Array(0);
  }

  write(chunk) {
    const bytes = concat(this.pending, toBytes(chunk));
    const { text, rest } = this._decode(bytes, false);
    this.pending = rest;
    return text;
  }

  end(chunk) {
    const bytes = chunk === undefined ? this.pending : concat(this.pending, toBytes(chunk));
    const { text } = this._decode(bytes, true);
    this.pending = new Uint8Array(0);
    return text;
  }

  _decode(bytes, flush) {
    switch (this.encoding) {
      case "utf8":
        return decodeUtf8(bytes, flush);
      case "utf16le":
        return decodeUtf16le(bytes, flush);
      case "latin1":
        return { text: decodeLatin1(bytes), rest: new Uint8Array(0) };
      case "ascii": {
        const text = decodeLatin1(bytes).replace(/[\u0080-\uFFFF]/g, "");
        return { text, rest: new Uint8Array(0) };
      }
      case "hex": {
        let text = "";
        for (let i = 0; i < bytes.length; i++) text += (bytes[i] < 16 ? "0" : "") + bytes[i].toString(16);
        return { text, rest: new Uint8Array(0) };
      }
      case "base64": {
        const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        let text = "";
        for (let i = 0; i < bytes.length; i += 3) {
          const b0 = bytes[i];
          const b1 = i + 1 < bytes.length ? bytes[i + 1] : undefined;
          const b2 = i + 2 < bytes.length ? bytes[i + 2] : undefined;
          text += table[b0 >> 2];
          text += table[((b0 & 3) << 4) | (b1 === undefined ? 0 : b1 >> 4)];
          text += b1 === undefined ? "=" : table[((b1 & 15) << 2) | (b2 === undefined ? 0 : b2 >> 6)];
          text += b2 === undefined ? "=" : table[b2 & 63];
        }
        return { text, rest: new Uint8Array(0) };
      }
      default:
        return { text: "", rest: new Uint8Array(0) };
    }
  }
}

export default { StringDecoder };
