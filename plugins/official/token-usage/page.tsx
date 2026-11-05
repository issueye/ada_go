/**
 * Token 用量（official/token-usage）：工作区与当前会话的 token 聚合。
 *
 * 数据来自 Core usage.get 的只读投影 props.data.usage：
 *   { thread: {provider,model,inputTokens,outputTokens,cacheTokens,costMicros}, workspace: {...} }
 * 插件只做展示与换算，不自己统计 token。
 *
 * P2-4 首个真实页面迁移：同一份 defineComponent 服务端 renderToJSON + 浏览器
 * hydrate；「显示缓存命中率」是客户端本地状态，不重新请求数据。
 */
import { defineComponent } from "ada:render";

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

function totalOf(usage: any): number {
  if (!usage) return 0;
  return Number(usage.inputTokens || 0) + Number(usage.outputTokens || 0);
}

function cacheRate(usage: any): string {
  const input = Number((usage && usage.inputTokens) || 0);
  const cache = Number((usage && usage.cacheTokens) || 0);
  if (!isFinite(input) || input <= 0) return "—";
  return Math.round((cache * 100) / input) + "%";
}

function UsageCard(h: any, title: string, usage: any, hint: string) {
  const total = totalOf(usage);
  return (
    <section class="card tu-card">
      <div class="tu-card__head">
        <span class="tu-card__title">{title}</span>
        <span class="tu-card__total" title={formatNumber(total) + " tokens"}>{formatNumber(total)}</span>
      </div>
      <p class="tu-card__hint">{hint}</p>
      <div class="tu-stats">
        <div class="tu-stat">
          <span class="tu-stat__label">输入</span>
          <span class="tu-stat__value">{formatNumber(usage && usage.inputTokens)}</span>
        </div>
        <div class="tu-stat">
          <span class="tu-stat__label">输出</span>
          <span class="tu-stat__value">{formatNumber(usage && usage.outputTokens)}</span>
        </div>
        <div class="tu-stat">
          <span class="tu-stat__label">缓存</span>
          <span class="tu-stat__value">{formatNumber(usage && usage.cacheTokens)}</span>
        </div>
        <div class="tu-stat">
          <span class="tu-stat__label">成本</span>
          <span class="tu-stat__value">{formatCost(usage && usage.costMicros)}</span>
        </div>
      </div>
      {usage && usage.provider ? (
        <p class="tu-card__model">
          {usage.provider}
          {usage.model ? " / " + usage.model : ""}
        </p>
      ) : null}
    </section>
  );
}

export default defineComponent({
  props: { data: { default: {} } },
  data() {
    return { showCache: false };
  },
  methods: {
    toggleCache() {
      this.showCache = !this.showCache;
    },
  },
  render(h) {
    const usage = this.data && this.data.usage ? this.data.usage : null;
    return (
      <div class="tu-page">
        <div class="tu-head">
          <div>
            <h1 class="tu-title">Token 用量</h1>
            <p class="tu-sub">工作区聚合与当前会话累计；数据来自 Core 的 usage.get。</p>
          </div>
          <button
            class="ada-btn ada-btn--secondary ada-btn--sm"
            type="button"
            aria-pressed={this.showCache ? "true" : "false"}
            onClick={this.toggleCache}
          >
            {this.showCache ? "隐藏缓存命中率" : "显示缓存命中率"}
          </button>
        </div>
        <div class="tu-grid">
          {UsageCard(h, "工作区", usage && usage.workspace, "所有会话的累计用量")}
          {UsageCard(h, "当前会话", usage && usage.thread, "本条线程的累计用量")}
        </div>
        {this.showCache ? (
          <p class="tu-card__model">
            缓存命中率：工作区 {cacheRate(usage && usage.workspace)}，当前会话 {cacheRate(usage && usage.thread)}
          </p>
        ) : null}
      </div>
    );
  },
});
