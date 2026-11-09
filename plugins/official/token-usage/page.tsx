/**
 * Token 用量（official/token-usage）：工作区与当前会话的 token 聚合。
 *
 * 数据来自插件读取的每轮原始用量行（events.read），页面在插件内聚合：
 *   { thread: {provider,model,inputTokens,outputTokens,cacheTokens,costMicros}, workspace: {...} }
 * 插件只做展示与换算，不自己统计 token。
 *
 * 设计稿里画了「近 7 天趋势 / 按模型拆分 / 环比 +12.4%」，但原始行只含每轮计数
 * 两个标量聚合（没有日期分桶、没有按模型分组、没有上期值），所以这一版全部换成
 * 真实可算的内容：
 *   - 时间范围分段 → 「工作区 / 当前会话」视图切换（两个对象都真实存在）；
 *   - 趋势图 → 「用量构成」三条占比计量条（输入 / 输出 / 缓存按真实数值算比例）；
 *   - 按模型拆分 → 「模型与成本」卡：单组时显示 provider/model 与 costMicros，
 *     多组时后端会把分组折叠，这里如实说明而不是编造拆分；
 *   - 环比 → 去掉，改成「占累计 x%」这类由当前数据直接算出的说明。
 *
 * 仍是同一份 defineComponent 服务端 renderToJSON + 浏览器 hydrate。
 * 说明走 ⓘ tooltip，一次性反馈走底部 toast（方法名 showToast，data 字段叫 toast，
 * 避免 ada-render 单 Proxy 里 data 遮蔽 methods）。
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

function num(usage: any, key: string): number {
  const n = Number(usage && usage[key]);
  return isFinite(n) ? n : 0;
}

function totalOf(usage: any): number {
  return num(usage, "inputTokens") + num(usage, "outputTokens");
}

function cacheRate(usage: any): string {
  const input = num(usage, "inputTokens");
  const cache = num(usage, "cacheTokens");
  if (input <= 0) return "—";
  return Math.round((cache * 100) / input) + "%";
}

function percent(part: number, whole: number): number {
  if (whole <= 0 || part <= 0) return 0;
  return Math.round((part * 100) / whole);
}

function share(part: number, whole: number): string {
  return percent(part, whole) + "%";
}

/** 计量条不用内联 style（SSR VNode 白名单不含 style），按 10% 一档映射类名。 */
function meterClass(part: number, whole: number): string {
  const p = percent(part, whole);
  const bucket = Math.min(100, Math.max(0, Math.round(p / 10) * 10));
  return "tu-meter__fill tu-meter__fill--p" + bucket;
}

function modelLabel(usage: any): string {
  const provider = String((usage && usage.provider) || "");
  const model = String((usage && usage.model) || "");
  if (!provider && !model) return "";
  return provider + (model ? " / " + model : "");
}

