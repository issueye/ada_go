// Sidebar.js —— 侧栏：会话树（当前工作区展开 + 其它工作区懒加载）、搜索过滤、
// 主题面板（themes.js 事件委托接管 data-theme-* 的行为）、导航与网关状态。
import { ref, reactive, computed, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";
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

	const otherCount = computed(() => Math.max(0, groups.length - 1));
	const sidebarClass = computed(() => (shellState.sidebarOpen ? "is-open" : ""));

	async function load() {
		try {
			const data = await fetch("/partials/threads.json?limit=50");
			failed.value = null;
			const current = {
				path: data.workspace,
				name: data.workspaceName,
				index: (bootstrap.workspaces ?? []).findIndex((w) => w.path === data.workspace),
				loaded: true,
				threads: sortThreads(data.threads ?? []),
			};
			const rest = (bootstrap.workspaces ?? [])
				.filter((w) => w.path && w.path !== data.workspace)
				.map((w, i) => ({ path: w.path, name: w.name, index: (bootstrap.workspaces ?? []).indexOf(w), loaded: false, threads: [] }));
			groups.splice(0, groups.length, current, ...rest);
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}

	function sortThreads(list) {
		return [...list].sort((a, b) => parseTimeMS(b.updatedAt) - parseTimeMS(a.updatedAt));
	}

	async function onToggle(event, g) {
		const details = event.target;
		if (!details?.open || g.loaded) return;
		// 不可变替换分组：数组元素整体换新（索引写入必然触发 v-for 重渲染），
		// 不在原地改对象属性——原地改依赖 effect 的收集时机，异步完成后界面
		// 可能不刷新（2026-11-11 走查：展开后仍停在「展开以加载会话…」）。
		const replaceGroup = (patch) => {
			const idx = groups.findIndex((x) => x.path === g.path);
			if (idx < 0) return;
			groups.splice(idx, 1, { path: g.path, name: g.name, index: g.index, loaded: true, threads: g.threads, ...patch });
		};
		replaceGroup({ loaded: true, threads: g.threads });
		try {
			// 注入的 fetch 是 makeFetcher 封装：JSON 响应已经解析成对象，
			// 不能再调 .json()（2026-11-11 走查：其它工作区展开不加载的根因）。
			const data = await fetch("/partials/threads.json?path=" + encodeURIComponent(g.path) + "&limit=50");
			replaceGroup({ loaded: true, threads: sortThreads((data && data.threads) || []) });
		} catch (err) {
			replaceGroup({ loaded: false });
			failed.value = err?.message ?? String(err);
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
	function visibleThreads(g) {
		return g.threads;
	}
	const searchLabel = computed(() => {
		const q = needle.value.trim();
		if (!q) return "";
		const rows = groups.flatMap((g) => g.threads).filter(threadHit);
		return rows.length > 0 ? `${rows.length} / ${groups.reduce((n, g) => n + g.threads.length, 0)}` : "未找到";
	});

	function timeLabel(t) {
		return relativeTime(parseTimeMS(t.updatedAt));
	}
	const threadURL = (id) => threadPath(id);
	const openThread = (id) => {
		shellState.closeSidebar();
		router.navigate(threadPath(id));
	};
	async function openThreadIn(t, g) {
		// 其它工作区的会话：先切工作区 cookie（服务端 fetch 都按 cookie 解析），
		// 再进会话页——HEAD 的 ?ws= 链接语义在 SPA 下等价实现。
		if (g.index >= 0 && g.path !== workspace) await switchWorkspace(g.path);
		openThread(t.id);
	}
	async function switchWorkspace(path) {
		await fetch("/api/workspace/select", {
			method: "POST",
			headers: { "Content-Type": "application/x-www-form-urlencoded" },
			body: new URLSearchParams({ path }).toString(),
		});
	}

	async function removeThread(t, g) {
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
	const pluginNav = computed(() => registry.nav().map((n) => ({ path: n.path, label: n.label ?? n.path })));
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
	const railThreads = computed(() => (groups.length > 0 ? groups[0].threads.slice(0, 8) : []));
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
		shellState.sidebarOpen = true;
		const input = document.getElementById("chat-thread-search");
		if (input) {
			input.focus();
			input.select();
		}
	}
	// Ctrl/Cmd+N 新建任务（HEAD bindNewTaskShortcut）。
	function onNewTaskKey(event) {
		if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "n") return;
		const el = document.activeElement;
		if (el && (el.tagName === "TEXTAREA" || el.isContentEditable)) return;
		event.preventDefault();
		go("/threads");
	}
	// Esc 关抽屉。
	function onEscKey(event) {
		if (event.key !== "Escape") return;
		if (shellState.sidebarOpen) shellState.closeSidebar();
	}

	onMounted(() => {
		void load();
		document.addEventListener("keydown", onKeydown);
		document.addEventListener("keydown", onNewTaskKey);
		document.addEventListener("keydown", onEscKey);
		document.addEventListener("click", mirrorThemeClick);
	});
	onUnmounted(() => {
		document.removeEventListener("keydown", onKeydown);
		document.removeEventListener("keydown", onNewTaskKey);
		document.removeEventListener("keydown", onEscKey);
		document.removeEventListener("click", mirrorThemeClick);
	});

	return {
		groups, needle, failed, activeThreadID, otherCount, sidebarClass,
		mode, palette, themeModes, themePalettes,
		brandName, productBadge, workspace, gatewayAddr, gatewayTitle, logoStyle, wsDOMID,
		pluginNav, railThreads, railInitial,
		isActive, go, goRoot, load, onToggle, groupRunning, threadHit, visibleThreads, searchLabel,
		timeLabel, threadURL, openThread, openThreadIn, switchWorkspace, removeThread, openMenu,
	};
};
