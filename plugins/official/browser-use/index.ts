// browser-use —— 官方插件：把 browser-use 桌面浏览器接入为模型可调用的工具。
//
// 传输方式（本插件不新开端口、不实现协议）：
//   1. browser-use 在 127.0.0.1 上跑一个带 Bearer token 的 HTTP API，端口与 token
//      写在 <user-config>/browser-use/runtime.json（Windows 是 %APPDATA% 下）；
//   2. 本插件用 ctx.fs 读这个文件（读路径不受工作区限制），再用 ctx.fetch 调
//      POST /v1/call；
//   3. 应用没启动时用 ctx.exec 跑 `browser-use cli status`：CLI 会以 --serve 拉起
//      应用并等到健康，随后 runtime.json 就绪，再复用同一份 HTTP 通道。
//
// 边界：页面动作仍然全部走 browser-use 自己的 URL 策略、租约（120s 空闲释放）、
// 遮罩与审计日志；本插件只负责「模型友好的工具形状 + 结果裁剪 + 启动兜底」。
//
// 入口是纯 JavaScript（刻意不写类型注解）：dist/index.js 由 build.mjs 生成，
// 对无类型源码 strip 后与原文逐字等价，任何 Node 版本的 --check 都不会漂移。

import { join } from "node:path";

const DEFAULT_CALL_TIMEOUT_MS = 60000;
const MAX_CALL_TIMEOUT_MS = 110000;
const START_TIMEOUT_MS = 45000;
const HEALTH_TIMEOUT_MS = 2500;
const MAX_MODEL_TEXT = 24000;

// 一次进程内只解析一次运行时信息；应用重启（token 轮换）时由 401/连接失败清空重读。
let runtime = null;
let resolvedExe = "";
let starting = null;

// ---------- 小工具 ----------

function trimStr(value) {
  return typeof value === "string" ? value.trim() : "";
}

function clip(value, max) {
  const text = String(value == null ? "" : value);
  if (text.length <= max) return text;
  return text.slice(0, max) + "\n…（已截断，原长 " + text.length + " 字符）";
}

function intIn(value, lo, hi, fallback) {
  // null / undefined / 空串是「没给」：必须回落默认值，不能当 0 再被夹到下限。
  if (value === null || value === undefined || value === "") return fallback;
  const n = typeof value === "number" ? value : Number(value);
  if (!isFinite(n)) return fallback;
  const i = Math.round(n);
  if (i < lo) return lo;
  if (i > hi) return hi;
  return i;
}

function envValue(name) {
  const env = process.env || {};
  const value = env[name];
  return typeof value === "string" ? value : "";
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function callTimeout(args) {
  const raw = args && args.timeout_ms != null ? args.timeout_ms : null;
  return intIn(raw, 1000, MAX_CALL_TIMEOUT_MS, DEFAULT_CALL_TIMEOUT_MS);
}

function oneOf(value, allowed, fallback) {
  const text = trimStr(value).toLowerCase();
  if (!text) return fallback;
  if (allowed.indexOf(text) < 0) {
    throw new Error("不认识的动作 " + JSON.stringify(value) + "（可用：" + allowed.join(" / ") + "）");
  }
  return text;
}

// ---------- 运行时发现与启动 ----------

function appName() {
  return envValue("BROWSER_USE_APP_NAME") || "browser-use";
}

// runtimeFileCandidates 按平台给出 runtime.json 的候选路径（os.UserConfigDir 的口径）。
function runtimeFileCandidates() {
  const name = appName();
  const out = [];
  const push = (p) => {
    if (p && out.indexOf(p) < 0) out.push(p);
  };
  const xdg = envValue("XDG_CONFIG_HOME");
  if (xdg) push(join(xdg, name, "runtime.json"));
  const appdata = envValue("APPDATA");
  if (appdata) push(join(appdata, name, "runtime.json"));
  const home = envValue("HOME");
  if (home && process.platform === "darwin") {
    push(join(home, "Library", "Application Support", name, "runtime.json"));
  }
  if (home) push(join(home, ".config", name, "runtime.json"));
  const profile = envValue("USERPROFILE");
  if (profile) push(join(profile, "AppData", "Roaming", name, "runtime.json"));
  return out;
}

async function readRuntimeFile(ctx) {
  const fs = ctx.fs;
  if (!fs) return null;
  for (const path of runtimeFileCandidates()) {
    let exists = false;
    try {
      exists = await fs.exists(path);
    } catch (_err) {
      exists = false;
    }
    if (!exists) continue;
    let text = "";
    try {
      text = await fs.readText(path);
    } catch (_err) {
      continue;
    }
    let info = null;
    try {
      info = JSON.parse(text);
    } catch (_err) {
      continue;
    }
    if (!info || typeof info.port !== "number" || info.port <= 0 || !info.token) continue;
    return {
      base: "http://127.0.0.1:" + info.port,
      token: String(info.token),
      port: info.port,
      pid: typeof info.pid === "number" ? info.pid : 0,
      version: trimStr(info.version),
      file: path,
    };
  }
  return null;
}

async function healthy(ctx, base) {
  try {
    const res = await ctx.fetch(base + "/v1/health", { timeoutMs: HEALTH_TIMEOUT_MS });
    return !!res && res.status === 200;
  } catch (_err) {
    return false;
  }
}

async function currentRuntime(ctx) {
  if (runtime && (await healthy(ctx, runtime.base))) return runtime;
  runtime = null;
  const info = await readRuntimeFile(ctx);
  if (info && (await healthy(ctx, info.base))) {
    runtime = info;
    return info;
  }
  return null;
}

async function waitForRuntime(ctx, budgetMs) {
  const deadline = Date.now() + budgetMs;
  for (;;) {
    const info = await readRuntimeFile(ctx);
    if (info && (await healthy(ctx, info.base))) {
      runtime = info;
      return info;
    }
    if (Date.now() >= deadline) return null;
    await sleep(250);
  }
}

// exeCandidates 给出 browser-use 可执行文件的查找顺序：
//   1. 插件设置 exe/path（宿主没透传设置时不存在）；
//   2. 环境变量 BROWSER_USE_EXE（清单 backend.env 声明过才会出现在 process.env）；
//   3. 仓库内开发构建（相对路径按宿主进程的工作目录解析，通常就是工作区根）；
//   4. 常见安装位置（%LOCALAPPDATA%\Programs\browser-use）与 PATH 上的 browser-use。
function exeCandidates(ctx) {
  const out = [];
  const push = (p) => {
    const text = trimStr(p);
    if (text && out.indexOf(text) < 0) out.push(text);
  };
  const settings = ctx.settings && typeof ctx.settings === "object" ? ctx.settings : {};
  push(settings.exe);
  push(settings.path);
  push(envValue("BROWSER_USE_EXE"));
  // 仓库内开发构建（相对路径按宿主进程的工作目录解析，通常就是工作区根）。
  push("mcp_projects/browser_use/bin/browser-use.exe");
  push(join("..", "browser_use", "bin", "browser-use.exe"));
  // 常见安装位置与 PATH。
  const localAppData = envValue("LOCALAPPDATA");
  if (localAppData) push(join(localAppData, "Programs", "browser-use", "browser-use.exe"));
  push(process.platform === "win32" ? "browser-use.exe" : "browser-use");
  return out;
}

// ---------- 进程检测与启动（默认 HTTP 模式） ----------
//
// 「HTTP 启动方式」= `browser-use cli start`：以 --serve 起一个后台实例——控制 API 可用，
// 而且不碰用户会话（不恢复、不保存标签页）。模式来自插件设置 start 或环境变量
// BROWSER_USE_START：
//   http   默认：没在跑就起一个后台 HTTP 实例；
//   attach 只连接、不自动启动（用户的窗口自己开好）。
//
// 进程检测分两层，避免「窗口卡死时又起一个实例」：
//   1. runtime.json + /v1/health 是权威判据（API 活着就是活着）；
//   2. 进程表探测（Windows tasklist / Unix ps）区分「进程在但 API 无响应」和「根本没进程」。
//      单实例锁会让第二个进程立刻退出，盲目重启只会白等。

const START_MODE_HTTP = "http";
const START_MODE_ATTACH = "attach";

function pluginSettings(ctx) {
  return ctx.settings && typeof ctx.settings === "object" ? ctx.settings : {};
}

function startMode(ctx) {
  const raw = trimStr(pluginSettings(ctx).start) || envValue("BROWSER_USE_START");
  const mode = raw.toLowerCase();
  if (mode === START_MODE_ATTACH) return START_MODE_ATTACH;
  return START_MODE_HTTP;
}

function startPort(ctx) {
  const raw = pluginSettings(ctx).port != null ? pluginSettings(ctx).port : envValue("BROWSER_USE_PORT");
  return intIn(raw, 0, 65535, 0);
}

// runExec 走宿主的 ctx.exec(cmd, args, {timeoutMs})：它不抛异常，返回
// {code, stdout, stderr, error?}——error 是失败类别（start/timeout/maxbuffer/aborted）。
// 「进程根本没起来」（路径不存在/没有权限）用 error === "start" 表示。
async function runExec(ctx, cmd, argv, timeoutMs) {
  const res = await ctx.exec(cmd, argv, { timeoutMs: timeoutMs || 5000 });
  const outcome = res && typeof res === "object" ? res : {};
  return {
    ran: trimStr(outcome.error) !== "start",
    kind: trimStr(outcome.error),
    code: typeof outcome.code === "number" ? outcome.code : 0,
    stdout: typeof outcome.stdout === "string" ? outcome.stdout : "",
    stderr: trimStr(outcome.stderr),
  };
}

async function runCli(ctx, exe, args, timeoutMs) {
  return runExec(ctx, exe, args, timeoutMs);
}

// pidState 查一个 pid 是否还活着、镜像名是什么（探测不可用时 known=false）。
async function pidState(ctx, pid) {
  if (!pid) return { known: false, alive: false, name: "" };
  if (!ctx.exec || typeof ctx.exec !== "function") return { known: false, alive: false, name: "" };
  if (process.platform === "win32") {
    const res = await runExec(ctx, "tasklist", ["/FI", "PID eq " + pid, "/FO", "CSV", "/NH"], 5000);
    if (!res.ran) return { known: false, alive: false, name: "" };
    const m = /^"([^"]+)","(\d+)"/m.exec(res.stdout);
    if (!m || Number(m[2]) !== pid) return { known: true, alive: false, name: "" };
    return { known: true, alive: true, name: m[1] };
  }
  const res = await runExec(ctx, "ps", ["-p", String(pid), "-o", "pid=,comm="], 5000);
  if (!res.ran) return { known: false, alive: false, name: "" };
  const line = trimStr(res.stdout.split(/\r?\n/)[0]);
  if (!line) return { known: true, alive: false, name: "" };
  const parts = line.split(/\s+/);
  return { known: true, alive: true, name: parts.slice(1).join(" ") };
}

