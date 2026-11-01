/**
 * Token 用量 block（Markdown ```adag-ui 内联卡片）。
 *
 * data 是插件自己的静态契约：scope + 可选的 usage 数值。
 * 这里不做查询、不读全局状态；需要实时统计时走 /ext/token-usage/usage 页面。
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

function Stat(label: string, value: string) {
  return (
    <div class="tu-stat">
      <span class="tu-stat__label">{label}</span>
      <span class="tu-stat__value">{value}</span>
    </div>
  );
}

export default function UsageCardBlock(props: any) {
  const data = props && props.block && props.block.data ? props.block.data : {};
  const usage = data.usage || {};
  const title = data.title || (data.scope === "workspace" ? "工作区用量" : "本会话用量");
  const total = Number(usage.inputTokens || 0) + Number(usage.outputTokens || 0);

  return (
    <section class="card tu-card">
      <div class="tu-card__head">
        <span class="tu-card__title">{title}</span>
        <span class="tu-card__total" title={formatNumber(total) + " tokens"}>{formatNumber(total)}</span>
      </div>
      <p class="tu-card__hint">
        {data.scope === "workspace" ? "工作区累计" : "当前会话累计"}
        {usage.provider ? " · " + usage.provider : ""}
        {usage.model ? " / " + usage.model : ""}
      </p>
      <div class="tu-stats">
        {Stat("输入", formatNumber(usage.inputTokens))}
        {Stat("输出", formatNumber(usage.outputTokens))}
        {Stat("缓存", formatNumber(usage.cacheTokens))}
        {Stat("成本", formatCost(usage.costMicros))}
      </div>
    </section>
  );
}
