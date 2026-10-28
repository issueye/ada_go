// cz-01: node:crypto 与 node:zlib 的存在性（设计 6.12.2 两张表的 crypto/zlib 行）。
// 上游 alpha.11 两者都没有，WebCrypto 的全局 crypto 也没有：矩阵的 TierAlways
// 在这里全是宿主工作量，本条断言的是「模块不存在」这一负向事实。
export const meta = { id: "cz-01-crypto-zlib-absent", layer: "crypto-zlib", title: "crypto/zlib 模块存在性" };

import { importState, typeofGlobals } from "../_lib/shape.inc.mjs";

export default async function run() {
  const crypto = await importState("node:crypto");
  const zlib = await importState("node:zlib");
  const gzipSync = await importState("node:zlib");
  return {
    returns: {
      crypto,
      zlib,
      gzipSyncExists: gzipSync.state,
      globals: typeofGlobals(["crypto", "CompressionStream", "DecompressionStream"]),
      subtleType: (() => { try { return typeof globalThis.crypto.subtle } catch (e) { return "absent" } })(),
    },
    codes: { crypto: crypto.code, zlib: zlib.code },
    order: [],
    effects: {},
  };
}
