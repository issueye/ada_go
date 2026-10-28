// lang-01: microtask 先于 timer；await 不阻塞宿主（设计 6.11 Promise 与 job 行）。
export const meta = { id: "lang-01-job-order", layer: "lang-async", title: "job 顺序" };

export default async function run() {
  const log = [];
  setTimeout(() => log.push("timer0"), 0);
  queueMicrotask(() => log.push("queueMicrotask"));
  Promise.resolve().then(() => log.push("then"));
  (async () => { await null; log.push("async-body") })();
  const nested = Promise.resolve().then(() => { log.push("outer"); return 1 }).then(() => log.push("inner"));
  await new Promise((res) => setTimeout(res, 30));
  await nested;
  return {
    returns: { nested: "resolved" },
    codes: {},
    order: log,
    effects: {},
  };
}
