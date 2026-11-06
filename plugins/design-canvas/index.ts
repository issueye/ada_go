// design-canvas —— ada-go 插件（trust=third-party）
//
// ## 与 adago_design MCP 的分工（读这段再改代码）
//
//   MCP（由官方 mcp-client 插件在装配期接入）：设计体系的 Token / 组件查询、
//   预览生成、整页模板、走查、截图 —— 属于「模型侧的生产工具」，工具名形如
//   mcp_<serverId>_<toolSlug>（serverId=adago_design 时即 mcp_adago_design_design_preview）。
//
//   本插件：**不重复注册这些工具**，只补 MCP 给不了的两件事 ——
//     1) 把画布的 /api/nodes 现状沉淀成线程级台账（state），供后续回答引用；
//     2) 把台账渲染成 Markdown 动态块（design-canvas/canvas-ledger）。
//   之所以不重复：MCP 工具已经是普通工具描述符，再包一层只会让模型困惑于
//   「两个 design_preview 该叫哪个」，并且要重新承担 Schema 转换与审批语义。
//
// ## 宿主运行时边界（写错就报 "fetch is not defined"）
//
//   - 没有全局 fetch / XMLHttpRequest / WebSocket；出网只能 await ctx.fetch(...)，
//     返回 { status, ok, headers, text }，text 是字符串，JSON 要自己 parse。
//   - 没有 TypeScript 运行时：本文件是纯 JavaScript（.ts 只是约定后缀）。
//   - 不能 import npm 包；ctx.state.get/set 都是 Promise，必须 await。
//   - 工具字段是 parameters + execute，不是 MCP 风格的 inputSchema + run。
//
// ## 失败策略
//
//   画布没启动是**常态**（MCP 的 serve/mcp 形态才会拉起 HTTP 服务），
//   因此所有失败都返回可读文本 + details.error，不抛异常 ——
//   抛异常会累计「连续 3 次调用失败自动禁用插件」，把一次没开画布变成插件被停用。

const DEFAULT_BASE_URL = "http://127.0.0.1:8788";
const CACHE_KEY = "canvas_ledger_v1";
const MAX_NODES = 12;
const MAX_GROUPS = 12;
const FETCH_TIMEOUT_MS = 6000;
const RENDERER_ID = "design-canvas/canvas-ledger";

// ---------------- 基础设施 ----------------

/** 让 http://127.0.0.1:8788/ 与 http://127.0.0.1:8788 等价。 */
function normalizeBase(raw) {
  const value = typeof raw === "string" ? raw.trim() : "";
  if (!value) return DEFAULT_BASE_URL;
  return value.replace(/\/+$/, "");
}

/** 画布地址来源：插件设置 [plugins.settings.design-canvas].canvas_url > 默认值。 */
function canvasBase(ctx) {
  const settings = (ctx && ctx.settings) || {};
  return normalizeBase(settings.canvas_url || settings.canvasUrl);
}

/**
 * 读画布的一个只读端点。
 * 返回 { ok:true, url, data } 或 { ok:false, code, message, url }，永不抛异常。
 */
async function canvasGet(ctx, path) {
  const base = canvasBase(ctx);
  const endpoint = base + path;
  if (!ctx || typeof ctx.fetch !== "function") {
    return {
      ok: false,
      code: "capability_missing",
      message: "插件未获得 net.fetch 能力，无法访问画布（检查 plugin.json 的 backend.capabilities）",
      base: base,
      endpoint: endpoint,
    };
  }
  let res;
  try {
    res = await ctx.fetch(endpoint, {
      method: "GET",
      headers: { Accept: "application/json" },
      timeoutMs: FETCH_TIMEOUT_MS,
    });
  } catch (err) {
    return {
      ok: false,
      code: "unreachable",
      message: "画布服务不可达（先让 adago_design 以 serve 或 mcp 形态启动）: " + String(err),
      base: base,
      endpoint: endpoint,
    };
  }
  if (!res || !res.ok) {
    return {
      ok: false,
      code: "http_error",
      message: "画布返回 HTTP " + (res ? res.status : 0),
      base: base,
      endpoint: endpoint,
    };
  }
  try {
    // base 用于展示与台账（链接指向画布首页）；endpoint 只用于报错定位
    return { ok: true, base: base, endpoint: endpoint, data: JSON.parse(res.text || "{}") };
  } catch (err) {
    return { ok: false, code: "bad_json", message: "画布返回的不是 JSON 对象", base: base, endpoint: endpoint };
  }
}

