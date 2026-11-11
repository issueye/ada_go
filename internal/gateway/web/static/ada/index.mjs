// index.mjs —— shell 入口（S2 + 基座页面重建）：布局 + 路由 + 基座页面组件 + ui-* 注册。
//
// 布局与 12 插槽（docs/07 §7.2 词汇表不变）：
//   sidebar.top / sidebar.bottom / topbar.right        → shell 布局
//   chat.corner / composer.* / thread.tray / transcript.after → 会话页
//   extensions.list → 扩展页；dashboard.widgets → 概览页
// 硬规则（docs/00 §1.5.7）：交互托盘外壳/登录页/错误页为基座组件，插件不可替换。
//
// 基座页面全部是 static/ada/pages/*.vue（loadSFC 运行时装载，defineAsyncComponent）；
// 本文件只保留宿主 UI kit、shell 布局与路由表。

import { h, createApp, defineComponent, ref, reactive, computed, onMounted, onUnmounted, inject, defineAsyncComponent } from "./runtime.mjs";
import { watch, nextTick } from "./reactivity.mjs";
import { createRouter } from "./router.mjs";
import { readBootstrap, makeFetcher, createRegistryStore, SlotOutlet, PluginScope } from "./host.mjs";
import { renderVNodeJsonChildren } from "./vnodejson.mjs";
import { loadSFC } from "./sfc.mjs";
import { tokenize } from "./highlight.js";
import { shellState } from "./pages/state.js";

// ---------------------------------------------------------------------------
// 启动：bootstrap → 服务实例化 → provide → mount
// ---------------------------------------------------------------------------

// 轻量 toast 服务（HEAD app.js 的 flash/notify 语义）：reactive 队列 + 自动消失。
// makeFetcher 的 onFlash 与服务端 303 回执（?notice=/?error=）都汇到这里。
const TOAST_MS = 4000;
const toastState = reactive({ items: [] });
let toastSeq = 0;
function pushToast(message, kind) {
	const text = String(message ?? "").trim();
	if (text === "") return;
	const id = ++toastSeq;
	toastState.items.push({ id, text, kind: kind === "error" ? "error" : "info" });
	window.setTimeout(() => dismissToast(id), TOAST_MS);
}
function dismissToast(id) {
	const i = toastState.items.findIndex((t) => t.id === id);
	if (i >= 0) toastState.items.splice(i, 1);
}
const toastService = { show: pushToast, dismiss: dismissToast, state: toastState };

const bootstrap = readBootstrap();
const adaFetch = makeFetcher(bootstrap, { onFlash: (kind, message) => pushToast(message, kind) });
const registry = createRegistryStore(adaFetch);

// 全局确认对话框服务（promise 化；禁 window.confirm 的既有约定）。
const confirmState = reactive({ open: false, text: "", resolve: null });
function confirmService(text) {
	return new Promise((resolve) => {
		confirmState.text = text;
		confirmState.open = true;
		confirmState.resolve = resolve;
	});
}

// 图片查看浮层状态（HEAD chat-images.js 的 data-image-viewer 交互）。
// 大图用 background-image 呈现（白名单仍不允许 <img>）；多图时可左右切换。
// 焦点处理照 HEAD：打开时记住 activeElement，关闭时还原；浮层内 Tab 循环。
const imageViewerState = reactive({ open: false, images: [], index: 0 });
let imageViewerReturnFocus = null;
function openImageViewer(images, index = 0) {
	const list = (images ?? []).filter((x) => x && typeof x.src === "string" && x.src !== "");
	if (list.length === 0) return;
	if (imageViewerState.open !== true) {
		// 记录打开前的焦点（HEAD lastTrigger）：关闭后要还回去。
		try {
			imageViewerReturnFocus = typeof document !== "undefined" ? (document.activeElement ?? null) : null;
		} catch {
			imageViewerReturnFocus = null;
		}
	}
	imageViewerState.images = list;
	imageViewerState.index = Math.min(Math.max(0, Number(index) || 0), list.length - 1);
	imageViewerState.open = true;
	try {
		document.body.classList.add("ada-image-viewer-open");
	} catch { /* 无 DOM 环境（测试）忽略 */ }
}
function closeImageViewer() {
	imageViewerState.open = false;
	imageViewerState.images = [];
	imageViewerState.index = 0;
	try {
		document.body.classList.remove("ada-image-viewer-open");
	} catch { /* 无 DOM 环境（测试）忽略 */ }
	// 焦点还原：trigger 已随页面重绘消失时不强抢焦点（HEAD lastTrigger.isConnected 检查）。
	const target = imageViewerReturnFocus;
	imageViewerReturnFocus = null;
	if (target !== null && target !== undefined && target.isConnected !== false && typeof target.focus === "function") {
		try { target.focus(); } catch { /* 焦点失败不影响关闭 */ }
	}
}
function stepImageViewer(delta) {
	const len = imageViewerState.images.length;
	if (!imageViewerState.open || len === 0) return;
	imageViewerState.index = (imageViewerState.index + delta + len) % len;
}
const imageViewerService = {
	open: openImageViewer,
	close: closeImageViewer,
	next: () => stepImageViewer(1),
	prev: () => stepImageViewer(-1),
	state: imageViewerState,
};

