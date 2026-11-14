// Sidebar.js —— 侧栏：工作区列表（全部工作区同一滚动区，逐组折叠 + 懒加载会话）、
// 搜索过滤、主题面板（themes.js 事件委托接管 data-theme-* 的行为）、导航与网关状态。
//
// 结构语义（2026-11-11 调整）：侧栏不再分「当前工作区 / 其它工作区」两块各自滚动，
// 也没有「其它工作区」分区标题。所有工作区是同一条列表里的可折叠组：
//   - 当前工作区默认展开（会话在 load() 里就拉回来）；
//   - 其余工作区默认折叠，首次展开时才按 ?path= 拉会话（懒加载）；
//   - 点组头即切换当前工作区（同时按新工作区重新加载当前组）。
import { ref, reactive, computed, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";
import { bindSidebarWidth } from "./sidebar-width.js";
import { relativeTime, parseTimeMS, threadPath } from "./format.js";

export const setup = () => {
	const fetch = inject("fetch");
	const router = inject("router");
	const bootstrap = inject("bootstrap");
	const registry = inject("registry");
	const confirm = inject("confirmService");
	const threadMenu = inject("threadMenu");

	const groups = reactive([]);
	const needle = ref("");
	const failed = ref(null);
	const activeThreadID = computed(() => {
		const m = /^\/threads\/([^/]+)/.exec(shellState.routePath);
		return m ? decodeURIComponent(m[1]) : "";
	});

	// 主题面板初始态：themes.js 只在点击时同步 UI，这里按它的键名读初始值。
	const MODES = ["light", "dark", "system"];
	const PALETTES = ["black-swan", "moss", "ocean", "dusk"];
	const themeModes = [
		{ id: "light", label: "亮色", icon: "i-sun" },
		{ id: "dark", label: "暗色", icon: "i-moon" },
		{ id: "system", label: "跟随系统", icon: "i-monitor" },
	];
	const themePalettes = [
		{ id: "black-swan", label: "黑天鹅" },
		{ id: "moss", label: "苔绿" },
		{ id: "ocean", label: "深海" },
		{ id: "dusk", label: "暮色" },
	];
	function readLS(key) {
		try { return window.localStorage.getItem(key); } catch { return null; }
	}
	// wsDOMID 把工作区路径映射成稳定的 DOM id（FNV-1a，HEAD wsDOMID 同形）。
	function wsDOMID(key) {
		let hash = 0x811c9dc5;
		for (const b of Bufferish(String(key))) {
			hash ^= b;
			hash = Math.imul(hash, 0x01000193) >>> 0;
		}
		return "ws-" + hash.toString(16).padStart(8, "0");
	}
	function Bufferish(s) {
		const out = [];
		for (let i = 0; i < s.length; i++) out.push(s.charCodeAt(i) & 0xff);
		return out;
	}
	// 主题面板的选中态镜像：themes.js 负责应用主题（事件委托），这里只把
	// data-theme-mode / data-theme-pick 的点击同步回面板高亮。
	function mirrorThemeClick(event) {
		const target = event.target?.closest?.("[data-theme-mode], [data-theme-pick]");
		if (!target) return;
		if (target.getAttribute("data-theme-mode")) mode.value = target.getAttribute("data-theme-mode");
		if (target.getAttribute("data-theme-pick")) palette.value = target.getAttribute("data-theme-pick");
	}
	const modeRaw = readLS("a_da_theme_mode");
	const mode = ref(MODES.includes(modeRaw) ? modeRaw : "system");
	const paletteRaw = readLS("a_da_theme_palette");
	const palette = ref(PALETTES.includes(paletteRaw) ? paletteRaw : "black-swan");

	const brandName = String(bootstrap.brandName ?? "ada-go");
	const productBadge = String(bootstrap.productBadge ?? "");
	const workspace = String(bootstrap.workspace ?? "");
	const gatewayAddr = String(bootstrap.gateway ?? "").replace(/^[a-z]+:\/\//i, "").replace(/\/$/, "");
	const gatewayTitle = "已连接 · " + String(bootstrap.gateway ?? "");
	const logoStyle = { backgroundImage: "url(/static/logo.png)" };

	const sidebarClass = computed(() => (shellState.sidebarOpen ? "is-open" : ""));

	// ── 侧栏宽度拖拽 ─────────────────────────────────────────────────────
	// 实现放在 sidebar-width.js（纯 DOM/存储，独立可测）：宽度写进
	// --ada-sidebar-width（sidebar-boot.js 在首帧前预置同一个变量），拖动只改
	// 变量、松手才落 localStorage；窄屏抽屉与折叠窄轨不参与。
	let resizerCleanup = null;

	// 拖拽条的指针/键盘/双击绑定：元素由模板渲染，只能在挂载后按属性查询。
	// 返回一个清理函数（卸载时解绑 window 上的拖动监听，避免重挂载后重复处理指针）。
	function bindSidebarResizer() {
		const handle = document.querySelector("[data-sidebar-resizer]");
		if (handle === null) return null;
		return bindSidebarWidth(handle, {
			isDisabled: () => shellState.isNarrow || shellState.collapsed,
		});
	}

	// ── 主题面板开合 ─────────────────────────────────────────────────────
	// 开合是组件状态（模板 :hidden 绑定 themeOpen）；themes.js 只负责应用主题
	// 与持久化（它不再直接改 DOM，否则重渲染会把展开态盖回去）。
	const themeOpen = ref(false);
	function toggleThemePanel() {
		themeOpen.value = !themeOpen.value;
		if (!themeOpen.value) return;
		// 展开态先落 DOM 再聚焦：选中项在 panel 里，下一轮 flush 后才存在。
		window.setTimeout(() => {
			const panel = document.getElementById("ada-theme-panel");
			if (panel === null) return;
			const target = panel.querySelector(".is-selected") || panel.querySelector("button");
			if (target !== null && typeof target.focus === "function") {
				try { target.focus(); } catch { /* 焦点失败不影响展开 */ }
			}
		}, 0);
	}
	function closeThemePanel(returnFocus) {
		if (!themeOpen.value) return;
		themeOpen.value = false;
		if (returnFocus !== true) return;
		const trigger = document.querySelector("[data-theme-panel-toggle]");
		if (trigger !== null && typeof trigger.focus === "function") {
			try { trigger.focus(); } catch { /* 触发器可能已随重渲染消失 */ }
		}
	}
	// 点面板外关闭（触发器本身由 toggleThemePanel 处理，这里跳过它）。
	function onDocClickTheme(event) {
		if (!themeOpen.value) return;
		const target = event.target;
		if (target && typeof target.closest === "function") {
			if (target.closest(".ada-theme-wrap") !== null) return;
		}
		closeThemePanel(false);
	}

	// 全部工作区都来自 bootstrap（服务端已按工作区列表投影）；当前工作区的会话
	// 在 load() 里随当前组一起拉回，其余工作区按需。
	const workspaces = Array.isArray(bootstrap.workspaces) ? bootstrap.workspaces : [];

	// group: { path, name, index, current, open, loaded, threads }
	function makeGroup(ws, index, current) {
		return {
			path: String(ws.path ?? ""),
			name: String(ws.name ?? ws.path ?? ""),
			index,
			current,
			open: current,   // 当前工作区默认展开
			loaded: false,
			threads: [],
		};
	}

	async function load() {
		// 先铺骨架（全部工作区都可见，当前区标记出来），再补当前区的会话。
		const currentPath = String(bootstrap.workspace ?? "");
		const skeleton = workspaces
			.filter((w) => w.path)
			.map((w, i) => makeGroup(w, i, w.path === currentPath));
		if (skeleton.length === 0) {
			skeleton.push(makeGroup({ path: currentPath, name: bootstrap.workspaceName || currentPath }, 0, true));
		}
		groups.splice(0, groups.length, ...skeleton);
		const idx = groups.findIndex((g) => g.current);
		if (idx < 0) return;
		await loadGroup(groups[idx]);
	}

	// loadGroup 拉某个工作区的会话并把分组就地替换（数组元素整体换新：
	// 原地改对象属性依赖 effect 的收集时机，异步回来后界面可能不刷新）。
	async function loadGroup(g) {
		const idx = groups.findIndex((x) => x.path === g.path);
		if (idx < 0) return;
		try {
			const q = g.current ? "" : "?path=" + encodeURIComponent(g.path);
			// 注入的 fetch 是 makeFetcher 封装：JSON 响应已经解析成对象，不能再 .json()。
			const data = await fetch("/partials/threads.json" + (q ? q + "&limit=50" : "?limit=50"));
			groups.splice(idx, 1, { ...g, loaded: true, threads: sortThreads(data?.threads ?? []) });
			failed.value = null;
		} catch (err) {
			groups.splice(idx, 1, { ...g, loaded: false });
			failed.value = err?.message ?? String(err);
		}
	}

	function sortThreads(list) {
		return [...list].sort((a, b) => parseTimeMS(b.updatedAt) - parseTimeMS(a.updatedAt));
	}

	// toggleGroup：折叠/展开该组；展开即懒加载（并把它切成当前工作区）。
	async function toggleGroup(g) {
		const idx = groups.findIndex((x) => x.path === g.path);
		if (idx < 0) return;
		groups.splice(idx, 1, { ...g, open: !g.open });
		if (!g.open) return;
		// 展开一个非当前工作区 = 切换过去（服务端 cookie 决定后续请求的解析工作区）。
		if (!g.current) await switchWorkspace(g.path);
		if (!g.loaded) await loadGroup({ ...g, open: true });
	}

	const allOpen = computed(() => groups.every((g) => g.open));
	async function toggleAll() {
		const open = !allOpen.value;
		const snapshot = groups.map((g) => ({ ...g }));
		groups.splice(0, groups.length, ...snapshot.map((g) => ({ ...g, open })));
		if (!open) return;
		// 展开全部：顺手把还没加载的非当前工作区拉回来。
		for (const g of snapshot) {
			if (g.open) continue;
			const next = groups.find((x) => x.path === g.path);
			if (next && !next.loaded) await loadGroup(next);
		}
	}

	function groupRunning(g) {
		return g.threads.some((t) => t.running);
	}

	// 搜索过滤（HEAD bindThreadSearch 的本地过滤语义）：标题包含关键词。
	function threadHit(t) {
		const q = needle.value.trim().toLowerCase();
		if (!q) return true;
		return String(t.title ?? "").toLowerCase().includes(q);
	}
	// hitThreads 是模板里 v-for 的源：搜索时直接过滤，不做「渲染后 hidden」。
	function hitThreads(g) {
		const list = g.threads ?? [];
		if (needle.value.trim() === "") return list;
		return list.filter(threadHit);
	}
	const searchLabel = computed(() => {
		const q = needle.value.trim();
		if (!q) return "";
		const rows = groups.flatMap((g) => g.threads).filter(threadHit);
		return rows.length > 0 ? `${rows.length} / ${groups.reduce((n, g) => n + g.threads.length, 0)}` : "未找到";
	});

	function focusSearch() {
		shellState.sidebarOpen = true;
		const input = document.getElementById("chat-thread-search");
		if (input) {
			input.focus();
			try { input.select(); } catch { /* 非文本输入不支持 select */ }
		}
	}

	function timeLabel(t) {
		return relativeTime(parseTimeMS(t.updatedAt));
	}
	const threadURL = (id) => threadPath(id);
	const openThread = (id) => {
		shellState.closeSidebar();
		router.navigate(threadPath(id));
	};
	async function openThreadIn(t, g) {
		// 非当前工作区：先切工作区 cookie（服务端 fetch 都按 cookie 解析），再进会话页。
		if (!g.current && g.path !== workspace) await switchWorkspace(g.path);
		openThread(t.id);
	}
	async function switchWorkspace(path) {
		await fetch("/api/workspace/select", {
			method: "POST",
			headers: { "Content-Type": "application/x-www-form-urlencoded" },
			body: new URLSearchParams({ path }).toString(),
		});
	}

	async function removeThread(t) {
		const ok = await confirm(`删除会话「${t.title || t.id}」？删除后无法恢复。`);
		if (!ok) return;
		try {
			await fetch(`/threads/${encodeURIComponent(t.id)}/delete`, {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded" },
				body: new URLSearchParams({ next: shellState.routePath }).toString(),
			});
			await load();
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}

	// 会话行「更多」菜单（复制 ID / 归档）：菜单本体与动作在 ShellApp 根部
	//（threadMenu 服务），这里只负责带上归档成功后的列表重拉回调。
	function openMenu(t, event) {
		threadMenu.open(t, event, load);
	}

	// 插件导航（registry.nav 排序后）；SSE 的 evt.plugin.changed 会自动重拉注册表。
	// icon 来自清单 frontend.nav[].icon（省略时用通用插件图标）。
	const pluginNav = computed(() => registry.nav().map((n) => ({
		path: n.path,
		label: n.label ?? n.path,
		icon: n.icon ?? "",
	})));
	function isActive(path) {
		if (path === "/") return shellState.routePath === "/" || shellState.routePath === "";
		return shellState.routePath === path || shellState.routePath.startsWith(path + "/");
	}
	const go = (path) => {
		shellState.closeSidebar();
		router.navigate(path);
	};
	// 品牌位回会话列表（模板里避免字符串字面量实参的引号嵌套）。
	const goRoot = () => go("/");

	// 窄轨缩略列表：当前工作区最近 8 条（HEAD rail 语义）。
	const railThreads = computed(() => {
		const current = groups.find((g) => g.current);
		return current ? current.threads.slice(0, 8) : [];
	});
	const railInitial = (t) => {
		const trimmed = String(t.title ?? "").trim();
		return trimmed !== "" ? [...trimmed][0] : "·";
	};

	// Ctrl/Cmd+K 聚焦搜索（HEAD bindThreadSearch 快捷键）；输入中不抢焦点。
	function onKeydown(event) {
		if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "k") return;
		const el = document.activeElement;
		if (el && (el.tagName === "TEXTAREA" || el.isContentEditable)) return;
		event.preventDefault();
		focusSearch();
	}
	// Ctrl/Cmd+N 新建任务（HEAD bindNewTaskShortcut）。
	function onNewTaskKey(event) {
		if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "n") return;
		const el = document.activeElement;
		if (el && (el.tagName === "TEXTAREA" || el.isContentEditable)) return;
		event.preventDefault();
		go("/threads");
	}
	// Esc 关抽屉 / 关主题面板（面板优先：它是浮层，Esc 应先收浮层）。
	function onEscKey(event) {
		if (event.key !== "Escape") return;
		if (themeOpen.value) {
			event.preventDefault();
			closeThemePanel(true);
			return;
		}
		if (shellState.sidebarOpen) shellState.closeSidebar();
	}

	onMounted(() => {
		void load();
		document.addEventListener("keydown", onKeydown);
		document.addEventListener("keydown", onNewTaskKey);
		document.addEventListener("keydown", onEscKey);
		document.addEventListener("click", mirrorThemeClick);
		document.addEventListener("click", onDocClickTheme);
		resizerCleanup = bindSidebarResizer();
	});
	onUnmounted(() => {
		document.removeEventListener("keydown", onKeydown);
		document.removeEventListener("keydown", onNewTaskKey);
		document.removeEventListener("keydown", onEscKey);
		document.removeEventListener("click", mirrorThemeClick);
		document.removeEventListener("click", onDocClickTheme);
		if (typeof resizerCleanup === "function") resizerCleanup();
		resizerCleanup = null;
	});

	return {
		groups, needle, failed, activeThreadID, sidebarClass,
		mode, palette, themeModes, themePalettes, themeOpen, toggleThemePanel,
		brandName, productBadge, workspace, gatewayAddr, gatewayTitle, logoStyle, wsDOMID,
		pluginNav, railThreads, railInitial,
		isActive, go, goRoot, load, toggleGroup, toggleAll, allOpen,
		groupRunning, threadHit, hitThreads, searchLabel, focusSearch,
		timeLabel, threadURL, openThread, openThreadIn, switchWorkspace, removeThread, openMenu,
	};
};
