#!/usr/bin/env node
// 官方插件构建：插件根目录的每个 .ts → dist/<同名>.js。
//
// 设计 6.3 原定 esbuild；本仓库没有 npm 依赖（发布物只带一个 Go 二进制），因此用
// Node 内建的 TypeScript 擦除（module.stripTypeScriptTypes，Node ≥ 22.13）完成
// 「构建期预编译」：产物是与 dist 等价的 ESM，运行时与第三方插件走同一入口。
// 插件源码里不允许出现有运行时语义的 TS 特性（enum / namespace / import =），
// 这与 moejs 的 CompileTS 限制一致。
//
// 入口不止 index.ts：cron-manager 还有一个由网关拉起的 scheduler.ts（长驻进程，
// 与插件本体不是同一个 Runtime）。所以这里按「根目录的每个 .ts」逐个产出，
// 产物名 = 源文件名，--check 同样逐个比对。
//
// 用法：
//   node plugins/official/build.mjs           # 重新生成所有 dist/*.js
//   node plugins/official/build.mjs --check   # 只校验 dist 与源码同步（CI 用；无 node 时跳过）
import { createHash } from "node:crypto";
import { mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { stripTypeScriptTypes } from "node:module";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const check = process.argv.includes("--check");

// 产物头：写明来源与内容指纹，dist 被手改时 check 会报出来。
function banner(sourceName, source) {
  const sum = createHash("sha256").update(source).digest("hex").slice(0, 16);
  return `// generated from ${sourceName} (sha256:${sum}) by plugins/official/build.mjs — do not edit\n`;
}

function pluginDirs() {
  return readdirSync(here, { withFileTypes: true })
    .filter((e) => e.isDirectory() && !e.name.startsWith("."))
    .map((e) => e.name)
    .filter((name) => {
      try {
        return statSync(join(here, name, "index.ts")).isFile();
      } catch {
        return false;
      }
    })
    .sort();
}

// entrySources 返回插件根目录下需要预编译的 .ts 文件（按名字排序，保证输出稳定）。
function entrySources(dir) {
  return readdirSync(dir, { withFileTypes: true })
    .filter((e) => e.isFile() && e.name.endsWith(".ts") && !e.name.endsWith(".d.ts"))
    .map((e) => e.name)
    .sort();
}

let stale = 0;
for (const id of pluginDirs()) {
  const pluginDir = join(here, id);
  const sources = entrySources(pluginDir);
  if (sources.length === 0) continue;
  mkdirSync(join(pluginDir, "dist"), { recursive: true });
  for (const sourceName of sources) {
    const srcPath = join(pluginDir, sourceName);
    const outName = sourceName.replace(/\.ts$/, ".js");
    const outPath = join(pluginDir, "dist", outName);
    const source = readFileSync(srcPath, "utf8");
    // 擦除类型；source map 关掉（产物随 Electron resources 发布，保持单文件）。
    const js = stripTypeScriptTypes(source, { mode: "strip" });
    const want = banner(sourceName, source) + js;
    if (check) {
      let have = "";
      try {
        have = readFileSync(outPath, "utf8");
      } catch {
        console.error(`official: 缺少 ${id}/dist/${outName}（运行 node plugins/official/build.mjs）`);
        stale++;
        continue;
      }
      if (have !== want) {
        console.error(`official: ${id}/dist/${outName} 与 ${sourceName} 不同步（运行 node plugins/official/build.mjs）`);
        stale++;
      }
      continue;
    }
    writeFileSync(outPath, want);
    console.log(`official: ${id}/${sourceName} → dist/${outName}（${want.length} 字节）`);
  }
}

if (check) {
  if (stale > 0) process.exit(1);
  console.log("official: dist 与源码同步");
}
