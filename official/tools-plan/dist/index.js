// generated from index.ts (sha256:bebd8b6d49143937) by official/build.mjs — do not edit
// tools-plan —— 官方插件：维护当前线程的任务计划（05 §6.3、02 §5.4 的 tools-plan）。
//
// 计划是线程级 KV（能力位 state），不是模型上下文：工具只返回一段文本给模型，
// 结构化步骤走 details 给界面（INV-10）。这样模型不必重新读一遍整盘计划。
//
// 类型形状与 agent:api 一致，本地声明避免官方插件依赖 sdk 的类型包。

                                                     

                    
               
                     
 

                       
                                         
                                                  
 

                       
                      
                                                             
 

                   
               
                      
                     
                                                
                                                    
 

                     
               
                 
                         
                   
                                                     
 

                    
                                   
                                       
 

const STATE_KEY = "plan";

const STATUSES               = ["pending", "in_progress", "done"];

function normalizeStatus(value         )             {
  const s = String(value || "").trim().toLowerCase();
  if (s === "in_progress" || s === "in-progress" || s === "active") return "in_progress";
  if (s === "done" || s === "completed") return "done";
  return "pending";
}

function normalizeSteps(raw         )             {
  if (!Array.isArray(raw)) {
    const e = new TypeError("steps 必须是数组");
    (e       ).code = "ERR_INVALID_ARG_TYPE";
    throw e;
  }
  return raw.map((item     , i        ) => {
    const text = typeof item === "string" ? item : String(item && item.step != null ? item.step : "");
    const step = text.trim();
    if (step.length === 0) {
      const e = new TypeError(`steps[${i}] 为空`);
      (e       ).code = "ERR_INVALID_ARG_TYPE";
      throw e;
    }
    return { step: step.slice(0, 500), status: normalizeStatus(item && item.status) };
  });
}

function render(steps            )         {
  if (steps.length === 0) return "计划已清空。";
  const mark = (s            ) => (s === "done" ? "[x]" : s === "in_progress" ? "[~]" : "[ ]");
  const lines = steps.map((s, i) => `${i + 1}. ${mark(s.status)} ${s.step}`);
  const done = steps.filter((s) => s.status === "done").length;
  return `当前计划（${done}/${steps.length} 完成）：\n` + lines.join("\n");
}

// planView 是给前端（ui-task-plan）的结构化视图：total=0 表示当前没有计划。
function planView(steps            )                                                     {
  const done = steps.filter((s) => s.status === "done").length;
  return { done, total: steps.length, steps };
}

function requireState(ctx             )              {
  if (!ctx.state) {
    const e = new Error("tools-plan 需要能力 state（清单 backend.capabilities 未声明？）");
    (e       ).code = "ERR_ACCESS_DENIED";
    throw e;
  }
  return ctx.state;
}

export default function (agent          )       {
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
    execute: async (args     , ctx             ) => {
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
    execute: async (_args     , ctx             ) => {
      const raw = await requireState(ctx).get            (STATE_KEY);
      const steps = Array.isArray(raw) ? raw : [];
      return {
        content: [{ type: "text", text: render(steps) }],
        details: { steps },
      };
    },
  });

  // 只读动作 plan：给 ui-task-plan 的 thread.tray 卡片取数（7.5 的 action.invoke 桥）。
  // 返回 { plan }；total=0 表示当前线程没有计划（前端据此不画卡）。
  agent.registerAction({
    name: "plan",
    mode: "read",
    execute: async (_input     , ctx             ) => {
      const raw = await requireState(ctx).get            (STATE_KEY);
      const steps = Array.isArray(raw) ? raw : [];
      return { plan: planView(steps) };
    },
  });
}
