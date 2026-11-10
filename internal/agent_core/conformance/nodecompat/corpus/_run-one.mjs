// _run-one.mjs 是被 run-node20.mjs 逐用例 fork 出去的子进程：一个用例一个进程，
// 这样语料里的 process.exit、未处理的 stream error、崩溃都只会终止子进程，
// 不会把整条对跑腿带下去。stdout 只输出一行 JSON。
import { closeSync, mkdirSync, mkdtempSync, openSync, realpathSync, rmSync, symlinkSync, writeFileSync, writeSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const [layer, file, withMany] = process.argv.slice(2);
const here = path.dirname(fileURLToPath(import.meta.url));

// 与 Go 侧 prepareWorkspace、run-node20.mjs 同一套夹具。
function prepareWorkspace(many) {
  const root0 = (() => { try { return realpathSync(tmpdir()) } catch { return tmpdir() } })();
  const base = mkdtempSync(path.join(root0, "adag-nodecompat-"));
  const root = path.join(base, "ws");
  const outside = path.join(base, "outside");
  for (const d of [root, outside, path.join(root, "dir")]) mkdirSync(d, { recursive: true });
  writeFileSync(path.join(root, "hello.txt"), "hello moejs");
  writeFileSync(path.join(root, "dir", "inner.txt"), "inner");
  writeFileSync(path.join(root, ".env"), "SECRET=hunter2");
  writeFileSync(path.join(outside, "secret.txt"), "outside secret");
  try { symlinkSync(path.join(outside, "secret.txt"), path.join(root, "out.link")) } catch { /* 平台不支持 */ }
  try { symlinkSync(path.join(root, "hello.txt"), path.join(root, "inner.link")) } catch { /* 平台不支持 */ }
  if (many === "1") {
    const dir = path.join(root, "many");
    mkdirSync(dir, { recursive: true });
    for (let i = 0; i < 10500; i++) writeFileSync(path.join(dir, `f${String(i).padStart(5, "0")}.txt`), "x");
    const f = openSync(path.join(root, "big.bin"), "w");
    const chunk = "A".repeat(1 << 20);
    for (let i = 0; i < 20; i++) writeSync(f, chunk);
    closeSync(f);
  }
  return { base, root };
}

let unhandled = 0;
process.on("unhandledRejection", () => { unhandled++ });
process.on("error", () => { /* 同 moejs 侧：不让异步错误把腿带下去 */ });

const { base, root } = prepareWorkspace(withMany);
let result;
try {
  const mod = await import(pathToFileURL(path.join(here, layer, file)).href);
  result = await mod.default({ workspace: root, readOnly: false, host: "node20" });
} catch (e) {
  result = { caseThrew: { name: String(e && e.name), code: (e && e.code) !== undefined ? e.code : null, message: String(e && e.message) } };
}
rmSync(base, { recursive: true, force: true });
process.stdout.write(JSON.stringify({ result, unhandled }) + "\n");
