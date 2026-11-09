// generated from scheduler.ts (sha256:0317cb5e29ddb203) by plugins/official/build.mjs — do not edit
// scheduler 进程：cron-manager 的单例调度器（网关拉起，不随工作区增减）。
//
// 为什么是独立进程而不是 Core 内的 agent.every：
//   - Core 按工作区拉起、空闲回收，多工作区时每个 Core 都跑一份循环 → 同一任务被
//     触发多次；
//   - scheduler 是全局单例（scope=global），同一时刻只有一份到期判断；
//   - 它只做「读任务 → 判断到期 → 经调度层投递 → 写运行历史」，不解释工作区业务。
//
// 它与插件（Core 内）共用同一份 ctx.config 数据（$ADAGO_HOME/config/cron.json），
// 靠 globalconfig 的跨进程文件锁串行化写入。
//
// 凭据：网关在拉起本进程时注入 ADAGO_GATEWAY_CONTROL_URL/TOKEN（只带发送范围）。
// 令牌身份绑定 plugin:cron-manager，sender 由网关盖章，本进程无法冒充别的插件。

import { readFileSync, writeFileSync, renameSync, mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { homedir } from "node:os";

const TICK_MS = 30_000;
const MAX_TASKS = 200;
const MAX_NAME = 128;
const MAX_TEXT = 64 * 1024;
const MAX_RUNS = 20;
const CONFIG_FILE_VERSION = 1;

// ---- 配置读写（与 globalconfig 同一份信封格式）----

function configPath() {
  const root = String(process.env.ADAGO_HOME || "").trim();
  const base = root || join(homedir(), ".adago");
  return join(base, "config", "cron.json");
}

function readEnvelope(path) {
  let raw;
  try {
    raw = readFileSync(path, "utf8");
  } catch (e) {
    if (e && e.code === "ENOENT") {
      return { version: CONFIG_FILE_VERSION, values: {} };
    }
    throw e;
  }
  const parsed = JSON.parse(raw);
  if (!parsed || typeof parsed !== "object") throw new Error("cron 配置不是对象");
  if (!parsed.values || typeof parsed.values !== "object") parsed.values = {};
  if (!parsed.version) parsed.version = CONFIG_FILE_VERSION;
  return parsed;
}

// writeEnvelope 原子写：临时文件 + rename（与宿主同目录、同 0600 口径由宿主负责）。
function writeEnvelope(path, envelope) {
  const body = JSON.stringify(envelope, null, 2) + "\n";
  if (body.length > 4 * 1024 * 1024) throw new Error("cron 配置超过 4MB");
  mkdirSync(dirname(path), { recursive: true });
  const tmp = path + ".tmp-" + process.pid;
  writeFileSync(tmp, body, { mode: 0o600 });
  renameSync(tmp, path);
}

function loadTasks() {
  const path = configPath();
  const env = readEnvelope(path);
  const raw = env.values.tasks;
  if (!raw) return { path, envelope: env, tasks: [], warnings: [] };
  if (!Array.isArray(raw)) {
    return { path, envelope: env, tasks: [], warnings: ["tasks 配置不是数组，已忽略"] };
  }
  const tasks = [];
  const warnings = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    try {
      const fields = validateCron(String(item.cron || ""));
      if (!item.id || !item.name) throw new Error("缺少 id/name");
      tasks.push({
        ...item,
        enabled: item.enabled !== false,
        action: { type: "agent", text: String((item.action && item.action.text) || "") },
        runs: Array.isArray(item.runs) ? item.runs.slice(-MAX_RUNS) : [],
        _fields: fields,
      });
    } catch (e) {
      warnings.push("任务 " + String(item.id || item.name || "?") + " 已跳过：" + (e && e.message));
    }
  }
  return { path, envelope: env, tasks, warnings };
}

