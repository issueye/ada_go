/* browser-use page runtime. Injected at document start, before the page's scripts. */
(() => {
  "use strict";

  const VERSION = "0.3.0";
  if (window.__browserUse && window.__browserUse.version === VERSION) return;
  const isTopFrame = window === window.top;

  // FRAME_TOKEN is replaced with a per-process secret before the runtime is
  // injected (internal/browser/runtime.SourceWithToken). It lives in this
  // closure, so page scripts cannot read it. Every frame carries the same
  // value; it gates the iframe relay so a hostile top page cannot postMessage
  // a cmd into a cross-origin child and read that child's DOM (same-origin
  // policy bypass). The placeholder below only survives in tests.
  const FRAME_TOKEN = "__BU_FRAME_TOKEN__";

  const RELAY_OPS = new Set([
    "snapshot", "read", "click", "type", "press", "select", "scroll",
    "hover", "query", "info", "dialogs", "network", "console", "upload",
    "drag", "storage",
  ]);

  const state = {
    version: VERSION,
    seq: 0,
    snapSeq: 0,
    navCount: 0,
    lastSnap: null,
    refs: new Map(),          // ref -> {ref, el: WeakRef, fp}
    byEl: new WeakMap(),      // element -> ref
    inputLocked: false,
    bypass: false,
    lockHandler: null,
    dialogs: [],
    dialogPolicy: { confirm: false, prompt: null, promptMode: "default" },
    netSeq: 0,
    netEntries: [],
    netDropped: 0,
    netBytes: 0,
    consoleSeq: 0,
    consoleEntries: [],
    consoleDropped: 0,
    consoleBytes: 0,
    consoleWaiters: new Set(),
    netWaiters: new Set(),
    refsSweptAt: 0,
    fullPageHidden: null,
    lastHumanBlockedAt: 0,
    lastConsumedBlockedAt: 0,
    lastHumanBlockedTick: 0,
    activeFrame: null,
    frameWindows: new Map(),
    framePongs: new Map(),
    framePending: new Map(),
    frameSeq: 0,
  };

  const MAX_ELEMENTS = 8000;
  const MAX_NODES = 400;
  // ref 表的硬上限:超限时从最旧开始淘汰(见 sweepRefs)。正常会话靠快照清扫
  // 就能有界;上限只兜底「大量动作建 ref 却从不快照」的病态流程。
  const REFS_MAX = 2000;
  const NET_MAX_ENTRIES = 300;
  const NET_MAX_REQUEST = 16 * 1024;
  const NET_MAX_RESPONSE = 128 * 1024;
  const NET_MAX_TOTAL = 4 * 1024 * 1024;
  const NET_SECRET = /(password|passwd|pwd|token|secret|authorization|cookie|session)/i;

  // Console capture: one ring buffer per document. 500 lines / 512 KiB are
  // plenty for debugging a page without letting a chatty page pin memory.
  const CONSOLE_MAX_ENTRIES = 500;
  const CONSOLE_MAX_TEXT = 8 * 1024;
  const CONSOLE_MAX_TOTAL = 512 * 1024;
  const CONSOLE_LEVELS = ["log", "info", "warn", "error", "debug"];

  const INTERACTIVE_ROLES = new Set([
    "button", "link", "textbox", "searchbox", "checkbox", "radio", "combobox",
    "listbox", "option", "menuitem", "menuitemcheckbox", "menuitemradio",
    "tab", "switch", "slider", "spinbutton", "treeitem", "gridcell",
  ]);

  const LANDMARK_ROLES = new Set([
    "banner", "main", "navigation", "complementary", "contentinfo", "region",
    "search", "form", "dialog", "alertdialog", "alert", "status", "tabpanel",
  ]);

  const SKIP_TAGS = new Set([
    "script", "style", "noscript", "template", "link", "meta", "head", "title",
    "base", "source", "track", "param", "br", "wbr",
  ]);

  function err(code, message, hint) {
    const e = new Error(message || code);
    e.code = code;
    if (hint) e.hint = hint;
    return e;
  }

  function norm(s) {
    return String(s == null ? "" : s).replace(/\s+/g, " ").trim();
  }

  function clip(s, n) {
    s = norm(s);
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  }

  function sliceText(s, n) {
    s = String(s == null ? "" : s);
    return s.length > n ? s.slice(0, n) : s;
  }

  function netHeaders(h) {
    const out = {};
    if (!h) return out;
    try {
      if (typeof h.forEach === "function") {
        h.forEach((v, k) => { out[String(k)] = String(v); });
      } else if (Array.isArray(h)) {
        h.forEach((p) => { if (p && p.length === 2) out[String(p[0])] = String(p[1]); });
      } else {
        Object.keys(h).forEach((k) => { out[k] = String(h[k]); });
      }
    } catch (e) { /* ignore */ }
    return out;
  }

  function netSafeHeaders(h) {
    const out = {};
    Object.keys(h || {}).forEach((k) => { out[k] = NET_SECRET.test(k) ? "***" : h[k]; });
    return out;
  }

  function netRedact(s) {
    if (typeof s !== "string" || !s) return s;
    try {
      const walk = (v) => {
        if (Array.isArray(v)) return v.map(walk);
        if (v && typeof v === "object") {
          const o = {};
          Object.keys(v).forEach((k) => { o[k] = NET_SECRET.test(k) ? "***" : walk(v[k]); });
          return o;
        }
        return v;
      };
      return JSON.stringify(walk(JSON.parse(s)));
    } catch (e) {
      return s.replace(/((?:^|[?&])(?:password|passwd|pwd|token|access_token|secret)=)[^&]*/gi, "$1***");
    }
  }

  function netRequestBody(body) {
    if (body == null) return "";
    try {
      if (typeof body === "string") return sliceText(netRedact(body), NET_MAX_REQUEST);
      if (body instanceof URLSearchParams) return sliceText(netRedact(body.toString()), NET_MAX_REQUEST);
      if (typeof FormData !== "undefined" && body instanceof FormData) {
        const keys = [];
        body.forEach((v, k) => keys.push(k));
        return "[form-data] " + keys.join(", ");
      }
      if (typeof Blob !== "undefined" && body instanceof Blob) return "[blob " + body.size + " bytes]";
      if (body instanceof ArrayBuffer || ArrayBuffer.isView(body)) {
        return "[binary " + (body.byteLength != null ? body.byteLength : body.length) + " bytes]";
      }
      return sliceText(netRedact(String(body)), NET_MAX_REQUEST);
    } catch (e) {
      return "[unreadable]";
    }
  }

  function netResponseBody(text) {
    if (typeof text !== "string") return { body: "", truncated: false };
    const body = netRedact(text);
    return {
      body: sliceText(body, NET_MAX_RESPONSE),
      truncated: body.length > NET_MAX_RESPONSE,
    };
  }

  function netRecord(e) {
    e.id = "n" + (++state.netSeq);
    e.ts = Date.now();
    e.ms = Math.round(e.ms || 0);
    e.reqHeaders = e.reqHeaders || {};
    e.resHeaders = e.resHeaders || {};
    e.resBody = e.resBody || "";
    e.reqBody = e.reqBody || "";
    e.size = e.resBody.length;
    state.netEntries.push(e);
    state.netBytes += e.resBody.length + e.reqBody.length;
    while (state.netEntries.length > NET_MAX_ENTRIES || state.netBytes > NET_MAX_TOTAL) {
      const old = state.netEntries.shift();
      if (!old) break;
      state.netBytes -= old.resBody.length + old.reqBody.length;
      state.netDropped++;
    }
    if (state.netWaiters.size > 0) {
      // Copy: each waiter removes itself from the set when it resolves.
      Array.from(state.netWaiters).forEach((w) => {
        if (w.done) return;
        if (Number(e.id.slice(1)) <= w.baseline || !netMatch(e, w.args)) return;
        w.done = true;
        clearTimeout(w.timer);
        state.netWaiters.delete(w);
        w.resolve({
          entry: netWaitEntry(e, w.args),
          count: state.netEntries.length,
          dropped: state.netDropped,
          total_bytes: state.netBytes,
          matched: true,
          elapsed_ms: 0,
        });
      });
    }
  }

  function netSummary(e, withBody) {
    const o = {
      id: e.id,
      ts: e.ts,
      type: e.type,
      method: e.method,
      url: e.url,
      status: e.status,
      ok: e.ok,
      ms: e.ms,
      content_type: e.contentType || "",
      size: e.size,
      error: e.error || "",
      truncated: !!e.truncated,
    };
    if (withBody) {
      o.req_headers = e.reqHeaders;
      o.res_headers = e.resHeaders;
      o.req_body = e.reqBody;
      o.res_body = e.resBody;
    }
    return o;
  }

  function network(args) {
    args = args || {};
    const action = args.action || "list";
    if (action === "clear") {
      const n = state.netEntries.length;
      state.netEntries = [];
      state.netDropped = 0;
      state.netBytes = 0;
      state.netWaiters.clear(); // 缓冲清了,挂着的等待不会再有命中
      return { cleared: n, count: 0 };
    }
    if (action === "get") {
      const id = String(args.id || "");
      const e = state.netEntries.find((x) => x.id === id);
      if (!e) throw err("not_found", "no request " + id);
      const entry = netSummary(e, true);
      const max = Number(args.max_body) || 0;
      if (max > 0) {
        entry.req_body = sliceText(entry.req_body, max);
        entry.res_body = sliceText(entry.res_body, max);
      }
      return { entry };
    }
    if (action === "wait") return networkWait(args);
    if (action !== "list" && action !== "find") {
      throw err("bad_request", "unknown network action: " + action);
    }
    let list = state.netEntries.slice();
    if (args.url_contains) {
      const s = String(args.url_contains).toLowerCase();
      list = list.filter((e) => e.url.toLowerCase().includes(s));
    }
    if (args.method) {
      const m = String(args.method).toUpperCase();
      list = list.filter((e) => e.method === m);
    }
    if (args.status != null && args.status !== "") {
      const st = Number(args.status);
      list = list.filter((e) => e.status === st);
    }
    if (args.since) {
      const n = Number(String(args.since).replace(/^n/, "")) || 0;
      list = list.filter((e) => Number(e.id.slice(1)) > n);
    }
    const limit = Math.max(1, Math.min(200, Number(args.limit) || 50));
    list = list.slice(-limit);
    return {
      entries: list.map((e) => netSummary(e, !!args.include_body)),
      count: state.netEntries.length,
      dropped: state.netDropped,
      total_bytes: state.netBytes,
    };
  }

  // netMatch 是 wait 的过滤:URL 子串、方法、状态码,与 list 一致。
  function netMatch(e, args) {
    if (args.url_contains) {
      const s = String(args.url_contains).toLowerCase();
      if (!e.url.toLowerCase().includes(s)) return false;
    }
    if (args.method) {
      if (e.method !== String(args.method).toUpperCase()) return false;
    }
    if (args.status != null && args.status !== "") {
      if (e.status !== Number(args.status)) return false;
    }
    return true;
  }

  function netWaitEntry(e, args) {
    const entry = netSummary(e, true);
    const max = Number(args.max_body) || 0;
    if (max > 0) {
      entry.req_body = sliceText(entry.req_body, max);
      entry.res_body = sliceText(entry.res_body, max);
    }
    return entry;
  }

  // networkWait 在页面里等一条匹配的新请求:命中时由 netRecord 直接 resolve,
  // 不再让 Go 侧每 100ms 轮询一次(与 console wait 同一套机制)。语义对齐旧
  // Go 轮询:无显式 since 时接受最近 5 秒内的命中(触发请求的 click 通常先于
  // wait 落地);显式 since 只认之后的新请求;超时返回 matched:false。
  function networkWait(args) {
    const timeout = Math.max(100, Math.min(120000, Number(args.wait_ms) || 5000));
    const started = Date.now();
    let baseline = Number(String(args.since || "").replace(/^n/, "")) || 0;
    if (!args.since) {
      const cutoff = started - 5000;
      const recent = state.netEntries.filter((e) => e.ts >= cutoff && netMatch(e, args));
      if (recent.length > 0) {
        const e = recent[recent.length - 1];
        return {
          entry: netWaitEntry(e, args),
          count: state.netEntries.length,
          dropped: state.netDropped,
          total_bytes: state.netBytes,
          matched: true,
          elapsed_ms: 0,
        };
      }
      const last = state.netEntries[state.netEntries.length - 1];
      baseline = last ? Number(last.id.slice(1)) : 0;
    }
    return new Promise((resolve) => {
      const waiter = { args, baseline, done: false, resolve, timer: null };
      waiter.timer = setTimeout(() => {
        if (waiter.done) return;
        waiter.done = true;
        state.netWaiters.delete(waiter);
        resolve({
          entry: null,
          count: state.netEntries.length,
          dropped: state.netDropped,
          total_bytes: state.netBytes,
          matched: false,
          elapsed_ms: Date.now() - started,
        });
      }, timeout);
      state.netWaiters.add(waiter);
    });
  }

  function installNetwork() {
    if (window.__buNetInstalled) return;
    try {
      Object.defineProperty(window, "__buNetInstalled", { value: true, configurable: true });
    } catch (e) { window.__buNetInstalled = true; }

    const origFetch = window.fetch;
    if (typeof origFetch === "function") {
      window.fetch = function (input, init) {
        const start = performance.now();
        const req = { type: "fetch", method: "GET", url: "", reqHeaders: {}, reqBody: "" };
        try {
          const isReq = typeof Request !== "undefined" && input instanceof Request;
          req.url = new URL(isReq ? input.url : String(input), location.href).href;
          req.method = String((init && init.method) || (isReq && input.method) || "GET").toUpperCase();
          req.reqHeaders = netSafeHeaders(netHeaders((init && init.headers) || (isReq && input.headers)));
          req.reqBody = netRequestBody(init && init.body != null ? init.body : null);
        } catch (e) {
          req.url = String(input);
        }
        const p = origFetch.apply(this, arguments);
        p.then((res) => {
          const entry = {
            ...req,
            status: res.status,
            ok: res.ok,
            ms: performance.now() - start,
            resHeaders: netSafeHeaders(netHeaders(res.headers)),
            contentType: res.headers.get("content-type") || "",
            resBody: "",
          };
          const ct = entry.contentType.toLowerCase();
          if (ct.includes("text/event-stream")) {
            netRecord(entry);
            return;
          }
          let clone;
          try { clone = res.clone(); } catch (e) { netRecord(entry); return; }
          clone.text().then((text) => {
            const rb = netResponseBody(text);
            entry.resBody = rb.body;
            entry.truncated = rb.truncated;
            netRecord(entry);
          }).catch(() => netRecord(entry));
        }, (e) => {
          netRecord({
            ...req,
            status: 0,
            ok: false,
            ms: performance.now() - start,
            error: String(e && e.message || e),
          });
        });
        return p;
      };
    }

    const XHR = window.XMLHttpRequest;
    if (XHR && XHR.prototype) {
      const origOpen = XHR.prototype.open;
      const origSend = XHR.prototype.send;
      const origSetHeader = XHR.prototype.setRequestHeader;
      XHR.prototype.open = function (method, url) {
        this.__buNet = {
          type: "xhr",
          method: String(method || "GET").toUpperCase(),
          url: "",
          reqHeaders: {},
          reqBody: "",
          start: performance.now(),
        };
        try { this.__buNet.url = new URL(String(url), location.href).href; } catch (e) { this.__buNet.url = String(url); }
        return origOpen.apply(this, arguments);
      };
      XHR.prototype.setRequestHeader = function (k, v) {
        if (this.__buNet) {
          this.__buNet.reqHeaders[String(k)] = NET_SECRET.test(String(k)) ? "***" : String(v);
        }
        return origSetHeader.apply(this, arguments);
      };
      XHR.prototype.send = function (body) {
        const n = this.__buNet;
        if (n) {
          n.reqBody = netRequestBody(body);
          this.addEventListener("loadend", () => {
            let text = "";
            try {
              if (!this.responseType || this.responseType === "text") text = this.responseText;
              else if (this.responseType === "json") text = JSON.stringify(this.response);
              else text = "[responseType " + this.responseType + "]";
            } catch (e) { /* ignore */ }
            const rb = netResponseBody(text);
            const headers = {};
            String(this.getAllResponseHeaders() || "").split(/\r?\n/).forEach((line) => {
              const i = line.indexOf(":");
              if (i > 0) headers[line.slice(0, i).trim()] = line.slice(i + 1).trim();
            });
            netRecord({
              ...n,
              url: this.responseURL || n.url,
              status: this.status,
              ok: this.status >= 200 && this.status < 400,
              ms: performance.now() - n.start,
              resHeaders: netSafeHeaders(headers),
              contentType: headers["content-type"] || headers["Content-Type"] || "",
              resBody: rb.body,
              truncated: rb.truncated,
            });
          }, { once: true });
        }
        return origSend.apply(this, arguments);
      };
    }
  }

  // ---- console capture ---------------------------------------------------
  //
  // The runtime is injected at document start, before the page's own scripts,
  // so wrapping console.* here sees every line the page logs from its first
  // script on. Uncaught errors and unhandled rejections are recorded too: a
  // broken SPA usually shows up there before the DOM changes.

  function consoleValue(v, depth) {
    depth = depth || 0;
    try {
      if (v === undefined) return "undefined";
      if (v === null) return "null";
      const t = typeof v;
      if (t === "string") return v;
      if (t === "number" || t === "boolean" || t === "bigint") return String(v);
      if (t === "symbol") return String(v);
      if (t === "function") return "[function " + (v.name || "anonymous") + "]";
      if (v instanceof Error || (typeof v.message === "string" && typeof v.stack === "string")) {
        return (v.name ? v.name + ": " : "") + String(v.message == null ? "" : v.message) +
          (v.stack ? "\n" + v.stack : "");
      }
      if (v.nodeType === 1) {
        const tag = String(v.tagName || "").toLowerCase();
        const id = v.id ? "#" + v.id : "";
        const cls = typeof v.className === "string" && v.className.trim()
          ? "." + v.className.trim().split(/\s+/).slice(0, 2).join(".")
          : "";
        return "<" + tag + id + cls + ">";
      }
      if (v.nodeType === 3) return "#text " + sliceText(v.nodeValue || "", 200);
      if (typeof v.toJSON === "function") {
        const j = v.toJSON();
        if (j !== v) return consoleValue(j, depth + 1);
      }
      const seen = new WeakSet();
      const walk = (x, d) => {
        if (x === null || typeof x !== "object") return x;
        if (seen.has(x)) return "[circular]";
        seen.add(x);
        if (Array.isArray(x)) {
          return d >= 3 ? "[array " + x.length + "]" : x.slice(0, 50).map((it) => walk(it, d + 1));
        }
        const out = {};
        Object.keys(x).slice(0, 50).forEach((k) => {
          out[k] = NET_SECRET.test(k) ? "***" : walk(x[k], d + 1);
        });
        return out;
      };
      const s = JSON.stringify(walk(v, depth));
      return s === undefined ? String(v) : s;
    } catch (e) {
      return "[unprintable]";
    }
  }

  function consoleRedact(s) {
    return String(s == null ? "" : s).replace(
      /((?:^|[?&\s])(?:password|passwd|pwd|token|access_token|refresh_token|secret|api_key|apikey|authorization|cookie|session)=)[^&\s]*/gi,
      "$1***",
    );
  }

  function consoleRecord(level, text, source, meta) {
    const full = consoleRedact(typeof text === "string" ? text : consoleValue(text));
    const entry = {
      id: "c" + (++state.consoleSeq),
      ts: Date.now(),
      level: String(level || "log"),
      text: sliceText(full, CONSOLE_MAX_TEXT),
      source: source || "console",
      url: (meta && meta.url) || location.href,
      line: (meta && meta.line) || 0,
      column: (meta && meta.column) || 0,
      truncated: full.length > CONSOLE_MAX_TEXT,
    };
    state.consoleEntries.push(entry);
    state.consoleBytes += entry.text.length;
    while (state.consoleEntries.length > CONSOLE_MAX_ENTRIES || state.consoleBytes > CONSOLE_MAX_TOTAL) {
      const old = state.consoleEntries.shift();
      if (!old) break;
      state.consoleBytes -= old.text.length;
      state.consoleDropped++;
    }
    if (state.consoleWaiters.size > 0) {
      // Copy: each waiter removes itself from the set when it resolves.
      Array.from(state.consoleWaiters).forEach((w) => {
        if (w.done || !consoleMatch(entry, w.args)) return;
        w.done = true;
        clearTimeout(w.timer);
        state.consoleWaiters.delete(w);
        w.resolve({
          entries: [consoleSummary(entry)],
          count: state.consoleEntries.length,
          dropped: state.consoleDropped,
          total_bytes: state.consoleBytes,
          matched: true,
          elapsed_ms: 0,
        });
      });
    }
  }

  function consoleSummary(e) {
    return {
      id: e.id, ts: e.ts, level: e.level, text: e.text, source: e.source,
      url: e.url || "", line: e.line || 0, column: e.column || 0,
      truncated: !!e.truncated,
    };
  }

  function consoleQuery(args) {
    args = args || {};
    const action = args.action || "list";
    if (action === "clear") {
      const n = state.consoleEntries.length;
      state.consoleEntries = [];
      state.consoleDropped = 0;
      state.consoleBytes = 0;
      return { cleared: n, entries: [], count: 0, dropped: 0, total_bytes: 0 };
    }
    if (action === "get") {
      const id = String(args.id || "");
      if (!id) throw err("bad_request", "id is required for console get");
      const found = state.consoleEntries.find((e) => e.id === id);
      if (!found) throw err("not_found", "no console entry " + id, "run console list to see the current buffer");
      return {
        entry: consoleSummary(found),
        count: state.consoleEntries.length,
        dropped: state.consoleDropped,
        total_bytes: state.consoleBytes,
      };
    }
    if (action !== "list" && action !== "wait") throw err("bad_request", "unknown console action: " + action);
    if (action === "wait") return consoleWait(args);
    const list = state.consoleEntries.filter((e) => consoleMatch(e, args)).slice(-limitOf(args));
    return {
      entries: list.map(consoleSummary),
      count: state.consoleEntries.length,
      dropped: state.consoleDropped,
      total_bytes: state.consoleBytes,
      matched: list.length > 0,
    };
  }

  function limitOf(args) {
    return Math.max(1, Math.min(200, Number(args.limit) || 50));
  }

  // consoleMatch 是 list 与 wait 共用的过滤：级别、来源、文本子串、游标。
  function consoleMatch(e, args) {
    if (args.level && e.level !== String(args.level).toLowerCase()) return false;
    if (args.source && e.source !== String(args.source).toLowerCase()) return false;
    if (args.text_contains && e.text.toLowerCase().indexOf(String(args.text_contains).toLowerCase()) < 0) {
      return false;
    }
    if (args.since) {
      const n = Number(String(args.since).replace(/^c/, "")) || 0;
      if (Number(e.id.slice(1)) <= n) return false;
    }
    return true;
  }

  // consoleWait 在页面里等一条匹配的新日志：命中时由 consoleRecord 直接 resolve，
  // 不再让 Go 侧每 100ms 发一次 eval（那会把整个动作队列占满）。
  function consoleWait(args) {
    const timeout = Math.max(100, Math.min(120000, Number(args.timeout_ms) || 5000));
    const started = Date.now();
    const pending = state.consoleEntries.filter((e) => consoleMatch(e, args)).slice(-limitOf(args));
    if (pending.length > 0) {
      return {
        entries: pending.map(consoleSummary),
        count: state.consoleEntries.length,
        dropped: state.consoleDropped,
        total_bytes: state.consoleBytes,
        matched: true,
        elapsed_ms: 0,
      };
    }
    return new Promise((resolve) => {
      const waiter = { args, done: false, resolve, timer: null };
      waiter.timer = setTimeout(() => {
        if (waiter.done) return;
        waiter.done = true;
        state.consoleWaiters.delete(waiter);
        resolve({
          entries: [],
          count: state.consoleEntries.length,
          dropped: state.consoleDropped,
          total_bytes: state.consoleBytes,
          matched: false,
          elapsed_ms: Date.now() - started,
        });
      }, timeout);
      state.consoleWaiters.add(waiter);
    });
  }

  function installConsole() {
    if (window.__buConsoleInstalled) return;
    try {
      Object.defineProperty(window, "__buConsoleInstalled", { value: true, configurable: true });
    } catch (e) { window.__buConsoleInstalled = true; }
    if (window.console) {
      CONSOLE_LEVELS.forEach((level) => {
        const orig = window.console[level];
        const call = typeof orig === "function" ? orig.bind(window.console) : null;
        try {
          window.console[level] = function () {
            try {
              consoleRecord(level, Array.prototype.map.call(arguments, (a) => consoleValue(a)).join(" "), "console");
            } catch (e) { /* never break the page because of logging */ }
            if (call) return call.apply(null, arguments);
          };
        } catch (e) { /* frozen console */ }
      });
    }
    window.addEventListener("error", (ev) => {
      try {
        const where = ev && ev.filename
          ? " @ " + ev.filename + ":" + (ev.lineno || 0) + ":" + (ev.colno || 0)
          : "";
        consoleRecord("error", String((ev && ev.message) || "uncaught error") + where, "exception", {
          line: (ev && ev.lineno) || 0, column: (ev && ev.colno) || 0,
        });
      } catch (e) { /* ignore */ }
    }, true);
    window.addEventListener("unhandledrejection", (ev) => {
      try {
        consoleRecord("error", consoleValue(ev && ev.reason), "rejection");
      } catch (e) { /* ignore */ }
    });
  }

  function roleOf(el) {
    const explicit = el.getAttribute && (el.getAttribute("role") || "").trim().toLowerCase();
    if (explicit) return explicit.split(/\s+/)[0];
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    switch (tag) {
      case "a": return el.hasAttribute("href") ? "link" : null;
      case "button": return "button";
      case "summary": return "button";
      case "select": return el.multiple ? "listbox" : "combobox";
      case "textarea": return "textbox";
      case "input": {
        const t = (el.getAttribute("type") || "text").toLowerCase();
        if (t === "checkbox") return "checkbox";
        if (t === "radio") return "radio";
        if (t === "range") return "slider";
        if (t === "number") return "spinbutton";
        if (t === "search") return "searchbox";
        if (t === "submit" || t === "button" || t === "reset" || t === "image") return "button";
        if (t === "hidden") return null;
        if (t === "file") return "button";
        return "textbox";
      }
      case "img": return el.getAttribute("alt") ? "img" : null;
      case "dialog": return "dialog";
      case "h1": case "h2": case "h3": case "h4": case "h5": case "h6": return "heading";
      case "nav": return "navigation";
      case "main": return "main";
      case "header": return "banner";
      case "footer": return "contentinfo";
      case "aside": return "complementary";
      case "form": return "form";
      case "iframe": return "iframe";
      default: break;
    }
    if (el.isContentEditable) return "textbox";
    return null;
  }

  function labelText(el) {
    try {
      if (el.labels && el.labels.length) {
        return [...el.labels].map((l) => norm(l.innerText || l.textContent)).filter(Boolean).join(" ");
      }
    } catch (e) { /* ignore */ }
    const id = el.id;
    if (id) {
      const l = document.querySelector('label[for="' + CSS.escape(id) + '"]');
      if (l) return norm(l.innerText || l.textContent);
    }
    const wrap = el.closest && el.closest("label");
    if (wrap) return norm(wrap.innerText || wrap.textContent);
    return "";
  }

  function nameOf(el) {
    const aria = norm(el.getAttribute && el.getAttribute("aria-label"));
    if (aria) return aria;
    const by = el.getAttribute && el.getAttribute("aria-labelledby");
    if (by) {
      const parts = by.split(/\s+/).map((id) => document.getElementById(id)).filter(Boolean)
        .map((n) => norm(n.innerText || n.textContent));
      const joined = parts.filter(Boolean).join(" ");
      if (joined) return joined;
    }
    const alt = norm(el.getAttribute && el.getAttribute("alt"));
    if (alt) return alt;
    const title = norm(el.getAttribute && el.getAttribute("title"));
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    const labelled = labelText(el);
    if (labelled) return labelled;
    const ph = norm(el.getAttribute && el.getAttribute("placeholder"));
    if (ph) return ph;
    if (tag === "input" || tag === "textarea" || tag === "select") {
      const value = norm(el.value);
      if (value && (el.type === "submit" || el.type === "button" || el.type === "reset")) return value;
    }
    if (tag === "button" || tag === "a" || tag === "summary" || /^h[1-6]$/.test(tag) || el.getAttribute("role")) {
      return norm(el.innerText || el.textContent);
    }
    if (title) return title;
    return "";
  }

  function valueOf(el) {
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    if (tag === "input") {
      const t = (el.getAttribute("type") || "text").toLowerCase();
      if (t === "password") return el.value ? "•••" : "";
      if (t === "checkbox" || t === "radio") return el.checked ? "true" : "false";
      return clip(el.value, 80);
    }
    if (tag === "textarea") return clip(el.value, 80);
    if (tag === "select") {
      return [...el.selectedOptions].map((o) => norm(o.textContent)).join(", ").slice(0, 80);
    }
    return "";
  }

  function hrefOf(el) {
    if (el.tagName && el.tagName.toLowerCase() === "a") {
      try { return String(el.href).slice(0, 160); } catch (e) { return ""; }
    }
    return "";
  }

  function isDisabled(el) {
    if (el.disabled) return true;
    if (el.getAttribute && el.getAttribute("aria-disabled") === "true") return true;
    return false;
  }

  function isVisible(el) {
    if (!el || !el.isConnected) return false;
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    if (tag === "input" && (el.getAttribute("type") || "").toLowerCase() === "hidden") return false;
    const cs = getComputedStyle(el);
    if (cs.display === "none" || cs.visibility === "hidden" || cs.visibility === "collapse") return false;
    if (parseFloat(cs.opacity) === 0) return false;
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    if (el.checkVisibility) {
      try {
        if (!el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return false;
      } catch (e) { /* older engines */ }
    }
    return true;
  }

  // Hover-only controls (menus, card actions) often live in a wrapper with
  // opacity: 0 until :hover. Custom events cannot set :hover, so reveal the
  // wrapper for the click and restore it afterwards.
  function revealAncestors(el) {
    const changed = [];
    let n = el;
    for (let i = 0; n && n !== document.documentElement && i < 6; i++, n = n.parentElement) {
      const cs = getComputedStyle(n);
      const hiddenByOpacity = parseFloat(cs.opacity) === 0;
      const hiddenByVisibility = cs.visibility === "hidden" || cs.visibility === "collapse";
      const blocked = cs.pointerEvents === "none";
      if (!hiddenByOpacity && !hiddenByVisibility && !blocked) continue;
      changed.push({
        el: n,
        opacity: n.style.opacity,
        visibility: n.style.visibility,
        pointerEvents: n.style.pointerEvents,
      });
      n.style.opacity = "1";
      n.style.visibility = "visible";
      n.style.pointerEvents = "auto";
    }
    return () => {
      for (const c of changed) {
        c.el.style.opacity = c.opacity;
        c.el.style.visibility = c.visibility;
        c.el.style.pointerEvents = c.pointerEvents;
      }
    };
  }

  function isInteractive(el, role) {
    if (role && INTERACTIVE_ROLES.has(role)) return true;
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    if (tag === "a" && el.hasAttribute("href")) return true;
    if (tag === "button" || tag === "select" || tag === "textarea" || tag === "summary") return true;
    if (tag === "input") return (el.getAttribute("type") || "").toLowerCase() !== "hidden";
    if (tag === "label") return true;
    if (el.isContentEditable) return true;
    if (el.hasAttribute("onclick")) return true;
    const ti = el.getAttribute("tabindex");
    if (ti != null && Number(ti) >= 0) return true;
    return false;
  }

  function actionText(el) {
    return norm(el && (el.innerText || el.textContent) || "");
  }

  // isClickable extends the semantic roles with the very common "div/span with
  // cursor:pointer and a click handler" pattern (React/Vue menus, cards, list
  // rows). Without it those controls never enter a snapshot and text=... cannot
  // target them.
  function isClickable(el, role) {
    if (!el || !el.tagName || isDisabled(el)) return false;
    if (isInteractive(el, role)) return true;
    const tag = el.tagName.toLowerCase();
    if (SKIP_TAGS.has(tag) || tag === "html" || tag === "body") return false;
    try {
      const cs = getComputedStyle(el);
      if (cs.cursor !== "pointer" || cs.pointerEvents === "none") return false;
      return !!(actionText(el) || nameOf(el));
    } catch (e) {
      return false;
    }
  }

  function depthOf(el) {
    let d = 0;
    for (let n = el; n && n !== document.documentElement; n = n.parentElement) d++;
    return d;
  }

  function scoreMatch(el) {
    const r = el.getBoundingClientRect();
    let score = 0;
    if (isVisible(el)) score += 100;
    if (r.right > 0 && r.bottom > 0 && r.left < window.innerWidth && r.top < window.innerHeight) score += 20;
    score += Math.min(depthOf(el), 20);
    const area = Math.max(1, r.width * r.height);
    // Prefer the innermost of nested candidates (a clickable card containing a
    // clickable title should not be ambiguous).
    score += Math.max(0, 20 - Math.log2(area / 100));
    return score;
  }

  function rankMatches(matches) {
    if (!matches || matches.length === 0) return null;
    if (matches.length === 1) return matches[0];
    let best = null;
    let bestScore = -Infinity;
    let second = -Infinity;
    for (const el of matches) {
      const s = scoreMatch(el);
      if (s > bestScore) {
        second = bestScore;
        bestScore = s;
        best = el;
      } else if (s > second) {
        second = s;
      }
    }
    return bestScore > second ? best : null;
  }

  function fpOf(el) {
    return {
      tag: (el.tagName || "").toLowerCase(),
      role: roleOf(el) || "",
      name: clip(nameOf(el), 120),
      id: el.id || "",
      type: (el.getAttribute && el.getAttribute("type")) || "",
    };
  }

  // sweepRefs 清理 ref 表:元素已被 GC(WeakRef 死了)且自上轮清扫以来没被
  // 用过的条目直接删除;最近用过的多留一轮,给 stale_ref 的指纹恢复留余地。
  // 超过 REFS_MAX 时从最旧开始淘汰(Map 保持插入序=创建序),含活引用 ——
  // 那是「从不快照」的病态流程的兜底,正常会话走不到。
  function sweepRefs() {
    const cutoff = state.refsSweptAt;
    for (const [ref, rec] of state.refs) {
      if (rec.el && rec.el.deref && rec.el.deref()) continue;
      if ((rec.touchedAt || rec.createdAt) >= cutoff) continue;
      state.refs.delete(ref);
    }
    state.refsSweptAt = Date.now();
    while (state.refs.size > REFS_MAX) {
      const oldest = state.refs.keys().next().value;
      state.refs.delete(oldest);
    }
  }

  function refFor(el) {
    const old = state.byEl.get(el);
    if (old) {
      const rec = state.refs.get(old);
      if (rec && (!rec.el || rec.el.deref() === el)) {
        rec.touchedAt = Date.now();
        return old;
      }
    }
    if (state.refs.size >= REFS_MAX) sweepRefs();
    const ref = "e" + (++state.seq);
    const rec = {
      ref,
      el: typeof WeakRef === "function" ? new WeakRef(el) : { deref: () => el },
      fp: fpOf(el),
      createdAt: Date.now(),
      touchedAt: 0,
    };
    state.refs.set(ref, rec);
    state.byEl.set(el, ref);
    return ref;
  }

  function deref(ref) {
    const rec = state.refs.get(ref);
    if (!rec) return null;
    const el = rec.el && rec.el.deref ? rec.el.deref() : null;
    if (el && el.isConnected) {
      rec.touchedAt = Date.now();
      return el;
    }
    return null;
  }

  function findByFp(fp) {
    if (!fp) return null;
    if (fp.id) {
      const byId = document.getElementById(fp.id);
      if (byId && (!fp.role || roleOf(byId) === fp.role)) return byId;
    }
    if (!fp.role && !fp.name) return null;
    const all = deepQueryAll(document, "a,button,input,select,textarea,summary,[role],[tabindex],[contenteditable]");
    const matches = all.filter((el) => roleOf(el) === fp.role && clip(nameOf(el), 120) === fp.name);
    return matches.length === 1 ? matches[0] : null;
  }

  function resolveRef(raw) {
    let ref = String(raw || "").trim();
    ref = ref.replace(/^ref=/, "").replace(/^@/, "");
    if (/^\d+$/.test(ref)) ref = "e" + ref;
    const el = deref(ref);
    if (el) return { el, ref };
    const rec = state.refs.get(ref);
    const found = rec ? findByFp(rec.fp) : null;
    if (found) {
      const newRef = refFor(found);
      return { el: found, ref: newRef };
    }
    throw err("stale_ref", "ref " + ref + " is no longer in the page", "take a new snapshot");
  }

  function deepQueryAll(root, selector) {
    const out = [];
    if (!root || !root.querySelectorAll) return out;
    try { out.push(...root.querySelectorAll(selector)); } catch (e) { throw err("bad_request", "invalid selector: " + selector); }
    for (const el of root.querySelectorAll("*")) {
      if (el.shadowRoot) out.push(...deepQueryAll(el.shadowRoot, selector));
    }
    return out;
  }

  // deepTextMatch 单次遍历收集 textContent 含 s 的元素(粗筛):旧实现
  // deepQueryAll(document, "*") 对全 DOM 枚举两遍(选择器一遍 + shadow 检测
  // 一遍),再对每个元素做样式计算和 innerText 布局;大页面上 text= 定位是
  // O(N) 的重活。textContent 不触发布局,先粗筛再对命中者做精确检查。
  function deepTextMatch(s, deadline) {
    const out = [];
    const visit = (root) => {
      if (!root || Date.now() > deadline) return;
      let walker;
      try {
        walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
      } catch (e) {
        return;
      }
      const shadows = [];
      for (let el = walker.nextNode(); el; el = walker.nextNode()) {
        if (el.shadowRoot) shadows.push(el.shadowRoot);
        const text = el.textContent;
        // 超时降级:兆级 DOM 上单次遍历也可能慢,到点后停止收集 —— text=
        // 在病态大页面上是 best-effort,正常页面远走不到这里。
        if (Date.now() > deadline) break;
        if (text && text.toLowerCase().includes(s)) out.push(el);
      }
      for (const sh of shadows) visit(sh);
    };
    visit(document);
    return out;
  }

  function parseTextLocator(spec, requireClickable) {
    let s = spec;
    let exact = false;
    if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
      exact = true;
      s = s.slice(1, -1);
    }
    s = norm(s).toLowerCase();
    // textContent includes 是精确检查的超集,粗筛不改变语义;样式计算与
    // innerText 布局只花在粗筛命中者上。
    const candidates = deepTextMatch(s, Date.now() + 500);
    const hits = [];
    let exactHits = [];
    for (const el of candidates) {
      if (requireClickable && !isClickable(el, roleOf(el))) continue;
      if (!requireClickable && !isVisible(el)) continue;
      const text = norm(el.innerText || el.textContent || nameOf(el)).toLowerCase();
      if (exact ? text === s : text.includes(s)) {
        hits.push(el);
        if (!exact && text === s) exactHits.push(el);
      }
    }
    // "Submit" should win over "Submit all" even though both contain the query.
    return exactHits.length > 0 ? exactHits : hits;
  }

  function resolveLocator(spec, opts) {
    // textClickable:false 供 drag 等动作用——拖拽源不必是可点击元素,
    // 只要可见且有文本即可。
    const wantClickable = !(opts && opts.textClickable === false);
    if (spec == null) throw err("bad_request", "no target");
    if (typeof spec === "object") {
      if (spec.ref) return resolveRef(spec.ref);
      if (spec.selector) return resolveLocator(String(spec.selector));
      if (spec.text != null) {
        const matches = parseTextLocator(String(spec.text), wantClickable);
        return unique(matches, "text=" + spec.text, true);
      }
      if (spec.point) {
        const el = elementAt(spec.point);
        return { el, ref: refFor(el), point: spec.point };
      }
    }
    const s = String(spec).trim();
    let m = /^(?:ref=|@)?(e?\d+)$/.exec(s);
    if (m) return resolveRef(m[1]);
    if (s.startsWith("ref=") || s.startsWith("@")) return resolveRef(s);
    if (s.startsWith("css=")) return unique(deepQueryAll(document, s.slice(4)), s);
    if (s.startsWith("loc=css:")) return unique(deepQueryAll(document, s.slice(8)), s);
    if (s.startsWith("text=")) {
      const matches = parseTextLocator(s.slice(5), wantClickable);
      return unique(matches, s, true);
    }
    if (s.startsWith("role=")) {
      return unique(parseRoleLocator(s.slice(5)), s, true);
    }
    if (s.startsWith("xpath=")) {
      const out = [];
      const r = document.evaluate(s.slice(6), document, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
      for (let i = 0; i < r.snapshotLength; i++) {
        const n = r.snapshotItem(i);
        if (n && n.nodeType === 1) out.push(n);
      }
      return unique(out, s);
    }
    return unique(deepQueryAll(document, s), s);
  }

  function parseRoleLocator(spec) {
    const m = /^([a-zA-Z-]+)(?:\[name([*^$]?)=["']?(.+?)["']?\])?$/.exec(spec.trim());
    if (!m) throw err("bad_request", "invalid role locator: role=" + spec);
    const role = m[1].toLowerCase();
    const op = m[2] || "";
    const want = norm(m[3] || "").toLowerCase();
    const all = deepQueryAll(document, "[role],a,button,input,select,textarea,summary,h1,h2,h3,h4,h5,h6");
    const matches = all.filter((el) => {
      if (roleOf(el) !== role) return false;
      if (!want) return true;
      const name = clip(nameOf(el), 120).toLowerCase();
      if (op === "*") return name.includes(want);
      if (op === "^") return name.startsWith(want);
      if (op === "$") return name.endsWith(want);
      return name === want;
    });
    return matches;
  }

  function unique(matches, spec, allowRank) {
    if (!matches || matches.length === 0) throw err("not_found", "no element matches " + spec);
    if (matches.length > 1) {
      if (allowRank) {
        const best = rankMatches(matches);
        if (best) return { el: best, ref: refFor(best) };
      }
      const candidates = matches.slice(0, 5).map((el) => {
        const r = el.getBoundingClientRect();
        return { ref: refFor(el), role: roleOf(el) || el.tagName.toLowerCase(), name: clip(nameOf(el) || actionText(el), 80), rect: { x: r.left, y: r.top, w: r.width, h: r.height } };
      });
      const e = err("ambiguous", matches.length + " elements match " + spec, "use a ref or a more specific selector");
      e.details = { candidates };
      throw e;
    }
    const el = matches[0];
    return { el, ref: refFor(el) };
  }

  function elementAt(pt) {
    const x = Number(pt && pt.x);
    const y = Number(pt && pt.y);
    if (!Number.isFinite(x) || !Number.isFinite(y)) throw err("bad_request", "invalid point");
    const el = document.elementFromPoint(x, y);
    if (!el) throw err("not_found", "no element at " + x + "," + y);
    return el;
  }

  function centerOf(el) {
    const r = el.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2, rect: { x: r.left, y: r.top, w: r.width, h: r.height } };
  }

  function frames(n) {
    return new Promise((resolve) => {
      let left = Math.max(1, n);
      let done = false;
      const finish = () => {
        if (done) return;
        done = true;
        resolve();
      };
      const step = () => (--left <= 0 ? finish() : requestAnimationFrame(step));
      requestAnimationFrame(step);
      // Hidden/background tabs throttle or pause rAF; a layout wait must not
      // stall an action forever just because the tab is not visible.
      setTimeout(finish, 250);
    });
  }

  const ACTION_WAIT_MS = 5000;
  const ACTION_RETRY_MS = 100;

  function pause(ms) {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }

  function actionWaitMs(opts) {
    const n = Number(opts && opts.wait_ms);
    return Number.isFinite(n) && n > 0 ? Math.min(n, 30000) : ACTION_WAIT_MS;
  }

  // Only transient actionability failures are worth retrying: the element may
  // still be loading, animating, disabled or covered. A stale ref or an invalid
  // selector needs the agent to change its input, not wait.
  function retryableActionError(e) {
    const code = e && e.code;
    return code === "not_visible" || code === "not_enabled" || code === "covered" || code === "not_found";
  }

  function withBypass(fn) {
    const prev = state.bypass;
    state.bypass = true;
    try { return fn(); } finally { state.bypass = prev; }
  }

  function modsOf(list) {
    const out = {};
    for (const m of list || []) {
      const k = String(m).toLowerCase();
      if (k === "alt") out.altKey = true;
      else if (k === "control" || k === "ctrl") out.ctrlKey = true;
      else if (k === "meta" || k === "cmd" || k === "command") out.metaKey = true;
      else if (k === "shift") out.shiftKey = true;
    }
    return out;
  }

  async function click(target, opts) {
    opts = opts || {};
    const deadline = Date.now() + actionWaitMs(opts);
    let resolved = null;
    let el = null;
    let c = null;
    let restoreReveal = null;
    for (;;) {
      try {
        resolved = resolveLocator(target);
        el = resolved.el;
        el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
        await frames(1);
        if (!isVisible(el) && opts.reveal !== false && (resolved.point || opts.reveal)) {
          if (!restoreReveal) {
            restoreReveal = revealAncestors(el);
            await frames(1);
          }
          if (resolved.point) {
            const at = elementAt(resolved.point);
            if (at) el = at;
          }
        }
        if (!isVisible(el)) throw err("not_visible", "element is not visible", "scroll or wait for it to show");
        if (isDisabled(el)) throw err("not_enabled", "element is disabled", "wait for it to become enabled");
        // A point click must land on the requested coordinates, not on the center
        // of the element that happens to contain them (canvas, maps, video).
        c = resolved.point
          ? { x: Number(resolved.point.x), y: Number(resolved.point.y) }
          : centerOf(el);
        if (!opts.force) {
          const hit = document.elementFromPoint(c.x, c.y);
          if (hit && hit !== el && !el.contains(hit) && !hit.contains(el)) {
            throw err("covered", "element is covered by <" + hit.tagName.toLowerCase() + ">", "remove the overlay or use force");
          }
        }
        break;
      } catch (e) {
        if (!retryableActionError(e) || Date.now() >= deadline) throw e;
        await pause(ACTION_RETRY_MS);
      }
    }
    const button = opts.button === "right" ? 2 : opts.button === "middle" ? 1 : 0;
    const mods = modsOf(opts.modifiers);
    const base = {
      bubbles: true, cancelable: true, composed: true, clientX: c.x, clientY: c.y,
      screenX: window.screenX + c.x, screenY: window.screenY + c.y,
      button, buttons: button === 0 ? 1 : 0, detail: 1, ...mods,
    };
    const info = snapshotInfo(el, resolved.ref);
    const PE = typeof PointerEvent === "function" ? PointerEvent : MouseEvent;
    // Return in the same task as the click: a link activation may start a
    // navigation, and awaiting frames afterwards would lose this document.
    withBypass(() => {
      el.dispatchEvent(new PE("pointerdown", { ...base, pointerId: 1, pointerType: "mouse", isPrimary: true }));
      el.dispatchEvent(new MouseEvent("mousedown", base));
      el.dispatchEvent(new PE("pointerup", { ...base, buttons: 0, pointerId: 1, pointerType: "mouse", isPrimary: true }));
      el.dispatchEvent(new MouseEvent("mouseup", { ...base, buttons: 0 }));
      // Dispatch the click ourselves instead of el.click(): only an explicit
      // MouseEvent carries clientX/clientY, and point clicks (canvas/maps) need
      // those coordinates in the click listener. Synthetic click still runs the
      // element's activation behavior (link, checkbox, submit).
      const clickBase = { ...base, buttons: 0 };
      if (button === 2) {
        el.dispatchEvent(new MouseEvent("contextmenu", { ...clickBase, detail: 1 }));
      } else if (opts.count === 2) {
        // One activation like el.click() did before, plus a coordinate-carrying
        // dblclick; two clicks would submit a form twice.
        el.dispatchEvent(new MouseEvent("click", { ...clickBase, detail: 1 }));
        el.dispatchEvent(new MouseEvent("dblclick", { ...clickBase, detail: 2 }));
      } else {
        el.dispatchEvent(new MouseEvent("click", { ...clickBase, detail: 1 }));
      }
    });
    if (restoreReveal) setTimeout(restoreReveal, 300);
    return { clicked: info, url: location.href, title: document.title, revealed: !!restoreReveal };
  }

  // drag 把元素从 from 拖到 to。两套事件序列都发:HTML5 DnD(源可拖或显式
  // html5=true,携带 DataTransfer)与 pointer/mouse 步进序列(自绘拖拽、
  // canvas 看板)。遮罩期里合成事件走 withBypass 穿透输入锁。
  async function drag(args) {
    args = args || {};
    if (!args.from || !args.to) throw err("bad_request", "from and to targets are required");
    const deadline = Date.now() + actionWaitMs(args);
    let from = null;
    let to = null;
    for (;;) {
      try {
        from = resolveLocator(args.from, { textClickable: false });
        to = resolveLocator(args.to, { textClickable: false });
        from.el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
        to.el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
        await frames(1);
        if (!isVisible(from.el)) throw err("not_visible", "drag source is not visible", "scroll or wait for it to show");
        if (isDisabled(from.el)) throw err("not_enabled", "drag source is disabled", "wait for it to become enabled");
        if (!isVisible(to.el)) throw err("not_visible", "drag target is not visible", "scroll or wait for it to show");
        break;
      } catch (e) {
        if (!retryableActionError(e) || Date.now() >= deadline) throw e;
        await pause(ACTION_RETRY_MS);
      }
    }
    const fc = centerOf(from.el);
    const tc = centerOf(to.el);
    const text = String(args.text || "");
    const html5 = args.html5 === true || from.el.draggable === true || !!text;
    withBypass(() => {
      if (html5) {
        const dt = new DataTransfer();
        if (text) dt.setData("text/plain", text);
        const fire = (el, type, x, y) => {
          // DragEvent 构造器在部分场景不可用:退回 MouseEvent + 手挂 dataTransfer。
          let ev;
          try {
            ev = new DragEvent(type, { bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y, dataTransfer: dt });
          } catch (e) {
            ev = new MouseEvent(type, { bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y });
            try { Object.defineProperty(ev, "dataTransfer", { value: dt }); } catch (e2) { /* ignore */ }
          }
          el.dispatchEvent(ev);
        };
        fire(from.el, "dragstart", fc.x, fc.y);
        fire(to.el, "dragenter", tc.x, tc.y);
        fire(to.el, "dragover", tc.x, tc.y);
        fire(to.el, "drop", tc.x, tc.y);
        fire(from.el, "dragend", fc.x, fc.y);
      }
      const PE = typeof PointerEvent === "function" ? PointerEvent : MouseEvent;
      const pe = (type, el, x, y, buttons) => new PE(type, {
        bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y,
        pointerId: 1, pointerType: "mouse", isPrimary: true, button: 0, buttons,
      });
      const me = (type, x, y, buttons) => new MouseEvent(type, {
        bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y, button: 0, buttons,
      });
      from.el.dispatchEvent(pe("pointerdown", from.el, fc.x, fc.y, 1));
      from.el.dispatchEvent(me("mousedown", fc.x, fc.y, 1));
      const steps = Math.max(2, Math.min(16, Number(args.steps) || 8));
      for (let i = 1; i <= steps; i++) {
        const x = fc.x + ((tc.x - fc.x) * i) / steps;
        const y = fc.y + ((tc.y - fc.y) * i) / steps;
        const over = document.elementFromPoint(x, y) || to.el;
        over.dispatchEvent(pe("pointermove", over, x, y, 1));
        over.dispatchEvent(me("mousemove", x, y, 1));
      }
      to.el.dispatchEvent(pe("pointerup", to.el, tc.x, tc.y, 0));
      to.el.dispatchEvent(me("mouseup", tc.x, tc.y, 0));
    });
    return {
      dragged: true,
      from: snapshotInfo(from.el, from.ref),
      to: snapshotInfo(to.el, to.ref),
      html5,
      url: location.href,
      title: document.title,
    };
  }

  // storage 读写当前 origin 的 localStorage / sessionStorage(会话调试、
  // 登录态检查)。set 上限 64 KiB,get 返回值截 16 KiB。
  const STORAGE_MAX_SET = 64 * 1024;
  const STORAGE_MAX_GET = 16 * 1024;

  function storageArea(kind) {
    const k = String(kind || "local").toLowerCase();
    if (k !== "local" && k !== "session") throw err("bad_request", "kind must be local or session");
    const area = k === "session" ? window.sessionStorage : window.localStorage;
    if (!area) throw err("unsupported", "web storage is unavailable", "the page may block storage");
    return { kind: k, area };
  }

  function storage(args) {
    args = args || {};
    const { kind, area } = storageArea(args.kind);
    const action = String(args.action || "list");
    if (action === "list") {
      const keys = [];
      let total = 0;
      const n = area.length;
      for (let i = 0; i < n && keys.length < 200; i++) {
        const key = area.key(i);
        if (key === null) continue;
        const bytes = (area.getItem(key) || "").length;
        total += key.length + bytes;
        keys.push({ key, bytes });
      }
      return { kind, count: n, keys, total_bytes: total };
    }
    if (action === "get") {
      const key = String(args.key || "");
      if (!key) throw err("bad_request", "key is required for storage get");
      const value = area.getItem(key);
      if (value === null) return { kind, key, found: false, value: "" };
      const truncated = value.length > STORAGE_MAX_GET;
      return { kind, key, found: true, value: truncated ? value.slice(0, STORAGE_MAX_GET) : value, truncated };
    }
    if (action === "set") {
      const key = String(args.key || "");
      const value = String(args.value == null ? "" : args.value);
      if (!key) throw err("bad_request", "key is required for storage set");
      if (value.length > STORAGE_MAX_SET) throw err("bad_request", "value exceeds 64 KiB storage limit");
      area.setItem(key, value);
      return { kind, key, bytes: value.length };
    }
    if (action === "clear") {
      const key = String(args.key || "");
      if (key) {
        const had = area.getItem(key) !== null;
        area.removeItem(key);
        return { kind, key, removed: had };
      }
      const cleared = area.length;
      area.clear();
      return { kind, cleared };
    }
    throw err("bad_request", "unknown storage action: " + action);
  }

  // humanBlocked 记录遮罩期被输入锁拦下的人类操作:agent 每次写动作前消费一次,
  // 命中则发 control.human_active 事件(模型可据此释放租约让用户先操作)。
  function humanBlocked(args) {
    args = args || {};
    if (String(args.action || "consume") !== "consume") {
      throw err("bad_request", "unknown humanBlocked action");
    }
    const blocked = state.lastHumanBlockedAt > state.lastConsumedBlockedAt;
    state.lastConsumedBlockedAt = Math.max(state.lastConsumedBlockedAt, state.lastHumanBlockedAt);
    return { blocked };
  }

  function fileInputFor(el) {
    if (!el) return null;
    if (el.tagName === "INPUT" && String(el.type).toLowerCase() === "file") return el;
    if (el.querySelector) {
      const inside = el.querySelector('input[type="file"]');
      if (inside) return inside;
    }
    let n = el.parentElement;
    for (let i = 0; n && i < 4; i++, n = n.parentElement) {
      if (n.querySelector) {
        const found = n.querySelector('input[type="file"]');
        if (found) return found;
      }
    }
    return null;
  }

  async function upload(args) {
    const resolved = resolveLocator(args.target);
    const input = fileInputFor(resolved.el);
    if (!input) {
      throw err("no_file_input", "no file input near the target", "target the upload button or its input");
    }
    const files = Array.isArray(args.files) ? args.files : [];
    if (files.length === 0) throw err("bad_request", "no files");
    const dt = new DataTransfer();
    const names = [];
    let total = 0;
    for (const f of files) {
      const bin = atob(String(f.data || ""));
      const bytes = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
      const file = new File([bytes], String(f.name || "file"), { type: String(f.mime || "") });
      if (!input.multiple && names.length >= 1) break;
      dt.items.add(file);
      names.push(file.name);
      total += bytes.length;
    }
    if (names.length === 0) throw err("bad_request", "no usable files");
    input.files = dt.files;
    input.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
    input.dispatchEvent(new Event("change", { bubbles: true }));
    return {
      uploaded: {
        ref: refFor(input),
        names,
        count: names.length,
        bytes: total,
        multiple: !!input.multiple,
      },
      url: location.href,
      title: document.title,
    };
  }

  function nativeSetValue(el, value) {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const desc = Object.getOwnPropertyDescriptor(proto, "value");
    if (desc && desc.set) desc.set.call(el, value);
    else el.value = value;
  }

  function isEditable(el) {
    const tag = el.tagName ? el.tagName.toLowerCase() : "";
    if (tag === "input") {
      const t = (el.getAttribute("type") || "text").toLowerCase();
      return !["checkbox", "radio", "button", "submit", "reset", "range", "file", "hidden", "image"].includes(t);
    }
    return tag === "textarea" || el.isContentEditable;
  }

  async function typeText(target, text, opts) {
    opts = opts || {};
    const deadline = Date.now() + actionWaitMs(opts);
    let resolved = null;
    let el = null;
    for (;;) {
      try {
        resolved = resolveLocator(target);
        el = resolved.el;
        if (!isEditable(el)) throw err("not_editable", "element does not accept text", "use click/select for this element");
        el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
        await frames(1);
        if (!isVisible(el)) throw err("not_visible", "element is not visible", "scroll or wait for it to show");
        if (isDisabled(el)) throw err("not_enabled", "element is disabled", "wait for it to become enabled");
        break;
      } catch (e) {
        if (!retryableActionError(e) || Date.now() >= deadline) throw e;
        await pause(ACTION_RETRY_MS);
      }
    }
    el.focus();
    const value = String(text == null ? "" : text);
    withBypass(() => {
      if (el.isContentEditable) {
        if (opts.clear !== false) {
          el.textContent = "";
          el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, inputType: "deleteContentBackward" }));
        }
        if (value) {
          let ok = false;
          try { ok = document.execCommand("insertText", false, value); } catch (e) { ok = false; }
          if (!ok) {
            el.textContent = (opts.clear !== false ? "" : el.textContent) + value;
            el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, inputType: "insertText", data: value }));
          }
        }
      } else {
        if (opts.clear !== false) nativeSetValue(el, "");
        if (!opts.slowly) {
          nativeSetValue(el, value);
          el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, inputType: "insertText", data: value }));
        } else {
          let cur = opts.clear !== false ? "" : el.value;
          for (const ch of value) {
            cur += ch;
            nativeSetValue(el, cur);
            el.dispatchEvent(new KeyboardEvent("keydown", { key: ch, bubbles: true, composed: true }));
            el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, inputType: "insertText", data: ch }));
            el.dispatchEvent(new KeyboardEvent("keyup", { key: ch, bubbles: true, composed: true }));
          }
        }
        el.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
      }
    });
    if (opts.submit) {
      const info = snapshotInfo(el, resolved.ref);
      withBypass(() => {
        el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true, composed: true }));
        el.dispatchEvent(new KeyboardEvent("keypress", { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true, composed: true }));
        el.dispatchEvent(new KeyboardEvent("keyup", { key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, composed: true }));
        const form = el.form;
        if (form && typeof form.requestSubmit === "function") {
          try { form.requestSubmit(); } catch (e) { form.submit(); }
        }
      });
      return { typed: info, submitted: true, url: location.href };
    }
    await frames(2);
    return { typed: snapshotInfo(el, resolved.ref), submitted: false, url: location.href };
  }

  const KEYMAP = {
    Enter: ["Enter", "Enter", 13], Tab: ["Tab", "Tab", 9], Escape: ["Escape", "Escape", 27],
    Backspace: ["Backspace", "Backspace", 8], Delete: ["Delete", "Delete", 46], Space: [" ", "Space", 32],
    ArrowUp: ["ArrowUp", "ArrowUp", 38], ArrowDown: ["ArrowDown", "ArrowDown", 40],
    ArrowLeft: ["ArrowLeft", "ArrowLeft", 37], ArrowRight: ["ArrowRight", "ArrowRight", 39],
    Home: ["Home", "Home", 36], End: ["End", "End", 35], PageUp: ["PageUp", "PageUp", 33], PageDown: ["PageDown", "PageDown", 34],
  };

  function parseChord(chord) {
    const parts = String(chord || "").split("+").map((s) => s.trim()).filter(Boolean);
    const mods = {};
    let key = "";
    for (const p of parts) {
      const l = p.toLowerCase();
      if (l === "ctrl" || l === "control") mods.ctrlKey = true;
      else if (l === "shift") mods.shiftKey = true;
      else if (l === "alt") mods.altKey = true;
      else if (l === "meta" || l === "cmd" || l === "command") mods.metaKey = true;
      else key = p;
    }
    return { key, mods };
  }

  function keyInfo(key) {
    if (KEYMAP[key]) return { key: KEYMAP[key][0], code: KEYMAP[key][1], keyCode: KEYMAP[key][2] };
    if (key.length === 1) {
      const up = key.toUpperCase();
      return { key, code: /[A-Z]/.test(up) ? "Key" + up : "", keyCode: up.charCodeAt(0) };
    }
    return { key, code: key, keyCode: 0 };
  }

  function tabbables() {
    const all = deepQueryAll(document, "a[href],button,input,select,textarea,summary,[tabindex],[contenteditable]");
    return all.filter((el) => !isDisabled(el) && el.tabIndex >= 0 && isVisible(el));
  }

  function focusNext(back) {
    const list = tabbables();
    if (!list.length) return;
    const cur = document.activeElement;
    let i = list.indexOf(cur);
    if (i < 0) i = back ? 0 : -1;
    const next = list[(i + (back ? -1 + list.length : 1)) % list.length];
    if (next) next.focus();
  }

  async function pressKey(key, opts) {
    opts = opts || {};
    const parsed = parseChord(key);
    const info = keyInfo(parsed.key);
    const mods = { ...parsed.mods, ...modsOf(opts.modifiers) };
    let el = document.activeElement || document.body;
    if (opts.target) el = resolveLocator(opts.target).el;
    if (opts.target) el.focus();
    const base = {
      key: info.key, code: info.code, keyCode: info.keyCode, which: info.keyCode,
      bubbles: true, cancelable: true, composed: true, ...mods,
    };
    withBypass(() => {
      el.dispatchEvent(new KeyboardEvent("keydown", base));
      if (info.key.length === 1 || info.key === "Enter") {
        el.dispatchEvent(new KeyboardEvent("keypress", base));
      }
      el.dispatchEvent(new KeyboardEvent("keyup", base));
      if (parsed.key === "Tab") focusNext(!!mods.shiftKey);
      if (parsed.key === "Enter" && el.form && typeof el.form.requestSubmit === "function") {
        try { el.form.requestSubmit(); } catch (e) { /* ignore */ }
      }
    });
    return { pressed: { key: parsed.key, modifiers: Object.keys(mods).filter((k) => mods[k]) }, url: location.href };
  }

  async function selectOption(target, values) {
    const deadline = Date.now() + actionWaitMs({});
    const wanted = Array.isArray(values) ? values.map(String) : [String(values == null ? "" : values)];
    let resolved = null;
    let el = null;
    let opts = [];
    let chosen = [];
    for (;;) {
      try {
        resolved = resolveLocator(target);
        el = resolved.el;
        const tag = (el.tagName || "").toLowerCase();
        if (tag !== "select") throw err("not_select", "element is not a <select>");
        if (!isVisible(el)) throw err("not_visible", "element is not visible", "scroll or wait for it to show");
        opts = [...el.options];
        chosen = [];
        for (const want of wanted) {
          const o = opts.find((o) => o.value === want) || opts.find((o) => norm(o.textContent) === norm(want));
          if (!o) throw err("not_found", "no option matches " + want);
          chosen.push(o);
        }
        break;
      } catch (e) {
        if (!retryableActionError(e) || Date.now() >= deadline) throw e;
        await pause(ACTION_RETRY_MS);
      }
    }
    withBypass(() => {
      if (!el.multiple) {
        for (const o of opts) o.selected = false;
        if (chosen[0]) chosen[0].selected = true;
      } else {
        for (const o of chosen) o.selected = true;
      }
      el.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
      el.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
    });
    await frames(1);
    return { selected: chosen.map((o) => norm(o.textContent)), value: el.value, url: location.href };
  }

  async function scroll(opts) {
    opts = opts || {};
    if (opts.xy) {
      // 绝对滚动,整页截图的分段捕获用。
      window.scrollTo({ left: Number(opts.xy.x) || 0, top: Number(opts.xy.y) || 0, behavior: "instant" });
      await frames(1);
    } else if (opts.to) {
      const el = resolveLocator(opts.to).el;
      el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
      await frames(1);
    } else if (opts.point) {
      const el = elementAt(opts.point);
      el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
      await frames(1);
    } else {
      const amount = Number(opts.amount || 600);
      const dir = String(opts.direction || "down").toLowerCase();
      let dx = 0, dy = 0;
      if (dir === "up") dy = -amount;
      else if (dir === "down") dy = amount;
      else if (dir === "left") dx = -amount;
      else if (dir === "right") dx = amount;
      window.scrollBy({ left: dx, top: dy, behavior: "instant" });
      await frames(1);
    }
    return { scroll: { x: window.scrollX, y: window.scrollY } };
  }

  async function hover(target) {
    const resolved = resolveLocator(target);
    const el = resolved.el;
    el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
    await frames(1);
    const c = centerOf(el);
    const base = { bubbles: true, cancelable: true, composed: true, clientX: c.x, clientY: c.y };
    withBypass(() => {
      el.dispatchEvent(new MouseEvent("mouseover", base));
      el.dispatchEvent(new MouseEvent("mouseenter", base));
      el.dispatchEvent(new MouseEvent("mousemove", base));
    });
    await frames(1);
    return { hovered: snapshotInfo(el, resolved.ref), url: location.href };
  }

  function snapshotInfo(el, ref) {
    const r = el.getBoundingClientRect();
    return {
      ref: ref || refFor(el),
      role: roleOf(el) || (isClickable(el, roleOf(el)) ? "clickable" : (el.tagName || "").toLowerCase()),
      name: clip(nameOf(el) || actionText(el), 160),
      rect: { x: r.left, y: r.top, w: r.width, h: r.height },
      tag: (el.tagName || "").toLowerCase(),
    };
  }

  function snapshot(args) {
    args = args || {};
    const maxChars = Math.min(Math.max(Number(args.max_chars || 12000), 1000), 50000);
    const filter = norm(args.filter).toLowerCase();
    const scope = args.scope || "full";
    // 快照是 ref 的主要产出点,也顺带做一次清扫:长驻 SPA 会话里 ref 表
    // 不再随快照次数无界增长。
    sweepRefs();
    const viewport = { x: 0, y: 0, w: window.innerWidth, h: window.innerHeight };
    const nodes = [];
    let visited = 0;
    let hitCap = false;

    function intersects(r) {
      return r.left < viewport.w && r.top < viewport.h && r.right > 0 && r.bottom > 0;
    }

    function visit(el, depth) {
      if (!el || depth > 120 || visited >= MAX_ELEMENTS || nodes.length >= MAX_NODES) {
        hitCap = true;
        return;
      }
      visited++;
      if (el.nodeType === 1) {
        const tag = (el.tagName || "").toLowerCase();
        if (SKIP_TAGS.has(tag)) return;
        const cs = getComputedStyle(el);
        if (cs.display === "none") return;
        const role = roleOf(el);
        const clickable = isClickable(el, role);
        const heading = /^h[1-6]$/.test(tag);
        const landmark = role && LANDMARK_ROLES.has(role);
        const img = tag === "img" && el.getAttribute("alt");
        const iframe = tag === "iframe";
        const dialog = role === "dialog" || role === "alertdialog";
        const texty = tag === "p" || tag === "li" || tag === "td" || tag === "th" || tag === "pre" || tag === "blockquote";
        const want = clickable || heading || landmark || img || iframe || dialog || (texty && args.include_text);
        if (want) {
          const visible = isVisible(el);
          if (visible) {
            const r = el.getBoundingClientRect();
            if (scope !== "viewport" || intersects(r)) {
              const name = clip(nameOf(el) || actionText(el), 160);
              const hay = (role || tag) + " " + name + " " + tag;
              if (!filter || hay.toLowerCase().includes(filter)) {
                nodes.push({
                  ref: refFor(el), role: role || (clickable ? "clickable" : tag), name, tag,
                  value: valueOf(el), href: hrefOf(el), disabled: isDisabled(el),
                  checked: el.checked === true, rect: { x: r.left, y: r.top, w: r.width, h: r.height },
                  inViewport: intersects(r), texty, clickable,
                });
              }
            }
          }
        }
        if (tag === "iframe") return;
        if (el.shadowRoot) visit(el.shadowRoot, depth + 1);
        for (const child of el.children) visit(child, depth + 1);
      } else if (el.nodeType === 11) {
        for (const child of el.children) visit(child, depth + 1);
      }
    }

    visit(document.documentElement, 0);
    const snapVersion = ++state.snapSeq;
    const useDiff = !!args.diff;
    const prev = state.lastSnap;
    const diffBase = useDiff && prev && prev.nodes ? prev.nodes : null;
    const prevMap = diffBase || new Map();
    const currentMap = new Map();
    const sigOf = (n) => JSON.stringify([n.role, n.name, n.value, n.href, !!n.checked, !!n.disabled]);
    const lineOf = (n) => {
      let line = "[" + n.ref + "] " + n.role;
      if (n.name) line += ' "' + n.name.replace(/"/g, '\\"') + '"';
      if (n.value) line += " value=" + JSON.stringify(n.value);
      if (n.href) line += " href=" + n.href;
      if (n.checked) line += " checked";
      if (n.disabled) line += " disabled";
      if (!n.inViewport) line += " offscreen";
      return line;
    };
    let changes = 0;
    for (const n of nodes) {
      currentMap.set(n.ref, n);
      const old = prevMap.get(n.ref);
      if (useDiff && old && sigOf(old) === sigOf(n)) continue;
      if (useDiff) changes++;
    }
    if (useDiff) {
      for (const ref of prevMap.keys()) {
        if (!currentMap.has(ref)) changes++;
      }
    }
    const header = "snapshot v" + snapVersion + "  url=" + location.href + "  title=" + clip(document.title, 120) +
      "\nviewport " + window.innerWidth + "x" + window.innerHeight + "  scroll=(" + Math.round(window.scrollX) + "," + Math.round(window.scrollY) + ")" +
      "  page=" + Math.round(document.documentElement.scrollWidth) + "x" + Math.round(document.documentElement.scrollHeight) +
      "  dpr=" + (window.devicePixelRatio || 1) + "  refs=" + nodes.length + "  nodes=" + visited + "  nav=" + state.navCount +
      (useDiff ? "  diff=1  changes=" + changes + (prev ? "" : "  (no previous snapshot)") : "");
    const lines = [header];
    let used = header.length;
    let truncated = hitCap;
    let shown = 0;
    for (const n of nodes) {
      const old = prevMap.get(n.ref);
      let prefix = "";
      if (useDiff) {
        if (old && sigOf(old) === sigOf(n)) continue;
        prefix = old ? "~ " : "+ ";
      }
      const line = prefix + lineOf(n);
      if (used + line.length + 1 > maxChars) { truncated = true; break; }
      lines.push(line);
      used += line.length + 1;
      shown++;
    }
    if (useDiff && prevMap.size > 0) {
      for (const [ref, old] of prevMap) {
        if (currentMap.has(ref)) continue;
        const line = "- " + lineOf(old);
        if (used + line.length + 1 > maxChars) { truncated = true; break; }
        lines.push(line);
        used += line.length + 1;
        shown++;
      }
    }
    if (truncated && nodes.length > shown) lines.push("[truncated: " + Math.max(0, nodes.length - shown) + " more nodes; narrow with filter/scope]");
    // Keep the full node set: the next diff compares against it, and UIs use nodes.
    state.lastSnap = { version: snapVersion, nodes: currentMap };
    return {
      text: lines.join("\n"),
      nodes: nodes.map((n) => ({ ref: n.ref, role: n.role, name: n.name, rect: n.rect })),
      version: snapVersion,
      truncated,
      diff: useDiff,
      changes,
      url: location.href,
      title: document.title,
      viewport: { w: window.innerWidth, h: window.innerHeight, dpr: window.devicePixelRatio || 1 },
      scroll: { x: window.scrollX, y: window.scrollY },
      nodes_count: visited,
    };
  }

  function query(args) {
    args = args || {};
    const spec = args.target || args.selector || args.text;
    const stateWant = args.state || "visible";
    let matches;
    try {
      if (args.text != null && !args.target && !args.selector) matches = parseTextLocator(String(args.text));
      else matches = [resolveLocator(spec).el];
    } catch (e) {
      if (e.code === "not_found") {
        // hidden 的语义是“不存在或不可见”：元素根本不在 DOM 里时，wait hidden 应立即命中。
        return { count: 0, found: stateWant === "hidden", visible: false };
      }
      throw e;
    }
    if (matches.length === 0) {
      return { count: 0, found: stateWant === "hidden", visible: false };
    }
    if (stateWant === "hidden") {
      const anyVisible = matches.some((el) => isVisible(el));
      return { count: matches.length, found: !anyVisible, visible: anyVisible };
    }
    if (stateWant === "attached") return { count: matches.length, found: true };
    const visible = matches.filter((el) => isVisible(el));
    return {
      count: visible.length, found: visible.length > 0,
      info: visible.length ? snapshotInfo(visible[0], refFor(visible[0])) : null,
    };
  }

  function textOfBody() {
    const body = document.body;
    return body ? String(body.innerText || body.textContent || "") : "";
  }

  function readPage(args) {
    args = args || {};
    let root = document.body;
    if (args.selector) {
      const matches = deepQueryAll(document, args.selector);
      if (!matches.length) throw err("not_found", "no element matches " + args.selector);
      root = matches[0];
    }
    const max = Math.min(Math.max(Number(args.max_chars || 12000), 1000), 50000);
    const format = String(args.format || "text").toLowerCase();
    let content = "";
    if (format === "links") {
      content = readLinks(root, max);
    } else if (format === "tables" || format === "markdown") {
      const tables = readTables(root, max);
      if (format === "tables") {
        content = tables;
      } else {
        // markdown = 可读正文 + 表格转 markdown(innerText 会把 <table> 打平,
        // 模型读不清行列关系)。
        const text = String(root ? root.innerText || root.textContent || "" : "")
          .replace(/\r\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
        content = tables ? (text ? text + "\n\n" + tables : tables) : text;
      }
    } else {
      content = String(root ? root.innerText || root.textContent || "" : "")
        .replace(/\r\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
    }
    if (content.length > max) {
      content = content.slice(0, max) + "\n… [truncated]";
    }
    return {
      content,
      truncated: content.length > max,
      format,
      url: location.href,
      title: document.title,
      chars: content.length,
    };
  }

  // readLinks 抽取 root 里的链接(文本 + href),去重,常用于「这页有哪些入口」。
  function readLinks(root, max) {
    const seen = new Set();
    const out = [];
    const anchors = root ? root.querySelectorAll("a[href]") : [];
    for (const a of anchors) {
      if (out.length >= 200) break;
      const href = String(a.getAttribute("href") || "");
      if (!href || href.startsWith("javascript:")) continue;
      const text = clip(a.innerText || a.textContent || href, 120);
      const key = href + "\u0000" + text;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push("- " + text + " -> " + href);
    }
    if (out.length >= 200) out.push("[links truncated at 200]");
    return out.join("\n");
  }

  // readTables 把 root 里的 <table> 渲染成 markdown,行/列对齐关系保得住,
  // 而 innerText 会把表格打平。每表最多 40 行(超出标注),单元格截 120 字符。
  function readTables(root, max) {
    const tables = root ? root.querySelectorAll("table") : [];
    const out = [];
    for (let t = 0; t < tables.length && out.join("\n").length < max; t++) {
      const table = tables[t];
      if (out.length >= 20) { out.push("[tables truncated at 20]"); break; }
      const rows = [];
      for (const tr of table.querySelectorAll("tr")) {
        const cells = [];
        for (const cell of tr.querySelectorAll("th,td")) {
          cells.push(clip(cell.innerText || cell.textContent || "", 120).replace(/\|/g, "\\|").replace(/\n/g, " "));
        }
        if (cells.length > 0) rows.push(cells);
        if (rows.length >= 41) break;
      }
      if (rows.length === 0) continue;
      const cols = Math.max(...rows.map((r) => r.length));
      const md = [];
      const head = rows[0];
      md.push("| " + head.map((c) => c || " ").join(" | ") + " |");
      md.push("|" + head.map(() => " --- ").join("|") + "|");
      const body = rows.slice(1, 41);
      for (const r of body) {
        const padded = r.slice();
        while (padded.length < cols) padded.push("");
        md.push("| " + padded.join(" | ") + " |");
      }
      if (rows.length > 41) md.push("[table truncated at 40 rows]");
      out.push(md.join("\n"));
    }
    return out.join("\n\n");
  }

  // fullPage 辅助整页截图:prepare 给出页面尺寸;hide 把 fixed/sticky 元素藏起来
  // (它们会出现在每个分段里,只应在第一段出现一次);restore 恢复。
  // 隐藏列表存在 state 里,restore 与 hide 不必是同一次调用。
  function fullPage(args) {
    args = args || {};
    const action = String(args.action || "prepare");
    if (action === "prepare") {
      const de = document.documentElement;
      return {
        page_w: Math.round(de.scrollWidth),
        page_h: Math.round(de.scrollHeight),
        viewport_w: window.innerWidth,
        viewport_h: window.innerHeight,
        dpr: window.devicePixelRatio || 1,
      };
    }
    if (action === "hide") {
      if (state.fullPageHidden) return { hidden: state.fullPageHidden.length };
      state.fullPageHidden = [];
      const all = document.body ? document.body.querySelectorAll("*") : [];
      const cap = Math.min(all.length, 4000);
      for (let i = 0; i < cap && state.fullPageHidden.length < 60; i++) {
        const el = all[i];
        let cs;
        try { cs = getComputedStyle(el); } catch (e) { continue; }
        if (cs.position !== "fixed" && cs.position !== "sticky") continue;
        if (!isVisible(el)) continue;
        try {
          el.__buPrevVisibility = el.style.visibility;
          el.style.visibility = "hidden";
          state.fullPageHidden.push(el);
        } catch (e) { /* ignore */ }
      }
      return { hidden: state.fullPageHidden.length };
    }
    if (action === "restore") {
      const hidden = state.fullPageHidden || [];
      for (const el of hidden) {
        try {
          if (el && el.style) el.style.visibility = el.__buPrevVisibility || "";
          delete el.__buPrevVisibility;
        } catch (e) { /* element may be gone */ }
      }
      state.fullPageHidden = null;
      return { restored: hidden.length };
    }
    throw err("bad_request", "unknown fullPage action: " + action);
  }

  function setInputLock(on) {
    on = !!on;
    if (on === state.inputLocked) return { locked: on };
    state.inputLocked = on;
    if (on) {
      if (!state.lockHandler) {
        state.lockHandler = (ev) => {
          if (!state.inputLocked || state.bypass) return;
          // 记录「人类在遮罩期尝试操作」:agent 下一次写动作前会消费并上报。
          const now = Date.now();
          if (now - state.lastHumanBlockedTick > 500) {
            state.lastHumanBlockedTick = now;
            state.lastHumanBlockedAt = now;
          }
          if (ev.cancelable) ev.preventDefault();
          ev.stopImmediatePropagation();
        };
      }
      for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click", "dblclick",
        "wheel", "touchstart", "touchmove", "touchend", "contextmenu", "dragstart", "drag", "dragover", "drop",
        "keydown", "keypress", "keyup"]) {
        window.addEventListener(type, state.lockHandler, { capture: true, passive: false });
      }
      try { document.activeElement && document.activeElement.blur && document.activeElement.blur(); } catch (e) { /* ignore */ }
      try { const s = getSelection(); s && s.removeAllRanges(); } catch (e) { /* ignore */ }
    } else if (state.lockHandler) {
      for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click", "dblclick",
        "wheel", "touchstart", "touchmove", "touchend", "contextmenu", "dragstart", "drag", "dragover", "drop",
        "keydown", "keypress", "keyup"]) {
        window.removeEventListener(type, state.lockHandler, true);
      }
    }
    return { locked: on };
  }

  function recordDialog(type, message, defaultValue, answer) {
    state.dialogs.push({
      type, message: clip(message, 500), defaultValue: defaultValue == null ? null : clip(defaultValue, 200),
      answer: answer === undefined ? null : answer, ts: Date.now(),
    });
    if (state.dialogs.length > 50) state.dialogs.shift();
  }

  // Dialog calls are synchronous in the page, so the host cannot ask a human
  // and wait. Instead the agent sets a policy for the next round of dialogs:
  // accept makes confirm() true and prompt() return text/default; dismiss makes
  // confirm() false and prompt() return null.
  function dialogQuery(args) {
    args = args || {};
    const action = args.action || "list";
    if (action === "clear") {
      const n = state.dialogs.length;
      state.dialogs = [];
      return { cleared: n, dialogs: [], policy: { ...state.dialogPolicy } };
    }
    if (action === "accept") {
      state.dialogPolicy.confirm = true;
      if (args.text != null) {
        state.dialogPolicy.prompt = String(args.text);
        state.dialogPolicy.promptMode = "value";
      } else {
        state.dialogPolicy.prompt = null;
        state.dialogPolicy.promptMode = "default";
      }
      return { dialogs: state.dialogs.slice(), policy: { ...state.dialogPolicy } };
    }
    if (action === "dismiss") {
      state.dialogPolicy.confirm = false;
      state.dialogPolicy.prompt = null;
      state.dialogPolicy.promptMode = "dismiss";
      return { dialogs: state.dialogs.slice(), policy: { ...state.dialogPolicy } };
    }
    if (action !== "list") throw err("bad_request", "unknown dialogs action: " + action);
    return { dialogs: state.dialogs.slice(), policy: { ...state.dialogPolicy } };
  }

  function installDialogs() {
    try {
      Object.defineProperty(window, "alert", {
        configurable: true, writable: true,
        value: (message) => { recordDialog("alert", message, null, null); },
      });
      Object.defineProperty(window, "confirm", {
        configurable: true, writable: true,
        value: (message) => {
          const answer = !!state.dialogPolicy.confirm;
          recordDialog("confirm", message, null, answer);
          return answer;
        },
      });
      Object.defineProperty(window, "prompt", {
        configurable: true, writable: true,
        value: (message, def) => {
          const mode = state.dialogPolicy.promptMode;
          const answer = mode === "dismiss"
            ? null
            : mode === "value"
              ? state.dialogPolicy.prompt
              : (def == null ? null : String(def));
          recordDialog("prompt", message, def, answer);
          return answer;
        },
      });
    } catch (e) { /* ignore */ }
  }

  function installNav() {
    try {
      for (const name of ["pushState", "replaceState"]) {
        const orig = history[name];
        if (typeof orig !== "function") continue;
        history[name] = function () {
          const out = orig.apply(this, arguments);
          state.navCount++;
          return out;
        };
      }
    } catch (e) { /* frozen history */ }
    window.addEventListener("popstate", () => { state.navCount++; });
    window.addEventListener("hashchange", () => { state.navCount++; });
  }

  function postFrame(win, msg) {
    try { win.postMessage(msg, "*"); } catch (e) { /* frame gone */ }
  }

  // ---- iframe relay（支持嵌套）-------------------------------------------
  //
  // 帧按层级编号：顶层直接子帧 f1/f2/…，f2 的子帧是 f2.1/f2.2，依此类推
  // （深度上限 MAX_FRAME_DEPTH，索引路径天然无环）。命令沿索引路径逐跳下发
  // （cmd 带 route，中继帧转发），结果/subpong 沿原路逐跳上传。中继帧只转发,
  // 不缓存路由状态,跨域帧之间全部走 postMessage。

  const MAX_FRAME_DEPTH = 5;
  const FRAME_PING_MS = 250;

  function isOwnFrame(win) {
    // window.frames 就是 window 本身(legacy 别名):不能用 for...of,
    // 用 window.length + 索引访问枚举直接子帧。
    try {
      const n = window.frames.length;
      for (let i = 0; i < n; i++) {
        if (window.frames[i] === win) return true;
      }
    } catch (e) { /* ignore */ }
    return false;
  }

  // routeOf 把帧 id 解成 0 基索引路径："f2.1" → [1, 0]。
  function routeOf(id) {
    const parts = String(id).replace(/^f/, "").split(".");
    const out = [];
    for (const p of parts) {
      const n = Number(p);
      if (!Number.isInteger(n) || n < 1) throw err("no_frame", "bad frame id " + id, "run frames list");
      out.push(n - 1);
    }
    return out;
  }

  // collectChildren ping 本窗口的直接子帧并等 pong;pong 带 childCount,
  // 父帧据此决定要不要继续递归。
  async function collectChildren() {
    const total = window.frames.length;
    state.framePongs = new Map();
    const tokens = [];
    for (let i = 0; i < total; i++) {
      const token = "f" + (++state.frameSeq);
      tokens.push(token);
      postFrame(window.frames[i], { __buFrame: "ping", token, auth: FRAME_TOKEN });
    }
    await new Promise((r) => setTimeout(r, FRAME_PING_MS));
    const out = [];
    for (let i = 0; i < tokens.length; i++) {
      const info = state.framePongs.get(tokens[i]);
      if (info) out.push({ index: i + 1, url: info.url, title: info.title, child_count: info.childCount || 0 });
    }
    state.framePongs = new Map();
    return out;
  }

  function installFrameRelay() {
    if (isTopFrame) {
      window.addEventListener("message", (ev) => {
        const d = ev.data;
        if (!d || !d.__buFrame || !isOwnFrame(ev.source)) return;
        if (d.__buFrame === "pong") {
          state.framePongs.set(d.token, { url: d.url || "", title: d.title || "", childCount: d.child_count || 0 });
          return;
        }
        if (d.__buFrame === "subpong") {
          const p = state.frameSubPending.get(d.token);
          if (!p) return;
          clearTimeout(p.timer);
          state.frameSubPending.delete(d.token);
          p.resolve(Array.isArray(d.frames) ? d.frames : []);
          return;
        }
        if (d.__buFrame === "result") {
          const p = state.framePending.get(d.id);
          if (!p) return;
          clearTimeout(p.timer);
          state.framePending.delete(d.id);
          p.resolve(d.payload);
        }
      });
      return;
    }
    window.addEventListener("message", async (ev) => {
      const d = ev.data;
      if (!d || !d.__buFrame) return;
      // 消息来自父帧(命令下发)或本帧自己的子帧(结果/subpong 上传)。
      const fromParent = ev.source === window.parent;
      const fromChild = isOwnFrame(ev.source);
      if (!fromParent && !fromChild) return;
      if (d.__buFrame === "ping" && fromParent && d.auth === FRAME_TOKEN) {
        try {
          ev.source.postMessage({
            __buFrame: "pong", token: d.token,
            url: location.href, title: document.title,
            child_count: window.frames.length,
          }, "*");
        } catch (e) { /* ignore */ }
        return;
      }
      if (d.__buFrame === "pong" && fromChild) {
        // 本帧 collectChildren 的探针回包(嵌套收集时任意层级都会发生)。
        state.framePongs.set(d.token, { url: d.url || "", title: d.title || "", childCount: d.child_count || 0 });
        return;
      }
      if (d.__buFrame === "subping" && fromParent && d.auth === FRAME_TOKEN) {
        // 父帧要我们收集直接子帧清单;收集完沿原路上传。
        try {
          const frames = await collectChildren();
          ev.source.postMessage({ __buFrame: "subpong", token: d.token, frames }, "*");
        } catch (e) {
          ev.source.postMessage({ __buFrame: "subpong", token: d.token, frames: [] }, "*");
        }
        return;
      }
      if (d.__buFrame === "cmd" && fromParent && d.auth === FRAME_TOKEN) {
        const route = Array.isArray(d.route) ? d.route : [];
        if (route.length > 0) {
          // 不是给本帧的:按索引转发一跳,结果由叶子沿原路上传。
          const win = window.frames[Number(route[0])];
          if (!win) {
            try {
              ev.source.postMessage({
                __buFrame: "result", id: d.id,
                payload: JSON.stringify({ ok: false, error: { code: "no_frame", message: "frame is gone; run frames list" } }),
              }, "*");
            } catch (e) { /* ignore */ }
            return;
          }
          postFrame(win, { __buFrame: "cmd", id: d.id, auth: FRAME_TOKEN, route: route.slice(1), op: d.op, args: d.args || {} });
          return;
        }
        let payload;
        try {
          payload = await invoke({ op: d.op, args: d.args || {} });
        } catch (e) {
          payload = JSON.stringify({
            ok: false,
            error: { code: "frame_error", message: String(e && e.message || e) },
          });
        }
        try {
          ev.source.postMessage({ __buFrame: "result", id: d.id, payload }, "*");
        } catch (e) { /* ignore */ }
        return;
      }
      // result / subpong 来自子帧:原样转发给父帧(逐跳上传)。
      if ((d.__buFrame === "result" || d.__buFrame === "subpong") && fromChild) {
        try { window.parent.postMessage(d, "*"); } catch (e) { /* ignore */ }
      }
    });
  }

  async function listFrames() {
    const out = [];
    const pendingSub = new Map();

    // askSubtree 沿索引路径向某个后代帧要它自己的子帧清单;
    // 中继帧逐跳转发 subping,subpong 沿原路上传。
    const askSubtree = (route) => new Promise((resolve) => {
      const token = "sub" + (++state.frameSeq);
      const timer = setTimeout(() => {
        pendingSub.delete(token);
        resolve(null);
      }, FRAME_PING_MS + 1500);
      pendingSub.set(token, { resolve, timer });
      postFrame(window.frames[route[0]], {
        __buFrame: "subping", token, auth: FRAME_TOKEN, route: route.slice(1),
      });
    });
    state.frameSubPending = pendingSub;

    const walk = async (route, id, depth) => {
      if (depth >= MAX_FRAME_DEPTH) return;
      let children;
      if (route.length === 0) {
        children = await collectChildren();
      } else {
        children = await askSubtree(route);
        if (!children) return;
      }
      for (const child of children) {
        const cid = id ? id + "." + child.index : "f" + child.index;
        out.push({ id: cid, url: child.url, title: child.title });
        if (child.child_count > 0) {
          await walk(route.concat(child.index - 1), cid, depth + 1);
        }
      }
    };
    await walk([], "", 0);
    state.frameSubPending = new Map();
    return { frames: out, total: out.length, active: state.activeFrame };
  }

  async function frameCommand(op, args) {
    const id = state.activeFrame;
    if (!id) throw err("no_frame", "no active frame");
    const route = routeOf(id);
    const win = window.frames[route[0]];
    if (!win) throw err("no_frame", "frame is gone; run frames list");
    const rid = "r" + (++state.frameSeq);
    return await new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        state.framePending.delete(rid);
        reject(err("frame_timeout", "frame did not answer"));
      }, 30000);
      state.framePending.set(rid, { resolve, timer });
      postFrame(win, { __buFrame: "cmd", id: rid, auth: FRAME_TOKEN, route: route.slice(1), op, args: args || {} });
    });
  }

  async function frames(args) {
    args = args || {};
    const action = args.action || "list";
    if (action === "list") return await listFrames();
    if (action === "use") {
      const id = String(args.frame || args.id || "");
      if (!id) throw err("bad_request", "frame is required");
      // 帧不再登记窗口表(嵌套后路由走索引路径):合法性由 id 形状决定。
      routeOf(id);
      state.activeFrame = id;
      return { frames: [], active: id };
    }
    if (action === "main") {
      state.activeFrame = null;
      return { frames: [], active: null };
    }
    if (action === "current") return { frames: [], active: state.activeFrame };
    throw err("bad_request", "unknown frames action: " + action);
  }

  async function invoke(payload, token) {
    try {
      const op = payload && payload.op;
      const args = (payload && payload.args) || {};
      // Only calls made through Go's EvalContext carry the frame token. Page
      // scripts can reach window.__browserUse.invoke; without the token they
      // must not cross an origin boundary or toggle the input mask.
      const trusted = typeof token === "string" && token === FRAME_TOKEN;
      if (op === "frames") {
        if (!trusted) throw err("forbidden", "frame control is not available to page scripts");
        if (!isTopFrame) throw err("no_frame", "frames is only available in the top frame");
        return JSON.stringify({ ok: true, ...(await frames(args)) });
      }
      if (isTopFrame && state.activeFrame && RELAY_OPS.has(op)) {
        if (!trusted) throw err("forbidden", "frame relay is not available to page scripts");
        try {
          return await frameCommand(op, args);
        } catch (e) {
          return JSON.stringify({
            ok: false,
            error: { code: e.code || "frame_error", message: e.message || String(e), hint: e.hint || "" },
          });
        }
      }
      switch (op) {
        case "snapshot": return JSON.stringify({ ok: true, ...snapshot(args) });
        case "read": return JSON.stringify({ ok: true, ...readPage(args) });
        case "click": return JSON.stringify({ ok: true, ...(await click(args.target, args)) });
        case "type": return JSON.stringify({ ok: true, ...(await typeText(args.target, args.text, args)) });
        case "press": return JSON.stringify({ ok: true, ...(await pressKey(args.key, args)) });
        case "select": return JSON.stringify({ ok: true, ...(await selectOption(args.target, args.values)) });
        case "scroll": return JSON.stringify({ ok: true, ...(await scroll(args)) });
        case "hover": return JSON.stringify({ ok: true, ...(await hover(args.target)) });
        case "query": return JSON.stringify({ ok: true, ...query(args) });
        case "info": return JSON.stringify({ ok: true, url: location.href, title: document.title, readyState: document.readyState, nav: state.navCount, scroll: { x: window.scrollX, y: window.scrollY }, viewport: { w: window.innerWidth, h: window.innerHeight, dpr: window.devicePixelRatio || 1 } });
        case "setInputLock":
          if (!trusted) throw err("forbidden", "the input mask is controlled by the harness");
          return JSON.stringify({ ok: true, ...setInputLock(args.on) });
        case "dialogs": return JSON.stringify({ ok: true, ...dialogQuery(args) });
        case "network": return JSON.stringify({ ok: true, ...(await network(args)) });
        case "console": return JSON.stringify({ ok: true, ...(await consoleQuery(args)) });
        case "upload": return JSON.stringify({ ok: true, ...(await upload(args)) });
        case "fullPage": return JSON.stringify({ ok: true, ...fullPage(args) });
        case "drag": return JSON.stringify({ ok: true, ...(await drag(args)) });
        case "storage": return JSON.stringify({ ok: true, ...storage(args) });
        case "humanBlocked": return JSON.stringify({ ok: true, ...humanBlocked(args) });
        default: throw err("bad_request", "unknown op: " + op);
      }
    } catch (e) {
      return JSON.stringify({
        ok: false,
        error: { code: e.code || "runtime_error", message: e.message || String(e), hint: e.hint || "", details: e.details || null },
      });
    }
  }

  installDialogs();
  installNetwork();
  installConsole();
  installFrameRelay();
  installNav();

  Object.defineProperty(window, "__browserUse", {
    configurable: true,
    value: Object.freeze({
      version: VERSION,
      invoke,
      snapshot: (args) => snapshot(args || {}),
      dialogs: (args) => dialogQuery(args || {}),
      network: (args) => network(args || {}),
      console: (args) => consoleQuery(args || {}),
      upload: (args) => upload(args || {}),
    }),
  });
})();
