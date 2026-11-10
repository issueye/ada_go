// err-01: 错误对象的最小契约与栈映射（设计 6.12.4 末段：name/message/code 齐备，
// 栈里只出现 plugin:// 路径，不泄露宿主绝对路径）。
export const meta = { id: "err-01-shape-stack", layer: "errors", title: "错误形状与栈映射" };

import fs from "node:fs";

export default async function run(ctx) {
  let e;
  try { fs.readFileSync(ctx.workspace + "/nope.txt") } catch (x) { e = x }
  const stack = String(e.stack || "");
  const lines = stack.split("\n").slice(0, 4);
  return {
    returns: {
      hasName: e.name === "Error",
      hasMessage: typeof e.message === "string" && e.message.length > 0,
      hasCode: e.code === "ENOENT",
      isErrorInstance: e instanceof Error,
      stackFrameCount: lines.length,
      // 栈帧应当指向虚拟模块名（plugin://…），而不是宿主临时目录。
      stackMentionsVirtual: stack.indexOf("plugin://") >= 0,
      stackMentionsHost: stack.indexOf("/tmp/") >= 0 || stack.indexOf("AppData") >= 0 || /^[A-Za-z]:\\\\/m.test(stack),
    },
    codes: { code: e.code },
    order: [],
    effects: { ownNames: Object.getOwnPropertyNames(e).filter((n) => n !== "stack").sort() },
  };
}
