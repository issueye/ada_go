// hello-echo —— 第三方示例插件（M3）。
//
// 注意：第三方插件与官方插件走同一个 moejs 宿主，宿主不做 TypeScript 编译
// （设计 05 §6.1、06 §6.10：依赖在打包期内联，入口是宿主可直接执行的 ESM）。
// 因此本文件虽名为 .ts，内容是纯 JavaScript；类型形状见 sdk/agent.d.ts。
//
// 能力清单：无。trust=third-party，没有 fs/net/exec 等任何可安装能力，
// 只贡献一个 access=read 的工具（只读工具不需要策略插件即可用，5.4）。

export default function register(agent) {
  agent.registerTool({
    name: "echo",
    description: "原样返回 text 参数，用于验证第三方插件的加载、禁用与热重载。",
    parameters: {
      type: "object",
      properties: {
        text: { type: "string", description: "要回显的文本" },
      },
      required: ["text"],
      additionalProperties: false,
    },
    access: "read",
    async execute(args, ctx) {
      const text = args && typeof args.text === "string" ? args.text : "";
      ctx.log("info", "echo 工具被调用", { length: text.length });
      return {
        content: [{ type: "text", text: text }],
      };
    },
  });
}
