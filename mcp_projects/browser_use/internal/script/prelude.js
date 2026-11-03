/* browser-use script prelude. Runs in moejs before the user's script. */
(() => {
  "use strict";

  class BrowserError extends Error {
    constructor(message, code, hint) {
      super(String(message || "browser call failed"));
      this.name = "BrowserError";
      this.code = code || "failed";
      this.hint = hint || "";
    }
  }

  async function call(name, args) {
    const r = await globalThis.__bu_call(String(name), args || {});
    if (!r || r.ok !== true) {
      const e = (r && r.error) || {};
      throw new BrowserError(e.message, e.code, e.hint);
    }
    return r.data;
  }

  function target(t) {
    if (t && typeof t === "object" && !Array.isArray(t)) return t;
    const s = String(t == null ? "" : t).trim();
    if (/^(?:ref=|@)?e?\d+$/.test(s)) {
      return { ref: s.replace(/^(?:ref=|@)/, "") };
    }
    return { selector: s };
  }

  function waitOptions(o) {
    const out = { ...(o || {}) };
    if (out.timeout != null) {
      out.timeout_ms = out.timeout;
      delete out.timeout;
    }
    return out;
  }

  class Page {
    constructor(id) {
      this.id = String(id);
    }

    async goto(url, opts) { return call("navigate", { action: "goto", tab: this.id, url, ...(opts || {}) }); }
    async back(opts) { return call("navigate", { action: "back", tab: this.id, ...(opts || {}) }); }
    async forward(opts) { return call("navigate", { action: "forward", tab: this.id, ...(opts || {}) }); }
    async reload(opts) { return call("navigate", { action: "reload", tab: this.id, ...(opts || {}) }); }
    async stop() { return call("navigate", { action: "stop", tab: this.id }); }

    async snapshot(opts) {
      const d = await call("snapshot", { tab: this.id, ...(opts || {}) });
      return d.snapshot;
    }
    async snapshotRaw(opts) { return call("snapshot", { tab: this.id, ...(opts || {}) }); }
    async read(opts) {
      const d = await call("read", { tab: this.id, ...(opts || {}) });
      return d.content;
    }

    async click(t, opts) { return call("click", { tab: this.id, ...target(t), ...(opts || {}) }); }
    async dblclick(t, opts) { return call("click", { tab: this.id, ...target(t), count: 2, ...(opts || {}) }); }
    async fill(t, text, opts) { return call("type", { tab: this.id, ...target(t), text, clear: true, ...(opts || {}) }); }
    async type(t, text, opts) { return call("type", { tab: this.id, ...target(t), text, clear: false, ...(opts || {}) }); }
    async press(key, opts) {
      const o = opts || {};
      const at = o.ref || o.selector || o.text ? target(o.ref || o.selector || o.text) : {};
      const rest = { ...o };
      delete rest.ref;
      delete rest.selector;
      delete rest.text;
      return call("press", { tab: this.id, key, ...at, ...rest });
    }
    async select(t, values) {
      return call("select", {
        tab: this.id, ...target(t),
        values: Array.isArray(values) ? values : [String(values)],
      });
    }
    async hover(t) { return call("hover", { tab: this.id, ...target(t) }); }
    async scroll(o) {
      o = o || {};
      if (typeof o === "string" || (o && (o.ref || o.selector))) {
        return call("scroll", { tab: this.id, ...target(o) });
      }
      if (o.to) {
        const rest = { ...o };
        const to = rest.to;
        delete rest.to;
        return call("scroll", { tab: this.id, ...target(to), ...rest });
      }
      return call("scroll", { tab: this.id, ...o });
    }

    async waitForSelector(sel, opts) {
      return call("wait", { tab: this.id, for: "selector", value: String(sel), ...waitOptions(opts) });
    }
    async waitForURL(url, opts) {
      return call("wait", { tab: this.id, for: "url", value: String(url), ...waitOptions(opts) });
    }
    async waitForText(text, opts) {
      return call("wait", { tab: this.id, for: "text", value: String(text), ...waitOptions(opts) });
    }
    async waitForLoad(opts) {
      return call("wait", { tab: this.id, for: "load", ...waitOptions(opts) });
    }
    async waitForDownload(opts) {
      return call("wait", { tab: this.id, for: "download", ...waitOptions(opts) });
    }

    async evaluate(fn, arg) {
      const src = typeof fn === "function" ? fn.toString() : String(fn);
      const expr = typeof fn === "function"
        ? "(" + src + ")(" + JSON.stringify(arg === undefined ? null : arg) + ")"
        : src;
      const d = await call("eval", { tab: this.id, expression: expr });
      return d.result;
    }

    async screenshot(opts) { return call("screenshot", { tab: this.id, ...(opts || {}) }); }
    async dialogs() { return (await call("dialogs", { tab: this.id })).dialogs; }
    async cookies(opts) { return call("cookies", opts || {}); }
    async frames(opts) { return call("frames", { tab: this.id, ...(opts || {}) }); }
    async useFrame(id) { return call("frames", { action: "use", tab: this.id, frame: id }); }
    async mainFrame() { return call("frames", { action: "main", tab: this.id }); }
    async upload(t, paths) {
      return call("files", {
        action: "upload", tab: this.id, ...target(t),
        paths: Array.isArray(paths) ? paths : [paths],
      });
    }
    async requests(opts) { return call("network", { tab: this.id, ...(opts || {}) }); }
    async request(id, opts) { return call("network", { action: "get", tab: this.id, id, ...(opts || {}) }); }
    async clearRequests() { return call("network", { action: "clear", tab: this.id }); }
    async waitForRequest(opts) { return call("network", { action: "wait", tab: this.id, ...(opts || {}) }); }

    async info() {
      const d = await call("tabs", { action: "list" });
      const t = (d.tabs || []).find((x) => x.id === this.id);
      if (!t) throw new BrowserError("tab " + this.id + " is gone", "invalid_tab");
      return t;
    }
    async url() { return (await this.info()).url; }
    async title() { return (await this.info()).title; }
    async activate() { return call("tabs", { action: "select", tab: this.id }); }
    async close() { return call("tabs", { action: "close", tab: this.id, force: true }); }
  }

  class Browser {
    async tabs() { return (await call("tabs", { action: "list" })).tabs; }
    async active() {
      const d = await call("status", { include_tabs: false });
      if (!d.active_tab || !d.active_tab.id) {
        throw new BrowserError("no active tab", "no_tab");
      }
      return new Page(d.active_tab.id);
    }
    page(id) {
      if (!id) throw new BrowserError("page id is required", "bad_request");
      return new Page(String(id));
    }
    async newPage(url, opts) {
      const d = await call("tabs", { action: "new", url: url || "about:blank", ...(opts || {}) });
      return new Page(d.tab.id);
    }
    async release(opts) { return call("control", { action: "release", ...(opts || {}) }); }
    async status() { return call("status", { include_tabs: true }); }
    async downloads() { return call("downloads", {}); }
    async cookies(opts) { return call("cookies", opts || {}); }
    async call(name, args) { return call(name, args || {}); }
  }

  function fmt(v) {
    if (typeof v === "string") return v;
    if (v instanceof Error) return v.stack || (v.name + ": " + v.message);
    try {
      const s = JSON.stringify(v);
      return s === undefined ? String(v) : s;
    } catch (e) {
      return String(v);
    }
  }
  function log(level, args) {
    globalThis.__bu_log(String(level), args.map(fmt).join(" "));
  }

  globalThis.BrowserError = BrowserError;
  globalThis.browser = new Browser();
  globalThis.sleep = (ms) => new Promise((resolve) => setTimeout(resolve, Math.max(1, Number(ms) || 1)));
  globalThis.console = {
    log: (...a) => log("log", a),
    info: (...a) => log("info", a),
    warn: (...a) => log("warn", a),
    error: (...a) => log("error", a),
    debug: (...a) => log("debug", a),
  };
})();
