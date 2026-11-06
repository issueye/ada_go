/**
 * MCP 服务端管理页（official/mcp-client）：/ext/mcp-client/servers
 *
 * 与 provider-admin / env-manager 一样是 SSR + hydrate 的岛屿组件：
 *   - SSR 预取：props.data.backend["mcp-client"].page_servers（绑定名 page_servers，
 *     不是 action 名 state）；取不到时 mounted() 再走 /servers 补齐；
 *   - 说明性文案走 ⓘ + tooltip，一次性反馈走底部 toast（方法名 showToast，
 *     data 里的字段叫 toast，避免 ada-render 单 Proxy 里 data 遮蔽 methods）；
 *   - 不用内联 svg（SSR VNode 白名单不含 svg），状态灯/开关都是文字 + CSS。
 *
 * 数据面仍是本插件的 route：GET /servers、GET|POST /config、POST /servers/toggle。
 * 配置原文只在用户点开弹窗时读取；地址与命令行在服务端已脱敏。
 */
import { defineComponent } from "ada:render";

const CONFIG_PATH = ".adago/mcp.json";
const USER_CONFIG_PATH = "~/.adago/mcp.json";
const MAX_HIGHLIGHT = 64 * 1024;

function statusLabel(status: string) {
  if (status === "ready") return "已连接";
  if (status === "disabled") return "已停用";
  return "连接失败";
}

function accessLabel(access: string) {
  return access === "read" ? "只读" : "写入";
}

function errorText(err: any) {
  const message = err && err.message ? String(err.message) : String(err);
  const cut = message.indexOf("失败: ");
  return (cut >= 0 ? message.slice(cut + "失败: ".length) : message) || "操作失败";
}

function clockText() {
  const d = new Date();
  const pad = (n: number) => (n < 10 ? "0" + String(n) : String(n));
  return pad(d.getHours()) + ":" + pad(d.getMinutes());
}

/** describeJSONError 把 JSON.parse 的错误换成中文行/列，方便定位。
 *  V8 有两种文案：`... at position 15 (line 1 column 16)` 与
 *  `Unexpected token '}', ..."servers":[}" is not valid JSON`（带代码帧、无位置），
 *  后者从代码帧反查出错字符所在位置。 */
function describeJSONError(err: any, text: string) {
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
    // 代码帧形如 ..."servers":[}" ，末尾那个字符就是 V8 报的 token。
    const frame = message.indexOf('..."');
    if (frame >= 0) {
      // 跳过 `..."`，其余到下一个引号为止就是 V8 给的代码帧（含开头引号）。
      const rest = message.slice(frame + 4);
      const end = rest.indexOf('"');
      if (end > 0) {
        const snippet = '"' + rest.slice(0, end);
        const found = text.lastIndexOf(snippet);
        if (found >= 0) pos = found + snippet.length - 1;
      }
    }
  }
  if (pos < 0) return message;
  const before = text.slice(0, pos);
  const line = before.split("\n").length;
  const col = pos - before.lastIndexOf("\n");
  return "第 " + line + " 行第 " + col + " 列 · " + message;
}

/** parseConfig 同时做语法与结构校验，返回编辑器状态条所需的一切。 */
function parseConfig(text: string) {
  const lines = text ? text.split("\n").length : 0;
  if (!text || !text.trim()) {
    return { ok: false, message: "配置为空", servers: 0, lines: lines };
  }
  try {
    const value = JSON.parse(text);
    if (!value || typeof value !== "object" || !Array.isArray(value.servers)) {
      return { ok: false, message: '需要 {"version":1,"servers":[…]} 结构', servers: 0, lines: lines };
    }
    return { ok: true, message: "JSON 有效", servers: value.servers.length, lines: lines };
  } catch (err) {
    return { ok: false, message: describeJSONError(err, text), servers: 0, lines: lines };
  }
}

function prettyJSON(text: string) {
  const parsed = parseConfig(text);
  if (!parsed.ok) return { ok: false, text: text, message: parsed.message };
  try {
    return { ok: true, text: JSON.stringify(JSON.parse(text), null, 2) + "\n", message: "" };
  } catch (err) {
    return { ok: false, text: text, message: describeJSONError(err, text) };
  }
}

