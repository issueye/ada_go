// node:timers —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 定时器本身就是 eventloop 装的全局（12 §6.12.2 timers 行）：本模块只是按模块形状
// 再导出**同一个函数对象**（与 Node 一致：timers.setTimeout === setTimeout），
// 这样 `clearTimeout(timers.setTimeout(...))` 与混用两边的写法都能成立。
//
// 已知偏差：eventloop 的即时阶段（immediates）与 Node 的 check 阶段不是同一件事——
// 上游按 timers→tasks→immediates 三阶段推进，setImmediate 与 0ms setTimeout 的相对
// 顺序不保证与 Node 一致（实测顺序见 conformance 语料 tim-01/tim-05）。
// timers/promises 不实现：上游没有 signal 取消与 setInterval 清理保证（E-8）。

const g = globalThis;

// bind 返回全局函数本身（保持同一性）；宿主没装这个全局时给一个诚实失败的函数，
// 而不是导出 undefined（那样调用点拿到的是 "not a function" 的误导性 TypeError）。
function bind(name) {
  const fn = g[name];
  if (typeof fn === "function") return fn;
  const missing = function () {
    const e = new Error("宿主没有安装全局 " + name + "，node:timers 不可用");
    e.code = "ERR_NOT_SUPPORTED";
    throw e;
  };
  return missing;
}

export const setTimeout = bind("setTimeout");
export const clearTimeout = bind("clearTimeout");
export const setInterval = bind("setInterval");
export const clearInterval = bind("clearInterval");
export const setImmediate = bind("setImmediate");
export const clearImmediate = bind("clearImmediate");

export default {
  setTimeout,
  clearTimeout,
  setInterval,
  clearInterval,
  setImmediate,
  clearImmediate,
};