/** 截断到 max 个字符（按码点，避免把 emoji 切一半）。 */
function clip(text, max) {
  const s = typeof text === "string" ? text : String(text == null ? "" : text);
  const chars = Array.from(s);
  return chars.length > max ? chars.slice(0, max - 1).join("") + "…" : s;
}

function nodeSize(node) {
  const w = Number(node && node.width) || 0;
  const h = Number(node && node.height) || 0;
  return w > 0 && h > 0 ? w + "×" + h : "";
}

/** 按出现次数降序，次数相同按名称升序 —— 保证同一份画布每次生成的台账稳定。 */
function topEntries(map, limit) {
  const rows = [];
  map.forEach((count, label) => rows.push({ label: label, value: count }));
  rows.sort((a, b) => b.value - a.value || a.label.localeCompare(b.label));
  return rows.slice(0, limit);
}

/**
 * 把 /api/nodes 的返回归一成「台账」——既是工具 details，
 * 也是动态块 data（字段必须与 schemas/canvas-ledger.json 一一对应）。
 */
function buildLedger(url, payload, now) {
  const list = Array.isArray(payload && payload.nodes) ? payload.nodes : [];
  const bySystem = new Map();
  const byArtboard = new Map();
  for (const node of list) {
    const system = node && typeof node.system === "string" && node.system ? node.system : "未标注";
    bySystem.set(system, (bySystem.get(system) || 0) + 1);
    const artboard = node && typeof node.artboard === "string" && node.artboard ? node.artboard : "无画板";
    byArtboard.set(artboard, (byArtboard.get(artboard) || 0) + 1);
  }
  const artboards = topEntries(byArtboard, MAX_GROUPS);
  const subtitleParts = [];
  if (artboards.length > 0) {
    subtitleParts.push(artboards.map((row) => row.label + " ×" + row.value).join(" · "));
  }

  const nodes = list.slice(0, MAX_NODES).map((node) => ({
    title: clip((node && (node.title || node.id)) || "(无标题)", 60),
    system: clip((node && node.system) || "未标注", 24),
    artboard: clip((node && node.artboard) || "无画板", 24),
    size: clip(nodeSize(node), 20),
  }));

  return {
    title: "设计画布台账",
    subtitle: clip(subtitleParts.join(" · "), 118),
    total: list.length,
    canvasUrl: clip(url, 180),
    groups: topEntries(bySystem, MAX_GROUPS),
    nodes: nodes,
    syncedAt: now,
  };
}

/** 台账 → 一段可直接贴进回答的 adag-ui 封套（模型照抄即可渲染）。 */
function ledgerFence(ledger) {
  const envelope = {
    renderer: RENDERER_ID,
    id: "canvas-ledger-1",
    version: 1,
    fallback: "设计画布台账：" + ledger.total + " 个节点（动态块不可用）",
    data: {
      title: ledger.title,
      subtitle: ledger.subtitle,
      total: ledger.total,
      canvasUrl: ledger.canvasUrl,
      groups: ledger.groups,
      nodes: ledger.nodes,
    },
  };
  return "```adag-ui\n" + JSON.stringify(envelope, null, 2) + "\n```";
}

function ledgerText(ledger) {
  const lines = ["画布 " + ledger.canvasUrl + "：共 " + ledger.total + " 个节点。"];
  if (ledger.groups.length > 0) {
    lines.push("体系分布：" + ledger.groups.map((g) => g.label + " " + g.value).join(" / "));
  }
  if (ledger.total > ledger.nodes.length) {
    lines.push("（台账仅保留前 " + ledger.nodes.length + " 个节点，完整清单用 canvas_list）");
  }
  return lines.join("\n");
}

async function loadCached(ctx) {
  try {
    const raw = await ctx.state.get(CACHE_KEY);
    if (!raw) return null;
    const parsed = typeof raw === "string" ? JSON.parse(raw) : raw;
    return parsed && typeof parsed.total === "number" ? parsed : null;
  } catch (err) {
    return null;
  }
}

async function saveCached(ctx, ledger) {
  try {
    await ctx.state.set(CACHE_KEY, ledger);
  } catch (err) {
    // 缓存失败不影响本次结果，下次重新拉取即可
    if (ctx && typeof ctx.log === "function") {
      ctx.log("warn", "台账缓存写入失败", { err: String(err) });
    }
  }
}

async function syncLedger(ctx) {
  const res = await canvasGet(ctx, "/api/nodes");
  if (!res.ok) {
    return { ok: false, code: res.code, message: res.message, base: res.base, endpoint: res.endpoint };
  }
  const ledger = buildLedger(res.base, res.data, new Date().toISOString());
  await saveCached(ctx, ledger);
  return { ok: true, ledger: ledger };
}

