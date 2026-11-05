/**
 * 供应商管理 · P2-4b-2 组件化原型（只读）。
 *
 * 通过本插件 route GET /state 读取供应商目录，用 ada-ui 同构组件渲染
 * 列表、详情与模型表；读写操作仍留在 legacy `providers` 页。用于验证
 * 「真实业务数据 + 客户端 fetch + 本地状态」在岛屿运行时里成立。
 */
import { defineComponent } from "ada:render";
import { Badge, Button, Empty, Input, Table } from "ada:ui";

export default defineComponent({
  props: {
    data: { default: {} },
  },
  data() {
    const data = (this as any).data || {};
    const backend = data.backend || {};
    const mine = backend["provider-admin"] || {};
    const state = mine.state;
    const providers = state && Array.isArray(state.providers) ? state.providers : [];
    const current = state && state.current
      ? state.current
      : { providerId: "", modelId: "" };
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
      modelModalOpen: false,
      modelModalMode: "add",
      levelDraft: "",
      levelInputOpen: false,
      modelForm: {
        id: "",
        displayName: "",
        contextWindow: "",
        supportsTools: false,
        disabled: false,
        thinkingLevels: [],
        inputModes: [],
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
          render: (row: any, index: number, h: any) =>
            h("span", { class: "badge badge-neutral" }, row && row.supportsTools ? "支持" : "—"),
        },
        {
          label: "当前",
          render: (row: any, index: number, h: any) => {
            const current = self.current.providerId === self.selectedId && self.current.modelId === row.id;
            if (current) return h("span", { class: "badge badge-success" }, "当前");
            return h(Button, {
              size: "sm",
              variant: "secondary",
              onClick: () => self.selectModel(row.id),
            }, "设为当前");
          },
        },
        {
          label: "操作",
          render: (row: any, index: number, h: any) =>
            h(Button, {
              size: "sm",
              variant: "ghost",
              onClick: () => self.openEditModel(row),
            }, "编辑"),
        },
      ];
    },
  },
  mounted() {
    if (this.loading) this.load();
  },
  methods: {
    load() {
      this.loading = true;
      this.error = "";
      this.status = "";
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
    select(id: string) {
      this.selectedId = id;
      this.tab = "info";
      this.editing = false;
    },
    setTab(tab: string) {
      this.tab = tab;
    },
    isCurrentModel(row: any) {
      return this.current.providerId === this.selectedId && this.current.modelId === row.id;
    },
    selectModel(modelId: string) {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法切换模型。";
        return;
      }
      this.status = "正在切换当前模型…";
      api("provider-admin", "/select", {
        method: "POST",
        body: { providerId: selected.id, modelId },
      })
        .then(() => {
          this.current = { providerId: selected.id, modelId };
          this.status = "已切换当前模型。";
        })
        .catch((err: any) => {
          this.status = "切换失败：" + ((err && err.message) || String(err));
        });
    },
    toggleProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法更新供应商。";
        return;
      }
      const nextDisabled = !selected.disabled;
      this.status = nextDisabled ? "正在停用供应商…" : "正在启用供应商…";
      // /save 是整条替换：必须把未编辑字段原样带回，apiKey 留空表示保留原值。
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
          this.status = nextDisabled ? "供应商已停用。" : "供应商已启用。";
          this.load();
        })
        .catch((err: any) => {
          this.status = "更新失败：" + ((err && err.message) || String(err));
        });
    },
    removeProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法删除供应商。";
        return;
      }
      const remove = () => {
        this.status = "正在删除供应商…";
        api("provider-admin", "/remove", {
          method: "POST",
          body: { id: selected.id },
        })
          .then(() => {
            this.status = "供应商已删除。";
            this.selectedId = "";
            this.load();
          })
          .catch((err: any) => {
            this.status = "删除失败：" + ((err && err.message) || String(err));
          });
      };
      const confirm = (window as any).adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除供应商「" + (selected.displayName || selected.id) + "」？其模型配置会一起删除。", remove);
      } else {
        remove();
      }
    },
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
    },
    toggleFetchModel(modelId: string, checked: boolean) {
      this.selectedFetch = Object.assign({}, this.selectedFetch, { [modelId]: checked });
    },
    addSelectedModels() {
      const selected = this.selected;
      if (!selected) return;
      const existing = {};
      (selected.models || []).forEach((m: any) => { existing[m.id] = true; });
      const added = this.fetchedModels.filter(
        (m: any) => m && m.id && !existing[m.id] && this.selectedFetch[m.id],
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
      Object.keys(headers).forEach((key) => { lines.push(key + ": " + headers[key]); });
      this.form = {
        name: selected.displayName || selected.id,
        protocol: selected.protocol || "",
        baseUrl: selected.baseUrl || "",
        proxy: selected.proxy || "",
        headersText: lines.join("\n"),
        apiKey: "",
      };
      this.editing = true;
    },
    cancelEdit() {
      this.editing = false;
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
    saveProvider() {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法保存供应商。";
        return;
      }
      const body: any = {
        id: selected.id,
        name: String(this.form.name || "").trim() || selected.displayName || selected.id,
        protocol: String(this.form.protocol || "").trim() || selected.protocol || "",
        baseUrl: String(this.form.baseUrl || "").trim(),
        proxy: String(this.form.proxy || "").trim(),
        headers: this.parseHeaders(this.form.headersText),
        disabled: !!selected.disabled,
        models: selected.models || [],
      };
      const key = String(this.form.apiKey || "").trim();
      if (key) body.apiKey = key;
      this.status = "正在保存供应商…";
      api("provider-admin", "/save", { method: "POST", body })
        .then(() => {
          this.status = "供应商已保存。";
          this.editing = false;
          this.load();
        })
        .catch((err: any) => {
          this.status = "保存失败：" + ((err && err.message) || String(err));
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
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.status = "当前页面没有 adaAPI，无法新增供应商。";
        return;
      }
      const body: any = {
        name: String(this.newProvider.name || "").trim(),
        protocol: String(this.newProvider.protocol || "").trim(),
        baseUrl: String(this.newProvider.baseUrl || "").trim(),
        proxy: String(this.newProvider.proxy || "").trim(),
        headers: this.parseHeaders(this.newProvider.headersText),
        disabled: false,
        models: [],
      };
      if (!body.name) {
        this.status = "供应商名称不能为空。";
        return;
      }
      const key = String(this.newProvider.apiKey || "").trim();
      if (key) body.apiKey = key;
      this.status = "正在新增供应商…";
      api("provider-admin", "/save", { method: "POST", body })
        .then(() => {
          this.status = "供应商已新增。";
          this.addingProvider = false;
          this.load();
        })
        .catch((err: any) => {
          this.status = "新增失败：" + ((err && err.message) || String(err));
        });
    },
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
        supportsTools: false,
        disabled: false,
        thinkingLevels: [],
        inputModes: [],
      };
      this.modelModalOpen = true;
    },
    closeModelModal() {
      this.modelModalOpen = false;
      this.editingModelId = "";
      this.levelInputOpen = false;
      this.levelDraft = "";
    },
    cancelModelEdit() {
      this.closeModelModal();
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
    removeLevel(index: number) {
      this.modelForm.thinkingLevels.splice(index, 1);
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
    persistModels(models: any[], message: string) {
      const selected = this.selected;
      if (!selected) return;
      const api = (window as any).adaAPI;
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
          models,
        },
      })
        .then(() => {
          this.status = message;
          this.editingModelId = "";
          this.modelModalOpen = false;
          this.load();
        })
        .catch((err: any) => {
          this.status = "保存模型失败：" + ((err && err.message) || String(err));
        });
    },
    modelFormPatch() {
      const patch: any = {
        displayName: String(this.modelForm.displayName || "").trim(),
        supportsTools: !!this.modelForm.supportsTools,
        disabled: !!this.modelForm.disabled,
      };
      const context = String(this.modelForm.contextWindow || "").trim();
      if (context) patch.contextWindow = Number(context);
      const levels = this.modelForm.thinkingLevels.slice();
      if (levels.length) patch.thinkingLevels = levels;
      const inputs = this.modelForm.inputModes.slice();
      if (inputs.length) patch.input = inputs;
      return patch;
    },
    saveModel() {
      const selected = this.selected;
      if (!selected) return;
      if (this.modelModalMode === "add") {
        const id = String(this.modelForm.id || "").trim();
        if (!id) {
          this.status = "模型 ID 不能为空。";
          return;
        }
        if ((selected.models || []).some((m: any) => m && m.id === id)) {
          this.status = "模型 ID 已存在。";
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
        return Object.assign({}, m, this.modelFormPatch());
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
    const selected = this.selected;
    const providers = this.providers || [];
    return (
      <section class="pm-page" data-provider-admin-next>
        <div class="pm-head">
          <div class="pm-head__text">
            <h1 class="pm-title">模型设置</h1>
            <p class="pm-sub">管理自定义模型供应商，配置后可在聊天时选择使用。</p>
          </div>
          <div class="pm-head__actions">
            <Badge tone="brand" label={providers.length ? providers.length + " 个供应商" : "无供应商"} />
            <Button variant="primary" size="sm" onClick={this.startAddProvider}>添加供应商</Button>
            <Button variant="secondary" size="sm" onClick={this.load}>刷新</Button>
          </div>
        </div>

        {this.error ? (
          <p class="pm-status" data-tone="danger" role="alert">{this.error}</p>
        ) : null}
        {this.status ? (
          <p class="pm-status" role="status">{this.status}</p>
        ) : null}

        <div class="pm-layout">
          <div class="pm-list" aria-label="供应商列表">
            {this.loading ? (
              <p class="pm-empty">正在读取供应商…</p>
            ) : providers.length ? (
              providers.map((provider: any) => (
                <button
                  key={provider.id}
                  class={"pm-item" + (selected && provider.id === selected.id ? " is-active" : "")}
                  type="button"
                  aria-pressed={selected && provider.id === selected.id ? "true" : "false"}
                  onClick={() => this.select(provider.id)}
                >
                  <span class="pm-item__name">{provider.displayName || provider.id}</span>
                  <span class="pm-item__meta">{provider.protocol || "未设置协议"}</span>
                </button>
              ))
            ) : (
              <Empty title="还没有供应商" description="点击右上角「添加供应商」创建。" />
            )}
          </div>

          <section class="pm-detail" aria-label="供应商详情">
            {this.addingProvider ? (
              <div class="pm-info">
                <h2 class="pm-detail__title">新增供应商</h2>
                <Input
                  id="pm-new-provider-name"
                  label="名称"
                  modelValue={this.newProvider.name}
                  placeholder="例如 我的 OpenAI"
                  onInput={(value: any) => { this.newProvider.name = String(value || ""); }}
                />
                <Input
                  id="pm-new-provider-protocol"
                  label="API 格式"
                  modelValue={this.newProvider.protocol}
                  placeholder="openai-chat / openai-responses / anthropic-messages"
                  onInput={(value: any) => { this.newProvider.protocol = String(value || ""); }}
                />
                <Input
                  id="pm-new-provider-base-url"
                  label="端点"
                  modelValue={this.newProvider.baseUrl}
                  placeholder="https://api.example.com/v1"
                  onInput={(value: any) => { this.newProvider.baseUrl = String(value || ""); }}
                />
                <Input
                  id="pm-new-provider-proxy"
                  label="代理"
                  modelValue={this.newProvider.proxy}
                  placeholder="http://127.0.0.1:7890（可选）"
                  onInput={(value: any) => { this.newProvider.proxy = String(value || ""); }}
                />
                <Input
                  id="pm-new-provider-headers"
                  label="请求头（每行 Key: Value）"
                  multiline
                  rows={4}
                  modelValue={this.newProvider.headersText}
                  onInput={(value: any) => { this.newProvider.headersText = String(value || ""); }}
                />
                <Input
                  id="pm-new-provider-api-key"
                  type="password"
                  label="API Key"
                  modelValue={this.newProvider.apiKey}
                  placeholder="可后补；留空表示未配置"
                  onInput={(value: any) => { this.newProvider.apiKey = String(value || ""); }}
                />
                <p class="pm-info__actions">
                  <Button variant="primary" size="sm" onClick={this.createProvider}>创建供应商</Button>
                  <Button variant="secondary" size="sm" onClick={this.cancelAddProvider}>取消</Button>
                </p>
              </div>
            ) : selected ? (
              <div>
                <div class="pm-detail__head">
                  <h2 class="pm-detail__title">{selected.displayName || selected.id}</h2>
                  <div class="pm-detail__meta">
                    <Badge tone={selected.disabled ? "neutral" : "success"} label={selected.disabled ? "已停用" : "已启用"} />
                    <Badge tone={selected.hasApiKey ? "success" : "warn"} label={selected.hasApiKey ? "密钥已保存" : "未配置密钥"} />
                  </div>
                  <div class="pm-detail__actions">
                    <Button
                      size="sm"
                      variant={selected.disabled ? "secondary" : "ghost"}
                      onClick={this.toggleProvider}
                    >{selected.disabled ? "启用" : "停用"}</Button>
                    <Button
                      size="sm"
                      variant="danger"
                      onClick={this.removeProvider}
                    >删除</Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={this.fetching}
                      onClick={this.openFetchModels}
                    >{this.fetching ? "拉取中…" : "拉取模型"}</Button>
                  </div>
                </div>

                <div class="ada-tabs__list" role="tablist">
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
                  >模型列表</button>
                </div>

                {this.tab === "info" ? (
                  this.editing ? (
                    <div class="pm-info">
                      <Input
                        id="pm-name"
                        label="名称"
                        modelValue={this.form.name}
                        onInput={(value: any) => { this.form.name = String(value || ""); }}
                      />
                      <Input
                        id="pm-protocol"
                        label="API 格式"
                        modelValue={this.form.protocol}
                        placeholder="openai-chat / openai-responses / anthropic-messages"
                        onInput={(value: any) => { this.form.protocol = String(value || ""); }}
                      />
                      <Input
                        id="pm-base-url"
                        label="端点"
                        modelValue={this.form.baseUrl}
                        placeholder="https://api.example.com/v1"
                        onInput={(value: any) => { this.form.baseUrl = String(value || ""); }}
                      />
                      <Input
                        id="pm-proxy"
                        label="代理"
                        modelValue={this.form.proxy}
                        placeholder="http://127.0.0.1:7890（可选）"
                        onInput={(value: any) => { this.form.proxy = String(value || ""); }}
                      />
                      <Input
                        id="pm-headers"
                        label="请求头（每行 Key: Value）"
                        multiline
                        rows={4}
                        modelValue={this.form.headersText}
                        placeholder="X-Api-Version: 2024-01-01"
                        onInput={(value: any) => { this.form.headersText = String(value || ""); }}
                      />
                      <Input
                        id="pm-api-key"
                        type="password"
                        label="API Key"
                        modelValue={this.form.apiKey}
                        placeholder="留空保持原值"
                        onInput={(value: any) => { this.form.apiKey = String(value || ""); }}
                      />
                      <p class="pm-info__hint">保存会整条替换供应商配置；API Key 留空表示保持原值，模型列表原样保留。</p>
                      <div class="pm-info__actions">
                        <Button variant="primary" size="sm" onClick={this.saveProvider}>保存</Button>
                        <Button variant="secondary" size="sm" onClick={this.cancelEdit}>取消</Button>
                      </div>
                    </div>
                  ) : (
                    <div class="pm-info">
                      <p aria-label={"ID " + selected.id}><span class="pm-info__label">ID</span><code>{selected.id}</code></p>
                      <p aria-label={"协议 " + (selected.protocol || "未设置")}><span class="pm-info__label">协议</span>{selected.protocol || "—"}</p>
                      <p aria-label={"端点 " + (selected.baseUrl || "未设置")}><span class="pm-info__label">端点</span><code>{selected.baseUrl || "—"}</code></p>
                      <p aria-label={"密钥 " + (selected.apiKeyHint ? "尾号 " + selected.apiKeyHint : "未保存")}><span class="pm-info__label">密钥</span>{selected.apiKeyHint ? "尾号 " + selected.apiKeyHint : "未保存"}</p>
                      <div class="pm-info__actions">
                        <Button variant="secondary" size="sm" onClick={this.startEdit}>编辑</Button>
                      </div>
                    </div>
                  )
                ) : (
                  <div>
                    <p class="pm-info__actions">
                      <Button size="sm" variant="secondary" onClick={this.openAddModel}>添加模型</Button>
                    </p>
                    <Table
                      caption={(selected.displayName || selected.id) + " 的模型"}
                      rowKey="id"
                      rows={selected.models || []}
                      columns={this.modelColumns}
                    />
                  </div>
                )}
              </div>
            ) : (
              <Empty title="选择一个供应商" description="从左侧列表选择，或点击右上角添加供应商。" />
            )}
          </section>
        </div>
        {this.fetchOpen ? (
          <div class="pm-modal" role="dialog" aria-modal="true" aria-labelledby="pm-fetch-title">
            <div class="pm-modal__backdrop" onClick={this.closeFetch}></div>
            <div class="pm-modal__panel">
              <div class="pm-modal__head">
                <h2 id="pm-fetch-title" class="pm-modal__title">
                  从「{selected ? (selected.displayName || selected.id) : ""}」选择模型
                </h2>
                <Button size="sm" variant="ghost" onClick={this.closeFetch}>关闭</Button>
              </div>
              <p class="pm-fetch__hint">勾选要添加的模型；已添加的模型不可重复选择。</p>
              {this.fetching ? (
                <p class="pm-fetch__state">正在拉取模型…</p>
              ) : this.fetchError ? (
                <p class="pm-fetch__state pm-fetch__state--error" role="alert">{this.fetchError}</p>
              ) : this.fetchedModels.length === 0 ? (
                <p class="pm-fetch__state">没有拉取到模型。</p>
              ) : (
                <div class="pm-fetch__list">
                  {this.fetchedModels.map((model: any) => {
                    const added = !!(selected && (selected.models || []).some((m: any) => m && m.id === model.id));
                    return (
                      <label class={"pm-fetch__row" + (added ? " is-added" : "")} key={model.id}>
                        <input
                          type="checkbox"
                          checked={!!this.selectedFetch[model.id]}
                          disabled={added}
                          onChange={(event: any) => this.toggleFetchModel(model.id, event.target.checked)}
                        />
                        <span class="ada-remote__id">{model.id}</span>
                        <span class="ada-remote__meta">{model.displayName || ""}</span>
                        {added ? <span class="badge badge-neutral">已添加</span> : null}
                      </label>
                    );
                  })}
                </div>
              )}
              <div class="pm-modal__actions">
                <Button size="sm" variant="secondary" onClick={this.closeFetch}>取消</Button>
                <Button
                  size="sm"
                  variant="primary"
                  disabled={this.selectedFetchCount === 0}
                  onClick={this.addSelectedModels}
                >添加所选（{this.selectedFetchCount}）</Button>
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
                  {this.modelModalMode === "add" ? "添加模型" : "编辑模型"}
                </h2>
                <Button size="sm" variant="ghost" onClick={this.closeModelModal}>关闭</Button>
              </div>
              <div class="pm-model-modal__body">
                {this.modelModalMode === "add" ? (
                  <Input
                    id="pm-model-id"
                    label="模型 ID"
                    modelValue={this.modelForm.id}
                    placeholder="例如 gpt-4.1"
                    onInput={(value: any) => { this.modelForm.id = String(value || ""); }}
                  />
                ) : (
                  <p class="pm-info__hint">ID：<code>{this.modelForm.id}</code></p>
                )}
                <Input
                  id="pm-model-name"
                  label="显示名"
                  modelValue={this.modelForm.displayName}
                  onInput={(value: any) => { this.modelForm.displayName = String(value || ""); }}
                />
                <Input
                  id="pm-model-context"
                  type="number"
                  label="上下文窗口"
                  modelValue={this.modelForm.contextWindow}
                  placeholder="例如 128000"
                  onInput={(value: any) => { this.modelForm.contextWindow = String(value || ""); }}
                />
                <div class="pm-field">
                  <span class="pm-field__label">推理等级</span>
                  <div class="pm-levels">
                    {this.modelForm.thinkingLevels.map((level: string, index: number) => (
                      <span class="pm-level" key={level}>
                        <span class="pm-level__name">{level}</span>
                        <button
                          type="button"
                          class="pm-level__remove"
                          aria-label={"移除推理等级 " + level}
                          onClick={() => this.removeLevel(index)}
                        >×</button>
                      </span>
                    ))}
                    {this.levelInputOpen ? (
                      <input
                        id="pm-level-input"
                        class="pm-level__input"
                        value={this.levelDraft}
                        placeholder="low / high"
                        onInput={(event: any) => { this.levelDraft = event.target.value; }}
                        onKeydown={this.onLevelKeydown}
                      />
                    ) : (
                      <button
                        type="button"
                        class="pm-level__add"
                        aria-label="添加推理等级"
                        onClick={this.openLevelInput}
                      >+</button>
                    )}
                  </div>
                </div>
                <div class="pm-field">
                  <span class="pm-field__label">输入模态</span>
                  <div class="pm-checks">
                    {["text", "image", "audio", "video"].map((mode: string) => (
                      <label class="checkbox-row" key={mode}>
                        <input
                          id={"pm-mode-" + mode}
                          type="checkbox"
                          checked={this.modelForm.inputModes.indexOf(mode) >= 0}
                          onChange={(event: any) => this.toggleInputMode(mode, event.target.checked)}
                        />
                        {mode}
                      </label>
                    ))}
                  </div>
                </div>
                <div class="pm-info__actions">
                  <Button
                    size="sm"
                    variant={this.modelForm.supportsTools ? "primary" : "secondary"}
                    onClick={() => { this.modelForm.supportsTools = !this.modelForm.supportsTools; }}
                  >工具：{this.modelForm.supportsTools ? "支持" : "不支持"}</Button>
                  <Button
                    size="sm"
                    variant={this.modelForm.disabled ? "secondary" : "ghost"}
                    onClick={() => { this.modelForm.disabled = !this.modelForm.disabled; }}
                  >状态：{this.modelForm.disabled ? "停用" : "启用"}</Button>
                </div>
              </div>
              <div class="pm-modal__actions">
                {this.modelModalMode === "edit" ? (
                  <Button size="sm" variant="danger" onClick={this.removeModel}>删除模型</Button>
                ) : null}
                <Button size="sm" variant="secondary" onClick={this.closeModelModal}>取消</Button>
                <Button size="sm" variant="primary" onClick={this.saveModel}>保存模型</Button>
              </div>
            </div>
          </div>
        ) : null}
      </section>
    );
  },
});
