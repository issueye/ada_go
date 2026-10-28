// fs-08: symlink 越界按真实落点拒绝；根内的链接可用（设计 6.13）。
// 夹具由运行器铺（out.link -> 工作区外、inner.link -> 工作区内），建不出链接的
// 平台（Windows 非开发者模式）自报 skip。
export const meta = { id: "fs-08-symlink", layer: "fs", title: "符号链接落点检查" };

import fs from "node:fs";
import { shapeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const lstat = (() => { try { return fs.lstatSync(P("out.link")).isSymbolicLink() } catch (e) { return "absent" } })();
  if (lstat !== true) {
    return { skip: "运行器没能创建工作区内的符号链接（平台或权限限制），本条未验证" };
  }
  const outside = shapeOf(() => fs.readFileSync(P("out.link")));
  const realpath = shapeOf(() => fs.realpathSync(P("out.link")));
  const follow = shapeOf(() => fs.statSync(P("out.link")));
  const inside = shapeOf(() => fs.readFileSync(P("inner.link"), "utf8"));
  return {
    returns: {
      lstatIsLink: lstat,
      outsideCode: outside.code,
      realpathCode: realpath.code,
      realpathSyscall: realpath.syscall,
      statFollowCode: follow.code,
      insideValue: inside.ok ?? inside.code,
    },
    codes: { outside: outside.code, realpath: realpath.code, follow: follow.code, inside: inside.code },
    order: [],
    effects: { pathEchoed: outside.path === P("out.link") },
  };
}
