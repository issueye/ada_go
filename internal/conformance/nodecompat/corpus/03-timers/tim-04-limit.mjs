// tim-04: 每 Runtime 定时器数量上限 + ERR_TIMER_LIMIT（设计 6.12.2 上限行、6.12.4）。
// Node 20 没有这个上限（也没有 ERR_TIMER_LIMIT），这条是为 ada-go 宿主夹 256 准备的。
export const meta = { id: "tim-04-limit", layer: "timers", title: "256 个未完成定时器" };

export default async function run() {
  let fired = 0;
  let err = "none";
  const ids = [];
  try {
    for (let i = 0; i < 600; i++) ids.push(setTimeout(() => { fired++ }, 1));
  } catch (e) {
    err = e.code || e.name;
  }
  await new Promise((res) => setTimeout(res, 150));
  for (const id of ids) clearTimeout(id);
  return {
    returns: { scheduled: ids.length, err },
    codes: { limit: err === "none" ? null : err },
    order: [],
    effects: { fired },
  };
}
