// fs-06: 只读安装时写成员的实际行为（设计 6.12.1(2)、6.13：应「访问即抛
// ERR_ACCESS_DENIED」；上游 alpha.11 是「存在 + 调用抛 EROFS」，D-7 据此把
// 宿主裁剪退成「调用即抛」）。
export const meta = { id: "fs-06-readonly-write", layer: "fs", title: "只读时的写成员" };

import fs from "node:fs";
import fsp from "node:fs/promises";
import { shapeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const d = Object.getOwnPropertyDescriptor(fs, "writeFile");
  const call = shapeOf(() => fs.writeFileSync(P("new.txt"), "x"));
  const mkdir = shapeOf(() => fs.mkdirSync(P("new-dir")));
  const readStillWorks = fs.readFileSync(P("hello.txt"), "utf8");
  return {
    returns: {
      writeFileMemberType: typeof fs.writeFile,
      descriptor: { writable: Boolean(d && d.writable), configurable: Boolean(d && d.configurable), enumerable: Boolean(d && d.enumerable), isDataProperty: Boolean(d && "value" in d) },
      readStillWorks,
      promisesWriteFileMemberType: typeof fsp.writeFile,
    },
    codes: { writeFile: call.code, mkdir: mkdir.code, appendFile: shapeOf(() => fs.appendFileSync(P("hello.txt"), "x")).code },
    order: [],
    effects: {
      errnoIsNegative: typeof call.errno === "number" && call.errno < 0,
      syscall: call.syscall,
      pathIsInput: call.path === P("new.txt"),
    },
  };
}