// 会话行操作菜单（HEAD app.js 的 data-thread-menu-action：复制 ID / 归档）。
// 菜单挂在 ShellApp 根部（body 级 fixed 定位），不受侧栏滚动容器裁剪。
const threadMenuState = reactive({ open: false, id: "", title: "", running: false, x: 0, y: 0 });
let threadMenuOnArchived = null;
let threadMenuNavigate = null;
function closeThreadMenu() {
	threadMenuState.open = false;
}
function openThreadMenu(thread, event, onArchived) {
	const id = String(thread?.id ?? "");
	if (id === "") return;
	threadMenuState.id = id;
	threadMenuState.title = String(thread?.title ?? "");
	threadMenuState.running = thread?.running === true;
	const win = typeof window === "undefined" ? { innerWidth: 1280, innerHeight: 800 } : window;
	const x = Number(event?.clientX ?? 8) || 8;
	const y = Number(event?.clientY ?? 8) || 8;
	threadMenuState.x = Math.max(8, Math.min(x, win.innerWidth - 190));
	threadMenuState.y = Math.max(8, Math.min(y, win.innerHeight - 96));
	threadMenuOnArchived = typeof onArchived === "function" ? onArchived : null;
	threadMenuState.open = true;
}
async function copyThreadMenuID() {
	const id = threadMenuState.id;
	closeThreadMenu();
	if (id === "") return;
	try {
		if (typeof navigator === "undefined" || !navigator.clipboard?.writeText) {
			throw new Error("当前环境不支持剪贴板");
		}
		await navigator.clipboard.writeText(id);
		pushToast("会话 ID 已复制", "info");
	} catch (err) {
		console.error("[ada] 复制会话 ID 失败", err);
		pushToast("复制失败，请手动选择会话 ID。", "error");
	}
}
async function archiveThreadMenu() {
	const id = threadMenuState.id;
	const title = threadMenuState.title !== "" ? threadMenuState.title : id;
	const running = threadMenuState.running;
	closeThreadMenu();
	if (id === "") return;
	if (running) {
		pushToast("运行中的会话不能归档", "error");
		return;
	}
	const ok = await confirmService(`归档会话「${title}」？归档后不再显示在侧栏。`);
	if (!ok) return;
	try {
		const next = window.location.pathname + window.location.search;
		await adaFetch(`/threads/${encodeURIComponent(id)}/archive`, {
			method: "POST",
			headers: { "Content-Type": "application/x-www-form-urlencoded" },
			body: new URLSearchParams({ next }).toString(),
		});
		// 成功文案由服务端 303 回执的 ?notice= 经 makeFetcher 的 onFlash 推入 toast。
		const active = window.location.pathname === `/threads/${encodeURIComponent(id)}`;
		threadMenuOnArchived?.();
		if (active && typeof threadMenuNavigate === "function") threadMenuNavigate("/");
	} catch (err) {
		pushToast(`归档失败：${err?.message ?? err}`, "error");
	}
}
const threadMenuService = { open: openThreadMenu, close: closeThreadMenu, state: threadMenuState };

