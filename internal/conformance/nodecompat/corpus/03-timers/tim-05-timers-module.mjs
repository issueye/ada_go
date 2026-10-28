// tim-05: node:timers 与 node:timers/promises 是否存在（设计 6.12.2、6.11.1 E-8）。
export const meta = { id: "tim-05-timers-module", layer: "timers", title: "timers 模块存在性" };

import { importState } from "../_lib/shape.inc.mjs";

export default async function run() {
  const timers = await importState("node:timers");
  const promises = await importState("node:timers/promises");
  const bareTimers = await importState("timers");
  return {
    returns: { timers, promises, bareTimers },
    codes: { timers: timers.code, promises: promises.code },
    order: [],
    effects: {},
  };
}
