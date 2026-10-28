// node:os —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// 形状来自宿主的前置数据（import.meta.binding），不读宿主文件系统：
//   - platform/arch/type/release/eol/endianness/cpus/totalmem/freemem/tmpdir 由宿主给；
//   - hostname()/homedir() 恒为空串，userInfo()/networkInterfaces()/loadavg() 不存在
//     （12 §6.12.2 os 行：「hostname/homedir 为空；没有 networkInterfaces/userInfo/loadavg」）。
//
// tmpdir 返回虚拟路径 "/tmp" 而不是宿主临时目录：宿主绝对路径不进脚本（12 §6.12.1）。

const binding = import.meta.binding || {};

const UNKNOWN = "";

export const EOL = binding.eol === undefined ? "\n" : binding.eol;
export const devNull = "/dev/null";

export function platform() {
  return binding.platform || UNKNOWN;
}

export function arch() {
  return binding.arch || UNKNOWN;
}

export function type() {
  return binding.type || UNKNOWN;
}

export function release() {
  return binding.release || UNKNOWN;
}

export function version() {
  return binding.version || UNKNOWN;
}

export function endianness() {
  return binding.endianness || "LE";
}

export function hostname() {
  // 有意为空：宿主名不暴露给插件。
  return "";
}

export function homedir() {
  // 有意为空：工作区之外没有可见的「家目录」。
  return "";
}

export function tmpdir() {
  return "/tmp";
}

export function totalmem() {
  return typeof binding.totalmem === "number" ? binding.totalmem : 0;
}

export function freemem() {
  return typeof binding.freemem === "number" ? binding.freemem : 0;
}

export function uptime() {
  return 0;
}

export function loadavg() {
  return [0, 0, 0];
}

// cpus 返回 n 份占位 CPU：模型、频率与时间片由宿主固定，避免插件做时间侧信道。
export function cpus() {
  const n = typeof binding.cpus === "number" && binding.cpus > 0 ? binding.cpus : 1;
  const out = [];
  for (let i = 0; i < n; i++) {
    out.push({
      model: "ada-go virtual cpu",
      speed: 0,
      times: { user: 0, nice: 0, sys: 0, idle: 0, irq: 0 },
    });
  }
  return out;
}

export function availableParallelism() {
  const n = typeof binding.cpus === "number" && binding.cpus > 0 ? binding.cpus : 1;
  return n;
}

export function networkInterfaces() {
  // 不存在（不是返回空对象）：调用即抛，避免把「没有网络面」误读成「没有网卡」。
  const e = new Error("os.networkInterfaces 不提供：插件没有网络栈（12 §6.12.2）");
  e.code = "ERR_NOT_SUPPORTED";
  throw e;
}

export function userInfo() {
  const e = new Error("os.userInfo 不提供：宿主用户信息不进脚本（12 §6.12.2）");
  e.code = "ERR_NOT_SUPPORTED";
  throw e;
}

export default {
  EOL,
  devNull,
  platform,
  arch,
  type,
  release,
  version,
  endianness,
  hostname,
  homedir,
  tmpdir,
  totalmem,
  freemem,
  uptime,
  loadavg,
  cpus,
  availableParallelism,
  networkInterfaces,
  userInfo,
};
