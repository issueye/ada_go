// token-usage 后端：用量页与卡片的数据面。
//
// 基座只持久化每轮原始用量行，并通过 ctx.events.usage 暴露；本插件自己
// 按线程/工作区/模型聚合展示口径。禁用本插件后页面/卡片/导航消失，基座
// 不再补一个 usage.get 展示投影。

function number(value) {
  const n = Number(value);
  return Number.isFinite(n) ? n : 0;
}

function summarize(rows) {
  const groups = new Map();
  let input = 0;
  let output = 0;
  let cache = 0;
  let cost = 0;
  for (const row of rows || []) {
    const n = number(row.inputTokens);
    const o = number(row.outputTokens);
    const c = number(row.cacheTokens);
    const m = number(row.costMicros);
    input += n;
    output += o;
    cache += c;
    cost += m;
    const key = String(row.provider || "") + "\u0000" + String(row.model || "");
    const prev = groups.get(key) || {
      provider: String(row.provider || ""),
      model: String(row.model || ""),
      inputTokens: 0, outputTokens: 0, cacheTokens: 0, costMicros: 0, rows: 0,
    };
    prev.inputTokens += n;
    prev.outputTokens += o;
    prev.cacheTokens += c;
    prev.costMicros += m;
    prev.rows += 1;
    groups.set(key, prev);
  }
  const list = Array.from(groups.values()).sort(
    (a, b) => b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens),
  );
  return {
    provider: list.length === 1 ? list[0].provider : "",
    model: list.length === 1 ? list[0].model : "",
    inputTokens: input,
    outputTokens: output,
    cacheTokens: cache,
    costMicros: cost,
    rows: (rows || []).length,
    groups: list,
  };
}

async function stateOf(ctx, threadId) {
  const workspaceRows = await ctx.events.usage({ limit: 1000 });
  const threadRows = threadId ? await ctx.events.usage({ threadId, limit: 1000 }) : [];
  return {
    workspace: summarize(workspaceRows),
    thread: threadId ? summarize(threadRows) : null,
    threadId: threadId || "",
    generatedAt: new Date().toISOString(),
  };
}

export default function (agent) {
  // 页面 SSR 预取：路由带 threadId 时同时给出当前会话；否则只有工作区。
  agent.registerAction({
    name: "usage",
    mode: "read",
    execute: (_input, ctx) => stateOf(ctx, String(ctx.threadId || "")),
  });

  // 浏览器刷新/导出用：与动作同源，避免首屏与刷新两套口径。
  agent.route("GET", "/usage", (req, ctx) => {
    const threadId = String((req && req.query && req.query.threadId) || ctx.threadId || "").trim();
    return stateOf(ctx, threadId);
  });
}
