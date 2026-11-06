/**
 * 供应商管理 · 模型供应商页（组件化实现）。
 *
 * 结构：页面头部（标题 / 说明图标 / 操作）→ 左侧供应商目录（搜索 + 状态筛选 +
 * 富列表）→ 右侧详情（头部信息 + 页签 + 基本信息 / 概览 / 模型速览 + 危险区）。
 *
 * 约定：
 *   - 说明性文案一律走 ⓘ tooltip（class="pm-tip" data-tip=...），正文只留状态与数据；
 *   - 停用 / 删除下沉到详情底部危险区，头部只保留「编辑配置 / 拉取模型」；
 *   - 推理级别是动态标签：档位集合由模型自身决定，可增删，深色为默认档；
 *   - 数据全部来自本插件 route GET /state，API Key 不经过浏览器。
 */
import { defineComponent } from "ada:render";
import { Badge, Button, Empty, Input } from "ada:ui";

// 协议取值必须与各适配器 ProviderDef.protocol 完全一致：
// provider-openai-chat → openai_chat、provider-openai-responses → openai_responses、
// provider-anthropic-messages → anthropic。写错不会报错，只会存下一个没有任何适配器认领的值。
const PROTOCOL_OPTIONS = [
  { value: "openai_chat", label: "OpenAI Chat" },
  { value: "openai_responses", label: "OpenAI Responses" },
  { value: "anthropic", label: "Anthropic" },
];

const INPUT_MODES = [
  { key: "text", label: "文本" },
  { key: "image", label: "图片" },
  { key: "audio", label: "音频" },
  { key: "video", label: "视频" },
];

function protocolLabel(protocol: string) {
  for (let i = 0; i < PROTOCOL_OPTIONS.length; i += 1) {
    if (PROTOCOL_OPTIONS[i].value === protocol) return PROTOCOL_OPTIONS[i].label;
  }
  return protocol || "未设置";
}

function formatContext(value: any) {
  const size = Number(value);
  if (!value || !isFinite(size) || size <= 0) return "—";
  if (size >= 1000000) return Math.round(size / 100000) / 10 + "M";
  if (size >= 1000) return Math.round(size / 100) / 10 + "k";
  return String(size);
}

function headerCount(map: any) {
  if (!map || typeof map !== "object") return 0;
  return Object.keys(map).length;
}

