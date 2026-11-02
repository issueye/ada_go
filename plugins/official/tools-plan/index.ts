// tools-plan —— 官方插件：维护当前线程的任务计划（05 §6.3、02 §5.4 的 tools-plan）。
//
// 计划不写进会话 entry：插件自己把每个会话的计划保存到工作区 `.adag/todo/`
// 下的 TODO_<threadId>.json。工具调用与前端只读动作都从 ctx.threadId 取对应会话的
// ID，读写同一个文件；结构化步骤只走 details 给界面（INV-10），模型只拿到一段
// 渲染好的文本。旧版本留在线程级 state KV 里的计划会在首次读文件时迁移一次，
// 之后文件是唯一真源。
//
// 为什么 access=read：.adag/todo/TODO_<threadId>.json 是本插件自己的会话状态文件（与原先
// 线程级 KV 同一语义），不是用户文件编辑，不应触发写/执行审批。

type PlanStatus = "pending" | "in_progress" | "done";

interface PlanStep {
  step: string;
  status: PlanStatus;
}

interface FsBridge {
  readText(path: string, opts?: { maxBytes?: number }): Promise<string>;
  writeText(path: string, text: string): Promise<{ bytes: number }>;
  exists(path: string): Promise<boolean>;
}

interface StateBridge {
  get<T>(key: string): Promise<T | null>;
}

interface ToolContext {
  workspace: string;
  threadId?: string;
  now?: string;
  fs?: FsBridge;
  state?: StateBridge;
  log: (level: string, msg: string, fields?: object) => void;
}

interface ToolDef {
  name: string;
  description: string;
  parameters: object;
  access: "read" | "write" | "exec" | "network";
  execute: (args: any, ctx: ToolContext) => unknown;
}

interface ActionDef {
  name: string;
  label?: string;
  mode: "read" | "write";
  confirm?: string;
  execute: (input: any, ctx: ToolContext) => unknown;
}

interface AgentAPI {
  registerTool(def: ToolDef): void;
  registerAction(def: ActionDef): void;
}

function fail(code: string, message: string): never {
  const e = new Error(message);
  (e as any).code = code;
  throw e;
}

function normalizeStatus(value: unknown): PlanStatus {
  const s = String(value || "").trim().toLowerCase();
  if (s === "in_progress" || s === "in-progress" || s === "active") return "in_progress";
  if (s === "done" || s === "completed") return "done";
  return "pending";
}

function normalizeSteps(raw: unknown): PlanStep[] {
  if (!Array.isArray(raw)) {
    const e = new TypeError("steps 必须是数组");
    (e as any).code = "ERR_INVALID_ARG_TYPE";
    throw e;
  }
  return raw.map((item: any, i: number) => {
    const text = typeof item === "string" ? item : String(item && item.step != null ? item.step : "");
    const step = text.trim();
    if (step.length === 0) {
      const e = new TypeError(`steps[${i}] 为空`);
      (e as any).code = "ERR_INVALID_ARG_TYPE";
      throw e;
    }
    return { step: step.slice(0, 500), status: normalizeStatus(item && item.status) };
  });
}

// stepsFromStored 读文件时更宽容：用户手改过的 TODO 文件里坏行直接跳过，
// 不让一条脏数据把整张卡片或整次读取拖垮。
function stepsFromStored(raw: unknown): PlanStep[] {
  if (!Array.isArray(raw)) return [];
  const out: PlanStep[] = [];
  for (let i = 0; i < raw.length; i += 1) {
    const item: any = raw[i];
    const text = typeof item === "string" ? item : String(item && item.step != null ? item.step : "");
    const step = text.trim();
    if (step.length === 0) continue;
    out.push({ step: step.slice(0, 500), status: normalizeStatus(item && item.status) });
  }
  return out;
}

function render(steps: PlanStep[]): string {
  if (steps.length === 0) return "计划已清空。";
  const mark = (s: PlanStatus) => (s === "done" ? "[x]" : s === "in_progress" ? "[~]" : "[ ]");
  const lines = steps.map((s, i) => `${i + 1}. ${mark(s.status)} ${s.step}`);
  const done = steps.filter((s) => s.status === "done").length;
  return `当前计划（${done}/${steps.length} 完成）：\n` + lines.join("\n");
}

// matchStep 判断 focus 是否与某一步骤文本互相覆盖（大小写不敏感）。
// 子串匹配（任一方向）即算命中：模型传入的 focus 通常是自然语言动作描述，
// 与计划步骤不必逐字相同。
function matchStep(step: string, focus: string): boolean {
  const a = step.toLowerCase();
  const b = focus.toLowerCase();
  return a.includes(b) || b.includes(a);
}

