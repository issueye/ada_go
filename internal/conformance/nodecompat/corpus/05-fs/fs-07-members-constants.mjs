// fs-07: 成员清单、fs.constants 与「未实现但 typeof 是 function」的占位成员
// （设计 6.12.2 fs 行的兼容范围与偏差列）。
export const meta = { id: "fs-07-members-constants", layer: "fs", title: "成员面与 constants" };

import fs from "node:fs";
import fsp from "node:fs/promises";
import { codeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const matrixListed = ["readFile", "writeFile", "appendFile", "readdir", "stat", "lstat", "mkdir", "rm", "rename", "copyFile", "realpath", "readlink", "existsSync"];
  const promisedV1 = ["watch", "open", "close", "read", "write", "chmod", "chown", "utimes"];
  return {
    returns: {
      listedAllPresent: matrixListed.map((n) => n + ":" + typeof fs[n]).join(","),
      v1ExcludedButPresent: promisedV1.filter((n) => typeof fs[n] === "function"),
      extraMembers: ["glob", "globSync", "cp", "cpSync", "access", "exists", "opendir"].filter((n) => typeof fs[n] === "function"),
      memberCount: Object.getOwnPropertyNames(fs).length,
      promisesMemberCount: Object.keys(fsp).length,
      constants: { F_OK: fs.constants.F_OK, O_RDONLY: fs.constants.O_RDONLY, O_WRONLY: fs.constants.O_WRONLY, O_APPEND: fs.constants.O_APPEND },
      createReadStreamType: typeof fs.createReadStream,
      statsFile: fs.statSync(P("hello.txt")).isFile(),
      statsIsBuffered: (() => { const s = fs.statSync(P("hello.txt")); return [typeof s.size, typeof s.mtimeMs, typeof s.ino].join(",") })(),
    },
    codes: {
      // 上游给的是抛 ERR_METHOD_NOT_IMPLEMENTED 的占位函数；真 Node 会真的建流，
      // 所以这里立刻挂 error 监听并销毁，避免运行器删夹具之后炸出未处理的 error 事件。
      createReadStreamCall: (() => {
        try {
          const s = fs.createReadStream(P("hello.txt"));
          s.on("error", () => {});
          if (typeof s.destroy === "function") s.destroy();
          return null;
        } catch (e) { return e.code || e.name }
      })(),
      openSyncCall: codeOf(() => fs.openSync(P("hello.txt"), "r")),
    },
    order: [],
    effects: {},
  };
}
