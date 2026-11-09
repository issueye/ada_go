/**
 * 输入框下方辅助区（official/composer-telemetry 的前端段）：composer.footer 纯客户端岛。
 *
 * 服务端不渲染这个模板（清单里 slot 标了 clientOnly），浏览器用 static/client.js
 * 普通 mount 接管空壳。数据来自基座通用投影 GET /threads/{id}/state：
 * `{ run, context, usage }` —— 与 SSR 页面用的是同一份 Core 调用，插件不自己算 token。
 *
 * 运行中每 2s 轮询一次，进入终态即停（终态后基座会整页 reload，那时再取一次新值）。
 * 没有 SSR 回退：插件停用或脚本加载失败时，辅助区保持空白。
 */
import { h, defineComponent } from "ada:render";

// 与基座 buildContextView 同源的口径：环半径 15，周长 2πr；dash 值走 SVG 属性
// （CSP 禁内联 style，所以不写 style=""）。
const RING_CIRCUMFERENCE = 2 * Math.PI * 15;
const POLL_INTERVAL_MS = 2000;
const TERMINAL_STATUSES = { terminal: true };
const ACTIVE_STATUSES = { running: true, finishing: true };

const RUN_LABELS: Record<string, string> = {
  running: "运行中",
  waiting_input: "等待答复",
  finishing: "收尾中",
};

// compactTokenCount 与基座展示口径一致：≥10 取整，否则保留 1 位小数并去掉 ".0"。
function compactTokenCount(n: number): string {
  if (!isFinite(n) || n < 0) n = 0;
  if (n >= 1000000) {
    const v = n / 1000000;
    return (v >= 10 ? String(Math.round(v)) : trimZero(v.toFixed(1))) + "M";
  }
  if (n >= 1000) {
    const v = n / 1000;
    return (v >= 10 ? String(Math.round(v)) : trimZero(v.toFixed(1))) + "K";
  }
  return String(Math.round(n));
}

function trimZero(text: string): string {
  return text.endsWith(".0") ? text.slice(0, -2) : text;
}

function percentOf(part: number, total: number): number {
  if (!isFinite(total) || total <= 0) return 0;
  return Math.max(0, Math.min(100, Math.round((part * 100) / total)));
}

function dashOffset(percent: number): string {
  return (RING_CIRCUMFERENCE * (1 - percent / 100)).toFixed(2);
}

function num(value: any): number {
  const n = Number(value);
  return isFinite(n) ? n : 0;
}

function contextView(ctx: any) {
  if (!ctx || typeof ctx !== "object") return null;
  // 供应商没有声明窗口时按 1M 估算，与基座同一套兜底（文案里也如实标注）。
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
    hint,
  };
}

function cacheHit(usage: any): string | null {
  const thread = usage && usage.thread ? usage.thread : null;
  if (!thread) return null;
  const input = num(thread.inputTokens);
  if (input <= 0) return "—";
  return percentOf(num(thread.cacheTokens), input) + "%";
}