export default defineComponent({
  props: {
    data: { default: {} },
  },
  data() {
    const data = (this as any).data || {};
    const backend = data.backend || {};
    const mine = backend["provider-admin"] || {};
    // SSR 预取按「绑定名」落在 data.backend.<插件>.<绑定名>，这里先读 page_providers，
    // 再兼容旧的 state 键：否则首屏永远是空占位，只能等客户端再拉一次。
    const state = mine.page_providers || mine.state;
    const providers = state && Array.isArray(state.providers) ? state.providers : [];
    const current = state && state.current ? state.current : { providerId: "", modelId: "" };
    return {
      loading: !state,
      error: "",
      toast: null as any,
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
        apiKey: "",
      },
      // 拉取模型弹窗
      fetchOpen: false,
      fetching: false,
      fetchedModels: [] as any[],
      selectedFetch: {} as any,
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
        thinkingLevels: [] as string[],
        inputModes: [] as string[],
      },
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
      this.providers.forEach((p: any) => { if (!p.disabled) count += 1; });
      return count;
    },
    filtered() {
      const query = String(this.search || "").trim().toLowerCase();
      const filter = this.filter;
      return this.providers.filter((p: any) => {
        if (filter === "enabled" && p.disabled) return false;
        if (filter === "disabled" && !p.disabled) return false;
        if (!query) return true;
        const hay = [
          p.displayName || "",
          p.id || "",
          p.protocol || "",
          p.baseUrl || "",
        ].join(" ").toLowerCase();
        return hay.indexOf(query) >= 0;
      });
    },
    selectedModels(): any[] {
      const provider = this.selected;
      return provider && Array.isArray(provider.models) ? provider.models : [];
    },
    modelStats() {
      const models = this.selectedModels;
      let enabled = 0;
      let tools = 0;
      let thinking = 0;
      models.forEach((m: any) => {
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
      const map: any = {};
      this.selectedModels.forEach((m: any) => { if (m && m.id) map[m.id] = true; });
      return map;
    },
    fetchedVisible(): any[] {
      const query = String(this.fetchQuery || "").trim().toLowerCase();
      if (!query) return this.fetchedModels;
      return this.fetchedModels.filter((m: any) => {
        const hay = [m && m.id, m && m.displayName].join(" ").toLowerCase();
        return hay.indexOf(query) >= 0;
      });
    },
    fetchedAddedCount() {
      let count = 0;
      this.fetchedVisible.forEach((m: any) => { if (this.existingModelIds[m.id]) count += 1; });
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
      this.fetchedVisible.forEach((m: any) => {
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
        list.forEach((item) => { if (item.value === value) found = true; });
        if (!found) list.push({ value, label: value });
      }
      return list;
    },
  },
  mounted() {
    if (this.loading) this.load();
  },
  methods: {
    /// 轻提示：底部居中浮层，自动消失。tone=error 时用危险色、停留更久。
    /// 用自增 seq 做 key，连续两次相同文案也会重新触发进场动画。
    showToast(message: string, tone?: string) {
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
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.loading = false;
        this.error = "当前页面没有 adaAPI，无法读取供应商。";
        return;
      }
      api("provider-admin", "/state")
        .then((data: any) => {
          this.providers = data && Array.isArray(data.providers) ? data.providers : [];
          this.current = (data && data.current) || { providerId: "", modelId: "" };
          const exists = this.providers.some((p: any) => p.id === this.selectedId);
          if (!exists) this.selectedId = this.providers.length ? this.providers[0].id : "";
          this.loading = false;
        })
        .catch((err: any) => {
          this.loading = false;
          this.error = (err && err.message) || String(err);
        });
    },
    setFilter(filter: string) {
      this.filter = filter;
    },
    select(id: string) {
      this.selectedId = id;
      this.tab = "info";
    },
    setTab(tab: string) {
      this.tab = tab;
    },
    copyText(text: string, label: string) {
      const value = String(text || "");
      if (!value) return;
      const nav: any = typeof navigator === "undefined" ? null : navigator;
      if (nav && nav.clipboard && typeof nav.clipboard.writeText === "function") {
        nav.clipboard.writeText(value)
          .then(() => { this.showToast("已复制" + label + "。"); })
          .catch(() => { this.showToast("复制失败，请手动选择复制。", "error"); });
        return;
      }
      this.showToast("当前环境不支持一键复制。", "error");
    },
    avatarTone(index: number) {
      return " is-tone-" + (index % 3);
    },
    selectModel(modelId: string) {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法切换模型。", "error");
        return;
      }
      api("provider-admin", "/select", {
        method: "POST",
        body: { providerId: selected.id, modelId },
      })
        .then(() => {
          this.current = { providerId: selected.id, modelId };
          this.showToast("已切换当前模型。");
        })
        .catch((err: any) => {
          this.showToast("切换失败：" + ((err && err.message) || String(err)), "error");
        });
    },
    toggleProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法更新供应商。", "error");
        return;
      }
      const nextDisabled = !selected.disabled;
      // /save 是整条替换：未编辑字段原样带回，apiKey 留空表示保留原值。
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
          models: selected.models || [],
        },
      })
        .then(() => {
          this.showToast(nextDisabled ? "供应商已停用。" : "供应商已启用。");
          this.load();
        })
        .catch((err: any) => {
          this.showToast("更新失败：" + ((err && err.message) || String(err)), "error");
        });
    },
    removeProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法删除供应商。", "error");
        return;
      }
      const remove = () => {
        api("provider-admin", "/remove", {
          method: "POST",
          body: { id: selected.id },
        })
          .then(() => {
            this.showToast("供应商已删除。");
            this.selectedId = "";
            this.load();
          })
          .catch((err: any) => {
            this.showToast("删除失败：" + ((err && err.message) || String(err)), "error");
          });
      };
      const confirm = (window as any).adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除供应商「" + (selected.displayName || selected.id) + "」？其模型配置会一起删除。", remove);
      } else {
        remove();
      }
    },
    parseHeaders(text: string) {
      const out: any = {};
      String(text || "").split(/\r?\n/).forEach((line) => {
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
        apiKey: "",
      };
      this.providerModalOpen = true;
    },
    openEditProvider() {
      const selected = this.selected;
      if (!selected) return;
      const headers = selected.headers || {};
      const lines: string[] = [];
      Object.keys(headers).forEach((key) => { lines.push(key + ": " + headers[key]); });
      this.providerModalMode = "edit";
      this.providerForm = {
        id: selected.id,
        name: selected.displayName || selected.id,
        protocol: selected.protocol || PROTOCOL_OPTIONS[0].value,
        baseUrl: selected.baseUrl || "",
        proxy: selected.proxy || "",
        headersText: lines.join("\n"),
        apiKey: "",
      };
      this.providerModalOpen = true;
    },
    closeProviderModal() {
      this.providerModalOpen = false;
    },
    setProviderProtocol(value: string) {
      this.providerForm.protocol = value;
    },
    saveProviderForm() {
      const api = (window as any).adaAPI;
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
      const body: any = {
        id: editing && selected ? selected.id : this.providerForm.id,
        name,
        protocol: String(this.providerForm.protocol || "").trim(),
        baseUrl: String(this.providerForm.baseUrl || "").trim(),
        proxy: String(this.providerForm.proxy || "").trim(),
        headers: this.parseHeaders(this.providerForm.headersText),
        disabled: editing && selected ? !!selected.disabled : false,
        models: editing && selected ? selected.models || [] : [],
      };
      const key = String(this.providerForm.apiKey || "").trim();
      if (key) body.apiKey = key;
      api("provider-admin", "/save", { method: "POST", body })
        .then(() => {
          this.showToast(editing ? "供应商已保存。" : "供应商已新增。");
          this.providerModalOpen = false;
          this.load();
        })
        .catch((err: any) => {
          this.showToast((editing ? "保存失败：" : "新增失败：") + ((err && err.message) || String(err)), "error");
        });
    },
    // ── 拉取模型弹窗 ──────────────────────────────────────────────────
    openFetchModels() {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
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
        body: { id: selected.id },
      })
        .then((models: any) => {
          this.fetching = false;
          this.fetchedModels = Array.isArray(models) ? models : [];
        })
        .catch((err: any) => {
          this.fetching = false;
          this.fetchError = "拉取失败：" + ((err && err.message) || String(err));
        });
    },
    closeFetch() {
      this.fetchOpen = false;
      this.fetchedModels = [];
      this.selectedFetch = {};
      this.fetchError = "";
      this.fetchQuery = "";
    },
    toggleFetchModel(modelId: string, checked: boolean) {
      this.selectedFetch = Object.assign({}, this.selectedFetch, { [modelId]: checked });
    },
    toggleSelectAll(checked: boolean) {
      const next = Object.assign({}, this.selectedFetch);
      this.fetchedVisible.forEach((m: any) => {
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
        (m: any) => m && m.id && !this.existingModelIds[m.id] && this.selectedFetch[m.id],
      );
      if (!added.length) {
        this.showToast("没有勾选可添加的模型。", "error");
        return;
      }
      this.closeFetch();
      this.persistModels((selected.models || []).concat(added), "已添加 " + added.length + " 个模型。");
    },
    // ── 模型弹窗 ──────────────────────────────────────────────────────
    openEditModel(row: any) {
      if (!row) return;
      this.modelModalMode = "edit";
      this.editingModelId = row.id;
      this.levelInputOpen = false;
      this.levelDraft = "";
      this.modelForm = {
        id: row.id,
        displayName: row.displayName || "",
        contextWindow: row.contextWindow === undefined || row.contextWindow === null ? "" : String(row.contextWindow),
        maxOutput: row.maxOutput === undefined || row.maxOutput === null ? "" : String(row.maxOutput),
        supportsTools: !!row.supportsTools,
        disabled: !!row.disabled,
        thinkingLevels: Array.isArray(row.thinkingLevels) ? row.thinkingLevels.slice() : [],
        inputModes: Array.isArray(row.input) ? row.input.slice() : [],
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
        inputModes: ["text"],
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
    addLevel(preset?: string) {
      const value = String(preset === undefined ? this.levelDraft : preset).trim();
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
    removeLevel(index: number) {
      const next = this.modelForm.thinkingLevels.slice();
      next.splice(index, 1);
      this.modelForm.thinkingLevels = next;
    },
    onLevelKeydown(event: any) {
      if (!event) return;
      if (event.key === "Enter") {
        event.preventDefault();
        this.addLevel();
      } else if (event.key === "Escape") {
        event.preventDefault();
        this.cancelLevelInput();
      }
    },
    toggleInputMode(mode: string, checked: boolean) {
      const list = this.modelForm.inputModes.slice();
      const index = list.indexOf(mode);
      if (checked && index < 0) list.push(mode);
      if (!checked && index >= 0) list.splice(index, 1);
      this.modelForm.inputModes = list;
    },
    toggleModelTools() {
      this.modelForm.supportsTools = !this.modelForm.supportsTools;
    },
    toggleModelEnabled(checked: boolean) {
      this.modelForm.disabled = !checked;
    },
    modelFormPatch() {
      const patch: any = {
        displayName: String(this.modelForm.displayName || "").trim(),
        supportsTools: !!this.modelForm.supportsTools,
        disabled: !!this.modelForm.disabled,
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
    persistModels(models: any[], message: string) {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
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
          models,
        },
      })
        .then(() => {
          this.showToast(message);
          this.editingModelId = "";
          this.modelModalOpen = false;
          this.load();
        })
        .catch((err: any) => {
          this.showToast("保存模型失败：" + ((err && err.message) || String(err)), "error");
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
        if ((selected.models || []).some((m: any) => m && m.id === id)) {
          this.showToast("模型 ID 已存在。", "error");
          return;
        }
        const next: any = Object.assign({ id }, this.modelFormPatch());
        if (!next.displayName) next.displayName = id;
        this.persistModels((selected.models || []).concat(next), "模型已添加。");
        return;
      }
      const target = this.editingModelId;
      if (!target) return;
      const models = (selected.models || []).map((m: any) => {
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
        this.persistModels((selected.models || []).filter((m: any) => m && m.id !== target), "模型已删除。");
      };
      const confirm = (window as any).adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除模型「" + target + "」？", run);
      } else {
        run();
      }
    },
  },
  render(h) {
    const self = this;
    const selected = this.selected;
    const providers = this.providers || [];
    const stats = this.modelStats;
    const models = this.selectedModels;

    // 感叹号图标 + hover tooltip：说明性文案统一走这里，正文不重复。
    // 说明：SSR 的 VNode 白名单不含 svg，所以图标一律用文字 + CSS 画，不用内联矢量。
    const tip = (text: string, placement?: string) => (
      <span
        class={"pm-tip" + (placement === "below" ? " pm-tip--below" : "")}
        data-tip={text}
        aria-label={text}
        tabindex="0"
        role="img"
      >!</span>
    );

    const copyBtn = (text: string, label: string) => (
      <button
        class="pm-copy"
        type="button"
        aria-label={"复制" + label}
        title={"复制" + label}
        onClick={() => self.copyText(text, label)}
      >复制</button>
    );

    const defRow = (label: string, value: any, extra?: any) => (
      <div class="pm-def">
        <span class="pm-def__label">{label}</span>
        <span class="pm-def__value">{value}{extra || null}</span>
      </div>
    );

    // 头像用首字母字标：不依赖图标集，同一供应商永远同一个色块。
    const providerAvatar = (provider: any, index: number, size: string) => {
      const name = String((provider && (provider.displayName || provider.id)) || "?").trim();
      return (
        <span class={"pm-avatar" + self.avatarTone(index) + " pm-avatar--" + size}>
          {name.charAt(0).toUpperCase() || "?"}
        </span>
      );
    };

    const selectedIndex = (() => {
      for (let i = 0; i < providers.length; i += 1) {
        if (selected && providers[i].id === selected.id) return i;
      }
      return 0;
    })();

    return (
      <section class="pm-page" data-provider-admin-next>
        <div class="pm-head">
          <div class="pm-head__text">
            <h1 class="pm-title">模型供应商{tip("管理自定义模型供应商，配置后可在聊天时选择使用。", "below")}</h1>
          </div>
          <div class="pm-head__actions">
            <span class="pm-count">{providers.length} 个供应商</span>
            <Button variant="secondary" size="sm" onClick={this.load}>刷新</Button>
            <Button variant="primary" size="sm" onClick={this.openAddProvider}>添加供应商</Button>
          </div>
        </div>


        <div class="pm-layout">
          <div class="pm-list" aria-label="供应商列表">
            <div class="pm-list__head">
              <div class="pm-list__title">
                <span>供应商</span>
                <span class="pm-list__num">{providers.length}</span>
              </div>
              <label class="pm-search">
                <input
                  type="search"
                  value={this.search}
                  placeholder="搜索名称或协议"
                  aria-label="搜索供应商"
                  onInput={(event: any) => { self.search = event.target.value; }}
                />
              </label>
              <div class="pm-filters" role="group" aria-label="按状态筛选">
                <button
                  type="button"
                  class={"pm-filter" + (this.filter === "all" ? " is-active" : "")}
                  aria-pressed={this.filter === "all" ? "true" : "false"}
                  onClick={() => this.setFilter("all")}
                >全部 {providers.length}</button>
                <button
                  type="button"
                  class={"pm-filter" + (this.filter === "enabled" ? " is-active" : "")}
                  aria-pressed={this.filter === "enabled" ? "true" : "false"}
                  onClick={() => this.setFilter("enabled")}
                >已启用 {this.enabledCount}</button>
                <button
                  type="button"
                  class={"pm-filter" + (this.filter === "disabled" ? " is-active" : "")}
                  aria-pressed={this.filter === "disabled" ? "true" : "false"}
                  onClick={() => this.setFilter("disabled")}
                >已停用 {providers.length - this.enabledCount}</button>
              </div>
            </div>

            <div class="pm-list__scroll">
              {this.error ? (
                <p class="pm-error" role="alert">{this.error}</p>
              ) : this.loading ? (
                <p class="pm-empty">正在读取供应商…</p>
              ) : this.filtered.length ? (
                this.filtered.map((provider: any) => {
                  const count = Array.isArray(provider.models) ? provider.models.length : 0;
                  const active = selected && provider.id === selected.id;
                  return (
                    <button
                      key={provider.id}
                      class={"pm-item" + (active ? " is-active" : "") + (provider.disabled ? " is-off" : "")}
                      type="button"
                      aria-pressed={active ? "true" : "false"}
                      onClick={() => this.select(provider.id)}
                    >
                      {providerAvatar(provider, selectedIndex, "sm")}
                      <span class="pm-item__body">
                        <span class="pm-item__name">{provider.displayName || provider.id}</span>
                        <span class="pm-item__meta">
                          {protocolLabel(provider.protocol)}{count ? " · " + count + " 个模型" : ""}
                        </span>
                      </span>
                      <span class={"pm-dot" + (provider.disabled ? " is-off" : "")} aria-hidden="true"></span>
                    </button>
                  );
                })
              ) : providers.length ? (
                <p class="pm-empty">没有匹配的供应商。</p>
              ) : (
                <p class="pm-empty">还没有供应商。</p>
              )}
            </div>

            <button class="pm-list__add" type="button" onClick={this.openAddProvider}>
              <span class="pm-list__plus" aria-hidden="true">+</span>
              添加供应商
            </button>
          </div>

          <section class="pm-detail" aria-label="供应商详情">
            {selected ? (
              <div class="pm-detail__col">
                <div class="pm-detail__head">
                  {providerAvatar(selected, selectedIndex, "md")}
                  <div class="pm-detail__id">
                    <div class="pm-detail__title-row">
                      <h2 class="pm-detail__name">{selected.displayName || selected.id}</h2>
                      <Badge
                        tone={selected.disabled ? "neutral" : "success"}
                        label={selected.disabled ? "已停用" : "已启用"}
                      />
                      <Badge
                        tone={selected.hasApiKey ? "neutral" : "warn"}
                        label={selected.hasApiKey ? "密钥已保存" : "未配置密钥"}
                      />
                    </div>
                    <p class="pm-detail__meta">
                      {protocolLabel(selected.protocol)}
                      {stats.total ? " · " + stats.total + " 个模型" : " · 暂无模型"}
                    </p>
                  </div>
                  <div class="pm-detail__actions">
                    <Button variant="secondary" size="sm" onClick={this.openEditProvider}>编辑配置</Button>
                    <Button
                      variant="primary"
                      size="sm"
                      disabled={this.fetching}
                      onClick={this.openFetchModels}
                    >{this.fetching ? "拉取中…" : "拉取模型"}</Button>
                  </div>
                </div>

                <div class="ada-tabs__list pm-tabs" role="tablist">
                  <button
                    class="ada-tabs__tab"
                    type="button"
                    role="tab"
                    aria-selected={this.tab === "info" ? "true" : "false"}
                    tabindex={this.tab === "info" ? 0 : -1}
                    onClick={() => this.setTab("info")}
                  >供应商信息</button>
                  <button
                    class="ada-tabs__tab"
                    type="button"
                    role="tab"
                    aria-selected={this.tab === "models" ? "true" : "false"}
                    tabindex={this.tab === "models" ? 0 : -1}
                    onClick={() => this.setTab("models")}
                  >
                    模型列表
                    {stats.total ? <span class="pm-tab__num">{stats.total}</span> : null}
                  </button>
                </div>

                {this.tab === "info" ? (
                  <div class="pm-panel">
                    <p class="pm-section">基本信息</p>
                    <div class="pm-defs">
                      {defRow("供应商 ID", <code>{selected.id}</code>, copyBtn(selected.id, "供应商 ID"))}
                      {defRow("协议", <code>{selected.protocol || "—"}</code>)}
                      {defRow("端点", <code>{selected.baseUrl || "—"}</code>, selected.baseUrl ? copyBtn(selected.baseUrl, "端点") : null)}
                      {defRow("代理", <code>{selected.proxy || "—"}</code>)}
                      {defRow("请求头", headerCount(selected.headers) ? headerCount(selected.headers) + " 条" : "—")}
                      {defRow(
                        "密钥",
                        selected.hasApiKey
                          ? "尾号 " + (selected.apiKeyHint || "••••")
                          : "未配置",
                        selected.hasApiKey ? null : <span class="pm-def__warn">需配置后才能拉取模型</span>,
                      )}
                    </div>

                    <p class="pm-section">概览</p>
                    <div class="pm-stats">
                      <div class="pm-stat">
                        <span class="pm-stat__value">{stats.total}</span>
                        <span class="pm-stat__label">全部模型</span>
                      </div>
                      <div class="pm-stat">
                        <span class="pm-stat__value">{stats.enabled}</span>
                        <span class="pm-stat__label">可用模型</span>
                      </div>
                      <div class="pm-stat">
                        <span class="pm-stat__value">{stats.tools}</span>
                        <span class="pm-stat__label">支持工具</span>
                      </div>
                    </div>

                    <p class="pm-section">模型速览</p>
                    {models.length ? (
                      <div class="pm-chips">
                        {models.slice(0, 4).map((model: any) => (
                          <span class={"pm-chip" + (model.disabled ? " is-off" : "")} key={model.id}>
                            <span class="pm-chip__name">{model.id}</span>
                            <span class="pm-chip__ctx">{formatContext(model.contextWindow)}</span>
                          </span>
                        ))}
                        {models.length > 4 ? (
                          <button class="pm-chip pm-chip--more" type="button" onClick={() => this.setTab("models")}>
                            还有 {models.length - 4} 个
                          </button>
                        ) : null}
                        {models.length <= 4 ? (
                          <button class="pm-chip pm-chip--more" type="button" onClick={() => this.setTab("models")}>
                            查看全部
                          </button>
                        ) : null}
                      </div>
                    ) : (
                      <p class="pm-hint">还没有模型，点右上角「拉取模型」从服务商同步，或到「模型列表」手动添加。</p>
                    )}
                  </div>
                ) : (
                  <div class="pm-panel">
                    <div class="pm-models__head">
                      <p class="pm-section pm-section--flush">模型列表</p>
                      <Button variant="secondary" size="sm" onClick={this.openAddModel}>添加模型</Button>
                    </div>
                    {models.length ? (
                      <div class="pm-models">
                        {models.map((model: any) => {
                          const isCurrent = this.currentModelId === model.id;
                          const levels = Array.isArray(model.thinkingLevels) ? model.thinkingLevels : [];
                          const inputs = Array.isArray(model.input) ? model.input : [];
                          return (
                            <div class={"pm-mrow" + (isCurrent ? " is-current" : "") + (model.disabled ? " is-disabled" : "")} key={model.id}>
                              <div class="pm-mrow__main">
                                <span class="pm-mrow__id">{model.id}</span>
                                {model.displayName && model.displayName !== model.id ? (
                                  <span class="pm-mrow__name">{model.displayName}</span>
                                ) : null}
                                <span class="pm-mrow__tags">
                                  <span class="pm-tag">{formatContext(model.contextWindow)}</span>
                                  {inputs.map((mode: string) => (
                                    <span class="pm-tag" key={mode}>{mode}</span>
                                  ))}
                                  {model.supportsTools ? <span class="pm-tag">工具</span> : null}
                                  {levels.length ? (
                                    <span class="pm-tag pm-tag--brand" title={"推理档位：" + levels.join(" / ")}>
                                      推理 {levels.join("/")}
                                    </span>
                                  ) : null}
                                  {model.disabled ? <span class="pm-tag pm-tag--off">已停用</span> : null}
                                </span>
                              </div>
                              <div class="pm-mrow__actions">
                                {isCurrent ? (
                                  <Badge tone="success" label="当前" />
                                ) : (
                                  <Button
                                    size="sm"
                                    variant="secondary"
                                    disabled={!!model.disabled}
                                    onClick={() => this.selectModel(model.id)}
                                  >设为当前</Button>
                                )}
                                <Button size="sm" variant="ghost" onClick={() => this.openEditModel(model)}>编辑</Button>
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    ) : (
                      <Empty title="还没有模型" description="点「拉取模型」从服务商同步，或手动添加一个。" />
                    )}
                  </div>
                )}

                <div class="pm-danger">
                  <span class="pm-danger__label">
                    危险操作
                    {tip("停用后将从聊天列表中隐藏，配置与密钥保留；删除会同时移除其下所有模型。")}
                  </span>
                  <span class="pm-danger__actions">
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={this.toggleProvider}
                    >{selected.disabled ? "启用此供应商" : "停用此供应商"}</Button>
                    <Button size="sm" variant="danger" onClick={this.removeProvider}>删除</Button>
                  </span>
                </div>
              </div>
            ) : (
              <div class="pm-placeholder">
                <span class="pm-placeholder__title">选择一个供应商</span>
                <span class="pm-placeholder__hint">从左侧选择，或点击「添加供应商」创建。</span>
              </div>
            )}
          </section>
        </div>

        {this.providerModalOpen ? (
          <div class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-provider-title">
            <div class="pm-modal__backdrop" onClick={this.closeProviderModal}></div>
            <div class="pm-modal__panel">
              <div class="pm-modal__head">
                <h2 id="pm-provider-title" class="pm-modal__title">
                  {this.providerModalMode === "edit" ? "编辑供应商" : "添加供应商"}
                  {tip("保存后会整条替换供应商配置；API Key 留空表示保持原值。", "below")}
                </h2>
                <button class="pm-modal__close" type="button" aria-label="关闭" onClick={this.closeProviderModal}>×</button>
              </div>
              <div class="pm-modal__body">
                <Input
                  id="pm-provider-name"
                  label="名称"
                  modelValue={this.providerForm.name}
                  placeholder="例如 unisound"
                  onInput={(value: any) => { self.providerForm.name = String(value || ""); }}
                />
                <div class="pm-field">
                  <span class="pm-field__label">协议</span>
                  <div class="pm-seg" role="group" aria-label="协议">
                    {this.protocolOptions.map((option: any) => (
                      <button
                        key={option.value}
                        type="button"
                        class={"pm-seg__item" + (this.providerForm.protocol === option.value ? " is-active" : "")}
                        aria-pressed={this.providerForm.protocol === option.value ? "true" : "false"}
                        title={option.value}
                        onClick={() => this.setProviderProtocol(option.value)}
                      >{option.label}</button>
                    ))}
                  </div>
                </div>
                <Input
                  id="pm-provider-base-url"
                  label="端点"
                  modelValue={this.providerForm.baseUrl}
                  placeholder="https://api.example.com/v1"
                  onInput={(value: any) => { self.providerForm.baseUrl = String(value || ""); }}
                />
                <div class="pm-field">
                  <span class="pm-field__label">API 密钥{tip("密钥仅保存在本地配置中，不会随会话上传，也不会出现在浏览器请求里。")}</span>
                  <input
                    id="pm-provider-key"
                    class="pm-input"
                    type="password"
                    value={this.providerForm.apiKey}
                    placeholder={this.providerModalMode === "edit" ? "留空保持原值" : "可后补；留空表示未配置"}
                    onInput={(event: any) => { self.providerForm.apiKey = event.target.value; }}
                  />
                </div>
                <Input
                  id="pm-provider-proxy"
                  label="代理"
                  modelValue={this.providerForm.proxy}
                  placeholder="http://127.0.0.1:7890（可选）"
                  onInput={(value: any) => { self.providerForm.proxy = String(value || ""); }}
                />
                <Input
                  id="pm-provider-headers"
                  label="请求头（每行 Key: Value）"
                  multiline
                  rows={3}
                  modelValue={this.providerForm.headersText}
                  placeholder="X-Api-Version: 2024-01-01"
                  onInput={(value: any) => { self.providerForm.headersText = String(value || ""); }}
                />
              </div>
              <div class="pm-modal__foot">
                <span class="pm-modal__hint">保存后可在聊天中切换模型</span>
                <span class="pm-modal__actions">
                  <Button size="sm" variant="secondary" onClick={this.closeProviderModal}>取消</Button>
                  <Button size="sm" variant="primary" onClick={this.saveProviderForm}>
                    {this.providerModalMode === "edit" ? "保存" : "创建供应商"}
                  </Button>
                </span>
              </div>
            </div>
          </div>
        ) : null}

        {this.fetchOpen ? (
          <div class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-fetch-title">
            <div class="pm-modal__backdrop" onClick={this.closeFetch}></div>
            <div class="pm-modal__panel pm-modal__panel--wide">
              <div class="pm-modal__head">
                <h2 id="pm-fetch-title" class="pm-modal__title">
                  拉取模型
                  {tip("从该端点读取可用模型清单；已经添加过的模型不会重复导入。", "below")}
                </h2>
                <button class="pm-modal__close" type="button" aria-label="关闭" onClick={this.closeFetch}>×</button>
              </div>

              <div class="pm-fetch__ctx">
                <span class="pm-fetch__ctx-left">
                  {providerAvatar(selected, selectedIndex, "sm")}
                  <span class="pm-fetch__ctx-text">
                    <span class="pm-fetch__ctx-name">{selected ? (selected.displayName || selected.id) : ""}</span>
                    <span class="pm-fetch__ctx-url">{selected ? selected.baseUrl || "未设置端点" : ""}</span>
                  </span>
                </span>
                <span class="pm-count">
                  {this.fetching ? "读取中…" : this.fetchedModels.length + " 个可用"}
                </span>
              </div>

              <div class="pm-fetch__bar">
                <label class="pm-search pm-search--grow">
                  <input
                    type="search"
                    value={this.fetchQuery}
                    placeholder="搜索模型 ID"
                    aria-label="搜索模型"
                    onInput={(event: any) => { self.fetchQuery = event.target.value; }}
                  />
                </label>
                <label class="pm-check">
                  <input
                    type="checkbox"
                    checked={!!this.allFetchSelected.all}
                    disabled={this.allFetchSelected.selectable === 0}
                    onChange={(event: any) => this.toggleSelectAll(event.target.checked)}
                  />
                  全选
                </label>
                <span class="pm-fetch__picked">
                  {this.selectedFetchCount} / {this.fetchedModels.length}
                </span>
              </div>

              {this.fetching ? (
                <p class="pm-fetch__state">正在拉取模型…</p>
              ) : this.fetchError ? (
                <p class="pm-fetch__state pm-fetch__state--error" role="alert">{this.fetchError}</p>
              ) : this.fetchedModels.length === 0 ? (
                <p class="pm-fetch__state">没有拉取到模型。请确认端点与密钥是否正确。</p>
              ) : this.fetchedVisible.length === 0 ? (
                <p class="pm-fetch__state">没有匹配「{this.fetchQuery}」的模型。</p>
              ) : (
                <div class="pm-fetch__list">
                  {this.fetchedVisible.map((model: any) => {
                    const added = !!this.existingModelIds[model.id];
                    return (
                      <label class={"pm-fetch__row" + (added ? " is-added" : "")} key={model.id}>
                        <input
                          type="checkbox"
                          checked={!!this.selectedFetch[model.id]}
                          disabled={added}
                          onChange={(event: any) => this.toggleFetchModel(model.id, event.target.checked)}
                        />
                        <span class="pm-fetch__id">{model.id}</span>
                        <span class="pm-fetch__meta">
                          {model.displayName && model.displayName !== model.id ? model.displayName : ""}
                        </span>
                        <span class="pm-fetch__tail">
                          {added ? "已添加" : formatContext(model.contextWindow)}
                        </span>
                      </label>
                    );
                  })}
                </div>
              )}

              <div class="pm-modal__foot">
                <span class="pm-modal__hint">
                  {selected ? "将追加到 " + (selected.displayName || selected.id) + " 的模型列表" : ""}
                </span>
                <span class="pm-modal__actions">
                  <Button size="sm" variant="secondary" onClick={this.closeFetch}>取消</Button>
                  <Button
                    size="sm"
                    variant="primary"
                    disabled={this.selectedFetchCount === 0}
                    onClick={this.addSelectedModels}
                  >导入 {this.selectedFetchCount} 个模型</Button>
                </span>
              </div>
            </div>
          </div>
        ) : null}

        {this.modelModalOpen ? (
          <div class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-model-title">
            <div class="pm-modal__backdrop" onClick={this.closeModelModal}></div>
            <div class="pm-modal__panel pm-modal__panel--wide">
              <div class="pm-modal__head">
                <h2 id="pm-model-title" class="pm-modal__title">
                  {this.modelModalMode === "add" ? "添加模型" : "修改模型"}
                  {selected ? <span class="pm-modal__tag">{selected.displayName || selected.id}</span> : null}
                </h2>
                <button class="pm-modal__close" type="button" aria-label="关闭" onClick={this.closeModelModal}>×</button>
              </div>

              <div class="pm-modal__body">
                {this.modelModalMode === "add" ? (
                  <Input
                    id="pm-model-id"
                    label="模型 ID"
                    modelValue={this.modelForm.id}
                    placeholder="例如 gpt-4.1"
                    onInput={(value: any) => { self.modelForm.id = String(value || ""); }}
                  />
                ) : (
                  <div class="pm-field">
                    <span class="pm-field__label">模型 ID{tip("模型 ID 由服务商定义，改名会让已有会话找不到这个模型，因此只读。")}</span>
                    <div class="pm-input pm-input--readonly">
                      <code>{this.modelForm.id}</code>
                      <span class="pm-input__flag">只读</span>
                    </div>
                  </div>
                )}

                <Input
                  id="pm-model-name"
                  label="显示名称"
                  modelValue={this.modelForm.displayName}
                  placeholder="留空则显示模型 ID"
                  onInput={(value: any) => { self.modelForm.displayName = String(value || ""); }}
                />

                <div class="pm-form-row">
                  <Input
                    id="pm-model-context"
                    type="number"
                    label="上下文长度"
                    modelValue={this.modelForm.contextWindow}
                    placeholder="128000"
                    onInput={(value: any) => { self.modelForm.contextWindow = String(value || ""); }}
                  />
                  <Input
                    id="pm-model-max-output"
                    type="number"
                    label="最大输出"
                    modelValue={this.modelForm.maxOutput}
                    placeholder="8192"
                    onInput={(value: any) => { self.modelForm.maxOutput = String(value || ""); }}
                  />
                </div>

                <div class="pm-field">
                  <span class="pm-field__label">
                    能力
                    {tip("决定模型在聊天里能做什么：输入模态影响能否收图片/音频，工具调用影响能否使用插件与命令。")}
                  </span>
                  <div class="pm-chips pm-chips--input">
                    {INPUT_MODES.map((item) => (
                      <label
                        key={item.key}
                        class={"pm-chip-btn" + (this.modelForm.inputModes.indexOf(item.key) >= 0 ? " is-active" : "")}
                      >
                        <input
                          type="checkbox"
                          checked={this.modelForm.inputModes.indexOf(item.key) >= 0}
                          onChange={(event: any) => this.toggleInputMode(item.key, event.target.checked)}
                        />
                        {item.label}
                      </label>
                    ))}
                    <button
                      type="button"
                      class={"pm-chip-btn" + (this.modelForm.supportsTools ? " is-active" : "")}
                      aria-pressed={this.modelForm.supportsTools ? "true" : "false"}
                      onClick={this.toggleModelTools}
                    >工具调用</button>
                  </div>
                </div>

                <div class="pm-field">
                  <span class="pm-field__label">
                    推理级别
                    {tip("每个模型支持的档位不同，所以这里是可增删的标签；深色为默认档，聊天时优先使用。")}
                  </span>
                  <div class="pm-levels">
                    {this.modelForm.thinkingLevels.map((level: string, index: number) => (
                      <span class={"pm-level" + (index === 0 ? " is-default" : "")} key={level}>
                        {index === 0 ? <span class="pm-level__dot" aria-hidden="true"></span> : null}
                        <span class="pm-level__name">{level}</span>
                        {index === 0 ? null : (
                          <button
                            type="button"
                            class="pm-level__remove"
                            aria-label={"移除推理级别 " + level}
                            onClick={() => this.removeLevel(index)}
                          >×</button>
                        )}
                      </span>
                    ))}
                    {this.levelInputOpen ? (
                      <input
                        id="pm-level-input"
                        class="pm-level__input"
                        value={this.levelDraft}
                        placeholder="low / high"
                        onInput={(event: any) => { self.levelDraft = event.target.value; }}
                        onKeydown={this.onLevelKeydown}
                      />
                    ) : (
                      <button
                        type="button"
                        class="pm-level__add"
                        onClick={this.openLevelInput}
                      >+ 添加级别</button>
                    )}
                  </div>
                </div>

                <div class="pm-switch-row">
                  <span class="pm-switch-row__text">
                    <span class="pm-switch-row__title">在聊天中启用</span>
                    <span class="pm-switch-row__hint">停用后不在模型选择器中出现</span>
                  </span>
                  <label class="pm-switch">
                    <input
                      type="checkbox"
                      checked={!this.modelForm.disabled}
                      aria-label="在聊天中启用"
                      onChange={(event: any) => this.toggleModelEnabled(event.target.checked)}
                    />
                    <span class="pm-switch__track"></span>
                  </label>
                </div>
              </div>

              <div class="pm-modal__foot">
                <span class="pm-modal__hint">
                  {this.modelModalMode === "edit" ? "保存后立即生效，下次对话可选用" : "添加后可在聊天中直接选择"}
                </span>
                <span class="pm-modal__actions">
                  {this.modelModalMode === "edit" ? (
                    <Button size="sm" variant="danger" onClick={this.removeModel}>删除</Button>
                  ) : null}
                  <Button size="sm" variant="secondary" onClick={this.closeModelModal}>取消</Button>
                  <Button size="sm" variant="primary" onClick={this.saveModel}>保存</Button>
                </span>
              </div>
            </div>
          </div>
        ) : null}

        {this.toast ? (
          <div
            class="pm-toast"
            key={this.toast.id}
            data-tone={this.toast.tone}
            role="status"
            aria-live="polite"
          >{this.toast.message}</div>
        ) : null}
      </section>
    );
  },
});
