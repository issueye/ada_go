// tools-ask —— 官方插件：ask_user（05 §6.3 的 tools-ask、02 §5.4 的 ask 类工具）。
//
// 模型用这一个工具向用户提问，三种形态：
//   confirm —— 是/否；select —— 在 options 里选一个；input —— 自由输入。
//
// 两条失败关闭（比三态本身更重要，INV-4）：
//   - ctx.ui 不存在或 available=false（无界面、断线）：不问，直接按 denied 语义报错；
//   - 用户拒绝 / 取消 / 超时（宿主返回 null）：同样按 denied 语义报错。
// 工具标了 terminatesTurn：拿到答案即收尾（02 §5.4 的 ask 类），答案已作为工具
// 结果进历史，下一轮模型可以接着处理。
//
// access=read：提问本身没有外部效果；能不能问由 ui.ask 能力位决定（只授予 builtin）。

interface UIContext {
  available?: boolean;
  confirm(title: string, message: string, opts?: { timeoutMs?: number }): Promise<boolean | null>;
  select(title: string, options: string[], opts?: { timeoutMs?: number }): Promise<string | null>;
  input(title: string, message: string, opts?: { timeoutMs?: number }): Promise<string | null>;
}

interface ToolContext {
  ui?: UIContext;
  log: (level: string, msg: string, fields?: object) => void;
}

interface ToolDef {
  name: string;
  description: string;
  parameters: object;
  access: "read" | "write" | "exec" | "network";
  terminatesTurn?: boolean;
  execute: (args: any, ctx: ToolContext) => unknown;
}

interface AgentAPI {
  registerTool(def: ToolDef): void;
}

type AskKind = "confirm" | "select" | "input";

function fail(code: string, message: string): never {
  const e = new Error(message);
  (e as any).code = code;
  throw e;
}

// denied 把「没问成 / 用户没同意」收敛成同一个失败关闭语义。
function denied(reason: string): never {
  fail("ERR_ASK_DENIED", reason);
}

function requireUI(ctx: ToolContext): UIContext {
  if (!ctx.ui) {
    denied("当前会话没有可用的用户界面（ui.ask 未授予或未接 UI），ask_user 失败关闭");
  }
  if (ctx.ui!.available === false) {
    denied("用户界面不可用（无头模式或连接已断开），ask_user 失败关闭");
  }
  return ctx.ui!;
}

export default function (agent: AgentAPI): void {
  agent.registerTool({
    name: "ask_user",
    description:
      "向用户提问并等待回答。kind=confirm 问是/否；kind=select 让用户在 options 里选一个；" +
      "kind=input 让用户输入一行文本。用户拒绝、取消、超时或没有可用界面时本工具失败" +
      "（失败关闭）：不要重复追问，改用其它方法或在回复里说明。",
    access: "read",
    terminatesTurn: true,
    parameters: {
      type: "object",
      properties: {
        question: { type: "string", description: "要问的问题（一句话说清需要用户决定什么）" },
        kind: { type: "string", enum: ["confirm", "select", "input"], description: "提问形态" },
        options: {
          type: "array",
          items: { type: "string" },
          description: "kind=select 时的候选项（2–8 个；其它形态不需要）",
        },
      },
      required: ["question", "kind"],
    },
    execute: async (args: any, ctx: ToolContext) => {
      const question = typeof args?.question === "string" ? args.question.trim() : "";
      if (question.length === 0) fail("ERR_INVALID_ARG_TYPE", "question 必须是非空字符串");
      const kind = String(args?.kind ?? "") as AskKind;
      if (kind !== "confirm" && kind !== "select" && kind !== "input") {
        fail("ERR_INVALID_ARG_VALUE", `kind 必须是 confirm | select | input，得到 ${JSON.stringify(args?.kind)}`);
      }

      const ui = requireUI(ctx);
      let answer: string;
      switch (kind) {
        case "select": {
          const raw = args?.options;
          if (!Array.isArray(raw)) fail("ERR_INVALID_ARG_TYPE", "kind=select 需要 options 字符串数组");
          const options = raw.map((o: unknown) => String(o)).filter((o: string) => o.length > 0);
          if (options.length < 2 || options.length > 8) {
            fail("ERR_INVALID_ARG_VALUE", "options 需要 2–8 个非空候选项");
          }
          const picked = await ui.select(question, options);
          if (picked === null || picked === undefined) denied("用户取消或超时，未做出选择");
          if (!options.includes(picked)) denied("用户返回了候选项之外的值，按拒绝处理");
          answer = picked;
          break;
        }
        case "input": {
          const typed = await ui.input(question, question);
          if (typed === null || typed === undefined) denied("用户取消或超时，未输入内容");
          if (String(typed).trim().length === 0) denied("用户提交了空输入，按取消处理");
          answer = String(typed);
          break;
        }
        default: {
          const ok = await ui.confirm(question, question);
          if (ok === null || ok === undefined) denied("用户拒绝、取消或超时，未得到确认");
          answer = ok === true ? "yes" : "no";
          break;
        }
      }

      ctx.log("info", "ask_user answered", { kind, answerChars: answer.length });
      return {
        content: [{ type: "text", text: `用户的回答（${kind}）：${answer}` }],
        details: { kind, question, answer },
      };
    },
  });
}