// reflectionFor 生成反思结论：基于当前计划与 focus（模型即将执行的动作描述），
// 确认是否仍走在计划路径上，并提醒只做当前步、不做计划外工作。这是
// reflect_plan 的核心：把「遵循规划路径、避免走偏或越做越多」变成可见引导。
function reflectionFor(steps: PlanStep[], focus: string): string {
  const next = steps.find((s) => s.status !== "done");
  const nextLine =
    steps.length === 0
      ? "当前没有计划。"
      : next
        ? `${steps.indexOf(next) + 1}. ${next.step}（${next.status === "in_progress" ? "进行中" : "待办"}）`
        : "全部步骤均已完成，无待办。";

  const lines: string[] = [];
  lines.push(`下一步应聚焦：${nextLine}`);
  lines.push("");
  lines.push("反思结论：");

  if (steps.length === 0) {
    lines.push("- 当前没有计划。请先 update_plan 建立任务计划，再开始执行，避免漫无目的地铺开。");
  } else if (!focus) {
    if (next) {
      lines.push(`- 请先完成下一步「${next.step}」，再进入后续步骤；不要跳到计划外的工作上。`);
    } else {
      lines.push("- 计划已全部完成。若任务尚未结束，先 update_plan 建立新计划，再继续执行。");
    }
  } else {
    const onPlan = steps.find((s) => s.status !== "done" && matchStep(s.step, focus));
    const alreadyDone = steps.find((s) => s.status === "done" && matchStep(s.step, focus));
    if (onPlan) {
      lines.push(`- focus「${focus}」命中计划内步骤「${onPlan.step}」，请继续完成该步，完成后再进入下一步。`);
    } else if (alreadyDone) {
      lines.push(`- focus「${focus}」对应的步骤「${alreadyDone.step}」已完成，不要重复执行；请转向下一步。`);
    } else {
      lines.push(`- focus「${focus}」未命中任何计划步骤，可能在做计划外工作。`);
      if (next) {
        lines.push(`- 请回到计划路径：先完成「${next.step}」，或先用 update_plan 调整计划，不要擅自扩大范围。`);
      } else {
        lines.push("- 请先用 update_plan 建立新计划，再继续执行。");
      }
    }
  }
  lines.push("- 只做当前步骤，不做计划外工作；发现新需求先记入计划，避免越做越多。");
  return lines.join("\n");
}

// planView 是本插件前端段（Todo 卡片）的结构化视图：total=0 表示当前没有计划。
function planView(steps: PlanStep[]): { done: number; total: number; steps: PlanStep[] } {
  const done = steps.filter((s) => s.status === "done").length;
  return { done, total: steps.length, steps };
}

function requireFs(ctx: ToolContext): FsBridge {
  if (!ctx.fs) {
    fail("ERR_ACCESS_DENIED", "tools-plan 需要能力 fs.read/fs.write（清单 backend.capabilities 未声明？）");
  }
  return ctx.fs!;
}

// 工具与动作都必须拿到当前会话 ID：任务计划按会话隔离，缺了它无法定位文件。
function requireThreadID(ctx: ToolContext): string {
  const id = String(ctx.threadId || "").trim();
  if (id.length === 0) {
    fail("ERR_INVALID_ARG_VALUE", "TODO 需要当前会话 id（ctx.threadId 为空）");
  }
  return id;
}

// .adag/todo/TODO_<threadId>.json：文件名带会话 ID，插件自己读写；目录由宿主挂载为
// /workspace（ctx.workspace）。ID 里的非法文件名字符做保守替换。
function todoPath(ctx: ToolContext): string {
  const threadID = requireThreadID(ctx);
  const safe = threadID.replace(/[^A-Za-z0-9_-]/g, "_");
  const workspace = String(ctx.workspace || "/workspace").replace(/\/+$/, "");
  return workspace + "/.adag/todo/TODO_" + safe + ".json";
}

async function loadSteps(ctx: ToolContext): Promise<PlanStep[]> {
  const fs = requireFs(ctx);
  const path = todoPath(ctx);
  if (!(await fs.exists(path))) {
    // 旧版本把计划放在线程级 state KV；首次读文件时顺手迁移一次，
    // 之后文件就是唯一真源（写失败也不影响本次返回旧计划）。
    const legacy = await loadLegacyState(ctx);
    if (legacy.length > 0) {
      try {
        await saveSteps(ctx, legacy);
      } catch (_err) {
        /* 迁移失败不致命：本次先用旧计划，下次工具调用会再试 */
      }
      return legacy;
    }
    return [];
  }
  const text = await fs.readText(path);
  if (!text.trim()) return [];
  let parsed: any;
  try {
    parsed = JSON.parse(text);
  } catch (_err) {
    fail("ERR_INVALID_ARG_VALUE", `TODO 文件不是合法 JSON：${path}`);
  }
  return stepsFromStored(Array.isArray(parsed) ? parsed : parsed && parsed.steps);
}

