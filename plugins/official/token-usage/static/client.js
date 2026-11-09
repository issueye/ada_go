// generated from client.tsx (sha256:e3811812a87122bc) by tools/pluginbuild — do not edit
// ada-global:ada:render
var R = globalThis.AdaRender;
if (!R) throw new Error("[pluginbuild] 请先加载 ada-render.js");
var h = R.h;
var host = R.host;
var text = R.text;
var defineComponent = R.defineComponent;
var createApp = R.createApp;
var reactive = R.reactive;
var ref = R.ref;
var computed = R.computed;
var watchEffect = R.watchEffect;
var nextTick = R.nextTick;
var renderToJSON = R.renderToJSON;

// page.tsx
function formatNumber(value) {
  const n = Number(value || 0);
  if (!isFinite(n)) return "0";
  const text2 = String(Math.round(n));
  let out = "";
  for (let i = 0; i < text2.length; i += 1) {
    if (i > 0 && (text2.length - i) % 3 === 0) out += ",";
    out += text2.charAt(i);
  }
  return out;
}
function formatCost(micros) {
  const n = Number(micros || 0);
  if (!isFinite(n) || n <= 0) return "—";
  return "$" + (n / 1e6).toFixed(4);
}
function num(usage, key) {
  const n = Number(usage && usage[key]);
  return isFinite(n) ? n : 0;
}
function totalOf(usage) {
  return num(usage, "inputTokens") + num(usage, "outputTokens");
}
function cacheRate(usage) {
  const input = num(usage, "inputTokens");
  const cache = num(usage, "cacheTokens");
  if (input <= 0) return "—";
  return Math.round(cache * 100 / input) + "%";
}
function percent(part, whole) {
  if (whole <= 0 || part <= 0) return 0;
  return Math.round(part * 100 / whole);
}
function share(part, whole) {
  return percent(part, whole) + "%";
}
function meterClass(part, whole) {
  const p = percent(part, whole);
  const bucket = Math.min(100, Math.max(0, Math.round(p / 10) * 10));
  return "tu-meter__fill tu-meter__fill--p" + bucket;
}
function modelLabel(usage) {
  const provider = String(usage && usage.provider || "");
  const model = String(usage && usage.model || "");
  if (!provider && !model) return "";
  return provider + (model ? " / " + model : "");
}
var page_default = defineComponent({
  props: { data: { default: {} } },
  data() {
    return {
      view: "workspace",
      toast: null,
      toastSeq: 0,
      toastTimer: 0
    };
  },
  computed: {
    usage() {
      const data = this.data || {};
      const mine = (data.backend || {})["token-usage"] || {};
      const root = mine.page_usage || mine.usage || {};
      return this.view === "thread" ? root.thread || null : root.workspace || null;
    },
    thread() {
      const data = this.data || {};
      const mine = (data.backend || {})["token-usage"] || {};
      const root = mine.page_usage || mine.usage || {};
      return root.thread || null;
    },
    workspace() {
      const data = this.data || {};
      const mine = (data.backend || {})["token-usage"] || {};
      const root = mine.page_usage || mine.usage || {};
      return root.workspace || null;
    },
    totals() {
      const u = this.usage;
      const input = num(u, "inputTokens");
      const output = num(u, "outputTokens");
      const cache = num(u, "cacheTokens");
      const total = input + output;
      const whole = input + output + cache;
      return { input, output, cache, total, whole };
    },
    hasData() {
      return this.totals.whole > 0;
    },
    inputShare() {
      return percent(this.totals.input, this.totals.total) + "%";
    },
    outputShare() {
      return percent(this.totals.output, this.totals.total) + "%";
    },
    modelText() {
      return modelLabel(this.workspace);
    }
  },
  methods: {
    showToast(message, tone) {
      const seq = (this.toastSeq || 0) + 1;
      this.toastSeq = seq;
      this.toast = { id: seq, message, tone: tone || "info" };
      if (this.toastTimer) clearTimeout(this.toastTimer);
      this.toastTimer = setTimeout(() => {
        if (this.toast && this.toast.id === seq) this.toast = null;
      }, tone === "error" ? 4200 : 2400);
    },
    setView(view) {
      this.view = view;
    },
    summaryText() {
      const w = this.workspace;
      const t = this.thread;
      const row = (label, u) => [label, num(u, "inputTokens"), num(u, "outputTokens"), num(u, "cacheTokens"), num(u, "costMicros")].join(",");
      return [
        "scope,inputTokens,outputTokens,cacheTokens,costMicros",
        row("workspace", w),
        row("thread", t)
      ].join("\n");
    },
    copySummary() {
      const text2 = this.summaryText();
      const nav = typeof navigator === "undefined" ? null : navigator;
      if (nav && nav.clipboard && typeof nav.clipboard.writeText === "function") {
        nav.clipboard.writeText(text2).then(() => {
          this.showToast("已复制用量汇总。");
        }).catch(() => {
          this.showToast("复制失败，请手动导出 CSV。", "error");
        });
        return;
      }
      this.showToast("当前环境不支持一键复制，请导出 CSV。", "error");
    },
    exportCSV() {
      if (typeof document === "undefined" || typeof URL === "undefined") {
        this.showToast("当前环境不支持导出。", "error");
        return;
      }
      const text2 = this.summaryText();
      const url = URL.createObjectURL(new Blob([text2], { type: "text/csv;charset=utf-8" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = "token-usage.csv";
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      setTimeout(() => {
        URL.revokeObjectURL(url);
      }, 0);
      this.showToast("已导出 token-usage.csv。");
    }
  },
  render(h2) {
    const self = this;
    const t = this.totals;
    const tip = (text2) => /* @__PURE__ */ h2("span", { class: "tu-tip", "data-tip": text2, "aria-label": text2, tabindex: "0", role: "img" }, "!");
    const kpi = (label, value, note) => /* @__PURE__ */ h2("div", { class: "tu-kpi" }, /* @__PURE__ */ h2("span", { class: "tu-kpi__label" }, label), /* @__PURE__ */ h2("span", { class: "tu-kpi__value" }, value), /* @__PURE__ */ h2("span", { class: "tu-kpi__note" }, note));
    const meter = (label, value, whole, tone, extra) => /* @__PURE__ */ h2("div", { class: "tu-row" }, /* @__PURE__ */ h2("span", { class: "tu-row__label" }, label), /* @__PURE__ */ h2("span", { class: "tu-meter" }, /* @__PURE__ */ h2("span", { class: meterClass(value, whole) + " tu-meter__tone--" + tone })), /* @__PURE__ */ h2("span", { class: "tu-row__value" }, formatNumber(value)), /* @__PURE__ */ h2("span", { class: "tu-row__pct" }, extra));
    return /* @__PURE__ */ h2("div", { class: "tu-page", "data-token-page": true }, /* @__PURE__ */ h2("div", { class: "tu-head" }, /* @__PURE__ */ h2("div", { class: "tu-title-row" }, /* @__PURE__ */ h2("h1", { class: "tu-title" }, "Token 用量"), tip("工作区聚合与当前会话累计，数据来自插件用 events.read 读取的每轮原始计数；聚合口径在插件内，基座不再提供展示投影。成本按 costMicros 折算，为 0 时显示 —。")), /* @__PURE__ */ h2("div", { class: "tu-actions" }, /* @__PURE__ */ h2("button", { class: "tu-btn", type: "button", onClick: this.copySummary }, "复制汇总"), /* @__PURE__ */ h2("button", { class: "tu-btn tu-btn--primary", type: "button", onClick: this.exportCSV }, "导出 CSV"))), /* @__PURE__ */ h2("div", { class: "tu-meta" }, /* @__PURE__ */ h2("span", { class: "tu-meta__label" }, "数据由插件聚合"), /* @__PURE__ */ h2("div", { class: "tu-seg", role: "group", "aria-label": "切换统计范围" }, /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "tu-seg__item" + (this.view === "workspace" ? " is-active" : ""),
        "aria-pressed": this.view === "workspace" ? "true" : "false",
        onClick: () => this.setView("workspace")
      },
      "工作区"
    ), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "tu-seg__item" + (this.view === "thread" ? " is-active" : ""),
        "aria-pressed": this.view === "thread" ? "true" : "false",
        onClick: () => this.setView("thread")
      },
      "当前会话"
    )), /* @__PURE__ */ h2("span", { class: "tu-meta__right" }, "随页面读取 · 累计聚合")), /* @__PURE__ */ h2("div", { class: "tu-kpis" }, kpi("累计 Token", formatNumber(t.total), "输入 + 输出"), kpi("输入", formatNumber(t.input), "占累计 " + this.inputShare), kpi("输出", formatNumber(t.output), "占累计 " + this.outputShare), kpi("缓存命中率", cacheRate(this.usage), "命中 " + formatNumber(t.cache) + " tokens")), /* @__PURE__ */ h2("div", { class: "tu-lower" }, /* @__PURE__ */ h2("section", { class: "tu-panel", "aria-label": "用量构成" }, /* @__PURE__ */ h2("div", { class: "tu-panel__head" }, /* @__PURE__ */ h2("span", { class: "tu-panel__title" }, "用量构成"), /* @__PURE__ */ h2("span", { class: "tu-panel__hint" }, this.view === "thread" ? "当前会话" : "工作区", " · 累计 ", formatNumber(t.total), " tokens")), this.hasData ? /* @__PURE__ */ h2("div", { class: "tu-panel__body" }, meter("输入", t.input, t.total, "ink", share(t.input, t.total)), meter("输出", t.output, t.total, "mid", share(t.output, t.total)), meter("缓存命中", t.cache, t.input, "soft", share(t.cache, t.input)), /* @__PURE__ */ h2("p", { class: "tu-panel__note" }, "累计 = 输入 + 输出；缓存是输入里命中缓存的部分，不重复计入。")) : /* @__PURE__ */ h2("p", { class: "tu-panel__empty" }, "这个范围还没有记录到用量。")), /* @__PURE__ */ h2("section", { class: "tu-panel", "aria-label": "模型与成本" }, /* @__PURE__ */ h2("div", { class: "tu-panel__head" }, /* @__PURE__ */ h2("span", { class: "tu-panel__title" }, "模型与成本"), /* @__PURE__ */ h2("span", { class: "tu-panel__hint" }, "当前会话 ", formatNumber(totalOf(this.thread)))), /* @__PURE__ */ h2("div", { class: "tu-panel__body" }, /* @__PURE__ */ h2("div", { class: "tu-detail" }, /* @__PURE__ */ h2("span", { class: "tu-detail__label" }, "本次会话累计"), /* @__PURE__ */ h2("span", { class: "tu-detail__value" }, formatNumber(totalOf(this.thread)))), /* @__PURE__ */ h2("div", { class: "tu-detail" }, /* @__PURE__ */ h2("span", { class: "tu-detail__label" }, "成本"), /* @__PURE__ */ h2("span", { class: "tu-detail__value" }, formatCost(num(this.usage, "costMicros")))), /* @__PURE__ */ h2("div", { class: "tu-detail" }, /* @__PURE__ */ h2("span", { class: "tu-detail__label" }, "模型"), /* @__PURE__ */ h2("span", { class: "tu-detail__value tu-detail__value--mono" }, this.modelText || "多模型合并")), /* @__PURE__ */ h2("p", { class: "tu-panel__note" }, this.modelText ? "单一 provider/model 分组时显示来源；多分组会被后端折叠成一个聚合值。" : "多组 provider/model 由插件聚合；这里不做按模型拆分。")))), /* @__PURE__ */ h2("div", { class: "tu-foot" }, /* @__PURE__ */ h2("span", { class: "tu-foot__left" }, tip("成本按 Core 记录的 costMicros 折算；为 0 时显示 —。缓存命中率 = cacheTokens / inputTokens。"), /* @__PURE__ */ h2("span", null, "成本按 Core 记录折算；缓存命中率 = 缓存 / 输入")), /* @__PURE__ */ h2("span", { class: "tu-foot__right" }, "events.read · 插件聚合")), this.toast ? /* @__PURE__ */ h2("div", { class: "tu-toast", key: this.toast.id, "data-tone": this.toast.tone, role: "status", "aria-live": "polite" }, this.toast.message) : null);
  }
});
export {
  page_default as default
};
