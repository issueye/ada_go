/* browser-use page runtime. Injected at document start, before the page's scripts. */
(() => {
  "use strict";

  const VERSION = "0.2.0";
  if (window.__browserUse && window.__browserUse.version === VERSION) return;
  const isTopFrame = window === window.top;

  const RELAY_OPS = new Set([
    "snapshot", "read", "click", "type", "press", "select", "scroll",
    "hover", "query", "info", "dialogs", "network", "upload",
  ]);

  const state = {
    version: VERSION,
    seq: 0,
    snapSeq: 0,
    refs: new Map(),          // ref -> {ref, el: WeakRef, fp}
    byEl: new WeakMap(),      // element -> ref
    inputLocked: false,
    bypass: false,
    lockHandler: null,
    dialogs: [],
    netSeq: 0,
    netEntries: [],
    netDropped: 0,
    netBytes: 0,
    activeFrame: null,
    frameWindows: new Map(),
    framePongs: new Map(),
    framePending: new Map(),
    frameSeq: 0,
  };

  const MAX_ELEMENTS = 8000;
  const MAX_NODES = 400;
  const NET_MAX_ENTRIES = 300;
  const NET_MAX_REQUEST = 16 * 1024;
  const NET_MAX_RESPONSE = 128 * 1024;
  const NET_MAX_TOTAL = 4 * 1024 * 1024;
  const NET_SECRET = /(password|passwd|pwd|token|secret|authorization|cookie|session)/i;

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

  function fpOf(el) {
    return {
      tag: (el.tagName || "").toLowerCase(),
      role: roleOf(el) || "",
      name: clip(nameOf(el), 120),
      id: el.id || "",
      type: (el.getAttribute && el.getAttribute("type")) || "",
    };
  }

  function refFor(el) {
    const old = state.byEl.get(el);
    if (old) {
      const rec = state.refs.get(old);
      if (rec && (!rec.el || rec.el.deref() === el)) return old;
    }
    const ref = "e" + (++state.seq);
    const rec = { ref, el: typeof WeakRef === "function" ? new WeakRef(el) : { deref: () => el }, fp: fpOf(el) };
    state.refs.set(ref, rec);
    state.byEl.set(el, ref);
    return ref;
  }

  function deref(ref) {
    const rec = state.refs.get(ref);
    if (!rec) return null;
    const el = rec.el && rec.el.deref ? rec.el.deref() : null;
    if (el && el.isConnected) return el;
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

  function parseTextLocator(spec) {
    let s = spec;
    let exact = false;
    if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
      exact = true;
      s = s.slice(1, -1);
    }
    s = norm(s).toLowerCase();
    const all = deepQueryAll(document, "a,button,input,select,textarea,summary,label,[role],[tabindex],[contenteditable]");
    const matches = all.filter((el) => {
      const role = roleOf(el);
      if (!isInteractive(el, role)) return false;
      const text = norm(el.innerText || el.textContent || nameOf(el)).toLowerCase();
      return exact ? text === s : text.includes(s);
    });
    return matches;
  }

  function resolveLocator(spec) {
    if (spec == null) throw err("bad_request", "no target");
    if (typeof spec === "object") {
      if (spec.ref) return resolveRef(spec.ref);
      if (spec.selector) return resolveLocator(String(spec.selector));
      if (spec.text != null) {
        const matches = parseTextLocator(String(spec.text));
        return unique(matches, "text=" + spec.text);
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
      const matches = parseTextLocator(s.slice(5));
      return unique(matches, s);
    }
    if (s.startsWith("role=")) {
      return unique(parseRoleLocator(s.slice(5)), s);
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

  function unique(matches, spec) {
    if (!matches || matches.length === 0) throw err("not_found", "no element matches " + spec);
    if (matches.length > 1) {
      const candidates = matches.slice(0, 5).map((el) => {
        const r = el.getBoundingClientRect();
        return { ref: refFor(el), role: roleOf(el) || el.tagName.toLowerCase(), name: clip(nameOf(el), 80), rect: { x: r.left, y: r.top, w: r.width, h: r.height } };
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
      const step = () => (--left <= 0 ? resolve() : requestAnimationFrame(step));
      requestAnimationFrame(step);
    });
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
    const resolved = resolveLocator(target);
    let el = resolved.el;
    el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
    await frames(2);
    let restoreReveal = null;
    if (!isVisible(el) && opts.reveal !== false && (resolved.point || opts.reveal)) {
      restoreReveal = revealAncestors(el);
      await frames(1);
      if (resolved.point) {
        const at = elementAt(resolved.point);
        if (at) el = at;
      }
    }
    if (!isVisible(el)) throw err("not_visible", "element is not visible", "scroll or wait for it to show");
    if (isDisabled(el)) throw err("not_enabled", "element is disabled", "wait for it to become enabled");
    const c = centerOf(el);
    if (!opts.force) {
      const hit = document.elementFromPoint(c.x, c.y);
      if (hit && hit !== el && !el.contains(hit) && !hit.contains(el)) {
        throw err("covered", "element is covered by <" + hit.tagName.toLowerCase() + ">", "remove the overlay or use force");
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
      if (button === 2) {
        el.dispatchEvent(new MouseEvent("contextmenu", { ...base, buttons: 0 }));
      } else if (opts.count === 2) {
        el.click();
        el.dispatchEvent(new MouseEvent("dblclick", { ...base, detail: 2 }));
      } else {
        el.click();
      }
    });
    if (restoreReveal) setTimeout(restoreReveal, 300);
    return { clicked: info, url: location.href, title: document.title, revealed: !!restoreReveal };
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
    const resolved = resolveLocator(target);
    const el = resolved.el;
    if (!isEditable(el)) throw err("not_editable", "element does not accept text", "use click/select for this element");
    el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
    await frames(1);
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
    const resolved = resolveLocator(target);
    const el = resolved.el;
    const tag = (el.tagName || "").toLowerCase();
    if (tag !== "select") throw err("not_select", "element is not a <select>");
    const wanted = Array.isArray(values) ? values.map(String) : [String(values == null ? "" : values)];
    const opts = [...el.options];
    const chosen = [];
    for (const want of wanted) {
      const o = opts.find((o) => o.value === want) || opts.find((o) => norm(o.textContent) === norm(want));
      if (!o) throw err("not_found", "no option matches " + want);
      chosen.push(o);
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
    if (opts.to) {
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
      role: roleOf(el) || (el.tagName || "").toLowerCase(),
      name: clip(nameOf(el), 160),
      rect: { x: r.left, y: r.top, w: r.width, h: r.height },
      tag: (el.tagName || "").toLowerCase(),
    };
  }

  function snapshot(args) {
    args = args || {};
    const maxChars = Math.min(Math.max(Number(args.max_chars || 12000), 1000), 50000);
    const filter = norm(args.filter).toLowerCase();
    const scope = args.scope || "full";
    const viewport = { x: 0, y: 0, w: window.innerWidth, h: window.innerHeight };
    const nodes = [];
    let visited = 0;

    function intersects(r) {
      return r.left < viewport.w && r.top < viewport.h && r.right > 0 && r.bottom > 0;
    }

    function visit(el, depth) {
      if (!el || depth > 120 || visited >= MAX_ELEMENTS || nodes.length >= MAX_NODES) return;
      visited++;
      if (el.nodeType === 1) {
        const tag = (el.tagName || "").toLowerCase();
        if (SKIP_TAGS.has(tag)) return;
        const cs = getComputedStyle(el);
        if (cs.display === "none") return;
        const role = roleOf(el);
        const interactive = isInteractive(el, role);
        const heading = /^h[1-6]$/.test(tag);
        const landmark = role && LANDMARK_ROLES.has(role);
        const img = tag === "img" && el.getAttribute("alt");
        const iframe = tag === "iframe";
        const dialog = role === "dialog" || role === "alertdialog";
        const texty = tag === "p" || tag === "li" || tag === "td" || tag === "th" || tag === "pre" || tag === "blockquote";
        const want = interactive || heading || landmark || img || iframe || dialog || (texty && args.include_text);
        if (want) {
          const visible = isVisible(el);
          if (visible) {
            const r = el.getBoundingClientRect();
            if (scope !== "viewport" || intersects(r)) {
              const name = clip(nameOf(el), 160);
              const hay = (role || tag) + " " + name + " " + tag;
              if (!filter || hay.toLowerCase().includes(filter)) {
                nodes.push({
                  ref: refFor(el), role: role || tag, name, tag,
                  value: valueOf(el), href: hrefOf(el), disabled: isDisabled(el),
                  checked: el.checked === true, rect: { x: r.left, y: r.top, w: r.width, h: r.height },
                  inViewport: intersects(r), texty,
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
    const header = "snapshot v" + snapVersion + "  url=" + location.href + "  title=" + clip(document.title, 120) +
      "\nviewport " + window.innerWidth + "x" + window.innerHeight + "  scroll=(" + Math.round(window.scrollX) + "," + Math.round(window.scrollY) + ")" +
      "  page=" + Math.round(document.documentElement.scrollWidth) + "x" + Math.round(document.documentElement.scrollHeight) +
      "  dpr=" + (window.devicePixelRatio || 1) + "  refs=" + nodes.length + "  nodes=" + visited;
    const lines = [header];
    let used = header.length;
    let truncated = false;
    let shown = 0;
    for (const n of nodes) {
      let line = "[" + n.ref + "] " + n.role;
      if (n.name) line += ' "' + n.name.replace(/"/g, '\\"') + '"';
      if (n.value) line += " value=" + JSON.stringify(n.value);
      if (n.href) line += " href=" + n.href;
      if (n.checked) line += " checked";
      if (n.disabled) line += " disabled";
      if (!n.inViewport) line += " offscreen";
      if (used + line.length + 1 > maxChars) { truncated = true; break; }
      lines.push(line);
      used += line.length + 1;
      shown++;
    }
    if (truncated && nodes.length > shown) lines.push("[truncated: " + (nodes.length - shown) + " more nodes; narrow with filter/scope]");
    return {
      text: lines.join("\n"),
      nodes: nodes.map((n) => ({ ref: n.ref, role: n.role, name: n.name, rect: n.rect })),
      version: snapVersion,
      truncated,
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
    let matches;
    try {
      if (args.text != null && !args.target && !args.selector) matches = parseTextLocator(String(args.text));
      else matches = [resolveLocator(spec).el];
    } catch (e) {
      if (e.code === "not_found") return { count: 0, found: false };
      throw e;
    }
    if (matches.length === 0) return { count: 0, found: false };
    const stateWant = args.state || "visible";
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
    const text = String(root ? root.innerText || root.textContent || "" : "")
      .replace(/\r\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
    const max = Math.min(Math.max(Number(args.max_chars || 12000), 1000), 50000);
    const truncated = text.length > max;
    return {
      content: truncated ? text.slice(0, max) + "\n… [truncated]" : text,
      truncated,
      url: location.href,
      title: document.title,
      chars: text.length,
    };
  }

  function setInputLock(on) {
    on = !!on;
    if (on === state.inputLocked) return { locked: on };
    state.inputLocked = on;
    if (on) {
      if (!state.lockHandler) {
        state.lockHandler = (ev) => {
          if (!state.inputLocked || state.bypass) return;
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

  function recordDialog(type, message, defaultValue) {
    state.dialogs.push({ type, message: clip(message, 500), defaultValue: defaultValue == null ? null : clip(defaultValue, 200), ts: Date.now() });
    if (state.dialogs.length > 50) state.dialogs.shift();
  }

  function installDialogs() {
    try {
      Object.defineProperty(window, "alert", {
        configurable: true, writable: true,
        value: (message) => { recordDialog("alert", message, null); },
      });
      Object.defineProperty(window, "confirm", {
        configurable: true, writable: true,
        value: (message) => { recordDialog("confirm", message, null); return false; },
      });
      Object.defineProperty(window, "prompt", {
        configurable: true, writable: true,
        value: (message, def) => { recordDialog("prompt", message, def); return def == null ? null : String(def); },
      });
    } catch (e) { /* ignore */ }
  }

  function postFrame(win, msg) {
    try { win.postMessage(msg, "*"); } catch (e) { /* frame gone */ }
  }

  function installFrameRelay() {
    if (isTopFrame) {
      window.addEventListener("message", (ev) => {
        const d = ev.data;
        if (!d || !d.__buFrame) return;
        if (d.__buFrame === "pong") {
          state.framePongs.set(d.token, { url: d.url || "", title: d.title || "" });
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
      if (!d || !d.__buFrame || ev.source !== window.parent) return;
      if (d.__buFrame === "ping") {
        try {
          ev.source.postMessage({
            __buFrame: "pong", token: d.token,
            url: location.href, title: document.title,
          }, "*");
        } catch (e) { /* ignore */ }
        return;
      }
      if (d.__buFrame === "cmd") {
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
      }
    });
  }

  async function listFrames() {
    const total = window.frames.length;
    state.framePongs = new Map();
    const tokens = [];
    for (let i = 0; i < total; i++) {
      const token = "f" + (++state.frameSeq);
      const win = window.frames[i];
      tokens.push({ token, win });
      state.frameWindows.set(token, win);
      postFrame(win, { __buFrame: "ping", token });
    }
    await new Promise((r) => setTimeout(r, 250));
    const out = [];
    for (const t of tokens) {
      const info = state.framePongs.get(t.token);
      if (info) out.push({ id: t.token, url: info.url, title: info.title });
    }
    return { frames: out, total, active: state.activeFrame };
  }

  async function frameCommand(op, args) {
    const token = state.activeFrame;
    if (!token) throw err("no_frame", "no active frame");
    const win = state.frameWindows.get(token);
    if (!win) throw err("no_frame", "frame is gone; run frames list");
    const id = "r" + (++state.frameSeq);
    return await new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        state.framePending.delete(id);
        reject(err("frame_timeout", "frame did not answer"));
      }, 30000);
      state.framePending.set(id, { resolve, timer });
      postFrame(win, { __buFrame: "cmd", id, token, op, args: args || {} });
    });
  }

  async function frames(args) {
    args = args || {};
    const action = args.action || "list";
    if (action === "list") return await listFrames();
    if (action === "use") {
      const id = String(args.frame || args.id || "");
      if (!id) throw err("bad_request", "frame is required");
      if (!state.frameWindows.has(id)) throw err("no_frame", "unknown frame " + id);
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

  async function invoke(payload) {
    try {
      const op = payload && payload.op;
      const args = (payload && payload.args) || {};
      if (isTopFrame && op === "frames") {
        return JSON.stringify({ ok: true, ...(await frames(args)) });
      }
      if (isTopFrame && state.activeFrame && RELAY_OPS.has(op)) {
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
        case "info": return JSON.stringify({ ok: true, url: location.href, title: document.title, readyState: document.readyState, scroll: { x: window.scrollX, y: window.scrollY }, viewport: { w: window.innerWidth, h: window.innerHeight, dpr: window.devicePixelRatio || 1 } });
        case "setInputLock": return JSON.stringify({ ok: true, ...setInputLock(args.on) });
        case "dialogs": return JSON.stringify({ ok: true, dialogs: state.dialogs.slice() });
        case "network": return JSON.stringify({ ok: true, ...network(args) });
        case "upload": return JSON.stringify({ ok: true, ...(await upload(args)) });
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
  installFrameRelay();

  Object.defineProperty(window, "__browserUse", {
    configurable: true,
    value: Object.freeze({
      version: VERSION,
      invoke,
      snapshot: (args) => snapshot(args || {}),
      setInputLock,
      dialogs: () => state.dialogs.slice(),
      network: (args) => network(args || {}),
      upload: (args) => upload(args || {}),
      frames: (args) => frames(args || {}),
    }),
  });
})();
