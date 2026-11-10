// err-02: 参数类型/范围错误的 name 与 code（设计 6.12.4 ERR_INVALID_ARG_TYPE /
// ERR_OUT_OF_RANGE）。
export const meta = { id: "err-02-arg-type", layer: "errors", title: "参数类型与范围错误" };

import fs from "node:fs";
import { shapeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  return {
    returns: {
      nonStringPath: (() => { const s = shapeOf(() => fs.readFileSync(12345)); return [s.name, s.code] })(),
      badEncoding: (() => { const s = shapeOf(() => fs.readFileSync(P("hello.txt"), "no-such-encoding")); return [s.name, s.code] })(),
      negativeBuffer: (() => { const s = shapeOf(() => Buffer.alloc(-1)); return [s.name, s.code] })(),
      badJoinArgs: (() => { const s = shapeOf(() => fs.readFileSync({})); return [s.name, s.code] })(),
      errorCause: (() => { const e = new Error("外层", { cause: new Error("内层") }); return [e.cause && e.cause.message] })(),
      aggregate: (() => { const a = new AggregateError([new Error("x"), new Error("y")], "聚合"); return [a.name, a.errors.length, a.message] })(),
      domException: (() => { try { return typeof DOMException } catch (e) { return "absent" } })(),
    },
    codes: {},
    order: [],
    effects: {},
  };
}
