// tools-plan —— 官方插件：维护当前线程的任务计划（05 §6.3、02 §5.4 的 tools-plan）。
//
// 计划是线程级 KV（能力位 state），不是模型上下文：工具只返回一段文本给模型，
// 结构化步骤走 details 给界面（INV-10）。这样模型不必重新读一遍整盘计划。
//
// 类型形状与 agent:api 一致，本地声明避免官方插件依赖 sdk 的类型包。

type PlanStatus = "pending" | "in_progress" | "done";

interface PlanStep {
  step: string;
  status: PlanStatus;
}

interface StateBridge {
  get<T>(key: string): Promise<T | null>;
  set(key: string, value: unknown): Promise<void>;
}

interface ToolContext {
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

interface AgentAPI {
  registerTool(def: ToolDef): void;
}

const STATE_KEY = "plan";

const STATUSES: PlanStatus[] = ["pending", "in_progress", "done"];

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

function render(steps: PlanStep[]): string {
  if (steps.length === 0) return "计划已清空。";
  const mark = (s: PlanStatus) => (s === "done" ? "[x]" : s === "in_progress" ? "[~]" : "[ ]");
  const lines = steps.map((s, i) => `${i + 1}. ${mark(s.status)} ${s.step}`);
  const done = steps.filter((s) => s.status === "done").length;
  return `当前计划（${done}/${steps.length} 完成）：\n` + lines.join("\n");
}

function requireState(ctx: ToolContext): StateBridge {
  if (!ctx.state) {
    const e = new Error("tools-plan 需要能力 state（清单 backend.capabilities 未声明？）");
    (e as any).code = "ERR_ACCESS_DENIED";
    throw e;
  }
  return ctx.state;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "update_plan",
    description:
      "创建或更新当前线程的任务计划。传入完整步骤列表（会整体替换），每步标 pending/in_progress/done。适合把多步工作显式化并在推进中更新。",
    access: "read", // 线程级 KV 不算外部效果（05 §6.3）
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
      await requireState(ctx).set(STATE_KEY, steps);
      ctx.log("info", "plan updated", { steps: steps.length });
      return {
        content: [{ type: "text", text: render(steps) }],
        details: { steps },
      };
    },
  });

  agent.registerTool({
    name: "read_plan",
    description: "读取当前线程的任务计划。没有计划时返回空计划说明。",
    access: "read",
    parameters: { type: "object", properties: {} },
    execute: async (_args: any, ctx: ToolContext) => {
      const raw = await requireState(ctx).get<PlanStep[]>(STATE_KEY);
      const steps = Array.isArray(raw) ? raw : [];
      return {
        content: [{ type: "text", text: render(steps) }],
        details: { steps },
      };
    },
  });
}
