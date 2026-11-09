// generated from client.tsx (sha256:c3ebf4fbea98f05d) by tools/pluginbuild — do not edit
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

// composer-footer.tsx
var RING_CIRCUMFERENCE = 2 * Math.PI * 15;
var POLL_INTERVAL_MS = 2e3;
var TERMINAL_STATUSES = { terminal: true };
var ACTIVE_STATUSES = { running: true, finishing: true };
var RUN_LABELS = {
  running: "运行中",
  waiting_input: "等待答复",
  finishing: "收尾中"
};
function compactTokenCount(n) {
  if (!isFinite(n) || n < 0) n = 0;
  if (n >= 1e6) {
    const v = n / 1e6;
    return (v >= 10 ? String(Math.round(v)) : trimZero(v.toFixed(1))) + "M";
  }
  if (n >= 1e3) {
    const v = n / 1e3;
    return (v >= 10 ? String(Math.round(v)) : trimZero(v.toFixed(1))) + "K";
  }
  return String(Math.round(n));
}
function trimZero(text2) {
  return text2.endsWith(".0") ? text2.slice(0, -2) : text2;
}
function percentOf(part, total) {
  if (!isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round(part * 100 / total)));
}
function dashOffset(percent) {
  return (RING_CIRCUMFERENCE * (1 - percent / 100)).toFixed(2);
}
function num(value) {
  const n = Number(value);
  return isFinite(n) ? n : 0;
}
function contextView(ctx) {
  if (!ctx || typeof ctx !== "object") return null;
  const assumed = num(ctx.window) <= 0;
  const windowTokens = assumed ? 1048576 : num(ctx.window);
  const tokens = num(ctx.tokens);
  const percent = assumed ? percentOf(tokens, windowTokens) : Math.max(0, Math.min(100, num(ctx.percent)));
  let messages = num(ctx.messages);
  if (messages === 0 && num(ctx.system) === 0 && num(ctx.tools) === 0) messages = tokens;
  const messagePercent = percentOf(messages, windowTokens);
  let hint = "";
  if (assumed) hint = "模型未声明窗口，占比按 1M 估算。";
  else if (messagePercent >= 80) hint = "对话消息已到压缩水位，下一轮会先压缩历史。";
  else if (percent >= 80) hint = "接近窗口上限，建议压缩历史或新开会话。";
  return {
    percent,
    high: percent >= 80,
    tokens: compactTokenCount(tokens),
    window: compactTokenCount(windowTokens),
    assumed,
    system: compactTokenCount(num(ctx.system)),
    tools: compactTokenCount(num(ctx.tools)),
    messages: compactTokenCount(messages),
    barOffset: (100 - percent).toFixed(2),
    hint
  };
}
function cacheHit(usage) {
  const thread = usage && usage.thread ? usage.thread : null;
  if (!thread) return null;
  const input = num(thread.inputTokens);
  if (input <= 0) return "—";
  return percentOf(num(thread.cacheTokens), input) + "%";
}
var composer_footer_default = defineComponent({
  props: { app: { default: {} }, data: { default: {} } },
  data() {
    const props = this.$props || {};
    const thread = props.data && props.data.thread || {};
    return {
      threadId: String(thread.id || ""),
      ready: false,
      run: null,
      context: null,
      usage: null,
      timer: 0,
      inflight: false,
      lastError: ""
    };
  },
  computed: {
    workspaceName() {
      const app = this.app || {};
      return String(app.workspaceName || app.workspace || "");
    },
    runLabel() {
      const status = this.run && this.run.status ? String(this.run.status) : "";
      return RUN_LABELS[status] || "";
    },
    runActive() {
      const status = this.run && this.run.status ? String(this.run.status) : "";
      return !!ACTIVE_STATUSES[status];
    },
    contextInfo() {
      return contextView(this.context);
    },
    cacheLabel() {
      return cacheHit(this.usage);
    }
  },
  methods: {
    endpoint() {
      if (!this.threadId) return "";
      return "/threads/" + encodeURIComponent(this.threadId) + "/state";
    },
    usageEndpoint() {
      if (!this.threadId) return "";
      return "/ext/composer-telemetry/api/usage?threadId=" + encodeURIComponent(this.threadId);
    },
    refresh() {
      const url = this.endpoint();
      const usageUrl = this.usageEndpoint();
      if (!url || this.inflight || typeof window.fetch !== "function") return;
      this.inflight = true;
      const get = (target) => window.fetch(target, { credentials: "same-origin", headers: { Accept: "application/json" } }).then((res) => res.ok ? res.json() : null);
      Promise.all([get(url), usageUrl ? get(usageUrl) : Promise.resolve(null)]).then(([body, usage]) => {
        this.inflight = false;
        if (body && typeof body === "object") {
          this.run = body.run || null;
          this.context = body.context || null;
          if (usage && typeof usage === "object") this.usage = usage;
          this.ready = true;
        } else {
          this.lastError = "读取会话遥测失败";
        }
        this.schedule();
      }).catch(() => {
        this.inflight = false;
        this.lastError = "读取会话遥测失败";
        this.schedule();
      });
    },
    schedule() {
      const terminal = this.run && TERMINAL_STATUSES[String(this.run.status || "")];
      if (terminal) {
        this.stop();
        return;
      }
      if (this.timer) return;
      this.timer = window.setInterval(() => this.refresh(), POLL_INTERVAL_MS);
    },
    stop() {
      if (this.timer) {
        window.clearInterval(this.timer);
        this.timer = 0;
      }
    }
  },
  mounted() {
    this.refresh();
  },
  unmounted() {
    this.stop();
  },
  render(h3) {
    if (!this.ready) {
      return h3("span", { class: "ada-telemetry__pending", "aria-hidden": "true" });
    }
    const group = [];
    if (this.runActive) {
      group.push(
        h3("span", { class: "ada-aux__item ada-aux__item--brand" }, [
          h3("svg", { class: "ada-icon", width: 12, height: 12, "aria-hidden": "true" }, [
            h3("use", { href: "#i-wave" })
          ]),
          h3("span", null, this.runLabel)
        ])
      );
    }
    if (this.workspaceName) {
      group.push(
        h3("span", { class: "ada-aux__item ada-aux__item--neutral" }, [
          h3("span", null, "工作区：" + this.workspaceName)
        ])
      );
    }
    const actions = [];
    if (this.cacheLabel) {
      actions.push(
        h3(
          "span",
          { class: "ada-aux__cache", title: "缓存命中率：输入 token 中命中缓存的比例" },
          [
            h3("svg", { class: "ada-icon", width: 12, height: 12, "aria-hidden": "true" }, [
              h3("use", { href: "#i-database" })
            ]),
            h3("span", null, "缓存命中 " + this.cacheLabel)
          ]
        )
      );
    }
    const ctx = this.contextInfo;
    if (ctx) {
      const ringLabel = "上下文已用 " + ctx.percent + "% · 约 " + ctx.tokens + " / " + ctx.window;
      actions.push(
        h3("details", { class: "ada-context" }, [
          h3(
            "summary",
            { class: "ada-token-ring", "aria-label": ringLabel },
            [
              h3(
                "svg",
                {
                  class: "ada-token-ring__svg",
                  viewBox: "0 0 36 36",
                  "aria-hidden": "true"
                },
                [
                  h3("circle", {
                    class: "ada-token-ring__track",
                    cx: 18,
                    cy: 18,
                    r: 15
                  }),
                  h3("circle", {
                    class: "ada-token-ring__value" + (ctx.high ? " is-high" : ""),
                    cx: 18,
                    cy: 18,
                    r: 15,
                    transform: "rotate(-90 18 18)",
                    "stroke-dasharray": RING_CIRCUMFERENCE.toFixed(2),
                    "stroke-dashoffset": dashOffset(ctx.percent)
                  })
                ]
              ),
              h3("span", { class: "ada-token-ring__label" }, ctx.percent + "%")
            ]
          ),
          h3("div", { class: "ada-context__panel", role: "group", "aria-label": "上下文用量明细" }, [
            h3("p", { class: "ada-context__head" }, [
              h3("span", { class: "ada-context__head-main" }, "上下文已用 " + ctx.percent + "%"),
              h3("span", { class: "ada-context__head-total" }, "约 " + ctx.tokens + " / " + ctx.window)
            ]),
            h3(
              "svg",
              {
                class: "ada-context__bar",
                viewBox: "0 0 100 4",
                preserveAspectRatio: "none",
                "aria-hidden": "true"
              },
              [
                h3("line", { class: "ada-context__bar-track", x1: 0, y1: 2, x2: 100, y2: 2 }),
                h3("line", {
                  class: "ada-context__bar-value" + (ctx.high ? " is-high" : ""),
                  x1: 0,
                  y1: 2,
                  x2: 100,
                  y2: 2,
                  "stroke-dasharray": "100",
                  "stroke-dashoffset": ctx.barOffset
                })
              ]
            ),
            h3("ul", { class: "ada-context__list" }, [
              contextRow(h3, "system", "系统提示词", ctx.system),
              contextRow(h3, "tools", "工具定义", ctx.tools),
              contextRow(h3, "messages", "对话消息", ctx.messages)
            ]),
            ctx.hint ? h3("p", { class: "ada-context__hint" }, ctx.hint) : null
          ])
        ])
      );
    }
    return h3("div", { class: "ada-aux" }, [
      h3("span", { class: "ada-aux__group" }, group),
      h3("div", { class: "ada-aux__actions" }, actions)
    ]);
  }
});
function contextRow(h3, dot, name, value) {
  return h3("li", { class: "ada-context__row" }, [
    h3("span", { class: "ada-context__dot ada-context__dot--" + dot, "aria-hidden": "true" }),
    h3("span", { class: "ada-context__name" }, name),
    h3("span", { class: "ada-context__value" }, "约 " + value)
  ]);
}

// client.tsx
var islands = {
  "slot:composer.footer": composer_footer_default
};
var client_default = composer_footer_default;
export {
  client_default as default,
  islands
};
