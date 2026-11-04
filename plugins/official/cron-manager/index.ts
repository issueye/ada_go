// cron-manager 后端：定时任务管理页的数据面。
//
// 能力位 cron.admin（仅 builtin）提供 ctx.cron：
//   - list()                         任务列表（含 nextRunAt 预览）+ 警告；
//   - create(task)                   新建（id/时间戳由宿主生成）；
//   - update({id, task})             整体替换定义字段（runs/threadId 运行态不动）；
//   - remove({id})                   删除；
//   - setEnabled({id, enabled})      启停；
//   - trigger({id})                  打「立即运行」标记（cron 二进制下个 tick 消费）；
//   - runs({id, limit})              运行历史（最新在前）。
//
// 任务存于 ~/.adago/cron.json（宿主校验、0600 原子写、跨进程文件锁）。
// 动作只有 AI 任务（agent）：到期经网关 /api/cron/fire 送达 core；HTTP 动作已移除。
// 调度与执行在独立 cron 二进制（mcp_projects/cron），本插件只做管理面。
//
// 布局：页面骨架在 page.tsx，交互在 static/cron-manager.js。

interface CronAction {
  type: "agent";
  text?: string;
}

interface CronTask {
  id: string;
  name: string;
  enabled: boolean;
  cron: string;
  workspace?: string;
  action: CronAction;
}

interface RouteRequest {
  method: "GET" | "POST";
  path: string;
  query: Record<string, string>;
  body?: unknown;
}

interface CronContext {
  cron: {
    list(): { tasks: unknown[]; warnings: string[] };
    create(task: CronTask): { id: string };
    update(body: { id: string; task: CronTask }): { updated: boolean };
    remove(body: { id: string }): { removed: boolean };
    setEnabled(body: { id: string; enabled: boolean }): { updated: boolean };
    trigger(body: { id: string }): { triggered: boolean };
    runs(body: { id: string; limit?: number }): { runs: unknown[] };
  };
}

interface AgentAPI {
  route(
    method: "GET" | "POST",
    path: string,
    handler: (req: RouteRequest, ctx: CronContext) => unknown,
  ): void;
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

// taskOf 从请求体取任务定义字段（name/cron/workspace/action），丢弃其余。
// 动作只有 AI 任务一种；请求里带 type 时必须是 agent。
// workspace 为空表示默认工作区（网关列表第一个，即“综合工作区”）。
function taskOf(body: Record<string, unknown>): CronTask {
  const name = String(body.name || "").trim();
  const cron = String(body.cron || "").trim();
  const workspace = String(body.workspace || "").trim();
  const rawAction = body.action;
  if (!name) throw fail("需要任务名");
  if (!cron) throw fail("需要调度表达式（cron）");
  if (!rawAction || typeof rawAction !== "object" || Array.isArray(rawAction)) {
    throw fail("需要动作定义（action）");
  }
  const a = rawAction as Record<string, unknown>;
  if (a.type !== undefined && String(a.type) !== "agent") {
    throw fail("动作类型只能是 agent（AI 任务），HTTP 动作已移除");
  }
  const action: CronAction = { type: "agent", text: String(a.text || "") };
  return { id: "", name, enabled: Boolean((body as { enabled?: unknown }).enabled ?? true), cron, workspace, action };
}

export default function (agent: AgentAPI): void {
  agent.route("GET", "/state", (_req, ctx) => ctx.cron.list());

  agent.route("POST", "/create", (req, ctx) => {
    const task = taskOf(bodyOf(req));
    return ctx.cron.create(task);
  });

  agent.route("POST", "/update", (req, ctx) => {
    const body = bodyOf(req);
    // 管理页按 { id, task } 提交；task 才是任务定义字段。
    const rawTask = body.task;
    if (!rawTask || typeof rawTask !== "object" || Array.isArray(rawTask)) {
      throw fail("需要任务定义（task）");
    }
    return ctx.cron.update({ id: idOf(body), task: taskOf(rawTask as Record<string, unknown>) });
  });

  agent.route("POST", "/remove", (req, ctx) => ctx.cron.remove({ id: idOf(bodyOf(req)) }));

  agent.route("POST", "/toggle", (req, ctx) => {
    const body = bodyOf(req);
    return ctx.cron.setEnabled({ id: idOf(body), enabled: Boolean(body.enabled) });
  });

  agent.route("POST", "/trigger", (req, ctx) => ctx.cron.trigger({ id: idOf(bodyOf(req)) }));

  agent.route("GET", "/runs", (req, ctx) => {
    const id = String(req.query.id || "").trim();
    if (!id) throw fail("需要任务 id");
    const limitRaw = Number(req.query.limit || "0");
    return ctx.cron.runs({ id, limit: limitRaw > 0 ? Math.floor(limitRaw) : undefined });
  });
}