// ---------------------------------------------------------------------------
// ui-* 宿主组件（宿主 UI kit；插件模板可引用）
// ---------------------------------------------------------------------------

// ui-icon：CSS mask 图标（static/ada/icons.css，形状数据与 a_da iconDefs 同源）。
// 渲染为单色 span，颜色跟随 currentColor；props: name（"i-send" 或 "send"）、size（px）。
const UiIcon = defineComponent({
	name: "ui-icon",
	props: ["name", "size"],
	setup() { return {}; },
	render(_s, ctx) {
		const raw = String(ctx.props.name ?? "");
		const key = (raw.startsWith("i-") ? raw.slice(2) : raw).replace(/[^a-z0-9-]/g, "");
		const size = Number(ctx.props.size ?? 14) || 14;
		return h("span", {
			class: `ada-icon ada-icon--mask ada-icon--${key}`,
			"aria-hidden": "true",
			style: { width: `${size}px`, height: `${size}px` },
		}, null);
	},
});

// ui-markdown：模型正文（display profile VNode JSON）渲染；DOM 与 HEAD 的
// ui-markdown 同形（div.part-text 直落节点，无多余包装层）。流式阶段还没有
// AST 时用 text 纯文本占位。
const UiMarkdown = defineComponent({
	name: "ui-markdown",
	props: ["nodes", "text", "mdClass"],
	setup() { return {}; },
	render(_s, ctx) {
		const cls = ctx.props.mdClass ? String(ctx.props.mdClass) : "part-text";
		const nodes = ctx.props.nodes;
		if (Array.isArray(nodes) && nodes.length > 0) {
			return h("div", { class: cls }, renderVNodeJsonChildren(nodes));
		}
		const text = String(ctx.props.text ?? "");
		if (text !== "") return h("div", { class: [cls, "markdown-pending"] }, text);
		return h("div", { class: cls }, null);
	},
});

const UiBadge = defineComponent({
	name: "ui-badge",
	setup(_p, ctx) {
		// tone/label 都从 ctx.props 读：props 是 reactive 的，render 内读取即建依赖。
		return {};
	},
	render(_s, ctx) {
		const tone = ctx.props.tone ?? "info";
		const label = ctx.props.label ?? "";
		return h("span", { class: `ada-badge tone-${tone}` }, label !== "" ? label : ctx.kids);
	},
});

const UiButton = defineComponent({
	name: "ui-button",
	setup() { return {}; },
	render(_s, ctx) {
		const kind = ctx.props.kind ?? "default";
		return h("button", {
			class: `ada-btn kind-${kind}`,
			disabled: ctx.props.disabled === true,
			"on:click": ctx.props["on:click"],
		}, ctx.kids);
	},
});

const UiPanel = defineComponent({
	name: "ui-panel",
	setup() { return {}; },
	render(_s, ctx) {
		const title = ctx.props.title ?? "";
		return h("section", { class: "ada-panel" }, [
			title !== "" ? h("h3", { class: "ada-panel-title" }, title) : null,
			...ctx.kids,
		]);
	},
});

const UiEmpty = defineComponent({
	name: "ui-empty",
	setup() { return {}; },
	render(_s, ctx) {
		const text = ctx.props.text ?? "暂无内容";
		return h("div", { class: "ada-empty" }, text);
	},
});

// ui-code：源码预览的客户端高亮（HEAD 的 chroma 服务端渲染的替代）。
// props: text（源码原文）、lang（语言名或扩展名；未知/缺省按纯文本）。
// token 的 class 直接对齐 chroma（.chroma .k/.s/.c/.m/.nf/…，配色规则在
// app-local.css），所以容器要带 class="chroma"（由调用方给）。这里只拼
// span/text VNode，不碰 innerHTML；未知语言时直接把原文作为文本子节点。
const UiCode = defineComponent({
	name: "ui-code",
	props: ["text", "lang"],
	setup() { return {}; },
	render(_s, ctx) {
		const tokens = tokenize(String(ctx.props.text ?? ""), String(ctx.props.lang ?? ""));
		if (tokens.length === 1 && tokens[0].cls === "") return h("code", null, tokens[0].text);
		return h("code", null, tokens.map((tk) =>
			tk.cls === "" ? tk.text : h("span", { class: tk.cls }, tk.text)));
	},
});

