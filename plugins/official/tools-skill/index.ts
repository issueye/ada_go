// tools-skill —— 官方插件：技能检索（6.7）。
//
// 基座在装配期把 name/description/path 注入系统提示；正文不进入基座内存。
// 本插件只提供搜索：当技能多、模型记不全时，按关键词在 name/description 里做子串匹配，
// 返回候选列表；需要正文时模型用 read_file 读取 XML 里的 path。

interface SkillInfo {
  name: string;
  description: string;
  origin: "workspace" | "plugin";
  pluginId?: string;
  path?: string;
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
  timeoutMs?: number;
  execute: (args: any, ctx: ToolContext) => unknown;
}

interface PromptDef {
  id: string;
  content: string;
  priority?: number;
}

interface AgentAPI {
  registerTool(def: ToolDef): void;
  registerPrompt?(def: PromptDef): void;
}

const MAX_RESULTS = 50;
const SKILL_READ_HINT =
  "系统提示里的「可用技能」带有 name、description 与 path。需要某个技能的完整正文时，" +
  "直接用 read_file 读取该技能的 path；不要一次读完所有技能。";

function fail(code: string, message: string): never {
  const e = new Error(message);
  (e as any).code = code;
  throw e;
}

// matches 单字段子串匹配（大小写不敏感；中缀即可，不做分词）。
function matches(haystack: string, needle: string): boolean {
  return haystack.toLowerCase().indexOf(needle.toLowerCase()) >= 0;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "skill_search",
    description:
      "按关键词搜索已装配的技能（匹配技能名与简介的子串）。返回候选列表后，" +
      "用 read_file 读取对应 path 获取正文。当前装配没有任何技能时返回空列表。",
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
        details: {
          query,
          total: all.length,
          hits: hits.length,
          truncated: hits.length > shown.length,
          paths: shown.map((s) => s.path).filter((p): p is string => typeof p === "string" && p.length > 0),
        },
      };
    },
  });

  if (agent.registerPrompt) {
    agent.registerPrompt({ id: "skills-read", content: SKILL_READ_HINT, priority: 20 });
  }
}
