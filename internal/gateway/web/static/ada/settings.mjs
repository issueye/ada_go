// settings.mjs —— 「设置」弹窗（2026-11-16《AI Agent · 桌面客户端》重设计）。
//
// 左侧导航 / 右侧内容两栏，六页：
//   供应商管理（提供 providers 页面的插件只读投影 + 跳转入口）
//   主题设置（明暗 / 色板 / 密度 / 减少动效，全部落到 html 属性与 localStorage）
//   插件管理（/partials/extensions.json + 启停；插件页面入口）
//   通用（本机偏好：恢复上次会话、执行耗时、最后活动时间）
//   关于与更新（产品 / 版本 / 协议 / 网关 / 工作区）
//   数据与隐私（本机存储说明）
//
// 页面状态由 ShellApp 通过 provide("settings") 暴露；侧栏底部「设置」行与
// 顶栏齿轮按钮都调用 settings.open(tab)。主题的实际应用由 themes.js 的
// 事件委托消费 data-theme-mode / data-theme-pick（本组件只写 localStorage +
// 直接 toggle 根类名，两条路径结论一致）。
import {
  h, defineComponent, reactive, ref, computed, onMounted, onUnmounted, inject,
} from "./runtime.mjs";
import { watch } from "./reactivity.mjs";

export const settingsState = reactive({ open: false, tab: "providers" });

export function createSettingsService() {
  return {
    state: settingsState,
    open(tab) {
      if (typeof tab === "string" && tab !== "") settingsState.tab = tab;
      settingsState.open = true;
    },
    close() {
      settingsState.open = false;
    },
  };
}

// ---------------------------------------------------------------------------
// 偏好（通用页）：localStorage 与 html 根类名
// ---------------------------------------------------------------------------

const PREFS_KEY = "ada_prefs";
const DEFAULT_PREFS = {
  restoreSession: true,
  showDurations: true,
  showTimes: true,
};

export function readPrefs() {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (raw === null) return { ...DEFAULT_PREFS };
    const parsed = JSON.parse(raw);
    return { ...DEFAULT_PREFS, ...(parsed && typeof parsed === "object" ? parsed : {}) };
  } catch {
    return { ...DEFAULT_PREFS };
  }
}

export function applyPrefs(prefs) {
  const root = document.documentElement;
  root.classList.toggle("c-hide-durations", !prefs.showDurations);
  root.classList.toggle("c-hide-times", !prefs.showTimes);
  root.classList.toggle("c-reduce-motion", prefs.reduceMotion === true);
  const text = prefs.textSize === "sm" ? "sm" : prefs.textSize === "lg" ? "lg" : "md";
  root.classList.toggle("c-text-sm", text === "sm");
  root.classList.toggle("c-text-lg", text === "lg");
  const density = prefs.density === "compact" ? "compact" : prefs.density === "relaxed" ? "relaxed" : "comfortable";
  root.setAttribute("data-density", density);
}

export function writePrefs(prefs) {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(prefs));
  } catch {
    /* 隐私模式不可用：本次会话内仍然生效 */
  }
  applyPrefs(prefs);
}

// 恢复上次会话：只记住会话详情页（插件页/概览不参与会话恢复）。
export function rememberRoute(path) {
  try {
    if (!/^\/threads\/[^/]+$/.test(String(path ?? ""))) return;
    localStorage.setItem("ada_last_route", path);
  } catch {
    /* 忽略 */
  }
}

export function lastRoute() {
  try {
    return localStorage.getItem("ada_last_route") || "";
  } catch {
    return "";
  }
}

// ---------------------------------------------------------------------------
// 主题（明暗 / 色板）
// ---------------------------------------------------------------------------

const MODE_KEY = "a_da_theme_mode";
const PALETTE_KEY = "a_da_theme_palette";
const LEGACY_KEY = "a_da_theme";

const THEME_MODES = [
  { id: "light", label: "浅色" },
  { id: "dark", label: "深色" },
  { id: "system", label: "跟随系统" },
];
const PALETTES = [
  { id: "azure", label: "蓝海" },
  { id: "black-swan", label: "黑天鹅" },
  { id: "moss", label: "苔绿" },
  { id: "ocean", label: "深海" },
  { id: "dusk", label: "暮色" },
];

