// err-04: 只有 ada-go 宿主才会产生的错误码（设计 6.12.4 的 ERR_ACCESS_DENIED /
// ERR_TIMER_LIMIT / ERR_FS_FILE_TOO_LARGE / ERR_CHILD_PROCESS_STDIO_MAXBUFFER /
// ERR_NATIVE_ADDON_UNSUPPORTED / ERR_MODULE_NOT_FOUND 的 JS 层形状）。
// M1-C 的宿主安装层没落地之前跑不出来 → decision=skip-host。
export const meta = { id: "err-04-host-codes", layer: "errors", title: "宿主侧错误码" };

import fs from "node:fs";
import { codeOf } from "../_lib/shape.inc.mjs";

export default async function run(ctx) {
  const P = (x) => ctx.workspace + "/" + x;
  return {
    returns: {
      accessDeniedOnReadonlyWrite: codeOf(() => fs.writeFileSync(P("new.txt"), "x")),
      addonUnsupported: (() => { try { return typeof process.dlopen } catch (e) { return "no-process" } })(),
      moduleNotFoundCode: await import("node:does-not-exist").then(() => null, (e) => e.code),
      fsTooLarge: null,
      timerLimit: null,
      maxBuffer: null,
    },
    codes: {
      accessDenied: codeOf(() => fs.writeFileSync(P("new.txt"), "x")),
      moduleNotFound: (() => { try { return null } catch (e) { return e.code } })(),
    },
    order: [],
    effects: {},
  };
}
