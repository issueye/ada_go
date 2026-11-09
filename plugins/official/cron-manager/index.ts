// cron-manager 后端：定时任务的任务模型、调度与运行历史都在本插件。
//
// 数据：ctx.config 的 "tasks" 键（机器级命名空间，0600/原子写/跨进程锁由宿主负责）。
// 调度：agent.every(30s, tick) 是通用定时器；本插件自己解析 5 段 cron 表达式、
//       判断到期并调用 ctx.turn.invoke 触发一轮对话。
// 运行历史：写回任务条目的 runs（最多 20 条）；threadId 复用一个专用线程。
//
// 基座不再读取 cron.json、不再解析 cron 表达式、不再调度任务，也不再暴露
// /api/cron/fire；禁用本插件后定时任务整体消失。

const MAX_TASKS = 200;
const MAX_NAME = 128;
const MAX_TEXT = 64 * 1024;
const MAX_RUNS = 20;
const TICK_MS = 30_000;

interface CronTask {
  id: string;
  name: string;
  enabled: boolean;
  cron: string;
  workspace?: string;
  action: { type: "agent"; text?: string };
  threadId?: string;
  lastFiredAt?: string;
  createdAt?: string;
  updatedAt?: string;
  runs?: CronRun[];
}

interface CronRun {
  at: string;
  status: string;
  durationMs?: number;
  detail?: string;
  threadId?: string;
}

interface RouteRequest {
  method: "GET" | "POST";
  path: string;
  query: Record<string, string>;
  body?: unknown;
}

interface ConfigStore {
  get(key: string): Promise<unknown>;
  set(key: string, value: unknown): Promise<void>;
  path(): string;
}

interface CronContext {
  config: ConfigStore;
  fs: { readText(path: string): Promise<string> };
  turn: {
    invoke(req: { threadId?: string; title?: string; prompt: string }): Promise<{ threadId: string; runId: string }>;
  };
}

interface AgentAPI {
  route(
    method: "GET" | "POST",
    path: string,
    handler: (req: RouteRequest, ctx: CronContext) => unknown,
  ): void;
  every(ms: number, handler: (req: unknown, ctx: CronContext) => unknown): void;
}

function fail(message: string): never {
  const e = new Error(message);
  (e as any).code = "ERR_INVALID_ARG_VALUE";
  throw e;
}

function bodyOf(req: RouteRequest): Record<string, unknown> {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw fail("请求体必须是 JSON 对象");
  }
  return body as Record<string, unknown>;
}

function idOf(body: Record<string, unknown>): string {
  const id = String(body.id || "").trim();
  if (!id) throw fail("需要任务 id");
  return id;
}

// ---- 5 段 cron 解析（分 时 日 月 周；支持 *、数字、a-b、*/n、列表）----

function fieldMatch(field: string, value: number, min: number, max: number): boolean {
  const parts = field.split(",");
  if (parts.length === 0) throw fail("cron 字段为空");
  let matched = false;
  for (const part of parts) {
    const raw = part.trim();
    if (!raw) throw fail("cron 字段含空项");
    const slash = raw.split("/");
    if (slash.length > 2) throw fail("cron 步长格式非法: " + raw);
    const step = slash.length === 2 ? Number(slash[1]) : 1;
    if (!Number.isInteger(step) || step <= 0) throw fail("cron 步长必须是正整数: " + raw);
    let lo = 0;
    let hi = 0;
    const range = slash[0];
    if (range === "*") {
      lo = min;
      hi = max;
    } else if (range.includes("-")) {
      const pair = range.split("-");
      if (pair.length !== 2) throw fail("cron 范围格式非法: " + raw);
      lo = Number(pair[0]);
      hi = Number(pair[1]);
    } else {
      lo = Number(range);
      hi = lo;
    }
    if (!Number.isInteger(lo) || !Number.isInteger(hi) || lo < min || hi > max || lo > hi) {
      throw fail(`cron 字段 ${raw} 超出 ${min}…${max}`);
    }
    for (let v = lo; v <= hi; v += step) {
      if (v === value) matched = true;
    }
  }
  return matched;
}

