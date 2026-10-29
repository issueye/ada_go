// tools-skill —— 官方插件：技能搜索（6.7 的 tools-skill，保持薄）。
//
// 基座已经把 name/description 注入系统提示，并提供了按名读正文的 read_skill
// （基座工具，不属于任何插件）。本插件只补一件事：当技能多、模型记不全时，
// 按关键词在已装配技能的 name/description 里做子串匹配，返回候选列表。
// 它不读正文、不做安装（M3 的安装走 CLI/zip）：正文永远走 read_skill。
//
// 技能表来自 ctx.skills（宿主按装配注入的只读元数据；不含正文）。宿主没有提供时
// （老宿主或技能能力未启用）工具直接失败，而不是悄悄返回空列表。

interface SkillInfo {
  name: string;
  description: string;
  origin: "workspace" | "plugin";
  pluginId?: string;
}

interface ToolContext {
  skills?: () => SkillInfo[];
  log: (level: string, msg: string, fields?: object) => void;
}

interface ToolDef {
  name: string;
  description: string;
  parameters: object;
  access: "read" | "write" | "exec" | "network";
  execute: (args: any, ctx: ToolContext) => unknown;
}

interface AgentAPI {
  registerTool(def: ToolDef): void;
}

const MAX_RESULTS = 50;

function fail(code: string, message: string): never {
  const e = new Error(message);
  (e as any).code = code;
  throw e;
}

// matchSkill 单字段子串匹配（大小写不敏感；中缀即可，不做分词）。
function matches(haystack: string, needle: string): boolean {
  return haystack.toLowerCase().indexOf(needle.toLowerCase()) >= 0;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "skill_search",
    description:
      "按关键词搜索已装配的技能（匹配技能名与简介的子串）。返回候选列表后，" +
      "用 read_skill 读取正文的完整指引。当前装配没有任何技能时返回空列表。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        query: { type: "string", description: "关键词（对技能名与简介做大小写不敏感的子串匹配）" },
      },
      required: ["query"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const query = typeof args?.query === "string" ? args.query.trim() : "";
      if (query.length === 0) fail("ERR_INVALID_ARG_TYPE", "query 必须是非空字符串");
      if (!ctx.skills) {
        fail("ERR_ACCESS_DENIED", "宿主没有提供技能表（ctx.skills）：本次装配未启用技能或宿主过旧");
      }

      const all = ctx.skills!();
      const hits = all.filter((s) => matches(s.name, query) || matches(s.description, query));
      const shown = hits.slice(0, MAX_RESULTS);

      const lines = shown.map(
        (s) => `- ${s.name}（${s.origin}${s.pluginId ? `，来自插件 ${s.pluginId}` : ""}）：${s.description}`,
      );
      const body =
        hits.length === 0
          ? `没有匹配 ${JSON.stringify(query)} 的技能（已装配 ${all.length} 个）。`
          : lines.join("\n") + (hits.length > shown.length ? `\n…（共 ${hits.length} 个候选，只显示前 ${MAX_RESULTS} 个）` : "");
      ctx.log("info", "skill_search finished", { query, hits: hits.length });

      return {
        content: [{ type: "text", text: body }],
        details: { query, total: all.length, hits: hits.length, truncated: hits.length > shown.length },
      };
    },
  });
}
