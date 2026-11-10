// lang-04: 关闭代码生成（设计 6.11「WithoutCodeGeneration」、6.10「eval/new Function
// 对第三方关闭」）。上游的开关位是 moejs.Options.DisableDynamicCode。
export const meta = { id: "lang-04-codegen", layer: "lang-async", title: "关闭代码生成" };

export default async function run() {
  const shape = (fn) => { try { return { ok: fn() } } catch (e) { return { name: e.name, message: String(e.message) } } };
  return {
    returns: {
      eval: shape(() => eval("1+1")),
      functionCtor: shape(() => new Function("return 2")()),
      wasm: typeof globalThis.WebAssembly,
    },
    codes: {},
    order: [],
    effects: {},
  };
}
