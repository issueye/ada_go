// composer-telemetry 后端（预编译产物；源文件 index.ts）。
function number(value) {
  const n = Number(value);
  return Number.isFinite(n) ? n : 0;
}
function summarize(rows) {
  let input = 0;
  let output = 0;
  let cache = 0;
  let cost = 0;
  for (const row of rows || []) {
    input += number(row.inputTokens);
    output += number(row.outputTokens);
    cache += number(row.cacheTokens);
    cost += number(row.costMicros);
  }
  return { inputTokens: input, outputTokens: output, cacheTokens: cache, costMicros: cost };
}
async function usageOf(ctx, threadId) {
  const workspace = await ctx.events.usage({ limit: 1000 });
  const thread = threadId ? await ctx.events.usage({ threadId, limit: 1000 }) : [];
  return {
    thread: threadId ? summarize(thread) : null,
    workspace: summarize(workspace),
  };
}
export default function (agent) {
  agent.route("GET", "/usage", async (req, ctx) => {
    const threadId = String((req && req.query && req.query.threadId) || ctx.threadId || "").trim();
    return usageOf(ctx, threadId);
  });
}