// probeProcesses 尽力列出 browser-use 进程；探测不可用时返回 null（未知，不等于 0）。
async function probeProcesses(ctx) {
  if (!ctx.exec || typeof ctx.exec !== "function") return null;
  const wanted = [appName() + ".exe", appName() + ".app", "browser-use.exe", "browser-use"];
  if (process.platform === "win32") {
    const seen = [];
    for (const image of wanted) {
      const res = await runExec(ctx, "tasklist", ["/FI", "IMAGENAME eq " + image, "/FO", "CSV", "/NH"], 5000);
      if (!res.ran) return null;
      for (const line of res.stdout.split(/\r?\n/)) {
        const m = /^"([^"]+)","(\d+)"/.exec(line.trim());
        if (!m) continue;
        const pid = Number(m[2]);
        if (seen.some((p) => p.pid === pid)) continue;
        seen.push({ name: m[1], pid: pid });
      }
    }
    return seen;
  }
  const res = await runExec(ctx, "pgrep", ["-f", "browser-use"], 5000);
  if (!res.ran) return null;
  const out = [];
  for (const line of res.stdout.split(/\r?\n/)) {
    const pid = Number(trimStr(line));
    if (pid > 0) out.push({ name: "browser-use", pid: pid });
  }
  return out;
}

// startApp 用 `cli start`（HTTP 模式）拉起应用；旧版 CLI 没有 start 子命令时退回
// `cli status`（它同样以 --serve 启动）。退出码非零不代表没起来，统一用
// runtime.json + /v1/health 复核。
async function startApp(ctx, portOverride) {
  if (!ctx.exec || typeof ctx.exec !== "function") {
    throw new Error(
      "browser-use 没有在运行，而且本插件没有 exec 能力，无法自动启动。\n" +
        "请先手动启动浏览器：browser-use.exe --serve（或直接双击打开 browser-use），再重试。",
    );
  }
  const port = portOverride != null ? intIn(portOverride, 0, 65535, 0) : startPort(ctx);
  const tried = [];
  for (const exe of exeCandidates(ctx)) {
    const argv = ["cli", "start"];
    if (port > 0) argv.push("--port", String(port));
    const outcome = await runCli(ctx, exe, argv, START_TIMEOUT_MS);
    if (!outcome.ran) {
      tried.push(exe + "（无法执行：路径不存在或没有权限）");
      continue;
    }
    if (outcome.code === 0) {
      const info = await waitForRuntime(ctx, 8000);
      if (info) {
        resolvedExe = exe;
        runtime = info;
        return info;
      }
      tried.push(exe + "（cli start 成功但控制 API 未就绪）");
      continue;
    }
    const text = outcome.stderr + "\n" + outcome.stdout;
    if (/unknown command: start/i.test(text)) {
      // 旧版 CLI：没有 start 子命令，用 status 兜底（Ensure 内部同样是 --serve）。
      const fallback = await runCli(ctx, exe, ["cli", "status"], START_TIMEOUT_MS);
      if (fallback.ran) {
        const info = await waitForRuntime(ctx, 10000);
        if (info) {
          resolvedExe = exe;
          runtime = info;
          return info;
        }
      }
      tried.push(exe + "（旧版 CLI 兜底失败：" + clip(text, 160) + "）");
      continue;
    }
    tried.push(exe + "（cli start 失败：" + clip(text, 200) + "）");
  }
  const detected = await probeProcesses(ctx);
  throw new Error(
    [
      "无法使用 browser-use：没有找到运行中的实例，也没能自动启动。",
      tried.length ? "已尝试：" + tried.join("；") : "",
      detected && detected.length > 0
        ? "检测到的 browser-use 进程：" + detected.map((p) => "pid " + p.pid + " " + p.name).join("、") +
          "（可能用了别的 BROWSER_USE_APP_NAME，或控制 API 未就绪）"
        : "",
      "处理办法（任一）：",
      "1) 先手动启动浏览器：<browser-use.exe 绝对路径> --serve；",
      "2) 在启动 ada-go 之前设置环境变量 BROWSER_USE_EXE=<browser-use.exe 绝对路径>，插件会自动启动它；",
      "3) 本仓库的开发构建一般在 mcp_projects/browser_use/bin/browser-use.exe，确认已经 go build 出来。",
    ]
      .filter((line) => line !== "")
      .join("\n"),
  );
}

async function ensureRuntime(ctx) {
  const live = await currentRuntime(ctx);
  if (live) return live;
  const stale = await readRuntimeFile(ctx);
  if (startMode(ctx) === START_MODE_ATTACH) {
    throw new Error(
      "browser-use 没有在运行（启动模式 attach：只连接、不自动启动）。\n" +
        (stale ? "runtime.json 存在但控制 API 无响应（pid " + stale.pid + "）。\n" : "") +
        "请自己打开 browser-use，或把 BROWSER_USE_START 设为 http 让插件自动启动。",
    );
  }
  if (stale && stale.pid) {
    const state = await pidState(ctx, stale.pid);
    if (state.alive && /browser-use/i.test(state.name)) {
      throw new Error(
        "检测到 browser-use 进程（pid " + stale.pid + "），但控制 API 无响应（runtime.json 过期或窗口卡住）。\n" +
          "请关闭那个窗口后重试；插件不会再启动第二个实例（单实例锁会让新进程立即退出）。",
      );
    }
  }
  if (!starting) {
    starting = (async () => {
      try {
        return await startApp(ctx);
      } finally {
        starting = null;
      }
    })();
  }
  return await starting;
}

// ---------- 长任务：异步接口 + CLI 兜底 ----------
//
// 宿主的出网桥把「响应头超时」固定在 ProviderConnectTimeout=15s（httpbridge.go 里
// ResponseHeaderTimeout: connect）。browser-use 的 /v1/call 是同步接口：响应头要等
// 脚本/等待动作跑完才发出，所以任何超过 ~15s 的调用都会被桥掐断（客户端断开后
// browser-use 会把脚本记成 script_timeout）。
//
// 两条慢通道，按优先级：
//   1. 异步接口 POST /v1/calls + GET /v1/calls/{id}（browser-use 0.2+）：启动请求
//      毫秒级返回，任务与请求解耦，client 归因仍是 adag-<thread>；
//   2. 老版本没有异步接口时退回 CLI（browser-use run / cli call，320s 客户端）。
// 其余调用仍走 HTTP 同步路径；只读调用遇到 15s 超时会自动改走上面两条。

const CLI_SLOW_THRESHOLD_MS = 12000;
const MAX_CLI_SOURCE_BYTES = 20000;
const MAX_EXEC_TIMEOUT_MS = 118000;

function utf8Len(text) {
  const s = String(text == null ? "" : text);
  let n = 0;
  for (let i = 0; i < s.length; i += 1) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4;
      i += 1;
    } else n += 3;
  }
  return n;
}

