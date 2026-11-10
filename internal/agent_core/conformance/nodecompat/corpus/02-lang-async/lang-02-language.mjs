// lang-02: Node 20 常用语法面（设计 6.11 语言与内建行）。
export const meta = { id: "lang-02-language", layer: "lang-async", title: "语言与内建覆盖" };

class Counter {
  #n = 0;
  inc() { this.#n++; return this.#n }
  static { Counter.stamped = true }
}

export default async function run() {
  const log = [];
  const p = new Proxy({}, { get: (t, k) => (k in t ? t[k] : "proxy-" + String(k)) });
  const rev = Reflect.ownKeys({ a: 1 });
  const gen = function* () { yield 1; yield 2 };
  const agen = async function* () { yield "a"; yield "b" };
  const got = [];
  for (const x of gen()) got.push(x);
  for await (const x of agen()) got.push(x);
  const map = new Map([[1, "one"]]);
  const set = new Set([1, 1, 2]);
  const wm = new WeakMap(); const holder = {}; wm.set(holder, 1);
  const ws = new WeakSet(); ws.add(holder);
  log.push("step-1", "step-2");
  return {
    returns: {
      privateField: new Counter().inc(),
      classStatic: Boolean(Counter.stamped),
      destructure: (({ a, b = 2 }, [c]) => a + b + c)( { a: 1 }, [3] ),
      optionalChain: ({}?.a?.b) ?? "fallback",
      nullish: (0 ?? "x") + "/" + (null ?? "y"),
      proxyGet: p.missing,
      reflectKeys: rev.length,
      iteration: got.join(","),
      mapSize: map.size + set.size + wm.get(holder) + (ws.has(holder) ? 1 : 0),
      bigInt: String(10n ** 20n * 2n),
      symbol: typeof Symbol.asyncIterator,
      spread: [...new Set([1, 2, 2])].join("+"),
      templateTag: ((s, ...v) => s.raw.length + ":" + v.join("|"))`a${1}b${2}c`,
    },
    codes: {},
    order: log,
    effects: {},
  };
}