// 确认对话框（基座组件，插件不可替换外壳）。
// 确认对话框：DOM 与类名照抄 HEAD 的 <ada-modal> 确认框（ada-modal__backdrop /
// __panel / __title / __message / __actions，样式在 ui/widgets.css）。SPA 里用
// 普通元素承载同一套类名（Web Component 不在渲染白名单），Esc 关闭在 setup 里补。
const ConfirmDialog = defineComponent({
	name: "ConfirmDialog",
	setup() {
		const onKey = (event) => {
			if (event.key !== "Escape" || !confirmState.open) return;
			event.preventDefault();
			confirmState.open = false;
			confirmState.resolve?.(false);
			confirmState.resolve = null;
		};
		if (typeof document !== "undefined") document.addEventListener("keydown", onKey);
		onUnmounted(() => document.removeEventListener("keydown", onKey));
		return { state: confirmState };
	},
	render(s) {
		if (!s.state.open) return null;
		const resolve = (v) => {
			s.state.open = false;
			s.state.resolve?.(v);
			s.state.resolve = null;
		};
		return h("div", { class: "ada-modal", role: "presentation" }, [
			h("div", { class: "ada-modal__backdrop", "on:click": () => resolve(false) }),
			h("div", { class: "ada-modal__panel", role: "dialog", "aria-modal": "true", "aria-labelledby": "ada-confirm-title" }, [
				h("h2", { class: "ada-modal__title", id: "ada-confirm-title" }, "确认操作"),
				h("p", { class: "ada-modal__message" }, s.state.text),
				h("div", { class: "ada-modal__actions" }, [
					h("button", { class: "ada-btn ada-btn--secondary ada-btn--sm", type: "button", "on:click": () => resolve(false) }, "取消"),
					h("button", { class: "ada-btn ada-btn--primary ada-btn--sm", type: "button", "on:click": () => resolve(true) }, "确认"),
				]),
			]),
		]);
	},
});

