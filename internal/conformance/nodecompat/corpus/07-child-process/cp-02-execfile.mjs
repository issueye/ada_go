// cp-02: execFile 的全部 E-5 语义（设计 6.13 命令行）。ada-go 自建模块，M1-C 才落地，
// 所以 moejs 侧 decision=skip-host；本条的主要读者是真 Node 20（对跑基准）。
export const meta = { id: "cp-02-execfile", layer: "child-process", title: "execFile 语义" };

import os from "node:os";
import path from "node:path";

const isWin = process.platform === "win32";
const ECHO = isWin ? "cmd.exe" : "/bin/echo";
const ECHO_ARGS = isWin ? ["/c", "echo", "cp-out"] : ["cp-out"];

function call(execFile, file, args, opts) {
  return new Promise((resolve, reject) => execFile(file, args, opts, (err, stdout, stderr) => {
    if (err) { Object.assign(err, { stdout, stderr }); return reject(err) }
    resolve({ stdout, stderr, code: 0 });
  }));
}

const codeOf = async (fn) => { try { await fn(); return null } catch (e) { return e.code || e.name } };

export default async function run(ctx) {
  const { execFile } = await import("node:child_process");
  const out = {};
  const ok = await call(execFile, ECHO, ECHO_ARGS, { cwd: ctx.workspace }).catch((e) => ({ err: e.code || e.name }));
  out.stdout = ok.stdout ? ok.stdout.trim() : null;
  out.okErr = ok.err ?? null;
  out.shell = await codeOf(() => call(execFile, ECHO, ECHO_ARGS, { shell: true }));
  out.maxBuffer = await codeOf(() => call(execFile, ECHO, [isWin ? "/c" : "x".repeat(4096)], { maxBuffer: 128 }));
  out.cwdEscape = await codeOf(() => call(execFile, ECHO, ECHO_ARGS, { cwd: path.sep === "\\" ? process.env.SystemRoot : "/etc" }));
  out.missingProgram = await codeOf(() => call(execFile, "definitely-not-a-real-program-xyz", [], {}));
  out.nonZero = await codeOf(() => call(execFile, isWin ? "cmd.exe" : "/usr/bin/env", isWin ? ["/c", "exit", "7"] : ["false"], {}));
  return {
    returns: out,
    codes: out,
    order: [],
    effects: { platform: process.platform, eol: JSON.stringify(os.EOL) },
  };
}
