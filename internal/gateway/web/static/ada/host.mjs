// host.mjs —— shell 装配 glue：bootstrap 解析、取数封装、注册表存储、
// 工作区 SSE、插件作用域（SlotOutlet / PluginScope）、插件服务面（provide("ada")）。
//
// 对应开发计划 F6/S2/S3。契约见 proposals/dev-plan-spa-frontend.md §6。

import { reactive, computed, ref, h, defineComponent, provide, inject, onErrorCaptured, defineAsyncComponent } from "./runtime.mjs";
import { loadSFC } from "./sfc.mjs";

// ---------------------------------------------------------------------------
// bootstrap（shell 模板注入的 JSON）
// ---------------------------------------------------------------------------

export function readBootstrap() {
	let data = { workspace: "", workspaceName: "", product: "", csrf: "", app: {} };
	const el = document.getElementById("ada-bootstrap");
	if (el !== null) {
		try {
			data = { ...data, ...JSON.parse(el.textContent) };
		} catch (err) {
			console.error("[ada] bootstrap JSON 解析失败", err);
		}
	}
	// CSRF 双提交：JSON 缺失时回退 meta 标签。
	if ((data.csrf ?? "") === "") {
		data.csrf = document.querySelector('meta[name="csrf-token"]')?.getAttribute("content") ?? "";
	}
	return data;
}

// ---------------------------------------------------------------------------
// 取数封装（CSRF 双提交；401 → 登录页；303 flash 回执 → onFlash）
// ---------------------------------------------------------------------------

// flashFromURL 从最终响应 URL 的查询串提取 ?notice= / ?error= 文案。
// 服务端 POST 的 303 回执用 flashRedirect 把提示挂在重定向目标上；fetch 跟随
// 重定向后 res.url 就是带查询串的那个页面地址，文案不需要解析页面本体。
function flashFromURL(url) {
	if (typeof url !== "string" || !url.includes("?")) return null;
	let params;
	try {
		params = new URL(url, "http://ada.invalid").searchParams;
	} catch {
		return null;
	}
	const error = params.get("error");
	if (error !== null && error !== "") return { kind: "error", message: error };
	const notice = params.get("notice");
	if (notice !== null && notice !== "") return { kind: "notice", message: notice };
	return null;
}

export function makeFetcher(bootstrap, options = {}) {
	const csrf = bootstrap.csrf ?? "";
	// onFlash 是 toast 服务的注入点（第二参可以直接给函数，也可以 { onFlash }）。
	const onFlash = typeof options === "function" ? options : options?.onFlash;
	return async function adaFetch(path, opts = {}) {
		const headers = { "X-CSRF-Token": csrf, ...(opts.headers ?? {}) };
		let body = opts.body;
		if (body !== undefined && typeof body !== "string" && !(body instanceof FormData)) {
			headers["Content-Type"] = "application/json";
			body = JSON.stringify(body);
		}
		const res = await fetch(path, {
			method: opts.method ?? "GET",
			headers,
			body,
			credentials: "same-origin",
		});
		if (res.status === 401) {
			window.location.href = "/login";
			return new Promise(() => {}); // 跳转中
		}
		if (!res.ok) {
			const text = await res.text().catch(() => "");
			throw new Error(`请求失败 ${res.status}: ${path} ${text.slice(0, 200)}`);
		}
		const ct = res.headers.get("content-type") ?? "";
		if (ct.includes("application/json")) return res.json();
		// 非 JSON 响应（303 重定向后的页面/HTML）：只读 URL 上的 flash 文案，
		// 响应本体不解析（页面内容对 SPA 没有意义）。
		const flash = flashFromURL(res.url);
		if (flash !== null && typeof onFlash === "function") {
			try {
				onFlash(flash.kind, flash.message);
			} catch (err) {
				console.error("[ada] flash 回调失败", err);
			}
		}
		return res.text();
	};
}

// ---------------------------------------------------------------------------
// 注册表存储（/frontend-registry + /events/workspace）
// ---------------------------------------------------------------------------

