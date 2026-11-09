// cron-manager 后端：定时任务的任务模型与管理面。
//
// 数据：ctx.config 的 "tasks" 键（机器级命名空间，0600/原子写/跨进程锁由宿主负责）。
// 调度：装配期经 ctx.processes 注册一个 global 单例进程 cron-manager/scheduler
//       （runtime=node，entry=dist/scheduler.js，start=on_enable，restart=on_failure）。
//       该进程自己解析 5 段 cron、判断到期，并经网关调度层投递消息——所以多个工作区
//       同时打开时也只有一份调度循环。禁用本插件后引用归零，进程随之停止。
// 运行历史：scheduler 把 lastFiredAt/threadId/runs 合并回同一份配置；本插件页面只读。
//
// 基座不再读取 cron.json、不再解析 cron 表达式、不再调度任务，也不再暴露
// /api/cron/fire。
//
// 管理工具（cron_*）全部 discovery=true：不注入模型工具清单，只经 find_tool/
// exec_tool 使用；审批链、超时与循环检测沿用各自描述符。

const MAX_TASKS = 200;
const MAX_NAME = 128;
const MAX_TEXT = 64 * 1024;
const MAX_RUNS = 20;
const SCHEDULER_NAME = "scheduler";
const SCHEDULER_ENTRY = "dist/scheduler.js";

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

interface ProcessStatus {
  id: string;
  owner: string;
  name: string;
  scope: string;
  workspace?: string;
  runtime: string;
  pid: number;
  running: boolean;
  restarts: number;
  refs: number;
  startMode: string;
  restart: string;
  lastError?: string;
  startedAt?: string;
}

interface ProcessesBridge {
  register(spec: {
    name: string;
    scope?: "global" | "workspace";
    runtime?: "node" | "binary";
    entry: string;
    args?: string[];
    start?: "on_enable" | "lazy";
    restart?: "no" | "on_failure" | "always";
    stopTimeoutMs?: number;
  }): Promise<ProcessStatus>;
  ensure(name: string, scope?: string): Promise<ProcessStatus>;
  stop(name: string, scope?: string): Promise<ProcessStatus>;
  status(name: string, scope?: string): Promise<ProcessStatus | undefined>;
  unregister(name: string, scope?: string): Promise<void>;
  list(): Promise<ProcessStatus[]>;
}

interface DispatchBridge {
  send(req: {
    workspace?: string;
    threadId?: string;
    title?: string;
    text: string;
    mode?: "auto" | "start" | "steer";
    idempotencyKey?: string;
    taskId?: string;
  }): Promise<{ workspace: string; threadId: string; runId?: string; mode: string }>;
}

interface CronContext {
  config: ConfigStore;
  fs: { readText(path: string): Promise<string> };
  dispatch?: DispatchBridge;
  processes?: ProcessesBridge;
}

interface ToolContext extends CronContext {
  threadId?: string;
}

interface ToolDef {
  name: string;
  description: string;
  parameters: unknown;
  access: "read" | "write" | "exec";
  discovery?: boolean;
  execute: (args: Record<string, unknown>, ctx: ToolContext) => Promise<unknown>;
}