function validateCron(expr: string): string[] {
  const fields = String(expr || "").trim().split(/\s+/);
  if (fields.length !== 5) throw fail("cron 表达式需要 5 段：分 时 日 月 周");
  fieldMatch(fields[0], 0, 0, 59);
  fieldMatch(fields[1], 0, 0, 23);
  fieldMatch(fields[2], 1, 1, 31);
  fieldMatch(fields[3], 1, 1, 12);
  fieldMatch(fields[4], 0, 0, 7);
  return fields;
}

function dowOf(date: Date): number {
  return date.getDay();
}

function cronMatches(expr: string, date: Date): boolean {
  const f = validateCron(expr);
  if (!fieldMatch(f[0], date.getMinutes(), 0, 59)) return false;
  if (!fieldMatch(f[1], date.getHours(), 0, 23)) return false;
  if (!fieldMatch(f[3], date.getMonth() + 1, 1, 12)) return false;
  const domAny = f[2] === "*";
  const dowAny = f[4] === "*" || f[4] === "*/1";
  const dom = fieldMatch(f[2], date.getDate(), 1, 31);
  const dowRaw = dowOf(date);
  const dow = fieldMatch(f[4], dowRaw, 0, 7) || (dowRaw === 0 && fieldMatch(f[4], 7, 0, 7));
  if (domAny && dowAny) return true;
  if (domAny) return dow;
  if (dowAny) return dom;
  return dom || dow;
}

function nextAfter(expr: string, after: Date): Date | null {
  validateCron(expr);
  const d = new Date(after.getTime());
  d.setSeconds(0, 0);
  d.setMinutes(d.getMinutes() + 1);
  const limit = 366 * 24 * 60;
  for (let i = 0; i < limit; i += 1) {
    if (cronMatches(expr, d)) return d;
    d.setMinutes(d.getMinutes() + 1);
  }
  return null;
}

function sameMinute(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear()
    && a.getMonth() === b.getMonth()
    && a.getDate() === b.getDate()
    && a.getHours() === b.getHours()
    && a.getMinutes() === b.getMinutes();
}

// ---- 任务模型 ----

function newID(): string {
  return "cron_" + Date.now().toString(36) + "_" + Math.random().toString(36).slice(2, 10);
}

function taskOf(body: Record<string, unknown>): Omit<CronTask, "id"> {
  const name = String(body.name || "").trim();
  const cron = String(body.cron || "").trim();
  const workspace = String(body.workspace || "").trim();
  const rawAction = body.action;
  if (!name) throw fail("需要任务名");
  if (name.length > MAX_NAME) throw fail(`任务名超过 ${MAX_NAME} 字节`);
  validateCron(cron);
  if (!rawAction || typeof rawAction !== "object" || Array.isArray(rawAction)) {
    throw fail("需要动作定义（action）");
  }
  const a = rawAction as Record<string, unknown>;
  if (a.type !== undefined && String(a.type) !== "agent") {
    throw fail("动作类型只能是 agent（AI 任务）");
  }
  const text = String(a.text || "");
  if (text.length > MAX_TEXT) throw fail("提示词超过 64KB");
  return {
    name,
    enabled: body.enabled === undefined ? true : Boolean(body.enabled),
    cron,
    workspace,
    action: { type: "agent", text },
  };
}

