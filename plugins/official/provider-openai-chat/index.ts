// provider-openai-chat —— 官方插件：OpenAI Chat Completions 的流式协议映射（03 §5.5）。
//
// 这一层只做「SSE 事件 → 归一化 Delta」：TLS、代理、重定向、超时、取消、allowlist、
// 响应大小全在宿主的 ctx.http.sse 里执行。兼容端点（DeepSeek/Ollama/Moonshot 等）
// 由产品配置的 baseUrl 复用同一适配器，插件不读全局配置。
//
// 终止信号必须显式：只有收到 data: [DONE] 才算完成；EOF 没有 [DONE] 是协议错误。

interface ProviderModelInfo {
  id: string;
  displayName?: string;
  contextWindow: number;
  maxOutput?: number;
  input: string[];
  supportsTools: boolean;
  supportsThinking?: boolean;
}

interface ProviderRequest {
  model: string;
  system: string;
  messages: ProviderMessage[];
  tools?: ProviderToolSchema[];
  params?: {
    temperature?: number;
    topP?: number;
    maxTokens?: number;
    thinking?: boolean;
    extra?: unknown;
  };
  baseUrl?: string;
  apiKey?: string;
}

interface ProviderMessage {
  role: string;
  parts?: { type: string; text?: string; mime?: string; data?: string }[];
  toolCalls?: { callId?: string; tool?: string; args?: unknown }[];
  callId?: string;
}

interface ProviderToolSchema {
  name: string;
  description: string;
  parameters: unknown;
}

interface HttpInit {
  method?: "GET" | "POST";
  headers?: Record<string, string>;
  body?: string;
  maxBytes?: number;
}

interface ProviderContext {
  signal: { aborted: boolean };
  secret(name: string): string | null;
  http: {
    sse(
      url: string,
      init: HttpInit,
      onEvent: (e: { event?: string; data: string }) => void | Promise<void>,
    ): Promise<{ status: number; headers: Record<string, string> }>;
    json(
      url: string,
      init: HttpInit,
    ): Promise<{ status: number; headers: Record<string, string>; body: unknown }>;
  };
  emit(delta: Record<string, unknown>): void;
  log(level: "debug" | "info" | "warn" | "error", msg: string, fields?: object): void;
  settings?: Record<string, unknown>;
}

interface ProviderDef {
  id: string;
  models: ProviderModelInfo[];
  stream(req: ProviderRequest, ctx: ProviderContext): Promise<void>;
}

interface AgentAPI {
  registerProvider(def: ProviderDef): void;
}

const DEFAULT_BASE_URL = "https://api.openai.com/v1";
const SECRET_NAME = "openai_api_key";

function fail(message: string): never {
  const e = new Error(message);
  (e as any).code = "ERR_PROVIDER";
  throw e;
}

function endpointOf(baseUrl: string | undefined): string {
  const raw = String(baseUrl || DEFAULT_BASE_URL).trim();
  const base = raw.length > 0 ? raw : DEFAULT_BASE_URL;
  if (/\/chat\/completions\/?$/.test(base)) return base.replace(/\/+$/, "");
  return base.replace(/\/+$/, "") + "/chat/completions";
}

// textOf 把消息的 text 部件拼起来（thinking 不回灌给模型）。
function textOf(msg: ProviderMessage): string {
  const parts = Array.isArray(msg.parts) ? msg.parts : [];
  const out: string[] = [];
  for (const part of parts) {
    if (part && part.type === "text" && typeof part.text === "string") out.push(part.text);
  }
  return out.join("");
}

function imageParts(msg: ProviderMessage): { type: string; image_url: { url: string } }[] {
  const parts = Array.isArray(msg.parts) ? msg.parts : [];
  const out: { type: string; image_url: { url: string } }[] = [];
  for (const part of parts) {
    if (part && part.type === "image" && part.mime && part.data) {
      out.push({ type: "image_url", image_url: { url: `data:${part.mime};base64,${part.data}` } });
    }
  }
  return out;
}

function argsString(raw: unknown): string {
  if (raw === null || raw === undefined) return "{}";
  if (typeof raw === "string") return raw;
  try {
    return JSON.stringify(raw);
  } catch {
    return "{}";
  }
}

