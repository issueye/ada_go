// generated from client.tsx (sha256:f69cc159d0f4a217) by tools/pluginbuild — do not edit
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
  props: {
    data: { default: {} }
  },
  data() {
    const data = this.data || {};
    const backend = data.backend || {};
    const mine = backend["provider-admin"] || {};
    const state = mine.state;
    const providers = state && Array.isArray(state.providers) ? state.providers : [];
    const current = state && state.current ? state.current : { providerId: "", modelId: "" };
    return {
      loading: !state,
      error: "",
      providers,
      current,
      selectedId: providers.length ? providers[0].id : "",
      tab: "info",
      status: "",
      fetchOpen: false,
      fetching: false,
      fetchedModels: [],
      selectedFetch: {},
      fetchError: "",
      editing: false,
      form: { name: "", protocol: "", baseUrl: "", proxy: "", headersText: "", apiKey: "" },
      addingProvider: false,
      newProvider: { name: "", protocol: "", baseUrl: "", proxy: "", headersText: "", apiKey: "" },
      editingModelId: "",
      addingModel: false,
      newModel: { id: "", displayName: "", contextWindow: "" },
      levelDraft: "",
      levelInputOpen: false,
      modelForm: {
        displayName: "",
        contextWindow: "",
        supportsTools: false,
        disabled: false,
        thinkingLevels: [],
        inputModes: []
      }
    };
  },
  computed: {
    selected() {
      for (let i = 0; i < this.providers.length; i += 1) {
        if (this.providers[i].id === this.selectedId) return this.providers[i];
      }
      return this.providers[0] || null;
    },
    selectedFetchCount() {
      let count = 0;
      Object.keys(this.selectedFetch).forEach((key) => {
        if (this.selectedFetch[key]) count += 1;
      });
      return count;
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
        },
        {
          label: "操作",
          render: (row, index, h2) => h2(Button, {
            size: "sm",
            variant: "ghost",
            onClick: () => self.startEditModel(row)
          }, "编辑")
        }
      ];
    }
  },
  mounted() {
    if (this.loading) this.load();
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
      this.editing = false;
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
    openFetchModels() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.fetchError = "当前页面没有 adaAPI，无法拉取模型。";
        return;
      }
      this.fetchOpen = true;
      this.fetching = true;
      this.fetchError = "";
      this.fetchedModels = [];
      this.selectedFetch = {};
      api("provider-admin", "/fetch-models", {
        method: "POST",
        body: { id: selected.id }
      }).then((models) => {
        this.fetching = false;
        this.fetchedModels = Array.isArray(models) ? models : [];
      }).catch((err) => {
        this.fetching = false;
        this.fetchError = "拉取失败：" + (err && err.message || String(err));
      });
    },
    closeFetch() {
      this.fetchOpen = false;
      this.fetchedModels = [];
      this.selectedFetch = {};
      this.fetchError = "";
    },
    toggleFetchModel(modelId, checked) {
      this.selectedFetch = Object.assign({}, this.selectedFetch, { [modelId]: checked });
    },
    addSelectedModels() {
      const selected = this.selected;
      if (!selected) return;
      const existing = {};
      (selected.models || []).forEach((m) => {
        existing[m.id] = true;
      });
      const added = this.fetchedModels.filter(
        (m) => m && m.id && !existing[m.id] && this.selectedFetch[m.id]
      );
      if (!added.length) {
        this.status = "没有勾选可添加的模型。";
        return;
      }
      this.closeFetch();
      this.persistModels((selected.models || []).concat(added), "已添加 " + added.length + " 个模型。");
    },
    startEdit() {
      const selected = this.selected;
      if (!selected) return;
      this.addingProvider = false;
      const headers = selected.headers || {};
      const lines = [];
      Object.keys(headers).forEach((key) => {
        lines.push(key + ": " + headers[key]);
      });
      this.form = {
        name: selected.displayName || selected.id,
        protocol: selected.protocol || "",
        baseUrl: selected.baseUrl || "",
        proxy: selected.proxy || "",
        headersText: lines.join("\n"),
        apiKey: ""
      };
      this.editing = true;
    },
    cancelEdit() {
      this.editing = false;
    },
    parseHeaders(text2) {
      const out = {};
      String(text2 || "").split(/\r?\n/).forEach((line) => {
        const index = line.indexOf(":");
        if (index <= 0) return;
        const key = line.slice(0, index).trim();
        const value = line.slice(index + 1).trim();
        if (key) out[key] = value;
      });
      return out;
    },
    saveProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法保存供应商。";
        return;
      }
      const body = {
        id: selected.id,
        name: String(this.form.name || "").trim() || selected.displayName || selected.id,
        protocol: String(this.form.protocol || "").trim() || selected.protocol || "",
        baseUrl: String(this.form.baseUrl || "").trim(),
        proxy: String(this.form.proxy || "").trim(),
        headers: this.parseHeaders(this.form.headersText),
        disabled: !!selected.disabled,
        models: selected.models || []
      };
      const key = String(this.form.apiKey || "").trim();
      if (key) body.apiKey = key;
      this.status = "正在保存供应商…";
      api("provider-admin", "/save", { method: "POST", body }).then(() => {
        this.status = "供应商已保存。";
        this.editing = false;
        this.load();
      }).catch((err) => {
        this.status = "保存失败：" + (err && err.message || String(err));
      });
    },
    startAddProvider() {
      this.addingProvider = true;
      this.editing = false;
      this.newProvider = { name: "", protocol: "", baseUrl: "", proxy: "", headersText: "", apiKey: "" };
    },
    cancelAddProvider() {
      this.addingProvider = false;
    },
    createProvider() {
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法新增供应商。";
        return;
      }
      const body = {
        name: String(this.newProvider.name || "").trim(),
        protocol: String(this.newProvider.protocol || "").trim(),
        baseUrl: String(this.newProvider.baseUrl || "").trim(),
        proxy: String(this.newProvider.proxy || "").trim(),
        headers: this.parseHeaders(this.newProvider.headersText),
        disabled: false,
        models: []
      };
      if (!body.name) {
        this.status = "供应商名称不能为空。";
        return;
      }
      const key = String(this.newProvider.apiKey || "").trim();
      if (key) body.apiKey = key;
      this.status = "正在新增供应商…";
      api("provider-admin", "/save", { method: "POST", body }).then(() => {
        this.status = "供应商已新增。";
        this.addingProvider = false;
        this.load();
      }).catch((err) => {
        this.status = "新增失败：" + (err && err.message || String(err));
      });
    },
    startEditModel(row) {
      if (!row) return;
      this.editingModelId = row.id;
      this.levelInputOpen = false;
      this.levelDraft = "";
      this.modelForm = {
        displayName: row.displayName || "",
        contextWindow: row.contextWindow === void 0 || row.contextWindow === null ? "" : String(row.contextWindow),
        supportsTools: !!row.supportsTools,
        disabled: !!row.disabled,
        thinkingLevels: Array.isArray(row.thinkingLevels) ? row.thinkingLevels.slice() : [],
        inputModes: Array.isArray(row.input) ? row.input.slice() : []
      };
    },
    cancelModelEdit() {
      this.editingModelId = "";
      this.levelInputOpen = false;
      this.levelDraft = "";
    },
    openLevelInput() {
      this.levelInputOpen = true;
      this.levelDraft = "";
    },
    addLevel() {
      const value = String(this.levelDraft || "").trim();
      if (value && this.modelForm.thinkingLevels.indexOf(value) < 0) {
        this.modelForm.thinkingLevels.push(value);
      }
      this.levelDraft = "";
      this.levelInputOpen = false;
    },
    cancelLevelInput() {
      this.levelDraft = "";
      this.levelInputOpen = false;
    },
    removeLevel(index) {
      this.modelForm.thinkingLevels.splice(index, 1);
    },
    onLevelKeydown(event) {
      if (!event) return;
      if (event.key === "Enter") {
        event.preventDefault();
        this.addLevel();
      } else if (event.key === "Escape") {
        event.preventDefault();
        this.cancelLevelInput();
      }
    },
    toggleInputMode(mode, checked) {
      const list = this.modelForm.inputModes.slice();
      const index = list.indexOf(mode);
      if (checked && index < 0) list.push(mode);
      if (!checked && index >= 0) list.splice(index, 1);
      this.modelForm.inputModes = list;
    },
    persistModels(models, message) {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法保存模型。";
        return;
      }
      this.status = "正在保存模型…";
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
          models
        }
      }).then(() => {
        this.status = message;
        this.editingModelId = "";
        this.addingModel = false;
        this.load();
      }).catch((err) => {
        this.status = "保存模型失败：" + (err && err.message || String(err));
      });
    },
    saveModel() {
      const selected = this.selected;
      if (!selected || !this.editingModelId) return;
      const target = this.editingModelId;
      const models = (selected.models || []).map((m) => {
        if (!m || m.id !== target) return m;
        const next = Object.assign({}, m, {
          displayName: String(this.modelForm.displayName || "").trim(),
          supportsTools: !!this.modelForm.supportsTools,
          disabled: !!this.modelForm.disabled
        });
        const context = String(this.modelForm.contextWindow || "").trim();
        if (context) next.contextWindow = Number(context);
        else delete next.contextWindow;
        const levels = this.modelForm.thinkingLevels.slice();
        if (levels.length) next.thinkingLevels = levels;
        else delete next.thinkingLevels;
        const inputs = this.modelForm.inputModes.slice();
        if (inputs.length) next.input = inputs;
        else delete next.input;
        return next;
      });
      this.persistModels(models, "模型已保存。");
    },
    startAddModel() {
      this.addingModel = true;
      this.editingModelId = "";
      this.newModel = { id: "", displayName: "", contextWindow: "" };
    },
    cancelAddModel() {
      this.addingModel = false;
    },
    addModel() {
      const selected = this.selected;
      if (!selected) return;
      const id = String(this.newModel.id || "").trim();
      if (!id) {
        this.status = "模型 ID 不能为空。";
        return;
      }
      if ((selected.models || []).some((m) => m && m.id === id)) {
        this.status = "模型 ID 已存在。";
        return;
      }
      const next = {
        id,
        displayName: String(this.newModel.displayName || "").trim() || id,
        supportsTools: false
      };
      const context = String(this.newModel.contextWindow || "").trim();
      if (context) next.contextWindow = Number(context);
      this.persistModels((selected.models || []).concat(next), "模型已添加。");
    },
    removeModel() {
      const selected = this.selected;
      const target = this.editingModelId;
      if (!selected || !target) return;
      const run = () => {
        this.persistModels((selected.models || []).filter((m) => m && m.id !== target), "模型已删除。");
      };
      const confirm = window.adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除模型「" + target + "」？", run);
      } else {
        run();
      }
    }
  },
  render(h2) {
    const selected = this.selected;
    const providers = this.providers || [];
    return /* @__PURE__ */ h2("section", { class: "pm-page", "data-provider-admin-next": true }, /* @__PURE__ */ h2("div", { class: "pm-head" }, /* @__PURE__ */ h2("div", { class: "pm-head__text" }, /* @__PURE__ */ h2("h1", { class: "pm-title" }, "模型设置"), /* @__PURE__ */ h2("p", { class: "pm-sub" }, "管理自定义模型供应商，配置后可在聊天时选择使用。")), /* @__PURE__ */ h2("div", { class: "pm-head__actions" }, /* @__PURE__ */ h2(Badge, { tone: "brand", label: providers.length ? providers.length + " 个供应商" : "无供应商" }), /* @__PURE__ */ h2(Button, { variant: "primary", size: "sm", onClick: this.startAddProvider }, "添加供应商"), /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.load }, "刷新"))), this.error ? /* @__PURE__ */ h2("p", { class: "pm-status", "data-tone": "danger", role: "alert" }, this.error) : null, this.status ? /* @__PURE__ */ h2("p", { class: "pm-status", role: "status" }, this.status) : null, /* @__PURE__ */ h2("div", { class: "pm-layout" }, /* @__PURE__ */ h2("div", { class: "pm-list", "aria-label": "供应商列表" }, this.loading ? /* @__PURE__ */ h2("p", { class: "pm-empty" }, "正在读取供应商…") : providers.length ? providers.map((provider) => /* @__PURE__ */ h2(
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
    )) : /* @__PURE__ */ h2(Empty, { title: "还没有供应商", description: "点击右上角「添加供应商」创建。" })), /* @__PURE__ */ h2("section", { class: "pm-detail", "aria-label": "供应商详情" }, this.addingProvider ? /* @__PURE__ */ h2("div", { class: "pm-info" }, /* @__PURE__ */ h2("h2", { class: "pm-detail__title" }, "新增供应商"), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-provider-name",
        label: "名称",
        modelValue: this.newProvider.name,
        placeholder: "例如 我的 OpenAI",
        onInput: (value) => {
          this.newProvider.name = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-provider-protocol",
        label: "API 格式",
        modelValue: this.newProvider.protocol,
        placeholder: "openai-chat / openai-responses / anthropic-messages",
        onInput: (value) => {
          this.newProvider.protocol = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-provider-base-url",
        label: "端点",
        modelValue: this.newProvider.baseUrl,
        placeholder: "https://api.example.com/v1",
        onInput: (value) => {
          this.newProvider.baseUrl = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-provider-proxy",
        label: "代理",
        modelValue: this.newProvider.proxy,
        placeholder: "http://127.0.0.1:7890（可选）",
        onInput: (value) => {
          this.newProvider.proxy = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-provider-headers",
        label: "请求头（每行 Key: Value）",
        multiline: true,
        rows: 4,
        modelValue: this.newProvider.headersText,
        onInput: (value) => {
          this.newProvider.headersText = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-provider-api-key",
        type: "password",
        label: "API Key",
        modelValue: this.newProvider.apiKey,
        placeholder: "可后补；留空表示未配置",
        onInput: (value) => {
          this.newProvider.apiKey = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("p", { class: "pm-info__actions" }, /* @__PURE__ */ h2(Button, { variant: "primary", size: "sm", onClick: this.createProvider }, "创建供应商"), /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.cancelAddProvider }, "取消"))) : selected ? /* @__PURE__ */ h2("div", null, /* @__PURE__ */ h2("div", { class: "pm-detail__head" }, /* @__PURE__ */ h2("h2", { class: "pm-detail__title" }, selected.displayName || selected.id), /* @__PURE__ */ h2("div", { class: "pm-detail__meta" }, /* @__PURE__ */ h2(Badge, { tone: selected.disabled ? "neutral" : "success", label: selected.disabled ? "已停用" : "已启用" }), /* @__PURE__ */ h2(Badge, { tone: selected.hasApiKey ? "success" : "warn", label: selected.hasApiKey ? "密钥已保存" : "未配置密钥" })), /* @__PURE__ */ h2("div", { class: "pm-detail__actions" }, /* @__PURE__ */ h2(
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
        onClick: this.openFetchModels
      },
      this.fetching ? "拉取中…" : "拉取模型"
    ))), /* @__PURE__ */ h2("div", { class: "ada-tabs__list", role: "tablist" }, /* @__PURE__ */ h2(
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
    )), this.tab === "info" ? this.editing ? /* @__PURE__ */ h2("div", { class: "pm-info" }, /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-name",
        label: "名称",
        modelValue: this.form.name,
        onInput: (value) => {
          this.form.name = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-protocol",
        label: "API 格式",
        modelValue: this.form.protocol,
        placeholder: "openai-chat / openai-responses / anthropic-messages",
        onInput: (value) => {
          this.form.protocol = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-base-url",
        label: "端点",
        modelValue: this.form.baseUrl,
        placeholder: "https://api.example.com/v1",
        onInput: (value) => {
          this.form.baseUrl = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-proxy",
        label: "代理",
        modelValue: this.form.proxy,
        placeholder: "http://127.0.0.1:7890（可选）",
        onInput: (value) => {
          this.form.proxy = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-headers",
        label: "请求头（每行 Key: Value）",
        multiline: true,
        rows: 4,
        modelValue: this.form.headersText,
        placeholder: "X-Api-Version: 2024-01-01",
        onInput: (value) => {
          this.form.headersText = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-api-key",
        type: "password",
        label: "API Key",
        modelValue: this.form.apiKey,
        placeholder: "留空保持原值",
        onInput: (value) => {
          this.form.apiKey = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("p", { class: "pm-info__hint" }, "保存会整条替换供应商配置；API Key 留空表示保持原值，模型列表原样保留。"), /* @__PURE__ */ h2("div", { class: "pm-info__actions" }, /* @__PURE__ */ h2(Button, { variant: "primary", size: "sm", onClick: this.saveProvider }, "保存"), /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.cancelEdit }, "取消"))) : /* @__PURE__ */ h2("div", { class: "pm-info" }, /* @__PURE__ */ h2("p", { "aria-label": "ID " + selected.id }, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "ID"), /* @__PURE__ */ h2("code", null, selected.id)), /* @__PURE__ */ h2("p", { "aria-label": "协议 " + (selected.protocol || "未设置") }, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "协议"), selected.protocol || "—"), /* @__PURE__ */ h2("p", { "aria-label": "端点 " + (selected.baseUrl || "未设置") }, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "端点"), /* @__PURE__ */ h2("code", null, selected.baseUrl || "—")), /* @__PURE__ */ h2("p", { "aria-label": "密钥 " + (selected.apiKeyHint ? "尾号 " + selected.apiKeyHint : "未保存") }, /* @__PURE__ */ h2("span", { class: "pm-info__label" }, "密钥"), selected.apiKeyHint ? "尾号 " + selected.apiKeyHint : "未保存"), /* @__PURE__ */ h2("div", { class: "pm-info__actions" }, /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.startEdit }, "编辑"))) : /* @__PURE__ */ h2("div", null, this.addingModel ? /* @__PURE__ */ h2("div", { class: "pm-info" }, /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-model-id",
        label: "模型 ID",
        modelValue: this.newModel.id,
        placeholder: "例如 gpt-4.1",
        onInput: (value) => {
          this.newModel.id = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-model-name",
        label: "显示名",
        modelValue: this.newModel.displayName,
        onInput: (value) => {
          this.newModel.displayName = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-new-model-context",
        type: "number",
        label: "上下文窗口",
        modelValue: this.newModel.contextWindow,
        placeholder: "例如 128000",
        onInput: (value) => {
          this.newModel.contextWindow = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("p", { class: "pm-info__actions" }, /* @__PURE__ */ h2(Button, { size: "sm", variant: "primary", onClick: this.addModel }, "添加"), /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.cancelAddModel }, "取消"))) : /* @__PURE__ */ h2("p", { class: "pm-info__actions" }, /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.startAddModel }, "添加模型")), this.editingModelId ? /* @__PURE__ */ h2("div", { class: "pm-info" }, /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-model-name",
        label: "显示名",
        modelValue: this.modelForm.displayName,
        onInput: (value) => {
          this.modelForm.displayName = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-model-context",
        type: "number",
        label: "上下文窗口",
        modelValue: this.modelForm.contextWindow,
        placeholder: "例如 128000",
        onInput: (value) => {
          this.modelForm.contextWindow = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "推理等级"), /* @__PURE__ */ h2("div", { class: "pm-levels" }, this.modelForm.thinkingLevels.map((level, index) => /* @__PURE__ */ h2("span", { class: "pm-level", key: level }, /* @__PURE__ */ h2("span", { class: "pm-level__name" }, level), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-level__remove",
        "aria-label": "移除推理等级 " + level,
        onClick: () => this.removeLevel(index)
      },
      "×"
    ))), this.levelInputOpen ? /* @__PURE__ */ h2(
      "input",
      {
        id: "pm-level-input",
        class: "pm-level__input",
        value: this.levelDraft,
        placeholder: "low / high",
        onInput: (event) => {
          this.levelDraft = event.target.value;
        },
        onKeydown: this.onLevelKeydown
      }
    ) : /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-level__add",
        "aria-label": "添加推理等级",
        onClick: this.openLevelInput
      },
      "+"
    ))), /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "输入模态"), /* @__PURE__ */ h2("div", { class: "pm-checks" }, ["text", "image", "audio", "video"].map((mode) => /* @__PURE__ */ h2("label", { class: "checkbox-row", key: mode }, /* @__PURE__ */ h2(
      "input",
      {
        id: "pm-mode-" + mode,
        type: "checkbox",
        checked: this.modelForm.inputModes.indexOf(mode) >= 0,
        onChange: (event) => this.toggleInputMode(mode, event.target.checked)
      }
    ), mode)))), /* @__PURE__ */ h2("p", { class: "pm-info__actions" }, /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: this.modelForm.supportsTools ? "primary" : "secondary",
        onClick: () => {
          this.modelForm.supportsTools = !this.modelForm.supportsTools;
        }
      },
      "工具：",
      this.modelForm.supportsTools ? "支持" : "不支持"
    ), /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: this.modelForm.disabled ? "secondary" : "ghost",
        onClick: () => {
          this.modelForm.disabled = !this.modelForm.disabled;
        }
      },
      "状态：",
      this.modelForm.disabled ? "停用" : "启用"
    ), /* @__PURE__ */ h2(Button, { size: "sm", variant: "primary", onClick: this.saveModel }, "保存模型"), /* @__PURE__ */ h2(Button, { size: "sm", variant: "danger", onClick: this.removeModel }, "删除模型"), /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.cancelModelEdit }, "取消"))) : null, /* @__PURE__ */ h2(
      Table,
      {
        caption: (selected.displayName || selected.id) + " 的模型",
        rowKey: "id",
        rows: selected.models || [],
        columns: this.modelColumns
      }
    ))) : /* @__PURE__ */ h2(Empty, { title: "选择一个供应商", description: "从左侧列表选择，或点击右上角添加供应商。" }))), this.fetchOpen ? /* @__PURE__ */ h2("div", { class: "pm-modal", role: "dialog", "aria-modal": "true", "aria-labelledby": "pm-fetch-title" }, /* @__PURE__ */ h2("div", { class: "pm-modal__backdrop", onClick: this.closeFetch }), /* @__PURE__ */ h2("div", { class: "pm-modal__panel" }, /* @__PURE__ */ h2("div", { class: "pm-modal__head" }, /* @__PURE__ */ h2("h2", { id: "pm-fetch-title", class: "pm-modal__title" }, "从「", selected ? selected.displayName || selected.id : "", "」选择模型"), /* @__PURE__ */ h2(Button, { size: "sm", variant: "ghost", onClick: this.closeFetch }, "关闭")), /* @__PURE__ */ h2("p", { class: "pm-fetch__hint" }, "勾选要添加的模型；已添加的模型不可重复选择。"), this.fetching ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state" }, "正在拉取模型…") : this.fetchError ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state pm-fetch__state--error", role: "alert" }, this.fetchError) : this.fetchedModels.length === 0 ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state" }, "没有拉取到模型。") : /* @__PURE__ */ h2("div", { class: "pm-fetch__list" }, this.fetchedModels.map((model) => {
      const added = !!(selected && (selected.models || []).some((m) => m && m.id === model.id));
      return /* @__PURE__ */ h2("label", { class: "pm-fetch__row" + (added ? " is-added" : ""), key: model.id }, /* @__PURE__ */ h2(
        "input",
        {
          type: "checkbox",
          checked: !!this.selectedFetch[model.id],
          disabled: added,
          onChange: (event) => this.toggleFetchModel(model.id, event.target.checked)
        }
      ), /* @__PURE__ */ h2("span", { class: "ada-remote__id" }, model.id), /* @__PURE__ */ h2("span", { class: "ada-remote__meta" }, model.displayName || ""), added ? /* @__PURE__ */ h2("span", { class: "badge badge-neutral" }, "已添加") : null);
    })), /* @__PURE__ */ h2("div", { class: "pm-modal__actions" }, /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.closeFetch }, "取消"), /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: "primary",
        disabled: this.selectedFetchCount === 0,
        onClick: this.addSelectedModels
      },
      "添加所选（",
      this.selectedFetchCount,
      "）"
    )))) : null);
  }
});

// client.tsx
var islands = {
  "page:providers": providers_next_default
};
var client_default = providers_next_default;
export {
  client_default as default,
  islands
};
