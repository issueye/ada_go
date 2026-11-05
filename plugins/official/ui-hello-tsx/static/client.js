// generated from counter.tsx (sha256:e3ec87751d2c9f26) by tools/pluginbuild — do not edit
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

// counter.tsx
var counter_default = defineComponent({
  props: { start: { default: 0 } },
  data() {
    return { n: this.start };
  },
  methods: {
    inc() {
      this.n += 1;
    }
  },
  render(h3) {
    return /* @__PURE__ */ h3("section", { class: "ext-hello", "data-island-counter": true }, /* @__PURE__ */ h3("h2", null, "Island counter"), /* @__PURE__ */ h3("p", { class: "muted" }, "这个页面由同一份 TSX 在服务端 SSR，并在浏览器 hydrate；点击按钮只更新本岛。"), /* @__PURE__ */ h3("p", null, "count: ", /* @__PURE__ */ h3("strong", null, this.n)), /* @__PURE__ */ h3("button", { class: "ada-btn ada-btn--primary ada-btn--sm", type: "button", onClick: this.inc }, "+1"));
  }
});
export {
  counter_default as default
};
