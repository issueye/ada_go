// run-node20.mjs 是真 Node LTS 侧的语料运行器（设计 12 §6.14.1 双跑的另一条腿）。
//
//	node internal/conformance/nodecompat/corpus/run-node20.mjs            # 全部层，人类可读
//	node internal/conformance/nodecompat/corpus/run-node20.mjs 05-fs      # 单层
//	node internal/conformance/nodecompat/corpus/run-node20.mjs --json     # 逐用例一行 JSON，含与基线的差异
//
// 每个用例 fork 一个子进程（_run-one.mjs）：语料里有 process.exit、未处理的 stream
// error 之类，进程隔离才不会被一条用例把整条腿带下去。
//
// 它必须与 Go 侧 harness_test.go 铺同一套夹具（见 README「夹具」段），否则结果不可比。
// 注意：本地一般是 Node 25/24 而不是 20 LTS，输出里的版本号要说清楚；对跑基线以 20 为准。
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const layers = ["01-modules", "02-lang-async", "03-timers", "04-binary", "05-fs",
  "06-process-os", "07-child-process", "08-crypto-zlib", "09-errors"];

const argv = process.argv.slice(2);
const jsonMode = argv.includes("--json");
const only = argv.filter((a) => !a.startsWith("--"));

function casesIn(layer) {
  let names;
  try { names = readdirSync(path.join(here, layer)) } catch { return [] }
  return names.filter((n) => n.endsWith(".mjs") && !n.endsWith(".inc.mjs") && !n.startsWith("_")).sort()
    .map((n) => ({ layer, file: n, id: n.replace(/\.mjs$/, "") }));
}

function readExpect(layer, id) {
  try { return JSON.parse(readFileSync(path.join(here, layer, id + ".expect.json"), "utf8")) } catch { return null }
}

// subsetDiff：基线只声明要比较的键，逐键比（与 Go 侧 subsetDiff 同语义）。
function subsetDiff(expect, got, at = "") {
  if (expect === undefined) return [];
  if (Array.isArray(expect)) {
    if (!Array.isArray(got)) return [`${at}: 期望数组，实得 ${typeof got}`];
    return expect.flatMap((e, i) => subsetDiff(e, got[i], `${at}[${i}]`));
  }
  if (expect && typeof expect === "object") {
    return Object.entries(expect).flatMap(([k, v]) => subsetDiff(v, got && got[k], at ? `${at}.${k}` : k));
  }
  // Node 把没有的键读成 undefined，基线里是 null：两者都表示「没有值」，不算差异。
  if (expect === null && got === undefined) return [];
  return expect === got ? [] : [`${at}: 基线 ${JSON.stringify(expect)} ≠ 实测 ${JSON.stringify(got)}`];
}

let failures = 0;
let ran = 0;
const selected = only.length ? layers.filter((l) => only.some((o) => l === o || l.startsWith(o))) : layers;
for (const layer of selected) {
  for (const c of casesIn(layer)) {
    const expect = readExpect(layer, c.id);
    ran++;
    let payload;
    try {
      const stdout = execFileSync(process.execPath, [path.join(here, "_run-one.mjs"), layer, c.file, expect?.runtime?.withMany ? "1" : "0"], { encoding: "utf8" });
      payload = JSON.parse(stdout.trim().split("\n").filter((l) => l.startsWith("{")).pop());
    } catch (e) {
      payload = { result: { caseThrew: { name: "RunnerError", code: null, message: String(e.message).split("\n")[0] } }, unhandled: 0 };
    }
    const result = payload.result;
    const skipped = result && typeof result === "object" && typeof result.skip === "string";
    const diff = skipped || !expect?.result ? [] : subsetDiff(expect.result, result);
    if (diff.length) failures++;
    if (jsonMode) {
      process.stdout.write(JSON.stringify({ id: c.id, layer, node: process.version, skip: skipped ? result.skip : null, unhandled: payload.unhandled, parity: expect?.nodeParity ?? "no-baseline", ok: diff.length === 0, result, expectDiff: diff }) + "\n");
    } else {
      process.stdout.write(`[${skipped ? "SKIP" : diff.length ? "DIFF" : "SAME"}] ${c.id}\n`);
      for (const d of diff) process.stdout.write(`    ${d}\n`);
      if (process.env.VERBOSE) process.stdout.write("    " + JSON.stringify(result) + "\n");
    }
  }
}
process.stdout.write(`\n本地 Node ${process.version}（对跑基线是 20 LTS，版本不同要按实际标注）· 跑 ${ran} 条 · 与 moejs 基线不一致 ${failures} 条\n`);
