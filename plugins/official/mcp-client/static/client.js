// generated from client.tsx (sha256:0a9c3d0a1736150b) by tools/pluginbuild — do not edit
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
var CONFIG_PATH = ".adago/mcp.json";
var USER_CONFIG_PATH = "~/.adago/mcp.json";
var MAX_HIGHLIGHT = 64 * 1024;
function statusLabel(status) {
  if (status === "ready") return "已连接";
  if (status === "disabled") return "已停用";
  return "连接失败";
}
function accessLabel(access) {
  return access === "read" ? "只读" : "写入";
}
function errorText(err) {
  const message = err && err.message ? String(err.message) : String(err);
  const cut = message.indexOf("失败: ");
  return (cut >= 0 ? message.slice(cut + "失败: ".length) : message) || "操作失败";
}
function clockText() {
  const d = /* @__PURE__ */ new Date();
  const pad = (n) => n < 10 ? "0" + String(n) : String(n);
  return pad(d.getHours()) + ":" + pad(d.getMinutes());
}
function describeJSONError(err, text2) {
  const message = err && err.message ? String(err.message) : String(err);
  let pos = -1;
  const marker = "position ";
  const at = message.indexOf(marker);
  if (at >= 0) {
    let i = at + marker.length;
    let digits = "";
    while (i < message.length && message.charAt(i) >= "0" && message.charAt(i) <= "9") {
      digits += message.charAt(i);
      i++;
    }
    if (digits) pos = Number(digits);
  }
  if (pos < 0) {
    const frame = message.indexOf('..."');
    if (frame >= 0) {
      const rest = message.slice(frame + 4);
      const end = rest.indexOf('"');
      if (end > 0) {
        const snippet = '"' + rest.slice(0, end);
        const found = text2.lastIndexOf(snippet);
        if (found >= 0) pos = found + snippet.length - 1;
      }
    }
  }
  if (pos < 0) return message;
  const before = text2.slice(0, pos);
  const line = before.split("\n").length;
  const col = pos - before.lastIndexOf("\n");
  return "第 " + line + " 行第 " + col + " 列 · " + message;
}
function parseConfig(text2) {
  const lines = text2 ? text2.split("\n").length : 0;
  if (!text2 || !text2.trim()) {
    return { ok: false, message: "配置为空", servers: 0, lines };
  }
  try {
    const value = JSON.parse(text2);
    if (!value || typeof value !== "object" || !Array.isArray(value.servers)) {
      return { ok: false, message: '需要 {"version":1,"servers":[…]} 结构', servers: 0, lines };
    }
    return { ok: true, message: "JSON 有效", servers: value.servers.length, lines };
  } catch (err) {
    return { ok: false, message: describeJSONError(err, text2), servers: 0, lines };
  }
}
function prettyJSON(text2) {
  const parsed = parseConfig(text2);
  if (!parsed.ok) return { ok: false, text: text2, message: parsed.message };
  try {
    return { ok: true, text: JSON.stringify(JSON.parse(text2), null, 2) + "\n", message: "" };
  } catch (err) {
    return { ok: false, text: text2, message: describeJSONError(err, text2) };
  }
}
function highlightTokens(text2) {
  const out = [];
  if (text2.length > MAX_HIGHLIGHT) return [{ text: text2, cls: "" }];
  let plain = "";
  const flush = () => {
    if (plain) {
      out.push({ text: plain, cls: "" });
      plain = "";
    }
  };
  const push = (value, cls) => {
    out.push({ text: value, cls });
  };
  const isDigit = (c) => c >= "0" && c <= "9";
  const isWord = (c) => isDigit(c) || c >= "a" && c <= "z" || c >= "A" && c <= "Z" || c === "_";
  const matchAt = (word, at) => {
    for (let k = 0; k < word.length; k++) {
      if (text2.charAt(at + k) !== word.charAt(k)) return false;
    }
    return true;
  };
  let i = 0;
  while (i < text2.length) {
    const ch = text2.charAt(i);
    if (ch === '"') {
      let j = i + 1;
      while (j < text2.length) {
        const c = text2.charAt(j);
        if (c === "\\") {
          j += 2;
          continue;
        }
        if (c === '"') {
          j++;
          break;
        }
        j++;
      }
      let k = j;
      while (k < text2.length && (text2.charAt(k) === " " || text2.charAt(k) === "	" || text2.charAt(k) === "\n" || text2.charAt(k) === "\r")) k++;
      flush();
      push(text2.slice(i, j), text2.charAt(k) === ":" ? "mc-tok-key" : "mc-tok-str");
      i = j;
      continue;
    }
    if (isDigit(ch) || ch === "-" && isDigit(text2.charAt(i + 1))) {
      let j = i + (ch === "-" ? 1 : 0);
      while (j < text2.length) {
        const c = text2.charAt(j);
        if (isDigit(c) || c === "." || c === "e" || c === "E" || c === "+" || c === "-") j++;
        else break;
      }
      const token = text2.slice(i, j);
      if (token.length && !isWord(text2.charAt(j))) {
        flush();
        push(token, "mc-tok-num");
        i = j;
        continue;
      }
    }
    let literal = "";
    if (ch === "t" && matchAt("true", i)) literal = "true";
    else if (ch === "f" && matchAt("false", i)) literal = "false";
    else if (ch === "n" && matchAt("null", i)) literal = "null";
    if (literal && !isWord(text2.charAt(i + literal.length))) {
      flush();
      push(literal, "mc-tok-lit");
      i += literal.length;
      continue;
    }
    plain += ch;
    i++;
  }
  flush();
  return out;
}
var page_default = defineComponent({
  props: {
    data: { default: {} }
  },
  data() {
    const data = this.data || {};
    const backend = data.backend || {};
    const mine = backend["mcp-client"] || {};
    const state = mine.page_servers || mine.state || null;
    return {
      state,
      loading: !state,
      error: "",
      toast: null,
      toastSeq: 0,
      toastTimer: 0,
      filter: "all",
      lastRefresh: "",
      reloading: false,
      expanded: {},
      modalOpen: false,
      configText: "",
      configLoaded: false,
      saving: false
    };
  },
  computed: {
    servers() {
      return this.state && Array.isArray(this.state.servers) ? this.state.servers : [];
    },
    counts() {
      const all = this.servers;
      return {
        all: all.length,
        ready: all.filter((s) => s.status === "ready").length,
        degraded: all.filter((s) => s.status === "degraded").length,
        disabled: all.filter((s) => s.status === "disabled").length
      };
    },
    filters() {
      const out = [
        { value: "all", label: "全部", count: this.counts.all },
        { value: "ready", label: "已连接", count: this.counts.ready }
      ];
      if (this.counts.degraded) out.push({ value: "degraded", label: "异常", count: this.counts.degraded });
      out.push({ value: "disabled", label: "已停用", count: this.counts.disabled });
      return out;
    },
    filteredServers() {
      if (this.filter === "all") return this.servers;
      return this.servers.filter((s) => s.status === this.filter);
    },
    configPath() {
      return this.state && this.state.configPath || CONFIG_PATH;
    },
    userConfigPath() {
      return this.state && this.state.userConfigPath || USER_CONFIG_PATH;
    },
    validation() {
      return parseConfig(this.configText);
    },
    tokens() {
      return highlightTokens(this.configText);
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
    api(path, options) {
      const fn = window.adaAPI;
      if (typeof fn !== "function") throw new Error("页面缺少 adaAPI，无法调用插件接口");
      return fn("mcp-client", path, options || {});
    },
    load() {
      this.loading = true;
      this.error = "";
      this.api("/servers").then((data) => {
        this.state = data || null;
        this.loading = false;
        this.lastRefresh = clockText();
      }).catch((err) => {
        this.loading = false;
        this.error = errorText(err);
        this.showToast("读取 server 状态失败：" + this.error, "error");
      });
    },
    refresh() {
      this.load();
      this.showToast("已刷新 server 状态。");
    },
    setFilter(value) {
      this.filter = value;
    },
    toggleTools(server) {
      const next = Object.assign({}, this.expanded);
      if (next[server.id]) delete next[server.id];
      else next[server.id] = true;
      this.expanded = next;
    },
    isExpanded(server) {
      return !!this.expanded[server.id];
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
    toggleServer(server, wanted) {
      if (this.reloading) return;
      this.api("/servers/toggle", { method: "POST", body: { id: server.id, enabled: wanted } }).then(() => {
        this.showToast("已保存：" + server.id + (wanted ? " 已启用" : " 已停用") + "，重载插件后生效。");
        this.load();
      }).catch((err) => {
        this.showToast("启停失败：" + errorText(err), "error");
        this.load();
      });
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
        this.showToast("插件已重载，正在刷新状态…");
        this.load();
      }).catch((err) => {
        this.reloading = false;
        this.showToast("重载失败：" + errorText(err), "error");
      });
    },
    /** probe 就是重新装配：会重新 initialize + tools/list，等于对降级 server 重探测。 */
    probe() {
      this.showToast("正在重新探测…");
      this.reloadPlugins();
    },
    openModal() {
      this.modalOpen = true;
      if (!this.configLoaded) this.loadConfig();
    },
    closeModal() {
      this.modalOpen = false;
    },
    loadConfig() {
      this.api("/config").then((config) => {
        const text2 = config && config.text || "";
        const pretty = text2.trim() ? prettyJSON(text2) : { ok: true, text: text2 };
        this.configText = pretty.ok ? pretty.text : text2;
        this.configLoaded = true;
      }).catch((err) => {
        this.showToast("读取配置失败：" + errorText(err), "error");
      });
    },
    onEditorInput(event) {
      this.configText = event && event.target ? event.target.value : "";
    },
    /** 高亮层与 textarea 的滚动位置必须一致，否则长配置会错行。 */
    syncScroll(event) {
      if (typeof document === "undefined" || !event || !event.target) return;
      const input = event.target;
      const wrap = input.parentNode;
      const pre = wrap ? wrap.querySelector("[data-mcp-hl]") : null;
      if (!pre) return;
      pre.scrollTop = input.scrollTop;
      pre.scrollLeft = input.scrollLeft;
    },
    format() {
      const pretty = prettyJSON(this.configText);
      if (!pretty.ok) {
        this.showToast("格式化失败：" + pretty.message, "error");
        return;
      }
      if (pretty.text === this.configText) {
        this.showToast("已经是格式化后的 JSON。");
        return;
      }
      this.configText = pretty.text;
      this.showToast("已格式化（保存后写入文件）。");
    },
    save() {
      if (this.saving || !this.validation.ok) return;
      this.saving = true;
      this.api("/config", { method: "POST", body: { text: this.configText } }).then((result) => {
        this.saving = false;
        const warnings = result && Array.isArray(result.warnings) ? result.warnings : [];
        let message = "已保存（" + (result && result.servers || 0) + " 个 server），点「重载插件」后生效。";
        if (warnings.length) message += " 警告：" + warnings.join("；");
        this.showToast(message, warnings.length ? "error" : "info");
        this.load();
      }).catch((err) => {
        this.saving = false;
        this.showToast("保存失败：" + errorText(err), "error");
      });
    }
  },
  render(h2) {
    const self = this;
    const tip = (text2) => /* @__PURE__ */ h2("span", { class: "mc-tip mc-tip--below", "data-tip": text2, "aria-label": text2, tabindex: "0", role: "img" }, "!");
    const chip = (text2, tone) => /* @__PURE__ */ h2("span", { class: "mc-chip" + (tone ? " mc-chip--" + tone : "") }, text2);
    const renderTools = (server) => {
      const items = Array.isArray(server.items) ? server.items : [];
      if (!items.length) return null;
      const expanded = self.isExpanded(server);
      const visible = expanded ? items : items.slice(0, 4);
      return /* @__PURE__ */ h2("div", { class: "mc-tools" }, /* @__PURE__ */ h2("div", { class: "mc-tools__head" }, /* @__PURE__ */ h2("span", { class: "mc-tools__title" }, "工具 ", /* @__PURE__ */ h2("span", { class: "mc-count" }, items.length)), /* @__PURE__ */ h2("button", { class: "mc-link", type: "button", onClick: () => self.toggleTools(server) }, expanded ? "收起" : "展开全部 " + items.length + " 个")), /* @__PURE__ */ h2("div", { class: "mc-tools__grid mc-tools__grid--head" }, /* @__PURE__ */ h2("span", null, "工具名"), /* @__PURE__ */ h2("span", null, "访问"), /* @__PURE__ */ h2("span", null, "备注")), visible.map((item) => /* @__PURE__ */ h2("div", { class: "mc-tools__grid", key: server.id + ":" + item.name }, /* @__PURE__ */ h2("code", { class: "mc-tools__name" }, item.name), /* @__PURE__ */ h2("span", null, /* @__PURE__ */ h2("span", { class: "mc-chip " + (item.access === "read" ? "mc-chip--ok" : "mc-chip--write") }, accessLabel(item.access))), /* @__PURE__ */ h2("span", { class: "mc-tools__note" }, item.schemaSimplified ? "Schema 已简化" : item.mcpName && item.mcpName !== item.name ? "MCP " + item.mcpName : "—"))));
    };
    const renderCard = (server) => {
      const status = String(server.status || "degraded");
      const items = Array.isArray(server.items) ? server.items : [];
      return /* @__PURE__ */ h2("section", { class: "mc-card", "data-status": status, key: server.id }, /* @__PURE__ */ h2("div", { class: "mc-card__head" }, /* @__PURE__ */ h2("div", { class: "mc-card__title" }, /* @__PURE__ */ h2("span", { class: "mc-dot", "data-status": status }), /* @__PURE__ */ h2("code", { class: "mc-card__name" }, server.id), chip(server.transport || "http"), chip(statusLabel(status), status === "ready" ? "ok" : status === "degraded" ? "danger" : "")), /* @__PURE__ */ h2("div", { class: "mc-card__right" }, items.length || server.tools ? /* @__PURE__ */ h2("span", { class: "mc-card__count" }, server.tools || items.length, " 个工具") : null, /* @__PURE__ */ h2(
        "button",
        {
          class: "mc-switch",
          type: "button",
          "data-on": server.enabled !== false ? "true" : "false",
          role: "switch",
          "aria-checked": server.enabled !== false ? "true" : "false",
          "aria-label": (server.enabled !== false ? "停用 " : "启用 ") + server.id,
          onClick: () => self.toggleServer(server, server.enabled === false)
        },
        /* @__PURE__ */ h2("span", { class: "mc-switch__knob" })
      ))), /* @__PURE__ */ h2("div", { class: "mc-card__cmd" }, /* @__PURE__ */ h2("code", null, server.url || ""), /* @__PURE__ */ h2("button", { class: "mc-link", type: "button", onClick: () => self.copyText(server.url || "", "命令") }, "复制")), server.error ? /* @__PURE__ */ h2("div", { class: "mc-error" }, /* @__PURE__ */ h2("span", null, "启动失败：", server.error), /* @__PURE__ */ h2("button", { class: "mc-link mc-link--danger", type: "button", onClick: () => self.probe() }, "重新探测")) : null, /* @__PURE__ */ h2("div", { class: "mc-card__meta" }, "来源 ", /* @__PURE__ */ h2("code", null, server.source || self.configPath), server.toolPrefix ? /* @__PURE__ */ h2("span", null, " · 前缀 ", /* @__PURE__ */ h2("code", null, server.toolPrefix)) : null), renderTools(server));
    };
    return /* @__PURE__ */ h2("div", { class: "mc-page", "data-mcp-page": true }, /* @__PURE__ */ h2("div", { class: "mc-head" }, /* @__PURE__ */ h2("div", { class: "mc-title-row" }, /* @__PURE__ */ h2("h1", { class: "mc-title" }, "MCP 服务端"), tip("管理接入本工作区的 MCP server。工作区 .adago/mcp.json 可编辑，用户级 ~/.adago/mcp.json 只读；同名 server 由工作区覆盖。保存或启停后需重载插件，工具列表才会更新。")), /* @__PURE__ */ h2("div", { class: "mc-actions" }, /* @__PURE__ */ h2("button", { class: "mc-btn", type: "button", onClick: this.refresh }, "刷新状态"), /* @__PURE__ */ h2("button", { class: "mc-btn", type: "button", disabled: this.reloading, onClick: this.reloadPlugins }, this.reloading ? "重载中…" : "重载插件"), /* @__PURE__ */ h2("button", { class: "mc-btn mc-btn--primary", type: "button", onClick: this.openModal }, "编辑配置"))), /* @__PURE__ */ h2("div", { class: "mc-summary" }, /* @__PURE__ */ h2("span", null, "共 ", this.counts.all, " 个 server"), /* @__PURE__ */ h2("div", { class: "mc-seg", role: "group", "aria-label": "按状态筛选" }, this.filters.map((item) => /* @__PURE__ */ h2(
      "button",
      {
        key: item.value,
        type: "button",
        class: "mc-seg__item" + (this.filter === item.value ? " is-active" : ""),
        "aria-pressed": this.filter === item.value ? "true" : "false",
        onClick: () => this.setFilter(item.value)
      },
      item.label,
      " ",
      item.count
    ))), /* @__PURE__ */ h2("span", { class: "mc-summary__time" }, "最后刷新 ", this.lastRefresh || "—")), this.error ? /* @__PURE__ */ h2("div", { class: "mc-load-error" }, /* @__PURE__ */ h2("span", null, "读取 server 状态失败：", this.error), /* @__PURE__ */ h2("button", { class: "mc-link", type: "button", onClick: this.load }, "重试")) : null, this.loading && !this.state ? /* @__PURE__ */ h2("p", { class: "mc-loading" }, "正在读取 server 状态…") : this.filteredServers.length ? /* @__PURE__ */ h2("section", { class: "mc-list", "aria-label": "MCP server 列表", "data-mcp-servers": true }, this.filteredServers.map(renderCard)) : this.filter !== "all" ? /* @__PURE__ */ h2("div", { class: "mc-empty" }, /* @__PURE__ */ h2("p", { class: "mc-empty__title" }, "这个状态下没有 server"), /* @__PURE__ */ h2("button", { class: "mc-btn", type: "button", onClick: () => this.setFilter("all") }, "查看全部")) : /* @__PURE__ */ h2("div", { class: "mc-empty", "data-mcp-servers": true }, /* @__PURE__ */ h2("p", { class: "mc-empty__title" }, "还没有 MCP server"), /* @__PURE__ */ h2("p", { class: "mc-empty__desc" }, "在 ", this.configPath, " 里添加 server，保存后重载插件即可接入。"), /* @__PURE__ */ h2("button", { class: "mc-btn", type: "button", onClick: this.openModal }, "编辑配置")), this.modalOpen ? /* @__PURE__ */ h2("div", { class: "mc-modal", role: "dialog", "aria-modal": "true", "aria-label": "编辑配置", "data-mcp-modal": true }, /* @__PURE__ */ h2("div", { class: "mc-modal__backdrop", onClick: this.closeModal }), /* @__PURE__ */ h2("div", { class: "mc-modal__panel" }, /* @__PURE__ */ h2("div", { class: "mc-modal__head" }, /* @__PURE__ */ h2("div", { class: "mc-modal__title" }, "编辑配置", /* @__PURE__ */ h2("code", { class: "mc-modal__tag" }, this.configPath)), /* @__PURE__ */ h2("button", { class: "mc-modal__close", type: "button", "aria-label": "关闭", onClick: this.closeModal }, "×")), /* @__PURE__ */ h2("div", { class: "mc-modal__body" }, /* @__PURE__ */ h2("p", { class: "mc-modal__note" }, "用户级 ", this.userConfigPath, " 只读；同名 server 由工作区配置覆盖。"), /* @__PURE__ */ h2("div", { class: "mc-editor" }, /* @__PURE__ */ h2("pre", { class: "mc-editor__hl", "data-mcp-hl": true, "aria-hidden": "true" }, /* @__PURE__ */ h2("code", null, this.tokens.map((token, index) => token.cls ? /* @__PURE__ */ h2("span", { class: token.cls, key: index }, token.text) : token.text), "\n")), /* @__PURE__ */ h2(
      "textarea",
      {
        class: "mc-editor__input",
        "data-mcp-editor": true,
        value: this.configText,
        rows: "13",
        "aria-label": "MCP 配置文件内容",
        onInput: this.onEditorInput,
        onScroll: this.syncScroll
      }
    )), /* @__PURE__ */ h2("div", { class: "mc-editor__status", "data-tone": this.validation.ok ? "ok" : "error", role: "status", "aria-live": "polite", "data-mcp-config-status": true }, /* @__PURE__ */ h2("span", { class: "mc-editor__state" }, /* @__PURE__ */ h2("span", { class: "mc-status-dot" }), this.validation.ok ? "JSON 有效 · " + this.validation.servers + " 个 server" : "JSON 无效：" + this.validation.message), /* @__PURE__ */ h2("span", null, "共 ", this.validation.lines, " 行"))), /* @__PURE__ */ h2("div", { class: "mc-modal__foot" }, /* @__PURE__ */ h2("p", { class: "mc-modal__hint" }, "密钥用 ", /* @__PURE__ */ h2("code", null, "${secret:mcp_token}"), " 引用，不要写明文"), /* @__PURE__ */ h2("div", { class: "mc-modal__actions" }, /* @__PURE__ */ h2("button", { class: "mc-btn", type: "button", onClick: this.loadConfig }, "重新读取"), /* @__PURE__ */ h2("button", { class: "mc-btn", type: "button", "data-mcp-format": true, onClick: this.format }, "格式化"), /* @__PURE__ */ h2(
      "button",
      {
        class: "mc-btn mc-btn--primary",
        type: "button",
        "data-mcp-save": true,
        disabled: !this.validation.ok || this.saving,
        onClick: this.save
      },
      this.saving ? "保存中…" : "保存"
    ))))) : null, this.toast ? /* @__PURE__ */ h2("div", { class: "mc-toast", key: this.toast.id, "data-tone": this.toast.tone, role: "status", "aria-live": "polite" }, this.toast.message) : null);
  }
});

// client.tsx
var islands = {
  "page:servers": page_default
};
var client_default = page_default;
export {
  client_default as default,
  islands
};
