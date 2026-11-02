// generated from index.ts (sha256:7a3d4bc292ec111f) by plugins/official/build.mjs — do not edit
// mcp-client —— 官方插件：把 MCP server 的工具接入为普通工具（v2 设计见 docs/18）。
//
// 范围：
//   - HTTP / Streamable HTTP transport；
//   - 装配期 initialize + tools/list，把每个 MCP 工具 registerTool；
//   - 运行期 tools/call，结果映射成普通 ToolReturn；
//   - 配置来自工作区 .adag/mcp.json（或 plugins.settings.mcp-client.servers）；
//   - stdio transport 在 phase 2 接入，本文件遇到时跳过并记警告。
//
// 基座不知道 MCP：这里用的都是普通插件能力（fs.read / net.fetch / registerTool）。

                         
              
                 
                            
                                                                   
                   
                   
                    
                  
                        
                   
     
               
 

                           
                    
                                     
                                                             
                   
        
                                                                          
    
           
                
                  
                                                                                
                                           
             
          
                  
                     
                                                                  
                              
    
 

                                               
           
                
                  
                                                                                
 

                   
               
                      
                     
                                                
                     
                         
                       
                     
                                                    
 

                    
                                   
                                                                                                          
 

                        
             
                    
                     
               
                                   
                   
                  
               
                 
                     
                          
                      
 

                   
               
                       
                    
                 
                           
                              
                             
                            
    
 

const PROTOCOL_VERSION = "2025-03-26";
const CONFIG_VERSION = 1;
const MAX_SERVERS = 32;
const MAX_TOOLS_PER_SERVER = 200;
const MAX_LIST_PAGES = 32;
const DEFAULT_TIMEOUT_MS = 60000;
const MAX_TIMEOUT_MS = 600000;
const MAX_TEXT_BYTES = 50 * 1024;
const MAX_IMAGE_BYTES = 1024 * 1024;

                        
             
                    
              
                               
                
                 
 

function fail(code        , message        )        {
  const e = new Error(message);
  (e       ).code = code;
  throw e;
}

function errorText(e     )         {
  if (!e) return "unknown error";
  if (typeof e.message === "string") return e.message;
  return String(e);
}

function shortHash(s        )         {
  // djb2：只用于工具名去重/截断，不参与安全判定。
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) >>> 0;
  return h.toString(16).padStart(8, "0");
}

function slug(s        )         {
  return String(s)
    .toLowerCase()
    .replace(/[^a-z0-9_]+/g, "_")
    .replace(/_+/g, "_")
    .replace(/^_+|_+$/g, "");
}

function clampTimeout(v         )         {
  if (typeof v !== "number" || !Number.isFinite(v)) return DEFAULT_TIMEOUT_MS;
  return Math.max(1000, Math.min(MAX_TIMEOUT_MS, Math.trunc(v)));
}

function byteLength(s        )         {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}

function capText(s        , limit        )         {
  if (byteLength(s) <= limit) return s;
  let out = s.slice(0, limit);
  while (out.length > 0 && byteLength(out) > limit) out = out.slice(0, -1);
  return out + "\n…（MCP 输出已截断）";
}

function safeString(v         )         {
  return typeof v === "string" ? v.replace(/\u0000/g, "") : "";
}

function headerValue(headers        , name        )         {
  const want = name.toLowerCase();
  for (const [k, v] of Object.entries(headers ?? {})) {
    if (k.toLowerCase() === want) return String(v);
  }
  return "";
}

function safeUrl(raw         )         {
  const s = safeString(raw);
  if (!/^https?:\/\//i.test(s)) return "";
  return s;
}

// —— 配置 ——

function parseServers(raw     )                 {
  const out                 = [];
  if (!raw || !Array.isArray(raw)) return out;
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const s = item                ;
    if (typeof s.id !== "string" || !/^[a-z][a-z0-9_]{0,15}$/.test(s.id)) {
      fail("ERR_INVALID_ARG_VALUE", `mcp.server.id 非法：${String(s.id)}`);
    }
    out.push(s);
  }
  return out;
}

function settingsServers(ctx                 )                 {
  const settings = ctx.settings ?? {};
  return parseServers((settings       ).servers);
}

