// fs-04: 读写/追加/列目录/stat/mkdir/rename/copyFile/rm/existsSync 的往返
// （设计 6.12.2 fs 行的兼容范围）。
export const meta = { id: "fs-04-roundtrip", layer: "fs", title: "同步读写往返" };

import fs from "node:fs";
import { codeOf, sorted } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const order = [];
  fs.writeFileSync(P("a.txt"), "one"); order.push("write");
  const read = fs.readFileSync(P("a.txt"), "utf8"); order.push("read");
  fs.appendFileSync(P("a.txt"), "+two"); order.push("append");
  const appended = fs.readFileSync(P("a.txt"), "utf8"); order.push("read-again");
  fs.mkdirSync(P("sub")); order.push("mkdir");
  fs.writeFileSync(P("sub/b.txt"), "inner2"); order.push("write-in-sub");
  fs.copyFileSync(P("sub/b.txt"), P("c.txt")); order.push("copyFile");
  fs.renameSync(P("sub/b.txt"), P("sub/d.txt")); order.push("rename");
  const st = fs.statSync(P("a.txt")); order.push("stat");
  const listing = sorted(fs.readdirSync(P("sub"))); order.push("readdir");
  const exists = fs.existsSync(P("c.txt")); order.push("existsSync");
  fs.rmSync(P("c.txt")); order.push("rm");
  const afterRm = fs.existsSync(P("c.txt")); order.push("verify-rm");
  return {
    returns: {
      read, appended, exists, afterRm,
      sizeIsFour: st.size === 4,
      isFile: st.isFile(),
      mtimeMsType: typeof st.mtimeMs,
      bufferWhenNoEncoding: Buffer.isBuffer(fs.readFileSync(P("a.txt"))),
      eexistNoRecursive: codeOf(() => fs.mkdirSync(P("sub"))),
      eexistWithRecursive: codeOf(() => fs.mkdirSync(P("sub"), { recursive: true })),
      rmNonEmptyDir: codeOf(() => fs.rmSync(P("sub"))),
      listing,
    },
    codes: {},
    order,
    effects: { rootHasA: sorted(fs.readdirSync(ctx.workspace)).includes("a.txt") },
  };
}
