// generated from index.ts (sha256:d486a4980ed8e70b) by plugins/official/build.mjs — do not edit
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

                     
                      
                                                                                                  
                                                                                                  
                                                                                               
                                                          
                                                                                 
 

                       
                 
                                                             
 

                   
               
                      
                     
                                                
                           
                                                    
 

                    
                                   
 

                                              

                   
                   
                
                    
 

const MAX_QUESTIONS = 5;

function fail(code        , message        )        {
  const e = new Error(message);
  (e       ).code = code;
  throw e;
}

// denied 把「没问成 / 用户没同意」收敛成同一个失败关闭语义。
function denied(reason        )        {
  fail("ERR_ASK_DENIED", reason);
}

function normalizeKind(value         , where        )          {
  const kind = String(value ?? "");
  if (kind !== "confirm" && kind !== "select" && kind !== "input") {
    fail("ERR_INVALID_ARG_VALUE", `${where}kind 必须是 confirm | select | input，得到 ${JSON.stringify(value)}`);
  }
  return kind           ;
}

function normalizeOptions(raw         , kind         , where        )           {
  if (kind !== "select") return [];
  if (!Array.isArray(raw)) fail("ERR_INVALID_ARG_TYPE", `${where}kind=select 需要 options 字符串数组`);
  const options = raw.map((o         ) => String(o).trim()).filter((o        ) => o.length > 0);
  if (options.length < 2 || options.length > 8) {
    fail("ERR_INVALID_ARG_VALUE", `${where}options 需要 2–8 个非空候选项`);
  }
  return options;
}

// normalizeQuestions 接受两种形状：questions:[{question,kind,options}] 一次问多个，
// 或旧的 question/kind/options 单问（保持兼容）。
function normalizeQuestions(args     )            {
  const list = args && Array.isArray(args.questions) ? args.questions : null;
  if (!list) {
    const question = typeof args?.question === "string" ? args.question.trim() : "";
    if (question.length === 0) {
      fail("ERR_INVALID_ARG_TYPE", "question 必须是非空字符串（多问题用 questions 数组）");
    }
    const kind = normalizeKind(args?.kind, "");
    return [{ question, kind, options: normalizeOptions(args?.options, kind, "") }];
  }
  if (list.length === 0) fail("ERR_INVALID_ARG_TYPE", "questions 不能是空数组");
  if (list.length > MAX_QUESTIONS) {
    fail("ERR_INVALID_ARG_VALUE", `questions 最多 ${MAX_QUESTIONS} 个，得到 ${list.length}`);
  }
  return list.map((raw     , i        ) => {
    const at = `questions[${i}].`;
    const question = String(raw && raw.question != null ? raw.question : "").trim();
    if (question.length === 0) fail("ERR_INVALID_ARG_VALUE", `${at}question 不能为空`);
    const kind = normalizeKind(raw && raw.kind, at);
    return { question, kind, options: normalizeOptions(raw && raw.options, kind, at) };
  });
}

function askOne(ui           , item         )                  {
  switch (item.kind) {
    case "select": {
      return ui.select(item.question, item.options).then((picked) => {
        if (picked === null || picked === undefined) denied("用户取消或超时，未做出选择");
        if (!item.options.includes(picked)) denied("用户返回了候选项之外的值，按拒绝处理");
        return picked;
      });
    }
    case "input": {
      return ui.input(item.question, item.question).then((typed) => {
        if (typed === null || typed === undefined) denied("用户取消或超时，未输入内容");
        if (String(typed).trim().length === 0) denied("用户提交了空输入，按取消处理");
        return String(typed);
      });
    }
    default: {
      return ui.confirm(item.question, item.question).then((ok) => {
        if (ok === null || ok === undefined) denied("用户拒绝、取消或超时，未得到确认");
        return ok === true ? "yes" : "no";
      });
    }
  }
}

// askForm 走一张分步表单（上一题/下一题/提交），一次性收集全部答案。
function askForm(ui           , questions           )                    {
  return ui.form(JSON.stringify(questions)).then((raw) => {
    if (raw === null || raw === undefined) denied("用户取消或超时，未提交表单");
    let parsed      = null;
    try {
      parsed = JSON.parse(String(raw));
    } catch (_err) {
      denied("界面返回的回答不是合法 JSON");
    }
    const list = parsed && Array.isArray(parsed.answers) ? parsed.answers : null;
    if (!list || list.length !== questions.length) denied("回答数量与问题不一致，按取消处理");
    return questions.map((q, i) => {
      const answer = String(list[i] === null || list[i] === undefined ? "" : list[i]).trim();
      if (q.kind === "select" && !q.options.includes(answer)) {
        denied("用户返回了候选项之外的值，按拒绝处理");
      }
      if (q.kind === "input" && answer.length === 0) denied("用户提交了空输入，按取消处理");
      return answer;
    });
  });
}

function requireUI(ctx             )            {
  if (!ctx.ui) {
    denied("当前会话没有可用的用户界面（ui.ask 未授予或未接 UI），ask_user 失败关闭");
  }
  if (ctx.ui .available === false) {
    denied("用户界面不可用（无头模式或连接已断开），ask_user 失败关闭");
  }
  return ctx.ui ;
}

export default function (agent          )       {
  agent.registerTool({
    name: "ask_user",
    description:
      "向用户提问并等待回答（一次可以问多个）。kind=confirm 问是/否；kind=select 在 options 里选一个；" +
      "kind=input 输入一行文本。单问用 question/kind/options，多问用 questions 数组（每项同形，最多 5 个），" +
      "界面会依次弹出。用户拒绝、取消、超时或没有可用界面时本工具失败（失败关闭）：不要重复追问，" +
      "改用其它方法或在回复里说明。",
    access: "read",
    terminatesTurn: true,
    parameters: {
      type: "object",
      properties: {
        question: { type: "string", description: "单个问题（与 questions 二选一）" },
        kind: { type: "string", enum: ["confirm", "select", "input"], description: "单问的形态" },
        options: {
          type: "array",
          items: { type: "string" },
          description: "单问 kind=select 时的候选项（2–8 个；其它形态不需要）",
        },
        questions: {
          type: "array",
          description: "一次问多个问题（最多 5 个，依次弹出）",
          items: {
            type: "object",
            properties: {
              question: { type: "string", description: "问题（一句话说清需要用户决定什么）" },
              kind: { type: "string", enum: ["confirm", "select", "input"] },
              options: { type: "array", items: { type: "string" }, description: "kind=select 时的候选（2–8 个）" },
            },
            required: ["question", "kind"],
          },
        },
      },
    },
    execute: async (args     , ctx             ) => {
      const questions = normalizeQuestions(args);
      const ui = requireUI(ctx);

      // 单问走原来的确认/选择/输入；多问用一张分步表单（上一题/下一题/提交），
      // 一次收集完再交给模型——线程同时只允许一个待答项，逐题弹反而更慢。
      const answers = questions.length === 1
        ? [await askOne(ui, questions[0])]
        : await askForm(ui, questions);

      ctx.log("info", "ask_user answered", { count: questions.length });
      const lines = questions.map((q, i) => `${i + 1}. ${q.question}\n   答：${answers[i]}`);
      const single = questions.length === 1;
      return {
        content: [
          {
            type: "text",
            text: single ? `用户的回答（${questions[0].kind}）：${answers[0]}` : "用户的回答：\n" + lines.join("\n"),
          },
        ],
        details: {
          questions: questions.map((q, i) => ({
            question: q.question,
            kind: q.kind,
            options: q.options,
            answer: answers[i],
          })),
        },
      };
    },
  });
}
