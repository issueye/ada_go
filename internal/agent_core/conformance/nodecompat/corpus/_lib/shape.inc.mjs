// 语料共享片段（不是用例，无 .expect.json）。
// 只放取样助手：任何 ada-go 专有的判断都不许写在这里。

export const meta = { lib: true };

// codeOf：跑一个同步函数，返回它的错误 code（没有 code 就退回 name），成功返回 null。
export const codeOf = (fn) => {
  try { fn(); return null } catch (e) { return e && e.code !== undefined ? String(e.code) : String(e && e.name) }
};

// shapeOf：抽错误对象的全部可比较字段（设计 6.12.4 要求 code/errno/syscall/path 齐全）。
export const shapeOf = (fn) => {
  try { const v = fn(); return { ok: v === undefined ? "undefined" : v } } catch (e) {
    if (!e || typeof e !== "object") return { thrown: String(e) };
    return {
      name: e.name ?? null,
      code: e.code ?? null,
      errno: e.errno ?? null,
      syscall: e.syscall ?? null,
      path: e.path ?? null,
      message: typeof e.message === "string" ? e.message : null,
      ownNames: Object.getOwnPropertyNames(e).filter((n) => n !== "stack").sort(),
      stackIsString: typeof e.stack === "string",
    };
  }
};

// awaitCodeOf：跑一个返回 Promise 的函数，resolve 给 null，reject 给 code。
export const awaitCodeOf = async (fn) => {
  try { await fn(); return null } catch (e) { return e && e.code !== undefined ? String(e.code) : String(e && e.name) }
};

// importState：动态 import 的存在性探针（不抛错，返回 "ok" 或 code/消息名）。
// 两侧都补齐键，免得 JSON.stringify 丢掉 undefined 让「键缺失」被误读成差异。
export const importState = async (spec) => {
  try {
    const m = await import(spec);
    return { state: "ok", keys: Object.keys(m).length, code: null, name: null, message: null }
  } catch (e) {
    return { state: "absent", code: (e && e.code) ?? null, name: (e && e.name) ?? null, message: String((e && e.message) ?? e) }
  }
};

// typeofGlobals：一批全局名的 typeof，按名字典序，便于跨引擎比较。
export const typeofGlobals = (names) => {
  const out = {};
  for (const n of [...names].sort()) out[n] = typeof globalThis[n];
  return out;
};

// sorted：稳定的列目录结果（目录顺序不是契约，内容才是）。
export const sorted = (xs) => [...xs].sort();
