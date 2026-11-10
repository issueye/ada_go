// proc-01: process 的只读子集（设计 6.12.2 process 行、6.13 环境变量白名单）。
// 上游 alpha.11 根本没有 process（既没有全局也没有 node:process 模块），
// 这一整行都是 ada-go 宿主 M1-C 的活，所以本条在 moejs 侧 decision=skip-host。
export const meta = { id: "proc-01-process", layer: "process-os", title: "process 只读子集" };

import { importState } from "../_lib/shape.inc.mjs";

export default async function run() {
  const mod = await importState("node:process");
  const p = globalThis.process;
  const out = { globalProcess: typeof p, module: mod };
  if (p) {
    out.argv = Array.isArray(p.argv);
    out.envKeys = Object.keys(p.env).sort();
    out.cwd = p.cwd();
    out.version = p.version;
    out.versionsNode = p.versions && p.versions.node;
    out.pidType = typeof p.pid;
    out.hrtimeType = typeof p.hrtime;
    out.nextTickType = typeof p.nextTick;
    out.exitThrows = (() => { try { p.exit(0); return "no-throw" } catch (e) { return "throw:" + (e.code || e.name) } })();
    out.missingMembers = ["kill", "dlopen", "binding", "setuid", "umask"].filter((n) => typeof p[n] === "function");
  }
  return { returns: out, codes: {}, order: [], effects: {} };
}
