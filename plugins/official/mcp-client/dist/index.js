// generated from index.ts (sha256:889e31687ca3c5d0) by plugins/official/build.mjs — do not edit
// mcp-client —— 官方插件：把 MCP server 的工具接入为普通工具（v2 设计见 docs/18）。
//
// 范围：
//   - HTTP / Streamable HTTP 与 stdio transport；
//   - 装配期 initialize + tools/list，把每个 MCP 工具 registerTool；
//   - 运行期 tools/call，结果映射成普通 ToolReturn；
//   - 配置来自工作区 .adag/mcp.json（或 plugins.settings.mcp-client.servers）；
//   - 管理页数据面：GET /servers、GET|POST /config、POST /servers/toggle
//     （页面 /ext/mcp-client/servers，配置写在宿主 fs 的工作区 scope 内）。
//
// 基座不知道 MCP：这里用的都是普通插件能力（fs.read/fs.write / net.fetch / process）。

                         
              
                 
                            
                                                                   
                   
                   
                    
                  
                        
                   
     
               
 

                           
                    
                                     
                                                             
                   
        
                                                                          
                                            
                                                    
                                                                       
    
           
                
                  
                                                                                
                                           
             
          
                  
                     
                                                                  
                              
    
 

                                               
           
                
                  
                                                                                
 

                   
               
                      
                     
                                                
                     
                         
                       
                     
                                                    
 

                    
                                   
                                                                                                               
 

                        
             
                                       
                  
                    
                     
               
                                   
                   
                  
               
                 
                     
                          
                      
 

                   
               
                       
                    
                 
                           
                              
                             
                            
    
 

const PROTOCOL_VERSION = "2025-03-26";
const CONFIG_VERSION = 1;
const MAX_SERVERS = 32;
const MAX_TOOLS_PER_SERVER = 200;
const MAX_LIST_PAGES = 32;
const DEFAULT_TIMEOUT_MS = 60000;
const MAX_TIMEOUT_MS = 600000;
const MAX_TEXT_BYTES = 50 * 1024;
const MAX_IMAGE_BYTES = 1024 * 1024;
// details.resources 只是排障用的索引，不跟着 16MB 响应无限增长。
const MAX_RESOURCE_DETAILS = 64;
// 装配期发现的总预算：backend.initTimeoutMs=30000，留 5s 给宿主收尾。
// 单个 server 挂起不能把整个插件（乃至整个产品装配）拖到超时。
const DISCOVERY_BUDGET_MS = 25000;
const MIN_DISCOVERY_MS = 1000;
// stdio 分帧的单条消息上限：超过即按协议错误处理，避免 unlimited 缓冲。
const MAX_FRAME_BYTES = 4 * 1024 * 1024;
// 每次 proc.read 的增量上限（与宿主 MaxProcessReadBytes 对齐）。
// 宿主在缓冲超过 maxBytes 时返回的是头部片段并把余量留到下一次 read，
// 这里必须给足余量，否则 >50KB 的 JSON-RPC 消息会被截断成非法 JSON（Unix 管道 64KB 时必现）。
const PROCESS_READ_BYTES = 1024 * 1024;
// 管理页只编辑工作区配置；用户级配置按设计只读。
const CONFIG_DISPLAY_PATH = ".adag/mcp.json";
const USER_CONFIG_DISPLAY_PATH = "~/.adago/mcp.json";
const MAX_CONFIG_BYTES = 256 * 1024;

// ServerToolStatus 是管理页展示用的工具摘要（不含完整 Schema）。
                            
               
                  
                 
                            
 

                        
             
                    
                                                     
              
                 
                   
                                            
                
                            
                         
                     
                 
 

// RouteRequest 是 agent.route 收到的请求形状（与 05 §6.3 一致）。
                        
                 
               
                                 
             
 

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

