// node:console —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 输出不写进程 stdout：一律经 import.meta.binding.write(level, text) 交给宿主结构化日志
// （12 §6.12.2 console 行：映射到结构化日志；不写进程 stdout）。
//
// 有意简化：
//   - 参数格式化用本文件里的简易 inspect（JSON 优先），不做 Node util.inspect 的
//     深度/宽度/颜色语义；长文本由宿主按上限截断（这里先截到 8KB）；
//   - 没有 Console 类、没有 Console 构造的 stdout/stderr 注入、没有 group 缩进树；
//   - count/countReset/time/timeEnd 只维护本对象内的表，且 name 重名时不报错。

const binding = import.meta.binding;
const MAX_TEXT = 8192;

const counters = new Map();
const timers = new Map();

function quote(s) {
  return "'" + s.replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/\n/g, "\\n") + "'";
}

function inspect(value, depth, seen) {
  if (value === null) return "null";
  if (value === undefined) return "undefined";
  const t = typeof value;
  if (t === "string") return quote(value);
  if (t === "number" || t === "boolean" || t === "bigint") return String(value);
  if (t === "symbol") return value.toString();
  if (t === "function") return "[Function: " + (value.name || "(anonymous)") + "]";
  if (depth <= 0) return Array.isArray(value) ? "[Array]" : "[Object]";
  if (seen.indexOf(value) >= 0) return "[Circular]";
  seen.push(value);
  try {
    if (Array.isArray(value)) {
      const parts = [];
      for (let i = 0; i < value.length && i < 100; i++) parts.push(inspect(value[i], depth - 1, seen));
      if (value.length > 100) parts.push("... " + (value.length - 100) + " more items");
      return parts.length === 0 ? "[]" : "[ " + parts.join(", ") + " ]";
    }
    if (value instanceof Date) return value.toISOString();
    if (value instanceof RegExp) return value.toString();
    if (value instanceof Error) return value.stack || (value.name + ": " + value.message);
    if (value instanceof Map) {
      const parts = [];
      value.forEach((v, k) => parts.push(inspect(k, depth - 1, seen) + " => " + inspect(v, depth - 1, seen)));
      return "Map(" + value.size + ") { " + parts.join(", ") + " }";
    }
    if (value instanceof Set) {
      const parts = [];
      value.forEach((v) => parts.push(inspect(v, depth - 1, seen)));
      return "Set(" + value.size + ") { " + parts.join(", ") + " }";
    }
    const keys = Object.keys(value);
    if (keys.length === 0) {
      const ctor = value.constructor && value.constructor.name;
      return ctor && ctor !== "Object" ? ctor + " {}" : "{}";
    }
    const parts = [];
    for (const key of keys.slice(0, 100)) parts.push(key + ": " + inspect(value[key], depth - 1, seen));
    return "{ " + parts.join(", ") + " }";
  } finally {
    seen.pop();
  }
}

// format 粗略模拟 console 的 util.format：支持 %s %d %i %f %j %o %O %%，其余按空格拼接。
function format(args) {
  if (args.length === 0) return "";
  const first = args[0];
  let i = 1;
  let out = "";
  if (typeof first === "string") {
    out = first.replace(/%[sdifjoO%]/g, (m) => {
      if (m === "%%") return "%";
      if (i >= args.length) return m;
      const v = args[i++];
      switch (m) {
        case "%s":
          return typeof v === "string" ? v : inspect(v, 2, []);
        case "%d":
        case "%i":
          return typeof v === "number" ? String(Math.trunc(v)) : String(Number(v));
        case "%f":
          return String(Number(v));
        case "%j":
          try {
            return JSON.stringify(v);
          } catch (e) {
            return "[Circular]";
          }
        default:
          return inspect(v, 2, []);
      }
    });
  } else {
    out = inspect(first, 2, []);
  }
  for (; i < args.length; i++) out += " " + (typeof args[i] === "string" ? args[i] : inspect(args[i], 2, []));
  if (out.length > MAX_TEXT) out = out.slice(0, MAX_TEXT) + "... (" + (out.length - MAX_TEXT) + " more characters)";
  return out;
}

function emit(level, args) {
  const text = format(args);
  if (binding && typeof binding.write === "function") binding.write(level, text);
}

const console_ = {
  log: (...args) => emit("info", args),
  info: (...args) => emit("info", args),
  debug: (...args) => emit("debug", args),
  warn: (...args) => emit("warn", args),
  error: (...args) => emit("error", args),
  trace: (...args) => emit("error", args),
  dir: (value) => emit("info", [inspect(value, 4, [])]),
  table: (value) => emit("info", [inspect(value, 3, [])]),
  assert: (cond, ...args) => {
    if (!cond) emit("error", ["Assertion failed"].concat(args));
  },
  group: () => {},
  groupCollapsed: () => {},
  groupEnd: () => {},
  count: (label) => {
    const key = String(label === undefined ? "default" : label);
    const next = (counters.get(key) || 0) + 1;
    counters.set(key, next);
    emit("info", [key + ": " + next]);
  },
  countReset: (label) => {
    counters.delete(String(label === undefined ? "default" : label));
  },
  time: (label) => {
    timers.set(String(label === undefined ? "default" : label), Date.now());
  },
  timeEnd: (label) => {
    const key = String(label === undefined ? "default" : label);
    const started = timers.get(key);
    timers.delete(key);
    if (started !== undefined) emit("info", [key + ": " + (Date.now() - started) + "ms"]);
  },
  timeLog: (label, ...args) => {
    const key = String(label === undefined ? "default" : label);
    const started = timers.get(key);
    if (started !== undefined) emit("info", [key + ": " + (Date.now() - started) + "ms"].concat(args));
  },
};

export const log = console_.log;
export const info = console_.info;
export const debug = console_.debug;
export const warn = console_.warn;
export const error = console_.error;
export const trace = console_.trace;
export const dir = console_.dir;
export const table = console_.table;
export const assert = console_.assert;
export const count = console_.count;
export const countReset = console_.countReset;
export const time = console_.time;
export const timeEnd = console_.timeEnd;
export const timeLog = console_.timeLog;
export const group = console_.group;
export const groupCollapsed = console_.groupCollapsed;
export const groupEnd = console_.groupEnd;
export default console_;