async function readConfigFile(ctx                 , path        , label        )                          {
  if (!ctx.fs || !path) return [];
  try {
    const raw = await ctx.fs.readText(path, { maxBytes: 256 * 1024 });
    const parsed = JSON.parse(raw);
    if (parsed?.version !== undefined && parsed.version !== CONFIG_VERSION) {
      ctx.log("warn", "mcp: 未知配置版本，按 v1 解析", { source: label, version: parsed.version });
    }
    return parseServers(parsed?.servers);
  } catch (e     ) {
    // 文件不存在是正常空配置；其它错误也要可见，但不能让插件起不来。
    if (e && (e.code === "ENOENT" || String(e.message || "").includes("no such file"))) return [];
    ctx.log("warn", `mcp: 读取 ${label} 失败，按空配置处理`, { err: errorText(e) });
    return [];
  }
}

function mergeServers(base                , override                )                 {
  const byID = new Map                      ();
  for (const s of base) byID.set(s.id, s);
  for (const s of override) byID.set(s.id, s);
  return Array.from(byID.values());
}

async function loadServers(ctx                 )                          {
  const fromSettings = settingsServers(ctx);
  const base = String(ctx.workspace || "").replace(/\/+$/, "");
  const global = await readConfigFile(ctx, globalConfigPath(), "~/.adago/mcp.json");
  const local = await readConfigFile(ctx, `${base}/.adag/mcp.json`, ".adag/mcp.json");
  // 优先级：工作区 > 用户级 > settings 默认值；同 id 后者被前者覆盖。
  return mergeServers(mergeServers(fromSettings, global), local);
}

function globalConfigPath()         {
  const p = (globalThis       ).process;
  const home = String(p?.env?.HOME || p?.env?.USERPROFILE || "").replace(/\\/g, "/").replace(/\/+$/, "");
  return home ? `${home}/.adago/mcp.json` : "";
}

function resolveHeaders(server              , ctx                 )                         {
  const out                         = {};
  for (const [key, raw] of Object.entries(server.headers ?? {})) {
    const value = String(raw).replace(/\$\{secret:([a-z0-9_]+)\}/gi, (_m, name        ) => {
      if (!ctx.secret) {
        fail("ERR_ACCESS_DENIED", `mcp: 配置引用了密钥 ${name}，但清单没有 secrets 能力`);
      }
      const v = ctx.secret(name);
      if (v === null || v === undefined || v === "") {
        fail("ERR_ACCESS_DENIED", `mcp: 密钥 ${name} 未配置`);
      }
      return v;
    });
    out[key] = value;
  }
  return out;
}

function validateServer(server              )               {
  if (server.enabled === false) return server;
  const transport = String(server.transport || "http").toLowerCase();
  if (transport === "stdio") {
    if (!safeString(server.command)) {
      fail("ERR_INVALID_ARG_VALUE", `mcp server ${server.id}: stdio 缺少 command`);
    }
    if (server.args !== undefined && !Array.isArray(server.args)) {
      fail("ERR_INVALID_ARG_VALUE", `mcp server ${server.id}: stdio args 必须是字符串数组`);
    }
    if (Array.isArray(server.args) && server.args.some((a) => typeof a !== "string")) {
      fail("ERR_INVALID_ARG_VALUE", `mcp server ${server.id}: stdio args 每个元素必须是字符串`);
    }
    return {
      ...server,
      transport,
      args: Array.isArray(server.args) ? server.args : [],
      timeoutMs: clampTimeout(server.timeoutMs),
    };
  }
  if (transport !== "http") {
    fail("ERR_NOT_SUPPORTED", `mcp server ${server.id}: 未知 transport ${transport}（只支持 http/stdio）`);
  }
  const url = safeString(server.url);
  if (!url) fail("ERR_INVALID_ARG_VALUE", `mcp server ${server.id}: 缺少 url`);
  if (!/^https:\/\//i.test(url) && !/^http:\/\/(127\.0\.0\.1|localhost)(:\d+)?(\/|$)/i.test(url)) {
    fail("ERR_ACCESS_DENIED", `mcp server ${server.id}: url 只允许 https 或回环 http`);
  }
  if (/\/\/[^/]*@/.test(url)) {
    fail("ERR_INVALID_ARG_VALUE", `mcp server ${server.id}: url 不能内嵌凭据`);
  }
  return { ...server, transport, url, timeoutMs: clampTimeout(server.timeoutMs) };
}

