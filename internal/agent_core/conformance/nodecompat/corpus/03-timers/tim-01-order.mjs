// tim-01: 定时器与循环的相对顺序（设计 6.12.2「setImmediate 与 Node 的 check 阶段
// 顺序不完全相同」这一行的实测）。
export const meta = { id: "tim-01-order", layer: "timers", title: "microtask/immediate/timeout 顺序" };

export default async function run() {
  const log = [];
  setTimeout(() => log.push("timeout0"), 0);
  setImmediate(() => log.push("immediate"));
  queueMicrotask(() => log.push("microtask"));
  Promise.resolve().then(() => log.push("promise"));
  await new Promise((res) => setTimeout(res, 40));
  return {
    returns: { immediateType: typeof setImmediate, clearImmediateType: typeof clearImmediate },
    codes: {},
    order: log,
    effects: {},
  };
}