// migrateOld 一次性导入旧版 ~/.adago/cron.json（基座已不再读取它）。
// 只在当前命名空间还没有 tasks 时执行；失败静默，不阻塞空态。
async function migrateOld(ctx: CronContext): Promise<CronTask[]> {
  let oldPath = "";
  try {
    const cfg = String(ctx.config.path() || "");
    const sep = cfg.includes("\\") ? "\\" : "/";
    const home = cfg.replace(/[\\/]config[\\/][^\\/]+$/, "");
    if (!home || home === cfg) return [];
    oldPath = home.replace(/[\\/]+$/, "") + sep + "cron.json";
  } catch {
    return [];
  }
  try {
    const text = await ctx.fs.readText(oldPath);
    const parsed = JSON.parse(text);
    const rawTasks = parsed && Array.isArray(parsed.tasks) ? parsed.tasks : [];
    const tasks: CronTask[] = [];
    for (const item of rawTasks) {
      if (!item || typeof item !== "object") continue;
      const t = item as any;
      try {
        validateCron(String(t.cron || ""));
        if (!t.id || !t.name) continue;
        tasks.push({
          id: String(t.id), name: String(t.name), enabled: t.enabled !== false,
          cron: String(t.cron), workspace: String(t.workspace || ""),
          action: { type: "agent", text: String(t.action && t.action.text || "") },
          threadId: String(t.threadId || ""),
          lastFiredAt: String(t.lastFiredAt || ""),
          createdAt: String(t.createdAt || ""), updatedAt: String(t.updatedAt || ""),
          runs: Array.isArray(t.runs) ? t.runs.slice(-MAX_RUNS) : [],
        });
      } catch {
        // 单条损坏只跳过。
      }
    }
    if (tasks.length > 0) await ctx.config.set("tasks", tasks);
    return tasks;
  } catch {
    return [];
  }
}

async function loadTasks(ctx: CronContext): Promise<{ tasks: CronTask[]; warnings: string[] }> {
  const raw = await ctx.config.get("tasks");
  if (!raw) {
    const migrated = await migrateOld(ctx);
    return { tasks: migrated, warnings: [] };
  }
  if (!Array.isArray(raw)) return { tasks: [], warnings: ["tasks 配置不是数组，已忽略"] };
  const tasks: CronTask[] = [];
  const warnings: string[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const t = item as CronTask;
    try {
      validateCron(String(t.cron || ""));
      if (!t.id || !t.name) throw new Error("缺少 id/name");
      tasks.push({
        ...t,
        enabled: t.enabled !== false,
        action: { type: "agent", text: String(t.action && t.action.text || "") },
        runs: Array.isArray(t.runs) ? t.runs.slice(-MAX_RUNS) : [],
      });
    } catch (e) {
      warnings.push(`任务 ${String(t.id || t.name || "?")} 已跳过：${(e as Error).message}`);
    }
  }
  return { tasks, warnings };
}

async function saveTasks(ctx: CronContext, tasks: CronTask[]): Promise<void> {
  await ctx.config.set("tasks", tasks);
}

function addRun(task: CronTask, run: CronRun): void {
  const runs = (task.runs || []).concat([run]);
  task.runs = runs.slice(-MAX_RUNS);
}

async function fire(ctx: CronContext, task: CronTask, at: Date): Promise<void> {
  const started = Date.now();
  try {
    const res = await ctx.turn.invoke({
      threadId: task.threadId || undefined,
      title: task.name,
      prompt: String(task.action && task.action.text || "").trim() || task.name,
    });
    task.threadId = res.threadId;
    task.lastFiredAt = at.toISOString();
    addRun(task, {
      at: at.toISOString(), status: "started", durationMs: Date.now() - started,
      threadId: res.threadId, detail: res.runId,
    });
  } catch (e) {
    task.lastFiredAt = at.toISOString();
    addRun(task, {
      at: at.toISOString(), status: "error", durationMs: Date.now() - started,
      detail: String((e as Error).message || e),
    });
  }
}

function withPreview(tasks: CronTask[], now: Date): unknown[] {
  return tasks.map((t) => {
    let nextRunAt = "";
    if (t.enabled) {
      try {
        const next = nextAfter(t.cron, now);
        if (next) nextRunAt = next.toISOString();
      } catch {
        // 已跳过非法任务；这里只影响预览。
      }
    }
    return { ...t, nextRunAt };
  });
}

