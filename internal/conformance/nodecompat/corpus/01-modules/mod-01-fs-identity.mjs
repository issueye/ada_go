// mod-01: 裸名 fs 与 node:fs 是否同一实例（设计 6.12.3、6.11 模块策略行）。
export const meta = { id: "mod-01-fs-identity", layer: "modules", title: "fs 与 node:fs 同一实例" };

import dflt from "fs";
import named, { readFile, readFileSync } from "node:fs";
import promisesDefault from "node:fs/promises";
import * as ns from "node:fs";

export default async function run(ctx) {
  const dyn = await import("node:fs");
  return {
    returns: {
      defaultSame: dflt === named,
      namedSame: readFile === dflt.readFile,
      nsDefaultSame: ns.default === dflt,
      nsNamedSame: ns.readFile === readFileSync,
      promisesSame: promisesDefault === dflt.promises,
      dynamicDefaultSame: dyn.default === dflt,
      dynamicNamedSame: dyn.readFile === readFile,
      readFileSyncType: typeof dflt.readFileSync,
    },
    codes: {},
    order: [],
    effects: {},
  };
}
