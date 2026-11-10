// mod-05: CJS 表面（设计 6.11「ESM 与 CJS 双向互操作」、6.11.1 E-3）。
// Node 20 在 .mjs 里同样没有 require/module/exports，所以这一条两边都可跑；
// 真正的缺口是 moejs 连 CJS 包裹都没有，互操作只能在构建期由 esbuild 完成。
export const meta = { id: "mod-05-cjs-surface", layer: "modules", title: "CJS require 表面" };

export default async function run() {
  const requireResult = (() => {
    try { return typeof require; } catch (e) { return "throw:" + e.name }
  })();
  return {
    returns: {
      require: requireResult,
      module: typeof globalThis.module,
      exports: typeof globalThis.exports,
      dirnameGlobal: typeof globalThis.__dirname,
      builtinModules: (() => {
        try { return typeof globalThis.module?.builtinModules } catch (e) { return "throw" }
      })(),
    },
    codes: {},
    order: [],
    effects: {},
  };
}
