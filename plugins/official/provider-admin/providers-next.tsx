/**
 * 供应商管理 · P2-4b-2 组件化原型（只读）。
 *
 * 通过本插件 route GET /state 读取供应商目录，用 ada-ui 同构组件渲染
 * 列表、详情与模型表；读写操作仍留在 legacy `providers` 页。用于验证
 * 「真实业务数据 + 客户端 fetch + 本地状态」在岛屿运行时里成立。
 */
import { defineComponent } from "ada:render";
import { Badge, Button, Empty, Table } from "ada:ui";

export default defineComponent({
  data() {
    return {
      loading: true,
      error: "",
      providers: [],
      current: { providerId: "", modelId: "" },
      selectedId: "",
      tab: "info",
      status: "",
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
      ];
    },
  },
  mounted() {
    this.load();
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
  },
  render(h) {
    const selected = this.selected;
    const providers = this.providers || [];
    return (
      <section class="pm-page" data-provider-admin-next>
        <div class="pm-head">
          <div class="pm-head__text">
            <h1 class="pm-title">模型设置（组件化原型）</h1>
            <p class="pm-sub">只读演示：数据经插件 route /state 读取；写入仍使用原页面。</p>
          </div>
          <div class="pm-head__actions">
            <Badge tone="brand" label={providers.length ? providers.length + " 个供应商" : "无供应商"} />
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
              <Empty title="还没有供应商" description="到原「供应商」页添加一个供应商。" />
            )}
          </div>

          <section class="pm-detail" aria-label="供应商详情">
            {selected ? (
              <div>
                <div class="pm-detail__head">
                  <h2 class="pm-detail__title">{selected.displayName || selected.id}</h2>
                  <Badge tone={selected.disabled ? "neutral" : "success"} label={selected.disabled ? "已停用" : "已启用"} />
                  <Badge tone={selected.hasApiKey ? "success" : "warn"} label={selected.hasApiKey ? "密钥已保存" : "未配置密钥"} />
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
                  <div class="pm-info">
                    <p><span class="pm-info__label">ID</span><code>{selected.id}</code></p>
                    <p><span class="pm-info__label">协议</span>{selected.protocol || "—"}</p>
                    <p><span class="pm-info__label">端点</span><code>{selected.baseUrl || "—"}</code></p>
                    <p><span class="pm-info__label">密钥</span>{selected.apiKeyHint ? "尾号 " + selected.apiKeyHint : "未保存"}</p>
                  </div>
                ) : (
                  <Table
                    caption={(selected.displayName || selected.id) + " 的模型"}
                    rowKey="id"
                    rows={selected.models || []}
                    columns={this.modelColumns}
                  />
                )}
              </div>
            ) : (
              <Empty title="选择一个供应商" description="从左侧列表选择，或到原页面添加。" />
            )}
          </section>
        </div>
      </section>
    );
  },
});
