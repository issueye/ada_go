// mod-04: import.meta 的虚拟路径与 resolve（设计 6.11 模块系统、6.12.3）。
export const meta = { id: "mod-04-import-meta", layer: "modules", title: "import.meta 形状" };

export default async function run() {
  const resolve = (() => {
    try { return typeof import.meta.resolve; } catch (e) { return "throw:" + e.name }
  })();
  return {
    returns: {
      url: typeof import.meta.url,
      filename: typeof import.meta.filename,
      dirname: typeof import.meta.dirname,
      resolve,
      ownCount: Object.getOwnPropertyNames(import.meta).length,
      protoNull: Object.getPrototypeOf(import.meta) === null,
    },
    codes: {},
    order: [],
    effects: {},
  };
}
