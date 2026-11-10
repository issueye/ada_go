// tim-02: unref 后不阻止 Runtime 结束（设计 6.11 定时器行、6.11.1 E-2）。
// 用例只排一个 unref 的长定时器就返回：事件循环若不被 unref 阻止，回调不会跑，
// effects.log 必须为空；否则说明 unref 语义没生效。
export const meta = { id: "tim-02-unref", layer: "timers", title: "unref 不阻止结束" };

export default async function run() {
  const log = [];
  const t = setTimeout(() => log.push("unref-fired"), 250);
  const api = { hasUnref: typeof t.unref, hasRef: typeof t.ref, hasRefresh: typeof t.refresh };
  t.unref();
  return {
    returns: {
      ...api,
      hasRefAfterUnref: typeof t.hasRef === "function" ? t.hasRef() : "no-hasRef",
      idIsNumber: Number.isFinite(Number(t)),
      idType: typeof t,
    },
    codes: {},
    order: [],
    effects: { log },
  };
}
