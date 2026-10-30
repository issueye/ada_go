// generated from index.ts (sha256:cce8217564465c7d) by plugins/official/build.mjs — do not edit
// tools-plan —— 官方插件：维护当前线程的任务计划（05 §6.3、02 §5.4 的 tools-plan）。
//
// 计划不写进会话 entry：插件自己把每个会话的计划保存到工作区根目录的
// TODO_<threadId>.json。工具调用与前端只读动作都从 ctx.threadId 取对应会话的
// ID，读写同一个文件；结构化步骤只走 details 给界面（INV-10），模型只拿到一段
// 渲染好的文本。旧版本留在线程级 state KV 里的计划会在首次读文件时迁移一次，
// 之后文件是唯一真源。
//
// 为什么 access=read：TODO_<threadId>.json 是本插件自己的会话状态文件（与原先
// 线程级 KV 同一语义），不是用户文件编辑，不应触发写/执行审批。

                                                     

                    
               
                     
 

                    
                                                                        
                                                                    
                                         
 

                       
                                         
 

                       
                    
                    
               
                
                      
                                                             
 

                   
               
                      
                     
                                                
                                                    
 

                     
               
                 
                         
                   
                                                     
 

                    
                                   
                                       
 

function fail(code        , message        )        {
  const e = new Error(message);
  (e       ).code = code;
  throw e;
}

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

// stepsFromStored 读文件时更宽容：用户手改过的 TODO 文件里坏行直接跳过，
// 不让一条脏数据把整张卡片或整次读取拖垮。
function stepsFromStored(raw         )             {
  if (!Array.isArray(raw)) return [];
  const out             = [];
  for (let i = 0; i < raw.length; i += 1) {
    const item      = raw[i];
    const text = typeof item === "string" ? item : String(item && item.step != null ? item.step : "");
    const step = text.trim();
    if (step.length === 0) continue;
    out.push({ step: step.slice(0, 500), status: normalizeStatus(item && item.status) });
  }
  return out;
}

function render(steps            )         {
  if (steps.length === 0) return "计划已清空。";
  const mark = (s            ) => (s === "done" ? "[x]" : s === "in_progress" ? "[~]" : "[ ]");
  const lines = steps.map((s, i) => `${i + 1}. ${mark(s.status)} ${s.step}`);
  const done = steps.filter((s) => s.status === "done").length;
  return `当前计划（${done}/${steps.length} 完成）：\n` + lines.join("\n");
}

// planView 是本插件前端段（Todo 卡片）的结构化视图：total=0 表示当前没有计划。
function planView(steps            )                                                     {
  const done = steps.filter((s) => s.status === "done").length;
  return { done, total: steps.length, steps };
}

function requireFs(ctx             )           {
  if (!ctx.fs) {
    fail("ERR_ACCESS_DENIED", "tools-plan 需要能力 fs.read/fs.write（清单 backend.capabilities 未声明？）");
  }
  return ctx.fs ;
}

// 工具与动作都必须拿到当前会话 ID：任务计划按会话隔离，缺了它无法定位文件。
function requireThreadID(ctx             )         {
  const id = String(ctx.threadId || "").trim();
  if (id.length === 0) {
    fail("ERR_INVALID_ARG_VALUE", "TODO 需要当前会话 id（ctx.threadId 为空）");
  }
  return id;
}

// TODO_<threadId>.json：文件名带会话 ID，插件自己读写；目录由宿主挂载为
// /workspace（ctx.workspace）。ID 里的非法文件名字符做保守替换。
function todoPath(ctx             )         {
  const threadID = requireThreadID(ctx);
  const safe = threadID.replace(/[^A-Za-z0-9_-]/g, "_");
  const workspace = String(ctx.workspace || "/workspace").replace(/\/+$/, "");
  return workspace + "/TODO_" + safe + ".json";
}

async function loadSteps(ctx             )                      {
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
  let parsed     ;
  try {
    parsed = JSON.parse(text);
  } catch (_err) {
    fail("ERR_INVALID_ARG_VALUE", `TODO 文件不是合法 JSON：${path}`);
  }
  return stepsFromStored(Array.isArray(parsed) ? parsed : parsed && parsed.steps);
}

// loadLegacyState 只在文件还不存在时读一次旧 KV（state 能力仅为此保留）。
async function loadLegacyState(ctx             )                      {
  if (!ctx.state) return [];
  try {
    const raw      = await ctx.state.get("plan");
    return stepsFromStored(Array.isArray(raw) ? raw : raw && raw.steps);
  } catch (_err) {
    return [];
  }
}

async function saveSteps(ctx             , steps            )                  {
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

export default function (agent          )       {
  agent.registerTool({
    name: "update_plan",
    description:
      "创建或更新当前会话的任务计划。传入完整步骤列表（会整体替换），每步标 pending/in_progress/done。适合把多步工作显式化并在推进中更新。",
    access: "read", // 只写插件自己的 TODO_<threadId>.json（会话状态，不是用户文件编辑）
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
    execute: async (_args     , ctx             ) => {
      const steps = await loadSteps(ctx);
      return {
        content: [{ type: "text", text: render(steps) }],
        details: { steps, file: todoPath(ctx) },
      };
    },
  });

  // 只读动作 plan：给本插件前端段的 Todo 卡片取数（7.5 的 action.invoke 桥）。
  // 返回 { plan }；total=0 表示当前会话没有计划（前端据此不画卡）。
  agent.registerAction({
    name: "plan",
    mode: "read",
    execute: async (_input     , ctx             ) => {
      const steps = await loadSteps(ctx);
      return { plan: planView(steps) };
    },
  });
}
