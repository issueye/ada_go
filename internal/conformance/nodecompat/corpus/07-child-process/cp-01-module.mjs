// cp-01: node:child_process 的存在性与「第二条命令面」（设计 6.12.2 child_process 行、E-5）。
// 上游 alpha.11 没有这个模块：spawn/fork/exec 是「不存在」而不是「存在但能绕过」。
export const meta = { id: "cp-01-module", layer: "child-process", title: "child_process 模块存在性" };

import { importState } from "../_lib/shape.inc.mjs";

export default async function run() {
  const cp = await importState("node:child_process");
  const workerThreads = await importState("node:worker_threads");
  let members = null;
  if (cp.state === "ok") {
    const m = await import("node:child_process");
    members = {
      execFileType: typeof m.execFile,
      execFileSyncType: typeof m.execFileSync,
      forbiddenPresent: ["spawn", "fork", "exec", "execSync", "spawnSync"].filter((n) => typeof m[n] === "function"),
    };
  }
  return {
    returns: { child_process: cp, workerThreads, members },
    codes: { child_process: cp.code, workerThreads: workerThreads.code },
    order: [],
    effects: {},
  };
}
