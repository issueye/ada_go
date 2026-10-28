// fs-05: promise 与回调两种形状（设计 6.12.2「同步与 promise 两种形状；不提供回调形式」）。
export const meta = { id: "fs-05-promises-callback", layer: "fs", title: "promise 形状与回调形式" };

import fs from "node:fs";
import fsp from "node:fs/promises";
import { awaitCodeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  const order = [];
  const written = await fsp.writeFile(P("p1.txt"), "promise-write"); order.push("write");
  const read = await fsp.readFile(P("p1.txt"), "utf8"); order.push("read");
  const stat = await fsp.stat(P("p1.txt")); order.push("stat");
  await fsp.mkdir(P("pd")); order.push("mkdir");
  await fsp.writeFile(P("pd/x.txt"), "x"); order.push("write-in-dir");
  const dir = (await fsp.readdir(P("pd"))).join(","); order.push("readdir");
  await fsp.rm(P("pd"), { recursive: true, force: true }); order.push("rm");
  const cbErr = "none";
  const cbData = await new Promise((res) => {
    try {
      fs.readFile(P("p1.txt"), "utf8", (err, data) => res(err ? "err:" + err.code : data));
    } catch (e) {
      res("throw:" + (e.code || e.name));
    }
  });
  order.push("callback");
  return {
    returns: {
      writeIsUndefined: written === undefined,
      read,
      sizeIsThirteen: Number(stat.size) === 13,
      dir,
      cbData,
      cbErr,
      promisesOnFs: fs.promises === fsp,
      callbackFormType: typeof fs.readFile,
      readFileArity: fs.readFile.length,
    },
    codes: { missingPromise: await awaitCodeOf(() => fsp.readFile(P("nope.txt"))) },
    order,
    effects: {},
  };
}