// 图片查看浮层（基座组件；点击遮罩/关闭按钮/Esc 关闭，多图左右切换）。
// 大图沿用 background-image：白名单没有 <img>，与消息缩略图同一套呈现方式。
// 焦点（HEAD chat-images.js §19.5.1）：打开时聚焦关闭按钮、Tab 圈在浮层内，
// 关闭时还原到打开前的元素（closeImageViewer 负责还原）。
// 浮层元素用 document.querySelector 定位：白名单没有 ref 属性，而浮层只有
// 一个实例（ShellApp 根部），类名就是它的身份。
function imageViewerEl() {
	try {
		return typeof document !== "undefined" && typeof document.querySelector === "function"
			? document.querySelector(".ada-image-viewer")
			: null;
	} catch {
		return null;
	}
}
function focusImageViewerClose() {
	const el = imageViewerEl();
	if (el === null) return;
	const close = typeof el.querySelector === "function" ? el.querySelector(".ada-image-viewer__close") : null;
	if (close !== null && typeof close.focus === "function") {
		try { close.focus(); } catch { /* 焦点失败不影响展示 */ }
	}
}
const UiImageViewer = defineComponent({
	name: "UiImageViewer",
	setup() {
		const onKey = (event) => {
			if (!imageViewerState.open) return;
			if (event.key === "Escape") {
				event.preventDefault();
				closeImageViewer();
				return;
			}
			if (event.key === "ArrowLeft") {
				event.preventDefault();
				stepImageViewer(-1);
				return;
			}
			if (event.key === "ArrowRight") {
				event.preventDefault();
				stepImageViewer(1);
				return;
			}
			// Tab 陷阱：浮层有 aria-modal 就必须圈住焦点（HEAD 同款循环实现）。
			// 可聚焦元素在浮层里动态取（多图时还有左右切换钮），不缓存。
			if (event.key === "Tab") {
				const root = imageViewerEl();
				if (root === null) return;
				const focusables = Array.from(typeof root.querySelectorAll === "function" ? root.querySelectorAll("button, [href]") : []);
				if (focusables.length === 0) {
					event.preventDefault();
					return;
				}
				const first = focusables[0];
				const last = focusables[focusables.length - 1];
				const active = typeof document !== "undefined" ? document.activeElement : null;
				const inside = typeof root.contains === "function" ? active !== null && root.contains(active) : true;
				if (!inside) {
					event.preventDefault();
					first.focus?.();
				} else if (event.shiftKey === true && active === first) {
					event.preventDefault();
					last.focus?.();
				} else if (event.shiftKey !== true && active === last) {
					event.preventDefault();
					first.focus?.();
				}
			}
		};
		onMounted(() => document.addEventListener("keydown", onKey));
		onUnmounted(() => document.removeEventListener("keydown", onKey));
		// 打开时把焦点送进浮层（HEAD openViewer 末尾的 close.focus()）。
		// watch 的回调可能早于本组件的渲染 job（id 更小），所以 focus 推到下一轮
		// flush 之后（浮层 DOM 已经 patch 出来）。
		watch(() => imageViewerState.open, (open) => {
			if (open) nextTick(focusImageViewerClose);
		});
		return {
			state: imageViewerState,
			close: closeImageViewer,
			prev: () => stepImageViewer(-1),
			next: () => stepImageViewer(1),
		};
	},
	render(s) {
		if (!s.state.open) return null;
		const list = s.state.images;
		const cur = list[s.state.index] ?? {};
		const stop = (e) => {
			if (e && typeof e.stopPropagation === "function") e.stopPropagation();
		};
		const navButton = (cls, label, icon, handler) =>
			h("button", {
				class: `ada-image-viewer__nav ${cls}`,
				type: "button",
				"aria-label": label,
				title: label,
				"on:click": (e) => { stop(e); handler(); },
			}, h(UiIcon, { name: icon, size: 18 }));
		return h("div", {
			class: "ada-image-viewer",
			role: "dialog",
			"aria-modal": "true",
			"aria-label": "图片预览",
			"on:click": () => s.close(),
		}, [
			h("div", {
				class: "ada-image-viewer__canvas",
				role: "img",
				"aria-label": String(cur.alt ?? "图片"),
				style: { backgroundImage: `url(${String(cur.src ?? "")})` },
				"on:click": stop,
			}, null),
			list.length > 1 ? navButton("ada-image-viewer__nav--prev", "上一张", "i-arrow-left", s.prev) : null,
			list.length > 1 ? h("span", { class: "ada-image-viewer__count" }, `${s.state.index + 1} / ${list.length}`) : null,
			list.length > 1 ? navButton("ada-image-viewer__nav--next", "下一张", "i-arrow-right", s.next) : null,
			h("button", {
				class: "ada-image-viewer__close",
				type: "button",
				"aria-label": "关闭图片预览",
				title: "关闭图片预览",
				"on:click": (e) => { stop(e); s.close(); },
			}, "×"),
		]);
	},
});

// Toast 队列（ShellApp 根部；样式复用 HEAD 的 .ada-flash + [data-flash]）。
const UiToast = defineComponent({
	name: "UiToast",
	setup() { return { state: toastState, dismiss: dismissToast }; },
	render(s) {
		if (s.state.items.length === 0) return null;
		return h("div", { class: "ada-toast-stack", "aria-live": "polite" }, s.state.items.map((item) =>
			h("div", {
				key: String(item.id),
				class: `ada-flash alert ${item.kind === "error" ? "alert-danger" : "alert-info"}`,
				"data-flash": item.kind,
				role: item.kind === "error" ? "alert" : "status",
			}, [
				h("p", { class: "alert-message" }, item.text),
				h("button", {
					class: "ada-flash__close",
					type: "button",
					"aria-label": "关闭提示",
					title: "关闭",
					"on:click": () => s.dismiss(item.id),
				}, "×"),
			]),
		));
	},
});

