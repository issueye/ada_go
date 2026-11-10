// bin-03: atob/btoa 与结构化克隆（设计 6.11 二进制与编码行）。
export const meta = { id: "bin-03-atob-btoa-clone", layer: "binary", title: "atob/btoa 与 structuredClone" };

export default async function run() {
  const ta = new Uint8Array([1, 2, 3]);
  const clone = structuredClone({ ta, m: new Map([[1, "a"]]), d: new Date(0), nested: { x: [1, 2] } });
  const buf = new ArrayBuffer(8);
  const view = new Uint8Array(buf);
  let transferred = true;
  let transferErr = null;
  try { structuredClone({ buf }, { transfer: [buf] }) } catch (e) { transferred = false; transferErr = e.name }
  const cyc = { a: 1 }; cyc.self = cyc;
  return {
    returns: {
      atob: typeof atob,
      btoaRound: atob(btoa("ada")),
      btoaThrows: (() => { try { btoa("中"); return "no-throw" } catch (e) { return "throw:" + e.name } })(),
      deep: clone.ta[2] === 3 && clone.m.get(1) === "a" && clone.d.getTime() === 0 && clone.nested.x[1] === 2,
      notSameObject: clone.ta !== ta,
      transferred,
      transferErr,
      detachedLength: (() => { try { return view.length } catch (e) { return "throw:" + e.name } })(),
      cycle: (() => { try { return structuredClone(cyc).self.a } catch (e) { return "throw:" + e.name } })(),
      proxy: (() => { try { structuredClone(new Proxy({}, {})); return "no-throw" } catch (e) { return e.name } })(),
    },
    codes: {},
    order: [],
    effects: {},
  };
}