function readStore(key) {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}
function writeStore(key, value) {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* 忽略 */
  }
}

function applyMode(mode) {
  const root = document.documentElement;
  if (mode === "system") {
    try { localStorage.removeItem(LEGACY_KEY); } catch { /* 忽略 */ }
    const dark = !!(window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches);
    root.classList.toggle("dark", dark);
    root.classList.toggle("light", !dark);
    return;
  }
  writeStore(LEGACY_KEY, mode);
  root.classList.toggle("dark", mode === "dark");
  root.classList.toggle("light", mode !== "dark");
}

function applyPalette(palette) {
  const root = document.documentElement;
  if (palette === "black-swan") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", palette);
}

// ---------------------------------------------------------------------------
// 弹窗组件
// ---------------------------------------------------------------------------

const TABS = [
  { id: "providers", label: "供应商管理", icon: "i-server", group: "连接与能力" },
  { id: "theme", label: "主题设置", icon: "i-sun", group: "连接与能力" },
  { id: "plugins", label: "插件管理", icon: "i-plug", group: "连接与能力" },
  { id: "general", label: "通用", icon: "i-wrench", group: "其他" },
  { id: "about", label: "关于与更新", icon: "i-circle", group: "其他" },
  { id: "privacy", label: "数据与隐私", icon: "i-shield", group: "其他" },
];

function findProviderSource(registry) {
  // 基座不特判插件 id（1.5.5）：按「提供 providers 页面的插件」动态解析。
  for (const p of registry.store.plugins ?? []) {
    for (const page of p.pages ?? []) {
      if (page.slug === "providers") return { pluginId: p.id, path: page.path };
    }
  }
  return null;
}

function statusTone(plugin) {
  if (plugin.enabled === false) return "muted";
  return "ok";
}