export default function (agent: AgentAPI): void {
  agent.route("GET", "/state", async (_req, ctx) => {
    const { tasks, warnings } = await loadTasks(ctx);
    return { tasks: withPreview(tasks, new Date()), warnings };
  });

  agent.route("POST", "/create", async (req, ctx) => {
    const { tasks } = await loadTasks(ctx);
    if (tasks.length >= MAX_TASKS) throw fail(`任务数超过上限 ${MAX_TASKS}`);
    const now = new Date().toISOString();
    const task: CronTask = {
      id: newID(), ...taskOf(bodyOf(req)), createdAt: now, updatedAt: now,
      runs: [],
    };
    tasks.push(task);
    await saveTasks(ctx, tasks);
    return { id: task.id };
  });

  agent.route("POST", "/update", async (req, ctx) => {
    const body = bodyOf(req);
    const id = idOf(body);
    const rawTask = body.task;
    if (!rawTask || typeof rawTask !== "object" || Array.isArray(rawTask)) {
      throw fail("需要任务定义（task）");
    }
    const { tasks } = await loadTasks(ctx);
    const idx = tasks.findIndex((t) => t.id === id);
    if (idx < 0) throw fail("任务不存在: " + id);
    const next = taskOf(rawTask as Record<string, unknown>);
    // 编辑表单没有带 enabled 时保留原启停状态，避免“停用的任务一保存又跑起来”。
    if ((rawTask as Record<string, unknown>).enabled === undefined) {
      next.enabled = tasks[idx].enabled;
    }
    tasks[idx] = {
      ...tasks[idx], ...next,
      id, threadId: tasks[idx].threadId, lastFiredAt: tasks[idx].lastFiredAt,
      runs: tasks[idx].runs, createdAt: tasks[idx].createdAt,
      updatedAt: new Date().toISOString(),
    };
    await saveTasks(ctx, tasks);
    return { updated: true };
  });

  agent.route("POST", "/remove", async (req, ctx) => {
    const id = idOf(bodyOf(req));
    const { tasks } = await loadTasks(ctx);
    const next = tasks.filter((t) => t.id !== id);
    if (next.length === tasks.length) throw fail("任务不存在: " + id);
    await saveTasks(ctx, next);
    return { removed: true };
  });

  agent.route("POST", "/toggle", async (req, ctx) => {
    const body = bodyOf(req);
    const id = idOf(body);
    const { tasks } = await loadTasks(ctx);
    const task = tasks.find((t) => t.id === id);
    if (!task) throw fail("任务不存在: " + id);
    task.enabled = Boolean(body.enabled);
    task.updatedAt = new Date().toISOString();
    await saveTasks(ctx, tasks);
    return { updated: true };
  });

  agent.route("POST", "/trigger", async (req, ctx) => {
    const id = idOf(bodyOf(req));
    const { tasks } = await loadTasks(ctx);
    const task = tasks.find((t) => t.id === id);
    if (!task) throw fail("任务不存在: " + id);
    await fire(ctx, task, new Date());
    await saveTasks(ctx, tasks);
    return { triggered: true };
  });

  agent.route("GET", "/runs", async (req, ctx) => {
    const id = String(req.query.id || "").trim();
    if (!id) throw fail("需要任务 id");
    const limitRaw = Number(req.query.limit || "0");
    const limit = limitRaw > 0 ? Math.min(Math.floor(limitRaw), MAX_RUNS) : MAX_RUNS;
    const { tasks } = await loadTasks(ctx);
    const task = tasks.find((t) => t.id === id);
    if (!task) throw fail("任务不存在: " + id);
    return { runs: (task.runs || []).slice(-limit) };
  });

  // 通用定时器：每 30s 检查一次到期任务；同一条任务同一分钟只触发一次。
  agent.every(TICK_MS, async (_req, ctx) => {
    const { tasks } = await loadTasks(ctx);
    const now = new Date();
    let changed = false;
    for (const task of tasks) {
      if (!task.enabled) continue;
      if (!cronMatches(task.cron, now)) continue;
      const last = task.lastFiredAt ? new Date(task.lastFiredAt) : null;
      if (last && sameMinute(last, now)) continue;
      await fire(ctx, task, now);
      changed = true;
    }
    if (changed) await saveTasks(ctx, tasks);
  });
}
