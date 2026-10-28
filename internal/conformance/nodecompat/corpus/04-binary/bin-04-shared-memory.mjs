// bin-04: SharedArrayBuffer/Atomics 与 DataView 越界（设计 6.11「插件 Runtime 不提供
// SharedArrayBuffer/Atomics」这一行的实测）。
export const meta = { id: "bin-04-shared-memory", layer: "binary", title: "共享内存与 DataView" };

export default async function run() {
  const dv = new DataView(new ArrayBuffer(4));
  dv.setUint32(0, 0xdeadbeef);
  return {
    returns: {
      sharedArrayBuffer: typeof SharedArrayBuffer,
      atomics: typeof Atomics,
      atomicsAdd: (() => {
        try { return String(Atomics.add(new Int32Array(new SharedArrayBuffer(4)), 0, 1)) } catch (e) { return "throw:" + e.name }
      })(),
      worker: typeof Worker,
      dataview: dv.getUint32(0).toString(16),
      outOfRange: (() => { try { dv.getUint8(9); return "no-throw" } catch (e) { return "throw:" + e.name } })(),
      maxTypedArray: (() => { try { return String(new Uint8Array(2 ** 31).length) } catch (e) { return "throw:" + e.name } })(),
    },
    codes: {},
    order: [],
    effects: {},
  };
}
