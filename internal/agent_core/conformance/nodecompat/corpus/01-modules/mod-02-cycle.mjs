// mod-02: 循环依赖不挂起（设计 6.11 模块系统行）。
export const meta = { id: "mod-02-cycle", layer: "modules", title: "循环依赖不挂起" };

import { fromA, valueB } from "./mod-02-a.inc.mjs";

export default async function run() {
  return {
    returns: { fromA: fromA(), valueB: valueB(), aType: typeof fromA },
    codes: {},
    order: [],
    effects: {},
  };
}
