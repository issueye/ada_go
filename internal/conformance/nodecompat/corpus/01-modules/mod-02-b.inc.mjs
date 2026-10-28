// mod-02 夹具：b 反向引用 a。
import { fromA } from "./mod-02-a.inc.mjs";

export function nameB() { return "B-of-" + fromA() }
