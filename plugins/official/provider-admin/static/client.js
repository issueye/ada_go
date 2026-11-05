// generated from client.tsx (sha256:ce690a2ed015eb61) by tools/pluginbuild — do not edit
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

// providers-next.tsx
var providers_next_default = defineComponent({
  data() {
    return {
      loading: true,
      error: "",
      providers: [],
      current: { providerId: "", modelId: "" },
      selectedId: "",
      tab: "info",
      status: "",
      fetching: false,
      remoteModels: [],
      fetchError: ""
    };
  },
  computed: {
    selected() {
      for (let i = 0; i < this.providers.length; i += 1) {
        if (this.providers[i].id === this.selectedId) return this.providers[i];
      }
      return this.providers[0] || null;
    },
    modelColumns() {
      const self = this;
      return [
        { key: "id", label: "模型" },
        { key: "displayName", label: "显示名" },
        { key: "contextWindow", label: "上下文" },
        {
          label: "工具",
          render: (row, index, h2) => h2("span", { class: "badge badge-neutral" }, row && row.supportsTools ? "支持" : "—")
        },
        {
          label: "当前",
          render: (row, index, h2) => {
            const current = self.current.providerId === self.selectedId && self.current.modelId === row.id;
            if (current) return h2("span", { class: "badge badge-success" }, "当前");
            return h2(Button, {
              size: "sm",
              variant: "secondary",
              onClick: () => self.selectModel(row.id)
            }, "设为当前");
          }
        }
      ];
    }
  },
  mounted() {
    this.load();
  },
  methods: {
    load() {
      this.loading = true;
      this.error = "";
      this.status = "";
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.loading = false;
        this.error = "当前页面没有 adaAPI，无法读取供应商。";
        return;
      }
      api("provider-admin", "/state").then((data) => {
        this.providers = data && Array.isArray(data.providers) ? data.providers : [];
        this.current = data && data.current || { providerId: "", modelId: "" };
        const exists = this.providers.some((p) => p.id === this.selectedId);
        if (!exists) this.selectedId = this.providers.length ? this.providers[0].id : "";
        this.loading = false;
      }).catch((err) => {
        this.loading = false;
        this.error = err && err.message || String(err);
      });
    },
    select(id) {
      this.selectedId = id;
      this.tab = "info";
    },
    setTab(tab) {
      this.tab = tab;
    },
    isCurrentModel(row) {
      return this.current.providerId === this.selectedId && this.current.modelId === row.id;
    },
    selectModel(modelId) {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法切换模型。";
        return;
      }
      this.status = "正在切换当前模型…";
      api("provider-admin", "/select", {
        method: "POST",
        body: { providerId: selected.id, modelId }
      }).then(() => {
        this.current = { providerId: selected.id, modelId };
        this.status = "已切换当前模型。";
      }).catch((err) => {
        this.status = "切换失败：" + (err && err.message || String(err));
      });
    },
    toggleProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法更新供应商。";
        return;
      }
      const nextDisabled = !selected.disabled;
      this.status = nextDisabled ? "正在停用供应商…" : "正在启用供应商…";
      api("provider-admin", "/save", {
        method: "POST",
        body: {
          id: selected.id,
          name: selected.displayName || selected.id,
          protocol: selected.protocol || "",
          baseUrl: selected.baseUrl || "",
          proxy: selected.proxy || "",
          headers: selected.headers || {},
          disabled: nextDisabled,
          models: selected.models || []
        }
      }).then(() => {
        this.status = nextDisabled ? "供应商已停用。" : "供应商已启用。";
        this.load();
      }).catch((err) => {
        this.status = "更新失败：" + (err && err.message || String(err));
      });
    },
    removeProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法删除供应商。";
        return;
      }
      const remove = () => {
        this.status = "正在删除供应商…";
        api("provider-admin", "/remove", {
          method: "POST",
          body: { id: selected.id }
        }).then(() => {
          this.status = "供应商已删除。";
          this.selectedId = "";
          this.load();
        }).catch((err) => {
          this.status = "删除失败：" + (err && err.message || String(err));
        });
      };
      const confirm = window.adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除供应商「" + (selected.displayName || selected.id) + "」？其模型配置会一起删除。", remove);
      } else {
        remove();
      }
    },
    fetchModels() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.fetchError = "当前页面没有 adaAPI，无法拉取模型。";
        return;
      }
      this.fetching = true;
      this.fetchError = "";
      this.remoteModels = [];
      api("provider-admin", "/fetch-models", {
        method: "POST",
        body: { id: selected.id }
      }).then((models) => {
        this.fetching = false;
        this.remoteModels = Array.isArray(models) ? models : [];
        this.status = this.remoteModels.length ? "拉取到 " + this.remoteModels.length + " 个模型。" : "没有拉取到模型。";
      }).catch((err) => {
        this.fetching = false;
        this.fetchError = "拉取失败：" + (err && err.message || String(err));
      });
    },
    addFetchedModels() {
      const selected = this.selected;
      if (!selected) return;
      const existing = {};
      (selected.models || []).forEach((m) => {
        existing[m.id] = true;
      });
      const added = this.remoteModels.filter((m) => m && m.id && !existing[m.id]);
      if (!added.length) {
        this.status = "没有需要新增的模型。";
        return;
      }
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法保存模型。";
        return;
      }
      this.status = "正在添加 " + added.length + " 个模型…";
      api("provider-admin", "/save", {
        method: "POST",
        body: {
          id: selected.id,
          name: selected.displayName || selected.id,
          protocol: selected.protocol || "",
          baseUrl: selected.baseUrl || "",
          proxy: selected.proxy || "",
          headers: selected.headers || {},
          disabled: !!selected.disabled,
          models: (selected.models || []).concat(added)
        }
      }).then(() => {
        this.status = "已添加 " + added.length + " 个模型。";
        this.remoteModels = [];
        this.load();
      }).catch((err) => {
        this.status = "添加失败：" + (err && err.message || String(err));
      });
    }
  },
  render(h2) {
    const selected = this.selected;
    const providers = this.providers || [];
    return /* @__PURE__ */ h2("section", { class: "pm-page", "data-provider-admin-next": true }, /* @__PURE__ */ h2("div", { class: "pm-head" }, /* @__PURE__ */ h2("div", { class: "pm-head__text" }, /* @__PURE__ */ h2("h1", { class: "pm-title" }, "模型设置（组件化原型）"), /* @__PURE__ */ h2("p", { class: "pm-sub" }, "只读演示：数据经插件 route /state 读取；写入仍使用原页面。")), /* @__PURE__ */ h2("div", { class: "pm-head__actions" }, /* @__PURE__ */ h2(Badge, { tone: "brand", label: providers.length ? providers.length + " 个供应商" : "无供应商" }), /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.load }, "刷新"))), this.error ? /* @__PURE__ */ h2("p", { class: "pm-status", "data-tone": "danger", role: "alert" }, this.error) : null, this.status ? /* @__PURE__ */ h2("p", { class: "pm-status", role: "status" }, this.status) : null, /* @__PURE__ */ h2("div", { class: "pm-layout" }, /* @__PURE__ */ h2("div", { class: "pm-list", "aria-label": "供应商列表" }, this.loading ? /* @__PURE__ */ h2("p", { class: "pm-empty" }, "正在读取供应商…") : providers.length ? providers.map((provider) => /* @__PURE__ */ h2(
      "button",
      {
        key: provider.id,
        class: "pm-item" + (selected && provider.id === selected.id ? " is-active" : ""),
        type: "button",
        "aria-pressed": selected && provider.id === selected.id ? "true" : "false",
        onClick: () => this.select(provider.id)
      },
      /* @__PURE__ */ h2("span", { class: "pm-item__name" }, provider.displayName || provider.id),
      /* @__PURE__ */ h2("span", { class: "pm-item__meta" }, provider.protocol || "未设置协议")
    )) : /* @__PURE__ */ h2(Empty, { title: "还没有供应商", description: "到原「供应商」页添加一个供应商。" })), /* @__PURE__ */ h2("section", { class: "pm-detail", "aria-label": "供应商详情" }, selected ? /* @__PURE__ */ h2("div", null, /* @__PURE__ */ h2("div", { class: "pm-detail__head" }, /* @__PURE__ */ h2("h2", { class: "pm-detail__title" }, selected.displayName || selected.id), /* @__PURE__ */ h2(Badge, { tone: selected.disabled ? "neutral" : "success", label: selected.disabled ? "已停用" : "已启用" }), /* @__PURE__ */ h2(Badge, { tone: selected.hasApiKey ? "success" : "warn", label: selected.hasApiKey ? "密钥已保存" : "未配置密钥" }), /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: selected.disabled ? "secondary" : "ghost",
        onClick: this.toggleProvider
      },
      selected.disabled ? "启用" : "停用"
    ), /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: "danger",
        onClick: this.removeProvider
      },
      "删除"
    ), /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: "secondary",
        disabled: this.fetching,
        onClick: this.fetchModels
      },
      this.fetching ? "拉取中…" : "拉取模型"
    )), /* @__PURE__ */ h2("div", { class: "ada-tabs__list", role: "tablist" }, /* @__PURE__ */ h2(
      "button",
      {
        class: "ada-tabs__tab",
        type: "button",
        role: "tab",
        "aria-selected": this.tab === "info" ? "true" : "false",
        tabindex: this.tab === "info" ? 0 : -1,
        onClick: () => this.setTab("info")
      },
      "供应商信息"
    ), /* @__PURE__ */ h2(
      "button",
      {
        class: "ada-tabs__tab",
        type: "button",
        role: "tab",
        "aria-selected": this.tab === "models" ? "true" : "false",
        tabindex: this.tab === "models" ? 0 : -1,
        onClick: () => this.setTab("models")
      },
      "模型列表"
    )), this.tab === "info" ? /* @__PURE__ */ h2("div", { class: "pm-info" }, /* @__PURE__ */ h2("p", null, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "ID"), /* @__PURE__ */ h2("code", null, selected.id)), /* @__PURE__ */ h2("p", null, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "协议"), selected.protocol || "—"), /* @__PURE__ */ h2("p", null, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "端点"), /* @__PURE__ */ h2("code", null, selected.baseUrl || "—")), /* @__PURE__ */ h2("p", null, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "密钥"), selected.apiKeyHint ? "尾号 " + selected.apiKeyHint : "未保存")) : /* @__PURE__ */ h2("div", null, this.fetchError ? /* @__PURE__ */ h2("p", { class: "pm-status", "data-tone": "danger", role: "alert" }, this.fetchError) : null, this.remoteModels.length ? /* @__PURE__ */ h2("p", { class: "pm-status" }, "拉取到 ", this.remoteModels.length, " 个模型。", /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.addFetchedModels }, "添加未配置模型")) : null, /* @__PURE__ */ h2(
      Table,
      {
        caption: (selected.displayName || selected.id) + " 的模型",
        rowKey: "id",
        rows: selected.models || [],
        columns: this.modelColumns
      }
    ))) : /* @__PURE__ */ h2(Empty, { title: "选择一个供应商", description: "从左侧列表选择，或到原页面添加。" }))));
  }
});

// client.tsx
var islands = {
  "page:providers": page_default,
  "page:providers-next": providers_next_default
};
var client_default = page_default;
export {
  client_default as default,
  islands
};