// preferAsync 判断这次调用是否天然可能超过 15s：
//   - script 一定走异步（HTTP body 传输，不受命令行长度限制）；
//   - wait / navigate / network(wait) 的预算超过 12s 时走异步。
function preferAsync(tool, args) {
  const a = args || {};
  if (tool === "script") return true;
  const budget = typeof a.timeout_ms === "number" ? a.timeout_ms : 0;
  if (budget > CLI_SLOW_THRESHOLD_MS && (tool === "wait" || tool === "navigate" || tool === "network")) {
    return true;
  }
  return false;
}

// preferCli 是 async 不可用时的兜底判定：脚本源码要能放进命令行。
function preferCli(tool, args) {
  if (tool === "script") {
    return utf8Len((args || {}).source) + 512 <= MAX_CLI_SOURCE_BYTES;
  }
  return preferAsync(tool, args);
}

function isTransportTimeout(err) {
  const text = String(err && err.message ? err.message : err);
  return text.indexOf("HTTP 请求超时") >= 0 || text.indexOf("deadline exceeded") >= 0;
}

// retryableByCli 判断「超时后重放」是否安全：只对只读调用重放。
// 点击/输入/按键/脚本/eval 可能已经生效，重放会重复副作用，因此宁可报错让模型先看状态。
const CLI_REPLAY_TOOLS = {
  status: true,
  snapshot: true,
  read: true,
  screenshot: true,
  network: true,
  wait: true,
  frames: true,
  downloads: true,
  dialogs: true,
  cookies: true,
  control: true,
};

function retryableByCli(tool) {
  return CLI_REPLAY_TOOLS[tool] === true;
}

// cleanTransportText 去掉宿主 domain 错误码后缀：模型看到 "(5001 provider_error)"
// 会误判成供应商问题，把整轮结论带偏。
function cleanTransportText(text) {
  return String(text == null ? "" : text)
    .replace(/\s*\(\s*5001\s+[A-Za-z_]+\s*\)/g, "")
    .replace(/\s*\(5001\)/g, "")
    .trim();
}

// resolveExe 只在需要 CLI 通道时解析一次可执行文件（`browser-use help` 只打用法）。
async function resolveExe(ctx) {
  if (resolvedExe) return resolvedExe;
  if (!ctx.exec || typeof ctx.exec !== "function") {
    throw new Error(
      "browser-use CLI 通道需要 exec 能力（清单 backend.capabilities 未声明？）；" +
        "没有它，超过 15s 的脚本/等待无法执行。",
    );
  }
  const tried = [];
  for (const cand of exeCandidates(ctx)) {
    const out = await runCli(ctx, cand, ["help"], 8000);
    if (out.ran && out.code === 0) {
      resolvedExe = cand;
      return cand;
    }
    tried.push(cand);
  }
  throw new Error(
    "找不到可用的 browser-use 可执行文件（试过：" + tried.join("、") + "）。\n" +
      "请在启动 ada-go 前设置环境变量 BROWSER_USE_EXE=<browser-use.exe 绝对路径>。",
  );
}

// cliReady 是「CLI 通道能不能用」的一次性探测：找不到可执行文件时只降级、不阻断。
// 否则桌面安装版（cwd 不是仓库根、又没设 BROWSER_USE_EXE）会连导航都做不了。
let cliState = ""; // "" 未知 / "ready" / "missing"
let cliProblem = "";

async function cliReady(ctx) {
  if (resolvedExe || cliState === "ready") return true;
  if (cliState === "missing") return false;
  try {
    await resolveExe(ctx);
    cliState = "ready";
    return true;
  } catch (err) {
    cliState = "missing";
    cliProblem = err && err.message ? String(err.message) : String(err);
    if (ctx && typeof ctx.log === "function") {
      ctx.log(
        "warn",
        "browser-use CLI 通道不可用：超过 15s 的脚本/等待会被 ada-go 出网桥掐断。",
      );
    }
    return false;
  }
}

function scriptArgv(rawArgs) {
  const a = rawArgs || {};
  const argv = ["run", "-e", String(a.source || "")];
  if (a.args && typeof a.args === "object") argv.push("--args", JSON.stringify(a.args));
  if (typeof a.timeout_ms === "number" && a.timeout_ms > 0) {
    argv.push("--timeout", String(Math.round(a.timeout_ms)));
  }
  const tab = trimStr(a.tab);
  if (tab) argv.push("--tab", tab);
  if (a.release_on_finish === false) argv.push("--keep-control");
  if (trimStr(a.language) === "ts") argv.push("--language", "ts");
  return argv;
}

async function rpcViaCli(ctx, tool, args, timeoutMs) {
  const exe = await resolveExe(ctx);
  const argv =
    tool === "script"
      ? scriptArgv(args)
      : ["cli", "call", tool, "--args", JSON.stringify(args || {})];
  const budget = intIn(timeoutMs, 1000, MAX_CALL_TIMEOUT_MS, DEFAULT_CALL_TIMEOUT_MS) + 5000;
  const res = await ctx.exec(exe, argv, { timeoutMs: Math.min(budget, MAX_EXEC_TIMEOUT_MS) });
  const kind = trimStr(res && res.error);
  if (kind === "start") {
    throw new Error("无法执行 browser-use CLI（" + exe + "）：路径不存在或没有权限。");
  }
  const stdout = trimStr(res && res.stdout);
  let envelope = null;
  try {
    envelope = stdout ? JSON.parse(stdout) : null;
  } catch (_err) {
    envelope = null;
  }
  if (!envelope || typeof envelope !== "object") {
    const detail = clip(
      stdout || trimStr(res && res.stderr) || "CLI 退出码 " + (res && res.code) + (kind ? "/" + kind : ""),
      400,
    );
    throw new Error("browser-use CLI 没有返回可解析的结果：" + detail);
  }
  if (envelope.ok !== true) throw toolFailure(tool, envelope.error);
  return envelope;
}

// ---------- 异步调用（长任务的优先通道） ----------

// rpcAsync 启动一个后台任务并轮询到结束；老版本没有 /v1/calls 时返回 null。
async function rpcAsync(ctx, tool, args, timeoutMs) {
  const rt = await ensureRuntime(ctx);
  const auth = "Bearer " + rt.token;
  const start = await ctx.fetch(rt.base + "/v1/calls", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: auth,
      "X-Client-Id": clientId(ctx),
    },
    body: JSON.stringify({ name: tool, args: args || {}, timeout_ms: timeoutMs }),
    timeoutMs: 10000,
  });
  if (start.status === 404) return null; // 旧版本：没有异步接口，交给 CLI 兜底
  if (start.status === 429) {
    throw new Error("browser-use 正在执行其它任务（并发上限 4），请稍后重试。");
  }
  if (!start.ok) {
    throw new Error("browser-use 拒绝启动任务 HTTP " + start.status + "：" + clip(start.text, 300));
  }
  let job = null;
  try {
    job = JSON.parse(start.text);
  } catch (_err) {
    job = null;
  }
  const id = trimStr(job && job.id);
  if (!id || !/^[A-Za-z0-9_-]+$/.test(id)) {
    throw new Error("browser-use 异步接口返回异常：" + clip(start.text, 300));
  }
  const headers = { Authorization: auth, "X-Client-Id": clientId(ctx) };
  const deadline = Date.now() + timeoutMs + 8000;
  let delay = 250;
  for (;;) {
    if (Date.now() > deadline) {
      // 别把任务留在后台占并发槽：取消它，把决定权交回模型。
      try {
        await ctx.fetch(rt.base + "/v1/calls/" + id, { method: "DELETE", headers, timeoutMs: 5000 });
      } catch (_err) {
        /* 取消失败不影响结论 */
      }
      throw new Error(
        "browser-use 任务 " + id + " 超过 " + Math.round(timeoutMs / 1000) + "s 仍未结束，已取消。\n" +
          "请缩小脚本范围或把轮询上限降到 10s 以内。",
      );
    }
    await sleep(delay);
    delay = Math.min(delay + 150, 1000);
    let poll = null;
    try {
      poll = await ctx.fetch(rt.base + "/v1/calls/" + id, { headers, timeoutMs: 10000 });
    } catch (_err) {
      continue; // 单次轮询失败不算任务失败
    }
    if (poll.status === 404) {
      throw new Error("browser-use 任务 " + id + " 不存在（应用可能重启过）。");
    }
    if (!poll.ok) continue;
    let body = null;
    try {
      body = JSON.parse(poll.text);
    } catch (_err) {
      continue;
    }
    const view = (body && body.job) || body || {};
    const status = trimStr(view.status);
    if (status === "" || status === "running") continue;
    const result = view.result || {};
    if (status === "cancelled") {
      throw new Error("browser-use 任务被取消（" + tool + "）。");
    }
    if (result.ok !== true) throw toolFailure(tool, result.error);
    return result;
  }
}

