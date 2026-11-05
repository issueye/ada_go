// generated from page.tsx (sha256:843c88a905f53db5) by tools/pluginbuild — do not edit
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
function totalOf(usage) {
  if (!usage) return 0;
  return Number(usage.inputTokens || 0) + Number(usage.outputTokens || 0);
}
function cacheRate(usage) {
  const input = Number(usage && usage.inputTokens || 0);
  const cache = Number(usage && usage.cacheTokens || 0);
  if (!isFinite(input) || input <= 0) return "—";
  return Math.round(cache * 100 / input) + "%";
}
function UsageCard(h2, title, usage, hint) {
  const total = totalOf(usage);
  return /* @__PURE__ */ h2("section", { class: "card tu-card" }, /* @__PURE__ */ h2("div", { class: "tu-card__head" }, /* @__PURE__ */ h2("span", { class: "tu-card__title" }, title), /* @__PURE__ */ h2("span", { class: "tu-card__total", title: formatNumber(total) + " tokens" }, formatNumber(total))), /* @__PURE__ */ h2("p", { class: "tu-card__hint" }, hint), /* @__PURE__ */ h2("div", { class: "tu-stats" }, /* @__PURE__ */ h2("div", { class: "tu-stat" }, /* @__PURE__ */ h2("span", { class: "tu-stat__label" }, "输入"), /* @__PURE__ */ h2("span", { class: "tu-stat__value" }, formatNumber(usage && usage.inputTokens))), /* @__PURE__ */ h2("div", { class: "tu-stat" }, /* @__PURE__ */ h2("span", { class: "tu-stat__label" }, "输出"), /* @__PURE__ */ h2("span", { class: "tu-stat__value" }, formatNumber(usage && usage.outputTokens))), /* @__PURE__ */ h2("div", { class: "tu-stat" }, /* @__PURE__ */ h2("span", { class: "tu-stat__label" }, "缓存"), /* @__PURE__ */ h2("span", { class: "tu-stat__value" }, formatNumber(usage && usage.cacheTokens))), /* @__PURE__ */ h2("div", { class: "tu-stat" }, /* @__PURE__ */ h2("span", { class: "tu-stat__label" }, "成本"), /* @__PURE__ */ h2("span", { class: "tu-stat__value" }, formatCost(usage && usage.costMicros)))), usage && usage.provider ? /* @__PURE__ */ h2("p", { class: "tu-card__model" }, usage.provider, usage.model ? " / " + usage.model : "") : null);
}
var page_default = defineComponent({
  props: { data: { default: {} } },
  data() {
    return { showCache: false };
  },
  methods: {
    toggleCache() {
      this.showCache = !this.showCache;
    }
  },
  render(h2) {
    const usage = this.data && this.data.usage ? this.data.usage : null;
    return /* @__PURE__ */ h2("div", { class: "tu-page" }, /* @__PURE__ */ h2("div", { class: "tu-head" }, /* @__PURE__ */ h2("div", null, /* @__PURE__ */ h2("h1", { class: "tu-title" }, "Token 用量"), /* @__PURE__ */ h2("p", { class: "tu-sub" }, "工作区聚合与当前会话累计；数据来自 Core 的 usage.get。")), /* @__PURE__ */ h2(
      "button",
      {
        class: "ada-btn ada-btn--secondary ada-btn--sm",
        type: "button",
        "aria-pressed": this.showCache ? "true" : "false",
        onClick: this.toggleCache
      },
      this.showCache ? "隐藏缓存命中率" : "显示缓存命中率"
    )), /* @__PURE__ */ h2("div", { class: "tu-grid" }, UsageCard(h2, "工作区", usage && usage.workspace, "所有会话的累计用量"), UsageCard(h2, "当前会话", usage && usage.thread, "本条线程的累计用量")), this.showCache ? /* @__PURE__ */ h2("p", { class: "tu-card__model" }, "缓存命中率：工作区 ", cacheRate(usage && usage.workspace), "，当前会话 ", cacheRate(usage && usage.thread)) : null);
  }
});
export {
  page_default as default
};