// loadLegacyState 只在文件还不存在时读一次旧 KV（state 能力仅为此保留）。
async function loadLegacyState(ctx: ToolContext): Promise<PlanStep[]> {
  if (!ctx.state) return [];
  try {
    const raw: any = await ctx.state.get("plan");
    return stepsFromStored(Array.isArray(raw) ? raw : raw && raw.steps);
  } catch (_err) {
    return [];
  }
}

async function saveSteps(ctx: ToolContext, steps: PlanStep[]): Promise<string> {
  const fs = requireFs(ctx);
  const threadID = requireThreadID(ctx);
  const path = todoPath(ctx);
  const body = {
    threadId: threadID,
    steps: steps,
    updatedAt: ctx.now || "",
  };
  await fs.writeText(path, JSON.stringify(body, null, 2) + "\n");
  return path;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "update_plan",
    description:
      "创建或更新当前会话的任务计划。传入完整步骤列表（会整体替换），每步标 pending/in_progress/done。适合把多步工作显式化并在推进中更新。",
    access: "read", // 只写插件自己的 .adag/todo/TODO_<threadId>.json（会话状态，不是用户文件编辑）
    parameters: {
      type: "object",
      properties: {
        steps: {
          type: "array",
          items: {
            type: "object",
            properties: {
              step: { type: "string", description: "步骤描述" },
              status: {
                type: "string",
                enum: ["pending", "in_progress", "done"],
                description: "步骤状态，缺省 pending",
              },
            },
            required: ["step"],
          },
        },
      },
      required: ["steps"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const steps = normalizeSteps(args && args.steps);
      const file = await saveSteps(ctx, steps);
      ctx.log("info", "plan saved", { steps: steps.length, threadId: ctx.threadId, file: file });
      return {
        content: [{ type: "text", text: render(steps) }],
        details: { steps, file },
      };
    },
  });

  agent.registerTool({
    name: "read_plan",
    description: "读取当前会话的任务计划。没有计划时返回空计划说明。",
    access: "read",
    parameters: { type: "object", properties: {} },
    execute: async (_args: any, ctx: ToolContext) => {
      const steps = await loadSteps(ctx);
      return {
        content: [{ type: "text", text: render(steps) }],
        details: { steps, file: todoPath(ctx) },
      };
    },
  });

  // reflect_plan：基于当前计划生成反思结论，引导 AI 遵循规划路径、避免走偏或越做越多。
  // 纯只读：只读取计划并对照 focus 输出反思，不修改计划（access=read，不触发写审批）。
  // focus 是模型即将执行的动作描述（可选）；省略时只提示聚焦下一步。
  agent.registerTool({
    name: "reflect_plan",
    description:
      "基于当前任务计划做一次反思：确认接下来该聚焦哪一步、是否仍在计划路径上、有没有走偏或越做越多。可选传入 focus（你即将执行的动作描述），工具会把它与计划步骤对照，指出是否命中计划内步骤、是否在重复已完成步骤、或是否在做计划外工作。适合在每轮动手前调用，确保只做当前步骤、不做计划外工作。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        focus: {
          type: "string",
          description: "即将执行的动作描述，用于与计划步骤对照（可选）",
        },
      },
      required: [],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const steps = await loadSteps(ctx);
      const focus = String((args && args.focus) || "").trim();
      ctx.log("info", "plan reflect", { steps: steps.length, focus: focus, threadId: ctx.threadId });
      return {
        content: [{ type: "text", text: reflectionFor(steps, focus) }],
        details: { steps, focus, file: todoPath(ctx) },
      };
    },
  });

  // 只读动作 plan：给本插件前端段的 Todo 卡片取数（7.5 的 action.invoke 桥）。
  // 返回 { plan }；total=0 表示当前会话没有计划（前端据此不画卡）。
  // 没有会话上下文（首页/概览页的插槽预取）时直接返回空计划：读动作失败
  // 会被宿主记成插件降级，不能因为“没有会话”把整个插件标坏。
  agent.registerAction({
    name: "plan",
    mode: "read",
    execute: async (_input: any, ctx: ToolContext) => {
      if (!String(ctx.threadId || "").trim()) {
        return { plan: planView([]) };
      }
      const steps = await loadSteps(ctx);
      return { plan: planView(steps) };
    },
  });
}
