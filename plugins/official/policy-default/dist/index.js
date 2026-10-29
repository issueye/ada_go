// generated from index.ts (sha256:c969f8ca7ec47312) by plugins/official/build.mjs — do not edit
// policy-default —— 官方插件：tool.beforeCall 的默认审批策略（03 §5.6）。
//
// 基座不内置审批规则，只提供钩子与 ctx.ui；这个插件就是「默认那套规则」：
//
//   模式      读     写              执行/网络
//   readonly  允许   拒绝            拒绝
//   ask       允许   询问            询问
//   auto      允许   允许（scope 内） 询问
//   full      允许   允许（scope 内） 允许
//
// 两条失败关闭（比规则本身更重要）：
//   - 没有 UI（CLI/print、SSE 断线、ctx.ui 缺失）时，写/执行/网络一律拒绝；
//   - 参数里出现工作区之外的绝对路径时，无论什么模式都拒绝（scope 防御纵深）。
//
// 模式来自插件自己的设置（[plugins.settings.policy-default] mode，13 §附录 B），
// 经 ctx.settings 透传；基座不解释这个字段。

                     
                      
                                                                                                  
 

                         
                
                
                  
                  
                    
                 
                                 
 

                       
                    
                    
                                                             
                 
                               
 

                    
                                                                                                                                
 

                                                 

const ALLOW = { action: "observe" }         ;

function deny(reason        ) {
  return { action: "deny", reason };
}

function modeOf(ctx             )       {
  const raw = String(ctx.settings?.mode ?? "ask").trim().toLowerCase();
  if (raw === "readonly" || raw === "auto" || raw === "full") return raw;
  return "ask";
}

function accessOf(evt               )         {
  const a = String(evt.access ?? "read").trim().toLowerCase();
  return a === "write" || a === "exec" || a === "network" ? a : "read";
}

// 参数里是否有工作区之外的绝对路径：path/file/dest/cwd 等键，深度 3 以内。
function findOutsidePath(value         , workspace        , depth        )                {
  if (depth > 3 || value === null || typeof value !== "object") return null;
  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findOutsidePath(item, workspace, depth + 1);
      if (found) return found;
    }
    return null;
  }
  for (const [key, val] of Object.entries(value                           )) {
    if (typeof val === "string" && val.startsWith("/")) {
      const looksPathy = /^(path|file|dest|destination|cwd|dir|directory|target|source|src)$/i.test(key);
      if (looksPathy) {
        const ok = val === workspace || val.startsWith(workspace + "/") || val.startsWith("/tmp");
        if (!ok) return `${key}=${val}`;
      }
    } else if (val !== null && typeof val === "object") {
      const found = findOutsidePath(val, workspace, depth + 1);
      if (found) return found;
    }
  }
  return null;
}

// renderInput 把参数摘要成可读的一行（截断，转义换行），询问时给人看。
function renderInput(input         )         {
  let text        ;
  try {
    text = JSON.stringify(input ?? {});
  } catch {
    text = String(input);
  }
  if (text.length > 2000) text = text.slice(0, 2000) + "…";
  return text.replace(/\\n/g, "\\n");
}

function shortArgs(input         )         {
  if (input === null || typeof input !== "object") return "";
  const obj = input                           ;
  if (typeof obj.command === "string") {
    const args = Array.isArray(obj.args) ? obj.args.map((a) => String(a)) : [];
    return `${obj.command} ${args.join(" ")}`.trim();
  }
  if (typeof obj.path === "string") return obj.path;
  return "";
}

export default function (agent          )       {
  agent.on(
    "tool.beforeCall",
    async (evt               , ctx             ) => {
      const mode = modeOf(ctx);
      const access = accessOf(evt);
      const tool = String(evt.tool ?? "(未知工具)");
      const workspace = ctx.workspace && ctx.workspace.length > 0 ? ctx.workspace : "/workspace";

      // 只读工具不受策略影响（任何模式都放行）。
      if (access === "read") return ALLOW;

      // 防御纵深：无论模式，工作区外的绝对路径一律拒绝。
      const outside = findOutsidePath(evt.snapshot?.input, workspace, 0);
      if (outside) {
        ctx.log("warn", "policy denied outside path", { tool, outside });
        return deny(`denied: ${tool} 的参数指向工作区之外（${outside}），scope 校验不通过`);
      }

      if (mode === "readonly") {
        return deny(`denied: ${tool} 需要 ${access} 权限，当前策略是 readonly（只读模式）`);
      }
      if (mode === "full") return ALLOW;

      const mustAsk = access === "exec" || access === "network" || mode === "ask";
      if (!mustAsk) return ALLOW; // auto 模式下的写操作：工作区内，直接允许

      // 需要询问：没有 UI 就是拒绝（失败关闭，03 §5.6）。
      if (!ctx.ui || ctx.ui.available === false || typeof ctx.ui.confirm !== "function") {
        return deny(`denied: ${tool} 需要 ${access} 权限，但当前没有可用的交互界面（失败关闭）`);
      }

      const summary = shortArgs(evt.snapshot?.input);
      const message = [
        `工具：${tool}（${access}）`,
        summary ? `目标：${summary}` : "",
        evt.threadId ? `线程：${evt.threadId}` : "",
        "",
        "参数：",
        renderInput(evt.snapshot?.input),
      ]
        .filter((line) => line !== "")
        .join("\n");

      let approved                 = null;
      try {
        approved = await ctx.ui.confirm(`允许执行 ${tool}？`, message, { timeoutMs: 300000 });
      } catch (e     ) {
        ctx.log("warn", "policy confirm failed", { tool, err: e && e.message ? e.message : String(e) });
        return deny(`denied: 询问用户失败（${e && e.message ? e.message : String(e)}），按拒绝处理`);
      }
      if (approved === true) return ALLOW;
      return deny(`denied: 用户拒绝了这次 ${tool} 调用`);
    },
    { priority: 10 },
  );
}
