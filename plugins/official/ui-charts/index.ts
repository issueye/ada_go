// ui-charts —— 官方插件：通用图表（SPEC §8.1/8.2 的动态块渲染器 + 配套工具）。
//
// 前端段提供六个 Markdown 动态块 renderer（bar/column/compare/stacked/heatmap/spark），
// 由 Gateway 渲染桥装配；本文件是后端段，只注册一个只读工具 chart_guide：
// 当模型需要把数据展示成图表时，用它查询 renderer 的用法、数据 Schema 与
// adag-ui 封套示例，拿到确切的 renderer id + data，而不是自己猜。
//
// 图表数据是模型从工具结果/用户输入里挑选的静态展示数据，不触发写审批，
// 因此工具 access=read，不申请任何能力位。

interface ToolContext {
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

// 每种 renderer 的用途、适用场景与 data 要点。这个表与 SPEC §8.1、README 保持同步；
// 改 renderer 或 Schema 时三处一起改。
interface RendererInfo {
  id: string;
  title: string;
  usage: string;
  bestFor: string;
  dataExample: string;
  note: string;
}

const RENDERERS: RendererInfo[] = [
  {
    id: "ui-charts/bar",
    title: "横向条",
    usage: "按数值大小对比多个条目的横向条形图，适合条目多、名称长、强调排名。",
    bestFor: "排行榜、资源占用、统计对比",
    dataExample:
      '{"title":"仓库大小","unit":"MB","items":[{"label":"core","value":42},{"label":"web","value":28},{"label":"docs","value":9}]}',
    note: "items 最多 12 条；value 为 number。",
  },
  {
    id: "ui-charts/column",
    title: "纵向柱",
    usage: "按数值大小对比的纵向柱状图，适合时间序列、离散类别对比。",
    bestFor: "月度趋势、按类别的统计",
    dataExample:
      '{"title":"月度访问","unit":"k","items":[{"label":"1月","value":12},{"label":"2月","value":19},{"label":"3月","value":15}]}',
    note: "items 最多 12 条；value 为 number。",
  },
  {
    id: "ui-charts/compare",
    title: "双条对比",
    usage: "每个条目两组数值的对比图（两条并排的横向条）。",
    bestFor: "计划 vs 实际、去年 vs 今年",
    dataExample:
      '{"title":"目标 vs 实际","unit":"%","legendA":"目标","legendB":"实际","items":[{"label":"Q1","value":80,"value2":65},{"label":"Q2","value":80,"value2":72}]}',
    note: "items 最多 12 条；value 与 value2 都为 number。",
  },
  {
    id: "ui-charts/stacked",
    title: "堆叠占比",
    usage: "多个条目占总量的 100% 堆叠条，按比例着色并给出百分比。",
    bestFor: "占比构成、份额分布",
    dataExample:
      '{"title":"技术栈占比","items":[{"label":"Go","value":45},{"label":"TypeScript","value":30},{"label":"其它","value":25}]}',
    note: "items 最多 12 条；按 value 总和算占比，最多 4 段调色板循环。",
  },
  {
    id: "ui-charts/heatmap",
    title: "热力方块",
    usage: "按等级着色的热力方块网格，适合密集的多维状态展示。",
    bestFor: "一周活跃、仓库热度、依赖健康度",
    dataExample:
      '{"title":"仓库活跃度","items":[{"label":"周一","value":3},{"label":"周二","value":5},{"label":"周三","value":2}]}',
    note: "items 最多 12 条；value 自动换算 5 档，也可显式传 level（0..5）。",
  },
  {
    id: "ui-charts/spark",
    title: "迷你折线",
    usage: "用文本块字符绘制的迷你折线，适合在段落里展示紧凑趋势。",
    bestFor: "趋势概览、涨跌方向",
    dataExample: '{"title":"近 7 日趋势","values":[1,3,2,5,4,6,5]}',
    note: "values 最多 48 个 number，不走 items。",
  },
];

// 公共字段（title 必填；subtitle/unit 可选）与 adag-ui 封套的通用约束。
const COMMON_FIELDS = `公共字段（所有 renderer 通用）：
- title（必填）：图表标题，≤80 字符
- subtitle（可选）：副标题，≤120 字符
- unit（可选）：数值单位后缀，≤16 字符

公共约束：
- 未知字段会被 Schema 拒绝（additionalProperties: false）
- items 内每个条目：label（必填，≤80 字符）+ value（必填 number）+ value2（可选，compare 用）+ note（可选，≤80 字符）
- 比例一律换算成固定档位 class，不使用 SVG/内联 style（CSP + block profile）`;

// 渲染一个 renderer 的完整说明文本。
function rendererDoc(r: RendererInfo): string {
  return [
    `## ${r.id}（${r.title}）`,
    r.usage,
    `适合：${r.bestFor}`,
    `data 示例：\n\`\`\`json\n${r.dataExample}\n\`\`\``,
    `注意：${r.note}`,
  ].join("\n");
}

function fail(code: string, message: string): never {
  const e = new Error(message);
  (e as any).code = code;
  throw e;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "chart_guide",
    description:
      "查询【通用图表】插件（ui-charts）的 Markdown 动态块用法：renderer id、数据 Schema、" +
      "适用场景与 adag-ui 封套示例。需要把数据展示成条形图/柱状图/双条对比/堆叠占比/" +
      "热力图/迷你折线时调用，拿到确切的 renderer id 与 data，而不是自己猜。",
    access: "read",
    parameters: {
      type: "object",
      properties: {
        kind: {
          type: "string",
          enum: ["bar", "column", "compare", "stacked", "heatmap", "spark"],
          description: "要查询的图表形态；省略时返回全部 renderer 的用法",
        },
      },
      required: [],
      additionalProperties: false,
    },
    execute: async (args: any, ctx: ToolContext) => {
      const kind = typeof args?.kind === "string" ? args.kind.trim() : "";
      const all = kind.length === 0;
      const hit = RENDERERS.find((r) => r.id.split("/")[1] === kind);

      if (!all && !hit) {
        fail("ERR_INVALID_ARG_VALUE", `未知图表形态 ${JSON.stringify(kind)}（可选：${RENDERERS.map((r) => r.id.split("/")[1]).join(", ")}）`);
      }

      const lines: string[] = [];
      lines.push("# 通用图表（ui-charts）渲染指南");
      lines.push("");
      lines.push("在回答里用 adag-ui 动态块展示结构化数据时，按下面的 renderer 与 data 形状构造封套。");
      lines.push("");
      lines.push(COMMON_FIELDS);
      lines.push("");
      if (all) {
        lines.push("# 全部 renderer");
        for (const r of RENDERERS) {
          lines.push(rendererDoc(r));
          lines.push("");
        }
      } else {
        lines.push(rendererDoc(hit!));
      }
      lines.push("# adag-ui 封套语法");
      lines.push("在 Markdown 里用顶层 fenced block，fence 名固定为 adag-ui，内容是 JSON 封套：");
      lines.push("");
      lines.push("```adag-ui");
      lines.push('{');
      lines.push('  "renderer": "ui-charts/bar",');
      lines.push('  "id": "b1",');
      lines.push('  "version": 1,');
      lines.push('  "fallback": "图表暂不可用",');
      lines.push('  "data": { "title": "示例", "items": [{"label": "a", "value": 1}] }');
      lines.push('}');
      lines.push("```");
      lines.push("");
      lines.push("规则：renderer 必须是 ui-charts/bar|column|compare|stacked|heatmap|spark 之一；");
      lines.push("data 只放展示所需的最小静态数据；始终写 fallback；一块只表达一个主题，一次回答 ≤ 8 块。");

      ctx.log("info", "chart_guide", { kind: kind || "all", hit: hit ? 1 : 0 });

      return {
        content: [{ type: "text", text: lines.join("\n") }],
        details: {
          kind: kind || "all",
          renderers: RENDERERS.map((r) => r.id),
          selected: hit ? hit.id : null,
        },
      };
    },
  });
}