// toOpenAIMessages 做统一消息 → Chat Completions 消息的唯一转换。
function toOpenAIMessages(messages: ProviderMessage[]): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = [];
  const callNames = new Map<string, string>();
  for (const msg of messages) {
    if (!msg || typeof msg !== "object") continue;
    if (msg.role === "system") continue; // 系统提示走顶层 system 字段
    if (msg.role === "tool") {
      const entry: Record<string, unknown> = {
        role: "tool",
        tool_call_id: String(msg.callId || ""),
        content: textOf(msg),
      };
      const name = callNames.get(String(msg.callId || ""));
      if (name) entry.name = name;
      out.push(entry);
      continue;
    }
    if (msg.role === "assistant") {
      const text = textOf(msg);
      const calls = Array.isArray(msg.toolCalls) ? msg.toolCalls : [];
      if (calls.length === 0) {
        out.push({ role: "assistant", content: text });
        continue;
      }
      for (const c of calls) {
        if (c && c.callId) callNames.set(String(c.callId), String(c.tool || ""));
      }
      out.push({
        role: "assistant",
        content: text.length > 0 ? text : null,
        tool_calls: calls.map((c) => ({
          id: String(c.callId || ""),
          type: "function",
          function: { name: String(c.tool || ""), arguments: argsString(c.args) },
        })),
      });
      continue;
    }
    const images = imageParts(msg);
    const text = textOf(msg);
    if (images.length === 0) {
      out.push({ role: "user", content: text });
    } else {
      const content: Record<string, unknown>[] = [];
      if (text.length > 0) content.push({ type: "text", text });
      for (const img of images) content.push(img);
      out.push({ role: "user", content });
    }
  }
  return out;
}

function toOpenAITools(tools: ProviderToolSchema[] | undefined): Record<string, unknown>[] | undefined {
  if (!Array.isArray(tools) || tools.length === 0) return undefined;
  return tools.map((t) => ({
    type: "function",
    function: {
      name: String(t.name || ""),
      description: String(t.description || ""),
      parameters: t.parameters ?? { type: "object", properties: {} },
    },
  }));
}

function buildBody(req: ProviderRequest): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: req.model,
    stream: true,
    stream_options: { include_usage: true },
    messages: toOpenAIMessages(req.messages || []),
  };
  if (req.system && req.system.length > 0) body.messages = [{ role: "system", content: req.system }].concat(body.messages as Record<string, unknown>[]);
  const tools = toOpenAITools(req.tools);
  if (tools) body.tools = tools;
  const params = req.params || {};
  if (typeof params.temperature === "number") body.temperature = params.temperature;
  if (typeof params.topP === "number") body.top_p = params.topP;
  if (typeof params.maxTokens === "number" && params.maxTokens > 0) body.max_tokens = params.maxTokens;
  if (params.extra && typeof params.extra === "object") {
    const extra = params.extra as Record<string, unknown>;
    for (const key of Object.keys(extra)) {
      if (!(key in body)) body[key] = extra[key];
    }
  }
  return body;
}