export function createRegistryStore(adaFetch) {
	const store = reactive({
		rev: 0,
		plugins: [],
		loaded: false,
		loadError: null,
	});

	async function load() {
		try {
			const data = await adaFetch("/frontend-registry");
			store.rev = data.rev ?? 0;
			store.plugins = data.plugins ?? [];
			store.loaded = true;
			store.loadError = null;
		} catch (err) {
			store.loadError = err;
		}
		syncPluginStyles();
	}

	// ---- 插件全局样式（manifest styles[]）：每插件一张 CSSStyleSheet，----
	// ---- 启用即挂、禁用/内容变化即换（与 SFC scoped 样式互补）。      ----
	const styleSheets = new Map(); // pluginId → { key, sheet }
	function detachSheet(sheet) {
		const sheets = [...document.adoptedStyleSheets];
		const i = sheets.indexOf(sheet);
		if (i >= 0) sheets.splice(i, 1);
		document.adoptedStyleSheets = sheets;
	}
	async function syncPluginStyles() {
		if (typeof CSSStyleSheet === "undefined" || typeof document === "undefined") return;
		const wanted = new Map();
		for (const p of store.plugins) {
			if (Array.isArray(p.styles) && p.styles.length > 0) wanted.set(p.id, p.styles);
		}
		for (const [id, entry] of [...styleSheets]) {
			if (!wanted.has(id)) {
				detachSheet(entry.sheet);
				styleSheets.delete(id);
			}
		}
		for (const [id, urls] of wanted) {
			const key = urls.join("|");
			const existing = styleSheets.get(id);
			if (existing !== undefined && existing.key === key) continue;
			if (existing !== undefined) detachSheet(existing.sheet);
			try {
				const texts = await Promise.all(urls.map((u) => fetch(u).then((r) => (r.ok ? r.text() : ""))));
				const sheet = new CSSStyleSheet();
				sheet.replaceSync(texts.join("\n"));
				document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
				styleSheets.set(id, { key, sheet });
			} catch {
				// 样式拉取失败不阻断渲染（scoped 样式仍在）
			}
		}
	}

	function slots(name) {
		const out = [];
		for (const p of store.plugins) {
			for (const s of p.slots ?? []) {
				if (s.slot === name) out.push({ ...s, pluginId: p.id, pluginName: p.name, trust: p.trust });
			}
			// dashboard 卡即 dashboard.widgets 插槽的贡献（dev-plan §6：dashboard[].component）
			if (name === "dashboard.widgets") {
				for (const d of p.dashboard ?? []) {
					out.push({ slot: name, component: d.component, order: d.order, pluginId: p.id, pluginName: p.name, trust: p.trust });
				}
			}
		}
		out.sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
		return out;
	}

	function page(id, slug) {
		for (const p of store.plugins) {
			if (p.id !== id) continue;
			for (const pg of p.pages ?? []) {
				if (pg.slug === slug) return { ...pg, pluginId: p.id, trust: p.trust };
			}
		}
		return null;
	}

	function nav() {
		const out = [];
		for (const p of store.plugins) {
			for (const n of p.nav ?? []) out.push({ ...n, pluginId: p.id });
		}
		out.sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
		return out;
	}

	function plugin(id) {
		return store.plugins.find((p) => p.id === id) ?? null;
	}

	function actionBinding(id, action) {
		const p = plugin(id);
		return p?.actions?.[action] ?? null;
	}

	// 工作区级 SSE：evt.plugin.changed → 重拉注册表（简单正确优先）。
	function subscribeWorkspaceEvents() {
		const es = new EventSource("/events/workspace");
		es.addEventListener("evt.plugin.changed", () => { load(); });
		es.addEventListener("evt.ready", () => { load(); });
		es.onerror = () => { /* EventSource 自动重连 */ };
		return es;
	}

	return { store, load, slots, page, nav, plugin, actionBinding, subscribeWorkspaceEvents };
}

// ---------------------------------------------------------------------------
// 插件服务面（provide("ada")）
// ---------------------------------------------------------------------------