/**
 * highlightTokens 是给「透明 textarea + 背后高亮层」用的分词器。
 * 刻意用手写扫描而不是正则 exec：SSR 的 TSX 运行时与浏览器 bundle 两边都要能跑，
 * 且输出是纯数据（由 JSX 生成 span），永远不会把配置内容当 HTML 注入。
 */
function highlightTokens(text: string) {
  const out: any[] = [];
  if (text.length > MAX_HIGHLIGHT) return [{ text: text, cls: "" }];
  let plain = "";
  const flush = () => {
    if (plain) {
      out.push({ text: plain, cls: "" });
      plain = "";
    }
  };
  const push = (value: string, cls: string) => {
    out.push({ text: value, cls: cls });
  };
  const isDigit = (c: string) => c >= "0" && c <= "9";
  const isWord = (c: string) => isDigit(c) || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || c === "_";
  const matchAt = (word: string, at: number) => {
    for (let k = 0; k < word.length; k++) {
      if (text.charAt(at + k) !== word.charAt(k)) return false;
    }
    return true;
  };
  let i = 0;
  while (i < text.length) {
    const ch = text.charAt(i);
    if (ch === '"') {
      let j = i + 1;
      while (j < text.length) {
        const c = text.charAt(j);
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
      while (k < text.length && (text.charAt(k) === " " || text.charAt(k) === "\t" || text.charAt(k) === "\n" || text.charAt(k) === "\r")) k++;
      flush();
      push(text.slice(i, j), text.charAt(k) === ":" ? "mc-tok-key" : "mc-tok-str");
      i = j;
      continue;
    }
    if (isDigit(ch) || (ch === "-" && isDigit(text.charAt(i + 1)))) {
      let j = i + (ch === "-" ? 1 : 0);
      while (j < text.length) {
        const c = text.charAt(j);
        if (isDigit(c) || c === "." || c === "e" || c === "E" || c === "+" || c === "-") j++;
        else break;
      }
      const token = text.slice(i, j);
      if (token.length && !isWord(text.charAt(j))) {
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
    if (literal && !isWord(text.charAt(i + literal.length))) {
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

export default defineComponent({
  props: {
    data: { default: {} },
  },
  data() {
    const data = (this as any).data || {};
    const backend = data.backend || {};
    const mine = backend["mcp-client"] || {};
    // SSR 预取按「绑定名」落在 data.backend.<插件>.<绑定名>。
    const state = mine.page_servers || mine.state || null;
    return {
      state: state,
      loading: !state,
      error: "",
      toast: null as any,
      toastSeq: 0,
      toastTimer: 0,
      filter: "all",
      lastRefresh: "",
      reloading: false,
      expanded: {} as any,
      modalOpen: false,
      configText: "",
      configLoaded: false,
      saving: false,
    };
  },
  computed: {
    servers(): any[] {
      return this.state && Array.isArray(this.state.servers) ? this.state.servers : [];
    },
    counts() {
      const all = this.servers;
      return {
        all: all.length,
        ready: all.filter((s: any) => s.status === "ready").length,
        degraded: all.filter((s: any) => s.status === "degraded").length,
        disabled: all.filter((s: any) => s.status === "disabled").length,
      };
    },
    filters(): any[] {
      const out: any[] = [
        { value: "all", label: "全部", count: this.counts.all },
        { value: "ready", label: "已连接", count: this.counts.ready },
      ];
      // 有异常才出现这一档，避免常态下多一个永远为 0 的分段。
      if (this.counts.degraded) out.push({ value: "degraded", label: "异常", count: this.counts.degraded });
      out.push({ value: "disabled", label: "已停用", count: this.counts.disabled });
      return out;
    },
    filteredServers(): any[] {
      if (this.filter === "all") return this.servers;
      return this.servers.filter((s: any) => s.status === this.filter);
    },
    configPath() {
      return (this.state && this.state.configPath) || CONFIG_PATH;
    },
    userConfigPath() {
      return (this.state && this.state.userConfigPath) || USER_CONFIG_PATH;
    },
    validation() {
      return parseConfig(this.configText);
    },
    tokens() {
      return highlightTokens(this.configText);
    },
  },
  mounted() {
    if (this.loading) this.load();
  },
  methods: {
    showToast(message: string, tone?: string) {
      const seq = (this.toastSeq || 0) + 1;
      this.toastSeq = seq;
      this.toast = { id: seq, message: message, tone: tone || "info" };
      if (this.toastTimer) clearTimeout(this.toastTimer);
      this.toastTimer = setTimeout(() => {
        if (this.toast && this.toast.id === seq) this.toast = null;
      }, tone === "error" ? 4200 : 2400);
    },
    api(path: string, options?: any) {
      const fn = (window as any).adaAPI;
      if (typeof fn !== "function") throw new Error("页面缺少 adaAPI，无法调用插件接口");
      return fn("mcp-client", path, options || {});
    },
    load() {
      this.loading = true;
      this.error = "";
      this.api("/servers")
        .then((data: any) => {
          this.state = data || null;
          this.loading = false;
          this.lastRefresh = clockText();
        })
        .catch((err: any) => {
          this.loading = false;
          this.error = errorText(err);
          this.showToast("读取 server 状态失败：" + this.error, "error");
        });
    },
    refresh() {
      this.load();
      this.showToast("已刷新 server 状态。");
    },
    setFilter(value: string) {
      this.filter = value;
    },
    toggleTools(server: any) {
      const next = Object.assign({}, this.expanded);
      if (next[server.id]) delete next[server.id];
      else next[server.id] = true;
      this.expanded = next;
    },
    isExpanded(server: any) {
      return !!this.expanded[server.id];
    },
    copyText(text: string, label: string) {
      const value = String(text === undefined || text === null ? "" : text);
      const nav: any = typeof navigator === "undefined" ? null : navigator;
      if (nav && nav.clipboard && typeof nav.clipboard.writeText === "function") {
        nav.clipboard.writeText(value)
          .then(() => { this.showToast("已复制" + label + "。"); })
          .catch(() => { this.showToast("复制失败，请手动选择复制。", "error"); });
        return;
      }
      this.showToast("当前环境不支持一键复制。", "error");
    },
    toggleServer(server: any, wanted: boolean) {
      if (this.reloading) return;
      this.api("/servers/toggle", { method: "POST", body: { id: server.id, enabled: wanted } })
        .then(() => {
          this.showToast("已保存：" + server.id + (wanted ? " 已启用" : " 已停用") + "，重载插件后生效。");
          this.load();
        })
        .catch((err: any) => {
          this.showToast("启停失败：" + errorText(err), "error");
          this.load();
        });
    },
    reloadPlugins() {
      if (this.reloading) return;
      this.reloading = true;
      const meta = typeof document === "undefined" ? null : document.querySelector('meta[name="csrf-token"]');
      const headers: any = { Accept: "text/html" };
      if (meta && meta.getAttribute("content")) headers["X-CSRF-Token"] = meta.getAttribute("content");
      fetch("/agent-plugins/reload", { method: "POST", credentials: "same-origin", headers: headers })
        .then((response: any) => {
          if (!response.ok) throw new Error("HTTP " + response.status);
          this.reloading = false;
          this.showToast("插件已重载，正在刷新状态…");
          this.load();
        })
        .catch((err: any) => {
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
      this.api("/config")
        .then((config: any) => {
          const text = (config && config.text) || "";
          const pretty = text.trim() ? prettyJSON(text) : { ok: true, text: text };
          this.configText = pretty.ok ? pretty.text : text;
          this.configLoaded = true;
        })
        .catch((err: any) => {
          this.showToast("读取配置失败：" + errorText(err), "error");
        });
    },
    onEditorInput(event: any) {
      this.configText = event && event.target ? event.target.value : "";
    },
    /** 高亮层与 textarea 的滚动位置必须一致，否则长配置会错行。 */
    syncScroll(event: any) {
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
      this.api("/config", { method: "POST", body: { text: this.configText } })
        .then((result: any) => {
          this.saving = false;
          const warnings = result && Array.isArray(result.warnings) ? result.warnings : [];
          let message = "已保存（" + ((result && result.servers) || 0) + " 个 server），点「重载插件」后生效。";
          if (warnings.length) message += " 警告：" + warnings.join("；");
          this.showToast(message, warnings.length ? "error" : "info");
          this.load();
        })
        .catch((err: any) => {
          this.saving = false;
          this.showToast("保存失败：" + errorText(err), "error");
        });
    },
  },
  render(h) {
    const self = this;
    const tip = (text: string) => (
      <span class="mc-tip mc-tip--below" data-tip={text} aria-label={text} tabindex="0" role="img">!</span>
    );
    const chip = (text: string, tone?: string) => (
      <span class={"mc-chip" + (tone ? " mc-chip--" + tone : "")}>{text}</span>
    );

    const renderTools = (server: any) => {
      const items: any[] = Array.isArray(server.items) ? server.items : [];
      if (!items.length) return null;
      const expanded = self.isExpanded(server);
      const visible = expanded ? items : items.slice(0, 4);
      return (
        <div class="mc-tools">
          <div class="mc-tools__head">
            <span class="mc-tools__title">工具 <span class="mc-count">{items.length}</span></span>
            <button class="mc-link" type="button" onClick={() => self.toggleTools(server)}>
              {expanded ? "收起" : "展开全部 " + items.length + " 个"}
            </button>
          </div>
          <div class="mc-tools__grid mc-tools__grid--head">
            <span>工具名</span><span>访问</span><span>备注</span>
          </div>
          {visible.map((item: any) => (
            <div class="mc-tools__grid" key={server.id + ":" + item.name}>
              <code class="mc-tools__name">{item.name}</code>
              <span>
                <span class={"mc-chip " + (item.access === "read" ? "mc-chip--ok" : "mc-chip--write")}>
                  {accessLabel(item.access)}
                </span>
              </span>
              <span class="mc-tools__note">
                {item.schemaSimplified
                  ? "Schema 已简化"
                  : item.mcpName && item.mcpName !== item.name
                    ? "MCP " + item.mcpName
                    : "—"}
              </span>
            </div>
          ))}
        </div>
      );
    };

    const renderCard = (server: any) => {
      const status = String(server.status || "degraded");
      const items: any[] = Array.isArray(server.items) ? server.items : [];
      return (
        <section class="mc-card" data-status={status} key={server.id}>
          <div class="mc-card__head">
            <div class="mc-card__title">
              <span class="mc-dot" data-status={status}></span>
              <code class="mc-card__name">{server.id}</code>
              {chip(server.transport || "http")}
              {chip(statusLabel(status), status === "ready" ? "ok" : status === "degraded" ? "danger" : "")}
            </div>
            <div class="mc-card__right">
              {items.length || server.tools ? (
                <span class="mc-card__count">{(server.tools || items.length)} 个工具</span>
              ) : null}
              <button
                class="mc-switch"
                type="button"
                data-on={server.enabled !== false ? "true" : "false"}
                role="switch"
                aria-checked={server.enabled !== false ? "true" : "false"}
                aria-label={(server.enabled !== false ? "停用 " : "启用 ") + server.id}
                onClick={() => self.toggleServer(server, server.enabled === false)}
              ><span class="mc-switch__knob"></span></button>
            </div>
          </div>
          <div class="mc-card__cmd">
            <code>{server.url || ""}</code>
            <button class="mc-link" type="button" onClick={() => self.copyText(server.url || "", "命令")}>复制</button>
          </div>
          {server.error ? (
            <div class="mc-error">
              <span>启动失败：{server.error}</span>
              <button class="mc-link mc-link--danger" type="button" onClick={() => self.probe()}>重新探测</button>
            </div>
          ) : null}
          <div class="mc-card__meta">
            来源 <code>{server.source || self.configPath}</code>
            {server.toolPrefix ? <span> · 前缀 <code>{server.toolPrefix}</code></span> : null}
          </div>
          {renderTools(server)}
        </section>
      );
    };

    return (
      <div class="mc-page" data-mcp-page>
        <div class="mc-head">
          <div class="mc-title-row">
            <h1 class="mc-title">MCP 服务端</h1>
            {tip("管理接入本工作区的 MCP server。工作区 .adago/mcp.json 可编辑，用户级 ~/.adago/mcp.json 只读；同名 server 由工作区覆盖。保存或启停后需重载插件，工具列表才会更新。")}
          </div>
          <div class="mc-actions">
            <button class="mc-btn" type="button" onClick={this.refresh}>刷新状态</button>
            <button class="mc-btn" type="button" disabled={this.reloading} onClick={this.reloadPlugins}>
              {this.reloading ? "重载中…" : "重载插件"}
            </button>
            <button class="mc-btn mc-btn--primary" type="button" onClick={this.openModal}>编辑配置</button>
          </div>
        </div>

        <div class="mc-summary">
          <span>共 {this.counts.all} 个 server</span>
          <div class="mc-seg" role="group" aria-label="按状态筛选">
            {this.filters.map((item: any) => (
              <button
                key={item.value}
                type="button"
                class={"mc-seg__item" + (this.filter === item.value ? " is-active" : "")}
                aria-pressed={this.filter === item.value ? "true" : "false"}
                onClick={() => this.setFilter(item.value)}
              >{item.label} {item.count}</button>
            ))}
          </div>
          <span class="mc-summary__time">最后刷新 {this.lastRefresh || "—"}</span>
        </div>

        {this.error ? (
          <div class="mc-load-error">
            <span>读取 server 状态失败：{this.error}</span>
            <button class="mc-link" type="button" onClick={this.load}>重试</button>
          </div>
        ) : null}

        {this.loading && !this.state ? (
          <p class="mc-loading">正在读取 server 状态…</p>
        ) : this.filteredServers.length ? (
          <section class="mc-list" aria-label="MCP server 列表" data-mcp-servers>
            {this.filteredServers.map(renderCard)}
          </section>
        ) : this.filter !== "all" ? (
          <div class="mc-empty">
            <p class="mc-empty__title">这个状态下没有 server</p>
            <button class="mc-btn" type="button" onClick={() => this.setFilter("all")}>查看全部</button>
          </div>
        ) : (
          <div class="mc-empty" data-mcp-servers>
            <p class="mc-empty__title">还没有 MCP server</p>
            <p class="mc-empty__desc">在 {this.configPath} 里添加 server，保存后重载插件即可接入。</p>
            <button class="mc-btn" type="button" onClick={this.openModal}>编辑配置</button>
          </div>
        )}

        {this.modalOpen ? (
          <div class="mc-modal" role="dialog" aria-modal="true" aria-label="编辑配置" data-mcp-modal>
            <div class="mc-modal__backdrop" onClick={this.closeModal}></div>
            <div class="mc-modal__panel">
              <div class="mc-modal__head">
                <div class="mc-modal__title">
                  编辑配置
                  <code class="mc-modal__tag">{this.configPath}</code>
                </div>
                <button class="mc-modal__close" type="button" aria-label="关闭" onClick={this.closeModal}>×</button>
              </div>
              <div class="mc-modal__body">
                <p class="mc-modal__note">用户级 {this.userConfigPath} 只读；同名 server 由工作区配置覆盖。</p>
                <div class="mc-editor">
                  <pre class="mc-editor__hl" data-mcp-hl aria-hidden="true"><code>{this.tokens.map((token: any, index: number) => (
                    token.cls ? <span class={token.cls} key={index}>{token.text}</span> : token.text
                  ))}{"\n"}</code></pre>
                  <textarea
                    class="mc-editor__input"
                    data-mcp-editor
                    value={this.configText}
                    rows="13"
                    aria-label="MCP 配置文件内容"
                    onInput={this.onEditorInput}
                    onScroll={this.syncScroll}
                  ></textarea>
                </div>
                <div class="mc-editor__status" data-tone={this.validation.ok ? "ok" : "error"} role="status" aria-live="polite" data-mcp-config-status>
                  <span class="mc-editor__state">
                    <span class="mc-status-dot"></span>
                    {this.validation.ok
                      ? "JSON 有效 · " + this.validation.servers + " 个 server"
                      : "JSON 无效：" + this.validation.message}
                  </span>
                  <span>共 {this.validation.lines} 行</span>
                </div>
              </div>
              <div class="mc-modal__foot">
                <p class="mc-modal__hint">密钥用 <code>{"${secret:mcp_token}"}</code> 引用，不要写明文</p>
                <div class="mc-modal__actions">
                  <button class="mc-btn" type="button" onClick={this.loadConfig}>重新读取</button>
                  <button class="mc-btn" type="button" data-mcp-format onClick={this.format}>格式化</button>
                  <button
                    class="mc-btn mc-btn--primary"
                    type="button"
                    data-mcp-save
                    disabled={!this.validation.ok || this.saving}
                    onClick={this.save}
                  >{this.saving ? "保存中…" : "保存"}</button>
                </div>
              </div>
            </div>
          </div>
        ) : null}

        {this.toast ? (
          <div class="mc-toast" key={this.toast.id} data-tone={this.toast.tone} role="status" aria-live="polite">
            {this.toast.message}
          </div>
        ) : null}
      </div>
    );
  },
});