function streamOnce(req: ProviderRequest, ctx: ProviderContext): Promise<void> {
  const url = endpointOf(req.baseUrl);
  const key = String(req.apiKey || "").trim() || ctx.secret(SECRET_NAME);
  if (!key) {
    // 没有密钥是配置错误，不是可重试的网络错误：明确说出来。
    fail(`缺少 ${SECRET_NAME}：请在供应商管理页填写 API Key，或通过环境变量/secrets.toml 配置`);
  }
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Authorization: `Bearer ${key}`,
  };
  const body = JSON.stringify(buildBody(req));

  let textOpen = false;
  let thinkingOpen = false;
  const calls = new Map<number, { id: string; name: string }>();
  let finishReason = "";
  let sawDone = false;
  let usageSeen = false;

  function emitText(text: string): void {
    if (!text) return;
    if (!textOpen) {
      ctx.emit({ kind: "text.start" });
      textOpen = true;
    }
    ctx.emit({ kind: "text.delta", text });
  }

  function emitThinking(text: string): void {
    if (!text) return;
    if (!thinkingOpen) {
      ctx.emit({ kind: "thinking.start" });
      thinkingOpen = true;
    }
    ctx.emit({ kind: "thinking.delta", text });
  }

  function emitUsage(usage: any): void {
    if (!usage || usageSeen) return;
    usageSeen = true;
    const details = usage.prompt_tokens_details || {};
    ctx.emit({
      kind: "usage",
      usage: {
        inputTokens: Number(usage.prompt_tokens || 0),
        outputTokens: Number(usage.completion_tokens || 0),
        cacheTokens: Number(details.cached_tokens || 0),
        costMicros: 0,
      },
    });
  }

  function closeOpenParts(): void {
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

  function handleChunk(chunk: any): void {
    if (!chunk || typeof chunk !== "object") return;
    if (chunk.error) {
      const msg = chunk.error.message || JSON.stringify(chunk.error);
      fail(`provider 返回错误帧: ${msg}`);
    }
    if (chunk.usage) emitUsage(chunk.usage);
    const choices = Array.isArray(chunk.choices) ? chunk.choices : [];
    for (const choice of choices) {
      if (!choice) continue;
      const delta = choice.delta || {};
      if (typeof delta.reasoning_content === "string") emitThinking(delta.reasoning_content);
      else if (typeof delta.reasoning === "string") emitThinking(delta.reasoning);
      if (typeof delta.content === "string") emitText(delta.content);
      const toolCalls = Array.isArray(delta.tool_calls) ? delta.tool_calls : [];
      for (const tc of toolCalls) {
        if (!tc) continue;
        const index = typeof tc.index === "number" ? tc.index : 0;
        const fn = tc.function || {};
        let state = calls.get(index);
        if (!state) {
          state = { id: String(tc.id || ""), name: String(fn.name || "") };
          calls.set(index, state);
          ctx.emit({ kind: "tool_call.start", index, callId: state.id, tool: state.name });
        } else if (!state.name && fn.name) {
          state.name = String(fn.name);
        }
        if (typeof fn.arguments === "string" && fn.arguments.length > 0) {
          ctx.emit({ kind: "tool_call.delta", index, argsFragment: fn.arguments });
        }
      }
      if (choice.finish_reason) finishReason = String(choice.finish_reason);
    }
  }

  return ctx.http
    .sse(url, { method: "POST", headers, body }, (event) => {
      const data = event && typeof event.data === "string" ? event.data : "";
      if (data === "[DONE]") {
        sawDone = true;
        return;
      }
      if (data.length === 0) return;
      let chunk: any;
      try {
        chunk = JSON.parse(data);
      } catch (e: any) {
        fail(`provider SSE 数据不是合法 JSON: ${e && e.message ? e.message : String(e)}`);
      }
      handleChunk(chunk);
    })
    .then((res) => {
      if (!sawDone) {
        // EOF 不等于完成（03 §5.5）：没有 [DONE] 就是协议错误。
        fail(`provider 流在收到 [DONE] 前结束（HTTP ${res && res.status}）`);
      }
      closeOpenParts();
      ctx.emit({ kind: "finish", finishReason: finishReason || "stop", terminalMarker: "[DONE]" });
    });
}

const models: ProviderModelInfo[] = [
  { id: "gpt-4o", displayName: "GPT-4o", contextWindow: 128000, maxOutput: 16384, input: ["text", "image"], supportsTools: true },
  { id: "gpt-4o-mini", displayName: "GPT-4o mini", contextWindow: 128000, maxOutput: 16384, input: ["text", "image"], supportsTools: true },
  { id: "gpt-4.1", displayName: "GPT-4.1", contextWindow: 1047576, maxOutput: 32768, input: ["text", "image"], supportsTools: true },
  { id: "gpt-4.1-mini", displayName: "GPT-4.1 mini", contextWindow: 1047576, maxOutput: 32768, input: ["text", "image"], supportsTools: true },
  { id: "o3-mini", displayName: "o3-mini", contextWindow: 200000, maxOutput: 100000, input: ["text"], supportsTools: true, supportsThinking: true },
  { id: "deepseek-chat", displayName: "DeepSeek Chat（OpenAI 兼容）", contextWindow: 128000, maxOutput: 8192, input: ["text"], supportsTools: true },
  { id: "deepseek-reasoner", displayName: "DeepSeek Reasoner（OpenAI 兼容）", contextWindow: 128000, maxOutput: 8192, input: ["text"], supportsTools: true, supportsThinking: true },
  { id: "u2-flash", displayName: "U2 Flash（云知声 MaaS）", contextWindow: 1048576, maxOutput: 131072, input: ["text"], supportsTools: true, supportsThinking: true },
  { id: "llama3.1", displayName: "Llama 3.1（Ollama 兼容）", contextWindow: 128000, maxOutput: 8192, input: ["text"], supportsTools: true },
];

export default function (agent: AgentAPI): void {
  agent.registerProvider({
    id: "openai",
    models,
    stream(req, ctx) {
      return streamOnce(req, ctx);
    },
  });
}
