// generated from index.ts (sha256:d1af9654614c046f) by plugins/official/build.mjs — do not edit
// provider-openai-responses —— 官方插件：OpenAI Responses API 的流式协议映射（03 §5.5）。
//
// 与 Chat Completions 的差别：system 走顶层 instructions；历史用 Responses 的消息形状
// （assistant 文本 output_text、工具调用 function_call、工具回执 function_call_output）；
// 工具声明是扁平的 {type:"function", name, parameters}；SSE 事件名即 data.type。
// baseUrl 允许网关类兼容端点覆盖，插件不读全局配置。
//
// 终止信号必须显式：Responses 的 SSE 没有 [DONE]，终止是 response.completed
// （data.type 或 event 名）；EOF 没有 response.completed 是协议错误。

                             
             
                       
                        
                     
                  
                         
                             
 

                           
                
                 
                              
                               
            
                         
                  
                       
                       
                    
    
                   
 

                           
               
                                                                          
                                                                   
                  
 

                              
               
                      
                      
 

                    
                          
                                   
                
                    
 

                           
                               
                                      
         
        
                  
                     
                                                                             
                                                                    
         
                  
                     
                                                                                   
    
                                             
                                                                                      
                                     
 

                       
             
                              
                                                                    
 

                    
                                           
 

const DEFAULT_BASE_URL = "https://api.openai.com/v1";
const SECRET_NAME = "openai_api_key";
const TERMINAL_MARKER = "response.completed";

function fail(message        )        {
  const e = new Error(message);
  (e       ).code = "ERR_PROVIDER";
  throw e;
}

function endpointOf(baseUrl                    )         {
  const raw = String(baseUrl || DEFAULT_BASE_URL).trim();
  const base = raw.length > 0 ? raw : DEFAULT_BASE_URL;
  if (/\/responses\/?$/.test(base)) return base.replace(/\/+$/, "");
  return base.replace(/\/+$/, "") + "/responses";
}

// textOf 把消息的 text 部件拼起来（thinking 不回灌给模型）。
function textOf(msg                 )         {
  const parts = Array.isArray(msg.parts) ? msg.parts : [];
  const out           = [];
  for (const part of parts) {
    if (part && part.type === "text" && typeof part.text === "string") out.push(part.text);
  }
  return out.join("");
}

function imageParts(msg                 )                            {
  const parts = Array.isArray(msg.parts) ? msg.parts : [];
  const out                            = [];
  for (const part of parts) {
    if (part && part.type === "image" && part.mime && part.data) {
      out.push({ type: "input_image", image_url: `data:${part.mime};base64,${part.data}` });
    }
  }
  return out;
}

function argsString(raw         )         {
  if (raw === null || raw === undefined) return "{}";
  if (typeof raw === "string") return raw;
  try {
    return JSON.stringify(raw);
  } catch {
    return "{}";
  }
}

// toResponsesInput 做统一消息 → Responses 输入项的唯一转换：
// user/assistant 用 role + content 消息形状，工具调用与回执用独立项
// function_call / function_call_output（call_id 与 Chat 的 tool_call_id 对应）。
function toResponsesInput(messages                   )                            {
  const out                            = [];
  for (const msg of messages) {
    if (!msg || typeof msg !== "object") continue;
    if (msg.role === "system") continue; // 系统提示走顶层 instructions
    if (msg.role === "tool") {
      out.push({
        type: "function_call_output",
        call_id: String(msg.callId || ""),
        output: textOf(msg),
      });
      continue;
    }
    if (msg.role === "assistant") {
      const text = textOf(msg);
      if (text.length > 0) {
        out.push({ role: "assistant", content: [{ type: "output_text", text }] });
      }
      const calls = Array.isArray(msg.toolCalls) ? msg.toolCalls : [];
      for (const c of calls) {
        out.push({
          type: "function_call",
          call_id: String(c && c.callId ? c.callId : ""),
          name: String(c && c.tool ? c.tool : ""),
          arguments: argsString(c ? c.args : undefined),
        });
      }
      continue;
    }
    const content                            = [];
    const text = textOf(msg);
    if (text.length > 0) content.push({ type: "input_text", text });
    for (const img of imageParts(msg)) content.push(img);
    if (content.length === 0) content.push({ type: "input_text", text: "" });
    out.push({ role: "user", content });
  }
  return out;
}

// toResponsesTools 转成 Responses 的扁平工具声明（没有 Chat 的 function 包一层）。
function toResponsesTools(tools                                  )                                        {
  if (!Array.isArray(tools) || tools.length === 0) return undefined;
  return tools.map((t) => ({
    type: "function",
    name: String(t.name || ""),
    description: String(t.description || ""),
    parameters: t.parameters ?? { type: "object", properties: {} },
  }));
}

function buildBody(req                 )                          {
  const body                          = {
    model: req.model,
    stream: true,
    input: toResponsesInput(req.messages || []),
  };
  if (req.system && req.system.length > 0) body.instructions = req.system;
  const tools = toResponsesTools(req.tools);
  if (tools) body.tools = tools;
  const params = req.params || {};
  if (typeof params.temperature === "number") body.temperature = params.temperature;
  if (typeof params.topP === "number") body.top_p = params.topP;
  if (typeof params.maxTokens === "number" && params.maxTokens > 0) body.max_output_tokens = params.maxTokens;
  if (params.extra && typeof params.extra === "object") {
    const extra = params.extra                           ;
    for (const key of Object.keys(extra)) {
      if (!(key in body)) body[key] = extra[key];
    }
  }
  return body;
}