// —— HTTP JSON-RPC ——

                     
              
                  
 

function parseJsonRpc(body        , id        )      {
  const text = String(body || "").trim();
  if (!text) throw fail("MCP_PROTOCOL", "MCP 返回空响应");
  // 先按普通 JSON 解析；失败再按 SSE data: 行解析。
  const candidates        = [];
  try {
    candidates.push(JSON.parse(text));
  } catch {
    for (const line of text.split(/\r?\n/)) {
      const m = line.match(/^data:\s*(.*)$/);
      if (!m) continue;
      const payload = m[1].trim();
      if (!payload || payload === "[DONE]") continue;
      try {
        candidates.push(JSON.parse(payload));
      } catch {
        // 非 JSON 的 data 行忽略；协议错误最终由空命中报出。
      }
    }
  }
  for (const item of candidates) {
    if (!item || typeof item !== "object") continue;
    if (item.error) {
      const msg = item.error.message || JSON.stringify(item.error);
      const e = new Error(`MCP JSON-RPC error ${item.error.code ?? ""}: ${msg}`);
      (e       ).code = "MCP_RPC_ERROR";
      throw e;
    }
    if (item.id === id || (item.id === undefined && item.result !== undefined)) {
      return item.result;
    }
  }
  throw fail("MCP_PROTOCOL", "MCP 响应没有匹配的 JSON-RPC id");
}

async function postRpc(
  ctx                 ,
  server              ,
  session        ,
  method        ,
  params     ,
)                     {
  if (!ctx.fetch) {
    throw fail("ERR_ACCESS_DENIED", "mcp: 需要 net.fetch 能力（清单 backend.capabilities 未声明？）");
  }
  const id = Math.floor(Math.random() * 0x3fffffff) + 1;
  const headers                         = {
    ...resolveHeaders(server, ctx),
    "content-type": "application/json",
    accept: "application/json, text/event-stream",
    "MCP-Protocol-Version": PROTOCOL_VERSION,
  };
  if (session) headers["Mcp-Session-Id"] = session;
  const payload = JSON.stringify({ jsonrpc: "2.0", id, method, params: params ?? {} });
  let res     ;
  try {
    res = await ctx.fetch(server.url , { method: "POST", headers, body: payload });
  } catch (e) {
    throw fail("MCP_TRANSPORT", `mcp ${server.id}: ${errorText(e)}`);
  }
  const status = Number(res?.status ?? 0);
  const body = typeof res?.text === "function" ? await res.text() : String(res?.text ?? "");
  if (status < 200 || status >= 300) {
    throw fail("MCP_HTTP", `mcp ${server.id}: HTTP ${status}: ${capText(body, 2000)}`);
  }
  const nextSession = headerValue(res?.headers ?? {}, "mcp-session-id");
  return {
    result: parseJsonRpc(body, id),
    session: nextSession || session,
  };
}

async function postNotification(
  ctx                 ,
  server              ,
  session        ,
  method        ,
  params     ,
)                {
  if (!ctx.fetch) return;
  const headers                         = {
    ...resolveHeaders(server, ctx),
    "content-type": "application/json",
    accept: "application/json, text/event-stream",
    "MCP-Protocol-Version": PROTOCOL_VERSION,
  };
  if (session) headers["Mcp-Session-Id"] = session;
  try {
    await ctx.fetch(server.url , {
      method: "POST",
      headers,
      body: JSON.stringify({ jsonrpc: "2.0", method, params: params ?? {} }),
    });
  } catch {
    // 通知失败不改变已建立的会话；下一次 tools/list 或 tools/call 会暴露真实问题。
  }
}

class McpClient {
  server              ;
  session = "";
  proc                       = null;
  buffer = "";
  stderr = "";

  constructor(server              ) {
    this.server = server;
  }

