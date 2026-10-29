// generated from index.ts (sha256:5d7e3726b0eb27a9) by plugins/official/build.mjs — do not edit
// provider-admin 后端：供应商配置的增删改查、模型拉取与当前模型切换。
//
// 数据面经宿主能力 model.admin 提供（ctx.providers）：
//   - list()         所有配置（含停用项），API Key 只返回 hasApiKey/尾号；
//   - save(input)    新建/更新供应商与模型清单；apiKey 留空=保持原值；
//   - remove(id)     删除供应商；
//   - fetchModels(id) 服务端用已保存端点/密钥拉取 /models；
//   - select()       切换当前模型。
// 页面只调用本插件的 route，不直接读 Core RPC；密钥不经过浏览器。

                         
             
                       
                         
                     
                   
                         
                             
                     
 

                        
             
                       
                    
                   
                     
                     
                      
                          
 

                           
                            
                                                   
 

                        
                         
               
                                
                 
 

                         
              
                
                    
                   
                  
                        
                     
                           
 

                        
              
                                     
                                                      
                                                              
                                                              
                                                                                                  
    
 

                    
        
                           
                 
                                                               
          
 

function bodyOf(req              )                          {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw new Error("请求体必须是 JSON 对象");
  }
  return body                           ;
}

function idOf(body                         )         {
  const id = String(body.id || "").trim();
  if (!id) throw new Error("缺少供应商 id");
  return id;
}

export default function (agent          )       {
  agent.route("GET", "/state", (_req, ctx) => ctx.providers.list());

  agent.route("POST", "/save", (req, ctx) => {
    const body = bodyOf(req);
    const input                = {
      id: String(body.id || "").trim(),
      name: String(body.name || "").trim(),
      protocol: String(body.protocol || "").trim(),
      baseUrl: String(body.baseUrl || "").trim(),
      apiKey: body.apiKey === undefined ? "" : String(body.apiKey),
      clearApiKey: body.clearApiKey === true,
      disabled: body.disabled === true,
      models: Array.isArray(body.models) ? (body.models                   ) : [],
    };
    return ctx.providers.save(input);
  });

  agent.route("POST", "/remove", (req, ctx) => {
    return ctx.providers.remove(idOf(bodyOf(req)));
  });

  agent.route("POST", "/fetch-models", (req, ctx) => {
    return ctx.providers.fetchModels(idOf(bodyOf(req)));
  });

  agent.route("POST", "/select", (req, ctx) => {
    const body = bodyOf(req);
    const providerId = String(body.providerId || "").trim();
    const modelId = String(body.modelId || "").trim();
    if (!providerId || !modelId) {
      throw new Error("需要 providerId 与 modelId");
    }
    return ctx.providers.select(providerId, modelId);
  });
}
