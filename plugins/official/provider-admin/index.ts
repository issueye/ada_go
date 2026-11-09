// provider-admin 后端：供应商配置的增删改查、模型拉取与当前模型切换。
//
// 数据面：
//   - 配置经 ctx.config 的 provider-config 命名空间读写（机器级、0600、跨进程锁）；
//   - 列表/当前模型经 ctx.providers.list/select（基座只读视图 + 通用切换）；
//   - /models 拉取经 ctx.fetch（宿主出网桥，密钥不出前端）。
// 页面只调用本插件的 route，不直接读 Core RPC；密钥不经过浏览器。
//
// 入口刻意写成可直接执行的 ESM JavaScript：官方构建期只做类型擦除，
// 产物 dist/index.js 与本文件去掉类型后逐字一致（build.mjs --check）。

function bodyOf(req) {
  const body = req.body;
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw new Error("请求体必须是 JSON 对象");
  }
  return body;
}

function idOf(body) {
  const id = String(body.id || "").trim();
  if (!id) throw new Error("缺少供应商 id");
  return id;
}

async function loadConfigs(ctx) {
  const raw = await ctx.config.get("providers");
  return Array.isArray(raw) ? raw : [];
}

async function saveConfigs(ctx, configs) {
  await ctx.config.set("providers", configs);
}

function normalizeModels(raw) {
  if (!Array.isArray(raw)) return [];
  const out = [];
  const seen = new Set();
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const id = String(item.id || "").trim();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const levels = [];
    if (Array.isArray(item.thinkingLevels)) {
      const dedup = new Set();
      for (const level of item.thinkingLevels) {
        const s = String(level || "").trim().toLowerCase();
        if (!s || dedup.has(s)) continue;
        if (!/^[a-z0-9_-]{1,32}$/.test(s)) throw new Error("推理等级 " + s + " 只能包含字母、数字、下划线和连字符");
        dedup.add(s);
        levels.push(s);
        if (levels.length >= 8) break;
      }
    }
    out.push({
      id,
      displayName: String(item.displayName || "").trim() || undefined,
      contextWindow: Number(item.contextWindow || 0) || undefined,
      maxOutput: Number(item.maxOutput || 0) || undefined,
      input: Array.isArray(item.input) ? item.input : undefined,
      supportsTools: item.supportsTools === true,
      supportsThinking: levels.length > 0 || item.supportsThinking === true,
      thinkingLevels: levels,
      disabled: item.disabled === true,
    });
  }
  return out;
}

function normalizeProxy(raw) {
  const proxy = String(raw || "").trim();
  if (!proxy) return "";
  if (!/^(https?|socks5):\/\/[^\s]+$/i.test(proxy)) {
    throw new Error("代理必须是 http(s) 或 socks5 地址");
  }
  return proxy;
}

function normalizeHeaders(raw) {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const out = {};
  for (const [key, value] of Object.entries(raw)) {
    const name = String(key).trim();
    if (!name) continue;
    if (/^(host|content-length)$/i.test(name)) {
      throw new Error("不能覆盖请求头 " + name);
    }
    out[name] = String(value ?? "");
  }
  return out;
}

function infoOf(cfg) {
  const key = String(cfg.apiKey || "");
  return {
    id: cfg.id,
    displayName: cfg.name,
    protocol: cfg.protocol,
    baseUrl: cfg.baseUrl,
    proxy: cfg.proxy,
    headers: cfg.headers,
    disabled: cfg.disabled,
    hasApiKey: key !== "",
    apiKeyHint: key ? "尾号 " + key.slice(-4) : "",
    models: cfg.models || [],
  };
}


