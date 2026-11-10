// tim-06: AbortSignal 传进 setTimeout 必须取消，而不是静默失效
// （设计 6.11 取消行、6.11.1 E-8）。
export const meta = { id: "tim-06-signal-cancel", layer: "timers", title: "定时器的 signal 取消" };

export default async function run() {
  const ac = new AbortController();
  ac.abort(new Error("轮次取消"));
  let fired = "no";
  let rejected = null;
  try {
    setTimeout(() => { fired = "YES-仍然触发" }, 20, { signal: ac.signal });
  } catch (e) {
    rejected = e.code || e.name;
  }
  await new Promise((res) => setTimeout(res, 80));
  return {
    returns: { fired, rejected },
    codes: { signal: rejected },
    order: [],
    effects: {},
  };
}
