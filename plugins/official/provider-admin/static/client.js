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
var page_default = defineComponent({
  render(h2) {
    return /* @__PURE__ */ h2("div", { class: "pm-page", "data-provider-admin": true }, /* @__PURE__ */ h2("div", { class: "pm-head" }, /* @__PURE__ */ h2("div", { class: "pm-head__text" }, /* @__PURE__ */ h2("h1", { class: "pm-title" }, "模型设置"), /* @__PURE__ */ h2("p", { class: "pm-sub" }, "管理自定义模型供应商，配置后可在聊天时选择使用。")), /* @__PURE__ */ h2("div", { class: "pm-head__actions" }, /* @__PURE__ */ h2("button", { class: "ada-btn ada-btn--secondary ada-btn--sm", type: "button", "data-pm-refresh": true, title: "重新读取供应商列表" }, "刷新"), /* @__PURE__ */ h2("button", { class: "ada-btn ada-btn--primary ada-btn--sm", type: "button", "data-pm-add": true }, "添加供应商"))), /* @__PURE__ */ h2("p", { class: "pm-status", "data-pm-status": true, role: "status", "aria-live": "polite" }), /* @__PURE__ */ h2("div", { class: "pm-layout" }, /* @__PURE__ */ h2("div", { class: "pm-list", "data-pm-list": true, "aria-label": "供应商列表" }, /* @__PURE__ */ h2("p", { class: "pm-empty" }, "正在读取供应商…")), /* @__PURE__ */ h2("section", { class: "pm-detail", "data-pm-detail": true, "aria-label": "供应商详情" }, /* @__PURE__ */ h2("div", { class: "pm-placeholder" }, /* @__PURE__ */ h2("span", { class: "pm-placeholder__title" }, "选择一个供应商"), /* @__PURE__ */ h2("span", { class: "pm-placeholder__hint" }, "从左侧选择，或添加一个新的供应商。")))));
  }
});
export {
  page_default as default
};
