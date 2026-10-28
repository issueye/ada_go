// fs-09: 单次读写 16MB 与目录 10k 条目上限（设计 6.13、6.11.1 E-6、6.12.4
// ERR_FS_FILE_TOO_LARGE）。上游没有这两个上限，宿主夹逼要到 M1-C 才有。
export const meta = { id: "fs-09-limits", layer: "fs", title: "单次字节与目录条目上限" };

import fs from "node:fs";
import { shapeOf, sorted } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const big = shapeOf(() => fs.readFileSync(P("big.bin")));
  const names = sorted(fs.readdirSync(P("many")));
  return {
    returns: {
      bigReadBytes: typeof big.ok === "object" && big.ok ? big.ok.length : 0,
      bigReadCode: big.code ?? null,
      entries: names.length,
      statSize: fs.statSync(P("big.bin")).size,
    },
    codes: { tooLarge: big.code ?? null },
    order: [],
    effects: { firstName: names[0] },
  };
}
