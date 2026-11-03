// generated from index.ts (sha256:ab970f7cb4c4202f) by plugins/official/build.mjs — do not edit
// env-manager 后端：环境变量管理页的数据面。
//
// 能力位 env.admin（仅 builtin）提供 ctx.env：
//   - list()                 两层（global/workspace）原文 + 合并视图 + 警告；
//   - set(scope, key, value) 写一份文件（scope 只能是 global / workspace）；
//   - remove(scope, key)     删一个变量。
// 宿主只接受固定路径（不接收脚本传路径），键名校验、大小上限、0600 原子写在
// internal/envstore 里完成；页面通过本插件 route 调用，写操作受 CSRF 约束。
//
// 布局：页面骨架在 page.tsx，交互在 static/env-manager.js。

                    
               
                               
 

                    
                   
                      
                                 
                     
 

                        
                
               
              
                   
 

                           
                
               
              
                   
 

                        
                         
               
                                
                 
 

                        
        
                     
                                                                 
                                                        
    
 

                    
        
                           
                 
                                                               
          
 

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

function scopeOf(body                         )         {
  const scope = String(body.scope || "").trim();
  if (scope !== "global" && scope !== "workspace") {
    throw fail(`scope 只能是 global 或 workspace（收到 ${JSON.stringify(scope)}）`);
  }
  return scope;
}

function keyOf(body                         )         {
  const key = String(body.key || "").trim();
  if (!key) throw fail("需要变量名 key");
  return key;
}

export default function (agent          )       {
  agent.route("GET", "/state", (_req, ctx) => ctx.env.list());

  agent.route("POST", "/set", (req, ctx) => {
    const body = bodyOf(req);
    const value = body.value === undefined || body.value === null ? "" : String(body.value);
    return ctx.env.set(scopeOf(body), keyOf(body), value);
  });

  agent.route("POST", "/remove", (req, ctx) => {
    const body = bodyOf(req);
    return ctx.env.remove(scopeOf(body), keyOf(body));
  });
}