export default defineComponent({
  props: { data: { default: {} } },
  data() {
    return {
      view: "workspace",
      toast: null as any,
      toastSeq: 0,
      toastTimer: 0,
    };
  },
  computed: {
    usage(): any {
      const data: any = this.data || {};
      const mine: any = ((data.backend || {})["token-usage"] || {});
      const root: any = mine.page_usage || mine.usage || {};
      return this.view === "thread" ? root.thread || null : root.workspace || null;
    },
    thread(): any {
      const data: any = this.data || {};
      const mine: any = ((data.backend || {})["token-usage"] || {});
      const root: any = mine.page_usage || mine.usage || {};
      return root.thread || null;
    },
    workspace(): any {
      const data: any = this.data || {};
      const mine: any = ((data.backend || {})["token-usage"] || {});
      const root: any = mine.page_usage || mine.usage || {};
      return root.workspace || null;
    },
    totals() {
      const u: any = this.usage;
      const input = num(u, "inputTokens");
      const output = num(u, "outputTokens");
      const cache = num(u, "cacheTokens");
      const total = input + output;
      const whole = input + output + cache;
      return { input, output, cache, total, whole };
    },
    hasData(): boolean {
      return this.totals.whole > 0;
    },
    inputShare(): string {
      return percent(this.totals.input, this.totals.total) + "%";
    },
    outputShare(): string {
      return percent(this.totals.output, this.totals.total) + "%";
    },
    modelText(): string {
      return modelLabel(this.workspace);
    },
  },
  methods: {
    showToast(message: string, tone?: string) {
      const seq = (this.toastSeq || 0) + 1;
      this.toastSeq = seq;
      this.toast = { id: seq, message, tone: tone || "info" };
      if (this.toastTimer) clearTimeout(this.toastTimer);
      this.toastTimer = setTimeout(() => {
        if (this.toast && this.toast.id === seq) this.toast = null;
      }, tone === "error" ? 4200 : 2400);
    },
    setView(view: string) {
      this.view = view;
    },
    summaryText(): string {
      const w: any = this.workspace;
      const t: any = this.thread;
      const row = (label: string, u: any) =>
        [label, num(u, "inputTokens"), num(u, "outputTokens"), num(u, "cacheTokens"), num(u, "costMicros")].join(",");
      return [
        "scope,inputTokens,outputTokens,cacheTokens,costMicros",
        row("workspace", w),
        row("thread", t),
      ].join("\n");
    },
    copySummary() {
      const text = this.summaryText();
      const nav: any = typeof navigator === "undefined" ? null : navigator;
      if (nav && nav.clipboard && typeof nav.clipboard.writeText === "function") {
        nav.clipboard.writeText(text)
          .then(() => { this.showToast("已复制用量汇总。"); })
          .catch(() => { this.showToast("复制失败，请手动导出 CSV。", "error"); });
        return;
      }
      this.showToast("当前环境不支持一键复制，请导出 CSV。", "error");
    },
    exportCSV() {
      if (typeof document === "undefined" || typeof URL === "undefined") {
        this.showToast("当前环境不支持导出。", "error");
        return;
      }
      const text = this.summaryText();
      const url = (URL as any).createObjectURL(new Blob([text], { type: "text/csv;charset=utf-8" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = "token-usage.csv";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => { (URL as any).revokeObjectURL(url); }, 0);
      this.showToast("已导出 token-usage.csv。");
    },
  },
  render(h) {
    const self = this;
    const t = this.totals;
    const tip = (text: string) => (
      <span class="tu-tip" data-tip={text} aria-label={text} tabindex="0" role="img">!</span>
    );
    const kpi = (label: string, value: string, note: string) => (
      <div class="tu-kpi">
        <span class="tu-kpi__label">{label}</span>
        <span class="tu-kpi__value">{value}</span>
        <span class="tu-kpi__note">{note}</span>
      </div>
    );
    const meter = (label: string, value: number, whole: number, tone: string, extra: string) => (
      <div class="tu-row">
        <span class="tu-row__label">{label}</span>
        <span class="tu-meter"><span class={meterClass(value, whole) + " tu-meter__tone--" + tone}></span></span>
        <span class="tu-row__value">{formatNumber(value)}</span>
        <span class="tu-row__pct">{extra}</span>
      </div>
    );

    return (
      <div class="tu-page" data-token-page>
        <div class="tu-head">
          <div class="tu-title-row">
            <h1 class="tu-title">Token 用量</h1>
            {tip("工作区聚合与当前会话累计，数据来自插件用 events.read 读取的每轮原始计数；聚合口径在插件内，基座不再提供展示投影。成本按 costMicros 折算，为 0 时显示 —。")}
          </div>
          <div class="tu-actions">
            <button class="tu-btn" type="button" onClick={this.copySummary}>复制汇总</button>
            <button class="tu-btn tu-btn--primary" type="button" onClick={this.exportCSV}>导出 CSV</button>
          </div>
        </div>

        <div class="tu-meta">
          <span class="tu-meta__label">数据由插件聚合</span>
          <div class="tu-seg" role="group" aria-label="切换统计范围">
            <button
              type="button"
              class={"tu-seg__item" + (this.view === "workspace" ? " is-active" : "")}
              aria-pressed={this.view === "workspace" ? "true" : "false"}
              onClick={() => this.setView("workspace")}
            >工作区</button>
            <button
              type="button"
              class={"tu-seg__item" + (this.view === "thread" ? " is-active" : "")}
              aria-pressed={this.view === "thread" ? "true" : "false"}
              onClick={() => this.setView("thread")}
            >当前会话</button>
          </div>
          <span class="tu-meta__right">随页面读取 · 累计聚合</span>
        </div>

        <div class="tu-kpis">
          {kpi("累计 Token", formatNumber(t.total), "输入 + 输出")}
          {kpi("输入", formatNumber(t.input), "占累计 " + this.inputShare)}
          {kpi("输出", formatNumber(t.output), "占累计 " + this.outputShare)}
          {kpi("缓存命中率", cacheRate(this.usage), "命中 " + formatNumber(t.cache) + " tokens")}
        </div>

        <div class="tu-lower">
          <section class="tu-panel" aria-label="用量构成">
            <div class="tu-panel__head">
              <span class="tu-panel__title">用量构成</span>
              <span class="tu-panel__hint">{this.view === "thread" ? "当前会话" : "工作区"} · 累计 {formatNumber(t.total)} tokens</span>
            </div>
            {this.hasData ? (
              <div class="tu-panel__body">
                {meter("输入", t.input, t.total, "ink", share(t.input, t.total))}
                {meter("输出", t.output, t.total, "mid", share(t.output, t.total))}
                {meter("缓存命中", t.cache, t.input, "soft", share(t.cache, t.input))}
                <p class="tu-panel__note">累计 = 输入 + 输出；缓存是输入里命中缓存的部分，不重复计入。</p>
              </div>
            ) : (
              <p class="tu-panel__empty">这个范围还没有记录到用量。</p>
            )}
          </section>

          <section class="tu-panel" aria-label="模型与成本">
            <div class="tu-panel__head">
              <span class="tu-panel__title">模型与成本</span>
              <span class="tu-panel__hint">当前会话 {formatNumber(totalOf(this.thread))}</span>
            </div>
            <div class="tu-panel__body">
              <div class="tu-detail">
                <span class="tu-detail__label">本次会话累计</span>
                <span class="tu-detail__value">{formatNumber(totalOf(this.thread))}</span>
              </div>
              <div class="tu-detail">
                <span class="tu-detail__label">成本</span>
                <span class="tu-detail__value">{formatCost(num(this.usage, "costMicros"))}</span>
              </div>
              <div class="tu-detail">
                <span class="tu-detail__label">模型</span>
                <span class="tu-detail__value tu-detail__value--mono">
                  {this.modelText || "多模型合并"}
                </span>
              </div>
              <p class="tu-panel__note">
                {this.modelText
                  ? "单一 provider/model 分组时显示来源；多分组会被后端折叠成一个聚合值。"
                  : "多组 provider/model 由插件聚合；这里不做按模型拆分。"}
              </p>
            </div>
          </section>
        </div>

        <div class="tu-foot">
          <span class="tu-foot__left">
            {tip("成本按 Core 记录的 costMicros 折算；为 0 时显示 —。缓存命中率 = cacheTokens / inputTokens。")}
            <span>成本按 Core 记录折算；缓存命中率 = 缓存 / 输入</span>
          </span>
          <span class="tu-foot__right">events.read · 插件聚合</span>
        </div>

        {this.toast ? (
          <div class="tu-toast" key={this.toast.id} data-tone={this.toast.tone} role="status" aria-live="polite">
            {this.toast.message}
          </div>
        ) : null}
      </div>
    );
  },
});