export const SettingsDialog = defineComponent({
  name: "SettingsDialog",
  setup() {
    const fetch = inject("fetch");
    const router = inject("router");
    const bootstrap = inject("bootstrap");
    const registry = inject("registry");
    const confirm = inject("confirmService");
    const toast = inject("toast");

    const state = settingsState;
    const tab = computed(() => state.tab);

    // 供应商（供应商页插件的只读投影；插件停用/未安装时显示空态）
    const providers = ref([]);
    const providersError = ref(null);
    const providersLoaded = ref(false);
    const providerSource = computed(() => findProviderSource(registry));
    const providerPage = computed(() => (providerSource.value === null ? "" : providerSource.value.path));

    // 插件
    const plugins = ref([]);
    const pluginError = ref(null);
    const pluginBusy = ref(false);
    const pluginFilter = ref("all");

    // 主题
    const mode = ref((() => {
      const raw = readStore(MODE_KEY);
      return ["light", "dark", "system"].includes(raw) ? raw : "system";
    })());
    const palette = ref((() => {
      const raw = readStore(PALETTE_KEY);
      return PALETTES.some((p) => p.id === raw) ? raw : "azure";
    })());

    // 偏好
    const prefs = reactive(readPrefs());

    // 关于
    const version = String(bootstrap.version ?? "");
    const revision = String(bootstrap.revision ?? "");
    const built = String(bootstrap.buildTime ?? "");
    const proto = bootstrap.protocolVersion ?? null;

    async function loadProviders() {
      providersError.value = null;
      const src = providerSource.value;
      if (src === null) {
        providers.value = [];
        providersError.value = "还没有找到提供供应商页面的插件。";
        providersLoaded.value = true;
        return;
      }
      try {
        const data = await fetch(`/ext/${encodeURIComponent(src.pluginId)}/api/state`);
        providers.value = Array.isArray(data?.providers) ? data.providers : [];
      } catch (err) {
        providers.value = [];
        providersError.value = err?.message ?? String(err);
      }
      providersLoaded.value = true;
    }

    async function loadPlugins() {
      pluginError.value = null;
      try {
        const data = await fetch("/partials/extensions.json");
        plugins.value = Array.isArray(data?.plugins) ? data.plugins : [];
      } catch (err) {
        plugins.value = [];
        pluginError.value = err?.message ?? String(err);
      }
    }

    const enabledPlugins = computed(() => plugins.value.filter((p) => p.enabled !== false).length);
    const filteredPlugins = computed(() => {
      if (pluginFilter.value === "on") return plugins.value.filter((p) => p.enabled !== false);
      if (pluginFilter.value === "off") return plugins.value.filter((p) => p.enabled === false);
      return plugins.value;
    });
    const navLinks = computed(() => registry.nav());

    const providerProtocolLabel = (protocol) => {
      if (protocol === "openai_chat") return "OpenAI Chat";
      if (protocol === "openai_responses") return "OpenAI Responses";
      if (protocol === "anthropic") return "Anthropic";
      return protocol || "未设置";
    };
    const modelCount = (p) => (Array.isArray(p.models) ? p.models.length : 0);
    const providerInitial = (p) => {
      const name = String(p.displayName || p.id || "?");
      return [...name][0]?.toUpperCase() ?? "?";
    };

    function selectTab(next) {
      state.tab = next;
      if (next === "providers" && !providersLoaded.value) void loadProviders();
      if (next === "plugins" && plugins.value.length === 0) void loadPlugins();
    }
    function setPluginFilter(next) {
      pluginFilter.value = next;
    }

    // 弹窗打开时补齐当前页数据（组件常驻挂载，渲染 null 时不取数）。
    watch(() => state.open, (open) => {
      if (!open) return;
      selectTab(state.tab);
    });
    watch(() => state.tab, (next) => {
      if (!state.open) return;
      selectTab(next);
    });
    // 注册表异步装载：供应商页面的插件晚到时补一次读取。
    watch(providerSource, (src) => {
      if (src === null) return;
      providersLoaded.value = false;
      if (state.open && state.tab === "providers") void loadProviders();
    });

    function goProviderPage() {
      if (providerPage.value === "") {
        toast.show("供应商插件未启用，先在「插件管理」里启用它。", "error");
        return;
      }
      state.open = false;
      router.navigate(providerPage.value);
    }

    async function togglePlugin(p) {
      const verb = p.enabled === false ? "启用" : "停用";
      const ok = await confirm(`${verb}插件「${p.name || p.id}」？${verb}后不再贡献工具与页面，重载插件后生效。`);
      if (!ok) return;
      pluginBusy.value = true;
      try {
        await fetch(`/extensions/${encodeURIComponent(p.id)}/${p.enabled === false ? "enable" : "disable"}`, {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ next: "/" }).toString(),
        });
        await loadPlugins();
      } catch (err) {
        pluginError.value = err?.message ?? String(err);
      } finally {
        pluginBusy.value = false;
      }
    }

    async function reloadPlugins() {
      pluginBusy.value = true;
      try {
        await fetch("/agent-plugins/reload", {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ next: "/" }).toString(),
        });
        await loadPlugins();
      } catch (err) {
        pluginError.value = err?.message ?? String(err);
      } finally {
        pluginBusy.value = false;
      }
    }

    function chooseMode(next) {
      mode.value = next;
      writeStore(MODE_KEY, next);
      applyMode(next);
    }
    function choosePalette(next) {
      palette.value = next;
      writeStore(PALETTE_KEY, next);
      applyPalette(next);
    }
    function setPref(key, value) {
      prefs[key] = value;
      writePrefs(prefs);
    }
    function resetPrefs() {
      Object.assign(prefs, DEFAULT_PREFS);
      writePrefs(prefs);
    }

    const onKey = (event) => {
      if (event.key === "Escape" && state.open) {
        event.preventDefault();
        state.open = false;
      }
    };
    onMounted(() => document.addEventListener("keydown", onKey));
    onUnmounted(() => document.removeEventListener("keydown", onKey));

    return {
      state, tab, TABS, providerPage, providers, providersError, providersLoaded,
      providerProtocolLabel, modelCount, providerInitial,
      plugins, pluginError, pluginBusy, pluginFilter, filteredPlugins, enabledPlugins, navLinks,
      tone: statusTone, setPluginFilter,
      version, revision, built, proto, bootstrap,
      mode, palette, THEME_MODES, PALETTES,
      prefs, setPref, resetPrefs,
      loadProviders, loadPlugins, selectTab, goProviderPage, togglePlugin, reloadPlugins,
      chooseMode, choosePalette,
    };
  },
  render(s) {
    if (!s.state.open) return null;

    const close = () => { s.state.open = false; };

    const navItem = (t) => h("button", {
      class: "c-set-nav__item" + (s.tab === t.id ? " is-active" : ""),
      type: "button",
      key: t.id,
      "aria-current": s.tab === t.id ? "page" : undefined,
      "on:click": () => s.selectTab(t.id),
    }, [
      h(SettingsIcon, { name: t.icon, size: 14 }),
      h("span", null, t.label),
    ]);

    // TABS 分组渲染（连接与能力 / 其他）
    const groups = [];
    let currentGroup = "";
    for (const t of s.TABS) {
      if (t.group !== currentGroup) {
        currentGroup = t.group;
        groups.push(h("span", { class: "c-set-nav__label", key: "g:" + t.group }, t.group));
      }
      groups.push(navItem(t));
    }

    const switchBox = (checked, onChange) => h("label", { class: "c-switch" }, [
      h("input", { type: "checkbox", checked: checked, "on:change": onChange }),
      h("span", { class: "c-switch__track" }),
    ]);

    const providersPane = () => {
      const rows = s.providers.map((p) => h("div", { class: "c-table__row", key: p.id }, [
        h("span", { class: "c-provider-avatar", "aria-hidden": "true" }, s.providerInitial(p)),
        h("div", { class: "c-table__grow c-table__name" }, [
          h("span", null, p.displayName || p.id),
          h("span", { class: "c-table__meta" }, p.disabled ? "已停用" : "已启用"),
        ]),
        h("span", { class: "c-table__mono" }, p.baseUrl || "—"),
        h("span", { class: p.disabled ? "c-tag c-tag--muted" : "c-tag c-tag--ok" }, p.disabled ? "停用" : "已连接"),
        h("span", { class: "c-table__mono" }, String(s.modelCount(p)) + " 个模型"),
      ]));
      return h("section", { class: "c-set-pane" }, [
        h("h3", null, "供应商管理"),
        h("p", { class: "c-set-pane__desc" }, "接入模型服务端点：支持任意 OpenAI 兼容接口；密钥只保存在本机，不进入会话或导出文件。"),
        h("div", { class: "c-set-row" }, [
          h("div", { class: "c-set-row__text" }, [
            h("span", { class: "c-set-row__title" }, s.providersLoaded ? `已配置 ${s.providers.length} 个供应商` : "正在读取供应商…"),
            h("span", { class: "c-set-row__desc" }, s.providersError || "增删改与密钥管理在供应商页面完成。"),
          ]),
          s.providerPage !== ""
            ? h("button", { class: "c-btn c-btn--primary", type: "button", "on:click": () => s.goProviderPage() }, "打开供应商管理")
            : h("button", { class: "c-btn", type: "button", "on:click": () => s.selectTab("plugins") }, "去启用插件"),
        ]),
        s.providersError !== null && s.providers.length === 0
          ? h("div", { class: "c-empty" }, "供应商页面当前不可用。到「插件管理」启用提供者插件后回到这里。")
          : h("div", { class: "c-table" }, [
            h("div", { class: "c-table__row c-table__row--head" }, [
              h("span", { class: "c-table__grow" }, "供应商"),
              h("span", null, "端点"),
              h("span", null, "状态"),
              h("span", null, "模型"),
            ]),
            ...rows,
          ]),
      ]);
    };

    const themePane = () => {
      const modeCards = s.THEME_MODES.map((m) => h("button", {
        class: "c-mode-card" + (m.id === "dark" ? " c-mode-card--dark" : "") + (m.id === "system" ? " c-mode-card--system" : "") + (s.mode === m.id ? " is-selected" : ""),
        type: "button",
        key: m.id,
        "aria-pressed": s.mode === m.id ? "true" : "false",
        "on:click": () => s.chooseMode(m.id),
      }, [
        h("span", { class: "c-mode-card__preview" }, [
          h("span", { class: "c-mode-bar" }), h("span", { class: "c-mode-bar" }), h("span", { class: "c-mode-bar" }),
        ]),
        h("span", { class: "c-mode-card__name" }, m.label),
      ]));

      const swatches = s.PALETTES.map((p) => h("button", {
        class: "c-swatch-btn" + (s.palette === p.id ? " is-selected" : ""),
        type: "button",
        key: p.id,
        title: p.label,
        "aria-label": p.label,
        "aria-pressed": s.palette === p.id ? "true" : "false",
        "on:click": () => s.choosePalette(p.id),
      }, h("span", { class: "c-swatch-dot ada-swatch--" + p.id })));

      return h("section", { class: "c-set-pane" }, [
        h("h3", null, "主题设置"),
        h("p", { class: "c-set-pane__desc" }, "选择界面明暗与色板；界面密度与动效只影响本机客户端外观，不写入会话内容。"),
        h("div", { class: "c-set-pane__note" }, [h("span", { class: "c-note-dot" }), "当前：" + (s.mode === "system" ? "跟随系统" : s.mode === "dark" ? "深色" : "浅色") + " · " + (s.PALETTES.find((p) => p.id === s.palette)?.label ?? "蓝海")]),
        h("div", { class: "c-mode-grid" }, modeCards),
        h("div", { class: "c-set-card" }, [
          h("div", { class: "c-set-card__head" }, "强调色板"),
          h("div", { class: "c-swatches" }, swatches),
          h("p", { class: "c-set-row__desc" }, "色板来自设计令牌，切换立即生效并保存在本机。"),
        ]),
        h("div", { class: "c-set-card" }, [
          h("div", { class: "c-set-card__head" }, "界面密度与字号"),
          h("div", { class: "c-set-row" }, [
            h("div", { class: "c-set-row__text" }, [h("span", { class: "c-set-row__title" }, "界面密度"), h("span", { class: "c-set-row__desc" }, "紧凑适合小窗口，宽松适合大屏。")]),
            h("div", { class: "c-seg" }, ["compact", "comfortable", "relaxed"].map((d, i) => h("button", {
              key: d,
              type: "button",
              class: "c-seg__item" + ((s.prefs.density ?? "comfortable") === d ? " is-active" : ""),
              "on:click": () => s.setPref("density", d),
            }, ["紧凑", "默认", "宽松"][i]))),
          ]),
          h("div", { class: "c-set-row" }, [
            h("div", { class: "c-set-row__text" }, [h("span", { class: "c-set-row__title" }, "正文字号"), h("span", { class: "c-set-row__desc" }, "影响正文与列表文字，不改变排版栅格。")]),
            h("div", { class: "c-seg" }, ["sm", "md", "lg"].map((d, i) => h("button", {
              key: d,
              type: "button",
              class: "c-seg__item" + ((s.prefs.textSize ?? "md") === d ? " is-active" : ""),
              "on:click": () => s.setPref("textSize", d),
            }, ["13px", "14px", "15px"][i]))),
          ]),
          h("div", { class: "c-set-row" }, [
            h("div", { class: "c-set-row__text" }, [h("span", { class: "c-set-row__title" }, "减少动态效果"), h("span", { class: "c-set-row__desc" }, "关闭面板过渡与打字滚动。")]),
            switchBox(s.prefs.reduceMotion === true, (e) => s.setPref("reduceMotion", e.target.checked)),
          ]),
        ]),
        h("button", { class: "c-btn c-btn--ghost", type: "button", "on:click": () => s.resetPrefs() }, "恢复默认外观"),
      ]);
    };

    const pluginsPane = () => {
      const rows = s.filteredPlugins.map((p) => h("div", { class: "c-table__row", key: p.id }, [
        h(SettingsIcon, { name: "i-plug", size: 16 }),
        h("div", { class: "c-table__grow c-table__name" }, [
          h("span", null, p.name || p.id),
          h("span", { class: "c-table__meta" }, p.id + (p.version ? " · v" + p.version : "") + (p.tools ? " · " + p.tools + " 个工具" : "")),
        ]),
        h("span", { class: "c-tag c-tag--" + s.tone(p) }, p.statusLabel || (p.enabled === false ? "未启用" : "运行中")),
        switchBox(p.enabled !== false, () => s.togglePlugin(p)),
      ]));
      return h("section", { class: "c-set-pane" }, [
        h("h3", null, "插件管理"),
        h("p", { class: "c-set-pane__desc" }, "管理 MCP 服务与 Agent 扩展；插件在独立进程里运行，启用后工具会出现在会话中。"),
        h("div", { class: "c-set-row" }, [
          h("div", { class: "c-set-row__text" }, [
            h("span", { class: "c-set-row__title" }, s.plugins.length > 0 ? `${s.enabledPlugins} 个运行中 · 共 ${s.plugins.length} 个` : "正在读取插件…"),
            h("span", { class: "c-set-row__desc" }, s.pluginError || "启停会写回工作区配置，重载后生效。"),
          ]),
          h("button", { class: "c-btn", type: "button", disabled: s.pluginBusy, "on:click": () => s.reloadPlugins() }, "重载插件"),
        ]),
        h("div", { class: "c-seg" }, ["all", "on", "off"].map((f, i) => h("button", {
          key: f,
          type: "button",
          class: "c-seg__item" + (s.pluginFilter === f ? " is-active" : ""),
          "on:click": () => { s.setPluginFilter(f); },
        }, ["全部", "已启用", "未启用"][i]))),
        s.pluginError !== null && s.plugins.length === 0
          ? h("div", { class: "c-empty" }, "插件列表不可用：" + s.pluginError)
          : h("div", { class: "c-table" }, rows),
        s.navLinks.length > 0
          ? h("div", { class: "c-set-card" }, [
            h("div", { class: "c-set-card__head" }, "插件页面"),
            ...s.navLinks.map((n) => h("a", {
              key: n.path,
              class: "c-set-row",
              href: n.path,
              "on:click": (e) => { e.preventDefault(); s.state.open = false; router.navigate(n.path); },
            }, [
              h(SettingsIcon, { name: n.icon || "i-plug", size: 14 }),
              h("span", { class: "c-set-row__title" }, n.label ?? n.path),
            ])),
          ])
          : null,
      ]);
    };

    const generalPane = () => h("section", { class: "c-set-pane" }, [
      h("h3", null, "通用"),
      h("p", { class: "c-set-pane__desc" }, "启动、存储与阅读的本地偏好；所有设置只作用于本机客户端，可随时恢复默认。"),
      h("div", { class: "c-set-card" }, [
        h("div", { class: "c-set-card__head" }, "启动与恢复"),
        h("div", { class: "c-set-row" }, [
          h("div", { class: "c-set-row__text" }, [h("span", { class: "c-set-row__title" }, "恢复上次会话"), h("span", { class: "c-set-row__desc" }, "启动时直接从概览回到上次打开的会话。")]),
          switchBox(s.prefs.restoreSession !== false, (e) => s.setPref("restoreSession", e.target.checked)),
        ]),
      ]),
      h("div", { class: "c-set-card" }, [
        h("div", { class: "c-set-card__head" }, "会话与阅读"),
        h("div", { class: "c-set-row" }, [
          h("div", { class: "c-set-row__text" }, [h("span", { class: "c-set-row__title" }, "显示执行耗时"), h("span", { class: "c-set-row__desc" }, "工具行与运行状态条显示耗时信息。")]),
          switchBox(s.prefs.showDurations !== false, (e) => s.setPref("showDurations", e.target.checked)),
        ]),
        h("div", { class: "c-set-row" }, [
          h("div", { class: "c-set-row__text" }, [h("span", { class: "c-set-row__title" }, "显示最后活动时间"), h("span", { class: "c-set-row__desc" }, "侧栏会话行显示相对时间。")]),
          switchBox(s.prefs.showTimes !== false, (e) => s.setPref("showTimes", e.target.checked)),
        ]),
      ]),
      h("div", { class: "c-empty" }, "草稿自动保存在浏览器本地；清除站点数据会一并删除未发送草稿。"),
    ]);

    const aboutPane = () => h("section", { class: "c-set-pane" }, [
      h("h3", null, "关于与更新"),
      h("p", { class: "c-set-pane__desc" }, "版本信息、协议版本与运行环境；更新随二进制发布，重启后生效。"),
      h("div", { class: "c-about-hero" }, [
        h("span", { class: "c-about-hero__mark" }, "A"),
        h("div", { class: "c-about-hero__body" }, [
          h("span", { class: "c-about-hero__name" }, [s.bootstrap.brandName || "ada-go", h("span", { class: "c-tag c-tag--brand" }, s.version || "dev")]),
          h("span", { class: "c-about-hero__sub" }, "本地优先的 AI Agent 控制台 · 无需账号 · 数据仅存本机"),
        ]),
      ]),
      h("div", { class: "c-set-card" }, [
        h("div", { class: "c-set-card__head" }, "版本信息"),
        h("div", { class: "c-kv" }, [
          kvRow("当前版本", s.version || "dev"),
          kvRow("修订", s.revision || "dev"),
          kvRow("构建时间", s.built || "dev"),
          kvRow("网关协议", s.proto === null ? "—" : "proto " + String(s.proto)),
          kvRow("网关地址", s.bootstrap.gateway || "—"),
          kvRow("当前工作区", s.bootstrap.workspace || "—"),
        ]),
      ]),
      h("div", { class: "c-set-card" }, [
        h("div", { class: "c-set-card__head" }, "开源与致谢"),
        h("p", { class: "c-set-row__desc" }, "界面令牌取自 Element Plus 设计体系衍生色板；图标来自内置 SVG sprite；字体 Inter / Noto Sans SC。"),
      ]),
    ]);

    const privacyPane = () => h("section", { class: "c-set-pane" }, [
      h("h3", null, "数据与隐私"),
      h("p", { class: "c-set-pane__desc" }, "会话、模型密钥与偏好都保存在本机；网关只监听回环地址。"),
      h("div", { class: "c-set-card" }, [
        h("div", { class: "c-set-card__head" }, "本机存储"),
        h("div", { class: "c-kv" }, [
          kvRow("会话与事件", "工作区 .adago 数据目录"),
          kvRow("模型密钥", "Windows 凭据管理器 / 本机配置文件"),
          kvRow("界面偏好", "浏览器 localStorage"),
        ]),
      ]),
      h("div", { class: "c-empty" }, "导出文件不包含密钥；审计日志可在命令行用 adag audit list 查看。"),
    ]);

    const panes = {
      providers: providersPane,
      theme: themePane,
      plugins: pluginsPane,
      general: generalPane,
      about: aboutPane,
      privacy: privacyPane,
    };
    const pane = panes[s.tab] ?? providersPane;

    return h("div", { class: "c-modal", role: "presentation" }, [
      h("div", { class: "c-modal__backdrop", "on:click": close }),
      h("div", { class: "c-modal__panel", role: "dialog", "aria-modal": "true", "aria-label": "设置" }, [
        h("div", { class: "c-modal__head" }, [
          h("h2", null, "设置"),
          h("span", null, (s.bootstrap.brandName || "ada-go") + " · 偏好与连接"),
          h("button", { class: "c-modal__close", type: "button", "aria-label": "关闭设置", "on:click": close }, h(SettingsIcon, { name: "i-x", size: 16 })),
        ]),
        h("div", { class: "c-modal__body" }, [
          h("div", { class: "c-set-nav", "aria-label": "设置分区", role: "navigation" }, groups),
          pane(),
        ]),
        h("div", { class: "c-modal__foot" }, [
          h("span", { class: "c-hint" }, "设置即时生效，随本机保存。"),
          h("span", { class: "c-sp" }),
          h("button", { class: "c-footer-btn c-footer-btn--primary", type: "button", "on:click": close }, "完成"),
        ]),
      ]),
    ]);
  },
});

// 图标：复用宿主 ui-icon 的同一套 CSS mask 类名；宿主全局注册表拿不到，
// 这里定义一个模块级常量（避免每次 render 新建组件定义导致反复重挂）。
const SettingsIcon = defineComponent({
  name: "SettingsIcon",
  props: ["name", "size"],
  render(_s, ctx) {
    const raw = String(ctx.props.name ?? "");
    const key = (raw.startsWith("i-") ? raw.slice(2) : raw).replace(/[^a-z0-9-]/g, "");
    const size = Number(ctx.props.size ?? 14) || 14;
    return h("span", {
      class: `ada-icon ada-icon--mask ada-icon--${key}`,
      "aria-hidden": "true",
      style: { width: size + "px", height: size + "px" },
    }, null);
  },
});

function kvRow(k, v) {
  return h("div", { class: "c-kv__row" }, [
    h("span", { class: "c-kv__k" }, k),
    h("span", { class: "c-kv__v" }, v),
  ]);
}