// isSessionExpired 识别 Streamable HTTP 的「会话已失效」（服务端用 404 表示）。
function isSessionExpired(e     )          {
  return !!e && e.code === "MCP_HTTP" && /HTTP 404\b/.test(String(e.message || ""));
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
  // 单遍扫描：按字节预算切，不反复重算整个字符串（旧实现是 O(n²)，
  // 一段 >50KB 的中文结果就能把插件线程阻塞 2s 以上）。
  let bytes = 0;
  let i = 0;
  while (i < s.length) {
    const c = s.charCodeAt(i);
    let n = 3;
    let step = 1;
    if (c < 0x80) n = 1;
    else if (c < 0x800) n = 2;
    else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
      n = 4;
      step = 2;
    }
    if (bytes + n > limit) break;
    bytes += n;
    i += step;
  }
  return s.slice(0, i) + "\n…（MCP 输出已截断）";
}

function safeString(v         )         {
  return typeof v === "string" ? v.replace(/\u0000/g, "") : "";
}

function headerValue(headers        , name        )         {
  const want = name.toLowerCase();
  for (const [k, v] of Object.entries(headers ?? {})) {
    if (k.toLowerCase() !== want) continue;
    // 宿主 HTTP 桥的 headers 是 http.Header 形状（值是数组）：显式取第一个，
    // 不依赖数组 String() 的逗号拼接。
    if (Array.isArray(v)) return v.length > 0 ? String(v[0]) : "";
    if (v === null || v === undefined) return "";
    return String(v);
  }
  return "";
}

function safeUrl(raw         )         {
  const s = safeString(raw);
  if (!/^https?:\/\//i.test(s)) return "";
  return s;
}

// —— 装配期期限与展示脱敏 ——

// withTimeout 给装配期发现加硬期限：宿主 fetch 的兜底是分钟级，
// 单个 server 挂起会把 backend.initTimeoutMs(30s) 耗尽，让整个插件装配失败。
function withTimeout   (p            , ms        , label        )             {
  const limit = Math.max(MIN_DISCOVERY_MS, Math.trunc(ms) || DEFAULT_TIMEOUT_MS);
  return new Promise   ((resolve, reject) => {
    let settled = false;
    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      const e = new Error(`${label} 超时（${limit}ms）`);
      (e       ).code = "MCP_TIMEOUT";
      reject(e);
    }, limit);
    p.then(
      (v) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        resolve(v);
      },
      (e) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        reject(e);
      },
    );
  });
}

const SENSITIVE_ARG = /(token|secret|password|passwd|api[-_]?key|authorization|bearer)/i;

// maskArgv 把 stdio 命令行里疑似密钥的参数值打码（状态页/日志用）。
function maskArgv(parts          )         {
  const out           = [];
  let maskNext = false;
  for (const part of parts) {
    if (maskNext) {
      out.push("***");
      maskNext = false;
      continue;
    }
    const eq = part.indexOf("=");
    if (eq > 0 && SENSITIVE_ARG.test(part.slice(0, eq))) {
      out.push(`${part.slice(0, eq)}=***`);
      continue;
    }
    out.push(part);
    if (SENSITIVE_ARG.test(part)) maskNext = true;
  }
  return out.filter((s) => s !== "").join(" ").trim();
}

