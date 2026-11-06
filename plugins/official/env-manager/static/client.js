// generated from client.tsx (sha256:e5ab4d3d2a229cd1) by tools/pluginbuild — do not edit
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

// env-page.tsx
var KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;
var SCOPES = [
  { value: "workspace", label: "工作区" },
  { value: "global", label: "全局" }
];
function scopeLabel(scope) {
  return scope === "global" ? "全局" : "工作区";
}
function varsOf(layer) {
  return layer && layer.vars && typeof layer.vars === "object" ? layer.vars : {};
}
function formatBytes(size) {
  const bytes = Number(size);
  if (!isFinite(bytes) || bytes <= 0) return "—";
  if (bytes >= 1024 * 1024) return Math.round(bytes / (1024 * 1024) * 10) / 10 + " MB";
  if (bytes >= 1024) return Math.round(bytes / 1024) + " KB";
  return bytes + " B";
}
function formatTime(value) {
  const text2 = String(value || "");
  if (!text2) return "—";
  const d = new Date(text2);
  if (isNaN(d.getTime())) return text2;
  const pad = (n) => n < 10 ? "0" + n : String(n);
  return d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) + " " + pad(d.getHours()) + ":" + pad(d.getMinutes());
}
function maskValue(value) {
  const text2 = String(value === void 0 || value === null ? "" : value);
  if (!text2) return "";
  const tail = text2.length > 4 ? text2.slice(-4) : "";
  return "••••••••••" + tail;
}
var env_page_default = defineComponent({
  props: {
    data: { default: {} }
  },
  data() {
    const data = this.data || {};
    const backend = data.backend || {};
    const mine = backend["env-manager"] || {};
    const state = mine.page_settings || mine.state || null;
    return {
      loading: !state,
      error: "",
      state,
      toast: null,
      toastSeq: 0,
      toastTimer: 0,
      filter: "all",
      form: { scope: "workspace", key: "", value: "" },
      revealed: {},
      busy: false,
      reloading: false
    };
  },
  computed: {
    workspaceVars() {
      return varsOf(this.state && this.state.workspace);
    },
    globalVars() {
      return varsOf(this.state && this.state.global);
    },
    wall() {
      return {
        workspace: this.state && this.state.workspace ? this.state.workspace.path || "" : "",
        global: this.state && this.state.global ? this.state.global.path || "" : ""
      };
    },
    /// 工作区优先展示；同名全局项标记为「被覆盖」，避免看成一式两份。
    rows() {
      const workspace = this.workspaceVars;
      const global = this.globalVars;
      const out = [];
      Object.keys(workspace).sort().forEach((key) => {
        out.push({
          id: "workspace:" + key,
          scope: "workspace",
          key,
          value: workspace[key],
          overridden: Object.prototype.hasOwnProperty.call(global, key)
        });
      });
      Object.keys(global).sort().forEach((key) => {
        out.push({
          id: "global:" + key,
          scope: "global",
          key,
          value: global[key],
          overridden: false
        });
      });
      return out;
    },
    filteredRows() {
      if (this.filter === "all") return this.rows;
      return this.rows.filter((row) => row.scope === this.filter);
    },
    counts() {
      return {
        all: this.rows.length,
        workspace: this.rows.filter((row) => row.scope === "workspace").length,
        global: this.rows.filter((row) => row.scope === "global").length
      };
    },
    warnings() {
      const list = this.state && Array.isArray(this.state.warnings) ? this.state.warnings : [];
      return list;
    },
    binPaths() {
      const bins = this.state && this.state.bins;
      return bins && Array.isArray(bins.paths) ? bins.paths : [];
    },
    binPrograms() {
      const bins = this.state && this.state.bins;
      return bins && Array.isArray(bins.programs) ? bins.programs : [];
    },
    keyError() {
      const key = String(this.form.key || "").trim();
      if (!key) return "";
      if (!KEY_PATTERN.test(key)) return "变量名需匹配 ^[A-Za-z_][A-Za-z0-9_]*$";
      if (key.indexOf("ADAG_") === 0) return "ADAG_ 前缀由宿主保留";
      return "";
    }
  },
  mounted() {
    if (this.loading) this.load();
  },
  methods: {
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
        this.error = "当前页面没有 adaAPI，无法读取环境变量。";
        return;
      }
      api("env-manager", "/state").then((data) => {
        this.state = data || null;
        this.loading = false;
      }).catch((err) => {
        this.loading = false;
        this.error = err && err.message || String(err);
      });
    },
    setFilter(filter) {
      this.filter = filter;
    },
    setScope(scope) {
      this.form.scope = scope;
    },
    setKey(value) {
      this.form.key = String(value || "");
    },
    setValue(value) {
      this.form.value = String(value || "");
    },
    isRevealed(row) {
      return !!this.revealed[row.id];
    },
    toggleReveal(row) {
      const next = Object.assign({}, this.revealed);
      if (next[row.id]) delete next[row.id];
      else next[row.id] = true;
      this.revealed = next;
    },
    displayValue(row) {
      if (!String(row.value === void 0 || row.value === null ? "" : row.value)) return "";
      return this.isRevealed(row) ? String(row.value) : maskValue(row.value);
    },
    copyText(text2, label) {
      const value = String(text2 === void 0 || text2 === null ? "" : text2);
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
    submit() {
      const key = String(this.form.key || "").trim();
      if (!key) {
        this.showToast("变量名不能为空。", "error");
        return;
      }
      if (this.keyError) {
        this.showToast(this.keyError, "error");
        return;
      }
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法写入。", "error");
        return;
      }
      this.busy = true;
      api("env-manager", "/set", {
        method: "POST",
        body: { scope: this.form.scope, key, value: String(this.form.value || "") }
      }).then(() => {
        this.busy = false;
        this.showToast("已写入 " + scopeLabel(this.form.scope) + " " + key + "，重载插件后生效。");
        this.form = { scope: this.form.scope, key: "", value: "" };
        this.load();
      }).catch((err) => {
        this.busy = false;
        this.showToast("写入失败：" + (err && err.message || String(err)), "error");
      });
    },
    removeVar(row) {
      const api = window.adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法删除。", "error");
        return;
      }
      const run = () => {
        api("env-manager", "/remove", {
          method: "POST",
          body: { scope: row.scope, key: row.key }
        }).then(() => {
          this.showToast("已删除 " + row.key + "。");
          this.load();
        }).catch((err) => {
          this.showToast("删除失败：" + (err && err.message || String(err)), "error");
        });
      };
      const confirm = window.adaConfirm;
      if (typeof confirm === "function") {
        confirm("删除 " + scopeLabel(row.scope) + " 变量「" + row.key + "」？", run);
      } else {
        run();
      }
    },
    reloadPlugins() {
      if (this.reloading) return;
      this.reloading = true;
      const meta = typeof document === "undefined" ? null : document.querySelector('meta[name="csrf-token"]');
      const headers = { Accept: "text/html" };
      if (meta && meta.getAttribute("content")) headers["X-CSRF-Token"] = meta.getAttribute("content");
      fetch("/agent-plugins/reload", { method: "POST", credentials: "same-origin", headers }).then((response) => {
        if (!response.ok) throw new Error("HTTP " + response.status);
        this.reloading = false;
        this.showToast("插件已重载，新子进程会拿到新环境。");
      }).catch((err) => {
        this.reloading = false;
        this.showToast("重载失败：" + (err && err.message || String(err)), "error");
      });
    }
  },
  render(h2) {
    const self = this;
    const tip = (text2, placement) => /* @__PURE__ */ h2(
      "span",
      {
        class: "em-tip" + (placement === "below" ? " em-tip--below" : ""),
        "data-tip": text2,
        "aria-label": text2,
        tabindex: "0",
        role: "img"
      },
      "!"
    );
    return /* @__PURE__ */ h2("div", { class: "em-page", "data-env-page": true }, /* @__PURE__ */ h2("div", { class: "em-head" }, /* @__PURE__ */ h2("div", { class: "em-title-row" }, /* @__PURE__ */ h2("h1", { class: "em-title" }, "环境变量"), tip(
      "注入插件启动的子进程（ctx.exec、后台任务、stdio MCP）。全局 ~/.adago/env.json 与工作区 .adago/env.json 两层，同名时工作区覆盖全局；改完需重载插件。",
      "below"
    )), /* @__PURE__ */ h2("div", { class: "em-actions" }, /* @__PURE__ */ h2("button", { class: "em-btn", type: "button", onClick: this.load }, "刷新"), /* @__PURE__ */ h2("button", { class: "em-btn", type: "button", disabled: this.reloading, onClick: this.reloadPlugins }, this.reloading ? "重载中…" : "重载插件"))), /* @__PURE__ */ h2("section", { class: "em-card", "aria-label": "环境变量" }, /* @__PURE__ */ h2("div", { class: "em-card__head" }, /* @__PURE__ */ h2("div", { class: "em-card__title" }, /* @__PURE__ */ h2("span", null, "环境变量"), /* @__PURE__ */ h2("span", { class: "em-count" }, this.counts.all)), /* @__PURE__ */ h2("div", { class: "em-seg em-seg--sm", role: "group", "aria-label": "按范围筛选" }, /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "em-seg__item" + (this.filter === "all" ? " is-active" : ""),
        "aria-pressed": this.filter === "all" ? "true" : "false",
        onClick: () => this.setFilter("all")
      },
      "全部"
    ), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "em-seg__item" + (this.filter === "workspace" ? " is-active" : ""),
        "aria-pressed": this.filter === "workspace" ? "true" : "false",
        onClick: () => this.setFilter("workspace")
      },
      "工作区"
    ), /* @__PURE__ */ h2(
      "button",
      {
        type: "button",
        class: "em-seg__item" + (this.filter === "global" ? " is-active" : ""),
        "aria-pressed": this.filter === "global" ? "true" : "false",
        onClick: () => this.setFilter("global")
      },
      "全局"
    ))), /* @__PURE__ */ h2(
      "form",
      {
        class: "em-form",
        onSubmit: (event) => {
          if (event) event.preventDefault();
          this.submit();
        }
      },
      /* @__PURE__ */ h2("div", { class: "em-seg", role: "group", "aria-label": "范围" }, SCOPES.map((scope) => /* @__PURE__ */ h2(
        "button",
        {
          key: scope.value,
          type: "button",
          class: "em-seg__item" + (this.form.scope === scope.value ? " is-active" : ""),
          "aria-pressed": this.form.scope === scope.value ? "true" : "false",
          onClick: () => this.setScope(scope.value)
        },
        scope.label
      ))),
      /* @__PURE__ */ h2(
        "input",
        {
          class: "em-input em-mono",
          type: "text",
          value: this.form.key,
          placeholder: "变量名，如 GITHUB_TOKEN",
          "aria-label": "变量名",
          "aria-invalid": this.keyError ? "true" : "false",
          onInput: (event) => this.setKey(event.target.value)
        }
      ),
      /* @__PURE__ */ h2(
        "input",
        {
          class: "em-input em-mono",
          type: "text",
          value: this.form.value,
          placeholder: "值（可为空）",
          "aria-label": "值",
          onInput: (event) => this.setValue(event.target.value)
        }
      ),
      /* @__PURE__ */ h2(
        "button",
        {
          class: "em-btn em-btn--primary",
          type: "submit",
          disabled: this.busy || !!this.keyError
        },
        "添加 / 更新"
      )
    ), this.keyError ? /* @__PURE__ */ h2("p", { class: "em-form__error", role: "alert" }, this.keyError) : null, /* @__PURE__ */ h2("div", { class: "em-list" }, this.error ? /* @__PURE__ */ h2("p", { class: "em-error", role: "alert" }, this.error) : this.loading ? /* @__PURE__ */ h2("p", { class: "em-empty" }, "正在读取环境变量…") : this.filteredRows.length ? this.filteredRows.map((row) => /* @__PURE__ */ h2("div", { class: "em-row", key: row.id }, /* @__PURE__ */ h2("code", { class: "em-row__key" + (row.overridden ? " is-overridden" : "") }, row.key), /* @__PURE__ */ h2("span", { class: "em-row__value em-mono" + (this.isRevealed(row) || !row.value ? " is-plain" : "") }, row.value ? this.displayValue(row) : "（空值）"), /* @__PURE__ */ h2("span", { class: "em-chip em-chip--" + row.scope }, scopeLabel(row.scope)), row.overridden ? /* @__PURE__ */ h2("span", { class: "em-chip em-chip--warn", title: "同名全局变量被工作区覆盖" }, "覆盖全局") : null, /* @__PURE__ */ h2("span", { class: "em-row__actions" }, row.value ? /* @__PURE__ */ h2("button", { class: "em-link", type: "button", onClick: () => this.toggleReveal(row) }, this.isRevealed(row) ? "隐藏" : "显示") : null, /* @__PURE__ */ h2("button", { class: "em-link", type: "button", onClick: () => this.copyText(row.value, "变量值") }, "复制"), /* @__PURE__ */ h2("button", { class: "em-link em-link--danger", type: "button", onClick: () => this.removeVar(row) }, "删除")))) : this.counts.all ? /* @__PURE__ */ h2("p", { class: "em-empty" }, "当前筛选下没有变量。") : /* @__PURE__ */ h2("div", { class: "em-empty em-empty--block" }, /* @__PURE__ */ h2("p", { class: "em-empty__title" }, "还没有环境变量"), /* @__PURE__ */ h2("p", { class: "em-empty__desc" }, "在上面添加，或直接用命令行写入："), /* @__PURE__ */ h2("div", { class: "em-cmd" }, /* @__PURE__ */ h2("code", null, "adag env set GITHUB_TOKEN=***"), /* @__PURE__ */ h2(
      "button",
      {
        class: "em-link",
        type: "button",
        onClick: () => this.copyText("adag env set GITHUB_TOKEN=", "命令")
      },
      "复制"
    )))), this.warnings.length ? /* @__PURE__ */ h2("ul", { class: "em-notes" }, this.warnings.map((item, index) => /* @__PURE__ */ h2("li", { key: index }, item))) : null, /* @__PURE__ */ h2("p", { class: "em-foot" }, "值默认打码，仅在本页可见；命名需匹配 ", /* @__PURE__ */ h2("code", null, "^[A-Za-z_][A-Za-z0-9_]*$"), "，", /* @__PURE__ */ h2("code", null, "ADAG_"), " 前缀保留。")), /* @__PURE__ */ h2("section", { class: "em-card", "aria-label": "服务程序目录" }, /* @__PURE__ */ h2("div", { class: "em-card__head" }, /* @__PURE__ */ h2("div", { class: "em-card__title" }, /* @__PURE__ */ h2("span", null, "服务程序目录"), tip("把可执行文件放进这些目录，插件子进程的 PATH 会自动前置它们（工作区优先）；新启动的子进程生效，不用写绝对路径。", "below")), /* @__PURE__ */ h2("span", { class: "em-card__hint" }, "放进目录的可执行文件会自动加入插件子进程 PATH")), /* @__PURE__ */ h2("div", { class: "em-bins" }, this.binPaths.map((item) => /* @__PURE__ */ h2("div", { class: "em-bin", key: item.scope }, /* @__PURE__ */ h2("div", { class: "em-bin__top" }, /* @__PURE__ */ h2("span", { class: "em-chip em-chip--" + item.scope }, scopeLabel(item.scope)), /* @__PURE__ */ h2("span", { class: "em-status" + (item.exists ? "" : " is-missing") }, item.exists ? "已创建" : "未创建")), /* @__PURE__ */ h2("code", { class: "em-bin__path" }, item.path), /* @__PURE__ */ h2(
      "button",
      {
        class: "em-link em-bin__copy",
        type: "button",
        onClick: () => this.copyText(item.path, "目录路径")
      },
      "复制路径"
    ))), this.binPaths.length ? null : /* @__PURE__ */ h2("p", { class: "em-empty" }, "还没有可用的服务程序目录。")), /* @__PURE__ */ h2("div", { class: "em-prog-head" }, /* @__PURE__ */ h2("span", { class: "em-prog-head__name" }, "程序"), /* @__PURE__ */ h2("span", { class: "em-prog-head__size" }, "大小"), /* @__PURE__ */ h2("span", { class: "em-prog-head__time" }, "修改时间")), /* @__PURE__ */ h2("div", { class: "em-progs" }, this.binPrograms.length ? this.binPrograms.map((item) => /* @__PURE__ */ h2("div", { class: "em-prog", key: item.scope + ":" + item.name }, /* @__PURE__ */ h2("span", { class: "em-prog__cell" }, /* @__PURE__ */ h2("code", { class: "em-prog__name" }, item.name), /* @__PURE__ */ h2("span", { class: "em-chip em-chip--" + item.scope }, scopeLabel(item.scope))), /* @__PURE__ */ h2("span", { class: "em-prog__size" }, formatBytes(item.size)), /* @__PURE__ */ h2("span", { class: "em-prog__time" }, formatTime(item.modified)))) : /* @__PURE__ */ h2("p", { class: "em-empty" }, "目录里还没有可执行文件，把程序放进去即可。"))), this.toast ? /* @__PURE__ */ h2(
      "div",
      {
        class: "em-toast",
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
  "page:settings": env_page_default
};
var client_default = env_page_default;
export {
  client_default as default,
  islands
};