  async initialize(ctx                 )                {
    try {
      if (this.server.transport === "stdio") {
        await this.startStdio(ctx);
      }
      const r = await this.request(ctx, "initialize", {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: { name: "ada-go-mcp-client", version: "0.1.0" },
      });
      this.session = r.session;
      // 协议要求 initialize 成功后发 notifications/initialized；失败不阻断工具发现。
      await this.notify(ctx, "notifications/initialized", {});
    } catch (e) {
      this.close();
      throw e;
    }
  }

  close()       {
    if (this.proc) {
      try {
        this.proc.kill();
      } catch {
        // 关闭失败由宿主进程管理器兜底。
      }
      this.proc = null;
    }
  }

  async request(ctx                 , method        , params     )                                            {
    if (this.proc) return this.stdioRequest(method, params);
    return postRpc(ctx, this.server, this.session, method, params);
  }

  async notify(ctx                 , method        , params     )                {
    if (this.proc) {
      this.writeMessage({ jsonrpc: "2.0", method, params: params ?? {} });
      return;
    }
    await postNotification(ctx, this.server, this.session, method, params);
  }

  async listTools(ctx                 )                     {
    const out            = [];
    let cursor = "";
    for (let page = 0; page < MAX_LIST_PAGES; page++) {
      const params                         = {};
      if (cursor) params.cursor = cursor;
      const r = await this.request(ctx, "tools/list", params);
      const tools = Array.isArray(r.result?.tools) ? r.result.tools : [];
      for (const t of tools) {
        if (t && typeof t.name === "string") out.push(t);
        if (out.length >= MAX_TOOLS_PER_SERVER) return out;
      }
      cursor = typeof r.result?.nextCursor === "string" ? r.result.nextCursor : "";
      if (!cursor) break;
    }
    return out;
  }

  async callTool(ctx             , name        , args         )               {
    const r = await this.request(ctx, "tools/call", { name, arguments: args ?? {} });
    return r.result;
  }

          async startStdio(ctx                 )                {
    if (!ctx.process) {
      throw fail("ERR_ACCESS_DENIED", "mcp stdio 需要 process 能力（清单 backend.capabilities 未声明？）");
    }
    this.proc = await ctx.process.start(this.server.command , this.server.args ?? [], {
      cwd: this.server.cwd,
      env: this.server.env,
    });
  }

          writeMessage(msg     )       {
    if (!this.proc) throw fail("MCP_TRANSPORT", "stdio 进程不存在");
    this.proc.write(JSON.stringify(msg) + "\n");
  }

          nextLine()                {
    const i = this.buffer.indexOf("\n");
    if (i < 0) return null;
    const line = this.buffer.slice(0, i).replace(/\r$/, "");
    this.buffer = this.buffer.slice(i + 1);
    return line;
  }

          async stdioRequest(method        , params     )                                            {
    if (!this.proc) throw fail("MCP_TRANSPORT", "stdio 进程不存在");
    const id = Math.floor(Math.random() * 0x3fffffff) + 1;
    this.writeMessage({ jsonrpc: "2.0", id, method, params: params ?? {} });
    const timeout = clampTimeout(this.server.timeoutMs);
    const deadline = Date.now() + timeout;
    let processExited = false;

    while (true) {
      const line = this.nextLine();
      if (line !== null) {
        if (!line.trim()) continue;
        let msg     ;
        try {
          msg = JSON.parse(line);
        } catch {
          // stdio server 的日志行不是协议消息，忽略。
          continue;
        }
        if (!msg || msg.id !== id) continue;
        if (msg.error) {
          const e = new Error(`MCP JSON-RPC error ${msg.error.code ?? ""}: ${msg.error.message || JSON.stringify(msg.error)}`);
          (e       ).code = "MCP_RPC_ERROR";
          throw e;
        }
        return { result: msg.result, session: "" };
      }
      if (processExited) {
        throw fail(
          "MCP_TRANSPORT",
          `mcp ${this.server.id}: stdio 进程退出${this.stderr ? ": " + capText(this.stderr, 1000) : ""}`,
        );
      }

      const remain = deadline - Date.now();
      if (remain <= 0) throw fail("MCP_TIMEOUT", `mcp ${this.server.id}: ${method} 超时`);
      const r = await this.proc.read({ timeoutMs: Math.min(500, remain), maxBytes: MAX_TEXT_BYTES });
      if (r.stderr) this.stderr = capText(this.stderr + r.stderr, 4096);
      if (r.stdout) this.buffer += r.stdout;
      if (r.exited) processExited = true;
    }
  }
}

