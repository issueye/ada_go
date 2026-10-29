// provider-anthropic-messages —— 官方插件：Anthropic Messages API 的流式协议映射（03 §5.5）。
//
// 这一层只做「SSE 事件 → 归一化 Delta」：TLS、代理、重定向、超时、取消、allowlist、
// 响应大小全在宿主的 ctx.http.sse 里执行。system 走顶层字段；工具参数按 content block
// index 用 input_json_delta 累积；usage 由 message_start（输入/缓存）与 message_delta
// （输出）合成一条。baseUrl 允许网关类兼容端点覆盖，插件不读全局配置。
//
// 终止信号必须显式：Anthropic 的 SSE 没有 [DONE]，终止是 message_stop
// （event:message_stop 或 data.type=message_stop）；EOF 没有 message_stop 是协议错误。

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

const DEFAULT_BASE_URL = "https://api.anthropic.com/v1";
const API_VERSION = "2023-06-01";
const SECRET_NAME = "anthropic_api_key";
const TERMINAL_MARKER = "message_stop";

function fail(message: string): never {
  const e = new Error(message);
  (e as any).code = "ERR_PROVIDER";
  throw e;
}

function endpointOf(baseUrl: string | undefined): string {
  const raw = String(baseUrl || DEFAULT_BASE_URL).trim();
  const base = raw.length > 0 ? raw : DEFAULT_BASE_URL;
  if (/\/messages\/?$/.test(base)) return base.replace(/\/+$/, "");
  return base.replace(/\/+$/, "") + "/messages";
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

// imageBlocks 把 image 部件转成 Anthropic 的 base64 图片块。
function imageBlocks(msg: ProviderMessage): Record<string, unknown>[] {
  const parts = Array.isArray(msg.parts) ? msg.parts : [];
  const out: Record<string, unknown>[] = [];
  for (const part of parts) {
    if (part && part.type === "image" && part.mime && part.data) {
      out.push({
        type: "image",
        source: { type: "base64", media_type: part.mime, data: part.data },
      });
    }
  }
  return out;
}

// parseArgs 把工具调用参数转成 Anthropic 要求的 JSON 对象（input 不能是字符串）。
function parseArgs(raw: unknown): Record<string, unknown> {
  if (raw && typeof raw === "object" && !Array.isArray(raw)) {
    return raw as Record<string, unknown>;
  }
  if (typeof raw === "string" && raw.length > 0) {
    try {
      const parsed = JSON.parse(raw);
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      // 流结束时的整体校验在基座做；这里保底给空对象。
    }
  }
  return {};
}

function isToolResultEntry(entry: Record<string, unknown> | undefined): boolean {
  if (!entry || entry.role !== "user") return false;
  const content = entry.content;
  if (!Array.isArray(content) || content.length === 0) return false;
  return content.every((b) => Boolean(b) && (b as Record<string, unknown>).type === "tool_result");
}

// toAnthropicMessages 做统一消息 → Messages 消息的唯一转换：
// assistant 的文本与 tool_use 都在 assistant 轮；工具回执是 user 轮里的 tool_result 块
// （相邻的多个 tool_result 合并进同一条 user 消息）。
function toAnthropicMessages(messages: ProviderMessage[]): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = [];
  for (const msg of messages) {
    if (!msg || typeof msg !== "object") continue;
    if (msg.role === "system") continue; // 系统提示走顶层 system 字段
    if (msg.role === "tool") {
      const block: Record<string, unknown> = {
        type: "tool_result",
        tool_use_id: String(msg.callId || ""),
        content: textOf(msg),
      };
      const prev = out.length > 0 ? out[out.length - 1] : undefined;
      if (isToolResultEntry(prev)) {
        (prev.content as Record<string, unknown>[]).push(block);
      } else {
        out.push({ role: "user", content: [block] });
      }
      continue;
    }
    if (msg.role === "assistant") {
      const blocks: Record<string, unknown>[] = [];
      const text = textOf(msg);
      if (text.length > 0) blocks.push({ type: "text", text });
      const calls = Array.isArray(msg.toolCalls) ? msg.toolCalls : [];
      for (const c of calls) {
        blocks.push({
          type: "tool_use",
          id: String(c && c.callId ? c.callId : ""),
          name: String(c && c.tool ? c.tool : ""),
          input: parseArgs(c ? c.args : undefined),
        });
      }
      if (blocks.length === 0) blocks.push({ type: "text", text: "" });
      out.push({ role: "assistant", content: blocks });
      continue;
    }
    const blocks: Record<string, unknown>[] = [];
    const text = textOf(msg);
    if (text.length > 0) blocks.push({ type: "text", text });
    for (const img of imageBlocks(msg)) blocks.push(img);
    if (blocks.length === 0) blocks.push({ type: "text", text: "" });
    out.push({ role: "user", content: blocks });
  }
  return out;
}

