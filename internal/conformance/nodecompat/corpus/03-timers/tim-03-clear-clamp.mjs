// tim-03: 清除定时器 + 毫秒截断与 32 位上限（设计 6.11、6.12.2「返回数字 id」）。
export const meta = { id: "tim-03-clear-clamp", layer: "timers", title: "清除与毫秒截断" };

export default async function run() {
  const cleared = [];
  const a = setTimeout(() => cleared.push("a"), 10);
  const b = setTimeout(() => cleared.push("b"), 10);
  clearTimeout(a);
  clearTimeout(Number(b));
  clearTimeout(" nonsense ");
  clearImmediate(7);
  clearInterval(undefined);

  const clamped = [];
  setTimeout(() => clamped.push("nan"), NaN);
  setTimeout(() => clamped.push("huge"), 2147483648);
  setTimeout(() => clamped.push("negative"), -5);
  setTimeout(() => clamped.push("fraction"), 1.7);
  const intervalId = setInterval(() => {}, 1000);
  await new Promise((res) => setTimeout(res, 60));
  clearInterval(intervalId);
  return {
    returns: { intervalIdType: typeof intervalId },
    codes: {},
    order: clamped,
    effects: { cleared },
  };
}