interface AgentAPI {
  route(
    method: "GET" | "POST",
    path: string,
    handler: (req: RouteRequest, ctx: CronContext) => unknown,
  ): void;
  registerTool(def: ToolDef): void;
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
//
// 与 scheduler.ts 是同一套规则：两边各自独立实现，但语义必须一致，
// 否则「页面显示下次运行」和「实际触发」会给出不同的时间。

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

function cronMatches(expr: string, date: Date): boolean {
  const f = validateCron(expr);
  if (!fieldMatch(f[0], date.getMinutes(), 0, 59)) return false;
  if (!fieldMatch(f[1], date.getHours(), 0, 23)) return false;
  if (!fieldMatch(f[3], date.getMonth() + 1, 1, 12)) return false;
  const domAny = f[2] === "*";
  const dowAny = f[4] === "*" || f[4] === "*/1";
  const dom = fieldMatch(f[2], date.getDate(), 1, 31);
  const dowRaw = date.getDay();
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

// fireOnce 是「立即运行」：直接经调度层投递，不依赖 scheduler 进程在跑。
async function fireOnce(ctx: CronContext, task: CronTask, at: Date): Promise<void> {
  const started = Date.now();
  const text = String(task.action && task.action.text || "").trim() || task.name;
  try {
    if (!ctx.dispatch) throw new Error("未接入网关调度层（dispatch.send 不可用）");
    const receipt = await ctx.dispatch.send({
      workspace: String(task.workspace || "").trim(),
      threadId: String(task.threadId || "").trim() || undefined,
      title: task.name,
      text,
      mode: "start",
      idempotencyKey: `cron:${task.id}:${at.toISOString().slice(0, 16)}:manual`,
      taskId: task.id,
    });
    if (receipt && receipt.threadId) task.threadId = receipt.threadId;
    task.lastFiredAt = at.toISOString();
    addRun(task, {
      at: at.toISOString(), status: "started", durationMs: Date.now() - started,
      threadId: String((receipt && receipt.threadId) || ""),
      detail: String((receipt && receipt.runId) || ""),
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

// schedulerState 返回单例调度进程的状态（页面用来显示「调度器未运行」）。
async function schedulerState(ctx: CronContext): Promise<ProcessStatus | null> {
  if (!ctx.processes) return null;
  try {
    const st = await ctx.processes.status(SCHEDULER_NAME, "global");
    return st || null;
  } catch {
    return null;
  }
}

// ensureScheduler 在管理操作后确保调度进程在跑（崩溃后退避重启失败时也会在这里补一次）。
async function ensureScheduler(ctx: CronContext): Promise<void> {
  if (!ctx.processes) return;
  try {
    await ctx.processes.ensure(SCHEDULER_NAME, "global");
  } catch {
    // 调度器起不来不应让管理面整体失败：状态会显示 lastError。
  }
}

function schedulerView(st: ProcessStatus | null): unknown {
  if (!st) return { available: false };
  return {
    available: true,
    running: st.running,
    pid: st.pid,
    restarts: st.restarts,
    lastError: st.lastError || "",
    startedAt: st.startedAt || "",
  };
}

export default function (agent: AgentAPI, ctx: CronContext): void {
  // ---- 管理页路由 ----

  agent.route("GET", "/state", async (_req, rctx) => {
    const { tasks, warnings } = await loadTasks(rctx);
    const scheduler = await schedulerState(rctx);
    return { tasks: withPreview(tasks, new Date()), warnings, scheduler: schedulerView(scheduler) };
  });

  agent.route("POST", "/create", async (req, rctx) => {
    const { tasks } = await loadTasks(rctx);
    if (tasks.length >= MAX_TASKS) throw fail(`任务数超过上限 ${MAX_TASKS}`);
    const now = new Date().toISOString();
    const task: CronTask = {
      id: newID(), ...taskOf(bodyOf(req)), createdAt: now, updatedAt: now,
      runs: [],
    };
    tasks.push(task);
    await saveTasks(rctx, tasks);
    await ensureScheduler(rctx);
    return { id: task.id };
  });

  agent.route("POST", "/update", async (req, rctx) => {
    const body = bodyOf(req);
    const id = idOf(body);
    const rawTask = body.task;
    if (!rawTask || typeof rawTask !== "object" || Array.isArray(rawTask)) {
      throw fail("需要任务定义（task）");
    }
    const { tasks } = await loadTasks(rctx);
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
    await saveTasks(rctx, tasks);
    await ensureScheduler(rctx);
    return { updated: true };
  });

  agent.route("POST", "/remove", async (req, rctx) => {
    const id = idOf(bodyOf(req));
    const { tasks } = await loadTasks(rctx);
    const next = tasks.filter((t) => t.id !== id);
    if (next.length === tasks.length) throw fail("任务不存在: " + id);
    await saveTasks(rctx, next);
    return { removed: true };
  });

  agent.route("POST", "/toggle", async (req, rctx) => {
    const body = bodyOf(req);
    const id = idOf(body);
    const { tasks } = await loadTasks(rctx);
    const task = tasks.find((t) => t.id === id);
    if (!task) throw fail("任务不存在: " + id);
    task.enabled = Boolean(body.enabled);
    task.updatedAt = new Date().toISOString();
    await saveTasks(rctx, tasks);
    if (task.enabled) await ensureScheduler(rctx);
    return { updated: true };
  });

  agent.route("POST", "/trigger", async (req, rctx) => {
    const id = idOf(bodyOf(req));
    const { tasks } = await loadTasks(rctx);
    const task = tasks.find((t) => t.id === id);
    if (!task) throw fail("任务不存在: " + id);
    await fireOnce(rctx, task, new Date());
    await saveTasks(rctx, tasks);
    return { triggered: true };
  });

  agent.route("GET", "/runs", async (req, rctx) => {
    const id = String(req.query.id || "").trim();
    if (!id) throw fail("需要任务 id");
    const limitRaw = Number(req.query.limit || "0");
    const limit = limitRaw > 0 ? Math.min(Math.floor(limitRaw), MAX_RUNS) : MAX_RUNS;
    const { tasks } = await loadTasks(rctx);
    const task = tasks.find((t) => t.id === id);
    if (!task) throw fail("任务不存在: " + id);
    return { runs: (task.runs || []).slice(-limit) };
  });

  // ---- 单例调度进程（on_enable；崩溃按 on_failure 退避重启）----
  //
  // 装配期上下文里的 ctx.processes 只在网关部署下存在（CLI 直连没有控制面，
  // 能力不安装）。注册失败不阻塞装配：页面会显示「调度器不可用」。
  if (ctx && ctx.processes) {
    ctx.processes
      .register({
        name: SCHEDULER_NAME,
        scope: "global",
        runtime: "node",
        entry: SCHEDULER_ENTRY,
        start: "on_enable",
        restart: "on_failure",
      })
      .catch(() => {
        // 见上：装配不因此失败。
      });
  }

  // ---- 管理工具（全部 discovery=true）----

  agent.registerTool({
    name: "cron_list",
    description: "列出全部定时任务（名称、cron、启停、下次运行时间）与调度器状态。",
    parameters: { type: "object", properties: {}, additionalProperties: false },
    access: "read",
    discovery: true,
    async execute(_args, tctx) {
      const { tasks, warnings } = await loadTasks(tctx);
      const scheduler = await schedulerState(tctx);
      const lines = tasks.map((t) => {
        let next = "";
        if (t.enabled) {
          try {
            const at = nextAfter(t.cron, new Date());
            if (at) next = " 下次=" + at.toISOString();
          } catch {
            next = " (cron 非法)";
          }
        }
        return `- ${t.id} ${t.name} [${t.cron}] ${t.enabled ? "启用" : "停用"}${next}`;
      });
      const head = `共 ${tasks.length} 个任务；调度器 ${JSON.stringify(schedulerView(scheduler))}`;
      const tail = warnings.length ? "\n警告：\n- " + warnings.join("\n- ") : "";
      return {
        content: [{ type: "text", text: head + (lines.length ? "\n" + lines.join("\n") : "") + tail }],
        details: { tasks: withPreview(tasks, new Date()), scheduler: schedulerView(scheduler), warnings },
      };
    },
  });

  agent.registerTool({
    name: "cron_get",
    description: "读取单个定时任务的定义与最近运行历史。",
    parameters: {
      type: "object",
      properties: { id: { type: "string", description: "任务 id" } },
      required: ["id"],
      additionalProperties: false,
    },
    access: "read",
    discovery: true,
    async execute(args, tctx) {
      const id = String(args.id || "").trim();
      const { tasks } = await loadTasks(tctx);
      const task = tasks.find((t) => t.id === id);
      if (!task) throw fail("任务不存在: " + id);
      const runs = (task.runs || []).map(
        (r) => `- ${r.at} ${r.status}${r.threadId ? " thread=" + r.threadId : ""}${r.detail ? " " + r.detail : ""}`,
      );
      return {
        content: [{
          type: "text",
          text: `${task.name} [${task.cron}] ${task.enabled ? "启用" : "停用"}\n` +
            `提示词：${task.action.text}\n运行历史：\n${runs.length ? runs.join("\n") : "（还没有运行记录）"}`,
        }],
        details: { task },
      };
    },
  });

  agent.registerTool({
    name: "cron_create",
    description: "新建定时任务（name/cron/workspace/action.text）。",
    parameters: {
      type: "object",
      properties: {
        name: { type: "string", description: "任务名" },
        cron: { type: "string", description: "5 段 cron（分 时 日 月 周）" },
        workspace: { type: "string", description: "工作区绝对路径；空表示默认工作区" },
        text: { type: "string", description: "到期发给模型的任务提示词" },
        enabled: { type: "boolean", description: "是否立即启用，缺省 true" },
      },
      required: ["name", "cron", "text"],
      additionalProperties: false,
    },
    access: "write",
    discovery: true,
    async execute(args, tctx) {
      const { tasks } = await loadTasks(tctx);
      if (tasks.length >= MAX_TASKS) fail(`任务数超过上限 ${MAX_TASKS}`);
      const now = new Date().toISOString();
      const task: CronTask = {
        id: newID(),
        ...taskOf({
          name: args.name, cron: args.cron, workspace: args.workspace,
          enabled: args.enabled, action: { type: "agent", text: args.text },
        }),
        createdAt: now, updatedAt: now, runs: [],
      };
      tasks.push(task);
      await saveTasks(tctx, tasks);
      await ensureScheduler(tctx);
      return {
        content: [{ type: "text", text: `已创建任务 ${task.id}（${task.name} [${task.cron}]）` }],
        details: { id: task.id },
      };
    },
  });

  agent.registerTool({
    name: "cron_update",
    description: "修改任务定义；未传 enabled 时保留原启停状态。",
    parameters: {
      type: "object",
      properties: {
        id: { type: "string", description: "任务 id" },
        name: { type: "string" },
        cron: { type: "string" },
        workspace: { type: "string" },
        text: { type: "string" },
        enabled: { type: "boolean" },
      },
      required: ["id"],
      additionalProperties: false,
    },
    access: "write",
    discovery: true,
    async execute(args, tctx) {
      const id = String(args.id || "").trim();
      const { tasks } = await loadTasks(tctx);
      const idx = tasks.findIndex((t) => t.id === id);
      if (idx < 0) fail("任务不存在: " + id);
      const prev = tasks[idx];
      const next = taskOf({
        name: args.name === undefined ? prev.name : args.name,
        cron: args.cron === undefined ? prev.cron : args.cron,
        workspace: args.workspace === undefined ? prev.workspace : args.workspace,
        enabled: args.enabled,
        action: { type: "agent", text: args.text === undefined ? prev.action.text : args.text },
      });
      if (args.enabled === undefined) next.enabled = prev.enabled;
      tasks[idx] = {
        ...prev, ...next, id,
        threadId: prev.threadId, lastFiredAt: prev.lastFiredAt, runs: prev.runs,
        createdAt: prev.createdAt, updatedAt: new Date().toISOString(),
      };
      await saveTasks(tctx, tasks);
      await ensureScheduler(tctx);
      return {
        content: [{ type: "text", text: `已更新任务 ${id}` }],
        details: { updated: true },
      };
    },
  });

  agent.registerTool({
    name: "cron_delete",
    description: "删除定时任务。",
    parameters: {
      type: "object",
      properties: { id: { type: "string", description: "任务 id" } },
      required: ["id"],
      additionalProperties: false,
    },
    access: "write",
    discovery: true,
    async execute(args, tctx) {
      const id = String(args.id || "").trim();
      const { tasks } = await loadTasks(tctx);
      const next = tasks.filter((t) => t.id !== id);
      if (next.length === tasks.length) fail("任务不存在: " + id);
      await saveTasks(tctx, next);
      return {
        content: [{ type: "text", text: `已删除任务 ${id}` }],
        details: { removed: true },
      };
    },
  });

  agent.registerTool({
    name: "cron_set_enabled",
    description: "启用或停用定时任务。",
    parameters: {
      type: "object",
      properties: {
        id: { type: "string", description: "任务 id" },
        enabled: { type: "boolean", description: "true 启用，false 停用" },
      },
      required: ["id", "enabled"],
      additionalProperties: false,
    },
    access: "write",
    discovery: true,
    async execute(args, tctx) {
      const id = String(args.id || "").trim();
      const { tasks } = await loadTasks(tctx);
      const task = tasks.find((t) => t.id === id);
      if (!task) fail("任务不存在: " + id);
      task.enabled = Boolean(args.enabled);
      task.updatedAt = new Date().toISOString();
      await saveTasks(tctx, tasks);
      if (task.enabled) await ensureScheduler(tctx);
      return {
        content: [{ type: "text", text: `任务 ${id} 已${task.enabled ? "启用" : "停用"}` }],
        details: { enabled: task.enabled },
      };
    },
  });

  agent.registerTool({
    name: "cron_run",
    description: "立即执行一次任务（不受启停状态影响，会触发一轮对话）。",
    parameters: {
      type: "object",
      properties: { id: { type: "string", description: "任务 id" } },
      required: ["id"],
      additionalProperties: false,
    },
    access: "exec",
    discovery: true,
    async execute(args, tctx) {
      const id = String(args.id || "").trim();
      const { tasks } = await loadTasks(tctx);
      const task = tasks.find((t) => t.id === id);
      if (!task) fail("任务不存在: " + id);
      await fireOnce(tctx, task, new Date());
      await saveTasks(tctx, tasks);
      const last = (task.runs || []).slice(-1)[0];
      return {
        content: [{
          type: "text",
          text: `已触发任务 ${id}：${last ? last.status + (last.detail ? " " + last.detail : "") : "未记录"}`,
        }],
        details: { taskId: id, threadId: task.threadId || "", run: last || null },
      };
    },
  });
}