// saveRuns 把本进程「拥有」的字段（lastFiredAt/threadId/runs）合并回配置文件。
//
// 不能整份覆盖 tasks：管理页（Core 内插件经 ctx.config）同时也会改任务定义。
// 所以写之前重新读一次，只按 task id 打补丁；其余字段以磁盘为准。
// 残留窗口：读与 rename 之间若插件刚好写入，那次插件写入会被覆盖（窗口在毫秒级，
// 且只影响同一条任务的定义）。任务定义的真源始终是 ctx.config，本进程不新建任务。
function saveRuns(path, tasks) {
  const fresh = readEnvelope(path);
  if (!Array.isArray(fresh.values.tasks)) return;
  const byID = new Map();
  for (const t of tasks) {
    if (!t.id) continue;
    byID.set(t.id, {
      lastFiredAt: t.lastFiredAt || "",
      threadId: t.threadId || "",
      runs: (t.runs || []).slice(-MAX_RUNS),
    });
  }
  let changed = false;
  fresh.values.tasks = fresh.values.tasks.map((item) => {
    if (!item || typeof item !== "object" || !item.id) return item;
    const patch = byID.get(String(item.id));
    if (!patch) return item;
    changed = true;
    return { ...item, ...patch };
  });
  if (changed) writeEnvelope(path, fresh);
  // 下一次读取重新解析（插件可能同时改了 cron 表达式）。
  for (const t of tasks) delete t._fields;
}

// ---- 5 段 cron（与 index.ts 同一套规则）----

function fail(message) {
  const e = new Error(message);
  e.code = "ERR_INVALID_ARG_VALUE";
  throw e;
}

function fieldMatch(field, value, min, max) {
  const parts = String(field).split(",");
  if (parts.length === 0) fail("cron 字段为空");
  let matched = false;
  for (const part of parts) {
    const raw = part.trim();
    if (!raw) fail("cron 字段含空项");
    const slash = raw.split("/");
    if (slash.length > 2) fail("cron 步长格式非法: " + raw);
    const step = slash.length === 2 ? Number(slash[1]) : 1;
    if (!Number.isInteger(step) || step <= 0) fail("cron 步长必须是正整数: " + raw);
    let lo = 0;
    let hi = 0;
    const range = slash[0];
    if (range === "*") {
      lo = min;
      hi = max;
    } else if (range.includes("-")) {
      const pair = range.split("-");
      if (pair.length !== 2) fail("cron 范围格式非法: " + raw);
      lo = Number(pair[0]);
      hi = Number(pair[1]);
    } else {
      lo = Number(range);
      hi = lo;
    }
    if (!Number.isInteger(lo) || !Number.isInteger(hi) || lo < min || hi > max || lo > hi) {
      fail("cron 字段 " + raw + " 超出 " + min + "…" + max);
    }
    for (let v = lo; v <= hi; v += step) {
      if (v === value) matched = true;
    }
  }
  return matched;
}

function validateCron(expr) {
  const fields = String(expr || "").trim().split(/\s+/);
  if (fields.length !== 5) fail("cron 表达式需要 5 段：分 时 日 月 周");
  fieldMatch(fields[0], 0, 0, 59);
  fieldMatch(fields[1], 0, 0, 23);
  fieldMatch(fields[2], 1, 1, 31);
  fieldMatch(fields[3], 1, 1, 12);
  fieldMatch(fields[4], 0, 0, 7);
  return fields;
}

function cronMatches(fields, date) {
  if (!fieldMatch(fields[0], date.getMinutes(), 0, 59)) return false;
  if (!fieldMatch(fields[1], date.getHours(), 0, 23)) return false;
  if (!fieldMatch(fields[3], date.getMonth() + 1, 1, 12)) return false;
  const domAny = String(fields[2]) === "*";
  const dowAny = String(fields[4]) === "*" || String(fields[4]) === "*/1";
  const dom = fieldMatch(fields[2], date.getDate(), 1, 31);
  const dowRaw = date.getDay();
  const dow = fieldMatch(fields[4], dowRaw, 0, 7) || (dowRaw === 0 && fieldMatch(fields[4], 7, 0, 7));
  if (domAny && dowAny) return true;
  if (domAny) return dow;
  if (dowAny) return dom;
  return dom || dow;
}

function sameMinute(a, b) {
  return a.getFullYear() === b.getFullYear()
    && a.getMonth() === b.getMonth()
    && a.getDate() === b.getDate()
    && a.getHours() === b.getHours()
    && a.getMinutes() === b.getMinutes();
}

// ---- 投递（经网关调度层；sender 由令牌盖章）----