// rpcSlow 是慢调用的统一入口：异步接口优先，旧版本退回 CLI。
async function rpcSlow(ctx, tool, args, timeout) {
  const envelope = await rpcAsync(ctx, tool, args, timeout);
  if (envelope) return envelope;
  if (preferCli(tool, args) && (await cliReady(ctx))) return rpcViaCli(ctx, tool, args, timeout);
  throw new Error(
    "browser-use 只支持同步 /v1/call（没有 /v1/calls 异步接口），而且 CLI 通道不可用：" +
      "长任务会被 ada-go 出网桥的 15s 响应头上限掐断。\n" +
      "请升级 browser-use，或在启动 ada-go 前设置 BROWSER_USE_EXE=<browser-use.exe 绝对路径>。",
  );
}

// ---------- /v1/call ----------

function clientId(ctx) {
  const raw = trimStr(ctx.threadId) || "main";
  const safe = raw.replace(/[^A-Za-z0-9_-]/g, "_").slice(0, 40);
  return "adag-" + safe;
}

function toolFailure(tool, info) {
  const code = info && info.code ? String(info.code) : "unknown";
  const message = info && info.message ? String(info.message) : "浏览器操作失败";
  const hint = info && info.hint ? String(info.hint) : "";
  let detail = "";
  if (info && info.details != null) {
    try {
      detail = clip(JSON.stringify(info.details), 600);
    } catch (_err) {
      detail = "";
    }
  }
  const err = new Error(
    "browser-use 工具 " + tool + " 失败（" + code + "）：" + message +
      (hint ? "；提示：" + hint : "") +
      (detail ? "；详情：" + detail : ""),
  );
  err.code = code;
  return err;
}

async function rpc(ctx, tool, args, timeoutMs) {
  const timeout = intIn(timeoutMs, 1000, MAX_CALL_TIMEOUT_MS, DEFAULT_CALL_TIMEOUT_MS);
  // 注定超过出网桥 15s 响应头上限的调用：异步接口优先，旧版本退回 CLI。
  if (preferAsync(tool, args)) return rpcSlow(ctx, tool, args, timeout);
  const body = JSON.stringify({ name: tool, args: args || {} });
  let lastErr = null;
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const rt = await ensureRuntime(ctx);
    const headers = {
      "Content-Type": "application/json",
      Authorization: "Bearer " + rt.token,
      "X-Client-Id": clientId(ctx),
    };
    let res = null;
    try {
      res = await ctx.fetch(rt.base + "/v1/call", {
        method: "POST",
        headers,
        body,
        timeoutMs: timeout,
      });
    } catch (err) {
      runtime = null;
      lastErr = err;
      if (isTransportTimeout(err)) {
        // 15s 响应头超时不是网络抖动：重试只会再等 15s。
        if (retryableByCli(tool)) {
          if (ctx && typeof ctx.log === "function") {
            ctx.log("info", "browser-use 出网桥超时，改走异步/CLI 通道", {
              tool: tool,
              timeout_ms: timeout,
            });
          }
          return rpcSlow(ctx, tool, args, timeout);
        }
        const cliHint = cliProblem ? "\n（慢通道不可用：" + clip(cliProblem, 300) + "）" : "";
        if (tool === "script") {
          throw new Error(
            "脚本在 15s 内没有返回，而且慢通道（/v1/calls 异步接口 / CLI）不可用。" + cliHint + "\n" +
              "处理：升级 browser-use（异步接口），或设置 BROWSER_USE_EXE 后重启 ada-go。",
          );
        }
        throw new Error(
          "browser-use 在 15s 内没有返回（" + tool + "）。这类动作用的是 ada-go 出网桥，" +
            "它把响应头超时固定在 15s；动作可能已经生效，重放会重复副作用。" + cliHint + "\n" +
            "先用 browser_status / browser_snapshot 确认当前页面状态，再决定是否重试。",
        );
      }
      if (ctx && typeof ctx.log === "function") {
        ctx.log("debug", "browser-use fetch failed", {
          tool: tool,
          timeout_ms: timeout,
          attempt: attempt,
          error: cleanTransportText(String(err && err.message ? err.message : err)),
        });
      }
      continue;
    }
    if (res.status === 401) {
      // 应用重启会轮换 token：丢掉缓存，读新的 runtime.json 再试一次。
      runtime = null;
      lastErr = new Error("browser-use 拒绝了当前 token（应用可能重启过）");
      continue;
    }
    if (!res.ok) {
      throw new Error("browser-use 返回 HTTP " + res.status + "：" + clip(res.text, 400));
    }
    let envelope = null;
    try {
      envelope = JSON.parse(res.text);
    } catch (_err) {
      throw new Error("browser-use 返回了非 JSON 响应：" + clip(res.text, 400));
    }
    if (!envelope || envelope.ok !== true) {
      throw toolFailure(tool, envelope && envelope.error);
    }
    return envelope;
  }
  const reason = cleanTransportText(lastErr && lastErr.message ? lastErr.message : String(lastErr));
  throw new Error(
    "无法连接 browser-use：" + reason + "（应用没在运行的话，先调用 browser_status 触发自动拉起）",
  );
}

// ---------- 结果格式化（给模型看的是文本，结构化数据只进 details） ----------

function jsonBlock(value) {
  if (value === undefined || value === null) return "（无返回值）";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch (_err) {
    return String(value);
  }
}

function withMeta(lines, env) {
  const out = [];
  for (const line of lines) {
    if (line === null || line === undefined) continue;
    const text = String(line);
    if (text === "" || text.trim() !== "") out.push(text);
  }
  if (env && Array.isArray(env.warnings) && env.warnings.length > 0) {
    out.push("注意：" + env.warnings.join("；"));
  }
  if (env && typeof env.timing_ms === "number" && env.timing_ms >= 2000) {
    out.push("（浏览器耗时 " + env.timing_ms + "ms）");
  }
  return clip(out.join("\n"), MAX_MODEL_TEXT);
}

function tabLabel(tab) {
  if (!tab) return "";
  const id = trimStr(tab.id) || "?";
  const title = trimStr(tab.title);
  const url = trimStr(tab.url);
  const bits = ["[" + id + "]", title || url || "（空白页）"];
  if (title && url && title !== url) bits.push("— " + url);
  if (tab.loading) bits.push("(加载中)");
  if (tab.locked) bits.push("(agent 租约)");
  if (trimStr(tab.owner) === "user") bits.push("(用户标签页)");
  return bits.join(" ");
}

function controlLine(control) {
  if (!control) return "浏览器控制：未知";
  const state = trimStr(control.state) || "idle";
  const label =
    state === "agent" ? "agent 持有租约" : state === "user" ? "用户接管中" : "空闲";
  const extra = [];
  if (trimStr(control.tab)) extra.push("tab=" + trimStr(control.tab));
  if (control.masked) extra.push("遮罩已显示");
  if (trimStr(control.lease_until)) extra.push("租约到 " + trimStr(control.lease_until));
  if (trimStr(control.client)) extra.push("client=" + trimStr(control.client));
  return "浏览器控制：" + label + (extra.length ? "（" + extra.join("，") + "）" : "");
}

function statusText(env, rt, detection) {
  const data = (env && env.data) || {};
  const lines = [];
  const version = rt && rt.version ? " v" + rt.version : "";
  const pid = rt && rt.pid ? "（pid " + rt.pid + "）" : "";
  lines.push("browser-use" + version + pid);
  const det = detection || {};
  lines.push(
    "启动模式：" +
      (det.mode === START_MODE_ATTACH ? "attach（只连接、不自动启动）" : "http（不存在就后台启动）"),
  );
  if (rt && rt.pid) {
    const state = det.pidState || {};
    let alive = "未知（当前平台无法探测进程）";
    if (state.known) alive = state.alive ? "运行中" : "已退出（runtime.json 可能是旧的）";
    lines.push("进程：pid " + rt.pid + " " + alive + (rt.port ? "；控制端口 " + rt.port : ""));
  }
  lines.push(controlLine(data.control));
  const active = data.active_tab || null;
  lines.push("活动标签页：" + (active ? tabLabel(active) : "（无）"));
  const tabs = Array.isArray(data.tabs) ? data.tabs : [];
  if (tabs.length > 0) {
    lines.push("标签页（" + tabs.length + "）：");
    for (const tab of tabs) {
      lines.push("  - " + tabLabel(tab) + (tab.id === data.active_tab_id ? " ← 活动" : ""));
    }
  } else if (typeof data.tabs_count === "number") {
    lines.push("标签页数：" + data.tabs_count);
  }
  if (trimStr(data.downloads_dir)) lines.push("下载目录：" + trimStr(data.downloads_dir));
  if (tabs.length >= 6) {
    lines.push("提示：标签页偏多（默认上限 8，agent.max_tabs）；用完用 browser_tab {action:\"close\"} 关掉自己开的，避免撞 too_many_tabs。");
  }
  if (data.control && data.control.state === "user") {
    lines.push("用户已接管：写操作会被 control_locked 拒绝；请让用户点 Resume agent 再继续。");
  }
  return withMeta(lines, env);
}