function streamOnce(req                 , ctx                 )                {
  const url = endpointOf(req.baseUrl);
  const key = ctx.secret(SECRET_NAME);
  if (!key) {
    // 没有密钥是配置错误，不是可重试的网络错误：明确说出来。
    fail(`缺少 ${SECRET_NAME}：请通过环境变量 ADAG_SECRET_OPENAI_API_KEY 或 secrets.toml 配置`);
  }
  const headers                         = {
    "Content-Type": "application/json",
    Authorization: `Bearer ${key}`,
  };
  const body = JSON.stringify(buildBody(req));

  let textOpen = false;
  let thinkingOpen = false;
  const calls = new Map                                      ();
  let finishReason = "";
  let sawCompleted = false;
  let usageSeen = false;

  function indexof(chunk     )         {
    return typeof chunk.output_index === "number" ? chunk.output_index : 0;
  }

  function startCall(index        , callId        , name        )       {
    const state = calls.get(index);
    if (state) {
      if (!state.name && name) state.name = name;
      return;
    }
    calls.set(index, { id: callId, name });
    ctx.emit({ kind: "tool_call.start", index, callId, tool: name });
  }

  function emitText(text        )       {
    if (!text) return;
    if (!textOpen) {
      ctx.emit({ kind: "text.start" });
      textOpen = true;
    }
    ctx.emit({ kind: "text.delta", text });
  }

  function emitThinking(text        )       {
    if (!text) return;
    if (!thinkingOpen) {
      ctx.emit({ kind: "thinking.start" });
      thinkingOpen = true;
    }
    ctx.emit({ kind: "thinking.delta", text });
  }

  function emitUsage(usage     )       {
    if (!usage || usageSeen) return;
    usageSeen = true;
    const details = usage.input_tokens_details || {};
    ctx.emit({
      kind: "usage",
      usage: {
        inputTokens: Number(usage.input_tokens || 0),
        outputTokens: Number(usage.output_tokens || 0),
        cacheTokens: Number(details.cached_tokens || 0),
        costMicros: 0,
      },
    });
  }

  function closeOpenParts()       {
    for (const [index] of Array.from(calls.entries()).sort((a, b) => a[0] - b[0])) {
      ctx.emit({ kind: "tool_call.end", index });
    }
    calls.clear();
    if (textOpen) {
      ctx.emit({ kind: "text.end" });
      textOpen = false;
    }
    if (thinkingOpen) {
      ctx.emit({ kind: "thinking.end" });
      thinkingOpen = false;
    }
  }

  function handleChunk(chunk     , eventName        )       {
    if (!chunk || typeof chunk !== "object") return;
    const type = typeof chunk.type === "string" && chunk.type.length > 0 ? chunk.type : eventName;
    if (type === "response.failed" || type === "response.error" || type === "error") {
      const res = chunk.response || {};
      const err = res.error || chunk.error || {};
      fail(`provider 返回错误帧: ${err.message || err.code || JSON.stringify(chunk)}`);
    }
    switch (type) {
      case "response.completed": {
        sawCompleted = true;
        const res = chunk.response || {};
        if (res.status === "incomplete") {
          const details = res.incomplete_details || {};
          finishReason = String(details.reason || "incomplete");
        }
        emitUsage(res.usage);
        return;
      }
      case "response.created":
      case "response.in_progress":
        // 元数据事件：in_progress 明确忽略（03 §5.5 不产生增量）。
        return;
      case "response.output_item.added": {
        const item = chunk.item || {};
        if (item.type === "function_call") {
          startCall(indexof(chunk), String(item.call_id || item.id || ""), String(item.name || ""));
        }
        return;
      }
      case "response.function_call_arguments.delta": {
        const index = indexof(chunk);
        startCall(index, String(chunk.item_id || ""), "");
        const fragment = typeof chunk.delta === "string" ? chunk.delta : "";
        if (fragment.length > 0) {
          ctx.emit({ kind: "tool_call.delta", index, argsFragment: fragment });
        }
        return;
      }
      case "response.output_text.delta": {
        if (typeof chunk.delta === "string") emitText(chunk.delta);
        return;
      }
      case "response.reasoning_summary_text.delta": {
        if (typeof chunk.delta === "string") emitThinking(chunk.delta);
        return;
      }
      default:
        // output_item.done / content_part.* 等不产生增量。
        return;
    }
  }

  return ctx.http
    .sse(url, { method: "POST", headers, body }, (event) => {
      const data = event && typeof event.data === "string" ? event.data : "";
      if (data.length === 0) return;
      let chunk     ;
      try {
        chunk = JSON.parse(data);
      } catch (e     ) {
        fail(`provider SSE 数据不是合法 JSON: ${e && e.message ? e.message : String(e)}`);
      }
      handleChunk(chunk, event && typeof event.event === "string" ? event.event : "");
    })
    .then((res) => {
      if (!sawCompleted) {
        // EOF 不等于完成（03 §5.5）：Responses 的终止信号是 response.completed。
        fail(`provider 流在收到 response.completed 前结束（HTTP ${res && res.status}）`);
      }
      closeOpenParts();
      ctx.emit({ kind: "finish", finishReason: finishReason || "stop", terminalMarker: TERMINAL_MARKER });
    });
}

const models                      = [
  { id: "gpt-5.1", displayName: "GPT-5.1", contextWindow: 400000, maxOutput: 128000, input: ["text", "image"], supportsTools: true, supportsThinking: true },
  { id: "gpt-5.1-mini", displayName: "GPT-5.1 mini", contextWindow: 400000, maxOutput: 128000, input: ["text", "image"], supportsTools: true, supportsThinking: true },
  { id: "o4-mini", displayName: "o4-mini", contextWindow: 200000, maxOutput: 100000, input: ["text"], supportsTools: true, supportsThinking: true },
];

export default function (agent          )       {
  agent.registerProvider({
    id: "openai-responses",
    models,
    stream(req, ctx) {
      return streamOnce(req, ctx);
    },
  });
}