function controlEndpoint(op) {
  const url = String(process.env.ADAGO_GATEWAY_CONTROL_URL || "").trim();
  const token = String(process.env.ADAGO_GATEWAY_CONTROL_TOKEN || "").trim();
  if (!url || !token) return null;
  return { url: url.replace(/\/+$/, "") + "/internal/" + op, token };
}

async function dispatchSend(body) {
  const ep = controlEndpoint("dispatch/send");
  if (!ep) throw new Error("未接入网关调度层：缺少 ADAGO_GATEWAY_CONTROL_*");
  const res = await fetch(ep.url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: "Bearer " + ep.token,
    },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let parsed = null;
  try {
    parsed = JSON.parse(text);
  } catch {
    parsed = null;
  }
  if (!res.ok) {
    const code = parsed && parsed.error && parsed.error.code ? parsed.error.code : "upstream_error";
    const msg = parsed && parsed.error && parsed.error.message ? parsed.error.message : text.slice(0, 400);
    throw new Error(code + ": " + msg);
  }
  return parsed && parsed.receipt ? parsed.receipt : {};
}

// ---- 到期判断与运行历史 ----

function addRun(task, run) {
  const runs = (task.runs || []).concat([run]);
  task.runs = runs.slice(-MAX_RUNS);
}

function minuteKey(at) {
  return at.toISOString().slice(0, 16);
}

async function fire(task, at) {
  const started = Date.now();
  const text = String((task.action && task.action.text) || "").trim() || task.name;
  try {
    const receipt = await dispatchSend({
      workspace: String(task.workspace || "").trim(),
      threadId: String(task.threadId || "").trim() || undefined,
      title: task.name,
      text,
      mode: "start",
      // 同一任务同一分钟只投递一次：网关按这个键做幂等。
      idempotencyKey: "cron:" + task.id + ":" + minuteKey(at),
      taskId: task.id,
    });
    if (receipt.threadId) task.threadId = receipt.threadId;
    task.lastFiredAt = at.toISOString();
    addRun(task, {
      at: at.toISOString(),
      status: "started",
      durationMs: Date.now() - started,
      threadId: String(receipt.threadId || ""),
      detail: String(receipt.runId || ""),
    });
    return true;
  } catch (e) {
    task.lastFiredAt = at.toISOString();
    addRun(task, {
      at: at.toISOString(),
      status: "error",
      durationMs: Date.now() - started,
      detail: String((e && e.message) || e),
    });
    return false;
  }
}

async function tick() {
  const now = new Date();
  const { path, tasks } = loadTasks();
  let changed = false;
  for (const task of tasks) {
    if (!task.enabled) continue;
    let fields;
    try {
      fields = validateCron(task.cron);
    } catch {
      continue; // 非法任务已由 loadTasks 过滤；这里只是防御。
    }
    if (!cronMatches(fields, now)) continue;
    const last = task.lastFiredAt ? new Date(task.lastFiredAt) : null;
    if (last && sameMinute(last, now)) continue;
    await fire(task, now);
    changed = true;
  }
  if (changed) saveRuns(path, tasks);
}

function log(message) {
  // 标准输出进网关日志环（截断 + 脱敏），不写文件。
  console.log("[cron-scheduler] " + message);
}

async function main() {
  if (!controlEndpoint("dispatch/send")) {
    // 没有网关凭据就退出并说明原因：不假装调度成功。
    log("缺少 ADAGO_GATEWAY_CONTROL_URL/TOKEN，调度器退出");
    process.exit(2);
  }
  log("启动：每 " + TICK_MS / 1000 + "s 扫描一次，配置 " + configPath());
  let stopping = false;
  const stop = (signal) => {
    stopping = true;
    log("收到 " + signal + "，退出");
    process.exit(0);
  };
  process.on("SIGTERM", () => stop("SIGTERM"));
  process.on("SIGINT", () => stop("SIGINT"));

  const loop = async () => {
    if (stopping) return;
    try {
      await tick();
    } catch (e) {
      // 单次失败不能让调度器退出：记录后按下一拍继续。
      log("扫描失败：" + String((e && e.message) || e));
    }
    if (!stopping) setTimeout(loop, TICK_MS);
  };
  await loop();
}

main().catch((e) => {
  log("致命错误：" + String((e && e.message) || e));
  process.exit(1);
});