// ---------------- 注册 ----------------

export default function register(agent) {
  // 1) 同步台账：唯一会出网的工具，只读（GET /api/nodes，不改画布）
  agent.registerTool({
    name: "design_canvas_sync",
    description:
      "读取 adago_design 无限画布的当前节点，生成设计台账并缓存到本线程。" +
      "只读，不修改画布。画布未启动时返回可读原因，不报错。",
    parameters: {
      type: "object",
      properties: {
        include_zero: {
          type: "boolean",
          description: "保留该参数仅为兼容性占位；空画布也会正常返回 0 个节点",
        },
      },
      additionalProperties: false,
    },
    access: "read",
    timeoutMs: 8000,
    async execute(args, ctx) {
      const result = await syncLedger(ctx);
      if (!result.ok) {
        return {
          content: [
            {
              type: "text",
              text:
                "无法读取画布：" + result.message + "\n" +
                "处理建议：先让 adago_design 以 serve 或 mcp 形态启动（地址 " + result.base + "），" +
                "或在插件设置里把 canvas_url 指到实际端口；设计规范与预览仍可继续用 MCP 工具。",
            },
          ],
          details: { error: result.code, canvasUrl: result.base, endpoint: result.endpoint },
        };
      }
      const ledger = result.ledger;
      const text = [
        "已同步画布台账：" + ledgerText(ledger),
        "",
        "需要展示时把下面这段整块贴进回答（renderer " + RENDERER_ID + "）：",
        ledgerFence(ledger),
      ].join("\n");
      return { content: [{ type: "text", text: text }], details: { ledger: ledger } };
    },
  });

  // 2) 取台账：默认读线程缓存，refresh=true 时才重新出网
  agent.registerTool({
    name: "design_canvas_ledger",
    description:
      "取本线程缓存的设计台账（含可直接粘贴的 adag-ui 动态块）。" +
      "refresh=true 时重新读取画布；无缓存且画布不可达时返回最近一次成功同步的说明。",
    parameters: {
      type: "object",
      properties: {
        refresh: { type: "boolean", description: "是否重新读取画布，默认 false（用缓存）" },
      },
      additionalProperties: false,
    },
    access: "read",
    timeoutMs: 8000,
    async execute(args, ctx) {
      const refresh = !!(args && args.refresh);
      let ledger = refresh ? null : await loadCached(ctx);
      let source = "cache";
      if (!ledger) {
        const result = await syncLedger(ctx);
        if (!result.ok) {
          return {
            content: [
              {
                type: "text",
                text:
                  "还没有台账，且当前读不到画布：" + result.message + "\n" +
                  "先跑一次 design_canvas_sync，或在画布启动后带 refresh=true 重试。",
              },
            ],
            details: { error: result.code, canvasUrl: result.base, endpoint: result.endpoint },
          };
        }
        ledger = result.ledger;
        source = "canvas";
      }
      const text = [
        "台账来源：" + (source === "cache" ? "线程缓存" : "实时读取") + "（" + ledger.syncedAt + "）",
        ledgerText(ledger),
        "",
        ledgerFence(ledger),
      ].join("\n");
      return { content: [{ type: "text", text: text }], details: { ledger: ledger, source: source } };
    },
  });

  // 3) route：给插件页/前端脚本读台账（浏览器经 /ext/design-canvas/api/ledger 访问）
  agent.route("GET", "/ledger", async (req, ctx) => {
    const ledger = await loadCached(ctx);
    if (!ledger) {
      return { ok: false, error: { message: "本线程还没有台账，先调用工具 design_canvas_sync" } };
    }
    return {
      ok: true,
      renderer: RENDERER_ID,
      ledger: ledger,
      fetchedAt: ledger.syncedAt,
    };
  });

  // 4) 系统提示片段：只放「必须遵守的硬规则」，流程放技能里按需读
  agent.registerPrompt({
    id: "design-workflow-guard",
    priority: 20,
    content:
      "做界面设计时遵守 adago_design MCP 的顺序：先定画板尺寸（design_artboard），" +
      "再查规范（design_tokens / design_components / design_icons / design_fonts），" +
      "然后用 design_preview 或 design_screen 生成预览（必须带 artboard），" +
      "最后 design_audit 走查。图标只能用 design_icons 返回的 SVG，" +
      "禁止 emoji 或 ▾ › ✕ ◉ 等字符充当图标；字体只用 design_fonts 中 commercial=true 的条目。" +
      "需要汇总画布成果时调用 design_canvas_sync（本插件），不要自己猜节点数量。",
  });
}