// toAnthropicTools 转成 Anthropic 的工具声明（JSON Schema 放在 input_schema）。
function toAnthropicTools(tools: ProviderToolSchema[] | undefined): Record<string, unknown>[] | undefined {
  if (!Array.isArray(tools) || tools.length === 0) return undefined;
  return tools.map((t) => ({
    name: String(t.name || ""),
    description: String(t.description || ""),
    input_schema: t.parameters ?? { type: "object", properties: {} },
  }));
}

// maxTokensOf：Messages API 必填 max_tokens——优先请求参数，其次模型声明的
// maxOutput，最后是保守默认。
function maxTokensOf(req: ProviderRequest): number {
  const params = req.params || {};
  if (typeof params.maxTokens === "number" && params.maxTokens > 0) return params.maxTokens;
  for (const m of models) {
    if (m.id === req.model && typeof m.maxOutput === "number" && m.maxOutput > 0) return m.maxOutput;
  }
  return 8192;
}

function buildBody(req: ProviderRequest): Record<string, unknown> {
  const body: Record<string, unknown> = {
    model: req.model,
    stream: true,
    max_tokens: maxTokensOf(req),
    messages: toAnthropicMessages(req.messages || []),
  };
  if (req.system && req.system.length > 0) body.system = req.system;
  const tools = toAnthropicTools(req.tools);
  if (tools) body.tools = tools;
  const params = req.params || {};
  if (typeof params.temperature === "number") body.temperature = params.temperature;
  if (typeof params.topP === "number") body.top_p = params.topP;
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
    "x-api-key": key,
    "anthropic-version": API_VERSION,
  };
  const body = JSON.stringify(buildBody(req));

  // 每个 content block 一个槽位（text|thinking|tool），content_block_stop 时关闭；
  // 未显式 stop 的块在 message_stop 后统一兜底关闭。
  const blocks = new Map<number, string>();
  let stopReason = "";
  let sawStop = false;
  let inputTokens = 0;
  let outputTokens = 0;
  let cacheTokens = 0;
  let usageSeen = false;

  function indexof(chunk: any): number {
    return typeof chunk.index === "number" ? chunk.index : 0;
  }

  function openBlock(index: number, kind: string, callId?: string, name?: string): void {
    if (blocks.has(index)) return;
    blocks.set(index, kind);
    if (kind === "text") ctx.emit({ kind: "text.start" });
    else if (kind === "thinking") ctx.emit({ kind: "thinking.start" });
    else ctx.emit({ kind: "tool_call.start", index, callId: callId || "", tool: name || "" });
  }

  function closeBlock(index: number): void {
    const kind = blocks.get(index);
    if (!kind) return;
    blocks.delete(index);
    if (kind === "text") ctx.emit({ kind: "text.end" });
    else if (kind === "thinking") ctx.emit({ kind: "thinking.end" });
    else ctx.emit({ kind: "tool_call.end", index });
  }

  function closeOpenBlocks(): void {
    for (const index of Array.from(blocks.keys()).sort((a, b) => a - b)) closeBlock(index);
  }

  function emitUsage(): void {
    if (usageSeen) return;
    usageSeen = true;
    ctx.emit({
      kind: "usage",
      usage: {
        inputTokens,
        outputTokens,
        cacheTokens,
        costMicros: 0,
      },
    });
  }

  function handleChunk(chunk: any, eventName: string): void {
    if (!chunk || typeof chunk !== "object") return;
    // 终止信号两种形状都认：event:message_stop 或 data.type=message_stop。
    const type = typeof chunk.type === "string" && chunk.type.length > 0 ? chunk.type : eventName;
    if (type === "error") {
      const err = chunk.error || {};
      fail(`provider 返回错误帧: ${err.message || JSON.stringify(chunk)}`);
    }
    switch (type) {
      case "message_start": {
        const usage = (chunk.message || {}).usage || {};
        inputTokens = Number(usage.input_tokens || 0);
        cacheTokens = Number(usage.cache_read_input_tokens || 0);
        return;
      }
      case "content_block_start": {
        const block = chunk.content_block || {};
        if (block.type === "tool_use") {
          openBlock(indexof(chunk), "tool", String(block.id || ""), String(block.name || ""));
        } else if (block.type === "thinking") {
          openBlock(indexof(chunk), "thinking");
        } else {
          openBlock(indexof(chunk), "text");
        }
        return;
      }
      case "content_block_delta": {
        const delta = chunk.delta || {};
        if (delta.type === "text_delta" && typeof delta.text === "string") {
          openBlock(indexof(chunk), "text");
          ctx.emit({ kind: "text.delta", text: delta.text });
        } else if (delta.type === "thinking_delta" && typeof delta.thinking === "string") {
          openBlock(indexof(chunk), "thinking");
          ctx.emit({ kind: "thinking.delta", text: delta.thinking });
        } else if (delta.type === "input_json_delta" && typeof delta.partial_json === "string" && delta.partial_json.length > 0) {
          openBlock(indexof(chunk), "tool");
          ctx.emit({ kind: "tool_call.delta", index: indexof(chunk), argsFragment: delta.partial_json });
        }
        return;
      }
      case "content_block_stop": {
        closeBlock(indexof(chunk));
        return;
      }
      case "message_delta": {
        const delta = chunk.delta || {};
        if (delta.stop_reason) stopReason = String(delta.stop_reason);
        const usage = chunk.usage || {};
        if (usage.output_tokens !== undefined) outputTokens = Number(usage.output_tokens || 0);
        return;
      }
      case "message_stop": {
        sawStop = true;
        return;
      }
      default:
        // ping 与未知事件不产生增量。
        return;
    }
  }

  return ctx.http
    .sse(url, { method: "POST", headers, body }, (event) => {
      const data = event && typeof event.data === "string" ? event.data : "";
      if (data.length === 0) return;
      let chunk: any;
      try {
        chunk = JSON.parse(data);
      } catch (e: any) {
        fail(`provider SSE 数据不是合法 JSON: ${e && e.message ? e.message : String(e)}`);
      }
      handleChunk(chunk, event && typeof event.event === "string" ? event.event : "");
    })
    .then((res) => {
      if (!sawStop) {
        // EOF 不等于完成（03 §5.5）：Anthropic 的终止信号是 message_stop。
        fail(`provider 流在收到 message_stop 前结束（HTTP ${res && res.status}）`);
      }
      emitUsage();
      closeOpenBlocks();
      ctx.emit({ kind: "finish", finishReason: stopReason || "end_turn", terminalMarker: TERMINAL_MARKER });
    });
}

const models: ProviderModelInfo[] = [
  { id: "claude-sonnet-4.5", displayName: "Claude Sonnet 4.5", contextWindow: 200000, maxOutput: 64000, input: ["text", "image"], supportsTools: true, supportsThinking: true },
  { id: "claude-haiku-4.5", displayName: "Claude Haiku 4.5", contextWindow: 200000, maxOutput: 64000, input: ["text"], supportsTools: true },
  { id: "claude-opus-4.1", displayName: "Claude Opus 4.1", contextWindow: 200000, maxOutput: 32000, input: ["text", "image"], supportsTools: true },
];

export default function (agent: AgentAPI): void {
  agent.registerProvider({
    id: "anthropic",
    models,
    stream(req, ctx) {
      return streamOnce(req, ctx);
    },
  });
}
