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
var PROTOCOL_OPTIONS = [
  { value: "openai_chat", label: "OpenAI Chat" },
  { value: "openai_responses", label: "OpenAI Responses" },
  { value: "anthropic", label: "Anthropic" }
];
var INPUT_MODES = [
  { key: "text", label: "文本" },
  { key: "image", label: "图片" },
  { key: "audio", label: "音频" },
  { key: "video", label: "视频" }
];
function protocolLabel(protocol) {
  for (let i = 0; i < PROTOCOL_OPTIONS.length; i += 1) {
    if (PROTOCOL_OPTIONS[i].value === protocol) return PROTOCOL_OPTIONS[i].label;
  }
  return protocol || "未设置";
}
function formatContext(value) {
  const size = Number(value);
  if (!value || !isFinite(size) || size <= 0) return "—";
  if (size >= 1e6) return Math.round(size / 1e5) / 10 + "M";
  if (size >= 1e3) return Math.round(size / 100) / 10 + "k";
  return String(size);
}
function headerCount(map) {
  if (!map || typeof map !== "object") return 0;
  return Object.keys(map).length;
}
var providers_next_default = defineComponent({
  props: {
    data: { default: {} }
  },
  data() {
    const data = this.data || {};
    const backend = data.backend || {};
    const mine = backend["provider-admin"] || {};
    const state = mine.page_providers || mine.state;
    const providers = state && Array.isArray(state.providers) ? state.providers : [];
    const current = state && state.current ? state.current : { providerId: "", modelId: "" };
    return {
      loading: !state,
      error: "",
      toast: null,
      toastSeq: 0,
      toastTimer: 0,
      providers,
      current,
      selectedId: providers.length ? providers[0].id : "",
      tab: "info",
      search: "",
      filter: "all",
      // 供应商表单弹窗（新增 / 编辑同一个弹窗）
      providerModalOpen: false,
      providerModalMode: "add",
      providerForm: {
        id: "",
        name: "",
        protocol: "",
        baseUrl: "",
        proxy: "",
        headersText: "",
        apiKey: ""
      },
      // 拉取模型弹窗
      fetchOpen: false,
      fetching: false,
      fetchedModels: [],
      selectedFetch: {},
      fetchError: "",
      fetchQuery: "",
      // 模型弹窗
      modelModalOpen: false,
      modelModalMode: "add",
      editingModelId: "",
      levelDraft: "",
      levelInputOpen: false,
      draftLevel: "",
      modelForm: {
        id: "",
        displayName: "",
        contextWindow: "",
        maxOutput: "",
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
    enabledCount() {
      let count = 0;
      this.providers.forEach((p) => {
        if (!p.disabled) count += 1;
      });
      return count;
    },
    filtered() {
      const query = String(this.search || "").trim().toLowerCase();
      const filter = this.filter;
      return this.providers.filter((p) => {
        if (filter === "enabled" && p.disabled) return false;
        if (filter === "disabled" && !p.disabled) return false;
        if (!query) return true;
        const hay = [
          p.displayName || "",
          p.id || "",
          p.protocol || "",
          p.baseUrl || ""
        ].join(" ").toLowerCase();
        return hay.indexOf(query) >= 0;
      });
    },
    selectedModels() {
      const provider = this.selected;
      return provider && Array.isArray(provider.models) ? provider.models : [];
    },
    modelStats() {
      const models = this.selectedModels;
      let enabled = 0;
      let tools = 0;
      let thinking = 0;
      models.forEach((m) => {
        if (!m) return;
        if (!m.disabled) enabled += 1;
        if (m.supportsTools) tools += 1;
        if (Array.isArray(m.thinkingLevels) && m.thinkingLevels.length) thinking += 1;
      });
      return { total: models.length, enabled, tools, thinking };
    },
    currentModelId() {
      const provider = this.selected;
      if (!provider) return "";
      if (this.current.providerId !== provider.id) return "";
      return this.current.modelId || "";
    },
    existingModelIds() {
      const map = {};
      this.selectedModels.forEach((m) => {
        if (m && m.id) map[m.id] = true;
      });
      return map;
    },
    fetchedVisible() {
      const query = String(this.fetchQuery || "").trim().toLowerCase();
      if (!query) return this.fetchedModels;
      return this.fetchedModels.filter((m) => {
        const hay = [m && m.id, m && m.displayName].join(" ").toLowerCase();
        return hay.indexOf(query) >= 0;
      });
    },
    fetchedAddedCount() {
      let count = 0;
      this.fetchedVisible.forEach((m) => {
        if (this.existingModelIds[m.id]) count += 1;
      });
      return count;
    },
    selectedFetchCount() {
      let count = 0;
      Object.keys(this.selectedFetch).forEach((key) => {
        if (this.selectedFetch[key]) count += 1;
      });
      return count;
    },
    allFetchSelected() {
      let selectable = 0;
      let picked = 0;
      this.fetchedVisible.forEach((m) => {
        if (!m || !m.id || this.existingModelIds[m.id]) return;
        selectable += 1;
        if (this.selectedFetch[m.id]) picked += 1;
      });
      return { selectable, picked, all: selectable > 0 && picked === selectable };
    },
    protocolOptions() {
      const list = PROTOCOL_OPTIONS.slice();
      const value = String(this.providerForm.protocol || "");
      if (value) {
        let found = false;
        list.forEach((item) => {
          if (item.value === value) found = true;
        });
        if (!found) list.push({ value, label: value });
      }
      return list;
    }
  },
  mounted() {
    if (this.loading) this.load();
  },
  methods: {
    /// 轻提示：底部居中浮层，自动消失。tone=error 时用危险色、停留更久。
    /// 用自增 seq 做 key，连续两次相同文案也会重新触发进场动画。
    showToast(message, tone) {
      const seq = (this.toastSeq || 0) + 1;
      this.toastSeq = seq;
      this.toast = { id: seq, message, tone: tone || "info" };
      if (this.toastTimer) clearTimeout(this.toastTimer);
      this.toastTimer = setTimeout(() => {
        if (this.toast && this.toast.id === seq) this.toast = null;
      }, tone === "error" ? 4200 : 2400);
    },
    load() {
      this.loading = true;
      this.error = "";
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
    setFilter(filter) {
      this.filter = filter;
    },
    select(id) {
      this.selectedId = id;
      this.tab = "info";
    },
    setTab(tab) {
      this.tab = tab;
    },
    copyText(text2, label) {
      const value = String(text2 || "");
      if (!value) return;
      const nav = typeof navigator === "undefined" ? null : navigator;
      if (nav && nav.clipboard && typeof nav.clipboard.writeText === "function") {
        nav.clipboard.writeText(value).then(() => {
          this.showToast("已复制" + label + "。");
        }).catch(() => {
          this.showToast("复制失败，请手动选择复制。", "error");
        });
        return;
      }
      this.showToast("当前环境不支持一键复制。", "error");
    },
    avatarTone(index) {
      return " is-tone-" + index % 3;
    },
    selectModel(modelId) {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法切换模型。", "error");
        return;
      }
      api("provider-admin", "/select", {
        method: "POST",
        body: { providerId: selected.id, modelId }
      }).then(() => {
        this.current = { providerId: selected.id, modelId };
        this.showToast("已切换当前模型。");
      }).catch((err) => {
        this.showToast("切换失败：" + (err && err.message || String(err)), "error");
      });
    },
    toggleProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法更新供应商。", "error");
        return;
      }
      const nextDisabled = !selected.disabled;
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
        this.showToast(nextDisabled ? "供应商已停用。" : "供应商已启用。");
        this.load();
      }).catch((err) => {
        this.showToast("更新失败：" + (err && err.message || String(err)), "error");
      });
    },
    removeProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法删除供应商。", "error");
        return;
      }
      const remove = () => {
        api("provider-admin", "/remove", {
          method: "POST",
          body: { id: selected.id }
        }).then(() => {
          this.showToast("供应商已删除。");
          this.selectedId = "";
          this.load();
        }).catch((err) => {
          this.showToast("删除失败：" + (err && err.message || String(err)), "error");
        });
      };
      const confirm = window.adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除供应商「" + (selected.displayName || selected.id) + "」？其模型配置会一起删除。", remove);
      } else {
        remove();
      }
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
    // ── 供应商表单弹窗 ────────────────────────────────────────────────
    openAddProvider() {
      this.providerModalMode = "add";
      this.providerForm = {
        id: "",
        name: "",
        protocol: PROTOCOL_OPTIONS[0].value,
        baseUrl: "",
        proxy: "",
        headersText: "",
        apiKey: ""
      };
      this.providerModalOpen = true;
    },
    openEditProvider() {
      const selected = this.selected;
      if (!selected) return;
      const headers = selected.headers || {};
      const lines = [];
      Object.keys(headers).forEach((key) => {
        lines.push(key + ": " + headers[key]);
      });
      this.providerModalMode = "edit";
      this.providerForm = {
        id: selected.id,
        name: selected.displayName || selected.id,
        protocol: selected.protocol || PROTOCOL_OPTIONS[0].value,
        baseUrl: selected.baseUrl || "",
        proxy: selected.proxy || "",
        headersText: lines.join("\n"),
        apiKey: ""
      };
      this.providerModalOpen = true;
    },
    closeProviderModal() {
      this.providerModalOpen = false;
    },
    setProviderProtocol(value) {
      this.providerForm.protocol = value;
    },
    saveProviderForm() {
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法保存供应商。", "error");
        return;
      }
      const editing = this.providerModalMode === "edit";
      const selected = this.selected;
      const name = String(this.providerForm.name || "").trim();
      if (!name) {
        this.showToast("供应商名称不能为空。", "error");
        return;
      }
      const body = {
        id: editing && selected ? selected.id : this.providerForm.id,
        name,
        protocol: String(this.providerForm.protocol || "").trim(),
        baseUrl: String(this.providerForm.baseUrl || "").trim(),
        proxy: String(this.providerForm.proxy || "").trim(),
        headers: this.parseHeaders(this.providerForm.headersText),
        disabled: editing && selected ? !!selected.disabled : false,
        models: editing && selected ? selected.models || [] : []
      };
      const key = String(this.providerForm.apiKey || "").trim();
      if (key) body.apiKey = key;
      api("provider-admin", "/save", { method: "POST", body }).then(() => {
        this.showToast(editing ? "供应商已保存。" : "供应商已新增。");
        this.providerModalOpen = false;
        this.load();
      }).catch((err) => {
        this.showToast((editing ? "保存失败：" : "新增失败：") + (err && err.message || String(err)), "error");
      });
    },
    // ── 拉取模型弹窗 ──────────────────────────────────────────────────
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
      this.fetchQuery = "";
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
      this.fetchQuery = "";
    },
    toggleFetchModel(modelId, checked) {
      this.selectedFetch = Object.assign({}, this.selectedFetch, { [modelId]: checked });
    },
    toggleSelectAll(checked) {
      const next = Object.assign({}, this.selectedFetch);
      this.fetchedVisible.forEach((m) => {
        if (!m || !m.id || this.existingModelIds[m.id]) return;
        next[m.id] = checked;
      });
      this.selectedFetch = next;
    },
    /// 只加选中且尚未添加的模型，保留已有模型配置不动。
    addSelectedModels() {
      const selected = this.selected;
      if (!selected) return;
      const added = this.fetchedModels.filter(
        (m) => m && m.id && !this.existingModelIds[m.id] && this.selectedFetch[m.id]
      );
      if (!added.length) {
        this.showToast("没有勾选可添加的模型。", "error");
        return;
      }
      this.closeFetch();
      this.persistModels((selected.models || []).concat(added), "已添加 " + added.length + " 个模型。");
    },
    // ── 模型弹窗 ──────────────────────────────────────────────────────
    openEditModel(row) {
      if (!row) return;
      this.modelModalMode = "edit";
      this.editingModelId = row.id;
      this.levelInputOpen = false;
      this.levelDraft = "";
      this.modelForm = {
        id: row.id,
        displayName: row.displayName || "",
        contextWindow: row.contextWindow === void 0 || row.contextWindow === null ? "" : String(row.contextWindow),
        maxOutput: row.maxOutput === void 0 || row.maxOutput === null ? "" : String(row.maxOutput),
        supportsTools: !!row.supportsTools,
        disabled: !!row.disabled,
        thinkingLevels: Array.isArray(row.thinkingLevels) ? row.thinkingLevels.slice() : [],
        inputModes: Array.isArray(row.input) ? row.input.slice() : []
      };
      this.modelModalOpen = true;
    },
    openAddModel() {
      this.modelModalMode = "add";
      this.editingModelId = "";
      this.levelInputOpen = false;
      this.levelDraft = "";
      this.modelForm = {
        id: "",
        displayName: "",
        contextWindow: "",
        maxOutput: "",
        supportsTools: false,
        disabled: false,
        thinkingLevels: [],
        inputModes: ["text"]
      };
      this.modelModalOpen = true;
    },
    closeModelModal() {
      this.modelModalOpen = false;
      this.editingModelId = "";
      this.levelInputOpen = false;
      this.levelDraft = "";
    },
    openLevelInput() {
      this.levelInputOpen = true;
      this.levelDraft = "";
    },
    addLevel(preset) {
      const value = String(preset === void 0 ? this.levelDraft : preset).trim();
      if (value && this.modelForm.thinkingLevels.indexOf(value) < 0) {
        this.modelForm.thinkingLevels = this.modelForm.thinkingLevels.concat([value]);
      }
      this.levelDraft = "";
      this.levelInputOpen = false;
    },
    cancelLevelInput() {
      this.levelDraft = "";
      this.levelInputOpen = false;
    },
    removeLevel(index) {
      const next = this.modelForm.thinkingLevels.slice();
      next.splice(index, 1);
      this.modelForm.thinkingLevels = next;
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
    toggleModelTools() {
      this.modelForm.supportsTools = !this.modelForm.supportsTools;
    },
    toggleModelEnabled(checked) {
      this.modelForm.disabled = !checked;
    },
    modelFormPatch() {
      const patch = {
        displayName: String(this.modelForm.displayName || "").trim(),
        supportsTools: !!this.modelForm.supportsTools,
        disabled: !!this.modelForm.disabled
      };
      const context = String(this.modelForm.contextWindow || "").trim();
      if (context) patch.contextWindow = Number(context);
      const output = String(this.modelForm.maxOutput || "").trim();
      if (output) patch.maxOutput = Number(output);
      const levels = this.modelForm.thinkingLevels.slice();
      if (levels.length) patch.thinkingLevels = levels;
      const inputs = this.modelForm.inputModes.slice();
      if (inputs.length) patch.input = inputs;
      return patch;
    },
    persistModels(models, message) {
      const selected = this.selected;
      if (!selected) return;
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法保存模型。", "error");
        return;
      }
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
        this.showToast(message);
        this.editingModelId = "";
        this.modelModalOpen = false;
        this.load();
      }).catch((err) => {
        this.showToast("保存模型失败：" + (err && err.message || String(err)), "error");
      });
    },
    saveModel() {
      const selected = this.selected;
      if (!selected) return;
      if (this.modelModalMode === "add") {
        const id = String(this.modelForm.id || "").trim();
        if (!id) {
          this.showToast("模型 ID 不能为空。", "error");
          return;
        }
        if ((selected.models || []).some((m) => m && m.id === id)) {
          this.showToast("模型 ID 已存在。", "error");
          return;
        }
        const next = Object.assign({ id }, this.modelFormPatch());
        if (!next.displayName) next.displayName = id;
        this.persistModels((selected.models || []).concat(next), "模型已添加。");
        return;
      }
      const target = this.editingModelId;
      if (!target) return;
      const models = (selected.models || []).map((m) => {
        if (!m || m.id !== target) return m;
        const merged = Object.assign({}, m, this.modelFormPatch());
        if (!this.modelForm.thinkingLevels.length) delete merged.thinkingLevels;
        if (!this.modelForm.inputModes.length) delete merged.input;
        return merged;
      });
      this.persistModels(models, "模型已保存。");
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
    const self = this;
    const selected = this.selected;
    const providers = this.providers || [];
    const stats = this.modelStats;
    const models = this.selectedModels;
    const tip = (text2, placement) => /* @__PURE__ */ h2(
      "span",
      {
        class: "pm-tip" + (placement === "below" ? " pm-tip--below" : ""),
        "data-tip": text2,
        "aria-label": text2,
        tabindex: "0",
        role: "img"
      },
      "!"
    );
    const copyBtn = (text2, label) => /* @__PURE__ */ h2(
      "button",
      {
        class: "pm-copy",
        type: "button",
        "aria-label": "复制" + label,
        title: "复制" + label,
        onClick: () => self.copyText(text2, label)
      },
      "复制"
    );
    const defRow = (label, value, extra) => /* @__PURE__ */ h2("div", { class: "pm-def" }, /* @__PURE__ */ h2("span", { class: "pm-def__label" }, label), /* @__PURE__ */ h2("span", { class: "pm-def__value" }, value, extra || null));
    const providerAvatar = (provider, index, size) => {
      const name = String(provider && (provider.displayName || provider.id) || "?").trim();
      return /* @__PURE__ */ h2("span", { class: "pm-avatar" + self.avatarTone(index) + " pm-avatar--" + size }, name.charAt(0).toUpperCase() || "?");
    };
    const selectedIndex = (() => {
      for (let i = 0; i < providers.length; i += 1) {
        if (selected && providers[i].id === selected.id) return i;
      }
      return 0;
    })();
    return /* @__PURE__ */ h2("section", { class: "pm-page", "data-provider-admin-next": true }, /* @__PURE__ */ h2("div", { class: "pm-head" }, /* @__PURE__ */ h2("div", { class: "pm-head__text" }, /* @__PURE__ */ h2("h1", { class: "pm-title" }, "模型供应商", tip("管理自定义模型供应商，配置后可在聊天时选择使用。", "below"))), /* @__PURE__ */ h2("div", { class: "pm-head__actions" }, /* @__PURE__ */ h2("span", { class: "pm-count" }, providers.length, " 个供应商"), /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.load }, "刷新"), /* @__PURE__ */ h2(Button, { variant: "primary", size: "sm", onClick: this.openAddProvider }, "添加供应商"))), /* @__PURE__ */ h2("div", { class: "pm-layout" }, /* @__PURE__ */ h2("div", { class: "pm-list", "aria-label": "供应商列表" }, /* @__PURE__ */ h2("div", { class: "pm-list__head" }, /* @__PURE__ */ h2("div", { class: "pm-list__title" }, /* @__PURE__ */ h2("span", null, "供应商"), /* @__PURE__ */ h2("span", { class: "pm-list__num" }, providers.length)), /* @__PURE__ */ h2("label", { class: "pm-search" }, /* @__PURE__ */ h2(
      "input",
      {
        type: "search",
        value: this.search,
        placeholder: "搜索名称或协议",
        "aria-label": "搜索供应商",
        onInput: (event) => {
          self.search = event.target.value;
        }
      }
    )), /* @__PURE__ */ h2("div", { class: "pm-filters", role: "group", "aria-label": "按状态筛选" }, /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-filter" + (this.filter === "all" ? " is-active" : ""),
        "aria-pressed": this.filter === "all" ? "true" : "false",
        onClick: () => this.setFilter("all")
      },
      "全部 ",
      providers.length
    ), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-filter" + (this.filter === "enabled" ? " is-active" : ""),
        "aria-pressed": this.filter === "enabled" ? "true" : "false",
        onClick: () => this.setFilter("enabled")
      },
      "已启用 ",
      this.enabledCount
    ), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-filter" + (this.filter === "disabled" ? " is-active" : ""),
        "aria-pressed": this.filter === "disabled" ? "true" : "false",
        onClick: () => this.setFilter("disabled")
      },
      "已停用 ",
      providers.length - this.enabledCount
    ))), /* @__PURE__ */ h2("div", { class: "pm-list__scroll" }, this.error ? /* @__PURE__ */ h2("p", { class: "pm-error", role: "alert" }, this.error) : this.loading ? /* @__PURE__ */ h2("p", { class: "pm-empty" }, "正在读取供应商…") : this.filtered.length ? this.filtered.map((provider) => {
      const count = Array.isArray(provider.models) ? provider.models.length : 0;
      const active = selected && provider.id === selected.id;
      return /* @__PURE__ */ h2(
        "button",
        {
          key: provider.id,
          class: "pm-item" + (active ? " is-active" : "") + (provider.disabled ? " is-off" : ""),
          type: "button",
          "aria-pressed": active ? "true" : "false",
          onClick: () => this.select(provider.id)
        },
        providerAvatar(provider, selectedIndex, "sm"),
        /* @__PURE__ */ h2("span", { class: "pm-item__body" }, /* @__PURE__ */ h2("span", { class: "pm-item__name" }, provider.displayName || provider.id), /* @__PURE__ */ h2("span", { class: "pm-item__meta" }, protocolLabel(provider.protocol), count ? " · " + count + " 个模型" : "")),
        /* @__PURE__ */ h2("span", { class: "pm-dot" + (provider.disabled ? " is-off" : ""), "aria-hidden": "true" })
      );
    }) : providers.length ? /* @__PURE__ */ h2("p", { class: "pm-empty" }, "没有匹配的供应商。") : /* @__PURE__ */ h2("p", { class: "pm-empty" }, "还没有供应商。")), /* @__PURE__ */ h2("button", { class: "pm-list__add", type: "button", onClick: this.openAddProvider }, /* @__PURE__ */ h2("span", { class: "pm-list__plus", "aria-hidden": "true" }, "+"), "添加供应商")), /* @__PURE__ */ h2("section", { class: "pm-detail", "aria-label": "供应商详情" }, selected ? /* @__PURE__ */ h2("div", { class: "pm-detail__col" }, /* @__PURE__ */ h2("div", { class: "pm-detail__head" }, providerAvatar(selected, selectedIndex, "md"), /* @__PURE__ */ h2("div", { class: "pm-detail__id" }, /* @__PURE__ */ h2("div", { class: "pm-detail__title-row" }, /* @__PURE__ */ h2("h2", { class: "pm-detail__name" }, selected.displayName || selected.id), /* @__PURE__ */ h2(
      Badge,
      {
        tone: selected.disabled ? "neutral" : "success",
        label: selected.disabled ? "已停用" : "已启用"
      }
    ), /* @__PURE__ */ h2(
      Badge,
      {
        tone: selected.hasApiKey ? "neutral" : "warn",
        label: selected.hasApiKey ? "密钥已保存" : "未配置密钥"
      }
    )), /* @__PURE__ */ h2("p", { class: "pm-detail__meta" }, protocolLabel(selected.protocol), stats.total ? " · " + stats.total + " 个模型" : " · 暂无模型")), /* @__PURE__ */ h2("div", { class: "pm-detail__actions" }, /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.openEditProvider }, "编辑配置"), /* @__PURE__ */ h2(
      Button,
      {
        variant: "primary",
        size: "sm",
        disabled: this.fetching,
        onClick: this.openFetchModels
      },
      this.fetching ? "拉取中…" : "拉取模型"
    ))), /* @__PURE__ */ h2("div", { class: "ada-tabs__list pm-tabs", role: "tablist" }, /* @__PURE__ */ h2(
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
      "模型列表",
      stats.total ? /* @__PURE__ */ h2("span", { class: "pm-tab__num" }, stats.total) : null
    )), this.tab === "info" ? /* @__PURE__ */ h2("div", { class: "pm-panel" }, /* @__PURE__ */ h2("p", { class: "pm-section" }, "基本信息"), /* @__PURE__ */ h2("div", { class: "pm-defs" }, defRow("供应商 ID", /* @__PURE__ */ h2("code", null, selected.id), copyBtn(selected.id, "供应商 ID")), defRow("协议", /* @__PURE__ */ h2("code", null, selected.protocol || "—")), defRow("端点", /* @__PURE__ */ h2("code", null, selected.baseUrl || "—"), selected.baseUrl ? copyBtn(selected.baseUrl, "端点") : null), defRow("代理", /* @__PURE__ */ h2("code", null, selected.proxy || "—")), defRow("请求头", headerCount(selected.headers) ? headerCount(selected.headers) + " 条" : "—"), defRow(
      "密钥",
      selected.hasApiKey ? "尾号 " + (selected.apiKeyHint || "••••") : "未配置",
      selected.hasApiKey ? null : /* @__PURE__ */ h2("span", { class: "pm-def__warn" }, "需配置后才能拉取模型")
    )), /* @__PURE__ */ h2("p", { class: "pm-section" }, "概览"), /* @__PURE__ */ h2("div", { class: "pm-stats" }, /* @__PURE__ */ h2("div", { class: "pm-stat" }, /* @__PURE__ */ h2("span", { class: "pm-stat__value" }, stats.total), /* @__PURE__ */ h2("span", { class: "pm-stat__label" }, "全部模型")), /* @__PURE__ */ h2("div", { class: "pm-stat" }, /* @__PURE__ */ h2("span", { class: "pm-stat__value" }, stats.enabled), /* @__PURE__ */ h2("span", { class: "pm-stat__label" }, "可用模型")), /* @__PURE__ */ h2("div", { class: "pm-stat" }, /* @__PURE__ */ h2("span", { class: "pm-stat__value" }, stats.tools), /* @__PURE__ */ h2("span", { class: "pm-stat__label" }, "支持工具"))), /* @__PURE__ */ h2("p", { class: "pm-section" }, "模型速览"), models.length ? /* @__PURE__ */ h2("div", { class: "pm-chips" }, models.slice(0, 4).map((model) => /* @__PURE__ */ h2("span", { class: "pm-chip" + (model.disabled ? " is-off" : ""), key: model.id }, /* @__PURE__ */ h2("span", { class: "pm-chip__name" }, model.id), /* @__PURE__ */ h2("span", { class: "pm-chip__ctx" }, formatContext(model.contextWindow)))), models.length > 4 ? /* @__PURE__ */ h2("button", { class: "pm-chip pm-chip--more", type: "button", onClick: () => this.setTab("models") }, "还有 ", models.length - 4, " 个") : null, models.length <= 4 ? /* @__PURE__ */ h2("button", { class: "pm-chip pm-chip--more", type: "button", onClick: () => this.setTab("models") }, "查看全部") : null) : /* @__PURE__ */ h2("p", { class: "pm-hint" }, "还没有模型，点右上角「拉取模型」从服务商同步，或到「模型列表」手动添加。")) : /* @__PURE__ */ h2("div", { class: "pm-panel" }, /* @__PURE__ */ h2("div", { class: "pm-models__head" }, /* @__PURE__ */ h2("p", { class: "pm-section pm-section--flush" }, "模型列表"), /* @__PURE__ */ h2(Button, { variant: "secondary", size: "sm", onClick: this.openAddModel }, "添加模型")), models.length ? /* @__PURE__ */ h2("div", { class: "pm-models" }, models.map((model) => {
      const isCurrent = this.currentModelId === model.id;
      const levels = Array.isArray(model.thinkingLevels) ? model.thinkingLevels : [];
      const inputs = Array.isArray(model.input) ? model.input : [];
      return /* @__PURE__ */ h2("div", { class: "pm-mrow" + (isCurrent ? " is-current" : "") + (model.disabled ? " is-disabled" : ""), key: model.id }, /* @__PURE__ */ h2("div", { class: "pm-mrow__main" }, /* @__PURE__ */ h2("span", { class: "pm-mrow__id" }, model.id), model.displayName && model.displayName !== model.id ? /* @__PURE__ */ h2("span", { class: "pm-mrow__name" }, model.displayName) : null, /* @__PURE__ */ h2("span", { class: "pm-mrow__tags" }, /* @__PURE__ */ h2("span", { class: "pm-tag" }, formatContext(model.contextWindow)), inputs.map((mode) => /* @__PURE__ */ h2("span", { class: "pm-tag", key: mode }, mode)), model.supportsTools ? /* @__PURE__ */ h2("span", { class: "pm-tag" }, "工具") : null, levels.length ? /* @__PURE__ */ h2("span", { class: "pm-tag pm-tag--brand", title: "推理档位：" + levels.join(" / ") }, "推理 ", levels.join("/")) : null, model.disabled ? /* @__PURE__ */ h2("span", { class: "pm-tag pm-tag--off" }, "已停用") : null)), /* @__PURE__ */ h2("div", { class: "pm-mrow__actions" }, isCurrent ? /* @__PURE__ */ h2(Badge, { tone: "success", label: "当前" }) : /* @__PURE__ */ h2(
        Button,
        {
          size: "sm",
          variant: "secondary",
          disabled: !!model.disabled,
          onClick: () => this.selectModel(model.id)
        },
        "设为当前"
      ), /* @__PURE__ */ h2(Button, { size: "sm", variant: "ghost", onClick: () => this.openEditModel(model) }, "编辑")));
    })) : /* @__PURE__ */ h2(Empty, { title: "还没有模型", description: "点「拉取模型」从服务商同步，或手动添加一个。" })), /* @__PURE__ */ h2("div", { class: "pm-danger" }, /* @__PURE__ */ h2("span", { class: "pm-danger__label" }, "危险操作", tip("停用后将从聊天列表中隐藏，配置与密钥保留；删除会同时移除其下所有模型。")), /* @__PURE__ */ h2("span", { class: "pm-danger__actions" }, /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: "secondary",
        onClick: this.toggleProvider
      },
      selected.disabled ? "启用此供应商" : "停用此供应商"
    ), /* @__PURE__ */ h2(Button, { size: "sm", variant: "danger", onClick: this.removeProvider }, "删除")))) : /* @__PURE__ */ h2("div", { class: "pm-placeholder" }, /* @__PURE__ */ h2("span", { class: "pm-placeholder__title" }, "选择一个供应商"), /* @__PURE__ */ h2("span", { class: "pm-placeholder__hint" }, "从左侧选择，或点击「添加供应商」创建。")))), this.providerModalOpen ? /* @__PURE__ */ h2("div", { class: "pm-modal", role: "dialog", "aria-modal": "true", "aria-labelledby": "pm-provider-title" }, /* @__PURE__ */ h2("div", { class: "pm-modal__backdrop", onClick: this.closeProviderModal }), /* @__PURE__ */ h2("div", { class: "pm-modal__panel" }, /* @__PURE__ */ h2("div", { class: "pm-modal__head" }, /* @__PURE__ */ h2("h2", { id: "pm-provider-title", class: "pm-modal__title" }, this.providerModalMode === "edit" ? "编辑供应商" : "添加供应商", tip("保存后会整条替换供应商配置；API Key 留空表示保持原值。", "below")), /* @__PURE__ */ h2("button", { class: "pm-modal__close", type: "button", "aria-label": "关闭", onClick: this.closeProviderModal }, "×")), /* @__PURE__ */ h2("div", { class: "pm-modal__body" }, /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-provider-name",
        label: "名称",
        modelValue: this.providerForm.name,
        placeholder: "例如 unisound",
        onInput: (value) => {
          self.providerForm.name = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "协议"), /* @__PURE__ */ h2("div", { class: "pm-seg", role: "group", "aria-label": "协议" }, this.protocolOptions.map((option) => /* @__PURE__ */ h2(
      "button",
      {
        key: option.value,
        type: "button",
        class: "pm-seg__item" + (this.providerForm.protocol === option.value ? " is-active" : ""),
        "aria-pressed": this.providerForm.protocol === option.value ? "true" : "false",
        title: option.value,
        onClick: () => this.setProviderProtocol(option.value)
      },
      option.label
    )))), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-provider-base-url",
        label: "端点",
        modelValue: this.providerForm.baseUrl,
        placeholder: "https://api.example.com/v1",
        onInput: (value) => {
          self.providerForm.baseUrl = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "API 密钥", tip("密钥仅保存在本地配置中，不会随会话上传，也不会出现在浏览器请求里。")), /* @__PURE__ */ h2(
      "input",
      {
        id: "pm-provider-key",
        class: "pm-input",
        type: "password",
        value: this.providerForm.apiKey,
        placeholder: this.providerModalMode === "edit" ? "留空保持原值" : "可后补；留空表示未配置",
        onInput: (event) => {
          self.providerForm.apiKey = event.target.value;
        }
      }
    )), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-provider-proxy",
        label: "代理",
        modelValue: this.providerForm.proxy,
        placeholder: "http://127.0.0.1:7890（可选）",
        onInput: (value) => {
          self.providerForm.proxy = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-provider-headers",
        label: "请求头（每行 Key: Value）",
        multiline: true,
        rows: 3,
        modelValue: this.providerForm.headersText,
        placeholder: "X-Api-Version: 2024-01-01",
        onInput: (value) => {
          self.providerForm.headersText = String(value || "");
        }
      }
    )), /* @__PURE__ */ h2("div", { class: "pm-modal__foot" }, /* @__PURE__ */ h2("span", { class: "pm-modal__hint" }, "保存后可在聊天中切换模型"), /* @__PURE__ */ h2("span", { class: "pm-modal__actions" }, /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.closeProviderModal }, "取消"), /* @__PURE__ */ h2(Button, { size: "sm", variant: "primary", onClick: this.saveProviderForm }, this.providerModalMode === "edit" ? "保存" : "创建供应商"))))) : null, this.fetchOpen ? /* @__PURE__ */ h2("div", { class: "pm-modal", role: "dialog", "aria-modal": "true", "aria-labelledby": "pm-fetch-title" }, /* @__PURE__ */ h2("div", { class: "pm-modal__backdrop", onClick: this.closeFetch }), /* @__PURE__ */ h2("div", { class: "pm-modal__panel pm-modal__panel--wide" }, /* @__PURE__ */ h2("div", { class: "pm-modal__head" }, /* @__PURE__ */ h2("h2", { id: "pm-fetch-title", class: "pm-modal__title" }, "拉取模型", tip("从该端点读取可用模型清单；已经添加过的模型不会重复导入。", "below")), /* @__PURE__ */ h2("button", { class: "pm-modal__close", type: "button", "aria-label": "关闭", onClick: this.closeFetch }, "×")), /* @__PURE__ */ h2("div", { class: "pm-fetch__ctx" }, /* @__PURE__ */ h2("span", { class: "pm-fetch__ctx-left" }, providerAvatar(selected, selectedIndex, "sm"), /* @__PURE__ */ h2("span", { class: "pm-fetch__ctx-text" }, /* @__PURE__ */ h2("span", { class: "pm-fetch__ctx-name" }, selected ? selected.displayName || selected.id : ""), /* @__PURE__ */ h2("span", { class: "pm-fetch__ctx-url" }, selected ? selected.baseUrl || "未设置端点" : ""))), /* @__PURE__ */ h2("span", { class: "pm-count" }, this.fetching ? "读取中…" : this.fetchedModels.length + " 个可用")), /* @__PURE__ */ h2("div", { class: "pm-fetch__bar" }, /* @__PURE__ */ h2("label", { class: "pm-search pm-search--grow" }, /* @__PURE__ */ h2(
      "input",
      {
        type: "search",
        value: this.fetchQuery,
        placeholder: "搜索模型 ID",
        "aria-label": "搜索模型",
        onInput: (event) => {
          self.fetchQuery = event.target.value;
        }
      }
    )), /* @__PURE__ */ h2("label", { class: "pm-check" }, /* @__PURE__ */ h2(
      "input",
      {
        type: "checkbox",
        checked: !!this.allFetchSelected.all,
        disabled: this.allFetchSelected.selectable === 0,
        onChange: (event) => this.toggleSelectAll(event.target.checked)
      }
    ), "全选"), /* @__PURE__ */ h2("span", { class: "pm-fetch__picked" }, this.selectedFetchCount, " / ", this.fetchedModels.length)), this.fetching ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state" }, "正在拉取模型…") : this.fetchError ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state pm-fetch__state--error", role: "alert" }, this.fetchError) : this.fetchedModels.length === 0 ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state" }, "没有拉取到模型。请确认端点与密钥是否正确。") : this.fetchedVisible.length === 0 ? /* @__PURE__ */ h2("p", { class: "pm-fetch__state" }, "没有匹配「", this.fetchQuery, "」的模型。") : /* @__PURE__ */ h2("div", { class: "pm-fetch__list" }, this.fetchedVisible.map((model) => {
      const added = !!this.existingModelIds[model.id];
      return /* @__PURE__ */ h2("label", { class: "pm-fetch__row" + (added ? " is-added" : ""), key: model.id }, /* @__PURE__ */ h2(
        "input",
        {
          type: "checkbox",
          checked: !!this.selectedFetch[model.id],
          disabled: added,
          onChange: (event) => this.toggleFetchModel(model.id, event.target.checked)
        }
      ), /* @__PURE__ */ h2("span", { class: "pm-fetch__id" }, model.id), /* @__PURE__ */ h2("span", { class: "pm-fetch__meta" }, model.displayName && model.displayName !== model.id ? model.displayName : ""), /* @__PURE__ */ h2("span", { class: "pm-fetch__tail" }, added ? "已添加" : formatContext(model.contextWindow)));
    })), /* @__PURE__ */ h2("div", { class: "pm-modal__foot" }, /* @__PURE__ */ h2("span", { class: "pm-modal__hint" }, selected ? "将追加到 " + (selected.displayName || selected.id) + " 的模型列表" : ""), /* @__PURE__ */ h2("span", { class: "pm-modal__actions" }, /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.closeFetch }, "取消"), /* @__PURE__ */ h2(
      Button,
      {
        size: "sm",
        variant: "primary",
        disabled: this.selectedFetchCount === 0,
        onClick: this.addSelectedModels
      },
      "导入 ",
      this.selectedFetchCount,
      " 个模型"
    ))))) : null, this.modelModalOpen ? /* @__PURE__ */ h2("div", { class: "pm-modal", role: "dialog", "aria-modal": "true", "aria-labelledby": "pm-model-title" }, /* @__PURE__ */ h2("div", { class: "pm-modal__backdrop", onClick: this.closeModelModal }), /* @__PURE__ */ h2("div", { class: "pm-modal__panel pm-modal__panel--wide" }, /* @__PURE__ */ h2("div", { class: "pm-modal__head" }, /* @__PURE__ */ h2("h2", { id: "pm-model-title", class: "pm-modal__title" }, this.modelModalMode === "add" ? "添加模型" : "修改模型", selected ? /* @__PURE__ */ h2("span", { class: "pm-modal__tag" }, selected.displayName || selected.id) : null), /* @__PURE__ */ h2("button", { class: "pm-modal__close", type: "button", "aria-label": "关闭", onClick: this.closeModelModal }, "×")), /* @__PURE__ */ h2("div", { class: "pm-modal__body" }, this.modelModalMode === "add" ? /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-model-id",
        label: "模型 ID",
        modelValue: this.modelForm.id,
        placeholder: "例如 gpt-4.1",
        onInput: (value) => {
          self.modelForm.id = String(value || "");
        }
      }
    ) : /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "模型 ID", tip("模型 ID 由服务商定义，改名会让已有会话找不到这个模型，因此只读。")), /* @__PURE__ */ h2("div", { class: "pm-input pm-input--readonly" }, /* @__PURE__ */ h2("code", null, this.modelForm.id), /* @__PURE__ */ h2("span", { class: "pm-input__flag" }, "只读"))), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-model-name",
        label: "显示名称",
        modelValue: this.modelForm.displayName,
        placeholder: "留空则显示模型 ID",
        onInput: (value) => {
          self.modelForm.displayName = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2("div", { class: "pm-form-row" }, /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-model-context",
        type: "number",
        label: "上下文长度",
        modelValue: this.modelForm.contextWindow,
        placeholder: "128000",
        onInput: (value) => {
          self.modelForm.contextWindow = String(value || "");
        }
      }
    ), /* @__PURE__ */ h2(
      Input,
      {
        id: "pm-model-max-output",
        type: "number",
        label: "最大输出",
        modelValue: this.modelForm.maxOutput,
        placeholder: "8192",
        onInput: (value) => {
          self.modelForm.maxOutput = String(value || "");
        }
      }
    )), /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "能力", tip("决定模型在聊天里能做什么：输入模态影响能否收图片/音频，工具调用影响能否使用插件与命令。")), /* @__PURE__ */ h2("div", { class: "pm-chips pm-chips--input" }, INPUT_MODES.map((item) => /* @__PURE__ */ h2(
      "label",
      {
        key: item.key,
        class: "pm-chip-btn" + (this.modelForm.inputModes.indexOf(item.key) >= 0 ? " is-active" : "")
      },
      /* @__PURE__ */ h2(
        "input",
        {
          type: "checkbox",
          checked: this.modelForm.inputModes.indexOf(item.key) >= 0,
          onChange: (event) => this.toggleInputMode(item.key, event.target.checked)
        }
      ),
      item.label
    )), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-chip-btn" + (this.modelForm.supportsTools ? " is-active" : ""),
        "aria-pressed": this.modelForm.supportsTools ? "true" : "false",
        onClick: this.toggleModelTools
      },
      "工具调用"
    ))), /* @__PURE__ */ h2("div", { class: "pm-field" }, /* @__PURE__ */ h2("span", { class: "pm-field__label" }, "推理级别", tip("每个模型支持的档位不同，所以这里是可增删的标签；深色为默认档，聊天时优先使用。")), /* @__PURE__ */ h2("div", { class: "pm-levels" }, this.modelForm.thinkingLevels.map((level, index) => /* @__PURE__ */ h2("span", { class: "pm-level" + (index === 0 ? " is-default" : ""), key: level }, index === 0 ? /* @__PURE__ */ h2("span", { class: "pm-level__dot", "aria-hidden": "true" }) : null, /* @__PURE__ */ h2("span", { class: "pm-level__name" }, level), index === 0 ? null : /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-level__remove",
        "aria-label": "移除推理级别 " + level,
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
          self.levelDraft = event.target.value;
        },
        onKeydown: this.onLevelKeydown
      }
    ) : /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "pm-level__add",
        onClick: this.openLevelInput
      },
      "+ 添加级别"
    ))), /* @__PURE__ */ h2("div", { class: "pm-switch-row" }, /* @__PURE__ */ h2("span", { class: "pm-switch-row__text" }, /* @__PURE__ */ h2("span", { class: "pm-switch-row__title" }, "在聊天中启用"), /* @__PURE__ */ h2("span", { class: "pm-switch-row__hint" }, "停用后不在模型选择器中出现")), /* @__PURE__ */ h2("label", { class: "pm-switch" }, /* @__PURE__ */ h2(
      "input",
      {
        type: "checkbox",
        checked: !this.modelForm.disabled,
        "aria-label": "在聊天中启用",
        onChange: (event) => this.toggleModelEnabled(event.target.checked)
      }
    ), /* @__PURE__ */ h2("span", { class: "pm-switch__track" })))), /* @__PURE__ */ h2("div", { class: "pm-modal__foot" }, /* @__PURE__ */ h2("span", { class: "pm-modal__hint" }, this.modelModalMode === "edit" ? "保存后立即生效，下次对话可选用" : "添加后可在聊天中直接选择"), /* @__PURE__ */ h2("span", { class: "pm-modal__actions" }, this.modelModalMode === "edit" ? /* @__PURE__ */ h2(Button, { size: "sm", variant: "danger", onClick: this.removeModel }, "删除") : null, /* @__PURE__ */ h2(Button, { size: "sm", variant: "secondary", onClick: this.closeModelModal }, "取消"), /* @__PURE__ */ h2(Button, { size: "sm", variant: "primary", onClick: this.saveModel }, "保存"))))) : null, this.toast ? /* @__PURE__ */ h2(
      "div",
      {
        class: "pm-toast",
        key: this.toast.id,
        "data-tone": this.toast.tone,
        role: "status",
        "aria-live": "polite"
      },
      this.toast.message
    ) : null);
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