function navigateText(env, action) {
  const data = (env && env.data) || {};
  const verb =
    { goto: "打开", back: "后退", forward: "前进", reload: "刷新", stop: "停止加载" }[action] ||
    action;
  const lines = [verb + "完成"];
  const tab = data.tab || (env && env.tab) || null;
  if (tab) lines.push(tabLabel(tab));
  return withMeta(lines, env);
}

function tabText(env, action) {
  const data = (env && env.data) || {};
  const lines = [];
  if (action === "new") {
    lines.push("已新建标签页");
    if (data.tab) lines.push(tabLabel(data.tab));
  } else if (action === "select") {
    lines.push("已切换到 [" + trimStr(data.active) + "]");
    if (env && env.tab) lines.push(tabLabel(env.tab));
  } else if (action === "close") {
    lines.push("已关闭 [" + trimStr(data.closed) + "]");
    if (env && env.tab) lines.push("当前活动：" + tabLabel(env.tab));
  } else {
    lines.push(jsonBlock(data));
  }
  return withMeta(lines, env);
}

function snapshotText(env) {
  const data = (env && env.data) || {};
  const lines = [];
  if (trimStr(data.url)) lines.push("URL：" + trimStr(data.url));
  if (trimStr(data.title)) lines.push("标题：" + trimStr(data.title));
  if (data.truncated) lines.push("（快照被 max_chars 截断：用 filter 或 scope=viewport 收窄）");
  if (typeof data.nodes_count === "number") lines.push("页面节点数：" + data.nodes_count);
  if (trimStr(data.snapshot)) {
    lines.push("");
    lines.push(trimStr(data.snapshot));
  } else {
    lines.push("（快照为空：页面可能还在加载，或视口里没有可交互元素）");
  }
  return withMeta(lines, env);
}

function readText(env) {
  const data = (env && env.data) || {};
  const lines = [];
  if (trimStr(data.url)) lines.push("URL：" + trimStr(data.url));
  if (trimStr(data.title)) lines.push("标题：" + trimStr(data.title));
  if (data.truncated) lines.push("（正文已按 max_chars 截断）");
  lines.push("");
  lines.push(trimStr(data.content) || "（没有可读文本）");
  return withMeta(lines, env);
}

function nodeLabel(node) {
  if (!node) return "元素";
  const bits = [trimStr(node.role) || "element"];
  if (trimStr(node.name)) bits.push('"' + trimStr(node.name) + '"');
  if (trimStr(node.ref)) bits.push("[" + trimStr(node.ref) + "]");
  return bits.join(" ");
}

function actText(env, action) {
  const data = (env && env.data) || {};
  const lines = [];
  if (action === "click" || action === "dblclick") {
    lines.push("已" + (action === "dblclick" ? "双击" : "单击") + " " + nodeLabel(data.clicked));
    if (data.revealed) lines.push("（点击时临时显示了 opacity:0 的悬停容器）");
  } else if (action === "type") {
    lines.push("已输入 " + nodeLabel(data.typed) + (data.submitted ? "，并按 Enter 提交" : ""));
  } else if (action === "hover") {
    lines.push("已悬停 " + nodeLabel(data.hovered));
  } else if (action === "press") {
    const pressed = data.pressed || {};
    const mods = Array.isArray(pressed.modifiers) && pressed.modifiers.length > 0
      ? "（+" + pressed.modifiers.join("+") + "）"
      : "";
    lines.push("已按键 " + (trimStr(pressed.key) || "?") + mods);
  } else if (action === "select") {
    const selected = Array.isArray(data.selected) ? data.selected : [];
    lines.push("已选中：" + (selected.length ? selected.join("、") : "（无）"));
  } else if (action === "scroll") {
    const pos = data.scroll || {};
    lines.push("已滚动到 (" + (pos.x || 0) + ", " + (pos.y || 0) + ")");
  } else if (action === "wait") {
    const ms = typeof data.elapsed_ms === "number" ? "（" + data.elapsed_ms + "ms）" : "";
    lines.push("等待完成" + ms + (trimStr(data.path) ? "：下载到 " + trimStr(data.path) : ""));
  } else {
    lines.push("操作完成");
  }
  if (data.navigated) lines.push("（页面已导航）");
  if (trimStr(data.url)) lines.push("URL：" + trimStr(data.url));
  if (trimStr(data.title)) lines.push("标题：" + trimStr(data.title));
  return withMeta(lines, env);
}

function evalText(env) {
  const data = (env && env.data) || {};
  return withMeta(["eval 结果：", jsonBlock(data.result)], env);
}

function scriptText(env) {
  const data = (env && env.data) || {};
  const lines = [];
  const ms = typeof data.duration_ms === "number" ? data.duration_ms : 0;
  lines.push("脚本执行完成（" + ms + "ms，script_id=" + (trimStr(data.script_id) || "?") + "）");
  lines.push("返回值：");
  lines.push(jsonBlock(data.result));
  const logs = Array.isArray(data.console) ? data.console : [];
  if (logs.length > 0) {
    lines.push("控制台（" + logs.length + " 行）：");
    for (const line of logs.slice(0, 50)) {
      lines.push("  [" + (trimStr(line.level) || "log") + "] " + String(line.text || ""));
    }
    if (logs.length > 50) lines.push("  …（其余 " + (logs.length - 50) + " 行省略）");
  }
  const actions = Array.isArray(data.actions) ? data.actions : [];
  if (actions.length > 0) {
    lines.push("浏览器动作（" + actions.length + " 条）：");
    for (const action of actions.slice(0, 40)) {
      const ok = action.ok ? "ok" : "失败" + (trimStr(action.code) ? " " + trimStr(action.code) : "");
      lines.push("  - " + (trimStr(action.tool) || "?") + " " + ok + " (" + (action.ms || 0) + "ms)");
    }
    if (actions.length > 40) lines.push("  …（其余 " + (actions.length - 40) + " 条省略）");
  }
  if (data.truncated) lines.push("（返回值已按 max_output_bytes 截断）");
  return withMeta(lines, env);
}

function networkText(env) {
  const data = (env && env.data) || {};
  const lines = [];
  if (typeof data.cleared === "number") lines.push("已清空 " + data.cleared + " 条抓包记录。");
  const entries = Array.isArray(data.entries) ? data.entries.slice() : [];
  // action=get / action=wait 返回单条记录（entry），list 返回 entries。
  if (data.entry && typeof data.entry === "object") entries.unshift(data.entry);
  if (entries.length === 0 && typeof data.cleared !== "number") {
    lines.push("没有匹配的请求（请求只在页面脚本发起 fetch/XHR 时被记录）。");
  }
  for (const entry of entries) {
    lines.push(
      "[" + (trimStr(entry.id) || "?") + "] " +
        (trimStr(entry.method) || "?") + " " + (entry.status || 0) + " " +
        (entry.ms || 0) + "ms " + (trimStr(entry.url) || ""),
    );
    if (trimStr(entry.req_body)) lines.push("  请求体：" + clip(entry.req_body, 2000));
    if (trimStr(entry.res_body)) lines.push("  响应体：" + clip(entry.res_body, 4000));
    if (entry.truncated) lines.push("  （body 已按上限截断，可用 max_body 放宽）");
  }
  if (typeof data.count === "number") {
    lines.push(
      "（共 " + data.count + " 条，丢弃 " + (data.dropped || 0) + " 条，合计 " +
        (data.total_bytes || 0) + " 字节）",
    );
  }
  return withMeta(lines, env);
}

function screenshotText(env) {
  const data = (env && env.data) || {};
  const lines = ["截图已保存：" + (trimStr(data.path) || "（未知路径）")];
  lines.push(
    "尺寸：" + (data.width || 0) + "x" + (data.height || 0) +
      "（CSS " + (data.css_width || 0) + "x" + (data.css_height || 0) + "，dpr " + (data.dpr || 1) + "）",
  );
  if (data.annotated) {
    const count = Array.isArray(data.nodes) ? data.nodes.length : 0;
    lines.push("已叠加 ref 标注" + (count ? "（" + count + " 个）" : ""));
  }
  if (trimStr(data.warning)) lines.push("注意：" + trimStr(data.warning));
  if (data.data) lines.push("（图片已随结果返回，可直接查看）");
  return withMeta(lines, env);
}

function controlText(env) {
  const data = (env && env.data) || {};
  const lines = [controlLine(data.control)];
  if (data.control && trimStr(data.control.state) === "idle") {
    lines.push("租约已释放，遮罩已收起；用户可以直接操作浏览器。");
  }
  return withMeta(lines, env);
}

function genericText(env, tool) {
  const data = (env && env.data) || {};
  return withMeta(["工具 " + tool + " 返回：", jsonBlock(data)], env);
}

// ---------- 目标定位 ----------

