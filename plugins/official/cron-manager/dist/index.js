// generated from index.ts (sha256:6152b092a8b1616d) by plugins/official/build.mjs — do not edit
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
// 调度与执行在独立 cron 二进制（mcp_projects/cron），本插件只做管理面。
//
// 布局：页面骨架在 page.tsx，交互在 static/cron-manager.js。

                      
                         
                  
               
                                   
                
                     
                
 

                    
             
               
                   
               
                     
 

                        
                         
               
                                
                 
 

                       
         
                                                     
                                           
                                                                       
                                                       
                                                                             
                                                          
                                                                    
    
 

                    
        
                           
                 
                                                              
          
 

function fail(message        )        {
  const e = new Error(message);
  (e       ).code = "ERR_INVALID_ARG_VALUE";
  throw e;
}

function bodyOf(req              )                          {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw fail("请求体必须是 JSON 对象");
  }
  return body                           ;
}

function idOf(body                         )         {
  const id = String(body.id || "").trim();
  if (!id) throw fail("需要任务 id");
  return id;
}

// taskOf 从请求体取任务定义字段（name/cron/action），丢弃其余。
function taskOf(body                         )           {
  const name = String(body.name || "").trim();
  const cron = String(body.cron || "").trim();
  const rawAction = body.action;
  if (!name) throw fail("需要任务名");
  if (!cron) throw fail("需要调度表达式（cron）");
  if (!rawAction || typeof rawAction !== "object" || Array.isArray(rawAction)) {
    throw fail("需要动作定义（action）");
  }
  const a = rawAction                           ;
  const type = String(a.type || "");
  const action             = { type: type === "agent" ? "agent" : "http" };
  if (action.type === "http") {
    action.method = String(a.method || "GET").toUpperCase() === "POST" ? "POST" : "GET";
    action.url = String(a.url || "").trim();
    if (a.timeoutMs !== undefined && a.timeoutMs !== null && Number(a.timeoutMs) > 0) {
      action.timeoutMs = Number(a.timeoutMs);
    }
    if (typeof a.body === "string" && a.body) action.body = a.body;
    if (a.headers && typeof a.headers === "object" && !Array.isArray(a.headers)) {
      const headers                         = {};
      for (const [k, v] of Object.entries(a.headers                           )) {
        headers[k] = String(v ?? "");
      }
      action.headers = headers;
    }
  } else {
    action.text = String(a.text || "");
  }
  return { id: "", name, enabled: Boolean((body                         ).enabled ?? true), cron, action };
}

export default function (agent          )       {
  agent.route("GET", "/state", (_req, ctx) => ctx.cron.list());

  agent.route("POST", "/create", (req, ctx) => {
    const task = taskOf(bodyOf(req));
    return ctx.cron.create(task);
  });

  agent.route("POST", "/update", (req, ctx) => {
    const body = bodyOf(req);
    return ctx.cron.update({ id: idOf(body), task: taskOf(body) });
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
