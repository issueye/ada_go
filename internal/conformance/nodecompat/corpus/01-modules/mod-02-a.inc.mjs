// mod-02 夹具：a ↔ b 互相引用（函数互调，避免 TDZ 差异）。
import { nameB } from "./mod-02-b.inc.mjs";

export function fromA() { return "A" }
export function valueB() { return nameB() }