// 会话行菜单（复制 ID / 归档）：HEAD 是 body 级右键菜单，SPA 由行内「更多」
// 按钮打开，定位与条目文案保持不变。
const ThreadMenu = defineComponent({
	name: "ThreadMenu",
	setup() {
		const onPointer = (event) => {
			if (!threadMenuState.open) return;
			const target = event?.target;
			if (target && typeof target.closest === "function" && target.closest(".ada-context-menu") !== null) return;
			closeThreadMenu();
		};
		const onKey = (event) => {
			if (event.key !== "Escape" || !threadMenuState.open) return;
			event.preventDefault();
			closeThreadMenu();
		};
		onMounted(() => {
			document.addEventListener("pointerdown", onPointer, true);
			document.addEventListener("keydown", onKey);
		});
		onUnmounted(() => {
			document.removeEventListener("pointerdown", onPointer, true);
			document.removeEventListener("keydown", onKey);
		});
		return {
			state: threadMenuState,
			copy: copyThreadMenuID,
			archive: archiveThreadMenu,
		};
	},
	render(s) {
		if (!s.state.open) return null;
		return h("div", {
			class: "ada-context-menu",
			role: "menu",
			"aria-label": "会话操作",
			style: { left: `${s.state.x}px`, top: `${s.state.y}px` },
		}, [
			h("button", {
				class: "ada-row-menu__item",
				type: "button",
				role: "menuitem",
				"on:click": () => s.copy(),
			}, "复制会话 ID"),
			h("button", {
				class: "ada-row-menu__item",
				type: "button",
				role: "menuitem",
				disabled: s.state.running,
				title: s.state.running ? "运行中的会话不能归档" : "",
				"on:click": () => s.archive(),
			}, "归档会话"),
		]);
	},
});

// ---------------------------------------------------------------------------
// 基座页面（static/ada/pages/*.vue，loadSFC 运行时装载）
// ---------------------------------------------------------------------------

// pageV 是本模块自身 URL 上的内容指纹（?v=<staticVersion>）——所有 /static 资源
// 共用同一个全局版本号。页面 .vue 与其 <script src> 都带上它，重建网关后
// 浏览器缓存立即失效（index.mjs 自身的 ?v 变了，页面版本跟着变）。
const pageV = typeof import.meta.url === "string" ? new URL(import.meta.url).search : "";

function page(url) {
	return defineAsyncComponent(() => loadSFC(url, {
		async fetchText(u) {
			const res = await fetch(u);
			if (!res.ok) throw new Error(`SFC 获取失败 ${res.status}: ${u}`);
			let text = await res.text();
			if (pageV !== "") {
				// 把 <script src="./X.js"> 同步带上版本号：new URL(src, url) 解析时
				// 相对路径会丢掉 base 的 query，所以在文本层补齐。
				text = text.replace(/<script src="([^"]+\.js)"/, `<script src="$1${pageV}"`);
			}
			return text;
		},
		// 逻辑模块走原生 ESM import（SFC 装载器的 io 契约要求 importModule 存在）
		importModule(u) { return import(/* @vite-ignore */ u); },
	}));
}

const OverviewPage = page("/static/ada/pages/Overview.vue");
const ThreadsPage = page("/static/ada/pages/Threads.vue");
const ThreadPage = page("/static/ada/pages/Thread.vue");
const ExtensionsPage = page("/static/ada/pages/Extensions.vue");
const PluginDetailPage = page("/static/ada/pages/PluginDetail.vue");
const WorkspacesPage = page("/static/ada/pages/Workspaces.vue");
const Sidebar = page("/static/ada/pages/Sidebar.vue");

// 插件页面（/ext/:id/:slug）：注册表查找 → PluginScope 装载 SFC。
const PluginPageRoute = defineComponent({
	name: "PluginPageRoute",
	props: ["params"],
	setup(props) {
		shellState.title = "插件页面";
		shellState.topbar.clear();
		const entry = computed(() => registry.page(props.params?.id, props.params?.slug));
		return { entry, params: props.params };
	},
	render(s) {
		if (s.entry === null) return h("div", { class: "ada-page" }, h(UiEmpty, { text: "插件页面不存在" }));
		return h("div", { class: "ada-page" }, [
			h(PluginScope, {
				pluginId: s.entry.pluginId,
				load: () => loadSFC(s.entry.component),
				extraProps: { pluginId: s.entry.pluginId },
			}),
		]);
	},
});

