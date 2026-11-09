// tool-discovery —— 系统插件：发现式工具的入口（find_tool / exec_tool）。
//
// 目录与内层执行都由宿主桥负责（capability: tool.discovery）：
//   - ctx.catalog.list 只返回 discovery=true 的目录快照；
//   - ctx.tool.invoke 由宿主按内层描述符重跑 beforeCall → policy → Runner，
//     插件不能绕过审批，也不能直接调用 Executor。
//
// 这两个名字是宿主保留名；只有持有 tool.discovery 的 builtin 插件能注册。

export default function (agent) {
  agent.registerTool({
    name: "find_tool",
    description:
      "发现可用工具：列出本会话里未直接注入的工具（名称/说明/参数 Schema）。" +
      "拿到结果后用 exec_tool 执行；没有匹配项时返回空数组。",
    parameters: {
      type: "object",
      properties: {
        query: {
          type: "string",
          description: "可选关键词：按名称或说明过滤（大小写不敏感的包含匹配）。省略时列出全部。",
        },
      },
      additionalProperties: false,
    },
    access: "read",
    async execute(args, ctx) {
      const query = args && args.query ? String(args.query) : "";
      const list = await ctx.catalog.list({ query });
      const out = (list || []).slice(0, 64).map((t) => ({
        name: t.name,
        description: t.description,
        access: t.access,
        parameters: t.parameters,
      }));
      return {
        content: [{ type: "text", text: JSON.stringify(out) }],
        details: { count: out.length, query },
      };
    },
  });

  agent.registerTool({
    name: "exec_tool",
    description:
      "执行一个发现式工具：tool 填 find_tool 结果里的名称，args 按它的 parameters Schema 构造。" +
      "审批与超时沿用该工具自身的配置；只有 find_tool 列出的工具才能这样执行。",
    parameters: {
      type: "object",
      required: ["tool"],
      properties: {
        tool: { type: "string", description: "要执行的工具名（来自 find_tool 的结果）" },
        args: { type: "object", description: "该工具的参数，必须符合 find_tool 返回的 parameters Schema" },
      },
      additionalProperties: false,
    },
    access: "read",
    async execute(args, ctx) {
      if (!args || !args.tool) {
        return { content: [{ type: "text", text: "invalid_args: exec_tool 缺少 tool；先用 find_tool 查看可用工具" }] };
      }
      const res = await ctx.tool.invoke({ tool: String(args.tool), args: args.args || {} });
      const details = Object.assign({}, res.details || {}, { tool: args.tool, innerStatus: res.status });
      return {
        content: res.content && res.content.length
          ? res.content
          : [{ type: "text", text: String(args.tool) + " 以 " + String(res.status || "error") + " 结束，没有产出内容" }],
        details,
        status: res.status,
        reason: res.reason,
      };
    },
  });
}
