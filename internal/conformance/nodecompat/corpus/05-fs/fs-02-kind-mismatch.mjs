// fs-02: 路径类型不符（EISDIR / ENOTDIR，设计 6.12.4）。
export const meta = { id: "fs-02-kind-mismatch", layer: "fs", title: "EISDIR 与 ENOTDIR" };

import fs from "node:fs";
import { shapeOf, codeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const eisdir = shapeOf(() => fs.readFileSync(P("dir")));
  const enotdir = shapeOf(() => fs.readFileSync(P("hello.txt/x")));
  return {
    returns: {
      eisdirCode: eisdir.code,
      eisdirSyscall: eisdir.syscall,
      eisdirErrno: eisdir.errno,
      enotdirCode: enotdir.code,
      enotdirPath: enotdir.path === P("hello.txt/x"),
      readdirOnFile: codeOf(() => fs.readdirSync(P("hello.txt"))),
      statWorks: fs.statSync(P("dir")).isDirectory(),
    },
    codes: { eisdir: eisdir.code, enotdir: enotdir.code },
    order: [],
    effects: {},
  };
}
