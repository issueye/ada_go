// bin-05: 超大分配报错（设计 6.11 资源边界、6.13 内存）。
// moejs 里这是「不可 catch 的中断」：try/catch 抓不到，Run 以 *InterruptedError 结束，
// 本用例因此不会上报（Go 侧断言 observe.reported=false + runErrContains=memory limit）。
// Node 20 给的是可捕获的 RangeError，会正常上报 —— 差异记在偏差表 D-16。
export const meta = { id: "bin-05-huge-alloc", layer: "binary", title: "超大分配" };

export default async function run() {
  let caught = "no";
  try {
    globalThis.hold = [new Uint8Array(32 * 1024 * 1024)];
  } catch (e) {
    caught = "catch:" + e.name;
  }
  return {
    returns: { caught, heldLength: globalThis.hold ? globalThis.hold[0].length : 0 },
    codes: {},
    order: [],
    effects: {},
  };
}
