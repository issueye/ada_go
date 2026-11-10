// bin-01: Buffer 的形状与全局同一性（设计 6.12.2 buffer 行）。
export const meta = { id: "bin-01-buffer", layer: "binary", title: "Buffer 与 Uint8Array" };

import buffer from "node:buffer";

export default async function run() {
  const unsafe = Buffer.allocUnsafe(8);
  return {
    returns: {
      isUint8: Buffer.from("abc") instanceof Uint8Array,
      utf8: Buffer.from("abc").toString("utf8"),
      hex: Buffer.from("616263", "hex").toString("utf8"),
      latin1: Buffer.from([0xe9]).toString("latin1"),
      concat: Buffer.concat([Buffer.from("a"), Buffer.from("b")]).toString(),
      isBuffer: [Buffer.isBuffer(Buffer.from("x")), Buffer.isBuffer(new Uint8Array(1))].join(","),
      byteLengthCJK: buffer.Buffer.byteLength("中文"),
      allocUnsafeZeroed: Array.from(unsafe).every((x) => x === 0),
      kMaxLength: buffer.kMaxLength,
      globalSame: buffer.Buffer === Buffer,
      noBigInt64Read: typeof Buffer.alloc(8).readBigInt64LE,
    },
    codes: {},
    order: [],
    effects: {},
  };
}
