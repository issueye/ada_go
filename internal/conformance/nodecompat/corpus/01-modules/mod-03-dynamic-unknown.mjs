// mod-03: 动态 import 未安装/未知 specifier 必须 reject，不得返回 undefined 命名空间
// （设计 6.11 模块策略、6.12.4 ERR_MODULE_NOT_FOUND）。
export const meta = { id: "mod-03-dynamic-unknown", layer: "modules", title: "动态 import 未知 specifier" };

import { importState } from "../_lib/shape.inc.mjs";

export default async function run() {
  const vm = await importState("node:vm");
  const net = await importState("node:net");
  const events = await importState("events");
  const missing = await importState("./mod-03-does-not-exist.mjs");
  return {
    returns: { vm, net, events, missing },
    codes: { vm: vm.code, net: net.code, events: events.code, missing: missing.code },
    order: [],
    effects: {},
  };
}
