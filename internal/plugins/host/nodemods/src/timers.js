// node:timers —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 定时器本身就是 eventloop 装的全局（12 §6.12.2 timers 行）：
// 这里只是把它们按模块形状再导出，并给 setImmediate 一个 0ms 定时器实现
// （矩阵已声明偏差：与 Node 的 check 阶段顺序不完全相同）。
//
// timers/promises 不实现：上游没有 signal 取消与 setInterval 清理保证（E-8），
// v1 明确不安装（见 internal/plugins/host/node/matrix.go 的 timers/promises 行）。

const g = globalThis;

function need(name, fn) {
  if (typeof fn !== "function") {
    const e = new Error("宿主没有安装全局 " + name + "，node:timers 不可用");
    e.code = "ERR_NOT_SUPPORTED";
    throw e;
  }
  return fn;
}

export function setTimeout(handler, delay, ...args) {
  return need("setTimeout", g.setTimeout)(handler, delay, ...args);
}

export function clearTimeout(id) {
  return need("clearTimeout", g.clearTimeout)(id);
}

export function setInterval(handler, delay, ...args) {
  return need("setInterval", g.setInterval)(handler, delay, ...args);
}

export function clearInterval(id) {
  return need("clearInterval", g.clearInterval)(id);
}

// setImmediate：用 0ms 定时器实现（矩阵里的已知偏差）。
export function setImmediate(handler, ...args) {
  if (typeof handler !== "function") {
    const e = new TypeError("The \"callback\" argument must be of type function");
    e.code = "ERR_INVALID_ARG_TYPE";
    throw e;
  }
  return need("setTimeout", g.setTimeout)(handler, 0, ...args);
}

export function clearImmediate(id) {
  return need("clearTimeout", g.clearTimeout)(id);
}

export default {
  setTimeout,
  clearTimeout,
  setInterval,
  clearInterval,
  setImmediate,
  clearImmediate,
};
