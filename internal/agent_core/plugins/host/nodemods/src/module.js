// node:module —— ada-go 宿主自建的 Node 20 子集（虚拟 ES 模块，moejs 装载）。
//
// E-3 仍然是开放缺口（12 §6.11.1）：createRequire 只能「解析」已安装的 node:*，
// 无法同步返回一个 ESM 模块的命名空间——引擎的模块是链接期的产物，没有同步 require。
// 因此本模块的行为是：
//   - require("node:fs") 这类已安装内置模块 → 抛 ERR_REQUIRE_ESM，文案直接告诉调用方
//     改用 import（这是诚实的失败，不是 "Cannot find module" 的误导）；
//   - 未安装/不认识的说明符 → ERR_MODULE_NOT_FOUND；
//   - 不读磁盘、不解析 node_modules、没有 require.cache（与 6.10 一致）。
//
// 有意简化：没有 Module 的实例语义（paths/filename/parent/children），只保留
// createRequire / builtinModules / isBuiltin / syncBuiltinESMExports 这几个实用面。

const binding = import.meta.binding || {};

export const builtinModules = Array.isArray(binding.builtins) ? binding.builtins.slice() : [];

function normalize(spec) {
  if (spec.indexOf("node:") === 0) return spec.slice(5);
  return spec;
}

export function isBuiltin(spec) {
  if (typeof spec !== "string") return false;
  const name = normalize(spec);
  if (builtinModules.indexOf(name) >= 0) return true;
  // 带 node: 前缀的一律视为内置名（能不能装由链接期判定）。
  return spec.indexOf("node:") === 0;
}

function makeRequire(from) {
  const origin = typeof from === "string" ? from : "";
  const require = function require(spec) {
    if (typeof spec !== "string") {
      const e = new TypeError('The "id" argument must be of type string. Received ' + typeof spec);
      e.code = "ERR_INVALID_ARG_TYPE";
      throw e;
    }
    if (isBuiltin(spec)) {
      if (builtinModules.indexOf(normalize(spec)) < 0) {
        const e = new Error("Cannot find module '" + spec + "'");
        e.code = "ERR_MODULE_NOT_FOUND";
        throw e;
      }
      const e = new Error(
        "require('" + spec + "') 不可用：ada-go 的内置模块是 ESM，没有同步 require 面（E-3 未闭合）；" +
          "请在插件里改用 import { ... } from '" + spec + "'",
      );
      e.code = "ERR_REQUIRE_ESM";
      throw e;
    }
    const e = new Error(
      "Cannot find module '" + spec + "'" + (origin ? " from " + origin : "") + "：宿主不解析 node_modules（06 §6.10）",
    );
    e.code = "ERR_MODULE_NOT_FOUND";
    throw e;
  };
  require.resolve = function resolve(spec) {
    if (typeof spec === "string" && isBuiltin(spec) && builtinModules.indexOf(normalize(spec)) >= 0) return spec;
    const e = new Error("Cannot find module '" + String(spec) + "'");
    e.code = "ERR_MODULE_NOT_FOUND";
    throw e;
  };
  require.cache = Object.create(null);
  return require;
}

export function createRequire(filename) {
  return makeRequire(filename);
}

export function syncBuiltinESMExports() {
  // 无操作：内置模块是不可变的 ESM，没有「同步到 CJS 导出」这件事。
}

export class Module {
  constructor(id) {
    this.id = typeof id === "string" ? id : "";
    this.filename = this.id;
    this.loaded = false;
  }

  require(spec) {
    return makeRequire(this.id)(spec);
  }

  static createRequire(filename) {
    return createRequire(filename);
  }
}

Module.builtinModules = builtinModules;

export default {
  createRequire,
  builtinModules,
  isBuiltin,
  syncBuiltinESMExports,
  Module,
};