const NotFoundPage = defineComponent({
	name: "NotFoundPage",
	render: () => h("div", { class: "ada-page" }, h(UiEmpty, { text: "页面不存在" })),
});

// ---------------------------------------------------------------------------
// Shell 布局（DOM 与 HEAD shell_start 同形：侧栏 + 遮罩 + main + topbar）
// ---------------------------------------------------------------------------

const ShellApp = defineComponent({
	name: "ShellApp",
	setup() {
		const router = inject("router");
		onUnmounted(() => { shellState.topbar.actions.splice(0); });
		return { route: router.current, state: shellState };
	},
	render(s) {
		const matched = s.route.matched;
		// 页面 vnode 带 path 作为 key：路由切换时 runtime 会卸载重建页面组件，
		// setup 重新执行拿到新 params（/threads ↔ /threads/:id 复用同一组件）。
		const pageKey = s.route.path;
		let view;
		if (matched === null) view = h(NotFoundPage);
		else if (matched === OverviewRoute) view = h(OverviewPage, { key: pageKey });
		else if (matched === ThreadsRoute) view = h(ThreadPage, { key: pageKey });
		else if (matched === ThreadRoute) view = h(ThreadPage, { key: pageKey, params: s.route.params });
		else if (matched === ExtensionsRoute) view = h(ExtensionsPage, { key: pageKey });
		else if (matched === ExtDetailRoute || matched === ExtensionsDetailRoute) view = h(PluginDetailPage, { key: pageKey, id: s.route.params.id });
		else if (matched === PluginPageRoute_) view = h(PluginPageRoute, { key: pageKey, params: s.route.params });
		else if (matched === WorkspacesRoute) view = h(WorkspacesPage, { key: pageKey });
		else view = h(NotFoundPage);

		// topbar 右侧：topbar.right 插槽在前（HEAD 顺序），其后是页面贡献的动作
		//（会话页的刷新/停止经 shellState.topbar.actions 注入；kind 区分图标钮/危险钮）。
		const actions = s.state.topbar.actions.map((a, i) =>
			a.kind === "danger"
				? h("button", {
					key: a.key ?? String(i),
					class: "ada-btn ada-btn--danger ada-btn--sm",
					type: "button",
					title: a.title ?? a.label ?? "",
					"on:click": () => a.onClick?.(),
				}, [h(UiIcon, { name: a.icon ?? "i-stop", size: 14 }), h("span", { class: "ada-btn__label" }, a.label ?? "")])
				: h("button", {
					key: a.key ?? String(i),
					class: "ada-icon-btn",
					type: "button",
					title: a.title ?? a.label ?? "",
					"aria-label": a.label ?? a.title ?? "",
					"on:click": () => a.onClick?.(),
				}, h(UiIcon, { name: a.icon ?? "i-refresh", size: a.size ?? 14 })),
		);

		return h("div", { class: "ada-shell" }, [
			h(Sidebar, {}),
			h("div", { class: "ada-backdrop", id: "chat-backdrop", "aria-hidden": "true", "on:click": () => { shellState.sidebarOpen = false; } }),
			h("main", { id: "main", class: "ada-main" }, [
				h("div", { class: "ada-topbar" }, [
					h("button", {
						class: "ada-icon-btn ada-menu-toggle",
						type: "button",
						"on:click": () => { shellState.toggleSidebar(); },
						"aria-label": "打开侧栏",
						title: "打开侧栏",
					}, h(UiIcon, { name: "i-menu", size: 16 })),
					h("div", { class: "ada-topbar__title" }, [
						h("h1", { title: s.state.title }, s.state.title),
						bootstrap.productBadge
							? h("span", { class: "ada-chip" }, bootstrap.productBadge)
							: h("span", { class: "ada-chip" }, bootstrap.productName ?? ""),
					]),
					h("div", { class: "ada-topbar__actions" }, [
						h(SlotOutlet, { name: "topbar.right" }),
						h("div", { id: "topbar-actions" }, actions),
					]),
				]),
				view,
			]),
			h(ConfirmDialog),
			h(UiImageViewer),
			h(UiToast),
			h(ThreadMenu),
		]);
	},
});