export default defineComponent({
  props: { app: { default: {} }, data: { default: {} } },
  data() {
    // props 名 data 与组件自身的 data() 同名：这里必须读 $props，否则拿到的是
    // 实例的响应式 data 对象而不是桥注入的投影。
    const props = (this as any).$props || {};
    const thread = (props.data && props.data.thread) || {};
    return {
      threadId: String(thread.id || ""),
      ready: false,
      run: null as any,
      context: null as any,
      usage: null as any,
      timer: 0 as any,
      inflight: false,
      lastError: "",
    };
  },
  computed: {
    workspaceName(): string {
      const app = this.app || {};
      return String(app.workspaceName || app.workspace || "");
    },
    runLabel(): string {
      const status = this.run && this.run.status ? String(this.run.status) : "";
      return RUN_LABELS[status] || "";
    },
    runActive(): boolean {
      const status = this.run && this.run.status ? String(this.run.status) : "";
      return !!ACTIVE_STATUSES[status];
    },
    contextInfo(): any {
      return contextView(this.context);
    },
    cacheLabel(): string | null {
      return cacheHit(this.usage);
    },
  },
  methods: {
    endpoint(): string {
      if (!this.threadId) return "";
      return "/threads/" + encodeURIComponent(this.threadId) + "/state";
    },
    usageEndpoint(): string {
      if (!this.threadId) return "";
      return "/ext/composer-telemetry/api/usage?threadId=" + encodeURIComponent(this.threadId);
    },
    refresh() {
      const url = this.endpoint();
      const usageUrl = this.usageEndpoint();
      if (!url || this.inflight || typeof window.fetch !== "function") return;
      this.inflight = true;
      const get = (target: string) =>
        window
          .fetch(target, { credentials: "same-origin", headers: { Accept: "application/json" } })
          .then((res: any) => (res.ok ? res.json() : null));
      Promise.all([get(url), usageUrl ? get(usageUrl) : Promise.resolve(null)])
        .then(([body, usage]: any[]) => {
          this.inflight = false;
          if (body && typeof body === "object") {
            this.run = body.run || null;
            this.context = body.context || null;
            // 用量来自插件自己的 route；失败时保持上一次的值。
            if (usage && typeof usage === "object") this.usage = usage;
            this.ready = true;
          } else {
            // 非 2xx 或空响应：保持上一次的值（首帧则仍是占位根），下个周期再试。
            this.lastError = "读取会话遥测失败";
          }
          this.schedule();
        })
        .catch(() => {
          this.inflight = false;
          this.lastError = "读取会话遥测失败";
          // 失败不打断页面：保持上一次的值，并在下一个周期再试。
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
    },
  },
  mounted() {
    // 先取一次：首屏空壳紧接着就有值；轮询只在运行中继续。
    this.refresh();
  },
  unmounted() {
    this.stop();
  },
  render(h: any) {
    if (!this.ready) {
      // 占位根的显示在 telemetry.css 里置为 none：取数之前不占版面。
      return h("span", { class: "ada-telemetry__pending", "aria-hidden": "true" });
    }
    const group: any[] = [];
    if (this.runActive) {
      group.push(
        h("span", { class: "ada-aux__item ada-aux__item--brand" }, [
          h("svg", { class: "ada-icon", width: 12, height: 12, "aria-hidden": "true" }, [
            h("use", { href: "#i-wave" }),
          ]),
          h("span", null, this.runLabel),
        ])
      );
    }
    if (this.workspaceName) {
      group.push(
        h("span", { class: "ada-aux__item ada-aux__item--neutral" }, [
          h("span", null, "工作区：" + this.workspaceName),
        ])
      );
    }

    const actions: any[] = [];
    if (this.cacheLabel) {
      actions.push(
        h(
          "span",
          { class: "ada-aux__cache", title: "缓存命中率：输入 token 中命中缓存的比例" },
          [
            h("svg", { class: "ada-icon", width: 12, height: 12, "aria-hidden": "true" }, [
              h("use", { href: "#i-database" }),
            ]),
            h("span", null, "缓存命中 " + this.cacheLabel),
          ]
        )
      );
    }
    const ctx = this.contextInfo;
    if (ctx) {
      const ringLabel = "上下文已用 " + ctx.percent + "% · 约 " + ctx.tokens + " / " + ctx.window;
      actions.push(
        h("details", { class: "ada-context" }, [
          h(
            "summary",
            { class: "ada-token-ring", "aria-label": ringLabel },
            [
              h(
                "svg",
                {
                  class: "ada-token-ring__svg",
                  viewBox: "0 0 36 36",
                  "aria-hidden": "true",
                },
                [
                  h("circle", {
                    class: "ada-token-ring__track",
                    cx: 18,
                    cy: 18,
                    r: 15,
                  }),
                  h("circle", {
                    class: "ada-token-ring__value" + (ctx.high ? " is-high" : ""),
                    cx: 18,
                    cy: 18,
                    r: 15,
                    transform: "rotate(-90 18 18)",
                    "stroke-dasharray": RING_CIRCUMFERENCE.toFixed(2),
                    "stroke-dashoffset": dashOffset(ctx.percent),
                  }),
                ]
              ),
              h("span", { class: "ada-token-ring__label" }, ctx.percent + "%"),
            ]
          ),
          h("div", { class: "ada-context__panel", role: "group", "aria-label": "上下文用量明细" }, [
            h("p", { class: "ada-context__head" }, [
              h("span", { class: "ada-context__head-main" }, "上下文已用 " + ctx.percent + "%"),
              h("span", { class: "ada-context__head-total" }, "约 " + ctx.tokens + " / " + ctx.window),
            ]),
            h(
              "svg",
              {
                class: "ada-context__bar",
                viewBox: "0 0 100 4",
                preserveAspectRatio: "none",
                "aria-hidden": "true",
              },
              [
                h("line", { class: "ada-context__bar-track", x1: 0, y1: 2, x2: 100, y2: 2 }),
                h("line", {
                  class: "ada-context__bar-value" + (ctx.high ? " is-high" : ""),
                  x1: 0,
                  y1: 2,
                  x2: 100,
                  y2: 2,
                  "stroke-dasharray": "100",
                  "stroke-dashoffset": ctx.barOffset,
                }),
              ]
            ),
            h("ul", { class: "ada-context__list" }, [
              contextRow(h, "system", "系统提示词", ctx.system),
              contextRow(h, "tools", "工具定义", ctx.tools),
              contextRow(h, "messages", "对话消息", ctx.messages),
            ]),
            ctx.hint ? h("p", { class: "ada-context__hint" }, ctx.hint) : null,
          ]),
        ])
      );
    }

    return h("div", { class: "ada-aux" }, [
      h("span", { class: "ada-aux__group" }, group),
      h("div", { class: "ada-aux__actions" }, actions),
    ]);
  },
});

function contextRow(h: any, dot: string, name: string, value: string) {
  return h("li", { class: "ada-context__row" }, [
    h("span", { class: "ada-context__dot ada-context__dot--" + dot, "aria-hidden": "true" }),
    h("span", { class: "ada-context__name" }, name),
    h("span", { class: "ada-context__value" }, "约 " + value),
  ]);
}
