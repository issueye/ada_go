/**
 * 环境变量管理页（official/env-manager）：/ext/env-manager/settings
 *
 * 结构：页面头部（标题 / 说明图标 / 刷新 / 重载插件）→ 环境变量卡片
 * （范围筛选 + 一行式添加表单 + 变量行列表 + 底注）→ 服务程序目录卡片
 * （两个目录块 + 程序清单）。
 *
 * 约定：
 *   - 说明性文案一律走「!」图标 + hover tooltip（.em-tip[data-tip]），正文只留必要规则；
 *   - 一次性反馈走底部 toast（方法名 showToast，别和 data 的 toast 字段同名）；
 *   - 值默认打码，逐行「显示 / 隐藏」，复制取原文；
 *   - 图标不用内联 svg（SSR 的 VNode 白名单不含 svg），全部文字 + CSS。
 */
import { defineComponent } from "ada:render";

const KEY_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

const SCOPES = [
  { value: "workspace", label: "工作区" },
  { value: "global", label: "全局" },
];

function scopeLabel(scope: string) {
  return scope === "global" ? "全局" : "工作区";
}

function varsOf(layer: any) {
  return layer && layer.vars && typeof layer.vars === "object" ? layer.vars : {};
}

function formatBytes(size: any) {
  const bytes = Number(size);
  if (!isFinite(bytes) || bytes <= 0) return "—";
  if (bytes >= 1024 * 1024) return Math.round((bytes / (1024 * 1024)) * 10) / 10 + " MB";
  if (bytes >= 1024) return Math.round(bytes / 1024) + " KB";
  return bytes + " B";
}

function formatTime(value: any) {
  const text = String(value || "");
  if (!text) return "—";
  const d = new Date(text);
  if (isNaN(d.getTime())) return text;
  const pad = (n: number) => (n < 10 ? "0" + n : String(n));
  return (
    d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
    " " + pad(d.getHours()) + ":" + pad(d.getMinutes())
  );
}

function maskValue(value: any) {
  const text = String(value === undefined || value === null ? "" : value);
  if (!text) return "";
  const tail = text.length > 4 ? text.slice(-4) : "";
  return "••••••••••" + tail;
}

