// generated from client.tsx (sha256:01fe6f0c781e5371) by tools/pluginbuild — do not edit
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
  render(h4) {
    return /* @__PURE__ */ h4("section", { class: "ext-hello", "data-island-counter": true }, /* @__PURE__ */ h4("h2", null, "Island counter"), /* @__PURE__ */ h4("p", { class: "muted" }, "这个页面由同一份 TSX 在服务端 SSR，并在浏览器 hydrate；点击按钮只更新本岛。"), /* @__PURE__ */ h4("p", null, "count: ", /* @__PURE__ */ h4("strong", null, this.n)), /* @__PURE__ */ h4("button", { class: "ada-btn ada-btn--primary ada-btn--sm", type: "button", onClick: this.inc }, "+1"));
  }
});

// ada-global:ada:ui
var ISO = globalThis.AdaUIIso;
var UI = ISO && ISO.components ? ISO : globalThis.AdaUI;
if (!UI) throw new Error("[pluginbuild] 请先加载 ada-ui-iso.js 或 ada-ui.js");
var C = UI.components || {};
var Button = C["Button"];
var IconButton = C["IconButton"];
var Input = C["Input"];
var TextField = C["TextField"];
var Textarea = C["Textarea"];
var Select = C["Select"];
var Dropdown = C["Dropdown"];
var DropdownButton = C["DropdownButton"];
var Tabs = C["Tabs"];
var Icon = C["Icon"];
var Markdown = C["Markdown"];
var Card = C["Card"];
var Badge = C["Badge"];
var Slider = C["Slider"];
var Range = C["Range"];
var Progress = C["Progress"];
var ProgressBar = C["ProgressBar"];
var Table = C["Table"];
var DataTable = C["DataTable"];
var Modal = C["Modal"];
var Message = C["Message"];
var Alert = C["Alert"];
var Notice = C["Notice"];
var Empty = C["Empty"];
var Spinner = C["Spinner"];
var Switch = C["Switch"];
var Checkbox = C["Checkbox"];
var Radio = C["Radio"];

// slot.tsx
var slot_default = defineComponent({
  render(h4) {
    return /* @__PURE__ */ h4("div", { class: "ext-hello-slot", title: "P2-3 插槽岛屿" }, /* @__PURE__ */ h4(Badge, { tone: "brand", label: "island" }));
  }
});

// client.tsx
var islands = {
  "page:counter": counter_default,
  "slot:sidebar.top": slot_default
};
var client_default = counter_default;
export {
  client_default as default,
  islands
};