// displayEndpoint 只给插件页/状态路由看：URL 去掉 query/fragment（token 可能在里面），
// stdio 显示打码后的命令行。
function displayEndpoint(server              )         {
  if (String(server.transport || "http").toLowerCase() === "stdio") {
    const argv = [safeString(server.command), ...(Array.isArray(server.args) ? server.args.map((a) => String(a)) : [])];
    return maskArgv(argv);
  }
  const url = safeString(server.url);
  const cut = url.search(/[?#]/);
  return cut < 0 ? url : `${url.slice(0, cut)}…`;
}

// —— 配置 ——

                            
                          
                     
 

function parseServers(raw     )                   {
  const servers                 = [];
  const warnings           = [];
  if (raw === undefined || raw === null) return { servers, warnings };
  if (!Array.isArray(raw)) {
    warnings.push("servers 必须是数组，按空配置处理");
    return { servers, warnings };
  }
  const seen = new Set        ();
  for (const item of raw) {
    if (!item || typeof item !== "object") {
      warnings.push("servers 里有非对象配置项，已忽略");
      continue;
    }
    const s = item                ;
    if (typeof s.id !== "string" || !/^[a-z][a-z0-9_]{0,15}$/.test(s.id)) {
      // 配置错误只降级这一项：不让 .adag/mcp.json 的笔误把整个产品装配拖垮。
      warnings.push(`server.id 非法：${String(s.id)}（需匹配 ^[a-z][a-z0-9_]{0,15}$），该项已跳过`);
      continue;
    }
    if (seen.has(s.id)) {
      warnings.push(`server.id 重复：${s.id}，保留第一条`);
      continue;
    }
    seen.add(s.id);
    servers.push(s);
  }
  return { servers, warnings };
}

async function readConfigFile(ctx                 , path        , label        )                          {
  if (!ctx.fs || !path) return [];
  let raw        ;
  try {
    if (typeof ctx.fs.exists === "function" && !(await ctx.fs.exists(path))) return [];
    raw = await ctx.fs.readText(path, { maxBytes: 256 * 1024 });
  } catch (e     ) {
    // 文件不存在是正常空配置；其它错误也要可见，但不能让插件起不来。
    if (e && (e.code === "ENOENT" || String(e.message || "").includes("no such file"))) return [];
    ctx.log("warn", `mcp: 读取 ${label} 失败，按空配置处理`, { err: errorText(e) });
    return [];
  }
  let parsed     ;
  try {
    parsed = JSON.parse(raw);
  } catch (e     ) {
    ctx.log("warn", `mcp: ${label} 不是合法 JSON，按空配置处理`, { err: errorText(e) });
    return [];
  }
  if (parsed?.version !== undefined && parsed.version !== CONFIG_VERSION) {
    ctx.log("warn", "mcp: 未知配置版本，按 v1 解析", { source: label, version: parsed.version });
  }
  const result = parseServers(parsed?.servers);
  for (const w of result.warnings) ctx.log("warn", `mcp: ${label}: ${w}`);
  // 记录来源层：合并后工作区覆盖用户级，管理页要显示每条配置最终来自哪里。
  for (const s of result.servers) s.source = label;
  return result.servers;
}

function mergeServers(base                , override                )                 {
  const byID = new Map                      ();
  for (const s of base) byID.set(s.id, s);
  for (const s of override) byID.set(s.id, s);
  return Array.from(byID.values());
}

async function loadServers(ctx                 )                          {
  const settings = parseServers((ctx.settings ?? {})["servers"]);
  for (const w of settings.warnings) ctx.log("warn", `mcp: plugins.settings.mcp-client: ${w}`);
  for (const s of settings.servers) s.source = "plugins.settings";
  const base = String(ctx.workspace || "").replace(/\/+$/, "");
  const global = await readConfigFile(ctx, globalConfigPath(), "~/.adago/mcp.json");
  const local = await readConfigFile(ctx, `${base}/.adag/mcp.json`, ".adag/mcp.json");
  // 优先级：工作区 > 用户级 > settings 默认值；同 id 后者被前者覆盖。
  return mergeServers(mergeServers(settings.servers, global), local);
}

// workspaceConfigPath 是管理页读写的工作区配置（虚拟挂载路径，宿主按工作区根解析）。
function workspaceConfigPath(ctx                 )         {
  const base = String(ctx.workspace || "").replace(/\/+$/, "");
  return `${base}/.adag/mcp.json`;
}

// readWorkspaceConfig 读工作区配置原文；文件不存在返回空文本而不是错误。
async function readWorkspaceConfig(ctx                 )                                             {
  if (!ctx.fs) return { exists: false, text: "" };
  try {
    if (typeof ctx.fs.exists === "function" && !(await ctx.fs.exists(workspaceConfigPath(ctx)))) {
      return { exists: false, text: "" };
    }
    const text = await ctx.fs.readText(workspaceConfigPath(ctx), { maxBytes: MAX_CONFIG_BYTES });
    return { exists: true, text };
  } catch (e     ) {
    if (e && (e.code === "ENOENT" || String(e.message || "").includes("no such file"))) {
      return { exists: false, text: "" };
    }
    throw e;
  }
}

// validateConfigText 只做形状校验：非法 JSON / 顶层不是对象直接报错；
// 非法 server 项按 parseServers 的规则跳过并作为 warning 返回（与装配期一致）。
function validateConfigText(text        )                                          {
  if (!String(text || "").trim()) throw fail("ERR_INVALID_ARG_VALUE", "配置内容不能为空");
  if (byteLength(text) > MAX_CONFIG_BYTES) {
    throw fail("ERR_INVALID_ARG_VALUE", `配置超过 ${MAX_CONFIG_BYTES} 字节上限`);
  }
  let parsed     ;
  try {
    parsed = JSON.parse(text);
  } catch (e     ) {
    throw fail("ERR_INVALID_ARG_VALUE", "配置不是合法 JSON：" + errorText(e));
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw fail("ERR_INVALID_ARG_VALUE", "配置顶层必须是 JSON 对象");
  }
  const result = parseServers(parsed.servers);
  return { servers: result.servers.length, warnings: result.warnings };
}

async function saveWorkspaceConfig(ctx                 , text        )           
                 
                  
                     
                
   {
  const check = validateConfigText(text);
  if (!ctx.fs || typeof ctx.fs.writeText !== "function") {
    throw fail("ERR_ACCESS_DENIED", "mcp: 修改配置需要 fs.write 能力（清单未声明？）");
  }
  const res = await ctx.fs.writeText(workspaceConfigPath(ctx), text);
  const bytes = res && typeof res.bytes === "number" ? res.bytes : byteLength(text);
  ctx.log("info", "mcp: 工作区配置已更新", { path: CONFIG_DISPLAY_PATH, bytes, servers: check.servers });
  return { saved: true, servers: check.servers, warnings: check.warnings, bytes };
}

// toggleServerConfig 改单个 server 的 enabled 并写回工作区配置：
// 保留其它字段与顺序，enable 时删掉 enabled 键（默认即启用）。
async function toggleServerConfig(ctx                 , id        , enabled         )           
                 
                  
                     
                
             
                   
   {
  const current = await readWorkspaceConfig(ctx);
  if (!current.exists) {
    throw fail("ERR_INVALID_ARG_VALUE", `工作区还没有 ${CONFIG_DISPLAY_PATH}；请先点「编辑配置」创建`);
  }
  let parsed     ;
  try {
    parsed = JSON.parse(current.text);
  } catch (e     ) {
    throw fail("ERR_INVALID_ARG_VALUE", "现有配置不是合法 JSON：" + errorText(e));
  }
  if (!parsed || typeof parsed !== "object" || !Array.isArray(parsed.servers)) {
    throw fail("ERR_INVALID_ARG_VALUE", "现有配置缺少 servers 数组");
  }
  let found = false;
  for (const item of parsed.servers) {
    if (!item || typeof item !== "object" || String(item.id) !== id) continue;
    if (enabled) delete item.enabled;
    else item.enabled = false;
    found = true;
    break;
  }
  if (!found) throw fail("ERR_INVALID_ARG_VALUE", `配置里没有 server ${id}`);
  const saved = await saveWorkspaceConfig(ctx, `${JSON.stringify(parsed, null, 2)}\n`);
  return { ...saved, id, enabled };
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
  timeoutMs        ,
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
    res = await ctx.fetch(server.url , { method: "POST", headers, body: payload, timeoutMs });
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
  timeoutMs        ,
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
      timeoutMs,
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
  // 装配期发现期限：HTTP 请求本身也用它。只用 withTimeout 不够——先到点放弃
  // 而请求还挂着时，事件循环的 hold 会一直拖到宿主 fetch 超时，可能超过
  // backend.initTimeoutMs，把「单 server 降级」变回「插件装配失败」。
  discoveryTimeoutMs = 0;

  constructor(server              ) {
    this.server = server;
  }

  // fetchTimeoutMs 是本次请求的宿主 fetch 期限：装配期用发现预算，运行期用 server.timeoutMs。
          fetchTimeoutMs()         {
    return this.discoveryTimeoutMs > 0 ? this.discoveryTimeoutMs : clampTimeout(this.server.timeoutMs);
  }

  async initialize(ctx                 )                {
    try {
      if (this.server.transport === "stdio") {
        await this.startStdio(ctx);
      }
      const r = await this.request(ctx, "initialize", {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: { name: "ada-go-mcp-client", version: "0.4.0" },
      });
      this.session = r.session;
      // 只做记录不阻断：老版本 server 可能回一个不同的协议版本，工具面通常仍可用。
      const negotiated = typeof r.result?.protocolVersion === "string" ? r.result.protocolVersion : "";
      if (negotiated && negotiated !== PROTOCOL_VERSION) {
        ctx.log("warn", `mcp: server ${this.server.id} 协议版本 ${negotiated}（客户端 ${PROTOCOL_VERSION}），按兼容模式继续`, {
          server: this.server.id,
          protocolVersion: negotiated,
        });
      }
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
    try {
      return await postRpc(ctx, this.server, this.session, method, params, this.fetchTimeoutMs());
    } catch (e     ) {
      // Streamable HTTP 用 404 表示会话已失效：重新 initialize 一次再重试。
      // 被 404 拒绝的请求没有执行，重试不会造成重复副作用（tools/call 也不重试别的错误）。
      if (method !== "initialize" && this.session && isSessionExpired(e)) {
        this.session = "";
        await this.initialize(ctx);
        return postRpc(ctx, this.server, this.session, method, params, this.fetchTimeoutMs());
      }
      throw e;
    }
  }

  async notify(ctx                 , method        , params     )                {
    if (this.proc) {
      this.writeMessage({ jsonrpc: "2.0", method, params: params ?? {} });
      return;
    }
    await postNotification(ctx, this.server, this.session, method, params, this.fetchTimeoutMs());
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
    const timeout = this.fetchTimeoutMs();
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
      const r = await this.proc.read({ timeoutMs: Math.min(500, remain), maxBytes: PROCESS_READ_BYTES });
      if (r.stderr) this.stderr = capText(this.stderr + r.stderr, 4096);
      if (r.stdout) this.buffer += r.stdout;
      if (this.buffer.length > MAX_FRAME_BYTES) {
        // 没有换行的巨量输出不能无限堆进字符串：按协议错误处理，别等超时。
        this.buffer = "";
        throw fail("MCP_PROTOCOL", `mcp ${this.server.id}: stdio 单条消息超过 ${MAX_FRAME_BYTES} 字节`);
      }
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
  let droppedResources = 0;
  const pushResource = (entry                         )       => {
    if (resources.length < MAX_RESOURCE_DETAILS) resources.push(entry);
    else droppedResources++;
  };

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
        pushResource({ uri: safeString(r.uri), mimeType: safeString(r.mimeType), text: typeof r.text === "string" });
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
        pushResource({ uri: safeString(item.uri), mimeType: safeString(item.mimeType) });
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

  if (droppedResources > 0) details.resourcesTruncated = droppedResources;
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
  let servers                 = [];
  try {
    servers = await loadServers(ctx);
  } catch (e     ) {
    // 配置读取失败（权限、半损坏的 IO）也不能让插件装配失败：
    // 官方/builtin 插件的装配错误会让整个产品起不来，而配置是用户可编辑的。
    ctx.log("error", "mcp: 读取配置失败，本次不接入任何 server", { err: errorText(e) });
    servers = [];
  }
  if (servers.length > MAX_SERVERS) {
    ctx.log("warn", `mcp: server 数超过上限 ${MAX_SERVERS}，其余已忽略`, { total: servers.length });
  }
  const budgetDeadline = Date.now() + DISCOVERY_BUDGET_MS;

  for (const raw of servers.slice(0, MAX_SERVERS)) {
    const disabled = raw.enabled === false;
    const status               = {
      id: raw.id,
      transport: String(raw.transport || "http").toLowerCase(),
      url: displayEndpoint(raw),
      source: raw.source || CONFIG_DISPLAY_PATH,
      enabled: !disabled,
      status: disabled ? "disabled" : "degraded",
      tools: 0,
      items: [],
      trustReadOnly: raw.trustReadOnly === true,
      toolPrefix: String(raw.toolPrefix || ""),
    };
    statuses.push(status);
    if (disabled) continue;
    const remaining = budgetDeadline - Date.now();
    if (remaining < MIN_DISCOVERY_MS) {
      status.error = `装配期发现预算（${DISCOVERY_BUDGET_MS}ms）已耗尽，未探测`;
      ctx.log("warn", `mcp: server ${raw.id} 未探测：装配期发现预算已耗尽`);
      continue;
    }

    let clientRef                   = null;
    try {
      const server = validateServer(raw);
      // 提前解析 header，让缺密钥在装配期就降级该 server，而不是带空 token 发请求。
      if (server.transport === "http") resolveHeaders(server, ctx);
      const client = new McpClient(server);
      clientRef = client;
      // 单个 server 的发现必须有硬期限：宿主 fetch 的兜底是分钟级，
      // 挂起的 server 会把 backend.initTimeoutMs 耗光，让整个插件装配失败。
      const budget = Math.min(clampTimeout(server.timeoutMs), remaining);
      // 让宿主 fetch 也用同一个期限：否则「脚本放弃等待」与「请求真的结束」
      // 之间有窗口，等待会一直拖到桥的超时（可能超过 initTimeoutMs）。
      client.discoveryTimeoutMs = budget;
      const tools = await withTimeout(
        (async () => {
          await client.initialize(ctx);
          return client.listTools(ctx);
        })(),
        budget,
        `mcp ${server.id}: initialize/tools/list`,
      );
      // 发现结束：运行期调用回到 server.timeoutMs，不再沿用装配期预算。
      client.discoveryTimeoutMs = 0;

      if (tools.length > MAX_TOOLS_PER_SERVER) {
        ctx.log("warn", `mcp: server ${server.id} 工具数超过上限 ${MAX_TOOLS_PER_SERVER}，只注册前面的`, {
          total: tools.length,
        });
      }
      let count = 0;
      let simplifiedCount = 0;
      for (const tool of tools.slice(0, MAX_TOOLS_PER_SERVER)) {
        try {
          const name = uniqueName(toolSlug(server, tool), usedNames);
          const translated = translateSchema(tool.inputSchema);
          const access = accessOf(server, tool);
          if (translated.simplified) simplifiedCount++;
          agent.registerTool({
            name,
            description: toolDescription(server, tool),
            parameters: translated.schema,
            access,
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
          status.items.push({ name, mcpName: tool.name, access, schemaSimplified: translated.simplified });
        } catch (e     ) {
          // 单个工具注册失败（与其它插件同名、名字/描述非法等）只丢这个工具，
          // 已经注册成功的保留，不把整个 server 标成「半注册 + degraded」。
          ctx.log("warn", `mcp: 工具 ${server.id}/${tool.name} 注册失败，已跳过`, { err: errorText(e) });
        }
      }
      status.status = "ready";
      status.tools = count;
      ctx.log("info", "mcp: server 已连接", {
        server: server.id,
        tools: count,
        simplified: simplifiedCount,
      });
    } catch (e     ) {
      if (clientRef) clientRef.close();
      status.status = "degraded";
      status.error = errorText(e);
      ctx.log("warn", `mcp: server ${raw.id} 不可用，已跳过`, { err: status.error });
    }
  }

  // 管理页数据面：状态列表 + 工作区配置的读写/启停。
  // 配置写在宿主 fs 的工作区 scope 内（固定路径，不接受用户传入路径）。
  agent.route("GET", "/servers", () => ({
    servers: statuses,
    configPath: CONFIG_DISPLAY_PATH,
    userConfigPath: USER_CONFIG_DISPLAY_PATH,
  }));

  agent.route("GET", "/config", async (_req              , ctx             ) => {
    const current = await readWorkspaceConfig(ctx);
    return {
      path: CONFIG_DISPLAY_PATH,
      userPath: USER_CONFIG_DISPLAY_PATH,
      exists: current.exists,
      text: current.exists ? current.text : defaultConfigTemplate(),
    };
  });

  agent.route("POST", "/config", (req              , ctx             ) => {
    const body = (req && req.body) || {};
    if (!body || typeof body !== "object" || typeof (body       ).text !== "string") {
      throw fail("ERR_INVALID_ARG_VALUE", "请求体需要 text 字段（字符串）");
    }
    return saveWorkspaceConfig(ctx, (body       ).text);
  });

  agent.route("POST", "/servers/toggle", (req              , ctx             ) => {
    const body = (req && req.body) || {};
    const id = String((body       ).id || "").trim();
    if (!id) throw fail("ERR_INVALID_ARG_VALUE", "请求体需要 server id");
    return toggleServerConfig(ctx, id, (body       ).enabled === true);
  });
}

// defaultConfigTemplate 是「编辑配置」时文件还不存在给的初稿。
function defaultConfigTemplate()         {
  return `${JSON.stringify({ version: CONFIG_VERSION, servers: [] }, null, 2)}\n`;
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