export function makeAda({ pluginId, fetch, registry, thread, confirm }) {
	const storageKey = (k) => `${pluginId}:${k}`;
	const svc = {
		pluginId,
		// 当前线程上下文（仅会话内插槽有）
		thread: thread ?? null,
		// route 桥：GET/POST /ext/{id}/api/{path}
		async api(path, opts = {}) {
			return fetch(`/ext/${pluginId}/api/${path.replace(/^\//, "")}`, opts);
		},
		// 插件 SSE route（沿用 window.adaSSE 的三段协议封装；未加载 api.js 时不可用）
		sse(path, handlers) {
			if (typeof window.adaSSE !== "function") {
				throw new Error("adaSSE 不可用（api.js 未加载）");
			}
			return window.adaSSE(pluginId, path, handlers);
		},
		// 动作桥：manifest 的 read:true 绑定是只读预取动作，走 GET（query 承载 input）；
		// 写动作走 POST，且 confirm 文案由宿主保证先确认（docs/07 §7.6）。
		async action(name, input = {}) {
			const binding = registry.actionBinding(pluginId, name);
			if (binding !== null && binding.read === true) {
				const qs = new URLSearchParams();
				for (const [k, v] of Object.entries(input)) {
					if (v !== undefined && v !== null) qs.set(k, String(v));
				}
				const q = qs.toString();
				return fetch(`/ext/${pluginId}/action/${name}${q ? `?${q}` : ""}`);
			}
			if (binding !== null && binding.confirm) {
				const ok = await confirm(binding.confirm, name);
				if (!ok) return undefined;
			}
			return fetch(`/ext/${pluginId}/action/${name}`, {
				method: "POST",
				body: input ?? {},
			});
		},
		confirm(text) {
			return confirm(text, pluginId);
		},
		// 插件隔离存储：键名自动 "<pluginId>:" 前缀。
		storage: {
			get(key) {
				const v = localStorage.getItem(storageKey(key));
				if (v === null) return null;
				try { return JSON.parse(v); } catch { return v; }
			},
			set(key, value) {
				localStorage.setItem(storageKey(key), JSON.stringify(value));
			},
			remove(key) {
				localStorage.removeItem(storageKey(key));
			},
		},
	};
	return svc;
}

// ---------------------------------------------------------------------------
// 插件作用域组件（错误边界 + ada 服务注入 + 异步 SFC 装载）
// ---------------------------------------------------------------------------

// 每个插件贡献一层独立边界：装载失败/运行错误都只影响本贡献。
export const PluginScope = defineComponent({
	name: "PluginScope",
	props: ["pluginId", "threadId", "load", "extraProps"],
	setup(props) {
		const registry = inject("registry");
		const confirm = inject("confirmService");
		const fetch = inject("fetch");
		const thread = props.threadId !== undefined && props.threadId !== null
			? reactive({ id: props.threadId })
			: null;
		provide("ada", makeAda({ pluginId: props.pluginId, fetch, registry, thread, confirm }));
		// 异步组件只创建一次（render 里重建会导致每次 patch 重新装载）。
		const Inner = defineAsyncComponent(() => props.load());
		return { Inner, extraProps: props.extraProps ?? {} };
	},
	render(s) {
		return h(s.Inner, s.extraProps);
	},
});

// SlotOutlet：按名字渲染注册表里的插槽贡献（order 排序，逐个独立边界）。
export const SlotOutlet = defineComponent({
	name: "SlotOutlet",
	props: ["name", "threadId"],
	setup(props) {
		const registry = inject("registry");
		const contribs = computed(() => registry.slots(props.name));
		return { contribs, threadId: props.threadId ?? null };
	},
	render(s) {
		if (s.contribs.length === 0) return null;
		return h(
			"div",
			{ class: "ada-slot", "data-slot": s.name, "data-contributors": String(s.contribs.length) },
			s.contribs.map((c) =>
				h(ContributionBoundary, { key: `${c.pluginId}:${c.slot}`, pluginId: c.pluginId, threadId: s.threadId, contrib: c }),
			),
		);
	},
});

// 单贡献边界：捕获装载/运行错误 → data-ada-error 占位。
const ContributionBoundary = defineComponent({
	name: "ContributionBoundary",
	props: ["pluginId", "threadId", "contrib"],
	setup(props) {
		const failed = ref(null);
		onErrorCaptured((err) => {
			failed.value = err?.message ?? String(err);
			return false;
		});
		return { failed, contrib: props.contrib, pluginId: props.pluginId, threadId: props.threadId };
	},
	render(s) {
		if (s.failed !== null) {
			return h("div", { class: "ada-slot-error", "data-ada-error": "1", "data-plugin": s.pluginId },
				`插件贡献装载失败：${s.failed}`);
		}
		return h(PluginScope, {
			pluginId: s.pluginId,
			threadId: s.threadId,
			load: () => loadSFC(s.contrib.component),
			extraProps: { pluginId: s.pluginId },
		});
	},
});