function pickTarget(args) {
  const ref = trimStr(args.ref);
  const selector = trimStr(args.selector);
  if (ref && selector) throw new Error("ref 与 selector 只能给一个");
  if (!ref && !selector) {
    throw new Error("需要 ref 或 selector：先调用 browser_snapshot 拿到 ref");
  }
  return ref ? { ref } : { selector };
}

function pickClickTarget(args) {
  const ref = trimStr(args.ref);
  const selector = trimStr(args.selector);
  const text = trimStr(args.text);
  const count = (ref ? 1 : 0) + (selector ? 1 : 0) + (text ? 1 : 0);
  if (count === 0) {
    throw new Error("需要 ref、selector 或 text 之一来定位元素（先调用 browser_snapshot 拿 ref）");
  }
  if (count > 1) throw new Error("ref、selector、text 只能给一个");
  if (ref) return { ref };
  if (selector) return { selector };
  return { text };
}

function optionalTab(args) {
  const tab = trimStr(args.tab);
  return tab ? { tab } : {};
}

// ---------- 插件入口 ----------

export default function register(agent) {
  agent.registerTool({
    name: "browser_status",
    description:
      "查看 browser-use 浏览器状态：启动模式与进程、控制权（agent 租约 / 用户接管）、活动标签页、全部标签页、下载目录。开始浏览器任务前先用它确认应用可用；用户接管时写操作会被拒绝，要先让用户点 Resume agent。应用没启动且模式是 http 时，这次调用会顺带把它启动起来。",
    access: "read",
    parameters: { type: "object", properties: {}, required: [] },
    execute: async (_args, ctx) => {
      const rt = await ensureRuntime(ctx);
      const mode = startMode(ctx);
      const processState = rt.pid ? await pidState(ctx, rt.pid) : { known: false, alive: false, name: "" };
      const env = await rpc(ctx, "status", { include_tabs: true });
      const data = env.data || {};
      return {
        content: [
          { type: "text", text: statusText(env, rt, { mode: mode, pidState: processState }) },
        ],
        details: {
          control: data.control || null,
          active_tab_id: trimStr(data.active_tab_id),
          tabs: Array.isArray(data.tabs) ? data.tabs : [],
          mode: mode,
          pid: rt.pid,
          port: rt.port,
          process: processState,
          exe: resolvedExe,
        },
      };
    },
  });

  agent.registerTool({
    name: "browser_start",
    description:
      "检测并启动 browser-use：默认以 HTTP 模式（后台 --serve 实例，不动用户会话）启动；已经在跑则直接复用并返回实例信息（不会起第二个窗口）。mode=attach 时只检测、不启动；检测到进程但控制 API 无响应时也不会重复启动，而是返回可操作的错误。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        mode: {
          type: "string",
          enum: ["http", "attach"],
          description: "缺省用插件设置 start / 环境变量 BROWSER_USE_START，默认 http",
        },
        port: {
          type: "integer",
          minimum: 0,
          maximum: 65535,
          description: "指定控制端口；0 或省略表示自动分配",
        },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const requested = trimStr(args.mode).toLowerCase();
      const mode = requested === START_MODE_ATTACH
        ? START_MODE_ATTACH
        : requested === START_MODE_HTTP
          ? START_MODE_HTTP
          : startMode(ctx);
      const port = args.port != null ? intIn(args.port, 0, 65535, 0) : startPort(ctx);

      const live = await currentRuntime(ctx);
      if (live) {
        const state = live.pid ? await pidState(ctx, live.pid) : { known: false, alive: false, name: "" };
        return {
          content: [
            {
              type: "text",
              text:
                "browser-use 已在运行，未重复启动\n" +
                (live.version ? "版本：v" + live.version + "\n" : "") +
                (live.pid ? "pid：" + live.pid + "\n" : "") +
                "控制端口：" + live.port,
            },
          ],
          details: { started: false, pid: live.pid, port: live.port, version: live.version, process: state },
        };
      }
      if (mode === START_MODE_ATTACH) {
        throw new Error(
          "browser-use 没有在运行，而且 mode=attach 只连接、不自动启动。\n" +
            "请自己打开 browser-use，或改用 mode=http 让插件启动它。",
        );
      }
      const stale = await readRuntimeFile(ctx);
      if (stale && stale.pid) {
        const state = await pidState(ctx, stale.pid);
        if (state.alive && /browser-use/i.test(state.name)) {
          throw new Error(
            "检测到 browser-use 进程（pid " + stale.pid + "），但控制 API 无响应（runtime.json 过期或窗口卡住）。\n" +
              "不会重复启动（单实例锁会让新进程立即退出）；请关闭那个窗口后重试。",
          );
        }
      }
      const info = await startApp(ctx, port);
      return {
        content: [
          {
            type: "text",
            text:
              "已启动 browser-use（HTTP 模式，后台 --serve）\n" +
              (info.version ? "版本：v" + info.version + "\n" : "") +
              "pid：" + info.pid + "\n" +
              "控制端口：" + info.port +
              (resolvedExe ? "\n可执行文件：" + resolvedExe : ""),
          },
        ],
        details: { started: true, pid: info.pid, port: info.port, version: info.version, mode: "http" },
      };
    },
  });

  agent.registerTool({
    name: "browser_navigate",
    description:
      "在当前标签页打开网址，或前进/后退/刷新/停止。action=goto 时给 url（可省略协议，默认 https）；没有标签页时会自动新建一个。返回标签页的标题与 URL。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        url: { type: "string", description: "要打开的地址（action=goto 时必填）" },
        action: {
          type: "string",
          enum: ["goto", "back", "forward", "reload", "stop"],
          description: "缺省 goto",
        },
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        wait_until: {
          type: "string",
          enum: ["load", "domcontentloaded", "network_idle", "none"],
          description: "导航完成的判定，缺省 load",
        },
        timeout_ms: { type: "integer", minimum: 100, maximum: 110000, description: "缺省 30000" },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const action = oneOf(args.action, ["goto", "back", "forward", "reload", "stop"], "goto");
      const params = { action, ...optionalTab(args) };
      if (action === "goto") {
        const url = trimStr(args.url);
        if (!url) throw new Error("action=goto 需要 url");
        params.url = url;
      }
      const waitUntil = trimStr(args.wait_until);
      if (waitUntil) params.wait_until = waitUntil;
      // 显式给预算：默认 30s，超过出网桥 15s 上限的导航会自动走 CLI 通道。
      params.timeout_ms = args.timeout_ms != null
        ? intIn(args.timeout_ms, 100, MAX_CALL_TIMEOUT_MS, 30000)
        : 30000;
      let env = null;
      try {
        env = await rpc(ctx, "navigate", params, callTimeout(args));
      } catch (err) {
        if (action === "goto" && String(err && err.code) === "no_tab") {
          // 池子里一个标签页都没有：开一个新的就等于这次导航。
          env = await rpc(
            ctx,
            "tabs",
            { action: "new", url: params.url, activate: true },
            callTimeout(args),
          );
        } else {
          throw err;
        }
      }
      return {
        content: [{ type: "text", text: navigateText(env, action) }],
        details: { tab: env.tab || null, action },
      };
    },
  });

  agent.registerTool({
    name: "browser_tab",
    description:
      "新建、切换或关闭标签页。action=new 可顺便打开 url（会等页面加载完再返回标题）；select 切到指定 tab；close 默认只关 agent 打开的标签页，force=true 才关用户标签页。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        action: { type: "string", enum: ["new", "select", "close"] },
        url: { type: "string", description: "action=new 时要打开的地址" },
        tab: { type: "string", description: "目标标签页 id（select/close）" },
        activate: { type: "boolean", description: "新建后是否切过去，缺省 true" },
        force: { type: "boolean", description: "close 时是否允许关闭用户标签页" },
      },
      required: ["action"],
    },
    execute: async (args, ctx) => {
      const action = oneOf(args.action, ["new", "select", "close"], "new");
      const params = { action, ...optionalTab(args) };
      if (action === "new") {
        params.url = trimStr(args.url) || "about:blank";
        params.activate = typeof args.activate === "boolean" ? args.activate : true;
      }
      if (action === "close") params.force = args.force === true;
      const env = await rpc(ctx, "tabs", params, callTimeout(args));
      // new 带 url 时补一次「等加载」并刷新标签页信息：NewTab 返回时标题往往还是
      // "New Tab"，直接交给模型会误导它「页面没打开」。等加载失败不算错。
      if (action === "new" && params.url && params.url !== "about:blank") {
        const created = (env.data && env.data.tab) || null;
        if (created && trimStr(created.id)) {
          try {
            await rpc(ctx, "wait", { tab: created.id, for: "load", timeout_ms: 30000 }, 40000);
          } catch (_err) {
            // 页面慢/被拦都不影响「标签页已建好」这个事实。
          }
          try {
            const status = await rpc(ctx, "status", { include_tabs: true });
            const tabs = (status.data && status.data.tabs) || [];
            for (const tab of tabs) {
              if (tab.id === created.id) {
                env.data.tab = tab;
                break;
              }
            }
          } catch (_err) {
            // 刷新信息失败就用创建时的信息。
          }
        }
      }
      return {
        content: [{ type: "text", text: tabText(env, action) }],
        details: { action, tab: env.tab || null },
      };
    },
  });

  agent.registerTool({
    name: "browser_snapshot",
    description:
      "给页面拍语义快照：每行一个可交互元素，带稳定 ref（如 [e12] button \"提交\"）。ref 是后续 click/type 的定位依据；导航、重绘或重新加载后 ref 可能失效（stale_ref），此时重新快照即可。页面很长时用 filter（按 role/name/tag 收窄）或 scope=viewport。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        scope: { type: "string", enum: ["full", "viewport"], description: "缺省 full（含屏外元素）" },
        filter: { type: "string", description: "按 role / 可见名 / 标签过滤，如 button、搜索、input" },
        max_chars: { type: "integer", minimum: 1000, maximum: 50000, description: "缺省 12000" },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const params = { ...optionalTab(args) };
      const scope = trimStr(args.scope);
      if (scope) params.scope = scope;
      const filter = trimStr(args.filter);
      if (filter) params.filter = filter;
      if (args.max_chars != null) params.max_chars = intIn(args.max_chars, 1000, 50000, 12000);
      const env = await rpc(ctx, "snapshot", params, callTimeout(args));
      const data = env.data || {};
      return {
        content: [{ type: "text", text: snapshotText(env) }],
        details: {
          url: trimStr(data.url),
          title: trimStr(data.title),
          nodes_count: data.nodes_count || 0,
          truncated: data.truncated === true,
        },
      };
    },
  });

  agent.registerTool({
    name: "browser_read",
    description:
      "读取页面（或某个元素）的可读正文，适合读文章、列表、表格文本。要的是接口 JSON 而不是页面文本时，用 browser_network 抓请求更准。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        selector: { type: "string", description: "只读某个元素（CSS 选择器），缺省整页" },
        max_chars: { type: "integer", minimum: 1000, maximum: 50000, description: "缺省 12000" },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const params = { ...optionalTab(args) };
      const selector = trimStr(args.selector);
      if (selector) params.selector = selector;
      if (args.max_chars != null) params.max_chars = intIn(args.max_chars, 1000, 50000, 12000);
      const env = await rpc(ctx, "read", params, callTimeout(args));
      const data = env.data || {};
      return {
        content: [{ type: "text", text: readText(env) }],
        details: { url: trimStr(data.url), chars: data.chars || 0, truncated: data.truncated === true },
      };
    },
  });

  agent.registerTool({
    name: "browser_act",
    description:
      "对页面做一次交互：click / dblclick / type / press / select / hover / scroll / wait。目标用 ref（来自 browser_snapshot）或 selector；click/hover 还可用 text 按可见文本定位。type 要输入的内容放 value（text 是 click/hover 的可见文本定位）。click 后默认自动等待导航/加载。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        action: {
          type: "string",
          enum: ["click", "dblclick", "type", "press", "select", "hover", "scroll", "wait"],
        },
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        ref: { type: "string", description: "元素 ref，如 e12（先 browser_snapshot）" },
        selector: { type: "string", description: "CSS / text= / role= 选择器（与 ref 二选一）" },
        text: {
          type: "string",
          description: "click/hover 的可见文本定位（与 ref/selector 互斥）",
        },
        value: { type: "string", description: "type 要输入的文本" },
        key: { type: "string", description: "press 的按键或组合键，如 Enter、Control+A" },
        values: {
          type: "array",
          items: { type: "string" },
          description: "select 的选项（value 或可见文本，可多个）",
        },
        submit: { type: "boolean", description: "type 后按 Enter 提交" },
        clear: { type: "boolean", description: "type 前是否清空输入框，缺省 true" },
        direction: {
          type: "string",
          enum: ["up", "down", "left", "right"],
          description: "scroll 方向，缺省 down",
        },
        amount: { type: "number", description: "scroll 的像素数，缺省 600" },
        force: { type: "boolean", description: "跳过可点击性检查（元素被遮挡时）" },
        reveal: { type: "boolean", description: "点击前临时显示 opacity:0 的悬停容器，缺省 true" },
        wait_after: {
          type: "string",
          enum: ["auto", "load", "none"],
          description: "操作后是否等待加载，缺省 auto",
        },
        for: {
          type: "string",
          enum: ["load", "url", "selector", "text", "download", "time", "idle"],
          description: "wait 的条件（load/url/selector/text/download/time/idle）",
        },
        state: {
          type: "string",
          enum: ["visible", "attached", "hidden"],
          description: "wait 的目标状态，缺省 visible",
        },
        timeout_ms: { type: "integer", minimum: 100, maximum: 110000, description: "缺省 30000" },
      },
      required: ["action"],
    },
    execute: async (args, ctx) => {
      const action = oneOf(
        args.action,
        ["click", "dblclick", "type", "press", "select", "hover", "scroll", "wait"],
        "click",
      );
      const tab = optionalTab(args);
      const waitAfter = trimStr(args.wait_after);
      const timeout = args.timeout_ms != null
        ? intIn(args.timeout_ms, 100, MAX_CALL_TIMEOUT_MS, 30000)
        : null;
      const withTime = (params) => {
        if (waitAfter === "auto" || waitAfter === "load" || waitAfter === "none") {
          params.wait_after = waitAfter;
        }
        if (timeout != null) params.timeout_ms = timeout;
        return params;
      };

      let params = null;
      if (action === "click" || action === "dblclick") {
        params = withTime({
          ...tab,
          ...pickClickTarget(args),
          count: action === "dblclick" ? 2 : 1,
        });
        if (args.force === true) params.force = true;
        if (typeof args.reveal === "boolean") params.reveal = args.reveal;
      } else if (action === "hover") {
        params = { ...tab, ...pickClickTarget(args) };
        if (timeout != null) params.timeout_ms = timeout;
      } else if (action === "type") {
        const value = typeof args.value === "string" ? args.value : "";
        if (value === "") throw new Error("type 需要 value（要输入的文本）");
        params = withTime({ ...tab, ...pickTarget(args), text: value });
        if (args.submit === true) params.submit = true;
        if (typeof args.clear === "boolean") params.clear = args.clear;
      } else if (action === "press") {
        const key = trimStr(args.key);
        if (!key) throw new Error("press 需要 key，如 Enter、Control+A");
        params = { ...tab, key };
        if (timeout != null) params.timeout_ms = timeout;
      } else if (action === "select") {
        const values = Array.isArray(args.values)
          ? args.values.map((v) => String(v)).filter((v) => v !== "")
          : typeof args.values === "string" && args.values !== ""
            ? [args.values]
            : [];
        if (values.length === 0) throw new Error("select 需要 values（value 或可见文本）");
        params = { ...tab, ...pickTarget(args), values };
        if (timeout != null) params.timeout_ms = timeout;
      } else if (action === "scroll") {
        params = { ...tab, direction: trimStr(args.direction) || "down" };
        if (args.amount != null) params.amount = Number(args.amount) || 600;
        const ref = trimStr(args.ref);
        const selector = trimStr(args.selector);
        if (ref && selector) throw new Error("scroll 的 ref 与 selector 只能给一个");
        if (ref) params.ref = ref;
        if (selector) params.selector = selector;
      } else {
        const condition = oneOf(
          args["for"],
          ["load", "url", "selector", "text", "download", "time", "idle"],
          "load",
        );
        params = { ...tab, for: condition };
        const value = trimStr(args.value) || trimStr(args.text);
        if (value) params.value = value;
        const state = trimStr(args.state);
        if (state) params.state = state;
        // 等待天然可能超过 15s：显式预算（默认 30s），rpc 会据此走 CLI 通道。
        params.timeout_ms = timeout != null ? timeout : 30000;
      }

      const env = await rpc(ctx, action, params, callTimeout(args));
      return {
        content: [{ type: "text", text: actText(env, action) }],
        details: { action, url: trimStr((env.data || {}).url) },
      };
    },
  });

  agent.registerTool({
    name: "browser_screenshot",
    description:
      "给页面截图（默认叠加 ref 标注并直接返回图片）。适合视觉确认、布局/样式检查、给用户看结果；图上的 ref 与 browser_snapshot 里的 ref 一致。只需要文件路径时把 inline 设为 false。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        annotate: { type: "boolean", description: "是否叠加 ref 标注，缺省 true" },
        max_width: { type: "integer", minimum: 320, maximum: 4096, description: "最大宽度，缺省 1280" },
        inline: { type: "boolean", description: "是否把图片返回给模型，缺省 true" },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const params = { ...optionalTab(args) };
      params.annotate = typeof args.annotate === "boolean" ? args.annotate : true;
      params.max_width = intIn(args.max_width, 320, 4096, 1280);
      const inline = typeof args.inline === "boolean" ? args.inline : true;
      params.inline = inline;
      const env = await rpc(ctx, "screenshot", params, callTimeout(args));
      const data = env.data || {};
      const content = [{ type: "text", text: screenshotText(env) }];
      if (inline && typeof data.data === "string" && data.data !== "") {
        content.push({
          type: "image",
          mime: trimStr(data.mime_type) || "image/png",
          data: data.data,
        });
      }
      return {
        content,
        details: {
          path: trimStr(data.path),
          width: data.width || 0,
          height: data.height || 0,
          inline,
        },
      };
    },
  });

  agent.registerTool({
    name: "browser_eval",
    description:
      "在页面里执行一段 JavaScript 表达式并返回 JSON 结果（受产品的 agent.allow_eval 控制）。适合快速读一个值或做简单 DOM 查询；多步骤流程改用 browser_script。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        expression: { type: "string", description: "要执行的 JS 表达式，返回值要能 JSON 序列化" },
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        timeout_ms: { type: "integer", minimum: 100, maximum: 110000, description: "缺省 30000" },
      },
      required: ["expression"],
    },
    execute: async (args, ctx) => {
      const expression = typeof args.expression === "string" ? args.expression : "";
      if (!expression.trim()) throw new Error("expression 不能为空");
      const params = { expression, ...optionalTab(args) };
      if (args.timeout_ms != null) {
        params.timeout_ms = intIn(args.timeout_ms, 100, MAX_CALL_TIMEOUT_MS, 30000);
      }
      const env = await rpc(ctx, "eval", params, callTimeout(args));
      return {
        content: [{ type: "text", text: evalText(env) }],
        details: { url: trimStr((env.data || {}).url) },
      };
    },
  });

  agent.registerTool({
    name: "browser_script",
    description:
      "用一段 JS/TS 异步函数体跑多步浏览器流程：一个调用代替十几次点击，适合登录后翻页抓取、批量填写、循环提取。可用 await browser.active() 拿 Page（goto/snapshot/read/click/fill/type/press/select/hover/scroll/waitForURL/waitForSelector/waitForText/evaluate/requests/request/screenshot 等），用 return 返回一个小 JSON。脚本走 CLI 通道，可以跑满 timeout_ms（≤110s）；不要写没有上限的轮询。脚本里没有 fetch/fs/import/eval；默认 60s、200 次动作、64MB 堆。",
    access: "exec",
    parameters: {
      type: "object",
      properties: {
        source: { type: "string", description: "脚本正文（async 函数体，可用 await 与 return）" },
        args: { type: "object", description: "传给脚本的参数对象（脚本里用 args.xxx 取）" },
        language: { type: "string", enum: ["js", "ts"], description: "缺省 js" },
        timeout_ms: { type: "integer", minimum: 1000, maximum: 110000, description: "缺省 60000" },
        tab: { type: "string", description: "先切到这个标签页再跑" },
        release_on_finish: {
          type: "boolean",
          description: "跑完是否释放租约，缺省 true",
        },
      },
      required: ["source"],
    },
    execute: async (args, ctx) => {
      const source = typeof args.source === "string" ? args.source : "";
      if (!source.trim()) throw new Error("source 不能为空");
      const timeout = args.timeout_ms != null
        ? intIn(args.timeout_ms, 1000, MAX_CALL_TIMEOUT_MS, 60000)
        : 60000;
      const params = {
        source,
        language: trimStr(args.language) === "ts" ? "ts" : "js",
        timeout_ms: timeout,
        ...optionalTab(args),
      };
      if (args.args && typeof args.args === "object") params.args = args.args;
      if (typeof args.release_on_finish === "boolean") params.release_on_finish = args.release_on_finish;
      // 脚本走 CLI 通道（browser-use run），可以用满 timeout_ms；rpcViaCli 会再加进程开销。
      const env = await rpc(ctx, "script", params, timeout);
      return {
        content: [{ type: "text", text: scriptText(env) }],
        details: {
          script_id: trimStr((env.data || {}).script_id),
          actions: Array.isArray((env.data || {}).actions) ? (env.data || {}).actions.length : 0,
        },
      };
    },
  });

  agent.registerTool({
    name: "browser_network",
    description:
      "查看页面里 fetch/XHR 抓到的请求（URL、状态、耗时、请求/响应体）。SPA 的后端接口藏在 UI 后面时用它拿到 JSON API：先 click 触发请求，再 network list 或 wait；拿到的 JSON 比抓 DOM 稳。URL 里的 token/password/cookie 字段已被脱敏。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        action: { type: "string", enum: ["list", "get", "clear", "wait"], description: "缺省 list" },
        tab: { type: "string", description: "标签页 id（缺省用活动标签页）" },
        url_contains: { type: "string", description: "URL 子串过滤，如 /api/schedule" },
        method: { type: "string", description: "HTTP 方法过滤，如 POST" },
        status: { type: "integer", description: "状态码过滤，如 200" },
        since: { type: "string", description: "只看这个请求 id 之后的记录（n12）" },
        limit: { type: "integer", minimum: 1, maximum: 200, description: "最多返回条数，缺省 50" },
        include_body: { type: "boolean", description: "是否带上请求/响应体，缺省 false" },
        max_body: { type: "integer", description: "正文截断长度（字符）" },
        id: { type: "string", description: "action=get 时的请求 id" },
        timeout_ms: {
          type: "integer",
          minimum: 100,
          maximum: 110000,
          description: "action=wait 的等待上限，缺省 5000",
        },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const action = oneOf(args.action, ["list", "get", "clear", "wait"], "list");
      const params = { action, ...optionalTab(args) };
      const urlContains = trimStr(args.url_contains);
      if (urlContains) params.url_contains = urlContains;
      const method = trimStr(args.method);
      if (method) params.method = method;
      if (args.status != null) params.status = intIn(args.status, 100, 599, 0);
      const since = trimStr(args.since);
      if (since) params.since = since;
      if (args.limit != null) params.limit = intIn(args.limit, 1, 200, 50);
      if (typeof args.include_body === "boolean") params.include_body = args.include_body;
      if (args.max_body != null) params.max_body = intIn(args.max_body, 0, 1 << 20, 0);
      const id = trimStr(args.id);
      if (id) params.id = id;
      if (action === "wait") {
        params.timeout_ms = args.timeout_ms != null
          ? intIn(args.timeout_ms, 100, 60000, 5000)
          : 5000;
      }
      if ((action === "get" || action === "wait") && params.include_body === undefined) {
        params.include_body = true;
      }
      const env = await rpc(ctx, "network", params, callTimeout(args));
      return {
        content: [{ type: "text", text: networkText(env) }],
        details: {
          action,
          count: Array.isArray((env.data || {}).entries) ? (env.data || {}).entries.length : 0,
        },
      };
    },
  });

  agent.registerTool({
    name: "browser_control",
    description:
      "释放 agent 对浏览器的租约（收起遮罩、把控制权交还给用户）或查询控制状态。任务做完、需要用户接手、或要避免遮罩挡住用户时调用 release。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        action: { type: "string", enum: ["release", "state"], description: "缺省 state" },
        tab: { type: "string", description: "只释放某个标签页（缺省释放全部）" },
      },
      required: [],
    },
    execute: async (args, ctx) => {
      const action = oneOf(args.action, ["release", "state"], "state");
      const params = { action, ...optionalTab(args) };
      const env = await rpc(ctx, "control", params, callTimeout(args));
      return {
        content: [{ type: "text", text: controlText(env) }],
        details: { control: (env.data || {}).control || null, action },
      };
    },
  });

  agent.registerTool({
    name: "browser_call",
    description:
      "调用 browser-use 的长尾工具（参数原样透传）：cookies（读/写/导入登录态）、files（把本地文件塞进 input[type=file]）、frames（列 iframe / 切到某个 frame / 回主文档）、downloads（下载列表）、dialogs（页面 alert/confirm/prompt 记录）。注意：这是 write 级工具，只读用法（如 downloads）也会走审批。",
    access: "write",
    parameters: {
      type: "object",
      properties: {
        tool: {
          type: "string",
          enum: ["cookies", "files", "frames", "downloads", "dialogs"],
          description: "要调用的 browser-use 工具名",
        },
        args: { type: "object", description: "该工具的参数对象（见随包技能）" },
      },
      required: ["tool"],
    },
    execute: async (args, ctx) => {
      const tool = trimStr(args.tool);
      const allowed = ["cookies", "files", "frames", "downloads", "dialogs"];
      if (allowed.indexOf(tool) < 0) {
        throw new Error(
          "browser_call 只开放 " + allowed.join(" / ") + "；页面动作请用对应工具" +
            "（eval/script 必须走 browser_eval / browser_script 以便审批）",
        );
      }
      const params = args.args && typeof args.args === "object" ? args.args : {};
      const env = await rpc(ctx, tool, params, callTimeout(args));
      return {
        content: [{ type: "text", text: genericText(env, tool) }],
        details: { tool },
      };
    },
  });
}
