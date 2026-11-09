// composer-telemetry 后端：输入框遥测里的用量部分。
//
// 运行状态/上下文占用仍来自会话页的通用投影（thread.get）；token/缓存/成本
// 由本插件用 events.read 读取每轮原始计数后聚合。禁用本插件后整块辅助区消失。

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
