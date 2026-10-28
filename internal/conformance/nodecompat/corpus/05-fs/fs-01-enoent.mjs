// fs-01: ENOENT 的错误形状（设计 6.12.4 第一行、6.11.1 E-4）。
export const meta = { id: "fs-01-enoent", layer: "fs", title: "ENOENT 的 code/errno/syscall/path" };

import fs from "node:fs";
import { shapeOf, codeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const missing = shapeOf(() => fs.readFileSync(P("nope.txt")));
  return {
    returns: {
      codeIsENOENT: missing.code === "ENOENT",
      errnoIsNegativeLibuv: typeof missing.errno === "number" && missing.errno < 0,
      hasSyscall: missing.syscall === "open",
      pathIsInput: missing.path === P("nope.txt"),
      name: missing.name,
      ownNames: missing.ownNames,
      messagePrefix: String(missing.message).slice(0, 6),
      existsSync: fs.existsSync(P("nope.txt")),
    },
    codes: {
      readMissing: codeOf(() => fs.readFileSync(P("nope.txt"))),
      statMissing: codeOf(() => fs.statSync(P("nope.txt"))),
      readdirMissing: codeOf(() => fs.readdirSync(P("no-dir"))),
      openMissing: codeOf(() => fs.writeFileSync(P("sub/nope.txt"), "x")),
    },
    order: [],
    effects: {},
  };
}