async function fetchProviderModels(ctx, id) {
  const configs = await loadConfigs(ctx);
  const cfg = configs.find((c) => c.id === id);
  if (!cfg) throw new Error("没有供应商 " + id);
  if (!cfg.apiKey) throw new Error("请先填写 API Key（或配置对应的 secrets.toml/环境变量）");
  const cat = await ctx.providers.list();
  const info = (cat.providers || []).find((p) => p.id === id) || {};
  const base = String(cfg.baseUrl || info.defaultBaseUrl || "").replace(/\/+$/, "");
  if (!/^https?:\/\//i.test(base)) throw new Error("供应商没有可用端点");
  const headers = Object.assign({}, cfg.headers || {});
  const authHeader = info.authHeader || "Authorization";
  const authPrefix = info.authPrefix === undefined ? "Bearer " : info.authPrefix;
  headers[authHeader] = authPrefix + cfg.apiKey;
  if (info.apiVersionHeader && info.apiVersion) headers[info.apiVersionHeader] = info.apiVersion;
  const res = await ctx.fetch(base + "/models", { method: "GET", headers });
  const text = await res.text();
  if (!res.ok) throw new Error("拉取模型列表失败（HTTP " + res.status + "）");
  let body = {};
  try { body = JSON.parse(text); } catch { throw new Error("模型列表不是合法 JSON"); }
  const list = Array.isArray(body.data) ? body.data : (Array.isArray(body.models) ? body.models : []);
  return list.map((m) => ({
    id: String(m.id || ""),
    displayName: String(m.name || m.display_name || m.id || ""),
    contextWindow: Number(m.context_window || m.contextWindow || 0) || undefined,
    maxOutput: Number(m.max_output || m.maxOutput || 0) || undefined,
    input: Array.isArray(m.input) ? m.input : ["text"],
    supportsTools: m.supports_tools === true || m.supportsTools === true,
    supportsThinking: m.supports_thinking === true || m.supportsThinking === true,
  })).filter((m) => m.id);
}

export default function (agent) {
  agent.route("GET", "/state", (_req, ctx) => ctx.providers.list());

  // 只读动作 state：供页面 SSR 预取，首屏/无 JS 也能看到供应商目录。
  agent.registerAction({
    name: "state",
    mode: "read",
    execute: async (_input, ctx) => ctx.providers.list(),
  });

  agent.route("POST", "/save", async (req, ctx) => {
    const body = bodyOf(req);
    const configs = await loadConfigs(ctx);
    const id = String(body.id || "").trim();
    if (!id) throw new Error("缺少供应商 id");
    const protocol = String(body.protocol || "").trim();
    if (!protocol) throw new Error("缺少协议格式 protocol");
    const idx = configs.findIndex((c) => c.id === id);
    const prev = idx >= 0 ? configs[idx] : undefined;
    const clear = body.clearApiKey === true;
    const apiKey = clear ? "" : (body.apiKey === undefined ? String(prev && prev.apiKey || "") : String(body.apiKey));
    const cfg = {
      id,
      name: String(body.name || "").trim() || id,
      protocol,
      baseUrl: String(body.baseUrl || "").trim(),
      apiKey,
      proxy: normalizeProxy(body.proxy),
      headers: normalizeHeaders(body.headers),
      disabled: body.disabled === true,
      models: normalizeModels(body.models),
    };
    if (cfg.baseUrl && !/^https?:\/\/[^\s]+$/i.test(cfg.baseUrl)) {
      throw new Error("Base URL 必须是 http(s) 地址");
    }
    if (idx >= 0) configs[idx] = cfg;
    else configs.push(cfg);
    await saveConfigs(ctx, configs);
    const cat = await ctx.providers.list();
    return cat.providers.find((p) => p.id === id) || infoOf(cfg);
  });

  agent.route("POST", "/remove", async (req, ctx) => {
    const id = idOf(bodyOf(req));
    const configs = await loadConfigs(ctx);
    const next = configs.filter((c) => c.id !== id);
    if (next.length !== configs.length) {
      await saveConfigs(ctx, next);
    }
    return { removed: true };
  });

  agent.route("POST", "/fetch-models", (req, ctx) => {
    return fetchProviderModels(ctx, idOf(bodyOf(req)));
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
