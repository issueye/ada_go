// cz-02: 已知向量与压缩往返（设计 6.14.2 crypto/zlib 层）。
// ada-go 宿主用 Go 标准库自建，M1-C 才落地 → moejs 侧 decision=skip-host；
// 这里的期望值来自 Node 20，正好当宿主实现的验收表。
export const meta = { id: "cz-02-vectors", layer: "crypto-zlib", title: "crypto/zlib 已知向量" };

export default async function run() {
  const { createHash, createHmac, pbkdf2Sync, timingSafeEqual, randomUUID, randomBytes, hkdfSync } = await import("node:crypto");
  const { gzipSync, gunzipSync, deflateSync, inflateSync, brotliCompressSync } = await import("node:zlib");
  const hex = (b) => Buffer.from(b).toString("hex");
  const eq = timingSafeEqual(Buffer.from("a"), Buffer.from("a"));
  return {
    returns: {
      sha256abc: createHash("sha256").update("abc").digest("hex"),
      sha1abc: createHash("sha1").update("abc").digest("hex"),
      md5abc: createHash("md5").update("abc").digest("hex"),
      sha3_256abc: createHash("sha3-256").update("abc").digest("hex"),
      hmac: createHmac("sha256", "key").update("abc").digest("hex"),
      pbkdf2: hex(pbkdf2Sync("password", "salt", 1, 16, "sha256")),
      hkdf: hex(hkdfSync("sha256", "ikm", "salt", "info", 16)),
      timingSafeEqual: eq,
      uuidVersion: String(randomUUID()).length,
      randomBytesLength: randomBytes(16).length,
      gzipRound: gunzipSync(gzipSync(Buffer.from("round-trip"))).toString(),
      deflateRound: inflateSync(deflateSync(Buffer.from("round-trip"))).toString(),
      badAlgo: (() => { try { createHash("no-such-algo"); return "no-throw" } catch (e) { return e.code || e.name } })(),
      brotli: typeof brotliCompressSync,
    },
    codes: {},
    order: [],
    effects: {},
  };
}