// —— Schema ——

const ALLOWED_SCHEMA_KEYS = new Set([
  "type", "properties", "required", "additionalProperties", "items", "enum",
  "minimum", "maximum", "minLength", "maxLength", "pattern", "minItems",
  "maxItems", "uniqueItems", "description", "default", "format", "title",
  "nullable", "$comment",
]);

function collectDefs(root     )                      {
  const defs                      = {};
  for (const key of ["$defs", "definitions"]) {
    const raw = root?.[key];
    if (raw && typeof raw === "object") {
      for (const [k, v] of Object.entries(raw)) defs[k] = v;
    }
  }
  return defs;
}

function resolveRef(ref        , defs                     )      {
  const m = ref.match(/^#\/(?:\$defs|definitions)\/(.+)$/);
  if (!m) return null;
  return defs[m[1]] ?? null;
}

function sanitizeSchema(node     , defs                     , depth        , seen             )      {
  if (depth > 16) return null;
  if (node === true) return {};
  if (node === false || node === null || typeof node !== "object") return null;
  if (Array.isArray(node)) return null;

  if (typeof node.$ref === "string") {
    if (seen.has(node.$ref)) return null;
    const target = resolveRef(node.$ref, defs);
    if (!target) return null;
    const next = new Set(seen);
    next.add(node.$ref);
    return sanitizeSchema(target, defs, depth + 1, next);
  }

  for (const key of Object.keys(node)) {
    if (key === "$defs" || key === "definitions" || key === "$schema" || key === "$id" || key === "examples") continue;
    if (key === "const") continue;
    if (!ALLOWED_SCHEMA_KEYS.has(key)) return null;
  }

  const out      = {};
  for (const key of ALLOWED_SCHEMA_KEYS) {
    if (!(key in node)) continue;
    if (key === "properties") {
      if (!node.properties || typeof node.properties !== "object") return null;
      const props                      = {};
      for (const [name, child] of Object.entries(node.properties)) {
        const s = sanitizeSchema(child, defs, depth + 1, seen);
        if (s === null) return null;
        props[name] = s;
      }
      out.properties = props;
      continue;
    }
    if (key === "items") {
      const s = sanitizeSchema(node.items, defs, depth + 1, seen);
      if (s === null) return null;
      out.items = s;
      continue;
    }
    if (key === "additionalProperties") {
      if (typeof node.additionalProperties === "boolean") out.additionalProperties = node.additionalProperties;
      else {
        const s = sanitizeSchema(node.additionalProperties, defs, depth + 1, seen);
        if (s === null) return null;
        out.additionalProperties = s;
      }
      continue;
    }
    if (key === "required") {
      if (!Array.isArray(node.required)) return null;
      const req = node.required.filter((x         ) => typeof x === "string");
      if (req.length !== node.required.length) return null;
      out.required = req;
      continue;
    }
    if (key === "enum") {
      if (!Array.isArray(node.enum)) return null;
      out.enum = node.enum;
      continue;
    }
    out[key] = node[key];
  }

  if ("const" in node) out.enum = [node.const];
  if (!out.type && out.properties) out.type = "object";
  if (out.type && out.type !== "object" && out.type !== "string" && out.type !== "number" &&
      out.type !== "integer" && out.type !== "boolean" && out.type !== "array" && out.type !== "null" &&
      !(Array.isArray(out.type) && out.type.every((x         ) => typeof x === "string"))) {
    return null;
  }
  return out;
}

function fallbackSchema(desc        )         {
  return {
    type: "object",
    additionalProperties: true,
    description: `${desc ? desc + "\n" : ""}原始 MCP inputSchema 含基座不支持的组合关键字，参数由 MCP server 自行校验。`,
  };
}

function translateSchema(raw     )                                          {
  const defs = collectDefs(raw);
  const s = sanitizeSchema(raw, defs, 0, new Set());
  if (!s || typeof s !== "object" || Array.isArray(s)) {
    return { schema: fallbackSchema(""), simplified: true };
  }
  if (!s.type) s.type = "object";
  if (s.type !== "object") {
    return { schema: fallbackSchema(""), simplified: true };
  }
  return { schema: s, simplified: false };
}

// —— 工具映射 ——

function toolSlug(server              , tool         )         {
  const prefix = server.toolPrefix ? slug(server.toolPrefix) : `mcp_${server.id}`;
  let base = `${prefix}_${slug(tool.name) || "tool"}`;
  if (base.length > 63) {
    base = base.slice(0, 54).replace(/_+$/g, "") + "_" + shortHash(`${server.id}:${tool.name}`);
  }
  return base;
}

function accessOf(server              , tool         )                               {
  const ann = tool.annotations ?? {};
  if (server.trustReadOnly === true && ann.readOnlyHint === true) return "read";
  if (ann.openWorldHint === true) return "network";
  return "write";
}

function toolDescription(server              , tool         )         {
  const desc = safeString(tool.description).replace(/\u0000/g, "").trim();
  const head = `[MCP ${server.id}] ${tool.name}`;
  const body = desc ? `\nMCP server description (untrusted): ${desc}` : "";
  return capText(`${head}${body}`, 2000);
}

function toToolReturn(server              , tool         , result     )      {
  let content        = [];
  let imageBytes = 0;
  let sawError = false;
  const resources        = [];

  for (const item of Array.isArray(result?.content) ? result.content : []) {
    if (!item || typeof item !== "object") continue;
    switch (item.type) {
      case "text": {
        content.push({ type: "text", text: capText(safeString(item.text), MAX_TEXT_BYTES) });
        break;
      }
      case "image": {
        const data = safeString(item.data);
        const mime = safeString(item.mimeType || item.mime);
        if (data && mime && imageBytes + data.length <= MAX_IMAGE_BYTES) {
          imageBytes += data.length;
          content.push({ type: "image", mime, data });
        } else {
          content.push({ type: "text", text: `[image omitted: ${mime || "unknown"} ${data.length} bytes]` });
        }
        break;
      }
      case "audio":
        content.push({ type: "text", text: `[audio omitted: ${safeString(item.mimeType || item.mime) || "unknown"}]` });
        break;
      case "resource": {
        const r = item.resource ?? {};
        resources.push({ uri: safeString(r.uri), mimeType: safeString(r.mimeType), text: typeof r.text === "string" });
        if (typeof r.text === "string") {
          content.push({ type: "text", text: capText(r.text, MAX_TEXT_BYTES) });
        } else {
          content.push({ type: "text", text: `[resource ${safeString(r.uri) || "?"} omitted: ${safeString(r.mimeType) || "blob"}]` });
        }
        break;
      }
      case "resource_link": {
        const uri = safeUrl(item.uri);
        const name = safeString(item.name) || safeString(item.uri) || "resource";
        resources.push({ uri: safeString(item.uri), mimeType: safeString(item.mimeType) });
        content.push({
          type: "text",
          text: uri ? `[${name}](${uri})` : `[resource ${name}: ${safeString(item.uri) || "?"}]`,
        });
        break;
      }
      default:
        content.push({ type: "text", text: `[unsupported MCP content: ${safeString(item.type) || "?"}]` });
    }
  }

  if (result?.isError === true) {
    sawError = true;
    const prefix = `MCP tool error: ${server.id}/${tool.name}`;
    if (content.length === 0) content.push({ type: "text", text: prefix });
    else if (content[0].type === "text") content[0].text = `${prefix}\n${content[0].text}`;
    else content.unshift({ type: "text", text: prefix });
  }

  if (content.length === 0 && result?.structuredContent !== undefined) {
    content.push({
      type: "text",
      text: capText(JSON.stringify(result.structuredContent), MAX_TEXT_BYTES),
    });
  }
  if (content.length === 0) content.push({ type: "text", text: "(无输出)" });

  // 基座单次结果上限 1MB：在插件侧先夹一次，避免一个超大 MCP 结果把调用打成 host 错误。
  const limited        = [];
  let totalBytes = 0;
  for (const part of content) {
    const size = part?.type === "text" ? byteLength(String(part.text ?? "")) : String(part?.data ?? "").length;
    if (totalBytes + size > 900 * 1024) {
      limited.push({ type: "text", text: "…（MCP 结果超过 1MB，已截断）" });
      break;
    }
    totalBytes += size;
    limited.push(part);
  }
  content = limited;

  const details                          = {
    serverId: server.id,
    tool: tool.name,
    isError: sawError,
  };
  if (resources.length > 0) details.resources = resources;
  if (result?.structuredContent !== undefined) {
    try {
      const raw = JSON.stringify(result.structuredContent);
      if (byteLength(raw) > 256 * 1024) {
        details.structuredContent = { truncated: true, bytes: byteLength(raw) };
      } else {
        details.structuredContent = result.structuredContent;
      }
    } catch {
      details.structuredContent = { truncated: true, error: "structuredContent 不可 JSON 序列化" };
    }
  }
  return { content, details };
}

// —— 装配 ——

export default async function (agent          , ctx                 )                {
  const statuses                 = [];
  const usedNames = new Set        ();
  const servers = (await loadServers(ctx)).slice(0, MAX_SERVERS);

  for (const raw of servers) {
    if (raw.enabled === false) continue;
    const status               = {
      id: raw.id,
      transport: String(raw.transport || "http"),
      url: safeString(raw.url) || [safeString(raw.command), ...(Array.isArray(raw.args) ? raw.args : [])].join(" ").trim(),
      status: "degraded",
      tools: 0,
    };
    statuses.push(status);

    let clientRef                   = null;
    try {
      const server = validateServer(raw);
      // 提前解析 header，让缺密钥在装配期就降级该 server，而不是带空 token 发请求。
      if (server.transport === "http") resolveHeaders(server, ctx);
      const client = new McpClient(server);
      clientRef = client;
      await client.initialize(ctx);
      const tools = await client.listTools(ctx);

      let count = 0;
      for (const tool of tools.slice(0, MAX_TOOLS_PER_SERVER)) {
        const name = uniqueName(toolSlug(server, tool), usedNames);
        const translated = translateSchema(tool.inputSchema);
        agent.registerTool({
          name,
          description: toolDescription(server, tool),
          parameters: translated.schema,
          access: accessOf(server, tool),
          timeoutMs: clampTimeout(server.timeoutMs),
          parallelSafe: false,
          repeatable: false,
          rollback: false,
          execute: async (args     , callCtx             ) => {
            try {
              const result = await client.callTool(callCtx, tool.name, args ?? {});
              const ret = toToolReturn(server, tool, result);
              if (translated.simplified) {
                ret.details.schemaSimplified = true;
              }
              return ret;
            } catch (e     ) {
              // 传输/认证/超时是服务器侧问题：作为工具结果回给模型，不抛异常触发插件禁用。
              return {
                content: [
                  {
                    type: "text",
                    text: `MCP ${server.id}/${tool.name} 调用失败：${errorText(e)}`,
                  },
                ],
                details: {
                  serverId: server.id,
                  tool: tool.name,
                  mcpError: true,
                  schemaSimplified: translated.simplified || undefined,
                },
              };
            }
          },
        });
        count++;
      }
      status.status = "ready";
      status.tools = count;
      ctx.log("info", "mcp: server 已连接", {
        server: server.id,
        tools: count,
        simplified: tools.filter((t) => translateSchema(t.inputSchema).simplified).length,
      });
    } catch (e     ) {
      if (clientRef) clientRef.close();
      status.status = "degraded";
      status.error = errorText(e);
      ctx.log("warn", `mcp: server ${raw.id} 不可用，已跳过`, { err: status.error });
    }
  }

  agent.route("GET", "/servers", () => ({ servers: statuses }));
}

function uniqueName(base        , used             )         {
  let name = base;
  if (!/^[a-z]/.test(name)) name = "mcp_" + name;
  name = name.slice(0, 63);
  let n = 2;
  while (used.has(name)) {
    const suffix = `_${n++}`;
    name = base.slice(0, Math.max(1, 63 - suffix.length)) + suffix;
  }
  used.add(name);
  return name;
}
