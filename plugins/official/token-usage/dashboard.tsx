/**
 * Token 用量卡片（official/token-usage 的 dashboard 贡献）。
 *
 * 只依赖 props.data.usage；没有数据时返回 null（概览页不留空卡）。
 */

function formatNumber(value: any): string {
  const n = Number(value || 0);
  if (!isFinite(n)) return "0";
  const text = String(Math.round(n));
  let out = "";
  for (let i = 0; i < text.length; i += 1) {
    if (i > 0 && (text.length - i) % 3 === 0) out += ",";
    out += text.charAt(i);
  }
  return out;
}

function formatCost(micros: any): string {
  const n = Number(micros || 0);
  if (!isFinite(n) || n <= 0) return "—";
  return "$" + (n / 1000000).toFixed(4);
}

export default function Dashboard(props: any) {
  const data = props && props.data ? props.data : null;
  const usage = data && data.usage ? data.usage : null;
  const workspace = usage && usage.workspace ? usage.workspace : null;
  const thread = usage && usage.thread ? usage.thread : null;
  if (!workspace && !thread) return null;

  const workspaceTotal =
    Number(workspace && workspace.inputTokens ? workspace.inputTokens : 0) +
    Number(workspace && workspace.outputTokens ? workspace.outputTokens : 0);

  return (
    <div class="card p-4 tu-widget">
      <div class="tu-widget__head">
        <span class="tu-widget__title">Token 用量</span>
        <span class="tu-widget__total">{formatNumber(workspaceTotal)}</span>
      </div>
      <p class="tu-widget__meta">
        工作区：输入 {formatNumber(workspace && workspace.inputTokens)} · 输出 {formatNumber(workspace && workspace.outputTokens)} · 缓存 {formatNumber(workspace && workspace.cacheTokens)} · 成本 {formatCost(workspace && workspace.costMicros)}
      </p>
      {thread ? (
        <p class="tu-widget__meta">
          当前会话：输入 {formatNumber(thread.inputTokens)} · 输出 {formatNumber(thread.outputTokens)} · 缓存 {formatNumber(thread.cacheTokens)}
        </p>
      ) : null}
    </div>
  );
}