// 路由记录（匹配只比较身份；页面组件由上面的 defineAsyncComponent 装载）
const OverviewRoute = { path: "/" };
const ThreadsRoute = { path: "/threads" };
const ThreadRoute = { path: "/threads/:id" };
const ExtensionsRoute = { path: "/extensions" };
const ExtensionsDetailRoute = { path: "/extensions/:id" };
const ExtDetailRoute = { path: "/ext/:id" };
const PluginPageRoute_ = { path: "/ext/:id/:slug" };
const WorkspacesRoute = { path: "/workspaces" };
const Routes = [
	{ path: "/", component: OverviewRoute },
	{ path: "/threads", component: ThreadsRoute },
	{ path: "/threads/:id", component: ThreadRoute },
	{ path: "/extensions", component: ExtensionsRoute },
	{ path: "/extensions/:id", component: ExtensionsDetailRoute },
	{ path: "/workspaces", component: WorkspacesRoute },
	{ path: "/ext/:id", component: ExtDetailRoute },
	{ path: "/ext/:id/:slug", component: PluginPageRoute_ },
];

// ---------------------------------------------------------------------------
// boot
// ---------------------------------------------------------------------------

export function boot() {
	// 路由：createRouter + 订阅（router 是自管理订阅，非响应式 —— 用 ref 镜像）
	// 路由：createRouter + 订阅（router 是自管理订阅，非响应式 —— 用 ref 镜像）。
	// 初值取 router 当前快照：router 创建时已按 location 解析过一次，若从
	// 空 matched 起步，订阅者要等下一次导航才会拿到命中路由（首屏直接 404）。
	const router = createRouter({
		routes: Routes,
	});
	const routeRef = ref(router.current.value ?? { path: window.location.pathname, params: {}, matched: null });
	router.subscribe((snap) => {
		routeRef.value = snap;
		shellState.routePath = snap.path;
	});
	registry.load();
	registry.subscribeWorkspaceEvents();

	const app = createApp(ShellApp);
	// 全局服务注入
	app.provide("router", {
		// current 直接给 reactivity ref：组件 render 作用域读取时自动解包，
		// `s.route.matched` 才是快照本体（此前的裸 getter 包装不会被解包，
		// ShellApp 会永远落到 404 视图——2026-11-11 走查踩到）。
		current: routeRef,
		navigate: (p) => router.push(p),
		replace: (p) => router.replace(p),
	});
	app.provide("registry", registry);
	app.provide("fetch", adaFetch);
	app.provide("confirmService", confirmService);
	app.provide("toast", toastService);
	app.provide("imageViewer", imageViewerService);
	app.provide("threadMenu", threadMenuService);
	app.provide("bootstrap", bootstrap);
	threadMenuNavigate = (p) => router.push(p);
	// ui-* 组件注册（插件模板可引用）；ui-slot 是 SlotOutlet 的模板名
	//（编译器只放行 ui- 前缀组件，slot-outlet 这个名字进不了模板）。
	app.component("slot-outlet", SlotOutlet);
	app.component("ui-slot", SlotOutlet);
	app.component("ui-icon", UiIcon);
	app.component("ui-markdown", UiMarkdown);
	app.component("ui-badge", UiBadge);
	app.component("ui-button", UiButton);
	app.component("ui-panel", UiPanel);
	app.component("ui-empty", UiEmpty);
	app.component("ui-code", UiCode);
	app.config.errorHandler = (err) => {
		console.error("[ada-shell]", err);
	};

	const root = document.getElementById("app");
	if (root === null) {
		console.error("[ada] 缺少 #app 挂载点");
		return null;
	}
	app.mount(root);
	// 调试钩子（只挂 window，不参与渲染）：方便在浏览器控制台观察路由快照。
	if (typeof window !== "undefined") {
		window.__adaDebug = { router, registry, shellState, routeRef };
	}
	return { app, router, registry, routeRef };
}

// 模块被 shell 直接 import 时自动启动。
if (typeof window !== "undefined" && document.getElementById("app") !== null) {
	boot();
}
