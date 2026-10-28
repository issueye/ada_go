// fs-03: 作用域越界（设计 6.13：根固定为工作区，`..` 按真实落点检查；
// 6.12.1：不暴露宿主绝对路径）。
export const meta = { id: "fs-03-escape", layer: "fs", title: ".. 与宿主路径越界" };

import fs from "node:fs";
import { shapeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const dotdot = shapeOf(() => fs.readFileSync(P("../outside/secret.txt")));
  const absolute = shapeOf(() => fs.readFileSync("/etc/passwd"));
  return {
    returns: {
      dotdotCode: dotdot.code,
      dotdotPath: dotdot.path === P("../outside/secret.txt"),
      absoluteCode: absolute.code,
      absoluteSyscall: absolute.syscall,
      // 语料把宿主路径原样传进去时，错误里的 path 是否被回显（不泄露：脚本本来就有它，
      // 但设计 6.13 的口径值得记录）。
      realpathInside: fs.realpathSync(P("hello.txt")).startsWith(ctx.workspace),
    },
    codes: { dotdot: dotdot.code, absolute: absolute.code },
    order: [],
    effects: {},
  };
}
