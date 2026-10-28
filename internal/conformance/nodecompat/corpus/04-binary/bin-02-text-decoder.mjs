// bin-02: TextEncoder/TextDecoder 的 UTF-8、fatal、ignoreBOM 与分片解码
// （设计 6.11 二进制与编码行）。
export const meta = { id: "bin-02-text-decoder", layer: "binary", title: "编解码与分片" };

export default async function run() {
  const enc = new TextEncoder();
  const bytes = enc.encode("中A");
  const d = new TextDecoder("utf-8");
  const parts = [];
  for (const b of bytes) parts.push(d.decode(new Uint8Array([b]), { stream: true }));
  parts.push(d.decode());
  return {
    returns: {
      encoded: Array.from(bytes).join(","),
      decodeRound: new TextDecoder().decode(bytes),
      streamed: parts.filter((x) => x !== "").length,
      fatal: (() => {
        try { return new TextDecoder("utf-8", { fatal: true }).decode(new Uint8Array([0xff])) } catch (e) { return "throw:" + e.name }
      })(),
      ignoreBom: JSON.stringify(new TextDecoder("utf-8", { ignoreBOM: true }).decode(new Uint8Array([0xef, 0xbb, 0xbf, 0x41]))),
      stripBom: JSON.stringify(new TextDecoder("utf-8").decode(new Uint8Array([0xef, 0xbb, 0xbf, 0x41]))),
      encoderEncoding: enc.encoding,
      decoderEncoding: new TextDecoder().encoding,
    },
    codes: {},
    order: [],
    effects: {},
  };
}
