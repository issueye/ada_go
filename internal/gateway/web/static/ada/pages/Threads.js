// Threads.js —— 会话列表页逻辑：分组（工作区）、运行中标识、按需加载其它工作区。
import { ref, reactive, computed, onMounted, onUnmounted, inject } from "/static/ada/ada-runtime.mjs";
import { shellState } from "./state.js";
import { relativeTime, parseTimeMS } from "./format.js";

export const setup = () => {
	const fetch = inject("fetch");
	const router = inject("router");
	const bootstrap = inject("bootstrap");
	const threadMenu = inject("threadMenu");

	shellState.title = "会话";
	shellState.topbar.clear();

	const groups = reactive([]);
	const failed = ref(null);
	const loading = ref(true);

	async function load() {
		loading.value = true;
		try {
			const current = await fetch("/partials/threads.json");
			failed.value = null;
			// HEAD 侧栏语义：当前工作区固定在最前且展开；其余工作区是折叠组，
			// 展开时才拉会话（跨工作区查询会各自拉起 Core）。
			groups.splice(0, groups.length);
			groups.push({
				path: current.workspace,
				name: current.workspaceName,
				loaded: true,
				threads: sortThreads(current.threads ?? []),
			});
			const others = String(bootstrap?.workspace ?? "") !== ""
				? (bootstrap.workspaces ?? []).filter((w) => w.path && w.path !== current.workspace)
				: [];
			for (const w of others) {
				if (groups.some((g) => g.path === w.path)) continue;
				groups.push({ path: w.path, name: w.name, loaded: false, threads: [] });
			}
		} catch (err) {
			failed.value = err?.message ?? String(err);
		} finally {
			loading.value = false;
		}
	}

	async function loadGroup(g) {
		try {
			const data = await fetch("/partials/threads.json?path=" + encodeURIComponent(g.path));
			g.threads = sortThreads(data.threads ?? []);
			g.loaded = true;
		} catch (err) {
			failed.value = err?.message ?? String(err);
		}
	}

	// 排序照 HEAD 侧栏：最后活动时间倒序（thread.list 已按 updatedAt 倒序，
	// 这里再兜一层底）。
	function sortThreads(list) {
		return [...list].sort((a, b) => parseTimeMS(b.updatedAt) - parseTimeMS(a.updatedAt));
	}

	function timeLabel(t) {
		return relativeTime(parseTimeMS(t.updatedAt));
	}

	const totalCount = computed(() => groups.reduce((n, g) => n + (g.loaded ? g.threads.length : 0), 0));
	const threadURL = (id) => "/threads/" + encodeURIComponent(id);
	const openThread = (id) => { router.navigate("/threads/" + encodeURIComponent(id)); };
	// 会话行「更多」菜单（复制 ID / 归档）；归档成功后整页分组重拉。
	const openMenu = (t, event) => { threadMenu.open(t, event, load); };

	onMounted(load);
	onUnmounted(() => shellState.topbar.clear());

	return {
		groups,
		failed,
		loading,
		totalCount,
		load,
		loadGroup,
		timeLabel,
		threadURL,
		openThread,
		openMenu,
		goNewTask: () => { router.navigate("/threads"); },
	};
};
