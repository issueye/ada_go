// proc-02: os 的受限面与 process.exit 的失败方向（设计 6.12.2 os 行）。
export const meta = { id: "proc-02-os", layer: "process-os", title: "os 受限面" };

import { importState } from "../_lib/shape.inc.mjs";

export default async function run() {
  const mod = await importState("node:os");
  let members = null;
  if (mod.state === "ok") {
    const os = await import("node:os");
    members = {
      platform: typeof os.platform === "function" ? os.platform() : null,
      EOL: JSON.stringify(os.EOL),
      cpus: Array.isArray(os.cpus()),
      tmpdir: typeof os.tmpdir === "function" ? os.tmpdir().length > 0 : null,
      emptyAsSpecified: [os.hostname(), os.homedir()].map((v) => JSON.stringify(v)),
      absentMembers: ["networkInterfaces", "userInfo", "loadavg"].filter((n) => typeof os[n] === "function"),
    };
  }
  return { returns: { module: mod, members }, codes: { os: mod.code }, order: [], effects: {} };
}
