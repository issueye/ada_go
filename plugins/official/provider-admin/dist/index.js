// generated from index.ts (sha256:308fb27d1ff2ea3e) by plugins/official/build.mjs — do not edit
// provider-admin 后端：供应商目录与当前模型切换。
//
// 数据面全部经宿主能力 model.admin 提供（ctx.providers）：
//   - list()   读取当前装配的供应商/模型目录与当前模型，不含密钥；
//   - select() 复用 model.set 的解析与线程校验。
// 页面/脚本只调用本插件的 route，不直接读 Core RPC。

                     
             
                       
                        
                     
                   
                         
                             
 

                        
             
                       
                   
                      
 

                           
                            
                                                   
 

                        
                         
               
                                
                 
 

                        
              
                                     
                                                                                                  
    
 

                    
        
                           
                 
                                                               
          
 

export default function (agent          )       {
  agent.route("GET", "/state", (_req, ctx) => ctx.providers.list());

  agent.route("POST", "/select", (req, ctx) => {
    const body = (req.body || {})                                               ;
    const providerId = String(body.providerId || "").trim();
    const modelId = String(body.modelId || "").trim();
    if (!providerId || !modelId) {
      const err = new Error("需要 providerId 与 modelId");
      (err       ).code = "ERR_BAD_REQUEST";
      throw err;
    }
    return ctx.providers.select(providerId, modelId);
  });
}