export default defineComponent({
  props: {
    data: { default: {} },
  },
  data() {
    const data = (this as any).data || {};
    const backend = data.backend || {};
    const mine = backend["env-manager"] || {};
    // SSR 预取按「绑定名」落在 data.backend.<插件>.<绑定名>。
    const state = mine.page_settings || mine.state || null;
    return {
      loading: !state,
      error: "",
      state,
      toast: null as any,
      toastSeq: 0,
      toastTimer: 0,
      filter: "all",
      form: { scope: "workspace", key: "", value: "" },
      revealed: {} as any,
      busy: false,
      reloading: false,
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
        global: this.state && this.state.global ? this.state.global.path || "" : "",
      };
    },
    /// 工作区优先展示；同名全局项标记为「被覆盖」，避免看成一式两份。
    rows() {
      const workspace = this.workspaceVars;
      const global = this.globalVars;
      const out: any[] = [];
      Object.keys(workspace).sort().forEach((key) => {
        out.push({
          id: "workspace:" + key,
          scope: "workspace",
          key,
          value: workspace[key],
          overridden: Object.prototype.hasOwnProperty.call(global, key),
        });
      });
      Object.keys(global).sort().forEach((key) => {
        out.push({
          id: "global:" + key,
          scope: "global",
          key,
          value: global[key],
          overridden: false,
        });
      });
      return out;
    },
    filteredRows() {
      if (this.filter === "all") return this.rows;
      return this.rows.filter((row: any) => row.scope === this.filter);
    },
    counts() {
      return {
        all: this.rows.length,
        workspace: this.rows.filter((row: any) => row.scope === "workspace").length,
        global: this.rows.filter((row: any) => row.scope === "global").length,
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
    },
  },
  mounted() {
    if (this.loading) this.load();
  },
  methods: {
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
        this.error = "当前页面没有 adaAPI，无法读取环境变量。";
        return;
      }
      api("env-manager", "/state")
        .then((data: any) => {
          this.state = data || null;
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
    setScope(scope: string) {
      this.form.scope = scope;
    },
    setKey(value: string) {
      this.form.key = String(value || "");
    },
    setValue(value: string) {
      this.form.value = String(value || "");
    },
    isRevealed(row: any) {
      return !!this.revealed[row.id];
    },
    toggleReveal(row: any) {
      const next = Object.assign({}, this.revealed);
      if (next[row.id]) delete next[row.id];
      else next[row.id] = true;
      this.revealed = next;
    },
    displayValue(row: any) {
      if (!String(row.value === undefined || row.value === null ? "" : row.value)) return "";
      return this.isRevealed(row) ? String(row.value) : maskValue(row.value);
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
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法写入。", "error");
        return;
      }
      this.busy = true;
      api("env-manager", "/set", {
        method: "POST",
        body: { scope: this.form.scope, key, value: String(this.form.value || "") },
      })
        .then(() => {
          this.busy = false;
          this.showToast("已写入 " + scopeLabel(this.form.scope) + " " + key + "，重载插件后生效。");
          this.form = { scope: this.form.scope, key: "", value: "" };
          this.load();
        })
        .catch((err: any) => {
          this.busy = false;
          this.showToast("写入失败：" + ((err && err.message) || String(err)), "error");
        });
    },
    removeVar(row: any) {
      const api = (window as any).adaAPI;
      if (typeof api !== "function") {
        this.showToast("当前页面没有 adaAPI，无法删除。", "error");
        return;
      }
      const run = () => {
        api("env-manager", "/remove", {
          method: "POST",
          body: { scope: row.scope, key: row.key },
        })
          .then(() => {
            this.showToast("已删除 " + row.key + "。");
            this.load();
          })
          .catch((err: any) => {
            this.showToast("删除失败：" + ((err && err.message) || String(err)), "error");
          });
      };
      const confirm = (window as any).adaConfirm;
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
      const headers: any = { Accept: "text/html" };
      if (meta && meta.getAttribute("content")) headers["X-CSRF-Token"] = meta.getAttribute("content");
      fetch("/agent-plugins/reload", { method: "POST", credentials: "same-origin", headers })
        .then((response: any) => {
          if (!response.ok) throw new Error("HTTP " + response.status);
          this.reloading = false;
          this.showToast("插件已重载，新子进程会拿到新环境。");
        })
        .catch((err: any) => {
          this.reloading = false;
          this.showToast("重载失败：" + ((err && err.message) || String(err)), "error");
        });
    },
  },
  render(h) {
    const self = this;
    const tip = (text: string, placement?: string) => (
      <span
        class={"em-tip" + (placement === "below" ? " em-tip--below" : "")}
        data-tip={text}
        aria-label={text}
        tabindex="0"
        role="img"
      >!</span>
    );

    return (
      <div class="em-page" data-env-page>
        <div class="em-head">
          <div class="em-title-row">
            <h1 class="em-title">环境变量</h1>
            {tip(
              "注入插件启动的子进程（ctx.exec、后台任务、stdio MCP）。全局 ~/.adago/env.json 与工作区 .adago/env.json 两层，同名时工作区覆盖全局；改完需重载插件。",
              "below",
            )}
          </div>
          <div class="em-actions">
            <button class="em-btn" type="button" onClick={this.load}>刷新</button>
            <button class="em-btn" type="button" disabled={this.reloading} onClick={this.reloadPlugins}>
              {this.reloading ? "重载中…" : "重载插件"}
            </button>
          </div>
        </div>

        <section class="em-card" aria-label="环境变量">
          <div class="em-card__head">
            <div class="em-card__title">
              <span>环境变量</span>
              <span class="em-count">{this.counts.all}</span>
            </div>
            <div class="em-seg em-seg--sm" role="group" aria-label="按范围筛选">
              <button
                type="button"
                class={"em-seg__item" + (this.filter === "all" ? " is-active" : "")}
                aria-pressed={this.filter === "all" ? "true" : "false"}
                onClick={() => this.setFilter("all")}
              >全部</button>
              <button
                type="button"
                class={"em-seg__item" + (this.filter === "workspace" ? " is-active" : "")}
                aria-pressed={this.filter === "workspace" ? "true" : "false"}
                onClick={() => this.setFilter("workspace")}
              >工作区</button>
              <button
                type="button"
                class={"em-seg__item" + (this.filter === "global" ? " is-active" : "")}
                aria-pressed={this.filter === "global" ? "true" : "false"}
                onClick={() => this.setFilter("global")}
              >全局</button>
            </div>
          </div>

          <form
            class="em-form"
            onSubmit={(event: any) => { if (event) event.preventDefault(); this.submit(); }}
          >
            <div class="em-seg" role="group" aria-label="范围">
              {SCOPES.map((scope) => (
                <button
                  key={scope.value}
                  type="button"
                  class={"em-seg__item" + (this.form.scope === scope.value ? " is-active" : "")}
                  aria-pressed={this.form.scope === scope.value ? "true" : "false"}
                  onClick={() => this.setScope(scope.value)}
                >{scope.label}</button>
              ))}
            </div>
            <input
              class="em-input em-mono"
              type="text"
              value={this.form.key}
              placeholder="变量名，如 GITHUB_TOKEN"
              aria-label="变量名"
              aria-invalid={this.keyError ? "true" : "false"}
              onInput={(event: any) => this.setKey(event.target.value)}
            />
            {/* 值输入框和变量名用同一个 .em-input：此前外面套了一层 div，
                内层 input 把 outline 关掉，导致这一格点进去没有任何聚焦反馈。 */}
            <input
              class="em-input em-mono"
              type="text"
              value={this.form.value}
              placeholder="值（可为空）"
              aria-label="值"
              onInput={(event: any) => this.setValue(event.target.value)}
            />
            <button
              class="em-btn em-btn--primary"
              type="submit"
              disabled={this.busy || !!this.keyError}
            >添加 / 更新</button>
          </form>
          {this.keyError ? <p class="em-form__error" role="alert">{this.keyError}</p> : null}

          <div class="em-list">
            {this.error ? (
              <p class="em-error" role="alert">{this.error}</p>
            ) : this.loading ? (
              <p class="em-empty">正在读取环境变量…</p>
            ) : this.filteredRows.length ? (
              this.filteredRows.map((row: any) => (
                <div class="em-row" key={row.id}>
                  <code class={"em-row__key" + (row.overridden ? " is-overridden" : "")}>{row.key}</code>
                  <span class={"em-row__value em-mono" + (this.isRevealed(row) || !row.value ? " is-plain" : "")}>
                    {row.value ? this.displayValue(row) : "（空值）"}
                  </span>
                  <span class={"em-chip em-chip--" + row.scope}>{scopeLabel(row.scope)}</span>
                  {row.overridden ? (
                    <span class="em-chip em-chip--warn" title="同名全局变量被工作区覆盖">覆盖全局</span>
                  ) : null}
                  <span class="em-row__actions">
                    {row.value ? (
                      <button class="em-link" type="button" onClick={() => this.toggleReveal(row)}>
                        {this.isRevealed(row) ? "隐藏" : "显示"}
                      </button>
                    ) : null}
                    <button class="em-link" type="button" onClick={() => this.copyText(row.value, "变量值")}>复制</button>
                    <button class="em-link em-link--danger" type="button" onClick={() => this.removeVar(row)}>删除</button>
                  </span>
                </div>
              ))
            ) : this.counts.all ? (
              <p class="em-empty">当前筛选下没有变量。</p>
            ) : (
              <div class="em-empty em-empty--block">
                <p class="em-empty__title">还没有环境变量</p>
                <p class="em-empty__desc">在上面添加，或直接用命令行写入：</p>
                <div class="em-cmd">
                  <code>adag env set GITHUB_TOKEN=***</code>
                  <button
                    class="em-link"
                    type="button"
                    onClick={() => this.copyText("adag env set GITHUB_TOKEN=", "命令")}
                  >复制</button>
                </div>
              </div>
            )}
          </div>

          {/* 提示放到列表之后：这些是「读写落点 / 旧目录兼容」的说明，
              之前放在表单和列表中间、又用整条告警边框，看起来像页面报错。 */}
          {this.warnings.length ? (
            <ul class="em-notes">
              {this.warnings.map((item: string, index: number) => (
                <li key={index}>{item}</li>
              ))}
            </ul>
          ) : null}

          {/* 写在一行里：JSX 的换行会被折叠成一个空格，中文里会多出 "匹配 ^[" 这样的空隙。 */}
          <p class="em-foot">值默认打码，仅在本页可见；命名需匹配 <code>^[A-Za-z_][A-Za-z0-9_]*$</code>，<code>ADAG_</code> 前缀保留。</p>
        </section>

        <section class="em-card" aria-label="服务程序目录">
          <div class="em-card__head">
            <div class="em-card__title">
              <span>服务程序目录</span>
              {tip("把可执行文件放进这些目录，插件子进程的 PATH 会自动前置它们（工作区优先）；新启动的子进程生效，不用写绝对路径。", "below")}
            </div>
            <span class="em-card__hint">放进目录的可执行文件会自动加入插件子进程 PATH</span>
          </div>

          <div class="em-bins">
            {this.binPaths.map((item: any) => (
              <div class="em-bin" key={item.scope}>
                <div class="em-bin__top">
                  <span class={"em-chip em-chip--" + item.scope}>{scopeLabel(item.scope)}</span>
                  <span class={"em-status" + (item.exists ? "" : " is-missing")}>
                    {item.exists ? "已创建" : "未创建"}
                  </span>
                </div>
                <code class="em-bin__path">{item.path}</code>
                <button
                  class="em-link em-bin__copy"
                  type="button"
                  onClick={() => this.copyText(item.path, "目录路径")}
                >复制路径</button>
              </div>
            ))}
            {this.binPaths.length ? null : <p class="em-empty">还没有可用的服务程序目录。</p>}
          </div>

          <div class="em-prog-head">
            <span class="em-prog-head__name">程序</span>
            <span class="em-prog-head__size">大小</span>
            <span class="em-prog-head__time">修改时间</span>
          </div>
          <div class="em-progs">
            {this.binPrograms.length ? (
              this.binPrograms.map((item: any) => (
                <div class="em-prog" key={item.scope + ":" + item.name}>
                  {/* 范围徽章放进「程序」这一格：单独占一列时表头对不上，
                      大小 / 修改时间会整体右移一个徽章的宽度。 */}
                  <span class="em-prog__cell">
                    <code class="em-prog__name">{item.name}</code>
                    <span class={"em-chip em-chip--" + item.scope}>{scopeLabel(item.scope)}</span>
                  </span>
                  <span class="em-prog__size">{formatBytes(item.size)}</span>
                  <span class="em-prog__time">{formatTime(item.modified)}</span>
                </div>
              ))
            ) : (
              <p class="em-empty">目录里还没有可执行文件，把程序放进去即可。</p>
            )}
          </div>
        </section>

        {this.toast ? (
          <div
            class="em-toast"
            key={this.toast.id}
            data-tone={this.toast.tone}
            role="status"
            aria-live="polite"
          >{this.toast.message}</div>
        ) : null}
      </div>
    );
  },
});
