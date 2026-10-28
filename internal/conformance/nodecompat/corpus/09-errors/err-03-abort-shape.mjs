// err-03: AbortSignal 取消的错误形状（设计 6.12.4 ABORT_ERR / name=AbortError）。
export const meta = { id: "err-03-abort-shape", layer: "errors", title: "取消的错误形状" };

export default async function run() {
  const ac = new AbortController();
  ac.abort(new Error("轮次取消"));
  const s = ac.signal;
  const caught = (() => { try { s.throwIfAborted(); return null } catch (e) { return { name: e.name, code: e.code ?? null, message: e.message, isDOM: e instanceof DOMException } } })();
  const timeoutCode = await new Promise((res) => {
    const t = AbortSignal.timeout(10);
    t.addEventListener("abort", () => {
      const e = t.reason;
      res({ name: e && e.name, code: (e && e.code) ?? null })
    });
    setTimeout(() => res({ name: "never" }), 300);
  });
  return {
    returns: {
      aborted: s.aborted,
      reasonMessage: s.reason && s.reason.message,
      throwIfAborted: caught,
      timeoutReason: timeoutCode,
      anyComposed: (() => {
        const a = new AbortController(); const b = new AbortController();
        const comp = AbortSignal.any([a.signal, b.signal]);
        b.abort(new Error("第二个"));
        return [comp.aborted, comp.reason && comp.reason.message];
      })(),
    },
    codes: { abort: caught && caught.code, timeout: timeoutCode.code },
    order: [],
    effects: {},
  };
}
