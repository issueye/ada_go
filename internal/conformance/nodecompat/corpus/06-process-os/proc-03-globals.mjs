// proc-03: 装上 webapi 之后的全局面（设计 6.12.2「Web 数据 API」一行）。
// 这一条在 moejs 侧是真断言：Blob/File/FormData 上游不提供，console/process 不存在。
export const meta = { id: "proc-03-globals", layer: "process-os", title: "全局数据 API 面" };

import { typeofGlobals } from "../_lib/shape.inc.mjs";

export default async function run() {
  const names = ["Buffer", "process", "console", "URL", "URLSearchParams", "fetch", "Headers", "Request",
    "Response", "AbortController", "AbortSignal", "EventTarget", "Event", "DOMException", "performance",
    "TextEncoder", "TextDecoder", "structuredClone", "queueMicrotask", "atob", "btoa", "ReadableStream",
    "WritableStream", "TransformStream", "Blob", "File", "FormData", "crypto", "Worker"];
  const perf = (() => { try { return [typeof performance.now, typeof performance.timeOrigin].join(",") } catch (e) { return "absent" } })();
  return {
    returns: { globals: typeofGlobals(names), performanceShape: perf },
    codes: {},
    order: [],
    effects: {},
  };
}
